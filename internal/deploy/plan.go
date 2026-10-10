package deploy

import (
	"fmt"
	"strconv"
	"strings"
)

// The plan is the whole decision `caf deploy` makes: which commands run, in what
// order, and what each one is for.
//
// It is pure. It reads no file, opens no socket and runs no command, which is
// what lets a test assert the exact argv of a dry run without a container
// runtime, a registry or an SSH connection anywhere in sight.

// Deployment is everything caf knows about a deployment before it asks Kamal
// anything: where the project is, which Kamal config describes it, and which
// secrets file supplies its values. It is a fact about files that exist, not
// about a config that has been resolved — resolving one is Kamal's job and needs
// Kamal, because the config is an ERB template.
type Deployment struct {
	// Dir is the absolute project directory. Everything caf names to the user is
	// absolute so a message is unambiguous about which project it is about.
	Dir string
	// Service is the service being deployed. It came from the manifest, not from
	// the Kamal config: Kamal's `service:` is the container name and the two are
	// not required to agree, so caf reads the contract it owns.
	Service string
	// Env is the Kamal destination — the environment overlay. Empty means no
	// overlay, which is a different thing from an overlay named "default".
	Env string
	// Config is config/deploy.yml, the base document every environment inherits.
	Config string
	// Overlay is config/deploy.<env>.yml, or "" when the environment has none.
	Overlay string
	// Secrets are the files Kamal will actually read, in the order it reads them.
	//
	// The list is longer than one for a reason worth stating: naming a
	// destination changes where credentials come from. Kamal's
	// Kamal::Secrets#secrets_filenames is
	//
	//	["<path>-common", "<path>.<destination>"]
	//
	// so `--destination staging` reads .kamal/secrets-common and
	// .kamal/secrets.staging and does NOT read .kamal/secrets. Measured on kamal
	// 2.12.0: a project with only .kamal/secrets and an environment set fails at
	// boot with "Secret 'x' not found, no secret files (.kamal/secrets-common,
	// .kamal/secrets.staging) provided". So caf resolves and names them rather
	// than assuming the one `kamal init` creates.
	Secrets []string

	// The Argo engine's facts, read from the service repository's chart. The
	// Kamal engine ignores them; they are here because the plan is the engine's
	// and both engines are asked for it with one signature, the way backup's
	// Cover is shared between its two engines.
	//
	// A deploy under Argo is a values commit: the image tag in chart/values.yaml
	// and the version in Chart.yaml ride inside the OCI chart artifact, and the
	// only lever caf has is a commit to the service repository. See argo.go.
	// Values is chart/values.yaml, the values the OCI chart ships.
	Values string
	// Chart is Chart.yaml, the chart's own version document.
	Chart string
	// ValuesContent is the current chart/values.yaml, as the chart ships it. The
	// plan reads the image tag out of it and writes the bumped copy, so the
	// pure plan function needs the bytes and not just the parsed tag.
	ValuesContent string
	// ChartContent is the current Chart.yaml. The plan reads the version out of
	// it and writes the bumped copy, for the same reason.
	ChartContent string
	// Image is the image reference in values.yaml, repository and tag, as the
	// chart ships it — e.g. "ghcr.io/cafaye/identity:0.1.0". The tag is the
	// release: a deploy bumps it.
	Image string
	// ChartVersion is the version in Chart.yaml, e.g. "0.1.0". It is the OCI tag
	// the publish workflow pushes, and the Application's targetRevision names
	// it — DEPLOYMENT.md: "the chart version is the OCI tag".
	ChartVersion string
	// Commit is the short hash of the service repository's HEAD, the commit the
	// release is built from. The commit message names it, so a reader can trace
	// a running release back to the source it came from.
	Commit string
	// Account is the account on the operated platform that a remote deploy
	// serves, as the --account flag names it. The Argo engine's facts are here
	// for the same reason Kamal's are: the plan is the engine's and both
	// engines are asked for it with one signature. The two enabled engines
	// leave it empty.
	Account string
}

// Options is one `caf deploy` invocation's choices, after flag parsing.
type Options struct {
	// Env is the Kamal destination, "" for none.
	Env string
	// Version pins the release. Empty means Kamal decides — in practice the short
	// commit hash of the repository it is run from — and the plan says so rather
	// than inventing one, because a version caf guessed is a version nobody can
	// roll back to by name.
	Version string
}

// When says whether a step runs on every invocation or only after a failed one.
// A failure report is a step with a condition, not a special case in the
// command: keeping it in the plan is what lets a dry run print it, and a dry
// run that hides the command a failure will run is a dry run that cannot be used
// to predict what happens.
type When int

const (
	// Always runs on every invocation.
	Always When = iota
	// OnFailure runs only when the deploy step has already failed.
	OnFailure
)

// Step is one command, the reason it is in the plan, and when it runs.
type Step struct {
	// Name is the short verb, for the progress line.
	Name string
	// Argv is the command as the operator would type it, the deploy engine first.
	// It is printed in a dry run and executed otherwise, from the same value, so
	// the two cannot drift: there is no second copy of an argument list in this
	// package to fall out of date.
	Argv []string
	// Why is the sentence a reader needs to decide whether to trust this step.
	Why string
	// On is Always or OnFailure.
	On When
	// Files are the files to write before the command runs, as path and content.
	//
	// A step that writes a document — the Argo commit writes the bumped
	// chart/values.yaml and Chart.yaml — carries the documents it writes, so
	// the engine writes them and the command commits them: the plan stays pure,
	// and the files are a fact about the step rather than a second copy the
	// caller has to keep in step with it. The Kamal plan's steps carry none,
	// and a dry run writes none, because a dry run executes nothing.
	Files []File
}

// File is one file a step writes before its command runs.
type File struct {
	// Path is the absolute path to write.
	Path string
	// Content is what to write.
	Content string
}

// The step names. They are constants rather than literals because the tests name
// them and a rename that missed one would be a test asserting on a step nothing
// runs.
const (
	// Preflight proves the deploy engine is installed.
	Preflight = "preflight"
	// Resolve reads the config with its ERB evaluated, and is the step whose
	// output the exposure check reads.
	Resolve = "resolve"
	// Deploy is the only step that changes a server.
	Deploy = "deploy"
	// State reports what is running, and only after a failure.
	State = "state"
)

// The Argo engine's step names. They are constants for the same reason the
// Kamal ones are: the report and the tests both name them.
//
// The Argo plan has no Resolve and no State. There is no Kamal to resolve a
// config against — the values are a file in the service repository, read by the
// engine before the plan is built. And there is no partial-failure state to
// read: the sync-wait already reads the Application's status, so a failure is
// reported from what it saw rather than from a second question.
const (
	// Commit writes the release into the service repository: it bumps the image
	// tag in chart/values.yaml and the version in Chart.yaml, and commits the
	// two files with a message naming the service, the version and the commit
	// it was built from.
	Commit = "commit"
	// Push publishes the commit, which is what makes the publish workflow push
	// the new OCI tag and gives Argo a new targetRevision to sync.
	Push = "push"
	// SyncWait polls the Application's status until Argo has synced the new
	// revision and the service is healthy, or fails with the status it stopped
	// at. A deploy that reports success without a sync is the one failure this
	// engine exists to prevent.
	SyncWait = "sync-wait"
)

// Plan is the whole decision, plus the facts the report echoes so that the
// transcript of a deploy says which project and which environment it was about.
type Plan struct {
	Service string
	Env     string
	Version string
	Dir     string
	Config  string
	Overlay string
	// Secrets are the files that exist, in Kamal's order. See Deployment.Secrets
	// for why that is a list and not a path.
	Secrets []string
	// Engine names the deploy engine that built this plan: "kamal" or "argo".
	// The two engines share the orchestration but neither the commands nor the
	// sentences about them, so every part of the deploy that is not a step —
	// the header, the dry run's prose, the after-the-fact report — is written
	// twice, once per engine, and this is how a reader (and a test) tells which
	// one a plan belongs to.
	Engine string
	// Commit is the short hash of the service repository's HEAD, the commit the
	// release is built from. It is the Argo engine's fact; the Kamal plan leaves
	// it empty, because Kamal names the release from the repository itself.
	Commit string
	// Steps are ordered, and the caller finds them by name.
	Steps []Step
}

// Step returns the named step, and the zero Step when there is no such step.
//
// It is a lookup rather than an index so that "the deploy step is missing" is a
// zero value every caller has to notice, rather than a wrong step executed
// because it happened to be at index 2.
func (p Plan) Step(name string) Step {
	for _, step := range p.Steps {
		if step.Name == name {
			return step
		}
	}
	return Step{}
}

// PlanFor builds the ordered command sequence for one invocation.
//
// # The shape of it is the answer to "does caf reimplement Kamal's deploy?"
//
// It does not. Kamal owns build, push, proxy boot, the rolling rollout, the
// health gate and the rollback, and caf runs exactly one Kamal command that
// changes anything: `kamal setup`. Everything else here either proves the deploy
// engine is present, reads back what it resolved, or reports.
//
// # Why `kamal setup` and not `kamal deploy`
//
// `setup` is "bootstrap Docker on the host, boot the accessories, then deploy"
// and `deploy` is the last of those three alone. Choosing `deploy` would mean a
// customer following the recommended path on a fresh VPS and getting a service
// with no database — a failure whose message is a connection refused from
// inside the app and whose cause is a step caf chose not to take. `setup` is
// idempotent about the accessories (Kamal skips an accessory whose container
// already exists, `Kamal::Cli::Accessory#boot`), so it is also the right command
// for every deploy after the first, and there is exactly one path to tell
// somebody about.
func PlanFor(dep Deployment, o Options) Plan {
	dest := destinationArgs(o.Env)

	return Plan{
		Service: dep.Service,
		Env:     o.Env,
		Version: o.Version,
		Dir:     dep.Dir,
		Config:  dep.Config,
		Overlay: dep.Overlay,
		Secrets: dep.Secrets,
		Engine:  EngineKamal,
		Steps: []Step{
			{
				Name: Preflight,
				// `kamal version`, NOT `kamal --version`. The latter is not a
				// subcommand: it prints the command list and exits 1. Measured on
				// kamal 2.12.0, and found by the live test rather than by reading —
				// which is the whole argument for having a live test.
				Argv: []string{"kamal", "version"},
				Why:  "kamal is the deploy engine; this prints its version and fails if it is not installed",
				On:   Always,
			},
			{
				Name: Resolve,
				Argv: append([]string{"kamal", "config"}, dest...),
				Why:  "reads config/deploy.yml with its ERB evaluated, and refuses a config kamal cannot use",
				On:   Always,
			},
			{
				Name: Deploy,
				Argv: append(append([]string{"kamal", "setup"}, dest...), versionArgs(o.Version)...),
				Why:  "installs Docker if the host lacks it, boots the accessories, builds, pushes, rolls out, and refuses a release that never goes healthy",
				On:   Always,
			},
			{
				Name: State,
				Argv: append([]string{"kamal", "app", "containers"}, dest...),
				Why:  "reports which containers are actually running, which is the only answer to what a partial failure left behind",
				On:   OnFailure,
			},
		},
	}
}

// PlanForArgo builds the ordered command sequence for one Argo deploy
// invocation, the way PlanFor builds the Kamal one.
//
// # The order is the Argo engine's argument about what a deploy is
//
// Argo is pull-only: CI never holds cluster credentials, and the only thing
// that changes the cluster is a commit. So a deploy is a values commit, a
// push, and a wait for Argo to sync — never a `kubectl apply` of a workload,
// never `helm`, never a cluster credential in CI. The commit bumps the image
// tag in chart/values.yaml and the version in Chart.yaml; the publish workflow
// pushes the new OCI tag; and the sync-wait polls the Application's status
// until Argo has synced the new revision and the service is healthy.
//
// # Why there is no Resolve and no State
//
// The Kamal plan resolves config/deploy.yml because the exposure check reads
// what Kamal resolved. The Argo engine has no Kamal to resolve and no
// accessory port to expose: the values are a file in the service repository,
// read by the engine before the plan is built. And there is no partial-failure
// state to read — the sync-wait already reads the Application's status, so a
// failure is reported from what it saw rather than from a second question.
//
// # The lever, and the second move caf cannot make
//
// The deploy lever is the chart version: bump the image tag, bump Chart.yaml,
// commit, push. The Application's targetRevision must then move to the new
// version — which is a second commit in kit, or an operator action, and caf
// does not make it. caf writes commits, never the cluster, and never another
// repository's tree. So the sync-wait refuses to claim a deploy that leaves
// targetRevision behind: it polls until the Application's synced revision
// matches the version caf committed, and reports the status it stopped at
// when it does not.
func PlanForArgo(dep Deployment, o Options) Plan {
	newVersion := o.Version
	if newVersion == "" {
		newVersion = bumpPatch(dep.ChartVersion)
	}
	newImage := bumpImageTag(dep.Image, newVersion)
	newValues := replaceImageTag(dep.ValuesContent, dep.Image, newImage)
	newChart := replaceChartVersion(dep.ChartContent, dep.ChartVersion, newVersion)

	message := fmt.Sprintf("%s: release %s, built from %s\n\n"+
		"Bump the image tag in chart/values.yaml from %s to %s and the chart version\n"+
		"in Chart.yaml from %s to %s. The chart version is the OCI tag: the publish workflow\n"+
		"pushes it, and the Application's targetRevision must move to it for Argo to sync\n"+
		"the new revision. That second move is a commit in kit or an operator action —\n"+
		"caf writes commits, never the cluster, and never another repository's tree.",
		dep.Service, newVersion, dep.Commit,
		dep.ChartVersion, newVersion, dep.ChartVersion, newVersion)

	return Plan{
		Service: dep.Service,
		Version: newVersion,
		Dir:     dep.Dir,
		Engine:  EngineArgo,
		Commit:  dep.Commit,
		Steps: []Step{
			{
				Name: Preflight,
				// `kubectl version --client`, the client's own version probe. It
				// is the Argo shape of `kamal version`: it fails if kubectl is
				// not installed, and it prints the version the report names.
				// It proves the tool, not the cluster — Argo's reachability is
				// the sync-wait's question, answered by reading the Application.
				Argv: []string{"kubectl", "version", "--client"},
				Why:  "kubectl is the deploy engine's tool; this prints its version and fails if it is not installed",
				On:   Always,
			},
			{
				Name: Commit,
				Argv: []string{"git", "commit", "-m", message},
				Why: fmt.Sprintf("writes the release into %s: the image tag %s -> %s and the chart version %s -> %s, with a message naming the service, the version and the commit it was built from",
					"chart/values.yaml", dep.ChartVersion, newVersion, dep.ChartVersion, newVersion),
				// The two files the commit carries, as path and content. The
				// engine writes them and commits them; a dry run prints them
				// and writes nothing, because a dry run with a side effect is
				// not a dry run.
				Files: []File{
					{Path: dep.Values, Content: newValues},
					{Path: dep.Chart, Content: newChart},
				},
				On: Always,
			},
			{
				Name: Push,
				Argv: []string{"git", "push"},
				Why:  "publishes the commit, which is what makes the publish workflow push the new OCI tag and gives Argo a new revision to sync",
				On:   Always,
			},
			{
				Name: SyncWait,
				// The read-only poll that waits for Argo to sync. It reads the
				// Application's synced revision and health, never writes: Argo
				// is pull-only, and the only thing that changes the cluster is
				// the commit above. The jsonpath names the two facts the wait
				// needs — the revision Argo has synced and the health it reports.
				Argv: []string{"kubectl", "get", "application", dep.Service, "-n", "argocd",
					"-o", "jsonpath={.status.sync.revision} {.status.health.status}"},
				Why: fmt.Sprintf("polls the %s Application's status until Argo has synced revision %s and the service is healthy, or fails with the status it stopped at — a deploy that reports success without a sync is the one failure this engine exists to prevent", dep.Service, newVersion),
				On:  Always,
			},
		},
	}
}

// bumpPatch bumps the patch component of a semantic version, which is the
// release caf derives when the operator pins none. A chart version is a
// semantic version (helm refuses to package one that is not), so the bump is
// the third dot-separated number. It is a derivation and not a judgement
// call: the operator's --version outranks it, and the default only has to be
// a version the chart has not used yet.
func bumpPatch(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		// Not a semantic version. Rather than guess at a shape helm would
		// refuse, answer the version with a suffix that is still sortable and
		// still not the one the chart ships — a deploy that guessed a version
		// helm rejects would fail at the publish workflow, not here.
		return version + ".1"
	}
	if n, err := strconv.Atoi(parts[2]); err == nil {
		parts[2] = strconv.Itoa(n + 1)
		return strings.Join(parts, ".")
	}
	return version + ".1"
}

// bumpImageTag replaces the tag of an image reference with newVersion,
// keeping the repository. The reference is "repository:tag", split at the
// last colon — a registry port is part of the repository, so a split at the
// first colon would turn "localhost:5000/identity:0.1.0" into the wrong pair.
func bumpImageTag(image, newVersion string) string {
	if repo, _, found := strings.Cut(image, ":"); found {
		return repo + ":" + newVersion
	}
	// No tag at all. The chart ships one (the generator never emits `latest`),
	// so this is a values.yaml somebody hand-edited; answer it with the tag
	// rather than refusing, because the refusal that matters is the missing
	// image reference, which the engine already raised.
	return image + ":" + newVersion
}

// replaceImageTag rewrites the image tag line in a values.yaml, replacing the
// old reference with the new one. It edits the `tag:` value under `image:`,
// which is where the generator puts it and where a release bumps it.
func replaceImageTag(values, oldImage, newImage string) string {
	// The values.yaml carries the tag on its own line, `  tag: "0.1.0"`, and
	// the repository on another. Replace the whole `tag:` value so the
	// repository is never touched — a deploy that rewrote the repository would
	// be a deploy that changed where the image comes from, which is not its
	// decision to make.
	oldTag := oldImage[strings.LastIndex(oldImage, ":")+1:]
	newTag := newImage[strings.LastIndex(newImage, ":")+1:]
	return strings.Replace(values, `tag: "`+oldTag+`"`, `tag: "`+newTag+`"`, 1)
}

// replaceChartVersion rewrites the version line in a Chart.yaml, replacing the
// old version with the new one. Chart.yaml carries `version:` and `appVersion:`;
// both move, because the chart version is the OCI tag and the appVersion is
// the service's own version, which the release also bumps.
func replaceChartVersion(chart, oldVersion, newVersion string) string {
	out := strings.Replace(chart, "version: "+oldVersion, "version: "+newVersion, 1)
	out = strings.Replace(out, "appVersion: "+oldVersion, "appVersion: "+newVersion, 1)
	return out
}

// destinationArgs is how an environment becomes a Kamal flag. The flag is what
// makes "self-host or cloud, the switch made once" one argument: the same
// command, and a different config/deploy.<env>.yml overlay beneath it.
//
// Nothing is passed for an empty environment rather than passing an empty
// string, because Kamal treats a present `--destination` as a demand for the
// overlay file and its absence as "no overlay", and an empty value is neither.
func destinationArgs(env string) []string {
	if env == "" {
		return nil
	}
	return []string{"--destination", env}
}

// versionArgs pins the release when the operator asked for one. Kamal's default
// is the short commit hash of the repository it runs in, which is a good default
// and not one caf should second-guess.
func versionArgs(version string) []string {
	if version == "" {
		return nil
	}
	return []string{"--version", version}
}

// Command renders a step as the operator would type it, which is what the dry
// run prints. Kamal's own output is YAML with symbol keys and an ANSI-coloured
// header, so it is printed rather than parsed; only the exposure check parses it,
// and only the keys it must.
func (s Step) Command() string { return strings.Join(s.Argv, " ") }

// Args is what the Runner is handed: the command without the binary, because the
// Runner owns resolving and running the program.
//
// Command and Args come from the same Argv, so a dry run prints the same sequence
// a real run executes. One value rather than two is what stops the printed plan
// from drifting away from the run it claims to describe.
func (s Step) Args() []string {
	if len(s.Argv) == 0 {
		return nil
	}
	return s.Argv[1:]
}

// String makes a step readable in a %v so a failing test names the command
// rather than a slice address.
func (s Step) String() string { return fmt.Sprintf("%s: %s", s.Name, s.Command()) }

// Runs reports whether a step is in scope for an invocation that has not
// failed. A dry run asks this of every step: a step that would not run is a
// step the dry run must print and not execute.
func (s Step) Runs(failed bool) bool {
	return s.On == Always || failed
}
