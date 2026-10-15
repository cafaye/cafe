package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cafaye/cafe/internal/agent"
	"github.com/cafaye/cafe/internal/box"
)

// loginDeps is what `cafe login` needs, mirroring agentDeps so the seams are
// identical: an injectable ring, an injectable dial, and no baked endpoint.
type loginDeps struct {
	dir     string
	ring    agent.Keyring
	dial    humanDial
	baseURL string
}

func (d loginDeps) withDefaults() loginDeps {
	if d.dir == "" {
		if dir, err := defaultConfigDir(); err == nil {
			d.dir = dir
		}
	}
	if d.baseURL == "" {
		d.baseURL = strings.TrimSpace(os.Getenv(identityURLEnv))
	}
	if d.ring == nil {
		d.ring = box.FileRing{Dir: filepath.Join(d.dir, "keyring")}
	}
	if d.dial == nil {
		d.dial = defaultHumanDial()
	}
	return d
}

// humanDial opens one endpoint for one baseURL. It is a function so the token
// is not held on the deps.
type humanDial func(baseURL string) humanEndpoint

// humanEndpoint is the session creation endpoint identity mounts for humans.
type humanEndpoint interface {
	Login(ctx context.Context, email, password string) (*humanLoginResult, error)
}

type humanLoginResult struct {
	Token string
	Email string
}

// httpHumanEndpoint is the real endpoint: POST /v1/session with email+password.
type httpHumanEndpoint struct {
	BaseURL string
	Client  *http.Client
	Timeout time.Duration
}

func defaultHumanDial() humanDial {
	return func(baseURL string) humanEndpoint {
		return &httpHumanEndpoint{BaseURL: baseURL}
	}
}

func (e *httpHumanEndpoint) client() *http.Client {
	if e.Client != nil {
		return e.Client
	}
	to := e.Timeout
	if to <= 0 {
		to = 30 * time.Second
	}
	return &http.Client{Timeout: to}
}

func (e *httpHumanEndpoint) Login(ctx context.Context, email, password string) (*humanLoginResult, error) {
	if strings.TrimSpace(e.BaseURL) == "" {
		return nil, agent.ErrNoBaseURL
	}
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(e.BaseURL, "/")+"/v1/session", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := e.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("call identity: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read identity's answer: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cafe login failed: status=%d body=%s", resp.StatusCode, truncateHuman(raw))
	}
	var payload struct {
		Token string `json:"token"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("cafe login: the answer was not the document this CLI expects: %w", err)
	}
	if strings.TrimSpace(payload.Token) == "" {
		return nil, fmt.Errorf("cafe login: the answer carried no token; identity did not mint one")
	}
	emailOut := payload.Email
	if strings.TrimSpace(emailOut) == "" {
		emailOut = email
	}
	return &humanLoginResult{Token: payload.Token, Email: emailOut}, nil
}

func truncateHuman(b []byte) string {
	s := strings.TrimSpace(string(b))
	const max = 240
	if len(s) > max {
		return strings.TrimSpace(s[:max]) + "..."
	}
	return s
}

func humanAccount(email string) string { return "human/" + strings.ToLower(strings.TrimSpace(email)) }

type loginDoc struct {
	Action      string `json:"action"`
	Email       string `json:"email"`
	BaseURL     string `json:"base_url"`
	TokenStored bool   `json:"token_stored"`
	Message     string `json:"message"`
}

func humanBaseURL(deps loginDeps, flagURL string) (string, error) {
	if url := strings.TrimSpace(flagURL); url != "" {
		return url, nil
	}
	if url := strings.TrimSpace(deps.baseURL); url != "" {
		return url, nil
	}
	return "", agent.ErrNoBaseURL
}

func sayLogin(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, "cafe login: "+format+"\n", args...)
}

type loginOptions struct {
	baseURL  string
	email    string
	password string
}

func newLoginCommand(deps loginDeps) *Command {
	opts := &loginOptions{}
	c := &Command{
		Name:    "login",
		Summary: "log in as a human: POST /v1/session with email and password, store the token in the OS keyring",
		Usage:   "cafe login --email <email> --password <password> [--base-url <url>]",
		LongHelp: "The HUMAN login, distinct from `cafe agent login` (the agent principal).\n\n" +
			"`cafe agent login` makes a saved agent session the active one — a local\n" +
			"decision, no network. `cafe login` authenticates a human against identity's\n" +
			"`POST /v1/session` (email + password, the same one the website uses), moves\n" +
			"the token straight into the OS keyring, and prints a machine document on\n" +
			"stdout with a prose sentence on stderr. The token is never printed in any\n" +
			"stream, and no endpoint is baked into the binary: pass --base-url or set\n" +
			identityURLEnv + "; an unset one is a refusal, not a guess.",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.baseURL, "base-url", "", "identity's origin; without it, "+identityURLEnv+" is read and an unset variable is a refusal")
			fs.StringVar(&opts.email, "email", "", "human email")
			fs.StringVar(&opts.password, "password", "", "human password; value is never printed, never logged, and stored only in the keyring")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		if strings.TrimSpace(opts.email) == "" {
			return fmt.Errorf("%w: cafe login wants --email <email> (usage: %s)", errUsage, c.Usage)
		}
		if strings.TrimSpace(opts.password) == "" {
			return fmt.Errorf("%w: cafe login wants --password <password> (usage: %s)", errUsage, c.Usage)
		}
		deps = deps.withDefaults()
		url, err := humanBaseURL(deps, opts.baseURL)
		if err != nil {
			_, _ = fmt.Fprintf(env.Stderr, "cafe login: %v\n", err)
			_, _ = fmt.Fprintf(env.Stderr, "cafe login: pass --base-url <url> or set %s\n", identityURLEnv)
			return errReported
		}
		ep := deps.dial(url)
		res, err := ep.Login(env.Context, strings.TrimSpace(opts.email), opts.password)
		if err != nil {
			_, _ = fmt.Fprintf(env.Stderr, "cafe login: %v\n", err)
			return errReported
		}
		ring := deps.ring
		if ring == nil {
			ring = box.FileRing{Dir: filepath.Join(deps.dir, "keyring")}
		}
		acct := humanAccount(res.Email)
		if err := ring.Set(agent.KeyringService, acct, res.Token); err != nil {
			_, _ = fmt.Fprintf(env.Stderr, "cafe login: store the token in the keyring: %v\n", err)
			return errReported
		}
		sayLogin(env.Stderr, "logged in as %s at %s; token_stored: true (the OS keyring; the token itself is never printed)", res.Email, url)
		doc := loginDoc{
			Action:      "logged-in",
			Email:       res.Email,
			BaseURL:     url,
			TokenStored: true,
			Message:     "logged in as " + res.Email + "; the token is in the keyring and is not printed",
		}
		return writeJSONDoc(env.Stdout, doc)
	}
	return c
}
