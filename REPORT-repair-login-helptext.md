# REPORT — `cafe login --help` names the env var the code reads (worktree: repair-login-helptext)

Date: 2026-10-15. Branch `repair/login-helptext`, two code commits plus this
report on top of `5a9492f`, working tree clean. Manager merges and pushes.

## The bug, both sides

- The help's example hard-coded a name the code does not read. Before this
  packet, `internal/cli/login.go:197` read:

  ```go
  "\tIDENTITY_URL=https://identity.cafaye.com cafe login --email you@example.com --password-stdin\n\n" +
  ```

- The code reads exactly one env var, `internal/cli/agent.go:44`:

  ```go
  const identityURLEnv = "CAF_IDENTITY_URL"
  ```

  and `internal/cli/login.go:36` reads it (`d.baseURL = strings.TrimSpace(os.Getenv(identityURLEnv))`).
  With an unset `--base-url`, a login refuses (`internal/cli/login.go:251`:
  `"cafe login: pass --base-url <url> or set %s\n"`). So pasting the help's own
  example set `IDENTITY_URL` — a no-op — and cafe then refused by name.

- The same help interpolated `identityURLEnv` correctly one paragraph above
  (`internal/cli/login.go:192`): `identityURLEnv + "; an unset one is a refusal, not a guess.\n\n"`.
  The bug was exactly one hard-coded string contradicting the line above it.

## What landed

**Q1 — `b2e5848` — the fix and its regression test.**
`internal/cli/login.go:203` now interpolates the constant rather than naming the
variable a second time:

```go
"\t" + identityURLEnv + "=https://identity.cafaye.com cafe login --email you@example.com --password-stdin\n\n" +
```

The example URL is unchanged (`https://identity.cafaye.com`), per the manager's
ruling — it is an example for a self-hosted box, not the hosted endpoint.

The regression test, `TestLoginHelpExampleNamesTheEnvVarTheCodeReads`
(`internal/cli/login_test.go`), renders the real help via `printHelp` and:

1. finds every `[A-Z][A-Z0-9_]*=` assignment the help teaches and fails if any
   names something other than `identityURLEnv` (the shape that matters: help and
   constant cannot disagree);
2. asserts the exact runnable example line is present, built as
   `identityURLEnv + "=https://identity.cafaye.com cafe login --email you@example.com --password-stdin"`.

The expected value is derived from `identityURLEnv` in both halves; the test
contains no second copy of the variable's name, which is the failure mode this
packet exists to close.

**Q2/Q3 — `ad873d4` — floors via the repo's own writer.**
The new top-level test grew the tree to declared 1457, so the ratchet
(`declared − minimum == expectedSkips`, 8) went red as designed in
`internal/ci` and `internal/gate`. `cafe gate floors --write` ran the gate,
derived `suite 1448→1449` (subtests unchanged at 1279), wrote it preserving every
comment, and re-pinned `caf.lock`'s `gate.yml` rule-bundle hash in the same run.
Up only; nothing hand-edited down.

Nothing else changed: not the flags, not the request shape, not the MFA refusal,
not the `/box` page.

## Evidence

- **`cafe login --help` prints a runnable example** (final tree):

  ```
  $ mise exec -- go run ./cmd/cafe login --help | grep 'cafe login --email you@example.com'
  	CAF_IDENTITY_URL=https://identity.cafaye.com cafe login --email you@example.com --password-stdin
  ```

- **Red proof for the new test.** With the help temporarily restored to the
  hard-coded `IDENTITY_URL=…` (the pre-fix state) and the test kept,
  `go test ./internal/cli/ -run TestLoginHelpExampleNamesTheEnvVarTheCodeReads`
  FAILS, naming the disagreement:

  ```
  --- FAIL: TestLoginHelpExampleNamesTheEnvVarTheCodeReads (0.00s)
  ```

  Reverted immediately; the red proof cannot be silently weakened because the
  assertion is on the rendered help, not on a hard-coded expectation.

- **Gate green on the final SHA**: `mise exec -- ./bin/prime` exit **0** —
  `33 packages, 1449 top-level passes, 1279 subtest passes, 0 failures, 8 skips`.
  `gofmt -l .` clean; `go vet ./...` clean.

## Notes for the manager / successor

- **One pre-existing flake observed, not this packet's.** The first full gate run
  showed three failures: the two ratchet tests above (expected, stale floor) plus
  `internal/deploy`'s `TestTheSyncWaitConvergesAfterSeveralPolls`
  ("Argo converged on the third check" but the poll stopped at `0.1.0
  Progressing`). That test is in an untouched package, passes alone repeatedly
  (`go test ./internal/deploy/ -run TestTheSyncWaitConvergesAfterSeveralPolls
  -count=1` three times, plus twice more inside subsequent full gate runs). It
  looks like a timing/`-race` flake under parallel package load; flagging it, not
  fixing it here — the brief scoped me to the help string and its test.
- **The predecessor's finding is now closed.** `REPORT-repair-seams-cafe.md:57-60`
  recorded that the brief's done-criterion loosely wrote `IDENTITY_URL=…` while
  the code reads `CAF_IDENTITY_URL`. That mismatch is what reached the help text;
  it no longer can, because the help interpolates the constant.
- **The predecessor's README gap still stands** (`REPORT-repair-seams-cafe.md:71-73`):
  `cafe login` has no row in the README command table. Out of scope here.
- Only this worktree was used: `/Users/kaka/Code/any/moon/wt/repair-login-helptext`.
  No other repository was touched. No merge, no push.
