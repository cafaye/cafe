package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/cafaye/caf/internal/failure"
)

// failureLongHelp is the prose under `caf failure check`'s usage line.
//
// The exit code is stated in the help because it is the part a script gets
// wrong. It is zero unless the observation itself could not be read: a
// condition holding is a finding and not a command failure, and the
// frozen-key entry holds on ANY box running muse (CAFAYE-OPS.md:748), so an
// exit code derived from "anything holds" would be red on a healthy one.
const failureLongHelp = `Checks a box against the cafaye failure-mode library — the documented
conditions of our own stack that a generic monitor cannot see
(moon/CAFAYE-OPS.md:731-748), evaluated against what the box daemon can
observe.

The observation is a JSON document: the facts the library's rules read, one
key per fact. Read it from <path>, or from stdin when no path is given (or
when the path is "-"). The document is the shape internal/failure's
Observation carries, so a producer of it is any code that can name the
services running, their settings' NAMES, the probe answers, the healthcheck
paths, the declared databases, the outbox depths, the publishers, the
collectors and the instrumented services.

NOTHING CONFIGURED IS READ. The observation carries the names of the
settings a service has set and never their values — the facts the ten rules
need are a name's presence and a probe's answer, which is why the library
can be checked without a credential anywhere in the process.

WHY THE INPUT IS A DOCUMENT. The box daemon's World is the seam that would
answer this on a real box, and it is not reachable from a caf process on a
workstation: it lives behind the outbound mTLS channel
(internal/boxd/daemon.go:101-162). A verb that read the box directly would
be a verb that only worked on a box, so this one takes the facts as data
and internal/failure.BoxObserver is what the box-side half looks like today.

A rule the observation cannot answer is reported as UNKNOWN, naming the fact
it needed. That is the specification of what the box owes this library, and
it is never reported as a pass — a green light over a blind spot is the
failure lock/verify.go refuses to produce.

Exit status: 0 when the report was printed, including when conditions hold.
Nonzero when the observation could not be read or decoded.`

func newFailureCommand() *Command {
	c := &Command{
		Name:    "failure check",
		Summary: "check an observation against the cafaye failure-mode library",
		Usage:   "caf failure check [<observation.json>]",
		LongHelp: failureLongHelp,
		Flags: func(fs *flag.FlagSet) {
			// No flags, and that is a decision: there is nothing to configure
			// about reading a document and printing what the rules say. The
			// first flag this command earns is the one that changes what is
			// checked, not how it is read.
			_ = fs
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 1, len(args)); err != nil {
			return err
		}
		obs, err := readObservation(args[0], env.Stdin)
		if err != nil {
			return fmt.Errorf("caf failure check: %w", err)
		}
		report := failure.Evaluate(failure.Library(), obs)
		fmt.Fprint(env.Stdout, report.String())
		fmt.Fprintf(env.Stdout, "caf failure: %s\n", report.Describe())
		return nil
	}
	return c
}

// readObservation decodes the observation document. A path of "" or "-" is
// stdin, so the verb composes: `caf box observe | caf failure check` is the
// shape the substrate packet's verb completes.
//
// A document that does not decode is refused with the JSON error's own
// sentence and the path it came from, because "your observation is wrong" is
// a sentence that sends a reader to a file they cannot fix.
func readObservation(path string, stdin io.Reader) (failure.Observation, error) {
	var (
		src    io.Reader
		closer io.Closer
		name   string
	)
	switch path {
	case "", "-":
		src, name = stdin, "<stdin>"
	default:
		f, err := os.Open(path)
		if err != nil {
			return failure.Observation{}, fmt.Errorf("read the observation: %w", err)
		}
		src, closer, name = f, f, path
	}
	if closer != nil {
		defer closer.Close()
	}

	var obs failure.Observation
	if err := json.NewDecoder(src).Decode(&obs); err != nil {
		return failure.Observation{}, fmt.Errorf("decode the observation in %s: %w", name, err)
	}
	return obs, nil
}
