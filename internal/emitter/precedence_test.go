package emitter

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/cafaye/cafe/internal/telemetry"
)

// The endpoint precedence, asserted over the WIRE rather than over a function.
//
// WHY IT IS NOT `EndpointFor`. There was already a table test for
// `EndpointFor` — `TestTheEndpointPrecedence`, in span_test.go — and it was green
// with a real precedence defect in this package, because `EndpointFor` is not
// where the endpoint is decided. `otlptracegrpc` resolves its own configuration
// in this order: built-in defaults, then `ApplyGRPCEnvConfigs`, which reads
// `OTEL_EXPORTER_OTLP_ENDPOINT` from the PROCESS environment, and then the
// caller's options. A caller that passes its address explicitly wins the address.
// A caller that passes nothing about the scheme loses it.
//
// That asymmetry is invisible from inside `EndpointFor`, and it is why these
// tests set the variables with `t.Setenv` rather than through `Options.Lookup`.
// An injected lookup cannot reach the SDK, which reads `os.Getenv` itself. The
// first version of this file injected a map, passed both addresses through
// `Lookup`, and reported the precedence holding — because the SDK had never seen
// the standard variable at all.
//
// It also means the test observes the connection rather than the config. What is
// on the wire after a dial is either the HTTP/2 client preface or a TLS record,
// and there is no third answer. That distinction is the whole assertion: a
// service that exports in the clear to an https endpoint produces one of exactly
// two byte sequences, and no configuration error anywhere.

// h2ClientPreface is what a plaintext HTTP/2 client sends before anything else.
// Comparing against it is what makes "dialed in the clear" a fact rather than an
// inference from an absence.
var h2ClientPreface = []byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")

// dialed says whether the bytes a dial produced were plaintext.
func dialed(first []byte) bool { return bytes.HasPrefix(first, h2ClientPreface) }

// The four cases the documented precedence distinguishes, plus the one that was
// wrong.
//
// `Endpoint()`'s own comment says the cafaye variable "wins over the standard
// `OTEL_EXPORTER_OTLP_ENDPOINT`". courier's worker measured the opposite and put
// it in `test/courier/otel_export_test.exs`. This file is the same measurement
// for cafe, and it found the ADDRESS half already correct and the SCHEME half not.
func TestTheEndpointPrecedenceHoldsOnTheWire(t *testing.T) {
	for _, row := range []struct {
		name string
		// values builds each variable's setting from the two loopback addresses,
		// so one row can point the two variables at DIFFERENT sockets — which is
		// the only way "the cafaye one was dialed" is a statement about anything.
		values func(mine, theirs string) (cafaye, standard string)
		// winner is the socket the documented precedence says was dialed.
		winner string
		// wantPlaintext is whether cafe must dial WITHOUT TLS. It is an answer, not
		// a restatement of the input: `only-standard` and `both-conflict` differ in
		// spelling and must not differ in expectation.
		wantPlaintext bool
		why           string
	}{
		{
			name: "only the standard variable, and it is what is used",
			values: func(_, theirs string) (string, string) {
				return "", "http://" + theirs
			},
			winner:        "the standard variable",
			wantPlaintext: true,
			why: "the fallback exists so cafe works with generic OTel tooling, and a fallback nothing " +
				"ever reaches is not a fallback",
		},
		{
			name: "only the cafaye variable, and it is what is used",
			values: func(mine, _ string) (string, string) {
				return "http://" + mine, ""
			},
			winner:        "the cafaye variable",
			wantPlaintext: true,
			why:           "the whole contract is this one variable",
		},
		{
			name: "both set and agreeing on one address",
			values: func(mine, _ string) (string, string) {
				address := "http://" + mine
				return address, address
			},
			winner:        "the cafaye variable",
			wantPlaintext: true,
			why: "agreement has no winner, and a resolution that complained about the conflict would " +
				"make an ordinary configuration an error state it does not need to be",
		},
		{
			name: "both set and conflicting on the address: the cafaye one wins",
			values: func(mine, theirs string) (string, string) {
				return "http://" + mine, "https://" + theirs
			},
			winner:        "the cafaye variable",
			wantPlaintext: true,
			why: "the documented rule, and the one courier measured going the other way. This half " +
				"already worked — a caller's options are applied after the SDK's own environment read — " +
				"and it is asserted because a fix for the scheme must not break it",
		},
		{
			name: "both set and conflicting on the scheme: the cafaye one wins",
			values: func(mine, theirs string) (string, string) {
				return "https://" + mine, "http://" + theirs
			},
			winner:        "the cafaye variable",
			wantPlaintext: false,
			why: "THIS ONE WAS WRONG, and only in this direction. newGRPCExporter used to append an " +
				"option only when it wanted plaintext, so with an https cafaye value the scheme was " +
				"left to whatever ApplyGRPCEnvConfigs read out of the standard variable — and a " +
				"self-hoster's http:// standard value downgraded their own https:// endpoint to " +
				"plaintext. MEASURED before the fix, the first bytes on the wire were " +
				"50 52 49 20 2a 20 48 54 54 50 2f 32 2e, the HTTP/2 client preface; after it, " +
				"16 03 01, a TLS record",
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			mine := startDialRecorder(t)
			theirs := startDialRecorder(t)

			// t.Setenv, not Options.Lookup: the SDK reads the process environment
			// itself and an injected map never reaches it. That is the whole reason
			// the first version of this test passed against a broken exporter.
			cafaye, standard := row.values(mine.addr, theirs.addr)
			t.Setenv(telemetry.EndpointVariable, cafaye)
			t.Setenv(otlpEndpointVariable, standard)
			clearOTELSwitches(t)

			sess, err := Install(context.Background(), Options{})
			if err != nil {
				t.Fatalf("Install: %v", err)
			}
			emitAndFlush(t, sess)

			// The address. Exactly one of the two sockets was dialed, so both are
			// checked and "neither" is distinguished from "both".
			mineDialed := mine.dialed(t)
			theirsDialed := theirs.dialed(t)
			if mineDialed == theirsDialed {
				t.Fatalf("cafaye=%q dialed=%t and standard=%q dialed=%t: exactly one must be true",
					cafaye, mineDialed, standard, theirsDialed)
			}
			wantMine := row.winner == "the cafaye variable"
			if mineDialed != wantMine {
				t.Errorf("%s was dialed; the documented precedence says %s was. %s",
					dialledName(mineDialed), row.winner, row.why)
			}

			// The scheme, which is the half that was wrong.
			recorder := theirs
			if wantMine {
				recorder = mine
			}
			first := recorder.firstBytes(t)
			if got := dialed(first); got != row.wantPlaintext {
				t.Errorf("dialed in the clear = %t, want %t. First bytes on the wire: % x. %s",
					got, row.wantPlaintext, first, row.why)
			}
		})
	}
}

func dialledName(dialed bool) string {
	if dialed {
		return "the cafaye endpoint"
	}
	return "the standard endpoint"
}

// clearOTELSwitches unsets every switch this package knows about, so a row is
// decided by the endpoint variables and not by whatever the machine running it had
// set. An unset variable reads as unset to the SDK as well as to `NoOpReasonFor`,
// because both treat "" as absent.
func clearOTELSwitches(t *testing.T) {
	t.Helper()
	t.Setenv(telemetry.SwitchSDKDisabled, "")
	for _, signal := range []string{
		telemetry.SwitchTracesExporter,
		telemetry.SwitchMetricsExporter,
		telemetry.SwitchLogsExporter,
	} {
		t.Setenv(signal, "")
	}
}

// emitAndFlush records one span and flushes, which is the whole of what a cafe
// invocation does with the exporter.
func emitAndFlush(t *testing.T, sess Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, span := sess.tracer.Start(ctx, "cafe.precedence")
	span.End()
	if err := sess.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// startDialRecorder is a plain TCP listener that reads the first bytes of every
// connection and reports them.
//
// It is deliberately NOT a gRPC server. This test is about which socket was
// dialed and in what transport, and a gRPC server would have to complete a
// handshake to say anything — which is precisely what a wrong-scheme exporter
// cannot do. The bytes are the evidence.
type dialRecorder struct {
	addr  string
	first chan []byte
	// seen is what dialed kept, so the ADDRESS and the SCHEME are answered from
	// the same dial rather than from two.
	seen []byte
}

func startDialRecorder(t *testing.T) *dialRecorder {
	t.Helper()
	listener, err := new(net.ListenConfig).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind a loopback port: %v", err)
	}
	recorder := &dialRecorder{addr: listener.Addr().String(), first: make(chan []byte, 4)}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, len(h2ClientPreface))
			n, _ := conn.Read(buf)
			recorder.first <- buf[:n]
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return recorder
}

// dialed reports whether this socket was dialed, and keeps the bytes so
// firstBytes can still answer for it afterwards. Consuming them would mean the
// scheme assertion silently read an empty slice, and `bytes.HasPrefix` on an
// empty slice is false — which is to say it would read "TLS" for a socket that
// dialed in the clear.
func (d *dialRecorder) dialed(t *testing.T) bool {
	t.Helper()
	select {
	case b := <-d.first:
		d.seen = b
		return true
	case <-time.After(750 * time.Millisecond):
		return false
	}
}

// firstBytes is what the dial produced, for a socket already known to have been
// dialed.
func (d dialRecorder) firstBytes(t *testing.T) []byte {
	t.Helper()
	if d.seen == nil {
		t.Fatalf("no bytes were read from a socket the address assertion says was dialed. " +
			"This is the failure mode of a recorder that consumed them: an empty read is not a " +
			"plaintext dial, and treating it as one would fail this for the wrong reason")
	}
	return d.seen
}
