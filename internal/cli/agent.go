package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
)

// aiDeps talks to ship's agent endpoints.
type aiDeps = shipDeps

func newAgentCommand(deps aiDeps) *Command {
	deps, urlErr := deps.withDefaults()
	c := &Command{
		Name:    "agent",
		Summary: "manage AI agents on cafaye ship",
		Usage:   "cafe agent <subcommand> [args]",
		LongHelp: "Manage AI agents on cafaye ship.\n\n" +
			"Subcommands:\n" +
			"  list                          List agents\n" +
			"  create                        Create an agent\n" +
			"  get <id>                      Get agent details\n" +
			"  delete <id>                   Delete an agent\n" +
			"  runs <agent-id>               List runs for an agent\n" +
			"  tools                         List available tools\n\n" +
			"Environment:\n" +
			"  SHIP_URL   Base URL of the ship service\n" +
			"  SHIP_TOKEN Service token, sent as `Authorization: Bearer …`\n",
	}
	c.Run = func(args []string, env *Env) error {
		if urlErr != nil {
			return urlErr
		}
		if len(args) == 0 {
			_, _ = fmt.Fprintf(env.Stderr, "cafe ai: missing subcommand (usage: %s)\n", c.Usage)
			return errUsage
		}
		switch args[0] {
		case "list":
			return aiAgentList(deps, env, args[1:])
		case "create":
			return aiAgentCreate(deps, env, args[1:])
		case "get":
			return aiAgentGet(deps, env, args[1:])
		case "delete":
			return aiAgentDelete(deps, env, args[1:])
		case "runs":
			return aiAgentRuns(deps, env, args[1:])
		case "tools":
			return aiAgentTools(deps, env, args[1:])
		default:
			_, _ = fmt.Fprintf(env.Stderr, "cafe ai: unknown subcommand %q\n", args[0])
			return errUsage
		}
	}
	return c
}

func aiAgentList(deps aiDeps, env *Env, args []string) error {
	req, err := shipJSONRequest("GET", deps.shipURL, "/v1/ship/agents", nil)
	if err != nil {
		return err
	}
	body, err := shipDo(deps.client, req)
	if err != nil {
		return err
	}
	var agents []map[string]any
	if err := json.Unmarshal(body, &agents); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}
	if len(agents) == 0 {
		fmt.Fprintln(env.Stdout, "No agents.")
		return nil
	}
	for _, a := range agents {
		fmt.Fprintf(env.Stdout, "%s  %s  %s  tools=%v\n", a["id"], a["slug"], a["name"], a["tools"])
	}
	return nil
}

func aiAgentCreate(deps aiDeps, env *Env, args []string) error {
	fs := flag.NewFlagSet("ai create", flag.ContinueOnError)
	slug := fs.String("slug", "", "agent slug")
	name := fs.String("name", "", "agent name")
	description := fs.String("description", "", "agent description")
	model := fs.String("model", "opencode-go/MiniMax-M3.1-Flash-Free", "muse model name")
	systemPrompt := fs.String("system-prompt", "", "system prompt (instructions)")
	maxTurns := fs.Int("max-turns", 10, "max turns per run")
	if err := shipParse(fs, args, "ai", env); err != nil {
		if errors.Is(err, errShipHelp) {
			return nil
		}
		return err
	}
	if *slug == "" {
		return fmt.Errorf("slug is required")
	}
	if *name == "" {
		*name = *slug
	}
	payload := map[string]any{
		"slug":          *slug,
		"name":          *name,
		"description":   *description,
		"model":         *model,
		"system_prompt": *systemPrompt,
		"tools":         []string{"list_files", "read_file", "write_file", "exec", "run_pipeline", "merge_landing", "notify_owner", "git_diff", "search_code"},
		"max_turns":     *maxTurns,
	}
	req, err := shipJSONRequest("POST", deps.shipURL, "/v1/ship/agents", payload)
	if err != nil {
		return err
	}
	body, err := shipDo(deps.client, req)
	if err != nil {
		return err
	}
	var agent map[string]any
	if err := json.Unmarshal(body, &agent); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}
	fmt.Fprintf(env.Stdout, "Created agent %s (%s)\n", agent["slug"], agent["id"])
	return nil
}

func aiAgentGet(deps aiDeps, env *Env, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: cafe ai get <id>")
	}
	req, err := shipJSONRequest("GET", deps.shipURL, "/v1/ship/agents/"+args[0], nil)
	if err != nil {
		return err
	}
	body, err := shipDo(deps.client, req)
	if err != nil {
		return err
	}
	var agent map[string]any
	if err := json.Unmarshal(body, &agent); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}
	out, _ := json.MarshalIndent(agent, "", "  ")
	fmt.Fprintln(env.Stdout, string(out))
	return nil
}

func aiAgentDelete(deps aiDeps, env *Env, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: cafe ai delete <id>")
	}
	req, err := shipJSONRequest("DELETE", deps.shipURL, "/v1/ship/agents/"+args[0], nil)
	if err != nil {
		return err
	}
	_, err = shipDo(deps.client, req)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Deleted agent %s\n", args[0])
	return nil
}

func aiAgentRuns(deps aiDeps, env *Env, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: cafe ai runs <agent-id>")
	}
	req, err := shipJSONRequest("GET", deps.shipURL, "/v1/ship/agents/"+args[0]+"/runs", nil)
	if err != nil {
		return err
	}
	body, err := shipDo(deps.client, req)
	if err != nil {
		return err
	}
	var runs []map[string]any
	if err := json.Unmarshal(body, &runs); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}
	if len(runs) == 0 {
		fmt.Fprintln(env.Stdout, "No runs.")
		return nil
	}
	for _, r := range runs {
		fmt.Fprintf(env.Stdout, "%s  %s  %s\n", r["id"], r["status"], r["started_at"])
		if r["result"] != nil && r["result"] != "" {
			fmt.Fprintf(env.Stdout, "  %v\n", r["result"])
		}
	}
	return nil
}

func aiAgentTools(deps aiDeps, env *Env, args []string) error {
	req, err := shipJSONRequest("GET", deps.shipURL, "/v1/ship/agents/tools", nil)
	if err != nil {
		return err
	}
	body, err := shipDo(deps.client, req)
	if err != nil {
		return err
	}
	var tools []map[string]any
	if err := json.Unmarshal(body, &tools); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}
	for _, t := range tools {
		fmt.Fprintf(env.Stdout, "%s  %s\n", t["name"], t["description"])
	}
	return nil
}
