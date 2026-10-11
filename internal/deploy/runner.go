package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// The two engines, and the whole set. `cafe deploy --engine` selects one, and
// the default is Kamal because the fleet runs Kamal today — DEPLOYMENT.md:
// "Kamal and kit/templates/kamal retire at parity, not before."
const (
	// EngineKamal is the Kamal engine: kamal setup, the rolling rollout, the
	// health gate. It is the default, and its behaviour is byte-identical to
	// what it was before the Argo engine existed.
	EngineKamal = "kamal"
	// EngineArgo is the Argo engine: a values commit, a push, and a wait for
	// Argo to sync. It is the launch substrate's engine, where there is no
	// Kamal and CI never holds cluster credentials.
	//
	// EngineRemote is declared in remote.go, beside the gate that pins it off.
	EngineArgo = "argo"
)

// ErrUsage is the sentinel for "cafe was invoked wrongly", which is a different
// class from both a refusal and a failed deploy and gets a different exit code.
//
// A typo in --engine is the operator invoking cafe wrongly, and the wrong answer
// to it is a deploy run against the wrong substrate — which is the one outcome
// a deploy command must never produce by accident. cafe exit-2s those and
// exit-1s the other two, so a pipeline can tell "I called it wrong" from "it
// refused" from "it ran and something broke" — three different fixes behind
// three different codes.
var ErrUsage = errors.New("cafe deploy: invalid invocation")

// RunnerFor returns the runner for an engine name. The empty name is the
// default, which is Kamal because the fleet runs Kamal today — DEPLOYMENT.md:
// "Kamal and kit/templates/kamal retire at parity, not before."
//
// An unknown name is an error and not a default: a typo in --engine is the
// operator invoking cafe wrongly, and the wrong answer to it is a deploy run
// against the wrong substrate — which is the one outcome a deploy command
// must never produce by accident. The error wraps ErrUsage so the caller
// exits 2, the same as any other bad flag.
func RunnerFor(engine string) (Runner, error) {
	switch engine {
	case "", EngineKamal:
		return NewKamalRunner(), nil
	case EngineArgo:
		return NewArgoRunner(), nil
	case EngineRemote:
		return NewRemoteRunner(), nil
	default:
		return nil, fmt.Errorf("%w: --engine must be %q (the default), %q or %q, not %q",
			ErrUsage, EngineKamal, EngineArgo, EngineRemote, engine)
	}
}

// Runner is the deploy engine for one service: it owns which commands run, in
// what order, and what each is for.
//
// # Why the plan is the engine's and not the caller's
//
// The caller builds argv today, and that is the defect this seam removes. A
// `kubectl` runner handed `["kamal", "setup", …]` is nonsense, and a runner
// that reinterprets Kamal argv is a second engine that can disagree with the
// first about what a deploy is — the disagreement would be found in a deploy
// nobody rehearsed. So the engine owns the plan: the caller asks for it,
// prints it for a dry run, and executes it step by step, but never builds argv
// itself.
//
// # What the caller keeps
//
// The parts either side of the engine, which this package's doc.go says are
// cafe's job: preflight, the refusal, the dry run, the confirmation and the
// after-the-fact report.
//
// The interface is declared HERE and satisfied by the engines in this package
// (kamal.go's KamalRunner, argo.go's ArgoRunner, remote.go's RemoteRunner),
// wired in internal/cli. This package imports neither cli nor backup, and that
// is deliberate: a package that imports another to reach an interface has an
// import edge where none is needed, and the two commands make different claims
// about different engines.
type Runner interface {
	// Plan returns the engine's plan for one service: which commands run, in
	// what order, and what each is for.
	//
	// It is pure: it reads no file, opens no socket and runs no command, which
	// is what lets a test assert the exact argv of a dry run with no container
	// runtime, no registry and no cluster anywhere in sight.
	Plan(dep Deployment, opts Options) (Plan, error)
	// Run executes one step of the plan in dir, with the process's output
	// attached to the two writers so a person watching a terminal sees the
	// deploy happen.
	Run(ctx context.Context, dir string, step Step, stdout, stderr io.Writer) error
}
