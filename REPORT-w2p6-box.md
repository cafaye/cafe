# REPORT — W2-P6: `cafe box enroll` and the box lifecycle commands

**Packet:** W2-P6, `cafe`. Branch `agents-first/w2p6`, worktree `cafe-w2p6`.
**State:** the packet is accomplished and the gate is green. 17 packages, 872
top-level passes, 1059 subtests, 0 failures, 8 skips (the 8 are the live tier
and the re-exec'd child, both named at `expectedSkips`).
**Commits:** two, both `w2p6:`-prefixed — the substrate in Q1/Q2, the CLI and
the gate floors in Q3/Q4.

---

## What landed

| Piece | Where |
|---|---|
| The keyring seam and its 0600 file ring | `internal/box/ring.go` |
| The `Identity` type (no private key field, by construction) | `internal/box/identity.go` |
| The `Conductor` seam and the HTTP client that redeems a capability | `internal/box/conductor.go` |
| The enrollment journal and the three-stage state machine | `internal/box/store.go` |
| The phase table and the five lifecycle verbs | `internal/box/lifecycle.go` |
| The `box` parent, its six verbs, and the human half of the split | `internal/cli/box.go`, `internal/cli/box_render.go` |
| The package's argument, in the repo's own words | `internal/box/doc.go`, AGENTS.md |
| 19 tests (17 + 2) and the raised floors | `internal/box/*_test.go`, `internal/cli/box_test.go`, `gate.yml`, `caf.lock` |

## The one design decision a successor should not re-litigate

**Idempotence is a COUNT, not an outcome.** Conductor's `Capability.redeem!`
enforces single-use in its own database — the UPDATE only matches a row whose
`redeemed_at` is still NULL. So a second redemption is *always* refused
server-side, and a test that asserted "the retry failed" would pass against a
client that redeemed twice. It would prove nothing.

The fixing is the enrollment journal, and it is `internal/ledger`'s ordering
rule applied to a capability instead of a container: **write the entry before
you consume the resource.**

```
1. keypair generated, filed under `box/enrollment-pending`
2. journal written: state=prepared, the public key
   ---- a crash here replays from scratch; nothing is spent
3. POST /v1/capabilities/redeem          <-- the irreversible step
4. journal written: state=redeemed, the installation facts
   ---- a crash here converges on retry, with NO second redemption
5. key moved to `box/<box_id>`, pending entry deleted
6. box.json written, journal deleted
```

Every stage is a row in a table, and each row asserts the number of redemptions
*performed* rather than the outcome. The `redeemed` row's conductor is a
**tripwire that panics if called**, which is the only way to prove the retry did
not go back to the network.

## The one window that cannot be closed, stated rather than hidden

A crash between step 3's response and step 4's write cannot be recovered from
this side: the retry presents the token again and conductor refuses it, and the
response carries no installation facts to write. `Enroll` answers an honest
refusal naming the window and the remedy (`cafe box enroll` with a freshly
minted token), and leaves the journal at `prepared` so the *next* capability
converges. `TestARefusedRedemptionLeavesARetryableState` proves that second
half.

## What I ruled out, and why

- **The OS keychain as the default ring.** `FileRing` is a 0600 directory store
  behind a two-call `Keyring` interface. It is not the macOS login keychain, and
  `ring.go` says so plainly. Shelling out to `security`/`secret-tool` would put
  a subprocess on the path that stores the one secret invariant 1 protects, and
  the repository's rule is that a subprocess is not free. An OS-keychain ring is
  a later packet that changes `withDefaults`'s constructor call and nothing
  else.
- **`apply` running the installer.** kit owns the substrate install (W2-P4) and
  `cafe boxd` drives it (W2-P7); neither exists. `apply` owns the RECORD — which
  phase was reached and what the handoff is — and says plainly that the
  installer is not here. A command that claimed a phase completed when nothing
  ran it would be a command nobody could trust.
- **A `notASourceTree`-style goldens directory.** None. The journal, the identity
  and the event log are written by the store and read back by tests through the
  store's own methods; there is no `testdata/` copy of anything.

## Two things I found while implementing, both now tested

1. **`status` on an unenrolled box is a REPORT, not a refusal.** My first
   version refused with `ErrNotEnrolled`, and the test went red. The report is
   right: "this box is not enrolled, and an enrollment is half-done in state
   `prepared`" is the question an agent asks a fresh box, and a refusal would
   throw away the half that is interesting. `plan` and `apply` still refuse.
2. **`apply --phase data` is ALLOWED from `substrate`.** My skip-refusal test
   assumed otherwise and went red twice. The refusal is for jumping *two*, and
   it now names the phase that IS next — which is the sentence that keeps a box
   honest about where it is.

## What the successor's first move is

**W2-P7, `cafe boxd`** — the box daemon, in the same binary, sharing this one.
Its seams are already here and already tested:

- `Store.BoxKey(id)` fetches the private key **for the duration of one call**.
  It has no caller yet, because nothing in cafe signs until boxd does. It exists
  so the day the signer arrives, the key is fetched at the point of use rather
  than threaded through three layers of arguments.
- The phase table is pure data, so a driver writes *into* it rather than
  inventing a second idea of what a phase is.
- `internal/box` has no dependencies and stays that way.

One thing to watch: `Phases` is a `var`, not a `const`, so a test that mutates it
would corrupt every later one in the same process. Nothing does today.

## Verification actually run

```
go vet ./...          clean
gofmt -l .            no output
./bin/prime           17 packages, 872 top-level passes, 1059 subtests,
                      0 failures, 8 skips
go run ./cmd/cafe lock .    12 files, re-pinned for the new gate.yml
```

The first gate run went **red on `TestTheGateFloorIsNotBelowTheSuiteCafClaims-
ToHave`**, which is the ratchet doing its job: it named 880 declared against a
853 floor and told me to raise it to 872. The floors were raised and the lock
re-generated in the same commit, which is the rule.
