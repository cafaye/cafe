# REPORT — repair the cafe half of seams A and B (worktree: repair-seams-cafe)

Date: 2026-10-15. Branch `repair/seams-cafe`, three commits on top of `1b31e40`,
working tree clean. Manager merges and pushes.

## What landed

**Q1 — `58d06ea` — `cafe login --password-stdin` (seam B's fix).**
`internal/cli/login.go` gained a `--password-stdin` boolean flag following the
tree's own pipe convention (`cafe box enroll --token -`, box.go:304): the value
is read through the `readerOrStdin` seam, trimmed of the pipe's trailing
newline, and handed straight to the endpoint. Nothing about it is echoed,
logged, or carried into an error or report; the keyring write path is
untouched. Usage and LongHelp name the paste-safe form. The two channels are
mutually exclusive with a named usage error that names both flags — "they are
the same secret on two channels" — and an empty/blank pipe is a named usage
refusal (`exitUsage`, endpoint never dialed, nothing stored), not a silent
login. A read failure on the pipe is exit 1, matching box enroll's shape.

**Q2 — `53f3911` — the MFA refusal is honest (seam A, divergence 2).**
`httpHumanEndpoint.Login` now decodes `mfa_required` alongside `token`/`email`.
On an empty token with `mfa_required` set the error says, verbatim:
`cafe login: identity answered mfa_required: this account has a second factor;
complete the MFA challenge in the browser and then log in again — identity
minted a challenge, not a session, so there is no token to store`. The
pre-existing hard error for a genuinely empty token ("identity did not mint
one") is unchanged. The challenge value itself is opaque and is not repeated in
the message. Request shape, keyring behavior, and the non-MFA success path are
untouched, as is the `email` fallback (seam A divergence 1, TOLERATED).

**Q3 — `7681504` — floors via the repo's own writer.** Five new top-level tests
(+2 subtests) grew the tree to declared 1456; the ratchet (`declared − minimum
== 8`) went red as designed. `cafe gate floors --write` derived and wrote
`suite 1443→1448`, `subtests 1277→1279` and re-pinned `caf.lock`'s gate.yml
rule-bundle hash in the same run. Up only; nothing hand-edited down.

## Evidence

- **Gate green**: `mise exec -- ./bin/prime` exit **0** — `33 packages, 1448
  top-level passes, 1279 subtest passes, 0 failures, 8 skips`. `gofmt -l .`
  clean; `go vet ./...` clean.
- **Red proofs** (pre-fix `login.go` = `HEAD~2`, tests kept): all four new
  negative/path tests FAIL — stdin-path token equivalence, both-flags refusal,
  empty-pipe refusal, MFA-naming error. The control test
  (`TestLoginEmptyTokenStillSaysIdentityDidNotMintOne`) PASSES against the old
  code, which is the point: it pins the behavior that must not change.
- **Seam-B acceptance, end to end with the real binary**: a scratch
  `POST /v1/session` server on 127.0.0.1, scratch `HOME` (keyring landed at
  `…/cafe/keyring/cafe-human_you_example.com`, mode 0600):
  `echo 'correct-horse' | CAF_IDENTITY_URL=http://127.0.0.1:8471 cafe login
  --email you@example.com --password-stdin` → exit 0, one JSON document on
  stdout, one prose line on stderr, no secret in either stream. The MFA answer
  (202) → exit 1 with the new sentence.

## Notes for the manager / successor

- **The env var is `CAF_IDENTITY_URL`, not `IDENTITY_URL`** (constant at
  internal/cli/agent.go:44). The brief's done-criterion wrote `IDENTITY_URL=…`
  loosely; the e2e proof used the real name. Not changed — renaming it is out of
  scope and would be a second seam.
- **Stderr double prefix, pre-existing**: failed logins print
  `cafe login: cafe login: …` because `Run` prefixes and the error string
  begins `cafe login:`. This shape predates the packet (the old "did not mint
  one" error had it) and is left alone to keep the diff to the two fixes.
- **A stale stash from an unrelated packet sits in this worktree**:
  `stash@{0}: WIP on caf-gen-01 …` (telemetry-file renames, gate floors). I
  accidentally popped it mid-run when `git stash push` found nothing to save
  (my work was already committed); recovered with `git reset --hard HEAD` —
  both commits intact, tree clean, stash entry preserved untouched. It is
  `caf-gen-01`'s, not this packet's; its owner should inspect it.
- **README has no `cafe login` row** (the parity packet that added the command
  didn't add one). Out of scope here — the brief scoped me to internal/cli — but
  it is a real gap: `cafe login --email … --password-stdin` deserves the row the
  `cafe box enroll --token -` row has.
- **We did NOT change identity** or any other repo; identity was read-only from
  its main checkout (`/Users/kaka/Code/any/moon/cafaye`). Only worktree used:
  `/Users/kaka/Code/any/moon/wt/repair-seams-cafe`. No merge, no push.
