package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// `cafe repo` manages repositories on Cafaye Git (Forgejo).
//
// The base URL and token come from the environment, so a sandbox that has
// FORGEJO_TOKEN in its vault gets them without extra flags:
//
//   FORGEJO_URL    base URL of Cafaye Git (default https://git.cafaye.com)
//   FORGEJO_TOKEN  access token with repo scope (from git.cafaye.com/user/settings/applications)

type repoDeps struct {
	baseURL string
	token   string
	client  *http.Client
}

func (d repoDeps) withDefaults() (repoDeps, error) {
	if d.baseURL == "" {
		d.baseURL = os.Getenv("FORGEJO_URL")
		if d.baseURL == "" {
			d.baseURL = os.Getenv("CAFAYE_GIT_URL")
		}
		if d.baseURL == "" {
			d.baseURL = "https://git.cafaye.com"
		}
	}
	d.baseURL = strings.TrimRight(d.baseURL, "/")
	if d.token == "" {
		d.token = os.Getenv("FORGEJO_TOKEN")
		if d.token == "" {
			d.token = os.Getenv("CAFAYE_GIT_TOKEN")
		}
	}
	if d.client == nil {
		d.client = &http.Client{}
	}
	return d, nil
}

func newRepoCommand(deps repoDeps) *Command {
	deps, _ = deps.withDefaults()
	c := &Command{
		Name:    "repo",
		Summary: "manage repositories on Cafaye Git",
		Usage:   "cafe repo <subcommand> [args]",
		LongHelp: "Manage repositories on Cafaye Git (Forgejo).\n\n" +
			"Subcommands:\n" +
			"  create <name>   Create a new repository\n" +
			"  list            List your repositories\n" +
			"  get <owner/name>  Get repository details\n" +
			"  delete <owner/name>  Delete a repository\n\n" +
			"Environment:\n" +
			"  FORGEJO_URL     Base URL (default: https://git.cafaye.com)\n" +
			"  FORGEJO_TOKEN   Access token with repo scope\n",
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) == 0 {
			_, _ = fmt.Fprintf(env.Stderr, "cafe repo: missing subcommand (usage: %s)\n", c.Usage)
			return errUsage
		}
		switch args[0] {
		case "create":
			return repoCreate(deps, env, args[1:])
		case "list":
			return repoList(deps, env, args[1:])
		case "get":
			return repoGet(deps, env, args[1:])
		case "delete":
			return repoDelete(deps, env, args[1:])
		default:
			_, _ = fmt.Fprintf(env.Stderr, "cafe repo: unknown subcommand %q\n", args[0])
			return errUsage
		}
	}
	return c
}

func repoRequireToken(deps repoDeps, env *Env) error {
	if deps.token == "" {
		return fmt.Errorf("%w: FORGEJO_TOKEN is required", errUsage)
	}
	return nil
}

func repoCreate(deps repoDeps, env *Env, args []string) error {
	if err := repoRequireToken(deps, env); err != nil {
		return err
	}
	fs := flag.NewFlagSet("repo create", flag.ContinueOnError)
	private := fs.Bool("private", true, "create a private repository")
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := ""
	if len(fs.Args()) > 0 {
		name = fs.Args()[0]
	}
	if name == "" {
		return fmt.Errorf("%w: cafe repo create: name is required", errUsage)
	}
	body, _ := json.Marshal(map[string]any{"name": name, "private": *private, "auto_init": true})
	req, err := http.NewRequest("POST", deps.baseURL+"/api/v1/user/repos", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+deps.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := deps.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("create repo failed: %s", resp.Status)
	}
	cloneURL, _ := result["clone_url"].(string)
	webURL, _ := result["html_url"].(string)
	_, _ = fmt.Fprintf(env.Stdout, "created: %s\n  clone: %s\n  web:   %s\n", name, cloneURL, webURL)
	return nil
}

func repoList(deps repoDeps, env *Env, args []string) error {
	if err := repoRequireToken(deps, env); err != nil {
		return err
	}
	req, err := http.NewRequest("GET", deps.baseURL+"/api/v1/user/repos?limit=50", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+deps.token)
	resp, err := deps.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var repos []map[string]any
	json.NewDecoder(resp.Body).Decode(&repos)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("list repos failed: %s", resp.Status)
	}
	for _, r := range repos {
		name, _ := r["full_name"].(string)
		private, _ := r["private"].(bool)
		cloneURL, _ := r["clone_url"].(string)
		vis := "public"
		if private {
			vis = "private"
		}
		_, _ = fmt.Fprintf(env.Stdout, "%-40s %-8s %s\n", name, vis, cloneURL)
	}
	return nil
}

func repoGet(deps repoDeps, env *Env, args []string) error {
	if err := repoRequireToken(deps, env); err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("%w: cafe repo get: missing owner/name", errUsage)
	}
	parts := strings.SplitN(args[0], "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("%w: cafe repo get: expected owner/name", errUsage)
	}
	req, err := http.NewRequest("GET", deps.baseURL+"/api/v1/repos/"+parts[0]+"/"+parts[1], nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+deps.token)
	resp, err := deps.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("get repo failed: %s", resp.Status)
	}
	out, _ := json.MarshalIndent(result, "", "  ")
	_, _ = fmt.Fprintln(env.Stdout, string(out))
	return nil
}

func repoDelete(deps repoDeps, env *Env, args []string) error {
	if err := repoRequireToken(deps, env); err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("%w: cafe repo delete: missing owner/name", errUsage)
	}
	parts := strings.SplitN(args[0], "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("%w: cafe repo delete: expected owner/name", errUsage)
	}
	req, err := http.NewRequest("DELETE", deps.baseURL+"/api/v1/repos/"+parts[0]+"/"+parts[1], nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+deps.token)
	resp, err := deps.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("delete repo failed: %s", resp.Status)
	}
	_, _ = fmt.Fprintf(env.Stdout, "deleted: %s/%s\n", parts[0], parts[1])
	return nil
}

