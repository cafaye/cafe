package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ConfigFileName is the file the agent's non-secrets live in. It sits beside
// box.json in ~/.config/caf and holds no credential of any kind: a config file
// that carried a token would make every dump, every transcript and every upload
// of the config directory a credential leak. That is the same split the box
// makes, and it is the reason this file is safe to read, share and paste.
const ConfigFileName = "config.json"

// Session is one registered agent at one identity origin. Every field is a
// non-secret: identifiers, a state word, and the NAME of the keyring account the
// token is filed under. The token itself is TokenRef's business and lives in
// the ring, never in here.
type Session struct {
	Name string `json:"name"`
	// AgentUsername is the username identity assigned.
	AgentUsername string `json:"agent_username"`
	// AgentID is identity's own stable identifier for this principal. The
	// previous version cached a `ref` identity has never carried, so the field
	// was silently always empty and then vanished.
	AgentID string `json:"agent_id,omitempty"`
	// AgentKeyID is the identity id of the token currently in the keyring for
	// this session: the bootstrap token's id at registration, updated whenever
	// a verb mints a replacement, cleared when the session's token is revoked.
	// The keyring holds the token's VALUE, which identity's token list does not
	// echo, so this id is the only handle the two share — it is what lets a
	// bare `caf agent token rotate` or `token revoke` find the session's own
	// token without listing and guessing.
	AgentKeyID string `json:"agent_key_id,omitempty"`
	// BaseURL is the identity origin this session talks to.
	BaseURL string `json:"base_url"`
	// TokenRef is the keyring account the token is filed under.
	TokenRef string `json:"token_ref"`
	// Status is the last lifecycle word identity said: unclaimed, claimed or
	// disabled. It is a cache of a remote fact, not a local decision.
	Status string `json:"status,omitempty"`
	// RegisteredAt is when this session was created, RFC3339.
	RegisteredAt string `json:"registered_at,omitempty"`
}

// Active is true when this session is the one commands act on by default.
func (s Session) Active(active string) bool { return s.Name != "" && s.Name == active }

// Config is the whole agent surface of the config file: every saved session and
// which one is active. It is a map keyed by session name because a session is
// identified by its name and a second structure that had to be searched for it
// would be a second place the name is spelled.
type Config struct {
	AgentSessions map[string]Session `json:"agent_sessions"`
	// ActiveAgentSession is the session name commands act on when no --agent
	// or --base-url names one. Empty means none.
	ActiveAgentSession string `json:"active_agent_session,omitempty"`
}

// ConfigPath is where the agent's non-secrets live for one config directory.
func ConfigPath(dir string) string { return filepath.Join(dir, ConfigFileName) }

// LoadConfig reads the config file. A missing file is an empty config rather
// than an error: a machine that has never registered an agent is the ordinary
// first state, and a caller that had to special-case os.ErrNotExist would do it
// differently in each of the five commands.
func LoadConfig(dir string) (Config, error) {
	raw, err := os.ReadFile(ConfigPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return Config{AgentSessions: map[string]Session{}}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read the agent config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("the agent config at %s is not readable JSON: %w", ConfigPath(dir), err)
	}
	if cfg.AgentSessions == nil {
		cfg.AgentSessions = map[string]Session{}
	}
	return cfg, nil
}

// Save writes the config file. It is written 0600 rather than the umask's
// default for one reason: the file names the keyring account a token is filed
// under, and a config file anybody on the machine can edit is a config file that
// can point a token at an account somebody else chose.
func (c Config) Save(dir string) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the agent config: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.WriteFile(ConfigPath(dir), raw, 0o600); err != nil {
		return fmt.Errorf("write the agent config: %w", err)
	}
	return nil
}

// Sessions returns every saved session, ordered by name, so a document a person
// reads and a document a machine reads list the same ones in the same order.
func (c Config) Sessions() []Session {
	out := make([]Session, 0, len(c.AgentSessions))
	for _, name := range sortedNames(c.AgentSessions) {
		s := c.AgentSessions[name]
		s.Name = name
		out = append(out, s)
	}
	return out
}

// Session returns the active session, or ErrNotRegistered when there is none.
// "No active session" and "the active session names something that is not in
// the map" are the same answer to a caller: neither has a session to act on.
func (c Config) Session() (Session, error) {
	if c.ActiveAgentSession == "" {
		return Session{}, ErrNotRegistered
	}
	s, ok := c.AgentSessions[c.ActiveAgentSession]
	if !ok || s.Name == "" {
		return Session{}, fmt.Errorf("%w: the active session %q is not in the config", ErrNotRegistered, c.ActiveAgentSession)
	}
	return s, nil
}

// Select finds the one session the selectors name. An empty selector means "no
// opinion", so `caf agent whoami` acts on the active session and
// `caf agent whoami --agent noel` narrows. More than one match is a refusal
// rather than a choice: picking the first of two environments is how a token
// ends up presented to the wrong origin.
func (c Config) Select(agentUsername, baseURL string) (Session, error) {
	if agentUsername == "" && baseURL == "" {
		return c.Session()
	}
	var matches []Session
	for _, name := range sortedNames(c.AgentSessions) {
		s := c.AgentSessions[name]
		s.Name = name
		if agentUsername != "" && s.AgentUsername != agentUsername {
			continue
		}
		if baseURL != "" && s.BaseURL != baseURL {
			continue
		}
		matches = append(matches, s)
	}
	switch len(matches) {
	case 0:
		return Session{}, fmt.Errorf("%w: no saved session matches --agent %q --base-url %q; run `caf agent register` first", ErrUnknownSession, agentUsername, baseURL)
	case 1:
		return matches[0], nil
	default:
		return Session{}, ErrMultipleSessions
	}
}

func sortedNames(m map[string]Session) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
