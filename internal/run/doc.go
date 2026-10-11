// Package run is the resumable install model: a run, its steps, an append-only
// event log, and a lease that says who is driving.
//
// It is the port of the run/steps/events/lease model in
// refs/cleo-agentic/internal/platform/postgres/state_store.go — the shape and
// the guarantees, in Go, and not a line-by-line translation. Four guarantees,
// and every one of them is a test rather than a sentence:
//
//   - A step is RECORDED BEFORE IT RUNS. BeginStep writes `running` to disk
//     before the caller performs the work, and CompleteStep writes `done`
//     after. A driver that dies between the two leaves a step in `running`,
//     which is the one state a resume must re-enter rather than trust.
//   - AN EVENT IS APPEND-ONLY. There is no API on this package that rewrites,
//     reorders or deletes a line of the log. The log is written in the same
//     critical section as the state change it records, so it cannot disagree
//     with the run — a property the Postgres original does not have, because
//     there the event insert is a statement outside the transaction.
//   - A STEP IS RESUMABLE. A run's resume point is its first incomplete step.
//     A new driver opens the store, claims the run, reads that key and
//     continues there. Nothing replays a step that is already done.
//   - A LEASE IS A LOCK WITH AN EXPIRY. One driver holds a run at a time; a
//     second claim while the first is live is refused; a driver that dies
//     loses its lease when the expiry passes, and a driver whose lease has
//     been taken over finds its writes refused rather than silently landing
//     on top of the new holder's.
//
// WHY FILE-BACKED, AND NOT POSTGRES ON CONDUCTOR'S DATA. The prior art keeps
// this state in Postgres because that is where cleo's platform state already
// lives. The box cannot do that, and the reason is the install itself: the
// first thing a box installs is the substrate that will eventually carry
// CloudNativePG, so a state store that needs Postgres cannot record the steps
// that bring Postgres up — the store would arrive after the work it was
// supposed to record. It would also need the network, and the whole point of
// the install is that it runs on a box whose only connectivity is outbound.
// So the model lives in files, under the same directory `internal/box` keeps
// its identity and its journal in, and its whole surface is stdlib.
//
// WHAT THIS PACKAGE IS NOT. It is not the installer. kit owns the substrate
// install (W2-P4) and `cafe boxd` owns the daemon that drives it (W2-P7); what
// this package is, is the state machine they write into, so that a driver
// that arrives does not have to grow a second idea of what a step is. The
// steps are named by the caller: the box's install passes the phase table
// from internal/box, and a caller that passes something else gets a run over
// whatever it passed. A step key is a string and a run is a directory; this
// package has no opinion about what the work is.
//
// THE SEAMS. One, and it is the clock: Store.now is injected so that lease
// expiry is a fact a test decides rather than a wall-clock sleep. The other
// thing a test would need — a subprocess — is deliberately absent: every
// guarantee above is held across two Store handles over one directory, which
// is the same two-process situation with no process in it.
package run
