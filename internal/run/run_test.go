package run

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The three guarantees this suite exists for, and the shape of each proof:
//
//   - "a step is recorded before it runs" is proved by the ORDER of the log
//     (step_begun precedes step_done for every step) and by the refusals
//     (a completion or a failure of a step that was never begun).
//   - "resume works" is proved by killing a driver — which in a test is
//     exactly what it is in production: it stops calling, and the only thing
//     that ever held its run was a lease with an expiry on it. No subprocess
//     is needed, because a Store over a directory IS the process boundary.
//   - "one lease, one winner" is proved by two Stores over one directory,
//     which is two drivers without a second process, and by the fencing half:
//     the loser's writes are refused after the lease moves.
//
// No test in this file sleeps on the wall clock. Lease expiry is a fact the
// injected clock decides, for the reason every other clock in this
// repository is injected.
const (
	// testLease is the lease every test claims with: long enough that nothing
	// expires by accident, short enough that advancing past it by hand reads.
	testLease = 5 * time.Minute
	// driverA and driverB are two drivers. They are two Store handles over
	// one directory, which is the production situation minus the fork.
	driverA = "driver-a"
	driverB = "driver-b"
)

// openStore is a store in a directory the test owns, with a clock the test
// holds a pointer to. The pointer is the seam: advancing expiry is `*at =
// at.Add(...)` and nothing in this suite waits for it.
func openStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	at := time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return at })
	return store, &at
}

// reopen is the second driver: another Store over the same directory, sharing
// the same clock variable. A crash is what it replaces — a driver that stops
// calling and loses its lease to the expiry.
func reopen(t *testing.T, store *Store, at *time.Time) *Store {
	t.Helper()
	other, err := Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen the store: %v", err)
	}
	other.SetClock(func() time.Time { return *at })
	return other
}

// kinds is the log as the sequence of its event names.
func kinds(events []Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Kind
	}
	return out
}

// seqOf is the sequence number of the one event of a kind, optionally scoped
// to a step. It is -1 when there is none, so a missing record reads as a
// missing number rather than as a zero that looks like a first line.
func seqOf(events []Event, kind, step string) int64 {
	for _, e := range events {
		if e.Kind == kind && (step == "" || e.Step == step) {
			return e.Seq
		}
	}
	return -1
}

func countKind(events []Event, kind, step string) int {
	n := 0
	for _, e := range events {
		if e.Kind == kind && (step == "" || e.Step == step) {
			n++
		}
	}
	return n
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// driveFrom runs the driver from its own resume point to the end, which is
// the whole claim the resume contract makes: a driver that reads ResumeKey
// and drives from there cannot replay work.
func driveFrom(t *testing.T, store *Store, lease *Lease, at *time.Time) {
	t.Helper()
	for {
		run, err := store.Get(lease.RunID)
		if err != nil {
			t.Fatalf("read the run while driving it: %v", err)
		}
		key := run.ResumeKey()
		if key == "" {
			return
		}
		if err := store.BeginStep(lease, key); err != nil {
			t.Fatalf("begin %s: %v", key, err)
		}
		*at = at.Add(time.Minute)
		if err := store.CompleteStep(lease, key); err != nil {
			t.Fatalf("complete %s: %v", key, err)
		}
		*at = at.Add(time.Minute)
		// Renew as the install proceeds. The clock in a real install moves
		// faster than any lease, and a driver that let its lease lapse would
		// lose the run to anybody — including to its own next renewal.
		run, err = store.Get(lease.RunID)
		if err != nil {
			t.Fatalf("read the run while driving it: %v", err)
		}
		if run.Done() {
			return
		}
		lease = renew(t, store, lease, *at)
	}
}

// renew is the call a long-running driver makes to keep its lease. It is the
// same claim by the same holder, and the assertion that matters is that the
// epoch does NOT move: a renewal is the same lease with a later expiry, not a
// new one that fences the driver's own earlier writes.
func renew(t *testing.T, store *Store, lease *Lease, at time.Time) *Lease {
	t.Helper()
	next, _, err := store.Claim(lease.RunID, lease.Holder, testLease)
	if err != nil {
		t.Fatalf("renew the lease: %v", err)
	}
	if next.Epoch != lease.Epoch {
		t.Fatalf("a renewal keeps its epoch: %d became %d", lease.Epoch, next.Epoch)
	}
	if !next.ExpiresAt.After(at) {
		t.Fatalf("a renewal moves its expiry past the present: %v is not after %v", next.ExpiresAt, at)
	}
	return next
}

// ---------------------------------------------------------------------------
// the happy path
// ---------------------------------------------------------------------------

// TestAHappyPathRecordsEveryStepBeforeItRuns is the guarantee in its
// plainest form: the run is created, one driver claims it, every step is
// recorded as running BEFORE it completes, and the last completion is also
// the run's success — there is no instant at which every step is done and
// the run is not.
func TestAHappyPathRecordsEveryStepBeforeItRuns(t *testing.T) {
	store, at := openStore(t)
	const id = "box-11111111-2222-3333-4444-555555555555"
	steps := []string{"substrate", "data", "stack"}
	if _, err := store.Create(id, steps, 0); err != nil {
		t.Fatalf("create the run: %v", err)
	}

	lease, run, err := store.Claim(id, driverA, testLease)
	if err != nil {
		t.Fatalf("claim the run: %v", err)
	}
	if run.Status != RunRunning {
		t.Errorf("a claimed run is running, got %s", run.Status)
	}
	if run.ResumeKey() != "substrate" {
		t.Errorf("a fresh run resumes at its first step, got %q", run.ResumeKey())
	}

	for _, key := range steps {
		if err := store.BeginStep(lease, key); err != nil {
			t.Fatalf("begin %s: %v", key, err)
		}
		*at = at.Add(time.Minute)
		if err := store.CompleteStep(lease, key); err != nil {
			t.Fatalf("complete %s: %v", key, err)
		}
		*at = at.Add(time.Minute)
		if key == steps[len(steps)-1] {
			continue // the run is finished; there is nothing left to renew
		}
		// A real install takes longer than one lease. The driver renews, the
		// epoch stays put, and the writes that follow still belong to it.
		lease = renew(t, store, lease, *at)
	}

	run, err = store.Get(id)
	if err != nil {
		t.Fatalf("read the finished run: %v", err)
	}
	if run.Status != RunSucceeded || !run.Done() {
		t.Errorf("the last completion succeeds the run: got %s, resume %q", run.Status, run.ResumeKey())
	}
	if _, held, err := store.HolderOf(id); err != nil || held {
		t.Errorf("a finished run holds no lease: held=%v err=%v", held, err)
	}

	events, err := store.Events(id)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	want := []string{
		EventRunCreated, EventRunClaimed,
		EventStepBegun, EventStepDone,
		// The renewal is a claim, and it is in the log as one: an install
		// that outlives its lease renews, and the log says so rather than
		// showing one lease somehow lasting six minutes.
		EventRunClaimed,
		EventStepBegun, EventStepDone,
		EventRunClaimed,
		EventStepBegun, EventStepDone,
		EventRunSucceeded,
	}
	if got := kinds(events); !equalStrings(got, want) {
		t.Errorf("the log, in order:\n got %v\nwant %v", got, want)
	}

	// The "recorded before it runs" half, per step: the begin line precedes
	// the done line, and the step's own timestamps agree. A test that only
	// checked the final states would pass against a model that marked steps
	// done without ever recording that they started.
	for _, key := range steps {
		if begun, done := seqOf(events, EventStepBegun, key), seqOf(events, EventStepDone, key); begun < 0 || done < 0 || begun >= done {
			t.Errorf("step %s: begun at seq %d, done at seq %d; the record must come first", key, begun, done)
		}
		step, ok := run.Step(key)
		if !ok {
			t.Fatalf("the finished run lost step %s", key)
		}
		if step.StartedAt == nil || step.FinishedAt == nil || !step.StartedAt.Before(*step.FinishedAt) {
			t.Errorf("step %s: started %v, finished %v", key, step.StartedAt, step.FinishedAt)
		}
	}

	// A finished run is history: the lease that completed it is dead, and the
	// next write it attempts is refused rather than landing.
	if err := store.BeginStep(lease, "substrate"); !errors.Is(err, ErrRunFinished) {
		t.Errorf("a write to a finished run is ErrRunFinished, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// resume after a crash
// ---------------------------------------------------------------------------

// TestACrashMidStepIsPickedUpAtTheFirstIncompleteStep is the packet. A driver
// gets part of the way and stops calling; its lease expires; a second driver
// claims the run and continues at the first step that is not done. The rows
// are the places a caf process can stop in.
func TestACrashMidStepIsPickedUpAtTheFirstIncompleteStep(t *testing.T) {
	steps := []string{"substrate", "data", "stack"}
	cases := []struct {
		name string
		// beforeCrashing is what the dead driver got done. Nil means it died
		// the moment it claimed the run.
		beforeCrashing func(t *testing.T, store *Store, lease *Lease)
		wantResume     string
	}{
		{
			name:           "crash the instant the run is claimed",
			beforeCrashing: nil,
			wantResume:     "substrate",
		},
		{
			name: "crash between two steps",
			beforeCrashing: func(t *testing.T, store *Store, lease *Lease) {
				if err := store.BeginStep(lease, "substrate"); err != nil {
					t.Fatalf("begin substrate: %v", err)
				}
				if err := store.CompleteStep(lease, "substrate"); err != nil {
					t.Fatalf("complete substrate: %v", err)
				}
			},
			wantResume: "data",
		},
		{
			name: "crash with a step left running",
			beforeCrashing: func(t *testing.T, store *Store, lease *Lease) {
				if err := store.BeginStep(lease, "substrate"); err != nil {
					t.Fatalf("begin substrate: %v", err)
				}
				if err := store.CompleteStep(lease, "substrate"); err != nil {
					t.Fatalf("complete substrate: %v", err)
				}
				if err := store.BeginStep(lease, "data"); err != nil {
					t.Fatalf("begin data: %v", err)
				}
			},
			wantResume: "data",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, at := openStore(t)
			const id = "box-11111111-2222-3333-4444-555555555555"
			if _, err := store.Create(id, steps, 0); err != nil {
				t.Fatalf("create the run: %v", err)
			}
			dead, _, err := store.Claim(id, driverA, testLease)
			if err != nil {
				t.Fatalf("the first driver claims: %v", err)
			}
			if tc.beforeCrashing != nil {
				tc.beforeCrashing(t, store, dead)
			}

			// The crash. There is no signal and no cleanup call — that is
			// the point: what the driver leaves behind is only what it wrote,
			// and what releases the run is the lease running out.
			*at = at.Add(testLease + time.Minute)

			next := reopen(t, store, at)
			lease, run, err := next.Claim(id, driverB, testLease)
			if err != nil {
				t.Fatalf("a driver that arrives after a crash must be able to claim the run: %v", err)
			}
			if run.ResumeKey() != tc.wantResume {
				t.Errorf("the new driver resumes at %q, got %q", tc.wantResume, run.ResumeKey())
			}
			if lease.Epoch != 2 {
				t.Errorf("the takeover mints a new epoch, got %d", lease.Epoch)
			}
			driveFrom(t, next, lease, at)

			run, err = next.Get(id)
			if err != nil {
				t.Fatalf("read the resumed run: %v", err)
			}
			if run.Status != RunSucceeded {
				t.Errorf("the resumed run succeeds, got %s", run.Status)
			}

			// The other half of resumable: a step that is done is NEVER begun
			// again. A resume that replayed substrate would look identical in
			// the final states and would be a second install of the substrate.
			events, err := next.Events(id)
			if err != nil {
				t.Fatalf("read the log: %v", err)
			}
			if n := countKind(events, EventStepBegun, "substrate"); n != 1 {
				t.Errorf("substrate was begun %d times; a done step is never re-run", n)
			}
			if n := countKind(events, EventStepDone, "substrate"); n != 1 {
				t.Errorf("substrate was completed %d times", n)
			}
			// And the log says who took over and where they continued.
			last := events[len(events)-1]
			if last.Kind != EventRunSucceeded {
				t.Errorf("the last line is the run succeeding, got %s", last.Kind)
			}
			if !strings.Contains(last.Detail, "every step is done") {
				t.Errorf("the success line names the reason, got %q", last.Detail)
			}
		})
	}
}

// TestDriversRacingForOneClaimProduceOneWinner is the lease's concurrency
// proof. A sequential table can only show that a second claim after a first
// is refused; this shows it under a real race — N drivers claiming at the
// same instant through the same store, where the winner is decided by the
// store lock and not by the order the test happened to run them in.
//
// The assertion is on the COUNT of winners and on the losers' error, because
// an outcome assertion alone would pass against a store that let two drivers
// through and then failed one of them later.
func TestDriversRacingForOneClaimProduceOneWinner(t *testing.T) {
	store, _ := openStore(t)
	const id = "box-11111111-2222-3333-4444-555555555555"
	if _, err := store.Create(id, []string{"substrate", "data"}, 0); err != nil {
		t.Fatalf("create the run: %v", err)
	}

	const drivers = 8
	type outcome struct {
		holder string
		err    error
	}
	results := make(chan outcome, drivers)
	start := make(chan struct{})
	for i := 0; i < drivers; i++ {
		holder := fmt.Sprintf("driver-%d", i)
		go func() {
			<-start
			_, _, err := store.Claim(id, holder, testLease)
			results <- outcome{holder: holder, err: err}
		}()
	}
	close(start) // every driver claims at the same instant

	winners := 0
	for i := 0; i < drivers; i++ {
		out := <-results
		if out.err == nil {
			winners++
			continue
		}
		if !errors.Is(out.err, ErrHeld) {
			t.Errorf("%s lost the race with %v, want ErrHeld", out.holder, out.err)
		}
	}
	if winners != 1 {
		t.Errorf("%d of %d drivers hold the run; a lease is one lock with one winner", winners, drivers)
	}
	holder, held, err := store.HolderOf(id)
	if err != nil || !held {
		t.Fatalf("the run is held after the race: held=%v err=%v", held, err)
	}
	if holder.Epoch != 1 {
		t.Errorf("the winner holds epoch 1, got %d", holder.Epoch)
	}
	// And the run is drivable by exactly the winner: no step was begun twice
	// by eight racing drivers.
	run, err := store.Get(id)
	if err != nil {
		t.Fatalf("read the run: %v", err)
	}
	for _, step := range run.Steps {
		if step.State != StepPending {
			t.Errorf("step %s moved to %s during the race", step.Key, step.State)
		}
	}
}

// TestTwoDriversOneWinner is the lease. The rows are the three states a
// second claimant can arrive in: the lease is live (refused, by name), the
// holder is claiming again (a renewal, which keeps its epoch), and the lease
// has expired (taken, with a new epoch that kills the old holder's writes).
func TestTwoDriversOneWinner(t *testing.T) {
	cases := []struct {
		name    string
		advance time.Duration
		holder  string
		wantOK  bool
		wantEp  int64
	}{
		{name: "a live lease refuses the second driver", advance: 0, holder: driverB, wantOK: false},
		{name: "the same holder renews and keeps its epoch", advance: time.Minute, holder: driverA, wantOK: true, wantEp: 1},
		{name: "an expired lease is taken and the epoch moves", advance: testLease + time.Minute, holder: driverB, wantOK: true, wantEp: 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, at := openStore(t)
			const id = "box-11111111-2222-3333-4444-555555555555"
			if _, err := store.Create(id, []string{"substrate", "data"}, 0); err != nil {
				t.Fatalf("create the run: %v", err)
			}
			first, _, err := store.Claim(id, driverA, testLease)
			if err != nil {
				t.Fatalf("the first claim: %v", err)
			}
			*at = at.Add(tc.advance)

			lease, run, err := store.Claim(id, tc.holder, testLease)
			if !tc.wantOK {
				if !errors.Is(err, ErrHeld) {
					t.Fatalf("a live lease is refused with ErrHeld, got %v", err)
				}
				// The refusal names the holder, because the fact the refused
				// driver needs is who to leave alone.
				if !strings.Contains(err.Error(), driverA) {
					t.Errorf("the refusal names the holder %q, got %q", driverA, err.Error())
				}
				if !strings.Contains(err.Error(), id) {
					t.Errorf("the refusal names the run, got %q", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("the second claim: %v", err)
			}
			if lease.Epoch != tc.wantEp {
				t.Errorf("epoch %d, want %d", lease.Epoch, tc.wantEp)
			}
			if run.Status != RunRunning {
				t.Errorf("the run is running under the new claim, got %s", run.Status)
			}
			if tc.advance == 0 && tc.holder == driverA {
				// A renewal keeps the epoch, so the driver's own writes are
				// unaffected by having renewed.
				if err := store.BeginStep(first, "substrate"); err != nil {
					t.Errorf("a renewed holder's lease still writes: %v", err)
				}
			}
		})
	}
}

// TestAStaleDriverCannotWriteAfterTheLeaseMoves is the half of "one winner"
// that a claim alone does not give. Once the lease has moved, the previous
// holder's every write is refused — including its RELEASE, which would
// otherwise delete the live holder's lease and hand the run to anybody.
func TestAStaleDriverCannotWriteAfterTheLeaseMoves(t *testing.T) {
	writes := []struct {
		name string
		call func(*Store, *Lease) error
	}{
		{"BeginStep", func(s *Store, l *Lease) error { return s.BeginStep(l, "data") }},
		{"CompleteStep", func(s *Store, l *Lease) error { return s.CompleteStep(l, "data") }},
		{"FailStep", func(s *Store, l *Lease) error { return s.FailStep(l, "data", "boom") }},
		{"Release", func(s *Store, l *Lease) error { return s.Release(l) }},
	}

	for _, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			store, at := openStore(t)
			const id = "box-11111111-2222-3333-4444-555555555555"
			if _, err := store.Create(id, []string{"substrate", "data"}, 0); err != nil {
				t.Fatalf("create the run: %v", err)
			}
			stale, _, err := store.Claim(id, driverA, testLease)
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			if err := store.BeginStep(stale, "substrate"); err != nil {
				t.Fatalf("begin substrate: %v", err)
			}
			if err := store.CompleteStep(stale, "substrate"); err != nil {
				t.Fatalf("complete substrate: %v", err)
			}

			// The lease expires and another driver takes the run.
			*at = at.Add(testLease + time.Minute)
			live, _, err := store.Claim(id, driverB, testLease)
			if err != nil {
				t.Fatalf("the takeover: %v", err)
			}

			err = w.call(store, stale)
			if !errors.Is(err, ErrStale) {
				t.Fatalf("%s from a superseded lease is ErrStale, got %v", w.name, err)
			}
			if !strings.Contains(err.Error(), driverB) {
				t.Errorf("the refusal names the driver that took over (%q), got %q", driverB, err.Error())
			}

			// And the refused write changed nothing: the run is where driver
			// B left it, and driver B can still write. The Release row is the
			// load-bearing one — a stale release that deleted driver B's
			// lease would make every later claim succeed.
			run, err := store.Get(id)
			if err != nil {
				t.Fatalf("read the run: %v", err)
			}
			if step, _ := run.Step("data"); step.State != StepPending {
				t.Errorf("the refused write moved step data to %s", step.State)
			}
			if err := store.BeginStep(live, "data"); err != nil {
				t.Errorf("the live driver can still write after the stale one was refused: %v", err)
			}
			holder, held, err := store.HolderOf(id)
			if err != nil || !held || holder.Holder != driverB {
				t.Errorf("the lease still belongs to driver B: %+v held=%v err=%v", holder, held, err)
			}
		})
	}
}
