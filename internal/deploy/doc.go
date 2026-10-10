// Package deploy turns a cafaye project into a running deployment, and reports
// afterwards what is actually running.
//
// # Three engines, one seam
//
// The engine is the runner's, selected by --engine: kamal (the default, the
// engine the fleet runs), argo (the launch substrate's, where there is no
// Kamal) and remote (the cafaye-operated platform, pinned off until TG-D1).
// All three are behind the same Runner interface, which is why nothing above
// the CLI changes — DEPLOYMENT.md: "The k3s/Argo implementation lands behind
// that same interface." Kamal and kit/templates/kamal retire at parity, not
// before.
//
// The third engine is the only one whose target is a cafaye deployment rather
// than one the operator owns, and it is the reason the seam was worth building:
// before it, `caf deploy` had two engines and both pointed at something the
// operator runs themselves, which is the "nowhere cafaye-operated to deploy to"
// gap AGENTS-FIRST.md named as knowingly not done. It ships REFUSED, and
// remote.go holds why: one deployment serving many accounts is only safe once
// one deployment is proven safe for strangers, and that proof is TENANCY-GATE.md.
// The refusal is a constant in the package rather than a flag, an environment
// variable or a config key, because a tenancy decision a CI job could set with
// nobody deciding it is not a decision.
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
// # What a deploy means under the remote engine
//
// The remote engine has no local deploy to drive: there is no config/deploy.yml
// to resolve, no accessory port to expose and no command to run. The plan is a
// question, a request and a report, and the third of those is the argument for
// the engine existing. The question is the BOUNDARY step — which deployment of
// the platform serves this account, and what does it serve now — because a
// request bound to no deployment is a request to a place nobody named. The
// request is ONE deploy request carrying the account, and the account rather
// than a per-client deployment is the whole point: the app is built once and
// deployed once, and every client it serves is an account on the shared
// platform.
//
// A request that was accepted is not an applied release and not a serving app,
// and the report says so in as many words (`printRemoteReport`). The platform
// owns both of those, and a report that left them out would read as a complete
// deploy when it is a request that was taken.
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
// Push, SyncWait, Boundary, Request) rather than literals in the plan, because
// the report and the tests both name them and a rename that missed one would be
// a report about a step nothing runs.
package deploy
