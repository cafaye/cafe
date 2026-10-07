// Package deploy turns a cafaye project into a running deployment, and reports
// afterwards what is actually running.
//
// # Two engines, one seam
//
// The engine is the runner's, selected by --engine: kamal (the default, the
// engine the fleet runs) or argo (the launch substrate's, where there is no
// Kamal). Both are behind the same Runner interface, which is why nothing above
// the CLI changes — DEPLOYMENT.md: "The k3s/Argo implementation lands behind
// that same interface." Kamal and kit/templates/kamal retire at parity, not
// before.
//
// # Why caf drives engines rather than being them
//
// Neither engine is reimplemented. Kamal owns the build, the push, the proxy
// boot, the rolling rollout, the health gate and the rollback; Argo owns the
// sync, the health check and the self-heal. Reimplementing either in Go would
// be a second deploy engine that can disagree with the first about what a
// deploy is, and the disagreement would be discovered in production.
//
// So caf owns the part on either side of the engine: the refusal that should
// happen before it, and the report that has to be true after it.
//
// # What a deploy means under each engine
//
// Under Kamal, caf runs exactly one command that changes anything — `kamal
// setup` — and refuses an accessory port published on every interface of the
// host. Under Argo, the substrate is pull-only: CI never holds cluster
// credentials, and the only thing that changes the cluster is a commit. So a
// deploy is a values commit (the image tag and the chart version inside the OCI
// chart artifact), a push, and a wait for Argo to sync — never a `kubectl
// apply` of a workload, never `helm`, never a cluster credential in CI. The
// sync-wait polls the Application's status, which is read-only and therefore
// allowed, and refuses to claim a deploy that leaves the targetRevision
// behind.
//
// # One pure plan per engine and one seam
//
// Each engine owns its plan: the caller asks the runner which commands run and
// in what order, and executes the answer, but never builds argv itself. The
// plan is pure — it reads no file, opens no socket and runs no command — which
// is why every claim this package makes about what a deploy will do is
// assertable without a container runtime or a cluster.
//
// Everything that touches the world is behind Runner, which is two methods wide.
// A fake that records steps is a dozen lines, so the whole command is testable.
//
// # What --dry-run means here, stated as a rule rather than a slogan
//
// A dry run executes nothing and prints every step that would run, including
// the one that only runs after a failure. Under Kamal the two read-only steps
// really do run, because the exposure check needs what kamal resolved; under Argo
// nothing runs, because there is no exposure check and no cluster to be
// reachable from a dry run. Nothing is written, no container is started, no
// commit is made, and no file in the project is touched.
//
// The step names are constants (Preflight, Resolve, Deploy, State, Commit,
// Push, SyncWait) rather than literals in the plan, because the report and the
// tests both name them and a rename that missed one would be a report about a
// step nothing runs.
package deploy
