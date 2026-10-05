package emitter

import (
	"strings"

	"github.com/cafaye/caf/internal/telemetry"
)

// This file is the seam between caf's own wiring and the generated contract.
//
// The generated package reads `os.Getenv` directly, which is correct for a
// service whose process IS the deployment and wrong for a library a caller has
// to configure: a test cannot set an environment variable without mutating the
// whole process, and two tests that each set one leak into each other.
//
// So the two switch-reading functions this package needs are re-expressed here
// over an injected `lookup`, and the generated ones are not called. The rule is
// NOT "read the env twice, in two places" — that is how two definitions of
// "disabled" start disagreeing, and core's otel-endpoint.schema.json says
// re-implementing one word in six languages is how six services acquire six
// definitions of it. The rule is that the WORD comes from the generated package
// and only the MECHANISM of reading is here: the switch names and the comparison
// are `telemetry.SwitchSDKDisabled` and friends, so core moves the contract and
// these two functions follow without being edited.

// EndpointFor is the generated Endpoint(), over an injected lookup.
//
// The precedence is unchanged and is core's: `CAF_OTEL_ENDPOINT` wins, the
// standard `OTEL_EXPORTER_OTLP_ENDPOINT` is the fallback so caf works with
// generic OTel tooling, and the shipped collector is the default value of the
// first.
//
// The `CAF_` prefix is read from the generated `EndpointVariable` rather than
// written here, because that constant is derived from the manifest by the same
// code that derived it before and a second derivation is a second thing to keep
// in step — which is exactly what the drift gate was measuring courier against
// courier before the previous commit.
func EndpointFor(lookup func(string) string) string {
	if endpoint := lookup(telemetry.EndpointVariable); endpoint != "" {
		return endpoint
	}
	if endpoint := lookup(otlpEndpointVariable); endpoint != "" {
		return endpoint
	}
	return telemetry.DefaultEndpoint
}

// otlpEndpointVariable is the OTel specification's own variable, which every
// SDK reads and which is therefore the fallback that makes caf work with
// tooling that knows nothing about cafaye.
const otlpEndpointVariable = "OTEL_EXPORTER_OTLP_ENDPOINT"

// NoOpReasonFor is the generated NoOpReason(), over an injected lookup, and "" means telemetry is on.
//
// The three cases are the generated package's three cases, in its order, for the
// same reasons:
//
//   - OTEL_SDK_DISABLED=true is the global switch and wins over everything,
//     because core's endpoint contract names it as the mechanism the no-op path
//     is implemented with rather than a cafaye reimplementation of "disabled".
//
//   - all three per-signal exporters set to `none` is the finer-grained half, and
//     naming all three is the point: "a no-op that only covers traces is a
//     service that still phones home for metrics — the failure discovered by a
//     customer's invoice rather than by a test."
//
// Case-insensitively, because these are read by six SDKs and typed by a person
// at a terminal, and `TRUE` is a misspelling that should mean off.
//
// Note what is NOT a reason here: an endpoint that is not set. Unset is the
// DEFAULT state — core's endpoint declaration is `required: false` with the
// collector as the default value, which is what makes observability on by
// default without being mandatory. Telemetry being on with nowhere configured is
// the shipped behaviour, not a misconfiguration, and returning a reason for it
// would train an operator to read a line that means nothing.
func NoOpReasonFor(lookup func(string) string) string {
	if switchedOff(lookup, telemetry.SwitchSDKDisabled) {
		return telemetry.SwitchSDKDisabled + "=true"
	}
	everySignalOff := true
	for _, signal := range []string{telemetry.SignalTraces, telemetry.SignalMetrics, telemetry.SignalLogs} {
		if lookup(exportersFor(signal)) != "none" {
			everySignalOff = false
			break
		}
	}
	if everySignalOff {
		return "every " + strings.Join([]string{
			telemetry.SwitchTracesExporter,
			telemetry.SwitchMetricsExporter,
			telemetry.SwitchLogsExporter,
		}, ", ") + " is none"
	}
	return ""
}

// exportersFor is the switch that turns one signal off.
//
// The generated package keeps a `SwitchFor(signal string) string` helper in its
// test file rather than its source, which means a caller cannot use it — so this
// is the mapping, and it is exhaustive over the three signals core names. An
// unknown signal maps to an empty name, which matches nothing, which means an
// unknown signal cannot accidentally be read as switched off.
func exportersFor(signal string) string {
	switch signal {
	case telemetry.SignalTraces:
		return telemetry.SwitchTracesExporter
	case telemetry.SignalMetrics:
		return telemetry.SwitchMetricsExporter
	case telemetry.SignalLogs:
		return telemetry.SwitchLogsExporter
	}
	return ""
}

// switchedOff is the case-insensitive "true" test for the global switch.
func switchedOff(lookup func(string) string, name string) bool {
	return strings.EqualFold(lookup(name), "true")
}
