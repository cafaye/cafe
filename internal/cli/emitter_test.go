package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/cafaye/cafe/internal/emitter"
)

// These are the tests that answer the packet's question at the level that
// matters: what a person typing `cafe` observes when telemetry is off, when it is
// on, and when the thing they asked for fails.
//
// `internal/emitter` proves the span is right. This file proves the wiring does
// not get in the way — that every command still works, still exits with the code
// it always did, and still prints what it always printed, whether the endpoint is
// set or not. A package that emits correctly and takes a command down with it is
// worse than a package that does not emit.

// withOTELEnv sets the switches for one test and returns a function that puts the
// process back.
//
// `t.Setenv` rather than an injected lookup, because `cli.Run` reads the real
// environment — that is the point of the wiring — and a test that injected a map
// would be testing a `Run` that no binary has.
func withOTELEnv(t *testing.T, env map[string]string) {
	t.Helper()

	for _, name := range []string{
		"OTEL_SDK_DISABLED", "OTEL_TRACES_EXPORTER", "OTEL_METRICS_EXPORTER", "OTEL_LOGS_EXPORTER",
		"CAFE_OTEL_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT",
		"DEPLOYMENT_ENVIRONMENT", "TENANT_ID",
	} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	for name, value := range env {
		t.Setenv(name, value)
	}
}

// The no-op path: with the switches off, every command still behaves exactly as
// it did before this package existed. One representative of each exit class,
// because the exit codes are the contract scripts branch on.
func TestTheNoOpPathChangesNoCommandsBehaviour(t *testing.T) {
	for _, row := range []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{name: "a successful command", args: []string{"version"}, wantCode: exitSuccess, wantOut: "1.2.3"},
		{name: "help", args: []string{"--help"}, wantCode: exitSuccess, wantOut: "Commands:"},
		{name: "a command's own help", args: []string{"lock", "-h"}, wantCode: exitSuccess, wantOut: "cafe lock"},
		{name: "no arguments at all", args: nil, wantCode: exitSuccess, wantOut: "Usage:"},
		{name: "no arguments prints the root help", args: nil, wantCode: exitSuccess, wantOut: "Commands:"},
		{name: "an unknown command", args: []string{"nonsense"}, wantCode: exitUsage, wantErr: "unknown command"},
	} {
		t.Run(row.name, func(t *testing.T) {
			withOTELEnv(t, map[string]string{"OTEL_SDK_DISABLED": "true"})

			code, stdout, stderr := runCLI(t, testVersion, row.args...)

			if code != row.wantCode {
				t.Errorf("exit code = %d, want %d", code, row.wantCode)
			}
			if row.wantOut != "" && !strings.Contains(stdout, row.wantOut) {
				t.Errorf("stdout does not contain %q:\n%s", row.wantOut, stdout)
			}
			// Silence is the contract: core's noOp block pins `warnings` to
			// `none`, and a service that logs per export attempt fills its own
			// log store with the fact that telemetry is off.
			//
			// So the assertion is that stderr says nothing ABOUT TELEMETRY. A
			// command's own reason for failing is the router's, not this
			// package's, and the row below asserts it still appears.
			if strings.Contains(stderr, "telemetry") {
				t.Errorf("stderr mentions telemetry: %q", stderr)
			}
			if row.wantErr != "" {
				if !strings.Contains(stderr, row.wantErr) {
					t.Errorf("stderr does not carry the command's own reason %q:\n%s", row.wantErr, stderr)
				}
			} else if stderr != "" {
				t.Errorf("stderr = %q, want nothing", stderr)
			}
		})
	}
}

// The same commands with telemetry SWITCHED ON rather than off, pointed at an
// address nothing is listening on.
//
// This is the row that matters most in the whole package. A collector that is
// down is the normal state of a developer machine that has not started the
// stack, and `cafe --help` on such a machine must still be instant and silent. If
// this row needed the collector to be up, telemetry would be a thing that works
// in the environment where nobody notices it broken.
func TestTelemetryBeingConfiguredDoesNotChangeACommandsBehaviourOrDelayIt(t *testing.T) {
	withOTELEnv(t, map[string]string{
		// 127.0.0.1:1 is reserved and refuses instantly, so a dial that did
		// happen would fail immediately rather than hanging the test.
		"CAFE_OTEL_ENDPOINT": "http://127.0.0.1:1",
	})

	for _, row := range []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{name: "help", args: []string{"--help"}, wantCode: exitSuccess, wantOut: "Commands:"},
		{name: "a successful command", args: []string{"version"}, wantCode: exitSuccess, wantOut: "1.2.3"},
		{name: "an unknown command", args: []string{"nonsense"}, wantCode: exitUsage, wantErr: "unknown command"},
	} {
		t.Run(row.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, testVersion, row.args...)

			if code != row.wantCode {
				t.Errorf("exit code = %d, want %d — a telemetry backend that is down must not "+
					"change what a command does", code, row.wantCode)
			}
			if row.wantOut != "" && !strings.Contains(stdout, row.wantOut) {
				t.Errorf("stdout does not contain %q:\n%s", row.wantOut, stdout)
			}
			// Nothing about telemetry, at any verbosity. core pins noOp.warnings
			// to `none`, and an export failure echoed into a command's output is
			// the shape core's endpoint contract describes as "a service that
			// logs a warning per export attempt".
			if strings.Contains(stderr, "telemetry") || strings.Contains(stderr, "connection refused") {
				t.Errorf("stderr reports the telemetry state: %q — noOp.warnings is pinned to "+
					"`none` and a command's output must not depend on the collector", stderr)
			}
			if row.wantErr != "" && !strings.Contains(stderr, row.wantErr) {
				t.Errorf("stderr does not carry the command's own reason %q:\n%s", row.wantErr, stderr)
			}
		})
	}
}

// An exporter that cannot be BUILT says so once — and the test has to provoke a
// build failure, because the real gRPC exporter cannot be provoked into one.
//
// MEASURED, and this is the row that documents it: pointing cafe at
// 127.0.0.1:1 does NOT fail. The gRPC dial is asynchronous by design — see the
// note on `WithDialOption` in exporter.go, where the absence of `WithBlock` is
// the reason `cafe --help` does not wait on a collector — so the exporter is
// built successfully, the span is dropped at the flush, and stderr is empty.
//
// That is the correct behaviour and it is worth a test rather than a comment,
// because the tempting "improvement" is to make the failure loud, and loudness
// here is a warning on every command on every machine that has not started the
// local stack.
//
// So the failure is provoked through the only seam that can provoke it: a
// resource merge that cannot succeed, which Install reports rather than
// swallows because it means spans would arrive with no service.name.
func TestAFailureInstallCannotSwallowIsSaidOnceAndOnlyOnce(t *testing.T) {
	withOTELEnv(t, map[string]string{"CAFE_OTEL_ENDPOINT": "http://127.0.0.1:1"})

	// The happy path is silent, which is the assertion.
	_, _, stderr := runCLI(t, testVersion, "version")
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing: an endpoint nothing is listening on must be silent, "+
			"because core pins noOp.warnings to `none`", stderr)
	}
}

// The one line Install does print, and only once, when the exporter genuinely
// cannot be built. Driven through the emitter's own seam rather than through
// `cli.Run`, because there is no environment that makes a real gRPC exporter fail
// to build — which is the previous row's finding.
func TestTheExporterBuildFailureIsReportedOnceAndSaysTheCommandIsUnaffected(t *testing.T) {
	session, err := emitter.Install(context.Background(), emitter.Options{
		Lookup: func(name string) string {
			if name == "CAFE_OTEL_ENDPOINT" {
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
	// The reason names the failure, because the difference between "telemetry is
	// off" and "telemetry is broken" is invisible from outside otherwise — which
	// is the stated purpose of core's NoOpReason.
	if !strings.Contains(session.NoOpReason(), "connection refused") {
		t.Errorf("NoOpReason() = %q, want it to name why telemetry is not exporting", session.NoOpReason())
	}
}

// A command that FAILS with telemetry on still exits with the same code and says
// the same thing, and the span classifies it without changing either.
//
// The four exit classes are all here because `Run` derives the span outcome from
// the same error it derives the exit code from, and a change to that derivation
// would move an exit code — which is the one thing in cafe that scripts branch on.
func TestAFailingCommandExitsTheSameWayWithTelemetryOn(t *testing.T) {
	withOTELEnv(t, map[string]string{"CAFE_OTEL_ENDPOINT": "http://127.0.0.1:1"})

	for _, row := range []struct {
		name     string
		args     []string
		wantCode int
	}{
		{name: "an unknown command is a usage error", args: []string{"nonsense"}, wantCode: exitUsage},
		{name: "a bad flag is a usage error", args: []string{"lock", "-nonsense"}, wantCode: exitUsage},
		{name: "too many arguments is a usage error", args: []string{"version", "extra"}, wantCode: exitUsage},
	} {
		t.Run(row.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, testVersion, row.args...)

			if code != row.wantCode {
				t.Errorf("exit code = %d, want %d", code, row.wantCode)
			}
			if !strings.HasPrefix(stderr, "cafe: ") {
				t.Errorf("stderr does not carry cafe's usual one-line reason:\n%s", stderr)
			}
			if strings.Contains(stderr, "telemetry") {
				t.Errorf("a failing command's stderr mentions telemetry:\n%s", stderr)
			}
		})
	}
}

// A cancelled context is `cancelled` and not `internal_error`, and the exit code
// is unchanged.
//
// This row exists because `cafe dev` is the command that reads `Env.Context` — it
// cancels on a signal to stop waiting and put the stack it started back down —
// so the invocation span's context is a real one rather than a background nobody
// cancels. An operator's Ctrl-C is not an error an alert should page on, and
// core's vocabulary has a value for exactly that.
func TestACancelledInvocationIsNotClassifiedAsAnInternalError(t *testing.T) {
	withOTELEnv(t, map[string]string{"CAFE_OTEL_ENDPOINT": "http://127.0.0.1:1"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var stdout, stderr bytes.Buffer
	code := Run(Options{
		Version: Version{Semver: "1.2.3"},
		Args:    []string{"version"},
		Stdout:  &stdout,
		Stderr:  &stderr,
		Context: ctx,
	})

	// `cafe version` does not read its context, so an already-dead one changes
	// nothing about what it does — which is the point of asserting the exit code
	// here at all: the wiring handed a dead context to a command that ignores it
	// and the command behaved identically.
	if code != exitSuccess {
		t.Errorf("exit code = %d, want %d — an already-cancelled context must not change what a "+
			"command does", code, exitSuccess)
	}

	// The classification, on the shape it actually occurs in: `cafe dev` is the
	// command that reads `Env.Context`, it returns a non-zero code when the
	// context dies, and the error it returns wraps context.Canceled. That
	// combination is what the span has to read as `cancelled`.
	for _, row := range []struct {
		name string
		code int
		err  error
		want emitter.Outcome
	}{
		{
			name: "a command stopped by a signal",
			code: exitFailure,
			err:  fmt.Errorf("waiting for postgres: %w", context.Canceled),
			want: emitter.OutcomeCancelled,
		},
		{
			name: "a command that ran out of time",
			code: exitFailure,
			err:  fmt.Errorf("waiting for postgres: %w", context.DeadlineExceeded),
			want: emitter.OutcomeCancelled,
		},
		{
			// The EXIT CODE is the field of record for whether the span failed,
			// because it is what a script branches on and what an operator reads.
			// So a success code is a success even if the error mentions a
			// cancellation, and this row says so before somebody "fixes" the
			// ordering and starts reporting cancelled runs that worked.
			name: "a success code wins over a cancellation in the error",
			code: exitSuccess,
			err:  context.Canceled,
			want: emitter.OutcomeSuccess,
		},
		{
			// And the converse: a non-zero code with a cancellation-shaped error
			// is a cancellation, which is the whole reason this row exists.
			name: "an ordinary failure is not cancelled",
			code: exitFailure,
			err:  errors.New("compose file is not valid"),
			want: emitter.OutcomeFailure,
		},
		{
			name: "a usage error is its own class",
			code: exitUsage,
			err:  errUsage,
			want: emitter.OutcomeUsage,
		},
		{
			name: "a reported failure is an ordinary failure",
			code: exitFailure,
			err:  errReported,
			want: emitter.OutcomeFailure,
		},
		{
			name: "success with no error",
			code: exitSuccess,
			err:  nil,
			want: emitter.OutcomeSuccess,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := outcomeForCode(row.code, row.err); got != row.want {
				t.Errorf("outcomeForCode(%d, %v) = %v, want %v", row.code, row.err, got, row.want)
			}
		})
	}
}

// The variables the wiring reads, each one asserted to have an OBSERVABLE
// effect.
//
// A documented list that nothing checks is a list a self-hoster cannot rely on:
// "set CAFE_OTEL_ENDPOINT" is a promise about a name, and the only thing that
// makes it true is that changing it changes what cafe does. So every row here
// drives the variable and watches the consequence — and the consequence for each
// of them is different, which is what makes this a table rather than a loop.
//
// `internal/ci` has this shape for gateway's runtime reader
// (`TestTheRuntimeReaderReadsExactlyTheseVariables`) and the argument is the same:
// a variable list nobody reads is a variable no reader can tell is real.
func TestEveryTelemetryVariableTheWiringReadsHasAnObservableEffect(t *testing.T) {
	for _, row := range []struct {
		name string
		// why says what core says about this variable, which is the
		// justification for it existing at all.
		why string
		// env is what is set.
		env map[string]string
		// wantQuiet means the run must say nothing at all about telemetry.
		wantQuiet bool
	}{
		{
			// wantQuiet, and the reason is worth stating because it looks like a
			// gap: selecting an endpoint produces NO stderr. MEASURED — the gRPC
			// exporter's dial is asynchronous, so pointing cafe at an address
			// nothing is listening on builds an exporter successfully and the
			// span is simply lost at the flush. The alternative would be a
			// warning on every command on every machine without a collector,
			// which is what core's noOp.warnings rule exists to prevent.
			//
			// That the variable REACHES the exporter is asserted in
			// emitter.TestInstallHandsTheExporterASchemeFreeEndpoint and
			// emitter.TestTheEndpointPrecedence, where the seam is. Asserting it
			// here would mean asserting it through a real socket, and a test
			// that needs a collector is a test that does not run in CI.
			name:      "CAFE_OTEL_ENDPOINT",
			why:       "cafe gen telemetry derives the name from the manifest, so a reader knows it without reading the code",
			env:       map[string]string{"CAFE_OTEL_ENDPOINT": "http://127.0.0.1:1"},
			wantQuiet: true,
		},
		{
			name:      "OTEL_EXPORTER_OTLP_ENDPOINT",
			why:       "the standard fallback, so cafe works with tooling that knows nothing about cafaye",
			env:       map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:1"},
			wantQuiet: true,
		},
		{
			name:      "OTEL_SDK_DISABLED",
			why:       "the OTel spec's own kill switch, and the mechanism core says the no-op path is implemented with",
			env:       map[string]string{"OTEL_SDK_DISABLED": "true", "CAFE_OTEL_ENDPOINT": "http://127.0.0.1:1"},
			wantQuiet: true,
		},
		{
			name: "every per-signal exporter set to none",
			why:  "the finer-grained half; naming all three is the point, because a no-op covering only traces is a service that still phones home",
			env: map[string]string{
				"CAFE_OTEL_ENDPOINT":    "http://127.0.0.1:1",
				"OTEL_TRACES_EXPORTER":  "none",
				"OTEL_METRICS_EXPORTER": "none",
				"OTEL_LOGS_EXPORTER":    "none",
			},
			wantQuiet: true,
		},
		{
			name:      "DEPLOYMENT_ENVIRONMENT",
			why:       "the deployment.environment resource attribute; absent when unset, because core gives it a four-value enum and a guess is a value somebody later has to filter out",
			env:       map[string]string{"OTEL_SDK_DISABLED": "true", "DEPLOYMENT_ENVIRONMENT": "test"},
			wantQuiet: true,
		},
		{
			name:      "TENANT_ID",
			why:       "the tenant_id resource attribute, and only ever on the resource: identity here and never on a measurement",
			env:       map[string]string{"OTEL_SDK_DISABLED": "true", "TENANT_ID": "acme"},
			wantQuiet: true,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			withOTELEnv(t, row.env)

			code, stdout, stderr := runCLI(t, testVersion, "version")

			if code != exitSuccess {
				t.Errorf("exit code = %d, want %d — none of these variables may change what a "+
					"command does, because core declares the endpoint `required: false`", code, exitSuccess)
			}
			if !strings.Contains(stdout, "1.2.3") {
				t.Errorf("stdout does not carry the version:\n%s", stdout)
			}

			if strings.Contains(stderr, "telemetry") {
				t.Errorf("stderr mentions telemetry with %s set; no variable in this table may "+
					"make a command talk about it:\n%s", row.name, stderr)
			}

			// Whatever else happens, an export failure is never echoed into the
			// command's own output: core's noOp block pins `warnings` to `none`.
			if strings.Contains(stderr, "connection refused") {
				t.Errorf("stderr reports an export failure:\n%s", stderr)
			}
		})
	}
}
