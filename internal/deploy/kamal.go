package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

// KamalRunner is the Kamal engine: it owns the Kamal plan and runs it through
// kamal, which stays the only place cafe knows how to execute kamal — including
// the "kamal is not installed" sentence, which is this engine's shape of the
// missing-tool refusal.
//
// It is the fleet's engine today, and it retires at parity with the Argo
// engine, not before — DEPLOYMENT.md: "Kamal and kit/templates/kamal retire at
// parity, not before."
type KamalRunner struct {
	// Binary is the resolved path to kamal. It is resolved on PATH and never
	// executed at construction, so building the command is safe on a machine
	// with no deploy engine and the failure arrives where it can be explained.
	Binary string
}

// NewKamalRunner is the wiring the router uses.
func NewKamalRunner() *KamalRunner { return &KamalRunner{Binary: ResolveKamal()} }

// ResolveKamal finds kamal on PATH, and answers the bare name when it is not
// there. An unresolved name still produces a useful error, because exec then
// reports which binary it could not find — and the preflight exists to turn
// that into an installation instruction.
func ResolveKamal() string {
	if path, err := exec.LookPath("kamal"); err == nil {
		return path
	}
	return "kamal"
}

// Plan is the Kamal plan, built by PlanFor — the pure function whose tests are
// the proof of what a Kamal deploy is. The engine owns it; the caller asks.
func (k *KamalRunner) Plan(dep Deployment, opts Options) (Plan, error) {
	return PlanFor(dep, opts), nil
}

// Run executes one step of the plan in dir.
//
// The working directory is the project, because Kamal resolves its own relative
// paths — `config/deploy.yml`, `Dockerfile`, `.kamal/secrets` — against it, and
// a deploy pointed at the wrong directory is a deploy that reads somebody
// else's configuration.
//
// The one error it translates is "the binary is not there", because that is the
// one a customer meets first and exec's own wording helps nobody.
func (k *KamalRunner) Run(ctx context.Context, dir string, step Step, stdout, stderr io.Writer) error {
	return k.run(ctx, dir, step.Args(), stdout, stderr)
}

// run is every kamal command in this engine, so the "kamal is not installed"
// sentence and the process wiring are in one place.
func (k *KamalRunner) run(ctx context.Context, dir string, argv []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, k.Binary, argv...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = nil

	if err := cmd.Run(); err != nil {
		if looksMissing(err) {
			return fmt.Errorf("%w\n%s", ErrKamalMissing, installNote(k.Binary))
		}
		return err
	}
	return nil
}

// ErrKamalMissing is the sentinel for "the deploy engine is not installed". It
// is a sentinel rather than a string so a caller can tell this apart from a
// deploy that ran and failed, which is a different thing with a different fix.
var ErrKamalMissing = fmt.Errorf("kamal is not installed")

// installNote is what to do about it.
//
// It says "gem install kamal" rather than nothing, because that is the install
// that works: Kamal is a Ruby gem and always has been, and the precompiled
// binaries in its toolbox (kamal-proxy, kamal-secrets) are downloaded onto the
// *servers*, not onto the operator's machine. The consequence worth stating is
// the one that surprises people: the service image never needs Ruby, but the
// machine you deploy *from* does.
func installNote(binary string) string {
	return "kamal is the deploy engine cafe drives, and it is not on PATH (" + binary + " could not be run).\n" +
		"  Install it with:  gem install kamal\n" +
		"  It is a Ruby gem, so the machine you deploy from needs Ruby. The deployed service does not:\n" +
		"  kamal-proxy and kamal-secrets are standalone binaries kamal puts on the server."
}

func looksMissing(err error) bool {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return errors.Is(execErr.Err, exec.ErrNotFound)
	}
	return false
}
