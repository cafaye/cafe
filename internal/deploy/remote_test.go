package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The remote engine's tests, in two halves, because the engine ships in two
// halves and the split has to be visible in the suite rather than described in
// prose:
//
//  1. THE GATE. With remoteEnabled as the tree declares it, a remote deploy is
//     refused, the refusal names TG-D1, and nothing asked the platform. That is
//     the current behaviour and the test asserts it is still the current
//     behaviour.
//
//  2. THE MECHANISM. With the flip recorded — the state after TG-D1 lands — the
//     plan is the two steps, the request is built from the plan's own argv, the
//     seam answers, and the report states what the deploy did not do.
//
// The second half drives the engine through the same RunnerFor the wiring uses,
// with the seam and the flag reached through the same door a real invocation
// uses. There is no production flag to force the engine on, so the tests carry
// their own copy of the plan and the runner — which is honest: they prove the
// mechanism, and nothing in the shipped code carries a way to bypass the gate.

// fakeRemote records what the engine asked, and answers with what the case
// planted.
type fakeRemote struct {
	account    *RemoteAccount
	deployment *RemoteDeployment
	// err is returned for whichever call should fail.
	err error

	readCalls  []string
	deployReqs []RemoteRequest
}

func (f *fakeRemote) ReadAccount(_ context.Context, accountID string) (*RemoteAccount, error) {
	f.readCalls = append(f.readCalls, accountID)
	if f.err != nil {
		return nil, f.err
	}
	return f.account, nil
}

func (f *fakeRemote) Deploy(_ context.Context, req RemoteRequest) (*RemoteDeployment, error) {
	f.deployReqs = append(f.deployReqs, req)
	if f.err != nil {
		return nil, f.err
	}
	return f.deployment, nil
}

// TestARemoteDeployIsRefusedUntilTheTenancyDecisionIsRecorded is half one, and
// it is the packet's headline: the mechanism exists and does not run.
//
// The refusal is asserted to be a REFUSAL (errors.Is ErrRefused, so the command
// exits as a refusal and not as a failed deploy) and to be the tenancy one
// (errors.Is ErrTenancyUndecided, so a later packet cannot reword it into
// something that stops being recognised).
func TestARemoteDeployIsRefusedUntilTheTenancyDecisionIsRecorded(t *testing.T) {
	runner, err := RunnerFor(EngineRemote)
	if err != nil {
		t.Fatalf("RunnerFor(%q): %v", EngineRemote, err)
	}
	dep := Deployment{Dir: ".", Service: "identity", Account: "acct-1"}
	_, err = runner.Plan(dep, Options{Version: "0.2.0"})
	if err == nil {
		t.Fatalf("the remote engine produced a plan, and it must not until TG-D1 is recorded")
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("the remote engine's refusal is not a refusal (errors.Is ErrRefused is false):\n  %v", err)
	}
	if !errors.Is(err, ErrTenancyUndecided) {
		t.Errorf("the remote engine's refusal is not the tenancy one (errors.Is ErrTenancyUndecided is false):\n  %v", err)
	}
}

// TestTheRefusalNamesTheDecisionAndTheEnoughOfIt is the sentence half: a reader
// who is told "not enabled" goes and looks, and the reader who does not look is
// why it is still broken next week. So the refusal has to carry the decision's
// id, the evidence that the mechanism is intact, and the two engines that do
// work today.
func TestTheRefusalNamesTheDecisionAndTheEnoughOfIt(t *testing.T) {
	runner, _ := RunnerFor(EngineRemote)
	_, err := runner.Plan(Deployment{Dir: ".", Service: "identity", Account: "acct-1"}, Options{})
	if err == nil {
		t.Fatal("no plan, so no refusal to read")
	}
	for _, want := range []string{
		"TG-D1",
		"TENANCY-GATE.md",
		"the mechanism is built, it is tested, and no tenancy check was weakened",
		"kamal, the default: your own box",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q; a reader sent to look would not find it:\n  %v", want, err)
		}
	}
}

// TestARemoteDeployRunsNothingWhileItIsRefused is the control half: the gate is
// not only a refusal in the plan, it is a refusal before anything at all
// happens. The seam would record a call it never received.
func TestARemoteDeployRunsNothingWhileItIsRefused(t *testing.T) {
	fake := &fakeRemote{}
	runner := &RemoteRunner{Remote: fake}
	// The RunnerFor wiring path is the one that carries the gate; a directly
	// built runner is checked too, because a constructor must not be a bypass.
	if _, err := runner.Plan(Deployment{Service: "identity", Account: "acct-1"}, Options{}); err == nil {
		t.Fatal("a directly built RemoteRunner produced a plan, so the constructor is a bypass")
	}
	err := Run(context.Background(), runner, Request{Service: "identity", Account: "acct-1", Engine: EngineRemote},
		&strings.Builder{}, &strings.Builder{})
	if err == nil {
		t.Fatal("Run succeeded through a refused engine")
	}
	if len(fake.readCalls) != 0 || len(fake.deployReqs) != 0 {
		t.Errorf("the refused engine reached the platform: %d account reads and %d requests", len(fake.readCalls), len(fake.deployReqs))
	}
}

// TestTheRefusedRemoteDeployIsToldOnStdoutAndNotOnStderr is the stream split.
// The refusal is several paragraphs; it belongs on stdout, which is where a
// machine reads a report and a person reads the explanation. stderr stays empty,
// which is what makes the command's errReported mapping true.
func TestTheRefusedRemoteDeployIsToldOnStdoutAndNotOnStderr(t *testing.T) {
	runner, _ := RunnerFor(EngineRemote)
	var stdout, stderr strings.Builder
	err := Run(context.Background(), runner, Request{Service: "identity", Account: "acct-1", Engine: EngineRemote},
		&stdout, &stderr)
	if err == nil {
		t.Fatal("Run succeeded")
	}
	if !strings.Contains(stdout.String(), "TG-D1") {
		t.Errorf("stdout does not carry the refusal's decision, so a person piping stdout gets nothing:\n%s", stdout.String())
	}
	if !strings.Contains(err.Error(), "TG-D1") {
		t.Errorf("the returned error does not carry the decision either:\n  %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr carries the refusal, which puts a second copy of the report in the place a CI log greps:\n%s", stderr.String())
	}
}

// TestAnUnknownEngineStillNamesAllThree is the flag's own contract: a typo is a
// usage error, and the answer to it now has three names rather than two.
func TestAnUnknownEngineStillNamesAllThree(t *testing.T) {
	_, err := RunnerFor("bogus")
	if err == nil {
		t.Fatal("an unknown engine resolved to a runner")
	}
	if !errors.Is(err, ErrUsage) {
		t.Errorf("an unknown engine is not a usage error (errors.Is ErrUsage is false): %v", err)
	}
	for _, want := range []string{EngineKamal, EngineArgo, EngineRemote} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the usage error does not name %q, so a reader cannot tell a typo from a spelling that changed: %v", want, err)
		}
	}
}

// --- half two: the mechanism, with the decision recorded ---------------------
//
// These tests reach the mechanism through planForRemote, the function the gated
// Plan calls once the decision is recorded. That is the smallest honest door
// into a gated engine: the gate itself is never bypassed — there is no flag, no
// environment variable and no build tag that opens it — and what is exercised is
// the code the enablement turns on rather than a test-only copy of it.
//
// Every test in this half begins by asserting the shipped gate is still closed,
// because a test that passed with the engine enabled would be testing nothing.

// enabledRemote is the post-TG-D1 state: the engine's own plan function over a
// Deployment that carries an account, and a fake seam behind the runner.
func enabledRemote(t *testing.T, remote Remote) (*RemoteRunner, Plan) {
	t.Helper()
	// The shipped gate is asserted to be closed here, on the way in.
	if remoteEnabled {
		t.Fatalf("remoteEnabled is true, so the tests below are not exercising a gated engine")
	}
	r := &RemoteRunner{Remote: remote}
	plan := planForRemote(Deployment{Dir: ".", Service: "identity", Account: "acct-1"}, Options{Version: "0.2.0"})
	return r, plan
}

// TestWhatTheRemotePlanWouldBe is the shape of the thing the enablement turns
// on, held as a test rather than as a comment: two steps, in order, each
// carrying the argv a dry run prints, and the account in that argv because
// "the platform" is not a destination until it is one account.
func TestWhatTheRemotePlanWouldBe(t *testing.T) {
	_, plan := enabledRemote(t, &fakeRemote{})
	if plan.Engine != EngineRemote {
		t.Errorf("the plan's engine is %q, not %q", plan.Engine, EngineRemote)
	}
	wantSteps := []string{Boundary, RequestStep}
	if len(plan.Steps) != len(wantSteps) {
		t.Fatalf("the plan has %d steps, not the two it should: %v", len(plan.Steps), stepNames(plan))
	}
	for i, want := range wantSteps {
		if plan.Steps[i].Name != want {
			t.Errorf("step %d is %q, not %q", i, plan.Steps[i].Name, want)
		}
	}
	// The account and the version are in the argv the dry run prints. A deploy
	// request the dry run never showed is a request nobody was shown.
	for _, step := range plan.Steps {
		if got := requestFromArgv(step.Argv).AccountID; got != "acct-1" {
			t.Errorf("step %q carries account %q, not the one the flag named", step.Name, got)
		}
		if got := requestFromArgv(step.Argv).Version; got != "0.2.0" {
			t.Errorf("step %q carries version %q, not the one the flag named", step.Name, got)
		}
		if got := requestFromArgv(step.Argv).Service; got != "identity" {
			t.Errorf("step %q carries service %q, not the one being deployed", step.Name, got)
		}
	}
}

func stepNames(p Plan) []string {
	out := make([]string, 0, len(p.Steps))
	for _, s := range p.Steps {
		out = append(out, s.Name)
	}
	return out
}

// TestTheBoundaryStepAsksAboutTheAccountItWasGiven is the first half of the
// mechanism: the request is read back out of the plan's own argv, and the
// account it carries is the account the flag named.
func TestTheBoundaryStepAsksAboutTheAccountItWasGiven(t *testing.T) {
	fake := &fakeRemote{account: &RemoteAccount{ID: "acct-1", DeploymentID: "dep-9", ServedVersion: "0.1.0"}}
	r, plan := enabledRemote(t, fake)
	var out strings.Builder
	if err := r.Run(context.Background(), plan.Dir, plan.Step(Boundary), &out, &strings.Builder{}); err != nil {
		t.Fatalf("the boundary step failed: %v", err)
	}
	if len(fake.readCalls) != 1 || fake.readCalls[0] != "acct-1" {
		t.Errorf("the boundary step asked about %v, not exactly once about acct-1", fake.readCalls)
	}
	if !strings.Contains(out.String(), "dep-9") {
		t.Errorf("the boundary step did not name the deployment serving the account:\n%s", out.String())
	}
}

// TestTheRequestStepHandsThePlatformTheAccountsDeploy is the second half: the
// app is built once and deployed once, and the account — not a per-client
// deployment — is what the request carries.
func TestTheRequestStepHandsThePlatformTheAccountsDeploy(t *testing.T) {
	fake := &fakeRemote{deployment: &RemoteDeployment{ID: "req-1", AccountID: "acct-1", Version: "0.2.0", Accepted: true}}
	r, plan := enabledRemote(t, fake)
	var out strings.Builder
	if err := r.Run(context.Background(), plan.Dir, plan.Step(RequestStep), &out, &strings.Builder{}); err != nil {
		t.Fatalf("the request step failed: %v", err)
	}
	if len(fake.deployReqs) != 1 {
		t.Fatalf("the request step made %d requests, not one", len(fake.deployReqs))
	}
	got := fake.deployReqs[0]
	if got.AccountID != "acct-1" || got.Service != "identity" || got.Version != "0.2.0" {
		t.Errorf("the request carries %+v, which is not the account's deploy for identity 0.2.0", got)
	}
}

// TestARemoteDeployWithNoAccountIsRefusedByName is the refusal that survives the
// enablement. The account is the destination, so a remote deploy without one is
// not a deploy with a default — it is a deploy to no place.
func TestARemoteDeployWithNoAccountIsRefusedByName(t *testing.T) {
	fake := &fakeRemote{}
	r := &RemoteRunner{Remote: fake}
	// The step is built by hand rather than by the gated plan, because this is
	// the shape the plan produces once the gate is open: an argv with no
	// --account in it.
	step := Step{Name: Boundary, Argv: []string{"caf", "deploy", "--engine", EngineRemote, "--yes", "identity"}}
	err := r.Run(context.Background(), ".", step, &strings.Builder{}, &strings.Builder{})
	if err == nil {
		t.Fatal("a remote deploy with no account ran")
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("the missing account is not a refusal: %v", err)
	}
	if !strings.Contains(err.Error(), AccountFlag) {
		t.Errorf("the refusal does not name the flag to pass: %v", err)
	}
	if len(fake.readCalls) != 0 {
		t.Errorf("the refusal still asked the platform: %v", fake.readCalls)
	}
}

// TestTheReportStatesWhatTheDeployDidNotDo is the report's own honesty: a taken
// request is not an applied release, and the sentence that says so is the
// difference between a deploy and a claim about one.
//
// It calls printRemoteReport directly because the full orchestration is behind
// the gate, and saying so is better than reaching around it: the report the
// enablement will print is this function, and the test proves what it says
// rather than implying it.
func TestTheReportStatesWhatTheDeployDidNotDo(t *testing.T) {
	fake := &fakeRemote{deployment: &RemoteDeployment{ID: "req-1", AccountID: "acct-1", Version: "0.2.0", Accepted: true}}
	r, plan := enabledRemote(t, fake)
	var out strings.Builder
	if err := r.Run(context.Background(), plan.Dir, plan.Step(RequestStep), &out, &strings.Builder{}); err != nil {
		t.Fatalf("the request step failed: %v", err)
	}
	for _, want := range []string{"req-1", "acct-1", "pending"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the request step's report does not say %q:\n%s", want, out.String())
		}
	}

	var report strings.Builder
	printRemoteReport(&report, plan, Deployment{Dir: ".", Service: "identity", Account: "acct-1"})
	for _, want := range []string{"what this did NOT do", "apply the release", "prove the app is answering"} {
		if !strings.Contains(report.String(), want) {
			t.Errorf("the report does not state the limits of what was done (%q):\n%s", want, report.String())
		}
	}
	if strings.Contains(report.String(), "is deployed") {
		t.Errorf("the report claims a deploy that has not happened:\n%s", report.String())
	}
}

// TestARemoteDeployDryRunRefusesToo is the order of the two gates. The tenancy
// gate is reached through Plan, which is before the dry run, so a dry run of a
// refused engine is a refusal and not a list of steps that reads like a deploy
// that works.
func TestARemoteDeployDryRunRefusesToo(t *testing.T) {
	runner, _ := RunnerFor(EngineRemote)
	var stdout strings.Builder
	err := Run(context.Background(), runner, Request{Service: "identity", Account: "acct-1", Engine: EngineRemote, DryRun: true},
		&stdout, &strings.Builder{})
	if err == nil {
		t.Fatal("a dry run of the refused engine succeeded")
	}
	if strings.Contains(stdout.String(), "this is what caf deploy would ask") {
		t.Errorf("the dry run printed the steps, so it reads as a deploy that works:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "TG-D1") {
		t.Errorf("the dry run's answer does not name the decision it is waiting on:\n%s", stdout.String())
	}
}

// TestTheRemoteRunnerHasNoRemoteByDefaultRefuses is the stub seam's own claim:
// a runner with nothing configured does not invent a destination.
func TestTheRemoteRunnerHasNoRemoteByDefaultRefuses(t *testing.T) {
	step := Step{Name: Boundary, Argv: []string{"caf", "deploy", "--engine", EngineRemote, AccountFlag, "acct-1", "--yes", "identity"}}
	r := &RemoteRunner{}
	if _, err := (StubRemote{}).ReadAccount(context.Background(), "acct-1"); err == nil {
		t.Error("the stub remote answered an account question")
	}
	err := r.Run(context.Background(), ".", step, &strings.Builder{}, &strings.Builder{})
	if err == nil {
		t.Fatal("a runner with no Remote ran anyway")
	}
}
