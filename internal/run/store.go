package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Store is a directory of runs. Two Stores over one directory are two
// drivers, and every mutation takes the store's lock so that their
// read-modify-write cycles cannot interleave — the same idiom
// internal/ledger uses, for the same reason: the arbiter has to be the
// filesystem, not a process.
type Store struct {
	dir string
	now func() time.Time
}

// Open makes a store in a directory, creating the layout. The layout is one
// file per run and a log beside it:
//
//	<dir>/runs/<id>.json          the run and its steps
//	<dir>/runs/<id>.events.jsonl  the append-only log
//	<dir>/runs/<id>.lease.json    who is driving it right now, if anybody
//	<dir>/store.lock              the lock serialising mutations
//
// It is Open and not New because opening a store that is already there is
// the normal case — the second driver opens the same directory as the first.
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("run store: the store needs a directory")
	}
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o700); err != nil {
		return nil, fmt.Errorf("run store: create %s: %w", dir, err)
	}
	return &Store{dir: dir, now: time.Now}, nil
}

// Dir is where the store is held. A report prints it, because "no run is
// recorded" and "the run was recorded somewhere else" are different
// sentences.
func (s *Store) Dir() string { return s.dir }

func (s *Store) stamp() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

// SetClock replaces the store's clock. It exists for a test and for a
// report that has to render times against a fixed instant; a lease's expiry
// is the only thing in this package that depends on time at all.
func (s *Store) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

func (s *Store) runPath(id string) string    { return filepath.Join(s.dir, "runs", id+".json") }
func (s *Store) eventsPath(id string) string { return filepath.Join(s.dir, "runs", id+".events.jsonl") }
func (s *Store) leasePath(id string) string  { return filepath.Join(s.dir, "runs", id+".lease.json") }

// validRunID is the guard on a caller-chosen name becoming a path. A run id
// with a separator in it would write outside the runs directory, and a store
// that let a caller do that has given every caller a write to the box.
func validRunID(id string) error {
	switch {
	case id == "":
		return errors.New("run store: a run needs an id")
	case id == "." || id == "..":
		return fmt.Errorf("run store: %q is not a run id", id)
	case strings.ContainsAny(id, "/\\\x00"):
		return fmt.Errorf("run store: %q is not a run id: it contains a path separator", id)
	}
	return nil
}

// Create records a run and its whole step list, every step pending.
//
// It is idempotent, and the idempotence is exact: a second Create with the
// same step list returns the stored run untouched, and a second Create with
// a DIFFERENT step list is refused. A run's steps are its identity — the
// thing a resume compares against — so redefining them quietly would be the
// one mutation that could make a resume point mean nothing.
//
// maxAttempts is how many attempts a run gets before it is dead-lettered;
// zero or less takes the default of three, which is the prior art's number.
func (s *Store) Create(id string, steps []string, maxAttempts int) (*Run, error) {
	if err := validRunID(id); err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("run store: run %s needs at least one step", id)
	}
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	seen := map[string]bool{}
	for _, k := range steps {
		if strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("run store: run %s has a step with no key", id)
		}
		if seen[k] {
			return nil, fmt.Errorf("run store: run %s names step %q twice", id, k)
		}
		seen[k] = true
	}

	release, err := s.hold()
	if err != nil {
		return nil, err
	}
	defer func() { _ = release() }()

	existing, err := s.load(id)
	switch {
	case err == nil:
		if strings.Join(existing.Keys(), "\x00") != strings.Join(steps, "\x00") {
			return nil, fmt.Errorf("run store: run %s already exists with steps %v; this Create named %v, and a run's steps are what a resume is measured against — start a new run rather than redefining this one", id, existing.Keys(), steps)
		}
		return existing, nil
	case !errors.Is(err, ErrNoSuchRun):
		return nil, err
	}

	now := s.stamp()
	run := &Run{
		ID:          id,
		Status:      RunQueued,
		Attempt:     0,
		MaxAttempts: maxAttempts,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	for _, k := range steps {
		run.Steps = append(run.Steps, Step{Key: k, State: StepPending})
	}
	if err := s.save(run); err != nil {
		return nil, err
	}
	if _, err := s.appendEvent(id, EventRunCreated, "", run.ResumeKey(), "steps: "+strings.Join(steps, ", ")); err != nil {
		return nil, err
	}
	return run, nil
}

// Get reads a run. A run the store has never seen is ErrNoSuchRun, and the
// sentence names the directory, because "no such run" and "the store is
// somewhere else" reach a person as the same problem.
func (s *Store) Get(id string) (*Run, error) {
	release, err := s.hold()
	if err != nil {
		return nil, err
	}
	defer func() { _ = release() }()
	return s.load(id)
}

// Claim takes the run for one driver, for one lease.
//
// The rules, and each is a refusal rather than a warning:
//
//   - A finished run is refused. Its steps are history and no driver may add
//     to them.
//   - A run another driver holds with a live lease is refused with ErrHeld,
//     naming the holder and the expiry. Two drivers, one winner.
//   - A run whose lease has expired — because its driver crashed, or because
//     it is alive and wedged — is free, and the claim mints a new epoch. The
//     previous holder's lease object is dead from that moment: its writes are
//     refused, so it cannot mark a step done on top of the driver that took
//     over. That is what makes "one winner" mean something after the claim.
//   - The SAME holder claiming again while its lease is live is a renewal:
//     the epoch is kept and the expiry moves. A driver that is working
//     should renew rather than let its lease lapse, because a lapsed lease
//     is a lease anybody may take.
//
// The holder names one driver and must be unique among the drivers of one
// run — a pid, a session id, a uuid. Two drivers sharing a name share a
// lease, which is the caller's defect and not the store's to detect.
func (s *Store) Claim(id, holder string, lease time.Duration) (*Lease, *Run, error) {
	if err := validRunID(id); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(holder) == "" {
		return nil, nil, fmt.Errorf("run store: claiming run %s needs a holder: a name unique to this driver, so an expired lease can be told apart from a live one", id)
	}
	if lease <= 0 {
		return nil, nil, fmt.Errorf("run store: claiming run %s needs a positive lease", id)
	}

	release, err := s.hold()
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = release() }()

	run, err := s.load(id)
	if err != nil {
		return nil, nil, err
	}
	if run.Status.Terminal() {
		return nil, nil, fmt.Errorf("%w: run %s is %s", ErrRunFinished, id, run.Status)
	}

	now := s.stamp()
	existing, held, err := s.loadLease(id)
	if err != nil {
		return nil, nil, err
	}
	epoch := int64(1)
	renewed := false
	if held {
		if existing.live(now) && existing.Holder != holder {
			return nil, nil, fmt.Errorf("%w: run %s is held by %s until %s", ErrHeld, id, existing.Holder, existing.ExpiresAt.UTC().Format(time.RFC3339))
		}
		epoch = existing.Epoch
		if existing.live(now) && existing.Holder == holder {
			renewed = true // the holder is still the holder: keep its epoch
		} else {
			epoch++ // a dead or superseded lease never mints a second epoch-1
		}
	}

	rec := leaseRecord{Holder: holder, Epoch: epoch, AcquiredAt: now, ExpiresAt: now.Add(lease)}
	if err := writeAtomic(s.leasePath(id), rec); err != nil {
		return nil, nil, err
	}
	run.Status = RunRunning
	run.UpdatedAt = now
	if err := s.save(run); err != nil {
		return nil, nil, err
	}
	what := "claimed"
	if renewed {
		what = "renewed"
	}
	resume := run.ResumeKey()
	if resume == "" {
		resume = "nothing: every step is done"
	}
	detail := fmt.Sprintf("%s by %s, epoch %d, lease %s; resume at %s", what, holder, epoch, lease, resume)
	if _, err := s.appendEvent(id, EventRunClaimed, holder, run.ResumeKey(), detail); err != nil {
		return nil, nil, err
	}
	return &Lease{RunID: id, Holder: holder, Epoch: epoch, ExpiresAt: rec.ExpiresAt}, run, nil
}

// BeginStep records that a step is about to run. It is the record made
// BEFORE the work, and it is the reason a crash costs at most one step
// rather than the install: a driver that died mid-step left `running` on
// disk, and the next driver re-enters exactly there.
//
// It is idempotent in both directions a retry can arrive from. Beginning a
// step that is already running is a success that changes nothing (an agent
// re-issuing the command converges), and beginning a step that is already
// done is a success that changes nothing either — the resume point moved
// past it, and the caller is told by the run's ResumeKey, not by a refusal.
func (s *Store) BeginStep(l *Lease, key string) error {
	release, err := s.hold()
	if err != nil {
		return err
	}
	defer func() { _ = release() }()

	run, err := s.load(l.RunID)
	if err != nil {
		return err
	}
	if err := s.checkLease(l, run); err != nil {
		return err
	}
	step, ok := run.Step(key)
	if !ok {
		return fmt.Errorf("%w: run %s names no step %q; its steps are %v", ErrNoSuchStep, l.RunID, key, run.Keys())
	}
	switch step.State {
	case StepDone, StepRunning:
		return nil
	}
	now := s.stamp()
	step.State = StepRunning
	step.Attempts++
	step.StartedAt = &now
	step.LastError = ""
	run.putStep(step)
	run.UpdatedAt = now
	if err := s.save(run); err != nil {
		return err
	}
	_, err = s.appendEvent(l.RunID, EventStepBegun, l.Holder, key, fmt.Sprintf("attempt %d", step.Attempts))
	return err
}

// CompleteStep records that a step finished. It is the record made AFTER the
// work, and it is what makes the resume point advance.
//
// Completing the last incomplete step succeeds the run and drops its lease:
// there is nothing left that could make a fully-done run not-succeeded, so
// the two facts are written as one. Completing a step that is not running is
// refused — a completion of a step that was never begun is a driver that
// skipped the record this model exists for, and it must not be able to
// write history backwards. Completing a step twice is a no-op.
func (s *Store) CompleteStep(l *Lease, key string) error {
	release, err := s.hold()
	if err != nil {
		return err
	}
	defer func() { _ = release() }()

	run, err := s.load(l.RunID)
	if err != nil {
		return err
	}
	if err := s.checkLease(l, run); err != nil {
		return err
	}
	step, ok := run.Step(key)
	if !ok {
		return fmt.Errorf("%w: run %s names no step %q; its steps are %v", ErrNoSuchStep, l.RunID, key, run.Keys())
	}
	if step.State == StepDone {
		return nil
	}
	if step.State != StepRunning {
		return fmt.Errorf("run %s: step %q is %s and was never begun; a step is recorded before it runs, so call BeginStep first — a completion that skips it is a completion of work nobody can point at", l.RunID, key, step.State)
	}
	now := s.stamp()
	step.State = StepDone
	step.FinishedAt = &now
	run.putStep(step)
	run.UpdatedAt = now
	succeeded := run.Done()
	if succeeded {
		run.Status = RunSucceeded
	}
	if err := s.save(run); err != nil {
		return err
	}
	if _, err := s.appendEvent(l.RunID, EventStepDone, l.Holder, key, ""); err != nil {
		return err
	}
	if !succeeded {
		return nil
	}
	if _, err := s.appendEvent(l.RunID, EventRunSucceeded, l.Holder, "", "every step is done"); err != nil {
		return err
	}
	// The lease goes with the finished run: nobody may claim it and nobody
	// may write it, so the record of who held it is history rather than a
	// lock. It is dropped quietly — the success line above already says the
	// run is over, and a separate "released" line after it would be a line
	// about a lock nobody was waiting on.
	return s.dropLease(l, false)
}

// FailStep records that a step ran and did not succeed.
//
// The run's attempt count moves, and what happens next is arithmetic rather
// than a judgement: under MaxAttempts the run is handed back as queued for
// another driver with the failed step still named, and at MaxAttempts it is
// dead-lettered as failed. Backoff between attempts is deliberately NOT in
// this package — the driver owns the delay, and a store that slept would be
// a store that hung the resume.
//
// A step that is done cannot be failed: history is append-only, and the one
// mutation that would rewrite it is refused here by name.
func (s *Store) FailStep(l *Lease, key, code string) error {
	release, err := s.hold()
	if err != nil {
		return err
	}
	defer func() { _ = release() }()

	run, err := s.load(l.RunID)
	if err != nil {
		return err
	}
	if err := s.checkLease(l, run); err != nil {
		return err
	}
	step, ok := run.Step(key)
	if !ok {
		return fmt.Errorf("%w: run %s names no step %q; its steps are %v", ErrNoSuchStep, l.RunID, key, run.Keys())
	}
	if step.State == StepDone {
		return fmt.Errorf("run %s: step %q is done and a done step stays done; the run's history is append-only, so record the failure on the step that failed or start a new run", l.RunID, key)
	}
	if step.State != StepRunning {
		return fmt.Errorf("run %s: step %q is %s and was never begun; call BeginStep first — including for a step that is about to fail, because the record of having tried it is the thing a retry resumes from", l.RunID, key, step.State)
	}
	now := s.stamp()
	step.State = StepFailed
	step.FinishedAt = &now
	if code != "" {
		step.LastError = code
	}
	run.putStep(step)
	run.Attempt++
	run.LastError = code
	run.UpdatedAt = now

	requeued := run.Attempt < run.MaxAttempts
	if requeued {
		run.Status = RunQueued
	} else {
		run.Status = RunFailed
	}
	if err := s.save(run); err != nil {
		return err
	}
	if _, err := s.appendEvent(l.RunID, EventStepFailed, l.Holder, key, code); err != nil {
		return err
	}
	if requeued {
		if _, err := s.appendEvent(l.RunID, EventRunRequeued, l.Holder, run.ResumeKey(),
			fmt.Sprintf("attempt %d of %d; the lease is released and the next claim resumes at %s", run.Attempt, run.MaxAttempts, run.ResumeKey())); err != nil {
			return err
		}
	} else if _, err := s.appendEvent(l.RunID, EventRunFailed, l.Holder, key,
		fmt.Sprintf("attempt %d of %d; dead-lettered", run.Attempt, run.MaxAttempts)); err != nil {
		return err
	}
	// The failing driver's lease goes with the failure. This is the prior
	// art's release-on-failure, and it is what makes a retry converge: the
	// next driver claims immediately rather than waiting out the dead
	// holder's expiry. Quietly, because the two lines above already say the
	// run was handed back.
	return s.dropLease(l, false)
}

// Release lets a lease go on purpose. It is not a failure and it is not an
// abort: the run stays exactly where it is, with whatever steps are done
// done, and the next driver claims it and resumes at the first incomplete
// one. A driver shutting down cleanly calls this; a driver that crashed
// cannot, which is what the expiry is for.
//
// A lease that has already been taken over is refused rather than dropped:
// the release of a stale lease would delete the live holder's record and
// hand the run to anybody, which is the one thing this package must never
// do.
func (s *Store) Release(l *Lease) error {
	release, err := s.hold()
	if err != nil {
		return err
	}
	defer func() { _ = release() }()

	if _, err := s.load(l.RunID); err != nil {
		return err
	}
	return s.dropLease(l, true)
}

// dropLease removes the lease record, but only when it still names this
// lease's holder and epoch. It is the one deletion in this package and it is
// guarded for the reason Release's comment gives. The record flag is false
// for the drops that a louder line has already announced — a finished run and
// a failed one — so the log does not carry a line about a lock nobody was
// waiting on.
func (s *Store) dropLease(l *Lease, record bool) error {
	rec, held, err := s.loadLease(l.RunID)
	if err != nil {
		return err
	}
	if held && (rec.Holder != l.Holder || rec.Epoch != l.Epoch) {
		return fmt.Errorf("%w: run %s has been claimed by %s at epoch %d; releasing the lease held by %s at epoch %d would hand the run to anybody", ErrStale, l.RunID, rec.Holder, rec.Epoch, l.Holder, l.Epoch)
	}
	if err := removeIfPresent(s.leasePath(l.RunID)); err != nil {
		return err
	}
	if !held || !record {
		return nil // already released, or a drop a louder line announced
	}
	_, err = s.appendEvent(l.RunID, EventLeaseReleased, l.Holder, "", fmt.Sprintf("epoch %d released", l.Epoch))
	return err
}

// checkLease is the fencing check every write goes through. The lease object
// a driver holds is only a claim about the world; the record on disk is the
// world. A write whose epoch the store no longer honours is refused, and the
// sentence names the holder that does, so the refused driver knows who to
// leave alone.
func (s *Store) checkLease(l *Lease, run *Run) error {
	if l == nil {
		return fmt.Errorf("run store: a write needs the lease its claim returned")
	}
	if run.Status.Terminal() {
		return fmt.Errorf("%w: run %s is %s", ErrRunFinished, l.RunID, run.Status)
	}
	rec, held, err := s.loadLease(l.RunID)
	if err != nil {
		return err
	}
	now := s.stamp()
	switch {
	case !held:
		return fmt.Errorf("%w: run %s has no live lease; it was released or finished, so claim it again rather than writing to it", ErrStale, l.RunID)
	case rec.Epoch != l.Epoch || rec.Holder != l.Holder:
		return fmt.Errorf("%w: run %s is held by %s at epoch %d, and this lease is %s at epoch %d; the later claim won and the earlier driver must not write", ErrStale, l.RunID, rec.Holder, rec.Epoch, l.Holder, l.Epoch)
	case !rec.live(now):
		return fmt.Errorf("%w: the lease held by %s on run %s expired at %s; claim the run again rather than writing to it", ErrStale, l.Holder, l.RunID, rec.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return nil
}

// HolderOf reads the lease a run is currently under, for a report that has to
// say who is driving it. It is a read and never a decision: nothing in this
// package consults it to decide whether a lease is held — the record's
// expiry does that, inside the store lock.
func (s *Store) HolderOf(id string) (*Lease, bool, error) {
	release, err := s.hold()
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = release() }()
	rec, held, err := s.loadLease(id)
	if err != nil || !held {
		return nil, false, err
	}
	return &Lease{RunID: id, Holder: rec.Holder, Epoch: rec.Epoch, ExpiresAt: rec.ExpiresAt}, true, nil
}

// --- the run document ------------------------------------------------------

func (s *Store) load(id string) (*Run, error) {
	raw, err := readFile(s.runPath(id))
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("%w: no run %s in %s", ErrNoSuchRun, id, s.dir)
	}
	var run Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, fmt.Errorf("run store: the run %s at %s is not readable: %w; remove it and start the run again", id, s.runPath(id), err)
	}
	if run.ID == "" || len(run.Steps) == 0 {
		return nil, fmt.Errorf("run store: the run %s at %s records no steps; remove it and start the run again", id, s.runPath(id))
	}
	return &run, nil
}

func (s *Store) save(run *Run) error {
	return writeAtomic(s.runPath(run.ID), run)
}

// putStep replaces one step in place. The steps keep their order: a run's
// step list is the sequence of the install, and a reordering would be a
// second plan.
func (r *Run) putStep(step Step) {
	for i := range r.Steps {
		if r.Steps[i].Key == step.Key {
			r.Steps[i] = step
			return
		}
	}
	r.Steps = append(r.Steps, step)
}

func (s *Store) loadLease(id string) (leaseRecord, bool, error) {
	raw, err := readFile(s.leasePath(id))
	if err != nil {
		return leaseRecord{}, false, err
	}
	if raw == nil {
		return leaseRecord{}, false, nil
	}
	var rec leaseRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return leaseRecord{}, false, fmt.Errorf("run store: the lease on run %s at %s is not readable: %w; remove it once no driver is running, and the run becomes claimable", id, s.leasePath(id), err)
	}
	return rec, true, nil
}
