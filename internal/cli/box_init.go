package cli

import (
	"fmt"
)

// boxInstallURL is the one canonical install script. The control plane serves
// it at https://cafaye.com/box/install and that is the only maintained copy:
// a second copy vendored in this binary would be a second source of truth that
// can disagree with the server, and the failure is a client and a server running
// different installers.
const boxInstallURL = "https://cafaye.com/box/install"

// boxInstallCommand is the exact one-liner a user runs. curl -fsSL so a 404
// is silent rather than piped into bash, and bash so the script runs.
const boxInstallCommand = "curl -fsSL " + boxInstallURL + " | bash"

// boxInitDoc is the machine document `cafe box init` prints on stdout. One JSON
// document, because every command in this tree speaks JSON on stdout and prose
// on stderr, and a script that parses the install URL should not have to scrape
// a terminal.
type boxInitDoc struct {
	InstallURL     string `json:"install_url"`
	InstallCommand string `json:"install_command"`
	EnrollCommand  string `json:"enroll_command"`
	Message        string `json:"message"`
}

const boxInitLongHelp = `The install front door.

There is exactly one maintained install script: the one the control plane serves
at ` + boxInstallURL + `. This command does not carry a second copy and never
fetches one — it GENERATES the front door from that URL, so the one-liner it
prints and the URL the server answers are the same string. A vendored script
would be a second source of truth that can drift, and a drift is a client that
installs one thing while the server documents another.

What it prints, in order: the one-liner that installs the box (curl the served
script into bash), then the enroll step that follows (` + "`cafe box enroll --token …`" + `,
which the installer prints again when it finishes). Each has a sentence on what
it does, because a pipeline a person cannot read is a pipeline a person will not
trust.

Deterministic and safe: no network, no config, no secret. Run it twice and the
bytes are identical, and piping the install_command into a shell is the intended
use.`

func newBoxInitCommand() *Command {
	c := &Command{
		Name:     "box init",
		Summary:  "print the one-line box installer and the enroll step that follows",
		Usage:    "cafe box init",
		LongHelp: boxInitLongHelp,
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		doc := boxInitDoc{
			InstallURL:     boxInstallURL,
			InstallCommand: boxInstallCommand,
			EnrollCommand:  "cafe box enroll --token <capability>",
			Message:        "run the install command, then enroll this box with the capability the control plane minted for it; see `cafe box enroll --help` for the token plumbing",
		}
		_, _ = fmt.Fprintf(env.Stderr, "cafe box init: install with %s, then %s\n", boxInstallCommand, doc.EnrollCommand)
		return writeJSONDoc(env.Stdout, doc)
	}
	return c
}
