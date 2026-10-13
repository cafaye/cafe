# roll-c4-cert-consume — the box writes its certificate to disk, and the daemon's identity names it

**Branch:** `roll/c4-cert-consume` (worktree `/Users/kaka/Code/any/moon/wt/roll-c4-cert-consume`)
**Base:** `ec96ac0` (c4 gate-ergonomics landed). Tree was clean at dispatch — no partial work to recover.
**State:** packet accomplished; `bin/prime` green.

## What it does, in one paragraph

A `box.enroll` redemption now carries the client certificate box issues out of
`Redeem` in a value whose private half is unexported, so no JSON document can
marshal it. Enrollment writes `box.crt` (0644) and `box.key` (0600) beside
`box.json` and records their **relative** names on the identity record. Setup's
daemon-identity step fills the daemon document's `certificate`/`key` fields with
those names and copies the two files **on the host** into the document's
directory — the directory the daemon's `resolve` joins against. A redemption
that returns no certificate (or a journal recovery, where the response is gone)
finishes with an honest identity that names none and says so; the daemon then
reports the credential by its own name rather than retrying in silence.

## The edits

1. **`internal/box/control_plane.go`** — `controlPlaneResponse` decodes
   `certificate`, `certificate_key`, `certificate_expires_at`,
   `certificate_fingerprint`. `Redeem` now returns
   `(*Installation, *ClientCertificate, error)`. `ClientCertificate` holds the
   public cert/expiry/fingerprint as exported fields and the **private key in an
   unexported field**, so `encoding/json` cannot marshal it; `String`/`GoString`
   redact it so `%v`/`%+v`/`%#v` cannot leak it either. A response with exactly
   one half of the pair is refused by name; no certificate at all is `nil`, not
   an error. An exported `NewClientCertificate` exists so out-of-package tests
   (the CLI fake) can simulate issuance.

2. **`internal/box/store.go`** — `finish(jr, cert)` writes the two files
   (`writeFile` grew a mode parameter; key 0600, cert 0644) **before** the
   identity that names them, then records `CertificateFileName`/`PrivateKeyFileName`
   as relative names. `EnrollResult` gained `certificate_stored` (a bool, never
   the credential) and a message that names the no-certificate case. The recovery
   branch calls `finish(*jr, nil)`.

3. **`internal/setup/plan.go`** — `identityRead` and `enrollment` carry the two
   relative names; `identityDocument` fills `certificate`/`key` (omitempty);
   `placeCertificateCommand` builds the host-side copy. The identity step's
   `apply` now carries that command.

4. **`internal/setup/setup.go`** (a deviation — see below) — the walk builds the
   daemon identity from the **post-enrollment** answer, not the pre-walk read.

## Deviations from the literal spec, with reasons at the code site

- **Setup copies the cert/key to the daemon document's directory.** The spec's
  edit 3 said only to fill the JSON fields. But enrollment writes to root's
  config directory (`/root/.config/cafe`) while the daemon reads
  `/etc/cafe/box-identity.json`, and a relative name is meaningful only against
  the *reader's* directory. The copy is a host-side `install` (two files, one
  `&&`), deliberately **not** two `Write`s from the controller: the private key
  must not cross the seam as a value, so the controller holds the two NAMES and
  only the host touches the bytes. See `placeCertificateCommand`'s comment.
- **`internal/setup/setup.go` is edited** (not in the listed three). The daemon
  document was built when the plan was constructed, i.e. before the enrollment
  step ran, so on a **fresh** box it carried an empty `box_id` and could not have
  known the certificate paths at all. The walk now rebuilds the document (and the
  copy command) from the host's own post-enrollment answer. This also fixes a
  latent first-run bug the e2e sim never caught because it returns a canned
  heartbeat. Named in the comment at the step.
- **Recovery finishes without a certificate** (spec point 5, the decision). The
  private half appears exactly once and box stores none, so it cannot be
  re-minted without a new capability; the recovery produces an honest identity
  and says re-enroll. The daemon already reports an identity with no certificate
  as `boxd.ErrNoClientCredential`. Both are tested.
- **`TestTheDaemonIdentityDocumentIsTheHonestStub` was renamed** to
  `TestTheDaemonIdentityDocumentNamesTheCertificateBesideIt` (same count), because
  it no longer pins a certificate-less stub.
- **Test files owned by the sibling packet were touched** (`internal/cli/box_test.go`,
  `box_setup_test.go`, `boxd_test.go` — fakes + one stale comment). The fakes had
  to change because `Redeem`'s signature did; the sibling's registration/wipe
  source files were not touched. If a conflict lands, the manager resolves it.
- **A new file, `internal/box/control_plane_test.go`**, tests the wire decode
  (which had no HTTP-level test before).

## Tests

- `internal/box/control_plane_test.go` (new, 3): the response carries the cert
  out and marshalling it drops the key; no cert → `nil`; half a pair → refused
  by name.
- `internal/box/store_test.go` (+4): files land with modes 0644/0600 and the
  identity names relative paths; the private key reaches **no file but box.key**
  (journal and box.json included, by bytes), no printable document, and no
  `%v`/`%+v`/`%#v` rendering; a redemption with no cert stays honest; a recovered
  enrollment finishes without a cert and says so.
- `internal/setup/setup_test.go`: the bare-host e2e asserts the daemon identity
  names the cert/key and that both files are placed beside it with 0644/0600;
  the renamed document test asserts relative names and the omitempty no-cert
  shape.
- `internal/cli/box_setup_test.go`: the end-to-end setup e2e now plants a cert,
  runs a host sim that performs the `install` copy, and asserts the placed modes
  and that neither the setup document nor the daemon identity carries the key.

## The gate

`bin/prime` (keyed to the worktree, exit captured):

```
cafe: 33 packages, 1387 top-level passes, 1250 subtest passes, 0 failures, 8 skips
cafe: live tier: 0 of 7 executed; not enabled, so the 7 live tests skipped rather than ran.
exit: 0
```

`cafaye/core/harness/bin/gate-check .` → `0 failure(s), 3 warning(s)` (the three
are the expected `gate.requirement-unproven` warnings for the external
toolchain/network/docker requirements).

Floors moved **1380 → 1387** via `cafe gate floors --write` (packages 33,
subtests 1250 unchanged), and `caf.lock` was re-pinned in the same commit, per
the house rule. `go vet ./...` clean; `gofmt -l .` clean.

## What I deliberately did NOT do

- Did not put the private key anywhere printable: not in `box.json`, the journal,
  `EnrollResult`, `setup.Result`, a log line, argv, or an environment variable.
  The copy carries paths in argv, never bytes.
- Did not make the controller read the key: the `install` copy runs on the host.
- Did not change `IdentityPath`: `/etc/cafe/box-identity.json` is a deliberate
  system-location decision in `internal/setup/plan.go`, and the copy preserves it
  rather than reversing it.
- Did not touch `cafaye/box` or any other repository, and did not merge or push.
- Did not run the live Contabo box: the daemon will now find a certificate on a
  freshly set-up box, but confirming that on the live box is the manager's.

## The successor's first move

Nothing is outstanding for the packet. If a follow-up is wanted: the identity
step's check is `test -s /etc/cafe/box-identity.json`, so a box whose cert files
were deleted but whose document survives will not repair itself on a re-run —
a check that also names the two files would close that. It was left out because
the check is `plan.go`'s and broadening it would re-run a host copy on a converged
box for a state no command produces today.
