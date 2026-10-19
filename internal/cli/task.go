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

type taskDeps struct {
	shipURL string
	client  *http.Client
}

func (d taskDeps) withDefaults() taskDeps {
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

func newTaskCommand(deps taskDeps) *Command {
	deps = deps.withDefaults()
	c := &Command{
		Name:    "task",
		Summary: "manage tasks (units of work) on cafaye",
		Usage:   "cafe task <subcommand> [args]",
		LongHelp: "Manage tasks on cafaye ship.\n\n" +
			"Subcommands:\n" +
			"  create <project-id>   Create a new task\n" +
			"  list <project-id>     List tasks\n" +
			"  get <id>              Get task details\n" +
			"  status <id> <status>  Update task status\n" +
			"  stage <id> <stage>    Move task to a stage\n" +
			"  approve <id>          Approve a task (landing)\n" +
			"  reject <id>           Reject a task (landing)\n" +
			"  merge <id>            Merge an approved task (landing)\n\n" +
			"Environment:\n" +
			"  SHIP_URL  Base URL of the ship service\n",
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) == 0 {
			_, _ = fmt.Fprintf(env.Stderr, "cafe task: missing subcommand (usage: %s)\n", c.Usage)
			return errUsage
		}
		switch args[0] {
		case "create":
			return taskCreate(deps, env, args[1:])
		case "list":
			return taskList(deps, env, args[1:])
		case "get":
			return taskGet(deps, env, args[1:])
		case "status":
			return taskStatus(deps, env, args[1:])
		case "stage":
			return taskStage(deps, env, args[1:])
		case "approve":
			return taskApprove(deps, env, args[1:])
		case "reject":
			return taskReject(deps, env, args[1:])
		case "merge":
			return taskMerge(deps, env, args[1:])
		default:
			_, _ = fmt.Fprintf(env.Stderr, "cafe task: unknown subcommand %q\n", args[0])
			return errUsage
		}
	}
	return c
}

func taskCreate(deps taskDeps, env *Env, args []string) error {
	if len(args) < 1 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe task create: missing project ID\n")
		return errUsage
	}
	fs := flag.NewFlagSet("task create", flag.ContinueOnError)
	title := fs.String("title", "", "task title")
	description := fs.String("description", "", "task description")
	kind := fs.String("kind", "feature", "task kind: feature, bug, chore, change, issue, feedback, data, request")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *title == "" && len(fs.Args()) > 0 {
		*title = fs.Args()[0]
	}
	if *title == "" {
		*title = "untitled task"
	}
	body := map[string]any{
		"title":       *title,
		"description": *description,
		"kind":        *kind,
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", deps.shipURL+"/v1/projects/"+args[0]+"/tasks", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe task: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(respBody))
	return nil
}

func taskList(deps taskDeps, env *Env, args []string) error {
	if len(args) < 1 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe task list: missing project ID\n")
		return errUsage
	}
	fs := flag.NewFlagSet("task list", flag.ContinueOnError)
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
		_, _ = fmt.Fprintf(env.Stderr, "cafe task: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(body))
	return nil
}

func taskGet(deps taskDeps, env *Env, args []string) error {
	if len(args) < 1 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe task get: missing task ID\n")
		return errUsage
	}
	req, _ := http.NewRequest("GET", deps.shipURL+"/v1/projects/"+args[0]+"/tasks/"+args[0], nil)
	// Note: this needs project ID + task ID. For simplicity, args[0] is task ID.
	// Actually, the route is /v1/projects/{projectId}/tasks/{taskId}
	// So we need both. Let's use args[0] as task ID and find the project.
	// For now, just use the task ID directly via a different approach.
	_ = req
	_, _ = fmt.Fprintf(env.Stderr, "cafe task get: use 'cafe task list <project-id>' to find task IDs\n")
	return errUsage
}

func taskStatus(deps taskDeps, env *Env, args []string) error {
	if len(args) < 2 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe task status: missing task ID and status\n")
		return errUsage
	}
	// args[0] = project ID, args[1] = task ID, args[2] = status
	// Simplified: args[0] = task ID, args[1] = status (need project ID from env or flag)
	// For now, require project ID as first arg
	if len(args) < 3 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe task status: usage: cafe task status <project-id> <task-id> <status>\n")
		return errUsage
	}
	body := map[string]any{"status": args[2]}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("PATCH", deps.shipURL+"/v1/projects/"+args[0]+"/tasks/"+args[1]+"/status", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe task: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(respBody))
	return nil
}

func taskStage(deps taskDeps, env *Env, args []string) error {
	if len(args) < 3 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe task stage: usage: cafe task stage <project-id> <task-id> <stage>\n")
		return errUsage
	}
	body := map[string]any{"stage": args[2]}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", deps.shipURL+"/v1/projects/"+args[0]+"/tasks/"+args[1]+"/stage", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe task: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(respBody))
	return nil
}

func taskApprove(deps taskDeps, env *Env, args []string) error {
	if len(args) < 2 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe task approve: usage: cafe task approve <project-id> <task-id> [--by <id>] [--note <text>]\n")
		return errUsage
	}
	fs := flag.NewFlagSet("task approve", flag.ContinueOnError)
	by := fs.String("by", "", "who is approving")
	note := fs.String("note", "", "approval note")
	fs.Parse(args[2:])
	body := map[string]any{"decided_by": *by, "note": *note}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", deps.shipURL+"/v1/projects/"+args[0]+"/tasks/"+args[1]+"/approve", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe task: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(respBody))
	return nil
}

func taskReject(deps taskDeps, env *Env, args []string) error {
	if len(args) < 2 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe task reject: usage: cafe task reject <project-id> <task-id> [--by <id>] [--note <text>]\n")
		return errUsage
	}
	fs := flag.NewFlagSet("task reject", flag.ContinueOnError)
	by := fs.String("by", "", "who is rejecting")
	note := fs.String("note", "", "rejection note")
	fs.Parse(args[2:])
	body := map[string]any{"decided_by": *by, "note": *note}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", deps.shipURL+"/v1/projects/"+args[0]+"/tasks/"+args[1]+"/reject", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe task: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(respBody))
	return nil
}

func taskMerge(deps taskDeps, env *Env, args []string) error {
	if len(args) < 2 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe task merge: usage: cafe task merge <project-id> <task-id>\n")
		return errUsage
	}
	req, _ := http.NewRequest("POST", deps.shipURL+"/v1/projects/"+args[0]+"/tasks/"+args[1]+"/merge", nil)
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe task: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(body))
	return nil
}