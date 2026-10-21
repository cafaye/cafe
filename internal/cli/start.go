package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/google/uuid"
)

// `cafe start` bootstraps a project on ship.
//
// It creates the project and prints what to do next. It does NOT provision a
// dev box: the dev box is an instabox resource, not a ship one, and the help
// used to promise a thing the command never did.

// The seam this command talks through is shipDeps, in ship.go. There is one of
// them on purpose: see that file.

func newStartCommand(deps startDeps) *Command {
	deps, urlErr := deps.withDefaults()
	var (
		name  string
		owner string
	)
	c := &Command{
		Name:    "start",
		Summary: "bootstrap a new project on cafaye (agent-driven)",
		Usage:   "cafe start [flags]",
		LongHelp: "Bootstrap a new project on cafaye. Creates the project on ship and\n" +
			"prints the connection info. The agent then takes over.\n\n" +
			"Flags:\n" +
			"  --name <name>    Project name (default: my-project)\n" +
			"  --owner <id>     Owner ID (identity account, or SHIP_OWNER_ID env)\n\n" +
			"The owner is the account your credential belongs to, so you do not have to\n" +
			"name it: ship resolves it from the credential you present. --owner is for\n" +
			"the deployment's own automation, which has no account of its own.\n\n" +
			"Environment:\n" +
			"  SHIP_URL         Base URL of the ship service\n" +
			"  SHIP_OWNER_ID    Default owner ID\n" +
			"  SHIP_TOKEN       Service token, sent as `Authorization: Bearer …`\n",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&name, "name", "my-project", "project name")
			fs.StringVar(&owner, "owner", os.Getenv("SHIP_OWNER_ID"), "owner ID")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if urlErr != nil {
			return urlErr
		}
		if len(args) > 0 {
			return fmt.Errorf("%w: cafe start: takes no arguments (usage: %s)", errUsage, c.Usage)
		}
		if owner == "" {
			owner = os.Getenv("SHIP_OWNER_ID")
		}
		// THE OWNER IS SENT ONLY WHEN IT WAS NAMED. Ship resolves the account
		// from the credential the command presents, so a person never names
		// the tenancy they act in — a caller that could name it could also
		// name somebody else's. The flag is still here for the deployment's
		// own automation, which has no account of its own to be resolved.
		payload := map[string]any{"name": name}
		if owner != "" {
			// The owner is parsed before the call, not after it: ship's
			// CreateProjectInput carries a uuid.UUID, so a name in that field is
			// a 400, and a 400 the CLI hides is a project that was never created
			// reported as one that was.
			ownerID, err := uuid.Parse(owner)
			if err != nil {
				return fmt.Errorf("%w: cafe start: invalid owner ID %q", errUsage, owner)
			}
			payload["owner_id"] = ownerID.String()
		}

		_, _ = fmt.Fprintf(env.Stdout, "Creating project %q...\n", name)
		req, err := shipJSONRequest("POST", deps.shipURL, "/v1/ship/projects/", payload)
		if err != nil {
			return err
		}
		body, err := shipDo(deps.client, req)
		if err != nil {
			return err
		}
		// The ID is read out of the answer, and an answer without one is a
		// failure: printing "Project created: " with an empty ID told the
		// agent to go and use a project that does not exist.
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(body, &created); err != nil {
			return fmt.Errorf("cafe start: decode project: %w", err)
		}
		if created.ID == "" {
			return fmt.Errorf("cafe start: ship returned no project ID")
		}
		_, _ = fmt.Fprintf(env.Stdout, "  Project created: %s\n", created.ID)

		// The dev box is NOT provisioned here. It is an instabox resource,
		// and the commands below are how one is made — the old help claimed
		// this command did it, and an agent that believed the help waited
		// for a box that was never going to arrive.
		_, _ = fmt.Fprintf(env.Stdout, "\n")
		_, _ = fmt.Fprintf(env.Stdout, "Project is ready. Next steps:\n")
		_, _ = fmt.Fprintf(env.Stdout, "  1. Create a dev box:  instabox create --name %s --auto-suspend 30m\n", name)
		_, _ = fmt.Fprintf(env.Stdout, "  2. Move your repo:    follow the instabox-move-repo skill\n")
		_, _ = fmt.Fprintf(env.Stdout, "  3. SSH in:            instabox ssh %s\n", name)
		_, _ = fmt.Fprintf(env.Stdout, "  4. Start coding:      your agent can now drive development\n")
		_, _ = fmt.Fprintf(env.Stdout, "\n")
		_, _ = fmt.Fprintf(env.Stdout, "Project ID: %s\n", created.ID)
		_, _ = fmt.Fprintf(env.Stdout, "Ship URL:   %s\n", deps.shipURL)
		return nil
	}
	return c
}
