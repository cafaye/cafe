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

type shipGateDeps struct {
	shipURL string
	client  *http.Client
}

func (d shipGateDeps) withDefaults() shipGateDeps {
	if d.shipURL == "" {
		d.shipURL = os.Getenv("CAFAYE_URL")
	}
	if d.shipURL == "" {
		d.shipURL = os.Getenv("SHIP_URL")
	}
	if d.shipURL == "" {
		d.shipURL = "https://cafaye.com"
	}
	if d.client == nil {
		d.client = &http.Client{Timeout: 30 * time.Second}
	}
	return d
}

func gateDecide(deps shipGateDeps, env *Env, args []string) error {
	deps = deps.withDefaults()
	if len(args) < 2 {
		_, _ = fmt.Fprintf(env.Stderr, "cafe gate decide: missing project ID and stage\n")
		return errUsage
	}
	fs := flag.NewFlagSet("gate decide", flag.ContinueOnError)
	status := fs.String("status", "passed", "gate status: passed or failed")
	by := fs.String("by", "", "who is deciding (user or agent ID)")
	note := fs.String("note", "", "decision note")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	body := map[string]any{
		"status":     *status,
		"decided_by": *by,
		"note":       *note,
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", deps.shipURL+"/v1/projects/"+args[0]+"/gates/"+args[1]+"/decide", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := deps.client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "cafe gate: %v\n", err)
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", string(respBody))
	return nil
}