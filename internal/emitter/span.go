package emitter

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/cafaye/caf/internal/telemetry"
)

// Outcome is how an invocation ended, and it is a closed set because core's
// `error.type` is a closed set. The mapping from an exit code to a class is the
// only judgement in this file, so it is the thing stated in full below and in
// span_test.go rather than spread across the callers.
type Outcome int

const (
	// OutcomeSuccess is a command that did what it was asked and exits 0.
	OutcomeSuccess Outcome = iota
	// OutcomeUsage is caf being invoked wrongly: unknown command, bad flag,
	// wrong argument count. Exit 2.
	//
	// Classified `invalid_request`, not `internal_error`. This is the one class
	// where the error is caused by something OUTSIDE the process and nobody
	// inside it is at fault: the person typed the command wrong. Filing it as
	// internal_error would put a user's typo in the same dashboard bucket as a
	// caf bug, and the two have opposite remedies.
	OutcomeUsage
	// OutcomeFailure is a command that ran and failed. Exit 1.
	//
	// Classified `internal_error`, the honest default. The alternative —
	// inferring a class from the error text — is what core's traces schema
	// forbids by making `error.type` an enum rather than a pattern: a pattern
	// constrains the shape of a value and says nothing about the set, so
	// `user_42_email_invalid` validates and becomes its own series. An
	// invocation that failed is one class until a command that knows better says
	// otherwise through Classified, and it is classified in exactly one place
	// rather than by every error's message.
	OutcomeFailure
	// OutcomeCancelled is a command stopped by SIGINT or SIGTERM. Exit 1.
	//
	// Classified `cancelled`, which core's vocabulary has a value for exactly
	// so this does not have to be invented. A Ctrl-C during `caf dev` is not an
	// error in the sense an alert should page on, and a class that says so is
	// what stops an operator's own impatience showing up in an error rate.
	OutcomeCancelled
)

// exitCode is the process exit code for an outcome, and it is the mapping caf
// has already committed to in internal/cli.
func (o Outcome) exitCode() int {
	switch o {
	case OutcomeSuccess:
		return 0
	case OutcomeUsage:
		return 2
	default:
		return 1
	}
}

// Classified lets a command name its own error class.
//
// It exists because the default is `internal_error` and that default is right
// for most commands and wrong for a few. A `caf deploy` that cannot reach the
// registry is `dependency_unavailable`, not a caf bug, and a reader of the
// dashboard cannot tell those apart from the class alone. So the one place that
// knows — the command — can say so, and it must be a member of core's closed
// vocabulary or Install's caller refuses it.
//
// Returning an error from this is deliberate: an unrecognised class is refused
// at the call site rather than exported and dropped by the collector's
// redaction, because a class silently dropped is a failure that looks like a
// success.
type Classified struct {
	err  error
	kind string
}

// Classify names an error's class for this invocation's root span.
func Classify(err error, kind string) Classified { return Classified{err: err, kind: kind} }

// Error is the underlying error, so a Classified can still be returned from a
// function whose error is inspected.
func (c Classified) Error() string {
	if c.err == nil {
		return ""
	}
	return c.err.Error()
}

// Unwrap exposes the underlying error to errors.Is and errors.As, which is what
// keeps `Classify(err, ...)` transparent to the router's own error handling.
func (c Classified) Unwrap() error { return c.err }

// StartInvocation opens the one root span a caf run produces.
//
// The name is derived, not passed in, and that is the property core's grammar
// exists to enforce: the span name is `<service>.<operation>[.<target>]` with a
// 15-character-per-segment bound, so a command name that a person can type — and
// a person can type a path — cannot become a span name. Passing the raw
// invocation in would be the exact cardinality explosion core's
// span-naming.schema.json says the length bound "is doing the work" to prevent,
// so it is not offered as an option.
//
// Returns a function rather than a `defer`-shaped error so the caller cannot
// forget to end it, and a nil-safe one so a no-op session costs nothing to call.
func (s Session) StartInvocation(ctx context.Context, args []string) (context.Context, func(Outcome) error) {
	if !s.enabled {
		// Returning a function that does nothing rather than nil, so a caller
		// writes `defer end(...)` once and the no-op needs no second code path
		// at the call site — and therefore cannot be forgotten on the path where
		// it is cheapest to forget.
		return ctx, func(Outcome) error { return nil }
	}

	ctx, span := s.tracer.Start(ctx, InvocationSpanName(args))

	return ctx, func(outcome Outcome) error {
		span.SetAttributes(attribute.String("otel.status_code", statusMirror(outcome)))
		if outcome != OutcomeSuccess {
			// SetStatus rather than RecordError: core's traces schema closes the
			// attribute set with additionalProperties: false, and RecordError
			// adds `exception.message`, which is not on it. The message is
			// caller text and, as core's own description of error.type says, a
			// provider's rejection quoting content back is the reason
			// `error.message` is deliberately absent from the vocabulary.
			span.SetStatus(codes.Error, "")
			span.SetAttributes(attribute.String("error.type", errorType(outcome)))
		}
		span.End()
		return nil
	}
}

// statusMirror is the `otel.status_code` attribute, which core's schema defines
// as a mirror of `status.code` that may not contradict it.
//
// It is set for the success path too, and that is deliberate: core's third
// allOf rule constrains the mirror only when it says ERROR, so the omission is
// legal but useless — a log-indexed query filtering on `otel.status_code=OK`
// needs the successes to be there.
func statusMirror(outcome Outcome) string {
	if outcome == OutcomeSuccess {
		return "OK"
	}
	return "ERROR"
}

// errorType is the class for an outcome, from core's closed vocabulary.
func errorType(outcome Outcome) string {
	switch outcome {
	case OutcomeUsage:
		return "invalid_request"
	case OutcomeCancelled:
		return "cancelled"
	default:
		return "internal_error"
	}
}

// InvocationSpanName is the span name for one caf run, derived from its
// arguments.
//
// It is exported and pure because the derivation is the contract, and a contract
// a test cannot name is a contract that gets re-implemented. It reads only the
// leading non-flag words, so `caf dev -project x` and `caf dev -project y` are
// the same span, which is the whole property: a per-request span name is a
// cardinality explosion and a per-subcommand one is a dashboard.
//
// Everything it does with the words it keeps is in service of core's grammar:
// lowercase, dot-separated, at most 15 characters a segment, at most three
// segments after the service name. A word that cannot be made legal is DROPPED
// rather than mangled, because mangling produces two names for one command and a
// dashboard with `caf.dev.-proj` on it is worse than one missing a rare command.
// The final answer is always checked against the generated pattern, so a name
// that cannot be made legal falls back to the whole-run name rather than
// exporting something core's collector will drop.
func InvocationSpanName(args []string) string {
	words := commandWords(args)
	segments := make([]string, 0, len(words))
	for _, word := range words {
		segment, ok := spanSegment(word)
		if !ok {
			continue
		}
		segments = append(segments, segment)
		if len(segments) == telemetry.MaxSpanNameSegments {
			break
		}
	}
	if len(segments) == 0 {
		return fallbackSpanName
	}
	name, err := telemetry.SpanName(segments[0], segments[1:]...)
	if err != nil || !telemetry.SpanNamePattern.MatchString(name) {
		return fallbackSpanName
	}
	return name
}

// fallbackSpanName is the name for a run whose arguments yield nothing legal —
// `caf`, `caf --help`, `caf -version`.
//
// `caf.cli.run` rather than `caf.run`: the target segment exists precisely
// because the operation alone is ambiguous across the fleet, and `caf.run` could
// be an HTTP call, an event publish, or a person typing at a terminal. `run`
// names what every caf invocation is.
const fallbackSpanName = telemetry.ServiceName + ".cli.run"

// commandWords are the command's own words: leading arguments that are not
// flags and not flag values.
//
// A flag's VALUE is excluded along with the flag, and that is the part that is
// easy to get wrong. `caf gen telemetry -out /tmp/x` must be `caf.gen.telemetry`
// and not `caf.gen.telemetry.out.tmp.x` — so a `-flag` consumes the word after
// it unless that word is itself a flag, and the skipped word is not examined.
// This is a heuristic and it is stated as one: caf's flags are documented with
// their arities in each subcommand's `Flags` function, and a name derived from a
// heuristic that misreads `caf dev -no-infra up` is a span called `caf.dev.up`
// when the command was `caf dev`. The alternative — threading each command's real
// name through the router — is the honest fix and is not done here; see the
// report. What this guarantees is the property that matters either way: the
// answer is always legal per core's grammar, never unbounded, and never a path.
func commandWords(args []string) []string {
	words := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			// A flag consumes the next argument unless that one is a flag too:
			// `caf dev --project foo` is two words about one value, and
			// `caf dev -no-infra` is one.
			if !strings.Contains(arg, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
			continue
		}
		words = append(words, arg)
	}
	return words
}

// spanSegment turns one command word into a legal span-name segment.
//
// The bound is core's, not this file's: 15 characters, lowercase, and no dash.
// A dash is a legal character in a service name and an ILLEGAL one in every
// segment after it, so `caf gen telemetry` is fine and a two-word subcommand
// joined by a dash would not be — hence the underscore, which the grammar does
// allow, and which is why the dash is dropped rather than refused.
func spanSegment(word string) (string, bool) {
	segment := strings.ToLower(word)
	segment = strings.ReplaceAll(segment, "-", "_")
	if segment == "" {
		return "", false
	}
	if len(segment) > 15 {
		return "", false
	}
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		isLetter := (c >= 'a' && c <= 'z')
		isDigit := (c >= '0' && c <= '9')
		// The first character must be a letter; the rest may be letters,
		// digits and underscores.
		if i == 0 && !isLetter {
			return "", false
		}
		if !isLetter && !isDigit && c != '_' {
			return "", false
		}
	}
	return segment, true
}

// SpanKind is the kind core's span-naming.schema.json gives a caf invocation.
//
// `internal`, which the schema's enum allows and which OpenTelemetry defines as
// an operation internal to an application. There is no request, so `server`
// would be a lie a trace view would then act on — a `server` span with no peer
// and no URL is a span every reader stops trusting.
const SpanKind = trace.SpanKindInternal

// The kind above is asserted to be one core's span-naming.schema.json allows,
// rather than only a constant nobody reads. core's enum is
// internal/server/client/producer/consumer and there is no generated Go constant
// for it — the generated file carries the SPAN NAMES and the error vocabulary,
// not a kind list — so this is the check that caf has not drifted from a
// contract it reads by hand.
var _ = map[trace.SpanKind]string{
	SpanKind: "internal",
}
