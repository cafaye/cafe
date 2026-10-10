# HANDOFF — OPS-P10, secret inventory + rotation

**Status: ACCOMPLISHED.** Branch `ops/w2-secret-inventory`, worktree
`/Users/kaka/Code/any/wt/w2-secret-inventory`. Everything committed; tree clean.
No merge, no push — that is the manager's.

## The three things, as asked

1. **Enumerate + classify with the REASON.** `internal/secret/inventory.go`
   holds every key the kit installer's SECRET RECORD declares plus the three it
   does not (Redis password, Postgres role passwords, muse's vault key). Each
   entry carries a class, a reason and a `file:line` citation.
2. **The rotation path for the rotatable class, on a schedule.** `caf secret
   rotate [<name>]` and bare `caf secret rotate` for everything DUE, with
   `--dry-run`, `--yes`, `--all`, `--timeout`. The schedule is a state file
   (`secret-rotations.json`, no credential in it), so "on a schedule" is a fact
   about the last run rather than a cron line.
3. **A NAMED refusal for the rest.** Non-rotatable and conditional entries are
   refused with the class, the reason and the citation, on stdout as JSON and
   on stderr as prose, exit 1. Nothing is skipped silently.

## Verification actually observed

| command | result |
|---|---|
| `go build ./...` | exit 0 |
| `go test -count=1 ./internal/secret/` | ok, exit 0 |
| `go test -count=1 ./internal/cli/` | ok, exit 0 |
| `./bin/prime` | **exit 0** — `24 packages, 1083 top-level passes, 1190 subtest passes, 0 failures, 8 skips` |
| `go vet ./...` | exit 0, no output |
| `gofmt -l .` | no output, exit 0 |

## If you are reading this because something is red

1. **The worktree is gone.** `git worktree prune && git -C <repo> worktree add
   <path> ops/w2-secret-inventory`. The branch never lost a commit. Two
   mid-session deletions of `/Users/kaka/Code/any/ops-wt/` took uncommitted
   files each time; the branch survived both.
2. **The lock tests are red.** If you edited `gate.yml`, re-run
   `go run ./cmd/caf lock .` in the same commit — gate.yml is the tree's one
   `rule-bundle` and its hash is pinned.
3. **The floor ratchet is red.** `declared - minimum == 8`. Raise the floor in
   `gate.yml` in the same commit as the tests; do not touch the assertion.

## What is deliberately left, and why

- `--timeout` is wired but has no test asserting it fires against a hung
  runner; `--all` has no test that it skips the prompt.
- The citations point at fleet paths outside this repository (`cafaye/kit`,
  `cafaye/identity`, `cafaye/muse`, `moon/CAFAYE-OPS.md`). They are checkable
  by hand and there is no CI that proves the line numbers, and there cannot be
  one without vendoring a copy that could drift — `internal/secret/doc.go`
  says so out loud.
- `postgres-role-<service>` is the one placeholder name in the table: the
  rotation is an ALTER ROLE per service role, and no test asserts the family.

The full argument is in `REPORT-w2p10-secret.md`, including the two defects
the tests found (an apply document with an empty key name, and a refusal report
spelled `Name`/`Reason`/`Source`) and the off-by-one the presence check's
position introduced.
