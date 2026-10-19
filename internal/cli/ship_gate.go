package cli

import (
	"errors"
	"flag"
	"fmt"
)

// `cafe gate decide` is the ship half of the gate: the decision a stage's
// gate records, with who decided it and why.
//
// `cafe gate floors` is the other half and it lives in gate.go: that one
// derives cafe's own floors from the tree, this one decides a stage of a
// project's pipeline.

// The seam this command talks through is shipDeps, in ship.go. There is one of
// them on purpose: see that file.

// gateDecide records the decision on one stage of a project's pipeline.
func gateDecide(deps shipGateDeps, env *Env, args []string) error {
	deps, urlErr := deps.withDefaults()
	if urlErr != nil {
		return urlErr
	}
	projectID, err := shipProjectArg("gate decide", args)
	if err != nil {
		return err
	}
	stage, err := shipArg("gate decide", "stage", args, 1)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("gate decide", flag.ContinueOnError)
	status := fs.String("status", "passed", "gate status: passed or failed")
	by := fs.String("by", "", "who is deciding (user or agent ID)")
	note := fs.String("note", "", "decision note")
	if err := shipParse(fs, args[2:], "gate", env); err != nil {
		if errors.Is(err, errShipHelp) {
			return nil
		}
		return err
	}
	req, err := shipJSONRequest("POST", deps.shipURL,
		"/v1/projects/"+projectID+"/gates/"+stage+"/decide",
		map[string]any{
			"status":     *status,
			"decided_by": *by,
			"note":       *note,
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
