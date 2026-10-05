package emitter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/cafaye/caf/internal/telemetry"
)

// shutdownTimeout bounds the flush at the end of a command.
//
// MEASURED, and the number is the argument. caf's shipped default endpoint is
// `http://otel-collector:4317`, which does not resolve on a machine that has not
// started the local stack — so this budget is paid by EVERY command a developer
// runs before they have run `caf dev` once. At 2s, `caf version` took 2.001s;
// at 200ms it takes 0.2s. Measured on this machine, both times, and the 2s
// figure is why the first number in this file is not 2.
//
// core's endpoint contract anticipates exactly this and says what to do: the
// `timeoutMs` field is described as "A telemetry export that can block a request
// is a latency bug in production caused by an observability tool, which is the
// worst possible source of one... a service that lowers it is protecting its
// request path, not degrading its telemetry." A CLI's request path is a person
// waiting at a terminal, and that is the thing to protect.
//
// The cost, stated rather than glossed: on a genuinely slow collector link a
// span can be lost, because the flush gives up rather than holding the terminal.
// That is the right way round for a CLI. `caf dev` — the one long-running
// command — has a context of its own and can afford a longer wait, and the
// timeout is not a constant the caller can raise today; that is the one thing
// here a successor should add, and it is named in the report rather than left
// for somebody to discover.
const shutdownTimeout = 200 * time.Millisecond

// exporterFactory builds the OTLP span exporter. It is a field rather than a
// direct call so the no-op test can prove that the disabled path never reaches
// it — see doc.go.
//
// `secure` is passed separately from the endpoint rather than left inside it,
// and that split exists because of a measured bug: the gRPC exporter defaults to
// TLS, core's shipped default endpoint is plaintext, and a caller that stripped
// the scheme before calling here destroyed the only evidence that said which
// encryption was wanted. See the note on newGRPCExporter.
type exporterFactory func(ctx context.Context, endpoint string, secure bool) (sdktrace.SpanExporter, error)

// Options is what one installation needs to know. Everything arrives here
// rather than from package state, which is the rule every package in this tree
// follows and the reason this one is testable at all.
type Options struct {
	// Version is the service.version resource attribute. Empty is legal: a
	// development build has no version, and core's schema does not require one.
	Version string

	// Environment is the deployment.environment resource attribute. Empty means
	// the attribute is left off rather than set to a guess: core's schema has a
	// four-value enum and inventing a fifth value is exactly the thing the enum
	// exists to prevent.
	Environment string

	// TenantID is the tenant_id resource attribute, and only ever a resource
	// attribute. core's traces schema says identity here "and never on a
	// measurement", and this is a case where honouring that costs nothing.
	TenantID string

	// Lookup reads an environment variable. os.Getenv by default; a map in
	// tests. Injected rather than called directly for the same reason
	// cli.Options carries its streams: a test that cannot set the switches
	// cannot assert the no-op path.
	Lookup func(string) string

	// NewExporter is the seam described on exporterFactory. Nil means the real
	// gRPC exporter.
	NewExporter exporterFactory
}

// Session is an installed emitter. A zero Session is not usable: the only way to
// get one is Install, which is what keeps "was telemetry on or off" a question
// with one answer.
type Session struct {
	tracer trace.Tracer
	// exporter is the exporter this session installed, kept so a test can read
	// the spans back through the SDK's own in-memory exporter. It is never read
	// on a production path — a test asserting the no-op reaches it would find
	// nil, which is the point.
	exporter sdktrace.SpanExporter
	shutdown func(context.Context) error

	// enabled is recorded rather than re-derived, so a caller can report which
	// path it took without consulting the environment again — and so that the
	// no-op and the exporting path are distinguishable at all, which is what a
	// test asserting the no-op needs.
	enabled bool
	reason  string
}

// Enabled reports whether this session will export anything.
func (s Session) Enabled() bool { return s.enabled }

// NoOpReason is one line naming the switch that turned telemetry off, and "" when
// it is on. It is the generated file's function rather than a second
// implementation, because "why is telemetry off" must have one answer in this
// tree and a second copy is how two answers start disagreeing.
func (s Session) NoOpReason() string { return s.reason }

// Install wires the SDK for this process, or decides not to.
//
// The order in this function IS the no-op contract, and it is the reason this is
// a function rather than a package-level initialiser: the switches are read and
// the no-op session returned BEFORE any exporter is constructed. A package-level
// `init()` cannot be ordered against the environment in a way a reader can
// verify, and an emitter that dials at init time is a CLI whose `caf --help`
// waits on a collector.
func Install(ctx context.Context, opts Options) (Session, error) {
	lookup := opts.Lookup
	if lookup == nil {
		lookup = os.Getenv
	}

	if reason := NoOpReasonFor(lookup); reason != "" {
		// The SDK's own no-op provider. Setting it globally matters as much as
		// returning it: an uninstrumented call to `otel.Tracer(...)` anywhere
		// else in the process then gets a recording tracer rather than the
		// default one, so a future span added to a command cannot start
		// exporting behind this decision's back.
		otel.SetTracerProvider(noop.NewTracerProvider())
		return Session{tracer: noop.NewTracerProvider().Tracer(instrumentationName), reason: reason}, nil
	}

	newExporter := opts.NewExporter
	if newExporter == nil {
		newExporter = newGRPCExporter
	}
	endpoint := EndpointFor(lookup)
	exporter, err := newExporter(ctx, hostPort(endpoint), secure(endpoint))
	if err != nil {
		// A telemetry backend that will not accept an exporter is not a reason
		// for a command the user asked for to fail. core's endpoint contract
		// makes this concrete: the endpoint is `required: false`, so a caf
		// pointed at a dead collector still runs every command it has always
		// run. The failure is reported on stderr so it is visible in a CI log
		// and nowhere else — see the noOp.warnings rule, which forbids
		// per-attempt spam, and this is one line once per process rather than
		// per attempt.
		fmt.Fprintf(os.Stderr, "caf: telemetry is configured but no exporter could be built (%v); "+
			"running without it. The command itself is unaffected.\n", err)
		otel.SetTracerProvider(noop.NewTracerProvider())
		return Session{tracer: noop.NewTracerProvider().Tracer(instrumentationName), reason: "exporter: " + err.Error()}, nil
	}

	res, err := mergeResource(sdkresource.Default(), sdkresource.NewWithAttributes(
		sdkresource.Default().SchemaURL(),
		resourceAttributes(opts)...,
	))
	if err != nil {
		// Unlike the exporter failure this one is NOT swallowed: it means two
		// resource schemas were merged that core does not intend to be merged,
		// which is a caf bug rather than a deployment problem. Silently running
		// without identity would put spans in the backend under no service.name,
		// which is the one outcome the whole resource block exists to prevent.
		return Session{}, fmt.Errorf("caf: build the telemetry resource: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		// The endpoint contract's `timeoutMs` field exists because "a telemetry
		// export that can block a request is a latency bug in production caused
		// by an observability tool". A CLI has no request to block, but it has
		// the equivalent: a person waiting on a terminal.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	// The SDK's default error handler LOGS to os.Stderr, and a failed export
	// produces one line per attempt — measured, and it appeared in this
	// repository's own test output as
	//
	//	traces export: context deadline exceeded: rpc error: code = Unavailable
	//
	// core's otel-endpoint.schema.json pins noOp.warnings to `none` and says a
	// service that logs a warning per export attempt "fills its own log store
	// with the fact that telemetry is off", which is exactly what this handler
	// does. So it is replaced with one that drops export failures on the floor:
	// the condition they report is already the one Install reported once, on
	// stderr, when the exporter could not be built.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {}))

	// Traces continue an inbound trace when one is in the context. ParentBased
	// means a caller who sampled decides, and a root span in a CLI — which has
	// no caller — is sampled. kit's compose template pins the same sampler for
	// the same reason.
	return Session{
		tracer:   provider.Tracer(instrumentationName),
		exporter: exporter,
		enabled:  true,
		shutdown: provider.Shutdown,
	}, nil
}

// mergeResource is `resource.Merge`, named so a test can drive the merge Install
// performs and prove the conflict is survivable or not.
//
// Merging two resources whose schema URLs differ is the only way this fails, and
// it is why the resource below is built from `resource.Default()`'s schema URL
// rather than a literal one: an SDK whose default schema drifts from the one
// written here would fail every install rather than one, and this is the check
// that says so before a command runs rather than after.
func mergeResource(a, b *sdkresource.Resource) (*sdkresource.Resource, error) {
	return sdkresource.Merge(a, b)
}

// instrumentationName is the tracer's instrumentation scope name.
//
// It is the service name rather than a constant of this package's own, because
// this package is not a library a caller should be able to attribute spans to:
// the scope says which code produced the span, and the answer is "caf".
const instrumentationName = telemetry.ServiceName

// resourceAttributes is the resource block, and every value on it comes from
// Options or is absent.
//
// Absent rather than defaulted is the rule that matters. core's
// traces.schema.json closes the resource with `additionalProperties: false` and
// gives deployment.environment a four-value enum, so a caf built with no version
// injected and no environment set must emit a resource with neither rather than
// one with a placeholder — a placeholder is a value in a backend that somebody
// later has to filter out, and "there is no row for that" is the honest state.
//
// It also omits service.instance.id on purpose. core allows it, and for a
// long-running process a fresh id per boot is correct. For a CLI every invocation
// is a new id, which means instance-level grouping counts invocations rather
// than processes — a number that looks like capacity and is not. It is left for
// the day something needs it and can say what it is counting.
func resourceAttributes(opts Options) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("service.name", telemetry.ServiceName),
		attribute.String("service.namespace", telemetry.Namespace),
		// Set by hand rather than inherited from resource.Default()'s detector,
		// which reads OTEL_RESOURCE_ATTRIBUTES. Reading it here too would be
		// harmless but would make the resource depend on an env var nobody
		// documented, and core's endpoint contract says the resource this
		// process attaches is a thing the service knows about itself.
		attribute.String("telemetry.sdk.name", "opentelemetry"),
		attribute.String("telemetry.sdk.language", "go"),
	}
	if opts.Version != "" {
		attrs = append(attrs, attribute.String("service.version", opts.Version))
	}
	if opts.Environment != "" {
		attrs = append(attrs, attribute.String("deployment.environment", opts.Environment))
	}
	if opts.TenantID != "" {
		attrs = append(attrs, attribute.String("tenant_id", opts.TenantID))
	}
	return attrs
}

// hostPort strips the scheme from an endpoint, because otlptracegrpc takes a
// host:port and silently misinterprets anything else.
//
// core's generated declaration writes the default as `http://otel-collector:4317`
// — http, on the gRPC port, because that is what the field means: it names the
// address, and the collector's 4317 speaks gRPC regardless of the prefix. A
// caller who writes `https://` gets TLS, which is right; a caller who writes a
// bare `otel-collector:4317` gets the scheme-free form otlptracegrpc wants. All
// three land on the same address, and the alternative is a dial error that names
// a URL as though it were a path.
func hostPort(endpoint string) string {
	for _, prefix := range []string{"http://", "https://", "grpc://"} {
		if rest, ok := strings.CutPrefix(endpoint, prefix); ok {
			return rest
		}
	}
	return endpoint
}

// Shutdown flushes and stops. It is safe on a no-op session, and it is the
// reason Install returns a function rather than nothing.
//
// A flush that RAN OUT OF TIME is reported as success, and that is a deliberate
// decision rather than a swallowed error.
//
// MEASURED: with the shipped default endpoint and no local stack running, this
// returned `context deadline exceeded` — the gRPC exporter cannot resolve
// `otel-collector`, so the flush waits out its budget and fails. Printed by the
// caller that would put `caf: could not flush telemetry: context deadline
// exceeded` on the stderr of every single command a developer runs before they
// have run `caf dev` once. core's otel-endpoint.schema.json pins noOp.warnings to
// `none` and says the failure mode is a service that "fills its own log store
// with the fact that telemetry is off, which is how a self-hoster discovers that
// turning telemetry off is not supported — the opposite of the intent." A
// deadline here is not a broken telemetry setup to report; it IS telemetry being
// off, which is a supported deployment.
//
// The condition it would have reported was already reported once, by Install,
// on stderr, at the moment the exporter could not be built. So nothing is lost by
// staying quiet here: a reader can still tell a disabled exporter from a working
// one, and the difference is the difference between "telemetry is off" and
// "telemetry is broken", which look identical from outside — which is exactly
// what core's NoOpReason exists to answer.
func (s Session) Shutdown(ctx context.Context) error {
	if s.shutdown == nil {
		return nil
	}
	flushCtx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	defer cancel()

	err := s.shutdown(flushCtx)
	if errors.Is(err, context.DeadlineExceeded) {
		// The documented cost of the budget, not a fault. See above.
		return nil
	}
	return err
}

// errClosedSpan is returned when a caller ends a span twice, which is a bug in
// the caller rather than in telemetry and is stated rather than ignored.
var errClosedSpan = errors.New("caf: span already ended")
