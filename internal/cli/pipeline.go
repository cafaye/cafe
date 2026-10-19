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

type pipelineDeps struct {
	shipURL string
	client  *http.Client
}

func (d pipelineDeps) withDefaults() pipelineDeps {
	if d.shipURL == "" {
		d.shipURL = os.Getenv("CAFAYE_URL")
	}
	if d.shipURL == "" {
		d.shipURL = os.Getenv("SHIP_URL")
	}
	if d.shipURL == "" {
		d.shipURL = "https://cafaye.com"
	}
	if d.client == nil {
		d.client = &http.Client{Timeout: 30 * time.Second}
	}
	return d
}

func newPipelineCommand(deps pipelineDeps) *Command {
	deps = deps.withDefaults()
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
			"  CAFAYE_URL  Base URL of your cafaye instance (default: https://cafaye.com)\n" +
			"  SHIP_URL    Legacy alias for CAFAYE_URL\n",
	}
	c.Run = func(args []string, env *Env) error {
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

func pipelineGet(deps pipelineDeps, env *Env, args []string) error {
	if len(args) < 1 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe pipeline get: missing project ID\n")
		return errUsage
	}
	req, _ := http.NewRequest("GET", deps.shipURL+"/v1/projects/"+args[0]+"/pipeline", nil)
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe pipeline: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(body))
	return nil
}

func pipelineUpdate(deps pipelineDeps, env *Env, args []string) error {
	if len(args) < 1 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe pipeline update: missing project ID\n")
		return errUsage
	}
	fs := flag.NewFlagSet("pipeline update", flag.ContinueOnError)
	recipe := fs.String("recipe", "", "recipe to install (software, deploy, contribution, default)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	body := map[string]any{}
	if *recipe != "" {
		body["recipe"] = *recipe
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("PUT", deps.shipURL+"/v1/projects/"+args[0]+"/pipeline", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe pipeline: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(respBody))
	return nil
}

func pipelineAdvance(deps pipelineDeps, env *Env, args []string) error {
	if len(args) < 1 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe pipeline advance: missing project ID\n")
		return errUsage
	}
	req, _ := http.NewRequest("POST", deps.shipURL+"/v1/projects/"+args[0]+"/pipeline/advance", nil)
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe pipeline: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(body))
	return nil
}

func pipelineRecipes(deps pipelineDeps, env *Env, args []string) error {
	req, _ := http.NewRequest("GET", deps.shipURL+"/v1/pipeline-recipes", nil)
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe pipeline: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(body))
	return nil
}

var _ = uuid.New
