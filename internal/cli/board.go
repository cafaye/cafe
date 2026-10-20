package cli

import (
	"fmt"
	"net/url"
)

// `cafe board` is the attention model: "something changed while you were
// away".
//
// The two verbs have to agree about WHO is asking. The view is recorded per
// user, and the attention check reads the same record — so the view sends the
// identity in the body and the attention check sends it in the query. The old
// attention check sent nothing, so ship fell back to "anonymous" and asked a
// user who had never looked at the board whether they had.

// The seam this command group talks through is shipDeps, in ship.go. There is
// one of them on purpose: see that file.

func newBoardCommand(deps boardDeps) *Command {
	deps, urlErr := deps.withDefaults()
	c := &Command{
		Name:    "board",
		Summary: "board view and attention tracking",
		Usage:   "cafe board <subcommand> [args]",
		LongHelp: "Board view and attention tracking on cafaye ship.\n\n" +
			"Subcommands:\n" +
			"  view <project-id>       Record that you viewed the board\n" +
			"  attention <project-id>  Check if attention is needed\n\n" +
			"Environment:\n" +
			"  SHIP_URL     Base URL of the ship service\n" +
			"  SHIP_TOKEN   Service token, sent as `Authorization: Bearer …`\n" +
			"  SHIP_USER_ID Who is asking (default: anonymous)\n",
	}
	c.Run = func(args []string, env *Env) error {
		if urlErr != nil {
			return urlErr
		}
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

// boardView records that the user looked at the board.
func boardView(deps boardDeps, env *Env, args []string) error {
	projectID, err := shipProjectArg("board view", args)
	if err != nil {
		return err
	}
	req, err := shipJSONRequest("POST", deps.shipURL,
		"/v1/ship/projects/"+projectID+"/board/view",
		map[string]any{"user_id": shipUserID()})
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

// boardAttention asks whether anything changed since the user last looked.
func boardAttention(deps boardDeps, env *Env, args []string) error {
	projectID, err := shipProjectArg("board attention", args)
	if err != nil {
		return err
	}
	params := url.Values{}
	params.Set("user_id", shipUserID())
	req, err := shipRequest("GET", deps.shipURL,
		"/v1/ship/projects/"+projectID+"/board/attention"+shipQuery(params), nil)
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
