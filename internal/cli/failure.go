package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/anywaye/cafe/internal/failure"
)

// failureDeps is what `cafe failure check` needs from the world, and it is a
// struct of parameters for the reason resolveDeps and deployDeps are: a test
// hands the command a reader it owns and drives the whole path — a document
// in, eleven rules over it, a report out — without a file, a box, or a stream the
// test cannot see.
type failureDeps struct {
	// stdin is where a document with no path comes from. Nil means the
	// process's own input, and a test always sets it: reading os.Stdin in a
	// test is a test that hangs rather than one that fails.
	stdin io.Reader
}

func (d failureDeps) withDefaults() failureDeps {
	if d.stdin == nil {
		d.stdin = os.Stdin
	}
	return d
}

// failureLongHelp is the prose under `cafe failure`'s usage line.
//
// The exit code is stated in the help because it is the part a script gets
// wrong. It is zero unless the observation itself could not be read: a
// condition holding is a finding and not a command failure, and the
// frozen-key entry holds on ANY box running muse (ANYWAYE-OPS.md:748), so an
// exit code derived from "anything holds" would be red on a healthy one.
const failureLongHelp = `Checks a box against the anywaye failure-mode library — the documented
conditions of our own stack that a generic monitor cannot see
(anywaye/notes/ops.md:731-748), evaluated against what the box daemon can
observe.

The observation is a JSON document: the facts the library's rules read, one
key per fact. Read it from <observation.json>, or from stdin when no path is
given (or when the path is "-"). The document is the shape internal/failure's
Observation carries, so a producer of it is any code that can name the
services running, their settings' NAMES, the probe answers, the healthcheck
paths, the declared databases, the outbox depths, the publishers, the
collectors and the instrumented services.

NOTHING CONFIGURED IS READ. The observation carries the names of the settings
a service has set and never their values — the facts the eleven rules need are a
name's presence and a probe's answer, which is why the library can be checked
with no credential anywhere in this process.

WHY THE INPUT IS A DOCUMENT. The box daemon's World is the seam that would
answer this on a real box, and it is not reachable from a cafe process on a
workstation: it lives behind the outbound mTLS channel
(internal/boxd/daemon.go:101-162). A verb that read the box directly would be
a verb that only worked on a box, so this one takes the facts as data, and
internal/failure.BoxObserver is what the box-side half looks like today: it
supplies the service names and nothing else, which is why a box as it stands
answers UNKNOWN rather than pass.

A rule the observation cannot answer is reported as UNKNOWN, naming the fact
it needed. That is the specification of what the box owes this library, and it
is never reported as a pass — a green light over a blind spot is the failure
lock/verify.go refuses to produce.

Exit status: 0 when the report was printed, including when conditions hold.
Nonzero when the observation could not be read or decoded.`

// newFailureCommand is the parent. It holds one verb and dispatches to it the
// way `cafe gen` does, because a command that groups verbs never registers
// itself in Commands() as the verb — the parent does, and the parent's row is
// what stub_test.go's workingCommands carries.
func newFailureCommand(deps failureDeps) *Command {
	deps = deps.withDefaults()

	c := &Command{
		Name:     "failure",
		Summary:  "check a box against the anywaye failure-mode library",
		Usage:    "cafe failure check [<observation.json>]",
		LongHelp: failureLongHelp,
	}
	c.Run = func(args []string, env *Env) error {
		// Bare `cafe failure` is a question ("what can this do?"), not a
		// mistake, so it answers rather than exiting 2 — the same call `cafe
		// gen` makes.
		if len(args) == 0 {
			printHelp(env.Stdout, c)
			return nil
		}
		if args[0] != "check" {
			return fmt.Errorf("%w: unknown cafe failure verb %q (usage: cafe failure check)", errUsage, args[0])
		}
		return failureCheckCommand(deps).Execute(env, args[1:])
	}
	return c
}

// failureCheckCommand is the verb. It is named with its full invocation so its
// usage errors read as the words the person typed.
func failureCheckCommand(deps failureDeps) *Command {
	c := &Command{
		Name:    "failure check",
		Summary: "evaluate the library's rules against an observation document",
		Usage:   "cafe failure check [<observation.json>]",
		LongHelp: "Reads an observation document — the facts the failure-mode library's rules read,\n" +
			"as JSON — evaluates the library's eleven rules against it, and prints which\n" +
			"conditions hold, which do not, and which the observation could not answer.\n" +
			"\n" +
			"With no path (or \"-\") the document is read from stdin. Run `cafe failure` for\n" +
			"what the document carries and why nothing configured is ever read.",
		Flags: func(fs *flag.FlagSet) {
			// No flags, and that is a decision rather than an omission: there
			// is nothing to configure about reading a document and printing
			// what the rules say. The first flag this verb earns changes WHAT
			// is checked, not how it is read.
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 1, len(args)); err != nil {
			return err
		}
		obs, err := readObservation(args[0], deps.stdin)
		if err != nil {
			return fmt.Errorf("cafe failure check: %w", err)
		}
		report := failure.Evaluate(failure.Library(), obs)
		_, _ = fmt.Fprint(env.Stdout, report.String())
		_, _ = fmt.Fprintf(env.Stdout, "cafe failure: %s\n", report.Describe())
		return nil
	}
	return c
}

// readObservation decodes the observation document. A path of "" or "-" is
// stdin, so the verb composes: `cafe box observe | cafe failure check` is the
// shape the substrate packet's verb completes.
//
// A document that does not decode is refused with the JSON error's own
// sentence and the path it came from, because "your observation is wrong" is a
// sentence that sends a reader to a file they cannot fix.
func readObservation(path string, stdin io.Reader) (failure.Observation, error) {
	var (
		src    io.Reader
		closer io.Closer
		name   string
	)
	switch strings.TrimSpace(path) {
	case "", "-":
		if stdin == nil {
			return failure.Observation{}, fmt.Errorf("no path was given and there is no stdin to read the observation from")
		}
		src, name = stdin, "<stdin>"
	default:
		f, err := os.Open(path)
		if err != nil {
			return failure.Observation{}, fmt.Errorf("read the observation: %w", err)
		}
		src, closer, name = f, f, path
	}
	if closer != nil {
		defer func() { _ = closer.Close() }()
	}

	var obs failure.Observation
	if err := json.NewDecoder(src).Decode(&obs); err != nil {
		return failure.Observation{}, fmt.Errorf("decode the observation in %s: %w", name, err)
	}
	return obs, nil
}
