package cli

import (
	"flag"
	"fmt"

	"github.com/cafaye/cafe/internal/scaffold"
)

// newOptions is the parsed flag state for `caf new`.
//
// The first version of this command said the template catalog "lands with the
// generator packet"; this is that packet, and the catalog is real: an embedded
// one, in internal/scaffold.
type newOptions struct {
	template string
	name     string
	force    bool
}

func newNewCommand() *Command {
	opts := &newOptions{}
	c := &Command{
		Name:    "new",
		Summary: "scaffold a new cafaye service or app",
		Usage:   "caf new [flags] <name>",
		LongHelp: "Scaffolds a project into <name>/ from the template catalog embedded in this\n" +
			"binary. The catalog is read from internal/scaffold: one directory per template,\n" +
			"one index naming them, so adding a template is adding a directory and a row\n" +
			"rather than a recompile of a table written beside the code.\n" +
			"\n" +
			"The scaffolded cafaye.yml is validated against core's manifest schema before\n" +
			"anything is written — the same check `caf contract lint` runs — so a scaffolded\n" +
			"project cannot be one the contract rejects.\n" +
			"\n" +
			"<name> is the cafaye namespace name: lowercase kebab-case, 2-40 characters.\n" +
			"It is the directory the project is written into, and the name the manifest\n" +
			"carries unless -name overrides it (a repository whose directory is not its\n" +
			"service name is exactly that case).\n" +
			"\n" +
			"A second `caf new` over an existing project is refused rather than clobbered:\n" +
			"the file in the way may be a manifest somebody wrote by hand, and -force is\n" +
			"the deliberate override.",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.template, "template", scaffold.DefaultTemplate, "template to scaffold from")
			fs.StringVar(&opts.name, "name", "", "override the name of the project")
			fs.BoolVar(&opts.force, "force", false, "overwrite an existing project")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 1, len(args)); err != nil {
			return err
		}
		out, err := scaffold.New(scaffold.Options{
			Template: opts.template,
			Name:     opts.name,
			Dir:      args[0],
			Force:    opts.force,
		})
		if err != nil {
			return fmt.Errorf("caf new: %w", err)
		}
		printScaffolded(env.Stdout, "new", out)
		return nil
	}
	return c
}
