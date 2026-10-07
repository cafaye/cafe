# REPORT — caf-w76-argo-deploy: the Argo deploy engine

**Branch:** `worker/caf-w76-argo-deploy`
**Base commit:** `933ca9c` (Merge worker/caf-w73-cnpg-backup — the CNPG backup.Runner)
**Landed:** `34e698b` (predecessor's compiling partial), `9ce7c64` (proofs + two bugs), `d1109c6` (engine-selection fix)

## What the predecessor left, and what I did with it

A predecessor worked this packet for 56 minutes, made 35 real edits, and
committed nothing before being killed. Its work was on disk, uncommitted. I
read it, found it sound in design but untested in execution, and landed it in
three commits:

1. **`34e698b`** — the predecessor's compiling partial, committed the moment
   `go build ./...` went green, per the packet's standing order.
2. **`9ce7c64`** — the Argo proofs (21 tests) and two bugs the proofs caught.
3. **`d1109c6`** — a third bug the dry-run proof caught, in the CLI wiring.

The predecessor's design was kept throughout. Three defects were found and
fixed; none of them was found by reading — each was found by a proof that went
red. They are in §4 and §5.

## 1. The base commit

`933ca9c` — the m73 merge. The worktree was cut from caf master at dispatch;
`git log --oneline -3` at start named it, and the branch carries it.

## 2. m73's seam: present

m73 makes the plan runner-owned. The evidence, read before writing a line:

- `internal/deploy/runner.go:80` — `type Runner interface { Plan(dep, opts) (Plan, error); Run(ctx, dir, step, stdout, stderr) error }`. The caller asks the runner for the plan and executes the answer; it never builds argv.
- `internal/deploy/runner.go:44` — `RunnerFor(engine)` resolves the engine name to a runner.
- `internal/deploy/run.go:70` — `Run(ctx, r Runner, req Request, ...)` takes a `Runner`, not an argv. The Kamal path calls `r.Plan(dep, ...)` and executes `plan.Step(...)` by name.
- `internal/backup/kamal.go:37` — the backup engine's `Run` takes a `deploy.Step` and converts its own local `Step` to `deploy.Step{Argv: step.Argv}` — the type unification this packet's compile blocker was about.

The seam is present and runner-owned. I did not redo m73.

## 3. The engine

### The exact read command, and why it is kubectl

```
kubectl get application <service> -n argocd -o jsonpath={.status.sync.revision} {.status.health.status}
```

Argo's `Application` is a CR. `kubectl` is already the tool `DEPLOYMENT.md`'s
observe loop uses for events and logs, so reaching for it before inventing an
`argocd` CLI dependency is the evidence-backed choice. The jsonpath reads the
two facts the wait needs — the revision Argo has synced and the health it
reports — and writes nothing. The poll is read-only, which is the only kind of
cluster access a pull-only substrate permits from CI. No new tool dependency
was added.

### The selection rule, and the refusal when it is ambiguous

`caf deploy --engine <name>` selects the engine. The rule:

- `--engine ""` (the default) or `--engine kamal` → the Kamal engine, because
  the fleet runs Kamal today (`DEPLOYMENT.md`: "Kamal and kit/templates/kamal
  retire at parity, not before").
- `--engine argo` → the Argo engine, the launch substrate's.
- Anything else → `ErrUsage`, exit 2. A typo in `--engine` is the operator
  invoking caf wrongly, and the wrong answer to it is a deploy run against the
  wrong substrate — the one outcome a deploy command must never produce by
  accident. There is no silent fallback and no guessing.

The engine is validated even when a test injects a runner, so a typo exits 2
under a test's fake rather than silently running the default engine.

### The deploy's exact commits, and their repositories

The deploy lever is **the chart version** (lever 1), not an inline `helm.values`
block (lever 2). The values ride inside the OCI chart artifact — measured on
kit's merged tree, every Application is `source: {repoURL: oci://…, chart:
<service>, targetRevision: 0.1.0}` with no `helm:` block, and `DEPLOYMENT.md`
forbids a git source in as many words. So:

1. caf resolves the service repository's `chart/values.yaml` and `Chart.yaml`.
2. It bumps the image tag in `chart/values.yaml` and the version in
   `Chart.yaml`, and commits the two files **in the service repository** with a
   message naming the service, the version and the commit it was built from.
3. It pushes **the service repository**.
4. It waits for Argo to sync by polling the Application's status.

The Application's `targetRevision` must then move to the new chart version —
a **second commit, in kit**, or an operator action. caf does not make it: caf
writes commits, never the cluster, and never another repository's tree. The
sync-wait refuses to claim a deploy that leaves `targetRevision` behind — it
polls until the Application's synced revision matches the version caf committed,
and reports the status it stopped at when it does not.

Lever 2 (an inline `helm.values` block) was rejected because it does not exist
in kit today and would require a kit packet to add it; faking it would mean
deploying against a block kit does not emit. **Required follow-up, not done
here:** a kit packet that adds the `helm.values` block, if the fleet ever wants
one lever per concern instead of the chart version.

## 4. Every refusal, and the proof for each

All refusals wrap `ErrRefused` (or `ErrKubectlMissing` / `ErrUsage`), so the
CLI tells a refusal from a failed deploy by the sentinel and not the message.
Each proof is a test in `internal/deploy/argo_test.go` (or
`internal/cli/deploy_test.go`), written against the engine and watched going
red before the fix.

| Refusal | Proof |
| --- | --- |
| `chart/values.yaml` not found | `TestReadChartRefusesWhenTheChartCannotBeRead` — names the file |
| `Chart.yaml` not found | same table — names the file |
| values without `image.repository`/`image.tag` | same table — refuses rather than writing a commit that changes nothing |
| `Chart.yaml` without `version` | same table — no OCI tag to push |
| not a git repository | same table — nothing to commit, nothing to push |
| dirty `chart/values.yaml` or `Chart.yaml` | `TestTheCommitStepRefusesADirtyWorkingTree` — the release commit would mix the bump with edits nobody meant |
| dirty unrelated file | same table — **allowed**: the deploy writes and commits exactly two paths, so an unrelated edit cannot enter the commit; refusing on it would block a deploy over an edit that cannot reach the commit |
| no git remote | `TestThePushStepRefusesARepositoryWithNoRemote` — a commit that goes nowhere is a release nobody can sync |
| kubectl not installed | `TestAMissingKubectlSaysHowToInstallIt` — `ErrKubectlMissing`, names the fix |
| Argo not reachable | `TestTheSyncWaitFailsWhenArgoIsNotReachable` — the read failed, which is a fact about the cluster, not a slow sync |
| Argo never converges | `TestTheSyncWaitFailsWhenArgoNeverConverges` — reports the status it stopped at and names `targetRevision` as the cause; **a deploy that reports success without a sync is the one failure this engine exists to prevent** |
| unknown `--engine` | `TestRunnerForSelectsTheEngineAndRefusesAnUnknownOne` + `TestDeployRefusesAnUnknownEngine` — `ErrUsage`, exit 2 |

The existing Kamal refusals (a config kamal refuses, an accessory port
published on the host's interfaces, a missing kamal) keep refusing unchanged —
their tests are the proof, and the Kamal path is byte-identical.

## 5. Three bugs the proofs caught

The predecessor's engine compiled and its plan was pure, but it had never run
a command. Three defects, each found by a proof going red:

1. **Every Argo step executed the wrong program.** `ArgoRunner.run` takes a
   full argv (`argv[0]` is the binary), but `Run`, `commit` and `push` handed
   it `step.Args()` — the binary-less shape `KamalRunner` uses, because
   `KamalRunner` supplies its own `k.Binary`. So `git commit` tried to execute
   a program named `commit`, and the sync-wait tried to execute a program
   named `get`, each failing with "kubectl is not installed". Fixed by passing
   `step.Argv`. (`internal/deploy/argo.go`)

2. **The refusals were not recognisable as refusals.** The dirty-tree and
   no-remote refusals used `%s` with `ErrRefused` instead of `%w`: the message
   read as a refusal but `errors.Is` did not, so the CLI would have reported
   an Argo refusal as a generic failure (stderr, exit 1) instead of a refusal
   (stdout, exit 1). Fixed to `%w`. (`internal/deploy/argo.go`)

3. **`--engine argo` did not select the Argo engine in production.**
   `withDefaults()` filled a nil `deps.runner` with a `KamalRunner`, and
   `runner()` prefers an injected runner over the engine-resolved one — so it
   could no longer tell a test's injected runner from a default it had filled
   in itself. The router passes the zero value, so `caf deploy --engine argo`
   ran the Argo orchestration (`runArgoDeploy`) with the **Kamal plan**: it
   printed `kamal setup` as the step and would have run kamal, not the values
   commit. Fixed by letting `runner()` resolve the engine and keeping a test's
   injection as the only thing that reaches `runner()` through `deps.runner`.
   (`internal/cli/deploy.go`) Caught by running the built binary's dry run
   against a real chart repository and watching it print the Kamal plan under
   the Argo footer.

## 6. The dry run

```
caf deploy --engine argo --dry-run identity
```

Run against a real local git repository with `chart/values.yaml` and
`Chart.yaml` at `0.1.0`. It prints the four steps — `kubectl version --client`,
the `git commit` with the two files it would write (message, paths, old tag →
new tag), `git push`, and the sync-wait's jsonpath read — and the fact that
nothing was executed. It writes no file and makes no commit: the working tree
is left clean, asserted by `TestAnArgoDryRunAgainstARealRepositoryChangesNothing`
and measured by hand (`git status --porcelain` empty).

## 7. What is unprovable on this machine

`k3s` and Argo are not installed here (kubectl v1.33.9 is). So:

- **Provable, and proved:** the plan's steps and argv, every refusal, the dry
  run, and the commit half — the values bump and its message — against a real
  local git repository in a temp dir, plus a fake Argo reader (a kubectl shim
  on PATH) for the sync-wait's three outcomes: converged, never-converged,
  unreachable.
- **Not provable, and not claimed:** a live sync against a real Argo on a real
  box. The sync-wait's polls, its timeout, and its failure report are proved
  against a shim that prints what a real Argo would print; that the shim's
  output matches a real Argo's is an assumption, not a measurement. That gap is
  what the substrate install (m74) and the runbook (m75) exist to close. The
  live tier (`bin/prime --live`) is where a real sync would be demonstrated,
  and it is not in the declared gate.

## 8. The gate

```
./bin/prime          # green — 16 packages, 840 top-level passes, 1007 subtest passes, 0 failures, 8 skips
go vet ./...         # clean
gofmt -l .           # clean
```

`gate.yml` moved with the tests, in the same commits:

- suite floor 819 → 840 (848 declared, 8 skips, so 840 run)
- subtests floor 983 → 1006 (measured 1007, set one lower by the file's rule)
- packages floor 15 → 16 (the emitter packet's comment already said 16; the
  minimum never moved — corrected, not moved by this packet)

`caf.lock` was re-run after the gate.yml change, per the rule that a commit
that moves a floor re-pins the tree.
