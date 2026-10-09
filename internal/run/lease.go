package run

import (
	"errors"
	"fmt"
	"time"
)

// A Lease is a lock with an expiry: the right to write one run, held by one
// driver, until a moment the store wrote down.
//
// The expiry is what makes it a lease rather than a lock. A kernel lock (the
// flock in internal/ledger) is released by the kernel when its holder dies,
// which is the strongest possible liveness — but it cannot be released when
// the holder is alive and wedged, because the kernel cannot tell a wedged
// process from a working one. A lease can: after its expiry the run is
// claimable by anybody, and the wedged holder's next write is refused
// instead of landing. That is the property this model needs, because the
// failure it exists for is a box whose driver stopped answering.
//
// Epoch is the fencing token. Every successful claim mints a new one, and
// every write presents the epoch it was issued: a driver whose lease has been
// taken over holds an epoch the store no longer honours, so it cannot mark a
// step done on top of the driver that is running it now. Without it, "one
// winner" would only be true at the instant of the claim.
type Lease struct {
	RunID     string
	Holder    string
	Epoch     int64
	ExpiresAt time.Time
}

// leaseRecord is the lease as it is durably stored, which is the same four
// facts plus when it was acquired. It is a separate file from the run rather
// than a field of it, for two reasons: the claim is a create-or-replace of
// one small document, and a person looking at the directory can see who holds
// a run without reading the run.
type leaseRecord struct {
	Holder     string    `json:"holder"`
	Epoch      int64     `json:"epoch"`
	AcquiredAt time.Time `json:"acquired_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// The three refusals, as sentinels so a caller can branch on the kind rather
// than on the prose. The prose is still the thing a person reads, so each
// one is wrapped with the sentence that names the remedy.
var (
	// ErrHeld is a run another driver holds with a live lease. Nothing is
	// broken; the answer is to wait, or to leave that driver alone.
	ErrHeld = errors.New("held by another driver")
	// ErrStale is a write from a driver whose lease the store no longer
	// honours — expired, released, or taken over by a later claim. The
	// remedy is to claim again, never to write again.
	ErrStale = errors.New("stale lease")
	// ErrNoSuchRun is a run id the store has never seen.
	ErrNoSuchRun = errors.New("no such run")
	// ErrRunFinished is a write to a run that has already succeeded or been
	// dead-lettered. Its steps are history.
	ErrRunFinished = errors.New("the run is finished")
	// ErrNoSuchStep is a step key the run does not name.
	ErrNoSuchStep = errors.New("no such step")
)

// live reports whether the record is still in force at the given moment.
func (r leaseRecord) live(now time.Time) bool { return r.ExpiresAt.After(now) }

// String is the lease as one line, for a report and for the error sentence
// that refuses a stale write: it names the holder, which is the fact the
// refused driver needs in order to know who took over.
func (l *Lease) String() string {
	if l == nil {
		return "no lease"
	}
	return fmt.Sprintf("holder %s epoch %d on run %s, expires %s", l.Holder, l.Epoch, l.RunID, l.ExpiresAt.UTC().Format(time.RFC3339))
}
