package emitter

import (
	"context"
	"strings"
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
func newGRPCExporter(ctx context.Context, endpoint string, secure bool) (sdktrace.SpanExporter, error) {
	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithTimeout(exportTimeout),
	}
	// TLS or not, decided by the SCHEME the endpoint was written with.
	//
	// MEASURED, and this was a real bug rather than a missing feature. The
	// gRPC exporter defaults to TLS, and core's shipped default is
	// `http://otel-collector:4317` — plaintext gRPC on the collector's 4317.
	// Without this, `caf` opened NO connection at all: `lsof` against a real
	// OTLP receiver listening on the configured port showed the listener and
	// nothing else, on every command, forever. The exporter's TLS handshake
	// could not complete against a plaintext collector, the failure was dropped
	// by the error handler installed for noOp.warnings, and the observable
	// result was a caf that exported nothing and said nothing about it.
	//
	// So the scheme is not decoration on core's default — it is the only thing
	// that says whether the bytes are encrypted, and a reader who strips it
	// before handing it over loses it. `hostPort` therefore does not strip it
	// destructively: `schemeOf` reads the scheme and `hostPort` removes it,
	// and the two are called at the same site so they cannot disagree.
	if !secure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	return otlptracegrpc.New(ctx, opts...)
}

// secure reports whether an endpoint wants TLS.
//
// Only `https` does. `http`, `grpc` and a bare host:port are plaintext, and the
// bare form has to be plaintext because it is what a self-hoster writes when the
// collector is on the same host with no scheme in front of it — and an operator
// who writes no scheme is saying "the address", not " the encryption".
//
// Case-folded, because a URL scheme is case-insensitive and a person typing
// `HTTPS://` means exactly what they mean by `https://`. Getting that wrong in the
// other direction is the silent failure: TLS on to a plaintext collector exports
// nothing and reports nothing.
func secure(endpoint string) bool {
	return strings.HasPrefix(strings.ToLower(endpoint), "https://")
}

// exportTimeout bounds one OTLP export.
const exportTimeout = 2 * time.Second

// The batch processor is the SDK default rather than an explicit choice here,
// and kit's pins.md says why in one line worth repeating: "Batch processors,
// never simple ones, in any production path. One export per span makes the
// collector the bottleneck and drops spans on a slow network." A caf run
// produces exactly one span, so the batch never fills — and Shutdown flushes it
// on the way out, which is the property the exporter would otherwise lack.
