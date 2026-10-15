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

	"github.com/cafaye/cafe/internal/box"
	"github.com/google/uuid"
)

// mintDeps is what `cafe box mint` needs from the world. It mirrors loginDeps
// and agentDeps: an injectable ring for the human session, an injectable HTTP
// client, and no baked endpoint.
type mintDeps struct {
	dir     string
	ring    box.Keyring
	client  *http.Client
	boxURL  string
	timeout time.Duration
}

func (d mintDeps) withDefaults() mintDeps {
	if d.dir == "" {
		if dir, err := defaultConfigDir(); err == nil {
			d.dir = dir
		}
	}
	if d.ring == nil {
		// Human tokens live under the config dir's keyring, the same place
		// `cafe login` writes them (loginDeps.withDefaults). Using box.FileRing
		// here rather than a new type keeps one secret store.
		if d.dir != "" {
			d.ring = box.FileRing{Dir: filepath.Join(d.dir, "keyring")}
		}
		if d.ring == nil {
			d.ring = box.FileRing{Dir: "keyring"}
		}
	}
	if d.client == nil {
		d.client = &http.Client{Timeout: 30 * time.Second}
	}
	if d.timeout <= 0 {
		d.timeout = 30 * time.Second
	}
	if d.boxURL == "" {
		d.boxURL = strings.TrimSpace(os.Getenv(boxURLEnv))
	}
	return d
}

func (d mintDeps) httpClient() *http.Client {
	if d.client != nil {
		return d.client
	}
	to := d.timeout
	if to <= 0 {
		to = 30 * time.Second
	}
	return &http.Client{Timeout: to}
}

// loadHumanToken finds the human session token in the keyring. It is the bearer
// `cafe login` stored under service "cafe" account "human/<email>".
func loadHumanToken(deps mintDeps) (string, string, error) {
	deps = deps.withDefaults()
	// Try the injectable ring via lister first: tests use an in-memory ring
	// that implements humanTokenLister.
	if lister, ok := deps.ring.(interface {
		ListHumanTokens() ([]string, []string, error)
	}); ok {
		tokens, accounts, err := lister.ListHumanTokens()
		if err != nil {
			return "", "", err
		}
		if len(tokens) == 0 {
			return "", "", fmt.Errorf("no human session: run `cafe login --email <email> --password <password>` first")
		}
		if len(tokens) > 1 {
			return "", "", fmt.Errorf("multiple human sessions found (%d); run `cafe login` to refresh the one you need, or clear the keyring", len(tokens))
		}
		return tokens[0], accounts[0], nil
	}
	// Fallback: scan the FileRing directory for files named "cafe-human_*".
	// This is the real path: the keyring is a directory of 0600 files.
	var ringDir string
	if fr, ok := deps.ring.(box.FileRing); ok {
		ringDir = fr.Dir
	} else {
		// Try to derive from dir: default is <configDir>/keyring
		if deps.dir != "" {
			ringDir = filepath.Join(deps.dir, "keyring")
		}
	}
	if ringDir == "" {
		return "", "", fmt.Errorf("no human session: run `cafe login --email <email> --password <password>` first")
	}
	entries, err := os.ReadDir(ringDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("no human session: run `cafe login --email <email> --password <password>` first")
		}
		return "", "", fmt.Errorf("read the keyring at %s: %w", ringDir, err)
	}
	var tokens []string
	var accounts []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "cafe-human_") {
			continue
		}
		path := filepath.Join(ringDir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		token := strings.TrimSpace(string(raw))
		if token == "" {
			continue
		}
		tokens = append(tokens, token)
		accounts = append(accounts, name)
	}
	if len(tokens) == 0 {
		return "", "", fmt.Errorf("no human session: run `cafe login --email <email> --password <password>` first")
	}
	if len(tokens) > 1 {
		return "", "", fmt.Errorf("multiple human sessions found (%d); run `cafe login` to refresh the one you need, or clear the keyring", len(tokens))
	}
	return tokens[0], accounts[0], nil
}

// mintRequest is the body of POST /v1/accounts/{account_id}/capabilities.
type mintRequest struct {
	BoxID   string `json:"box_id"`
	Purpose string `json:"purpose"`
}

// mintResponse is the answer: capability + installation.
type mintResponse struct {
	Capability struct {
		Token     string `json:"token"`
		Purpose   string `json:"purpose"`
		BoxID     string `json:"box_id"`
		ExpiresAt string `json:"expires_at"`
	} `json:"capability"`
	Installation map[string]any `json:"installation"`
}

// listAccounts calls GET /v1/accounts with the human bearer and returns account ids.
func listAccounts(ctx context.Context, client *http.Client, boxURL, bearer string) ([]string, error) {
	url := strings.TrimRight(boxURL, "/") + "/v1/accounts"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build the request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call box at %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read box's answer: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("box answered %d: %s", resp.StatusCode, truncateMint(raw))
	}
	// Try array first.
	var arr []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 && arr[0].ID != "" {
		ids := make([]string, 0, len(arr))
		for _, a := range arr {
			if strings.TrimSpace(a.ID) != "" {
				ids = append(ids, a.ID)
			}
		}
		if len(ids) > 0 {
			return ids, nil
		}
	}
	// Try object with "accounts" or "data"
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wrapper); err == nil {
		for _, key := range []string{"accounts", "data", "items"} {
			if v, ok := wrapper[key]; ok {
				var inner []struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(v, &inner); err == nil {
					ids := make([]string, 0, len(inner))
					for _, a := range inner {
						if strings.TrimSpace(a.ID) != "" {
							ids = append(ids, a.ID)
						}
					}
					if len(ids) > 0 {
						return ids, nil
					}
				}
			}
		}
		// Single account object?
		var single struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &single); err == nil && single.ID != "" {
			return []string{single.ID}, nil
		}
	}
	return nil, fmt.Errorf("box answered a document this CLI does not understand for GET /v1/accounts: %s", truncateMint(raw))
}

// mintCapability calls POST /v1/accounts/{account_id}/capabilities.
func mintCapability(ctx context.Context, client *http.Client, boxURL, bearer, accountID, boxID string) (*mintResponse, error) {
	url := strings.TrimRight(boxURL, "/") + "/v1/accounts/" + accountID + "/capabilities"
	body, _ := json.Marshal(mintRequest{BoxID: boxID, Purpose: "box.enroll"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call box at %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read box's answer: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("box answered %d: %s", resp.StatusCode, truncateMint(raw))
	}
	var out mintResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("box answered a document this CLI does not understand: %w body=%s", err, truncateMint(raw))
	}
	if strings.TrimSpace(out.Capability.Token) == "" {
		return nil, fmt.Errorf("box answered with no capability token: %s", truncateMint(raw))
	}
	return &out, nil
}

func truncateMint(b []byte) string {
	s := strings.TrimSpace(string(b))
	const max = 240
	if len(s) > max {
		return strings.TrimSpace(s[:max]) + "..."
	}
	if s == "" {
		return "(empty body)"
	}
	return s
}

// mintDoc is the journey-shaped document `cafe box mint` prints.
type mintDoc struct {
	Action        string `json:"action"`
	BoxID         string `json:"box_id"`
	AccountID     string `json:"account_id"`
	Token         string `json:"token"`
	ExpiresAt     string `json:"expires_at"`
	Purpose       string `json:"purpose"`
	EnrollCommand string `json:"enroll_command"`
	Message       string `json:"message"`
}

type boxMintOptions struct {
	box     string
	account string
	boxID   string
	json    bool
}

func newBoxMintCommand(deps mintDeps) *Command {
	opts := &boxMintOptions{}
	c := &Command{
		Name:    "box mint",
		Summary: "mint a single-use box.enroll capability for this box: the token `cafe box enroll` redeems",
		Usage:   "cafe box mint --box <url> [--account <uuid>] [--box-id <uuid>] [--json]",
		LongHelp: "Mint a capability that `cafe box enroll` redeems to enroll this box.\n\n" +
			"The mint is `POST /v1/accounts/{account_id}/capabilities` with a MEMBER SESSION\n" +
			"bearer (the token `cafe login` stored in the OS keyring) and\n" +
			"`{\"box_id\":\"<uuid>\",\"purpose\":\"box.enroll\"}`. The answer is a single-use\n" +
			"capability that lives minutes: `cafe box mint` then `cafe box enroll --token -\n" +
			"--box <origin>` is one flow, and the token cannot be re-spent.\n\n" +
			"The box origin comes from --box or $" + boxURLEnv + "; an unset one is a refusal,\n" +
			"the same one `cafe box enroll` gives. The account comes from --account or, when\n" +
			"omitted, from `GET /v1/accounts` with the same bearer: a sole account is\n" +
			"picked, an ambiguous choice is a refusal that names --account, and the API's\n" +
			"401/422 is surfaced with its message.\n\n" +
			"The box_id is the new box's future identity. Pass --box-id <uuid> or let\n" +
			"the command generate a fresh UUID. The document on stdout carries the token\n" +
			"(it IS the product), its expiry and the exact enroll command to paste;\n" +
			"stderr carries one token-free sentence naming the expiry and the single-use\n" +
			"nature. The token never goes in argv: `cafe box enroll --token -` reads it\n" +
			"from stdin.\n",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.box, "box", "", "box's origin; without it, "+boxURLEnv+" is read and an unset variable is a refusal")
			fs.StringVar(&opts.account, "account", "", "the account that will own the box; when omitted the sole account is picked")
			fs.StringVar(&opts.boxID, "box-id", "", "the new box's id; a fresh UUID when omitted")
			fs.BoolVar(&opts.json, "json", false, "print the machine document on stdout instead of the human one")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		deps = deps.withDefaults()
		boxURL := opts.box
		if boxURL == "" {
			boxURL = deps.boxURL
		}
		if strings.TrimSpace(boxURL) == "" {
			boxURL = strings.TrimSpace(os.Getenv(boxURLEnv))
		}
		if strings.TrimSpace(boxURL) == "" {
			_, _ = fmt.Fprintf(env.Stderr, "cafe box mint: %v\n", box.ErrNoBox)
			_, _ = fmt.Fprintf(env.Stderr, "cafe box mint: pass --box <url> or set %s\n", boxURLEnv)
			return errReported
		}
		// Human session bearer.
		bearer, _, err := loadHumanToken(deps)
		if err != nil {
			_, _ = fmt.Fprintf(env.Stderr, "cafe box mint: %v\n", err)
			return errReported
		}
		// Box ID.
		boxID := strings.TrimSpace(opts.boxID)
		if boxID == "" {
			boxID = uuid.NewString()
		} else {
			if _, err := uuid.Parse(boxID); err != nil {
				return fmt.Errorf("%w: cafe box mint: --box-id %q is not a UUID: %s (usage: %s)", errUsage, boxID, err.Error(), c.Usage)
			}
		}
		// Account.
		accountID := strings.TrimSpace(opts.account)
		if accountID == "" {
			ids, err := listAccounts(env.Context, deps.httpClient(), boxURL, bearer)
			if err != nil {
				_, _ = fmt.Fprintf(env.Stderr, "cafe box mint: list accounts: %v\n", err)
				return errReported
			}
			if len(ids) == 0 {
				_, _ = fmt.Fprintf(env.Stderr, "cafe box mint: no accounts for this session; the human has no membership\n")
				return errReported
			}
			if len(ids) > 1 {
				_, _ = fmt.Fprintf(env.Stderr, "cafe box mint: %d accounts for this session; pass --account <uuid> to choose\n", len(ids))
				for _, id := range ids {
					_, _ = fmt.Fprintf(env.Stderr, "  %s\n", id)
				}
				return errReported
			}
			accountID = ids[0]
		}
		// Mint.
		res, err := mintCapability(env.Context, deps.httpClient(), boxURL, bearer, accountID, boxID)
		if err != nil {
			_, _ = fmt.Fprintf(env.Stderr, "cafe box mint: %v\n", err)
			return errReported
		}
		enrollCmd := fmt.Sprintf("cafe box enroll --token - --box %s", strings.TrimRight(boxURL, "/"))
		doc := mintDoc{
			Action:        "minted",
			BoxID:         res.Capability.BoxID,
			AccountID:     accountID,
			Token:         res.Capability.Token,
			ExpiresAt:     res.Capability.ExpiresAt,
			Purpose:       res.Capability.Purpose,
			EnrollCommand: enrollCmd,
			Message:       "capability minted for box " + res.Capability.BoxID + "; single-use and minutes-lived — run `" + enrollCmd + "` and paste the token on stdin",
		}
		// Ensure the doc's BoxID is the one we minted for, if the server echoed
		// a different one (should not happen, but the doc should be honest).
		if doc.BoxID == "" {
			doc.BoxID = boxID
		}
		if doc.Purpose == "" {
			doc.Purpose = "box.enroll"
		}
		// Stderr: one token-free sentence.
		say(env.Stderr, "capability minted for box %s (expires %s); single-use, minutes-lived — paste it into `%s`", doc.BoxID, doc.ExpiresAt, enrollCmd)
		return writeDoc(env.Stdout, doc, opts.json)
	}
	return c
}
