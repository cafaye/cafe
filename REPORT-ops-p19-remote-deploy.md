# REPORT — OPS-P19: `caf deploy`'s operated remote

**Packet.** OPS-P19 (`CAFAYE-OPS.md`, Wave 3), repo `caf`, branch
`ops/w3-remote`, worktree `w3-remote-caf`.
**Goal.** the operated remote behind *build once, deploy once, use everywhere*:
the customer's app built once and deployed once, with every client they serve an
account on the shared platform rather than a cafaye deployment of its own.
**Status.** the mechanism is built, tested and documented; **it cannot be enabled
until TG-D1 records the tenancy decision**, and it does not deploy anything
today.
**Gate.** `bin/prime` — **exit 0**, 25 packages, 1135 top-level passes, 1204
subtest passes, 0 failures, 8 skips, `live tier: 0 of 7 executed`.
`gofmt -l .` printed nothing; `go vet ./...` printed nothing.
`go test ./internal/deploy ./internal/cli -count=1` — both `ok`.

---

## 1. The finding that shaped the packet

Reading the packet's row, the cross-reference and the barrier all say the same
thing in three places: *this is gated on TG-D1, so build the mechanism and
record plainly that it cannot be enabled until the decision is recorded.*
`CAFAYE-OPS.md:733` puts `TG-D1` in the depends column; `TENANCY-GATE.md:202-208`
says "until then no packet may assume either way"; the brief repeats it.

So the deliverable is **not** a working remote deploy. It is a real mechanism
that a one-line change turns on, plus a refusal that is honest about why it is
off. Everything below is answerable to that reading.

The second thing the reading settled: **where the gate lives.** caf has no
`tenancy.yml` and no access to `harness/tenancy_check.py` from a release
tarball, so caf cannot assert conductor's isolation for it — and pretending
otherwise would be inventing a check. What caf *can* assert is the decision's
state, because the decision is recorded in the moon repository and caf knows
whether it has been recorded. That is what the gate is.

## 2. What was built

### `internal/deploy/remote.go` — the third engine

| Piece | What it is |
|---|---|
| `EngineRemote = "remote"` | the third engine, in `RunnerFor` alongside kamal and argo |
| `RemoteRunner` | `Plan` + `Run`, the same `Runner` seam both other engines satisfy |
| `Remote` (interface) | two questions: `ReadAccount`, `Deploy` |
| `StubRemote` | the default seam, which refuses — a nil `Remote` is a caf built without the wiring |
| `Boundary`, `RequestStep` | the two step names, constants like the other engines' |
| `planForRemote` | the plan function the gate calls once the decision is recorded |
| `remoteEnabled` | **the gate**, one constant, `false` |
| `ErrTenancyUndecided` | the sentinel, wrapping `ErrRefused` so the CLI exits as a refusal |

The plan is two steps: ask the platform which deployment serves the named
account, then hand it one deploy request — service, version, account. The
request is read back out of the plan's own argv, which is the same argv the dry
run prints, so a request bound to account A can never be an answer about
account B and a dry run shows exactly what a real deploy asks.

`Deployment.Account` is new, declared beside the Argo engine's facts for the
reason those are: the plan is the engine's and both engines are asked with one
signature. `Request.Account` is new for the same reason.

### `internal/cli/deploy.go` — the flag and the help

`--account <uuid>` names the account `--engine remote` serves. The summary, the
`LongHelp` and the flag's own help text all name the remote engine **and say it
is pinned off**, because a help text that listed an engine without that sentence
would advertise a deploy nobody can perform. The prompt names the account too:
the account is the destination, so a confirmation that named only the service
would be approving a deploy to no stated place.

### `internal/deploy/remote_test.go` — 12 tests in two halves

Half one asserts the **gate**: the refusal is `ErrRefused` (exit 1, the CLI's
`errReported` mapping) *and* `ErrTenancyUndecided`; it names TG-D1,
TENANCY-GATE.md, and the two engines that do work; the refused deploy runs zero
steps and prints the whole answer on stdout with stderr empty; a dry run is
refused before the plan.

Half two drives the **mechanism** through `planForRemote` — the function the
gate calls once the decision lands. It asserts the two steps in order; the
account, version and service carried in the argv; the boundary question asked
exactly once about the right account; the request handed to the platform with
the right four fields; the missing-account refusal that survives the
enablement; the report that states what the deploy did *not* do; the seam's
default refusal; and an unknown `--engine` naming all three engines.

**Every test in half two opens by asserting `remoteEnabled` is false.** A test
written against an enabled engine would fail rather than pass by accident, and
that ordering is the reason the mechanism could be proved without touching the
switch.

## 3. The gate, and why it is the shape it is

`remoteEnabled` is a constant in the package that returns the refusal, and not
a flag, an environment variable or a config key:

- a **flag** makes a tenancy decision a command-line argument;
- an **environment variable** makes it something a CI job can set with nobody
  deciding it;
- a **config key** makes it a second place the platform's shape is recorded, and
  the moon repository owns that decision.

The refusal names the decision it waits on, the evidence that the mechanism is
intact, and the two engines that work today. Enabling it is `remoteEnabled =
true` in one line, and the test that asserts the refusal becomes the test that
asserts the plan — which is the evidence that this was a mechanism rather than a
placeholder.

**No tenancy check was weakened, and none was removed.** The refusal runs
*before* any step is built, so a dry run is refused too: the first thing a
remote deploy says is why it cannot deploy. A dry run that listed the steps and
left the refusal out would read as a deploy that works.

## 4. What I deliberately did not do

1. **No conductor-side route.** The `Remote` seam is deliberately an interface
   with a refusing stub. Conductor has no operated-platform deploy route today
   (its 16 routes are in `refs/fact-conductor-caf.md §1`, and none is one), and
   inventing one cab-side while the tenancy decision is unrecorded would be
   designing a contract for a platform whose shape is undecided. The seam is
   exactly where that client goes; the stub is what answers without it.
2. **No account lookup, no `caf agent` token.** That is conductor's job to
   define before it is worth a client.
3. **No live test.** Nothing here establishes a connection, so nothing belongs
   in the live tier.
4. **No new dependency.** `internal/deploy` still imports nothing outside the
   standard library plus `internal/contract`'s `go-yaml`, which was already
   there.

## 5. Two pre-existing reds, found by running the gate

Neither is from this packet, and both were red at `4f4d953` before it touched
anything:

- `internal/cli/command_test.go`'s want-list and `stub_test.go`'s
  `workingCommands` were both missing the two W2 commands. `caf cve` and
  `caf secret` are in the registry and both work; neither test knew about them,
  so `TestRegistryContainsEveryCommand` and
  `TestStubTableMatchesTheRegistry` were red. That is the W2 merge's debt. It is
  fixed by **naming them** — not by loosening either assertion, and there was no
  version of this that would have been easier to fix by weakening a check.
- `gate.yml`'s `suite` floor was 8 low for the same reason: the tree declares
  1143 top-level tests and the floor said 1079. It moves to 1135 with the
  measurement in the comment, and `caf.lock` follows because `gate.yml` is a
  pinned rule-bundle.

## 6. Commits

```
c1da37c  OPS-P19: the two gates this packet crossed — the W2 registry gap, and
         the floor
b7d8818  the operated remote: the mechanism, gated on TG-D1
```

The first commit of the packet carries a `recover(...)` message because the
fleet's recovery harness swept the tree four times while the endpoint dropped
the session — see §7. The second one is the packet's own text; the first is the
gate-floor increment. `git log --oneline` is the order of events, not the order
of authorship.

## 7. What the manager needs to know

**This packet cannot be enabled here.** The one thing it cannot do is enable
itself, and it does not: the refusal is the shipped behaviour, the constant is
`false`, and the tests assert it. To enable it, TG-D1 must be recorded in
`CAFAYE-OPS.md` with Waves A–C closed behind it, and then `remoteEnabled` flips
in the same commit that records the reference.

The other half is conductor's. The seam is declared, the stub refuses, and the
engine is ready to be pointed at a real platform the moment one exists to point
at. Building the client before the platform's tenancy decision is recorded would
be the exact failure mode `TENANCY-GATE.md` exists to prevent, and this packet
is deliberately on the near side of it.
