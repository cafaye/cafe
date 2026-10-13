# REPORT — roll/c5-cafe-tails

**Status:** both frictions addressed. The flags sweep is done across every
`cafe box` verb with a positional subject, and the `internal/edge` test is
reproduced, named with numbers, and fixed at the writer. Gate red only for the
documented pre-existing `internal/secret` citation failure, which this branch
does not touch.

**Worktree:** `/Users/kaka/Code/any/moon/wt/roll-c5-cafe-tails`, branch
`roll/c5-cafe-tails`, repo `cafaye/cafe`. Cut from `68cd2f9`. The tree was clean
at dispatch; the previous attempt left no partial work.

**Commits on this branch for this packet:**

- `c71d718` — the box flag-position sweep: four `Command` literals, the shared
  comment on `box_setup.go` rewritten from "this verb only", and ten CLI tests.
- `38879fa` — the edge test: one writer for the record, the flake's mechanism
  named in the commit and at the code site, plus the deterministic Sink test.
- `b1301af` — the gate floor raised to the measured tree (1417→1427) and
  `caf.lock` re-pinned.

---

## 1. Item 1 — the flags after the positional subject

**Decision: `Interspersed: true` on every `cafe box` verb whose subject is a
positional, not just the one that was measured.** The sibling packet
(`REPORT-roll-c4-setup-friction.md` §6) fixed `box setup` and named the
successor's first move in writing:

> Did not extend `Interspersed` to `box apply` / `box rollback` / `box publish`.
> They take an optional non-host positional and carry the same latent friction
> (`cafe box rollback rel-1 --service api` still says "wants 1 argument, got 3").
> A successor's first move is `Interspersed: true` on those three `Command`
> literals plus one table test.

That is exactly the sweep done here. `wipe` landed after `setup` and was not in
that list; it is the measured case.

**The measured case, reproduced on the real binary:**

```
$ cafe box wipe contabo --dry-run
cafe: usage: cafe box wipe wants 1 argument, got 2 (usage: cafe box wipe <host> [--dry-run] [--yes] [--json])
```

The count names a shape and sends nobody to the mistake — the same argument
`box_setup.go` makes. After the change the same command prints the dry-run plan
against `contabo` and removes nothing.

**The verbs, enumerated:**

| verb | positional subject | Interspersed |
|---|---|---|
| `box setup` | `<host>` (required) | yes (already) |
| `box wipe` | `<host>` (required) | **yes — the measured case** |
| `box apply` | optional `<phase>` | **yes** |
| `box rollback` | optional `<release-id>` | **yes** |
| `box publish` | `<release-id> <directory>` | **yes** |
| `box deploy` | none — host is `--host` | no |
| `box enroll` | none — the capability is `--token` | no |
| `box logs` / `box verify` | none — `--release` / `--observe` | no |
| `box plan` / `box status` / `box edge` | none | no |

`publish` is the one whose positionals are not a host: a release id and a
directory. It gets the same treatment because the brief's rule is "a positional
subject", not "a host".

The mechanism is unchanged and lives in `internal/cli/command.go`'s `parseArgs`:
the parse is looped, the word the flag package stops on is taken as ONE
positional, the rest is re-parsed, an unknown flag is still refused AS A FLAG
with its own name, and `--` still ends the flags.

**Proof at the CLI level** (the sibling's own shapes, per verb):

- the two flag positions are the SAME command — same document, same host, same
  planned steps (`wipe`, `apply`, `publish`, `rollback` each have this case);
- a misspelled flag after a positional is refused as a flag and names it
  (`-dryrun`, `--jsonn`, `--servicee`), and the host is contacted zero times or
  no release is written;
- a second subject is still a usage error and the count is right there
  (`wipe box.test other.test --json` → "wants 1 argument, got 2");
- `apply substrate --phase data` still reports "got the phase twice", so the
  explicit check the verb already had is untouched by the interspersed parse.

`box rollback` is worth one note: the edge verb of the same name
(`box_edge.go`'s `boxRollbackCommand`) takes no positional, and the router's
stack rollback shadows it (both are registered as `"box rollback"`; the first
wins in `lookupBoxVerb`). That shadowing is pre-existing and out of this
packet's scope — the reachable `box rollback` is the one that took the fix. It
is recorded under §4.

## 2. Item 2 — the `internal/edge` test that was not reliably green

**Verdict: a real flake on a clean tree, reproduced, named with numbers, and
fixed.** It was not the conflicted tree.

**Reproduction.** `go test ./internal/edge/ -run
TestTheSwitchIsARenameAndRollbackNeedsNoControlPlane -count=200 -v`:

```
--- FAIL: ... 57 times
FAIL	github.com/cafaye/cafe/internal/edge	1.694s
```

**57 failures in 200 runs.** The two failure lines are both faces of one defect:

```
edge_test.go:363: the routing row moved during a deploy:
  {Name:api.cafaye.test ... Health:unknown} -> {Name:api.cafaye.test ... Health:healthy}
edge_test.go:374: roll back: release "sample-stack-2" is serving and the edge
  spec records no earlier release to return to; ...
```

**The mechanism.** The test drives a real `Gateway` whose `Sink` is
`Save(b.dir, s)` — the edge's health writer — and it also runs `box.deploy`,
which is the CLI's write of the same `edge.json`. The gateway's `probeAll` does
a read-merge-write under an intra-process mutex (`writeMu`); the deploy writes
under no lock at all. Two writers on one file is a lost update, and the failure
prints both directions of it:

- the first line is a probe write landing between the test's two `Load`s, so the
  `Health` column moves under a test that compared the whole `Host`;
- the second is the harmful one: a probe's read-merge-write landed AFTER the
  second deploy's `Save` and wrote back the spec it had read BEFORE the deploy,
  with `Previous==""`. The symlink still pointed at `sample-stack-2`, so the
  rollback refused — the deploy's `Previous` pointer was gone.

**The fix, at the writer.** A test that mutates the record wants to be its only
writer, and the package says so: `SpecSink` documents a nil sink as *"a test that
does not want a second writer"*. So `boxState.gateway` now builds with no sink.
The edge still observes health and answers every request with it, out of its
in-memory probe table; it simply does not persist it. The one test that mutates
the record beside the gateway — the switch test — is now single-writer.

Verified: **0 failures in 300 runs** of the single test, the whole package green
**20 times** (`-count=20`) and **5 times under `-race`** (`-count=5`).

**Two smaller things came with it, and both tighten rather than loosen:**

- the Sink persistence path was only ever exercised by these racing gateways and
  never asserted. It has a deterministic test of its own now,
  `TestTheProbeLoopPersistsTheHealthItObserved`: `probeAll` as the only writer,
  the persisted health checked, and the release pointer checked not to have
  moved;
- the "routing row did not move" assertion compared the whole `Host`, health
  column included. `Health` is the edge's own observation and the package says
  it is rewritten as the edge probes, so the assertion now compares the routing
  identity — name, upstream, service — via `sameRoute`, which is the thing the
  test's own comment names and which a deploy must preserve. This is precision,
  not relaxation: the old form was asserting a coincidence about probe timing.

No sleep, no retry, no loosened assertion.

## 3. Tests

New, all green:

- `internal/cli/box_wipe_test.go` — 3 CLI cases + one helper (`wipeStepNames`):
  the two flag positions are the same command; a flag typo after the host is
  refused as a flag and the host is contacted zero times; a second subject is a
  usage error.
- `internal/cli/box_test.go` — 2: `apply`'s two positions reach the same phase;
  an `apply` flag typo leaves the box at its phase.
- `internal/cli/box_edge_test.go` — 2: `publish --json id dir` is the same
  command as `publish id dir --json`; a flag typo after the positionals writes
  no release.
- `internal/cli/box_deploy_test.go` — 2: `rollback <id> --service api` is the
  same command as `rollback --service api <id>`; a flag typo after the release
  id is refused as a flag.
- `internal/edge/edge_test.go` — 1: `TestTheProbeLoopPersistsTheHealthItObserved`
  (the deterministic Sink cover), plus the gateway helper and the switch
  assertion changes.

The existing `TestParseArgsTakesFlagsOnEitherSideOfAPositional` table in
`internal/cli/command_test.go` already covers the mechanism; no change was
needed there.

## 4. What was deliberately NOT done

- **Did not weaken the edge test.** The fix removes a second writer; it does not
  sleep, retry, or relax an assertion. The Sink path gained a deterministic test
  rather than losing its cover.
- **Did not fix the cross-process half of the lost update.** The same
  read-merge-write race exists between `cafe box publish` (the CLI writing
  `edge.json`) and a future boxd gateway's `Sink`. It is latent here — no
  production `Gateway` is constructed in this tree yet; only tests build one —
  and a real fix needs a cross-process lock. `internal/edge` is stdlib-only and
  the tree cross-compiles for `windows` (`.goreleaser.yml` lists it), so a
  `syscall.Flock` fix would break the release build; a fix belongs in its own
  packet with its own Windows story. This packet fixes the test's own two-writer
  contention, which is the layer the flake is at.
- **Did not touch the shadowed edge `box rollback`.** `newBoxRollbackCommand`
  (stack) and `boxEdgeVerbs`' `boxRollbackCommand` (edge) are both registered
  under the name `"box rollback"`; `lookupBoxVerb` returns the first, so the
  edge one is unreachable through the router and is exercised only by
  `runBoxVerb`, which looks it up in `boxEdgeVerbs` directly. That is a separate
  finding (§6), not a flag-position question, and changing which verb wins is a
  behaviour change no test here asked for.
- **Did not touch `internal/secret` or `cafaye/kit`.** The one red test is the
  citation tripwire firing on kit's reworded `install.sh`; fixing it means
  re-measuring cafe's citations against kit's new statements, which is a
  different packet and, per the brief, not another repo's to edit.
- **Did not touch any other repo, and spawned no other agent.**

## 5. Gate

`gofmt -l .` clean; `go vet ./...` clean; `go run ./cmd/cafe lock --verify .`
clean (`caf.lock matches 12 pinned file(s)`).

Floors raised by `cafe gate floors --write`, which ran the gate, derived the
numbers, set the floor, and re-pinned `caf.lock` in one run. Its reported delta:

```
cafe gate floors: the gate reported 33 packages, 1424 top-level passes, 1273 subtest passes, 3 failures, 8 skips
cafe gate floors: floors moved 1417→1427
cafe gate floors: re-pinned caf.lock
```

The run it measured was red on the stale floor — by design, since that is the
case the writer exists for. `Suite = TopLevel + TopLevelFailures` is invariant
to pass/fail: 1424 passes + 3 failures = 1427 tests that RAN. Packages and
subtests are unchanged (33, 1273).

`./bin/prime`:

```
cafe: 33 packages, 1426 top-level passes, 1273 subtest passes, 1 failures, 8 skips
cafe: live tier: 0 of 7 executed; not enabled, so the 7 live tests skipped rather than ran.
cafe: live tier: set CAF_LIVE_DOCKER=1, CAF_LIVE_RYUK=1 and CAF_LIVE_KAMAL=1, or run bin/prime --live
--- FAIL: TestEveryCitationPointsAtSomething (0.01s)   [internal/secret]
exit: 1
```

**The one failure is pre-existing and not this packet's.** It is the
`internal/secret` citation check against `cafaye/kit/templates/substrate/install.sh`:
kit reworded two statements the citations rest on, and the anchors
`"Role passwords: generated ONCE, read back on a re-run."` and `"minted on the
box in step 11, never printed, never an input"` are no longer in the file. It is
documented as pre-existing in `REPORT-c3-rename-cafe.md`,
`REPORT-roll-c3-boxd-identity.md`, `REPORT-roll-c3-enroll-stdin.md` and
`REPORT-roll-c4-setup-friction.md`; `git diff 68cd2f9..HEAD -- internal/secret`
is empty, so the gate is red at base and red now for that one reason.

## 6. Handoff / other findings

- The only red is the `internal/secret` citation failure; a packet that
  re-measures cafe's citations against kit's current `install.sh` clears it. The
  existing reports already carry this handoff.
- The two `box rollback`s (stack and edge) are one name with two implementations
  and the router silently prefers the stack one. If the edge `box rollback` was
  meant to be reachable, that is a real (pre-existing) defect; if not, it is dead
  code the tests keep alive through `runBoxEdgeVerbs`. Worth a decision, not a
  drive-by.
- The edge's health persistence has no production caller yet (no `Gateway` is
  wired outside tests). When boxd wires it, the `Sink`-vs-deploy lost update
  above becomes reachable and needs the cross-process answer.
