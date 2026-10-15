# REPORT — boxd-store-identity

**Packet:** one identity source for boxd. The daemon read its identity from
`CAF_BOX_IDENTITY` (the systemd unit pointed it at
`/etc/cafe/box-identity.json`) while the box's store — written by enroll,
migrated, owned by the box — lives at `/var/lib/cafe/box/`. Two sources of
truth; the daemon read the wrong one.

**Branch:** `feat/boxd-store-identity` (worktree
`/Users/kaka/Code/any/moon/wt/boxd-store-identity`, branch HEAD `d9a66c9`).
Only this worktree was used; no other repository was touched.

**Status:** done. `mise exec -- ./bin/prime` is green (exit 0), commit
`d9a66c9`, red proof captured.

---

## 1. The measured defect this fixes

Live on Contabo 2026-10-15: a re-enroll wrote the new keypair + certificate to
the store, and `boxd` kept presenting the OLD certificate from `/etc/cafe/`
(server: `x509: unknown certificate authority`). The manager fixed it by
hand-copying files. The cause is mechanical: `internal/cli/boxd.go` resolved the
identity ONLY from `-identity` / `$CAF_BOX_IDENTITY` / `~/.config/cafe/box-identity.json`
and never looked at the store the enroll verbs actually write.

## 2. The change

- `internal/cli/boxd.go`
  - New `resolveBoxdIdentity(flagPath, version)` returns a `boxdIdentity`, a
    **pair** of seams (the `boxd.IdentityStore` to dial as and the
    `boxd.KeySource` to dial with) resolved together. That pairing is the
    fix: a box can no longer dial as one source's identity while presenting
    another source's certificate.
  - Precedence: explicit `-identity <path>` **>** the box-owned store
    (`$CAF_BOX_DIR`, else `/var/lib/cafe/box`) **>** `$CAF_BOX_IDENTITY` **>**
    `~/.config/cafe/box-identity.json`.
  - Both present (store enrolled + `$CAF_BOX_IDENTITY` set): the **store
    wins**, and one log line names both paths
    (`the box store at <dir> is the source of truth; CAF_BOX_IDENTITY=<file> is
    also set and is ignored`), printed exactly once.
  - Env-file only: behaviour unchanged, plus one deprecation sentence naming
    the store (`CAF_BOX_IDENTITY=<file> is deprecated; the box store at <dir>
    is the source of truth`).
  - `boxWorld` now holds the resolved `boxd.KeySource` rather than an identity
    path, so the certificate an observation reports is the same one the
    handshake presents — store or file.
- `internal/box/store.go`
  - New `(*Store).ClientCertificate()` — the store's **own** accessor for the
    client credential it filed. It reads `box.json`, takes the relative names
    (`box.crt`, `box.key`) the store wrote, and resolves them against the store
    directory with the store's own `resolvePath`. The daemon does not
    re-derive those paths by hand.
  - New sentinel `box.ErrNoClientCredential` so "unenrolled" and "enrolled but
    no credential" stay distinct, and the CLI maps both onto
    `boxd.ErrNoClientCredential` (the daemon's permanent-failure bound).
- `README.md` — the `cafe boxd` command row and prose section.
- `gate.yml` + `caf.lock` — floors raised by the repo's own writer
  (`cafe gate floors --write`), suite 1484→1489, lock re-pinned.

### Precision on "the store's keyring"

The brief said "the certificate and key taken from the store's keyring". In the
code the store files the **client** credential as two files beside `box.json`
(`box.crt` 0644, `box.key` 0600 — `Store.writeClientCertificate`); the ed25519
*enrollment* key lives in the OS/keyring seam (`Store.BoxKey`), and the daemon
does not use it. So the store's own accessor is `ClientCertificate()` over
those two files — same source of truth, different storage than the brief's
word "keyring". Flagging it so a successor does not go looking for a
certificate that was never in the ring.

## 3. Tests

`internal/cli/boxd_store_test.go` — each case discriminates by the
certificate's **subject**, not by a path, so a build that read the wrong source
fails on the credential:

- **(a)** `TestABoxdOnAStoreEnrolledBoxUsesTheStoresCertificate` — no
  `CAF_BOX_IDENTITY`; the store's box id is loaded and the store's certificate
  (CN `box-store`) is what the seam loads.
- **(b)** `TestTheStoreBeatsTheEnvFileAndSaysSoOnce` — both present, with
  *different* certificates (CN `box-store` vs `box-env`); the store wins, the
  notice names both paths, the line appears exactly once in a full one-round
  daemon's own log, and the injected dialer sees the store's box id.
- **(c)** `TestTheEnvFileStillWorksAndNamesItsDeprecation` — env-file only; the
  old path runs and one deprecation sentence names the file and the store.

`internal/box/client_certificate_test.go` — `ClientCertificate` loads what the
store filed (subject checked) and names the two absences distinctly
(`ErrNotEnrolled`, `ErrNoClientCredential`).

### Red proof

`go test ./internal/cli/... -run 'TestABoxdOnAStoreEnrolledBox|TestTheStoreBeatsTheEnvFile|TestTheEnvFileStillWorks'`
with the store-first branch temporarily disabled (`...Identity(); err == nil && false`):

```
=== RUN   TestABoxdOnAStoreEnrolledBoxUsesTheStoresCertificate
    boxd_store_test.go:47: a store-enrolled box resolved somewhere other than the store
--- FAIL: TestABoxdOnAStoreEnrolledBoxUsesTheStoresCertificate (0.00s)
=== RUN   TestTheStoreBeatsTheEnvFileAndSaysSoOnce
    boxd_store_test.go:89: both sources present and the env-file won; the store must win
--- FAIL: TestTheStoreBeatsTheEnvFileAndSaysSoOnce (0.00s)
=== RUN   TestTheEnvFileStillWorksAndNamesItsDeprecation
--- PASS: TestTheEnvFileStillWorksAndNamesItsDeprecation (0.00s)
```

(a) and (b) go red; (c) stays green, which is correct — the env-only path is
the behaviour that must be preserved. The branch was reverted before the gate.

### Gate

```
cafe: 33 packages, 1489 top-level passes, 1290 subtest passes, 0 failures, 8 skips
cafe: live tier: 0 of 7 executed; not enabled, so the 7 live tests skipped rather than ran.
```

`bin/prime` exit 0. `gofmt -l .` clean, `go vet ./...` clean.

## 4. What I ruled out

- **Not running the store migration inside `boxd`.** The verbs run
  `box.MigrateStore` on first defaulted use; I deliberately did **not** put it
  in the daemon start path. Migration MOVES files and REFUSES when both stores
  hold different identities (`ErrBoxStoreDiverged`), and a daemon that refuses
  to start because a stale legacy file exists would be a new way for the live
  box to go down. `boxd` only READS the store; a box not yet migrated still
  resolves through `$CAF_BOX_IDENTITY`, untouched. A successor who wants boxd
  to migrate should add it deliberately and test the diverged case.
- **The `-identity` flag still outranks the store.** It is the operator saying
  which file they mean, and every existing CLI test that drives a file identity
  depends on it. The brief's "both present" is the env-file, which the store
  does outrank.
- **`boxd.Identity.Version` is the running build, not an enrolled build.** The
  store's `box.json` (`box.Identity`) records no cafe version, so a store-sourced
  identity would otherwise report an empty `deployed_version` in
  `observe.report`. `storeIdentity` fills it from `env.Version.Semver` — which
  in practice IS the build setup installed. If a real per-enrollment version is
  wanted, the durable fix is a `version` field on `box.Identity` written by
  enroll; that is a larger change and was not attempted.
- **The daemon's own state path is unchanged.** A store-sourced box still
  defaults `boxd-state.json` to the process CWD (`boxdDeps.withDefaults`),
  exactly as before; only the `--report` path derives it beside the source. I
  did not "tidy" this because moving it would silently reset a live box's
  recorded install phase, which is out of scope for this packet.

## 5. The kit-side follow-up (the manager dispatches this)

**The unit does not live in kit.** I searched both kit checkouts — the main
`/Users/kaka/Code/any/moon/cafaye/kit` (master) and the worktree
`/Users/kaka/Code/any/moon/wt/kit` (master) — for `CAF_BOX_IDENTITY`,
`box-identity.json`, `*.service` and `*boxd*`. kit's substrate
(`templates/substrate/install.sh`, `templates/substrate/README.md`) mentions
`cafe boxd` only in comments about the OS trust store; it writes no boxd
systemd unit and carries no `Environment=` line. The unit is generated by
**cafe** itself.

The line the successor needs to drop is in
`internal/setup/plan.go`, `unitDocument`:

```
"Environment=CAF_BOX_IDENTITY=" + IdentityPath + "\n"
```

with `IdentityPath = "/etc/cafe/box-identity.json"`, i.e. the rendered unit
line today is exactly:

```
Environment=CAF_BOX_IDENTITY=/etc/cafe/box-identity.json
```

Two cafe-side steps make it safe to drop, and both are the same follow-up:

1. `unitDocument` stops emitting that `Environment=` line (or the whole unit is
   rewritten) once the store path is proven live on Contabo.
2. `StepBoxdIdentity` / `placeCertificateCommand` stop writing
   `/etc/cafe/box-identity.json` and copying `box.crt`/`box.key` into
   `/etc/cafe` (the artifact the line points at).
3. `StepBoxdReport`'s check (`env CAF_BOX_IDENTITY=` + IdentityPath + ` cafe boxd --report`)
   drops the now-pointless `env` prefix.

If there is a kit-side unit I could not see (a branch, or a checkout the
manager holds), the successor should re-run the search with the exact string
`Environment=CAF_BOX_IDENTITY=/etc/cafe/box-identity.json`; it exists only in
cafe today.

## 6. Successor's first move

If this packet is continued (rather than merged):

1. `git log -1` on `feat/boxd-store-identity`; expect `d9a66c9`. The tree is
   clean and the gate is green.
2. Read `resolveBoxdIdentity` in `internal/cli/boxd.go` — the precedence and
   the paired seams are the whole packet.
3. The only open live question is the Contabo verification (manager): rebuild
   the binary with this commit, restart `cafe-boxd.service`, confirm the daemon
   logs `the box store at /var/lib/cafe/box is the source of truth` and dials
   with the store's certificate instead of the `/etc/cafe` copy. Nothing here
   needs the network to be proven in-suite; the wire proof is
   `internal/boxd/tls_test.go` plus these resolution tests.
