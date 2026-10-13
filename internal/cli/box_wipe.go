package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cafaye/cafe/internal/setup"
)

// boxWipeDeps is what `cafe box wipe` needs from the world, as a parameter for
// the reason every other command's deps are: a test hands the command a host it
// owns and a confirmation it controls, and the refusal and the dry run are
// driven through the real command tree with no SSH in sight.
type boxWipeDeps struct {
	// host is the box, reached over SSH. Nil means a real SSHHost built from
	// the flags, and a test always sets it.
	host setup.Host
	// confirm asks the operator. It is a seam because a confirmation prompt read
	// from os.Stdin is a prompt no test can answer and no CI job can satisfy:
	// `cafe box wipe` in a pipeline must either be given --yes or fail, and both
	// of those have to be assertable. It is handed to setup.Wipe rather than
	// called here, because the read-only probes decide whether the host is even
	// a box, and a prompt that came first would ask an operator to approve
	// wiping a host the command was about to refuse.
	confirm func(prompt string) (bool, error)
}

func (d boxWipeDeps) withDefaults() boxWipeDeps {
	if d.confirm == nil {
		d.confirm = func(prompt string) (bool, error) { return askOnStdin("cafe box wipe", prompt) }
	}
	return d
}

// boxWipeFlags is `cafe box wipe`'s flag state. The SSH flags are the same six
// `cafe box setup` takes, because it is the same connection through the same
// transport: an operator should not have to learn a second spelling of "which
// key".
type boxWipeFlags struct {
	user     string
	port     int
	identity string
	password bool
	dryRun   bool
	yes      bool
	json     bool
}

// boxWipeLongHelp is the prose `cafe box wipe` prints under its usage line. It
// is the answer to "what will this remove, and what will it leave", written
// down where a person reads it before running a command that uninstalls a
// cluster.
const boxWipeLongHelp = `The inverse of ` + "`cafe box setup`" + `: it takes a box back to the state setup
expects a bare host to be in, and it is the command an end-to-end test, a
"start over" and a decommission all reach for instead of a hand-rolled SSH
script.

DESTRUCTIVE BY CONSTRUCTION. It names every step it will take and asks once
before the destructive half. ` + "`--yes`" + ` skips the question for a pipeline;
without it, a machine with nobody to answer is told to pass ` + "`--yes`" + ` rather
than left to guess. ` + "`--dry-run`" + ` prints the plan and removes nothing — and it
is the SAME table the real run walks, so the plan cannot describe a wipe the
command would not perform.

WHAT IT REMOVES, each as a check and an apply so a second run is a no-op:

  daemon-stopped        systemctl disable --now ` + setup.UnitName + `
  unit-removed          ` + setup.UnitPath + `
  cafe-binary-removed   ` + setup.CafeBinPath + `
  cafaye-state-removed  ` + setup.EtcCafeDir + `, ` + setup.EnrolledDir + `,
                        ` + setup.KitStatePath + `, ` + setup.CafayeBinDir + `,
                        ` + setup.CafayeSubstrate + `, ` + setup.CafayeEdge + `
  k3s-removed           the installer's own uninstall scripts, then
                        /etc/rancher, /var/lib/rancher, /var/lib/kubelet,
                        /root/.kube and the k3s files in /usr/local/bin
  docker-removed        stop/disable and purge, then /var/lib/docker,
                        /var/lib/containerd and /etc/docker

WHAT IT KEEPS. This is the command's whole safety argument, and it is a list of
cafaye's own artifacts rather than an ` + "`rm -rf`" + ` over a directory somebody else
may share:

  ` + setup.SSHAuthorizedKeys + `            the operator's own access. Nothing here touches it,
                        because a wipe that can lock you out is worse than no
                        wipe.
  ` + setup.CafayeReleases + `   the deploy path's releases: the artifacts the box was
                        SERVING, not an installation fact. ` + "`/var/lib/cafaye`" + ` is
                        removed subdirectory by subdirectory, never wholesale.
  everything else       any file cafaye did not create is left exactly as it
                        was found.

WHAT IT REFUSES. A host with no cafe binary, no cafaye state and no enrollment
is not a box this command set up, and it is refused with that sentence BEFORE
the confirmation and before a single removal — so a stray ` + "`cafe box wipe`" + `
against a stranger's machine cannot uninstall a k3s it did not install. A host
where this operator cannot get root is refused the same way.

The verification is the same as setup's: the document on stdout (or the human
block) says per step whether the thing was already gone or was removed, and the
exit is 0 only when every step concluded.`

func newBoxWipeCommand(deps boxWipeDeps) *Command {
	opts := &boxWipeFlags{}
	c := &Command{
		Name:     "box wipe",
		Summary:  "take a box back to a bare host: stop boxd, remove cafe's state, uninstall k3s and Docker — keeping your SSH access and the releases",
		Usage:    "cafe box wipe <host> [--dry-run] [--yes] [--json]",
		LongHelp: boxWipeLongHelp,
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.user, "user", "", "the login name; empty means ssh_config's own answer")
			fs.IntVar(&opts.port, "port", 0, "the SSH port; 0 means 22")
			fs.StringVar(&opts.identity, "key", "", "a private key to authenticate with; empty means the agent and the default locations")
			fs.BoolVar(&opts.password, "password", false, "authenticate with a password read from $"+setupPasswordEnv+" on stdin, never from argv")
			fs.BoolVar(&opts.dryRun, "dry-run", false, "print the plan and remove nothing")
			fs.BoolVar(&opts.yes, "yes", false, "remove without asking; required where nobody can answer the prompt")
			fs.BoolVar(&opts.json, "json", false, "print the machine document on stdout instead of the human one")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) != 1 {
			return wantArgs(c.Name, c.Usage, 1, len(args))
		}
		host := args[0]
		deps := deps.withDefaults()

		// --yes is a confirmation that returns true. It is the same code path as
		// the interactive one, so a pipeline and a person take the same walk.
		confirm := deps.confirm
		if opts.yes {
			confirm = func(string) (bool, error) { return true, nil }
		}

		res, wipeErr := setup.Wipe(env.Context, setup.WipeOptions{
			Host:    wipeHostFor(deps, host, opts),
			Name:    host,
			DryRun:  opts.dryRun,
			Confirm: confirm,
		})
		printWipeResult(env, res, opts.json)

		if wipeErr != nil {
			// A declined confirmation is not a refusal and not a failure: the
			// operator was asked and said no, and nothing was attempted. It is
			// still a non-zero exit, because a script that ran a wipe without
			// --yes must not read as success.
			if errors.Is(wipeErr, setup.ErrWipeDeclined) {
				fmt.Fprintf(env.Stderr, "cafe box wipe: nothing was removed. Run it with --yes when you mean it.\n")
				return errReported
			}
			fmt.Fprintf(env.Stderr, "cafe box wipe: %v\n", wipeErr)
			return errReported
		}
		return nil
	}
	return c
}

// wipeHostFor builds the host the command talks to. It is a method on the deps
// so a test's injected host wins, and so the real SSHHost is constructed
// exactly once, at the point of use. It is the SAME transport `cafe box setup`
// uses — internal/setup's SSHHost — rather than a second SSH implementation.
func wipeHostFor(deps boxWipeDeps, host string, opts *boxWipeFlags) setup.Host {
	if deps.host != nil {
		return deps.host
	}
	return setup.SSHHost{
		Name:         host,
		User:         opts.user,
		Port:         opts.port,
		IdentityFile: opts.identity,
		Password:     wipePassword(opts),
	}
}

// wipePassword reads the SSH password from the environment, the same variable
// and the same discipline as setup's: `--password` is a mode, not a value, and
// a password in argv is a password in `ps aux`.
func wipePassword(opts *boxWipeFlags) string {
	if !opts.password {
		return ""
	}
	return os.Getenv(setupPasswordEnv)
}

// printWipeResult writes the document and the human log, with the same stream
// split every other box verb uses: the human log goes to stderr, the machine
// document to stdout, always.
func printWipeResult(env *Env, res setup.WipeResult, asJSON bool) {
	for _, line := range res.Log {
		fmt.Fprintln(env.Stderr, line)
	}
	if len(res.Kept) > 0 {
		say(env.Stderr, "host %s: kept %s", res.Host, strings.Join(res.Kept, ", "))
	}
	if err := writeDoc(env.Stdout, res, asJSON); err != nil {
		fmt.Fprintf(env.Stderr, "cafe box wipe: write the document: %v\n", err)
	}
}

// writeWipeHuman renders the human half: the steps and what was kept. Plain
// text, because this repository has no TUI framework and `cafe box setup`
// prints the same shape.
func writeWipeHuman(w io.Writer, res setup.WipeResult) error {
	if _, err := fmt.Fprintf(w, "cafe box wipe %s\n", res.Host); err != nil {
		return err
	}
	for _, step := range res.Steps {
		if _, err := fmt.Fprintf(w, "  %-22s %-13s %s\n", step.Name, step.Action, step.Title); err != nil {
			return err
		}
	}
	if len(res.Kept) > 0 {
		if _, err := fmt.Fprintf(w, "  kept: %s\n", strings.Join(res.Kept, ", ")); err != nil {
			return err
		}
	}
	return nil
}
