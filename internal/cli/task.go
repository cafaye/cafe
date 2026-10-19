package cli

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
)

// `cafe task` is the work object on ship: units of work under a project.
//
// Every route it calls is a task route. The approve/reject/merge verbs it used
// to carry are gone: ship has no task-scoped decision route, so they were a
// 404 that reported success. The decisions that DO exist are landing
// decisions, and `cafe landing` is where they live.

// The seam this command group talks through is shipDeps, in ship.go. There is
// one of them on purpose: see that file.

func newTaskCommand(deps taskDeps) *Command {
	deps, urlErr := deps.withDefaults()
	c := &Command{
		Name:    "task",
		Summary: "manage tasks (units of work) on cafaye",
		Usage:   "cafe task <subcommand> [args]",
		LongHelp: "Manage tasks on cafaye ship.\n\n" +
			"Subcommands:\n" +
			"  create <project-id>                       Create a new task\n" +
			"  list <project-id>                         List tasks\n" +
			"  get <project-id> <task-id>                Get task details\n" +
			"  status <project-id> <task-id> <status>    Update task status\n" +
			"  stage <project-id> <task-id> <stage>      Move task to a stage\n" +
			"  results <project-id> <task-id>            Record a result against a task\n\n" +
			"Environment:\n" +
			"  SHIP_URL   Base URL of the ship service\n" +
			"  SHIP_TOKEN Service token, sent as `Authorization: Bearer …`\n",
	}
	c.Run = func(args []string, env *Env) error {
		if urlErr != nil {
			return urlErr
		}
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
		case "results":
			return taskResults(deps, env, args[1:])
		default:
			_, _ = fmt.Fprintf(env.Stderr, "cafe task: unknown subcommand %q\n", args[0])
			return errUsage
		}
	}
	return c
}

// taskCreate creates a task under a project.
func taskCreate(deps taskDeps, env *Env, args []string) error {
	projectID, err := shipProjectArg("task create", args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("task create", flag.ContinueOnError)
	title := fs.String("title", "", "task title")
	description := fs.String("description", "", "task description")
	kind := fs.String("kind", "feature", "task kind: feature, bug, chore, change, issue, feedback, data, request")
	if err := shipParse(fs, args[1:], "task", env); err != nil {
		if errors.Is(err, errShipHelp) {
			return nil
		}
		return err
	}
	if *title == "" && len(fs.Args()) > 0 {
		*title = fs.Args()[0]
	}
	if *title == "" {
		*title = "untitled task"
	}
	req, err := shipJSONRequest("POST", deps.shipURL,
		"/v1/projects/"+projectID+"/tasks",
		map[string]any{
			"title":       *title,
			"description": *description,
			"kind":        *kind,
		})
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

// taskList lists the tasks of a project.
func taskList(deps taskDeps, env *Env, args []string) error {
	projectID, err := shipProjectArg("task list", args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("task list", flag.ContinueOnError)
	kind := fs.String("kind", "", "filter by kind")
	status := fs.String("status", "", "filter by status")
	if err := shipParse(fs, args[1:], "task", env); err != nil {
		if errors.Is(err, errShipHelp) {
			return nil
		}
		return err
	}
	params := url.Values{}
	if *kind != "" {
		params.Set("kind", *kind)
	}
	if *status != "" {
		params.Set("status", *status)
	}
	req, err := shipRequest("GET", deps.shipURL,
		"/v1/projects/"+projectID+"/tasks"+shipQuery(params), nil)
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

// taskGet fetches one task.
func taskGet(deps taskDeps, env *Env, args []string) error {
	projectID, err := shipProjectArg("task get", args)
	if err != nil {
		return err
	}
	taskID, err := shipArg("task get", "task ID", args, 1)
	if err != nil {
		return err
	}
	req, err := shipRequest("GET", deps.shipURL,
		"/v1/projects/"+projectID+"/tasks/"+taskID, nil)
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

// taskStatus moves a task to a status.
func taskStatus(deps taskDeps, env *Env, args []string) error {
	projectID, err := shipProjectArg("task status", args)
	if err != nil {
		return err
	}
	taskID, err := shipArg("task status", "task ID", args, 1)
	if err != nil {
		return err
	}
	status, err := shipArg("task status", "status", args, 2)
	if err != nil {
		return err
	}
	req, err := shipJSONRequest("PATCH", deps.shipURL,
		"/v1/projects/"+projectID+"/tasks/"+taskID+"/status",
		map[string]any{"status": status})
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

// taskStage moves a task to a stage.
func taskStage(deps taskDeps, env *Env, args []string) error {
	projectID, err := shipProjectArg("task stage", args)
	if err != nil {
		return err
	}
	taskID, err := shipArg("task stage", "task ID", args, 1)
	if err != nil {
		return err
	}
	stage, err := shipArg("task stage", "stage", args, 2)
	if err != nil {
		return err
	}
	req, err := shipJSONRequest("POST", deps.shipURL,
		"/v1/projects/"+projectID+"/tasks/"+taskID+"/stage",
		map[string]any{"stage": stage})
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

// taskResults records a result against a task: the evidence that the work
// happened, which is what a gate later reads.
func taskResults(deps taskDeps, env *Env, args []string) error {
	projectID, err := shipProjectArg("task results", args)
	if err != nil {
		return err
	}
	taskID, err := shipArg("task results", "task ID", args, 1)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("task results", flag.ContinueOnError)
	resultType := fs.String("type", "info", "result type: success, failure or info")
	message := fs.String("message", "", "what the result says")
	if err := shipParse(fs, args[2:], "task", env); err != nil {
		if errors.Is(err, errShipHelp) {
			return nil
		}
		return err
	}
	req, err := shipJSONRequest("POST", deps.shipURL,
		"/v1/projects/"+projectID+"/tasks/"+taskID+"/results",
		map[string]any{
			"type":    *resultType,
			"message": *message,
		})
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
