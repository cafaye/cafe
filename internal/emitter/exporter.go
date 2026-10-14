package emitter

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc/credentials"

	"github.com/cafaye/cafe/internal/telemetry"
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
// nothing, and fails nowhere. With it, `cafe --help` on a machine with no
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
	// TLS or not, decided by the SCHEME the endpoint was written with, and passed
	// EXPLICITLY IN BOTH DIRECTIONS.
	//
	// The second half is the fix, and it is a precedence leak rather than a
	// missing feature. `otlptracegrpc` builds its config as: defaults, then
	// `ApplyGRPCEnvConfigs`, then the caller's options. So a caller's options win
	// — which is why the ADDRESS half of the documented precedence already held
	// and `WithEndpoint` is enough for it.
	//
	// The SCHEME half did not, because `newGRPCExporter` used to append an option
	// only when it wanted plaintext. With no option appended, the config keeps
	// whatever `ApplyGRPCEnvConfigs` decided, and that function calls
	// `withEndpointScheme(u)` on the value of `OTEL_EXPORTER_OTLP_ENDPOINT` —
	// the standard variable the cafaye one is documented to beat. MEASURED, over
	// the wire, on a plain socket reading the first bytes a dial produces:
	//
	//	CAFE_OTEL_ENDPOINT=https://127.0.0.1:PORT     (cafe's own answer: TLS)
	//	OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:PORT
	//	-> first bytes on the wire: 50 52 49 20 2a 20 48 54 54 50 2f 32 2e
	//	   which is "PRI * HTTP/2.0" — the plaintext h2 client preface.
	//
	// So the standard variable won the scheme while losing the address, and a
	// self-hoster who set `OTEL_EXPORTER_OTLP_ENDPOINT=http://…` next to their own
	// `https://` endpoint silently downgraded the connection to plaintext. The
	// converse — a cafaye `http://` beside a standard `https://` — was already
	// right, and stayed right, because that direction appends an option.
	//
	// `WithTLSCredentials` is how the secure case is stated rather than assumed.
	// `otlptracegrpc` has no `WithSecure`, and its own config resolves
	// `GRPCCredentials` BEFORE `Insecure` ("Prioritize GRPCCredentials over
	// Insecure"), so naming credentials makes the choice survive whatever the
	// environment said. `credentials.NewTLS(nil)` is the host's root CA pool,
	// which is exactly what the secure default branch builds — so this changes
	// which input decides, and not what is dialed.
	//
	// The scheme is the last thing on the endpoint string and therefore the only
	// thing a reader has to look at twice, so it is worth stating that it is
	// carried through explicitly rather than left to an option that is present
	// in one direction and absent in the other.
	//
	// The plaintext half of that is not a guess either, and this was a real bug
	// before this packet. The
	// gRPC exporter defaults to TLS, and core's shipped default is
	// `http://otel-collector:4317` — plaintext gRPC on the collector's 4317.
	// Without this, `cafe` opened NO connection at all: `lsof` against a real
	// OTLP receiver listening on the configured port showed the listener and
	// nothing else, on every command, forever. The exporter's TLS handshake
	// could not complete against a plaintext collector, the failure was dropped
	// by the error handler installed for noOp.warnings, and the observable
	// result was a cafe that exported nothing and said nothing about it.
	//
	// So the scheme is not decoration on core's default — it is the only thing
	// that says whether the bytes are encrypted, and a reader who strips it
	// before handing it over loses it. `hostPort` therefore does not strip it
	// destructively: `schemeOf` reads the scheme and `hostPort` removes it,
	// and the two are called at the same site so they cannot disagree.
	if secure {
		opts = append(opts, otlptracegrpc.WithTLSCredentials(credentials.NewTLS(nil)))
	} else {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	return otlptracegrpc.New(ctx, opts...)
}

// secure reports whether an endpoint wants TLS, and it is the generated
// `telemetry.Secure` rather than a second implementation of it.
//
// The three rules that answer are core's and are now in the generated file, which
// is where a service that adopts `cafe gen telemetry` will read them: only `https`
// encrypts; a bare host:port is plaintext, because an operator who writes no
// scheme is saying "the address" and not "the encryption"; and the comparison is
// case-folded, because a person typing `HTTPS://` means what they mean by
// `https://`.
//
// Getting it wrong in either direction is the same silent failure: TLS at a
// plaintext collector exports nothing and reports nothing, and so does plaintext
// at a TLS one.
func secure(endpoint string) bool {
	return telemetry.Secure(endpoint)
}

// exportTimeout bounds one OTLP export.
const exportTimeout = 2 * time.Second

// The batch processor is the SDK default rather than an explicit choice here,
// and kit's pins.md says why in one line worth repeating: "Batch processors,
// never simple ones, in any production path. One export per span makes the
// collector the bottleneck and drops spans on a slow network." A cafe run
// produces exactly one span, so the batch never fills — and Shutdown flushes it
// on the way out, which is the property the exporter would otherwise lack.
