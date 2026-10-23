package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The Argo engine's proofs. The engine itself is argo.go; what is proved here
// is the packet's claim that a deploy under a pull-only substrate is
// reviewable with no cluster: the plan's steps and argv, the refusals, the
// dry run, and the commit half against a real local git repository, plus a
// fake Argo reader for the sync-wait's three outcomes.

// The chart facts a service repository ships, as fixtures. They are the
// generator's own output shape — `cafe gen chart` writes chart/values.yaml with
// image.repository and image.tag, and Chart.yaml with version and appVersion —
// so a fixture written any other way would be a fixture the generator cannot
// produce.
const (
	argoValues = `image:
  repository: ghcr.io/anywaye/identity
  tag: "0.1.0"
`
	argoChart = `apiVersion: v2
name: identity
version: 0.1.0
appVersion: 0.1.0
`
)

// argoDep is the Deployment the Argo plan is built over: the chart facts and
// the commit the release is built from, as readChart read them out of a real
// repository.
func argoDep() Deployment {
	return Deployment{
		Dir:           "/srv/identity",
		Service:       "identity",
		Values:        "/srv/identity/chart/values.yaml",
		Chart:         "/srv/identity/Chart.yaml",
		ValuesContent: argoValues,
		ChartContent:  argoChart,
		Image:         "ghcr.io/anywaye/identity:0.1.0",
		ChartVersion:  "0.1.0",
		Commit:        "3f9a1c2",
	}
}

// argoPlan is the plan the pure function builds over the fixture facts.
func argoPlan(version string) Plan {
	return PlanForArgo(argoDep(), Options{Version: version})
}

// argoPlanIn is the same plan against a real repository, for the proofs whose
// engine executes with the plan's directory as its working directory: a plan
// whose Dir does not exist makes the exec fail with chdir before it can fail
// with anything the case is about.
func argoPlanIn(t *testing.T, version string) Plan {
	t.Helper()
	dep := argoDep()
	dep.Dir = newArgoRepo(t)
	return PlanForArgo(dep, Options{Version: version})
}

// The Argo plan's steps, in order, and no others. The order is the engine's
// argument about what a deploy is: prove the tool, write the release into a
// commit, publish it, then wait for Argo to sync. There is no Resolve — the
// values are a file the engine read before the plan was built — and no State:
// the sync-wait already reads the Application's status, so a failure is
// reported from what it saw rather than from a second question.
func TestTheArgoPlanHasEveryStepAndNoOthers(t *testing.T) {
	plan := argoPlan("")

	want := []string{Preflight, Commit, Push, SyncWait}
	if len(plan.Steps) != len(want) {
		t.Fatalf("the plan has %d steps, want %d: %v", len(plan.Steps), len(want), plan.Steps)
	}
	for i, name := range want {
		if plan.Steps[i].Name != name {
			t.Errorf("step %d is %q, want %q", i, plan.Steps[i].Name, name)
		}
	}
	for _, absent := range []string{Resolve, State} {
		if plan.Step(absent).Argv != nil {
			t.Errorf("the Argo plan has a %q step, which is a Kamal step", absent)
		}
	}
	if plan.Engine != EngineArgo {
		t.Errorf("plan.Engine = %q, want %q", plan.Engine, EngineArgo)
	}
	if plan.Commit != argoDep().Commit {
		t.Errorf("plan.Commit = %q, want the commit the release is built from (%q)", plan.Commit, argoDep().Commit)
	}
}

// Every step's argv is the command as the operator would type it, and the
// sync-wait's is the exact read command the report names: a jsonpath read of
// the Application's synced revision and health, in the argocd namespace,
// writing nothing.
func TestEveryArgoStepHasAnArgvAndAReason(t *testing.T) {
	plan := argoPlan("")

	tests := []struct {
		name string
		argv []string
	}{
		{
			name: "preflight proves the tool, not the cluster",
			argv: []string{"kubectl", "version", "--client"},
		},
		{
			name: "commit writes the release and commits the two files by path",
			argv: []string{"git", "commit", "-m", plan.Step(Commit).Argv[3]},
		},
		{
			name: "push publishes the commit",
			argv: []string{"git", "push"},
		},
		{
			name: "sync-wait reads the Application's status and writes nothing",
			argv: []string{"kubectl", "get", "application", "identity", "-n", "argocd",
				"-o", "jsonpath={.status.sync.revision} {.status.health.status}"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step := plan.Step(stepNameFor(tt.argv))
			if step.Argv == nil {
				t.Fatalf("the plan has no step for %v", tt.argv)
			}
			if strings.Join(step.Argv, " ") != strings.Join(tt.argv, " ") {
				t.Errorf("argv = %v, want %v", step.Argv, tt.argv)
			}
			if step.Why == "" {
				t.Error("the step carries no reason, so a dry run cannot explain it")
			}
			if step.On != Always {
				t.Errorf("the step is %v, want Always — every Argo step runs on every deploy", step.On)
			}
		})
	}
}

// stepNameFor names the step an argv belongs to, the way the table above is
// keyed. The commit step is named by its own message because the message is
// the one argv that carries the release's facts inside it.
func stepNameFor(argv []string) string {
	if len(argv) < 2 {
		return ""
	}
	switch argv[0] {
	case "git":
		if argv[1] == "commit" {
			return Commit
		}
		return Push
	case "kubectl":
		if argv[1] == "get" {
			return SyncWait
		}
		return Preflight
	}
	return ""
}

// The commit step carries the two files it writes, as path and content, so the
// engine writes them and the command commits them — and a dry run prints them
// and writes nothing. The content is the release: the image tag and the chart
// version both move to the new version, and nothing else in either file is
// touched.
func TestTheArgoCommitStepCarriesTheTwoFilesItWrites(t *testing.T) {
	plan := argoPlan("")
	step := plan.Step(Commit)

	if len(step.Files) != 2 {
		t.Fatalf("the commit step carries %d files, want 2", len(step.Files))
	}

	values := step.Files[0]
	if values.Path != argoDep().Values {
		t.Errorf("values path = %q, want %q", values.Path, argoDep().Values)
	}
	if !strings.Contains(values.Content, `tag: "0.1.1"`) {
		t.Errorf("the bumped values do not carry the new tag:\n%s", values.Content)
	}
	if strings.Contains(values.Content, `tag: "0.1.0"`) {
		t.Errorf("the bumped values still carry the old tag:\n%s", values.Content)
	}
	if !strings.Contains(values.Content, "repository: ghcr.io/anywaye/identity") {
		t.Errorf("the bumped values lost the repository — a deploy must not change where the image comes from:\n%s", values.Content)
	}

	chart := step.Files[1]
	if chart.Path != argoDep().Chart {
		t.Errorf("chart path = %q, want %q", chart.Path, argoDep().Chart)
	}
	for _, want := range []string{"version: 0.1.1", "appVersion: 0.1.1"} {
		if !strings.Contains(chart.Content, want) {
			t.Errorf("the bumped chart does not carry %q:\n%s", want, chart.Content)
		}
	}
	for _, gone := range []string{"version: 0.1.0", "appVersion: 0.1.0"} {
		if strings.Contains(chart.Content, gone) {
			t.Errorf("the bumped chart still carries %q:\n%s", gone, chart.Content)
		}
	}
}

// The commit message is the release's traceability: it names the service, the
// version and the commit it was built from, so a reader can trace a running
// release back to the source it came from. It also names the second move cafe
// cannot make — the targetRevision — because a commit message that omitted it
// would read as a complete deploy when it is not.
func TestTheArgoCommitMessageNamesTheServiceTheVersionAndTheCommit(t *testing.T) {
	message := argoPlan("").Step(Commit).Argv[3]

	for _, want := range []string{
		"identity: release 0.1.1", // the service and the version
		"built from 3f9a1c2",      // the commit the release is built from
		"0.1.0",                   // the old chart version
		"targetRevision",          // the second move, named as not cafe's
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the commit message does not name %q:\n%s", want, message)
		}
	}
}

// The operator's --version outranks the derived one: a version cafe guessed is
// a version nobody can roll back to by name, so the pin wins when it is there
// and the derivation only fills the absence.
func TestAnOperatorVersionOutranksTheDerivedOne(t *testing.T) {
	plan := argoPlan("9.9.9")

	if plan.Version != "9.9.9" {
		t.Errorf("plan.Version = %q, want the operator's pin", plan.Version)
	}
	values := plan.Step(Commit).Files[0].Content
	if !strings.Contains(values, `tag: "9.9.9"`) {
		t.Errorf("the values do not carry the operator's version:\n%s", values)
	}
	if !strings.Contains(plan.Step(Commit).Argv[3], "release 9.9.9") {
		t.Errorf("the commit message does not name the operator's version:\n%s", plan.Step(Commit).Argv[3])
	}
	// The sync-wait waits for the version the operator pinned, not the derived
	// one — a wait for the wrong revision is a wait that never converges.
	if !strings.Contains(plan.Step(SyncWait).Why, "9.9.9") {
		t.Errorf("the sync-wait does not name the version it waits for:\n%s", plan.Step(SyncWait).Why)
	}
}

// The derived version bumps the patch component of a semantic version, which
// is the release cafe derives when the operator pins none. A non-semver chart
// version gets a suffix rather than a guess at a shape helm would refuse.
func TestTheDerivedVersionBumpsThePatchComponent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "a semantic version bumps its patch", in: "0.1.0", want: "0.1.1"},
		{name: "a two-component version is not semantic", in: "0.1", want: "0.1.1"},
		{name: "a non-numeric patch is not a number to bump", in: "0.1.x", want: "0.1.x.1"},
		{name: "a one-component version gets a suffix", in: "1", want: "1.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bumpPatch(tt.in); got != tt.want {
				t.Errorf("bumpPatch(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ArgoRunner satisfies the Runner seam, the way KamalRunner does: the caller
// asks for a plan and executes the answer, and never builds argv itself.
func TestArgoRunnerSatisfiesTheRunnerSeam(t *testing.T) {
	r := NewArgoRunner()
	if r == nil {
		t.Fatal("NewArgoRunner returned nil")
	}
	var _ Runner = r
}

// ResolveKubectl finds the binary on PATH, and answers the bare name when it is
// not there — an unresolved name still produces a useful error, because exec
// then reports which binary it could not find.
func TestResolveKubectlAnswersTheBinaryOnPathOrItsName(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if got := ResolveKubectl(); got != "kubectl" {
		t.Errorf("ResolveKubectl() = %q with an empty PATH, want the bare name %q", got, "kubectl")
	}

	// kubectl is installed on this machine, so the found case is the resolved
	// path; assert the function answers the path and not the bare name.
	if _, err := exec.LookPath("kubectl"); err == nil {
		if got := ResolveKubectl(); got == "kubectl" {
			t.Errorf("ResolveKubectl() = %q, want the resolved path when kubectl is on PATH", got)
		}
	}
}

// A missing kubectl is the Argo engine's shape of the missing-tool refusal:
// it is a sentinel so the caller can tell it apart from a deploy that ran and
// failed, and the sentence names the fix.
func TestAMissingKubectlSaysHowToInstallIt(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	runner := &ArgoRunner{Binary: "kubectl"}

	err := runner.Run(context.Background(), "", Step{Argv: []string{"kubectl", "version", "--client"}}, io.Discard, io.Discard)

	if !errors.Is(err, ErrKubectlMissing) {
		t.Fatalf("err = %v, want it to wrap ErrKubectlMissing", err)
	}
	if !strings.Contains(err.Error(), "kubectl") {
		t.Errorf("the sentence does not name the tool:\n%v", err)
	}
}

// readChart is the engine's refusal to deploy against a chart it cannot read
// or parse: a deploy that cannot find the image reference or the chart version
// would write a commit that changes nothing, and a commit that changes nothing
// pushes a new OCI tag that is the old chart — Argo syncs it as if it were a
// release. Each case names the file that is missing.
func TestReadChartRefusesWhenTheChartCannotBeRead(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		gitInit bool
		want    string
	}{
		{
			name:    "no chart/values.yaml",
			files:   nil,
			gitInit: true,
			want:    "chart/values.yaml",
		},
		{
			name:    "no Chart.yaml",
			files:   map[string]string{"chart/values.yaml": argoValues},
			gitInit: true,
			want:    "Chart.yaml",
		},
		{
			name: "values without an image reference",
			files: map[string]string{
				"chart/values.yaml": "image:\n  repository: ghcr.io/anywaye/identity\n",
				"Chart.yaml":        argoChart,
			},
			gitInit: true,
			want:    "image.repository and image.tag",
		},
		{
			name: "chart without a version",
			files: map[string]string{
				"chart/values.yaml": argoValues,
				"Chart.yaml":        "apiVersion: v2\nname: identity\n",
			},
			gitInit: true,
			want:    "version",
		},
		{
			name: "not a git repository",
			files: map[string]string{
				"chart/values.yaml": argoValues,
				"Chart.yaml":        argoChart,
			},
			gitInit: false,
			want:    "not a git repository",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.gitInit {
				git(t, dir, "init")
			}
			writeFiles(t, dir, tt.files)

			_, err := readChart(dir)
			if err == nil {
				t.Fatalf("readChart succeeded, want a refusal naming %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to name %q", err, tt.want)
			}
		})
	}
}

// The commit half, against a real local git repository in a temp dir: git
// init, commit the chart the way the generator shipped it, run the plan's
// Commit step, and assert the diff and the message. This is the proof that
// needs no cluster — the release is a commit, and a commit is assertable.
func TestTheCommitStepWritesTheReleaseIntoARealRepository(t *testing.T) {
	dir := newArgoRepo(t)
	runner := NewArgoRunner()

	plan, err := runner.Plan(Deployment{Dir: dir, Service: "identity"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	step := plan.Step(Commit)
	if step.Argv == nil {
		t.Fatal("the plan has no commit step")
	}

	if err := runner.Run(context.Background(), dir, step, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}

	// The commit message names the service, the version and the commit it was
	// built from — the release's traceability.
	message := git(t, dir, "log", "-1", "--format=%B")
	for _, want := range []string{"identity: release 0.1.1", "built from"} {
		if !strings.Contains(message, want) {
			t.Errorf("the commit message does not name %q:\n%s", want, message)
		}
	}

	// The diff is exactly the values bump: the image tag and the chart version
	// move, and nothing else. A diff that touched anything else would be a
	// release that changed more than the release.
	diff := git(t, dir, "show", "--format=", "--unified=0", "HEAD")
	for _, want := range []string{
		`-  tag: "0.1.0"`,
		`+  tag: "0.1.1"`,
		"-version: 0.1.0",
		"+version: 0.1.1",
		"-appVersion: 0.1.0",
		"+appVersion: 0.1.1",
	} {
		if !strings.Contains(diff, want) {
			t.Errorf("the commit diff does not carry %q:\n%s", want, diff)
		}
	}

	// The commit carries exactly the two files, because `git add` by path is
	// what keeps a release commit exactly the bump.
	names := git(t, dir, "show", "--name-only", "--format=", "HEAD")
	if len(strings.Fields(names)) != 2 {
		t.Errorf("the commit carries more than the two files:\n%s", names)
	}

	// The files on disk carry the bumped values, because the engine writes
	// them and the command commits them.
	values, err := os.ReadFile(filepath.Join(dir, "chart", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(values), `tag: "0.1.1"`) {
		t.Errorf("chart/values.yaml on disk does not carry the new tag:\n%s", values)
	}
	chart, err := os.ReadFile(filepath.Join(dir, "Chart.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(chart), "version: 0.1.1") {
		t.Errorf("Chart.yaml on disk does not carry the new version:\n%s", chart)
	}
}

// The dirty-tree refusal, and its exact boundary. The deploy writes exactly
// two files and commits exactly those two paths, so a working tree dirty
// anywhere else cannot enter the release commit — refusing on it would block
// a deploy over an edit that cannot reach the commit. A dirty chart/values.yaml
// or Chart.yaml is different: the deploy's write would mix the operator's
// uncommitted edit into the release commit, and a commit that sweeps in edits
// nobody meant is the failure this check exists to prevent.
func TestTheCommitStepRefusesADirtyWorkingTree(t *testing.T) {
	tests := []struct {
		name    string
		dirty   string
		wantErr bool
	}{
		{name: "a dirty chart/values.yaml", dirty: "chart/values.yaml", wantErr: true},
		{name: "a dirty Chart.yaml", dirty: "Chart.yaml", wantErr: true},
		{name: "a dirty unrelated file cannot enter the commit", dirty: "README.md", wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := newArgoRepo(t)
			runner := NewArgoRunner()

			plan, err := runner.Plan(Deployment{Dir: dir, Service: "identity"}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(tt.dirty)),
				[]byte("an edit nobody meant for this release\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			err = runner.Run(context.Background(), dir, plan.Step(Commit), io.Discard, io.Discard)
			if tt.wantErr {
				if !errors.Is(err, ErrRefused) {
					t.Fatalf("err = %v, want it to wrap ErrRefused", err)
				}
				// Nothing was committed: the refusal is a refusal, not a
				// warning. HEAD is still the chart the generator shipped.
				if got := git(t, dir, "log", "-1", "--format=%s"); !strings.Contains(got, "chart") {
					t.Errorf("a commit was made despite the refusal: %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("an unrelated dirty file refused the deploy: %v", err)
			}
			// The unrelated edit is still uncommitted: the release commit is
			// exactly the bump, and `git add` by path is what keeps it so.
			status := git(t, dir, "status", "--porcelain")
			if !strings.Contains(status, tt.dirty) {
				t.Errorf("the unrelated edit was swept into the release commit:\n%s", status)
			}
		})
	}
}

// A service repository with no git remote cannot be deployed: the publish
// workflow pushes the new OCI tag and Argo syncs it, and both need the commit
// on a remote. A commit that goes nowhere is a release nobody can sync.
func TestThePushStepRefusesARepositoryWithNoRemote(t *testing.T) {
	dir := newArgoRepo(t)
	runner := NewArgoRunner()

	plan, err := runner.Plan(Deployment{Dir: dir, Service: "identity"}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	err = runner.Run(context.Background(), dir, plan.Step(Push), io.Discard, io.Discard)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want it to wrap ErrRefused", err)
	}
	if !strings.Contains(err.Error(), "no git remote") {
		t.Errorf("err = %v, want it to name the missing remote", err)
	}
}

// The sync-wait's three outcomes, against a fake Argo reader: a kubectl shim
// on PATH that answers with whatever the case planted. The shim is the seam —
// the poll is a read of the Application's status, and a read is assertable
// with a script that prints, with no cluster anywhere in sight.

// The happy path: Argo has synced the revision cafe released and the service is
// healthy on the first check. The wait is then one read and a success.
func TestTheSyncWaitConvergesWhenArgoSyncedTheRevision(t *testing.T) {
	plan := argoPlanIn(t, "0.1.1")
	fakeKubectl(t, "0.1.1 Healthy", 0)
	shortenSync(t, 2*time.Second, 5*time.Millisecond)

	var out bytes.Buffer
	err := NewArgoRunner().syncWait(context.Background(), plan, &out, io.Discard)
	if err != nil {
		t.Fatalf("syncWait = %v, want nil — Argo synced the revision and the service is healthy", err)
	}
}

// A sync that is slow is not a sync that failed: the wait polls until the
// Application reports the released revision, and reports how many checks it
// took. The shim answers with the old revision twice, then the new one.
func TestTheSyncWaitConvergesAfterSeveralPolls(t *testing.T) {
	plan := argoPlanIn(t, "0.1.1")
	fakeKubectlCounted(t, 2, "0.1.0 Progressing", "0.1.1 Healthy", 0)
	shortenSync(t, 2*time.Second, 5*time.Millisecond)

	var out bytes.Buffer
	err := NewArgoRunner().syncWait(context.Background(), plan, &out, io.Discard)
	if err != nil {
		t.Fatalf("syncWait = %v, want nil — Argo converged on the third check", err)
	}
	if !strings.Contains(out.String(), "after 3 check(s)") {
		t.Errorf("the report does not name how many checks the sync took:\n%s", out.String())
	}
}

// The failure this engine exists to prevent: Argo never converges. The deploy
// committed the revision and pushed it, but the Application's synced revision
// is not the version cafe released — which means the targetRevision has not
// moved, and the report says so rather than claiming a deploy that left it
// behind. The budget is shortened so the proof does not sleep two minutes.
func TestTheSyncWaitFailsWhenArgoNeverConverges(t *testing.T) {
	plan := argoPlanIn(t, "0.1.1")
	fakeKubectl(t, "0.1.0 Progressing", 0)
	shortenSync(t, 300*time.Millisecond, 5*time.Millisecond)

	var out bytes.Buffer
	err := NewArgoRunner().syncWait(context.Background(), plan, &out, io.Discard)
	if err == nil {
		t.Fatal("syncWait succeeded against an Argo that never converged — a deploy that reports success without a sync is the one failure this engine exists to prevent")
	}
	for _, want := range []string{
		"never converged",
		"0.1.0",          // the revision Argo has synced
		"0.1.1",          // the version cafe released
		"targetRevision", // the second move, named as the cause
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
}

// An unreachable Argo is not a sync that is slow: it is a fact about the
// cluster, and reporting it as a timeout would send an operator to a clock
// instead of to the kubeconfig. The shim exits the way kubectl does when the
// cluster is not answering.
func TestTheSyncWaitFailsWhenArgoIsNotReachable(t *testing.T) {
	plan := argoPlanIn(t, "0.1.1")
	fakeKubectl(t, "The connection to the server localhost:8080 was refused", 1)
	shortenSync(t, 2*time.Second, 5*time.Millisecond)

	var out bytes.Buffer
	err := NewArgoRunner().syncWait(context.Background(), plan, &out, io.Discard)
	if err == nil {
		t.Fatal("syncWait succeeded against an unreachable Argo")
	}
	if !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("err = %v, want it to say Argo is not reachable", err)
	}
	if strings.Contains(err.Error(), "never converged") {
		t.Errorf("an unreachable Argo was reported as a slow sync — that sends the operator to a clock:\n%v", err)
	}
}

// shortenSync shortens the sync-wait's budget and poll for the tests above, and
// puts them back afterwards: a test that slept two minutes per poll to prove a
// timeout works is a test whose runtime is a function of how many polls it
// took.
func shortenSync(t *testing.T, timeout, poll time.Duration) {
	t.Helper()
	syncTimeoutForTests = timeout
	pollIntervalForTests = poll
	t.Cleanup(func() {
		syncTimeoutForTests = syncTimeout
		pollIntervalForTests = 500 * time.Millisecond
	})
}

// fakeKubectl puts a kubectl shim on PATH that answers with out and exits with
// code. It is the Argo reader the sync-wait's tests plant: the poll is a read
// of the Application's status, and a read is assertable with a script that
// prints.
func fakeKubectl(t *testing.T, out string, code int) {
	t.Helper()
	fakeKubectlCounted(t, 0, "", out, code)
}

// fakeKubectlCounted is fakeKubectl with a counter: the first `before` answers
// print `old`, every answer from then on prints `new`. A before of zero means
// every answer is `new`.
func fakeKubectlCounted(t *testing.T, before int, old, new string, code int) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
f="$(dirname "$0")/count"
n=$(cat "$f" 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > "$f"
if [ %d -gt 0 ] && [ "$n" -le %d ]; then
  printf '%%s\n' %s
else
  printf '%%s\n' %s
fi
exit %d
`, before, before, strconv.Quote(old), strconv.Quote(new), code)
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The dry run is the property that makes the whole packet reviewable without
// a cluster: it prints the exact commit — message, file, old tag to new tag —
// and the sync wait, and executes nothing. Driven through the package's own
// Run with a fake engine, so the proof covers the caller's path and not just
// the printer's.
func TestAnArgoDryRunPrintsTheExactCommitAndExecutesNothing(t *testing.T) {
	runner := newFakeArgoRunner()
	var out bytes.Buffer

	err := Run(context.Background(), runner, Request{
		Service: "identity",
		Dir:     newArgoRepo(t),
		Engine:  EngineArgo,
		DryRun:  true,
	}, &out, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"identity: release 0.1.1", // the commit message
		"built from 3f9a1c2",      // and the commit it was built from
		"chart/values.yaml",       // the two files the commit would write
		"Chart.yaml",
		`tag: "0.1.1"`, // the new tag, printed rather than written
		"kubectl get application identity -n argocd", // the sync wait
		"nothing was executed",                       // and what a dry run does not do
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the dry run does not print %q:\n%s", want, out.String())
		}
	}

	// Executes nothing: not the preflight, not the commit, not the push, not
	// the sync wait. A dry run that runs a command is a dry run that can fail
	// on a machine with no cluster.
	if len(runner.ran) != 0 {
		t.Errorf("the dry run executed %d command(s): %v", len(runner.ran), runner.ran)
	}
}

// The same proof against a real repository: the dry run prints the exact
// commit and leaves the working tree exactly as it was — the chart the
// generator shipped, unbumped and uncommitted.
func TestAnArgoDryRunAgainstARealRepositoryChangesNothing(t *testing.T) {
	dir := newArgoRepo(t)
	runner := NewArgoRunner()

	plan, err := runner.Plan(Deployment{Dir: dir, Service: "identity"}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	printArgoDryRun(&out, plan)

	if !strings.Contains(out.String(), "identity: release 0.1.1") {
		t.Errorf("the dry run does not print the commit message:\n%s", out.String())
	}
	if status := git(t, dir, "status", "--porcelain"); status != "" {
		t.Errorf("the dry run wrote to the repository:\n%s", status)
	}
	values, err := os.ReadFile(filepath.Join(dir, "chart", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(values) != argoValues {
		t.Errorf("the dry run changed chart/values.yaml:\n%s", values)
	}
}

// fakeArgoRunner is the Argo engine's test double: it plans with the pure
// production function and records what it was asked to run, so a dry run's
// "executes nothing" is an assertion about the caller's path.
type fakeArgoRunner struct {
	ran [][]string
}

func newFakeArgoRunner() *fakeArgoRunner { return &fakeArgoRunner{} }

func (f *fakeArgoRunner) Plan(dep Deployment, opts Options) (Plan, error) {
	return PlanForArgo(argoDep(), opts), nil
}

func (f *fakeArgoRunner) Run(_ context.Context, dir string, step Step, stdout, _ io.Writer) error {
	f.ran = append(f.ran, append([]string{}, step.Args()...))
	return nil
}

// Engine selection is explicit and never a silent fallback: the empty name is
// the default, which is Kamal because the fleet runs Kamal today, and an
// unknown name is a usage error — a typo in --engine is the operator invoking
// cafe wrongly, and the wrong answer to it is a deploy run against the wrong
// substrate.
func TestRunnerForSelectsTheEngineAndRefusesAnUnknownOne(t *testing.T) {
	tests := []struct {
		name    string
		engine  string
		want    string
		wantErr bool
	}{
		{name: "the empty name is the default, which is kamal", engine: "", want: EngineKamal},
		{name: "kamal is the fleet's engine", engine: "kamal", want: EngineKamal},
		{name: "argo is the launch substrate's engine", engine: "argo", want: EngineArgo},
		{name: "an unknown engine is a usage error", engine: "bogus", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := RunnerFor(tt.engine)
			if tt.wantErr {
				if !errors.Is(err, ErrUsage) {
					t.Fatalf("err = %v, want it to wrap ErrUsage", err)
				}
				if runner != nil {
					t.Errorf("RunnerFor(%q) returned a runner with an error", tt.engine)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			// The Kamal plan is pure, so the fixture facts suffice; the Argo
			// plan reads a real repository, so the argo case deploys against
			// one — a selection test that planned against a fixture the engine
			// cannot read would be testing the refusal and not the selection.
			dep := argoDep()
			if tt.want == EngineArgo {
				dep = Deployment{Dir: newArgoRepo(t), Service: "identity"}
			}
			plan, err := runner.Plan(dep, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Engine != tt.want {
				t.Errorf("the runner's plan is for engine %q, want %q", plan.Engine, tt.want)
			}
		})
	}
}

// git runs a git command in dir and returns its stdout, trimmed. It is the
// test harness's git — the same questions the engine asks, asked by the proof.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// newArgoRepo is a real local git repository with the chart committed the way
// the generator shipped it: chart/values.yaml and Chart.yaml at the version the
// chart is at before the release. It is the fixture every filesystem proof is
// built on — a deploy is a commit, and a commit needs a repository.
func newArgoRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init")
	git(t, dir, "config", "user.email", "cafe@anywaye.test")
	git(t, dir, "config", "user.name", "cafe")
	writeFiles(t, dir, map[string]string{
		"chart/values.yaml": argoValues,
		"Chart.yaml":        argoChart,
	})
	git(t, dir, "add", "chart/values.yaml", "Chart.yaml")
	git(t, dir, "commit", "-m", "chart: the values the chart ships")
	return dir
}
