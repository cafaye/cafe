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
		AgentRef:      result.Agent.Ref,
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
		Agent:         Agent{Username: sess.AgentUsername, Ref: sess.AgentRef, Status: sess.Status},
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
	agent := Agent{Username: sess.AgentUsername, Ref: sess.AgentRef, Status: sess.Status}
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

// TokenDoc is what every token verb prints. It carries the key's metadata and
// the fact that a token is stored, and it carries no token: the four verbs each
// handle a credential and each of them answers with this same document shape.
type TokenDoc struct {
	Action      string   `json:"action"`
	Session     string   `json:"session"`
	Agent       Agent    `json:"agent"`
	BaseURL     string   `json:"base_url"`
	Key         *KeyInfo `json:"key,omitempty"`
	TokenStored bool     `json:"token_stored"`
	Message     string   `json:"message"`
}

// TokenShow reads the current token's metadata off identity.
func (s *Store) TokenShow(ctx context.Context, agentUsername, baseURL string) (*TokenDoc, error) {
	sess, token, err := s.sessionToken(agentUsername, baseURL)
	if err != nil {
		return nil, err
	}
	key, err := s.endpoint(sess.BaseURL, token).Key(ctx)
	if err != nil {
		return nil, err
	}
	return &TokenDoc{
		Action: "shown", Session: sess.Name, Agent: agentOf(sess), BaseURL: sess.BaseURL,
		Key: key, TokenStored: true,
		Message: "the token for session " + sess.Name + " is in the keyring; its value is not printed",
	}, nil
}

// TokenCreate mints a fresh token and stores it, replacing whatever the ring
// held for this session.
func (s *Store) TokenCreate(ctx context.Context, agentUsername, baseURL string) (*TokenDoc, error) {
	sess, token, err := s.sessionToken(agentUsername, baseURL)
	if err != nil {
		return nil, err
	}
	fresh, err := s.endpoint(sess.BaseURL, token).CreateKey(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Ring.Set(KeyringService, ringAccount(sess.Name), fresh.Token); err != nil {
		return nil, fmt.Errorf("caf agent token create: the new token was minted but could not be stored: %w", err)
	}
	return &TokenDoc{
		Action: "created", Session: sess.Name, Agent: agentOf(sess), BaseURL: sess.BaseURL,
		Key: &fresh.KeyInfo, TokenStored: true,
		Message: "a new token was minted and stored in the keyring; the previous one is now the one to forget",
	}, nil
}

// TokenRotate replaces the token and stores the new one. Identity invalidates
// the old one, so the store write is the second half of one operation: a rotate
// whose new token could not be stored leaves the session unusable, and the
// error says so plainly rather than reporting success.
func (s *Store) TokenRotate(ctx context.Context, agentUsername, baseURL string) (*TokenDoc, error) {
	sess, token, err := s.sessionToken(agentUsername, baseURL)
	if err != nil {
		return nil, err
	}
	fresh, err := s.endpoint(sess.BaseURL, token).RotateKey(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Ring.Set(KeyringService, ringAccount(sess.Name), fresh.Token); err != nil {
		return nil, fmt.Errorf("caf agent token rotate: the new token was minted but could not be stored: %w", err)
	}
	return &TokenDoc{
		Action: "rotated", Session: sess.Name, Agent: agentOf(sess), BaseURL: sess.BaseURL,
		Key: &fresh.KeyInfo, TokenStored: true,
		Message: "the token was rotated and the new one is in the keyring; the old one no longer works",
	}, nil
}

// TokenRevoke destroys the token and then removes it from the ring.
//
// The ring entry is deleted only after identity has confirmed the revocation,
// and a revocation that is confirmed but whose local delete failed is reported
// as a failure: the credential is dead server-side either way, and the only
// thing at stake locally is a stale secret nobody can use.
func (s *Store) TokenRevoke(ctx context.Context, agentUsername, baseURL string) (*TokenDoc, error) {
	sess, token, err := s.sessionToken(agentUsername, baseURL)
	if err != nil {
		return nil, err
	}
	if err := s.endpoint(sess.BaseURL, token).RevokeKey(ctx); err != nil {
		return nil, err
	}
	if err := s.Ring.Delete(KeyringService, ringAccount(sess.Name)); err != nil {
		return nil, fmt.Errorf("caf agent token revoke: the token was revoked on identity but is still in the keyring: %w", err)
	}
	return &TokenDoc{
		Action: "revoked", Session: sess.Name, Agent: agentOf(sess), BaseURL: sess.BaseURL,
		TokenStored: false,
		Message:     "the token was revoked and removed from the keyring; this session can no longer authenticate",
	}, nil
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
	return Agent{Username: sess.AgentUsername, Ref: sess.AgentRef, Status: sess.Status}
}
