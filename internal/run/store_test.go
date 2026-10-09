package run

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// events are append-only
// ---------------------------------------------------------------------------

// TestEventsAreAppendOnly is the proof that the log is a log and not a
// document. Every operation appends, so the line count only ever grows; the
// bytes on disk after a sequence of operations begin with the bytes that were
// there before it; and the sequence numbers are contiguous, so a missing line
// is a visible gap rather than a silent one.
func TestEventsAreAppendOnly(t *testing.T) {
	store, at := openStore(t)
	const id = "box-11111111-2222-3333-4444-555555555555"
	steps := []string{"substrate", "data", "stack"}
	if _, err := store.Create(id, steps, 0); err != nil {
		t.Fatalf("create the run: %v", err)
	}

	ops := []struct {
		name string
		call func(*Store, *Lease)
	}{
		{"claim", func(s *Store, l *Lease) {}},
	}
	_ = ops

	// The sequence, with a line-count reading after every step. A rewrite, a
	// reorder or a delete anywhere in this package would show up as a count
	// that went backwards or a prefix that stopped matching.
	lease, _, err := store.Claim(id, driverA, testLease)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.BeginStep(lease, "substrate"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := store.CompleteStep(lease, "substrate"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := store.Release(lease); err != nil {
		t.Fatalf("release: %v", err)
	}

	mid, err := os.ReadFile(filepath.Join(store.Dir(), "runs", id+".events.jsonl"))
	if err != nil {
		t.Fatalf("read the log file: %v", err)
	}
	midLines := len(strings.Split(strings.TrimRight(string(mid), "\n"), "\n"))

	// A second driver, a lease, and the rest of the install.
	other := reopen(t, store, at)
	lease2, _, err := other.Claim(id, driverB, testLease)
	if err != nil {
		t.Fatalf("the second driver claims: %v", err)
	}
	driveFrom(t, other, lease2, at)

	final, err := os.ReadFile(filepath.Join(store.Dir(), "runs", id+".events.jsonl"))
	if err != nil {
		t.Fatalf("read the log file again: %v", err)
	}
	if !strings.HasPrefix(string(final), string(mid)) {
		t.Fatalf("the log was rewritten, not appended:\nbefore:\n%s\nafter:\n%s", mid, final)
	}
	finalLines := len(strings.Split(strings.TrimRight(string(final), "\n"), "\n"))
	if finalLines <= midLines {
		t.Errorf("the log grew from %d lines to %d; appending only ever adds", midLines, finalLines)
	}

	// Sequence numbers contiguous from one, and Events() is exactly the file.
	events, err := store.Events(id)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	if len(events) != finalLines {
		t.Errorf("Events() reports %d of %d lines", len(events), finalLines)
	}
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d; the sequence must be contiguous so a gap is visible", i, e.Seq)
		}
		if e.Kind == "" || e.RunID != id {
			t.Errorf("event %d is malformed: %+v", i, e)
		}
	}
}

// ---------------------------------------------------------------------------
// a failed step is retried, and then dead-lettered
// ---------------------------------------------------------------------------

// TestAFailedStepIsRetriedUntilTheAttemptsRunOut is the resume contract's
// other half: a step that ran and failed is re-run by the next driver, the
// run's attempt count moves, and the arithmetic — not a judgement call —
// decides between another attempt and a dead letter.
func TestAFailedStepIsRetriedUntilTheAttemptsRunOut(t *testing.T) {
	cases := []struct {
		name        string
		maxAttempts int
		failures    int
		wantStatus  RunStatus
	}{
		{name: "one failure and then it works", maxAttempts: 3, failures: 1, wantStatus: RunSucceeded},
		{name: "attempts exhausted", maxAttempts: 3, failures: 3, wantStatus: RunFailed},
		{name: "no retries configured", maxAttempts: 1, failures: 1, wantStatus: RunFailed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := openStore(t)
			const id = "box-11111111-2222-3333-4444-555555555555"
			steps := []string{"substrate", "data"}
			if _, err := store.Create(id, steps, tc.maxAttempts); err != nil {
				t.Fatalf("create the run: %v", err)
			}

			const failure = "postgres-unreachable"
			lease, _, err := store.Claim(id, driverA, testLease)
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			if err := store.BeginStep(lease, "substrate"); err != nil {
				t.Fatalf("begin substrate: %v", err)
			}
			if err := store.CompleteStep(lease, "substrate"); err != nil {
				t.Fatalf("complete substrate: %v", err)
			}

			failed := 0
			for attempt := 1; attempt <= tc.maxAttempts; attempt++ {
				if attempt > 1 {
					// The retry is a new claim by a new driver: the failed
					// attempt released the lease, which is what makes the
					// retry converge rather than deadlock behind a dead holder.
					lease, _, err = store.Claim(id, driverB, testLease)
					if err != nil {
						t.Fatalf("attempt %d claims: %v", attempt, err)
					}
				}
				run, err := store.Get(id)
				if err != nil {
					t.Fatalf("read the run: %v", err)
				}
				if run.ResumeKey() != "data" {
					t.Fatalf("attempt %d resumes at data, got %q", attempt, run.ResumeKey())
				}
				if err := store.BeginStep(lease, "data"); err != nil {
					t.Fatalf("attempt %d begins data: %v", attempt, err)
				}
				if attempt <= tc.failures {
					if err := store.FailStep(lease, "data", failure); err != nil {
						t.Fatalf("attempt %d fails data: %v", attempt, err)
					}
					failed++
					continue
				}
				if err := store.CompleteStep(lease, "data"); err != nil {
					t.Fatalf("attempt %d completes data: %v", attempt, err)
				}
				break
			}

			run, err := store.Get(id)
			if err != nil {
				t.Fatalf("read the run: %v", err)
			}
			if run.Status != tc.wantStatus {
				t.Errorf("the run is %s, want %s", run.Status, tc.wantStatus)
			}
			if run.Attempt != failed {
				t.Errorf("the run records %d attempts, want %d", run.Attempt, failed)
			}
			if tc.wantStatus == RunFailed && run.LastError != failure {
				t.Errorf("a dead-lettered run names its last error, got %q", run.LastError)
			}
			if tc.wantStatus == RunSucceeded {
				if step, _ := run.Step("data"); step.Attempts != tc.failures+1 {
					t.Errorf("data was attempted %d times, want %d", step.Attempts, tc.failures+1)
				}
			}

			events, err := store.Events(id)
			if err != nil {
				t.Fatalf("read the log: %v", err)
			}
			if got := countKind(events, EventStepFailed, ""); got != failed {
				t.Errorf("the log records %d step failures, want %d", got, failed)
			}
			// A requeued run hands its lease back; a dead-lettered one keeps
			// nothing either. Either way no stale lease survives the failure.
			if _, held, err := store.HolderOf(id); err != nil || held {
				t.Errorf("a finished or requeued run holds no lease: held=%v err=%v", held, err)
			}

			if tc.wantStatus != RunFailed {
				return
			}
			// The dead letter is terminal: no claim, and no write.
			if _, _, err := store.Claim(id, driverB, testLease); !errors.Is(err, ErrRunFinished) {
				t.Errorf("a dead-lettered run refuses a claim with ErrRunFinished, got %v", err)
			}
			last := events[len(events)-1]
			if last.Kind != EventRunFailed {
				t.Errorf("the last line is the dead letter, got %s", last.Kind)
			}
			if !strings.Contains(last.Detail, "dead-lettered") {
				t.Errorf("the dead-letter line says so, got %q", last.Detail)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// a step is recorded before it runs
// ---------------------------------------------------------------------------

// TestAStepIsRecordedBeforeItRuns is the refusal table. Every way a driver
// could write a step's history backwards — completing a step that was never
// begun, failing a step that is done, failing one that was never begun — is
// refused with the sentence that names the call it should have made.
func TestAStepIsRecordedBeforeItRuns(t *testing.T) {
	cases := []struct {
		name string
		call func(*Store, *Lease) error
		want string
	}{
		{
			name: "completing a step that was never begun",
			call: func(s *Store, l *Lease) error { return s.CompleteStep(l, "data") },
			want: "never begun",
		},
		{
			name: "failing a step that was never begun",
			call: func(s *Store, l *Lease) error { return s.FailStep(l, "data", "boom") },
			want: "never begun",
		},
		{
			name: "beginning a step the run does not name",
			call: func(s *Store, l *Lease) error { return s.BeginStep(l, "nope") },
			want: "no such step",
		},
		{
			name: "completing a step the run does not name",
			call: func(s *Store, l *Lease) error { return s.CompleteStep(l, "nope") },
			want: "no such step",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := openStore(t)
			const id = "box-11111111-2222-3333-4444-555555555555"
			if _, err := store.Create(id, []string{"substrate", "data"}, 0); err != nil {
				t.Fatalf("create the run: %v", err)
			}
			lease, _, err := store.Claim(id, driverA, testLease)
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			err = tc.call(store, lease)
			if err == nil {
				t.Fatalf("the call was refused by nothing")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal says %q, got %q", tc.want, err.Error())
			}
			// The refusal moved nothing: the run is exactly as claimed.
			run, err := store.Get(id)
			if err != nil {
				t.Fatalf("read the run: %v", err)
			}
			for _, step := range run.Steps {
				if step.State != StepPending {
					t.Errorf("step %s moved to %s on a refused call", step.Key, step.State)
				}
			}
		})
	}
}

// TestADoneStepCannotBeFailed is the one mutation that would rewrite history,
// refused by name and on its own because it is not a "never begun" case: the
// driver did the work, the work succeeded, and a later failure must not be
// able to walk it back.
func TestADoneStepCannotBeFailed(t *testing.T) {
	store, _ := openStore(t)
	const id = "box-11111111-2222-3333-4444-555555555555"
	if _, err := store.Create(id, []string{"substrate", "data"}, 3); err != nil {
		t.Fatalf("create the run: %v", err)
	}
	lease, _, err := store.Claim(id, driverA, testLease)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.BeginStep(lease, "substrate"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := store.CompleteStep(lease, "substrate"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	err = store.FailStep(lease, "substrate", "too-late")
	if err == nil {
		t.Fatal("failing a done step was refused by nothing")
	}
	if !strings.Contains(err.Error(), "done step stays done") {
		t.Errorf("the refusal says a done step stays done, got %q", err.Error())
	}
	run, err := store.Get(id)
	if err != nil {
		t.Fatalf("read the run: %v", err)
	}
	if run.Attempt != 0 {
		t.Errorf("the refused failure moved the attempt count to %d", run.Attempt)
	}
	if run.ResumeKey() != "data" {
		t.Errorf("the resume point is still data, got %q", run.ResumeKey())
	}
}

// ---------------------------------------------------------------------------
// create
// ---------------------------------------------------------------------------

// TestCreateIsIdempotentAndRefusesADifferentStepList covers the two sides of
// an idempotent create: the same list reaches the same run (and writes
// nothing), and a different list is refused, because a run's steps are what
// a resume point is measured against.
func TestCreateIsIdempotentAndRefusesADifferentStepList(t *testing.T) {
	store, _ := openStore(t)
	const id = "box-11111111-2222-3333-4444-555555555555"
	steps := []string{"substrate", "data", "stack"}

	first, err := store.Create(id, steps, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if first.MaxAttempts != 3 {
		t.Errorf("a maxAttempts of 0 takes the default of 3, got %d", first.MaxAttempts)
	}
	if _, err := store.Create(id, steps, 0); err != nil {
		t.Fatalf("create twice with the same steps: %v", err)
	}
	events, err := store.Events(id)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("the second create wrote %d events; an idempotent create writes none", len(events))
	}

	again, err := store.Get(id)
	if err != nil {
		t.Fatalf("read the run: %v", err)
	}
	if !again.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("the run's age is a fact and did not reset: %v then %v", first.CreatedAt, again.CreatedAt)
	}

	if _, err := store.Create(id, []string{"substrate", "data", "different"}, 0); err == nil {
		t.Fatal("a create with a different step list was refused by nothing")
	} else if !strings.Contains(err.Error(), "a run's steps are what a resume is measured against") {
		t.Errorf("the refusal names the reason, got %q", err.Error())
	}
}

// TestARunIDCannotEscapeTheStore is the guard on a caller-chosen name
// becoming a path. Every refused id leaves the runs directory holding
// exactly the run that was legitimately created.
func TestARunIDCannotEscapeTheStore(t *testing.T) {
	store, _ := openStore(t)
	for _, id := range []string{"", ".", "..", "a/b", "a\\b", "x\x00y"} {
		if _, err := store.Create(id, []string{"one"}, 0); err == nil {
			t.Errorf("Create(%q) was refused by nothing", id)
		}
	}
	if _, err := store.Create("good", []string{"one"}, 0); err != nil {
		t.Fatalf("a good id: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(store.Dir(), "runs"))
	if err != nil {
		t.Fatalf("read the runs directory: %v", err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	// The run and its log, and nothing else: no stray file from a refused id,
	// and no lease (nothing was ever claimed).
	sort.Strings(names)
	want := []string{"good.events.jsonl", "good.json"}
	if !equalStrings(names, want) {
		t.Errorf("the runs directory holds %v, want %v", names, want)
	}
}

// TestAnUnreadableRunIsNamedRatherThanSkipped is the failure mode that would
// silently un-resume an install: a run document that is not JSON. It is
// reported with the path, because "no such run" and "the run is damaged" have
// different remedies and only one of them is "delete it".
func TestAnUnreadableRunIsNamedRatherThanSkipped(t *testing.T) {
	store, _ := openStore(t)
	const id = "box-11111111-2222-3333-4444-555555555555"
	if _, err := store.Create(id, []string{"substrate"}, 0); err != nil {
		t.Fatalf("create: %v", err)
	}
	path := filepath.Join(store.Dir(), "runs", id+".json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("damage the run: %v", err)
	}
	_, err := store.Get(id)
	if err == nil {
		t.Fatal("a damaged run was read as nothing")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the error names the file %q, got %q", path, err.Error())
	}
	if _, _, err := store.Claim(id, driverA, testLease); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("the claim names the file too, got %v", err)
	}
}

// TestAClaimNeedsAHolderAndALease is the arity of the one operation that
// mints a lease. A holder-less claim would be a lease nobody could fence.
func TestAClaimNeedsAHolderAndALease(t *testing.T) {
	store, _ := openStore(t)
	const id = "box-11111111-2222-3333-4444-555555555555"
	if _, err := store.Create(id, []string{"substrate"}, 0); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := store.Claim(id, "  ", testLease); err == nil {
		t.Error("a claim with no holder was refused by nothing")
	}
	if _, _, err := store.Claim(id, driverA, 0); err == nil {
		t.Error("a claim with no lease was refused by nothing")
	}
	if _, _, err := store.Claim("never-created", driverA, testLease); !errors.Is(err, ErrNoSuchRun) {
		t.Errorf("claiming a run the store has never seen is ErrNoSuchRun, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// the //go:build surface
// ---------------------------------------------------------------------------

// Every lock implementation in this package has to be built by *some*
// platform. The two are named for platforms rather than for a condition, so a
// typo in a build tag would leave the package with no store lock at all — and
// every test on the host platform would still be green, because the host only
// ever compiled one of them. This is internal/ledger's own check, and it is
// repeated here because "is this file dead code" is a question about this
// package and nobody else will ask it.
func TestEveryLockImplementationIsReachableOnSomePlatform(t *testing.T) {
	if runtime.GOOS == "js" {
		t.Skip("no subprocesses on this platform")
	}
	impls := map[string]bool{}
	for _, target := range releaseTargets(t) {
		for _, name := range filesFor(t, target) {
			impls[name] = true
		}
	}
	for _, want := range []string{"lock_unix.go", "lock_other.go"} {
		if !impls[want] {
			t.Errorf("%s is not built for any release target; it is dead code that every test passes without", want)
		}
	}
}

func filesFor(t *testing.T, goos string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-f", "{{range .GoFiles}}{{.}} {{end}}{{range .IgnoredGoFiles}}{{.}} {{end}}", ".")
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH=amd64", "CGO_ENABLED=0")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list for %s: %v", goos, err)
	}
	return strings.Fields(string(out))
}

func releaseTargets(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, ".goreleaser.yml"))
	if err != nil {
		t.Fatalf("read .goreleaser.yml: %v", err)
	}
	var targets []string
	inGoos := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "goos:"):
			inGoos = true
		case inGoos && strings.HasPrefix(trimmed, "- "):
			targets = append(targets, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
		default:
			inGoos = false
		}
	}
	if len(targets) == 0 {
		t.Fatal("no goos targets found in .goreleaser.yml; this test would pass vacuously")
	}
	return targets
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for range 8 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("no go.mod in any parent directory of this package")
	return ""
}
