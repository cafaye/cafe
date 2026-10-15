# REPORT — fix/boxd-idle: the idle case is a quiet channel, not a violation

Branch `fix/boxd-idle`, worktree `/Users/kaka/Code/any/moon/wt/fix-boxd-idle`
(the only worktree used; no second repository was touched). Three commits on
top of `e340730`.

## The defect, as measured

Live on Contabo 2026-10-15 against the deployed channel server (box master
`527532d`), boxd repeated this pair every ~20 seconds:

```
boxd: enrolled as ca309b8b-…, outbound to https://box.svc.cafaye.com:8443
boxd: round ended (boxd: wait for a capability: box sent a capability with no id; a capability nobody can name cannot be acked); reconnecting in 1s
```

Box's `serve_next` holds a `next` up to its `next_wait` and, with nothing
queued, replies `{}` — its own words, "nothing for you". The client decoded
that as a capability with no id, ended the round as an error, and reconnected:
a protocol-violation log line and a fresh TLS connection (with a fresh
client-cert load) every poll interval, forever, on a healthy channel. The
idle case was never written into the contract — `client.go` said the protocol
was "written down here rather than discovered from a server", and idle was the
case nobody wrote.

## The fix

**The discriminator is the LINE, not the decoded value.** `Next` reads the
answer as `map[string]json.RawMessage` first:

- a line naming **no field at all** is box saying "nothing now" →
  `ErrIdle`, and the daemon re-polls on the same connection;
- a line naming fields but no id is **still the anomaly it always was** —
  `box sent a capability with no id; a capability nobody can name cannot be
  acked` — and it still ends the round.

The map decode is what makes the two distinguishable: `{}` and a capability
with its fields missing decode to the identical zero `Capability`, which is
exactly the confusion that produced the live symptom. After the idle check,
the same bytes are decoded into `Capability` exactly as before, so a real
capability's decode is unchanged. A malformed line is still an error (the
map decode fails), and it is *not* idle — the client does not quietly re-poll
garbage.

**Idle is handled in the daemon's round, not hidden inside `Next`.** `Next`
returns the sentinel `ErrIdle`; `round` re-polls on the same connection — no
round-end line, no reconnect, no new key load. The alternative (have `Next`
re-send internally) was considered and rejected: it would have kept the loop
trivial but moved the decision the daemon makes into the client, and the
fake-seam tests could then only observe responses, not the daemon's behaviour
— the seam test counts connections precisely because a daemon that
reconnected between idle answers answers every response correctly and is
still the defect.

Cancellation is checked at the idle boundary in `round`, because the real
CLI's context is `signal.NotifyContext`'s and carries no deadline: without
that check, a SIGTERM to an idle daemon would re-poll past forever. This is
the one place the new branch needed a decision the brief did not spell out.

**The contract now says it.** The `client.go` block comment ("written down
here rather than discovered from a server") names the idle case, and the
`ControlPlane.Next` interface comment does the same, so both sides have the
case in writing.

## Tests

The fake control plane now models what was measured: an empty queue is
answered `{}` after next_wait, not blocked on forever. The fake server gained
scripted answer lines and a `nexts`/`handshakes` counter.

- `TestAnIdleAnswerKeepsTheDaemonOnOneConnection` — three idle answers, then
  the queued capability; asserts **one dial**, four polls, the capability
  still acked accepted, and no "round ended" line.
- `TestAnIdleChannelHoldsTheConnectionForever` — nothing is ever queued; the
  daemon keeps polling on **one dial** and logs nothing but the enrollment
  line; cancellation still returns `context.Canceled`.
- `TestTheIdleAnswerIsDistinguishedFromTheNoIdAnomaly` — the discriminator as
  a five-row table over the **real client over a real loopback TLS
  connection**: `{}` is `ErrIdle`; a capability-shaped line with no id, and a
  line naming the id field empty, are both refusals and neither is absorbed
  into idle; a real capability still arrives; a malformed line is an error,
  not a re-poll.
- `TestTheDaemonIdlesOverTheRealChannelOnOneConnection` — end to end: real
  dialer, real client, real TLS listener, `{}` twice then the capability. One
  **handshake**, three polls, one key load, ack accepted.
- `TestTheProtocolRefusesAnUnnamedCapability` (pre-existing) still red-able
  and still green.

## Red proofs (Q3)

1. Client treats `{}` as an error again (idle branch disabled) →
   `TestTheIdleAnswerIsDistinguishedFromTheNoIdAnomaly` and
   `TestTheDaemonIdlesOverTheRealChannelOnOneConnection` FAIL.
2. Daemon treats `ErrIdle` as a round end (branch disabled) → all three
   daemon/e2e idle tests FAIL, with the live sentence verbatim:
   `boxd: wait for a capability: box has nothing queued; the channel re-polls`.
3. Discriminator absorbs the anomaly (any no-id answer becomes `ErrIdle`) →
   both anomaly tests FAIL, so the no-id refusal is watched from both sides.

Both broken states were reverted and re-verified green.

## Gate

- `bin/prime`: FAIL on its first run with exactly the expected ratchet
  (`TestTheGateFloorIsNotBelowTheSuiteCafClaimsToHave`,
  `TestTheWriterRoundTripsAndAHandLoweredFloorIsRed`): the tree declared 1475
  top-level tests against a 1463 floor.
- Floors raised via the repo's own writer: `cafe gate floors --write .` →
  "floors moved 1463→1467, subtests 1282→1287" and re-pinned `caf.lock`
  (committed as `b016ae7`).
- Second `bin/prime`: **33 packages, 1467 top-level passes, 1287 subtest
  passes, 0 failures, 8 skips, exit 0**. `gofmt -l .` silent, `go vet ./...`
  clean. `go test -race ./internal/boxd/` clean.

One caveat, stated because it is a gate-adjacent fact and not a pass:
`tests/gate-declaration-self-test.sh` **could not run** here — it needs
core's `harness/gate_check.py` beside the repository, and this worktree has no
`cafaye/core` checkout. It exits 2 ("the check could not happen, which is not
the same as having passed"). It should be run from the main checkout before
the floors commit is pushed far.

## Commits

```
8085247 boxd: the idle answer is a quiet channel, not a protocol violation
740b706 boxd: pin the idle case and the no-id anomaly with tests that count connections
b016ae7 gate floors: 1463->1467 suite, 1282->1287 subtests, via the repo's own writer
```

## Done means, and the live check that remains

Done here: a live boxd against the deployed channel holds ONE connection and
logs no error while idle; the anomaly still refuses; `bin/prime` green. The
live half of that sentence needs the manager: merge, push, rebuild the box
binary, and watch one idle daemon against `box master 527532d` — the expected
log is a single "enrolled as … outbound to …" line and then silence, with no
"round ended" line and no reconnect in `ss`/the server's connection count.
