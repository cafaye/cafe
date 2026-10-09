# W5-P3 — remote `caf mcp` tools, read-only first, transport auth

**Status: done, gated green.** Two commits: the auth substrate in Q1, the
remote tools and their wiring in Q3/Q4. `./bin/prime` green (999 top-level
passes, 8 skips), `go vet ./...` clean, `gofmt -l .` empty.

**Two things I got wrong on the way there, both worth knowing:** the first run
of the full gate found `identityAuthority` written but never wired, so the HTTP
transport would have served `caf_dev_up` to any caller that could reach the
port. `attachAuth` closes that, and the gate's whole body — the loopback refusal
without an authority, `TestAnUnauthenticatedRemoteCallerIsRefusedWithAUsableAnswer` —
is the control on it. And the wire test's tool list is **alphabetical**, not
registration order, which the SDK decides; the test now sorts before comparing
so the literal still reads as two halves.

---

## What this packet was for

`internal/mcp` served six tools over stdio, and its HTTP transport was
**loopback-only and unauthenticated**. `doc.go` said so in as many words:
*"Authentication is a later packet, and until there is one the flag is
documented as loopback-only rather than dressed up as production HTTP."*
That was defensible while everything the server served ran containers on the
machine it ran on — the listener being reachable was the whole attack.

The remote tools break that reasoning. A caller on another continent reading a
box's health is the point, and **an unauthenticated read is not a lesser thing
than an unauthenticated command**: the health read names which services are
running, and that is the thing an attacker probes before doing anything else.
So this packet lands the transport auth, and lands the tools on top of it.

## The two mechanisms, and why they are not one

They are deliberately separate, and conflating them is the mistake to avoid:

1. **The transport authenticates.** Over HTTP every request must present a
   bearer. An authenticated caller is who the transport says and nobody else —
   `principalFor` prefers the request's own token info over the standing
   principal, always, so a misconfigured server cannot let a remote caller
   inherit the box agent's scopes.
2. **The tools are gated on scopes, per call, before the handler runs.** A
   refused tool is one whose side effect did not happen. `TestARefusedToolNeverRunsItsHandler`
   and `TestAReadScopedAgentReadsAndCannotMutate` are the controls.

The gate is a **tool error**, never a JSON-RPC error — the first rule in the
package's doc. An agent reads the refusal, learns the scope and the command
that grants it, and carries on.

## The split across four files

| File | What it owns |
|---|---|
| `internal/mcp/auth.go` | `Principal`, `Authority`, `RequiredScope`, the transport middleware, the gate, `redactToken` |
| `internal/mcp/remote.go` | `World` seam, the six remote tools, their outputs |
| `internal/cli/mcp_remote.go` | `identityAuthority` (forward the bearer to identity) and `mcpBoxWorld` (read the box's own state) |
| `internal/cli/mcp.go` | the served table: local six unchanged, remote six added; `attachAuth` wires the auth per transport |

`mcp` still knows nothing about the CLI and the CLI still knows nothing about
MCP's wire format. `internal/mcp` gained `github.com/modelcontextprotocol/go-sdk/auth`
as an import of an existing module — **no new dependency**.

## Three decisions worth reading before changing anything

**1. The scope table is keyed by tool name, in one place.** `RequiredScope`
maps `caf_health` → `read`, `caf_apply` → `deploy.apply`, and so on. The gate
reads it; `TestTheRemoteTableIsExactlyWhatAddRemoteRegisters` walks the served
table and asserts nothing outside it is in the map. A tool absent from the map
needs no scope — which is what the six local tools do, so the local surface is
untouched. A field on `Tool` would be a second copy of the policy, and
`Tool[In, Out]` is generic so a map keyed on the tools is the only shape that
stays one place.

**2. `redactToken` is invariant 1 applied to an error path.** If an authority
echoed the token it was handed, the SDK's middleware writes the verifier's error
verbatim into the 401's body — and a 401 body is a thing a log collects and an
agent reads. The test drives an authority that *deliberately* does it, because
a guard that only works against sane input is not a guard.

**3. The writes are refused, and that is the honest v1.** `caf_propose` and
`caf_apply` are scope-gated and their handlers run, but `mcpBoxWorld` refuses
them with `ErrNotWired`: conductor's releases and approvals are W5-P1, and this
packet consumes them. A tool that pretended to apply a release here would be a
tool that changed a box without a human's approval — exactly the convenience
W5-P1's own rule calls a defect. The **reads are real and file-backed** over
`internal/box`'s store: health, logs, rollout state and status all answer from
the box's own enrollment journal, phase table and event log.

## The 401's two layers

The SDK's `RequireBearerToken` answers *"no bearer token"* without ever calling
the verifier — so that message cannot be made actionable from the verifier.
The answer is two layers: caf's own outer middleware owns "you presented
nothing" and writes a body naming `caf agent token create`; the SDK's layer owns
"you presented something unverifiable" and its body is the verifier's error,
which `bearerVerifier` wraps with the remedy for every authority. Both are 401
and both name the command.

## Invariant 2, enforced rather than trusted

`Principal.Expires` has no zero case: a principal with no expiry is refused
**twice**, once in `bearerVerifier` and once by the SDK's own check, to which
`AllowMissingExpiration` is deliberately not passed. A credential handed to an
agent that never lapses is the one thing invariant 2 forbids, and a verifier
that returned one would be minting it.

## The tests

- `auth_test.go` — 14 tests, including the two controls that matter most:
  `TestAnIncomingPrincipalNeverInheritsTheStandingPrincipalsScopes` (a
  read-scoped bearer cannot inherit a standing apply-scoped agent's scopes) and
  `TestARefusalNeverCarriesTheTokenItWasHanded`.
- `remote_test.go` — 15 tests. The load-bearing ones:
  `TestAReadScopedAgentReadsAndCannotMutate` (the world was never asked to
  mutate), `TestEachWriteScopeIsCheckedOnItsOwn` (three scopes, three rows —
  propose-only can propose and cannot apply), and
  `TestNoRemoteAnswerCarriesASecretShapedField` (a walk over every output type's
  JSON tags, the same control `internal/boxd/secret_test.go` applies to a route
  table).

**Two bugs the new tests caught in the new code**, both worth naming because
they are the kind that survive a green suite: the `phase` filter on `caf_logs`
was declared in the schema and **never applied** in `runLogs`, and the health
answer reached a caller without the world being asked at all (a tool that
answers from caf's opinion of a box rather than the box).

## Gate impact

`gate.yml`'s `suite` floor raised 968 → 999, and `caf.lock` regenerated by
`go run ./cmd/caf lock .` in the same commit, per the rule in AGENTS.md: the
floor's hash moved because `gate.yml` changed. The number was **measured**, not
guessed — the first attempt set 1001 and `internal/ci` refused it, naming the
measured 999 itself (`the tree declares 1007 top-level test(s)`).
`bin/prime` reports 999 top-level passes and 8 skips on the merged tree.

## The successor's first move

**W5-P1's approvals are the unblock.** Once conductor's releases and approvals
exist, `mcpBoxWorld.Propose` and `.Apply` stop returning `ErrNotWired` and start
recording proposals and applying approved releases — the tools, the scopes, the
gate and the `World` seam are all in place and that is the whole change. The
`mcp.Applied{Next}` field already carries what an apply produced so a caller
does not have to ask twice.

Second, if identity surfaces a principal's ref from `GET /v1/agents/key`,
`identityAuthority` should set `Subject` from it rather than from the saved
session's username. The gap is named in the file.

Third: `principalTTL` is 5 minutes, which is a number nobody has measured. A
revoked token is refused within it. Tighten it if the round trip is cheap.

## What this packet deliberately did not do

- Did not build conductor's approvals (W5-P1) — it consumes them.
- Did not weaken the local stdio/loopback surface: the six local tools are
  registered unchanged, and `TestTheRemoteHalfIsGatedAndTheLocalHalfIsNot`
  asserts the table still splits six and six.
- Did not revise the landing page's read-only promise (D6) — that is a decision
  in the same commit that ships the first mutating tool. **`caf_propose` and
  `caf_apply` are not yet live** (they are refused with `ErrNotWired`), so D6 is
  not yet triggered. A successor who wires W5-P1 in must read D6 before landing.

---

## What the local surface looks like now, exactly

The `caf mcp` table is twelve tools. The first six are unchanged and unscoped:
`caf_doctor`, `caf_manifest`, `caf_registry`, `caf_dev_plan`, `caf_dev_up`,
`caf_dev_down`. `TestTheRemoteHalfIsGatedAndTheLocalHalfIsNot` asserts the split
over the real served table — six gated, six not — and
`TestEveryToolNameIsOneCafWouldShip` names all twelve.

`caf_health`, `caf_logs`, `caf_rollout` and `caf_status` need `read`.
`caf_propose` needs `deploy.propose`. `caf_apply` needs `deploy.apply`.

**On stdio the process speaks as the machine's own agent session**, verified once
at startup against identity; on HTTP every request carries its own bearer. A
read-only agent's `caf mcp` therefore refuses `caf_propose` and `caf_apply` on
both transports, which is invariant 4 applied to the child process rather than
only to the request.
