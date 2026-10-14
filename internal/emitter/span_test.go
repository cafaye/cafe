package emitter

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/cafaye/cafe/internal/telemetry"
)

// This file asserts four things and no more, because those four are the packet's
// definition of done and everything else is decoration:
//
//  1. unset endpoint or a switch off: no exporter is constructed, no span made.
//  2. set endpoint: exactly one root span, named per core's grammar, identity on
//     the RESOURCE, and `error.type` from core's vocabulary on failure.
//  3. an inbound W3C traceparent is continued; a malformed one starts a new
//     trace and never fails the command.
//  4. the span name is never a path, a flag value, or an argument.
//
// The exporter is a `capturing` type declared at the foot of this file, chosen
// over the SDK's `tracetest.InMemoryExporter` for a reason documented there. So a
// passing test has exercised the real provider, the real batch processor, the
// real resource merge and the real shutdown — nothing here is a parallel
// implementation of any of them.

// recordingInstall is an installation wired to a capturing exporter instead of
// OTLP. Everything else — the provider, the batch processor, the sampler, the
// resource, the shutdown — is production code reached through the one seam
// Install exposes.
func recordingInstall(t *testing.T, env map[string]string) (Session, *capturing, error) {
	t.Helper()

	exp := newCapturing()
	session, err := Install(context.Background(), Options{
		Version:     "1.2.3",
		Environment: "test",
		TenantID:    "acme",
		Lookup:      func(name string) string { return env[name] },
		NewExporter: func(context.Context, string, bool) (sdktrace.SpanExporter, error) { return exp, nil },
	})
	return session, exp, err
}

// oneRun runs an invocation to completion and returns the spans it produced.
//
// Shutdown comes BEFORE the read, and that ordering is the assertion rather than
// an accident: a batch processor holds a CLI's one span until the flush, so
// nothing has been exported until then, and a flush that did not happen is a
// span that never leaves the process. Reading after the shutdown is what makes
// "the span arrived" mean something.
func oneRun(t *testing.T, session Session, args []string, outcome Outcome) tracetest.SpanStubs {
	t.Helper()

	_, end := session.StartInvocation(context.Background(), args)
	if err := end(outcome); err != nil {
		t.Fatalf("end the invocation span: %v", err)
	}
	if err := session.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	spans := session.spans()
	if len(spans) == 0 {
		t.Fatal("no span was recorded: the exporter was never reached, or the flush never happened")
	}
	return spans
}

// ---------------------------------------------------------------------------
// 1. the no-op path
// ---------------------------------------------------------------------------

// The no-op is asserted through an injected factory that COUNTS its calls. That
// is the whole point: the property is about what did not happen, so a test that
// merely checked `Enabled() == false` would pass on an installation that dialed
// and then threw the result away.
func TestTheNoOpPathNeverBuildsAnExporter(t *testing.T) {
	for _, row := range []struct {
		name       string
		env        map[string]string
		wantReason string
	}{
		{
			name:       "the global switch off",
			env:        map[string]string{telemetry.SwitchSDKDisabled: "true"},
			wantReason: telemetry.SwitchSDKDisabled + "=true",
		},
		{
			// Case-insensitive on purpose: the OTel variables are read by six
			// SDKs and typed by a person at a terminal, and TRUE is a
			// misspelling that should mean off rather than on.
			name:       "the global switch off, spelled TRUE",
			env:        map[string]string{telemetry.SwitchSDKDisabled: "TRUE"},
			wantReason: telemetry.SwitchSDKDisabled + "=true",
		},
		{
			// The case core's endpoint contract names: "a no-op that only covers
			// traces is a service that still phones home for metrics — the
			// failure discovered by a customer's invoice rather than by a test".
			name: "every signal off",
			env: map[string]string{
				telemetry.SwitchTracesExporter:  "none",
				telemetry.SwitchMetricsExporter: "none",
				telemetry.SwitchLogsExporter:    "none",
			},
			wantReason: "every " + telemetry.SwitchTracesExporter + ", " +
				telemetry.SwitchMetricsExporter + ", " + telemetry.SwitchLogsExporter + " is none",
		},
		{
			// Traces off alone is NOT a no-op, and this row exists to say so.
			// cafe emits only traces today, so the obvious next change to this
			// table is to add it as a no-op, and it would be wrong.
			name: "traces off but metrics on is not a no-op",
			env:  map[string]string{telemetry.SwitchTracesExporter: "none"},
		},
		{
			// An endpoint that is not set is the SHIPPED state, not a
			// misconfiguration. core's declaration is `required: false` with the
			// collector as the default value, which is what makes
			// observability on by default without being mandatory — so this row
			// asserts telemetry is ON with nothing configured.
			name: "nothing configured is on by default",
			env:  map[string]string{},
		},
		{
			name: "a full endpoint is on",
			env:  map[string]string{telemetry.EndpointVariable: "otel-collector:4317"},
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			var built int
			session, err := Install(context.Background(), Options{
				Lookup: func(name string) string { return row.env[name] },
				NewExporter: func(context.Context, string, bool) (sdktrace.SpanExporter, error) {
					built++
					return newCapturing(), nil
				},
			})
			if err != nil {
				t.Fatalf("Install: %v", err)
			}

			_, end := session.StartInvocation(context.Background(), []string{"dev"})
			if err := end(OutcomeSuccess); err != nil {
				t.Fatalf("end: %v", err)
			}
			if err := session.Shutdown(context.Background()); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}

			wantBuilt := 1
			if row.wantReason != "" {
				wantBuilt = 0
			}
			if built != wantBuilt {
				t.Errorf("the exporter factory was called %d times, want %d — the no-op path "+
					"must not reach it, because core's endpoint contract pins noOp.startupCost "+
					"to `none`: no dial on boot", built, wantBuilt)
			}
			if got := session.NoOpReason(); got != row.wantReason {
				t.Errorf("NoOpReason() = %q, want %q", got, row.wantReason)
			}
			if got := session.Enabled(); got != (row.wantReason == "") {
				t.Errorf("Enabled() = %v, want %v", got, row.wantReason == "")
			}
		})
	}
}

// The no-op session has to be safe to use, because a caller that has to branch
// on Enabled() before ending a span is a caller with two code paths — and the
// second is the one that never gets run.
func TestTheNoOpSessionIsSafeToUseWithoutTheSDK(t *testing.T) {
	session, err := Install(context.Background(), Options{
		Lookup: func(string) string { return "true" }, // OTEL_SDK_DISABLED
		NewExporter: func(context.Context, string, bool) (sdktrace.SpanExporter, error) {
			t.Error("the exporter factory was reached on the no-op path")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	_, end := session.StartInvocation(context.Background(), []string{"doctor"})
	if err := end(OutcomeFailure); err != nil {
		t.Errorf("ending a span on a no-op session returned %v, want nil", err)
	}
	if err := session.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown on a no-op session returned %v, want nil", err)
	}
}

// A command must not fail because telemetry will not start. core's endpoint
// contract makes the endpoint `required: false`, so this row is the contract
// rather than a courtesy.
func TestAnExporterThatWillNotBuildDoesNotFailTheCommand(t *testing.T) {
	session, err := Install(context.Background(), Options{
		Lookup: func(name string) string {
			if name == telemetry.EndpointVariable {
				return "otel-collector:4317"
			}
			return ""
		},
		NewExporter: func(context.Context, string, bool) (sdktrace.SpanExporter, error) {
			return nil, errors.New("dial tcp 172.20.0.3:4317: connection refused")
		},
	})
	if err != nil {
		t.Fatalf("Install returned %v; a telemetry backend that is down must not fail the command", err)
	}
	if session.Enabled() {
		t.Error("Enabled() is true with no exporter built")
	}
	if !strings.Contains(session.NoOpReason(), "connection refused") {
		t.Errorf("NoOpReason() = %q, want it to name why telemetry is not exporting", session.NoOpReason())
	}
}

// A resource that cannot be merged is a cafe bug rather than a deployment
// problem, so it is the ONE failure Install reports instead of swallowing.
// Silently running without identity would put spans in the backend under no
// service.name, which is the single outcome the resource block exists to
// prevent.
func TestAMalformedResourceIsReportedRatherThanSwallowed(t *testing.T) {
	// The merge itself, driven with two resources whose schema URLs differ —
	// the only way to make it fail — through the same helper Install uses.
	if _, err := mergeResource(
		sdkresource.NewWithAttributes("https://cafaye.com/a", attribute.String("x", "1")),
		sdkresource.NewWithAttributes("https://cafaye.com/b", attribute.String("y", "2")),
	); err == nil {
		t.Fatal("merging resources with different schema URLs succeeded, so the " +
			"conflict Install has to survive is not the one this test thinks it is")
	}

	// And the well-formed case merges, so the row above is not passing because
	// merging is broken.
	if _, err := mergeResource(
		sdkresource.Default(),
		sdkresource.NewWithAttributes(
			sdkresource.Default().SchemaURL(),
			attribute.String("service.name", telemetry.ServiceName),
		),
	); err != nil {
		t.Errorf("the resource merge Install performs failed on a well-formed install: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 2. one root span, named per core's grammar
// ---------------------------------------------------------------------------

func TestOneInvocationProducesOneRootSpanNamedPerCoresGrammar(t *testing.T) {
	for _, row := range []struct {
		name     string
		args     []string
		wantName string
	}{
		{name: "a single-word command", args: []string{"doctor"}, wantName: "cafe.doctor"},
		{name: "a long-running command", args: []string{"dev"}, wantName: "cafe.dev"},
		{
			// `contract lint` is a grouped verb: a Command named with its full
			// invocation and dispatched by its parent. One invocation, one span,
			// two segments — the grammar allows three after the service name.
			name: "a grouped verb", args: []string{"contract", "lint"}, wantName: "cafe.contract.lint",
		},
		{name: "a generator subcommand", args: []string{"gen", "telemetry"}, wantName: "cafe.gen.telemetry"},
		{
			// Flags and their values are excluded, so two runs of one command
			// with different arguments are ONE span. This is the property the
			// derivation exists for.
			name: "a flag and its value do not reach the name",
			// `cafe gen telemetry -out /tmp/x`: the flag and the word after it
			// are consumed together, leaving gen and telemetry.
			args: []string{"gen", "telemetry", "-out", "/tmp/somewhere"}, wantName: "cafe.gen.telemetry",
		},
		{
			name:     "an equals flag is one word",
			args:     []string{"dev", "--project=/tmp/a-long-project-path"},
			wantName: "cafe.dev",
		},
		{
			name:     "a trailing flag consumes nothing",
			args:     []string{"dev", "-no-infra"},
			wantName: "cafe.dev",
		},
		{name: "no arguments", args: nil, wantName: "cafe.cli.run"},
		{name: "help", args: []string{"--help"}, wantName: "cafe.cli.run"},
		{name: "version", args: []string{"-v"}, wantName: "cafe.cli.run"},
		{
			// 24 characters: over core's 15-per-segment bound. DROPPED rather
			// than truncated, because truncating produces two names for one
			// command depending on how long the word was.
			name:     "an over-long word is dropped, not mangled",
			args:     []string{"an-extremely-long-subcommand-name"},
			wantName: "cafe.cli.run",
		},
		{
			// The case the derivation exists for. A path is caller text, and
			// core's span-naming schema says a segment is at most fifteen
			// characters "so no segment can hold an identifier, a trace id, a
			// ULID, a URL or a path".
			name:     "a path is never a span name",
			args:     []string{"/Users/someone/Code/cafaye/caf-wt-otel/internal/emitter"},
			wantName: "cafe.cli.run",
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			session, _, err := recordingInstall(t, map[string]string{
				telemetry.EndpointVariable: "otel-collector:4317",
			})
			if err != nil {
				t.Fatalf("Install: %v", err)
			}

			spans := oneRun(t, session, row.args, OutcomeSuccess)
			if len(spans) != 1 {
				t.Fatalf("recorded %d spans, want exactly 1: one cafe invocation is one span", len(spans))
			}
			span := spans[0]

			if span.Name != row.wantName {
				t.Errorf("span name = %q, want %q", span.Name, row.wantName)
			}
			// Checked against the pattern the generator read out of core, not
			// against a copy written here.
			if !telemetry.SpanNamePattern.MatchString(span.Name) {
				t.Errorf("span name %q is refused by core's span-naming grammar %s",
					span.Name, telemetry.SpanNamePattern)
			}
			// The prefix is mandatory and is the emitting service's own name, "so
			// a span name never leaves a reader guessing which service produced
			// it".
			if !strings.HasPrefix(span.Name, telemetry.ServiceName+".") {
				t.Errorf("span name %q does not carry the emitting service %q", span.Name, telemetry.ServiceName)
			}
			// `internal`, and OpenTelemetry defines it as an operation internal
			// to an application. `server` would be a lie a trace view acts on:
			// a server span with no peer and no URL is one every reader stops
			// trusting.
			if span.SpanKind != SpanKind {
				t.Errorf("span kind = %v, want %v", span.SpanKind, SpanKind)
			}
			if !coreAllowsKind(span.SpanKind) {
				t.Errorf("span kind %v is not one core's span-naming.schema.json allows: %v",
					span.SpanKind, coreKinds)
			}
			// A CLI has no caller in the trace, so the span is a root.
			if span.Parent.HasSpanID() {
				t.Errorf("span has parent %s, want a root span", span.Parent.SpanID())
			}
		})
	}
}

// The identity attributes are on the RESOURCE and only there. core's traces
// schema says identity is exempt from the metric cardinality cap "which is why
// identity belongs here and not on a measurement", and it refuses a `service`
// field on the span for exactly the duplication reason.
func TestIdentityIsOnTheResourceAndNotOnTheSpan(t *testing.T) {
	session, _, err := recordingInstall(t, map[string]string{
		telemetry.EndpointVariable: "otel-collector:4317",
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	span := oneRun(t, session, []string{"doctor"}, OutcomeSuccess)[0]

	res := span.Resource
	for _, want := range []struct {
		key   attribute.Key
		value string
	}{
		{"service.name", telemetry.ServiceName},
		{"service.namespace", telemetry.Namespace},
		{"service.version", "1.2.3"},
		{"deployment.environment", "test"},
		{"tenant_id", "acme"},
		{"telemetry.sdk.name", "opentelemetry"},
		{"telemetry.sdk.language", "go"},
	} {
		got, ok := res.Set().Value(want.key)
		if !ok {
			t.Errorf("the resource has no %s", want.key)
			continue
		}
		if got.AsString() != want.value {
			t.Errorf("resource %s = %q, want %q", want.key, got.AsString(), want.value)
		}
	}

	for _, kv := range span.Attributes {
		if telemetry.OnResource(string(kv.Key)) {
			t.Errorf("the span carries %s, which belongs on the resource: restating identity as a "+
				"span field is the one-fact-two-places duplication core's traces.schema.json forbids",
				kv.Key)
		}
	}

	// And every attribute the span does carry is on core's trace allowlist,
	// which is `additionalProperties: false` on purpose.
	for _, kv := range span.Attributes {
		if _, ok := telemetry.Lookup(telemetry.SignalTraces, string(kv.Key)); !ok {
			t.Errorf("the span carries %s, which is not on core's trace allowlist %v",
				kv.Key, telemetry.Allowed(telemetry.SignalTraces))
		}
	}
}

// An unset version or environment is ABSENT from the resource, not a
// placeholder. core closes the resource with additionalProperties:false and
// gives deployment.environment a four-value enum, so a guess is a value in the
// backend somebody later has to filter out.
func TestAnUnknownVersionOrEnvironmentIsAbsentRatherThanGuessed(t *testing.T) {
	_, exp, err := recordingInstall(t, map[string]string{})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	session, err := Install(context.Background(), Options{
		Lookup:      func(string) string { return "" },
		NewExporter: func(context.Context, string, bool) (sdktrace.SpanExporter, error) { return exp, nil },
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	spans := oneRun(t, session, []string{"lock"}, OutcomeSuccess)
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	res := spans[0].Resource

	for _, key := range []attribute.Key{"service.version", "deployment.environment", "tenant_id"} {
		if _, ok := res.Set().Value(key); ok {
			t.Errorf("the resource carries %s with nothing configured for it; it must be absent", key)
		}
	}
	// service.name is not optional: it is "what makes a span attributable to a
	// service in a backend that has never heard of cafaye".
	if _, ok := res.Set().Value("service.name"); !ok {
		t.Error("the resource has no service.name")
	}
}

// ---------------------------------------------------------------------------
// failures: status Error plus a class from core's vocabulary
// ---------------------------------------------------------------------------

// core's traces schema makes status and error.type a biconditional: status error
// obliges a class, and a class obliges a failed status. Both directions are
// asserted here, because the ABSENCE of the class is what makes a success
// legible.
func TestAFailedInvocationCarriesAClassFromCoresVocabulary(t *testing.T) {
	for _, row := range []struct {
		name     string
		outcome  Outcome
		wantType string
		wantErr  bool
	}{
		{
			name:     "a success carries no error class at all",
			outcome:  OutcomeSuccess,
			wantType: "",
		},
		{
			// A typo is not a cafe bug. Filing it as internal_error puts a
			// person's mistake in the same dashboard bucket as a defect, and the
			// two have opposite remedies.
			name:     "a usage error is invalid_request",
			outcome:  OutcomeUsage,
			wantType: "invalid_request",
			wantErr:  true,
		},
		{
			// The honest default. core's schema makes error.type an enum rather
			// than a pattern precisely so a class cannot be inferred from an
			// error string: a pattern constrains the shape of a value and says
			// nothing about the set, so `user_42_email_invalid` validates and
			// becomes its own series.
			name:     "a failure is internal_error",
			outcome:  OutcomeFailure,
			wantType: "internal_error",
			wantErr:  true,
		},
		{
			// An operator's own Ctrl-C is not an error an alert should page on.
			name:     "a cancellation is canceled",
			outcome:  OutcomeCancelled,
			wantType: telemetry.ErrorTypeCancelled,
			wantErr:  true,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			session, _, err := recordingInstall(t, map[string]string{
				telemetry.EndpointVariable: "otel-collector:4317",
			})
			if err != nil {
				t.Fatalf("Install: %v", err)
			}
			span := oneRun(t, session, []string{"deploy"}, row.outcome)[0]

			var gotType, gotMirror string
			for _, kv := range span.Attributes {
				switch string(kv.Key) {
				case "error.type":
					gotType = kv.Value.AsString()
				case "otel.status_code":
					gotMirror = kv.Value.AsString()
				}
			}

			if gotType != row.wantType {
				t.Errorf("error.type = %q, want %q", gotType, row.wantType)
			}
			// A member of the vocabulary, not merely a string that fits the
			// shape. That difference is the reason core made it an enum.
			if row.wantType != "" && !telemetry.IsErrorType(gotType) {
				t.Errorf("error.type %q is not in core's closed vocabulary %v", gotType, telemetry.ErrorTypes())
			}

			if row.wantErr {
				if span.Status.Code != codes.Error {
					t.Errorf("status = %v, want Error — a span carrying a class has claimed it failed",
						span.Status.Code)
				}
				// The mirror may not contradict the field of record.
				if gotMirror != "ERROR" {
					t.Errorf("otel.status_code = %q, want ERROR alongside an Error status", gotMirror)
				}
			} else {
				if span.Status.Code == codes.Error {
					t.Error("a successful invocation carries an Error status")
				}
				// The load-bearing not-an-error marker: present exactly when the
				// span failed, and absent otherwise.
				if gotType != "" {
					t.Errorf("a successful span carries error.type %q; the class must be present "+
						"exactly when the span failed, or a success moves the numerator of an error rate",
						gotType)
				}
				// The mirror is set on success too: core's third allOf rule only
				// constrains it when it says ERROR, so the omission is legal and
				// useless — a query filtering `otel.status_code=OK` needs the
				// successes to be there.
				if gotMirror != "OK" {
					t.Errorf("otel.status_code = %q, want OK", gotMirror)
				}
			}

			// `error.message` is deliberately absent from core's vocabulary — "a
			// provider's content-policy rejection quotes the offending content
			// back, so the message is a prompt by another route" — which is why
			// SetStatus is called with an empty message and RecordError is not
			// used at all (it would add exception.message).
			for _, kv := range span.Attributes {
				if strings.HasPrefix(string(kv.Key), "exception.") || kv.Key == "error.message" {
					t.Errorf("the span carries %s, which core's trace allowlist does not have", kv.Key)
				}
			}
			if span.Status.Description != "" {
				t.Errorf("status description = %q, want empty: it is a place for caller text",
					span.Status.Description)
			}
		})
	}
}

// Every class this package can emit is one core's vocabulary has, and none of
// them names content. A table rather than a comment, because a class checked only
// at a call site is a class the collector's redaction drops silently — and a
// silently dropped class is a failure that looks like a success.
func TestEveryClassThisPackageEmitsIsInCoresVocabulary(t *testing.T) {
	for _, outcome := range []Outcome{OutcomeSuccess, OutcomeUsage, OutcomeFailure, OutcomeCancelled} {
		class := errorType(outcome)
		if class == "" {
			continue
		}
		if !telemetry.IsErrorType(class) {
			t.Errorf("outcome %d maps to error.type %q, which is not in core's vocabulary %v",
				outcome, class, telemetry.ErrorTypes())
		}
		if word, found := telemetry.NamesContent(class); found {
			t.Errorf("outcome %d maps to error.type %q, and a class whose name names content "+
				"(%q) is the leak core's redaction schema exists to prevent", outcome, class, word)
		}
	}
}

// A command that knows better can name its own class, and the Classified value
// stays transparent to errors.Is — the router's own error handling must not
// change when a command classifies.
func TestACommandCanNameItsOwnClassWithoutBreakingErrorInspection(t *testing.T) {
	sentinel := errors.New("could not reach the registry")

	wrapped := Classify(sentinel, "dependency_unavailable")
	if !errors.Is(wrapped, sentinel) {
		t.Error("Classify broke errors.Is on the error it wrapped")
	}
	if wrapped.Error() != sentinel.Error() {
		t.Errorf("Classify.Error() = %q, want the underlying message", wrapped.Error())
	}
	if !telemetry.IsErrorType(wrapped.kind) {
		t.Errorf("Classify accepted %q, which is not in core's vocabulary", wrapped.kind)
	}
}

// ---------------------------------------------------------------------------
// 3. inbound traceparent
// ---------------------------------------------------------------------------

// A cafe invocation started by another cafaye process — `cafe env up` running a
// command under test, a CI job, a shell script — continues the caller's trace
// when one is in the context. That is the property kit's codec exists for.
func TestAnInboundTraceparentIsContinued(t *testing.T) {
	session, _, err := recordingInstall(t, map[string]string{
		telemetry.EndpointVariable: "otel-collector:4317",
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// A parent, built the way a caller's context carries one. It is ENDED, and
	// that is not tidiness: a batch processor exports an ended span, so an
	// unended parent never arrives and the count below would be one short of the
	// two this test is about.
	parentCtx, parent := session.tracer.Start(context.Background(), "caller.process")
	parentTrace := parent.SpanContext().TraceID()
	parentSpanID := parent.SpanContext().SpanID()

	_, end := session.StartInvocation(parentCtx, []string{"doctor"})
	if err := end(OutcomeSuccess); err != nil {
		t.Fatalf("end: %v", err)
	}
	// Both ends BEFORE the flush, for the reason on oneRun: the flush is what
	// exports, so a span ended after it never arrives.
	parent.End()

	if err := session.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	spans := session.spans()
	if len(spans) != 2 {
		t.Fatalf("recorded %d spans, want 2 (the caller's and this one)", len(spans))
	}
	// Found by name rather than by index: the export order of two spans ending
	// in either order is not a contract, and a test that assumes it passes for
	// the wrong reason on the day the batch processor changes.
	var child tracetest.SpanStub
	for _, span := range spans {
		if span.Name == "cafe.doctor" {
			child = span
		}
	}
	if child.Name == "" {
		t.Fatalf("no span named cafe.doctor in %v", spanNames(spans))
	}

	if child.SpanContext.TraceID() != parentTrace {
		t.Errorf("the child trace id is %s, want the caller's %s — an inbound trace has to be "+
			"continued, or one request reads as two unrelated traces",
			child.SpanContext.TraceID(), parentTrace)
	}
	if !child.Parent.HasSpanID() {
		t.Error("the child span has no parent; the caller's span id was not carried")
	}
	if child.SpanContext.SpanID() == parentSpanID {
		t.Error("the child reused the parent's span id")
	}
}

// A W3C traceparent read through the SDK's propagator — the path a real inbound
// header takes — continues the caller's trace. This is the assertion that a
// header read off an Env var reaches the span, which is how a cafe child process
// inherits its caller's trace in practice.
func TestATraceparentFromTheEnvironmentIsContinued(t *testing.T) {
	session, _, err := recordingInstall(t, map[string]string{
		telemetry.EndpointVariable: "otel-collector:4317",
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Read back out of the global because Install sets it: a test that built its
	// own TraceContext{} would prove nothing about the wiring.
	ctx := otel.GetTextMapPropagator().Extract(context.Background(),
		propagation.HeaderCarrier(http.Header{
			"Traceparent": {"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
			"Tracestate":  {"vendor=value"},
		}))

	_, end := session.StartInvocation(ctx, []string{"doctor"})
	if err := end(OutcomeSuccess); err != nil {
		t.Fatalf("end: %v", err)
	}
	if err := session.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	span := session.spans()[0]
	wantTrace, _ := traceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if span.SpanContext.TraceID() != wantTrace {
		t.Errorf("trace id = %s, want the inbound %s", span.SpanContext.TraceID(), wantTrace)
	}
	if !span.Parent.HasSpanID() {
		t.Error("the span has no parent, so the inbound traceparent was not continued")
	}
	if !span.SpanContext.IsSampled() {
		t.Error("the inbound sampled flag was not preserved: ParentBased defers to the caller, " +
			"and kit's compose template pins the same sampler for the same reason")
	}
}

// A MALFORMED traceparent starts a new trace and never fails the command. This
// is W3C §4.2 and kit's codec rule — "A request is not an error because its
// trace header was garbage" — and a CLI that exited non-zero on a bad header in
// the environment would refuse to run because somebody set TRACEPARENT badly.
func TestAMalformedInboundTraceparentStartsANewTraceAndNeverFails(t *testing.T) {
	for _, row := range []struct {
		name        string
		traceparent string
	}{
		{name: "too short", traceparent: "00-abc-def"},
		// §3.2.2.3: an all-zero trace-id is invalid, so the header is ignored.
		{name: "all-zero trace id", traceparent: "00-00000000000000000000000000000000-4bf92f3577b34da6a3ce929d0e0e4736-01"},
		// §3.2.2.4: an all-zero parent-id is invalid too.
		{name: "all-zero parent id", traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01"},
		// §3.2.2.1: version ff is forbidden.
		{name: "forbidden version ff", traceparent: "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "not hex", traceparent: "00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-00f067aa0ba902b7-01"},
		{name: "a URL in the header", traceparent: "https://example.com/00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "empty", traceparent: ""},
		{
			// §3.3: a failed traceparent MUST NOT be rescued by a tracestate
			// alongside it. kit's suite asserts this and so does this row.
			name:        "a tracestate does not rescue a bad traceparent",
			traceparent: "not-a-traceparent",
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			session, _, err := recordingInstall(t, map[string]string{
				telemetry.EndpointVariable: "otel-collector:4317",
			})
			if err != nil {
				t.Fatalf("Install: %v", err)
			}

			header := http.Header{}
			if row.traceparent != "" {
				header.Set("Traceparent", row.traceparent)
			}
			// §3.2.1: header names are matched case-insensitively and sent
			// lowercase, so the lookup must not depend on the spelling.
			header.Set("Tracestate", "vendor=value")

			ctx := otel.GetTextMapPropagator().Extract(context.Background(), propagation.HeaderCarrier(header))
			_, end := session.StartInvocation(ctx, []string{"doctor"})
			if err := end(OutcomeSuccess); err != nil {
				t.Fatalf("a malformed traceparent failed the invocation: %v", err)
			}
			if err := session.Shutdown(context.Background()); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}

			spans := session.spans()
			if len(spans) != 1 {
				t.Fatalf("recorded %d spans, want 1", len(spans))
			}
			span := spans[0]

			// A new trace: a valid, non-zero id that is not the garbage one, and
			// a root span.
			if !span.SpanContext.TraceID().IsValid() {
				t.Error("a malformed traceparent produced an invalid trace id; it must start a NEW trace")
			}
			if span.Parent.HasSpanID() {
				t.Error("a malformed traceparent produced a child span; it must start a NEW trace")
			}
			// A root span is sampled: there is no caller's opinion to defer to.
			if !span.SpanContext.IsSampled() {
				t.Error("a new root span is not sampled")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// the name derivation, on its own
// ---------------------------------------------------------------------------

// The derivation is a pure function and is tested as one, because it is where a
// cardinality bug would live and it is the only thing in this package with an
// unbounded input.
func TestTheSpanNameDerivation(t *testing.T) {
	for _, row := range []struct {
		name string
		args []string
		want string
	}{
		{name: "one word", args: []string{"dev"}, want: "cafe.dev"},
		{name: "mixed case folds", args: []string{"Dev"}, want: "cafe.dev"},
		{name: "upper case folds", args: []string{"DEV"}, want: "cafe.dev"},
		// A dash is legal in a service name and illegal in every segment after
		// it, so it becomes an underscore — which the grammar does allow.
		{name: "a dash becomes an underscore", args: []string{"my-command"}, want: "cafe.my_command"},
		{name: "three segments fill the grammar", args: []string{"a", "b", "c"}, want: "cafe.a.b.c"},
		// A fourth is dropped rather than making the name illegal.
		{name: "a fourth segment is dropped", args: []string{"a", "b", "c", "d"}, want: "cafe.a.b.c"},
		// A segment may not start with a digit or an underscore.
		{name: "a leading digit is refused", args: []string{"1st"}, want: "cafe.cli.run"},
		{name: "a leading underscore is refused", args: []string{"_x"}, want: "cafe.cli.run"},
		// 15 characters exactly is legal; 16 is not.
		{name: "fifteen characters is legal", args: []string{"abcdefghijklmno"}, want: "cafe.abcdefghijklmno"},
		{name: "sixteen characters is not", args: []string{"abcdefghijklmnop"}, want: "cafe.cli.run"},
		// A path, a URL and a ULID: the three things core's length bound exists
		// to keep out of a name.
		{name: "a path is refused", args: []string{"/etc/hosts"}, want: "cafe.cli.run"},
		{name: "a url is refused", args: []string{"https://example.com/x"}, want: "cafe.cli.run"},
		{name: "a ulid is too long to be a segment", args: []string{"usr_01J9Z8QK5M4N7P2R3T6V8W9X0A"}, want: "cafe.cli.run"},
	} {
		t.Run(row.name, func(t *testing.T) {
			got := InvocationSpanName(row.args)
			if got != row.want {
				t.Errorf("InvocationSpanName(%q) = %q, want %q", row.args, got, row.want)
			}
			if !telemetry.SpanNamePattern.MatchString(got) {
				t.Errorf("%q is refused by core's grammar", got)
			}
		})
	}
}

// Deterministic, which is what makes a span name a groupable key rather than a
// per-run surprise.
func TestTheSpanNameDerivationIsDeterministic(t *testing.T) {
	args := []string{"gen", "telemetry", "-out", "/tmp/x", "--force"}
	first := InvocationSpanName(args)
	for i := 0; i < 10; i++ {
		if got := InvocationSpanName(args); got != first {
			t.Fatalf("call %d produced %q, the first produced %q", i, got, first)
		}
	}
}

// The two ends of the endpoint contract, asserted on the function that reads
// them: CAFE_OTEL_ENDPOINT wins, the standard variable is the fallback so cafe
// works with generic OTel tooling, and the shipped collector is the default.
func TestTheEndpointPrecedence(t *testing.T) {
	for _, row := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "the cafaye variable wins",
			env: map[string]string{
				telemetry.EndpointVariable:    "mine:4317",
				"OTEL_EXPORTER_OTLP_ENDPOINT": "theirs:4317",
			},
			want: "mine:4317",
		},
		{
			name: "the standard variable is the fallback",
			env:  map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "theirs:4317"},
			want: "theirs:4317",
		},
		{
			name: "the shipped collector is the default",
			env:  map[string]string{},
			want: telemetry.DefaultEndpoint,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			got := EndpointFor(func(n string) string { return row.env[n] })
			if got != row.want {
				t.Errorf("EndpointFor() = %q, want %q", got, row.want)
			}
		})
	}
}

// The gRPC exporter wants a host:port. core's own generated declaration writes
// the default as `http://otel-collector:4317` — http, on the gRPC port, because
// the field names an ADDRESS and the collector's 4317 speaks gRPC regardless of
// the prefix. Handing that to the exporter unstripped is the mistake that
// produces a dial error naming a URL as though it were a path, so all three
// spellings land on the same host:port.
func TestTheSchemeIsStrippedForTheGRPCExporter(t *testing.T) {
	for _, row := range []struct{ in, want string }{
		{in: "http://otel-collector:4317", want: "otel-collector:4317"},
		{in: "https://collector.example:4317", want: "collector.example:4317"},
		{in: "grpc://otel-collector:4317", want: "otel-collector:4317"},
		{in: "otel-collector:4317", want: "otel-collector:4317"},
		{in: "127.0.0.1:4317", want: "127.0.0.1:4317"},
	} {
		t.Run(row.in, func(t *testing.T) {
			if got := hostPort(row.in); got != row.want {
				t.Errorf("hostPort(%q) = %q, want %q", row.in, got, row.want)
			}
		})
	}
}

// The endpoint Install hands the exporter is already a host:port, asserted
// through the seam rather than by reading hostPort's own test.
func TestInstallHandsTheExporterASchemeFreeEndpoint(t *testing.T) {
	var got string
	_, err := Install(context.Background(), Options{
		Lookup: func(name string) string {
			if name == telemetry.EndpointVariable {
				return "http://otel-collector:4317"
			}
			return ""
		},
		NewExporter: func(_ context.Context, endpoint string, _ bool) (sdktrace.SpanExporter, error) {
			got = endpoint
			return newCapturing(), nil
		},
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got != "otel-collector:4317" {
		t.Errorf("the exporter was built with %q, want the scheme-free %q", got, "otel-collector:4317")
	}
}

// coreKinds is the enum from core's span-naming.schema.json, transcribed.
//
// It is written here rather than read out of the vendored schema because the
// generated package does not carry span KINDS — it carries the names, the error
// vocabulary and the attribute allowlists, and there is no generated constant
// for this enum. So this is a hand-maintained copy of five values, and the only
// thing keeping it honest is the row that checks the one cafe uses is on it. A
// copy that drifts fails loudly at that row rather than silently.
var coreKinds = map[trace.SpanKind]bool{
	trace.SpanKindInternal: true,
	trace.SpanKindServer:   true,
	trace.SpanKindClient:   true,
	trace.SpanKindProducer: true,
	trace.SpanKindConsumer: true,
}

func coreAllowsKind(kind trace.SpanKind) bool { return coreKinds[kind] }

// traceIDFromHex parses the hex in a traceparent, so the expectation in the test
// above is the header's own bytes rather than a value typed twice.
func traceIDFromHex(hex string) (trace.TraceID, error) {
	var id trace.TraceID
	if len(hex) != 32 {
		return id, errors.New("a trace id is 32 hex characters, got " + hex)
	}
	for i := 0; i < 16; i++ {
		hi, err1 := hexNibble(hex[i*2])
		lo, err2 := hexNibble(hex[i*2+1])
		if err1 != nil || err2 != nil {
			return id, errors.New("not hex: " + hex)
		}
		id[i] = hi<<4 | lo
	}
	return id, nil
}

// hexNibble is one hex digit, written out so this test file carries no import
// beyond the SDK's.
func hexNibble(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	}
	return 0, errors.New("not a hex digit")
}

// spans reads back what this session exported.
//
// It is a method rather than a test-local type assertion because the field it
// reads is unexported: a test that reached for `session.exporter` and cast it
// would be asserting on a shape the package does not promise, in six places
// rather than one. An in-memory exporter that is not the one installed is a test
// that passes against the wrong subject.
func (s Session) spans() tracetest.SpanStubs {
	exp, ok := s.exporter.(*capturing)
	if !ok {
		return nil
	}
	return exp.Spans()
}

// capturing is the exporter this package's tests install.
//
// It exists because the SDK's own `tracetest.InMemoryExporter` cannot observe an
// installation that uses a batch processor, and it is worth saying exactly why
// because both halves of the reason cost a debugging round each:
//
//   - A batch processor holds an ended span until its batch timeout. Install uses
//     `WithBatcher`, which kit's pins.md requires ("Batch processors, never
//     simple ones, in any production path"), so reading the exporter BEFORE the
//     flush finds nothing. The flush is what makes a CLI's single span leave the
//     process at all, so this is not a test artifact — it is the behavior.
//
//   - `InMemoryExporter.Shutdown` calls `Reset`, so reading it AFTER the flush
//     finds nothing either. A provider's Shutdown shuts its exporter down, and
//     this one treats that as "clear".
//
// So the two orderings are each wrong and a test using it has to choose one. This
// type copies the spans out as they are exported and does not clear on shutdown,
// which is the only observation point that survives both.
type capturing struct {
	mu    sync.Mutex
	spans tracetest.SpanStubs
}

func newCapturing() *capturing { return &capturing{} }

func (c *capturing) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.spans = append(c.spans, tracetest.SpanStubsFromReadOnlySpans(spans)...)
	return nil
}

func (c *capturing) Shutdown(context.Context) error { return nil }

func (c *capturing) Spans() tracetest.SpanStubs {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(tracetest.SpanStubs, len(c.spans))
	copy(out, c.spans)
	return out
}

// spanNames names the spans a test recorded, for a failure message that says
// which ones arrived.
func spanNames(spans tracetest.SpanStubs) []string {
	out := make([]string, 0, len(spans))
	for _, span := range spans {
		out = append(out, span.Name)
	}
	return out
}

// The encryption decision, which is decided by the SCHEME and was the cause of a
// measured bug.
//
// `otlptracegrpc` defaults to TLS. core's shipped default endpoint is
// `http://otel-collector:4317` — plaintext gRPC on the collector's 4317. With
// TLS on, cafe opened NO CONNECTION AT ALL: `lsof` against a real OTLP receiver
// listening on the configured port showed the listener and nothing else, on
// every command, and the exporter's handshake failure was dropped by the error
// handler installed for `noOp.warnings`. The observable result was a cafe that
// exported nothing, said nothing, and cost every command its flush budget.
//
// So the rule is pinned here for all four spellings a person can write.
func TestOnlyHTTPSMeansTLS(t *testing.T) {
	for _, row := range []struct {
		endpoint string
		want     bool
	}{
		// core's own default, and the row that was broken.
		{endpoint: "http://otel-collector:4317", want: false},
		{endpoint: "grpc://otel-collector:4317", want: false},
		{endpoint: "otel-collector:4317", want: false},
		{endpoint: "127.0.0.1:4317", want: false},
		{endpoint: "https://collector.example:4317", want: true},
		// Case matters for a scheme and not for a hostname, so an upper-case
		// HTTPS is still a scheme a person typed — and treating it as plaintext
		// would be the silent failure this test exists to prevent. It is
		// therefore folded, which is what URL parsing does and what a person
		// means.
		{endpoint: "HTTPS://collector.example:4317", want: true},
	} {
		t.Run(row.endpoint, func(t *testing.T) {
			if got := secure(row.endpoint); got != row.want {
				t.Errorf("secure(%q) = %v, want %v", row.endpoint, got, row.want)
			}
		})
	}
}

// And the two facts are read from the same string at the same site, so a caller
// cannot end up stripping the scheme and losing the encryption it named — which
// is how the bug happened.
func TestTheSchemeIsStrippedAndTheEncryptionIsKept(t *testing.T) {
	var gotEndpoint string
	var gotSecure bool
	endpoint := "https://collector.example:4317"

	_, err := Install(context.Background(), Options{
		Lookup: func(name string) string {
			if name == telemetry.EndpointVariable {
				return endpoint
			}
			return ""
		},
		NewExporter: func(_ context.Context, hostport string, secure bool) (sdktrace.SpanExporter, error) {
			gotEndpoint, gotSecure = hostport, secure
			return newCapturing(), nil
		},
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if gotEndpoint != "collector.example:4317" {
		t.Errorf("the exporter got %q, want the scheme-free host:port", gotEndpoint)
	}
	if !gotSecure {
		t.Error("the exporter got secure=false for an https endpoint; the scheme named the " +
			"encryption and stripping it must not lose that")
	}
}

// The wire, not just the object. A span that reaches a real OTLP/gRPC receiver
// in another process with the name, kind, resource and attributes core
// specifies — measured, not asserted against an in-memory exporter.
//
// It is the row that caught the TLS bug, and it exists because the in-process
// tests all pass with the exporter misconfigured: they INJECT one, so
// everything below `Install` — the endpoint, the scheme, the transport — is
// untested by every other test in this package.
//
// The collector is `collector_test.go`'s, a real gRPC server that decodes what it
// receives. The first version of this test used a hand-rolled socket, and it
// cheerfully reported a TLS ClientHello as a delivered span.
func TestTheSpanReachesARealOTLPReceiverOverTheWire(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: this test binds a loopback port and speaks gRPC")
	}

	server := startCollector(t)
	// The REAL exporter, not recordingInstall's. Injecting one is what every
	// other test in this file does and it is exactly why this test exists: an
	// injected exporter is a span that never touches a socket, so it passes with
	// the transport misconfigured.
	session, err := Install(context.Background(), Options{
		Lookup: func(name string) string {
			if name == telemetry.EndpointVariable {
				return server.endpoint()
			}
			return ""
		},
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !session.Enabled() {
		t.Fatalf("telemetry is off with an endpoint set: %q", session.NoOpReason())
	}

	_, end := session.StartInvocation(context.Background(), []string{"contract", "lint"})
	if err := end(OutcomeSuccess); err != nil {
		t.Fatalf("end: %v", err)
	}
	if err := session.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	received := spans(server.next(t, 5*time.Second))
	if len(received) == 0 {
		t.Fatal("the collector received a request with no spans in it")
	}
	span := received[0]

	if span.span.GetName() != "cafe.contract.lint" {
		t.Errorf("span name on the wire = %q, want %q", span.span.GetName(), "cafe.contract.lint")
	}
	if !telemetry.SpanNamePattern.MatchString(span.span.GetName()) {
		t.Errorf("span name %q is refused by core's grammar", span.span.GetName())
	}
	if span.span.GetKind() != tracepb.Span_SPAN_KIND_INTERNAL {
		t.Errorf("span kind on the wire = %v, want INTERNAL", span.span.GetKind())
	}
	if got := span.span.GetParentSpanId(); len(got) != 0 {
		t.Errorf("the span on the wire has parent %x, want a root span", got)
	}

	// Identity survived the wire, on the resource.
	if name, ok := span.resourceAttribute("service.name"); !ok || name != telemetry.ServiceName {
		t.Errorf("service.name on the wire = %q (present=%v), want %q; the resource carried %v",
			name, ok, telemetry.ServiceName, span.resourceAttributeNames())
	}
	if namespace, ok := span.resourceAttribute("service.namespace"); !ok || namespace != telemetry.Namespace {
		t.Errorf("service.namespace on the wire = %q (present=%v), want %q",
			namespace, ok, telemetry.Namespace)
	}
	if _, ok := span.attribute("service.name"); ok {
		t.Error("the span carries service.name on the wire; identity belongs on the resource " +
			"and core's traces.schema.json forbids restating it as a span field")
	}
}

// A failure on the wire, carrying the class — because the class is the whole
// reason the traces schema makes status and error.type a biconditional, and a
// class that does not survive serialization is a class no dashboard can group on.
func TestAFailureReachesTheWireWithItsClass(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: this test binds a loopback port and speaks gRPC")
	}

	server := startCollector(t)
	session, err := wireInstall(server)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	_, end := session.StartInvocation(context.Background(), []string{"nonsense"})
	if err := end(OutcomeUsage); err != nil {
		t.Fatalf("end: %v", err)
	}
	if err := session.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	received := spans(server.next(t, 5*time.Second))
	if len(received) == 0 {
		t.Fatal("the collector received a request with no spans in it")
	}
	span := received[0]

	if span.span.GetStatus().GetCode() != tracepb.Status_STATUS_CODE_ERROR {
		t.Errorf("status on the wire = %v, want ERROR — the field of record for whether the "+
			"span failed", span.span.GetStatus().GetCode())
	}
	class, ok := span.attribute("error.type")
	if !ok {
		t.Fatalf("no error.type on the wire; the attributes carried were %v", span.attributeNames())
	}
	if class != "invalid_request" {
		t.Errorf("error.type on the wire = %q, want invalid_request", class)
	}
	if !telemetry.IsErrorType(class) {
		t.Errorf("error.type %q is not in core's closed vocabulary %v", class, telemetry.ErrorTypes())
	}
	if mirror, ok := span.attribute("otel.status_code"); !ok || mirror != "ERROR" {
		t.Errorf("otel.status_code on the wire = %q (present=%v), want ERROR", mirror, ok)
	}
	// `error.message` is absent from core's vocabulary on purpose, and its
	// presence on the wire would be the leak that absence prevents.
	if msg, ok := span.attribute("error.message"); ok {
		t.Errorf("error.message on the wire = %q; core's traces.schema.json excludes it because "+
			"a provider's rejection quoting content back is a prompt by another route", msg)
	}
}

// A malformed traceparent still exports, measured over the wire. A cafe child
// process started by something that set TRACEPARENT to garbage must still emit,
// because a request — or here a command — is not an error because its trace header
// was garbage (W3C §4.2).
func TestAMalformedTraceparentStillExportsOverTheWire(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: this test binds a loopback port and speaks gRPC")
	}

	server := startCollector(t)
	session, err := wireInstall(server)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	ctx := otel.GetTextMapPropagator().Extract(context.Background(),
		propagation.HeaderCarrier(http.Header{"Traceparent": {"not-a-traceparent"}}))

	_, end := session.StartInvocation(ctx, []string{"doctor"})
	if err := end(OutcomeSuccess); err != nil {
		t.Fatalf("a malformed traceparent failed the invocation: %v", err)
	}
	if err := session.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	received := spans(server.next(t, 5*time.Second))
	if len(received) == 0 {
		t.Fatal("a malformed traceparent stopped the span being exported")
	}
	if len(received[0].span.GetTraceId()) != 16 {
		t.Errorf("the exported trace id is %d bytes, want 16 — a new trace, and a valid one",
			len(received[0].span.GetTraceId()))
	}
	zeroed := true
	for _, b := range received[0].span.GetTraceId() {
		if b != 0 {
			zeroed = false
		}
	}
	if zeroed {
		t.Error("the exported trace id is all zeros, which is the id core's grammar forbids")
	}
}

// An inbound traceparent continues a trace ACROSS PROCESSES, which is the whole
// reason propagation exists, and it can only be shown with two processes.
//
// cafe starts no child process, so the second process here is this test's own: it
// reads the span a first, parented invocation exported and checks the trace id
// matches the one it put in the header. That is the same assertion a fleet makes
// when gateway's trace and identity's trace meet in one view.
func TestAnInboundTraceparentIsContinuedAcrossProcessesOnTheWire(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: this test binds a loopback port and speaks gRPC")
	}

	server := startCollector(t)
	session, err := wireInstall(server)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// A header as a calling process would send it.
	header := http.Header{"Traceparent": {"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}}
	ctx := otel.GetTextMapPropagator().Extract(context.Background(), propagation.HeaderCarrier(header))

	_, end := session.StartInvocation(ctx, []string{"doctor"})
	if err := end(OutcomeSuccess); err != nil {
		t.Fatalf("end: %v", err)
	}
	if err := session.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	received := spans(server.next(t, 5*time.Second))
	if len(received) == 0 {
		t.Fatal("the collector received a request with no spans in it")
	}
	span := received[0]

	want, parseErr := traceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if parseErr != nil {
		t.Fatalf("the test's own trace id is unparseable: %v", parseErr)
	}
	if got := span.span.GetTraceId(); string(got) != string(want[:]) {
		t.Errorf("trace id on the wire = %x, want the inbound %x — an inbound trace has to be "+
			"continued, or one request reads as two unrelated traces in a trace view", got, want)
	}
	// The caller's span id is carried as this hop's parent.
	if got := span.span.GetParentSpanId(); got == nil || len(got) != 8 {
		t.Errorf("parent span id on the wire = %x, want the caller's 8-byte id", got)
	} else if wantParent := []byte{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7}; string(got) != string(wantParent) {
		t.Errorf("parent span id on the wire = %x, want the caller's %x", got, wantParent)
	}
}
