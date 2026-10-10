package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cafaye/caf/internal/cve"
)

// fakeObserver is the seam a test drives instead of a cluster. It answers
// with the two facts the scan cannot proceed without and records that it was
// asked exactly once, because a scan that observed the box twice is a scan
// that could disagree with itself.
type fakeObserver struct {
	components []cve.Component
	err        error
	calls      int
	phases     []string
}

func (f *fakeObserver) Observe(_ context.Context, e cve.Emitter) ([]cve.Component, error) {
	f.calls++
	f.phases = append(f.phases, "observed")
	e.Emit("observe", "the fake box")
	return f.components, f.err
}

// fakeSource answers from a table and counts its batches, so a test can assert
// that the network is bounded by a batch count rather than trusted to be.
type fakeSource struct {
	advisories []cve.Advisory
	err        error
	batches    int
	seen       []cve.Component
}

func (f *fakeSource) Advisories(_ context.Context, components []cve.Component, _ cve.Emitter) ([]cve.Advisory, error) {
	f.batches++
	f.seen = append(f.seen, components...)
	return f.advisories, f.err
}

// cveTestDeps is the half-built command a test drives: no kubectl, no network,
// no home directory.
func cveTestDeps(t *testing.T, observer cve.Observer, source cve.Source) (cveDeps, string) {
	t.Helper()
	return cveDeps{
		observer:  observer,
		source:    source,
		statePath: t.TempDir() + "/cve-state.json",
	}, ""
}

// TestBareCveAnswersRatherThanFailing is the same decision
// internal/cli/contract.go makes for a bare `caf contract`: a bare verb is a
// question, not a mistake.
func TestBareCveAnswersRatherThanFailing(t *testing.T) {
	env := newTestEnv()
	err := lookupCommandOrFail(t, "cve").Execute(env, nil)
	if err != nil {
		t.Fatalf("bare `caf cve` = %v, want the help, not a usage error", err)
	}
	for _, verb := range []string{"inventory", "scan", "schedule"} {
		if !strings.Contains(written(t, env.Stdout), verb) {
			t.Errorf("the help does not name %q:\n%s", verb, written(t, env.Stdout))
		}
	}
}

// TestAnUnknownCveVerbNamesTheVerbs is the difference between "unknown
// subcommand" and a sentence that tells the person what they could have typed.
func TestAnUnknownCveVerbNamesTheVerbs(t *testing.T) {
	env := newTestEnv()
	err := lookupCommandOrFail(t, "cve").Execute(env, []string{"audit"})
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want it to wrap errUsage", err)
	}
	for _, verb := range []string{"inventory", "scan", "schedule"} {
		if !strings.Contains(err.Error(), verb) {
			t.Errorf("the error does not name %q: %v", verb, err)
		}
	}
}

// TestCveScanEndToEnd runs the whole command through the seams and asserts
// the four facts reach the report.
//
// This is the test the packet is judged on: a command that ran and printed
// "1 advisory" would pass a smoke test and fail the brief.
func TestCveScanEndToEnd(t *testing.T) {
	observer := &fakeObserver{components: []cve.Component{
		{Name: "redis", Version: "7.4.1-alpine", Kind: cve.KindWorkload, Namespace: "data", Pinned: true, Declared: true,
			Blast: cve.Blast{Apply: "bump the tag in redis.yaml", Restarts: []string{"the redis deployment in namespace data"}}},
	}}
	source := &fakeSource{advisories: []cve.Advisory{
		{ID: "CVE-2025-1234", Component: "redis", Introduced: "1.0.0", Fixed: "7.4.2", Severity: cve.SeverityHigh,
			Summary: "a heap overflow in the reply encoder"},
	}}
	deps, _ := cveTestDeps(t, observer, source)

	env := newTestEnv()
	if err := newCveScanCommand(deps).Execute(env, nil); err != nil {
		t.Fatalf("caf cve scan: %v", err)
	}
	out := written(t, env.Stdout)
	for _, want := range []string{
		"redis 7.4.1-alpine",
		"CVE-2025-1234",
		"release 7.4.2",
		// The blast radius is the DECLARED catalogue's, not the observer's:
		// a rewrite of the tag lives in kit, and an observer that saw a
		// running pod has no opinion about which file to edit.
		"bump the image tag in manifests/data/redis.yaml",
		"the redis deployment in namespace data",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not contain %q:\n%s", want, out)
		}
	}
	if observer.calls != 1 || source.batches != 1 {
		t.Errorf("the box was observed %d times and the feed asked %d times; want 1 and 1 — a scan that observed twice could disagree with itself",
			observer.calls, source.batches)
	}
	// The state was written, which is what makes the next run a schedule
	// rather than another scan.
	state, err := cve.LoadState(deps.statePath)
	if err != nil || state.Findings != 1 {
		t.Errorf("state = %+v, err = %v; want 1 finding recorded", state, err)
	}
}

// TestCveScanJSONIsOneDocument is the repo's --json contract: one valid JSON
// document on stdout, and the human prose on stderr is a separate question.
func TestCveScanJSONIsOneDocument(t *testing.T) {
	observer := &fakeObserver{components: []cve.Component{{Name: "redis", Version: "7.4.1-alpine", Kind: cve.KindImage}}}
	source := &fakeSource{advisories: []cve.Advisory{
		{ID: "CVE-2025-1234", Component: "redis", Introduced: "1.0.0", Fixed: "7.4.2", Severity: cve.SeverityHigh},
	}}
	deps, _ := cveTestDeps(t, observer, source)
	env := newTestEnv()
	if err := newCveScanCommand(deps).Execute(env, []string{"--json"}); err != nil {
		t.Fatalf("caf cve scan --json: %v", err)
	}
	out := written(t, env.Stdout)
	if !json.Valid([]byte(out)) {
		t.Fatalf("--json printed something that is not JSON:\n%s", out)
	}
	var doc cve.Report
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Counts.Affected != 1 || len(doc.Findings) != 1 || doc.Findings[0].ReleaseTo != "7.4.2" {
		t.Errorf("the machine document = %+v, want 1 affected and a release_to", doc)
	}
}

// TestCveScanRefusesToReportOnABoxItCouldNotRead is the honest failure.
//
// An empty inventory would produce a clean report about a box nobody looked
// at, so the observer failing fails the run rather than falling through to
// "nothing to act on".
func TestCveScanRefusesToReportOnABoxItCouldNotRead(t *testing.T) {
	observer := &fakeObserver{err: errors.New("kubectl reached no cluster: no serverVersion in the answer")}
	deps, _ := cveTestDeps(t, observer, &fakeSource{})
	env := newTestEnv()
	err := newCveScanCommand(deps).Execute(env, nil)
	if err == nil {
		t.Fatal("caf cve scan succeeded against a box it could not read")
	}
	if !strings.Contains(err.Error(), "kubectl reached no cluster") {
		t.Errorf("err = %v, want the observer's own sentence rather than a paraphrase", err)
	}
	if strings.Contains(written(t, env.Stdout), "nothing to act on") {
		t.Error("a failed observation must not print a report — an unread box is not a clean box")
	}
}

// TestCveInventoryKeepsTheTwoHalvesApart is the command half of the
// declared/observed split, and it asserts the citations are in the output so
// an operator can check a pin without opening another repository.
func TestCveInventoryKeepsTheTwoHalvesApart(t *testing.T) {
	observer := &fakeObserver{components: []cve.Component{
		{Name: "k3s", Version: "v1.38.0+k3s2", Kind: cve.KindNode, Source: "observed with kubectl version"},
		{Name: "caddy", Version: "2.8.4", Kind: cve.KindImage, Namespace: "default", Source: "observed image caddy:2.8.4"},
	}}
	deps, _ := cveTestDeps(t, observer, nil)
	env := newTestEnv()
	if err := newCveInventoryCommand(deps).Execute(env, nil); err != nil {
		t.Fatalf("caf cve inventory: %v", err)
	}
	out := written(t, env.Stdout)
	if !strings.Contains(out, "kit/templates/substrate/install.sh") {
		t.Errorf("the declared half carries no citation a reader can check:\n%s", out)
	}
	if !strings.Contains(out, "caddy") || !strings.Contains(out, "2.8.4") {
		t.Errorf("the observed half does not name the undeclared component:\n%s", out)
	}
	if !strings.Contains(out, "running v1.38.0+k3s2") {
		t.Errorf("the drift is not named with both spellings:\n%s", out)
	}
}

// TestCveSchedulePrintsRatherThanInstalls is the boundary: the cron line is
// output for an operator to install, and nothing on this machine is edited.
func TestCveSchedulePrintsRatherThanInstalls(t *testing.T) {
	env := newTestEnv()
	if err := newCveScheduleCommand().Execute(env, []string{"--schedule", "1d"}); err != nil {
		t.Fatalf("caf cve schedule: %v", err)
	}
	out := written(t, env.Stdout)
	// Five cron fields, a command, and a comment. The binary path is whatever
	// the process was invoked as, which under `go test` is the test binary —
	// so this asserts the shape and the command, not the path.
	if !strings.Contains(out, "cve scan") {
		t.Errorf("the cron line does not name the command it runs:\n%s", out)
	}
	if !strings.Contains(out, "#") {
		t.Errorf("the cron line carries no reason for the reader:\n%s", out)
	}
	if strings.Contains(out, "--quiet") {
		t.Errorf("the cron line names a flag that does not exist:\n%s", out)
	}
	if strings.HasPrefix(out, "0 0 ") {
		t.Errorf("a daily schedule pins midnight (%q), which would put every box in the fleet on the feed at once", out)
	}
}

// TestCveScanIsNotDueStillAnswersAndSaysSo covers the schedule's own honesty:
// a stale answer presented as a fresh one is the defect the schedule exists to
// remove.
func TestCveScanIsNotDueStillAnswersAndSaysSo(t *testing.T) {
	observer := &fakeObserver{components: cve.Substrate}
	source := &fakeSource{}
	deps, _ := cveTestDeps(t, observer, source)
	// A box scanned a minute ago is not due, and the report must say so
	// rather than presenting a fresh-looking answer to a stale question.
	if err := cve.SaveState(deps.statePath, cve.State{Box: "box-1", LastRun: time.Now(), Findings: 2}); err != nil {
		t.Fatal(err)
	}
	env := newTestEnv()
	if err := newCveScanCommand(deps).Execute(env, []string{"--schedule", "1d"}); err != nil {
		t.Fatalf("caf cve scan: %v", err)
	}
	if !strings.Contains(written(t, env.Stdout), "next scan in") {
		t.Errorf("a run that was not due must say so:\n%s", written(t, env.Stdout))
	}
}
