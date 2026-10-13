# REPORT — roll-c3-boxd-identity: the box daemon's missing client credential

**Branch:** `roll/c3-boxd-identity` · **Commit:** `52cc96d` · **Date:** 2026-10-13
**Previous attempt:** died mid-work (endpoint failure, not the packet's fault). Its
uncommitted `internal/boxd/daemon.go` — the `ErrNoClientCredential` sentinel, the
`PermanentRounds` bound, the `refuse()` exit — was judged **correct and kept**;
this packet finished the wiring around it, the docs, and the tests.

## The decision, stated plainly

**(b), a documented deferral — with the truthful-state half landed now.**

The box daemon's client certificate does not exist and nothing in the fleet
issues one. The daemon is now either **connecting** or **honestly refusing** —
never the silent 30-second retry loop that made `systemctl is-active` say
"active" over a daemon that could do nothing (verified live on the Contabo box:
the 107-byte stub identity, `is-active` = `active`, the same error every 30s).

### Why not (a), enrollment-time issuance, this hour

(a) is the right **end state** and what the daemon was built for, but it cannot
land in this repository: it requires `cafaye/box` (Rails) to run a signing
endpoint and a CA the box trusts — a model, a migration, and contract work in a
a different repo this packet must not edit. The brief's own escape clause
applies: "if only part fits, land the truthful-state half (bounded retries +
surfacing) and write the rest as a precise handoff." The handoff is §4 below.

### Why the naive fixes inside cafe are rejected

- **Self-signed certificates** are authentication theater: anyone can mint one,
  so the daemon would connect without the box being any more authenticated than
  with no certificate — cryptography without authentication, and a
  credential-shaped lie in the identity document.
- **Connecting with no client certificate** weakens the model to match a server
  that does not enforce it, and breaks again the day the server starts.
- Both make the box's state **untruthful**, which is the defect this packet
  exists to end. The Phase B transport is the operator's SSH reverse tunnel (the
  identity names `http://127.0.0.1:4003` — loopback — and SSH already
  authenticates that channel), so the tunnel is not the gap; the gap is that no
  credential rides it yet.

### What landed

1. **The bound and the surfacing** (daemon's `Run`): a round that fails with
   `ErrNoClientCredential` is permanent — waiting cannot fix it. After
   `PermanentRounds` (default **3**) consecutive permanent rounds, `Run` stops
   and returns a refusal naming the missing fact and the packet that fixes it.
   The daemon exits **non-zero**, which is the systemd-visible failure state:
   the reason lands in the unit's journal and `is-active` stops saying "running"
   over a daemon that can do nothing. Transient failures (down control plane,
   dropped connection) keep the backoff-and-retry loop — the box's whole job is
   to be reachable, and a bound that fired on transient errors would be a daemon
   the supervisor has to babysit. The crash-loop-after-refusal tradeoff is
   argued in `internal/boxd/doc.go`: the loop *is* the alarm; the silence was
   the defect.
2. **The wiring**: `internal/cli/boxd.go`'s `fileIdentityStore.Certificate` now
   wraps `boxd.ErrNoClientCredential` when the identity names no certificate —
   the missing link that makes the live box's error classify as permanent.
3. **The honest observation**: `healthStatus` treats an absent certificate as a
   degraded reason ("the box has no client certificate and cannot dial box: …").
   Before, `cafe boxd --report` answered "healthy" over a daemon that could not
   connect — a green light over the exact defect.
4. **The verification carries the fact**: `internal/verify` adds a
   `certificate_absent` reason (guarded by `answered()` so the unread box's
   zero-value certificate stays a blind spot, not a fact), with the person's
   next line attached. The expired/expiring remedy no longer falsely claims
   `cafe box d enroll` issues a certificate.
5. **The decision is written** in `internal/boxd/doc.go` (the daemon's own
   header, where the next reader finds it), with the argument for and against
   each option and the **Phase C trigger**: the packet that lands
   client-credential issuance in `cafaye/box`. At that point enrollment writes
   the real certificate and key paths into the identity document and the
   daemon's dialer works **unchanged** — nothing here needs to know which of the
   two Phase C shapes was chosen.

### Tests that fail on the tree as found (proven, not asserted)

Dropped into a `b6d507d` worktree (and, for the wiring test, the tree as found —
sentinel defined, CLI store unwrapped), each fails; each passes on `52cc96d`:

- `TestACertificateThatDoesNotExistIsADegradedFactWithANextLine` (verify) —
  fails: `reasons = [health_degraded], want certificate_absent`.
- `TestHealthStatusNamesEveryDegradedReason` (cli) — fails: the unknown-certificate
  row verified "healthy".
- `TestTheIdentityWithoutACertificateIsAPermanentRefusal` (cli) — fails: the
  store's error did not wrap the sentinel.
- `TestTheDaemonRefusesAfterABoundOfPermanentFailures` (boxd) — fails: the bound
  was not wired to the store, so the live error never reached it.
- Controls that pass on both: transient failures keep retrying past the bound;
  `Open` defaults the bound to 3; once-mode refuses after one round; the setup
  test pins the honest stub (the identity document the one-command path writes
  names no certificate — deliberately, until Phase C fills it).

## What is half-done, and why

- **The daemon does not connect on the live box, and will not until Phase C.**
  What is truthful now: after ~1.5s of backoff it exits non-zero with the
  refusal, the journal carries it, `--report` says the certificate is unknown,
  and `cafe box verify` reports the box degraded with `certificate_absent`.
  The live box's unit will show the refusal in its loop — loud, dated, and
  actionable, which is the packet's definition of done for the truthful half.
- **The issuance path itself** (CA, signing endpoint, enroll response) is the
  `cafaye/box` work in §4. Nothing here is stubbed toward it: the identity
  document's `certificate`/`key` fields, the store's loader, and the mTLS
  dialer are already the shape issuance writes into.

## The exact `cafaye/box` changes required (Phase C — do not edit from here)

Recommended shape: **(a) enrollment-time issuance**, because it survives tunnel
restarts and matches what the daemon was built for. (b)'s key-bearer endpoint
over the tunnel is the fallback if the control plane decides against a CA.

1. **Migration**: `create_table :box_certificates` — `id` (bigint pk),
   `installation_id` (fk, indexed), `box_id` (string), `certificate_pem`
   (text), `private_key_pem` (text, encrypted at rest — see note), `fingerprint`
   (string, indexed), `status` (string: active/revoked), `expires_at`
   (datetime), `issued_at`, `revoked_at`, timestamps.
2. **Model** `BoxCertificate` (or extend `Installation`): a CA keypair lives in
   Rails credentials (`box_ca_key`, `box_ca_cert` — generate once via
   `openssl req -x509 -newkey rsa:4096 -nodes -days 3650 -subj /CN=cafaye-box-ca`).
   Issuance = `x509::Certificate` with `ExtKeyUsageClientAuth`, CN = box_id,
   NotAfter ~1 year, signed by the CA.
3. **Endpoint** `POST /v1/boxes/certificate` (or extend
   `POST /v1/capabilities/redeem`): body carries the enrollment's `public_key`
   (the ed25519 key already sent at redeem — `internal/box/control_plane.go:39`
   says it is sent "so the enrollment binds a key box can later verify a
   signature from") **and** a signature over `{box_id, public_key, nonce}` made
   with that key. Box verifies the signature, mints the certificate, stores it,
   returns `{certificate, key, expires_at}` in the redemption response.
4. **Contract change**: `GET`/`POST /v1/capabilities/redeem`'s response gains
   `certificate`/`certificate_key` fields (PEM strings). cafe's
   `internal/box/control_plane.go` `controlPlaneResponse` decodes them;
   `internal/cli/boxd.go`'s `identityDocument` writes them as **relative paths**
   beside the identity document (the store resolves them relative to the
   document — `fileIdentityStore.resolve`). The daemon's `tlsDialer` then
   works unchanged.
5. **Trust**: the box trusts the control plane's TLS certificate as today
   (system pool); the client certificate is what the box *presents*. No change
   to `RootCAs` is needed unless the control plane's own cert also moves to the
   CA — out of scope.

## What I deliberately did NOT do

- Did not edit `cafaye/box` or any other repo.
- Did not self-sign certificates, connect with no client credential, or weaken
  the mTLS model to match a non-enforcing server (all argued in `doc.go`).
- Did not add a sleep-and-retry "fix"; the bound *is* the fix and it surfaces.
- Did not break the memory-lifetime rule: the box key is still loaded per
  connection (`tlsDialer.Dial` → `KeySource.Certificate`), never cached.
- Did not put key material in argv/env: the capability still travels on stdin
  (`--token -`), as `internal/setup/plan.go` already holds.
- Did not touch the daemon's wire protocol, route table, or `secret_test.go`'s
  no-Secret-shaped-value invariant.
- Did not change `gate.yml`'s floors: no new packages, and the test count is
  unchanged in kind (the gate's own floors still bind).

## Verification

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `gofmt -l .` — clean (note: `internal/box/control_plane.go` was **already
  unformatted at `b6d507d`**; aligned so the gate is green — a pre-existing
  condition this packet fixed, not introduced).
- Focused packages: `go test ./internal/boxd/... ./internal/cli/... ./internal/setup/... ./internal/verify/... ./internal/box/...` — all `ok`.
- Full gate: `./bin/prime` — see the run log for the suite totals (the
  three-floors accounting in `gate.yml` is unchanged).

## The successor's first move

Read `internal/boxd/doc.go`'s "The client credential, and why this daemon
refuses without it", then pick up the Phase C handoff in §4: land issuance in
`cafaye/box` (migration + model + endpoint + contract), have enrollment write
the real certificate/key paths, and confirm the live Contabo box's daemon
connects — the same `systemctl is-active` line that was the defect's symptom
becomes its proof.
