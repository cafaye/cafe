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

	"github.com/google/uuid"
)

type projectDeps struct {
	shipURL string
	client  *http.Client
}

func (d projectDeps) withDefaults() projectDeps {
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

func newProjectCommand(deps projectDeps) *Command {
	deps = deps.withDefaults()
	c := &Command{
		Name:    "project",
		Summary: "manage projects on cafaye",
		Usage:   "cafe project <subcommand> [args]",
		LongHelp: "Manage projects on cafaye ship.\n\n" +
			"Subcommands:\n" +
			"  create <name>   Create a new project\n" +
			"  list            List your projects\n" +
			"  get <id>        Get project details\n\n" +
			"Environment:\n" +
			"  SHIP_URL  Base URL of the ship service (default: https://ship.cafaye.com)\n" +
			"  SHIP_OWNER_ID  Default owner ID\n",
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) == 0 {
			_, _ = fmt.Fprintf(env.Stderr, "cafe project: missing subcommand (usage: %s)\n", c.Usage)
			return errUsage
		}
		switch args[0] {
		case "create":
			return projectCreate(deps, env, args[1:])
		case "list":
			return projectList(deps, env, args[1:])
		case "get":
			return projectGet(deps, env, args[1:])
		default:
			_, _ = fmt.Fprintf(env.Stderr, "cafe project: unknown subcommand %q\n", args[0])
			return errUsage
		}
	}
	return c
}

func projectCreate(deps projectDeps, env *Env, args []string) error {
	fs := flag.NewFlagSet("project create", flag.ContinueOnError)
	name := fs.String("name", "", "project name")
	owner := fs.String("owner", "", "owner ID (identity account)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" && len(fs.Args()) > 0 {
		*name = fs.Args()[0]
	}
	if *name == "" {
		_, _ = fmt.Fprintf(env.Stderr, "cafe project create: --name is required\n")
		return errUsage
	}
	if *owner == "" {
		*owner = os.Getenv("SHIP_OWNER_ID")
	}
	if *owner == "" {
		_, _ = fmt.Fprintf(env.Stderr, "cafe project create: --owner is required\n")
		return errUsage
	}
	ownerID, err := uuid.Parse(*owner)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe project create: invalid owner ID\n")
		return err
	}
	body, _ := json.Marshal(map[string]any{"name": *name, "owner_id": ownerID.String()})
	req, _ := http.NewRequest("POST", deps.shipURL+"/v1/projects/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if tok := os.Getenv("SHIP_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe project: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		_, _ = fmt.Fprintf(env.Stderr, "cafe project: create failed (%d): %s\n", resp.StatusCode, string(respBody))
		return fmt.Errorf("create failed")
	}
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(respBody))
	return nil
}

func projectList(deps projectDeps, env *Env, args []string) error {
	fs := flag.NewFlagSet("project list", flag.ContinueOnError)
	owner := fs.String("owner", "", "owner ID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *owner == "" {
		*owner = os.Getenv("SHIP_OWNER_ID")
	}
	req, _ := http.NewRequest("GET", deps.shipURL+"/v1/projects/?owner_id="+*owner, nil)
	if tok := os.Getenv("SHIP_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe project: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(respBody))
	return nil
}

func projectGet(deps projectDeps, env *Env, args []string) error {
	if len(args) < 1 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe project get: missing project ID\n")
		return errUsage
	}
	req, _ := http.NewRequest("GET", deps.shipURL+"/v1/projects/"+args[0], nil)
	if tok := os.Getenv("SHIP_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe project: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(respBody))
	return nil
}

var _ = uuid.New
