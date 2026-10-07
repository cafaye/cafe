package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
func (r *CNPGRunner) Plan(cover Cover, cfg Config, opts Options) (Plan, error) {
	return PlanForCNPG(cover, opts)
}

// Run executes one step of the plan in dir.
//
// A step that carries files writes them before its command runs. That is the
// Restore step and the recovery Cluster: `kubectl apply` reads a file, and the
// manifest is a fact about the step rather than a second copy the caller has
// to keep in step with it. A dry run never runs a step, so the file is never
// written by a dry run — and a dry run that wrote a file would be a dry run
// with a side effect, which is the one thing it must never be.
func (r *CNPGRunner) Run(ctx context.Context, dir string, step Step, stdout, stderr io.Writer) error {
	for _, file := range step.Files {
		// The directory is made as well as the file: a fresh project has no
		// config/ yet, and WriteFile does not create it — measured by a test that
		// watched the write fail on a directory nobody had made.
		if err := os.MkdirAll(filepath.Dir(file.Path), 0o755); err != nil {
			return fmt.Errorf("caf backup: create the directory for %s: %w", file.Path, err)
		}
		if err := os.WriteFile(file.Path, []byte(file.Content), 0o600); err != nil {
			return fmt.Errorf("caf backup: write %s for the %s step: %w", file.Path, step.Name, err)
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

// runCNPGCycle is the CNPG engine's cycle: the same orchestration as the Kamal
// one — the plan's steps in order, the drop on both paths, the report — with
// none of the parts that are Kamal's. There is no config/kamal-backup.yml to
// read, no two-file contract to check, no accessory, no schedule and no
// secrets file: the cluster is a fact about the Kubernetes namespace, not a
// pair of rendered documents.
//
// The steps are the plan's, and the two waits are blocking `kubectl wait`
// commands rather than caller-side polls, so the cycle runs each step once and
// the phase vocabulary stays in kubectl where it belongs.
func runCNPGCycle(ctx context.Context, r Runner, req Request, stdout, stderr io.Writer) error {
	dir := req.Dir
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("caf backup: %s is not a directory caf can read: %w", abs, err)
	}
	if req.Env != "" {
		return fmt.Errorf("%w: --env is a Kamal destination and the CNPG engine has no destination\n"+
			"  The cluster is a fact about the Kubernetes namespace, not a rendered overlay.\n"+
			"  Back it up without --env", ErrUsage)
	}

	cover := Cover{Dir: abs, Service: req.Service}
	plan, err := r.Plan(cover, Config{}, Options{
		Scratch: req.Scratch,
		Tables:  req.Tables,
		Timeout: req.Timeout,
	})
	if err != nil {
		return err
	}

	// A dry run executes nothing — not even the preflight. The Kamal dry run
	// runs the two read-only steps because the contract check needs what kamal
	// resolved; the CNPG engine has no contract to check, so there is nothing
	// a dry run would catch by running, and a dry run that executes a command
	// is a dry run that can fail on a machine with no cluster.
	if req.DryRun {
		printCNPGDryRun(stdout, plan)
		return nil
	}

	version, err := preflight(ctx, r, plan, stderr)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "%s: backing up the %s cluster with kubectl cnpg %s\n",
		plan.Service, plan.Service, version)
	fmt.Fprintf(stdout, "  the restore is drilled into the %s cluster\n", plan.Scratch)
	fmt.Fprintf(stdout, "  the assertion checks %s after the restore\n", strings.Join(plan.Tables, ", "))

	cause := runAll(ctx, r, plan, []string{Backup, WaitBackup, Restore, WaitReady, Assert}, stdout, stderr)
	if cause != nil {
		drop(ctx, r, plan, stdout, stderr)
		return cnpgFailureReport(plan, stdout, cause)
	}
	drop(ctx, r, plan, stdout, stderr)
	return cnpgSuccessReport(plan, stdout)
}

// printCNPGDryRun is the CNPG engine's dry run: every command, the recovery
// manifest the restore would apply, and the fact that nothing was executed.
//
// The manifest is printed from the Restore step's own Files, which is the same
// value a real run writes — so the document on the screen and the document on
// disk are one fact, and a dry run cannot drift from the run it describes.
func printCNPGDryRun(stdout io.Writer, plan Plan) {
	fmt.Fprintf(stdout, "\nthis is what caf backup would run:\n\n")
	for _, step := range plan.Steps {
		fmt.Fprintf(stdout, "  %s\n", step.Command())
		fmt.Fprintf(stdout, "      %s\n", step.Why)
		for _, file := range step.Files {
			fmt.Fprintf(stdout, "    and writes %s:\n%s\n", file.Path, file.Content)
		}
	}
	fmt.Fprintf(stdout, "\nDry run, and nothing was executed: not the preflight a real run makes\n")
	fmt.Fprintf(stdout, "first, and none of the steps above. No backup was taken, no cluster was\n")
	fmt.Fprintf(stdout, "created or deleted, and no file in %s was written — the recovery\n", plan.Dir)
	fmt.Fprintf(stdout, "manifest above is the document the restore step would apply, printed\n")
	fmt.Fprintf(stdout, "rather than written.\n")
	fmt.Fprintf(stdout, "\nBack it up for real with:\n  caf backup --engine cnpg%s %s\n", yesFlag(plan), plan.Service)
}

// cnpgSuccessReport is what a CNPG cycle that restored something says.
//
// It names the scratch cluster and the tables, and it says what the cycle did
// NOT prove — which for this engine is the schedule. The Kamal report's limit
// is the backup.schedule in the config file; the CNPG engine does not read a
// schedule at all, so the honest limit is the cluster's own.
func cnpgSuccessReport(plan Plan, stdout io.Writer) error {
	fmt.Fprintf(stdout, "\n%s: the backup restored\n", plan.Service)
	fmt.Fprintf(stdout, "  kubectl cnpg backup took a real backup of the %s cluster during this run,\n", plan.Service)
	fmt.Fprintf(stdout, "  and the %s cluster was bootstrapped from it. The assertion's exit\n", plan.Scratch)
	fmt.Fprintf(stdout, "  status is the verdict, and the row counts it printed are above. %s\n", tablesSentence(plan.Tables))
	fmt.Fprintf(stdout, "  %s has been deleted, so nothing is left to restore into by accident\n", plan.Scratch)
	fmt.Fprintf(stdout, "  what this does NOT prove: that the next backup will be taken. This engine takes\n")
	fmt.Fprintf(stdout, "  a backup on every run; the schedule that protects the service between drills\n")
	fmt.Fprintf(stdout, "  is the cluster's own, and caf does not read it.\n")
	return nil
}

// cnpgFailureReport is what a partial CNPG cycle prints.
//
// It answers the question a failed cycle actually raises — what is left behind —
// and it states the fact that makes a failed drill survivable: the scratch
// cluster is deleted, and a backup that was already taken is a real backup of
// the live cluster, unaffected by the failure.
func cnpgFailureReport(plan Plan, stdout io.Writer, cause error) error {
	fmt.Fprintf(stdout, "\n%s\n", cause)
	fmt.Fprint(stdout, "\nwhat to do:\n")
	fmt.Fprint(stdout, "  the scratch cluster is deleted on the way out, including on this failure, so nothing is\n")
	fmt.Fprint(stdout, "  left to restore into by accident.\n")
	fmt.Fprint(stdout, "  a backup the backup step already took is a real backup of the live cluster, and a\n")
	fmt.Fprint(stdout, "  failed drill does not delete it: the next drill restores from it.\n")
	fmt.Fprint(stdout, "  the reason is the command's own, above.\n")
	// ErrCycleFailed is a sentinel so the caller can tell "the report is above"
	// from "caf failed before it could report anything" — the first is errReported
	// (exit 1, stderr empty), the second is an error that belongs there.
	return fmt.Errorf("%w: %s", ErrCycleFailed, plan.Service)
}
