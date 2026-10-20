package cli

import (
	"fmt"
	"os"
	"os/exec"
)

type devboxDeps struct{}

func newDevboxCommand(deps devboxDeps) *Command {
	c := &Command{
		Name:    "devbox",
		Summary: "manage dev boxes",
		Usage:   "cafe devbox <subcommand> [args]",
		LongHelp: "Manage cloud dev boxes.\n\n" +
			"Subcommands:\n" +
			"  create <name>   Create a new dev box\n" +
			"  ssh <name>      SSH into a dev box\n" +
			"  exec <name>     Run a command on a dev box\n" +
			"  ls              List dev boxes\n" +
			"  rm <name>       Delete a dev box\n",
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) == 0 {
			_, _ = fmt.Fprintf(env.Stderr, "cafe devbox: missing subcommand (usage: %s)\n", c.Usage)
			return errUsage
		}
		// Delegate to instabox CLI. The command carries the
		// invocation's context, so an interrupted run does not leave
		// the child process behind.
		cmd := exec.CommandContext(ctxOrBackground(env), "instabox", args...)
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
