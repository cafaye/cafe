package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cafaye/cafe/internal/scaffold"

	"github.com/google/uuid"
)

// initOptions is the parsed flag state for `cafe init`.
type initOptions struct {
	dir   string
	name  string
	force bool
}

func newInitCommand(deps shipDeps) *Command {
	opts := &initOptions{}
	c := &Command{
		Name:    "init",
		Summary: "create a cafaye.yml manifest for the current project",
		Usage:   "cafe init [flags]",
		LongHelp: "Writes a cafaye.yml into -dir and nothing else: the manifest, validated\n" +
			"against core's schema exactly as `cafe contract lint` will check it.\n" +
			"\n" +
			"The name is derived from the directory unless -name overrides it, which is the\n" +
			"case for a repository whose directory is not its service name — a worktree\n" +
			"holding one service is often named after the packet, not the service.\n" +
			"\n" +
			"An existing cafaye.yml is refused rather than overwritten, because it may be\n" +
			"one somebody wrote by hand. -force is the deliberate override.\n" +
			"\n" +
			"For a whole project scaffold — the service, the image, the README — see\n" +
			"`cafe new`.",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.dir, "dir", ".", "directory to initialize")
			fs.StringVar(&opts.name, "name", "", "override the service name; the directory's base name by default")
			fs.BoolVar(&opts.force, "force", false, "overwrite an existing cafaye.yml")
		},
	}
	c.Run = func(args []string, env *Env) error {
		deps, urlErr := deps.withDefaults()
		if urlErr != nil {
			// Manifest creation still works without ship
		}
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		out, err := scaffold.Init(scaffold.Options{
			Name:  opts.name,
			Dir:   opts.dir,
			Force: opts.force,
		})
		if err != nil {
			return fmt.Errorf("cafe init: %w", err)
		}
		printScaffolded(env.Stdout, "init", out)

		// Cloud bootstrap: create project on ship if configured.
		if urlErr == nil {
			name := opts.name
			if name == "" {
				name = filepath.Base(opts.dir)
				if name == "." || name == "/" {
					if cwd, err := os.Getwd(); err == nil {
						name = filepath.Base(cwd)
					}
				}
			}
			owner := os.Getenv("CAFAYE_OWNER_ID")
			if owner == "" {
				owner = os.Getenv("SHIP_OWNER_ID")
			}
			if owner != "" {
				ownerID, err := uuid.Parse(owner)
				if err == nil {
					_, _ = fmt.Fprintf(env.Stdout, "\nCreating cloud project %q...\n", name)
					req, err := shipJSONRequest("POST", deps.shipURL, "/v1/ship/projects/",
						map[string]any{"name": name, "owner_id": ownerID.String()})
					if err == nil {
						body, err := shipDo(deps.client, req)
						if err == nil {
							var proj struct {
								ID string `json:"id"`
							}
							json.Unmarshal(body, &proj)
							if proj.ID != "" {
								_, _ = fmt.Fprintf(env.Stdout, "  Project created: %s\n", proj.ID)
							}
						}
					}
				}
			}
		}
		return nil
	}
	return c
}
