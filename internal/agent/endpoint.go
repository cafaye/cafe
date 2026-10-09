package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// THE ROUTES, IN ONE PLACE, AND WHY THEY ARE SPELLED OUT.
//
// W1-P2 owns the identity side of this wave and had not landed when this packet
// was written, so these paths are the shapes moon/AGENTS-FIRST.md documents
// (`POST /v1/agents`, a claim-link refresh, and the scoped-token
// create/show/rotate/revoke set) expressed in identity's own `/v1/...`
// convention. They are one table rather than six string literals because the
// day W1-P2 lands with different paths, the fix is six lines in one file and a
// grep for a route literal finds this table and nothing else.
const (
	routeRegister  = "POST /v1/agents"
	routeHome      = "GET /v1/agents/home"
	routeKey       = "GET /v1/agents/key"
	routeCreateKey = "POST /v1/agents/key"
	routeRotateKey = "POST /v1/agents/key/rotate"
	routeRevokeKey = "POST /v1/agents/key/revoke"
)

// route is one entry of the table above. It carries its own method and path so
// a caller cannot send a GET to a POST route by mistyping one string.
type route string

func (r route) String() string { return string(r) }

func (r route) method() string { return strings.Fields(string(r))[0] }

func (r route) path() string { return strings.Fields(string(r))[1] }

// Agent is the principal identity says this session is. Every field is a
// non-secret.
type Agent struct {
	Username string `json:"username"`
	Ref      string `json:"ref,omitempty"`
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
	Agent    Agent
	Token    string
	ClaimURL string
}

// KeyInfo is the non-secret half of an agent token: what it is called, the
// characters that are safe to show, what it may do and when. There is
// deliberately no field that could hold the token — `token show` answers with
// this and nothing else.
type KeyInfo struct {
	Name       string   `json:"name,omitempty"`
	Prefix     string   `json:"prefix,omitempty"`
	Scopes     []string `json:"scopes,omitempty"`
	CreatedAt  string   `json:"created_at,omitempty"`
	LastUsedAt string   `json:"last_used_at,omitempty"`
}

// NewKey is what create and rotate answer: the metadata, plus the token for the
// length of one call. The token is moved to the ring and dropped; the metadata
// is what reaches a document.
type NewKey struct {
	KeyInfo
	Token string
}

// Home is identity's own answer about who a token is. It is passed through
// rather than decoded into a struct because the shape belongs to identity and
// W1-P2 owns it — a struct transcribed from a route that has not landed is a
// struct that will be wrong in a way nothing here could detect.
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
	// Key answers the current token's metadata.
	Key(ctx context.Context) (*KeyInfo, error)
	// CreateKey mints a fresh token server-side and returns it.
	CreateKey(ctx context.Context) (*NewKey, error)
	// RotateKey replaces the current token with a fresh one.
	RotateKey(ctx context.Context) (*NewKey, error)
	// RevokeKey destroys the current token.
	RevokeKey(ctx context.Context) error
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
	resp, err := e.do(ctx, routeRegister, body, false)
	if err != nil {
		return nil, err
	}
	// The response shape is the one the prior art established and W1-P2
	// documents: an agent, an api_key carrying the token, and a claim carrying
	// the URL a human opens.
	var payload struct {
		Agent struct {
			Username string `json:"username"`
			Ref      string `json:"ref"`
			Status   string `json:"status"`
		} `json:"agent"`
		APIKey struct {
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
		return nil, fmt.Errorf("caf agent register: the answer carried no api_key.token; identity is not answering the documented shape")
	}
	return &RegisterResult{
		Agent:    Agent{Username: payload.Agent.Username, Ref: payload.Agent.Ref, Status: payload.Agent.Status},
		Token:    payload.APIKey.Token,
		ClaimURL: payload.Claim.URL,
	}, nil
}

func (e *HTTPEndpoint) Home(ctx context.Context) (*Home, error) {
	resp, err := e.do(ctx, routeHome, nil, true)
	if err != nil {
		return nil, err
	}
	var home Home
	if err := decode(resp.Body, &home); err != nil {
		return nil, fmt.Errorf("caf agent whoami: the answer was not a JSON document: %w", err)
	}
	return &home, nil
}

func (e *HTTPEndpoint) Key(ctx context.Context) (*KeyInfo, error) {
	resp, err := e.do(ctx, routeKey, nil, true)
	if err != nil {
		return nil, err
	}
	var key KeyInfo
	if err := decode(resp.Body, &key); err != nil {
		return nil, fmt.Errorf("caf agent token show: the answer was not the document this CLI expects: %w", err)
	}
	return &key, nil
}

func (e *HTTPEndpoint) CreateKey(ctx context.Context) (*NewKey, error) {
	resp, err := e.do(ctx, routeCreateKey, map[string]any{"name": "cli-issued"}, true)
	if err != nil {
		return nil, err
	}
	return decodeNewKey("caf agent token create", resp.Body)
}

func (e *HTTPEndpoint) RotateKey(ctx context.Context) (*NewKey, error) {
	resp, err := e.do(ctx, routeRotateKey, map[string]any{}, true)
	if err != nil {
		return nil, err
	}
	return decodeNewKey("caf agent token rotate", resp.Body)
}

func (e *HTTPEndpoint) RevokeKey(ctx context.Context) error {
	_, err := e.do(ctx, routeRevokeKey, map[string]any{}, true)
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

func (e *HTTPEndpoint) do(ctx context.Context, r route, payload any, auth bool) (*response, error) {
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
	req, err := http.NewRequestWithContext(ctx, r.method(), strings.TrimRight(e.BaseURL, "/")+r.path(), body)
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
	case routeKey:
		return "token show"
	case routeCreateKey:
		return "token create"
	case routeRotateKey:
		return "token rotate"
	case routeRevokeKey:
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
