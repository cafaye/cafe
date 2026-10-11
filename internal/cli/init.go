package cli

import (
	"flag"
	"fmt"

	"github.com/cafaye/cafe/internal/scaffold"
)

// initOptions is the parsed flag state for `caf init`.
type initOptions struct {
	dir   string
	name  string
	force bool
}

func newInitCommand() *Command {
	opts := &initOptions{}
	c := &Command{
		Name:    "init",
		Summary: "create a cafaye.yml manifest for the current project",
		Usage:   "caf init [flags]",
		LongHelp: "Writes a cafaye.yml into -dir and nothing else: the manifest, validated\n" +
			"against core's schema exactly as `caf contract lint` will check it.\n" +
			"\n" +
			"The name is derived from the directory unless -name overrides it, which is the\n" +
			"case for a repository whose directory is not its service name — a worktree\n" +
			"holding one service is often named after the packet, not the service.\n" +
			"\n" +
			"An existing cafaye.yml is refused rather than overwritten, because it may be\n" +
			"one somebody wrote by hand. -force is the deliberate override.\n" +
			"\n" +
			"For a whole project scaffold — the service, the image, the README — see\n" +
			"`caf new`.",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.dir, "dir", ".", "directory to initialize")
			fs.StringVar(&opts.name, "name", "", "override the service name; the directory's base name by default")
			fs.BoolVar(&opts.force, "force", false, "overwrite an existing cafaye.yml")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		out, err := scaffold.Init(scaffold.Options{
			Name:  opts.name,
			Dir:   opts.dir,
			Force: opts.force,
		})
		if err != nil {
			return fmt.Errorf("caf init: %w", err)
		}
		printScaffolded(env.Stdout, "init", out)
		return nil
	}
	return c
}
