package cli

import (
	"flag"
	"fmt"

	"github.com/cafaye/cafe/internal/skills"
)

func newSkillsCommand() *Command {
	c := &Command{
		Name:    "skills",
		Summary: "install version-matched cafaye agent skill files",
		Usage:   "cafe skills <subcommand> [args]",
		LongHelp: "Install the cafaye agent skill into your workspace so your coding agent\n" +
			"(Claude Code, Codex, etc.) knows how to use cafaye.\n\n" +
			"Subcommands:\n" +
			"  install   Install/update the cafaye skill file\n\n" +
			"The skill is installed to ~/.agents/skills/cafaye/SKILL.md by default.\n" +
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
	root := fs.String("root", "", "workspace root (defaults to home directory)")
	if err := fs.Parse(args); err != nil {
		return err
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
	return nil
}