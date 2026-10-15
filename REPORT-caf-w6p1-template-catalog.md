# REPORT — cafe-w6p1: the template catalog and a real `cafe new` / `cafe init`

**Branch:** `agents-first/w6p1`
**Base commit:** `027693c` (gate floor 852 -> 853 for m169)
**Landed:** `d826f17` (catalog + both commands), `902b923` (gate floor 853 -> 871, re-lock), `1580439` (README glossary)

## What the packet was for

W6-P1 of AGENTS-FIRST.md: `cafe new` and `cafe init` were stubs — `new.go`
itself said the template catalog "lands with the generator packet", and this
is that packet. The deliverable is a catalog, a working `--template app`
scaffold whose manifest validates against the vendored manifest schema and
lints clean, tests over all three claims, and a green gate.

## What shipped

**`internal/scaffold`** — the catalog, split the way the rest of the tree is
split:

- `doc.go` — the package and its three rules: templates are embedded (one
  binary, one catalog), a scaffold is rendered into memory and refused before
  anything is written, and the manifest is validated by the same compiled
  code `cafe contract lint` runs.
- `catalog.go` — `templates/index.json` (one entry: `app`) plus a walk of the
  template's directory. The tree is the single source of truth for what a
  template contains; an index that listed files would be a second copy that
  could disagree with it. `//go:embed all:templates` — the `all:` matters:
  without it the embed rules silently drop dotfiles.
- `render.go` — `ValidateName` (a mirror of core's `serviceName` `$defs`, kept
  honest by a test that renders a table of names through the real schema),
  `text/template` execution with `missingkey=error`, and the
  `contract.CheckData` gate that refuses a non-linting manifest before the
  write.
- `write.go` — the one write; the refusal names every file in the way and
  writes nothing, `-force` is the only override.
- `scaffold.go` — `New` (whole project) and `Init` (manifest only), the name
  derived from the target directory unless `-name` overrides.

**The app template** — `cafaye.yml` (no `exposes`: a scaffolded app publishes
nothing yet, which is the `worker`/`spec` fixture shape), `main.go` (a real
service on :8080 answering `/healthz` and `/readyz`), `go.mod`,
`docker/Dockerfile` (where `cafe dev` looks), `README.md` (the loop, and the
two rules: the manifest is identity not configuration; the customer's app is
the editable surface). No `exposes.api`, deliberately — the OpenAPI document
would be W6-P2's surface, not a guess made here.

**Both commands are real.** `new.go` gained `-force` (symmetry with `init`
and `gen`), `init.go` gained `-name` (a worktree directory named after a
packet is not a service name), and `cli/scaffold.go` holds the shared
print-every-file-in-full report, for the reason `confirm.go` gives.

## The proofs, by hand, with the built binary

```
$ cafe new --template app acme          # wrote 5 files, all printed in full
$ cafe contract lint acme
OK acme/cafaye.yml                     # exit 0
$ cafe new acme                          # second scaffold
cafe new: nothing was written; 5 files already exist. ... then re-run with -force
                                       # exit 1; the hand-edited manifest survived
$ (cd acme && go build ./... && go vet ./...)   # the scaffolded Go compiles
$ cafe init -dir acme                   # in an existing project: manifest only
OK cafaye.yml
```

## The automated proofs (18 new top-level tests)

- `internal/scaffold/scaffold_test.go` — the catalog resolves every indexed
  template; an unknown template names the catalog; **every template's written
  tree passes a full `contract.Lint`** (schema + conventions + document
  resolution, off disk, not just bytes); the credential-key scan;
  deterministic renders; the clobber refusal plus `-force`; init writes only
  the manifest; name derivation; and the name table driven through BOTH
  `ValidateName` and the real schema — including the four reserved names the
  schema admits and cafe refuses (the divergence is deliberate and pinned).
- `internal/cli/new_test.go`, `init_test.go` — the same claims at the command
  surface, through `runCLI`, in a `t.Chdir`'d temp dir.

## Gate

`bin/prime`: **17 packages, 871 top-level passes, 0 failures, 8 skips** — the
floor was raised 853 -> 871 in the same commit and `cafe lock .` re-run
(gate.yml is the tree's pinned rule-bundle). `go vet ./...` clean, `gofmt -l .`
clean, `tests/gate-declaration-self-test.sh` green (3 controls, 21
breakages red), `cafe lock . --verify` green, `cafe contract lint .` green.
Coverage: tree 88.0% (CI floor 85%), `internal/scaffold` 79.7%.

## Ruled out, and why

- **A template index that lists files.** The walk over the template directory
  is the truth; a listed second copy fails by pinning a file nobody wrote.
- **`exposes.api` in the scaffolded manifest.** It would require an OpenAPI
  document the lint then resolves, and inventing a contract surface for an app
  that has none yet is exactly the guess this packet exists not to make.
- **A `.github/workflows/ci.yml` in the template.** kit's shared workflow ref
  is not on this machine and was not transcribed from memory; the README
  names kit as the starting point instead.
- **A checked-in `kit` convention set.** The `kit` repo is not checked out in
  this workspace; the Dockerfile comment says what it is (the minimum a Go
  service needs) and where to go next (kit's base), rather than claiming to
  be kit's.

## The successor's first move

W6-P2 composes identity + gateway + courier + parlor into a platform app
template. It needs the four dependencies' version constraints for the
manifest's `dependencies` (the schema's `serviceRef` allows name/version/
required) and a `cafe dev`-runnable compose path. The catalog already resolves
a second template directory; the `TestEveryTemplateScaffoldsAProjectThatLints`
case will hold it to the same pair of proofs the moment it exists.
