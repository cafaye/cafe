# REPORT-roll-c5-coverage-floor: the 85 floor becomes the 78 floor

**Packet:** roll-c5-coverage-floor — cafe declares `coverage-fail-under: '85'`
and measures below it, so CI's `go` job cannot pass.
**Path taken:** 2 — re-floor to the measured value. The commit message says so.
**Branch:** `roll/c5-coverage-floor`. One commit on top of `e13dc51`
(roll-c5-race-parity merge): `aab87b0`.

## Measured coverage

| Platform | Command | Total |
|---|---|---|
| linux (CI's platform) | `go test -race -count=1 -coverprofile` in `golang:1.25.0` (docker) | **78.7%** (11183/14229 stmts) |
| darwin (this machine) | `go test -coverprofile` | **79.1%** (11245/14228 stmts) |

The linux figure is the one the floor is read against: CI runs
`go test -race -count=1 -coverprofile=coverage.out ./...` on ubuntu-latest and
checks the total with `go tool cover -func` piped through an awk
`min` comparison (kit `ci.reusable.yml`, coverage step). The 0.4-point
darwin/linux gap is the same platform divergence the old comment predicted
(`doctor_probe_*`, `lock_unix`/`lock_other`, the rest of the `//go:build`
surface) — small, real, and exactly why the floor must sit at or under the
**linux** number, not the darwin one.

Floor landed: **`coverage-fail-under: '78'`**. Integer, not 78.7, because the
tooling takes integers: kit compares numerically, but this repo's own
`TestTheCoverageGateIsAboveZero` (`internal/ci/ci_test.go`) reads the input
with `strconv.Atoi`, so a decimal floor would fail `bin/prime`. The rounding
is forced by the gate, not headroom-seeking. `78.7 < 79`, so 79 was never an
option; `78` is the only integer the measurement supports.

## Why 85 was never met

85 dates to caf-06, when the tree measured 89.1% on darwin — the floor was
earned then. Packets since added under-tested code and the tree drifted to
78.7 without CI saying so, because every earlier failure was at the lint step
which runs first (the onion; this packet is its last layer). The drift is
visible per package (linux numbers):

- `internal/agent` **24.5%** (392 stmts) — `store.go` (15 zero-coverage
  funcs), `config.go` (8), `names.go`, `ring.go`, `endpoint.go` have no unit
  tests of their own; the cli-level `agent_test.go` exercises the commands
  but package coverage only counts in-package tests.
- `internal/cve` **49.2%**, `internal/provider` **54.8%**,
  `internal/edge` **60.0%**, `internal/secret` **66.7%**,
  `internal/box` **67.3%**, `internal/stack` **71.4%**,
  `internal/setup` **74.2%**, `internal/reclaim` **75.0%**.
- 1154 of 1740 functions sit at 0.0%.

Closing the 6.3-point gap to 85 needs ~912 newly covered statements across a
dozen packages. That is real, honest test-writing work for later packets — not
a number to reach in an hour, and not a number to reach with
assertion-free tests. So the floor now **prevents regression** rather than
describing an aspiration. It is not 0: 0 fails nothing (kit's doc, and this
repo's own `TestTheCoverageGateIsAboveZero`, say why).

## Decision record (against path 1)

Raising the tree to 85% would mean genuine behavior-asserting suites for the
agent store/ring/config, the cve scan/advisory paths, the provider
connection, the edge TLS modes, and the secret rotation runner — each a
packet-sized piece of work with its own fixtures and negative controls.
Writing 900 statements' worth in one hour could only be done with
call-functions-to-move-a-number tests, which the packet explicitly forbids.
Path 2, with the package names stated above, is what the evidence supports.

## Controls

All run against real `go test -coverprofile` output with the exact awk
comparison CI uses (`if ($3 + 0 < min + 0)`).

- **GREEN, linux profile:** floor 78 vs the container-measured profile
  (78.7%) → no output, exit 0. Passes with 0.7 margin.
- **GREEN, darwin profile:** floor 78 vs 79.1% → exit 0.
- **RED 1 (discrimination):** floor **79**, one point above measured, vs the
  linux profile → recorded output `coverage 78.7% < 79%`, exit 1.
  The check fails rather than trusting.
- **RED 2 (regression):** tree with `internal/cli/gen_test.go` and
  `box_setup_test.go` moved aside plus a temporary untested probe file
  (`zz_red2_probe.go`, ~60 untested statements, deleted after) measured
  **77.9%** → `coverage 77.9% < 78%`, exit 1. A lower-coverage tree fails
  the floor: it is not 0 wearing a number. All probe artifacts reverted;
  `git status` clean afterwards (verified).
- **GREEN, gate:** `bin/prime` on the landed tree — see below.
- **Gate-row test:** `TestTheCoverageGateIsAboveZero` passes with `'78'`
  (78 > 0), verified with `go test -run`.

## Incidents during the run (stated, not hidden)

- A background-wrapper quoting bug created stray files `-lin.log` /
  `-lin.exit` in the worktree; both removed, none committed.
- The first clean-`bin/prime` attempt ran concurrently with two other full
  `go test` runs and failed two ways: (a) my own `red2-stash/` directory
  broke the `./...` build — self-inflicted, fixed by moving the stash to the
  go-ignored `_red2-stash/`; (b) `TestTheSyncWaitConvergesAfterSeveralPolls`
  (internal/deploy) failed at 8.57s under triple load, then passed alone in
  2.4s with `-race -count=1`. Verdict: load-induced flake, unrelated to this
  packet (which touches only `ci.yml` + this report). The rerun of
  `bin/prime` below is the confirmation.
- The background docker run wrote `coverage.out` into the worktree via the
  volume mount; copied to `/tmp/c-linux.out` for the controls, then deleted
  (`*.out` is gitignored regardless).

## bin/prime verdict (landed tree)

`bin/prime` exit 0 on the landed tree (commit `aab87b0` + report): lint
0 issues; `cafe: 33 packages, 1433 top-level passes, 1273 subtest passes,
0 failures, 8 skips`; live tier `0 of 7 executed` as declared. The earlier
`TestTheSyncWaitConvergesAfterSeveralPolls` failure did not recur on the
unloaded rerun.

## For the manager (push/CI)

- Nothing to merge besides `aab87b0` + the report commit. No Go code changed;
  `gofmt`/`go vet` unaffected.
- CI expectation: `ci (kit: go)` coverage step passes (78.7 ≥ 78 on linux,
  verified with CI's own command and comparison); `gate (bin/prime)` green.
- Next packets: the package list above is the coverage backlog. When new
  suites genuinely lift the tree, raise the floor with the new measurement in
  the commit message — never to follow a single packet's number upward.
