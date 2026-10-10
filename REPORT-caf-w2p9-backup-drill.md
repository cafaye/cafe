# REPORT-caf-w2p9-backup-drill.md

**Packet:** OPS-P9 — backup drills that PROVE the backup restores, and report the result.
**Repo:** `caf` (`github.com/cafaye/caf`), branch `ops/w2-backup-drill`.
**Commits:** `57bfdd6` (the record and its store), `900c945` (the drill verb), and a third
landing the Windows fix, the `gate.yml` floor and the regenerated `caf.lock`.

## What the gap was

`caf backup` already takes a real snapshot and proves it restores into a scratch database it
then drops. I read where the verdict goes: `backup.Run` writes it with
`successReport`/`failureReport` (`internal/backup/run.go:477` and `:497`) and returns either
`nil` or the `ErrCycleFailed` sentinel. The engine's own doc comment states the boundary —
"the engine owns the plan …; the command owns everything either side of it"
(`internal/cli/backup.go:24-27`). So the verdict was printed to a stream the process closed
on exit, and nothing anywhere held it. The manager's read was right: **printed and
forgotten.**

## What was built

### 1. The record, and the store that keeps it — `internal/backup/drill.go`

`DrillRecord` is the drill as a fact about a service: service, engine, environment, the
scratch database's *name*, the tables asserted, the outcome, a machine-matchable reason, a
one-line detail, both timestamps and the duration in milliseconds.

`DrillStore` persists it as one JSON file per service under
`DefaultDrillDir()` — `CAF_DRILL_DIR`, else `$XDG_STATE_HOME/caf/drills`, else
`~/.local/state/caf/drills`. **This is not a new mechanism**: it is `ledger.DefaultDir`'s
exact resolution order and its exact argument
(`internal/ledger/ledger.go:127-140`), and the store's shape — a directory, a lock file, an
append under the lock, a write-by-rename — is `internal/run`'s `Store`
(`internal/run/store.go:13-46`, `internal/run/files.go`). Three outcomes rather than a
boolean, because "the restore worked" and "caf would not touch the service" are different
facts with different fixes.

### 2. The verb — `caf backup drill`, `internal/cli/backup_drill.go`

`caf backup drill <service>` is the same cycle with the confirmation taken away and the
outcome recorded. It never asks: a drill on a schedule has nobody to answer a prompt, and a
command that hung waiting for one would be a schedule that never ran. `--json` writes ONE
document to stdout and moves caf's own report to stderr — the split `caf agent` already
makes (`internal/cli/box_render.go:26-31`, `internal/cli/agent.go`).

`caf backup <service>` records too, so a manual drill is not forgotten either.

### 3. The alert

caf has no notification transport — the packet's own copy says nobody is paged (OPS-P20,
`CAFAYE-OPS.md:121-124`). So this uses the convention caf already has: **exit 1, a named
reason, one line on stderr** naming the service and where the record went.

The reason is extracted from the refusal's own report rather than invented:
`backup.ReportReason` (`internal/backup/drill.go:80`) reads back the `caf-backup/` line
`contract.go:288` writes, so a refused drill's record names the *clause* that broke
(`caf-backup/secret-not-declared`) and not only the class.

## Three decisions worth arguing about

**The alert line is the drill's, not the cycle's.** `caf backup`'s contract is that a
refusal is a report on stdout and **stderr stays empty**, because a `caf: ...` line there is
a truncated copy in the place a CI log greps (`internal/cli/backup.go:232-239`, and the test
that pins it at `internal/cli/backup_test.go:115-117`). I did not revisit that. A drill is
the other case — nobody is watching a terminal — so the line is gated on the verb.

**A record that cannot be written is a failure, not a warning.** The gap being closed is a
verdict that was printed and forgotten; a drill whose record did not land has been forgotten
exactly as thoroughly. It exits nonzero and names `CAF_DRILL_DIR`. I judged this worth the
behaviour change to `caf backup`, and it is the one place I would accept pushback.

**I did not build a scheduler.** The packet says "wrap it: a schedule". A daemon that sleeps
for a month is a daemon nobody has watched run, and `caf` has no long-lived process — its
only one, `boxd`, holds an outbound mTLS channel and is not a cron. So `caf backup drill` is
what a schedule RUNS, and the record (with `finished_at`) is what tells you whether the
schedule has been running. The verb's long help says so in a paragraph headed
"THE SCHEDULE IS NOT HERE" rather than implying otherwise. **Flagging this as the one place
I read the packet narrowly**, and I think it is the right narrowing, but it is a judgement.

## Verification — exit codes I actually observed

- `go test ./internal/backup/` (the 16 new drill tests, `-timeout 90s`) — **exit 0**
- `go test ./internal/cli/` (the 14 new drill-verb tests plus the 15 pre-existing backup
  tests) — **exit 0**
- `GOOS=windows go build ./...` — **exit 0** after the fix below; it was **exit 1** before it
- `gofmt -l internal/ cmd/` — **empty** (exit 0)
- `go vet ./internal/...` — **exit 0**
- `go test ./...` — **exit 0** on the final run (`/tmp/gate-w2-backup-drill-final2.log`,
  `/tmp/gate-w2-backup-drill-final2.exit`), 22 packages `ok`, no `FAIL` lines.
- `./bin/prime` — **exit 0**, and its countable line reads exactly
  `caf: 23 packages, 1078 top-level passes, 1157 subtest passes, 0 failures, 8 skips`
  on the run before the last test was added; the tree now declares 1087 and the floor is
  1079. **Every number in this report is one a run printed, not one I computed.**
- End-to-end, against the real binary in a scratch project with no `kamal` on PATH:
  `caf backup drill --table users --json identity` exits **1**, writes a single JSON
  document to stdout and nothing else, writes the report plus one alert line to stderr,
  and appends the record. Verified stream-by-stream, not from a merged terminal.

Three runs went red before going green, and all three were real:

1. `internal/backup/lock_other.go:69` used `err` where it meant `removeErr` — a bug that
   only compiles on Windows, caught by `TestTheTreeBuildsForEveryReleaseTarget` because
   `.goreleaser.yml` ships a windows binary. That test exists for exactly this and it was
   right.
2. `TestTheGateFloorIsNotBelowTheSuiteCafClaimsToHave` (`internal/ci/prime_test.go:529`) —
   I added tests, so the tree declares more than `gate.yml`'s `suite` floor. Raised to
   1079 (1087 − 8 expected skips) and `caf.lock` re-pinned with `go run ./cmd/caf lock .`,
   because `gate.yml` is the tree's one `rule-bundle`. No new subtests, so the `subtests`
   floor did not move.
3. The `detail` field on a `drill-not-run` record was picking the report's own header
   ("backing up from … with kamal 2.12.0") instead of the cause, because before the cycle
   starts the report's first line is a header. Fixed to take the error's first line for
   that one case, and re-verified against the real binary.

## Test style

All 29 tests are in the repo's existing style: one property per test, a sentence naming
what would break if it regressed, and a negative control where the property is "the thing
that should not happen did not". The store's tests are about bytes on disk rather than about
what a command printed (`loadJournal` reads the file back); the command's tests drive the
verb through the real router with an injected `Runner` and a scratch store, the way
`backup_test.go:28-62` does.

One test I had to rewrite rather than write: `TestTheStoreLockReportsWhenAnotherCafHoldsIt`
originally held the lock and called `Append`, expecting a refusal. It hung, because `flock`
is per open-file-description and the same process blocks on itself — which is correct
production behaviour (the lock is held for milliseconds, not for the length of a drill). The
test now asserts the property that matters: every append lands, and a caller that finds the
lock held is told rather than left waiting.

A second thing the tests caught in my own code: `DrillReasonNotRun` was unreachable for a
while, because every non-refusal, non-`ErrCycleFailed` error was falling through to
`DrillReasonFailed`. My own comment on `drillReasons` says a reason nothing can reach is a
reason a reader has to imagine, so the classification now uses the sentinels — `ErrCycleFailed`
is produced by exactly one place (`run.go`'s `failureReport`), so its absence *is* the signal
that the cycle never started — and `TestADrillThatCouldNotStartIsRecordedAsNotRun` produces
it.

## Notes for a successor

- The record's history is bounded at 12 (`drillHistoryLimit`, `internal/backup/drill.go`),
  a year of the monthly cadence. The monthly report (OPS-P13) reads
  `<state>/caf/drills/<service>.json` directly — the field names are asserted in
  `TestADrillRecordSurvivesTheRoundTrip` so a rename is a red test rather than a silent
  break of OPS-P13 and the box agent at once.
- `internal/ci`'s `notASourceTree` and the drift gate were untouched: I added files to two
  existing packages, not a new one.
- The worktree directory `/Users/kaka/Code/any/ops-wt/w2-backup-drill` was removed
  underneath me twice mid-run while other workers were running in the same parent
  directory. Both times the branch and its commits survived in
  `/Users/kaka/Code/any/moon/cafaye/caf` and I re-created the worktree with
  `git worktree add`. Nothing was lost, but it cost two gate runs.
