# REPORT — roll/c4-wipe

`cafe box wipe <host>`: the inverse of `cafe box setup`, as a command rather than
a hand-rolled SSH script. Branch `roll/c4-wipe`, repo `cafaye/cafe`.

## The verb's shape

`cafe box wipe <host> [--dry-run] [--yes] [--json]` with the same six SSH flags
`cafe box setup` takes (`--user`, `--port`, `--key`, `--password`, plus
`--range`/`--token` are setup-only). It is a verb under `cafe box`, registered in
`boxVerbs`, and it reaches the host through `internal/setup`'s `Host` seam —
`setup.SSHHost`, the same transport and the same `$CAF_BOX_PASSWORD` askpass
path as setup. There is no second SSH implementation.

It runs in the same three phases as setup:

1. **PROBE** (read-only, so a dry run runs it too): `probeSuperuser` (root or
   passwordless sudo), then a **fingerprint** — a shell loop that prints every
   cafaye artifact it finds under `/usr/local/bin/cafe`, `/etc/cafe`,
   `/root/.config/cafe`, `/var/lib/kit`, `/var/lib/cafaye`. An empty answer is a
   host this command did not set up, and it is **refused here**, before the
   confirmation and before a single removal.
2. **CONFIRM** once, after the probes and before the first removal. The prompt
   names every path it will remove (the plan's own `wipeRemovablePaths()`) and
   every path it will keep (`SSHAuthorizedKeys`, `CafayeReleases`). `--yes`
   supplies a confirmation that returns true; the library's nil `Confirm` is a
   **decline**, not an approval.
3. **WALK**: the ordered check-and-apply table. Every step checks first; a check
   that passes runs nothing, which is what makes a second wipe a no-op. After a
   removal it asks the check **again** — an exit 0 is the command's opinion, and
   the host's state is the authority. A removal that reports success and leaves
   the artifact is a refusal that names the step.

`--dry-run` prints the plan (the same table the real run walks) and removes
nothing. stdout carries the document; stderr carries the sentence and the
"kept …" line.

## Removed / kept, grounded in the live box

Read-only inspection of `contabo` on 2026-10-13 (SSH, no changes):

| artifact | live evidence | wipe step |
|---|---|---|
| `cafe-boxd.service` | `systemctl list-unit-files` → `cafe-boxd.service enabled enabled` | `daemon-stopped` (disable --now), `unit-removed` |
| `/usr/local/bin/cafe` | `-rwxr-xr-x 1 root root 29642667 Oct 13 15:03` | `cafe-binary-removed` |
| `/etc/cafe` | `box-identity.json` (107 B) | `cafaye-state-removed` |
| `/root/.config/cafe` | `box.json`, `box-events.jsonl`, `keyring/` | `cafaye-state-removed` |
| `/var/lib/kit` | `substrate-receipt.json` (355 B) | `cafaye-state-removed` |
| `/var/lib/cafaye/{bin,substrate,edge}` | `substrate/` present; `bin`/`edge` not on this box | `cafaye-state-removed` (each named explicitly) |
| k3s | `/usr/local/bin/k3s` (81,543,330 B), `k3s-killall.sh`, `k3s-uninstall.sh`, `kubectl → k3s`, `k3s.service enabled`; `/etc/rancher`, `/var/lib/rancher`, `/var/lib/kubelet`, `/root/.kube` | `k3s-removed` |
| Docker | 29.9.0; `/usr/bin/docker`; `docker.service`/`.socket`, `containerd.service`; `/etc/docker`, `/var/lib/docker`, `/var/lib/containerd`; `apt` pkgs `docker-ce`, `docker-ce-cli`, `containerd.io`, `docker-buildx-plugin`, `docker-compose-plugin`, `docker-model-plugin`, `docker-ce-rootless-extras`; `/etc/apt/sources.list.d/docker.list` | `docker-removed` |

| kept | why |
|---|---|
| `/root/.ssh` (authorized_keys) | the operator's access; no step references it and a test asserts no command does. |
| `/var/lib/cafaye/releases` | the deploy path's released artifacts — what the box was *serving*, not an installation fact. `/var/lib/cafaye` is removed subdirectory by subdirectory (`bin`, `substrate`, `edge`), **never wholesale**. |
| everything else | the wipe is a closed set of cafaye's own paths; `/srv/customer/data` in the test stands for a stranger's file and survives. |

Deliberate omission: **`/usr/local/bin/kubectl`**. It is a name every cluster
tool writes into, so the explicit list stops at names that belong to exactly one
product; k3s's own uninstall script removes the symlink if it created it. Same
category as the SSH keys.

## Tests

- `internal/setup/wipe_test.go` (12 top-level):
  - the first wipe removes everything in order (and the daemon is stopped before
    the binary goes, asserted by command position);
  - idempotence **twice** — the second and third wipes run zero destructive
    commands, asserted as a count;
  - `--dry-run` removes nothing and plans every step, in the table's order;
  - a stranger's host (k3s + Docker, no cafaye artifact) is refused at
    `box-fingerprint`, names "not a box", and removes nothing — with the control
    that ONE cafaye artifact (a lone release) makes it proceed;
  - the confirmation: a "no" removes nothing and the prompt names the host, the
    keys and the releases;
  - a confirmed wipe runs to completion; a nil confirmation seam declines;
  - a removal that reports success and leaves the artifact is refused (the
    post-apply re-check, with a lying host as its red control);
  - the plan is pure data; a wipe with no host refuses; and **every `rm`
    argument is in `wipeRemovablePaths()`**, with `SSHAuthorizedKeys`,
    `CafayeReleases`, `/usr/local/bin/kubectl`, `/` and `/var/lib/cafaye` as
    negative controls.
- `internal/cli/box_wipe_test.go` (5 top-level), through the real router with a
  simulator host: `--yes` reaches the host's destructive half (6 applies),
  declining exits non-zero with "nothing was removed", `--dry-run` asks nothing
  and removes nothing, a stranger host is refused *before the prompt*, and the
  verb is wired with its help saying what it keeps and refuses.
- `TestBareBoxIsAQuestionNotAMistake` updated for the new usage line.

No SSH and no network in any of them.

## The gate

`./bin/prime` (log `/tmp/gate-roll-c4-wipe-mine.log`):

```
cafe: 32 packages, 1386 top-level passes, 1248 subtest passes, 1 failures, 8 skips
cafe: live tier: 0 of 7 executed; not enabled, so the 7 live tests skipped rather than ran.
```

Exit 1, and the one failure is the pre-existing, unrelated citation failure
below. The ratchet passes: `gate.yml`'s `suite` floor is raised to **1387**
(1395 declared − 8 skips), and `caf.lock` is re-pinned for the `gate.yml` hash.

`TestEveryCitationPointsAtSomething` in `internal/secret` fails **pre-existing
and unrelated**: it cites line ranges into `cafaye/kit`'s `install.sh`, and
kit's template has drifted (named in `REPORT-roll-c3-boxd-identity.md`). This
packet touches neither `internal/secret` nor kit; the sibling
`roll-c4-gate-ergonomics` packet owns that friction.

## What I deliberately did NOT do

- I did **not** run the wipe against `contabo` (the manager's cycle depends on
  its state); the live evidence above is all read-only.
- I did not remove `/root/.ssh`, `/var/lib/cafaye/releases`, or anything under a
  bare `rm -rf /var/lib/cafaye`. A test walks every `rm` argument against the
  closed set and would go red if a step widened it.
- I did not remove `/usr/local/bin/kubectl` (argued at the code site).
- I did not add a second SSH transport, a `--force` bypass, or a hidden
  confirmation default: nil `Confirm` is a decline.
- I did not edit `cafaye/kit` or the pre-existing `internal/secret` citation
  failure.
- I did not touch the shared `/tmp/gate-*.log` path of another worker; my gate
  run uses `/tmp/gate-roll-c4-wipe-mine.log`.
