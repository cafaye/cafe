package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"
)

// `cafe repo` manages the repositories in your Cafaye account.
//
// IT TALKS TO SHIP, NOT TO THE GIT HOST, and that is the whole point of it.
//
// The old version of this command took a token for the git host from the
// environment — FORGEJO_TOKEN, or CAFAYE_GIT_TOKEN — and sent it to the git
// host's API. That put a credential that could reach every repository on the
// host into every developer's shell, every CI runner and every dev box, and
// it meant the answer to "who may touch this repository" was decided by
// whoever held the token rather than by the platform.
//
// So the CLI asks ship, ship decides, and the only credential that ever leaves
// the platform is one scoped to the caller's own repositories. `cafe repo
// token` is how you get that credential, and it is scoped to your own
// repositories and to nothing else.
//
//   cafe repo list               List the repositories in your account
//   cafe repo create <name>      Create a project and its repository
//   cafe repo token [name]       Mint a credential for your own machine
//   cafe repo delete <name>      Delete a repository from your account
//
// Environment:
//
//   SHIP_URL   Base URL of the deployment (default: https://cafaye.com)
//   SHIP_TOKEN Service token, sent as `Authorization: Bearer …`
//
// Without a service token the command uses the credential `cafe login` stored,
// which is how a person acts as themselves rather than as the platform.

type repoDeps = shipDeps

func newRepoCommand(deps repoDeps) *Command {
	deps, _ = deps.withDefaults()
	c := &Command{
		Name:    "repo",
		Summary: "manage the repositories in your Cafaye account",
		Usage:   "cafe repo <subcommand> [args]",
		LongHelp: "Manage the repositories in your Cafaye account.\n\n" +
			"Subcommands:\n" +
			"  create <name>   Create a project and its repository\n" +
			"  list            List the repositories in your account\n" +
			"  token [name]    Mint a credential for your own machine\n" +
			"  delete <name>   Delete a repository from your account\n\n" +
			"Environment:\n" +
			"  SHIP_URL   Base URL of the deployment (default: https://cafaye.com)\n" +
			"  SHIP_TOKEN Service token, sent as `Authorization: Bearer …`\n",
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
		case "token":
			return repoToken(deps, env, args[1:])
		case "delete":
			return repoDelete(deps, env, args[1:])
		default:
			_, _ = fmt.Fprintf(env.Stderr, "cafe repo: unknown subcommand %q\n", args[0])
			return errUsage
		}
	}
	return c
}

// repoList prints the repositories in the caller's account.
func repoList(deps repoDeps, env *Env, args []string) error {
	fs := flag.NewFlagSet("repo list", flag.ContinueOnError)
	if err := shipParse(fs, args, "repo", env); err != nil {
		return err
	}
	req, err := shipRequest("GET", deps.shipURL, "/v1/ship/repos", nil)
	if err != nil {
		return err
	}
	body, err := shipDo(deps.client, req)
	if err != nil {
		return err
	}
	var repos []struct {
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
		CloneURL string `json:"clone_url"`
		HTMLURL  string `json:"html_url"`
	}
	if err := json.Unmarshal(body, &repos); err != nil {
		return fmt.Errorf("cafe repo: read the repository list: %w", err)
	}
	if len(repos) == 0 {
		_, _ = fmt.Fprintln(env.Stdout, "no repositories yet — `cafe repo create <name>` makes one")
		return nil
	}
	for _, r := range repos {
		visibility := "public"
		if r.Private {
			visibility = "private"
		}
		_, _ = fmt.Fprintf(env.Stdout, "%-40s %-8s %s\n", r.FullName, visibility, r.CloneURL)
	}
	return nil
}

// repoCreate creates a project and, with it, the repository that holds the
// code.
//
// IT GOES THROUGH THE PROJECT ROUTE rather than making a repository directly,
// because a project and its repository are one thing seen from two sides: the
// repository holds the code and the project holds the runtime. A repository
// with nothing running behind it is a repository nobody will remember to
// clean up.
func repoCreate(deps repoDeps, env *Env, args []string) error {
	fs := flag.NewFlagSet("repo create", flag.ContinueOnError)
	private := fs.Bool("private", true, "create a private repository")
	if err := shipParse(fs, args, "repo", env); err != nil {
		return err
	}
	name, err := shipArg("repo create", "name", fs.Args(), 0)
	if err != nil {
		return err
	}
	req, err := shipJSONRequest("POST", deps.shipURL, "/v1/ship/projects/", map[string]any{
		"name":    name,
		"private": *private,
	})
	if err != nil {
		return err
	}
	body, err := shipDo(deps.client, req)
	if err != nil {
		return err
	}
	var project struct {
		Name             string `json:"name"`
		RepoFullName     string `json:"repo_full_name"`
		CafayeGitRepoURL string `json:"cafaye_git_repo_url"`
	}
	if err := json.Unmarshal(body, &project); err != nil {
		return fmt.Errorf("cafe repo: read the created project: %w", err)
	}
	_, _ = fmt.Fprintf(env.Stdout, "created: %s\n", project.Name)
	if project.RepoFullName != "" {
		_, _ = fmt.Fprintf(env.Stdout, "  repo:  %s\n", project.RepoFullName)
		_, _ = fmt.Fprintf(env.Stdout, "  clone: %s\n", project.CafayeGitRepoURL)
	}
	return nil
}

// repoToken mints a credential for the caller's own machine.
//
// THE SECRET IS SHOWN ONCE. It is in this output and nowhere else, so it
// belongs in a secret store and not in a shell history: the command says so
// rather than leaving the person to work it out.
func repoToken(deps repoDeps, env *Env, args []string) error {
	fs := flag.NewFlagSet("repo token", flag.ContinueOnError)
	readOnly := fs.Bool("read-only", false, "mint a token that can only read")
	if err := shipParse(fs, args, "repo", env); err != nil {
		return err
	}
	name := ""
	if len(fs.Args()) > 0 {
		name = fs.Args()[0]
	}
	req, err := shipJSONRequest("POST", deps.shipURL, "/v1/ship/credential", map[string]any{
		"name":      name,
		"read_only": *readOnly,
	})
	if err != nil {
		return err
	}
	body, err := shipDo(deps.client, req)
	if err != nil {
		return err
	}
	var token struct {
		Secret   string   `json:"secret"`
		Username string   `json:"username"`
		Scopes   []string `json:"scopes"`
		GitHost  string   `json:"git_host"`
	}
	if err := json.Unmarshal(body, &token); err != nil {
		return fmt.Errorf("cafe repo: read the minted credential: %w", err)
	}
	_, _ = fmt.Fprintf(env.Stdout, "username: %s\n", token.Username)
	_, _ = fmt.Fprintf(env.Stdout, "scopes:   %s\n", strings.Join(token.Scopes, " "))
	_, _ = fmt.Fprintf(env.Stdout, "token:    %s\n", token.Secret)
	_, _ = fmt.Fprintln(env.Stderr, "This token is shown once. Store it in a secret store, not in your shell history.")
	return nil
}

// repoDelete removes a repository from the caller's account.
func repoDelete(deps repoDeps, env *Env, args []string) error {
	fs := flag.NewFlagSet("repo delete", flag.ContinueOnError)
	if err := shipParse(fs, args, "repo", env); err != nil {
		return err
	}
	name, err := shipArg("repo delete", "name", fs.Args(), 0)
	if err != nil {
		return err
	}
	req, err := shipRequest("DELETE", deps.shipURL, "/v1/ship/repos/"+name, nil)
	if err != nil {
		return err
	}
	if _, err := shipDo(deps.client, req); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(env.Stdout, "deleted: %s\n", name)
	return nil
}
