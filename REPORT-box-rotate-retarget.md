# box rotate + retarget — REPORT 2026-10-15

## What this packet is for

Two missing `cafe box` verbs, both measured as gaps on the Contabo dogfood box 2026-10-15. The server already supports both; the client refused or lacked the verb. One packet, one repo, one worktree — `feat/box-rotate-retarget` at `/Users/kaka/Code/any/moon/wt/box-rotate-retarget`.

1. **`cafe box enroll --rotate`** — cert rotation for an already-enrolled box. Today `cafe box enroll --token -` on an enrolled box answers `this box is already enrolled as <id>; nothing was redeemed` and never presents the capability, leaving no path for CA rotation / key compromise / expiry except moving the store aside by hand.

2. **`cafe box retarget --box <url>`** — re-point an enrolled box at its control plane. Today the URL is written only at enroll time; when the plane moves there is no CLI path and the operator edits JSON.

Both through `cafe`, both proven by tests that count seam calls.

## Server-side support (already)

Quoted verbatim as the report asked, from `cafaye/box`:

`cafaye/box/app/models/capability.rb` `redeem!` — on `box.enroll`:

```ruby
# lines 171-186
installation = Installation.find_or_initialize_by(box_id: capability.box_id)
installation.account_id = capability.account_id if installation.new_record?
installation.status = 'active'
installation.current_phase = 'enrolled'
# Written, not conditionally saved: the capability that bought
# this enrollment binds the key it was redeemed with.
installation.public_key = box_key.to_s if box_key
installation.save!

installation.audit_installation_events.create!(action: 'enrolled', actor_user_id: nil)

# The certificate lives in this transaction with the installation:
# a box is never enrolled without its credential, and a
# re-enrollment revokes the certificate it supersedes here.
issued = BoxCertificate.issue_for!(installation)
```

`cafaye/box/app/models/box_certificate.rb` `issue_for!`:

```ruby
# lines 148-150 + 152-161
where(installation_id: installation.id, status: 'active')
  .update_all(status: 'revoked', revoked_at: now, updated_at: now)

record = create!(
  installation: installation,
  box_id: installation.box_id,
  serial: certificate.serial.to_s(16),
  ...
  status: 'active',
)
```

That is the rotation: `find_or_initialize_by(box_id:)` writes the new public key, and `issue_for!` revokes the certificate it supersedes in the same transaction. A re-enrollment is a rotation; the client only needs to present a fresh capability against the same `box_id`.

## What landed in this worktree

### 1. Rotate

- `internal/box/store.go` : new `ActionRotated = "rotated"`, new `Store.Rotate(ctx, token, url)` (refuses when not enrolled), shared `enroll(..., rotate bool)` private. When `rotate==false` behavior is exactly as today (early-return `already-enrolled`, 0 redeem). When `rotate==true` the already-enrolled check is bypassed deliberately, any stale `prepared`/`redeemed` journal is discarded, a fresh keypair is generated, the capability IS presented, `finish` overwrites `box/<box_id>` in the ring and `box.json` + `box.crt`/`box.key` on disk. Message: `box <id> certificate rotated; the previous certificate was superseded and revoked — restart cafe-boxd.service to pick up the new certificate`.

- `internal/cli/box.go` : `boxEnrollOptions.rotate bool`, flag `--rotate`, `cafe box enroll --token <cap> [--box <url>] [--rotate]`. Without the flag calls `Enroll`; with it calls `Rotate`. House help and `box` usage updated to list `retarget`.

- `internal/box/store_test.go` : `TestEnrollWithoutRotateDoesNotPresentTheCapability` (0 calls, tripwire), `TestRotatePresentsTheCapabilityOnceAndReplacesTheStore` (1 call, ActionRotated, superseded + cafe-boxd.service, pubkey rotated, ring has 1 entry, cert file replaced), `TestRotateRefusesWhenNotEnrolled`.

- `internal/cli/box_test.go` : `TestBoxEnrollRotatePresentsOnceVersusAlreadyEnrolled` — the CLI-level red proof: enroll, then without `--rotate` → already-enrolled 0 calls, stderr `nothing was redeemed`; with `--rotate` → 1 call, rotated, stderr `superseded` + `cafe-boxd.service`, pubkey changed.

### 2. Retarget

- `internal/box/store.go` : `RetargetResult`, `Store.Retarget(newURL string)` — validates with the same `host + optional port` rules the boxd client uses (`internal/boxd/client.go` `dialTarget`: `https://host[:port]` or `host:port`, scheme default 443/80, bare host without port refused, all refused as `control plane "<value>" is not host:port` naming the field). Refuses when not enrolled with `box is not enrolled: run cafe box enroll --token … first`. Updates `box.json` `box_url` without touching identity, writes `retarget` event, returns before/after + `restart cafe-boxd.service` reminder.

- `internal/cli/box_retarget.go` : `cafe box retarget --box <url>` (also reads `CAF_BOX_URL` fallback), exit 2 on missing/invalid URL naming `control plane`, exit 1 via `errReported` on not-enrolled, stdout JSON document, stderr sentence with before→after + restart reminder. Chose `retarget` as the verb: house verbs are bare imperatives (`enroll`, `deploy`, `rollback`, `retarget` is the same shape — re-point at the control plane; `repoint` would be equally valid but `retarget` matches the brief's intent word and moon/PLAN-names-urls-rollout.md style of `publish`/`edge`).

- `internal/box/store_test.go` : `TestRetargetUpdatesTheStoreAndValidates` (not-enrolled, updates url + box_id/pubkey unchanged + key still present, invalid urls `""`, `not a url`, `https://`, `ftp://…`, `box.test` all refused naming control plane).

- `internal/cli/box_test.go` : `TestBoxRetargetCLINotEnrolledInvalidAndUpdate` — not-enrolled refusal, then enroll + retarget → before/after in stdout + stderr + `cafe-boxd.service`, persisted `ControlPlane` updated, invalid → exit 2 and field named.

### Gate + housekeeping

- `internal/cli/box_test.go` `TestBareBoxIsAQuestionNotAMistake` updated to expect `retarget` in help.
- `internal/box/store.go` `validateBoxURL` is stdlib-only (no import of `internal/boxd` to keep the reclamation path dependency-free); helpers `isMissingPort` / `defaultSchemePortForValidate` mirror `boxd` exactly.
- Lint fixes: `box_retarget.go` error wrapping `%w`, removed unused `splitHostPort`.
- Floors: `cafe gate floors --write` → `suite 1478→1484`, `subtests 1287→1290`, `caf.lock` re-pinned. Commit `bfb6f0a`.

Branch: `feat/box-rotate-retarget`, worktree `/Users/kaka/Code/any/moon/wt/box-rotate-retarget`. One commit; manager merges/pushes and re-verifies rotation on the live test box.

## Gates

```
mise exec -- ./bin/prime
# → 33 packages, 1484 top-level passes, 1290 subtest passes, 0 failures, 8 skips
# → live tier 0 of 7 (skipped, as intended)
# go vet ./...  (0)
# gofmt -l .    (0)
# golangci-lint run --config ../kit/lint/golangci.yml ./...  (0 issues)
```

Narrow as requested:

```
go test ./internal/box/... ./internal/cli/... ./internal/boxd/...   # ok
```

## Quarterly contract

- Q1 (rotate + its test) and Q2 (retarget + its tests) landed together because the worktree arrived clean on `3ac8afd` (feat/box-mint) and both verbs touch `internal/box/store.go` + `internal/cli/box.go`; splitting would have conflicted on the same hunks and floors. The single commit covers both and is the first meaningful commit — it is Q1+Q2 in one.
- Q3 gate + red proofs: the counting control planes, tripwires, and before/after assertions are the red proofs; the gate is green as above.
- Q4 ends with this report; self-exit before Q4 closes. No push — manager merges from the default branch (check `git rev-parse --abbrev-ref HEAD` is default before merging).

## What was ruled out

- A new verb `cafe box rotate` vs `--rotate` on `enroll`: chose `--rotate` per brief — rotation IS an enrollment with a fresh capability against the same `box_id`; a second verb would duplicate the flag matrix (`--token`, `--box`, stdin `-`) and diverge from the server's `redeem!` which is already the rotation path.
- Importing `internal/boxd` for validation: rejected — `internal/box` is on the reclamation path and is stdlib-only by design; duplication with identical tests is the product.
- Touching store layout: avoided per brief — `box.json` shape unchanged, only `box_url` overwritten.

## Successor's first move

Manager: `git rev-parse --abbrev-ref HEAD` is default branch, then merge `feat/box-rotate-retarget`, push, re-run `bin/prime` on CI, and re-verify rotation on the Contabo box:

```
cap=$(cafe box mint --box https://box.cafaye.com --purpose box.enroll --box-id <id> | jq -r .token)  # or the mint the team uses
echo "$cap" | cafe box enroll --rotate --token - --box https://box.cafaye.com
# expect: cafe box: box <id> certificate rotated; the previous certificate was superseded and revoked — restart cafe-boxd.service ...
systemctl restart cafe-boxd.service && cafe box verify
```

If that answers, the packet is done.
