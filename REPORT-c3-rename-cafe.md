# REPORT-c3-rename-cafe — every live surface in `cafaye/cafe` says box, not conductor

**Worktree:** `/Users/kaka/Code/any/moon/wt/roll-c3-rename-cafe`
**Branch:** `roll/c3-rename-cafe` (base `b6d507d`)
**Commits:** 3, not merged, not pushed.

| commit | what |
|---|---|
| `10b97b5` | the rename itself — 8 struct tags, 3 doc comments |
| `1d40542` | the pinning test + `gate.yml` floor + `caf.lock` |
| `2409f72` | the pinning test's writer half re-cast as a key-set assertion |

## The decision, and why `box_url`

The field holds the box service's **origin**, and it was spelled `conductor`.
Two candidates were named in the brief; `box_url` is the one taken.

`box` was rejected because **`box` is already this tree's word for WHICH box,
not WHERE it is.** `internal/cve/report.go:22` and `internal/cli/box_edge.go:198`
both carry `"box"` for the enrolled box's name, and `box_edge.go`'s own flag is
`--box` for it. A third meaning for the same two letters would have been a
collision in the tree's own JSON vocabulary.

`box_url` is the word the operator already has for this fact: `--box <url>`
(`internal/cli/box.go:272`) and `CAF_BOX_URL` (`boxURLEnv`, `box.go:93`). So a
document, an environment variable and a flag now name one thing one way, which
is the consistency the brief asked the choice to be judged on.

**The Go identifier stays `ControlPlane`.** That is a different name from
`conductor` and there are ~40 uses of it across six packages, none of them a
residue of the old service. Renaming it would have been a sweep, not the rename
this packet is.

## Counts, per file

`grep -rni conductor`, whole repository, before and after.

**Live surfaces: 16 hits before, 0 after.**

| file | before | after | what was there |
|---|---|---|---|
| `internal/setup/plan.go` | 4 | 0 | 3 tags + 1 comment |
| `internal/setup/setup_test.go` | 2 | 0 | 2 fixtures |
| `internal/boxd/observe.go` | 2 | 0 | 1 tag + 1 comment |
| `internal/box/store.go` | 2 | 0 | 1 tag + 1 comment |
| `internal/box/identity.go` | 2 | 0 | 1 tag + 1 comment |
| `internal/cli/boxd.go` | 1 | 0 | 1 tag |
| `internal/cli/boxd_test.go` | 1 | 0 | 1 fixture |
| `internal/cli/box_test.go` | 1 | 0 | 1 fixture |
| `internal/boxd/route.go` | 1 | 0 | 1 tag |

**Total: 87 → 71.** Live 16 → 0; historical 71 → 71. The final state is
exactly the allowlist below and nothing else.

## The allowlist, and why each entry is in it

15 files, 71 hits, unchanged. The test applied is the brief's: *does a reader
consult this file to do something today?* For all fifteen the answer is no —
each one describes work that has already happened, and several of them are the
record of an earlier rename in the same fleet.

- `CHANGELOG.md` (2) — the named historical allowlist.
- `HANDOFF-w3-observe-03.md` (3) — the named historical allowlist.
- `REPORT-*.md` (66 across 12 files) — the named historical allowlist.

Counts: `REPORT-w3p4-provider.md` 21, `REPORT-w2p7-boxd.md` 10,
`REPORT-w3-observe.md` 8, `REPORT-ops-p19-remote-deploy.md` 8,
`REPORT-w2p6-box.md` 4, `REPORT-w5p3-remote-mcp.md` 3,
`REPORT-w3-observe-02.md` 3, `REPORT-caf-publish-01.md` 2,
`REPORT-caf-pantry-rename-01.md` 2, `REPORT-caf-owner-addressing-01.md` 2,
`REPORT-w5p4-run.md` 1, `REPORT-repair-seams-caf.md` 1,
`REPORT-caf-w1p4-agent.md` 1.

Two of these are load-bearing for the same reason this packet exists, and are
worth naming: `REPORT-w3-observe.md`/`REPORT-w3-observe-02.md` are the record of
the observe stream's design, and `REPORT-caf-pantry-rename-01.md` records a
sibling rename — pantry's `Service` schema renamed with its fields untouched.
Both describe the past and are consulted to understand a decision, not to run
one.

`REPORT-c3-rename-cafe.md` (this file) deliberately describes the old name in
order to say what it became; it is in the allowlist by the same rule.

## Contract-visible changes

**One field name, in eight places, across six documents:**

| reader / writer | the document | direction |
|---|---|---|
| `internal/boxd/route.go:176` | the `box.status` route's answer | wire, out |
| `internal/boxd/observe.go:66` | `Observation` — the `observe.report` answer | wire, out |
| `internal/box/identity.go:39` | `Identity` — `box.json` | on disk |
| `internal/box/store.go:127` | the enrollment journal — `box.json` | on disk |
| `internal/setup/plan.go:326` | `/etc/cafe/box-identity.json` | on disk, written |
| `internal/setup/plan.go:388` | setup's `Enrollment` record | printed JSON |
| `internal/setup/plan.go:492` | the host's `box.json`, read back | on disk, read |
| `internal/cli/boxd.go:242` | `/etc/cafe/box-identity.json` | on disk, read |

`conductor` → `box_url`, in every one.

**Nothing else changed shape.** No environment variable was touched
(`CAF_BOX_URL`, `CAF_BOX_IDENTITY` and `CAF_BOX_RANGE` already read `box`), no
path was touched, no manifest field was touched, no CLI flag was touched, and
no exit code or error string changed. The grep above is the evidence: the
renamed key was the only live surface left.

## The compatibility statement, said out loud

`internal/box/store.go`, `internal/box/identity.go` and `internal/setup/plan.go`
each carried a comment arguing that the key must *stay* `conductor` because a
new binary must read a document an old one wrote. That argument is real and this
packet overrides it, following `moon/PLAN-names-urls-rollout.md`'s own
instruction for the round — *nothing is in prod, rename cleanly* — and the
fleet's own convention that a compatibility shim nobody can see is worse than a
named break.

So the consequence, precisely:

- A `box.json` or a `box-identity.json` written by a **pre-rename** binary
  carries `conductor` and decodes with an **empty** control plane.
- `cafe boxd` on such a host refuses to start, naming it:
  `the enrolled identity at <path> has no control plane`
  (`internal/cli/boxd.go:276`).
- The remedy is one command, and it is the idempotent one the walk already
  runs: `cafe box setup --box <url>`, which rewrites the identity document from
  the operator's own origin and re-reads the host's enrollment. A re-enroll
  (`cafe box enroll`) achieves the same for `box.json`.

The three comments that argued for keeping the old key were rewritten to state
the new decision rather than deleted — a decision that leaves no trace is a
decision the next reader has to make again.

## The narrow command, and what it found

```
$ go build ./...   # BUILD OK
$ go vet ./...     # VET OK
$ gofmt -l .       # internal/box/control_plane.go   ← see below
$ ./bin/prime
cafe: 32 packages, 1358 top-level passes, 1245 subtest passes, 1 failures, 8 skips
cafe: live tier: 0 of 7 executed; not enabled, so the 7 live tests skipped rather than ran.
exit: 1
```

**The one failure is pre-existing and is not this packet's.**
`internal/secret.TestEveryCitationPointsAtSomething` fails on
`cafaye/kit/templates/substrate/install.sh:1239-1245`: the `# THE SPLIT IS BY
PROVENANCE` block was re-worded in another repository (its comment is now
shouted in upper case and the block that names `SECRET_KEY_BASE`, the Redis
password and the Postgres role passwords now sits two lines lower), so the
cited span mentions neither "secret" nor "password" and the citation check goes
red.

It was proved pre-existing by extracting `HEAD~1` — the first commit of this
packet is `10b97b5` and its parent is the branch base — into a scratch
directory and running the same test there:

```
$ git archive HEAD~1 | tar -x -C $D && cd $D && go test ./internal/secret/ \
    -run TestEveryCitationPointsAtSomething
--- FAIL: TestEveryCitationPointsAtSomething (0.00s)
    inventory_test.go:301: cafaye/kit/templates/substrate/install.sh:1239-1245
        does not mention a secret or a password
FAIL
```

My three commits touch 0 files in `internal/secret` (`git show --name-only HEAD`
returns nothing for it). **The gate is red at base, red now, and red for this
one reason.** See the cross-repo section for the exact edit.

`gofmt` flags one file, `internal/box/control_plane.go`, and it is also
pre-existing: a `json.Number` field added to the `installation` struct without
re-running gofmt's tag alignment. My commits do not touch it. Files I touched
are gofmt-clean (`gofmt -l internal/` names only that one).

Core's static half of the declaration check is clean:

```
$ ../core/harness/bin/gate-check .
OK /Users/kaka/.../roll-c3-rename-cafe: 0 failure(s), 3 warning(s)
```

The three warnings are the three `gate.requirement-unproven` entries the
declaration says it will emit by design. `--prove` was not run: it re-runs
`bin/prime`, which takes two minutes and would report `gate.floor` red for the
pre-existing reason above — the proof patterns are anchored on `0 failures`
precisely so a floor can never be read out of a red run.

`cafe lock --verify` is clean: `caf.lock matches 12 pinned file(s), and nothing
the tree declares is missing from it.` (`caf.lock` was regenerated after
`gate.yml` moved — see the next section.)

## The pinning test, and the draft that was thrown away

Every fixture this packet touched was **symmetric**: `internal/setup/setup_test.go`
wrote `enrolledDocument` with the new key and decoded it with the new key, so
the whole suite passed under any tag name at all. The rename was real but
unverified, and the old comment in `internal/boxd/observe.go` claimed "a test
proves the bytes" when none did.

`TestTheIdentityDocumentsKeyIsBoxURL` in `internal/setup/setup_test.go` pins it
in both directions, and was **watched red** on a reverted tag before it landed:

```
--- FAIL: TestTheIdentityDocumentsKeyIsBoxURL (0.00s)
    the daemon's identity document carries no box_url key:
        {"box_id":"","conductor":"https://box.test","version":"1.2.3"}
    the enrolled document decoded the control plane as "", want the document's own origin
```

The first draft asserted that the written bytes did not *contain* the old name,
and fed a document written in that spelling to prove the reader was not still
honouring it. Both wrote the word down in order to assert it was gone, which
left **three live hits of it in the tree** — the packet that removes the residue
would have been the thing a reader greps for and finds. That is the "a build
that redacted everything would pass by leaking nothing" trap from
`internal/agent`'s own control half, reached from the other side. Commit
`2409f72` threw it away.

The replacement asserts the document's **key set** — exactly `box_id`,
`box_url` and `version` — which is strictly stronger than the substring was: a
document carrying the old name as well as the new one has four keys and fails,
and no spelling has to be written down to check it. The reader half is
unchanged: it decodes the real `enrolledDocument` fixture and asserts the
origin, and it fires red on a reverted tag.

`gate.yml`'s `suite` floor is raised **1358 → 1359** for that one test, with the
packet named at the floor's own comment, and `caf.lock` regenerated in the same
commit because `gate.yml` is the tree's one `rule-bundle` and its hash moved.
The ratchet's identity holds: floor 1359, tree declares 1367, the gate runs 1359
and skips the same 8.

```
$ go test ./internal/ci/ -run TestTheGateFloorIsNotBelowTheSuiteCafClaimsToHave -v
gate.yml floor 1359, tree declares 1367 top-level tests, so the gate runs 1359 and skips 8
--- PASS: TestTheGateFloorIsNotBelowTheSuiteCafClaimsToHave (0.09s)
```

## References living in another repository

Three found. None is a consumer of the renamed field — see the first entry for
why that matters — and none is edited here.

### 1. `cafaye/box` — no consumer of the renamed field exists

This is the check the brief asked for and it came back clean. `cafaye/box`
contains **no reference in code to `box-identity.json`, `observe.report` or
`box.status`** (`grep` over `*.rb`, `*.go`, `*.erb`, `*.ts` in `app lib config`
returns nothing), and no live file in the whole fleet carries the `"conductor"`
JSON key. `internal/boxd`'s server half of the line protocol is written down in
`cafe`'s own `client.go` comment and nothing on the Ruby side decodes it yet.

So **no cross-repo edit is required by this rename.** For completeness, the two
`conductor` mentions that do exist in `cafaye/box` are not live surfaces of the
service's name in the sense this packet is about:

- `cafaye/box/cafaye.yml:157` — `url: git@github.com:cafaye/conductor.git`.
  This is a real git remote for the renamed repository, not a reference to the
  service. Changing it is an account/remote operation, not a text edit, and it
  belongs to whoever owns the repo transfer. Do not sed it.
- `cafaye/box/db/migrate/20261010100100_add_subject_user_id_to_audit_escalation_events.rb:14`
  — a dated migration's comment naming `conductor_development`. Migrations are
  history by the same rule the `REPORT-*.md` allowlist follows.

### 2. `internal/secret` — the pre-existing gate failure needs a new test

This is the one thing standing between the branch and a green gate, and it is
not this packet's to fix. The exact edit, in this repository, one line:

```diff
--- a/internal/secret/inventory_test.go
+++ b/internal/secret/inventory_test.go
@@ -281,7 +281,7 @@
 		"cafaye/kit/templates/substrate/install.sh:1106-1109":   {1106, 1109},
-		"cafaye/kit/templates/substrate/install.sh:1239-1245":   {1239, 1245},
+		"cafaye/kit/templates/substrate/install.sh:1239-1250":   {1239, 1250},
 		"cafaye/kit/templates/substrate/install.sh:1263-1265":   {1263, 1265},
```

The cited block is still *about* secrets — `SECRET_KEY_BASE`,
`COURIER_SECRET_BOX_KEY`, the Redis password, the Postgres role passwords — it
just starts two lines lower than the citation claims, so widening the span to
1250 restores what the citation always meant. Deliberately **not** applied here:
it changes what a check asserts, it is not the rename, and a citation that no
longer points where it claims is a finding the gate is correctly reporting. A
reader who wants it green should land it in its own commit with its own message.

### 3. `moon/PLAN-names-urls-rollout.md` and `moon/DECISIONS.md`

Both name remaining `CONDUCTOR_*` work across cafaye — `kit`'s
`KIT_SUBSTRATE_CONDUCTOR_URL`, the website's `conductor.ts` /
`conductor-context.tsx` / `conductor.test.ts`, and `docs/src/content/docs/security.md`.
Those are other repositories and another packet's scope. They were read only to
confirm none of them consumes cafaye's JSON field; they do not.

## Deliberately not done

- **`internal/box/control_plane.go`'s gofmt drift was left.** It is a
  `json.Number` field's tag alignment, unrelated, and fixing it in this packet
  would hide a formatting regression behind a rename.
- **The `internal/secret` citation was left red** rather than widened to make
  the gate green. See above.
- **No compatibility shim for the old key.** Stated above with the remedy
  command.
- **`bin/prime --live` was not run.** It needs a container runtime and the
  declared gate does not; nothing in this rename can reach the seven live
  tests, all of which are about ports, Ryuk and kamal.
- **`gofmt` was run on the files touched** (`gofmt -l internal/` names only the
  pre-existing offender), not on the whole tree as a repair.
- **`tests/gate-declaration-self-test.sh` was not run.** It is not part of
  `bin/prime` and takes minutes; nothing in this packet changes `gate.yml`'s
  shape, `bin/prime`'s summary or the CI workflow's gate step, so a run would
  re-verify a recipe this change cannot reach. The floor bump it would assert
  is the one covered by the ratchet test quoted above.
