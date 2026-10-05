package emitter

import (
	"context"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// The measurement that chose shutdownTimeout, kept as a test because the number
// is the argument and an argument that is only in a comment rots.
//
// It is a measurement, not a bound: the assertion is that the no-op path costs
// essentially nothing and the exporting path costs about the shutdown budget and
// no more. A test that asserted an exact millisecond count would fail on a
// loaded machine and teach people to raise the number, which is the same failure
// `internal/cli/mcp_wire_test.go` records for a 250ms budget.
func TestTheFlushIsBoundedAndTheNoOpIsFree(t *testing.T) {
	// No-op: no exporter, so nothing to flush.
	noOp, err := Install(context.Background(), Options{
		Lookup: func(string) string { return "true" },
		NewExporter: func(context.Context, string, bool) (sdktrace.SpanExporter, error) {
			t.Error("the exporter factory was reached on the no-op path")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	start := time.Now()
	_, end := noOp.StartInvocation(context.Background(), []string{"version"})
	if err := end(OutcomeSuccess); err != nil {
		t.Fatalf("end: %v", err)
	}
	if err := noOp.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	noOpElapsed := time.Since(start)
	if noOpElapsed > 50*time.Millisecond {
		t.Errorf("a no-op invocation took %v; core's endpoint contract pins noOp.startupCost "+
			"to `none`, so the disabled path must cost nothing anyone can feel", noOpElapsed)
	}

	// Exporting, pointed at an address nothing is listening on: the cost is the
	// shutdown budget and not one millisecond more, because the whole point of
	// the budget is that the terminal is not held while a collector is absent.
	session, err := Install(context.Background(), Options{
		Lookup: func(name string) string { return "127.0.0.1:1" },
		NewExporter: func(context.Context, string, bool) (sdktrace.SpanExporter, error) {
			return newCapturing(), nil
		},
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	start = time.Now()
	_, end = session.StartInvocation(context.Background(), []string{"version"})
	if err := end(OutcomeSuccess); err != nil {
		t.Fatalf("end: %v", err)
	}
	if err := session.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 2*shutdownTimeout {
		t.Errorf("an exporting invocation with an unreachable endpoint took %v, more than "+
			"twice the %v shutdown budget — something is waiting on something other than the flush",
			elapsed, shutdownTimeout)
	}
	t.Logf("the no-op path cost %v; the exporting path with an unresolvable collector cost %v "+
		"(shutdown budget %v)", noOpElapsed, elapsed, shutdownTimeout)
}
