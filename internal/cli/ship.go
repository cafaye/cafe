package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cafaye/cafe/internal/agent"
	"github.com/cafaye/cafe/internal/box"
)

// This file is the ONE place the CLI talks to ship.
//
// Before it existed, every ship-backed command built its own request, threw
// away the error `http.NewRequest` returned, never looked at the status code
// and — except in project.go — forgot the service token. Four of those are
// four separate ways for a command to report a success it did not have:
//
//   - a 400 printed its error body and exited 0, so a script could not tell a
//     created project from a refused one;
//   - a malformed SHIP_URL reached the line after the discarded error, which
//     wrote to a nil request's header, and the process died with a stack
//     trace instead of a sentence;
//   - a landing approval POSTed to a route ship does not have, got a 404, and
//     said the landing had been approved;
//   - the token was set in three places in one file and nowhere else, so ship
//     requiring it on every /v1 route would have broken seven of eight
//     command groups.
//
// So the helper does all four jobs once: it builds the request, it checks
// the status, it sends the token, and it hands the body back.

// shipTokenEnv is the service token ship requires on every /v1 route.
const shipTokenEnv = "SHIP_TOKEN"

// The base URL, and where it comes from.
//
// `CAFAYE_URL` is the one name for the deployment, and `SHIP_URL` is the
// older name kept working because a deployment that only knows the older one
// still has to reach the service. The order matters: the deployment's own
// name wins, and the legacy name is the fallback.
//
// The default is the deployment's own hostname rather than ship's, because
// ship is reached THROUGH the deployment: `/v1/ship/…` on the deployment's
// origin is the door, and ship's own hostname is not on the public internet.
const (
	cafayeURLEnv   = "CAFAYE_URL"
	shipURLEnv     = "SHIP_URL"
	defaultShipURL = "https://cafaye.com"
)

// shipDeps is what every ship-backed command needs: the base URL and the
// HTTP client.
//
// It is one type for all of them on purpose. The command groups are aliases
// of it rather than separate structs, because a second struct would be a
// second place to add a field, and the whole argument of this file is that
// there is one.
type shipDeps struct {
	shipURL string
	client  *http.Client
}

// The command groups that talk to ship. Each name is an alias, not a type of
// its own: they configure the same client against the same service.
type (
	taskDeps     = shipDeps
	landingDeps  = shipDeps
	boardDeps    = shipDeps
	pipelineDeps = shipDeps
	projectDeps  = shipDeps
	startDeps    = shipDeps
	shipGateDeps = shipDeps
)

// withDefaults fills in the base URL and the client, and refuses a base URL
// that is not an http or https URL.
//
// The refusal is here, once, rather than at every call site: the URL is
// configuration, and a mistake in configuration is found out about before a
// request is built rather than by a panic three lines into one.
func (d shipDeps) withDefaults() (shipDeps, error) {
	if d.shipURL == "" {
		// SHIP_URL points at the ship service directly; CAFAYE_URL is the
		// website origin, which only proxies ship under /v1/ship and is not
		// a substitute for a direct ship address. Prefer SHIP_URL.
		d.shipURL = os.Getenv(shipURLEnv)
		if d.shipURL == "" {
			d.shipURL = os.Getenv(cafayeURLEnv)
		}
		if d.shipURL == "" {
			d.shipURL = defaultShipURL
		}
	}
	if d.client == nil {
		d.client = &http.Client{Timeout: 30 * time.Second}
	}
	base, err := shipBaseURL(d.shipURL)
	if err != nil {
		return d, err
	}
	d.shipURL = base
	return d, nil
}

// shipBaseURL validates the base URL and trims its trailing slash.
//
// The trailing slash matters because every path in this file starts with one,
// and a base URL that ends with one turns every request into a double slash
// that a router may answer with a 404.
func shipBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("ship URL is empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid ship URL %q: %w", raw, err)
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("invalid ship URL %q: want an http or https URL", raw)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid ship URL %q: no host", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

// shipDo sends one request to ship and returns the response body.
//
// It returns an error for any status outside 2xx, and the error carries the
// status and the trimmed body: ship's errors are plain text written by
// http.Error, and that text is the only thing that says WHY the call failed.
// A command that got a 400 and printed the body on stdout without failing was
// reporting a refusal as a result.
func shipDo(client *http.Client, req *http.Request) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	// Resolve the bearer: prefer the logged-in human's session token (the
	// same one `cafe login` stores), falling back to the service token in
	// the environment. A user authenticates as themselves; the service token
	// is only for deployments and CI. This keeps SHIP_TOKEN out of dev boxes.
	token := strings.TrimSpace(os.Getenv(shipTokenEnv))
	if token == "" {
		// No service token: try the logged-in human's session token.
		if dir, err := defaultConfigDir(); err == nil {
			if email := readHumanSubject(dir); email != "" {
				ring := box.FileRing{Dir: filepath.Join(dir, "keyring")}
				if t, err := ring.Get(agent.KeyringService, humanAccount(email)); err == nil && strings.TrimSpace(t) != "" {
					token = strings.TrimSpace(t)
				}
			}
		}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read ship response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ship %s %s: %s: %s",
			req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// shipRequest builds a request against ship.
//
// The error `http.NewRequest` returns is the one the old code threw away. A
// malformed URL used to reach the next line, which wrote to the nil request's
// header, and a typo in an environment variable became a panic.
//
// The context is `context.Background()` and not a caller's, because a CLI
// command has nothing to cancel it with — but the request still carries one,
// so a request the fleet's linter asks about is a request that can be
// canceled by whoever holds the request.
func shipRequest(method, base, path string, body []byte) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, base+path, reader)
	if err != nil {
		return nil, fmt.Errorf("build %s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// shipJSONRequest builds a request with a JSON body.
func shipJSONRequest(method, base, path string, payload any) (*http.Request, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode %s %s: %w", method, path, err)
	}
	return shipRequest(method, base, path, body)
}

// shipQuery encodes a query string.
//
// The old code concatenated the values into the URL, so a value holding an
// "&" became two parameters and a value holding a "#" cut the path short.
// Encoding is what a query string is for.
func shipQuery(params url.Values) string {
	if len(params) == 0 {
		return ""
	}
	return "?" + params.Encode()
}

// shipUserID is the identity the CLI acts as.
//
// It is the same value in every command that acts as somebody. A command that
// recorded a board view under one identity and then asked for attention
// under another was asking about a board nobody had looked at.
func shipUserID() string {
	if id := strings.TrimSpace(os.Getenv("SHIP_USER_ID")); id != "" {
		return id
	}
	return "anonymous"
}

// errShipHelp means the operator asked for help, which is not a failure.
var errShipHelp = errors.New("help requested")

// shipParse parses one ship subcommand's flags.
//
// The flag set writes nowhere: the router reports the error in its own words,
// and a second copy of the usage text is noise. The error is wrapped in
// errUsage because a flag the command does not declare is a mistake in how
// cafe was invoked, and that is exit code 2 — not a command that ran and
// failed.
//
// `--help` is not a mistake: the flag package reports it, and the group help
// is printed instead, because the group help is where the subcommands and
// their flags are written down.
func shipParse(fs *flag.FlagSet, args []string, group string, env *Env) error {
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			shipHelp(env, group)
			return errShipHelp
		}
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	return nil
}

// shipHelp prints the help of a ship-backed command group.
func shipHelp(env *Env, group string) {
	if c, found := lookupCommand(Commands(), group); found {
		printHelp(env.Stdout, c)
	}
}

// shipProjectArg is the project ID a ship subcommand takes as its first
// argument.
//
// A flag in that position is refused: it means the project ID was left out,
// and addressing a project named "--title" would send the request somewhere
// no project is.
func shipProjectArg(group string, args []string) (string, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", fmt.Errorf("%w: cafe %s: missing project ID", errUsage, group)
	}
	return args[0], nil
}

// shipArg is one positional argument a ship subcommand needs.
func shipArg(group, what string, args []string, index int) (string, error) {
	if len(args) <= index {
		return "", fmt.Errorf("%w: cafe %s: missing %s", errUsage, group, what)
	}
	if strings.HasPrefix(args[index], "-") {
		return "", fmt.Errorf("%w: cafe %s: missing %s", errUsage, group, what)
	}
	return args[index], nil
}
