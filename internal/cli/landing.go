package cli

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"strings"
)

// `cafe landing` is a contribution: the thing that replaces a pull request.
//
// A landing is NOT a task, even though ship creates a linked task for every
// one: it has its own routes, its own decisions and its own contributor. The
// verbs here used to POST to the task routes, so a landing was never created,
// never listed, and every approval was a 404 that reported success.

// The seam this command group talks through is shipDeps, in ship.go. There is
// one of them on purpose: see that file.

func newLandingCommand(deps landingDeps) *Command {
	deps, urlErr := deps.withDefaults()
	c := &Command{
		Name:    "landing",
		Summary: "manage landings (contributions that replace PRs)",
		Usage:   "cafe landing <subcommand> [args]",
		LongHelp: "Manage landings on cafaye ship. A landing is a contribution to a project,\n" +
			"and ship keeps a linked task for its work lifecycle.\n\n" +
			"Subcommands:\n" +
			"  create <project-id>                        Submit a new landing\n" +
			"  list <project-id>                          List landings\n" +
			"  get <project-id> <landing-id>               Get landing details\n" +
			"  approve <project-id> <landing-id>           Approve a landing\n" +
			"  reject <project-id> <landing-id>            Reject a landing\n" +
			"  merge <project-id> <landing-id>             Merge an approved landing\n" +
			"  results <project-id> <landing-id>           Record a result against a landing\n\n" +
			"Environment:\n" +
			"  SHIP_URL     Base URL of the ship service\n" +
			"  SHIP_TOKEN   Service token, sent as `Authorization: Bearer …`\n" +
			"  SHIP_USER_ID Contributor recorded on a landing (default: anonymous)\n",
	}
	c.Run = func(args []string, env *Env) error {
		if urlErr != nil {
			return urlErr
		}
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
			return landingDecide(deps, env, args[1:], "approve")
		case "reject":
			return landingDecide(deps, env, args[1:], "reject")
		case "merge":
			return landingMerge(deps, env, args[1:])
		case "results":
			return landingResults(deps, env, args[1:])
		case "policy":
			return landingPolicy(deps, env, args[1:])
		default:
			_, _ = fmt.Fprintf(env.Stderr, "cafe landing: unknown subcommand %q\n", args[0])
			return errUsage
		}
	}
	return c
}

// landingCreate submits a landing.
//
// The contributor is sent, and it is not decoration: ship's idempotency key is
// (project, contributor, external id), so a landing submitted without one
// lands under the key "anonymous" and every later submission with the same
// external id collides with it.
func landingCreate(deps landingDeps, env *Env, args []string) error {
	projectID, err := shipProjectArg("landing create", args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("landing create", flag.ContinueOnError)
	kind := fs.String("kind", "change", "landing kind: change, issue, feedback, data, request")
	title := fs.String("title", "", "landing title")
	description := fs.String("description", "", "landing description")
	contributor := fs.String("contributor", shipUserID(), "who is contributing")
	contributorKind := fs.String("contributor-kind", "human", "contributor kind: human or agent")
	externalID := fs.String("external-id", "", "external identifier (the idempotency key)")
	sourceURL := fs.String("source-url", "", "source URL")
	if err := shipParse(fs, args[1:], "landing", env); err != nil {
		if errors.Is(err, errShipHelp) {
			return nil
		}
		return err
	}
	if *title == "" {
		*title = "landing: " + *kind
	}
	body := map[string]any{
		"title":            *title,
		"description":      *description,
		"kind":             *kind,
		"contributor":      *contributor,
		"contributor_kind": *contributorKind,
	}
	if *externalID != "" {
		body["external_id"] = *externalID
	}
	if *sourceURL != "" {
		body["source_url"] = *sourceURL
	}
	req, err := shipJSONRequest("POST", deps.shipURL,
		"/v1/ship/projects/"+projectID+"/landings", body)
	if err != nil {
		return err
	}
	respBody, err := shipDo(deps.client, req)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(respBody))
	return nil
}

// landingList lists the landings of a project.
func landingList(deps landingDeps, env *Env, args []string) error {
	projectID, err := shipProjectArg("landing list", args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("landing list", flag.ContinueOnError)
	kind := fs.String("kind", "", "filter by kind")
	status := fs.String("status", "", "filter by status")
	if err := shipParse(fs, args[1:], "landing", env); err != nil {
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
		"/v1/ship/projects/"+projectID+"/landings"+shipQuery(params), nil)
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

// landingGet fetches one landing.
func landingGet(deps landingDeps, env *Env, args []string) error {
	projectID, err := shipProjectArg("landing get", args)
	if err != nil {
		return err
	}
	landingID, err := shipArg("landing get", "landing ID", args, 1)
	if err != nil {
		return err
	}
	req, err := shipRequest("GET", deps.shipURL,
		"/v1/ship/projects/"+projectID+"/landings/"+landingID, nil)
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

// landingDecide approves or rejects a landing.
//
// The two are one function because they are one route with one body: the
// decision differs only in the word at the end of the path.
//
// `--by` is passed through as it was given, because ship's decided_by is a
// string and not a UUID, and a name is a legitimate value for it. It is
// recorded either way, so an approval with no `--by` is ship recording an
// approval nobody claimed — which is the caller's choice to make, not a
// reason for the CLI to invent one.
func landingDecide(deps landingDeps, env *Env, args []string, decision string) error {
	group := "landing " + decision
	projectID, err := shipProjectArg(group, args)
	if err != nil {
		return err
	}
	landingID, err := shipArg(group, "landing ID", args, 1)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet(group, flag.ContinueOnError)
	by := fs.String("by", "", "who is deciding")
	note := fs.String("note", "", "decision note")
	if err := shipParse(fs, args[2:], "landing", env); err != nil {
		if errors.Is(err, errShipHelp) {
			return nil
		}
		return err
	}
	req, err := shipJSONRequest("POST", deps.shipURL,
		"/v1/ship/projects/"+projectID+"/landings/"+landingID+"/"+decision,
		map[string]any{"decided_by": *by, "note": *note})
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

// landingMerge merges an approved landing.
func landingMerge(deps landingDeps, env *Env, args []string) error {
	projectID, err := shipProjectArg("landing merge", args)
	if err != nil {
		return err
	}
	landingID, err := shipArg("landing merge", "landing ID", args, 1)
	if err != nil {
		return err
	}
	req, err := shipRequest("POST", deps.shipURL,
		"/v1/ship/projects/"+projectID+"/landings/"+landingID+"/merge", nil)
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

// landingResults records a result against a landing: the evidence that the
// contribution was certified.
func landingResults(deps landingDeps, env *Env, args []string) error {
	projectID, err := shipProjectArg("landing results", args)
	if err != nil {
		return err
	}
	landingID, err := shipArg("landing results", "landing ID", args, 1)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("landing results", flag.ContinueOnError)
	stage := fs.String("stage", "", "stage the result belongs to")
	resultType := fs.String("type", "info", "result type: success, failure or info")
	message := fs.String("message", "", "what the result says")
	if err := shipParse(fs, args[2:], "landing", env); err != nil {
		if errors.Is(err, errShipHelp) {
			return nil
		}
		return err
	}
	req, err := shipJSONRequest("POST", deps.shipURL,
		"/v1/ship/projects/"+projectID+"/landings/"+landingID+"/results",
		map[string]any{
			"stage_name": *stage,
			"type":       *resultType,
			"message":    *message,
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

// landingPolicy gets or sets a project's landing policy.
func landingPolicy(deps landingDeps, env *Env, args []string) error {
	if len(args) == 0 {
		_, _ = fmt.Fprintf(env.Stderr, "usage: cafe landing policy <project-id> | cafe landing policy set <project-id> [flags]\n")
		return errUsage
	}
	if args[0] == "set" {
		if len(args) < 2 {
			_, _ = fmt.Fprintf(env.Stderr, "landing policy set: missing project id\n")
			return errUsage
		}
		projectID := args[1]
		fs := flag.NewFlagSet("landing policy set", flag.ContinueOnError)
		allowedKinds := fs.String("allowed-kinds", "", "comma-separated landing kinds allowed (change,issue,feedback,data,request)")
		allowedContribs := fs.String("allowed-contributors", "", "comma-separated cafaye user/team/org ids allowed")
		requiredEvidence := fs.String("required-evidence", "", "comma-separated evidence types required before approval")
		requireHuman := fs.Bool("require-human-approval", false, "require a human to approve")
		requireChecks := fs.Bool("require-checks-green", false, "require linked task checks to pass before merge")
		if err := shipParse(fs, args[2:], "landing", env); err != nil {
			if errors.Is(err, errShipHelp) {
				return nil
			}
			return err
		}
		body := map[string]any{}
		if *allowedKinds != "" {
			body["allowed_kinds"] = strings.Split(*allowedKinds, ",")
		}
		if *allowedContribs != "" {
			body["allowed_contributors"] = strings.Split(*allowedContribs, ",")
		}
		if *requiredEvidence != "" {
			body["required_evidence"] = strings.Split(*requiredEvidence, ",")
		}
		if *requireHuman {
			body["require_human_approval"] = true
		}
		if *requireChecks {
			body["require_checks_green"] = true
		}
		req, err := shipJSONRequest("PATCH", deps.shipURL, "/v1/projects/"+projectID+"/landing-policy", body)
		if err != nil {
			return err
		}
		respBody, err := shipDo(deps.client, req)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(respBody))
		return nil
	}
	projectID := args[0]
	req, err := shipRequest("GET", deps.shipURL, "/v1/projects/"+projectID+"/landing-policy", nil)
	if err != nil {
		return err
	}
	respBody, err := shipDo(deps.client, req)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(respBody))
	return nil
}
