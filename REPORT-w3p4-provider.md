# W3-P4 handoff — `caf provider connect` / `status`

**Status: done, committed, gate green.** `1611924` on `agents-first/w3p4`.
Gate: 919 top-level passes (floor 892 → 919), 1080 subtest passes (floor 1058 → 1079), 19 packages (floor 17 → 19).
`bin/prime`, `go vet ./...`, `gofmt -l .` and `caf lock --verify .` all green.

## What the packet is for

The client half of the sealed-connection model (boundary 6). The agent
seals a provider credential **to the box's public key** and hands conductor
the envelope — it never handles the plaintext beyond the moment of sealing,
and never prints it. `status` reports the connection state honestly and
opens no envelope.

## What I found in the partial work, and what I did with it

The previous attempt (killed by the endpoint, not the packet) had most of
`internal/provider/` and `internal/cli/provider.go` written. I read all of
it, kept what was correct, and fixed three things:

1. **Two compile errors.** An unused `encoding/json` import in
   `internal/cli/provider.go`, and `knownProvider(provider string)`
   shadowing the `provider` package (the call `provider.Providers` inside
   it resolved against the parameter). Both fixed.

2. **A dead seam.** Both verbs' `Run` did
   `deps.cond = provider.HTTPConductor{BaseURL: url}` unconditionally, so
   an injected test conductor was overwritten before it was used — the
   seam existed but was not real. Fixed to `if deps.cond == nil { … }`,
   the same shape `boxDeps` uses, so a test can drive a whole connect
   without a network. This is load-bearing: the never-print test depends
   on the fake conductor capturing the posted payload.

3. **A usage line that lied.** `caf provider connect <provider> --account …`
   cannot parse — Go's `flag` stops at the first non-flag argument, and
   this repo's convention (`caf new --template app acme`,
   `caf init -dir x -name y`) is flags-first. Changed the usage to
   `caf provider connect --account <id> [--credential <secret> | stdin]
   [--config key=value]… <provider>` and the tests to match.

I also added `provider` to `workingCommands` (stub_test.go), to
`TestRegistryContainsEveryCommand`'s want list (command_test.go), to the
README command table, and to AGENTS.md's Layout section and stdlib-only
list. `internal/provider` is stdlib-only, so the "two packages with
dependencies" count is unchanged.

## What the tests prove

`internal/cli/provider_test.go` — 12 top-level tests:

- **`TestNoProviderCommandEverPrintsTheCredential`** is the brief's test.
  It drives `connect` through the parent's dispatcher against a planted
  identity with a real keypair and a capturing conductor, then searches
  **both streams and the posted payload** for the credential in raw,
  base64 and hex form. The control half asserts the non-secret facts ARE
  printed (provider, account, box, config) and that the posted envelope
  **opens with the box's private key** — a build that redacted everything
  would pass the first half by leaking nothing at all, and a build that
  sealed to the wrong key would produce an envelope nobody can open.
- **`TestProviderConnectThroughStdinNeverPrintsTheCredential`** — the same
  tripwire over the stdin path, because a flag is visible in the process
  list and a pipe is not.
- Refusals, each asserting the conductor count where the count is the
  proof: unknown provider (0 calls, exit 2), unenrolled box (0 calls,
  exit 1, names `caf box enroll`), missing `--account` (exit 2), bad arity
  on both verbs (exit 2), unknown verb (exit 2), conductor failure
  (exit 1, says the credential was NOT posted in plaintext).
- Status: connected (envelopes not echoed, in any encoding), not-connected
  (honest `connected: false`, exit 0), unenrolled box (honest report,
  exit 0, 0 conductor calls), and the stdout/stderr split.

`internal/provider/` — 15 tests: the ed25519→X25519 conversion pinned to
the standard (derived-public-equals-converted-public), the seal/open
round-trip, one-box-only, tamper-refusal, the not-ours refusal table, the
envelope carrying no form of the credential, and the connect/status seam
over a counting conductor.

## What I learned and ruled out

- **Conductor's W3-P2 is merged** (`db98ef6` on `main`): the
  `EmailProviderConnection` model, the migration, `installations.public_key`
  and the `SealedEnvelope` primitive. **No HTTP route** — `config/routes.rb`
  has no email-provider entries. The client is built against the documented
  RESTful shape (POST/GET `/v1/email-provider-connections`, the model's
  fields) and says so in `internal/provider/doc.go`. When the route lands,
  the client needs no change.
- **The envelope algorithm differs from conductor's, deliberately.**
  Conductor's `SealedEnvelope` seals with RSA-OAEP to a PEM RSA key
  ≥2048 bits. The box's keypair (W2-P6) is ed25519 — 32 bytes, no RSA
  equivalent. The client converts ed25519→X25519 (the birational map, the
  construction NaCl's crypto_box is built on) and seals with
  ECDH+AES-256-GCM, writing `alg: "X25519+A256GCM"` and an ephemeral
  public key in `key` rather than a wrapped one. The JSON shape
  `{v, alg, key, iv, tag, data}` matches conductor's, so the same column
  stores either. Conductor never opens an envelope, so the difference is
  invisible to it; the box opens both with the same code.
- **A cross-repo tension worth a packet of its own.** Conductor's own
  `EmailProviderConnection.create_sealed!` seals with RSA-OAEP to
  `installation.public_key` — which requires the box to have reported an
  **RSA** public key. caf's box reports an **ed25519** one. So
  conductor's own sealing path cannot seal to a caf box as things stand;
  only the client-side path (this packet) works end-to-end. The box side
  that opens what conductor stores (W2-P7 `boxd`) will need the same
  ed25519→X25519 conversion. This is a W3-P2 follow-up question, not a
  W3-P4 defect — the client posts the envelope and conductor stores it
  without opening it.
- **The packages floor was already one behind.** At the w2p6 merge the
  tree measured 18 packages and the floor said 17 (the same lag the
  emitter packet's comment records). This packet corrects it to the
  measured 19.

## The successor's first move

W3-P3 (the `/email-providers` screens) or W3-P5 (the Cloudflare DNS
records) can start. If W3-P3's form posts a **plaintext** credential to
conductor, it must go through conductor's `create_sealed!` — which cannot
seal to an ed25519 key (see above). The honest options, in order:
(a) the website posts the envelope the same way this client does (it
would need the box's public key, which conductor has), or
(b) conductor's `create_sealed!` learns the X25519 conversion, or
(c) the installation's `public_key` becomes an RSA key the box generates
for this purpose — which contradicts "the box's ONE keypair" and should
be argued against. The manager should pick before W3-P3 builds the form.
