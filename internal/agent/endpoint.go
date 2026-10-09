package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// THE ROUTES, IN ONE PLACE, AND WHY THEY ARE SPELLED OUT.
//
// These are the routes identity actually mounts, read off identity's master
// (internal/httpapi/agents.go, registerAgentPublicRoutes) and verified against
// its own response shapes — NOT the shapes moon/AGENTS-FIRST.md documented
// before W1-P2 landed, which is how five of the six calls in the previous
// version of this table came to 404 against a real server. The one exception
// is the home route: REPAIR-C (identity adding GET /v1/agents/home) has not
// merged, so that row is the route its brief names, and the client passes its
// answer through rather than decoding a shape that does not exist yet.
//
// They are one table rather than six string literals because the day a route
// moves, the fix is six lines in one file and a grep for a route literal finds
// this table and nothing else.
const (
	routeRegister    = "POST /v1/agents"
	routeHome        = "GET /v1/agents/home"
	routeListTokens  = "GET /v1/agents/tokens"
	routeCreateToken = "POST /v1/agents/tokens"
	routeRotateToken = "POST /v1/agents/tokens/{tokenID}/rotate"
	routeRevokeToken = "DELETE /v1/agents/tokens/{tokenID}"
)

// route is one entry of the table above. It carries its own method and path so
// a caller cannot send a GET to a POST route by mistyping one string.
type route string

func (r route) String() string { return string(r) }

func (r route) method() string { return strings.Fields(string(r))[0] }

// path is the route's path with the {tokenID} placeholder filled from id. A
// route without the placeholder ignores id entirely, so the placeholder can
// only survive into a URL when a caller sends one to a parameterized route —
// and the store refuses that case before the call is built.
func (r route) path(id string) string {
	return strings.ReplaceAll(strings.Fields(string(r))[1], "{tokenID}", url.PathEscape(id))
}

// Agent is the principal identity says this session is. Every field is a
// non-secret, and every field is one identity's agentSummary actually carries
// (id, name, username, status) — the previous version read a `ref` that
// identity has never had, so it was silently always empty.
type Agent struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	// Status is the lifecycle word: unclaimed, claimed or disabled.
	Status string `json:"status"`
}

// RegisterInput is the body of `POST /v1/agents`. A username the caller did not
// choose is generated (see autoUsername) and retried on conflict.
type RegisterInput struct {
	Name     string
	Username string
}

// RegisterResult is what identity answers a registration with.
//
// The token has no JSON tag and no accessor that returns it to a document: it
// exists to be moved into the keyring in the same method that received it, and
// it never reaches a struct that anything marshals.
type RegisterResult struct {
	Agent Agent
	// Token is the bootstrap credential, for the length of one call.
	Token string
	// KeyID is the bootstrap token's identity id. The keyring holds the token's
	// value, which identity's token list does not echo, so this id is the only
	// handle the two share — it is what lets a bare `token rotate` or
	// `token revoke` find the session's own token.
	KeyID    string
	ClaimURL string
}

// KeyInfo is the non-secret half of an agent token: its id, what it is called
// and its dates. The shape is identity's agentTokenResponse, which the mint and
// rotate answers carry minus the token itself. There is deliberately no field
// that could hold the token — `token show` answers with this and nothing else.
//
// The three pointer fields are identity's "never" states: a token that has
// never been used has no last_used_at, and a live one has neither revoked_at
// nor revoke_reason. The pointers let omitempty tell those apart from a present
// instant, the same reason identity's own type uses them.
type KeyInfo struct {
	ID           string  `json:"id"`
	Name         string  `json:"name,omitempty"`
	ExpiresAt    string  `json:"expires_at,omitempty"`
	CreatedAt    string  `json:"created_at,omitempty"`
	LastUsedAt   *string `json:"last_used_at,omitempty"`
	RevokedAt    *string `json:"revoked_at,omitempty"`
	RevokeReason *string `json:"revoke_reason,omitempty"`
}

// NewKey is what create and rotate answer: the metadata, plus the token for the
// length of one call. The token is moved to the ring and dropped; the metadata
// is what reaches a document.
type NewKey struct {
	KeyInfo
	Token string
}

// Home is identity's own answer about who a token is. It is passed through
// rather than decoded into a struct because the shape belongs to identity:
// GET /v1/agents/home is REPAIR-C's route and has not landed, and a struct
// transcribed from a route that does not exist is a struct that will be wrong
// in a way nothing here could detect. The one fact read out of it is status,
// which REPAIR-C's brief guarantees the answer carries.
type Home map[string]any

// Endpoint is the HTTP surface of identity this CLI talks to, and it is an
// interface so a test can answer it without a network.
//
// Six methods rather than one generic `Do` because the generic form moves the
// route table, the auth rule and each verb's own semantics into the caller, and
// the caller is five commands. One method per route means a route that changes
// changes one place.
type Endpoint interface {
	// Register is the one UNAUTHENTICATED call: an agent that has no token yet
	// asks for one.
	Register(ctx context.Context, in RegisterInput) (*RegisterResult, error)
	// Home answers who this token is, and is what `caf agent whoami` reads.
	Home(ctx context.Context) (*Home, error)
	// Tokens lists the agent's credentials, newest first, revoked included.
	// It is the mounted shape of the token surface: an array, not one key.
	Tokens(ctx context.Context) ([]KeyInfo, error)
	// CreateToken mints a named token server-side and returns it.
	CreateToken(ctx context.Context, name string) (*NewKey, error)
	// RotateToken replaces the token id names with a fresh one of the same name.
	RotateToken(ctx context.Context, tokenID string) (*NewKey, error)
	// RevokeToken destroys the token id names.
	RevokeToken(ctx context.Context, tokenID string) error
}

// Dial opens one endpoint for one base URL and one token. It is a function
// rather than a field on Store because the token is read out of the ring at the
// moment of the call and must be dropped when the call returns: a Store that
// held an authenticated client would be a Store that held a token for as long
// as it lived, which is exactly what invariant 1 forbids.
//
// An empty token means "the public routes only", which is what a registration
// needs.
type Dial func(baseURL, token string) Endpoint

// DefaultDial is the real Dial: one HTTP client per call, built from the origin
// and the token, and nothing retained.
func DefaultDial() Dial {
	return func(baseURL, token string) Endpoint {
		return &HTTPEndpoint{BaseURL: baseURL, Token: token}
	}
}

// HTTPEndpoint is the real Endpoint: one request against identity per method,
// with the token as the bearer and nothing else carrying it.
type HTTPEndpoint struct {
	// BaseURL is identity's origin, e.g. https://cafaye.com. Empty is refused
	// rather than defaulted: a client with no endpoint is a client that would
	// POST to a relative path and get a confusing answer.
	BaseURL string
	// Token is the bearer for the authenticated routes. Empty is refused
	// before the request is built rather than sent as an empty bearer.
	Token string
	// Client is the HTTP client. Nil means one with a timeout, because a
	// register that hangs forever on a laptop nobody is watching is the failure
	// this repository refuses to ship.
	Client *http.Client
	// Timeout bounds one call. Default 30s: a human is waiting and the claim
	// link lives minutes.
	Timeout time.Duration
	// IDFn mints the idempotency key for a mutating call. It is a field so a
	// test can assert one was sent without asserting what it said.
	IDFn func() string
}

// DefaultEndpointTimeout is what a nil Timeout means.
const DefaultEndpointTimeout = 30 * time.Second

func (e *HTTPEndpoint) client() *http.Client {
	if e.Client != nil {
		return e.Client
	}
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = DefaultEndpointTimeout
	}
	return &http.Client{Timeout: timeout}
}

// --- the six routes -------------------------------------------------------

func (e *HTTPEndpoint) Register(ctx context.Context, in RegisterInput) (*RegisterResult, error) {
	body := map[string]string{}
	if strings.TrimSpace(in.Name) != "" {
		body["name"] = in.Name
	}
	if strings.TrimSpace(in.Username) != "" {
		body["username"] = in.Username
	}
	resp, err := e.do(ctx, routeRegister, body, false, "")
	if err != nil {
		return nil, err
	}
	// The response shape is identity's registerAgentResponse: an agentSummary
	// (id, name, username, status), the issued bootstrap token (id, name,
	// expires_at, token) and the claim link. The nesting is borrowed from the
	// prior art because a claim screen is written against it already.
	var payload struct {
		Agent struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Status   string `json:"status"`
		} `json:"agent"`
		APIKey struct {
			ID    string `json:"id"`
			Token string `json:"token"`
		} `json:"api_key"`
		Claim struct {
			URL string `json:"url"`
		} `json:"claim"`
	}
	if err := decode(resp.Body, &payload); err != nil {
		return nil, fmt.Errorf("caf agent register: the answer was not the document this CLI expects: %w", err)
	}
	if strings.TrimSpace(payload.APIKey.Token) == "" {
		return nil, fmt.Errorf("caf agent register: the answer carried no api_key.token; identity is not answering the mounted shape")
	}
	return &RegisterResult{
		Agent: Agent{
			ID:       payload.Agent.ID,
			Username: payload.Agent.Username,
			Status:   payload.Agent.Status,
		},
		Token:    payload.APIKey.Token,
		KeyID:    payload.APIKey.ID,
		ClaimURL: payload.Claim.URL,
	}, nil
}

func (e *HTTPEndpoint) Home(ctx context.Context) (*Home, error) {
	resp, err := e.do(ctx, routeHome, nil, true, "")
	if err != nil {
		return nil, err
	}
	var home Home
	if err := decode(resp.Body, &home); err != nil {
		return nil, fmt.Errorf("caf agent whoami: the answer was not a JSON document: %w", err)
	}
	return &home, nil
}

func (e *HTTPEndpoint) Tokens(ctx context.Context) ([]KeyInfo, error) {
	resp, err := e.do(ctx, routeListTokens, nil, true, "")
	if err != nil {
		return nil, err
	}
	var keys []KeyInfo
	if err := decode(resp.Body, &keys); err != nil {
		return nil, fmt.Errorf("caf agent token show: the answer was not the document this CLI expects: %w", err)
	}
	return keys, nil
}

func (e *HTTPEndpoint) CreateToken(ctx context.Context, name string) (*NewKey, error) {
	resp, err := e.do(ctx, routeCreateToken, map[string]any{"name": name}, true, "")
	if err != nil {
		return nil, err
	}
	return decodeNewKey("caf agent token create", resp.Body)
}

func (e *HTTPEndpoint) RotateToken(ctx context.Context, tokenID string) (*NewKey, error) {
	// The body is {} rather than none: identity's rotate handler decodes
	// unconditionally and answers 400 to an empty body, so the empty object is
	// the smallest body that means "no expires_in".
	resp, err := e.do(ctx, routeRotateToken, map[string]any{}, true, tokenID)
	if err != nil {
		return nil, err
	}
	return decodeNewKey("caf agent token rotate", resp.Body)
}

func (e *HTTPEndpoint) RevokeToken(ctx context.Context, tokenID string) error {
	// No body: identity's revoke reads one only when ContentLength is non-zero,
	// and this CLI revokes without a reason.
	_, err := e.do(ctx, routeRevokeToken, nil, true, tokenID)
	return err
}

// decodeNewKey reads a create or rotate answer. It decodes the body twice, once
// for the token and once for the metadata, because the two are different
// lifetimes: the token is dropped at the end of this function and the metadata
// is printed. Merging them into one struct with an inline tag is not possible
// in encoding/json, and a struct that duplicated every KeyInfo field by hand
// would be a second copy of it that could drift.
func decodeNewKey(op string, body []byte) (*NewKey, error) {
	var envelope struct {
		Token string `json:"token"`
	}
	if err := decode(body, &envelope); err != nil {
		return nil, fmt.Errorf("%s: the answer was not the document this CLI expects: %w", op, err)
	}
	if strings.TrimSpace(envelope.Token) == "" {
		return nil, fmt.Errorf("%s: the answer carried no token; identity did not mint one, so nothing was stored", op)
	}
	var info KeyInfo
	if err := decode(body, &info); err != nil {
		return nil, fmt.Errorf("%s: the answer's key metadata was not readable: %w", op, err)
	}
	return &NewKey{KeyInfo: info, Token: envelope.Token}, nil
}

// --- the one transport ----------------------------------------------------

type response struct {
	StatusCode int
	Body       []byte
}

func (e *HTTPEndpoint) do(ctx context.Context, r route, payload any, auth bool, pathID string) (*response, error) {
	if strings.TrimSpace(e.BaseURL) == "" {
		return nil, ErrNoBaseURL
	}
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode the request: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, r.method(), strings.TrimRight(e.BaseURL, "/")+r.path(pathID), body)
	if err != nil {
		return nil, fmt.Errorf("build the request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		if strings.TrimSpace(e.Token) == "" {
			// Refused here rather than sent: an empty bearer is a request a
			// server answers 401 for a token that never existed, and the log
			// it writes is a log about nothing.
			return nil, fmt.Errorf("caf agent: this call needs a token and the keyring returned none; run `caf agent register`")
		}
		req.Header.Set("Authorization", "Bearer "+e.Token)
	}
	if r.method() != "GET" {
		key := "caf-agent-" + randomSuffix(12)
		if e.IDFn != nil {
			key = e.IDFn()
		}
		req.Header.Set("Idempotency-Key", key)
	}

	resp, err := e.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("call identity: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read identity's answer: %w", err)
	}
	out := &response{StatusCode: resp.StatusCode, Body: raw}
	if resp.StatusCode >= 300 {
		return nil, statusError(r, out.StatusCode, raw)
	}
	return out, nil
}

// StatusError is one non-2xx answer, summarized. It carries the status and the
// error code identity named, and it answers its own Hint: the 403 an unclaimed
// agent gets is not a generic failure, it is a specific instruction, and a
// client that reported "403 Forbidden" for it would be sending the reader to a
// server log that says nothing useful.
type StatusError struct {
	Op     string
	Route  string
	Status int
	Code   string
	Body   string
}

func (e *StatusError) Error() string {
	switch {
	case e.Code != "":
		return fmt.Sprintf("%s failed: status=%d error=%s", e.Op, e.Status, e.Code)
	case e.Body != "":
		return fmt.Sprintf("%s failed: status=%d body=%s", e.Op, e.Status, e.Body)
	}
	return fmt.Sprintf("%s failed: status=%d", e.Op, e.Status)
}

// Hint is the actionable half. It is empty for a status that has no better
// instruction than its own message, and a caller that prints an empty hint
// prints nothing.
func (e *StatusError) Hint() string {
	switch {
	case e.Status == http.StatusForbidden && strings.Contains(strings.ToLower(e.Code+e.Body), "agent_unclaimed"):
		return "this agent is unclaimed: a human owner must open the claim URL `caf agent register` printed and approve it; until then identity answers every authenticated call with 403 agent_unclaimed"
	case e.Status == http.StatusUnauthorized:
		return "the token was not accepted; it may have been rotated or revoked — run `caf agent token rotate` or `caf agent register`"
	case e.Status == http.StatusTooManyRequests:
		return "identity is rate limiting this machine; wait a minute and try again"
	case e.Status >= 500:
		return "identity itself failed; this is not something the agent can fix — retry, and if it persists it is a server-side fault"
	}
	return ""
}

// statusError turns one non-2xx answer into a StatusError, reading the code out
// of the body rather than the status line because identity names its errors in
// the body (`{"error":"agent_unclaimed"}`) and a client that only read the
// status line could not produce the hint above.
func statusError(r route, status int, body []byte) *StatusError {
	raw := strings.TrimSpace(string(body))
	code := ""
	var parsed map[string]any
	if err := decode(body, &parsed); err == nil {
		for _, key := range []string{"error", "code"} {
			if s, ok := parsed[key].(string); ok && strings.TrimSpace(s) != "" {
				code = strings.TrimSpace(s)
				break
			}
		}
	}
	return &StatusError{
		Op:     "caf agent " + verbFor(r),
		Route:  r.String(),
		Status: status,
		Code:   code,
		Body:   truncate(raw),
	}
}

// verbFor names the route in an error the way the person typed it.
func verbFor(r route) string {
	switch r {
	case routeRegister:
		return "register"
	case routeHome:
		return "whoami"
	case routeListTokens:
		return "token show"
	case routeCreateToken:
		return "token create"
	case routeRotateToken:
		return "token rotate"
	case routeRevokeToken:
		return "token revoke"
	}
	return r.String()
}

// truncate bounds a body so a wall of HTML from a proxy in front of identity is
// not pasted into a terminal.
func truncate(s string) string {
	const max = 240
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max]) + "..."
}

func decode(body []byte, v any) error {
	return json.Unmarshal(body, v)
}

// errIsStatus reports whether an error is one particular HTTP status. It exists
// so a caller can branch on a status without importing net/http to do it — the
// register retry below is the caller that needs it.
func errIsStatus(err error, status int) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status == status
}

// isUsernameTaken is the one registration failure worth retrying: identity
// refuses a username that is already taken, and the username in question was
// generated by THIS client moments ago, so the remedy is a different generated
// username rather than a message to a person who never chose one.
func isUsernameTaken(err error) bool {
	return errIsStatus(err, http.StatusConflict) || errIsStatus(err, http.StatusUnprocessableEntity)
}
