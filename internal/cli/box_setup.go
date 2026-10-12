package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/cafaye/cafe/internal/setup"
)

// boxSetupDeps is what `cafe box setup` needs from the world, as a parameter for
// the reason every other command's deps are: a test hands the command a host it
// owns, and the idempotence proof and the single-redemption proof are driven
// through the real command tree with no SSH in sight.
type boxSetupDeps struct {
	// host is the box, reached over SSH. Nil means a real SSHHost built from
	// the flags, and a test always sets it.
	host setup.Host
	// selfBinary is the cafe binary to install on the host. Nil means
	// os.Executable, and a test points it at a scratch file.
	selfBinary func() (string, error)
}

// setupFlags is `cafe box setup`'s flag state. The capability's DEFAULT is
// stdin, not a value: a token typed on a command line is a token in the shell's
// history, in `ps aux` for as long as the process lives, and in the terminal
// scrollback of whoever looked over the operator's shoulder. It comes from the
// environment, or from stdin, and it goes to the host on a pipe.
type setupFlags struct {
	token      string
	user       string
	port       int
	identity   string
	password   bool
	rangeText  string
	controlURL string
	dryRun     bool
	json       bool
}

// setupTokenEnv is where the capability comes from when it is not on stdin. An
// environment variable rather than a flag for the reason above: argv is public
// and an environment is not.
const setupTokenEnv = "CAF_BOX_TOKEN"

// setupPasswordEnv is where the SSH password comes from. The same argument, and
// the same mechanism on the way to the host: an askpass helper reading a pipe.
const setupPasswordEnv = "CAF_BOX_PASSWORD"

func newBoxSetupCommand(deps boxSetupDeps) *Command {
	opts := &setupFlags{}
	c := &Command{
		Name:    "box setup",
		Summary: "take a bare host to a running box daemon: Docker, cafe, enroll, boxd, heartbeat",
		Usage:   "cafe box setup <host> [flags]",
		LongHelp: `The one command between a host nobody has touched and a box that answers.

It is kamal ` + "`setup`" + ` parity for the box side: SSH in, check what the host
already has, and install only what is missing — Docker the kamal way (installed,
running, in the docker group, checked in that order before ` + "`curl … | sh`" + ` is
ever reached), the cafe binary, the box's own enrollment, the daemon's identity
and unit, and the heartbeat that proves all of it landed.

WHAT RUNS WHERE. The keypair is generated ON THE HOST, by the host's own
` + "`cafe box enrol`" + `. This machine never holds a private key: what crosses the
SSH connection is the capability on its way in and the box's three non-secret
identifiers on the way out. That is the whole shape of the trust boundary, and
it is why a controller can be a laptop.

IDEMPOTENT. Every step asks the host a read-only question first. A second
` + "`cafe box setup`" + ` on a host that has had one runs nothing at all: no new
keypair, no second redemption of the capability, no rewritten unit. The
document reports ` + "`redemptions: 0`" + ` and the exit is 0.

THE CAPABILITY IS PRESENTED ONCE. box enforces single-use in its own database,
so a second presentation is always refused server-side and a client that
redeemed twice would have proved nothing. ` + "`cafe box enrol`" + ` on the host
journals the redemption before it writes anything, so a run that died between
them finishes on the next attempt without spending the token again.

DRY RUN. ` + "`--dry-run`" + ` prints the plan and asks the host nothing except
whether the operator can get root. No package is installed, no keypair is made.

THE TOKEN. ` + "`--token`" + ` reads the capability from $` + setupTokenEnv + `, or from
standard input when it is ` + "`-`" + `. It is never an argument, never written to a
file and never in an environment variable on the HOST — it reaches the
enrollment command's stdin over the SSH connection and exists nowhere else.`,
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.token, "token", "-", "the single-use capability: `-` reads it from stdin, never argv, never history")
			fs.StringVar(&opts.user, "user", "", "the login name; empty means ssh_config's own answer")
			fs.IntVar(&opts.port, "port", 0, "the SSH port; 0 means 22")
			fs.StringVar(&opts.identity, "key", "", "a private key to authenticate with; empty means the agent and the default locations")
			fs.BoolVar(&opts.password, "password", false, "authenticate with a password read from $"+setupPasswordEnv+" on stdin, never from argv")
			fs.StringVar(&opts.rangeText, "range", "", "the version constraint this box tracks, written into the unit as CAF_BOX_RANGE")
			fs.StringVar(&opts.controlURL, "control-plane", "", "the origin the box enrols against; empty means $"+boxURLEnv)
			fs.BoolVar(&opts.dryRun, "dry-run", false, "print the plan and run nothing on the host")
			fs.BoolVar(&opts.json, "json", false, "print the machine document on stdout instead of the human one")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) != 1 {
			return wantArgs(c.Name, c.Usage, 1, len(args))
		}
		host := args[0]
		opts, err := readSetupToken(env.Stdin, opts)
		if err != nil {
			return err
		}
		self, err := setupSelfBinary(deps)
		if err != nil {
			return err
		}
		res, setupErr := setup.Setup(env.Context, setup.Options{
			Host:         setupHostFor(deps, host, opts, env.Stderr),
			Name:         host,
			Capability:   opts.token,
			BinaryPath:   self,
			Range:        opts.rangeText,
			ControlPlane: opts.controlURL,
			Version:      env.Version.Semver,
			DryRun:       opts.dryRun,
		})
		printSetupResult(env, res, opts.json)
		if setupErr != nil {
			// The document is printed FIRST and the error returned second: the
			// steps that ran are the useful part, and a caller that only sees
			// the exit code has lost them.
			fmt.Fprintf(env.Stderr, "cafe box setup: %v\n", setupErr)
			return errReported
		}
		return nil
	}
	return c
}

// boxSetupOptions is the resolved state of one invocation, after the token has
// been read out of the environment or off stdin.
type boxSetupOptions struct {
	setupFlags
	// token is the capability. It is a field of a struct that lives for one
	// command and is handed to setup.Options, which is the last place in this
	// binary that holds it before it becomes bytes on a pipe.
	token string
}

// readSetupToken resolves the capability: the flag value, the environment, or
// stdin. The order is deliberate — stdin last because a stdin read on a
// command that was given the token another way would block forever on a
// terminal.
func readSetupToken(stdin io.Reader, opts *setupFlags) (*boxSetupOptions, error) {
	out := &boxSetupOptions{setupFlags: *opts}
	value := strings.TrimSpace(opts.token)
	switch value {
	case "", "-":
		// `-` is the operator's own word for "this comes on a pipe", and it is
		// the default because the alternative is a token in argv.
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return out, fmt.Errorf("cafe box setup: read the capability from stdin: %w", err)
		}
		if t := strings.TrimSpace(string(raw)); t != "" {
			out.token = t
			return out, nil
		}
		if fromEnv := strings.TrimSpace(os.Getenv(setupTokenEnv)); fromEnv != "" {
			out.token = fromEnv
			return out, nil
		}
		return out, fmt.Errorf("%w: cafe box setup wants the single-use capability on stdin or in $%s (usage: %s)",
			errUsage, setupTokenEnv, "cafe box setup <host> --token -")
	default:
		out.token = value
		return out, nil
	}
}

// setupHostFor builds the host the command talks to. It is a method on the deps
// so a test's injected host wins, and so the real one is constructed exactly
// once, at the point of use, rather than at construction time when the flags
// have not been parsed yet.
//
// The progress sink is wired here rather than inside setup.Setup because this
// is the only place in the tree that owns a stderr: a library that printed from
// inside a transfer would put operator prose into a stream some caller is
// parsing.
func setupHostFor(deps boxSetupDeps, host string, opts *boxSetupOptions, progress io.Writer) setup.Host {
	if deps.host != nil {
		return deps.host
	}
	real := setup.SSHHost{
		Name:         host,
		User:         opts.user,
		Port:         opts.port,
		IdentityFile: opts.identity,
		Password:     setupPassword(opts),
	}
	real.Progress = setupProgress(progress, host)
	return real
}

// setupProgress is what an operator watches while a 29 MB binary crosses a slow
// link: one line per chunk and a final one that says the bytes that arrived
// were proven to be the bytes sent. Both go to stderr, for the reason
// printSetupResult names — stdout is the document, and a machine parsing it
// must not have to skip progress lines.
func setupProgress(w io.Writer, host string) func(setup.WriteEvent, int, int) {
	if w == nil {
		return nil
	}
	return func(event setup.WriteEvent, wrote, total int) {
		switch event {
		case setup.WriteChunk:
			fmt.Fprintf(w, "cafe box setup: %s of %s written to %s\n", bytesHuman(wrote), bytesHuman(total), host)
		case setup.WriteVerified:
			fmt.Fprintf(w, "cafe box setup: %s on %s written and verified — the host's sha256 matches the local binary\n",
				bytesHuman(total), host)
		}
	}
}

// bytesHuman renders a byte count for a progress line: bytes under a kibibyte,
// one decimal above it. "27.9 MiB" is a number an operator holds in their head
// while they wait, and "29250507" is a number nobody reads.
func bytesHuman(n int) string {
	const (
		ki = 1024.0
		mi = ki * 1024.0
	)
	switch f := float64(n); {
	case f < ki:
		return strconv.Itoa(n) + " B"
	case f < mi:
		return strconv.FormatFloat(f/ki, 'f', 1, 64) + " KiB"
	default:
		return strconv.FormatFloat(f/mi, 'f', 1, 64) + " MiB"
	}
}

// setupPassword reads the SSH password from the environment. It is a function
// rather than a field assignment so there is exactly one place that names the
// variable, and so the flag itself never carries it — `--password` is a mode,
// not a value.
func setupPassword(opts *boxSetupOptions) string {
	if !opts.password {
		return ""
	}
	return os.Getenv(setupPasswordEnv)
}

// setupSelfBinary finds the cafe binary to install on the host: the injected
// one in a test, the running executable otherwise. It is resolved through a
// function so the command does not read the filesystem at construction time,
// which is when no flags have been parsed.
func setupSelfBinary(deps boxSetupDeps) (string, error) {
	if deps.selfBinary != nil {
		return deps.selfBinary()
	}
	return os.Executable()
}

// printSetupResult writes the document and the human log. The log goes to
// stderr and the document to stdout, ALWAYS — not only in the human mode —
// because that is the split this repository holds everywhere else (`cafe agent
// register` is the other half of it): an agent pipes stdout into a parser and a
// person reads stderr, and a flag that chose which stream got what would be a
// flag that guaranteed one of the two was wrong in some script.
func printSetupResult(env *Env, res setup.Result, asJSON bool) {
	for _, line := range res.Log {
		fmt.Fprintln(env.Stderr, line)
	}
	if res.Enrolled != nil {
		say(env.Stderr, "host %s holds box %s; %d capability presented on this setup",
			res.Host, res.Enrolled.BoxID, res.Redemptions)
	}
	if err := writeDoc(env.Stdout, res, asJSON); err != nil {
		fmt.Fprintf(env.Stderr, "cafe box setup: write the document: %v\n", err)
	}
}
