package deploy

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// ArgoRunner is the Argo engine: it owns the Argo plan and runs it through git
// and kubectl, which stay the only places caf knows how to drive a pull-only
// substrate — including the "kubectl is not installed" sentence, which is this
// engine's shape of the missing-tool refusal.
//
// It is the launch substrate's engine, where there is no Kamal: CI never
// holds cluster credentials, and the only thing that changes the cluster is a
// commit. So a deploy is a values commit, a push, and a wait for Argo to sync.
type ArgoRunner struct {
	// Binary is the resolved path to kubectl. It is resolved on PATH and never
	// executed at construction, so building the runner is safe on a machine
	// with no cluster and the failure arrives where it can be explained.
	Binary string
}

// NewArgoRunner is the wiring RunnerFor uses.
func NewArgoRunner() *ArgoRunner {
	return &ArgoRunner{Binary: ResolveKubectl()}
}

// ResolveKubectl finds kubectl on PATH, and answers the bare name when it is
// not there. An unresolved name still produces a useful error, because exec
// then reports which binary it could not find — and the preflight exists to
// turn that into an installation instruction. It is the Argo engine's shape
// of ResolveKamal.
func ResolveKubectl() string {
	if path, err := exec.LookPath("kubectl"); err == nil {
		return path
	}
	return "kubectl"
}

// Plan is the Argo plan. It reads the service repository's chart — the values
// the OCI chart ships and the version that is the OCI tag — and the commit the
// release is built from, then hands the facts to PlanForArgo, the pure
// function whose tests are the proof of what an Argo deploy is.
//
// The reading is the engine's and not the plan's: the plan stays pure, and a
// dry run's steps are assertable against a real local git repository with no
// cluster anywhere in sight.
func (r *ArgoRunner) Plan(dep Deployment, opts Options) (Plan, error) {
	facts, err := readChart(dep.Dir)
	if err != nil {
		return Plan{}, err
	}
	dep.Values = facts.valuesPath
	dep.Chart = facts.chartPath
	dep.ValuesContent = facts.valuesContent
	dep.ChartContent = facts.chartContent
	dep.Image = facts.image
	dep.ChartVersion = facts.chartVersion
	dep.Commit = facts.commit
	return PlanForArgo(dep, opts), nil
}

// chartFacts is what the Argo engine reads out of a service repository before
// it asks for the plan. It is the Argo shape of the Kamal engine's Deployment:
// the facts a deploy needs that are not a rendered document.
type chartFacts struct {
	valuesPath    string
	chartPath     string
	valuesContent string
	chartContent  string
	image         string
	chartVersion  string
	commit        string
}

// readChart reads the service repository's chart and the commit it is at,
// refusing with a message that names the file that is missing.
//
// It checks existence and parses for the two facts the plan needs — the image
// reference and the chart version — because a deploy that cannot find them
// would write a commit that changes nothing, and a commit that changes nothing
// is worse than no commit: it pushes a new OCI tag that is the old chart, and
// Argo syncs it as if it were a release.
func readChart(dir string) (chartFacts, error) {
	valuesPath := filepath.Join(dir, "chart", "values.yaml")
	chartPath := filepath.Join(dir, "Chart.yaml")

	valuesContent, err := os.ReadFile(valuesPath)
	if err != nil {
		return chartFacts{}, fmt.Errorf("caf deploy: %s has no chart/values.yaml, so there is no chart to release.\n"+
			"  Run `caf gen chart` in the service repository to write it, then deploy again.\n"+
			"  The values ride inside the OCI chart artifact — DEPLOYMENT.md — so the file\n"+
			"  the deploy resolves is the one the chart ships, not one the Application names", dir)
	}
	chartContent, err := os.ReadFile(chartPath)
	if err != nil {
		return chartFacts{}, fmt.Errorf("caf deploy: %s has no Chart.yaml, so there is no chart version to bump.\n"+
			"  Run `caf gen chart` in the service repository to write it, then deploy again.\n"+
			"  The chart version is the OCI tag — DEPLOYMENT.md — so without it there is\n"+
			"  no version for the publish workflow to push and no targetRevision to move", dir)
	}

	var values struct {
		Image struct {
			Repository string `yaml:"repository"`
			Tag        string `yaml:"tag"`
		} `yaml:"image"`
	}
	if err := yaml.Unmarshal(valuesContent, &values); err != nil {
		return chartFacts{}, fmt.Errorf("caf deploy: read chart/values.yaml: %w", err)
	}
	if values.Image.Repository == "" || values.Image.Tag == "" {
		return chartFacts{}, fmt.Errorf("caf deploy: chart/values.yaml has no image.repository and image.tag, so there is\n"+
			"  no image reference to bump. The generator writes both; a hand-edited values.yaml\n"+
			"  that dropped them is a chart nobody can release. Run `caf gen chart` to rewrite it")
	}

	var chart struct {
		Version string `yaml:"version"`
	}
	if err := yaml.Unmarshal(chartContent, &chart); err != nil {
		return chartFacts{}, fmt.Errorf("caf deploy: read Chart.yaml: %w", err)
	}
	if chart.Version == "" {
		return chartFacts{}, fmt.Errorf("caf deploy: Chart.yaml has no version, so there is no chart version to bump.\n"+
			"  The chart version is the OCI tag — DEPLOYMENT.md — so without it there is\n"+
			"  no version for the publish workflow to push")
	}

	commit, err := gitOutput(dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return chartFacts{}, fmt.Errorf("caf deploy: %s is not a git repository caf can read, so there is no commit\n"+
			"  to build the release from: %w\n"+
			"  A deploy under Argo is a values commit; without a git repository there is\n"+
			"  nothing to commit and nothing to push", dir, err)
	}

	return chartFacts{
		valuesPath:    valuesPath,
		chartPath:     chartPath,
		valuesContent: string(valuesContent),
		chartContent:  string(chartContent),
		image:         values.Image.Repository + ":" + values.Image.Tag,
		chartVersion:  chart.Version,
		commit:        commit,
	}, nil
}

// Run executes one step of the plan in dir.
//
// The Commit and Push steps are compound — they are git sequences, not single
// commands — so they have methods of their own. Every other step is one
// command, run the way the Kamal engine runs its steps.
func (r *ArgoRunner) Run(ctx context.Context, dir string, step Step, stdout, stderr io.Writer) error {
	switch step.Name {
	case Commit:
		return r.commit(ctx, dir, step, stdout, stderr)
	case Push:
		return r.push(ctx, dir, step, stdout, stderr)
	default:
		return r.run(ctx, dir, step.Args(), stdout, stderr)
	}
}

// commit writes the two files the release carries, checks the working tree,
// and commits. It is the half of the deploy that changes the service
// repository, and the only half caf is allowed to change: Argo is pull-only,
// and the cluster is never written.
func (r *ArgoRunner) commit(ctx context.Context, dir string, step Step, stdout, stderr io.Writer) error {
	// The dirty-tree check comes first, before anything is written. A release
	// commit must be exactly the values bump; a working tree that already
	// carries a change to either file would put that change into the commit
	// too, and a commit that sweeps in edits nobody meant is the failure this
	// check exists to prevent.
	dirty, err := r.dirtyPaths(ctx, dir)
	if err != nil {
		return err
	}
	for _, path := range dirty {
		if path == "chart/values.yaml" || path == "Chart.yaml" {
			fmt.Fprintf(stdout, "\n%s\n\n", ErrRefused)
			fmt.Fprintf(stdout, "  the working tree already carries a change to %s, so the release commit\n", path)
			fmt.Fprintf(stdout, "  would mix the values bump with edits nobody meant for this release.\n")
			fmt.Fprintf(stdout, "  Commit or stash what is there, then deploy again — a release commit\n")
			fmt.Fprintf(stdout, "  must be exactly the bump from %s to the new version.\n", step.Files[0].Path)
			return fmt.Errorf("%s: the working tree is dirty at %s", ErrRefused, path)
		}
	}

	for _, file := range step.Files {
		if err := os.MkdirAll(filepath.Dir(file.Path), 0o755); err != nil {
			return fmt.Errorf("caf deploy: create the directory for %s: %w", file.Path, err)
		}
		if err := os.WriteFile(file.Path, []byte(file.Content), 0o600); err != nil {
			return fmt.Errorf("caf deploy: write %s for the %s step: %w", file.Path, step.Name, err)
		}
	}

	// git add the two files by path, never `git add .`: a release commit is
	// exactly the values bump, and adding everything would sweep in unrelated
	// changes the dirty-tree check above did not catch because they are not
	// the two files the deploy writes.
	add := append([]string{"git", "add"}, filePaths(step.Files)...)
	if err := r.run(ctx, dir, add, stdout, stderr); err != nil {
		return fmt.Errorf("caf deploy: stage the release: %w", err)
	}

	if err := r.run(ctx, dir, step.Args(), stdout, stderr); err != nil {
		return fmt.Errorf("caf deploy: commit the release: %w", err)
	}
	return nil
}

// push publishes the commit. It checks for a remote first: a service
// repository with no remote cannot be deployed, because the publish workflow
// and Argo both need the pushed chart, and a commit that goes nowhere is a
// release nobody can sync.
func (r *ArgoRunner) push(ctx context.Context, dir string, step Step, stdout, stderr io.Writer) error {
	remotes, err := gitOutput(dir, "remote")
	if err != nil {
		return fmt.Errorf("caf deploy: read the git remotes: %w", err)
	}
	if strings.TrimSpace(remotes) == "" {
		fmt.Fprintf(stdout, "\n%s\n\n", ErrRefused)
		fmt.Fprintf(stdout, "  the service repository has no git remote, so the release commit cannot be pushed.\n")
		fmt.Fprintf(stdout, "  The publish workflow pushes the new OCI tag and Argo syncs it, and both need\n")
		fmt.Fprintf(stdout, "  the commit on a remote. Add the remote the service repository is published\n")
		fmt.Fprintf(stdout, "  from, then deploy again — caf pushes; it does not hand the commit to an operator.\n")
		return fmt.Errorf("%s: no git remote to push to", ErrRefused)
	}
	return r.run(ctx, dir, step.Args(), stdout, stderr)
}

// dirtyPaths returns the paths git reports as changed, relative to the
// repository root. It is the dirty-tree check's question, asked of git rather
// than of a file walk, because git is the thing that decides what a commit
// would carry.
func (r *ArgoRunner) dirtyPaths(ctx context.Context, dir string) ([]string, error) {
	out, err := gitOutput(dir, "status", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("caf deploy: read the working tree: %w", err)
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// porcelain is "XY path" or "XY orig -> renames"; the path is the last
		// field, and a rename's second path is the one that would be committed.
		fields := strings.Fields(line)
		path := fields[len(fields)-1]
		paths = append(paths, path)
	}
	return paths, nil
}

// syncWait polls the Application's status until Argo has synced the revision
// caf committed and the service is healthy, or fails with the status it
// stopped at.
//
// # Why it refuses to claim a deploy that leaves targetRevision behind
//
// The deploy lever is the chart version: caf bumps it, commits, and pushes.
// The Application's targetRevision must then move to the new version — a
// second commit in kit, or an operator action, which caf does not make. So
// the wait is for the Application's SYNCED revision to match the version caf
// committed. Until it does, Argo is still running the old chart, and a deploy
// that reported success then would be a deploy that lied about what is
// running. The status it stopped at is the report: the revision Argo has
// synced, the health it reports, and the version caf expected.
func (r *ArgoRunner) syncWait(ctx context.Context, plan Plan, stdout, stderr io.Writer) error {
	step := plan.Step(SyncWait)
	if step.Argv == nil {
		return fmt.Errorf("caf deploy: the plan has no %q step, so there is nothing to wait for", SyncWait)
	}
	deadline := time.Now().Add(syncTimeoutForTests)
	asked := 0
	for {
		var out strings.Builder
		err := r.Run(ctx, plan.Dir, step, io.MultiWriter(&out, stdout), stderr)
		if err != nil {
			// An unreachable Argo is not a sync that is slow. It is a fact
			// about the cluster, and reporting it as a timeout would send an
			// operator to a clock instead of to the kubeconfig.
			return fmt.Errorf("caf deploy: Argo is not reachable, so there is no sync to wait for: %w\n"+
				"  The poll is read-only — %s — and it failed, which means the cluster\n"+
				"  is not answering. Check the kubeconfig and that the argocd namespace\n"+
				"  is the one the Application lives in.", err, step.Command())
		}
		fields := strings.Fields(out.String())
		if len(fields) >= 2 {
			revision, health := fields[0], fields[1]
			if revision == plan.Version && health == "Healthy" {
				if asked > 0 {
					fmt.Fprintf(stdout, "  Argo synced revision %s and the service is healthy, after %d check(s)\n", revision, asked+1)
				}
				return nil
			}
			if asked == 0 {
				fmt.Fprintf(stdout, "\nwaiting for Argo to sync revision %s (the Application reports %s %s)\n",
					plan.Version, revision, health)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("caf deploy: Argo never converged.\n"+
				"  The deploy committed revision %s and pushed it, but the Application's synced\n"+
				"  revision is %s (%s), which is not the version caf released.\n"+
				"  The deploy lever is the chart version: the Application's targetRevision must\n"+
				"  move to %s for Argo to sync the new revision. That is a second commit in kit,\n"+
				"  or an operator action — caf writes commits, never the cluster, and never\n"+
				"  another repository's tree. Move targetRevision, then deploy again.\n"+
				"  The status it stopped at: revision %s, health %s.",
				plan.Version, fields[0], fields[1], plan.Version, fields[0], fields[1])
		}
		asked++
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollIntervalForTests):
		}
	}
}

// run is every kubectl and git command in this engine, so the "kubectl is not
// installed" sentence and the process wiring are in one place.
func (r *ArgoRunner) run(ctx context.Context, dir string, argv []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = nil

	if err := cmd.Run(); err != nil {
		if looksMissing(err) {
			return fmt.Errorf("%w\n%s", ErrKubectlMissing, kubectlInstallNote(r.Binary))
		}
		return err
	}
	return nil
}

// ErrKubectlMissing is the sentinel for "kubectl is not installed". It is a
// sentinel rather than a string so a caller can tell this apart from a deploy
// that ran and failed, which is a different thing with a different fix.
var ErrKubectlMissing = fmt.Errorf("kubectl is not installed")

// kubectlInstallNote is what to do about a missing kubectl. It is separate from
// the Kamal engine's installNote because the two engines' missing-tool
// sentences name different tools and different fixes.
func kubectlInstallNote(binary string) string {
	return "kubectl is the deploy engine's tool, and it is not on PATH (" + binary + " could not be run).\n" +
		"  Install kubectl, and make sure the kubeconfig for the cluster is the one in\n" +
		"  this shell. The preflight is where a missing kubectl surfaces."
}

// gitOutput runs a git command and returns its stdout, trimmed. It is the
// read half of the engine's git — the half that asks questions rather than
// making changes.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func filePaths(files []File) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

// The sync-wait's cadence and budget. They are variables and not constants so
// a test can shorten them: a test that slept two minutes per poll to prove a
// timeout works is a test whose runtime is a function of how many polls it
// took. The budget is a backstop and it names the event that never arrived,
// which is the rule AGENTS.md states about deadlines.
var (
	// syncTimeout is how long the sync-wait polls before it reports that Argo
	// never converged. DEPLOYMENT.md: "push, rolling within two minutes is
	// acceptable for this buyer" — so two minutes is the budget, and a sync
	// that has not converged in that time has not converged.
	syncTimeout = 2 * time.Minute
	// syncTimeoutForTests is what a test shortens the budget to.
	syncTimeoutForTests = syncTimeout
	// pollIntervalForTests is what a test shortens the cadence to.
	pollIntervalForTests = 500 * time.Millisecond
)

// runArgoDeploy is the Argo engine's deploy: the same orchestration as the
// Kamal one — the plan's steps in order, the report — with none of the parts
// that are Kamal's. There is no config/deploy.yml to resolve, no accessory
// port to expose, no rolling rollout to wait for: the cluster is a fact
// about the Kubernetes namespace, and the only thing that changes it is the
// commit the plan carries.
func runArgoDeploy(ctx context.Context, r Runner, req Request, stdout, stderr io.Writer) error {
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
		return fmt.Errorf("caf deploy: %s is not a directory caf can read: %w", abs, err)
	}

	dep := Deployment{Dir: abs, Service: req.Service}
	plan, err := r.Plan(dep, Options{Version: req.Version})
	if err != nil {
		return err
	}

	// A dry run executes nothing — not even the preflight. The Kamal dry run
	// runs the two read-only steps because the exposure check needs what kamal
	// resolved; the Argo engine has no exposure check and no cluster to be
	// reachable from a dry run, so there is nothing a dry run would catch by
	// running, and a dry run that executes a command is a dry run that can fail
	// on a machine with no cluster.
	if req.DryRun {
		printArgoDryRun(stdout, plan)
		return nil
	}

	version, err := preflight(ctx, r, plan, stderr)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "%s: releasing %s from %s with kubectl %s\n",
		plan.Service, plan.Version, shortPath(plan.Dir), version)
	fmt.Fprintf(stdout, "  the release is a values commit: the image tag and the chart version ride\n")
	fmt.Fprintf(stdout, "  inside the OCI chart artifact, and the only lever caf has is a commit\n")

	cause := runArgoSteps(ctx, r, plan, stdout, stderr)
	if cause != nil {
		return argoFailureReport(plan, stdout, cause)
	}
	return argoSuccessReport(plan, stdout)
}

// runArgoSteps runs the plan's steps in order and stops at the first failure,
// naming the one that failed rather than returning exec's exit status.
func runArgoSteps(ctx context.Context, r Runner, plan Plan, stdout, stderr io.Writer) error {
	for _, name := range []string{Commit, Push, SyncWait} {
		step := plan.Step(name)
		if step.Argv == nil {
			return fmt.Errorf("caf deploy: the plan has no %q step, so the deploy stopped there rather than pretending", name)
		}
		fmt.Fprintf(stdout, "\nrunning %s\n", step.Command())
		if err := r.Run(ctx, plan.Dir, step, stdout, stderr); err != nil {
			return fmt.Errorf("%s failed: %w", step.Command(), err)
		}
	}
	return nil
}

// argoSuccessReport is what an Argo deploy that synced says.
//
// It names the revision Argo synced and the commit it was built from, because
// "the deploy succeeded" is a claim about a sync this package observed, and
// the revision is the proof. It also says what the deploy did NOT do — move
// the targetRevision — because that is the second move caf cannot make, and
// a report that left it out would read as a complete deploy when it is not.
func argoSuccessReport(plan Plan, stdout io.Writer) error {
	fmt.Fprintf(stdout, "\n%s: Argo synced revision %s\n", plan.Service, plan.Version)
	fmt.Fprintf(stdout, "  the values commit is pushed and the Application reports the service healthy at\n")
	fmt.Fprintf(stdout, "  the revision caf released. The commit was built from %s.\n", plan.Commit)
	fmt.Fprintf(stdout, "  what this did NOT do: move the Application's targetRevision. That is a second\n")
	fmt.Fprintf(stdout, "  commit in kit, or an operator action — caf writes commits, never the cluster,\n")
	fmt.Fprintf(stdout, "  and never another repository's tree. If the targetRevision is not at %s, the\n", plan.Version)
	fmt.Fprintf(stdout, "  sync above is Argo reporting the old chart, and the deploy is not complete.\n")
	return nil
}

// argoFailureReport is what a partial Argo deploy prints.
//
// It answers the question a failed deploy actually raises — what is the
// cluster running — by reporting the status the sync-wait stopped at, which is
// the only honest answer to a deploy that did not converge.
func argoFailureReport(plan Plan, stdout io.Writer, cause error) error {
	fmt.Fprintf(stdout, "\n%s\n", cause)
	fmt.Fprint(stdout, "\nwhat to do:\n")
	fmt.Fprint(stdout, "  the values commit is made and pushed, so the release exists on the remote and the\n")
	fmt.Fprint(stdout, "  publish workflow can push the OCI tag. What did not happen is the sync: Argo is\n")
	fmt.Fprint(stdout, "  still running the old revision, and the service is not serving the release.\n")
	fmt.Fprint(stdout, "  the reason is the command's own, above.\n")
	// ErrDeployFailed is a sentinel so the caller can tell "the report is above"
	// from "caf failed before it could report anything" — the first is errReported
	// (exit 1, stderr empty), the second is an error that belongs there.
	return fmt.Errorf("%w: %s", ErrDeployFailed, plan.Service)
}

// printArgoDryRun is the Argo engine's dry run: every step, the two files the
// commit would write, and the fact that nothing was executed.
//
// The files are printed from the Commit step's own Files, which is the same
// value a real run writes — so the document on the screen and the document on
// disk are one fact, and a dry run cannot drift from the run it describes.
func printArgoDryRun(stdout io.Writer, plan Plan) {
	fmt.Fprintf(stdout, "\nthis is what caf deploy would run:\n\n")
	for _, step := range plan.Steps {
		fmt.Fprintf(stdout, "  %s\n", step.Command())
		fmt.Fprintf(stdout, "      %s\n", step.Why)
		for _, file := range step.Files {
			fmt.Fprintf(stdout, "    and writes %s:\n%s\n", file.Path, file.Content)
		}
	}
	fmt.Fprintf(stdout, "\nDry run, and nothing was executed: not the preflight a real run makes\n")
	fmt.Fprintf(stdout, "first, and none of the steps above. No commit was made, no push happened, no\n")
	fmt.Fprintf(stdout, "file in %s was written — the two documents above are the files the commit\n", plan.Dir)
	fmt.Fprintf(stdout, "would write, printed rather than written. The sync-wait a real run makes was\n")
	fmt.Fprintf(stdout, "not made: it is a read of the Application's status, and a dry run touches no cluster.\n")
	fmt.Fprintf(stdout, "\nDeploy it for real with:\n  caf deploy --engine argo%s %s\n", yesFlag(plan), plan.Service)
}

// strconv is used by bumpPatch in plan.go; this file needs no such import.
