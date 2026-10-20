package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/cafaye/cafe/internal/scaffold"
	"github.com/google/uuid"
)

// newOptions is the parsed flag state for `cafe new`.
//
// The first version of this command said the template catalog "lands with the
// generator packet"; this is that packet, and the catalog is real: an embedded
// one, in internal/scaffold.
type newOptions struct {
	template string
	name     string
	force    bool
}

func newNewCommand(deps shipDeps) *Command {
	opts := &newOptions{}
	c := &Command{
		Name:    "new",
		Summary: "scaffold a new cafaye service or app",
		Usage:   "cafe new [flags] <name>",
		LongHelp: "Scaffolds a project into <name>/ from the template catalog embedded in this\n" +
			"binary. The catalog is read from internal/scaffold: one directory per template,\n" +
			"one index naming them, so adding a template is adding a directory and a row\n" +
			"rather than a recompile of a table written beside the code.\n" +
			"\n" +
			"The scaffolded cafaye.yml is validated against core's manifest schema before\n" +
			"anything is written — the same check `cafe contract lint` runs — so a scaffolded\n" +
			"project cannot be one the contract rejects.\n" +
			"\n" +
			"<name> is the cafaye namespace name: lowercase kebab-case, 2-40 characters.\n" +
			"It is the directory the project is written into, and the name the manifest\n" +
			"carries unless -name overrides it (a repository whose directory is not its\n" +
			"service name is exactly that case).\n" +
			"\n" +
			"A second `cafe new` over an existing project is refused rather than clobbered:\n" +
			"the file in the way may be a manifest somebody wrote by hand, and -force is\n" +
			"the deliberate override.",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.template, "template", scaffold.DefaultTemplate, "template to scaffold from")
			fs.StringVar(&opts.name, "name", "", "override the name of the project")
			fs.BoolVar(&opts.force, "force", false, "overwrite an existing project")
		},
	}
	c.Run = func(args []string, env *Env) error {
		deps, urlErr := deps.withDefaults()
		if urlErr != nil {
			return urlErr
		}
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
			return fmt.Errorf("cafe new: %w", err)
		}
		printScaffolded(env.Stdout, "new", out)

		// Cloud bootstrap: create project on ship if configured.
		owner := os.Getenv("CAFAYE_OWNER_ID")
		if owner == "" {
			owner = os.Getenv("SHIP_OWNER_ID")
		}
		if owner != "" {
			ownerID, err := uuid.Parse(owner)
			if err == nil {
				_, _ = fmt.Fprintf(env.Stdout, "\nCreating cloud project...\n")
				req, err := shipJSONRequest("POST", deps.shipURL, "/v1/ship/projects/",
					map[string]any{"name": args[0], "owner_id": ownerID.String()})
				if err == nil {
					body, err := shipDo(deps.client, req)
					if err == nil {
						var proj struct {
							ID string `json:"id"`
						}
						json.Unmarshal(body, &proj)
						if proj.ID != "" {
							_, _ = fmt.Fprintf(env.Stdout, "  Project created: %s\n", proj.ID)
							_, _ = fmt.Fprintf(env.Stdout, "  Ship URL: %s\n", deps.shipURL)
						}
					}
				}
			}
		} else {
			_, _ = fmt.Fprintf(env.Stdout, "\n  (set CAFAYE_OWNER_ID to also create a cloud project)\n")
		}
		return nil
	}
	return c
}
