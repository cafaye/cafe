// Package cli implements the cafe command line: the hand-rolled router, the
// subcommand registry and one file per subcommand.
//
// The layout follows the pipeline-stage pattern the GoReleaser CLI uses: the
// router knows nothing about any subcommand, and each subcommand owns its own
// flags, help and run function, so a subcommand can be read, changed and
// tested without touching the others.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/anywaye/cafe/internal/emitter"
	"github.com/anywaye/cafe/internal/features"
	"github.com/anywaye/cafe/internal/skills"
)

// errUsage marks a mistake in how cafe was invoked, as opposed to a command
// that ran and failed. It is what separates exit code 2 from exit code 1.
var errUsage = errors.New("usage")

// errReported marks a command that has already said everything it has to say
// on stdout — a lint report with an invalid manifest in it, a resolve that
// came back "no". The verdict is the report; printing it a second time on
// stderr would put the same sentence in two places, one of which a CI log
// greps and the other of which nobody reads. It still exits 1: something was
// checked and it did not pass.
var errReported = errors.New("reported")

// exitCoder is an error that names the process exit code itself, for the one
// case where cafe is a parent rather than the thing being judged: `cafe env up`
// runs a command under test, and a CI job that read 0 out of a red suite would
// be a green badge for a red run — the exact invisible red run this binary
// exists to make impossible.
//
// It is an interface rather than a sentinel because the code is the child's, not
// one of cafe's three, and inventing a fourth cafe-wide code for it would be worse
// than a command that passes one through.
type exitCoder interface {
	error
	cafeExitCode() int
}

// Options is one CLI invocation. main fills it from the process; tests fill it
// from buffers.
type Options struct {
	// Version is the build identity reported by `cafe version`.
	Version Version
	// Args is the argument list without the program name.
	Args []string
	// Stdout and Stderr receive help, reports and errors. A nil writer is
	// discarded, so a caller that only wants an exit code can pass nothing.
	Stdout io.Writer
	Stderr io.Writer
	// Stdin is what a command that reads one gets. Nil means the process's
	// own, and a test sets it so `cafe box setup --token -` can be driven
	// without touching the test binary's standard input.
	Stdin io.Reader
	// Context is canceled when the process is interrupted. A nil context is a
	// background one, so a caller that only wants an exit code passes nothing.
	Context context.Context
	// Features is the global feature flag service. Nil means "use defaults".
	Features *features.Service
}

// Env is what a command is handed when it runs: the build identity and the two
// streams it may write to. Commands take no globals, which is what makes them
// testable.
type Env struct {
	Version Version
	Stdout  io.Writer
	Stderr  io.Writer
	// Stdin is what a command may read. It is a third stream rather than the
	// process's own standard input for the reason the other two are streams:
	// `cafe box setup --token -` takes the capability off a pipe, and a test
	// has to be able to hand it one without redirecting the test binary's own
	// standard input. Nil means the process's stdin, which is what a real
	// invocation wants.
	Stdin io.Reader
	// Context is canceled when the process is interrupted. It is optional: a
	// caller that only wants an exit code passes nothing, and the router fills
	// in a background context. `cafe dev` is the one command that reads it, and
	// it is there rather than read from a package-level variable so a test can
	// cancel it without sending itself a signal.
	Context context.Context
	// Features is the global feature flag service.
	Features *features.Service
}

// Run executes one invocation and returns the process exit code:
//
//	0  the command succeeded, or the user asked for help
//	1  the command ran and failed
//	2  cafe was invoked wrongly (unknown command or flag, bad argument count)
//
// This is where the invocation's one span is opened and closed, and the reason it
// is HERE rather than in `main` is that `Run` is where the outcome is known. A
// span ended in `main` would have to be handed the exit code across a boundary,
// and a boundary that carries an exit code is a second place the mapping from
// exit code to failure lives — which is the mapping core's `error.type`
// vocabulary exists to make single.
func Run(opts Options) int {
	if opts.Features == nil {
		opts.Features = features.New(features.Config{})
	}
	env := opts.env()
	env.Features = opts.Features
	// Auto-install skills on first run (idempotent, silent on success).
	if len(opts.Args) > 0 && opts.Args[0] != "skills" {
		if _, err := skills.EnsureDefaultInstalled(); err != nil {
			// Silent failure — skills are optional, don't block the command.
			_ = err
		}
	}
	session := installTelemetry(env)
	ctx, endInvocation := session.StartInvocation(env.Context, opts.Args)

	err := newRoot(env).execute(withContext(ctx, env), opts.Args)
	code := codeFor(env, err)
	if spanErr := endInvocation(outcomeForCode(code, err)); spanErr != nil {
		// Nothing here can act on this: a span that will not end is telemetry
		// that will not arrive, and the command the user asked for has already
		// run. Saying so on stderr and moving on is the only honest option —
		// swallowing it silently would make a broken exporter indistinguishable
		// from a disabled one, which is the confusion core's
		// otel-endpoint.schema.json exists to prevent.
		_, _ = fmt.Fprintf(env.Stderr, "cafe: could not end the invocation span: %v\n", spanErr)
	}
	// A flush error is not printed. `emitter.Shutdown` already returns nil for
	// the one failure a developer hits constantly — a collector that is not
	// running — and anything else it does return is a fault worth surfacing,
	// which core's noOp.warnings rule cannot mean for a CLI whose every command
	// would then carry it.
	if err := session.Shutdown(ctx); err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe: could not flush telemetry: %v\n", err)
	}
	return code
}

// codeFor maps an error to an exit code and prints it, and it is the whole of the
// router's error handling in one function so the three classes have one home.
func codeFor(env Env, err error) int {
	if err == nil {
		return exitSuccess
	}
	// A command that is a parent of the thing being judged passes that thing's
	// status through, and says nothing of its own: the child already wrote its
	// reason to stderr, and a second copy of it would be noise in a log a person
	// is reading once.
	var coded exitCoder
	if errors.As(err, &coded) {
		return coded.cafeExitCode()
	}
	// The command already printed its own report; the exit code is all that is
	// left to say, and stderr stays empty so the report is not duplicated.
	if errors.Is(err, errReported) {
		return exitFailure
	}

	_, _ = fmt.Fprintf(env.Stderr, "cafe: %v\n", err)
	if errors.Is(err, errUsage) {
		return exitUsage
	}
	return exitFailure
}

// outcomeForCode is the exit code to core's `error.type` vocabulary.
//
// A usage error is `invalid_request` because the person typed the command
// wrongly, which is the one class where the cause is outside the process — see
// emitter.OutcomeUsage for why filing it as an internal error would put a typo
// in the same dashboard bucket as a cafe bug.
//
// A canceled context is `canceled` for the same reason `cafe dev`'s Ctrl-C is:
// an operator stopping a long command is not an error an alert should page on.
func outcomeForCode(code int, err error) emitter.Outcome {
	switch {
	case code == exitSuccess:
		return emitter.OutcomeSuccess
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return emitter.OutcomeCancelled
	case code == exitUsage:
		return emitter.OutcomeUsage
	default:
		return emitter.OutcomeFailure
	}
}

// installTelemetry wires the SDK for this process.
//
// It reads the switches BEFORE any command runs and cannot fail the invocation:
// core's endpoint contract makes the endpoint `required: false`, so a cafe whose
// collector is unreachable still runs every command it has always run. The
// function exists as a seam rather than being inlined into Run because a test
// has to be able to prove the no-op path, and the only way to prove that is to
// run `Run` with a controlled environment.
func installTelemetry(env Env) emitter.Session {
	session, err := emitter.Install(env.Context, emitter.Options{
		Version:     env.Version.Semver,
		Environment: os.Getenv("DEPLOYMENT_ENVIRONMENT"),
		TenantID:    os.Getenv("TENANT_ID"),
	})
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe: telemetry is unavailable (%v); running without it.\n", err)
	}
	return session
}

// withContext returns an Env whose context is the one the invocation span lives
// in, so a command that reads `env.Context` — `dev` is the one that does — sees
// the span and not the bare background context.
func withContext(ctx context.Context, env Env) Env {
	env.Context = ctx
	return env
}

// Exit codes are a contract: scripts branch on them, so they do not change
// without a version bump.
const (
	exitSuccess = 0
	exitFailure = 1
	exitUsage   = 2
)

// env fills in the defaults: a discarded stream rather than a nil writer, and a
// development version when the build did not inject one.
func (o Options) env() Env {
	version := o.Version
	if version.Semver == "" {
		version.Semver = devVersion
	}
	if version.Commit == "" {
		version.Commit = unknownCommit
	}
	ctx := o.Context
	if ctx == nil {
		ctx = context.Background()
	}
	return Env{
		Version:  version,
		Stdout:   writerOrDiscard(o.Stdout),
		Stderr:   writerOrDiscard(o.Stderr),
		Stdin:    readerOrStdin(o.Stdin),
		Context:  ctx,
		Features: o.Features,
	}
}

func readerOrStdin(r io.Reader) io.Reader {
	if r != nil {
		return r
	}
	return os.Stdin
}

func writerOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// root routes "cafe <command> [flags] [arguments]" to the registry. It knows
// nothing about individual commands.
type root struct {
	env      Env
	commands []*Command
}

func newRoot(env Env) *root {
	return &root{env: env, commands: Commands()}
}

func (r *root) execute(env Env, args []string) error {
	if len(args) == 0 {
		printRootHelp(r.commands, env.Stdout)
		return nil
	}

	switch args[0] {
	case "help", "-h", "--help":
		return r.help(env, args[1:])
	case "-v", "--version":
		return printVersion(env)
	}
	return r.run(env, args[0], args[1:])
}

// help implements `cafe help` and `cafe help <command>`. It prints; it never runs
// anything.
func (r *root) help(env Env, args []string) error {
	if len(args) == 0 || args[0] == "help" {
		printRootHelp(r.commands, env.Stdout)
		return nil
	}
	c, found := lookupCommand(r.commands, args[0])
	if !found {
		return unknownCommand(args[0])
	}
	printHelp(env.Stdout, c)
	return nil
}

func (r *root) run(env Env, name string, args []string) error {
	c, found := lookupCommand(r.commands, name)
	if !found {
		return unknownCommand(name)
	}
	return c.Execute(&env, args)
}

func unknownCommand(name string) error {
	return fmt.Errorf("%w: unknown command %q (run \"cafe help\" for the command list)", errUsage, name)
}

// printRootHelp writes the top-level usage: what cafe is, the command table and
// the two global flags.
func printRootHelp(commands []*Command, w io.Writer) {
	_, _ = fmt.Fprint(w, "cafe - the anywaye platform CLI\n\n")
	_, _ = fmt.Fprint(w, "Usage:\n  cafe <command> [flags] [arguments]\n  cafe help <command>\n\n")
	_, _ = fmt.Fprint(w, "Commands:\n")

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range commands {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", c.Name, c.Summary)
	}
	_, _ = fmt.Fprintf(tw, "  %s\t%s\n", "help", "show help for a command")
	_ = tw.Flush()

	_, _ = fmt.Fprint(w, "\nFlags:\n")
	_, _ = fmt.Fprint(w, "  -h, --help      show this help\n")
	_, _ = fmt.Fprint(w, "  -v, --version   print the version\n\n")
	_, _ = fmt.Fprint(w, "Every subcommand has its own help: cafe help <command>\n")
}

// printHelp writes one command's help: its summary, its usage line, any prose
// the command adds, and every flag it declares, with defaults.
func printHelp(w io.Writer, c *Command) {
	_, _ = fmt.Fprintf(w, "cafe %s - %s\n\n", c.Name, c.Summary)
	_, _ = fmt.Fprintf(w, "Usage:\n  %s\n", c.Usage)
	if c.LongHelp != "" {
		_, _ = fmt.Fprintf(w, "\n%s", c.LongHelp)
	}

	fs := c.FlagSet()
	if countFlags(fs) == 0 {
		return
	}
	fs.SetOutput(w)
	_, _ = fmt.Fprint(w, "\nFlags:\n")
	fs.PrintDefaults()
}

func countFlags(fs *flag.FlagSet) int {
	count := 0
	fs.VisitAll(func(*flag.Flag) { count++ })
	return count
}
