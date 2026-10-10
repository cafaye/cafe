# REPORT — OPS-P10, the secret inventory and the rotation path

Branch: `ops/w2-secret-inventory`. Package: `internal/secret`, command:
`caf secret inventory` / `caf secret rotate`.

## What the packet was for

Every box holds the same secrets, because the kit substrate installer creates
the same ones in the same namespaces. What is NOT the same is which of them can
be rotated. An operator who does not know the difference spends a weekend
discovering it — muse's vault key seals every stored credential (a rotation
forks all of them), identity's JWKS publishes exactly one key (a rotation of
the OIDC signing key is a cutover with no overlap window), while
COURIER_SECRET_BOX_KEY, SECRET_KEY_BASE and the Redis password rotate by a
replace-and-restart with no second holder anywhere.

So the packet is that difference, written down: enumerate what the box holds,
class each entry rotatable / conditional / not-rotatable WITH THE REASON and
the file:line that reason comes from, put the rotatable class on a schedule,
and REFUSE the rest by name rather than skipping silently.

## What landed

| surface | what it is |
|---|---|
| `internal/secret/inventory.go` | the table: every key the installer's SECRET RECORD declares, plus the three it does not carry (the Redis password, the Postgres role passwords, muse's vault key). Each entry has a class, a reason and a citation. Also `Find`, which matches on the name and on the `namespace/secret/key` spelling. |
| `internal/secret/rotate.go` | `PlanRotation` (pure), the `Runner` seam, `Generate` in the installer's own spellings (base64 of 32/64 bytes, hex of 24), the redaction marker, and `Due`. |
| `internal/secret/state.go` | the schedule: `last rotated` per entry in a JSON file that holds no credential, written atomically. |
| `internal/secret/render.go` | the one JSON document `caf secret inventory` prints, and the refusal report. |
| `internal/cli/secret.go` | the two verbs, the `--dry-run`/`--yes`/`--all`/`--timeout` surface, the `askOnStdin` prompt, the stdout/stderr split. |
| tests | `internal/secret` (20 top-level, 29 subtests) and `internal/cli/secret_test.go` (10 top-level, 2 subtests). |

The classes as asserted by `TestTheRotatableClassIsThePacketThree`:
`courier/courier-secrets/SECRET_KEY_BASE`, `courier/courier-secrets/
COURIER_SECRET_BOX_KEY`, `data/redis-password/password`. Everything else is
conditional or not-rotatable, and each says why.

## The invariant, and how it is held

THE INVENTORY NEVER READS A SECRET'S VALUE. It is a property of the types:
`Entry` has no field that could hold one. The one read a rotation performs —
the presence check — is templated `{{range $k, $v := .data}}{{$k}} {{end}}`, so
what reaches the process is key NAMES. A generated value travels in an apply
document on stdin, because argv is visible to every other process on the box
through /proc. `Step.Redacted` is the marker-substituted document, and it is
the only one a dry run prints.

The tests hold it from both ends: `TestSecretInventoryNeverPrintsAValue`
rotates an entry, reads the generated value back out of the apply document the
runner received, and asserts the inventory does not contain it — with the
control half asserting MUSE_VAULT_KEY and "rotatable" ARE printed, because a
build that redacted everything would pass by leaking nothing.

## Two defects the tests found, both fixed

1. **`WriteTargets()` took `e.Key` raw.** Every entry but the Redis password
   has an empty `Key` (the key is the entry's own name), so the plan built an
   apply document with an EMPTY KEY NAME and a report that read
   `wrote courier/courier-secrets key , restarted courier/deployment/courier`.
   Fixed with `Entry.KeyOrName()`, used by both `String()` and
   `WriteTargets()`. `TestRotateAnswersWithTheReportOfWhatChanged` caught it.
2. **`secret.Refusal` had no json tags.** The refusal report — the product of
   the command — printed `"Name"`, `"Reason"`, `"Source"` while every other
   document in the tree speaks lowercase, so a script parsing the refusal
   would have found nothing to parse and the packet's named refusal would have
   been named at nobody. Caught by `cli/secret_test.go`'s
   `TestSecretRotateRefusesTheNotRotatableClassByName`.

Two more were found by reading the real command's output rather than by a test,
and both are fixed: the restart steps read `restart courier/courier` (namespace
and deployment name with no separator) instead of
`restart courier/deployment/courier`, and a dry run of ONE rotatable entry
printed a bare `{}` at the end because the report rendered with nothing rotated
and nothing refused.

A third bug was fixed by inspection rather than by test: the presence check is
the FIRST step of every plan, which made the positional
`writeTargetAt(entry, i)` off by one — it would have handed the presence check
a stdin manifest and the first write the second write's document. The step now
carries the `Target` it writes as an unexported field, so the plan says what it
writes instead of an index arithmetic that a new step would break.

## The schedule

"On a schedule" is a state file, not a timer: `Due(state, now)` is a function
of the last-rotated record and a clock, so the schedule is a fact about the
last run rather than a cron line nobody can see. 90 days, per entry, and a box
that has never rotated is due on all three — the honest first answer.

A named non-rotatable entry exits 1 with the reason and the citation on both
streams; the scheduled run rotates what is due and restates the standing
refusals every time, so the answer to "and the rest?" is never a silence.

## Gate numbers actually observed

- `go build ./...` — exit 0
- `go vet ./...` — no output, exit 0
- `gofmt -l .` — no output
- `go test -count=1 ./internal/secret/` — **ok, 0.63s, exit 0** (20 top-level)
- `go test -count=1 ./internal/cli/` — **ok, 62s, exit 0**
- `bin/prime` (the full gate) — **exit 0**, final run:
  `caf: 24 packages, 1083 top-level passes, 1190 subtest passes, 0 failures, 8 skips`
  plus `caf: live tier: 0 of 7 executed`, which the declaration requires and
  which `--live` is the mode that cannot silently not run.

Two red runs happened first, and both are named because a green that was not
earned is worth nothing:

1. `1080 top-level passes, 2 failures` — the lock tests, because the floors had
   been raised without `caf lock` being re-run over the moved `gate.yml` hash.
   Fixed by re-running the lock, which is AGENTS.md's step 6.
2. `1082 top-level passes, 1 failure` — `TestTheGateFloorIsNotBelowTheSuite-
   CafClaimsToHave`: the tree declared 1091 top-level tests, not 1090, because
   the citation walk was one more than the measurement was taken with. The
   floor went 1051 → 1082 → 1083, measured each time rather than guessed
   (1091 declared − 8 expected skips).

The floors are `packages: 23`, `suite: 1083`, `subtests: 1182`, and the
`subtests` floor is deliberately 8 under the measured 1190: the suite proof is
the ratchet (`declared − minimum == expectedSkips`, exact in both directions)
and the subtest floor is only a floor.

## The citations are checkable, and the walk found a real defect

`TestEveryCitationPointsAtSomething` walks every `file:line` this package cites
against the fleet when it is checked out beside the worktree, and skips when it
is not — a walk rather than a fetch, because a citation fetched at test time
would be a citation that could move without anybody editing this tree. It found
one: the Postgres role passwords were cited at `install.sh:888,893-910` when
the dry-run line is **887** and the creation is **907-909**. An off-by-one in a
citation is exactly the failure this test exists for — a reader who follows it
lands on the wrong sentence and concludes the reason is wrong.

## Two things about this worktree a successor must know

1. **`/Users/kaka/Code/any/ops-wt/` was deleted mid-session** — by the
   manager's cleanup, twice, taking uncommitted files with it. The branch and
   its commits always survived. The worktree was recreated at
   `/Users/kaka/Code/any/wt/w2-secret-inventory` and the session moved there;
   that path is stable. If you find the worktree gone, `git worktree prune`
   and re-add the branch — do not assume the work was lost.
2. **The harness auto-commits in-flight work as `recover(...)` commits.** The
   `c8f1a87` and `da85b0f` commits are those snapshots, and one of them holds
   a *different* (and complete) `internal/cli/secret.go` than the one being
   written when the snapshot fired. When you find your own uncommitted edit
   contradicting HEAD, read HEAD's version before overwriting it — that is how
   the CLI surface survived the deletion at all.

## What is left

Nothing blocking. Remaining, in order of value:

- `caf secret rotate --all` has no test that the prompt is skipped; the
  `--timeout` flag is wired but not asserted against a hung runner.
- The citations point at fleet paths (`cafaye/kit/templates/substrate/
  install.sh`, `cafaye/identity/README.md`, `cafaye/muse/README.md`,
  `moon/CAFAYE-OPS.md`) that are not in this repository. They are checkable by
  hand against the fleet; there is no CI that proves the line numbers, and
  there cannot be one without vendoring a copy that could drift — which
  `doc.go` says out loud.
- The three conditional entries with no `Deployment` (`OIDC_SIGNING_KEY_ID`,
  `OIDC_ISSUER`, `postgres-role-<service>`) are configuration or a placeholder
  shape. The `postgres-role-<service>` name is the one unresolved spelling:
  the rotation is an ALTER ROLE per service, and no test asserts the family.
