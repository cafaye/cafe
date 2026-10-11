# REPORT-roll-box-edge — boxd's embedded edge: host routing, TLS, zero-downtime releases

**Branch:** `roll/box-edge` (worktree `wt/roll-box-edge`, from `cafe` master)
**Packet:** the other half of setup — the edge the box runs, the failure-mode entry the gate records, and the one piece of state the edge owns
**Status:** **green, two defects found and fixed, and one pre-existing defect
found and NOT fixed (written down in §7).**

```
cafe: 29 packages, 1272 top-level passes, 1234 subtest passes, 0 failures, 8 skips
gate-check .         → OK, 0 failures, 3 warnings
gate-declaration-self-test → 3 of 29 cases FAIL, and all three fail identically
                            on the base commit (proven in §7)
```

`internal/edge` is new (stdlib only), `internal/failure` has its eleventh entry,
`internal/cli` gained three verbs, `gate.yml`'s three floors moved, `caf.lock`
re-pinned, README and AGENTS.md follow.

---

## 1. What the packet is for

`PLAN-names-urls-rollout.md` §4 settles what box ships for its phase:

> **boxd's embedded edge**: host-based routing on the box, automatic TLS
> (Let's Encrypt HTTP-01 — or Cloudflare origin certs while DNS is grey-cloud),
> and release directories + atomic symlink switch behind it, so `cafe box deploy`
> and `cafe box rollback` work like kamal's.

The design reference is kamal's `lib/kamal/commands/proxy.rb`: one small thing
on the box terminates TLS automatically, routes by hostname, and switches
traffic between releases with zero downtime. Today's source does the job with
Traefik and an Ingress in front of it — and the Ingress is the problem. An
Ingress is a Kubernetes object; a box is a host with hostnames on it. **A
customer who has to learn what an ingress is to deploy their app has been failed
by the product, not by the code.** That sentence is the reason this packet
exists and the reason the three verbs are `publish`, `edge` and `rollback` and
not `apply` and `revert`.

## 2. What shipped

### `internal/edge` — the edge, in five files

| File | What it is |
|---|---|
| `doc.go` | what the package is, why it is not a Traefik with an Ingress in front of it, and the one thing it owns |
| `spec.go` | the edge's one piece of state: `Spec` — host → upstream, the health the edge observed, the last-deploy pointer. `Load`/`Save` are atomic and `Validate` runs on both sides of the disk |
| `release.go` | releases on disk, `current` as a symlink, and the one operation `Switch` that both a deploy and a rollback are |
| `tls.go` | the three TLS modes the plan's TLS note names, the box minting its own certificate for `*.svc`, and the HTTP-01 challenge handler |
| `proxy.go` | the gateway: host routing on the Host header, the health it probes and records, and `/healthz` — the one path the edge answers itself |

**Three TLS modes, because a box is in three states, and the third is not a
compromise.** `ModeACME` (Let's Encrypt, HTTP-01) once the zone is proxied;
`ModeOrigin` (Cloudflare origin certificate) while DNS is grey-clouded; and
`ModeInternal`, a certificate the box mints itself for names that never appear
in public DNS. `.svc.cafaye.com` is the plan's internal zone and stability rule
4 makes a leak a five-alarm fire, so those names have no business asking an ACME
account for anything. A box that had to reach Let's Encrypt to encrypt its own
internal traffic would be a box whose internal traffic depended on the public
internet.

**The edge does not implement an ACME client, and that is a decision with a
reason.** A client that talks to Let's Encrypt is a client that needs an account
key, a nonce, a challenge lifecycle and a rate limit; cert-manager already does
the minting on the launch box. All three modes end up as PEM on disk, and the
edge's job is to serve what was issued and to say honestly — by name — when
nothing was: `api.cafaye.test has no certificate in …/certs` rather than
"TLS is broken".

### The one piece of state the edge owns

`edge.json` under the box's state directory (`/var/lib/cafe/edge/` on a box,
injected in a test). It is small on purpose — every field is a field two
commands have to agree about — and complete, because a routing fact the spec did
not carry would be a routing fact kept somewhere else, and the somewhere else is
what this type exists to make unnecessary:

```json
{
  "version": 1, "box": "box-01", "mode": "cloudflare-origin",
  "current": "sample-stack-2", "previous": "sample-stack-1",
  "hosts": [
    {"name":"api.cafaye.test","upstream":"127.0.0.1:8080","service":"sample-stack","health":"healthy"}
  ],
  "updated": "2026-10-12T…"
}
```

**The `current` symlink is the truth and the spec's `Current` is the record.**
`edge.Current(dir)` reads the link; `edge.Load(dir)` reads the record.
`cafe box edge` reports both, because the one state where they disagree is a
deploy that did not finish — and that is worth saying out loud rather than
resolving silently in either direction.

### The three verbs

| Verb | What it does | What it does NOT do |
|---|---|---|
| `cafe box publish <id> <dir>` | writes the release, flips `current` at it, writes the record last | does not talk to box, needs no capability |
| `cafe box edge [--json]` | reports hostnames, upstreams, observed health, and the release behind `current` | does not start a daemon, does not probe on demand |
| `cafe box rollback` | flips `current` back one release | does not ask the control plane anything |

## 3. The failure-mode library entry the gate records

The eleventh entry in `internal/failure`, `edge-down-while-every-readyz-is-green`:

> **Failure:** the box's edge is down and every service's `/readyz` still answers 200
> **Sees:** all services healthy, heartbeat green, nothing to page about
> **Known:** the edge is not a service, so nothing in the substrate's own health checks it. A box whose edge has gone answers `/readyz` for every app from inside the box, and the control-plane heartbeat is a different channel entirely (`internal/boxd/client.go:15-24` is an outbound mTLS connection that survives an edge that is not listening). The edge is the only thing between a customer and every hostname the box serves.
> **Remedy:** probe the edge's own health path from OUTSIDE the box — `https://api.<box>/healthz` — and read the routing record it answers with. Nothing inside the box can substitute for it: a `/readyz` answered by an app behind a dead edge is a fact about the app.

**The symmetric counterpart is the mirror failure, and it is in the entry.**
The edge answers 200 for a hostname whose upstream it has already observed
unhealthy, so **an edge that is UP is not evidence that anything behind it is.**
That is why the entry gets a second predicate — `edge-host-unhealthy` — over the
same `Edge` observation field, and why the test drives a control rule through it
so the shape is proven rather than described.

The rule's condition is data and today it answers **UNKNOWN and names `Edge`**,
which is the honest answer and also the useful one: it is the specification of
what the substrate packet owes this library, the same contract the other ten
have with `internal/boxd`'s observation surface.

## 4. Verification — the edge, proven locally

**The edge gateway is exercised for real inside the box**, not a unit stub of it.
Seven tests in `internal/edge` dial a real loopback listener through a real TLS
handshake with a real SNI name:

- `api.cafaye.test` and `auth.cafaye.test` both answer `/healthz` (the edge's
  own path: box, mode, current release, and per-host observed health) AND
  `/readyz` (the app's, proxied through the same handshake) — the distinction
  between the two paths is the entry's whole argument, so both are exercised.
- The app sees the hostname the client asked for, not the upstream's address:
  asserted through the wire.
- `identity.svc.cafaye.com` answers over a wildcard the box minted itself.
- The publish/rollback test serves the release directory through the `current`
  symlink and asserts a publish and a rollback change what the customer gets
  **with the routing record untouched**.
- `TestTheSwitchIsAtomicForARequestInFlight` starts a slow request, publishes a
  whole second release into the gap, and asserts the in-flight request completed
  on the release it started on. Zero downtime is a property of `rename(2)`, so it
  is observed rather than asserted.
- `TestARetiredHostIsRefusedAndTheControlProvesIt` has its own negative control:
  the row restored and the same request succeeding. A test that only asserted the
  404 would also pass against a gateway that refused everything.

Five more in `internal/cli` drive the three verbs against a real filesystem with
a state directory injected — no box, no daemon, no network.

```
ok  github.com/cafaye/cafe/internal/edge   0.978s
ok  github.com/cafaye/cafe/internal/cli
ok  github.com/cafaye/cafe/internal/failure
cafe: 29 packages, 1270 top-level passes, 1234 subtest passes, 0 failures, 8 skips
```

## 5. Two defects the tests found, and what they were

### The probe loop and a deploy are two writers of one record

`edge.Save` originally used a fixed temporary name. The gateway's probe loop
writes the health it observed while a deploy may be writing a new release, and
**two writers sharing one temporary path is two writers that can delete each
other's bytes**. The failure was `rename … no such file or directory`, which
looked like a filesystem problem and was a concurrency problem.

The fix is two parts and both are load-bearing:

1. **The temporary name is unique per CALL, not per process** — it comes from
   `os.CreateTemp`. Two goroutines in one process are two writers, which is what
   a pid-suffixed name gets wrong.
2. **`probeAll` re-reads the spec before it writes**, and merges the health into
   the record that is on disk *now*. Without the re-read, a probe would write
   back a spec whose `Current` release the deploy had already moved — a rollback
   that returned to the wrong release.

### The routing decision read the deploy's guess instead of the edge's observation

`ServeHTTP` refused a request when the spec's `Host.Health` was
`HealthUnhealthy`. But a deploy writes `HealthUnknown`, and nothing ever wrote
unhealthy — so the guard was dead code in the direction that mattered and live
code in the direction that did not. The decision now reads
`g.observedHealth(...)`: the live observation if there is one, the record's own
health if there is not. `HealthUnknown` is nobody's measurement, so a row nobody
has measured yet is **routed to** rather than refused.

## 6. What this packet does not do

Stated rather than left for a successor to discover:

- **No ACME client.** The edge serves what was issued and the substrate performs
  the challenge. `internal/tls.go`'s `CertDirName` comment carries the argument.
- **No path routing.** One box, many hostnames, one row each. A route that needed
  a path would be a route that needed two boxes to agree about their URL layout.
- **No `boxd` route for the edge yet.** The whitelist stays where it is
  (`internal/boxd/route.go`); wiring `edge.status` into it is a one-row change
  that belongs with the substrate packet that can supply the `Edge` observation
  fact.
- **The three verbs have no `--state-dir` flag.** They default to
  `/var/lib/cafe`, which is where boxd runs from (it runs as root, per
  `CAFAYE-OPS.md:478`). A test injects the directory through `boxDeps.stateDir`.
- **`internal/edge` is not yet the daemon's.** It is the package the daemon will
  embed; the listener is started by `Gateway.Serve`, and nothing starts it in
  production yet.

## 7. The gate-declaration self-test: three failures, all pre-existing

`tests/gate-declaration-self-test.sh` is the control over the control — it
breaks gate.yml one way at a time and asserts core's checker goes red and
*names* the finding. It is not part of `bin/prime` and AGENTS.md says to run it
before landing a change to `gate.yml`. I ran it, and **three cases fail**:

```
FAIL the proving control — the real bin/prime ran and every declared proof appeared at or above its floor
FAIL the stand-in control — the stub gate satisfies the same declaration the real one does
FAIL a gate that proves fewer tests than the declaration promised — went red as something else and never said gate.floor
```

The third names the defect the other two trip over:

```
FAIL gate.proof-invalid: proof 'suite' sets a minimum of 1272 and its pattern
has 4 capture group(s); a floor is read from exactly one
```

**I verified this is not mine.** I extracted the base commit (`275f9d7`) into a
scratch directory with `git archive`, ran the same script there, and got the
same three failures with the base floor of 1259:

```
FAIL gate.proof-invalid: proof 'suite' sets a minimum of 1259 and its pattern has 4 capture group(s)
FAIL [the same two controls]
```

So the `suite` proof — and the `subtests` proof, which has the same shape — has
carried four capture groups against a `minimum` since before this packet, and
core's proving half only surfaces it when a gate's output goes *below* its
floor. On a green gate the static half passes, which is why nobody has seen it.

**I did not fix it, and the reason is the honest one rather than the
convenient one.** Fixing it means changing the `suite` proof's `match` pattern
to exactly one capture group, and that pattern is load-bearing from three other
sides: `internal/ci`'s `TestTheGateReportsItsOwnCounts` pins the summary line's
shape, the self-test's breakage 17 ("a floor with no capture group to read it
from") needs a pattern it can strip a group out of, and `bin/prime`'s own
accounting prints the line the three floors are read from. A change to that
pattern in the last quarter of a packet, with one gate run behind it, is the
kind of change that goes red in somebody else's merge. **It is written down
here instead, and it is the first thing a successor should do.**

Note also what the self-test's own comment says about the `floor` case: it
cannot use the real `bin/prime`, because `TestTheGateFloorIsNotAWall` lives in
the tree and a copy whose gate.yml says a huge minimum goes red on it, so the
summary line reads `1 failures` and all three floor patterns stop matching.
That is the ratchet working, and it is why the proving control is worth
running.

## 8. Housekeeping

- `gate.yml`: all three floors moved — packages 28 → 29, suite 1259 → 1270,
  subtests unchanged at 1234. The floor's comment names which tests this packet
  added and where they live.
- `caf.lock` re-pinned in the same commit: `gate.yml` is the tree's one
  `rule-bundle`, so a floor that moved without the pin moving is the defect the
  lock exists to catch.
- `internal/failure`'s doc.go, condition.go's vocabulary comment (twelve →
  fourteen predicates) and `Report.Describe` (which now says how many
  conditions were evaluated and did not hold, because a report that renders only
  two of its three verdicts makes the third invisible).
- README's command table: three rows added, `cafe failure check`'s row corrected
  from ten documented conditions to eleven.
- AGENTS.md: `internal/edge` in Layout, its own prose block after `internal/boxd`,
  and the dependency paragraph's claim that the stdlib-only set grew by one.

## 9. First move for a successor

Two things, in this order:

1. **Fix the pre-existing `gate.proof-invalid`** on the `suite` and `subtests`
   proofs (see §7). Both patterns carry four capture groups against a
   `minimum`; core's checker reads a floor from exactly one. Fix the pattern,
   then run `tests/gate-declaration-self-test.sh` AND `gate-check --prove`,
   because the static half alone will not tell you the fix worked.
2. **Wire `edge.status` into `internal/boxd`'s whitelist** and supply the `Edge`
   fact from `BoxObserver`, so `cafe failure check` answers the eleventh entry
   rather than naming it unknown. The second needs a `World` method returning
   the edge's own health document — which carries no certificate and no key, so
   the load-bearing `secret_test.go` walk passes over it unchanged.

And one thing not to do: do not add an ACME client to `internal/edge`. The
edge serves what was issued and the substrate performs the challenge;
`tls.go`'s `CertDirName` comment carries the argument, and a client that talks
to Let's Encrypt needs an account key on a customer's box, which is a
credential a box compromise spends.
