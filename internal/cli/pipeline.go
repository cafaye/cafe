package cli

import (
	"errors"
	"flag"
	"fmt"
)

// `cafe pipeline` is the flow a project follows: the stages, the gates
// between them, and the recipes that install a preset of both.

// The seam this command group talks through is shipDeps, in ship.go. There is
// one of them on purpose: see that file.

func newPipelineCommand(deps pipelineDeps) *Command {
	deps, urlErr := deps.withDefaults()
	c := &Command{
		Name:    "pipeline",
		Summary: "manage project pipelines (stages, gates, recipes)",
		Usage:   "cafe pipeline <subcommand> [args]",
		LongHelp: "Manage project pipelines on cafaye ship.\n\n" +
			"Subcommands:\n" +
			"  get <project-id>              Show the project's pipeline\n" +
			"  update <project-id>           Update pipeline (use --recipe to install a preset)\n" +
			"  advance <project-id>          Advance to the next stage\n" +
			"  recipes                       List available pipeline recipes\n\n" +
			"Environment:\n" +
			"  SHIP_URL   Base URL of the ship service (default: " + defaultShipURL + ")\n" +
			"  SHIP_TOKEN Service token, sent as `Authorization: Bearer …`\n",
	}
	c.Run = func(args []string, env *Env) error {
		if urlErr != nil {
			return urlErr
		}
		if len(args) == 0 {
			_, _ = fmt.Fprintf(env.Stderr, "cafe pipeline: missing subcommand (usage: %s)\n", c.Usage)
			return errUsage
		}
		switch args[0] {
		case "get":
			return pipelineGet(deps, env, args[1:])
		case "update":
			return pipelineUpdate(deps, env, args[1:])
		case "advance":
			return pipelineAdvance(deps, env, args[1:])
		case "recipes":
			return pipelineRecipes(deps, env, args[1:])
		default:
			_, _ = fmt.Fprintf(env.Stderr, "cafe pipeline: unknown subcommand %q\n", args[0])
			return errUsage
		}
	}
	return c
}

// pipelineGet shows the project's pipeline.
func pipelineGet(deps pipelineDeps, env *Env, args []string) error {
	projectID, err := shipProjectArg("pipeline get", args)
	if err != nil {
		return err
	}
	req, err := shipRequest("GET", deps.shipURL, "/v1/projects/"+projectID+"/pipeline", nil)
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

// pipelineUpdate installs a recipe, or writes a pipeline definition directly.
func pipelineUpdate(deps pipelineDeps, env *Env, args []string) error {
	projectID, err := shipProjectArg("pipeline update", args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("pipeline update", flag.ContinueOnError)
	recipe := fs.String("recipe", "", "recipe to install (software, deploy, contribution, default)")
	if err := shipParse(fs, args[1:], "pipeline", env); err != nil {
		if errors.Is(err, errShipHelp) {
			return nil
		}
		return err
	}
	body := map[string]any{}
	if *recipe != "" {
		body["recipe"] = *recipe
	}
	req, err := shipJSONRequest("PUT", deps.shipURL,
		"/v1/projects/"+projectID+"/pipeline", body)
	if err != nil {
		return err
	}
	respBody, err := shipDo(deps.client, req)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(respBody))
	return nil
}

// pipelineAdvance moves the project to the next stage of its pipeline.
func pipelineAdvance(deps pipelineDeps, env *Env, args []string) error {
	projectID, err := shipProjectArg("pipeline advance", args)
	if err != nil {
		return err
	}
	req, err := shipRequest("POST", deps.shipURL,
		"/v1/projects/"+projectID+"/pipeline/advance", nil)
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

// pipelineRecipes lists the presets ship can install.
func pipelineRecipes(deps pipelineDeps, env *Env, args []string) error {
	req, err := shipRequest("GET", deps.shipURL, "/v1/pipeline-recipes", nil)
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
