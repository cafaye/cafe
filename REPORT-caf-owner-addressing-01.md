# REPORT — caf-owner-addressing-01

SEAM-AUDIT-01 finding F6, un-deferred by the user. Branch `worker/caf-owner`,
two commits: the failing tests first (`e8b745e`), then the surface (`37180af`).
This report records what landed, the ambiguity-retry semantics as implemented,
and the one clause of the brief that has no consumer in this tree.

## What pantry master serves, and what caf can now say

pantry's `openapi/v1.yaml` at document 1.7.0 (read directly from
`cafaye/pantry`, not from a summary) adds three things caf could not speak:

| pantry 1.7.0 | caf before | caf now |
| --- | --- | --- |
| `Project.owner_slug` required on every row — the derived namespace of the address | absent from `internal/resolve`'s `Project`, so a client could not tell *whose* row a bare name resolved to | decoded, and it is load-bearing (below) |
| `GET /v1/projects/{owner}/{name}` | absent — the only door was the bare-name route | `HTTPPantry.ProjectByAddress`, anonymous, both segments escaped |
| `GET /v1/projects?owner=<slug>` | absent | **still absent — see "The `?owner=` clause"** |

The address route resolves exactly when the row's bare name AND its derived
owner both match the two segments, and its 404 names the address that was
asked for ("no official cafaye project is registered under the address
alice/widget") — which is how a caller tells "no such project" from "no
project at that address". That detail is asserted in
`TestHTTPPantryProjectByAddressNotFoundNamesTheAddress`.

## The surface landed

**`internal/resolve/pantry.go`**

- `Project` gains `OwnerSlug` (`json:"owner_slug"`). Its comment said pantry
  publishes exactly FOUR advertising facts and the struct held them; it now
  says five and holds the fifth, and it says *why* this one exception to the
  deliberate minimalism exists: it is the field that tells a caller who asked
  for `alice/widget` and received a row from the bare-name fast path that the
  row is not at that address. Without it a resolver answers the wrong
  publisher's project and calls it a success.
- `Pantry` gains `ProjectByAddress(ctx, owner, name)` and `HTTPPantry`
  implements it against `/v1/projects/{owner}/{name}` with both segments
  `url.PathEscape`d. Escaping is a no-op for anything the grammar admits
  (neither slug grammar has a slash) and is done anyway, because a caller that
  passed a slash through would be asking the bare-name route a question it
  cannot answer and getting a 404 naming a name nobody typed.

**`internal/resolve/resolve.go`**

- `Resolution` gains `Owner` (`json:"owner,omitempty"`) and an `Address()`
  method, so a sentence about a resolution can say the address that was asked
  for. `Report.Services`' key is untouched (W3's recorded assumption stands).
- `ResolveAddress(ctx, owner, name, token, accountID)` is the entry point the
  CLI's slash form runs: one project, addressed, with no manifest behind it.
- `ambiguousName` is the collision detector.

**`internal/cli/resolve.go`**

- `addressOf` splits an argument into `owner/name` when it has exactly one
  slash, both halves match pantry's own slug grammar, and there is no
  `cafaye.yml` at the path. The grammar check is why `caf resolve
  ../some/path` is never mistaken for an address, and the manifest check is
  why a real project directory spelled `owner/name` is still a directory
  (`TestResolveTreatsADirectoryWithASlashAsADirectory`).
- The usage line and long help document both forms.

**`internal/publish/publish.go`, `internal/cli/publish.go`**

- `Registered` gains `OwnerSlug`, read out of the 201 body (which is pantry's
  own `Project` read back). `Report` gains `Address` (`json:"address,omitempty"`),
  and `printSteps` says "app is in the catalog, addressable as cafaye/app".
- An idempotent 409 carries no 201 body, so the address is empty rather than
  guessed (`TestAnIdempotentReRegisterHasNoAddressToName`).

## The ambiguity-retry semantics, as implemented

`resolveOne` reads the BARE NAME FIRST even when an owner is known — the fast
path the fleet uses, and the only form a manifest dependency can express
(core's `serviceName` grammar has no slash). The address read is the fallback,
taken on exactly two signals and on no others:

1. **A 404 whose detail signals a collision, with an owner known.** Pantry's
   document promises that the day `projects_name_key` is relaxed, the bare-name
   collision answers 404 "naming the addresses" and only `owner/name` resolves.
   With an owner known, the resolver asks the address that was meant. This is
   `ambiguousName`, and it matches `ambigu` in the detail or `more than one
   owner`.
2. **A row whose `owner_slug` is not the address that was asked for.** The
   bare name resolved — under somebody else. Answering it would be serving a
   project at an address that is not its own, which is exactly what pantry's
   own handler refuses to do; the address is asked and its 404 (which names the
   address) is the failure. Guarded by `proj.OwnerSlug != ""`: a pantry older
   than 1.7.0 sends no slug, and an ABSENT slug is not evidence of a mismatch.
   The safe direction, since the alternative fails every addressed resolution
   on a stale deployment.

And two non-retries, both asserted:

- **A plain "no such project" 404 never retries**, with or without an owner.
  There is nothing an address could resolve that the bare name did not.
- **With no owner supplied the address read is never made.** A manifest
  dependency is a bare name; the resolver asks nothing more than it was told.

`ambiguousName`'s detection is a PROVISIONAL CONTRACT and the report says so
rather than hiding it: the state is unreachable today (the unique key forbids
it), so there is no code to match on and no stable sentence to match on. Both
drift risks are named in the function's comment and both are the safe
direction — a false negative stays a plain 404 (the brief's requirement), a
false positive costs one address read whose own 404 names the address tried.
The alternative (waiting for pantry to publish a code) would be a client that
silently stops retrying the day the state becomes reachable.

One honest consequence, documented in the long help and pinned by a test: an
addressed PAID product cannot clear tier 3, because the address form carries no
version constraint and conductor's check requires a range — so
`caf resolve alice/identity` answers `version_required` at tier 3 with the
coordinates tier 2 returned intact, and conductor is never asked a question it
would answer 422.

## The `?owner=` clause of the brief, and why nothing landed for it

The brief asks that "the collection client passes `?owner=` when asked". There
is no collection client in this tree: `grep -rn "v1/projects" --include="*.go"`
finds five call sites — three reads in `resolve/pantry.go` (now six), three
writes in `publish/publish.go` — and nothing that lists the catalog. There is
no `caf` command that enumerates projects, and `caf resolve`'s job is a
manifest's dependencies, not the registry as a whole.

A client written for no caller would be a method nobody runs, which is the
thing this repository's culture is most against, and it would carry its own
`next_cursor` paging shape and its own tests against a route nothing calls.
The clause is therefore answered here rather than implemented: when a caf
command first needs to enumerate the catalog, `HTTPPantry` is where the
collection read belongs, and the filter is pantry's `?owner=` against its
slug grammar. The finding F6 itself is about addressability, and the address
route and `owner_slug` — the two things that make a name addressable — are
landed.

## Tests

14 top-level tests added, and the failing state was committed first
(`e8b745e`: the suite did not even compile, because `ProjectByAddress`,
`Registered.OwnerSlug`, `Report.Address`, `Resolution.Owner` and
`ResolveAddress` did not exist).

| file | tests |
| --- | --- |
| `internal/resolve/pantry_test.go` | 2 — the address read's path, query-lessness, anonymity and `owner_slug` decode; the address 404 naming the address |
| `internal/resolve/resolve_test.go` | 6 — the ambiguity retry; a plain 404 never retrying; no owner means no address read; the wrong-owner refusal (and no leaked coordinates); the matching address resolving without a second read; the addressed paid product's named range refusal |
| `internal/publish/publish_test.go` | 2 — the 201 body's address through the real HTTP client; the idempotent re-register naming no address |
| `internal/cli/resolve_test.go` | 3 — the slash form (document carries `owner`+`name`); the wrong owner refused at exit 1; a directory spelled `owner/name` staying a directory |
| `internal/cli/publish_test.go` | 1 — the address on both streams |

The two-questions rule is enforced by the fakes' call records rather than by
output shape: `fakePantry.ProjectByAddress` and
`fakeResolvePantry.ProjectByAddress` count their own calls, so a resolver that
merged the two reads into one wrapper could not pass.

## Verification

```
go test ./internal/resolve/... ./internal/cli/...   pass
go vet ./...                                        clean
gofmt -l .                                          prints nothing
go run ./cmd/caf lock .                             caf.lock re-pinned (12 files)
./bin/prime                                         28 packages, 1258 top-level passes,
                                                     1234 subtest passes, 0 failures, 8 skips
```

`gate.yml`'s `suite` floor moved 1244 → 1258, which is exactly the 14 tests
this packet adds, and `caf.lock` is re-pinned in the same commit because the
floor's hash moved — the friction `AGENTS.md` calls deliberate. The
`live-tier` line is unchanged (0 of 7 executed; `bin/prime` is the declared
gate and the live tier is not in it).

## What is deliberately not here

- **No collection read** (`?owner=`): the section above.
- **No `--owner` flag.** The brief's address syntax is the only way an owner
  reaches the resolver, because a manifest cannot carry one. A flag would be a
  second spelling of the same question, and the one that parses the slash in
  the CLI is the one a caller types.
- **No change to the write path beyond reading the 201 body's `owner_slug`**,
  and none to the scaffold.
- **No discovery walk.** The resolver does not enumerate the catalog to answer
  "who owns `widget`" when the bare name is ambiguous and no owner was given;
  it reports the 404 pantry sent. A discovery walk would be the library
  inventing a question the caller did not ask.
