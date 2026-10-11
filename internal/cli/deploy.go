package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/cafaye/cafe/internal/deploy"
)

// deployDeps is what `cafe deploy` needs from the world: a way to run Kamal.
//
// It is a parameter rather than a package-level variable for the same reason
// `devDeps` and `reclaimDeps` are: a test can hand the command a runner that
// records argv and answers with whatever the case planted, which is the only
// reason a dry run, a refusal, a partial failure and a successful deploy are
// testable in this repository without a VPS.
type deployDeps struct {
	runner deploy.Runner
	// confirm asks the operator. It is a seam because a confirmation prompt read
	// from os.Stdin is a prompt no test can answer and no CI job can satisfy:
	// `cafe deploy` in a pipeline must either be given -yes or fail, and both of
	// those have to be assertable.
	confirm func(prompt string) (bool, error)
}

func (d deployDeps) withDefaults() deployDeps {
	// The runner is deliberately NOT defaulted here. The router passes the
	// zero value, and a default filled in at this point would be a Kamal
	// runner — which runner() would then prefer over the engine the
	// --engine flag selected, because it can no longer tell a test's
	// injected runner from a default it filled in itself. So the runner
	// comes from runner(), which resolves the engine, and a test's
	// injection is the only thing that reaches runner() through this
	// field.
	if d.confirm == nil {
		d.confirm = confirmOnStdin
	}
	return d
}

// deployOptions is the parsed flag state for `cafe deploy`.
type deployOptions struct {
	environment string
	dryRun      bool
	yes         bool
	version     string
	// engine selects the deploy engine: deploy.EngineKamal (the default, the
	// engine the fleet runs) or deploy.EngineArgo (the launch substrate's,
	// where there is no Kamal).
	engine string
	// account is the account on the cafaye-operated platform that --engine
	// remote serves. It is refused with the other two engines rather than
	// hidden behind the gated one, so the flag's shape does not change when the
	// tenancy decision lands — and an operator who says which account is
	// answered rather than silently ignored.
	account string
}

// deployLongHelp is the prose `cafe deploy` prints under its usage line. It is
// the answer to "what do I type", written down where a person reads it before
// running a command that changes a server.
const deployLongHelp = `One command deploys a service, and the engine it uses is the one the
substrate runs. --engine selects it: kamal (the default, the engine the fleet
runs) or argo (the launch substrate's, where there is no Kamal and CI never
holds cluster credentials).
KAMAL, the self-host engine. It drives Kamal: Kamal installs Docker on the host
if the host lacks it, boots the accessories, builds and pushes the image, and
rolls the release out behind kamal-proxy only once the new container answers its
healthcheck. cafe does not decompose that, because a second implementation of a
rolling deploy is a second opinion about what a deploy is. The one command cafe
runs that changes anything is "kamal setup", because that is the one that also
creates the database on a fresh host; "kamal deploy" is the last third of it
alone. It needs config/deploy.yml, the Kamal configuration, copied from
cafaye/kit, and the five KIT_* variables exported in the shell that runs it.
Credentials are NAMES in .kamal/secrets and never in the config. It refuses an
accessory that publishes its port — a "port: 5432" line on the postgres
accessory — because that puts the database on every interface the host has.

ARGO, the pull-only substrate. Argo is pull-only: CI never holds cluster
credentials, and the only thing that changes the cluster is a commit. So a
deploy is a values commit, a push, and a wait for Argo to sync — never a
"kubectl apply" of a workload, never "helm", never a cluster credential in CI.
cafe resolves the service repository's chart/values.yaml — the values the OCI
chart ships — bumps the image tag and the chart version, commits the two files
with a message naming the service, the version and the commit it was built from,
pushes, and polls the Application's status until Argo has synced the new
revision and the service is healthy. The chart version is the OCI tag: the
publish workflow pushes it, and the Application's targetRevision must move to
it for Argo to sync. That second move is a commit in kit or an operator action —
cafe writes commits, never the cluster, and never another repository's tree — so
the sync-wait refuses to claim a deploy that leaves targetRevision behind. It
refuses a dirty working tree, a service repository with no git remote, a chart
whose values or image reference cannot be found, and an Argo that is not
reachable or never converges.

ONE PATH. cafe deploy <service> --engine <engine> --yes, from the project
directory. --dry-run prints every command that would run — and, under Argo, the
exact commit and the two files it would write — and executes nothing. Without
--yes it asks first, and a machine with nobody to answer is told to pass --yes
rather than left to guess.

REMOTE, the cafaye-operated platform, and it is pinned off. --engine remote is
the third engine: it asks the operated platform which deployment serves the
named --account, and hands it one deploy request — the service, the version and
the commit it was built from. --account goes with it and with nothing else; the
other two engines deploy somewhere you already pointed them at, so they have no
account to name. The customer's app is built once and deployed once, and every
client they serve is an account on the shared platform rather than a cafaye
deployment of its own, which is the point of the platform.

It does not deploy anything today, and the refusal is deliberate rather than a
gap in the work: one deployment serving many accounts is only safe once the
platform is proven safe for strangers, and that proof is TENANCY-GATE.md's
TG-D1 — a decision recorded in the moon repository. The mechanism is built and
tested, and the refusal names the decision it is waiting on. Until TG-D1 is
recorded, use kamal or argo.`

// newDeployCommand takes its seams, for the same reason newDevCommand and
// newReclaimCommand do: a test can hand the command a runner that records argv
// and a confirmation it controls, which is the only reason a dry run, a refusal,
// a partial failure and a successful deploy are testable in this repository
// without a VPS. The router passes the zero value and gets the real wiring.
func newDeployCommand(deps deployDeps) *Command {
	opts := &deployOptions{}
	c := &Command{
		Name:     "deploy",
		Summary:  "deploy a service: kamal (build, push, roll out), argo (a values commit, then wait for sync) or remote (the cafaye-operated platform, pinned off)",
		Usage:    "cafe deploy [flags] <service>",
		LongHelp: deployLongHelp,
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.environment, "env", "", "environment overlay to deploy, as config/deploy.<env>.yml merged over config/deploy.yml")
			fs.BoolVar(&opts.dryRun, "dry-run", false, "print every command that would run; changes nothing")
			fs.BoolVar(&opts.yes, "yes", false, "deploy without asking; required where nobody can answer the prompt")
			fs.StringVar(&opts.version, "version", "", "pin the release to this name; without it the engine decides")
			fs.StringVar(&opts.engine, "engine", "", "deploy engine: kamal (the default, the engine the fleet runs), argo (the launch substrate's, where there is no Kamal) or remote (the cafaye-operated platform, pinned off until TG-D1)")
			fs.StringVar(&opts.account, "account", "", "the cafaye account --engine remote serves; refused with the other two engines")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 1, len(args)); err != nil {
			return err
		}
		// The options are copied into the run and the seams are filled in inside
		// it, so a Command executed twice — which the router does not do but a test
		// does — cannot carry a flag value or a recorded argv from one run into the
		// next.
		return (deployRun{opts: *opts, service: args[0], deps: deps, env: env}).run()
	}
	return c
}

// deployRun is one `cafe deploy` invocation, gathered so the steps read top to
// bottom and can be tested without a closure capturing four variables.
type deployRun struct {
	opts    deployOptions
	service string
	deps    deployDeps
	env     *Env
}

// runner returns the engine's runner. A test injects a runner to record what
// would run; the wiring selects by the --engine flag, and an unknown engine is
// a usage error rather than a silent default — a typo is not a reason to deploy
// against the wrong substrate.
//
// The engine is validated even when a runner is injected: the injection is a
// test seam and must not bypass the flag's validation, or a typo in --engine
// would silently run the default engine under a test's fake — which is the one
// outcome the validation exists to prevent.
func (r deployRun) runner() (deploy.Runner, error) {
	resolved, err := deploy.RunnerFor(r.opts.engine)
	if err != nil {
		return nil, err
	}
	if r.deps.runner != nil {
		return r.deps.runner, nil
	}
	return resolved, nil
}

func (r deployRun) run() error {
	r.deps = r.deps.withDefaults()
	// --account names the one thing that makes a remote deploy a deploy, so
	// passing it to an engine that has no use for it is refused rather than
	// quietly dropped: an operator who typed --account meant to say which
	// account, and the answer "ignored" is a destination nobody was given.
	if r.opts.account != "" && r.opts.engine != deploy.EngineRemote {
		return fmt.Errorf("%w: --account is the cafaye-operated platform's, so it goes with --engine %s: "+
			"--engine %s has no account to name, because it deploys somewhere you already pointed it at",
			errUsage, deploy.EngineRemote, engineName(r.opts.engine))
	}
	runner, err := r.runner()
	if err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}

	// The dry run happens before the prompt, and that order is the point: a
	// refusal or a plan that says "this is wrong" is more useful than a question
	// about whether to go ahead and do it. Somebody who typed `cafe deploy` to see
	// what it would do gets the answer, not a prompt.
	if r.opts.dryRun {
		return deploy.Run(ctxOrBackground(r.env), runner, deploy.Request{
			Service: r.service,
			Env:     r.opts.environment,
			Version: r.opts.version,
			DryRun:  true,
			Engine:  r.opts.engine,
			Account: r.opts.account,
		}, r.out(), r.errOut())
	}

	ok, err := r.ask()
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintf(r.out(), "cafe deploy: nothing was deployed. Run it with --yes when you mean it.\n")
		return errReported
	}

	err = deploy.Run(ctxOrBackground(r.env), runner, deploy.Request{
		Service: r.service,
		Env:     r.opts.environment,
		Version: r.opts.version,
		Engine:  r.opts.engine,
		Account: r.opts.account,
	}, r.out(), r.errOut())
	// A refusal and a failed deploy are told apart by the sentinel, and the
	// difference is what the exit means to a script. Both exit 1; only one of
	// them is fixed by editing a file.
	if err != nil && isDeployRefusal(err) {
		// The refusal is printed in full on stdout by the package, several
		// sentences about a mechanism. It is the report, so it is not repeated on
		// stderr — a script greps one and a human reads the other.
		return errReported
	}
	return err
}

// isDeployRefusal asks the sentinel rather than the message, so a reworded
// refusal cannot quietly stop being recognised as one.
func isDeployRefusal(err error) bool {
	return errors.Is(err, deploy.ErrRefused)
}

// engineName is the engine as a message says it, because the default is the
// empty string and "not --engine " is a sentence with a hole in it.
func engineName(engine string) string {
	if engine == "" {
		return deploy.EngineKamal + " (the default)"
	}
	return engine
}

// ask is the confirmation, and it is the only interactive thing cafe does.
//
// A dry run never reaches it. --yes skips it. Everything else asks once, naming
// the service and the environment, because a prompt that does not say what it is
// about is a prompt people approve without reading. Under the remote engine it
// names the account too, because the account is the destination and a prompt
// that named only the service would be approving a deploy to no stated place.
func (r deployRun) ask() (bool, error) {
	if r.opts.yes {
		return true, nil
	}
	where := r.opts.environment
	if where == "" {
		where = "config/deploy.yml"
	} else {
		where = "config/deploy." + where + ".yml over config/deploy.yml"
	}
	if r.opts.engine == deploy.EngineRemote {
		// The account is the destination, so a prompt that named only the
		// service would be approving a deploy to no stated place — and a prompt
		// that printed an empty account shows the reader a hole where the
		// destination should be, which reads as cafe's bug rather than theirs.
		// It says so in as many words, and names the flag that fills it.
		if r.opts.account == "" {
			where = "the operated platform, and NO ACCOUNT is named — pass --account <uuid>, because the account is what the platform serves"
		} else {
			where = "the operated platform, account " + r.opts.account
		}
	}
	return r.deps.confirm(fmt.Sprintf("cafe deploy: deploy %s to %s? [y/N] ", r.service, where))
}

// out and errOut are the shared ones from confirm.go, so a nil writer is handled
// in one place for both commands that report.
func (r deployRun) out() io.Writer    { return outOrDiscard(r.env) }
func (r deployRun) errOut() io.Writer { return errOrDiscard(r.env) }
