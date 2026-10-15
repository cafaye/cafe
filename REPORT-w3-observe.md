# REPORT — OPS-P15, the observe pipeline (cafe half)

Branch: `ops/w3-observe`. Gate: `bin/prime` **exit 0**, observed and captured.
Two commits: `e460deb` (the packet) and `3e015bc` (gate housekeeping). The
manager merges; I did not push.

## What this packet is

`cafe boxd` already dials conductor outbound over mTLS and redeems capabilities
against a whitelisted route table (`internal/boxd`). OPS-P15 makes the READ
half of that channel real: a conductor mints an `observe.report` capability,
and the box answers with one machine-readable `Observation` document that the
policy engine (OPS-P16) and the monthly report (OPS-P13) consume as JSON
rather than prose.

The packet row is `conductor+cafe / both gates`. **This worktree is cafe; I
built the cafe (box-side) half.** The conductor half — the route that receives
and stores the report, and the mint that carries the account's pending
releases into an observe capability — is not in this repository and is the
successor's job (see "What a successor does next").

## What landed

- **`internal/boxd/observe.go`** — the `Observation` document (schema_version,
  box_id, deployed_version, version_range, health, certificate, disk,
  last_drill, pending_releases) and its sub-types, plus `ObserveParams` and the
  read-only `observe.report` route. stdlib-only (`context`, `time`) — the
  package's `TestTheDaemonCannotExec`/stdlib rule holds.
- **`internal/boxd/route.go`** — `World.Observe(ctx, ObserveParams)`,
  `observe.report` in `Register()` (after `box.status`), the section header
  "the four routes" → "the routes".
- **`internal/cli/observe.go`** — the pure, table-driven facts:
  `pendingAgainstRange` (reuses `contract`'s constraint grammar and version
  comparator — not a second parser), `healthStatus`, `certStatus`,
  `drillFact`/`ageDays`, `observeRange` (the `CAF_BOX_RANGE` seam). These live
  in `internal/cli` because they import `internal/contract` and
  `internal/backup`, which boxd may not.
- **`internal/cli/boxd.go`** — `boxWorld.Observe` assembles the report from the
  identity (deployed version), the daemon state file (install phase), the
  enrollment's own client certificate (expiry), the drill directory (last
  drill), and the version-range match against the candidate releases the
  capability carries.
- **`internal/cli/observe_test.go`** — 5 top-level tests: the match
  (in-range-newer/equal/older/out-of-range/malformed/empty-range), the health
  classification, the cert status, and two integration tests that drive the
  whole document through a real self-signed cert, a real drill journal and the
  two env seams, marshal it, and read it back as JSON.
- README route-table row for `observe.report`.

## The design call, and why

**Which releases are pending splits cleanly by who knows what.** Conductor
owns the release catalog; only the box knows which releases fit the range IT
was enrolled under. So the candidate releases ride the capability as
`ObserveParams`, and the box matches them against its own range and deployed
version. Consequence worth stating: **the observe path adds no network call of
its own** beyond the bounded mTLS channel (`dialTimeout`, per-request
deadlines in `client.go`) — the release universe arrives on the capability
that already carries every instruction, so "bound every network call" is
satisfied without inventing a second fetch. If a future version does add a
pantry fetch, it owes `cve`-style `Emit` progress checkpoints; that is not
this version's path.

**The document is shape-first.** Every field a future consumer needs is
declared even where v0 reports it `unknown`. Fields v0 cannot honestly read
carry a status and a reason, never a guess — the pantry principle applied to a
wire format.

## Pre-existing failures I found and swept up (not this packet's fault)

The `bin/prime` I fired first came back with **3 failures that pre-date my
work**: the w2 merges (OPS-P10 `cafe secret`, OPS-P11 `cafe cve`) added commands
and tests but never updated the tables that pin the registry, nor the gate's
suite floor.

- `command_test.go`'s `want` list was missing `cve` and `secret`; 
- `stub_test.go`'s `workingCommands` was missing `secret`;
- `gate.yml`'s `suite` floor was stale at 1079 against 1133 declared tests —
  the ratchet was red on the merged tree, reporting "skips 54 where
  expectedSkips says 8".

Commit `3e015bc` completes that interrupted housekeeping (AGENTS.md "adding a
subcommand" steps 3/4), raises the floor to `1133-8 = 1125` (which now also
carries my five observe tests), and regenerates `caf.lock` (a pinned
rule-bundle changed). Only `caf.lock`'s rule-bundle pin moved; `cafe lock
--verify` passes.

## Gate — exit codes I actually observed

- `bin/prime` → **`PRIME_EXIT=0`**, captured: `cafe: 25 packages, 1125
  top-level passes, 1218 subtest passes, 0 failures, 8 skips`. All three
  floors met (25≥23, 1125≥1125, 1218≥1157); the 8 skips are the 7 live tests
  plus the re-exec'd child, as `expectedSkips` names.
- `go vet ./...` → exit 0.
- `gofmt -l .` → clean (no output).
- `tests/gate-declaration-self-test.sh` → **exit 2, and it is NOT a pass**: it
  needs `cafaye/core`'s `harness/gate_check.py`, which is not checked out
  beside this worktree, so the check could not happen. The ordinary `bin/prime`
  gate is the one that ran, and it is green; the declaration self-test could
  not be exercised here for want of core.

The narrow scope my diff reaches (`go test ./internal/boxd/`, 
`go test ./internal/cli/ -run 'Observe|Pending|HealthStatus|CertStatus'`, and
the registry/stub/lock tests) is green.

## What is deliberately NOT done (honest v0 gaps)

- **Disk is `unknown`.** Reading root filesystem usage needs a platform
  `syscall` (`statfs`) the daemon may not import (`TestTheDaemonCannotExec`
  forbids `syscall`, and the tree must cross-compile for Windows), and the
  stdlib exposes none. The field exists so the day it is measurable does not
  move the schema; it lands with the substrate.
- **The version range comes from `CAF_BOX_RANGE`** (env seam, the same
  env-var-over-config idiom backup/ledger use). A box that has not been told
  its range reports `unknown` and classifies no pending releases rather than
  guessing. Enrollment should write the range into the identity document
  (backwards-compatible optional field) when that lands; until then the env is
  the source.
- **`LastDrill` is the newest drill across all services on the box.** The
  brief asks for "the last drill" (singular); per-service drills in the report
  is a later refinement.
- **The conductor half is not in this worktree.** The schema is fixed; a
  conductor-side worker must mint the capability, feed the pending releases,
  receive and store the report.

## What a successor does next (the relay)

1. **Conductor (the other repo):** add a `POST`/receive route that accepts one
   `Observation` JSON, and an observe mint that carries the account's pending
   `Release` records as `ObserveParams`. The box side is done and waiting on
   exactly that shape.
2. **Policy engine (OPS-P16):** consume `Observation`; the health/reasons,
   cert status and pending releases are the classified inputs the four-way
   classifier branches on.
3. **Disk, when the substrate lands:** flip `Disk.Status` from `unknown` to a
   real reading, without moving the schema.
4. **Enrollment:** write the core range into the box identity document, and
   swap `observeRange()`'s source from env to that field.
