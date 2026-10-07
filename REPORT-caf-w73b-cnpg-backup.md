# REPORT — caf-w73b: the CNPG backup engine

**Branch:** `worker/caf-w73-cnpg-backup`
**Base commit:** `17006c8` ("backup: the CNPG plan and runner — the engine's own argv, as a checkpoint")
**Head:** `d889abf` ("gate: the floors the CNPG tests moved")
**Gate:** `bin/prime` exit **0** — `caf: 16 packages, 817 top-level passes, 984 subtest passes, 0 failures, 8 skips`

## The refactor decision: finished, not reverted

The four dirty files (`cnpg.go`, `plan.go`, `run.go`, `runner.go`) were a **coherent,
complete refactor**: the recovery manifest moved off the `Plan` and the runner's
private fields and onto the `Restore` step's own `Files`. The rationale is sound and
written into the code: the runner is handed a `Step`, not a `Plan`, so a file the
step writes is part of what the step is; and a dry run prints the same bytes from
the same value, so the document on the screen and the document on disk are one fact.

The refactor was **not** half-applied. What was broken was the test files: an
interrupted merge had left **duplicate test functions** — `go vet` failed with
`TestTheCNPGPlanRunsItsStepsInOrder redeclared` and `TestTheEngineFlagSelectsTheCNPGEngine
redeclared`. The tree did not compile. I finished the packet by removing the
duplicates (keeping the better-commented version of each), deduplicating the
redundant near-duplicates in `run_test.go`, and fixing one real bug the tests caught
(see below). The refactor itself is the predecessor's, intact.

## One bug found and fixed

`internal/cli/backup.go`'s `runner()` short-circuited when a test injected a runner,
bypassing `RunnerFor`'s engine validation — so `--engine bogus` under a fake runner
would have silently run the default engine. `TestAnUnknownEngineIsAUsageErrorAndRunsNothing`
caught it (red: `err = <nil>, want errUsage`). The fix validates the engine *before*
honouring the injection. The code's own comment already claimed this behaviour; the
implementation now matches.

## The engine's argv — the subcommands and why

The CNPG engine drives `kubectl cnpg` and `kubectl` only. Every command the plan
runs, in order:

| step | argv | why |
|---|---|---|
| Preflight | `kubectl cnpg version` | the plugin's own version probe; fails if the plugin is not installed. The CNPG shape of `kamal version`. |
| Backup | `kubectl cnpg backup identity` | takes one real backup of the `identity` cluster now, creating a `Backup` resource. |
| WaitBackup | `kubectl wait --for=jsonpath={.status.phase}=completed backup/identity-drill --timeout 10m0s` | a backup is asynchronous; a drill that restores it before it is finished proves nothing. |
| Restore | `kubectl apply -f …/config/cnpg-recovery-identity_drill.yaml` | **the restore, declaratively** — `kubectl cnpg` has no `restore` subcommand. |
| WaitReady | `kubectl wait --for=jsonpath={.status.phase}=running cluster/identity_drill --timeout 10m0s` | a restore is asynchronous; an assertion against a half-started cluster proves nothing. |
| Assert | `kubectl cnpg psql identity_drill -- -v ON_ERROR_STOP=1 -c SELECT CASE WHEN (SELECT count(*) FROM users) = 0 THEN 1/0 ELSE 1 END;` | one query that divides by zero if any named table is empty; the exit status is the verdict. |
| Drop | `kubectl delete cluster identity_drill` | deletes the scratch cluster on every path, including a failed drill. |

The two waits are blocking `kubectl wait` commands rather than caller-side polls, so
the phase vocabulary stays in kubectl where it belongs. The backup name
(`identity-drill`) and scratch cluster (`identity_drill`) are derived from the
service name deterministically, so a dry run prints the same names a real run uses.

The recovery manifest the Restore step applies:

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: identity_drill
spec:
  bootstrap:
    recovery:
      backup:
        name: identity-drill
  instances: 1
```

## The refusals — what survives, what is gone

**Survive** (about the drill, not about Kamal):
- a restore with no `--table` — a restore whose only proof is that a command exited zero passes on an empty database; `TestTheCNPGScratchClusterDefaultsToTheServiceAndATableIsRequired`.
- a scratch cluster name that is not a plain identifier (space, quote, leading digit) — it is passed to kubectl as a resource name; `TestTheCNPGPlanRefusesAScratchNameThatIsNotAPlainIdentifier`.
- `--env` — a Kamal destination, and the CNPG engine has no destination; `TestTheCNPGEngineRefusesAnEnvironment`.

**Removed deliberately, and named:** the two-file contract refusal (`config/deploy.yml`
vs `config/kamal-backup.yml`). The CNPG substrate has no such pair — the cluster is a
fact about the Kubernetes namespace, not two rendered documents. `TestTheCNPGEngineHasNoTwoFileContractToCheck`
is the test that says it is gone and why: the cycle runs in a directory with no
configuration files at all, and the plan has no `Resolve` step.

**Never caf's, not carried:** the production-name refusal. It is kamal-backup's
(`Config#production_like_target?`), and the CNPG engine does not use kamal-backup.
caf keeps no copy of a rule about which database gets dropped.

## The proofs

- **The CNPG plan's argv is asserted exactly** — `TestEveryCNPGStepsArgvIsTheOneTheCycleRuns`,
  seven rows, the argv not a description of one. A dry run prints `Step.Command()`
  and a real run executes `Step.Args()`, so a test on the plan is a test on both.
- **The engine selection is tested** — `TestRunnerForSelectsTheEngineTheConfigurationNames`
  (empty → Kamal, `kamal` → Kamal, `cnpg` → CNPG, unknown → `ErrUsage`) and the
  command-layer tests `TestTheEngineFlagSelectsTheCNPGEngine` (real wiring) and
  `TestACNPGDryRunThroughTheCommandExecutesNothing` (fake runner, zero `Run` calls).
- **Every surviving refusal has a test; the removed one has a test that says it is
  gone and why** — see the table above.
- **`--dry-run` runs no command** — `TestACNPGDryRunExecutesNothing` (a fake runner
  records zero `Run` calls) and `TestACNPGDryRunWritesNoFile` (the tree is unchanged).
- **Red before green** — the tree as found did not compile (duplicate test functions,
  captured in `go vet` output); the `runner()` bug was caught red by
  `TestAnUnknownEngineIsAUsageErrorAndRunsNothing`. Both fixed, the suite is green.

## The dry-run transcript

`caf backup --engine cnpg --table users --dry-run identity`, on this machine with no
cluster and no kubectl:

```text
this is what caf backup would run:

  kubectl cnpg version
      kubectl cnpg is the backup engine; this prints its version and fails if it is not installed
  kubectl cnpg backup identity
      takes one real backup of the identity cluster now, creating a Backup resource the drill will restore from
  kubectl wait --for=jsonpath={.status.phase}=completed backup/identity-drill --timeout 10m0s
      waits for the identity-drill backup to complete, because a backup is asynchronous in Kubernetes and a drill that restores it before it is finished proves nothing
  kubectl apply -f …/config/cnpg-recovery-identity_drill.yaml
      applies the rendered recovery Cluster, which bootstraps identity_drill from the identity-drill backup — the restore, declaratively
    and writes …/config/cnpg-recovery-identity_drill.yaml:
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: identity_drill
spec:
  bootstrap:
    recovery:
      backup:
        name: identity-drill
  instances: 1

  kubectl wait --for=jsonpath={.status.phase}=running cluster/identity_drill --timeout 10m0s
      waits for the identity_drill cluster to become ready, because a restore is asynchronous and an assertion run against a half-started cluster proves nothing
  kubectl cnpg psql identity_drill -- -v ON_ERROR_STOP=1 -c SELECT CASE WHEN (SELECT count(*) FROM users) = 0 THEN 1/0 ELSE 1 END;
      runs the check whose exit status is the drill's verdict: every named table must hold rows in identity_drill after the restore
  kubectl delete cluster identity_drill
      deletes the identity_drill cluster on every path, including a failed drill, so nothing is left to restore into by accident

Dry run, and nothing was executed: not the preflight a real run makes
first, and none of the steps above. No backup was taken, no cluster was
created or deleted, and no file in … was written — the recovery
manifest above is the document the restore step would apply, printed
rather than written.

Back it up for real with:
  caf backup --engine cnpg --yes identity
```

Exit 0. No file written, no command executed.

## Kamal parity

The Kamal path is the default and is **byte-identical**. `git diff 9cbc5db..HEAD`
touches none of `contract.go`, `config.go`, `script.go`, `project.go` or
`internal/deploy/`. No existing test function was modified or removed — the diff
into the test files is additions only. The whole suite passes against the new
boundary, which is the proof the seam did not move.

## The gate

`bin/prime` exit **0**: `caf: 16 packages, 817 top-level passes, 984 subtest passes,
0 failures, 8 skips`. The floors in `gate.yml` were raised to match — suite 794 →
817, subtest 966 → 983 — and `caf.lock` was re-run. `go vet ./...` and `gofmt -l .`
are clean. The 8 skips are the seven live tests and the re-exec'd child, as
declared; none of the new tests is env-gated.

## What this packet did not prove

No restore was exercised against a real cluster. The proofs are the plan tests and
the dry run, which is what the spec allows. A real drill needs a CloudNativePG
cluster with the `kubectl cnpg` plugin installed, and the live tier
(`bin/prime --live`) is where that would go.

## The seam and m76

The seam — the plan belongs to the runner, not the caller — is what makes m76
(caf's Argo deploy) a new `deploy.Runner` implementation rather than a second copy
of the caller's orchestration. m76 fills the same seam this packet filled for
backup: a pure plan, a runner that owns its argv, and the caller keeps the parts
that are caf's.
