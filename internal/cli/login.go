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
	baseURL       string
	email         string
	password      string
	passwordStdin bool
}

func newLoginCommand(deps loginDeps) *Command {
	opts := &loginOptions{}
	c := &Command{
		Name:    "login",
		Summary: "log in as a human: POST /v1/session with email and password, store the token in the OS keyring",
		Usage:   "cafe login --email <email> (--password <password> | --password-stdin) [--base-url <url>]",
		LongHelp: "The HUMAN login, distinct from `cafe agent login` (the agent principal).\n\n" +
			"`cafe agent login` makes a saved agent session the active one — a local\n" +
			"decision, no network. `cafe login` authenticates a human against identity's\n" +
			"`POST /v1/session` (email + password, the same one the website uses), moves\n" +
			"the token straight into the OS keyring, and prints a machine document on\n" +
			"stdout with a prose sentence on stderr. The token is never printed in any\n" +
			"stream, and no endpoint is baked into the binary: pass --base-url or set\n" +
			identityURLEnv + "; an unset one is a refusal, not a guess.\n\n" +
			"The password arrives on one of two channels, never both: `--password` puts\n" +
			"it in argv, which is a password in `ps aux` on a shared box, while\n" +
			"`--password-stdin` reads it from a pipe, which is the paste-safe form the\n" +
			"website's copyable command uses:\n\n" +
			"\tIDENTITY_URL=https://identity.cafaye.com cafe login --email you@example.com --password-stdin\n\n" +
			"Nothing about the password is echoed, logged, or carried into an error or a\n" +
			"report on either channel.",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.baseURL, "base-url", "", "identity's origin; without it, "+identityURLEnv+" is read and an unset variable is a refusal")
			fs.StringVar(&opts.email, "email", "", "human email")
			fs.StringVar(&opts.password, "password", "", "human password; value is never printed, never logged, and stored only in the keyring")
			fs.BoolVar(&opts.passwordStdin, "password-stdin", false, "read the password from stdin, a pipe rather than argv; mutually exclusive with --password, and an empty pipe is a refusal")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		if strings.TrimSpace(opts.email) == "" {
			return fmt.Errorf("%w: cafe login wants --email <email> (usage: %s)", errUsage, c.Usage)
		}
		// The two channels are the same secret, and only one may carry it. The
		// refusal names both rather than preferring one, because a caller who
		// passed both has two ideas about where the password lives and neither
		// of them deserves to win silently.
		if opts.password != "" && opts.passwordStdin {
			return fmt.Errorf("%w: cafe login was given --password and --password-stdin; they are the same secret on two channels and mutually exclusive — pick one (usage: %s)", errUsage, c.Usage)
		}
		password := opts.password
		if opts.passwordStdin {
			// stdin is the pipe, the way `cafe box enroll --token -` reads the
			// capability: readerOrStdin is the seam a test hands a reader to, so
			// the stdin path is driven without touching the test binary's own
			// standard input. The value is trimmed because a pipe carries the
			// writer's trailing newline, and it goes straight to the endpoint:
			// nothing on this path echoes, logs, or reports it.
			raw, err := io.ReadAll(readerOrStdin(env.Stdin))
			if err != nil {
				return fmt.Errorf("cafe login: read the password from stdin: %w", err)
			}
			password = strings.TrimSpace(string(raw))
			if password == "" {
				return fmt.Errorf("%w: cafe login was given --password-stdin and stdin carried no password; an empty one is a refusal, not a login attempt (usage: %s)", errUsage, c.Usage)
			}
		}
		if password == "" {
			return fmt.Errorf("%w: cafe login wants --password <password>, or --password-stdin to read it from a pipe (usage: %s)", errUsage, c.Usage)
		}
		deps = deps.withDefaults()
		url, err := humanBaseURL(deps, opts.baseURL)
		if err != nil {
			_, _ = fmt.Fprintf(env.Stderr, "cafe login: %v\n", err)
			_, _ = fmt.Fprintf(env.Stderr, "cafe login: pass --base-url <url> or set %s\n", identityURLEnv)
			return errReported
		}
		ep := deps.dial(url)
		res, err := ep.Login(env.Context, strings.TrimSpace(opts.email), password)
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
