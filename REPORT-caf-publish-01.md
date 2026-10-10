# REPORT — caf publish: the submission flow (W11, caf side)

Packet: `moon/PLAN-pantry-registry.md` Phase 5, caf half.
Branch: `worker/caf-publish` (commits `b1cb69a`, `8812196`, and the docs/lock
commits that follow).
Base: `a4c1fe8` on master (the `/v1/projects` rename).

The pinned contract was followed exactly: the three write routes
(`POST /v1/projects`, `POST /v1/projects/{name}/versions`,
`POST /v1/projects/{name}/requirements`) and no others. The versions-LIST
route does not exist on the server and caf does not call it. Conductor is
untouched — publish is not gated on entitlement for community projects.

## The agent-first contract, in one paragraph

`caf publish` is OPS-D7's boundary — "the customer's own agent reads and
proposes freely; only a human approval mints an act capability" — realized at
the level of one command: the tool proposes, a human confirms, and the mint
(the three writes) happens only after a confirmation the command itself
cannot give. An agent's run reads the manifest, validates it, derives the
tag, and prints the proposal — the registration, the version tag, the
dependency edges — as data; it then either passes the explicit `--yes`
escape hatch or refuses, because a non-interactive run has nobody to ask
and the refusal is the honest answer. Every outcome — the dry run, the
refusal, the per-step results, the final state — is one JSON document on
stdout, because an agent runs this command in a pipeline and parses the
document rather than the prose. The boundary is the mint, not the reach:
nothing about reading or proposing is gated, and everything about writing
is.

## The surface

```
caf publish [--dry-run] [--yes] [--tag <git-tag>] [--kind <api|worker|both|cli>] [--json] [--pantry <url>] <project>
```

- `--dry-run` — print the proposal, write nothing. Safe to run anywhere,
  including a CI job.
- `--yes` — publish without asking. **Required** where nobody can answer the
  prompt; the only thing that authorizes a write without a confirmation.
- `--tag <git-tag>` — the git tag to publish, verbatim, leading `v` included.
  Without it the repository's latest semver tag is derived; a first publish
  has no tags, and the refusal names `--tag` rather than guessing. caf does
  not create tags: the tag is the publisher's act.
- `--kind <api|worker|both|cli>` — the project's kind when the manifest
  declares no contract surface. Derived from the surface (HTTP + events in
  is `both`) when there is one; required and refused-by-name when there is
  not, because a guessed kind is a claim the publisher never made.
- `--json` — one JSON document: the proposal, every step's result, the
  final state. The agent-readable artifact.
- `--pantry <url>` — pantry's origin; without it, `CAF_PANTRY_URL`.

**Streams.** stdout is the document (the proposal, the JSON report); stderr
is the sentence (warnings, step results for a human). The caller's token is
read from the keyring, presented to pantry on every write, and never
printed — asserted over both streams in the tests.

**Exit codes.** `0` — the command succeeded (a dry run, or a publish whose
steps all came back created or already-exists). `1` — the command ran and
failed: a declined or absent confirmation, a refused proposal (invalid
manifest, missing `--kind`, unbounded dependency), or a write that failed —
the report already names what and why, and stderr stays empty so the report
is not duplicated. `2` — caf was invoked wrongly (no project argument, an
unknown flag).

## The decision table, and its coverage

The command's tests are the decision table, one row per outcome a run can
have. The publisher, the confirmation and the tag derivation are injected
seams, so every row is driven without a network, a git repository or a
terminal:

| Row | Assertion |
|---|---|
| `--dry-run` | proposal on stdout, **zero** publisher calls, exit 0 |
| `--dry-run --json` | ONE document: proposal, no steps, published false, refusal naming the dry run; zero calls |
| no `--yes`, confirmation declines | proposal printed, **zero** calls, exit 1, stderr says nothing was published |
| no `--yes`, no terminal to ask | **zero** calls, exit 1; the JSON refusal carries the proposal and names `--yes` as the next move |
| `--yes` | register → version → edges **in order**, token on every call and in neither stream, report published, exit 0 |
| 409 `already registered` | step `already_exists` with the server's own detail, sequence **continues**, exit 0 |
| 409 `no published semver version` (the ordering rule) | step `failed`, sequence **stops**, published false, exit 1 |
| 401 | step `failed` carrying the server's code and detail, exit 1 |
| invalid manifest | refused by the contract linter's own gate **before** any write, exit 1 |
| directory with no manifest | refusal naming `caf init`, no proposal printed |
| `--tag v2.3.4` | the tag reaches the version write verbatim |
| no `--tag` | the derived tag is the proposal's tag, verbatim |
| manifest with a surface | kind derived (`both`), `--kind` not asked for |
| manifest with no surface | refusal naming `--kind`; with `--kind cli` the proposal carries `cli` |
| registration body | the manifest's own facts — name, language, core, repository, owner, sha256, state `published` |
| no project argument | usage error, exit 2 |
| `--help` | renders, names the flags, exit 0 |
| registry | `publish` is wired between `provider` and `reclaim`, usage names `--yes` |

The 409 split is the load-bearing row. A 409 carrying pantry's own idempotent
phrase ("already registered / published / declared") is the write having
already happened — a report line, and the sequence continues. Any other 409
is a refusal: the ordering rule's ("no published semver version of …")
arrives under the same status, and calling it idempotent would tell a
publisher their edge was stored when the database had rolled it back. An
unrecognized 409 detail is not idempotent either — the safe direction,
because the retry is cheap and the false success is not.

## What the proposal is

The proposal is built by `internal/publish.ProposalFor` from the validated
manifest, and it is the document a human confirms — which is why it is
printed before the question is asked and before anything is written:

- the registration: name, description, language, kind, core, consumes,
  exposes (API path + events), repository, owner, the manifest as a JSON
  document, its SHA-256 as the drift anchor, and state `published` —
  Docker Hub's model (MD28): push and it is public, no review queue;
- the version tag: the publisher's real git tag, verbatim;
- the edges: one per dependency — target, range verbatim, and the
  dependency class (`required` / `soft`; `required: false` is the soft
  edge, the registry's own word for a project that starts degraded).

The manifest is normalized through `encoding/json` and hashed, so the bytes
caf sends and the bytes pantry re-encodes to check `manifest_sha256` are
the same bytes. A dependency with no version is refused here rather than
sent: the registry resolves a range against the target's releases at write
time, and an unbounded edge is a 400 whose detail names a rule the manifest
already knew.

## What was deliberately NOT built

- **The versions-LIST read.** `GET /v1/projects/{name}/versions` is a later
  phase per the pinned contract. caf does not call it and does not declare
  it. Publish only writes.
- **A review or moderation flow.** The trust model is Docker Hub's (MD28):
  publish is not gated on entitlement for community projects, and conductor
  stays untouched. The registration's state is `published`, not `draft`.
- **Tag creation.** caf derives or accepts the tag; it never mints one. A
  tag caf created would be a tag the publisher never made.
- **A second manifest validator.** The manifest is validated by
  `contract.CheckData` — the same gate `caf contract lint` runs. caf has no
  second opinion about what a valid manifest is.
- **A second pantry client.** The write path is the write half of the same
  client resolve reads the catalog with: same origin, same bearer, same
  problem envelope (`resolve.PantryError` / `ProblemError`, exported this
  packet). A registry client with two ideas about pantry's wire shape would
  eventually have two shapes.
- **Scaffold templates and the manifest schema.** Untouched; the agent's
  editable surface is the customer's repository.

## The gate

`bin/prime` is the gate and the new tests are in it: the decision table
above is 21 top-level tests in `internal/cli/publish_test.go`, and the
package half (`internal/publish`) carries the proposal builder, the write
sequence and the HTTP publisher's own tests. `gate.yml`'s floors were
raised to the measured counts and `caf.lock` was re-run, because the floor
is a pinned rule bundle and a declaration that changed without the change
being deliberate is the defect. The live tier is untouched — publish's
tests need no container runtime.
