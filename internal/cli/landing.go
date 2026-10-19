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

// landingDeps delegates to the task API (a landing is a type of task).
type landingDeps struct {
	shipURL string
	client  *http.Client
}

func (d landingDeps) withDefaults() landingDeps {
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

func newLandingCommand(deps landingDeps) *Command {
	deps = deps.withDefaults()
	c := &Command{
		Name:    "landing",
		Summary: "manage landings (contributions that replace PRs)",
		Usage:   "cafe landing <subcommand> [args]",
		LongHelp: "Manage landings on cafaye ship. A landing is a task with kind in " +
			"{change, issue, feedback, data, request}. Delegates to the task API.\n\n" +
			"Subcommands:\n" +
			"  create <project-id>   Submit a new landing\n" +
			"  list <project-id>     List landings\n" +
			"  get <task-id>         Get landing details\n" +
			"  approve <project-id> <task-id>   Approve a landing\n" +
			"  reject <project-id> <task-id>    Reject a landing\n" +
			"  merge <project-id> <task-id>     Merge an approved landing\n\n" +
			"Environment:\n" +
			"  SHIP_URL  Base URL of the ship service\n",
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) == 0 {
			_, _ = fmt.Fprintf(env.Stderr, "cafe landing: missing subcommand (usage: %s)\n", c.Usage)
			return errUsage
		}
		switch args[0] {
		case "create":
			return landingCreate(deps, env, args[1:])
		case "list":
			return landingList(deps, env, args[1:])
		case "get":
			return landingGet(deps, env, args[1:])
		case "approve":
			return landingApprove(deps, env, args[1:])
		case "reject":
			return landingReject(deps, env, args[1:])
		case "merge":
			return landingMerge(deps, env, args[1:])
		default:
			_, _ = fmt.Fprintf(env.Stderr, "cafe landing: unknown subcommand %q\n", args[0])
			return errUsage
		}
	}
	return c
}

func landingCreate(deps landingDeps, env *Env, args []string) error {
	if len(args) < 1 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe landing create: missing project ID\n")
		return errUsage
	}
	fs := flag.NewFlagSet("landing create", flag.ContinueOnError)
	kind := fs.String("kind", "change", "landing kind: change, issue, feedback, data, request")
	title := fs.String("title", "", "landing title")
	description := fs.String("description", "", "landing description")
	externalID := fs.String("external-id", "", "external identifier")
	sourceURL := fs.String("source-url", "", "source URL")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *title == "" {
		*title = "landing: " + *kind
	}
	body := map[string]any{
		"title":       *title,
		"description": *description,
		"kind":        *kind,
	}
	if *externalID != "" {
		body["external_id"] = *externalID
	}
	if *sourceURL != "" {
		body["source_url"] = *sourceURL
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", deps.shipURL+"/v1/projects/"+args[0]+"/tasks", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe landing: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(respBody))
	return nil
}

func landingList(deps landingDeps, env *Env, args []string) error {
	if len(args) < 1 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe landing list: missing project ID\n")
		return errUsage
	}
	fs := flag.NewFlagSet("landing list", flag.ContinueOnError)
	kind := fs.String("kind", "", "filter by kind")
	status := fs.String("status", "", "filter by status")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	url := deps.shipURL + "/v1/projects/" + args[0] + "/tasks"
	params := ""
	if *kind != "" {
		params = "?kind=" + *kind
	}
	if *status != "" {
		if params == "" {
			params = "?status=" + *status
		} else {
			params += "&status=" + *status
		}
	}
	req, _ := http.NewRequest("GET", url+params, nil)
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe landing: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(body))
	return nil
}

func landingGet(deps landingDeps, env *Env, args []string) error {
	if len(args) < 2 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe landing get: usage: cafe landing get <project-id> <task-id>\n")
		return errUsage
	}
	req, _ := http.NewRequest("GET", deps.shipURL+"/v1/projects/"+args[0]+"/tasks/"+args[1], nil)
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe landing: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(body))
	return nil
}

func landingApprove(deps landingDeps, env *Env, args []string) error {
	if len(args) < 2 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe landing approve: usage: cafe landing approve <project-id> <task-id> [--by <id>] [--note <text>]\n")
		return errUsage
	}
	fs := flag.NewFlagSet("landing approve", flag.ContinueOnError)
	by := fs.String("by", "", "who is approving")
	note := fs.String("note", "", "approval note")
	fs.Parse(args[2:])
	body := map[string]any{"decided_by": *by, "note": *note}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", deps.shipURL+"/v1/projects/"+args[0]+"/tasks/"+args[1]+"/approve", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe landing: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(respBody))
	return nil
}

func landingReject(deps landingDeps, env *Env, args []string) error {
	if len(args) < 2 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe landing reject: usage: cafe landing reject <project-id> <task-id> [--by <id>] [--note <text>]\n")
		return errUsage
	}
	fs := flag.NewFlagSet("landing reject", flag.ContinueOnError)
	by := fs.String("by", "", "who is rejecting")
	note := fs.String("note", "", "rejection note")
	fs.Parse(args[2:])
	body := map[string]any{"decided_by": *by, "note": *note}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", deps.shipURL+"/v1/projects/"+args[0]+"/tasks/"+args[1]+"/reject", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe landing: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(respBody))
	return nil
}

func landingMerge(deps landingDeps, env *Env, args []string) error {
	if len(args) < 2 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe landing merge: usage: cafe landing merge <project-id> <task-id>\n")
		return errUsage
	}
	req, _ := http.NewRequest("POST", deps.shipURL+"/v1/projects/"+args[0]+"/tasks/"+args[1]+"/merge", nil)
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe landing: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(body))
	return nil
}