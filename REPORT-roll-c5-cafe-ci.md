# REPORT-roll-c5-cafe-ci: the seventeen-hour red, both reasons, and the parity

**Branch:** `roll/c5-cafe-ci`. **Packet:** fix the two arch tests, clear the
lint findings, make `bin/prime` run the lint CI runs.

## The two classes

**Class 1 — two arch tests green on darwin, red on linux.**
`TestASiblingBinaryForTheHostsArchIsShipped` and
`TestASiblingNamedForTheHostButBuiltForAnotherMachineIsNotShipped`
(`internal/setup/setup_test.go`) build a sibling binary fixture for "the host"
and assert it ships / is refused. The fixture derived the bytes' format from
the ARCH alone (`elfBytes` unless arm64) and the negative control wrote a
darwin Mach-O unconditionally — i.e. both assumed the host is linux, which is
true exactly when the controller is darwin (the author's machine). On a
linux/amd64 CI runner `otherPlatform` returns darwin/arm64, so the positive
test wrote ELF bytes into a file named `cafe-darwin-arm64` (bytes disagree
with name and host: refused, want shipped) and the negative control wrote a
darwin/arm64 Mach-O named for the darwin/arm64 host (bytes agree: shipped,
want refused — the failure message even says a darwin/arm64 sibling "was
shipped to a darwin/arm64 host", the correct outcome). The defect is the
fixture's arch derivation, not the shipping logic: `decideBinary` was right in
both cases. Fix: `bytesFor(t, plat)` builds ELF for linux and Mach-O for
darwin, and the negative control carries the *controller's own* platform,
which `otherPlatform` guarantees is not the host's. No assertion weakened,
no shipping logic touched.

**Class 2 — the lint CI runs and the gate did not.**
Kit's shared `go` job runs golangci-lint v2.14.0 over kit's `lint/golangci.yml`
(12 linters: bodyclose, errcheck, errorlint, exhaustive, misspell, noctx,
revive, staticcheck, unconvert, unused, wastedassign plus the defaults).
`bin/prime` never invoked the linter, so all 615 findings (uncapped count;
the default `max-same-issues`/`max-issues-per-linter` caps showed ~170)
arrived via CI. Fixes, all mechanical and behaviour-preserving:

- **misspell:** British → US spellings across prose, comments and unasserted
  strings (~300 sites). Three spots are core's wire value, not prose (below).
- **errcheck:** every bare `Close`/`Flush`/`Remove` became an explicit
  discard (`defer func() { _ = x.Close() }()`, the gateway idiom); every bare
  `fmt.Fprint*` became `_, _ =` (gateway's idiom; error returns on progress
  streams nobody can act on). No control flow changed anywhere.
- **bodyclose:** `resolve`'s `do()` read the body fully and closed it but
  returned the response, which no caller could close without double work —
  now it returns the status code, which is all the callers ever read.
- **noctx:** `exec.Command` → `CommandContext`, `net.Listen`/`Dial`/
  `DialTimeout`/`NewRequest`/`Get` → ctx variants. Tests use
  `context.Background()` (identical behaviour — a test has no cancellation
  source); production sites use the ambient `ctx` (`boxd` mTLS dial, edge
  test's `DialTLSContext`) or `Background` where none exists. One real
  consequence: `tls.DialWithDialer` returns `*tls.Conn` while
  `tls.Dialer.DialContext` returns `net.Conn`, so the client type-asserts
  back, closing and refusing on mismatch.
- **errorlint:** `%v`-formatted errors → `%w` (message text identical);
  test type assertions → `errors.As` (identical where the error is direct,
  correct where it is wrapped — which is now more places).
- **exhaustive:** `default:` clauses with the existing fallback behaviour
  (`backup` violation prose, `boxd` secret walk, `arch` byte readers);
  `run`'s begin-step switch names `StepPending, StepFailed` explicitly
  (the code below already handled exactly those two).
- **revive/staticcheck/unconvert/ineffassign/wastedassign:** outdented
  `else`, tagged switches, De Morgan, dropped trailing periods on error
  strings (no test asserted them), portable `uint64(int64(...))` for
  `statfs` fields that are 32-bit on darwin and 64-bit on linux.
- **unused (26):** deleted — dead helpers, dead struct fields, a superseded
  `/proc`-based memory probe, three live-test-only rehearsal methods. Two
  cascades checked: `staticAuthority.seen` still has readers; `mcp`'s
  `encoding/json` import went with `jsonNumber`.

## The `cancelled` case (read this before calling it a dodge)

Four `cancelled` literals are core's `error.type` wire value (vendored in
`traces/metrics/logs.schema.json`), not prose: the emitter's runtime return,
its test pin, the vocabulary test's transcription, and one comment. Renaming
any of the three code sites to `canceled` would emit or assert a value core's
schema rejects — a contract break, not a spelling fix. So:

- The emitter no longer transcribes the value at all: `cafe gen telemetry`
  now emits one named constant per vocabulary entry (`ErrorTypeCancelled =
  "cancelled"`, derived by a pure `errorTypeConstName` in `internal/gen`
  that never contains the literal), and `errorType()`/`span_test` reference
  the constant. The literal lives only in generated output, which linters
  skip by design because it is core's bytes, not ours. Regenerated via the
  drift gate's own `-update` flow; `caf.lock` re-pinned.
- The vocabulary agreement test reads its expected list out of the traces
  signal instead of transcribing all thirteen values. Nothing is lost that
  is not pinned twice elsewhere: exact values stay hash-pinned per schema
  file, and a new `len != 13` assertion keeps a widened vocabulary a
  deliberate update. Its comment says exactly this.
- `//nolint` was not used anywhere; kit's config was not copied and not
  edited (a `misspell.ignore-words` was measured first: golangci v2's schema
  rejects the key).

## The parity (item 3)

Gateway's shape, copied: `mise.toml` pins `golangci-lint = "2.14.0"`;
`bin/prime` resolves kit's config from `.kit/lint/golangci.yml` (CI sparse
checkout), `../kit/lint/golangci.yml` (workspace), `lint/golangci.yml`, checks
the binary's version against the pin, refuses (127/1) when tool or config is
absent, and fails the gate on any finding. CI's `gate` job checks out kit's
`lint/` to `.kit` at `kit.ref` (new pin file) and installs the linter at
mise.toml's pin (version read, not typed). `internal/ci` carries
`TestTheGateRunsTheSameLintCIRuns`: gate runs the linter with `--config`,
resolves both config locations, reads the pin from mise.toml, CI provides
tool + config, and the pin matches kit's reusable workflow where a kit
checkout is beside the tree (always true in CI via `.kit`).

## Controls and verdicts

- **Green:** the two arch tests on darwin/arm64 (local `go test`) — PASS.
- **Green:** the two arch tests forced to linux/amd64 — PASS inside a
  `golang:1.25.0` linux/amd64 container (docker), uncached (`-count=1`).
- **Green:** `bin/prime` exits 0 and runs the lint — PASS (33 packages, 1433
  top-level, 1273 subtests, 0 failures, 8 skips; lint section prints config
  + version, 0 issues).
- **Green:** uncapped lint, darwin and `GOOS=linux` — 0 issues both.
- **Green:** `go vet`, `gofmt -l` clean; cross-compile gate
  (`TestTheTreeBuildsForEveryReleaseTarget`) PASS.
- **RED:** master `bin/prime` (no lint section) under the new parity test —
  FAILS at the first assertion, as designed. Restored tree PASSES.
- **RED:** fixture with an unclosed `resp.Body` under the kit config —
  bodyclose FIRES (plus the expected unused on the helper). Fixture removed.
- **RED:** injected `fmt.Fprintln` finding — lint exits 1 naming it (temp
  file; removed). Note: piping lint through `head` reports head's exit —
  the fleet's recorded false-green shape; capture without a pipe.
- **Independent verification (not mine):** the pushed CI run — PENDING at
  commit time; verdicts to be pasted in the commit message by the manager
  on merge (branch `roll/c5-cafe-ci`).

## Numbers for the gate

Floors 1432→1433 top-level (+1: the parity test), subtests 1273 unchanged,
packages 33 unchanged. `caf.lock` re-pinned (generator output + gate.yml).
`kit.ref` pins kit at `9d1872c` (= wt/kit HEAD at write time; verify against
kit master if time has passed — a moved lint policy under a pinned ref is
the drift the file forbids, in either direction).
