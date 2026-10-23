# AGENTS.md

Conventions for `cafe`, the anywaye platform CLI. Read this before changing
anything; the house rules apply on top of it — SSH remotes for the repositories anywaye owns, and tests first: write the test, watch it fail, then implement.

## What this repository is

`module github.com/anywaye/cafe`, Go >= 1.25, one binary. It is the client side of
the platform: the manifest, the local stack, the generator, the deploy path and
the MCP server. The contracts themselves are owned by `anywaye/core`, and the
shared CI and lint configuration comes from `anywaye/kit` — neither lives here.

## Layout

```
cmd/cafe/main.go    thin entrypoint: build a cli.Options, exit with its code
internal/cli/       router, registry, one file per subcommand
internal/box/       cafe box: the keypair, the capability redemption, the phases
internal/run/       the resumable install model: run, steps, events, lease
internal/contract/  the core contract: manifests, the vendored schemas, versions
internal/cve/       cafe cve: the box's component inventory, the advisory match, what a fix touches
internal/backup/    cafe backup: the cross-file contract, the plan, the cycle
internal/dev/       the local stack: the planner, the renderer, two seams
internal/failure/   cafe failure: the failure-mode library, and the checker over it
internal/gate/      cafe gate: the floors gate.yml derives from the tree, and the writer that sets them
internal/gen/       cafe gen: the pure generator, and the drift gate that keeps it honest
internal/scaffold/  cafe new and cafe init: the template catalog, the render, the write
internal/lock/      cafe lock: the pin over specs, vendored schemas and generated output, and its verifier
internal/telemetry/ GENERATED, and compiled: cafe's own copy of `cafe gen telemetry`'s output
internal/mcp/       the MCP server: transports, the served table, the SDK seam
internal/boxd/      the box daemon: the whitelisted route table, the Seam-ed world,
                     and the outbound-only mTLS channel to box
internal/setup/     cafe box setup and its inverse, cafe box wipe: the host bootstrap
                     plan, its walk, and the SSH transport behind the Host seam
internal/edge/      boxd's embedded edge: host routing, the three TLS modes, the
                     release directories and the atomic `current` switch — the one
                     piece of state the edge owns so nothing else keeps a copy
internal/emitter/   the one span a cafe invocation earns: the OTel SDK wired to
                     core's closed contract, over the OTLP/gRPC exporter
internal/ledger/    the reclamation ledger: what cafe created, so cleanup is not memory
internal/ports/     the port block, the reservation that holds a port, the prober
internal/provider/  cafe provider: the client half of boundary 6 — the envelope sealed to the box's public key, the control-plane seam
internal/reclaim/   the sweep's container-runtime seam, scoped to one project
internal/resolve/   cafe resolve: the tiered catalog's consumer — a manifest's dependencies through tiers 1/2/3, and the named failures
internal/publish/   cafe publish: the write half of the same pantry client — the proposal, the three writes, the address a project registered under
internal/secret/    cafe secret: the box's secret inventory, its three classes, and the rotation path for the rotatable one
internal/docs/      cafe docs publish: press's ingest client — the kind inferred from the repository, the pages packed, and the session-to-press-token exchange before the ingest
internal/stack/    the boxed stack: releases on disk, the current pointer, the history, and the flip-back
internal/ryuk/      the reaper lease client, for testcontainers' Ryuk
internal/ci/        no code; the tests that keep .github/workflows/ci.yml and bin/prime honest
bin/prime           the gate: go mod download && go build ./... && go test -v ./... , then say what ran
gate.yml            what the gate is, what it needs, and what its own output must contain
tests/              the self-test that proves gate.yml can go red; not part of bin/prime
.github/workflows/  ci.yml calls kit's shared workflow; two jobs carry what kit cannot know
```

The dependency rule is the prose at the foot of this section, keyed
**Packages with dependencies** — a library is the default there, and every
package that carries none says why in its own words.

`internal/cli` is split by pipeline stage, the way `refs/goreleaser` splits
`cmd/` and `internal/`:

- `cli.go` — root routing, help rendering, exit codes. Knows nothing about any
  subcommand.
- `command.go` — the `Command` type, the flagset, the registry, the
  `not implemented in v0` sentinel.
- `<name>.go` — one subcommand: its flags, its help, its run function.

A subcommand never reaches into another subcommand, and nothing outside
`internal/cli` is imported by `internal/cli`.

`internal/dev` is split by what it is for, and exists so that `dev` and `deploy`
do not each grow their own idea of what a stack is:

- `ports.go` — the third seam, `PortAllocator`, and why the planner has it.
- `doc.go` — what the package is, and the JSON document it expects from a
  registry. The shape is written out there because it is the contract between
  cafe and pantry, and a contract that lives only in one function signature is a
  contract nobody outside this repository can implement.
- `plan.go` — `Plan`, the whole decision, and the three refusals: a dependency
  cycle, a port conflict, an unresolvable dependency.
- `render.go` — the compose document, written by hand.
- `registry.go` — the `Registry` seam, the catalog, and the language and
  infrastructure tables.
- `project.go` — reading a project directory: the manifest, the Dockerfile.
- `runtime.go` — the `Runtime` seam, the wait loop, and the state mapping.

`internal/contract` is split by what it is for, and owns everything the
contract needs so that `init`, `gen` and `deploy` do not each grow their own:

- `doc.go` — what the package is, and why those two libraries.
- `manifest.go` — a manifest: the document, and the few facts read off it.
- `schema.go` — the vendored schema, and the order violations are reported in.
- `violation.go` — one violation, and how each schema keyword becomes a
  sentence.
- `conventions.go` — the rules the schema cannot state.
- `lint.go` — the walk, one finding per manifest.
- `version.go` — the constraint grammar and the resolver.
- `telemetry.go` — the six vendored telemetry schemas, and the facts `cafe gen`
  reads out of them. This is the package's second job and it is the mirror of the
  first: `schema.go` validates a document against core's schema, and
  `telemetry.go` READS core's schema so a generator can emit what core wrote
  rather than what somebody transcribed. Both are the same argument — a pinned
  copy beats a fetch — applied in opposite directions.

`internal/scaffold` is `cafe new` and `cafe init`, split the same way, and it is
the packet that made both commands real:

- `doc.go` — what the package is, and the three rules the catalog holds:
  the templates are embedded, a scaffold is rendered into memory and refused
  before anything is written, and the manifest is validated by the same code
  `cafe contract lint` runs.
- `catalog.go` — the index and the walk over a template's directory. The tree
  is the single source of truth for what a template contains; an index that
  listed the files would be a second copy that could disagree with it.
- `render.go` — the name rules (a mirror of core's serviceName `$defs`, kept
  honest by a test that renders a table of names through both), the template
  execution, and the validation gate.
- `write.go` — the one write, and the refusal that names what is in the way.
- `scaffold.go` — `New` and `Init`, the two entry points.

The templates scaffold a customer's app: a real service, the image `cafe dev`
builds, and a manifest with no `exposes` — a scaffolded app publishes nothing
yet. What they do not scaffold is a patch to a platform service's source; the
agent's editable surface is the customer's repository.

`internal/gen` is `cafe gen`, split by what it is for, and it holds one rule that
the rest of this file leans on:

- `doc.go` — what the package is, the one target, and the three things it
  deliberately does not emit with the reasons. Those reasons are the package.
- `telemetry.go` — the pure function, and the two decisions that are judgement
  calls rather than derivations: the endpoint variable's derivation from a
  kebab-case name, and the refusal when it cannot be made legal.
- `render.go` — the hand-written renderers, and the `writer` with its two
  methods that makes a `%` in generated code impossible to mistake for a
  substitution in the generator.

**A generated artifact is written, printed, and compiled — and the compilation
is the rule this packet added.** `cafe gen telemetry` writes three files; two of
them are Go. cafe runs its own generator on its own `anywaye.yml`, so
`internal/telemetry/` is a committed copy of generated Go that `go build ./...`,
`go vet ./...`, `gofmt -l .` and `go test ./...` all reach, and
`telemetry/otel-endpoint.json` is validated against core's own vendored schema
before it is ever written.

The first arrangement was the conventional one and it was wrong: the goldens sat
under `internal/gen/testdata/`, which the go toolchain excludes from every
package, so the generated Go did not compile and its twelve-test suite never ran —
and `internal/ci`'s walker counted those twelve tests anyway, so `gate.yml`'s
suite floor was twelve high and the ratchet reported twenty-one skips where
`expectedSkips` says eight. Twenty-one is a number nobody can account for, which
is the ratchet doing its job.

So there is one copy, in the tree, compiled, and `internal/gen`'s drift gate
compares the generator's output against it byte for byte. A generator bug that
emits Go which does not compile is a red gate rather than a file somebody finds
out about when they run it. **A generated file nothing compiles is a
demonstration that rots**, and this repository already says that about the live
tier for the same reason.

`internal/telemetry` is therefore not cafe's telemetry: it is wired to nothing, it
imports no OTel SDK, and `cafe` exports no signal. It exists so that the generator
has a first consumer whose compiler and test runner are the ordinary gate, and it
is the first thing to delete when cafe has a real OTel setup of its own — at which
point `cafe gen telemetry` becomes a command other services run rather than a
command cafe runs on itself.

**A pin is of the bytes, and it is the fourth thing `internal/ci` holds.** This
tree vendors core's schemas, runs its own generator on its own manifest, and
declares its gate against a schema core owns — and until `internal/lock` landed,
nothing anywhere said "the generated output is exactly what the pinned spec
produces." Every individual file looked fine: a vendored schema with one
hand-edit in it is still valid JSON, and `internal/telemetry/telemetry.go` with a
trailing newline appended to it still compiles, vets, passes `gofmt -l` and
passes its twelve tests. The failure was a client and a server disagreeing on the
wire, found in production rather than in a gate.

`cafe.lock` at the repository root is one entry per file the tree DECLARES — four
kinds, `spec`, `vendored-schema`, `generated-client` and `rule-bundle` — each with
the SHA-256 of its bytes, plus the `lockVersion` that invalidates the format
wholesale and the `tool` that wrote it. Three rules hold it together:

- **Nothing is discovered from a filename.** The vendored schemas are walked from
  the directory whose README declares the convention, and the generated files are
  read out of `internal/gen.TargetNames()` and `Output.Paths()` rather than from
  a list written down beside them. A written-down list of "the three files cafe gen
  writes" would be a second copy of `internal/gen`'s idea of its own output, and
  the failure when the two disagree is a lockfile pinning two of three — the
  half-landed bump this was written for.
- **The hash is of the bytes.** No git blob ids, so the lock verifies the same way
  in a release tarball and in a Docker build context as it does in a checkout. A
  pin that needs a version-control system present is a pin that cannot be checked
  where a pin matters most.
- **There is no timestamp.** Two runs over one tree produce the same bytes, so a
  diff between two locks is a diff between two trees. Same argument as
  `cafe gen`, same payoff.

`cafe lock --verify` is the check, and **its output is the product**: every
mismatch named with its path, its kind, the pinned hash, the hash on disk, and
the remedy — all of them in one run, because a verifier that stops at the first
teaches people to re-run it and ends up committing the other four one at a time.
The remedy is stated once per problem, after the findings, because forty copies
of the same paragraph above forty filenames is a wall.

`internal/ci/lock_test.go` is the gate row, and it is an ordinary `go test` so
`bin/prime` runs it — the only place a gate belongs here, the same argument the
drift gate above makes for itself. Its second test is the self-test: it takes the
committed lock, copies the tree it pins into a scratch directory, changes one byte
of one pinned vendored schema, and asserts the report goes red and names it. A
check that has never been watched red is a check that might not work, and
`TestEveryProblemIsDrivableAndNamed` in `internal/lock` is the same control over
the same command with every problem as a row.

**`gate.yml` is a pinned file, and that is worth knowing before adding a test.**
`cafe lock` treats it as the tree's one `rule-bundle`: a named, versioned rule set
owned by core's `gate.schema.json` and checked by core's own checker. So a commit
that adds tests raises the floor in `gate.yml` AND has to re-run
`go run ./cmd/cafe lock .`, because the floor's hash moved. That is friction, it
is deliberate, and the argument is the same one as for the vendored schemas: a
declaration that changed without the change being deliberate is the defect. The
alternative — leaving `gate.yml` out of the lock — would leave the file that
decides what "green" means here as the one artifact in this repository with no
pin on it at all.

`internal/agent` is `cafe agent`, split by the same rule, and it is the client
half of the agents-first flow — the thing an agent actually runs. It is the
mirror of `internal/box` across the subject split in
anywaye/notes/agents-first.md: a box is a MACHINE with a keypair that enrols, an agent
is a PRINCIPAL that registers, becomes claimed by a human, and holds a token:

- `doc.go` — what the package is, its three seams, the output contract, and the
  one invariant everything else is downstream of.
- `ring.go` — the `Keyring` seam and the account a token is filed under. Same
  three calls as `box.Keyring`, same service name, different account prefix: an
  agent's token can never overwrite a box's key.
- `config.go` — `config.json`, every saved session, and which one is active.
  Non-secrets only, for the same reason `box.json` holds none.
- `endpoint.go` — the six routes identity answers, the `Endpoint` seam and
  `HTTPEndpoint`, and `StatusError` with its own hint: the 403 an unclaimed
  agent gets is an instruction, not a status code.
- `names.go` — the username grammar and the retry that regenerates on a
  collision, because a generated name is not a name anybody chose.
- `store.go` — register, login, whoami and the four token verbs, with the
  ledger's ordering rule applied to a credential.

**The token is never printed, and that is a property of the types rather than
of a comment.** `RegisterResult` carries the token for exactly as long as it
takes to move it into the keyring; every document a command prints answers with
`token_stored` — an existence — and the key's non-secret metadata. `Dial` is a
function rather than a stored client for the same reason: a Store that held an
authenticated client would be a Store that held a token for as long as it
lived, which is invariant 1 verbatim. The test renders every document every
command produces, across the whole normal flow, and searches both streams for
three distinct token values — with the control half asserting the agent's
username IS printed, because a build that redacted everything would pass by
leaking nothing at all.

**Human prose and the claim URL go to stderr; machine JSON goes to stdout.** An
agent pipes stdout into a parser; a person reads stderr. There is no `--json`
flag to toggle between them — a flag that chooses which stream gets the
document is a flag that guarantees one of the two is wrong in some script — and
the test asserts stdout is one CLEAN JSON document on every verb.

`internal/verify` is the verifier the box runs UNDER — the ops half of
`cafe box verify`, split by the same rule:

- `doc.go` — what the package is, the two questions it answers before the box
  answers, and the three rules that hold them together (no secret is ever a
  fact, the ledger keeps fingerprints and never values, a degraded answer
  names the human).
- `verify.go` — `Check(Probe) Verification`: the pure classifier over the
  observe.report document, the secrets the box must hold, and the minted-secret
  ledger, with the reason→remedy table that attaches the person's next line to
  every degraded fact.
- `state.go` — the minted-secret ledger: the one piece of state the verifier
  owns, and the reason it exists (an enclave build must not mint a second
  `SECRET_KEY_BASE`).

It imports `internal/boxd` for the three fact types it judges and nothing
else, so it stays buildable for every release target with the standard
library — the daemon's own rule, applied to the check the daemon runs. The
CLI half is `internal/cli/box_verify.go`: the inventory cross-reference, the
observation document on disk, and the value that arrives on stdin and never in
argv.

`internal/box` is `cafe box`, split by the same rule and it is the package the
whole agents-first layer turns on:

- `doc.go` — what the package is, its four seams, and the one invariant that
  shapes every other file.
- `ring.go` — the `Keyring` seam and `FileRing`, the 0600 store it defaults to.
- `identity.go` — the `Identity` type, which has NO private key field, and the
  two encodings that turn a keypair into wire form.
- `control_plane.go` — the `ControlPlane` seam and the HTTP client that redeems a
  capability, with box's four answers mapped onto three sentinels.
- `store.go` — the enrollment journal, the ordering rule it enforces, and the
  three stages a retry converges from.
- `lifecycle.go` — the phase table, and `status`/`plan`/`apply`/`logs`/`verify`
  over it.

**The private key is never printed, and that is a property of the type rather
than of a comment.** `Identity` has no field that could hold it, so no document,
error, report or `%v` over a store's output can carry one; the only thing that
ever leaves this package about the key is its EXISTENCE, which `verify` reports
as a boolean. The non-secrets are a separate file and the key is a separate
store, so the thing that gets shared carries no credential and the thing that
holds the credential is not a file anybody reads. The test asserts it over
every document every command produces AND over both streams, with the control
half being that the public key IS printed — a build that redacted everything
would pass by leaking nothing at all.

`internal/setup` is `cafe box setup`, split by the same rule, and it is the
command between a host nobody has touched and a box that answers. It is kamal
`setup` parity for the box side, and the precedence is kamal's precedence —
`superuser?`, `installed?`, `in_docker_group?`, in the order
`kamal/cli/server.rb`'s bootstrap runs them, spelled the way kamal's own command
test pins them:

- `doc.go` — what the command is, the four rules, and why the transport is the
  operator's own `ssh` rather than an SSH client library.
- `host.go` — the `Host` seam and `Output`, where a non-zero exit code is an
  ANSWER rather than an error, because a check is a command whose exit status
  says whether the thing is already done.
- `plan.go` — the ten steps as data, the two documents the host gets, and the
  four actions a step can conclude.
- `arch.go` — the architecture preflight, the one question whose answer changes
  WHICH BYTES cross the SSH connection. `uname -sm` on the host and three
  outcomes: the running binary ships when it is built for this host; a
  `cafe-<goos>-<goarch>` beside it ships when its BYTES say they are for this
  host; and otherwise the run refuses, naming both platforms and the one build
  command that produces the right one — before Docker, before any write, and
  with no capability presented. The bytes decide, not the filename: a sibling
  named for the host and built for another machine is not shipped, and that
  negative control is a test because it is the defect this package was measured
  on.
- `setup.go` — the three phases: probe, walk, verify.
- `wipe.go` — the mirror: `cafe box wipe`'s ordered check-and-apply table
  (boxd, its unit, the binary, anywaye's state, k3s, Docker), the read-only
  fingerprint that refuses a host setup did not install, the KEPT list the help
  and the document both read, and the one confirmation seam — asked after the
  probes and before the first removal, so a stranger's machine is refused rather
  than offered a prompt.
- `ssh.go` — `SSHHost`, the real `Host`: the local `ssh` binary under `os/exec`,
  and the askpass helper that carries a password on a pipe so it reaches
  neither argv, an environment variable nor a file.
- `write.go` — `SSHHost.Write` and the transfer under it, and it is the one
  method in this package whose shape was decided by a measurement rather than
  by a preference. See the paragraph below.

**A step is a check and an apply, and the check runs first every time.** That is
the whole idempotence claim and it is not satisfied by an apply that happens to
work: `docker -v` is asked before `curl … | sh` is reached, the binary is
compared by sha256 of the bytes rather than by a version string, and the
enrollment's own `box.json` is asked about before any capability is presented. A
second `cafe box setup` on a host that has had one runs nine read-only commands
and applies nothing.

**The controller holds no keypair, and that is a property of the seam rather
than of a comment.** The keypair is generated on the host by the host's own
`cafe box enroll`; what crosses back is three non-secret strings read out of the
host's box.json. `Host` has two methods — run a command, write a file — and
neither can return a credential. The test asserts the controller's keyring is
empty after a full setup and after a second one.

**The capability is presented once, and the count is what proves it.** Box
enforces single-use in its own database, so a second redemption is always
refused server-side and a test that asserted "the retry failed" would pass
against a client that redeemed twice. The journal is the fixing: `prepared`
before the POST, `redeemed` immediately after the response, and the identity
written last and only when the redemption is safely recorded. A crash between
the response and the record is the one window that cannot be closed from this
side, and it is stated rather than hidden — cafe answers an honest refusal naming
it. Everything else is drivable into a table with one row per state a cafe
process can stop in, each row asserting the NUMBER of redemptions performed.

**A 29 MB write must not be one 29 MB pipe write, and that is a measurement
rather than a preference.** 17,625,600 bytes of a 29,250,507-byte cafe binary
reached a Contabo box, the write reported success, and the next step failed
with "Exec format error" — a symptom that names the binary and not the
transport. The cause was `SSHHost.Write` putting the whole payload on one
`ssh "cat > tmp"` child's stdin: a pipe the sandbox's proxy caps in silence.
So `write.go` streams: N bounded chunks of 1 MiB appended with `cat >>`, a
remote `sha256sum` compared against the local hash, and only on a match the
`mv` into place. The destination is never opened for writing, so a failure the
transport can have — a cut pipe, a dead child — leaves the host with its
previous binary or with nothing, and never with a truncated one. The hash gate
is what makes the rename worth anything, and it is what a verifier without one
cannot have: a mismatch refuses naming BOTH hashes and BOTH byte counts, and
takes the sibling with it.

The shape is kamal's — sshkit ships with `ssh.scp.upload!`, net-scp over the
same connection, never one giant write (`reference/sshkit`) — and the reason
it is copied by hand rather than with a dependency is `internal/setup`'s
stdlib-only rule: the walk runs on the path that reclaims after a failed
bootstrap, and it imports nothing that could fail to be fetched. What the
chunking costs is one process per chunk, which is the same trade the walk
already made for its dozen commands, said out loud.

`transfer` is the loop with the runner passed in, and that is what makes the
defect a gate assertion rather than a war story: the test's `shellHost` is a
small interpreter that PERFORMS the commands — mkdir, cat with a redirect,
sha256sum, wc, mv — with a `pipeCap` set to the measured 17,625,600, so a
29,250,507-byte payload is refused in the suite, with both hashes named and the
destination untouched. The control half writes the same payload without the
cap and watches it land, because a test that only asserted the refusal would
pass against a transfer that always failed.


`internal/boxd` is `cafe boxd`, split the same way, and it is where invariant 4
lives — the enforcement on the box rather than in the agent's instructions:

- `doc.go` — what the daemon is, and the three rules it holds: a capability
  must be in the table, alive and bound; no route may answer with a
  Secret-shaped value; and the box key's lifetime is a connection's.
- `secret.go` — the `Secret` shape, and the walk that hunts for it. Declared
  in this package because the daemon defines what it is forbidden to return.
- `route.go` — the `Route` type, the `World` interface a handler is handed,
  and `Redeem`, the one entry point a capability has into the box. The
  interface is the rule: no method runs a process, no method returns a
  secret, so a handler cannot need what it was never given.
- `identity.go` — the enrolled identity, the capability, the outcome, and the
  `ControlPlane` channel's two outbound verbs.
- `daemon.go` — the round: load, dial, redeem, ack, reconnect with a bounded
  backoff. No listener in the type system any more than in the process.
- `client.go` — the mTLS dial and the one-JSON-object-per-line protocol. The
  server half of that protocol is box's (W1-P3); it is written down
  here so both sides have something to be checked against from day one.

`secret_test.go` is the load-bearing test of the whole packet: it renders
every route in the table through a recording world and walks each answer's
type graph, failing if any reachable type is Secret-shaped. It carries its
own negative control — a route with a `Credential` in four disguises,
watched going red — because a check nobody has seen fail is a check that
might not work. `tls_test.go` is the second half: a real loopback TLS
listener that requires the box's client certificate, proving the channel is
mTLS rather than described as it.

`internal/edge` is the edge the box runs — boxd's embedded edge, and the plan's
Phase B answer to kamal's per-box proxy (`lib/kamal/commands/proxy.rb`: one
small container on the box terminates TLS, routes by hostname, and switches
traffic between releases with zero downtime). It is split by what it is for,
and it holds three rules:

- `doc.go` — what the package is, why it is not a Traefik with an Ingress in
  front of it, and the one thing it owns.
- `spec.go` — the edge's one piece of state: host → upstream, the health the
  edge observed, and the last-deploy pointer. It is a small record on purpose
  — every field is a field two commands have to agree about — and complete,
  because a routing fact the spec did not carry would be a routing fact kept
  somewhere else. `Load`/`Save` are atomic and the spec is validated on both
  sides of the disk, so a record nobody can read is refused rather than
  interpreted.
- `release.go` — releases on disk, `current` as a symlink, and the one
  operation `Switch` that both a deploy and a rollback are. The flip is a
  symlink plus a `rename(2)` over `current`, so the switch is atomic from a
  reader's point of view: a request either reaches the old release or the new
  one, never a directory that is half-written.
- `tls.go` — the three TLS modes the plan's TLS note names: Let's Encrypt
  HTTP-01 once the zone is proxied, a Cloudflare origin certificate while DNS
  is grey-clouded, and a certificate the box mints itself for `*.svc` names,
  which never appear in public DNS and so have no business asking an ACME
  account for anything. It also serves the HTTP-01 challenge tokens, because
  the edge serves the challenge and the substrate performs it.
- `proxy.go` — the gateway: host routing on the Host header, the health the
  edge probes and records, and the one path the edge answers itself.

**Three TLS modes exist because a box is in three states, and the third is not
a compromise.** `*.svc.anywaye.com` never appears in public DNS (the plan's
stability rule 4 makes a leak a five-alarm fire), so the names that are only
ever reached cluster-internally are terminated with a certificate the box
issued itself and no ACME account is involved at all. A box that had to reach
Let's Encrypt to encrypt its own internal traffic would be a box whose
internal traffic depended on the public internet.

**The edge owns exactly one thing, and that is the whole design.** Host →
upstream, the health the edge observed, and the last-deploy pointer, in one
record under the box's own state directory. `cafe box publish` writes it,
`cafe box rollback` flips it back, and a caller that kept its own host table
would be a caller that could route a host the edge has already retired — a 404
nobody can explain. The `current` symlink is the TRUTH and the spec's
`Current` is the RECORD; `cafe box edge` reports both, because the one state
where they disagree is a deploy that did not finish and that is worth saying
out loud rather than resolving silently in either direction.

**A rollback needs no control plane, and that is the point rather than a
simplification.** The decision is about what is on this disk; it asks nothing
of anyone and cannot be refused by a network. The plan's sign-off checklist
asks for "a broken deploy demonstrates rollback", and on the day a deploy is
broken the control plane is the last thing an operator wants to depend on.

**The proof is the gateway over a real listener, not a unit stub of it.** Seven
tests in `internal/edge` dial a real loopback TLS listener with real SNI, so
`api.anywaye.test` and `auth.anywaye.test` answer `/healthz` (the edge's own
path) and `/readyz` (the app's) and `identity.svc.anywaye.com` answers over a
minted wildcard; the switch test serves the release directory through the
`current` symlink and asserts a publish and a rollback change what the customer
gets with the routing record untouched. `TestARetiredHostIsRefusedAndTheControl
ProvesIt` carries its own control — the row restored and the same request
succeeding — because a test that only asserted the 404 would also pass against
a gateway that refused everything.

`internal/edge` has no dependency beyond the standard library, for the same
reason `internal/box` and `internal/boxd` hold to it: it sits on the path that
keeps a box serving, and a dependency graph on that path is a graph that can
fail to load at exactly the wrong moment. It imports nothing else in this tree
either — which is what lets it be the first thing a box brings up.

`internal/resolve` is `cafe resolve`, split by the same rule, and it is the
consumer of W4's two upstream packets — the thing that makes a manifest's
`dependencies` mean something:

- `doc.go` — what the package is, the three tiers, the one invariant (the
  caller's credential is presented, never stored), and the two things it
  deliberately does not do.
- `resolve.go` — the pure resolver. `Resolve` is a function of the
  manifest, the token and two seam values, which is why every failure
  below is a field in a report rather than an error return: a project with
  one unentitled OPTIONAL dependency resolves, and an error would destroy
  that fact.
- `pantry.go` — tier 1 and tier 2. `HTTPPantry` is the real client; the
  `Pantry` interface is the seam a test drives without a network. Since W3 it
  serves `/v1/projects/{name}` and `/v1/projects/{name}/coordinates` and
  decodes a `Project` — pantry's `Service` schema renamed, its fields did not.
- `control_plane.go` — tier 3, the entitlement check → mint → redeem, in one
  call. The capability token crosses from the mint to the redemption and
  is dropped on the floor; the deliverable is the fetch GRANT, which is
  the capability's scope and not a secret.

**A failure is named with the server's own code, never paraphrased.**
`ControlPlaneError` and `PantryError` carry the code and detail the service
sent, and the resolver surfaces both verbatim: `403 entitlement_required`
with the product and where to start one, `403 version_not_granted` with
the granted range and the requested one. The tests assert the product
name and the URL are in the message, and that both ranges are — a
paraphrase that said "not entitled" would be a sentence that sends a
reader to a log that says nothing useful, which is the `403
agent_unclaimed` habit applied to a second service.

**The tiers are walked in order, and the order is the semantics.** A name
that does not exist has no coordinates and no entitlement to check, so a
tier-1 failure ends the walk; a caller with no credential cannot read
coordinates, so a tier-2 failure ends it before tier 3. A free service
never reaches tier 3 — box's check would refuse it with a 422, and
the client that asked anyway would be spending a round trip on a question
the server already answered at boot. That last fact is why the resolver
carries `Products` (box's paid vocabulary) rather than asking: there
is no "what are the products" operation, and the drift risk is named in
the constant's comment — the safe direction, because the alternative is a
product that never resolves.

**`required: false` degrades, and the decision is the command's, not the
resolver's.** The resolver reports every failure WITH its dependency's
`Required` flag and never consults it; `cafe resolve` is what turns that
flag into a warning on stderr and an exit 0, versus a BLOCKED line and an
exit 1. Box never sees `required` — it is cafe's side of the
contract, and a server that had an opinion about it would have two.

`internal/secret` is `cafe secret`, split by the same rule, and it is the
inventory the Attended tier sells as its first assurance item:

- `doc.go` — what the package is, where the enumeration comes from, the three
  classes, and the one invariant (the inventory never reads a secret's value).
- `inventory.go` — the table: every key the kit installer's SECRET RECORD
  declares, plus the three it does not carry (the Redis password, the Postgres
  role passwords, muse's vault key), each with its class, its reason and the
  file:line that reason comes from. It is also the lookup, which is why the
  inventory and the rotation cannot disagree about what a secret is.
- `rotate.go` — the pure `PlanRotation`, the `Runner` seam, and the value
  generator in the installer's own spellings. **A generated value never
  reaches argv** — argv is visible to every other process on the box through
  /proc — so it travels in an apply document on stdin instead, which is the
  discipline the kit installer holds itself to. The presence check is
  templated to answer with key NAMES only, so the read a rotation performs
  cannot carry a value even by accident.
- `state.go` — the schedule. `last rotated` per entry in a JSON file that
  holds no credential, which is what makes "on a schedule" a fact about the
  last run rather than a cron line nobody can see.
- `render.go` — the JSON document `cafe secret inventory` prints.

**A rotation REFUSES rather than skips.** The not-rotatable class is the
product: muse's vault key seals every stored credential, so a rotation forks
all of them, and identity's JWKS publishes exactly one key, so a rotation of
the OIDC signing key is a cutover with no overlap window. A run that skipped
those silently would answer "nothing to do", and the operator would conclude
the inventory held nothing that needed rotating — the weekend the packet
exists to prevent. So every refusal names the entry, the class, the reason and
the citation, and the exit is non-zero.

It has one import, and it is this repository's own `internal/deploy` — for
`ResolveKubectl` and `ErrKubectlMissing`, so that "kubectl is not installed"
is spelled in exactly one place in this tree. It adds no external dependency,
and its own two clients (the kubectl invocation and the state file) are
standard library only, which is the same constraint the reclamation path
holds.

`internal/failure` is the failure-mode library of OPS-P12, split by the same
rule, and it is the one branch of the tree whose product is a sentence a
customer reads rather than a file a machine writes:

- `doc.go` — what the package is, the ten entries it holds, and the three
  rules that hold them together: a condition is DATA, a rule the observation
  cannot answer reports UNKNOWN rather than a pass, and no configured value is
  ever a fact.
- `rule.go` — the library itself: the ten rows `anywaye/notes/ops.md:742-753`
  records, each with the doc's condition, what a generic monitor sees, what
  we know, the remedy derived from it, and the core contract it cites.
- `condition.go` — the twelve-predicate vocabulary and the evaluator that
  returns three answers rather than two. It is stdlib only.
- `observe.go` — the fact surface (`Observation`), the `Observer` seam, and
  `BoxObserver`, which adapts `internal/boxd`'s `World` into the facts it has.
- `check.go` — `Evaluate`, `Report`, and the rendering: every hold names the
  rule, the evidence, the remedy and the contract; every unknown names the
  fact the box did not supply. `cafe failure check` (`internal/cli/failure.go`)
  is the verb over it, and its observation document is the same `Observation`
  shape, so the CLI and the box daemon are two producers of one input.

**A rule that cannot be evaluated says so, and that is the whole design.**
The library's first consumer is the box daemon, whose `World` carries service
names and the install phase and nothing else, so a checker driven by it answers
`unknown` for all ten rules — and names the fact each one needs. A false "does
not hold" would be a green light over a blind spot, which is the report
`internal/lock` refuses to produce; `TestTheBoxDaemonsObservationAnswersNothingAndSaysSo`
in `check_test.go` watches that answer stay unknown rather than drift into a pass.

`internal/stack` is `cafe box deploy` and `cafe box rollback`, split by the
same rule, and it is the one piece of state a box owns so that a rollback
needs no control plane:

- `doc.go` — what the package is, the shape on disk, and why a stack root is
  one service's.
- `release.go` — `Release`, `History`, `Store`, and the four file operations a
  deploy path is made of: stage (an exclusive create, never in place), switch
  (a relative symlink moved by rename, which is the atomic step), current, and
  prune (keep N, never the pointer and never the last-good one).
- `deploy.go` — `Deploy` in six ordered steps, the `Box` seam the box's
  substrate implements, and `flipBack`.

**The switch comes before the gate, and that is the design.** You cannot know
whether a release works without putting it in front of traffic; a second
"test" copy of everything is a staging environment the box does not have and
that would prove nothing about the release actually serving. What makes it
safe is the fixing: `flipBack` writes the last-good pointer at the moment of
the failure and reads it back off local disk, so a rollback makes no network
call and cannot fail because something else is down. Two pointers, because two
questions — "undo this" walks back one release (`previousGood`), and "the
deploy broke" jumps to the last one that passed a gate (`LastGood`).

**The edge's host and certificate state is READ, never owned.** A deploy
refuses a host the edge does not route, or whose certificate is not valid
now, BEFORE it stages anything: that is the one moment the answer is free,
and the check is the last thing between a deploy and a release nobody can
reach. The state comes from the box's own edge file, because a second copy of
it in the deploy path would be a copy that can disagree with the one serving
traffic.

**A release record carries the NAME of the secret set, never a value.** The
box installs the set INTO the release directory at deploy time — that is
"secrets at deploy, pushed to the box, never baked into the image" — and the
record above it is a sentence a stranger could read without learning
anything, which is why history.json is 0644 rather than 0600: the release
history is the product an operator reads with `cat`.
`TestTheReleaseRecordCarriesNoSecretValue` installs one and searches every
record the stack wrote, with the control half asserting the name IS there.

**Nothing here needs a container runtime to prove anything.** The `Box` seam
is three methods; a test drives a fake that counts, and the e2e test serves
the release directory through a real loopback HTTP edge that routes on the
Host header — so a service change through `api.anywaye.test` and
`auth.anywaye.test`, and a broken deploy flipping back on purpose, are
assertions in the gate rather than a screenshot in a report.

`internal/stack` is stdlib only, and it holds to it for the same reason
`internal/box` does: the release history is the state a box recovers itself
with, and a rollback that needed a library fetched over the network would be a
rollback that could not run on the box that needs it.

**A dependency is the default, and the exception is the thing that has to be
argued for.** The old rule here was "write it ourselves unless the standard
library cannot do the job", and it is inverted: a good existing library is
encouraged, because the failure mode it prevents is a wheel reinvented badly and
reported as working. This tree has already lived that: `internal/emitter`'s
first wire test was a socket that read five bytes and replied with a fixed
frame, it counted a TLS ClientHello as a delivered span, and the lie hid the
bug until a real collector caught it. A hand-rolled transport does not fail
loudly; it reports success. So the default is: find the library, check it fits
the stack, use it. Writing our own is the exception, and **the exception needs
its reason at the code site** — which library was considered, and what "fit"
failed on: bloat, a wrong model of the problem, a dependency that drags in a
tree, a licence. A comment that says "we do not take dependencies" is not a
reason. A comment that says what the library could not do is.

**The cost of a dependency is measured here, and measuring it is usually the
whole job.** `golang.org/x/crypto` is already in this repository's module graph
— v0.32.0, required transitively by `x/net@v0.34.0`, with its own `x/sys` and
`x/term` already pinned beside it — so a direct `golang.org/x/crypto/ssh`
would add a line to `require`, not a module tree. The packages below that carry
none were argued for one at a time on their own facts, not on a count.

Where a package carries NO dependency, the reason is specific and it is written
here rather than assumed. `internal/box`, `internal/ledger`,
`internal/ports`, `internal/reclaim`, `internal/ryuk`,
`internal/lock` and
`internal/run` have none, and that is a constraint rather than an accident: the
reclamation path is the one that has to keep working when nothing else does, and a
dependency graph on the path that cleans up after a failure is a graph that can
fail to load at exactly the wrong moment. `internal/run` holds to it for a third
reason of its own: it is the state a box installs itself into, and the phases it
records are the ones that run before anything else is on the box — a store that
needed a library could not be read by the `cafe` binary that has to recover the
install, only by a newer one. `internal/lock` holds to it for a second reason: the
failure it exists to catch is two processes disagreeing about a vendored schema
or a generated client, and a verifier that cannot be rebuilt from a release
tarball with nothing but a standard library is a verifier nobody can run in the
one place a tarball gets checked. `internal/gate` holds to it for a reason of its
own: its whole job is to be the one place two numbers cannot disagree — the
ratchet's `declared` count and the writer's suite floor are the same count — and
a dependency that changed what "a test" is would move a floor silently. It is the
standard library and nothing else, and it imports nothing else in this tree.
`internal/cve` is the exception that proves the paragraph, in the other direction:
it holds to no-dependency for the reason the paragraphs above give for the
reclamation path, and for one of its own — the advisory source is `net/http` and
`encoding/json`, so the scan that answers "what on my box has a known advisory"
cannot fail to build, or fail to run, because of a dependency tree on the
operator's machine. A scanner that needed a library would be a scanner whose
answer depended on somebody else's release schedule.

`internal/scaffold` is stdlib plus `internal/contract` and brings that package's two libraries with it, which is
the deliberate answer rather than an accident of the import: the manifest a
template renders is validated by the same compiled schema `cafe contract lint`
runs, and the generator cannot be allowed a second, weaker copy of the check its
output has to pass. `internal/resolve` is the same trade for the same reason: it
imports `internal/contract` for the ONE type it must not have an opinion about —
`contract.Dependency`, the decoded form of a manifest's `dependencies` entry —
because a resolver that re-declared "a dependency is a name, a version and a
required flag" would eventually disagree with the parser about whether an absent
`version` is empty or absent, and that argument is the tier-3 question itself.
Its own two clients are stdlib only: the tier-1/tier-2 path to pantry and the
mint-and-redeem path to box are `net/http` and `encoding/json`, so the
resolution path carries no dependency the pantry client cannot be rebuilt from a
release tarball with. `internal/box` is stdlib only, and it holds to it for the
same reason: the keyring seam and the control-plane client sit on the path that
reclaims after a failed enrollment. `internal/failure` is stdlib plus
`internal/boxd` and nothing else, and it holds to it for the same reason
`internal/box` does: the library is checked against the box that reclaims
after a failure, so the rule set cannot carry a dependency the reclamation
path would have to fetch. What it imports from `internal/boxd` is the `World`
seam and nothing that reaches a network. `internal/edge` has none either, and
it holds to it for the same reason and one more of its own: it sits on the path
that keeps a box serving, so a dependency graph there is a graph that can fail
to load at exactly the wrong moment; and because it imports nothing else in
this tree, it is the first thing a box can bring up.

`internal/setup` has none either, and under the new rule that needs its reason
too — here it is the transport reason below, not the tarball one: the walk
runs on the path that reclaims after a failed bootstrap, and what it imports
from `internal/boxd` is ONE type — `Observation`, the document the heartbeat
step parses — which is stdlib-to-stdlib, so nothing that reaches a network
comes with it. The transport is the operator's own `ssh` under `os/exec`, and
the argument is fitness rather than count. `golang.org/x/crypto/ssh` is already
in the module graph, so it would cost a `require` line and not a tree — the
rule does not refuse it on cost. What a client library would not resolve is
the operator's own `ssh_config`: the `Host contabo` / HostName entry this
repository has been running against for two days, ProxyJump, ControlMaster,
the agent and the known_hosts. `ssh` reads all of that for free, and a
reimplementation that resolves a config file almost-right is a host that
connects on the operator's machine and on nobody else's. What exec costs is
one process per step — about a dozen in the walk, each with a bounded
timeout — and, measured live on 2026-10-13, a large payload handed to ssh on
stdin can be truncated by the write side. That is paid for rather than
hidden: a write lands on a sibling and moves into place, so a truncated one
leaves the previous binary behind, and the step that follows verifies the
installed bytes by sha256.

`internal/contract` has two: `github.com/goccy/go-yaml` for YAML (chosen for
parse errors with a line and a column, and for zero dependencies) and
`github.com/santhosh-tekuri/jsonschema/v6` for draft 2020-12 validation.

**Packages with dependencies, and the argument each one rests on** — seven
packages: `internal/contract` (two: the YAML library and the draft 2020-12
validator), `internal/backup` (one, the same YAML library),
`internal/scaffold` (both of `internal/contract`'s, for the same reason that
package uses them), `internal/resolve` (`internal/contract` again, for the ONE
type it must not have an opinion about), `internal/mcp` (the official Go SDK)
and `internal/emitter` (the OTel SDK and the OTLP/gRPC exporter) — plus
`internal/failure`, which imports `internal/boxd` and nothing else, in-tree
stdlib on both ends. Everything else is standard library only, and that is a
claim about the tree in the OTHER direction now: a package that carries none
says why in its own doc, because the default is a library, not a count. A
dependency added anywhere in this tree needs the same argument at its own code
site — which library, what it saves, and why the alternative is worse.

`internal/backup` has one, and it is the first of those: `github.com/goccy/go-yaml`
again, to read `config/kamal-backup.yml` and the accessory block out of the document
`kamal config` prints. The same argument answers it — the standard library has no YAML
parser at all, and this one reports a line and a column, which is the difference
between "your backup configuration is wrong" and a sentence naming what is wrong.

`internal/gen` has none, and that is a decision with a cost rather than a
default. It imports `regexp` and `encoding/json` and nothing else, which is why
`cafe gen telemetry` can emit an OTel setup that is itself stdlib-only: the
generator cannot write a file that pulls a dependency into a service, because the
generator has no way to know which version that service wants. The cost is that
the emitted setup is the *contract* — the allowlists, the prohibition, the resource
attributes, the endpoint variable, the no-op path — and not the SDK wiring, which
the emitted file's header names as `go get` lines instead. That is the honest
split: what core specifies is derived, and what a service's dependency graph
decides is left to the service.

`internal/telemetry` — the generated copy — also has none, for the same reason and
by the same mechanism: a generated file that imported an SDK would put a version
pin in a file cafe generates, which is a decision cafe is not making on a service's
behalf. It is also worth saying that a generated file with no `go.mod` change
cannot break a service's build with a dependency resolution failure, which is the
failure mode that makes generated code untrustworthy.

`internal/mcp` has one: `github.com/modelcontextprotocol/go-sdk`, the official
Go SDK for the Model Context Protocol. The standard library has no JSON-RPC, no
protocol-version negotiation and no tool-call semantics, and MCP's wire format
has moved several times — so a hand-rolled framing layer is a month of work that
never stops being maintained. That package doc holds the full argument and the
three candidates that were measured against it. A dependency added anywhere in
this tree needs the same argument at its own code site.

`internal/emitter` is the biggest of the seven, and it is the argument the rule
above is written for. It carries five of this repository's eight direct
`require` lines — the OTel SDK, the OTLP/gRPC exporter, `otel/sdk`,
`otel/trace` and `google.golang.org/grpc` — because the standard library has no
OTLP, no protobuf and no exporter semantics, and because the measurement is
already on the record: the package's first wire test spoke to a hand-rolled
socket that read five bytes and replied with a fixed frame, the socket counted
a TLS ClientHello as a delivered span, and the lie hid the bug until a real
collector caught it. **A transport written by hand does not fail loudly — it
reports success.** So this package emits through the SDK and is proven against
a real OTLP receiver on a real loopback port
(`TestTheSpanReachesARealOTLPReceiverOverTheWire`), which is the only shape of
proof that catches the failure the first version had.

**The schema is vendored, never fetched.** `internal/contract/schemas/` holds
a byte-identical copy of core's manifest schema, pinned by sha256 in
`schema_test.go`, with its provenance and refresh procedure in that
directory's README. Copy it over and update the pin; never edit it in place and
never download it at runtime. A linter that fetches the contract it validates
against gives a different answer on a different day.

`internal/contract/schemas/telemetry/` holds six more copies from the same core
commit, pinned one hash per file in `telemetryPins` and checked by
`TestEveryVendoredTelemetrySchemaIsPinned`. They are here for the opposite reason
to the manifest schema, and the difference is worth being precise about:
`cafe contract lint` validates a document against core's schema, while
`cafe gen telemetry` READS core's schema to know what to emit. So a fact in those
six files — an allowlist, the fourteen prohibited measurement attributes, the ten
resource attributes, the thirteen `error.type` values, the span-name grammar, the
`const`s that pin the no-op path — reaches cafe's output because cafe read it, and a
core release that moves one of them changes what cafe emits with nobody editing cafe.
That is the entire mechanism behind a generated artifact that does not drift, and
it is why there is no allowlist transcribed anywhere in this tree.

Four of core's ten telemetry schemas are deliberately absent, and which four is a
decision rather than an oversight: `probes.schema.json` needs a service's runtime
dependency list and no field in `anywaye.yml` records one — and a `readyz` that
checked nothing is the failure that schema exists to prevent — and the three `slo*`
schemas are about budgets a person decides per service. Emitting either would be
emitting a guess, and a guess in a file something else is written against is the
kind that gets copied. The reasoning is in `schemas/README.md`.

**Comments say why.** Explain the decision and the constraint, not the
mechanism. A comment restating the line below it is noise.

## Two tests that are not in the gate, and why

`internal/ports/live_test.go` and `internal/ryuk/live_test.go` are skipped
unless `CAF_LIVE_DOCKER=1` and `CAF_LIVE_RYUK=1` are set. Both need a container
runtime, and `bin/prime` must run on a bare CI runner.

They are still in the tree, as ordinary `go test` code, rather than as scripts in
a directory nobody runs — a demonstration that lives outside the suite is a
demonstration that rots. Each one carries the safety rules its own risk demands
in its own comments: a scratch port outside 15000-15999, a label on everything it
creates, a filter asserted non-empty and matching nothing before a reaper with the
Docker socket mounted is started, and a control container plus a sibling worker's
resources asserted intact afterwards. **Read those comments before running one on
a machine that is not yours.**

cafe-07 added the third way to run them, `bin/prime --live`, and the declaration
that keeps the tier accounted for. That is the next section, and it supersedes
the first two paragraphs of this one as the current answer to "why is the live
tier not in the gate" — this section is kept because the *files* are still the
thing you must read before running either of them.

`internal/ci` holds the other checks, and one of them is a gate cost worth
knowing about: `TestTheTreeBuildsForEveryReleaseTarget` cross-compiles the whole
tree for every `goos` in `.goreleaser.yml`, which is about fifteen seconds of a
cold build. It earns that, because it is the only thing in the repository that
would have noticed `cafe` did not compile for Windows at all — a gap that had been
there since `doctor`'s memory probe was written for two platforms.

## Gates

```sh
bin/prime            # go mod download && go build ./... && go test -v ./... , then say what ran
bin/prime --live     # the same, with the seven live tests running instead of skipping
go vet ./...
gofmt -l .           # must print nothing
```

All three are required before a commit lands. `go vet` and `gofmt` are not in
`bin/prime`; run all three.

**`gate.yml` at the repository root is the declaration of all this.** The format
is `anywaye/core`'s `schemas/gate.schema.json`, the checker is its
`harness/gate_check.py`, and the reasoning is its `docs/gate.md` — including the
two alternatives that were measured and rejected. Three consequences for this
repository, and they are rules rather than notes:

- **`bin/prime`'s last two lines are load-bearing.** They are the only countable
  output this gate has, and `gate.yml`'s three floors are read out of the first
  of them. `go test` on its own prints one `ok <pkg>` line per package and no
  test count at all, so before cafe-07 there was nothing in this tree a floor
  could be read from. The accounting section is not decoration and
  `internal/ci`'s `TestTheGateReportsItsOwnCounts` fails if either line changes
  shape. If you reformat them, fix `gate.yml` in the same commit.
- **Adding a test fails the gate until `gate.yml`'s floor is raised.** That is
  `TestTheGateFloorIsNotBelowTheSuiteCafClaimsToHave`, and it is core's ratchet.
  The identity it holds is `declared - minimum == expectedSkips` — currently 8,
  and the eight are the seven live tests and the re-exec'd child, named at the
  constant. It is exact in both directions, so a test that starts skipping is
  caught the same way. **Raise the number; do not edit the assertion.**
- **`tests/gate-declaration-self-test.sh` is the control over the control.** It
  copies this repository's declaration, breaks exactly one thing at a time, and
  asserts core's checker goes red and *names* the finding. It is not part of
  `bin/prime` — a self-test inside every gate invocation would be a second gate
  that can disagree with the first. Run it before landing a change to `gate.yml`,
  `bin/prime`'s summary, or the CI workflow's gate step. A stale recipe in it
  fails loudly rather than passing.

To check the declaration from core, without running anything:

```sh
../core/harness/bin/gate-check .            # the static half
../core/harness/bin/gate-check --prove .    # and the proving half, which runs bin/prime
```

`bin/prime` is **not** kit's Go template and does not claim to be. It is
anywaye's own, and it is the gate CI runs, because a CI-only variant of
a gate is worse than no gate: two commands that can disagree, one of them
nobody runs locally, and a green badge that means the one nobody runs. Adopting
kit's template is a decision, not a chore — it would add `go vet`, `-count=1`
and a `--fast` flag, and each of those changes what a green `bin/prime` means.
`-count=1` is **deliberately still not there**: `go test` replays a cached run's
verbose output verbatim, so a second `bin/prime` on an unchanged tree reports the
same numbers as the first, and the floors read the same either way.

Coverage is 88.8% and the CI gate is 85% (`.github/workflows/ci.yml`). Both
numbers move as code lands; raise the gate when the floor does, never the other
way round.

## The live tier, and why it is not in the gate

`bin/prime --live` sets `CAF_LIVE_DOCKER=1`, `CAF_LIVE_RYUK=1` and
`CAF_LIVE_KAMAL=1` and runs the seven live tests instead of skipping them. It is a
second mode and not the declared gate, for three measured reasons, all in
`REPORT-caf-07-gate`:

1. `TestTheSilentCollisionIsReal` asserts a blind spot of a **particular**
   container runtime. On a runtime that arbitrates properly its subtests
   `t.Fatalf` rather than skip, so a gate carrying it would be red on every
   GitHub-hosted runner, which is plain dockerd.
2. `TestAClosedLeaseReapsAndAnOpenOneDoesNot` is currently red on the machine
   cafe-06's report says it was verified on.
3. `bin/prime` must run on a bare CI runner. That has been the rule here since
   cafe-06 and nothing in this packet reverses it.

Reason 3 is the general one, and it is why `internal/deploy`'s two and
`internal/backup`'s three joined the tier rather than the gate: they need `kamal` on
PATH and a container runtime with a Docker socket the test can reach, and neither is
something a bare runner is guaranteed to have. They are demonstrations rather than
checks for the reason the first two are, and they take minutes.

**The live tier is not silently absent, and that is what the declaration is for.**
Every run prints `cafe: live tier: 0 of 7 executed` and `gate.yml` has a
`live-tier` proof that matches it, so a run in which the tier did not execute
says so in a line the declaration requires. Delete the line and the gate is
red. `--live` is the mode in which it *cannot* silently not run: it exits
nonzero if the tier did not execute, and
`tests/gate-declaration-self-test.sh` proves that by running it on a PATH with
no container runtime on it.

`internal/ports/live_test.go`, `internal/ryuk/live_test.go`,
`internal/deploy/live_test.go` and `internal/backup/live_test.go` stay in the tree,
as ordinary `go test` code, rather than as scripts in a directory nobody runs — a
demonstration that lives outside the suite is a demonstration that rots. Each one
carries the safety rules its own risk demands in its own comments: a scratch port
outside 15000-15999, a label on everything it creates, a filter asserted non-empty
and matching nothing before a reaper with the Docker socket mounted is started,
and a control container plus a sibling worker's resources asserted intact
afterwards. **Read those comments before running one on a machine that is not
yours** — and note that the second one's whole-machine assertion is currently too
strict to be reliable on a shared one.

`internal/ci` holds the other checks, and one of them is a gate cost worth
knowing about: `TestTheTreeBuildsForEveryReleaseTarget` cross-compiles the whole
tree for every `goos` in `.goreleaser.yml`, which is about fifteen seconds of a
cold build. It earns that, because it is the only thing in the repository that
would have noticed `cafe` did not compile for Windows at all — a gap that had been
there since `doctor`'s memory probe was written for two platforms.

**A fourth thing `internal/ci` holds is the drift gate for generated code**, and it
is worth being explicit about where it lives, because the obvious answer is wrong.
`TestTheGeneratedTelemetryHasNotDrifted` in `internal/gen/drift_test.go` regenerates
cafe's own telemetry files from the vendored core schemas and compares them byte for
byte. It is an ordinary `go test`, so `bin/prime` runs it and
`.github/workflows/ci.yml` runs `bin/prime` — which is the only place a gate belongs
in this repository, and the same shape `gate.yml` already declares for its three
floors. There is no separate script, no separate CI step, and no directory nobody
runs, because a drift gate in any of those is a demonstration that rots.

Its counterpart is `notASourceTree` in `internal/ci/prime_test.go`, and that exists
because of a measured failure: `internal/gen` first kept its goldens under
`testdata/`, the go toolchain excludes that directory from every package, and the
walker that counts the suite counted twelve tests `go test` never runs — so
`gate.yml`'s floor was twelve high and the ratchet named twenty-one skips where
`expectedSkips` says eight. If you add a directory that holds Go files `go test`
does not run, add it there in the same commit.


## Adding a subcommand

1. `internal/cli/<name>.go` — name, summary, usage, flags, run function.
2. Add the constructor to `Commands()` in `internal/cli/command.go`,
   alphabetically.
3. Add it to `stubCommands` in `internal/cli/stub_test.go` with the argument
   count it expects.
4. Add the row to the README command table.
5. Add the package to the Layout section above if it is a new one, and count it in
   the dependency paragraph — "Six packages have dependencies" is a claim
   about the tree, and a new package makes it wrong.
6. `bin/prime`, `gofmt -l .`, `go vet ./...` — and if the change added a test,
   raise the floor in `gate.yml` in the same commit, or step 6 will tell you so.

A subcommand that groups verbs (`cafe contract lint`) does all of that and one
more: each verb is a `Command` of its own, named with its full invocation so
its messages read as the words the user typed, and the parent dispatches to it
from `Run` instead of registering it in `Commands()`. Its row in
`stub_test.go` moves from `stubCommands` to `workingCommands` — the parent, not the
verbs — which is why `gen` is in that list and `gen telemetry` and
`gen chart` are not.

A subcommand that generates code has one more obligation: **its output has to be
compiled by this gate.** Write it into the tree, run it on this repository's own
manifest, and commit the result. `cafe gen telemetry` does, and the reason is in
the Layout section — the first arrangement put the goldens under `testdata/`,
which nothing compiles, and the gate's own test-count floor caught it.

## Changing the contract

The schema, the event types and the conventions belong to `anywaye/core`. When
core publishes a new one:

1. Copy it over `internal/contract/schemas/manifest-<version>.json` and update
   the table in that directory's README.
2. Update `manifestSchemaPin` in `internal/contract/schema_test.go` to the new
   sha256, and `TestVendoredSchemaPinsTheEventTypePattern` if the patterns
   moved. Both fail loudly otherwise, which is the point.
3. Re-copy the fixtures from core's `examples/` and `anywaye.yml` — see
   `internal/contract/testdata/README.md`.
4. `bin/prime`, and lint the tree: `go run ./cmd/cafe contract lint .`

If a refresh makes a vendored example invalid, core broke backwards
compatibility. That is a **major** core bump, and the fix belongs in core.

## Secrets
- No `.env` files with values. Use `cafe secret set/get/list` (dev) and `cafe dev` (injects). See `docs/secrets.md`.
