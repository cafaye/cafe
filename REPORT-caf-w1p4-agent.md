# REPORT — W1-P4 caf: `caf agent register` / `login` / `token …` / `whoami`

**State: COMPLETE for this packet, one commit on `agents-first/w1p4`. Gate green:
`bin/prime` exit 0, 19 packages, 902 top-level passes, 1069 subtest passes,
8 skips, `go vet ./...` clean, `gofmt -l .` empty.**

The client half of the agents-first flow now exists. Before this packet the CLI
had zero HTTP client, zero token machinery and zero URL handling — verified with
a grep for `http|URL|token` over `internal/cli/*.go`, which returned nothing.
Everything below is new, and `internal/agent` is the CLI's first network path.

## What landed

```
internal/agent/doc.go       what the package is, three seams, the output
                            contract, and the invariant everything is downstream of
internal/agent/ring.go      the Keyring seam, the service/account naming
internal/agent/config.go    config.json: every saved session and the active one
internal/agent/endpoint.go  the six routes, the Endpoint seam, HTTPEndpoint,
                            StatusError and its hint
internal/agent/names.go     the username grammar, the collision retry, the
                            session name
internal/agent/store.go     register, login, whoami, and the four token verbs
internal/cli/agent.go       `caf agent <register|login|whoami|token>`: the
                            command tree, the streams, the refusals
internal/cli/agent_test.go  the ten tests, including the two the brief names
```

Registry entry, `workingCommands`, `command_test.go`'s sorted list, the README
command table, AGENTS.md's Layout section, and `gate.yml`'s three floors
(17→18 packages, 892→902 suite, 1058→1069 subtests) with `caf.lock` regenerated
in the same commit.

## The two things the brief asked to be proved, and where they are

**`TestNoTokenEverReachesEitherStream`** drives the whole normal flow through
the real dispatcher — register, login, whoami, `token show`, `token create`,
`token rotate`, then `token revoke --yes` — and asserts that THREE distinct
token values appear in NEITHER stream of ANY of the seven invocations. Its
control half is the block after the loop: the agent's username IS printed, so a
build that redacted everything would fail there rather than pass by leaking
nothing at all. The same shape `internal/box`'s
`TestNoBoxCommandEverPrintsThePrivateKey` uses, and for the same reason.

**`TestAgentRegisterSplitsTheStreamsAndStoresTheToken`** asserts stdout is one
CLEAN JSON document (`json.Valid` over the whole buffer, because a machine
parsing it has no way to skip a prose line somebody helpfully added), that the
claim URL is on stderr, that the token is in the keyring under
`caf/agent/<session>`, and that `config.json` on disk carries the agent and not
the token.

## The decisions, and the two I took against the prior art

**The keyring is the repo's `Keyring` seam, and the default implementation is
`box.FileRing` — a 0600 file under `~/.config/caf/keyring`, not the macOS login
keychain.** `internal/box/ring.go` already makes the argument: a subprocess is
not free, and the one thing caf must never do is fail to store a key because a
keychain helper was not on PATH. The brief's "OS keyring" is satisfied by the
seam whose doc comment names the login keychain as an implementation of it, and
the swap is one constructor call in `agentDeps.withDefaults()`. What this buys
is the half the brief actually requires: **no test ever touches a keychain**, so
none can hang on a keychain prompt. `agentTestRing` is a map, and the ten tests
run on a bare runner with no keychain, no D-Bus and no network.

**No `--json` flag.** `caf box` has one and it is the shape the brief's prior
art does not have: there, stdout is ALWAYS the document and stderr is ALWAYS the
sentence. A flag that chooses which stream gets the document is a flag that
guarantees one of the two is wrong in some script, so there is nothing to
toggle. Documented in `agentLongHelp`.

**The endpoint is never guessed.** `--base-url` or `CAF_IDENTITY_URL`, and
neither is a refusal (exit 1, and the message names both). This is the house
rule `CAF_CONDUCTOR_URL` already states — a default baked into a binary goes
stale when the service moves — and it is a deliberate divergence from
`refs/cafaye-cli`, which defaults to `https://cafaye.com`.

**The claim URL is on stderr AND a field of the document.** The prior art
prints the full payload (claim included) on stdout and the human summary with
the URL on stderr. I kept both, because AGENTS-FIRST.md invariant 5 says "the
agent may surface the link; only a human click approves" — and an agent can only
surface a link through the channel it parses.

**The token never crosses a package boundary as a value.** `Dial` is
`func(baseURL, token string) Endpoint`, not a stored client: a Store that held an
authenticated client would hold a token for as long as it lived, which is
invariant 1 verbatim. `RegisterResult.Token` has no JSON tag and is dropped at
the end of the method that received it.

## The one thing that is not here, and it is W1-P2's

**The routes are the documented shapes, not observed ones.** W1-P2 (identity's
`POST /v1/agents`, the claim-link refresh, and the scoped-token set) had not
landed in `identity-w1p2` when this packet was written — that worktree is still
at the w1p1 merge. So `internal/agent/endpoint.go`'s route table is
`POST /v1/agents`, `GET /v1/agents/home`, `GET|POST /v1/agents/key{,/rotate,/revoke}`:
the shapes AGENTS-FIRST.md's W1-P2 row documents, expressed in identity's own
`/v1/...` convention. They are ONE table with method and path split once, so a
route rename is six lines in one file. **The successor's first move is to diff
them against what W1-P2 actually shipped** — that is a read, not a rewrite, and
the `Endpoint` interface is the only thing that would move.

Claim-link refresh is deliberately absent. `register` prints the claim URL and
`whoami` reports an unclaimed agent with the remedy; a refresh verb is the
obvious next one and it is not in the brief's vocabulary.

## Two smaller rulings

- **Register is not idempotent, and that is a stated limitation.** Each POST
  mints an unclaimed principal. `internal/box` is careful to be re-runnable
  because a capability is single-use; a registration has no such constraint, so
  I did not invent one. What IS here is the collision retry: a generated
  username is retried up to six times, and a caller-CHOSEN username is never
  retried — a person told `--username noel` is taken is being told something
  they can act on, and a client that silently registered `noel-a3f9` instead
  has done something they did not ask for. Both halves are tested.
- **`login` makes no network call.** It is a local decision about which saved
  session is active; `whoami` is the verb that proves a token works. A login
  that needed the network would be a login that fails on a plane. What login
  DOES check is that the keyring still holds the session's token, and it
  refuses with the remedy when it does not.

## Verification

```
bin/prime                    19 packages, 902 top-level passes,
                             1069 subtest passes, 0 failures, 8 skips
go vet ./...                 clean
gofmt -l .                   empty
caf lock --verify .          clean (caf.lock regenerated with gate.yml)
```

The eight skips are unchanged: the seven live tests and the re-exec'd child,
all env-gated, none of the ten new tests among them.
