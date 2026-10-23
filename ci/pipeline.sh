#!/usr/bin/env bash
# ci/pipeline.sh — cafe's pipeline, on the instabox backend or any other.
#
# A backend's job is only to provide an environment and call this script; the
# gate is this repository's. `bin/prime` is the gate a developer runs —
# download, build, the lint CI runs, the counted suite — and this file is the
# wiring that Actions used to hold, moved into the repository where it can be
# run by hand, by a sandbox, or by a runner.
#
# WHERE THE FACTS COME FROM: `ci/ci.yml` IN THIS REPOSITORY.
# That file is the source of truth, and this file follows it: the gate
# entrypoint (`bin/prime`, the `gate` job's "the gate a developer runs" step),
# the kit lint checkout beside the tree (the `kit (lint config)` step —
# sparse, never copied), the linter at mise.toml's pin (the
# `golangci-lint, the version mise.toml pins` step), and the lockfile guard.
# When the workflow changes, change this file with it.
#
# WHAT THE BACKEND PROVIDES (kit's backends/README.md is the contract):
# WORKSPACE, GH_TOKEN, CI. This pipeline needs nothing else: cafe is a CLI,
# it serves nothing, and its suite touches no database and no network (the
# live tier skips without a container runtime, loudly, and is counted).
set -euo pipefail

# ---------------------------------------------------------------------------
# cafe
# ---------------------------------------------------------------------------
# `run from anywhere`: the repository root is derived from this file, not from
# the caller's cwd. The agent resets the checkout to the fetched commit before
# calling us, and the pipeline does the same itself so that a hand-run is
# identical to a polled one.
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_DIR"
REPO_NAME="$(basename "$REPO_DIR")"

WORKSPACE="${WORKSPACE:-$(cd "$REPO_DIR/../.." && pwd)}"
BRANCH="${CI_BRANCH:-master}"
export CI="${CI:-1}"
export WORKSPACE

log() { printf '\n== %s\n' "$1"; }

log "pipeline: $REPO_NAME at $REPO_DIR"

# ===========================================================================
# THE GATE. `bin/prime` — the entrypoint `ci/ci.yml`'s `gate`
# job names in its "the gate a developer runs" step. Not a CI-only variant of
# it: kit's shared `go` job runs `go mod download`, `go build ./...` and
# `go test ./...` as three steps of its own, and a CI-only variant of the gate
# is worse than no gate at all — two commands that can disagree, one of which
# nobody runs locally.
# ===========================================================================
GATE=(./bin/prime)

# ===========================================================================
# PATHS THIS REPOSITORY'S CI ADDS. The one `echo … >> $GITHUB_PATH` line in
# `ci/ci.yml`: the `golangci-lint` step installs the binary
# into `$HOME/.local/bin` because `bin/prime` exits 127 without it. The
# pipeline installs the same binary the same way below, so the same directory
# belongs on PATH here.
# ===========================================================================
PATHS=("$HOME/.local/bin")

if [ "${#PATHS[@]}" -gt 0 ]; then
  for d in "${PATHS[@]}"; do
    # One entry per directory, in the order the workflow appends them: PATH is
    # searched front to back and a later entry must not shadow an earlier one.
    export PATH="$d:$PATH"
  done
fi

log "the toolchain the repository pins"
# `mise install` materializes the versions in mise.toml (go 1.25.0, the pin
# `internal/ci` asserts against the literal CI passes to kit's reusable
# workflow); `mise exec` runs the gate under them, so a developer, a runner
# and this sandbox all use the same compiler. When there is no mise.toml, or
# no mise, the gate runs on whatever this machine ships — which is what the
# workflow does on a runner without the setup step, and is reported rather
# than hidden.
if [ -f mise.toml ] && command -v mise >/dev/null 2>&1; then
  mise install
  GATE=(mise exec -- "${GATE[@]}")
elif [ -f mise.toml ]; then
  echo "pipeline: mise.toml is present and mise is NOT on PATH — the gate runs on the machine's own toolchain"
fi

log "kit's lint config, which bin/prime refuses without"
# ci.yml's `kit (lint config)` step: kit's `lint/golangci.yml`, sparse-checked
# out beside the repository at `.kit/lint/golangci.yml`. The config is NOT
# copied — kit's own header says why ("Not copied into a service repo") and a
# copy is a second policy to drift. `bin/prime` resolves it from `.kit/` or
# from kit beside the tree (`../kit`); on a sandbox the sibling checkout may
# already sit at `$WORKSPACE/anywaye/kit`, so link it rather than cloning a
# second copy. When neither exists the gate fails with the message it already
# carries, rather than running the linter with no config (which exits 0 on a
# file the config rejects).
if [ ! -f .kit/lint/golangci.yml ] && [ -f "$WORKSPACE/anywaye/kit/lint/golangci.yml" ]; then
  mkdir -p .kit/lint
  ln -sfn "$WORKSPACE/anywaye/kit/lint/golangci.yml" .kit/lint/golangci.yml
  echo "pipeline: linked kit's lint config from the sibling checkout"
fi

log "golangci-lint, the version mise.toml pins"
# ci.yml's `golangci-lint, the version mise.toml pins` step: `bin/prime` runs
# `golangci-lint run` over kit's config and exits 127 when the binary is not
# on PATH — the refusal working, not a defect. `mise install` above provides
# the pinned version; this step is for a machine whose gate still cannot find
# it, and it reads the version out of mise.toml rather than retyping it.
if ! command -v golangci-lint >/dev/null 2>&1; then
  version="$(sed -n 's/^"golangci-lint" *= *"\(.*\)"/\1/p' mise.toml)"
  if [ -z "$version" ]; then
    echo "pipeline: mise.toml pins no golangci-lint, and bin/prime refuses to run without the binary." >&2
    exit 1
  fi
  arch="$(uname -m)"
  case "$arch" in
    x86_64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
  esac
  base="https://github.com/golangci/golangci-lint/releases/download/v${version}"
  archive="golangci-lint-${version}-linux-${arch}.tar.gz"
  tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/caf-lint.XXXXXX")"
  trap 'rm -rf "$tmpdir"' EXIT
  curl -sSfL "$base/$archive" | tar -xz -C "$tmpdir"
  mkdir -p "$HOME/.local/bin"
  install -m 0755 "$tmpdir/golangci-lint-${version}-linux-${arch}/golangci-lint" "$HOME/.local/bin/golangci-lint"
  trap - EXIT
  rm -rf "$tmpdir"
  golangci-lint --version
else
  golangci-lint --version
fi

log "the gate"
# The command is a list, so a multi-word gate works without eval and nothing a
# project writes here is re-parsed.
"${GATE[@]}"

log "the gate left the lockfiles alone"
# ci.yml's `the gate left the lockfiles alone` step: go.sum is the lockfile
# and bin/prime's first step moves it on a tree with no go.sum; go.mod is
# guarded too, because a go.mod that moved is the worse drift and costs the
# same two words.
if ! git diff --exit-code -- go.mod go.sum; then
  echo "pipeline: bin/prime moved go.mod or go.sum; the commit and the lockfile disagree" >&2
  exit 1
fi
echo "pipeline: go.mod and go.sum unchanged by the gate"

# ---------------------------------------------------------------------------
# NO SHIP STEP, and that is the point. cafe is a CLI, not a service: there is
# no chart/Chart.yaml in this tree, and releases go through
# `ci/release.yml` (goreleaser, at mise.toml's pin) on a tag
# push — a path this backend does not run and this file does not reimplement.
# The gate is the whole run. A service with a chart keeps the template's
# publish block (`./bin/publish --service-dir .`) instead of this comment.
# ---------------------------------------------------------------------------
if [ "${CI_DRY_RUN:-0}" = "1" ]; then
  log "gate passed; CI_DRY_RUN=1, not shipping"
  exit 0
fi

SHIPPED=""
echo "pipeline: no chart/Chart.yaml — nothing to publish; the gate is the whole run"

# ---------------------------------------------------------------------------
# the last line a log tail reads
# ---------------------------------------------------------------------------
# Naming WHAT shipped and the COMMIT it was built from is the whole point of
# this line: `agent[<project>]: shipped <sha>` says the pipeline returned 0,
# and this says what that meant. A run that shipped nothing says so.
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
if [ -n "$SHIPPED" ]; then
  log "pipeline: shipped $REPO_NAME $SHIPPED at $COMMIT"
else
  log "pipeline: passed $REPO_NAME at $COMMIT (nothing published)"
fi
