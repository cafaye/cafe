package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// CNPGRunner is the CNPG engine: it owns the CNPG plan and runs it through
// kubectl, which stays the only place caf knows how to drive `kubectl cnpg` —
// including the "kubectl is not installed" sentence, which is this engine's
// shape of deploy's ErrKamalMissing.
//
// It is the launch substrate's engine, where there is no Kamal: the backup is
// a CloudNativePG Backup object and the restore is a declarative recovery
// Cluster, both driven by `kubectl cnpg` and `kubectl wait`.
type CNPGRunner struct {
	// Binary is the resolved path to kubectl. It is resolved on PATH and never
	// executed at construction, so building the runner is safe on a machine
	// with no cluster and the failure arrives where it can be explained.
	Binary string
	// recoveryManifest and recoveryPath are what Plan computed and what the
	// Restore step needs: the rendered recovery Cluster is a document, and
	// `kubectl apply` reads a file. Plan sets them; Run writes the manifest
	// when the Restore step runs. A dry run never runs Restore, so the file is
	// never written by a dry run — see Run.
	recoveryManifest string
	recoveryPath     string
}

// NewCNPGRunner is the wiring RunnerFor uses.
func NewCNPGRunner() *CNPGRunner {
	return &CNPGRunner{Binary: ResolveKubectl()}
}

// ResolveKubectl finds kubectl on PATH, and answers the bare name when it is
// not there. An unresolved name still produces a useful error, because exec
// then reports which binary it could not find — and the preflight exists to
// turn that into an installation instruction. It is the CNPG engine's shape
// of deploy's ResolveKamal.
func ResolveKubectl() string {
	if path, err := exec.LookPath("kubectl"); err == nil {
		return path
	}
	return "kubectl"
}

// Plan is the CNPG plan, built by PlanForCNPG — the pure function whose tests
// are the proof of what a CNPG backup is. The engine owns it; the caller asks.
//
// The recovery manifest and path are kept on the runner as well as on the
// plan, because the Restore step needs the manifest on disk and Run is handed
// the Step rather than the Plan. They are set here, once per cycle, so the
// step that applies the manifest writes the exact document the plan rendered.
func (r *CNPGRunner) Plan(cover Cover, cfg Config, opts Options) (Plan, error) {
	plan, err := PlanForCNPG(cover, opts)
	if err != nil {
		return Plan{}, err
	}
	r.recoveryManifest = plan.RecoveryManifest
	r.recoveryPath = plan.RecoveryPath
	return plan, nil
}

// Run executes one step of the plan in dir.
//
// The Restore step is the one step that needs something on disk before it can
// run: the recovery Cluster is a rendered document and `kubectl apply` reads a
// file. So Run writes the manifest Plan computed before applying it. The write
// is part of the step rather than a side effect of planning, because a dry run
// never runs Restore — and a dry run that wrote a file would be a dry run with
// a side effect, which is the one thing it must never be.
func (r *CNPGRunner) Run(ctx context.Context, dir string, step Step, stdout, stderr io.Writer) error {
	if step.Name == Restore {
		if r.recoveryPath == "" {
			return fmt.Errorf("caf backup: the plan has no recovery manifest, so there is nothing to restore into")
		}
		if err := os.WriteFile(r.recoveryPath, []byte(r.recoveryManifest), 0o600); err != nil {
			return fmt.Errorf("caf backup: write the recovery manifest for the restore: %w", err)
		}
	}
	return r.run(ctx, dir, step.Args(), stdout, stderr)
}

// run is every kubectl command in this engine, so the "kubectl is not
// installed" sentence and the process wiring are in one place.
//
// The working directory is the project, the way deploy's KamalRunner sets it:
// kubectl resolves nothing against it today, but a backup engine pointed at the
// wrong directory is a backup engine reading somebody else's kubeconfig, and
// the property is worth keeping for the same reason deploy keeps it.
func (r *CNPGRunner) run(ctx context.Context, dir string, argv []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, r.Binary, argv...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = nil

	if err := cmd.Run(); err != nil {
		if looksKubectlMissing(err) {
			return fmt.Errorf("%w\n%s", ErrKubectlMissing, installNote(r.Binary))
		}
		return err
	}
	return nil
}

// ErrKubectlMissing is the sentinel for "kubectl is not installed". It is a
// sentinel rather than a string so a caller can tell this apart from a backup
// that ran and failed, which is a different thing with a different fix.
var ErrKubectlMissing = fmt.Errorf("kubectl is not installed")

// installNote is what to do about it. It names the two things that have to be
// on PATH — kubectl itself and the CloudNativePG plugin that gives it the
// `cnpg` subcommand — because the preflight fails on the second even when the
// first is present, and the operator has to be able to tell which they are
// missing.
func installNote(binary string) string {
	return "kubectl is the backup engine caf drives, and it is not on PATH (" + binary + " could not be run).\n" +
		"  Install kubectl, then the CloudNativePG plugin that gives it the `cnpg` subcommand.\n" +
		"  The plugin is what `kubectl cnpg version` probes: without it, kubectl answers\n" +
		"  \"unknown command cnpg\" and the preflight above is where that surfaces."
}

// looksKubectlMissing reports whether err is exec's "the binary is not there",
// which is the one error a customer meets first and the one the preflight
// exists to turn into an installation instruction.
func looksKubectlMissing(err error) bool {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return errors.Is(execErr.Err, exec.ErrNotFound)
	}
	return false
}
