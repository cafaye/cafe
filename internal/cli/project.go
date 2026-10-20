package cli

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"

	"github.com/google/uuid"
)

// `cafe project` is the project on ship: the thing tasks, landings and
// pipelines live under.
//
// The owner is required and is parsed as a UUID before the call, because
// ship's CreateProjectInput carries a uuid.UUID: a name in that field is a
// 400, and a 400 that the CLI swallowed was a project list that came back
// empty and looked like a project with no projects.

// The seam this command group talks through is shipDeps, in ship.go. There is
// one of them on purpose: see that file.

func newProjectCommand(deps projectDeps) *Command {
	deps, urlErr := deps.withDefaults()
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
			"  SHIP_URL        Base URL of the ship service (default: " + defaultShipURL + ")\n" +
			"  SHIP_OWNER_ID   Default owner ID\n" +
			"  SHIP_TOKEN      Service token, sent as `Authorization: Bearer …`\n",
	}
	c.Run = func(args []string, env *Env) error {
		if urlErr != nil {
			return urlErr
		}
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

// projectCreate creates a project.
func projectCreate(deps projectDeps, env *Env, args []string) error {
	fs := flag.NewFlagSet("project create", flag.ContinueOnError)
	name := fs.String("name", "", "project name")
	owner := fs.String("owner", "", "owner ID (identity account)")
	if err := shipParse(fs, args, "project", env); err != nil {
		if errors.Is(err, errShipHelp) {
			return nil
		}
		return err
	}
	if *name == "" && len(fs.Args()) > 0 {
		*name = fs.Args()[0]
	}
	if *name == "" {
		return fmt.Errorf("%w: cafe project create: --name is required", errUsage)
	}
	if *owner == "" {
		*owner = os.Getenv("SHIP_OWNER_ID")
	}
	if *owner == "" {
		return fmt.Errorf("%w: cafe project create: --owner is required", errUsage)
	}
	ownerID, err := uuid.Parse(*owner)
	if err != nil {
		return fmt.Errorf("%w: cafe project create: invalid owner ID %q", errUsage, *owner)
	}
	req, err := shipJSONRequest("POST", deps.shipURL, "/v1/ship/projects/",
		map[string]any{"name": *name, "owner_id": ownerID.String()})
	if err != nil {
		return err
	}
	body, err := shipDo(deps.client, req)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(body))
	return nil
}

// projectList lists the projects of one owner.
//
// The owner is required: ship's list route refuses an empty owner_id, and the
// old code sent it anyway and printed the 400 body as if it were the list.
func projectList(deps projectDeps, env *Env, args []string) error {
	fs := flag.NewFlagSet("project list", flag.ContinueOnError)
	owner := fs.String("owner", "", "owner ID")
	if err := shipParse(fs, args, "project", env); err != nil {
		if errors.Is(err, errShipHelp) {
			return nil
		}
		return err
	}
	if *owner == "" {
		*owner = os.Getenv("SHIP_OWNER_ID")
	}
	if *owner == "" {
		return fmt.Errorf("%w: cafe project list: --owner or SHIP_OWNER_ID is required", errUsage)
	}
	ownerID, err := uuid.Parse(*owner)
	if err != nil {
		return fmt.Errorf("%w: cafe project list: invalid owner ID %q", errUsage, *owner)
	}
	params := url.Values{}
	params.Set("owner_id", ownerID.String())
	req, err := shipRequest("GET", deps.shipURL, "/v1/ship/projects/"+shipQuery(params), nil)
	if err != nil {
		return err
	}
	body, err := shipDo(deps.client, req)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(body))
	return nil
}

// projectGet fetches one project.
func projectGet(deps projectDeps, env *Env, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%w: cafe project get: missing project ID", errUsage)
	}
	req, err := shipRequest("GET", deps.shipURL, "/v1/ship/projects/"+args[0], nil)
	if err != nil {
		return err
	}
	body, err := shipDo(deps.client, req)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(body))
	return nil
}
