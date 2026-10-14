# REPORT-roll-c5-race-parity.md

The gate runs the race detector now, and the three fixture races that made CI
red are fixed. Two commits on `roll/c5-race-parity`, no test added or removed,
no floor moved, `daemon.go` untouched.

## 1. Every race found

`go test -race -count=1 ./...` over the whole tree on the base (`ed5d4b6`)
reported exactly three data races, all in `internal/boxd/daemon_test.go`,
all the same shape: the daemon's goroutine writes a fixture field while the
test goroutine reads it. 31 of 32 test packages passed; only `internal/boxd`
failed. There were no other races in the tree — that run is the measurement
of how many there really were, and the answer is three sites in one file.

| # | write (daemon goroutine) | read (test goroutine) | failing test |
|---|---|---|---|
| 1 | `fakeDialer.Dial` writes `dials` (`daemon_test.go:75` via `daemon.go:200`) | poll loop `dialer.dials < 2` (`:224`) | `TestTheDaemonReconnectsAfterTheChannelEnds` |
| 2 | `fakeStore.Load` writes `load` (`:92` via `daemon.go:196`) | poll loop `store.load < 2` (`:299`) | `TestTheDaemonLoadsTheIdentityPerConnection` |
| 3 | `fakeDialer.Dial` writes `dials` (`:75`) | poll loop `dialer.dials < 4` (`:421`) | `TestTheDaemonKeepsRetryingATransientFailurePastTheBound` |

## 2. How each fixture was synchronized

Atomics for the counters, which is what the packet allowed and what the file's
sibling already does (`tls_test.go` uses `atomic.AddInt32/LoadInt32` for its
own key-source double):

- `fakeDialer.dials`: `int` → `atomic.Int64`. Write is `Add(1)` in `Dial`;
  all 13 read sites are `.Load()`.
- `fakeStore.load`: `int` → `atomic.Int64`. Same shape, 4 read sites.
- `fakeControlPlane.closed`: `bool` → `atomic.Bool` (`Store(true)` in
  `Close`, `.Load()` at both read sites). It was synchronized-by-luck before
  (reads happened after `<-done`), and luck is not synchronization.
- `fakeControlPlane.nextCalls`: `int` → `atomic.Int64`. Currently write-only,
  synchronized anyway: the next reader must not have to audit history.

Deliberately unchanged: `fakeControlPlane.queue`/`acks`/`nextErr`/`ackErr`,
`fakeStore.id`/`err`, `fakeDialer.err`/`ch` — set at construction, before the
daemon's goroutine starts, so the goroutine creation is the happens-before
edge and the detector confirms it. `fakeDialer.lastID` is written by the
daemon and read by nobody; no edge was added for an unread field.
`testKeys.calls` in this file is never invoked by the fake dialer (only the
real dialer touches `Keys`), so it never executes concurrently. The daemon
(`internal/boxd/daemon.go`) is byte-identical: its concurrency is the subject,
the fixture was the defect. No assertion in the three tests changed —
`.Load()` replaced the bare reads one for one.

## 3. The gate change

`bin/prime` runs `go test -race -v ./...` (was `go test -v ./...`) and echoes
the command as `test command: ...` so the gate's own output proves which
detector ran. `-count=1` is still absent on purpose (AGENTS.md: a cached run
replays its verbose output verbatim, so the floors read the same either way);
CI's job runs `-race -count=1 -coverprofile`, the gate runs `-race -v` — the
flag that discriminates is the one now shared.

`TestTheGateReportsItsOwnCounts` (`internal/ci/prime_test.go`) pins the
invocation verbatim — `go test -race -v ./...` — covering both flags in one
assertion: `-v` is what the floors are counted out of, `-race` is what keeps
the gate honest about the detector CI runs. It lives in an existing test
function, so no test was added and no floor moved: the gate still reports
`33 packages, 1433 top-level passes, 1273 subtest passes, 0 failures, 8
skips`, exactly `gate.yml`'s minimums. `gate.yml` and `caf.lock` are
untouched.

## 4. The `-race` cost measurement

Same machine (darwin/arm64, warm module cache), back-to-back, both `-count=1`:

| command | real | user |
|---|---|---|
| `go test -count=1 ./...` (no detector) | 1m6.2s | 0m25.5s |
| `go test -race -count=1 ./...` | 1m11.6s | 0m28.9s |
| `./bin/prime` (warm cache, replays verbose output) | 10.5s | 9.7s |

The detector costs ~5s on a ~66s suite (~8%). Not material; no gate-shape
change was needed to pay for it. Spot costs: `internal/boxd` 6.8s → 8.1s,
`internal/ci` 4.1s → 10.5s under the detector. A slower gate that means what
it says beats a fast one that does not — and this one is barely slower.

## 5. The controls' verdicts

| control | command | verdict |
|---|---|---|
| RED: base tree under the detector | `go test -race -count=1 ./internal/boxd/` at `ed5d4b6` (scratch worktree, since removed) | **red as required**: exit 1, exactly the three tests above, 3 `WARNING: DATA RACE` |
| RED: base gate without the flag | `go test -count=1 ./internal/boxd/` at `ed5d4b6` (what `bin/prime` ran) | **green as required**: exit 0 — the check discriminates, the gate was green |
| GREEN: fixed tree under the detector | `go test -race -count=1 ./...` on the branch | **exit 0**, 32 `ok` + 1 no-test-files = 33 packages, 0 warnings — twice, plus `go test -race -count=2` over boxd/ci/ports/edge exit 0 |
| GREEN: gate runs `-race` | `./bin/prime` on the branch | **exit 0**, output shows `test command: go test -race -v ./...`, counts 1433/1273/8 unchanged |
| Independent verification | pushed CI run, both jobs | **pending push** — workers do not push; the manager pastes both verdicts at merge |

## 6. One honest anomaly

While measuring cost (§4), the detector run inside the timing script printed a
bare trailing `FAIL` (exit nonzero) with the failing package's line lost to a
`tail -3` truncation — my measurement harness's fault, not the tree's. It has
not reproduced: two full-tree `-race -count=1` runs since (exit 0, 0 warnings),
one `-race -count=2` stress over the four most timing-sensitive packages
(exit 0), and two green `bin/prime` runs. A bare `FAIL` with no package name
is not attributable, so it is recorded here rather than explained away. If CI
shows a flake on the pushed run, the first suspects are the deadline-driven
daemon tests (`2s`/`5s` waits over a `500ms→…` backoff), and the fix per the
brief is synchronization, not bounds.

## 7. Handoff

Branch `roll/c5-race-parity`, two commits (`683d6c5` fixtures + gate flag,
`b222b47` echo of the test command), working tree clean. Remaining for the
manager: push, paste both CI job verdicts, merge. No follow-up work is known;
the only open thread is the §6 anomaly and its prescribed response.
