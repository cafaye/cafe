# REPORT — cafe's catalog client: the project rename (W3, cafe side)

Packet: `moon/PLAN-pantry-registry.md` Phase 1, cafe half.
Branch: `worker/cafe-rename` (commits `e6799ff`, `50a0489`, `0eed2dc`).
Base: `697abe0` on master.

The pinned contract was followed exactly. No route outside it was invented,
no field was added to what cafe decodes, and no capability was added.

## The surface, and what moved

`grep -rn "v1/services" internal/ cmd/ test/` found 11 string-level matches
across five files. Every one moved. The final `grep -rn "v1/services"` over
the whole tree (including `README.md`, `AGENTS.md`, `CHANGELOG.md`, `tests/`,
`cmd/`) is **empty**.

| Where | What it was | What it is |
|---|---|---|
| `internal/resolve/pantry.go:99` | `"/v1/services/"+name` | `"/v1/projects/"+name` |
| `internal/resolve/pantry.go:110` | `"/v1/services/"+name+"/coordinates"` | `"/v1/projects/"+name+"/coordinates"` |
| `internal/resolve/doc.go:8-9` | the tier table's two routes | the tier table's two routes |
| `internal/resolve/pantry_test.go` x6 | path assertion, two `instance` fields, the tier-2 unauthorized detail's own sentence, the usage test | same shapes, new paths |
| `internal/cli/resolve_test.go:560,563` | the end-to-end route switch | new paths |
| `internal/contract/apilint_test.go:359,430` | two OpenAPI fixture paths | new paths |

The schema rename, `Service` → `Project`, is the second half:

- `resolve.Service` (the type) → **`resolve.Project`**.
- `HTTPPantry.Service()` → **`HTTPPantry.Project()`**.
- `Pantry`'s tier-1 method → **`Project(ctx, name)`**, and its one call site
  in `Resolver.resolveOne`.
- The two tests that named a catalog object in their names
  (`TestFreeServiceResolvesThroughTier1AndTier2`,
  `TestPaidServiceResolvesThroughAllThreeTiers`) now name a project. The
  test COUNT did not move, so `gate.yml`'s floors are untouched — see below.
- `fakePantry`/`fakeResolvePantry`'s `service`/`serviceErr` fields → `project`/
  `projectErr`.

Field names are untouched: `name`, `description`, `language`,
`repository.visibility`, and tier 2's `repository.url`/`defaultBranch`.

## What was deliberately NOT touched

**`Report.Services` and its `"services"` JSON key** (`internal/resolve/resolve.go:166`,
`internal/cli/resolve.go:165`). This is the assumption I recorded, and it is
the only judgement call in the packet. `Report.Services` is cafe's own output
document, not pantry's schema; the brief's rule is no behaviour change beyond
the rename, and the phase renames the CATALOG vocabulary (MD23) while the
runtime word survives. `cafe resolve`'s JSON therefore reads exactly as it did
before this commit, so no consumer's parser moves. **If the wave's seam audit
reads this the other way, the change is four lines and one test.**

**The versions LIST route.** `GET /v1/projects/{name}/versions` is a later
phase per the pinned contract. cafe does not call it and does not declare it.

**`.../required-by`.** Renamed by the contract, but cafe has no client for it
and never did — there was nothing to move. Recorded so the audit does not
look for a gap that was never a hole.

**The tier-1 struct's field set.** pantry's schema lists `kind`, `core`,
`basePath`, `exposes`, `consumes`, `dependencies` and `owner` alongside the
four fields cafe decodes. cafe asks tier 1 one question — does this name exist,
and is it public — so the struct still carries four. Adding the rest would be
extending, not renaming, and a struct that carried the whole schema would be a
second copy of it. The comment on `Project` says so.

**Conductor's `Products`.** `kit`, `platform`, `identity`, `courier` are still
service names. Tier 3's contract did not move, and `resolve.Products` is a
mirror of conductor's list, not pantry's.

**pantry's internal `catalog.ErrNoSuchService`** in two comments. It names a Go
identifier inside pantry's own tree, which a schema rename does not rename.
Leaving it keeps the comment true about what it cites.

**The scaffold templates and `cafaye.yml`.** Untouched, as instructed.

**`caf.lock`.** Does not need re-running: the tree's own `cafaye.yml` declares
`dependencies: []` and exposes no HTTP surface, and no vendored schema,
generated file, spec or rule bundle in the tree touched the pantry routes.
`gate.yml` is a pinned file and its hash did not move — no test was added or
removed.

## Behaviour, and why it is unchanged

Tier 1 is a single name and tier 2 is that name's coordinates, so there are no
filters and no paging to preserve. What there is to preserve:

- **Error handling.** `problemError`, `PantryError` (status, code, detail,
  trace_id), `firstLine`'s rune-boundary truncation and `failureFrom` are not
  touched. A 404 is still a `not_found` carrying pantry's own sentence, and
  that sentence still does not echo the requested name.
- **The tier-2 flow.** The cafe token is presented as a bearer for the length
  of one call and is still never stored; tier 3 still mints, redeems and drops
  the capability token on the floor. `internal/cli/resolve_test.go`'s
  token-discipline assertions (the token reaches neither stream, the
  capability token reaches neither stream) pass unmodified.
- **The walk.** `resolveOne` is byte-for-byte the same walk, and the tier
  order is still the semantics.

## Test-first, and the red it was watched going through

The tests were renamed first and run against the unmoved client. The failure
was the right one and not an accident:

```
internal/resolve/resolve_test.go:16:14: undefined: Project
internal/resolve/pantry_test.go:28:16: p.Project undefined (type HTTPPantry has no field or method Project)
internal/resolve/resolve_test.go:80:19: *fakePantry does not implement Pantry (missing method Service)
FAIL github.com/cafaye/cafe/internal/resolve [build failed]
FAIL github.com/cafaye/cafe/internal/cli   [build failed]
```

Six failures in two packages, all naming the rename and nothing else — which
is the control that the red was the rename and not a broken expectation.
Commit `e6799ff` is that tree; `50a0489` turns it green.

## The gate, and its real exit codes

```
$ go test ./internal/resolve/... ./internal/cli/...
ok  github.com/cafaye/cafe/internal/resolve  0.732s
ok  github.com/cafaye/cafe/internal/cli      66.209s
exit: 0

$ ./bin/prime
cafe: 27 packages, 1207 top-level passes, 1228 subtest passes, 0 failures, 8 skips
cafe: live tier: 0 of 7 executed; not enabled, so the 7 live tests skipped rather than ran.
exit: 0

$ gofmt -l .     # no output
$ go vet ./...   # no output
```

**The floors did not move** — 27 packages / 1207 top-level / 1228 subtests,
0 failures, 8 skips, the same as master's declaration. That is the positive
control the brief asks for: a rename that deleted a test would have dropped
the count and turned `TestTheGateFloorIsNotBelowTheSuiteCafClaimsToHave` red.
Two test NAMES changed, which the floor does not count. `internal/ci`'s walker
sees the same suite.

The narrow command from the brief (`./internal/resolve/... ./internal/cli/...`)
was run first and then the full gate; both exit 0. The full gate was run
backgrounded per the quarterly contract, keyed to
`/tmp/gate-cafe-rename-prime.log`, with the exit code captured into
`/tmp/gate-cafe-rename-prime2.exit`.

## Assumptions recorded (there are two)

1. **`Report.Services` keeps its name and its JSON key.** Reasoning above. If
   the audit disagrees it is a four-line change plus one assertion.
2. **pantry's document version 1.4.0 is a no-op for caf.** cafe asserts no
   pantry document version anywhere — `grep -rn "documentVersion\|document
   version"` over `internal/`, `cmd/`, `README.md` finds nothing — so the
   contract's "cafe asserts that value only if it asserts a document version
   today" clause has nothing to act on. Nothing was invented to act on it.

## What a successor should know

The join with pantry's side is the wave's seam audit's job, not this packet's.
The two sides that must agree are the two route strings in
`internal/resolve/pantry.go` and the four field names on `resolve.Project` —
if pantry serves a different shape at `/v1/projects/{name}`, that is where the
audit will see it, and it will see it as a 404 or a silently-empty
`visibility`, not as a compile error.
