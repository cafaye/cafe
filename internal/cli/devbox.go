package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"
)

// `cafe devbox` manages cloud dev boxes via cafaye (which provisions them
// on the underlying sandbox provider — users never touch the provider directly).

type devboxDeps = shipDeps

func newDevboxCommand(deps devboxDeps) *Command {
	deps, urlErr := deps.withDefaults()
	c := &Command{
		Name:    "devbox",
		Summary: "manage cloud dev boxes",
		Usage:   "cafe devbox <subcommand> [args]",
		LongHelp: "Manage cloud dev boxes via Cafaye.\n\n" +
			"Subcommands:\n" +
			"  create <name>   Create a new dev box\n" +
			"  ls              List dev boxes\n" +
			"  rm <name>       Delete a dev box\n" +
			"  exec <name>     Run a command on a dev box\n\n" +
			"Environment:\n" +
			"  CAFAYE_URL     Base URL (default: https://cafaye.com)\n" +
			"  SHIP_TOKEN     Service token (or use cafe login)\n",
	}
	c.Run = func(args []string, env *Env) error {
		if urlErr != nil {
			return urlErr
		}
		if len(args) == 0 {
			_, _ = fmt.Fprintf(env.Stderr, "cafe devbox: missing subcommand (usage: %s)\n", c.Usage)
			return errUsage
		}
		switch args[0] {
		case "create":
			return devboxCreate(deps, env, args[1:])
		case "ls":
			return devboxList(deps, env, args[1:])
		case "rm":
			return devboxDestroy(deps, env, args[1:])
		case "exec":
			return devboxExec(deps, env, args[1:])
		default:
			_, _ = fmt.Fprintf(env.Stderr, "cafe devbox: unknown subcommand %q\n", args[0])
			return errUsage
		}
	}
	return c
}

func devboxCreate(deps shipDeps, env *Env, args []string) error {
	fs := flag.NewFlagSet("devbox create", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := "dev-box"
	if len(fs.Args()) > 0 {
		name = fs.Args()[0]
	}
	req, err := shipJSONRequest("POST", deps.shipURL, "/v1/ship/devboxes", map[string]any{"name": name})
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

func devboxList(deps shipDeps, env *Env, args []string) error {
	req, err := shipRequest("GET", deps.shipURL, "/v1/ship/devboxes", nil)
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

func devboxDestroy(deps shipDeps, env *Env, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%w: cafe devbox rm: missing name", errUsage)
	}
	req, err := shipRequest("DELETE", deps.shipURL, "/v1/ship/devboxes/"+args[0], nil)
	if err != nil {
		return err
	}
	_, err = shipDo(deps.client, req)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(env.Stdout, "deleted: %s\n", args[0])
	return nil
}

func devboxExec(deps shipDeps, env *Env, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("%w: cafe devbox exec: missing name and command", errUsage)
	}
	name := args[0]
	command := strings.Join(args[1:], " ")
	req, err := shipJSONRequest("POST", deps.shipURL, "/v1/ship/devboxes/"+name+"/exec", map[string]any{"command": command})
	if err != nil {
		return err
	}
	body, err := shipDo(deps.client, req)
	if err != nil {
		return err
	}
	var result struct {
		Output string `json:"output"`
	}
	json.Unmarshal(body, &result)
	_, _ = fmt.Fprint(env.Stdout, result.Output)
	return nil
}
