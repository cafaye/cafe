# REPORT — w2p7-boxd

## What this is

`cafe boxd` is the root daemon that runs on the customer's box, in the same
binary as every other cafe subcommand. It dials **out** to conductor over mTLS,
waits for capabilities, and redeems them against a whitelisted route table
compiled into the binary. It opens no listener.

This packet is AGENTS-FIRST **invariant 4** as engineering rather than prose:
*the enforcement lives on the box, not in the agent's instructions.* The route
table is the security boundary, and a test renders every route in it and fails
if any route can return a Secret-shaped value. That test is the packet.

**`go test ./...` green: 898 top-level passes, 1060 subtest passes, 0 failures,
8 skips** — the same eight the tree had before this packet. `expectedSkips` did
not move, because nothing here is env-gated: the mTLS proof binds a loopback TLS
listener *inside the test process*, which is a socket and not a container, so
`bin/prime` on a bare CI runner runs it.

---

## 1. What landed

| File | What it is |
|---|---|
| `internal/boxd/doc.go` | the three rules: a capability must be in the table, alive and bound; no route may answer with a Secret-shaped value; the box key's lifetime is a connection's |
| `internal/boxd/secret.go` | the `Secret` shape, and the type-graph walk that hunts for it (marker OR name, on types and on field names) |
| `internal/boxd/route.go` | the `Route` type, the `World` interface a handler is handed, and `Redeem` — the one entry point a capability has into the box |
| `internal/boxd/identity.go` | the enrolled identity, the capability, the outcome, the ack, and the `Conductor` channel's two outbound verbs |
| `internal/boxd/daemon.go` | the round: load, dial, redeem, ack, reconnect with a bounded backoff |
| `internal/boxd/client.go` | the mTLS dial and the one-JSON-object-per-line protocol |
| `internal/boxd/secret_test.go` | **the load-bearing test** |
| `internal/boxd/daemon_test.go` | the loop's seven cases against a fake conductor |
| `internal/boxd/tls_test.go` | the real mTLS proof, over a loopback listener |
| `internal/cli/boxd.go` | `cafe boxd`: the identity store, the file contract, the v0 world |
| `internal/cli/boxd_test.go` | the command layer: wiring, refusals, the seam |

### The four routes

| Route | What it does | Mutates |
|---|---|---|
| `box.status` | the box's enrolled identity, daemon version, conductor address | no |
| `stack.status` | the substrate's phase, and the phase the install plan reached | no |
| `stack.apply` | apply one phase of the recorded plan, idempotently | yes |
| `provider.connect` | install a provider credential sealed to this box, as a Kubernetes Secret | yes |

---

## 2. Invariant 4, as four layers rather than one

The whitelist is not one check, because one check is one edit away from gone.

**Layer one — the table is a Go value.** A capability names a route and
`Redeem` looks it up in the table compiled into the binary, or refuses by name.
No prefix match, no fallback, no wildcard, and nothing arriving over the wire
can add an entry. `TestRedeemRefusesWhatIsNotInTheTable` drives `shell.exec`
and `secret.read` into `RouteRefused` and asserts the message names the route.

**Layer two — the interface is the rule.** A handler's second parameter is a
`World` interface with one method per capability the box has: `Identity`,
`Stack`, `Apply`, `Envelope`. There is no method that runs a process and no
method that returns a `Secret`, so a handler that needs either cannot be
written. This is what makes "no arbitrary exec" structural rather than
aspirational — `exec.Command` does not exist on `World`.

**Layer three — the rendered-table test.** `TestEveryRouteRefusesToAnswerWithASecret`
drives every route in the table through a recording world with representative
parameters and walks each answer's full type graph — pointers, maps, slices,
interfaces, named types. A Secret-shaped value reachable from any route's
answer is a red suite naming the route and the path. The walk fires on three
signals: the type implements the `secretShaped` marker, its name normalizes to
one of the secret names, or its *field* name does. The name walk catches
`type Envelope Secret`-style disguises; the field-name walk catches a field
called `Password` whatever type it was declared with.

**Layer four — the negative control.** `TestTheGuardRefusesARouteThatWouldReturnASecret`
declares a route that answer with a `Credential` (marker), a `Secret` behind a
pointer, a `map[string]PrivateKey`, and a `BearerToken`, and asserts the SAME
walk catches it and names `leaky.status.Credential`. This is the house rule
from the doctor severity table and `TestEveryProblemIsDrivableAndNamed`: a
check nobody has been watched seeing fail is a check that might not work. The
control also asserts a clean struct passes, so a walk that called everything a
secret would fail here rather than shipping.

The complement is `TestTheDaemonCannotExec`, which parses the package's own
imports and fails if `internal/boxd` imports `os/exec` or `syscall`. A future
edit that adds a process to run has to delete that check to do it, which is a
deletion a reviewer sees rather than a route nobody notices.

---

## 3. What W2-P6 and W1-P3 left for me to decide, and what I decided

**W2-P6 (`cafe box enroll`) has not landed in master.** I read `master` for it
before starting: there is no `box` subcommand, no keyring, no box identity. So
this packet could not authenticate with machinery that does not exist, and it
does not build that machinery — the brief says so too.

The decision this forced is the **identity document**: a JSON file beside a
certificate and key, with `box_id`, `conductor`, `version`, and relative paths
to the pair. It is written down in `internal/cli/boxd.go` as the seam between
this packet and W2-P6, and `TestTheIdentityFileIsTheSeam` pins it by generating
a real keypair, writing the pair, loading it through the store, and asserting
the certificate is for `box-seam`. When enroll lands and writes a different
shape, that test fails and names the field — which is what a file format with a
test is for.

**Conductor's capability machinery (W1-P3) is Rails, in another repo.** The
wire protocol this daemon speaks is therefore cafe's side of a contract with no
counterpart yet. It is written down in `client.go` as one JSON object per line
over TLS — `{"op":"next"}` / `{"op":"ack","ack":…}` — rather than discovered
from a server that does not exist in this tree. Both sides now have something
to be checked against; when conductor's server lands, the first disagreement is
a failing test on one side or the other rather than a protocol invented twice.

### What the v0 world refuses, and why

`Envelope` — the route that installs a sealed provider credential — is refused
by name in the CLI's world: opening an envelope needs the substrate on the box,
which is a later packet. The alternative was to accept the capability and do
nothing, which would be the box telling conductor a credential was installed
when it was not. `TestTheBoxWorldRefusesTheEnvelopeHonestly` asserts the
refusal names the substrate. A capability that comes back `failed` with a real
reason is a capability an operator can act on; a silent success is a lie.

---

## 4. The two things I would tell the successor to look at first

**The protocol is one-sided until conductor's half exists.** `tls_test.go`
proves cafe's client against a listener that follows cafe's own spec. That is
real crypto over a real socket, and it is not a conversation with conductor.
The first integration task of the packet after this one is to make the two
protocols meet; the shape to preserve is that the failure when they disagree
is a named test, not a silently ignored field.

**The phase model is a file, deliberately.** `stack.apply` records an idempotent
phase in `boxd-state.json` beside the identity. W2-P6 owns the install plan and
W5-P4 owns the resumable-install model (run/steps/events/lease). If those land
with a different state shape, this file is the thing to reconcile — and the
idempotence contract (a replayed capability for an applied phase applies zero
and says so) is the property to keep, because agents retry.

**One thing I did not do, named rather than skipped silently:** the key is
loaded from disk per connection and dropped with the connection, but Go cannot
force-zero the memory the TLS stack holds it in. The rule in `doc.go` says the
key's *lifetime* is the connection's, and `TestTheKeyIsLoadedPerDial` asserts
the key source is called per dial. A stronger guarantee — keyring residency,
HSM, never materialised outside the handshake — belongs with W2-P6's enrollment
flow, which owns the keyring this package reads through a seam.

---

## 5. Gate

`./bin/prime` on this tree: **18 packages, 898 top-level passes, 1060 subtest
passes, 0 failures, 8 skips.** The floors were measured here and set one lower
per the file's own rule: packages 16 → 17 (the new package), suite 873 → 898,
subtests 1006 → 1060. `expectedSkips` unmoved at 8; the seven live tests and
the re-exec'd child are still the whole tier. `caf.lock` re-run in the same
commit, because `gate.yml` is the tree's one `rule-bundle` pin and the floor's
hash moved.

`go vet ./...` clean. `gofmt -l .` prints nothing.
