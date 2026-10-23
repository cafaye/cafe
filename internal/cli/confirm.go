package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/anywaye/cafe/internal/agent"
	"github.com/anywaye/cafe/internal/box"
)

// The one interactive thing cafe does, shared by the commands that ask.
//
// It is here rather than in deploy.go because the subtle part is not the prompt
// text — it is that a closed or empty stdin must answer "no" rather than block.
// `cafe deploy` in a CI job with no terminal and no --yes has to fail with a
// sentence naming the missing flag and must not sit there holding a deploy lock,
// and `cafe backup` has to fail the same way rather than half-booting an
// accessory. Two copies of that behavior is two places to get it wrong, and the
// second one would be the one nobody reads.
//
// The command name is a parameter because the message has to read as the words
// the user typed, and `cafe deploy`'s error naming `cafe backup`'s flag would send
// somebody to the wrong help.

// errNoPrompt is the sentinel for "there was nobody to ask". It is a sentinel so
// a test can assert the refusal without matching prose, and so the same condition
// is one value rather than two messages that drift apart.
var errNoPrompt = errors.New("no terminal to confirm on")

// confirmOnStdin reads one line from the process's own input.
func confirmOnStdin(prompt string) (bool, error) { return askOnStdin("cafe deploy", prompt) }

func askOnStdin(command, prompt string) (bool, error) {
	if prompt == "" {
		return false, errNoPrompt
	}
	// The newline is added HERE and not left to the caller's sentence, for a reason
	// that is not cosmetic and was found by the gate rather than by a person at a
	// terminal: a prompt with no trailing newline leaves the answer on the same line
	// as the question, and `go test -v` prints its `--- PASS:` marker at the start
	// of a line. The two together put the marker after the prompt text, and
	// `bin/prime` counts those lines to report the suite — so one command that
	// asked a question without a newline was a test the gate could not see. The
	// interactive reading is the same defect one layer down: after `cafe backup`
	// asked and exited, the shell's own prompt appeared beside the question.
	_, _ = fmt.Fprintln(os.Stderr, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return false, fmt.Errorf("%s: no answer on stdin, so nothing was done.\n"+
			"  Pass --yes to go ahead without asking, or --dry-run to see the plan and change nothing: %w",
			command, errNoPrompt)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// callerToken reads the active agent session's token out of the keyring, for
// the length of exactly one command's server calls. It is a courier call: the
// token is read, presented to the server, and kept nowhere — never printed,
// never logged, never put in a document.
//
// It is shared by the commands that present a cafe credential to a server
// (resolve, publish) because the subtle part is not the reading — it is that a
// machine with no token is not a fact to hide. The server's named refusal is
// the honest answer to a caller with no credential, and the warning says so
// rather than leaving the reader to infer it from a 401 two steps later.
// humanSessionToken resolves the session a `cafe login` stored: the ring
// account is `human/<email>` and the email is recorded at login. It is the
// credential a service command exchanges for an audience-scoped token.
func humanSessionToken(command, dir string, ring agent.Keyring, env *Env) string {
	email := readHumanSubject(dir)
	if email == "" {
		return ""
	}
	if ring == nil {
		ring = box.FileRing{Dir: filepath.Join(dir, "keyring")}
	}
	token, err := ring.Get(agent.KeyringService, "human/"+strings.ToLower(strings.TrimSpace(email)))
	if err != nil || strings.TrimSpace(token) == "" {
		_, _ = fmt.Fprintf(errOrDiscard(env), "%s: no session in the keyring for %s, so the server will answer unauthorized; "+
			"run `cafe login` again\n", command, email)
		return ""
	}
	return token
}

func callerToken(command, dir string, ring agent.Keyring, env *Env) string {
	cfg, err := agent.LoadConfig(dir)
	if err != nil {
		_, _ = fmt.Fprintf(errOrDiscard(env), "%s: could not read the agent config: %v\n", command, err)
		return ""
	}
	sess, err := cfg.Select("", "")
	if err != nil {
		if errors.Is(err, agent.ErrNotRegistered) {
			_, _ = fmt.Fprintf(errOrDiscard(env), "%s: no agent session is registered, so the server will answer unauthorized; "+
				"run `cafe agent register` and have a human claim it\n", command)
			return ""
		}
		_, _ = fmt.Fprintf(errOrDiscard(env), "%s: could not select the agent session: %v\n", command, err)
		return ""
	}
	token, err := ring.Get(agent.KeyringService, sess.TokenRef)
	if err != nil || strings.TrimSpace(token) == "" {
		_, _ = fmt.Fprintf(errOrDiscard(env), "%s: no token in the keyring for session %s, so the server will answer unauthorized; "+
			"run `cafe agent register`\n", command, sess.Name)
		return ""
	}
	return token
}

// out and errOut default to a discarded stream, so a run built by a test with
// only its options writes somewhere rather than dereferencing a nil writer. A nil
// io.Writer in fmt.Fprintf is a panic, and a panic in a report is the worst
// possible outcome for a command whose job is to report.
func outOrDiscard(env *Env) io.Writer {
	if env == nil || env.Stdout == nil {
		return io.Discard
	}
	return env.Stdout
}

func errOrDiscard(env *Env) io.Writer {
	if env == nil || env.Stderr == nil {
		return io.Discard
	}
	return env.Stderr
}
