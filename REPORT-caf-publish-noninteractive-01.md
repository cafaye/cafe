# REPORT — caf publish goes non-interactive by default (R5)

Packet: R5, `caf publish` non-interactive flip.
Branch: `worker/caf-noninteractive` (base `7da4191`, the W11 publish merge).
Base report: `REPORT-caf-publish-01.md` — this packet supersedes its
agent-first contract paragraph, not its wire surface.

## The ruling, verbatim

> "for cafaye i think we should make everything non interactive by default as it shall mostly be used by agents"

— user ruling, 2026-10-11. This resolves the seam-audit's D1: **(B) is ruled
out** — there is no interactive requirement on first publish; the agent path
is THE path. Option A's reviewability companion lands separately in pantry (a
`publisher.minted` audit event — another packet, not this one).

## The agent-first contract, rewritten

`caf publish` is OPS-D7's boundary — "the customer's own agent reads and
proposes freely" — realized at the level of one command, minus the approval
step the ruling removed: the tool proposes AND mints by default. An agent's
run reads the manifest, validates it, derives the tag, prints the proposal —
the registration, the version tag, the dependency edges — as data, and then
runs the three writes. `--dry-run` is the proposal mode: print the proposal,
write nothing, safe anywhere — still the way a human or an agent previews.
Reviewability is after the fact, in pantry's audit trail (the companion
packet). The boundary is the mint, not the reach: nothing about reading or
proposing is gated, and the mint's confirmation is the ruling, not a prompt.

## The new default path

```
caf publish <project>            # validates, prints the proposal, publishes
caf publish --dry-run <project>  # prints the proposal, writes nothing
caf publish --yes <project>      # identical to the default (accepted no-op)
```

- **Default = publish.** The confirmation prompt is REMOVED from the default
  path — not gated behind a TTY check, gone. No code path in the command can
  block on a TTY: the `confirm` seam is deleted from `publishDeps`, the
  `confirmPublish` function is deleted, and the refusal branch (no-terminal
  JSON refusal, declined-confirmation sentence) is deleted with it.
- **`--yes` is retained as an accepted no-op.** Parsed, ignored; its flag help
  reads "accepted for compatibility; publishing is non-interactive by default
  since 2026-10-11". No new flag replaces it.
- **Untouched, per the packet:** the write sequence (`internal/publish.Run`),
  the proposal JSON shape (`Proposal`, `Report`, `Step`), and pantry.

## The decision table, and what flipped

| Row | Before | After |
|---|---|---|
| default run, no flags | refuses without a TTY (exit 1) | **publishes the three writes in order** (exit 0) |
| `--yes` | the escape hatch that authorizes the writes | accepted no-op; publishes exactly what the default publishes |
| `--dry-run` / `--dry-run --json` | proposal, zero calls, exit 0 | stands, unchanged (stderr now says "Run it without --dry-run to publish") |
| `--json` report, 409 tolerance, 401, invalid manifest, `--tag`, `--kind` | as in REPORT-caf-publish-01.md | stand, unchanged |

The two old tests (`TestNoYesAndADeclinedConfirmationWritesNothing`,
`TestNoYesAndNoTerminalRefuses`) are deleted and replaced by
`TestDefaultPublishesWithoutPrompting` (no flags → register → version →
edges in order, report published) and `TestYesIsAnAcceptedNoOp` (`--yes` →
the same four calls, published). A refusal that no longer exists keeps no
test. There is no confirmation seam left to inject, so a future prompt would
hang the default test rather than ask it. Top-level test count is unchanged
(2 for 2); the gate's floors must not drop.

## What was deliberately NOT built

- **No TTY-gated prompt.** The brief forbids it: a prompt kept behind
  `isatty` is still a prompt in the default path.
- **No new flag.** `--yes` stays as the no-op; a replacement flag would be
  two spellings of one fact.
- **No changes to deploy/backup/secret confirmations.** The ruling says
  "everything", but this packet's scope is publish; the siblings keep their
  prompts and their tests.
- **No pantry changes.** The `publisher.minted` audit event is the companion
  packet.

## The gate

`go test ./internal/publish/... ./internal/cli/...` green; full `bin/prime`
once (see handoff/final report for exit codes and counts). `gate.yml`'s
floors stand — the packet adds and removes no top-level tests. `caf.lock`
re-run, because the tree's bytes moved.
