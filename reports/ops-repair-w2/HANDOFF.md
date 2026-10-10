# ops-repair-w2 — handoff

Packet: REPAIR worker on `caf`, worktree `/Users/kaka/Code/any/ops-wt/repair-w2`,
branch `ops/repair-w2`, commit `b138656`.

## What this packet was for

Four parallel packets merged into `caf` master and collided in three mechanical
ways. This worker owns the middle one: the CLI fixture that names every command,
and the lock. The manager owns `gate.yml`.

## Outcome per failure

### 1. CLI fixture — FIXED, green

`internal/cli/command_test.go`'s `TestRegistryContainsEveryCommand` expected list
was missing `cve` and `secret`. `internal/cli/stub_test.go`'s `workingCommands`
already named `cve` but not `secret`. Both fixtures now match the registry, in
alphabetical position so `TestRegistryIsSorted` holds.

- `command_test.go`: `"cve"` after `"contract"`, `"secret"` after `"resolve"`.
- `stub_test.go`: `"secret"` after `"resolve"`.

Two-line fixture change. No production code touched, no test weakened, no
assertion relaxed.

### 2. `caf.lock` — NOT CONFLICTED, needed no change

**The failure described in the brief did not reproduce.** `caf.lock` at HEAD
carries no conflict markers, and all three lock tests pass:

- `TestTheTreeMatchesItsCommittedLock`
- `TestTheCommittedLockGoesRedWhenAPinnedFileMoves`
- `TestTheCommittedLockOnlyUsesKindsCafKnows`

So the merge that produced HEAD (`4f4d953`) had already resolved it. To satisfy
the "regenerated, never edited" rule I ran the repo's own command —

```sh
go run ./cmd/caf lock .          # "wrote caf.lock — 12 files (1 spec, 7 vendored-schema, 3 generated-client, 1 rule-bundle)"
go run ./cmd/caf lock --verify . # "caf.lock matches 12 pinned file(s), and nothing the tree declares is missing from it."
```

`git diff caf.lock` after regeneration was **empty**: the committed lock is
byte-identical to what the tree's own generator emits. There is nothing to
commit, and committing a no-op rewrite would add a commit with no content.

### 3. Conflict markers in `gate.yml` / `caf.lock` — NONE

Grepped both files for `<<<<<<<`, `>>>>>>>` and `=======`. No markers anywhere in
`gate.yml` or `caf.lock`, nor in any tracked file. No STOP was required.

## The one remaining red, which is the manager's

`go test ./internal/ci/` is red on **exactly one** test, and it is not this
packet's:

```
--- FAIL: TestTheGateFloorIsNotBelowTheSuiteCafClaimsToHave
    the tree declares 1128 top-level test(s) and gate.yml's "suite" proof sets
    minimum: 1079, so this gate runs 1079 of them and skips 49. The skips are
    supposed to be exactly 8.
      If you ADDED tests, raise minimum to 1120 in this same commit.
```

The other 10 tests in `internal/ci` pass, including all three lock tests.

**The collision, named precisely.** Both branches raised the same floor, measured
against different trees, and the merge took the lower number:

| branch | `suite` floor | measured against |
|---|---|---|
| `ops/w2-cve-surface` (`0b43fa1`) | 1080 | 1088 declared on the merged tree |
| `ops/w2-backup-drill` (`80ef001`) | 1079 | 1087 declared, at that branch |

HEAD kept `1079`. The tree now declares 1128; with `expectedSkips = 8` the floor
belongs at **1120**.

This is `gate.yml`, which the manager owns. This worker did not touch it.

## THE TRAP for whoever lands `gate.yml`

`caf.lock` pins `gate.yml` as the tree's one `rule-bundle` (sha256
`9d6a088f…`). Raising the floor changes that hash and
`TestTheTreeMatchesItsCommittedLock` **goes red on the next run** unless the lock
is regenerated in the same commit:

```sh
go run ./cmd/caf lock .   # after editing gate.yml, in the same commit
```

This is AGENTS.md's own rule ("a commit that adds tests raises the floor in
`gate.yml` AND has to re-run `go run ./cmd/caf lock .`"). Whoever resolves the
gate-floor conflict must do both edits together or the merge lands red.

## Exit codes actually observed

| command | exit |
|---|---|
| `go test ./internal/cli/` | **0** (62.993s, full package) |
| `go test ./internal/ci/` | **1** — `TestTheGateFloorIsNotBelowTheSuiteCafClaimsToHave` only |
| `go test ./internal/cli/ ./internal/ci/` | **1** (for that same reason) |
| `go build ./...` | **0** |
| `gofmt -l .` | **0** (prints nothing) |
| `go vet ./...` | **0** |
| `go run ./cmd/caf lock .` | **0** (byte-identical output, empty diff) |
| `go run ./cmd/caf lock --verify .` | **0** |

`bin/prime` was also run in the background. Its summary line reads
`caf: 25 packages, 1119 top-level passes, 1204 subtest passes, 1 failures, 8 skips`
— i.e. the whole tree's only failure is the manager's floor test. Its own exit
code was **not** captured into a file (I omitted the `echo $? > $G.exit` wrapper
for that one run), so treat the exit code above as authoritative rather than
prime's; the per-package codes are exact.

## Successor's first move

Nothing to continue here. This packet's two failures are done: the fixture is
fixed and the lock is confirmed byte-identical to what the generator emits. The
only open item is the manager's `gate.yml` floor — read it in the table above,
set `minimum: 1120`, and re-run `go run ./cmd/caf lock .` in the same commit.
