# REPORT — mvp-cafe-dburl-name-01

**Packet:** `cafe dev` hands every service the wrong database variable name.
**Repository:** `cafaye/cafe`, branch `worker/cafe-mvp-cafe-dburl-name-01`,
worktree `wt-m39-cafe-dburl-name-01`, based on `master` at `42b53d8`.
**Not pushed, not merged.** One commit. §9 says a commit is worth a reviewer's
attention; this one is.

---

## 1. What the defect was

`internal/dev/registry.go` built the environment a service gets for the
infrastructure it is wired to. One table, one hardcoded name per kind of
infrastructure, and a comment claiming the names were platform-wide:

```go
// connectionURLs is the environment a service gets for each piece of
// infrastructure it is wired to. The names are the conventional ones every
// language in the platform already reads, so a service image needs nothing
// added to it to find its own database.
var connectionURLs = map[string]func(project string) Env{
	"postgres": func(project string) Env {
		return Env{Name: "DATABASE_URL", Value: "postgres://…"}
	},
	…
```

**The claim was false.** I re-measured it myself rather than taking the packet's
table on trust, by grepping each service's own config loader for the variable it
opens a database with:

| service | reads | `cafe dev` gave it | |
| --- | --- | --- | --- |
| identity | `DATABASE_URL` | `DATABASE_URL` | ok |
| darkroom | `DATABASE_URL` | `DATABASE_URL` | ok |
| **pantry** | **`PANTRY_DATABASE_URL`** | `DATABASE_URL` | **broken** |
| muse | `MUSE_DATABASE_URL` | `DATABASE_URL` | broken, **out of MVP scope, untouched** |

The measurement, reproduced on this machine from a clean build of `master`:

```
$ cd pantry && cafe dev --dry-run
  pantry:
      DATABASE_URL: postgres://pantry:pantry@postgres:5432/pantry
      REDIS_URL: redis://redis:6379/0
```

and pantry's own loader refuses exactly that stack in one sentence
(`pantry/cmd/pantry/main.go:157`):

```
no database is configured: PANTRY_DATABASE_URL is unset, so there is no address
to reach the registry
```

So the cost of this bug was not the failure. It was the shape of it: the CLI
documented a platform-wide convention it had never checked, and every service
that disagreed with the convention got a container that exited on boot.

## 2. How I proved it was real, before changing anything

Three measurements, in this order, all on `master` at `42b53d8`.

**(a) The old CLI could not even express the fix.** cafe refuses catalog keys it
does not model — that is `checkCatalogKeys`, and it is the same guard that
caught a misspelled `image`. So a catalog that tried to say which variable a
service reads was not ignored; it was *rejected*:

```
$ cafe-before dev -registry caf.dev.json --dry-run
cafe: cafe dev: unusable service catalog: 1 key no entry has: "connections" in the
entry for pantry; cafe would have dropped it without saying so. An entry takes
name, image, command, port, publish, environment, volumes, healthcheck,
dependencies; …
```

This is worth stating because it is the shape of the hole: the declaration did
not exist anywhere, and the guard that should have caught the omission pointed at
a *hand-typed key list* instead.

**(b) The defect itself**, same catalog with the key removed, so cafe had nothing
to go on:

```
  darkroom:  DATABASE_URL: postgres://demo:demo@postgres:5432/demo
  demo:      DATABASE_URL: postgres://demo:demo@postgres:5432/demo
  pantry:    DATABASE_URL: postgres://demo:demo@postgres:5432/demo     ← wrong
```

**(c) After**, same catalog, same project, new binary:

```
  darkroom:  DATABASE_URL: postgres://demo:demo@postgres:5432/demo
  demo:      DATABASE_URL: postgres://demo:demo@postgres:5432/demo
  pantry:    PANTRY_DATABASE_URL: postgres://demo:demo@postgres:5432/demo  ← fixed
```

The address did not move. Only the name did.

## 3. What I changed

**The mechanism: a service's catalog entry names the variable it reads.**
`Entry.Connections map[string]string`, keyed by infrastructure name:

```json
"pantry": {
  "name": "pantry",
  "image": "ghcr.io/cafaye/pantry:0.4.1",
  "connections": { "postgres": "PANTRY_DATABASE_URL" }
}
```

Four decisions, each with the alternative I rejected:

**Why the catalog entry, not the manifest.** `cafaye.yml` is closed — core's
schema ends in `additionalProperties: false`, and pantry's own
`registry/index.yml` says so in as many words ("`kind` and `basePath` cannot be
added to a manifest — and adding them would be inventing vocabulary core does not
have"). cafe also vendors that schema byte-for-byte with a pinned sha256
(`internal/contract/schemas/README.md`), so a manifest field is a core release, a
re-vendor, a sha bump and a spec-version bump before it validates. The catalog is
cafe's own type with no such chain. **Rejected alternative:** the manifest, because
it is not cafe's to change.

**Why a map keyed by infrastructure, not a `database` string field.** A single
`"database": "PANTRY_DATABASE_URL"` would have needed `if kind == "postgres"`
somewhere to know which row it overrode — a special case, and this packet's first
constraint is that there be none. Keyed by infrastructure, `wire` reads
`svc.Connections[dep]` and there is no service name anywhere in the mechanism.
It also generalises for free: a service that reads `PANTOPHERY_REDIS_URL` is
already covered, and a test says so.

**Why `connections` and not `environment`.** The address is built per project
(`postgres://<project>:<project>@postgres:5432/<project>`). A catalog that spelled
one out would go stale the moment a developer renamed their checkout, and the
failure would be a container booting against a database that does not exist. The
catalog names the *variable*; cafe renders the *address*.

**Why a bad declaration is refused rather than ignored.** `connections` is a map,
and a map's keys are silent by construction — nothing in a decoder complains about
a map key it does not recognise. `{"postgresql": …}` would decode into a perfectly
good entry and the service would get the default `DATABASE_URL`: the original
defect, back again, in a catalog that reads as though it were fixed. So
`ReadCatalog` refuses a key that is not in `infra` and a value that is not a
variable name, naming every mistake in one sentence. This is the same reasoning as
the `imag`/`image` refusal already in this file, one layer down.

The false comment at `internal/dev/registry.go:580-583` is **replaced**, not
appended to — it now carries the measurement, the three-service default argument,
and the reason a derived default was rejected.

## 4. The tests, and the mutation that proves they bite

Thirteen new tests: eight in `internal/dev/plan_test.go`, five in
`internal/dev/registry_test.go`. `gate.yml`'s suite floor moved 714 → 727 in this
commit with the arithmetic written out (735 declared, 8 skips, 727 run).

**The pair the packet asked for**, and the mutation:

`TestAServiceThatDeclaresNothingStillGetsTheConventionalName` is the one that
stops this being a rename. I watched it go red.

*Mutation 1 — change the default.* `internal/dev/registry.go:721`,
`Variable: "DATABASE_URL"` → `Variable: "PANTRY_DATABASE_URL"`.

The mutation is in the file (verified by `grep -n` and `diff` on the file itself,
not by exit status):

```
721c721
< 		Variable: "DATABASE_URL",
---
> 		Variable: "PANTRY_DATABASE_URL",
```

Result: `go test ./internal/dev/` → **exit 1**, seven failures including both of
the required pair, and the declaration test stayed green:

```
--- PASS: TestAServiceThatDeclaresItsDatabaseVariableIsHandedThatName
    plan_test.go:1432: alpha DATABASE_URL = "", want "postgres://stack:stack@postgres:5432/stack"
    plan_test.go:1432: stack DATABASE_URL = "", want "postgres://stack:stack@postgres:5432/stack"
--- FAIL: TestAServiceThatDeclaresNothingStillGetsTheConventionalName
```

Reverted; suite exit 0.

*Mutation 2 — the forbidden implementation.* I added
`if svc.Name == "pantry" { variable = "PANTRY_DATABASE_URL" }` to
`connectionEnv` and confirmed it was in the file at lines 754-756, then ran the
suite: **exit 1**, `TestAServiceThatDeclaresItsDatabaseVariableIsHandedThatName`
and `TestADeclaredNameBelongsToTheServiceThatDeclaredIt` both red — because a
hardcode overrides the *declared* value with the wrong address, and because alpha
on the same stack would be renamed by pantry's presence. Reverted; `grep -c
'svc.Name == "pantry"' internal/dev/registry.go` → 0.

**Before: default-rename mutation exit 1, hardcode mutation exit 1. After:
suite exit 0, three consecutive `bin/prime` runs green.**

The rest of the thirteen, and what each is for:

| test | what it holds |
| --- | --- |
| `…DeclaresItsDatabaseVariableIsHandedThatName` | the declared name reaches the rendered compose, scoped to pantry's own block (a document-wide `DATABASE_URL` search would be red for the *right* behaviour, and `PANTRY_DATABASE_URL` *contains* `DATABASE_URL` — the check is anchored on the indent) |
| `…DeclaresNothingStillGetsTheConventionalName` | the default, for a registry entry **and** for the project no catalog describes |
| `ADeclaredNameBelongsToTheServiceThatDeclaredIt` | one stack, two names: pantry declared, alpha and stack not |
| `TheProjectServiceHonoursItsOwnDeclaredName` | `addProject`, not just `add` — the direction a reader looks at second |
| `AnyDeclaredVariableNameIsHonoured` | five shapes, including `db`, `_INTERNAL` and a non-prefix name, because a mechanism that only understands `<SERVICE>_DATABASE_URL` has the same hardcode in a loop |
| `ADeclaredNameAppliesToAnyPieceOfInfrastructure` | the mechanism is the table's, not the database's: declaring redis leaves the database alone |
| `ARegistryValueStillOutranksTheDeclaredAddress` | the declaration chooses the *name*; it does not outrank a value the registry supplied |
| `ANoInfraStackCarriesNoDatabaseVariableUnderAnyName` | a declaration does not conjure infrastructure |
| `ReadCatalogRefusesAConnectionsKeyThatNamesNoInfrastructure` | scope derived from `infraNames()`, not a written list, so a third kind of infrastructure is covered on the day it lands |
| `ReadCatalogRefusesAConnectionsValueThatIsNotAVariableName` | five rejected shapes including a pasted URL |
| `TheConnectionsErrorNamesTheServiceAndEveryMistake` | every mistake at once, not the first |
| `TheConnectionsErrorReadsAsASentenceAtEveryCount` | the `counted`/`pronoun` grammar rule at 1 and 2, which is the rule that once produced `cafe would have dropped 1 it` |
| `ReadCatalogAcceptsAnEntryThatDeclaresNoConnections` | the undeclared shape stays free — it is three of the four services |

I also added `connections` to `TestCatalogWireShape`, because a field nothing
reads on the wire is a field that only works in a test. That test caught nothing
on its own; it is there so the wire form is covered when pantry starts serving
the official catalog.

## 5. `bin/prime`

Green, three consecutive runs, from a warm cache:

```
cafe: 15 packages, 727 top-level passes, 876 subtest passes, 0 failures, 8 skips
cafe: live tier: 0 of 7 executed; not enabled, so the 7 live tests skipped rather than ran.
```

- **suite floor** 714 → **727**. Arithmetic: 735 declared, 8 skips, 727 run.
  `expectedSkips` stays at 8 — none of the thirteen is env-gated, so no new test
  arrived as a new tier, which is the same reason cafe-30's seven and cafe-33's two
  did not move it.
- **subtests floor** stays at **875**. Measured 876, floor one lower by the
  standing one-lower rule. It did not move because **I added zero `t.Run` rows** —
  all thirteen are flat asserts. That is stated in `gate.yml` because a floor that
  moved for no reason is how the next reader stops believing these comments.
- `caf.lock` re-pinned: `gate.yml` is a `rule-bundle` in the lock, so the tree
  stopped matching its committed lock and said so. Fixed with `cafe lock .`, not by
  editing the assertion — 12 files (1 spec, 7 vendored-schema, 3 generated-client,
  1 rule-bundle).

### Skipped, named and counted

**The live tier did not run: 0 of 7.** No container runtime was enabled for this
dispatch, and every run says so in the line `gate.yml`'s `live-tier` proof
matches. Those seven tests are `internal/{ports,ryuk,deploy,backup}` and are not
affected by this change — nothing here starts a container.

**One flake fired once, and it is not mine.** During development a full-suite run
reported `TestAnIPv4OnlyProberMissesAnIPv6Listener` red:

```
ports_test.go:272: the IPv4-only prober saw an ::1 listener on port 49374;
this machine does not have the blind spot the test is about
```

Characterised: it failed **1 run in 5** full-suite runs, and passed **5/5** in
isolation on my branch and **8/8** in isolation on unmodified `master`. My diff
touches `internal/dev/{plan,registry}{,_test}.go` and nothing in `internal/ports`.
The test holds a port from `net.Listen("tcp6", "[::1]:0")` — an *ephemeral* port —
and asserts that nothing else on the machine has it between the listen and the
probe. Under a parallel `go test ./...` with several fleet workers running, an
ephemeral port is a shared resource. See §7.

## 6. Which of the four services this fixes, and which it does not

| service | status | why |
| --- | --- | --- |
| **pantry** | **fixed** | declare `connections.postgres: PANTRY_DATABASE_URL` in its catalog entry. Measured before/after in §2. |
| identity | unaffected, and must stay so | reads `DATABASE_URL`; the default did not move. |
| darkroom | unaffected, and must stay so | same. |
| **muse** | **still broken, deliberately not touched** | out of MVP scope per the packet. The same one-line declaration fixes it; nothing in this change makes muse worse. |

**The one thing this does not fix, stated plainly: `cd pantry && cafe dev
--dry-run` with no `-registry` still prints `DATABASE_URL`.** Measured, not
assumed. The reason is structural and it is the honest limit of the
catalog-entry mechanism:

`addProject` resolves the project through the registry too, so a catalog entry
describing pantry *is* honoured — but naming the project in a catalog replaces its
Dockerfile with a pull of `image:`, which is the trap `parlor/caf.dev.json`'s
`x-self` key documents at length ("Naming parlor here would silently replace this
repository's Dockerfile with a pull of an image that does not exist locally").
Measured:

```
$ cd pantry && cafe dev --dry-run                                       # no -registry
      DATABASE_URL: postgres://pantry:pantry@postgres:5432/pantry      # unchanged
$ cafe-after dev -registry pantry.dev.json --dry-run                    # catalog names pantry
    image: ghcr.io/cafaye/pantry:0.4.1                                 # no build:
      PANTRY_DATABASE_URL: postgres://pantry:pantry@postgres:5432/pantry
```

So the project-itself case needs a *second* mechanism — a declaration cafe reads
from the project directory, or a change to `addProject`'s build-over-image rule,
or a core manifest field. All three are real designs and none of them is this
packet's scope. I chose not to fold one in because each changes behaviour for
every existing project (auto-discovery changes `cafe dev` for `parlor`, which
already ships a `caf.dev.json`), and a reviewer asked to approve a database
variable name should not also be approving a catalog discovery rule.

**The dependency case — the one the packet was dispatched for, and the one that
produces "a service that depends on pantry cannot start" — is fully fixed**, and
`TestTheProjectServiceHonoursItsOwnDeclaredName` covers the other direction so the
gap is a stated boundary rather than an unknown.

## 7. Findings outside the packet

1. **`internal/ports`: `TestAnIPv4OnlyProberMissesAnIPv6Listener` is
   load-sensitive and will flake.** It takes an ephemeral port and asserts no
   other process on the machine holds it. 1 failure in 5 full-suite runs on this
   machine; 0 in 13 isolated runs. Fix is not obvious and is not mine to guess:
   either probe a port inside a block the test owns (the file already has
   `scratch = Block{42000, 42099}` for exactly this reason) or re-listen until the
   port is stable. **Not fixed here** — it is another package's concurrency
   assumption and folding it in would widen this diff for no gain to the packet.

2. **The project's own repository still cannot declare this**, for the structural
   reason in §6. Worth its own packet; the shape is a cafe-owned sidecar read by
   `dev.Load`, not a manifest field (core closes the manifest) and not auto-
   discovery of `-registry` (that changes `cafe dev` for every repo that already
   ships a catalog).

3. **pantry's catalog entry has to be published for any of this to reach a
   developer.** The mechanism exists and is measured; what does not yet exist is
   a catalog naming pantry with `connections`, in `pantry/registry/` or the repo's
   own `caf.dev.json`. I did not add it: it is a different repository, and the
   packet is scoped to caf. **One JSON file's worth of work.**

4. **`muse` needs the same one line** and is deliberately untouched. Named so it is
   not rediscovered.

## 8. Not this packet, confirmed

- **The stale `pantry/caf.dev.compose.yaml`.** Re-checked: the file is gitignored,
   rewritten on every run, and a regenerated one says `Dockerfile`, not
   `docker/Dockerfile`. `resolveBuild` (`internal/dev/project.go:102-112`) tries
   `docker/Dockerfile` first and falls back correctly. The packet asked for a
   cheap way to make "a generated file was read as a source of truth" louder and
   said to move on if there was not one. **There is already one, and it was
   written by someone else:** the rendered document's own header, printed by
   `cafe dev` on every run:

   ```
   # cafe dev — the local stack for "pantry-dev", generated from cafaye.yml.
   # Do not edit: every run rewrites this file. The manifest is the
   # source of truth; this is what it turned into.
   ```

   The finding is that it is a *comment*, which no tool reads. A generation stamp
   naming the cafe build would be machine-checkable, but that is a change to
   `render.go`, which the packet said not to touch. Recorded, not done.

- **`muse`** — measured, noted, untouched. See §6.

## 9. What I would do next

1. Publish a catalog entry for pantry with
   `"connections": {"postgres": "PANTRY_DATABASE_URL"}`. One file, unblocks the
   developer-facing half, and is the only thing standing between this commit and
   `cafe dev` working in pantry.
2. Decide the project-side declaration (§7.2) as its own packet, with `parlor`'s
   existing `caf.dev.json` as the precedent to design against.
3. Fix the `internal/ports` flake (§7.1) in its own packet, with the scratch block
   the file already declares.
4. `muse` gets the same declaration.

## 10. Checklist

- [x] Committed to `worker/cafe-mvp-cafe-dburl-name-01`. Not pushed, not merged, not
      tagged.
- [x] One commit, worth a reviewer's slot.
- [x] Every command bounded by `timeout` (600s on `go test`, 900s on `bin/prime`,
      180s on `go build`).
- [x] Skips named and counted: the live tier (0 of 7), and the one flake with its
      1-in-5 measurement.
- [x] Both "fixed" claims carry before/after: §2 for the defect, §4 for the two
      mutations, each with the mutation verified present in the file by text.
- [x] `REPORT-mvp-cafe-dburl-name-01.md` exists and is committed.
- [x] Findings outside the packet are named in §7.
- [x] No container, image or volume left running. Nothing in this packet starts
      one; `cafe dev --dry-run` renders and exits, and the two demo projects live
      in a temp directory.
- [x] No other session's worktree, branch or container touched. The pantry repo was
      read and `cafe dev --dry-run` was run in it, which rewrites its gitignored
      generated compose file; `git status` in `pantry` is clean, verified after.