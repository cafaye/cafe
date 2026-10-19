package cli

import (
	"fmt"
	"os"
	"os/exec"
)

type devboxDeps struct{}

func (d devboxDeps) withDefaults() devboxDeps { return d }

func newDevboxCommand(deps devboxDeps) *Command {
	deps = deps.withDefaults()
	c := &Command{
		Name:    "devbox",
		Summary: "manage dev boxes on instabox",
		Usage:   "cafe devbox <subcommand> [args]",
		LongHelp: "Manage dev boxes on instabox.\n\n" +
			"Subcommands:\n" +
			"  create <name>   Create a new dev box\n" +
			"  ssh <name>      SSH into a dev box\n" +
			"  exec <name>     Run a command on a dev box\n" +
			"  ls              List dev boxes\n" +
			"  rm <name>       Delete a dev box\n\n" +
			"Wraps the instabox CLI. Requires instabox to be installed.\n",
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) == 0 {
			_, _ = fmt.Fprintf(env.Stderr, "cafe devbox: missing subcommand (usage: %s)\n", c.Usage)
			return errUsage
		}
		// Delegate to instabox CLI
		cmd := exec.Command("instabox", args...)
		cmd.Stdout = env.Stdout
		cmd.Stderr = env.Stderr
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err != nil {
			return err
		}
		return nil
	}
	return c
}