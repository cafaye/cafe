package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type startDeps struct {
	shipURL string
	client  *http.Client
}

func (d startDeps) withDefaults() startDeps {
	if d.shipURL == "" {
		d.shipURL = os.Getenv("SHIP_URL")
		if d.shipURL == "" {
			d.shipURL = "https://ship.cafaye.com"
		}
	}
	if d.client == nil {
		d.client = &http.Client{Timeout: 30 * time.Second}
	}
	return d
}

func newStartCommand(deps startDeps) *Command {
	deps = deps.withDefaults()
	c := &Command{
		Name:    "start",
		Summary: "bootstrap a new project on cafaye (agent-driven)",
		Usage:   "cafe start [flags]",
		LongHelp: "Bootstrap a new project on cafaye. Creates the project, provisions a dev box,\n" +
			"and prints connection info. The agent then takes over.\n\n" +
			"Flags:\n" +
			"  --name <name>    Project name (default: my-project)\n" +
			"  --owner <id>     Owner ID (identity account, or SHIP_OWNER_ID env)\n\n" +
			"Environment:\n" +
			"  SHIP_URL         Base URL of the ship service\n" +
			"  SHIP_OWNER_ID    Default owner ID\n",
	}
	c.Run = func(args []string, env *Env) error {
		fs := flag.NewFlagSet("start", flag.ContinueOnError)
		name := fs.String("name", "my-project", "project name")
		owner := fs.String("owner", "", "owner ID")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *owner == "" {
			*owner = os.Getenv("SHIP_OWNER_ID")
		}
		if *owner == "" {
			_, _ = fmt.Fprintf(env.Stderr, "cafe start: --owner or SHIP_OWNER_ID is required\n")
			return errUsage
		}

		// Step 1: Create project
		_, _ = fmt.Fprintf(env.Stdout, "Creating project %q...\n", *name)
		body, _ := json.Marshal(map[string]any{"name": *name, "owner_id": *owner})
		req, _ := http.NewRequest("POST", deps.shipURL+"/v1/projects/", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := deps.client.Do(req)
		if err != nil {
			_, _ = fmt.Fprintf(env.Stderr, "cafe start: %v\n", err)
			return err
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		var project map[string]any
		json.Unmarshal(respBody, &project)
		projectID, _ := project["id"].(string)
		_, _ = fmt.Fprintf(env.Stdout, "  Project created: %s\n", projectID)

		// Step 2: Print next steps
		_, _ = fmt.Fprintf(env.Stdout, "\n")
		_, _ = fmt.Fprintf(env.Stdout, "Project is ready. Next steps:\n")
		_, _ = fmt.Fprintf(env.Stdout, "  1. Create a dev box:  instabox create --name %s --auto-suspend 30m\n", *name)
		_, _ = fmt.Fprintf(env.Stdout, "  2. Move your repo:    follow the instabox-move-repo skill\n")
		_, _ = fmt.Fprintf(env.Stdout, "  3. SSH in:            instabox ssh %s\n", *name)
		_, _ = fmt.Fprintf(env.Stdout, "  4. Start coding:      your agent can now drive development\n")
		_, _ = fmt.Fprintf(env.Stdout, "\n")
		_, _ = fmt.Fprintf(env.Stdout, "Project ID: %s\n", projectID)
		_, _ = fmt.Fprintf(env.Stdout, "Ship URL:   %s\n", deps.shipURL)
		return nil
	}
	return c
}