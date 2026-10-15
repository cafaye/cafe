# REPORT — release-machinery

## What this is

`cafe` had a `.goreleaser.yml`, a `CHANGELOG.md` and a README that told people
to run `go install github.com/cafaye/cafe/cmd/cafe@latest` — and **no git tag
had ever existed**, so `@latest` resolved to nothing and the documented install
did nothing. This packet lands the machinery that makes a tag the only human step
to a real GitHub Release: a tag-driven workflow, a goreleaser config whose header
is true and whose notes are the changelog of record, and the version discipline
written down. **The tag itself was deliberately not created** — that is the
manager's step, below.

Branch `feat/release-machinery`, worktree `/Users/kaka/Code/any/moon/wt/release-cafe`.
One repository was touched: `cafe`. No other worktree was used.

---

## The exact tag command for the manager

After this branch is merged and `bin/prime` is green on the default branch:

```sh
git switch master && git pull --ff-only
bin/prime
git tag -a v0.1.0 -m "cafe v0.1.0"
git push origin v0.1.0
```

Then watch **Actions → release → the `goreleaser` job**. A green run creates
`https://github.com/cafaye/cafe/releases/tag/v0.1.0`.

**Precondition for `@latest` to work:** the repository must be public (it is), so
the Go module proxy can read the tag. After the tag is pushed and the proxy has
it, `go install github.com/cafaye/cafe/cmd/cafe@latest` resolves to `v0.1.0` and
installs `cafe 0.1.0 (commit …)`.

### Release-notes source

goreleaser's **generated notes** (`changelog:` in `.goreleaser.yml`), read from
`git log` — not GitHub's API (`use: git`) — between the previous tag and this
one, grouped and filtered (docs/tests/ci/chore/report and merge commits are
excluded; Features / Bug fixes / Other changes). They are the
**changelog of record for the tag**. `CHANGELOG.md` stays the human log and a
release does not rewrite it.

> **First-release caveat, measured.** `v0.1.0` is the first tag, so there is no
> previous tag and goreleaser's changelog will cover the **entire commit
> history**. That is standard goreleaser behaviour for a first tag, not a defect
> in this config — but the first Release's notes will be long. Every subsequent
> tag's notes start at the previous tag.

### What the release will contain

For each of `darwin`, `linux`, `windows` × `amd64`, `arm64` (six targets):

- `cafe_<version>_<os>_<arch>.tar.gz` (a `.zip` on Windows), each containing the
  `cafe` binary, `README.md` and `LICENSE`;
- `cafe_<version>_checksums.txt` — the sha256 of every archive;
- the generated notes above.

---

## 1. The two files that are the packet

### `.github/workflows/release.yml` (new)

`on: push: tags: ['v*']`, one job, least privilege:

- **workflow-level `permissions: contents: read`**, raised to `contents: write`
  **only at the `release` job** — the job that creates the Release and uploads
  artifacts. That write is the workflow's own `GITHUB_TOKEN`; no PAT and no
  stored secret.
- `actions/checkout@v7` with **`fetch-depth: 0`** — goreleaser's changelog needs
  the history between the previous tag and this one, and Actions' default
  shallow clone would silently produce a one-line changelog.
- `actions/setup-go@v7` with `go-version-file: go.mod` — the same pinned
  toolchain `ci.yml`'s `gate` job sets up, read from the same source.
- a step that reads the **goreleaser pin out of `mise.toml`** into `$GITHUB_ENV`,
  mirroring exactly how `ci.yml` reads `golangci-lint`'s pin. No `@latest`: the
  action is `goreleaser/goreleaser-action@v6` with `version:` from that pin.
- `args: release --clean` (which also removes the previous run's `dist/`).

### `.goreleaser.yml` (updated)

- The "build-only … until the install story has an owner" header is replaced by
  what a release contains and the one-sentence split between the generated notes
  (changelog of record) and `CHANGELOG.md` (human log). No Homebrew/Scoop/Docker
  targets, with the owners-missing reason kept.
- New `changelog:` section: `use: git`, `sort: asc`, `filters.exclude`
  (docs/doc/test/tests/ci/chore/report and merge commits), and `groups` for
  Features / Bug fixes / Other changes. The "Other changes" bucket is explicit
  because this repository's commits are often `scope: …` rather than
  conventional-commit prefixed, and a note that named only two groups would drop
  them.
- **`before.hooks`: `go mod tidy` → `go mod download`.** See §3 — this is the one
  non-obvious change and it was forced by a measurement, not a preference.
- `archives` and `checksum` were already present and correct; nothing needed
  adding there. (`cafe_<version>_checksums.txt`, six lines.)

### `mise.toml` (updated)

Pins `"goreleaser" = "2.18.3"` and says why in its header: one source for the
version, which the release workflow reads. `mise install` materialises it
locally, so a local `goreleaser release --snapshot --clean` exercises the same
config CI runs.

### `README.md` (updated)

- `## Install` now says `@latest` resolves to the newest tag and points at the
  Releases page.
- New `## Release` section: the tag **is** the version (a Go CLI has no version
  file), the exact tag commands, what lands, the notes-vs-`CHANGELOG.md` split,
  and `go install …@vX.Y.Z`, with the honest "no Homebrew/Scoop/Docker until
  their owners exist; GitHub Releases + `go install` are the install paths" line.
- The roadmap's `Homebrew and Scoop install targets` box is still **unchecked**,
  now with the install paths that do exist today.

---

## 2. Version wiring — verified, not assumed

`cmd/cafe/main.go` declares `var version` / `var commit` in `package main`, and
`internal/cli` defaults both when they are empty (`0.0.0-dev` / `unknown`).
`.goreleaser.yml` sets `-X main.version={{ .Version }}` and
`-X main.commit={{ .ShortCommit }}`. Three proofs, increasing in strength:

1. **Direct ldflags** (the symbol is `main.version`):
   `go build -ldflags "-X main.version=v0.1.0 -X main.commit=abc1234" …` →
   `cafe v0.1.0 (commit abc1234)`.
2. **Through goreleaser's own snapshot build** — the binary goreleaser produced
   at `dist/cafe_darwin_arm64_v8.0/cafe` reports
   `cafe 0.0.1-next (commit 30f0e69)`. That is the actual release path: goreleaser
   computed `0.0.1-next` from the snapshot version template and injected it.
   On a `v0.1.0` tag the same path reports `cafe 0.1.0 (commit …)` (goreleaser
   trims the leading `v`).
3. The config validated: `goreleaser check` → `1 configuration file(s) validated`.

No wiring change was needed; the field names already matched.

---

## 3. What was measured, and one change it forced

### Local snapshot proof (`goreleaser release --snapshot --clean`)

goreleaser was installed through mise at the pin (`2.18.3`). Two runs:

- **Run 1** — green, `release succeeded after 1m26s`, but it left `go.mod` and
  `go.sum` **modified**. The cause was the pre-existing `before.hooks:
  go mod tidy`: on this tree it promoted `github.com/google/uuid` and
  `go.opentelemetry.io/proto/otlp` from `// indirect` to direct (both are in fact
  imported non-indirectly, by `internal/cli/box_mint.go` and
  `internal/emitter/*_test.go`) and added two `go.sum` lines.
- **The change** — `before.hooks` is now `go mod download`. The tag's `go.mod`/
  `go.sum` are the pinned module graph; `tidy` rewrites them, so a release would
  have been built from module files that are **not the ones in the tag** — the
  "a pin is of the bytes" rule the vendored schemas and `caf.lock` hold to, broken
  on the release path. `download` populates the cache and leaves the tree alone,
  so a genuinely inconsistent `go.mod` fails the build loudly instead of being
  tidied in silence.
- **Run 2** — green in 8s, and `git status` **byte-identical** afterwards. This
  is the evidence for the change.

Artifacts produced (snapshot, `dist/` is git-ignored, nothing outward):

```
cafe_0.0.1-next_{darwin,linux}_{amd64,arm64}.tar.gz
cafe_0.0.1-next_windows_{amd64,arm64}.zip
cafe_0.0.1-next_checksums.txt          # six sha256 lines
```

`tar tzf …_darwin_arm64.tar.gz` → `LICENSE`, `README.md`, `cafe`.

### The gate

`mise exec -- ./bin/prime` on HEAD:

```
cafe: 33 packages, 1489 top-level passes, 1290 subtest passes, 0 failures, 8 skips
cafe: live tier: 0 of 7 executed; not enabled, so the 7 live tests skipped rather than ran.
```

Exit 0. **This matches `gate.yml`'s floors exactly** (`1489` / `1290`), which is
the expected result: this packet adds no Go test, so no floor moved and `gate.yml`
/ `caf.lock` are unchanged. `gofmt -l .` printed nothing; `go vet ./...` exit 0.
The workflow/config YAML was parsed with a real YAML parser, and `goreleaser
check` validated the schema.

---

## 4. Findings left open (not fixed here)

1. **The tree's `go.mod` is not `go mod tidy`-clean** (pre-existing). Two
   dependencies imported directly are annotated `// indirect`, and `go.sum` is
   missing two lines that `tidy` adds. It does **not** affect `go build`,
   `go install` or the release — but it is why the old before-hook produced a
   diff. Suggested follow-up: run `go mod tidy` once, on its own, and commit the
   result. Deliberately **not** done here: it is unrelated to releasing, it
   churns `go.mod`/`go.sum`, and CI's lockfile guard only checks that `bin/prime`
   leaves them alone (it does).

2. **The first Release's notes cover the whole history** (see the caveat above).
   Standard for a first tag; no config change warranted.

3. **`goreleaser` is now a local mise tool.** `mise install` will fetch
   `goreleaser 2.18.3`. If that is unwanted, the alternative is a version literal
   in the workflow — but that is the second copy of a pin the fleet's convention
   avoids.

---

## 5. Commits

- `30f0e69` — `release: a tag push is the only human step` (workflow, goreleaser
  header + changelog, mise pin, README Release section).
- `b5098b2` — `release: build from the tag's go.mod, not a tidied one` (the
  `go mod download` hook and the measured justification).

Nothing was merged, pushed, or tagged. The branch is the baton.
