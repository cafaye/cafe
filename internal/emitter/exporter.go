package emitter

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// newGRPCExporter is the one real exporter.
//
// otlptracegrpc rather than otlptracehttp because the shipped collector's 4317
// is gRPC, and that is the default in core's own endpoint contract
// (`http://otel-collector:4317`). The endpoint arriving here has already had its
// scheme stripped by hostPort, which is what this exporter's WithEndpoint wants.
//
// `WithDialOption(WithBlock())` is deliberately ABSENT, and this is the single
// most consequential line in the package. Without it the gRPC dial is
// asynchronous: constructing an exporter starts no connection, blocks on
// nothing, and fails nowhere. With it, `caf --help` on a machine with no
// collector — which is every machine that has not started the stack — would wait
// out a context timeout before printing anything, and would do so in the one
// place core's no-op block says must cost nothing ("startupCost: none. No dial
// on boot"). A telemetry backend that makes the CLI slow to start is a
// telemetry backend that gets turned off.
//
// `WithTimeout` is the export timeout, and it is short for the same reason as
// shutdownTimeout: a person is watching a terminal. core's endpoint schema has
// `timeoutMs` for precisely this — "a telemetry export that can block a request
// is a latency bug in production caused by an observability tool" — and a CLI
// has no request to block, only a person's patience.
func newGRPCExporter(ctx context.Context, endpoint string) (sdktrace.SpanExporter, error) {
	return otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithTimeout(exportTimeout),
	)
}

// exportTimeout bounds one OTLP export.
const exportTimeout = 2 * time.Second

// The batch processor is the SDK default rather than an explicit choice here,
// and kit's pins.md says why in one line worth repeating: "Batch processors,
// never simple ones, in any production path. One export per span makes the
// collector the bottleneck and drops spans on a slow network." A caf run
// produces exactly one span, so the batch never fills — and Shutdown flushes it
// on the way out, which is the property the exporter would otherwise lack.
