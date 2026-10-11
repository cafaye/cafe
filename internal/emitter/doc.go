// Package emitter wires the OpenTelemetry SDK to one cafe invocation, and is
// what makes a cafe run visible in HyperDX under service.name=cafe.
//
// # Why cafe emits at all
//
// core's contract does not say "HTTP services". Three things in it imply a CLI
// is in scope, and the implication is the whole justification for this package:
//
//   - span-naming.schema.json's `kind` enum is
//     internal/server/client/producer/consumer. `internal` is OpenTelemetry's
//     kind for "an operation internal to an application", which is the only kind
//     a one-shot command line invocation honestly is. There is no server span
//     without a server, and pretending otherwise would mean inventing a span
//     shape core does not have a vocabulary for.
//
//   - traces.schema.json's `resource` block is identity and deployment —
//     service.name, service.version, service.namespace, deployment.environment,
//     tenant_id — and every one of those is a fact about a process rather than
//     about a request. A CLI has a process. The resource block was written for
//     exactly this and a CLI simply fills in more of it.
//
//   - otel-endpoint.schema.json's noOp block pins `startupCost` to `none` and
//     says "No dial on boot. A service that opens a connection before anyone
//     has configured a collector is a service whose availability now depends on
//     a component that ships with the product". For a binary whose whole
//     lifetime is one command, boot is every invocation, so that sentence is
//     stricter here than it is anywhere else in the fleet — and it is the reason
//     Install refuses to construct an exporter before it has read the switches.
//
// # What this package deliberately does not emit
//
// **No metrics and no logs.** core's otel-endpoint.schema.json lists three
// signals and this emits the first. A cafe invocation is a span with a duration
// and an outcome; a CLI counter would be a number that exists once and is
// aggregated by whoever runs cafe, and a CLI log line is the terminal the user is
// already reading. Emitting them would mean two more processors, two more
// shutdown paths, and two more failure modes in the one code path that has to
// not be the reason a command fails.
//
// **No attributes on the invocation span.** core's trace allowlist is closed and
// `additionalProperties: false`, and it contains no attribute for a command name,
// an argument, a project directory or an exit code — because each of those is
// either unbounded or caller text. So the invocation span carries no attributes
// at all on the success path, and on the failure path only `error.type` and the
// `otel.status_code` mirror, both drawn from core's closed vocabulary. The
// command being run lives in the SPAN NAME, where core's grammar puts the
// operation and where a 15-character-per-segment bound makes it low cardinality
// by construction.
//
// **No child spans around the work.** A root span that contains no children is
// what a span is when the work is one opaque call into a package. Child spans
// would mean instrumenting `dev`, `deploy` and `backup` individually, which is a
// per-command decision each of those commands should make when it has something
// worth showing; guessing at it here would put spans in the trace that say
// nothing.
//
// # The no-op
//
// Unset endpoint, or `OTEL_SDK_DISABLED=true`, or `OTEL_TRACES_EXPORTER=none`,
// and Install returns a session backed by the SDK's own no-op tracer provider
// without constructing an exporter, opening a socket or starting a processor.
// That is asserted in this package's tests through an injected factory rather
// than by reading the code, because the property is about what was NOT called.
package emitter
