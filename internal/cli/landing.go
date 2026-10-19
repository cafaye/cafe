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

type landingDeps struct {
	shipURL string
	client  *http.Client
}

func (d landingDeps) withDefaults() landingDeps {
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

func newLandingCommand(deps landingDeps) *Command {
	deps = deps.withDefaults()
	c := &Command{
		Name:    "landing",
		Summary: "manage landings (contributions that replace PRs)",
		Usage:   "cafe landing <subcommand> [args]",
		LongHelp: "Manage landings on cafaye ship.\n\n" +
			"Subcommands:\n" +
			"  create <project-id>   Submit a new landing\n" +
			"  list <project-id>     List landings\n" +
			"  get <landing-id>      Get landing details\n" +
			"  approve <landing-id>  Approve a landing\n" +
			"  reject <landing-id>   Reject a landing\n" +
			"  merge <landing-id>    Merge an approved landing\n\n" +
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
	contributor := fs.String("contributor", "", "contributor ID (agent or user)")
	externalID := fs.String("external-id", "", "external identifier")
	sourceURL := fs.String("source-url", "", "source URL")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *contributor == "" {
		*contributor = os.Getenv("SHIP_CONTRIBUTOR_ID")
	}
	if *contributor == "" {
		_, _ = fmt.Fprintf(env.Stderr, "cafe landing create: --contributor is required\n")
		return errUsage
	}
	body := map[string]any{
		"kind":        *kind,
		"contributor": *contributor,
	}
	if *externalID != "" {
		body["external_id"] = *externalID
	}
	if *sourceURL != "" {
		body["source_url"] = *sourceURL
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", deps.shipURL+"/v1/projects/"+args[0]+"/landings", bytes.NewReader(data))
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
	url := deps.shipURL + "/v1/projects/" + args[0] + "/landings"
	params := ""
	if *kind != "" {
		params += "?kind=" + *kind
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
	if len(args) < 1 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe landing get: missing landing ID\n")
		return errUsage
	}
	req, _ := http.NewRequest("GET", deps.shipURL+"/v1/landings/"+args[0], nil)
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
	if len(args) < 1 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe landing approve: missing landing ID\n")
		return errUsage
	}
	fs := flag.NewFlagSet("landing approve", flag.ContinueOnError)
	by := fs.String("by", "", "who is approving")
	note := fs.String("note", "", "approval note")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	body := map[string]any{"decided_by": *by, "note": *note}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", deps.shipURL+"/v1/landings/"+args[0]+"/approve", bytes.NewReader(data))
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
	if len(args) < 1 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe landing reject: missing landing ID\n")
		return errUsage
	}
	fs := flag.NewFlagSet("landing reject", flag.ContinueOnError)
	by := fs.String("by", "", "who is rejecting")
	note := fs.String("note", "", "rejection note")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	body := map[string]any{"decided_by": *by, "note": *note}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", deps.shipURL+"/v1/landings/"+args[0]+"/reject", bytes.NewReader(data))
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
	if len(args) < 1 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe landing merge: missing landing ID\n")
		return errUsage
	}
	req, _ := http.NewRequest("POST", deps.shipURL+"/v1/landings/"+args[0]+"/merge", nil)
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