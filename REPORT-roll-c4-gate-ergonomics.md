# REPORT — roll/c4-gate-ergonomics

**Packet.** Two pieces of friction in the gate itself, both paid for twice in one
day. The guard each one implements is wanted; the hand maintenance is not.

1. `gate.yml`'s suite floors were derived numbers a human hand-edited.
2. `internal/secret`'s citations into kit's `install.sh` were line ranges, and
   kit moves.

Worktree `wt/roll-c4-gate-ergonomics`, branch `roll/c4-gate-ergonomics`, repo
`cafaye/cafe`. The previous attempt died mid-work with the tree clean; there was
no partial work to keep or discard.

---

## Friction 1 — the floors are now derived

### The shape

- **`internal/gate`** (new, stdlib-only) owns the derivation:
  - `ParseFloors` / `RewriteFloors` read and set the three countable floors in
    `gate.yml`'s bytes, preserving every comment and every other byte. The parse
    is by shape (`- id:` then `minimum:`) rather than through a YAML library,
    because a parse-into-struct + marshal would drop the prose above each floor.
  - `Measured` / `Measured.Floors()` turn one gate run into the floors the tree
    implies.
  - `Delta` is the review signal: `"floors moved 1376→1380, subtests 1248→1250"`.
  - `CountTestFuncs`, `NotASourceTree`, `IsTestFunc` and `ExpectedSkips` moved
    here from `internal/ci`, so the ratchet's `declared` count and the writer's
    suite floor are the same code and the same constant.
- **`cafe gate floors [--write] [path]`** (`internal/cli/gate.go`) is the writer.
  It runs the gate (`bin/prime`) so the numbers are the gate's own summary line,
  derives the floors, prints the delta, and with `--write` sets them in `gate.yml`
  and re-pins `caf.lock` in the same run. It has no network or database
  dependency of its own; the only network `bin/prime` can touch is `go mod
  download` on a cold module cache, which is the gate's own property.

### The argument (stated at the code site, in `internal/gate` and `internal/cli/gate.go`)

A floor is a decrease detector, and the ratchet holds the identity exactly:
`declared - minimum == expectedSkips`. That makes the number a **function of the
tree**, and a function a human retypes on every test-adding change goes stale. It
did — twice in one day: two packets each raised the number, a rebase resolved the
conflict mechanically, and the resolution kept the lower of two correct numbers,
leaving the floor one short of the merged tree. The number was right in both
branches and wrong in the merge, which is the signature of a value that should
never have been written by hand.

**The review signal is now the printed delta.** A floor that moves is visible in
the command's output and in the `gate.yml` diff; a floor that moves **down** still
requires a human to run the writer and read the number, because the writer is the
only thing that can lower a floor and running it is a deliberate act.

**Why the floor is "tests that RAN", not "tests that passed".** The common reason
to run the writer is a gate that is red on the stale floor — and the ratchet test
itself is then one of the failures. Counting only passes would derive a number one
short, write it, and leave the gate red. `Suite = TopLevel + TopLevelFailures`
(the same for subtests) is invariant to pass/fail and equal to `declared - skips`.

### What the guard still catches (unchanged, exact)

`internal/ci`'s `TestTheGateFloorIsNotBelowTheSuiteCafClaimsToHave` still holds
`declared - minimum == expectedSkips`, in both directions:

- a floor **below** the tree's count (a test added, floor not moved) is red;
- a floor **above** the tree's count is still its own failure
  (`TestTheGateFloorIsNotAWall`), a wall rather than a stale ratchet;
- a test that **starts skipping** still moves the margin and is red, so a new
  tier cannot appear silently;
- a test **deleted** without the floor following it down is red.

The only thing that changed is that the number's *remedy* is `cafe gate floors
--write` rather than "raise minimum by hand".

---

## Friction 2 — citations carry content, not line ranges

### The shape

A stored citation is now `path: "anchor"` — the file and the **exact statement**
the reason is about, in double quotes, several joined by `, `. The line number is
derived at check time (`resolveCitation`) and only logged; it is never the stored
form. The grammar lives in `internal/secret/citation_test.go`; the constants and
the table in `internal/secret/inventory.go`.

`TestEveryCitationPointsAtSomething` now walks **every** citation of **every**
entry (it used to check a hardcoded subset), searches the file for the anchor,
and fails naming what it looked for and where. A citation that has merely moved
passes, which is the point.

### What the guard still catches

- The cited **content deleted or reworded** → red, naming the anchor and the file.
- The **control**: if no citation could be checked at all (fleet root wrong, or
  every cited file missing), the test fails rather than passing vacuously.
- A citation that does not parse into `path: "anchor"` is red
  (`TestEverySourceParsesAsCitations`, and the shape check in
  `TestEveryEntryHasAClassAReasonAndACitation`).
- What it no longer catches — and must not — is a statement that **moved**. The
  fixture test `TestACitationSurvivesAMoveAboveIt` proves the move case, and
  `TestACitationFailsWhenItsContentIsGone` proves the deletion case, both driven
  by `internal/secret/testdata/install-fixture.sh` and not by editing kit.

`kit`'s `install.sh` moved ~100+ lines **during this packet** (1845 → 1866 lines
between two reads), which is the friction reproducing itself live.

---

## Idempotence proof

Run 1 (`--write`) on the tree that carried the new tests but still the old
floors — a red gate, which is exactly the case the writer has to survive:

```
$ go run ./cmd/cafe gate floors --write .
cafe gate floors: the gate reported 33 packages, 1376 top-level passes, 1250 subtest passes, 4 failures, 8 skips
cafe gate floors: floors moved 1376→1380
cafe gate floors: re-pinned caf.lock
```

Committed. Then, on the committed state, the writer run again:

```
$ md5 gate.yml caf.lock                                  # before
MD5 (gate.yml) = 2284f231d2dbc0965e9c2a268ed9aee5
MD5 (caf.lock) = fda9825b709087c94c9cce05c964da7b
$ go run ./cmd/cafe gate floors --write .
cafe gate floors: the gate reported 33 packages, 1380 top-level passes, 1250 subtest passes, 0 failures, 8 skips
cafe gate floors: floors unchanged: packages 33, suite 1380, subtests 1250
cafe gate floors: re-pinned caf.lock
$ md5 gate.yml caf.lock                                  # after
MD5 (gate.yml) = 2284f231d2dbc0965e9c2a268ed9aee5
MD5 (caf.lock) = fda9825b709087c94c9cce05c964da7b
$ git status --short
                                                         # (empty)
```

The bytes are identical and the tree is clean. `RewriteFloors` reports
`changed == false`, so `gate.yml` is not written at all, and `caf.lock` is
re-written with identical bytes. That is the property that makes it safe to run
at every merge.

**Note the first run's red gate.** It reported 4 failures — the stale-floor
ratchet among them — and the writer still derived the right number, because the
floor is what RAN (`TopLevel + TopLevelFailures` = 1376 + 4 = 1380), not what
passed. Counting only passes would have written 1376 and left the gate red.

---

## Gate

```
$ ./bin/prime
cafe: 33 packages, 1380 top-level passes, 1250 subtest passes, 0 failures, 8 skips
cafe: live tier: 0 of 7 executed; not enabled, so the 7 live tests skipped rather than ran.
cafe: live tier: set CAF_LIVE_DOCKER=1, CAF_LIVE_RYUK=1 and CAF_LIVE_KAMAL=1, or run bin/prime --live
```

Green, exit 0. The three floors in `gate.yml` are `packages: 33`, `suite: 1380`,
`subtests: 1250`, all three written by the writer, and `caf.lock` pins the new
`gate.yml` bytes (`c2274ca3…`). `go vet ./...` clean, `gofmt -l .` empty.

---

## What I deliberately did NOT do

- **Did not weaken either assertion.** The ratchet identity and the citation
  content check are the same strength or stronger; the citation walk now checks
  every citation instead of a hardcoded subset.
- **Did not add a `go run ./internal/ci/cmd` helper.** `internal/ci` is declared
  "no code" in the layout, and the repo's user-facing writers are `cafe`
  subcommands (`cafe lock`). `cafe gate floors` is the shape that fits.
- **Did not give the writer a network or database dependency.**
- **Did not touch `internal/cli/box*.go` or `internal/setup/*`** (sibling
  packets), and did not touch another repository (the citation test uses a local
  fixture, not kit).
- **Did not keep the per-packet floor-bump log in `gate.yml`.** Its intent — a
  reader learns why the floors exist — is kept in shorter comments that point at
  the writer; the hand-editing instructions and the merge-history are gone,
  because the writer supersedes them.
- **Did not convert `moon/CAFAYE-OPS.md`'s citation into something that resolves
  under the fleet root.** The file is not at the path the citation names; it is
  skipped with a log, and its anchor is stored so it is checkable where the file
  is. This matches the previous behavior (that citation was never checked).
- **Did not add a subtests ratchet test.** There was none, and a static subtest
  count is not possible; the subtests floor is checked from above by core's
  `gate-check --prove`, as before.
