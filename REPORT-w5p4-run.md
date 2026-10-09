# REPORT — W5-P4: the resumable install model (run + steps + events + lease)

**Packet:** W5-P4, `caf`. Branch `agents-first/w5p4`, worktree `caf-w5p4`.
**State:** the packet is accomplished and the gate is green. 19 packages, 906
top-level passes, 1087 subtest passes, 0 failures, 8 skips (the 8 are the live
tier and the re-exec'd child, both named at `expectedSkips`).
**Commits:** two, both `w5p4:`-prefixed — the model and its table in Q1, the
racing claim plus the floors and the layout entry in Q3.

The worktree was clean when this attempt started: the previous attempt of this
packet died to a model-endpoint failure with nothing committed, so nothing
here is inherited partial work. It is all new.

---

## What landed

| Piece | Where |
|---|---|
| The package's argument: why file-backed, the four guarantees, the one seam | `internal/run/doc.go` |
| The run, its ordered steps, `ResumeKey`, the states | `internal/run/run.go` |
| `Create`/`Claim`/`BeginStep`/`CompleteStep`/`FailStep`/`Release`, and the fencing check | `internal/run/store.go` |
| The lease: expiry, epoch, the three sentinels | `internal/run/lease.go` |
| The append-only log and its sequence numbers | `internal/run/events.go` |
| Atomic run writes, the append mode, the run-id guard | `internal/run/files.go` |
| The store lock, two build-tagged files and its own reachability test | `internal/run/lock_unix.go`, `lock_other.go` |
| 14 top-level tests / 17 subtests | `internal/run/run_test.go`, `store_test.go` |
| The floors, re-measured, and the re-lock | `gate.yml`, `caf.lock` |
| The Layout entry and the stdlib-only list | `AGENTS.md` |

## The one design decision a successor should not re-litigate

**File-backed, not Postgres on conductor's data — and the reason is the install
itself.** The prior art keeps this state in Postgres because that is where
cleo's platform state already lives. The box cannot: the first thing a box
installs is the substrate that will eventually carry CloudNativePG, so a state
store that needs Postgres could not record the steps that bring Postgres up.
The store would arrive after the work it was supposed to record. It would also
need the network, and the install runs on a box whose only connectivity is
outbound. So the model is three files per run in one directory, stdlib only,
0600 — the neighbourhood `internal/box` already keeps its identity and its
journal in.

**And the lease is a record with an expiry, not a kernel lock.** This is the
shape the brief asked for and it is also the only one that works here. A kernel
lock (the flock in `internal/ledger`) is released by the kernel when its holder
dies, which is the strongest liveness there is — but it cannot be released when
the holder is alive and wedged, because the kernel cannot tell a wedged process
from a working one. An expiry can. So the flock in `internal/run` is a
critical-section lock held for one operation and nothing else, and the lease is
a durable record: `{holder, epoch, acquired_at, expires_at}`.

The **epoch** is the half of "one winner" that a claim alone does not give.
Every successful claim mints a new one and every write presents the epoch it
was issued, so a driver whose lease has been taken over finds its writes
refused — `BeginStep`, `CompleteStep`, `FailStep` and **`Release`** alike. The
Release case is the load-bearing one and it is a test row of its own: a stale
release that deleted the live holder's record would hand the run to anybody,
which is the one thing the package must never do.

## The four guarantees, and the proof shape of each

| Guarantee | Proof |
|---|---|
| A step is recorded before it runs | The happy path asserts the log's ORDER — `step_begun` before `step_done` for every step, with the step's own timestamps agreeing — plus five refusal rows: completing or failing a step that was never begun, failing one that is done, and the two unknown-key shapes. A test that only checked final states would pass against a model that marked steps done without recording that they started. |
| Resume works | Three crash rows, one per place a caf process can stop in: the instant it claimed, between two steps, and with a step left `running`. Each asserts the new driver's resume key AND that a done step is never begun again — a resume that replayed `substrate` would look identical in the final states and would be a second install of the substrate. |
| One lease, one winner | Three claim rows (live refuses by name, same holder renews and keeps its epoch, expired is taken with a new epoch), four stale-write rows, and `TestDriversRacingForOneClaimProduceOneWinner`: eight drivers claiming one run at the same instant, asserted on the COUNT of winners. An outcome assertion would pass against a store that let two through and failed one later. |
| Events are append-only | A byte comparison — the log after more work still begins with the bytes that were there before it — plus contiguous sequence numbers, so a missing line is a visible gap. There is no API in the package that rewrites a line. |

## What "the driver died" means in a test, and why no subprocess was needed

A `Store` over a directory IS the process boundary. So the crash is exactly
what it is in production: the driver stops calling, and the only thing that
ever held its run was a lease with an expiry on it. `reopen()` hands the test a
second `Store` over the same directory sharing the same clock variable — the
second driver. Advancing `*at` past the lease is the death. No test sleeps on
the wall clock, and no test starts a container or a process. The suite is
green under `-race` as well.

## What I ruled out, and why

- **A janitor.** The prior art's `ClaimNextRunnableRun` only picks runs whose
  status is `queued`, so its `RecoverExpiredClaims` is *required* for a resume
  to happen at all — a resume that depends on a sweeper having run is a resume
  that silently does not happen. Here `Claim` takes a `running` run whose lease
  has expired directly, so the recovery is inside the one call a driver makes.
- **Backoff inside the model.** The prior art carries `next_attempt_at`. A
  store that slept would be a store that hung the resume, so the delay is the
  driver's: the model records attempts and dead-letters at `MaxAttempts`, and
  the requeue line names the step the next claim resumes at.
- **A separate `Succeed` call.** Completing the last incomplete step succeeds
  the run, because there is no state in which every step is done and the run is
  not succeeded. A separate finish call would be a call a driver could die
  before making, and it would leave a run nothing can finish.
- **CLI verbs.** `caf box run`/`resume` are not in this packet's Done means,
  the consumer is `caf boxd` (W2-P7) and kit's headless installer (W2-P5), and
  a new verb brings the whole subcommand checklist with it. The model is the
  packet; the wiring is the next one.

## Three things the tests found, all now fixed

1. **`FailStep` must release the lease.** My first version requeued the run and
   left the lease held, and the retry table went red on the second claim:
   `held by another driver`. The prior art releases on failure and it is right
   — a retry must converge rather than wait out the dead holder's expiry.
2. **A finished run's lease drop must be quiet.** Completing the last step
   emitted `step_done`, `run_succeeded`, `lease_released`, and the crash table's
   last-line assertion went red. The success line already says the run is over;
   a line about a lock nobody was waiting on after it is noise.
3. **The happy path's clock outruns the lease**, which is how the renewal path
   got exercised in the happy path rather than only in the lease table. It
   should be: an install takes longer than one lease, the driver renews, and
   the log shows the renewal as the claim it is. A renewal keeps its epoch —
   that is the assertion, because a renewal that minted a new one would fence
   the driver's own earlier writes.

## The honest limits, stated rather than hidden

- **No fsync.** `writeAtomic` renames, so a torn run document is impossible and
  a process that dies mid-write leaves the old document or the new one. A
  machine that loses *power* can lose the last write. The failure this packet
  is about is a driver process that stops, not a box that loses the machine.
- **State first, log second.** The run document is the authority and the log is
  its history, so the one window a crash can open costs a log line and never a
  state. A missing sequence number is visible; a state that never happened
  would not be.
- **Two drivers sharing a holder name share a lease.** The holder must be
  unique among the drivers of one run — a pid, a session id, a uuid. Two
  drivers sharing a name is the caller's defect and the store cannot detect it,
  and `Claim`'s comment says so.
- **A wedged driver's work may be duplicated.** The takeover is safe (the
  epoch fences every write) but not free: a driver that was mid-step when its
  lease expired, and then finishes, will have its step re-run by the driver
  that took over. The model makes that visible in the log rather than
  preventing it, because preventing it would need a kernel lock that cannot
  expire.

## What the successor's first move is

**Wire it into the box.** `internal/box.Phases` is already the step table — the
install's seven phases, in order — so a driver is `Create(boxID, phaseNames)`
once and then `Claim`/`BeginStep`/`CompleteStep`/`FailStep`/`Release` per
phase. `caf boxd` (W2-P7) is the driver, and kit's headless installer (W2-P5)
is the thing whose JSONL progress a `step_done` event should be able to feed.

Two entry points are already shaped for it and have no caller yet:

- `Store.HolderOf(id)` reads the lease a run is under, which is the fact
  `caf box status` would report ("the install is being driven by X and its
  lease runs out at T").
- `Store.Events(id)` is the run's log, oldest first, contiguous sequence
  numbers — the shape `caf box logs` already prints for the box's own event
  log in `internal/box/lifecycle.go`.

One thing to watch: the phases in `internal/box` are a `var`, so a driver that
built its step list from them would inherit that. Nothing mutates it today,
but a driver that did would corrupt every later run in the same process.

## Verification actually run

```
go test -count=1 ./internal/run/     ok
go test -race -count=1 ./internal/run/  ok
go vet ./...                         clean
gofmt -l .                           no output
./bin/prime                          19 packages, 906 top-level passes,
                                     1087 subtest passes, 0 failures, 8 skips
go test -count=1 ./internal/ci/      ok   (the new floors checked, not replayed)
go run ./cmd/caf lock .              12 files, re-pinned for the new gate.yml
./tests/gate-declaration-self-test.sh 3 controls green, 21 breakages went red
                                     and each named its finding, 5 warnings
                                     stayed green at exit 0, 0 skipped
```

The floors were re-measured rather than incremented by this packet's own
count, because both had gone stale underneath it: the packages floor was 17
against a tree of 18, and the subtest floor 12 below its own measurement. The
suite floor moved 892 → 906 (fourteen new tests, 914 declared, 8 expected
skips), and the identity `declared - minimum == expectedSkips` holds exactly.
