# REPORT — cafe-w88: the no-Application packet

**Branch:** `worker/cafe-w88-no-application`
**Base commit:** `1c8d4d3` (Merge worker/cafe-w81-chart-ingress)
**Head when reported:** `791aab0` (three commits on the base, listed below)

## What the packet did

`cafe gen chart` emitted a seventh file, `chart/templates/application.yaml` —
an ArgoCD `Application` document generated per service. The substrate owns the
Application: one static per service, OCI-sourced, applied by `install.sh`
(moon/DEPLOYMENT.md, "Who owns the Application"). A generated one would be a
second `Application` of the same name in the same namespace with a different
source, and the later `kubectl apply` wins — a deploy whose target depends on
apply order is not a deploy. The generator now emits **six** files.

## Commits

| commit | what |
|---|---|
| `bedce49` | The red-first proof: `TestTheGeneratorEmitsNoArgoApplication` added — red while the generator still emits the Application. |
| `c757c46` | The fix: the `chartApplicationPath` const, the `File` entry, `renderApplication`, and the `targetRevision` value that fed only the Application's source are gone. Six files now. The gate.yml comment block and the lock's `gate.yml` hash moved here. |
| `791aab0` | The gofmt pass the removal required: with one path gone from the `chartYAMLPath …` const block, the remaining five `=` columns were misaligned. Verified: `gofmt -l` named `internal/gen/chart.go` at `c757c46`; `gofmt -l .` prints nothing at `791aab0`. |

## The red-first proof — all three states measured this session

The test (`internal/gen/chart_test.go`, `TestTheGeneratorEmitsNoArgoApplication`)
asserts over the real generator output: no file at
`chart/templates/application.yaml`, no `kind: Application` document under any
path, and no `targetRevision` in `values.yaml`.

1. **Red at base.** In a throwaway worktree at `1c8d4d3` with the test appended
   to the base's `chart_test.go`: `--- FAIL` — the base generator emits the
   Application, so the path and `kind: Application` assertions both fire.
2. **Red when the emission is re-added.** On this tree, with one `File` entry
   for `chart/templates/application.yaml` temporarily re-added to
   `internal/gen/chart.go`: `--- FAIL` — both assertions fire again, with the
   test's own sentences ("the substrate owns the Application … the later apply
   wins"). The temp edit was reverted; the tree is clean and the test is green.
3. **Green after removal.** `go test -run TestTheGeneratorEmitsNoArgoApplication
   ./internal/gen/` on the final tree: `--- PASS`. The test is also one of the
   795 in the gate run below.

## The real-manifest proof

`cafe gen chart` run against `internal/contract/testdata/valid/identity.cafaye.yml`
— a real manifest with an HTTP surface (`exposes.api: openapi/openapi.yaml`),
copied to a scratch directory outside the repository:

```
$ cafe gen chart
cafe gen chart wrote 6 file(s) from this project's cafaye.yml:
exit: 0

chart/Chart.yaml
chart/README.md
chart/templates/deployment.yaml
chart/templates/ingress.yaml
chart/templates/service.yaml
chart/values.yaml

chart/templates/application.yaml ABSENT
```

Six files, `chart/templates/application.yaml` among them nowhere. The chart
README's loop section now says who owns the Application and where that is
written down, so the next reader does not re-add it.

## The gate.yml floors — before and after, and why they are unchanged

| floor | at base `1c8d4d3` | at head `791aab0` |
|---|---|---|
| packages | 15 | 15 |
| suite (top-level passes) | 795 | 795 |
| subtests | 969 | 969 |

The floors were last moved by `d17519d` (cafe-w81, the ingress packet). This
packet **swapped one top-level test for another** —
`TestTheApplicationSyncsTheManifestsRepository` out (its subject no longer
exists), `TestTheGeneratorEmitsNoArgoApplication` in — and neither test has
subtests, so neither floor moves. The measured run confirms the arithmetic
exactly: **803 declared − 8 skips = 795 top-level passes**, and **970 subtest
passes** against a floor of 969 (the file's standing one-lower convention).
The only `gate.yml` change on the branch is the comment block recording this,
added in `c757c46`.

## The lock

`caf.lock` pins `gate.yml` as the tree's one `rule-bundle`, and `gate.yml`
changed on this branch (the comment block), so the pin moved — in `c757c46`,
the fix commit:

- `gate.yml` sha256 `541ff444…` → `a3339fad…`

No other entry moved. `791aab0` touched only `internal/gen/chart.go`, which is
generator source, not one of the four pinned kinds (`spec`, `vendored-schema`,
`generated-client`, `rule-bundle`), so it needed no re-pin. Verified on the
final tree:

```
$ cafe lock --verify .
cafe: caf.lock matches 12 pinned file(s), and nothing the tree declares is missing from it.
```

## The gate

`go mod download && go build ./... && go test ./...` on the final tree
(`791aab0`), run in the background with the log keyed to this worktree:

- **exit code: 0**
- 15 packages `ok`, 1 `[no test files]`, 0 failures
- verbose count: **795 top-level passes, 8 skips, 970 subtest passes** —
  exactly the declared identity (803 − 8 = 795; subtest floor 969 ≤ 970)

`gofmt -l .` prints nothing. `go vet ./...` was not part of the packet's gate
command and was not run; the gate as declared is the three commands above.

## Out of scope, as instructed

No new behaviour; `m76` (cafe's Argo deploy) is a separate packet; no other
repository was touched; the generator was not re-run into another repo's tree.
