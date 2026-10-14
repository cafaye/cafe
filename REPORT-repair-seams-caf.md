# REPORT — repair-seams-cafe: finding 1a (already landed) and finding 2c (drift, fixed)

Worktree `/Users/kaka/Code/any/ops-wt/repair-seams-cafe`, branch
`ops/repair-seams`, dispatched against `SEAM-AUDIT-OPS.md` findings 1a and 2c.
Two findings, two outcomes: one needed no code, one did. Both are verified below
against this tree, with the command and the line actually observed.

---

## FIX 1 (finding 1a) — no change needed: the seam is already closed in this tree

The audit measures cafe @ `205f3cd`, against which `cafe report` is unreachable.
**This worktree is not that tree.** Its HEAD is `aa54700` (the OPS-P13 merge),
which already carries all five rows of finding 1a. Measured, not assumed:

| finding 1a row | file:line now | state |
|---|---|---|
| registry | `internal/cli/command.go:87` | `newReportCommand(reportDeps{})` — registry holds 23 commands |
| fixture | `internal/cli/command_test.go:32` | `"report"` present, in registry order |
| stub table | `internal/cli/stub_test.go:33` | `"report"` in `workingCommands`, sorted |
| verb wiring | `internal/cli/command.go:87` | called from a non-test file; nothing dangling |
| CLI test | `internal/cli/report_test.go` | 7 top-level tests, 4 subtests, all passing |

`git log -S` dates the registration to `dea86c3` (a `recover(w2-report-cafe)`
commit), carried into this branch by `aa54700`. The audit's snapshot precedes it.
So the honest finding here is **an audit-vs-worktree drift**, not a defect: the
audit is right about `205f3cd` and the merge already repaired it.

Behaviour, run from this tree (not from the tests):

```
$ echo '{"availability":{"uptime_pct":100}}' > /tmp/in.json
$ go run ./cmd/cafe report render --json /tmp/in.json
cafe report: 7 section(s) could not be filled; they are named in the page
{ "box": "", "period": {...}, "gaps": [...] }          # one JSON document, exit 0
```

The gaps line goes to stderr and the document to stdout, which is the split
`internal/agent` holds. `go test ./internal/cli/` is green, which is the proof
that `TestRegistryIsSorted`, `TestRegistryContainsEveryCommand`,
`TestStubTableMatchesTheRegistry` and `TestRegistryHelpListsEveryCommand` all
pass with `report` in place. **Nothing was changed for FIX 1.**

---

## FIX 2 (finding 2c) — the comments, and the two floors they had outrun

`gate.yml` was rewritten in three comment blocks and two floors. The audit named
the comment drift; it did not name that **two of the three floors were themselves
stale**, and that is the half of the defect with teeth. `aa54700`'s message claims
"floors measured 27/1195/1214" but its diff moves one line — the suite floor,
1180 → 1195. The packages floor stayed at 26 and the subtests floor at 1208 while
the tree measures 27 and 1214. A floor below what runs is a floor that has
stopped detecting a decrease, which is its entire job.

Measured on this tree, `./bin/prime` (background run, log captured, exit captured):

```
cafe: 27 packages, 1195 top-level passes, 1214 subtest passes, 0 failures, 8 skips
cafe: live tier: 0 of 7 executed; not enabled, so the 7 live tests skipped rather than ran.
```

Final state of `gate.yml`:

| proof | was | now | what the comment says |
|---|---|---|---|
| packages | comment 24 / floor 26 | comment 27 / floor 27 | `go list ./...` at `aa54700`; one package per packet — internal/cve (OPS-P11) → 25, internal/failure (OPS-P12) → 26, internal/report (OPS-P13) → 27 |
| suite | comment 1128 / floor 1195 | comment 1195 / floor 1195 | the wave's four moves, ending at OPS-P13's 8 tests in internal/report plus 7 in internal/cli/report_test.go; and that 1195 is exact — 1203 declared test functions minus the 8 the gate skips |
| subtests | comment 1165 / floor 1208 | comment 1214 / floor 1214 | the floor the last bump left behind, and why a floor under what runs is not a floor |
| header | "de25e37 + cafe-07" | "aa54700 — the OPS-P13 monthly-report merge" | with the four measured numbers in the sentence |

Stale sentences deleted rather than edited: the header's three-merges-old
provenance, the suite comment's false claim that "each added top-level tests and
no subtests, so only this number changed" (false at the OPS-P12 merge, which
moved the subtests floor too), and the packages comment's "internal/cve landed
with OPS-P11" as if that were the last thing to happen.

Every number in the new comments is one I observed on this tree, except two that
are read out of the ratchet's own log line:
`gate.yml floor 1195, tree declares 1203 top-level tests, so the gate runs 1195
and skips 8`. Where I could not attribute a number to a packet — the 2 subtests
of the 6 that are not `TestReportPeriodParsing`'s four — the comment does not
claim an attribution, it claims the delta.

`gate.yml` is the tree's one `rule-bundle`, so `caf.lock` is regenerated in the
same commit. `go run ./cmd/cafe lock .` wrote 12 pinned files and
`cafe lock --verify .` reports: "caf.lock matches 12 pinned file(s), and nothing
the tree declares is missing from it."

---

## VERIFY, as observed

```
$ go build ./...                       # clean
$ go test ./internal/cli/ ./internal/ci/
ok  github.com/cafaye/cafe/internal/cli   65.261s
ok  github.com/cafaye/cafe/internal/ci     3.358s
$ gofmt -l .                           # prints nothing
$ go vet ./internal/cli/ ./internal/ci/ # clean
$ timeout 400 ./bin/prime
cafe: 27 packages, 1195 top-level passes, 1214 subtest passes, 0 failures, 8 skips
cafe: live tier: 0 of 7 executed; not enabled, so the 7 live tests skipped rather than ran.
exit: 0
```

The exit code, the summary line, and the zero `--- FAIL` lines are all from the
captured log, not from memory. The three floors now sit exactly at the
measurement, so `gate-check --prove` would report `gate.floor` for neither.
`internal/ci`'s ratchet is green, which is the identity that matters most here:
`1203 declared − 1195 floor == 8 expected skips`.

## What a successor should know

- The audit is being read against a moving target. Re-check which commit the
  audited tree claims before fixing "finding 1a" again — the work is done.
- Findings 3b/4a/4b/4d live in the **website** and **conductor** repos and are
  the four that actually break a user-visible screen. They need their own
  workers in their own worktrees; nothing in this tree fixes them.
- If a test is ever added here, the floor in `gate.yml` moves in the same commit
  and `go run ./cmd/cafe lock .` follows it. The half-landed bump that produced
  this finding is the reason.
