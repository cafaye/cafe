# REPORT — box-init-complete: `cafe box init` emits the complete install command

Branch `feat/box-init-complete`, worktree `/Users/kaka/Code/any/moon/wt/box-init`
(cafe only; no other repository was committed to).

## The packet

`cafe box init` printed a bare `curl -fsSL https://cafaye.com/box/install | bash`.
The installer fails fast **by name** — one refusal per run — so a stranger
following the printed path met `KIT_SUBSTRATE_ACME_EMAIL is not set`, re-ran,
met `KIT_SUBSTRATE_DATABASES is not set`, and so on, with no way to see the whole
set in advance. The command now emits the whole set, parameterized, as one
copyable block, plus the enroll step.

## What changed

- `internal/cli/box_init.go`
  - `boxInstallInputs` — the single table of every `KIT_SUBSTRATE_*` input the
    installer reads, each marked `required` / `optional` / `default`, with a
    one-line meaning, a safe value, and a `[replace]` / `[keep]` tag.
  - `renderInstallCommand()` — the copyable block (`export`s then the served
    one-liner), built from that table so the block cannot disagree with it.
  - `renderInstallProse()` — the stderr walkthrough (required / optional /
    defaults), then the block, then the enroll step.
  - `boxInitDoc` gains `inputs[]` (name, kind, meaning, value/default); the
    existing `install_url` / `install_command` / `enroll_command` fields remain.
  - Help text rewritten; `README.md` gains the missing `cafe box init` row.
- `internal/cli/box_init_test.go` — the pins (see below).
- `gate.yml` + `caf.lock` — suite floor 1489→1496, subtests 1290 (unchanged,
  no subtests added), re-pinned by `cafe gate floors --write .`.

## THE LIST SOURCE, exactly

The authority is **`cafaye/kit`'s `templates/substrate/install.sh`** — the bytes
the control plane serves at `https://cafaye.com/box/install`. The required rows
mirror its `kit_substrate_refuse "<NAME>" …` calls; the default rows mirror its
`${KIT_SUBSTRATE_…:=…}` lines. Read at kit's main checkout
`/Users/kaka/Code/any/moon/cafaye/kit` (read-only; not modified).

**cafe cannot read it at runtime** — it is another repository and it is served
over the network, which is exactly what `cafe box init` promises not to touch
(deterministic, no network). So the list is copied **by hand into one place**,
`boxInstallInputs` in `internal/cli/box_init.go`, with a comment naming the
installer as the authority. That is the brief's "if you cannot, say so and keep
the list in one place" branch, stated at the code site and in the help text.

The required set (the installer refuses, every live run, no default):
`KIT_SUBSTRATE_ACME_EMAIL`, `KIT_SUBSTRATE_DATABASES`,
`KIT_SUBSTRATE_SMTP_HOST`, `KIT_SUBSTRATE_SMTP_USERNAME`,
`KIT_SUBSTRATE_SMTP_PASSWORD`,
`KIT_SUBSTRATE_COURIER_INBOUND_RESEND_SECRET`.

Deliberately **not** required, and this is a judgement worth recording:
`KIT_SUBSTRATE_BOX_ID` and `KIT_SUBSTRATE_REGISTRY_CAPABILITY`. `install.sh`
step 5 probes GHCR *first* and demands them **only on the capability path** — when
an artifact is private (`install_registry_pull_secret`: "the anonymous path … no
box id, no capability, no exchange"). The fleet's artifacts are public, so a
standard install needs neither; the brief's own measurement annotates the old
box-id refusal "(before billing went public; now anonymous)". They are emitted as
`optional` with a note that they are needed only for a private image. OIDC
issuer/login-UI (all-or-nothing) and `APP_BASE_URL` (unset = the supported
no-mail path) are likewise `optional`. Defaults named, not demanded:
`BOX_URL`, `REGISTRY_URL`, `SMTP_ADAPTER= smtp`, `SMTP_PORT=587`,
`POSTGRES_EXTENSIONS=vector`, `IDENTITY_MFA_ENCRYPTION_KEY` (generated),
`IDENTITY_OAUTH_ENCRYPTION_KEY` (unset), `MAIL_FROM`/`MAIL_FROM_NAME`.

## The pins (`internal/cli/box_init_test.go`)

- `TestBoxInitRequiredSetIsTheInstallersRefusals` — the required set the table
  emits equals a **hand-written** `boxInitRequiredWant`. Hand-written on
  purpose: a table-derived assertion would pass if an entry were silently
  dropped, because the renderer would drop with it.
- `TestBoxInitEmitsEveryKnownInput` — every table input appears in the emitted
  command **and** is tagged `[replace]`/`[keep]`.
- `TestBoxInitRequiredInputsAreLiveExports` — each required variable is a live
  `export NAME=` line, never a comment (a name in a comment is not a value).
- `TestBoxInitCommandRunsOnlyTheServedScript` — exactly one `curl`, at
  `boxInstallURL`; no `git`/`wget`/`ssh`/`go get`.
- `TestBoxInitExamplesCarryNoSecret` — no `cap_`+64hex, no `whsec_`+tail, no PEM,
  no bearer token; the two secret examples are `<angle-bracket>` placeholders.
- `TestBoxInitDocumentListsEveryInputWithItsKind` and
  `TestBoxInitNamesTheEnrollStep` — the machine document and the enroll spelling
  (`--token - --box …`, capability on stdin, not argv).
- Kept: the original determinism / canonical-URL / help tests.

## Red proof (Q2)

Dropped `KIT_SUBSTRATE_SMTP_PASSWORD` from `boxInstallInputs`. Four tests went
red naming it: `TestBoxInitRequiredSetIsTheInstallersRefusals`,
`TestBoxInitRequiredInputsAreLiveExports`, `TestBoxInitExamplesCarryNoSecret`,
`TestBoxInitDocumentListsEveryInputWithItsKind`. Restored; green.
(Note: `TestBoxInitEmitsEveryKnownInput` stays green under that mutation because
it iterates the table — which is why the hand-pinned list exists.)

## Gate

`mise exec -- ./bin/prime` → **green**: `cafe: 33 packages, 1496 top-level
passes, 1290 subtest passes, 0 failures, 8 skips` and the live-tier line intact.
`gofmt -l .` prints nothing; `go vet ./...` is clean. Narrow:
`go test ./internal/cli/...` green (62s).

One honest note: the first `cafe gate floors --write .` run reported "3 failures"
before it wrote the floor. Those were the repo's own ratchet/CI tests rebelling
against the *stale* floor while 7 new tests existed (the deliberate friction
AGENTS.md documents); the re-run after the writer set 1496 is 0 failures.

## What I ruled out

- Folding `box_id`/`capability` into the required set. They are capability-path
  only; making them required would demand a credential for a public pull, which
  is the exact ritual `install.sh` step 5 was written to stop.
- Reading the installer at runtime. It is served, not vendored, and this command
  promises no network; a fetch would also make the output non-deterministic.
- Emitting `VAR=… curl … | bash` (env prefix) — the prefix binds to `curl`, and
  the installer runs in the `bash` on the right of the pipe. The `export`s
  followed by the one-liner are the shape that works, and it is the substrate
  README's own example.

## Website follow-up the manager should dispatch

The served `/box` page still shows the bare one-liner. Website repo
`cafaye/website` (currently on `master`; **not touched**):

- `src/app/box/enroll-capability.tsx:53` — `INSTALL_COMMAND = "curl -fsSL
  https://cafaye.com/box/install | bash"`.
- `src/app/box/two-paths.tsx:119` — the command block.
- `src/app/box/page.tsx` — the page, pinned by `src/app/box/page.test.tsx`
  (asserts the bare string at :247, :301, :342).

Dispatch a website packet to render the same parameterized block (required /
optional / defaults + the enroll step), and note the drift risk: the website is
TypeScript and cannot import the Go table, so it becomes a **second** copy of the
input list. Cheapest honest fix is a website test that asserts its required set
equals the six names above, or a build step that captures `cafe box init`'s
`inputs[]`. `src/app/box/install-drift.test.ts` is where that belongs.

## Successor's first move

Nothing is outstanding on the cafe side: branch committed, gate green. If
continuing, the natural next packet is the website mirror above; the list to
mirror is `boxInstallInputs` in `internal/cli/box_init.go`.
