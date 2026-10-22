package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

// errNotImplemented reports a subcommand that is wired but whose real behavior
// lands in a later packet. Callers (and scripts) match it with errors.Is, so
// the message can gain context without losing the sentinel.
var errNotImplemented = errors.New("not implemented in v0")

// Command is one cafe subcommand.
//
// Name is the word the user types, Summary is its one-line help, Usage is the
// full usage line, LongHelp is optional prose printed under the usage, Flags
// declares the flags it accepts and Run receives the positional arguments left
// over after parsing.
//
// Interspersed is off by default because Go's flag package stops at the first
// positional argument, and stopping there is the right default for a command
// that forwards its remaining arguments to another program. A command whose
// subject is a word the operator types first — `cafe box setup contabo` — turns
// it on; the reason is at its own code site.
//
// A subcommand of a subcommand is a Command too, with its full invocation as
// its Name: `cafe contract lint` is one command named "contract lint", so its
// help and its usage errors read exactly as the words the person typed.
type Command struct {
	Name         string
	Summary      string
	Usage        string
	LongHelp     string
	Flags        func(fs *flag.FlagSet)
	Run          func(args []string, env *Env) error
	Interspersed bool
}

// FlagSet builds a fresh flag set for the command. A new one per invocation
// means a parsed value can never leak from one run into the next.
func (c *Command) FlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet(c.Name, flag.ContinueOnError)
	// The router reports errors with its own wording; the flag package must not
	// also write usage to the stream.
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	if c.Flags != nil {
		c.Flags(fs)
	}
	return fs
}

// Execute parses args and runs the command.
//
// `-h` is not a declared flag, so the flag package reports ErrHelp: the command
// prints its help and succeeds, which is what a person typing `cafe new --help`
// expects.
func (c *Command) Execute(env *Env, args []string) error {
	positional, err := parseArgs(c.FlagSet(), args, c.Interspersed)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printHelp(env.Stdout, c)
			return nil
		}
		return fmt.Errorf("%w: cafe %s: %w", errUsage, c.Name, err)
	}
	return c.Run(positional, env)
}

// parseArgs parses the flags out of args and returns the positionals.
//
// When interspersed is false it is the flag package's own behavior: parsing
// stops at the first non-flag argument, and everything from there on — flags
// included — comes back as positional. That is the rule a command that forwards
// its tail to a child process needs, and it is why it stays the default.
//
// When interspersed is true the command accepts the shape an operator actually
// types: the subject first, then the flags. `cafe box setup contabo
// -control-plane …` is the one the Circle-3 run measured as a usage error
// counting three arguments, and the count was the tell — the operator's
// mistake, if there was one, was a misspelled flag, and no count of arguments
// can say so. So the parse is looped instead: fs stops at the first non-flag
// word, that word is taken as ONE positional, and the rest is parsed again. A
// value flag still consumes its value (`-user root contabo` puts root in the
// flag and contabo in the positionals), `--` still ends the flags, and an
// unknown flag is STILL refused as a flag rather than silently counted as a
// subject — which is the half that makes accepting this shape honest rather
// than permissive.
func parseArgs(fs *flag.FlagSet, args []string, interspersed bool) ([]string, error) {
	if !interspersed {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		return fs.Args(), nil
	}
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// Commands returns every subcommand, in the order `cafe help` lists them.
func Commands() []*Command {
	return []*Command{
		newAddCommand(addDeps{}),
		newAgentCommand(aiDeps{}),
		newBackupCommand(backupDeps{}),
		newBoardCommand(boardDeps{}),
		newBoxdCommand(boxdDeps{}),
		newContractCommand(),
		newCveCommand(cveDeps{}),
		newDeployCommand(deployDeps{}),
		newDevCommand(defaultDevDeps()),
		newDevboxCommand(devboxDeps{}),
		newDocsCommand(docsDeps{}),
		newDoctorCommand(),
		newEnvCommand(),
		newErrorsCommand(),
		newFailureCommand(failureDeps{}),
		newGateCommand(gateDeps{}),
		newGenCommand(),
		newGrafanaCommand(),
		newInitCommand(shipDeps{}),
		newLandingCommand(landingDeps{}),
		newLockCommand(),
		newLoginCommand(loginDeps{}),
		newLogsCommand(),
		newMCPCommand(),
		newMetricsCommand(),
		newNewCommand(shipDeps{}),
		newPipelineCommand(pipelineDeps{}),
		newProjectCommand(projectDeps{}),
		newProviderCommand(providerDeps{}),
		newReclaimCommand(reclaimDeps{}),
		newRepoCommand(repoDeps{}),
		newReportCommand(reportDeps{}),
		newSecretCommand(secretDeps{}),
		newSkillsCommand(),
		newStartCommand(startDeps{}),
		newTaskCommand(taskDeps{}),
		newTunnelCommand(tunnelDeps{}),
		newUpdateCommand(),
		newUserAgentCommand(agentDeps{}),
		newVersionCommand(),
	}
}

func lookupCommand(commands []*Command, name string) (*Command, bool) {
	for _, c := range commands {
		if c.Name == name {
			return c, true
		}
	}
	return nil, false
}

// wantArgs reports a usage error unless the command got exactly the number of
// positional arguments it expects.
func wantArgs(name, usage string, want, got int) error {
	if got == want {
		return nil
	}
	return fmt.Errorf("%w: cafe %s wants %s, got %d (usage: %s)", errUsage, name, pluralArgs(want), got, usage)
}

func pluralArgs(n int) string {
	if n == 1 {
		return "1 argument"
	}
	return fmt.Sprintf("%d arguments", n)
}
