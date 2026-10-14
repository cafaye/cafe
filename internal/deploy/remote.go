package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The operated remote is the third engine, and the only one whose target is a
// cafaye deployment rather than one the operator owns.
//
//	kamal    the operator's own box: build, push, roll out behind kamal-proxy
//	argo     the launch substrate's cluster, by a values commit and a sync
//	remote   the cafaye-operated platform: build once, deploy once, serve many
//
// The engine exists so `cafe deploy` can name a cafaye-operated target at all.
// Before it, the command had two engines and both pointed at something the
// operator runs themselves, which is the "nowhere cafaye-operated to deploy to"
// gap AGENTS-FIRST.md named as knowingly not done.

// EngineRemote is the operated remote: the customer's app is built once and
// deployed once, and every client they serve is an account on the shared
// platform rather than a cafaye deployment of its own.
//
// It is not the default and it does not deploy anything today. See
// ErrTenancyUndecided: the plan is refused until the tenancy decision is
// recorded, and that refusal is the packet rather than an oversight.
const EngineRemote = "remote"

// ErrTenancyUndecided is the sentinel for "the operated remote is built and
// will not run".
//
// It is a sentinel rather than a sentence so three callers can name the same
// refusal without sharing a string: the engine's Plan returns it, the command's
// isDeployRefusal finds ErrRefused through it (so the exit code is a refusal's
// and not a failure's), and remote_test.go asserts it is still the current state
// so a later packet cannot enable the engine by quietly deleting a branch.
var ErrTenancyUndecided = errors.New("the operated remote is not enabled")

// tenancyUndecidedRefusal is the one refusal this engine returns, written out in
// full because a refusal that says only "not enabled" sends the reader to a log
// that says nothing useful.
//
// It names what would have to be true, where the decision is recorded, and what
// the reader can do today — the same shape as the 403 agent_unclaimed sentence
// in internal/agent: an instruction, not a status code.
const tenancyUndecidedRefusal = `
The remote engine deploys a service into the cafaye-operated platform, where
every client the account serves is an account on one shared deployment rather
than a cafaye deployment of its own. That is only safe once one deployment is
proven safe for strangers, and that proof is TENANCY-GATE.md.

The decision that records it is TG-D1: shared multi-tenant platform as the
default, a dedicated box as the premium option, with the evidence linked. It
lives in the moon repository, it is a decision rather than a packet, and until
it is recorded no packet may assume the platform's shape either way.

So this is the state of the record, not a bug to work around:

  the mechanism is built, it is tested, and no tenancy check was weakened to
  make it work.

To deploy today, use the two engines that are enabled:

  cafe deploy <service>                     kamal, the default: your own box
  cafe deploy <service> --engine argo       the launch substrate's cluster

When TG-D1 is recorded, enabling this is one line: flip remoteEnabled to true in
internal/deploy/remote.go. Nothing else moves, and the test asserting the refusal
becomes the test asserting the plan — which is the evidence that this was a
mechanism and not a placeholder.`

// remoteEnabled is the switch, and it is a constant rather than a flag, an
// environment variable or a config key on purpose.
//
// A flag would make a tenancy decision a command-line argument. An environment
// variable would make it something a CI job could set with nobody deciding it. A
// config key would make it a second place the platform's shape is recorded, and
// the moon repository owns that decision. Every way this could be a mechanism is
// worse than one constant in the package the refusal is returned from, changed
// by the packet that records the decision.
const remoteEnabled = false

// AccountFlag is the flag naming the account the operated platform serves. It is
// a constant because the plan writes it into the argv it prints and the runner
// reads it back out, and a spelling that existed in two places would eventually
// be two spellings.
const AccountFlag = "--account"

// RemoteAccount is one account on the operated platform, as the boundary step
// reads it in. The three fields are what makes a deploy legible afterwards:
// which account, which deployment serves it, and what that deployment serves now
// — because "the platform" is not a place until it is one deployment id carrying
// a version.
type RemoteAccount struct {
	// ID is the account's uuid, the one the flag names.
	ID string
	// DeploymentID is the shared deployment serving this account.
	DeploymentID string
	// ServedVersion is what that deployment serves right now, which is the
	// release a deploy moves. Empty means the platform has not answered.
	ServedVersion string
}

// RemoteDeployment is the platform's answer to a deploy request.
//
// It carries an id and a version rather than a health answer, because the
// platform applies a release on its own schedule and the cab-side claim that is
// true is "accepted". The report says that rather than dressing it up.
type RemoteDeployment struct {
	// ID is the deploy request's id, to ask about it later.
	ID string
	// Version is the version requested, echoed by the platform.
	Version string
	// AccountID is the account it was requested for.
	AccountID string
	// Accepted reports whether the platform took the request.
	Accepted bool
}

// Remote is the seam between this engine and the operated platform, declared the
// way the other packages declare theirs: an interface with the two questions the
// engine asks, and a stub that refuses beside it, so every test drives the whole
// engine with no network and no platform.
type Remote interface {
	// ReadAccount answers the boundary question: which deployment serves this
	// account, and what does it serve now.
	ReadAccount(ctx context.Context, accountID string) (*RemoteAccount, error)
	// Deploy hands the platform one deploy request.
	Deploy(ctx context.Context, req RemoteRequest) (*RemoteDeployment, error)
}

// RemoteRequest is one deploy request against the operated platform.
//
// The account is a field rather than something assembled at the edge because it
// is the whole point of the engine: the app is built once and deployed once, and
// the account is what is served — not a deployment being created for the client.
type RemoteRequest struct {
	// AccountID is the account on the shared platform.
	AccountID string
	// Service is the service being deployed.
	Service string
	// Version pins the release. Empty means the platform decides.
	Version string
	// Commit is the commit the release was built from, carried so the record
	// names what is running rather than only what was asked for.
	Commit string
}

// RemoteRunner is the remote engine's runner.
//
// It has no plan of its own beyond the two steps below, because there is no
// local deploy engine to drive: the plan is a question, a request and a report,
// and the third of those is the argument for the engine existing.
type RemoteRunner struct {
	// Remote is the platform seam. Nil means StubRemote, which refuses — a
	// runner asked to deploy with nothing configured must not silently invent
	// a destination.
	Remote Remote
}

// NewRemoteRunner is the wiring RunnerFor uses. The gate is checked in Plan and
// not here, so a refused remote deploy is a refusal and not a panic in a
// constructor.
func NewRemoteRunner() *RemoteRunner {
	return &RemoteRunner{Remote: nil}
}

// The remote engine's step names, constants for the reason the other engines' are:
// the report and the tests both name them, and a rename that missed one would be
// a report about a step nothing runs.
const (
	// Boundary is the account-binding question: which deployment of the
	// platform serves this account, and what does it serve now. It runs before
	// the request because a request bound to no deployment is a request to a
	// place nobody named.
	Boundary = "boundary"
	// RequestStep is the deploy request itself.
	RequestStep = "request"
)

// Plan is the remote engine's plan: bind to the account's deployment, then ask.
//
// It is pure in the same sense the other engines' are — it reads no file, opens
// no socket and runs no command — which is what lets a dry run print the exact
// steps and a test assert them with no platform anywhere in sight.
func (r *RemoteRunner) Plan(dep Deployment, opts Options) (Plan, error) {
	// The gate is first, before any step is built, because a plan for a
	// disabled engine is a plan nobody should watch being executed. A dry run
	// that printed the steps and skipped the refusal would read as a deploy
	// that works, which is the one impression this packet cannot afford.
	if !remoteEnabled {
		return Plan{}, remoteRefusal()
	}
	return planForRemote(dep, opts), nil
}

// planForRemote is the remote engine's plan with the gate already answered, and
// it is its own function for the same reason the engine is: everything remote.go
// ships can be reached either through the gate or past it, and the test needs to
// reach the mechanism without touching the switch. A function is the smallest
// such door, and it is one the production path also goes through rather than a
// hook only tests know about.
//
// The account is a parameter rather than something the runner looks up, because
// it arrived on a flag and the plan is where a flag becomes a fact.
func planForRemote(dep Deployment, opts Options) Plan {
	argv := []string{"cafe", "deploy", "--engine", EngineRemote}
	// The account is written only when there is one. A plan whose argv carried
	// `--account` with nothing after it would be an argv nobody could run —
	// `cafe deploy --account --yes identity` parses --yes as the account — and
	// the omission is what makes Run's refusal name the flag rather than the
	// platform being asked about an account called "--yes".
	if dep.Account != "" {
		argv = append(argv, AccountFlag, dep.Account)
	}
	if opts.Version != "" {
		argv = append(argv, "--version", opts.Version)
	}
	// The service is last, so the argv reads as the command an operator would
	// type — flags first, the argument last — and so requestFromArgv can read
	// it back without guessing which token is which.
	argv = append(argv, "--yes", dep.Service)
	return Plan{
		Service: dep.Service,
		Env:     dep.Env,
		Version: opts.Version,
		Dir:     dep.Dir,
		Engine:  EngineRemote,
		Steps: []Step{
			{
				Name: Boundary,
				Argv: argv,
				Why:  "ask the platform which deployment serves this account, so the request is bound to one place",
				On:   Always,
			},
			{
				Name: RequestStep,
				Argv: argv,
				Why:  "hand the platform one deploy request: the service, the account, and the commit it was built from",
				On:   Always,
			},
		},
	}
}

// Run executes one step of the plan.
//
// Both steps are seam calls, which is why there is no argv to reinvoke here: the
// Remote seam answers, and the answer is the report. The request is read back out
// of the step's own argv — the same value the dry run printed — so a request
// bound to account A cannot be an answer to a question about account B.
func (r *RemoteRunner) Run(ctx context.Context, dir string, step Step, stdout, stderr io.Writer) error {
	rem := r.Remote
	if rem == nil {
		rem = StubRemote{}
	}
	req := requestFromArgv(step.Argv)
	if req.AccountID == "" {
		return noAccountRefusal()
	}
	switch step.Name {
	case Boundary:
		found, err := rem.ReadAccount(ctx, req.AccountID)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "  account %s is served by deployment %s at %s\n",
			found.ID, found.DeploymentID, orUnrecorded(found.ServedVersion))
		return nil
	case RequestStep:
		return runRemoteRequest(ctx, rem, req, stdout)
	default:
		return fmt.Errorf("cafe deploy: the remote engine has no %q step, so nothing was requested", step.Name)
	}
}

// runRemoteRequest is the step that changes the platform: one request, and a
// report of what the platform said it did with it.
func runRemoteRequest(ctx context.Context, rem Remote, req RemoteRequest, stdout io.Writer) error {
	got, err := rem.Deploy(ctx, req)
	if err != nil {
		return err
	}
	if !got.Accepted {
		return fmt.Errorf("%w: the platform took the request and did not accept it, so nothing changed", ErrRefused)
	}
	_, _ = fmt.Fprintf(stdout, "  deploy request %s accepted for account %s\n", got.ID, got.AccountID)
	if got.Version != "" {
		_, _ = fmt.Fprintf(stdout, "  release %s is now pending on the deployment that serves that account\n", got.Version)
	}
	return nil
}

// requestFromArgv reads the request back out of the plan's own argv.
//
// It is a parser rather than four parameters because the plan is the single copy:
// what the dry run printed is what the runner asked for, and a request assembled
// from a value the dry run never showed would be a deploy nobody was shown.
//
// Two rules hold it to that, and both are about the argv the plan writes rather
// than about argv in general:
//
//   - The service is the LAST token, whatever it is called. The plan writes the
//     flags first and the argument last, so the last element is the argument —
//     including for a service named after the engine, which the previous version
//     dropped and which would have been a deploy of the wrong service.
//   - A value that looks like a flag is a MISSING value. `--account` followed by
//     `--yes` is an account nobody named, and reading `--yes` as the account id
//     would send the platform a question about a destination that does not exist.
//     Account ids are uuids, so this costs nothing that is real.
func requestFromArgv(argv []string) RemoteRequest {
	req := RemoteRequest{}
	if n := len(argv); n > 0 {
		req.Service = argv[n-1]
	}
	// The last token is the service, so no flag can have a value after it.
	for i := 0; i+1 < len(argv); i++ {
		var into *string
		switch argv[i] {
		case AccountFlag:
			into = &req.AccountID
		case "--version":
			into = &req.Version
		default:
			continue
		}
		if !strings.HasPrefix(argv[i+1], "-") {
			*into = argv[i+1]
		}
	}
	return req
}

// StubRemote is the seam's default: a remote that is not configured refuses.
//
// A nil Remote is a cafe built without the remote wiring, and a deploy that
// proceeded anyway would be a deploy to a destination nobody named.
type StubRemote struct{}

func (StubRemote) ReadAccount(context.Context, string) (*RemoteAccount, error) {
	return nil, fmt.Errorf("%w: no operated platform is configured for this remote deploy", ErrTenancyUndecided)
}

func (StubRemote) Deploy(context.Context, RemoteRequest) (*RemoteDeployment, error) {
	return nil, fmt.Errorf("%w: no operated platform is configured for this remote deploy", ErrTenancyUndecided)
}

func orUnrecorded(version string) string {
	if strings.TrimSpace(version) == "" {
		return "an unrecorded version"
	}
	return version
}

// noAccountRefusal is the refusal a remote deploy with no --account gets, from
// the two places that can notice: the step, which reads the account back out of
// the plan's own argv, and the orchestration before it prints a dry run. One
// function rather than two sentences, because the sentence names the flag and a
// reader who is told the same thing twice should be told it the same way.
func noAccountRefusal() error {
	return fmt.Errorf("%w: the remote engine has no account to deploy for.\n"+
		"  Name it with %s <uuid>: the account is what the shared platform serves, and\n"+
		"  a remote deploy with no account is a deploy to no place. The two engines that\n"+
		"  are enabled need no account, because neither of them is the shared platform", ErrRefused, AccountFlag)
}

// remoteRefusal is the error the gate returns.
//
// It wraps BOTH sentinels so a caller can ask either question of it: errors.Is
// ErrRefused is what the command asks, to exit as a refusal rather than as a
// failed deploy; errors.Is ErrTenancyUndecided is what this package's own test
// asks, so the refusal cannot be reworded into something that stops being
// recognized. The prose is appended rather than referenced because an error is
// not guaranteed to reach a log — but the sentence a reader needs is.
func remoteRefusal() error {
	return fmt.Errorf("%w: %w\n\n%s", ErrRefused, ErrTenancyUndecided, tenancyUndecidedRefusal)
}

// runRemoteDeploy is the remote engine's deploy: the same orchestration as the
// other two — the plan's steps in order, then the report — with none of the
// parts that are ours. There is no config/deploy.yml to resolve, no accessory
// port to expose, no command to run: the platform is a fact about one account,
// and the only thing that changes it is the request the plan carries.
//
// The gate is reached through Plan, which means a dry run is refused too. That
// order is deliberate: the first thing a remote deploy says is why it cannot
// deploy, and a dry run that listed the steps and left the refusal out would
// read as a deploy that works.
func runRemoteDeploy(ctx context.Context, r Runner, req Request, stdout, stderr io.Writer) error {
	dir := req.Dir
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", dir, err)
	}
	// A remote deploy needs no project directory at all — the platform has the
	// source — but a deploy run from a directory that does not exist is a
	// deploy whose --version and --account came from nowhere, so it is refused
	// by name rather than accepted.
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return fmt.Errorf("cafe deploy: %s is not a directory cafe can read: %w", abs, err)
	}

	dep := Deployment{Dir: abs, Service: req.Service, Env: req.Env, Account: req.Account}
	plan, err := r.Plan(dep, Options{Env: req.Env, Version: req.Version})
	if err != nil {
		// The refusal is printed in full on stdout, for the same reason the
		// Kamal engine's is: it is several paragraphs about a mechanism, and
		// an operator who saw only the first line of it would not know what to
		// do. It is printed as the prose and not as the error's message,
		// because the message leads with two sentinels that mean nothing to a
		// person. The error is returned unchanged and the command maps it to
		// errReported — a script greps the error and a human reads the prose.
		if errors.Is(err, ErrTenancyUndecided) {
			_, _ = fmt.Fprintf(stdout, "%s\n", tenancyUndecidedRefusal)
		}
		return err
	}

	if dep.Account == "" {
		// The account is checked before anything is printed, dry run or not,
		// for the same reason the tenancy gate is checked first: a dry run
		// that listed the steps and left out why they cannot run would read as
		// a deploy that works. This one is a refusal about a missing flag
		// rather than about a decision, so it is not dressed up as the gate.
		return noAccountRefusal()
	}

	if req.DryRun {
		printRemoteDryRun(stdout, plan)
		return nil
	}

	_, _ = fmt.Fprintf(stdout, "%s: deploying to the operated platform for account %s\n", plan.Service, dep.Account)
	_, _ = fmt.Fprintf(stdout, "  the app is built once and deployed once; every client it serves is an\n")
	_, _ = fmt.Fprintf(stdout, "  account on the shared platform, not a deployment of its own\n")

	if err := runRemoteSteps(ctx, r, plan, dep, stdout, stderr); err != nil {
		return err
	}
	printRemoteReport(stdout, plan, dep)
	return nil
}

// runRemoteSteps runs the plan's steps in order and stops at the first failure,
// naming the one that failed rather than returning the platform's exit status —
// which it does not have.
func runRemoteSteps(ctx context.Context, r Runner, plan Plan, dep Deployment, stdout, stderr io.Writer) error {
	for _, name := range []string{Boundary, RequestStep} {
		step := plan.Step(name)
		if step.Argv == nil {
			return fmt.Errorf("cafe deploy: the plan has no %q step, so the deploy stopped there rather than pretending", name)
		}
		_, _ = fmt.Fprintf(stdout, "\nrunning %s\n", step.Command())
		if err := r.Run(ctx, plan.Dir, step, stdout, stderr); err != nil {
			return fmt.Errorf("%s failed: %w", step.Command(), err)
		}
	}
	return nil
}

// printRemoteReport is what a remote deploy that was accepted says.
//
// It states what the deploy did NOT do — it did not apply a release, and it did
// not prove the app is answering — because the platform owns both of those, and
// a report that left them out would read as a complete deploy when it is a
// request that was taken.
func printRemoteReport(stdout io.Writer, plan Plan, dep Deployment) {
	_, _ = fmt.Fprintf(stdout, "\n%s: deploy request accepted on the operated platform\n", plan.Service)
	if plan.Version != "" {
		_, _ = fmt.Fprintf(stdout, "  release %s is pending on the deployment serving account %s\n", plan.Version, dep.Account)
	} else {
		_, _ = fmt.Fprintf(stdout, "  the platform chose the release; account %s will serve it when it has applied\n", dep.Account)
	}
	_, _ = fmt.Fprint(stdout, "  what this did NOT do: apply the release, and prove the app is answering. Both are\n")
	_, _ = fmt.Fprint(stdout, "  the platform's, and neither is claimed here. Ask for the deployment's state before\n")
	_, _ = fmt.Fprint(stdout, "  treating this as a deploy that is serving.\n")
}

// printRemoteDryRun is the remote engine's dry run: every step, and the fact
// that no request was made.
func printRemoteDryRun(stdout io.Writer, plan Plan) {
	_, _ = fmt.Fprintf(stdout, "\nthis is what cafe deploy would ask:\n\n")
	for _, step := range plan.Steps {
		_, _ = fmt.Fprintf(stdout, "  %s\n", step.Command())
		_, _ = fmt.Fprintf(stdout, "      %s\n", step.Why)
	}
	_, _ = fmt.Fprintf(stdout, "\nDry run, and nothing was asked: not the boundary question and not the request.\n")
	_, _ = fmt.Fprintf(stdout, "No account was queried and no release was requested on the platform.\n")
	_, _ = fmt.Fprintf(stdout, "\nDeploy it for real with:\n  cafe deploy --engine remote --account <uuid> %s --yes\n", plan.Service)
}
