package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// maxRegisterAttempts bounds the username-collision retry. Six is the number
// the prior art uses: with a 4-character suffix over a 36-letter alphabet, six
// attempts collide with probability far below the probability that identity is
// down, and a seventh attempt would be a retry of a retry.
const maxRegisterAttempts = 6

// Store is the agent's own state: the config file's non-secrets, the ring that
// holds the token, and the two seams it needs to touch the world.
//
// It holds NO token. What it holds is the ability to fetch one from the ring
// for the duration of one method call, and nothing keeps a reference
// afterwards — which is why Dial is a function and not a client: an
// authenticated client on the Store would be a token on the Store.
type Store struct {
	// Dir is where config.json lives. It is the operator's config directory
	// (~/.config/caf), never a temp directory: a registration is durable state
	// and a temp directory is not.
	Dir string
	// Ring holds the token. See ring.go.
	Ring Keyring
	// Dial opens an endpoint for one call. See endpoint.go.
	Dial Dial
	// now is the clock. It is a field for the reason every other clock in this
	// repository is: a test that cannot control time is a test that either
	// sleeps on the wall clock or asserts nothing about it.
	now func() time.Time
}

// NewStore defaults the clock and the dial, and nothing else. A store with no
// ring is refused at the moment it is used rather than at construction, so a
// caller that only reads state (`caf agent whoami` against an existing session)
// does not need anything configured.
func NewStore(dir string, ring Keyring, dial Dial) *Store {
	return &Store{Dir: dir, Ring: ring, Dial: dial, now: time.Now}
}

func (s *Store) stamp() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

// dial is the per-call endpoint. A nil Dial means the real one, and a nil Ring
// means the first call that touches a token fails naming the cause.
func (s *Store) endpoint(baseURL, token string) Endpoint {
	dial := s.Dial
	if dial == nil {
		dial = DefaultDial()
	}
	return dial(baseURL, token)
}

// --- register -------------------------------------------------------------

// RegisterOptions is what `caf agent register` was asked for.
type RegisterOptions struct {
	BaseURL  string
	Name     string
	Username string
	// LogIn makes this session the active one even when another agent session
	// is already active. Without it, an existing active session is left alone:
	// stealing a developer's active agent because they registered a second one
	// is a change nobody asked for.
	LogIn bool
}

// Registration is what one registration concluded.
//
// There is no token field, and that is the whole design: the token exists on
// this type for exactly as long as it takes to move it into the ring, and the
// document a machine reads says `token_stored: true` — an existence, never a
// value. See the invariant in doc.go.
type Registration struct {
	// Action is "registered" when this call created the session, and
	// "already-registered" when it found one and did nothing.
	Action string `json:"action"`
	Agent  Agent  `json:"agent"`
	// Session is the local name this session is filed under.
	Session string `json:"session"`
	// LoggedIn is true when this session became the active one.
	LoggedIn bool `json:"logged_in"`
	// ActiveSession is the active session after this call, whether or not it is
	// this one — a caller that wants to know what the default now is should not
	// have to run a second command to find out.
	ActiveSession string `json:"active_session,omitempty"`
	// ClaimURL is the URL a human opens to claim this agent. It is a link, not
	// a credential, and it is printed on STDERR: the one thing a person has to
	// do with this whole command.
	ClaimURL string `json:"claim_url,omitempty"`
	// TokenStored is an existence fact. It is as close to the token as any
	// document this CLI prints ever gets.
	TokenStored bool   `json:"token_stored"`
	Message     string `json:"message"`
}

// Register is the whole sequence: ask identity for an unclaimed agent, move the
// token straight into the ring, then write the session's non-secrets.
//
// The order is the ledger's rule with the roles swapped. The token goes to the
// ring BEFORE the config is written, so a crash between the two leaves a
// keyring entry nothing names — which is a stale secret, and costs nothing but
// the disk it sits on. The reverse leaves a config naming a token that is not
// there, which is a session that silently cannot authenticate and that no
// sweep can find.
func (s *Store) Register(ctx context.Context, opts RegisterOptions) (*Registration, error) {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil, ErrNoBaseURL
	}
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return nil, fmt.Errorf("caf agent register: a display name is required; pass --name <name>")
	}
	if s.Ring == nil {
		return nil, fmt.Errorf("caf agent register: no keyring is configured; the token has nowhere to go")
	}

	username := strings.TrimSpace(opts.Username)
	generated := username == ""
	result, err := s.registerWithRetry(ctx, opts.BaseURL, name, username, generated)
	if err != nil {
		return nil, err
	}

	username = result.Agent.Username
	session := sessionName(username, opts.BaseURL)
	if err := s.Ring.Set(KeyringService, ringAccount(session), result.Token); err != nil {
		return nil, fmt.Errorf("caf agent register: store the token in the keyring: %w", err)
	}

	cfg, err := LoadConfig(s.Dir)
	if err != nil {
		return nil, err
	}
	cfg.AgentSessions[session] = Session{
		Name:          session,
		AgentUsername: username,
		AgentID:       result.Agent.ID,
		AgentKeyID:    result.KeyID,
		BaseURL:       opts.BaseURL,
		TokenRef:      ringAccount(session),
		Status:        result.Agent.Status,
		RegisteredAt:  s.stamp().Format(time.RFC3339Nano),
	}
	loggedIn := false
	if opts.LogIn || cfg.ActiveAgentSession == "" {
		cfg.ActiveAgentSession = session
		loggedIn = true
	}
	if err := cfg.Save(s.Dir); err != nil {
		return nil, err
	}

	return &Registration{
		Action:        "registered",
		Agent:         result.Agent,
		Session:       session,
		LoggedIn:      loggedIn,
		ActiveSession: cfg.ActiveAgentSession,
		ClaimURL:      result.ClaimURL,
		TokenStored:   true,
		Message:       "registered agent " + username + " at " + opts.BaseURL + "; the token is in the keyring and is not printed",
	}, nil
}

// registerWithRetry asks identity for an agent, regenerating the username when
// the generated one is taken. A name the caller chose is NOT retried: a person
// who typed `--username noel` and was told "noel is taken" is being told
// something they can act on, and a client that silently registered
// `noel-a3f9` instead has done something they did not ask for.
func (s *Store) registerWithRetry(ctx context.Context, baseURL, name, username string, generated bool) (*RegisterResult, error) {
	for attempt := 0; attempt < maxRegisterAttempts; attempt++ {
		in := RegisterInput{Name: name, Username: username}
		if generated {
			in.Username = autoUsername(name)
		}
		result, err := s.endpoint(baseURL, "").Register(ctx, in)
		if err == nil {
			return result, nil
		}
		if !generated || !isUsernameTaken(err) || attempt == maxRegisterAttempts-1 {
			return nil, fmt.Errorf("caf agent register: %w", err)
		}
	}
	// Unreachable: the loop either returns or returns inside its last attempt.
	return nil, fmt.Errorf("caf agent register: could not find a free username after %d attempts", maxRegisterAttempts)
}

// --- login ----------------------------------------------------------------

// LoginResult is what one login concluded. It is a local decision — which
// session is active — and it deliberately makes no network call: `caf agent
// whoami` is the verb that proves a token works, and a login that needed the
// network to succeed would be a login that fails on a plane.
type LoginResult struct {
	Action  string `json:"action"`
	Session string `json:"session"`
	Agent   Agent  `json:"agent"`
	BaseURL string `json:"base_url"`
	// TokenStored is the existence fact, and it is the one thing login can
	// honestly report about the credential without touching the network.
	TokenStored   bool   `json:"token_stored"`
	ActiveSession string `json:"active_session"`
	Message       string `json:"message"`
}

// Login makes one saved session the active one.
func (s *Store) Login(agentUsername, baseURL string) (*LoginResult, error) {
	cfg, err := LoadConfig(s.Dir)
	if err != nil {
		return nil, err
	}
	sess, err := cfg.Select(agentUsername, baseURL)
	if err != nil {
		return nil, fmt.Errorf("caf agent login: %w", err)
	}
	stored, err := s.token(sess)
	if err != nil {
		return nil, fmt.Errorf("caf agent login: %w", err)
	}
	_ = stored // read to prove it is there; never returned, never printed
	cfg.ActiveAgentSession = sess.Name
	if err := cfg.Save(s.Dir); err != nil {
		return nil, err
	}
	return &LoginResult{
		Action:        "logged-in",
		Session:       sess.Name,
		Agent:         Agent{Username: sess.AgentUsername, ID: sess.AgentID, Status: sess.Status},
		BaseURL:       sess.BaseURL,
		TokenStored:   true,
		ActiveSession: sess.Name,
		Message:       "session " + sess.Name + " is now the active agent session",
	}, nil
}

// --- whoami ---------------------------------------------------------------

// WhoAmI is the answer to "who is this, here". The local half is a fact about
// this machine; the Remote half is identity's own answer, passed through
// because its shape is identity's business.
type WhoAmI struct {
	Session     string `json:"session"`
	Agent       Agent  `json:"agent"`
	BaseURL     string `json:"base_url"`
	Active      bool   `json:"active"`
	TokenStored bool   `json:"token_stored"`
	Remote      Home   `json:"remote,omitempty"`
	Message     string `json:"message,omitempty"`
}

// WhoAmI asks identity who this token is, through the active session.
func (s *Store) WhoAmI(ctx context.Context, agentUsername, baseURL string) (*WhoAmI, error) {
	cfg, err := LoadConfig(s.Dir)
	if err != nil {
		return nil, err
	}
	sess, err := cfg.Select(agentUsername, baseURL)
	if err != nil {
		return nil, fmt.Errorf("caf agent whoami: %w", err)
	}
	token, err := s.token(sess)
	if err != nil {
		return nil, fmt.Errorf("caf agent whoami: %w", err)
	}
	home, err := s.endpoint(sess.BaseURL, token).Home(ctx)
	if err != nil {
		return nil, err
	}
	agent := Agent{Username: sess.AgentUsername, ID: sess.AgentID, Status: sess.Status}
	// The remote answer wins on status: a local "unclaimed" cache that has gone
	// stale is worse than no cache, and this is the one call where the server's
	// own word is free.
	if status, ok := (*home)["status"].(string); ok && status != "" {
		agent.Status = status
	}
	return &WhoAmI{
		Session:     sess.Name,
		Agent:       agent,
		BaseURL:     sess.BaseURL,
		Active:      sess.Active(cfg.ActiveAgentSession),
		TokenStored: true,
		Remote:      *home,
		Message:     "session " + sess.Name + " is authenticated against " + sess.BaseURL,
	}, nil
}

// --- the token verbs ------------------------------------------------------

// TokenDoc is what every token verb prints. It carries the keys' metadata and
// the fact that a token is stored, and it carries no token: the four verbs each
// handle a credential and each of them answers with this same document shape.
type TokenDoc struct {
	Action  string `json:"action"`
	Session string `json:"session"`
	Agent   Agent  `json:"agent"`
	BaseURL string `json:"base_url"`
	// Keys is the token metadata the verb produced or selected: the list for
	// show, one entry for create and rotate. It never carries a token's value.
	Keys        []KeyInfo `json:"keys,omitempty"`
	TokenStored bool      `json:"token_stored"`
	Message     string    `json:"message"`
}

// defaultTokenName is the name `token create` mints when the caller did not
// choose one. Identity requires a name; this is the CLI's own.
const defaultTokenName = "cli-issued"

// TokenShow reads the agent's tokens off identity. With no name it lists them
// all — the array identity returns, newest first, revoked included. With a
// name it selects the one LIVE token with that name: identity's
// one_live_name_per_agent partial index means a name identifies a live token,
// so the selection is unambiguous and a revoked lookalike is never picked.
func (s *Store) TokenShow(ctx context.Context, agentUsername, baseURL, name string) (*TokenDoc, error) {
	sess, token, err := s.sessionToken(agentUsername, baseURL)
	if err != nil {
		return nil, err
	}
	keys, err := s.endpoint(sess.BaseURL, token).Tokens(ctx)
	if err != nil {
		return nil, err
	}
	if name != "" {
		selected := []KeyInfo{}
		for _, k := range keys {
			if k.Name == name && k.RevokedAt == nil {
				selected = append(selected, k)
				break
			}
		}
		if len(selected) == 0 {
			return nil, fmt.Errorf("caf agent token show: no live token named %q for session %s; `caf agent token show` lists them", name, sess.Name)
		}
		keys = selected
	}
	return &TokenDoc{
		Action: "shown", Session: sess.Name, Agent: agentOf(sess), BaseURL: sess.BaseURL,
		Keys: keys, TokenStored: true,
		Message: "the tokens for session " + sess.Name + " are listed; their values are not printed",
	}, nil
}

// TokenCreate mints a fresh named token and stores it, replacing whatever the
// ring held for this session. An empty name is the CLI's own default.
func (s *Store) TokenCreate(ctx context.Context, agentUsername, baseURL, name string) (*TokenDoc, error) {
	sess, token, err := s.sessionToken(agentUsername, baseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		name = defaultTokenName
	}
	fresh, err := s.endpoint(sess.BaseURL, token).CreateToken(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := s.Ring.Set(KeyringService, ringAccount(sess.Name), fresh.Token); err != nil {
		return nil, fmt.Errorf("caf agent token create: the new token was minted but could not be stored: %w", err)
	}
	// The session's token is now the fresh one: the keyring holds it, so the
	// id that names it moves with it.
	if err := s.saveSessionKeyID(sess, fresh.ID); err != nil {
		return nil, err
	}
	return &TokenDoc{
		Action: "created", Session: sess.Name, Agent: agentOf(sess), BaseURL: sess.BaseURL,
		Keys: []KeyInfo{fresh.KeyInfo}, TokenStored: true,
		Message: "a new token was minted and stored in the keyring; the previous one is now the one to forget",
	}, nil
}

// TokenRotate replaces one token with a fresh one of the same name. With no id
// it rotates the session's own token — the one in the keyring — and stores the
// replacement, because identity has invalidated the old one and a session whose
// new token was not stored is a session that cannot authenticate. With an id it
// rotates that token; the keyring follows only when the id is the session's
// own, so rotating some other token never overwrites the credential this
// session authenticates with.
func (s *Store) TokenRotate(ctx context.Context, agentUsername, baseURL, tokenID string) (*TokenDoc, error) {
	sess, token, err := s.sessionToken(agentUsername, baseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(tokenID) == "" {
		tokenID = sess.AgentKeyID
	}
	if strings.TrimSpace(tokenID) == "" {
		return nil, fmt.Errorf("caf agent token rotate: pass a token id (`caf agent token show` lists them)")
	}
	fresh, err := s.endpoint(sess.BaseURL, token).RotateToken(ctx, tokenID)
	if err != nil {
		return nil, err
	}
	message := "the token was rotated; the old one no longer works"
	if tokenID == sess.AgentKeyID {
		if err := s.Ring.Set(KeyringService, ringAccount(sess.Name), fresh.Token); err != nil {
			return nil, fmt.Errorf("caf agent token rotate: the new token was minted but could not be stored: %w", err)
		}
		if err := s.saveSessionKeyID(sess, fresh.ID); err != nil {
			return nil, err
		}
		message = "the token was rotated and the new one is in the keyring; the old one no longer works"
	}
	return &TokenDoc{
		Action: "rotated", Session: sess.Name, Agent: agentOf(sess), BaseURL: sess.BaseURL,
		Keys: []KeyInfo{fresh.KeyInfo}, TokenStored: true,
		Message: message,
	}, nil
}

// TokenRevoke destroys one token. With no id it revokes the session's own and
// removes it from the keyring — the credential is dead server-side, and a
// keyring entry for a dead credential is a stale secret. With an id it revokes
// that token; the keyring is touched only when the id is the session's own.
func (s *Store) TokenRevoke(ctx context.Context, agentUsername, baseURL, tokenID string) (*TokenDoc, error) {
	sess, token, err := s.sessionToken(agentUsername, baseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(tokenID) == "" {
		tokenID = sess.AgentKeyID
	}
	if strings.TrimSpace(tokenID) == "" {
		return nil, fmt.Errorf("caf agent token revoke: pass a token id (`caf agent token show` lists them)")
	}
	if err := s.endpoint(sess.BaseURL, token).RevokeToken(ctx, tokenID); err != nil {
		return nil, err
	}
	stored := true
	message := "the token was revoked on identity; it no longer authenticates"
	if tokenID == sess.AgentKeyID {
		if err := s.Ring.Delete(KeyringService, ringAccount(sess.Name)); err != nil {
			return nil, fmt.Errorf("caf agent token revoke: the token was revoked on identity but is still in the keyring: %w", err)
		}
		if err := s.saveSessionKeyID(sess, ""); err != nil {
			return nil, err
		}
		stored = false
		message = "the token was revoked and removed from the keyring; this session can no longer authenticate"
	}
	return &TokenDoc{
		Action: "revoked", Session: sess.Name, Agent: agentOf(sess), BaseURL: sess.BaseURL,
		TokenStored: stored,
		Message:     message,
	}, nil
}

// saveSessionKeyID records the id of the token the keyring now holds for one
// session. The config is a cache of remote facts, and this is the one that lets
// a later bare rotate or revoke find the session's own token: the keyring holds
// the token's value, which identity's list does not echo.
func (s *Store) saveSessionKeyID(sess Session, keyID string) error {
	cfg, err := LoadConfig(s.Dir)
	if err != nil {
		return err
	}
	updated := cfg.AgentSessions[sess.Name]
	updated.AgentKeyID = keyID
	cfg.AgentSessions[sess.Name] = updated
	return cfg.Save(s.Dir)
}

// --- the two shared steps -------------------------------------------------

// sessionToken resolves a session and reads its token out of the ring. Both in
// one call because every authenticated verb needs both and a caller that did
// them separately would have a window with a token in a local variable.
func (s *Store) sessionToken(agentUsername, baseURL string) (Session, string, error) {
	cfg, err := LoadConfig(s.Dir)
	if err != nil {
		return Session{}, "", err
	}
	sess, err := cfg.Select(agentUsername, baseURL)
	if err != nil {
		return Session{}, "", fmt.Errorf("caf agent: %w", err)
	}
	token, err := s.token(sess)
	if err != nil {
		return Session{}, "", fmt.Errorf("caf agent: %w", err)
	}
	return sess, token, nil
}

// token reads one session's token. It is never returned to a document: every
// caller passes it straight into an endpoint that drops it when the call ends.
func (s *Store) token(sess Session) (string, error) {
	if s.Ring == nil {
		return "", fmt.Errorf("%w: no keyring is configured", ErrNoToken)
	}
	stored, err := s.Ring.Get(KeyringService, ringAccount(sess.Name))
	if err != nil {
		// The cause is wrapped, not the value: an absent entry, an unreadable
		// store and a refused keychain all mean the same thing to the caller,
		// and the reason is printed so an operator can tell them apart without
		// this package growing three sentinels.
		return "", fmt.Errorf("%w (%v)", ErrNoToken, err)
	}
	if strings.TrimSpace(stored) == "" {
		return "", ErrNoToken
	}
	return stored, nil
}

func agentOf(sess Session) Agent {
	return Agent{Username: sess.AgentUsername, ID: sess.AgentID, Status: sess.Status}
}
