package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/anywaye/cafe/internal/skills"
)

func newSkillsCommand() *Command {
	c := &Command{
		Name:    "skills",
		Summary: "install version-matched anywaye agent skill files",
		Usage:   "cafe skills <subcommand> [args]",
		LongHelp: "Install the anywaye agent skill into your workspace so your coding agent\n" +
			"(Claude Code, Codex, etc.) knows how to use anywaye.\n\n" +
			"Subcommands:\n" +
			"  install   Install/update the anywaye skill file\n\n" +
			"The skill is installed to ~/.agents/skills/anywaye/SKILL.md by default.\n" +
			"It is version-matched to the CLI — run `cafe skills install` after\n" +
			"updating the CLI to get the latest skill.",
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) == 0 {
			_, _ = fmt.Fprintf(env.Stderr, "cafe skills: missing subcommand (usage: %s)\n", c.Usage)
			return errUsage
		}
		switch args[0] {
		case "install":
			return skillsInstall(skillsDeps{}, env, args[1:])
		default:
			_, _ = fmt.Fprintf(env.Stderr, "cafe skills: unknown subcommand %q\n", args[0])
			return errUsage
		}
	}
	return c
}

type skillsDeps struct {
	root string
}

func skillsInstall(deps skillsDeps, env *Env, args []string) error {
	fs := flag.NewFlagSet("skills install", flag.ContinueOnError)
	// The flag set writes nowhere: the router reports the error, in its own
	// words, and a second copy of the usage text is noise.
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	root := fs.String("root", "", "workspace root (defaults to home directory)")
	if err := fs.Parse(args); err != nil {
		// `--help` is not a mistake: the group help is printed instead and
		// the command succeeds, which is the same rule the ship-backed
		// groups follow.
		if errors.Is(err, flag.ErrHelp) {
			shipHelp(env, "skills")
			return nil
		}
		// A flag the command does not declare is a mistake in how cafe was
		// invoked, and that is exit code 2 — not a command that ran and
		// failed.
		return fmt.Errorf("%w: cafe skills install: %w", errUsage, err)
	}
	if *root != "" {
		deps.root = *root
	}

	var res skills.InstallResult
	var err error
	if deps.root == "" {
		res, err = skills.EnsureDefaultInstalled()
	} else {
		res, err = skills.InstallForRoot(deps.root)
	}
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe skills install: %v\n", err)
		return err
	}
	_, _ = fmt.Fprintf(env.Stdout, "skill_path: %s\n", res.Path)
	_, _ = fmt.Fprintf(env.Stdout, "updated: %t\n", res.Updated)
	// The backup is named out loud: a file of the person's own that was kept
	// rather than overwritten is something they have to know about, and a
	// path they cannot see is not something they can use.
	if res.BackupPath != "" {
		_, _ = fmt.Fprintf(env.Stdout, "backup_path: %s\n", res.BackupPath)
	}
	return nil
}
