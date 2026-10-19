package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type boardDeps struct {
	shipURL string
	client  *http.Client
}

func (d boardDeps) withDefaults() boardDeps {
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

func newBoardCommand(deps boardDeps) *Command {
	deps = deps.withDefaults()
	c := &Command{
		Name:    "board",
		Summary: "board view and attention tracking",
		Usage:   "cafe board <subcommand> [args]",
		LongHelp: "Board view and attention tracking on cafaye ship.\n\n" +
			"Subcommands:\n" +
			"  view <project-id>       Record that you viewed the board\n" +
			"  attention <project-id>  Check if attention is needed\n\n" +
			"Environment:\n" +
			"  SHIP_URL  Base URL of the ship service\n",
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) == 0 {
			_, _ = fmt.Fprintf(env.Stderr, "cafe board: missing subcommand (usage: %s)\n", c.Usage)
			return errUsage
		}
		switch args[0] {
		case "view":
			return boardView(deps, env, args[1:])
		case "attention":
			return boardAttention(deps, env, args[1:])
		default:
			_, _ = fmt.Fprintf(env.Stderr, "cafe board: unknown subcommand %q\n", args[0])
			return errUsage
		}
	}
	return c
}

func boardView(deps boardDeps, env *Env, args []string) error {
	if len(args) < 1 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe board view: missing project ID\n")
		return errUsage
	}
	userID := os.Getenv("SHIP_USER_ID")
	if userID == "" {
		userID = "anonymous"
	}
	body := map[string]any{"user_id": userID}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", deps.shipURL+"/v1/projects/"+args[0]+"/board/view", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe board: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(respBody))
	return nil
}

func boardAttention(deps boardDeps, env *Env, args []string) error {
	if len(args) < 1 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe board attention: missing project ID\n")
		return errUsage
	}
	req, _ := http.NewRequest("GET", deps.shipURL+"/v1/projects/"+args[0]+"/board/attention", nil)
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe board: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(body))
	return nil
}