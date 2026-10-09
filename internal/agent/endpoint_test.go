package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// THE ROUTE TABLE, PROVEN AGAINST A SERVER.
//
// Each test below drives one HTTPEndpoint method against a fake identity and
// asserts the exact method, path and body it sent. These are the calls that
// 404'd (or decoded the wrong shape) when the table was transcribed from the
// plan doc before identity's W1-P2 landed; the assertions are the fix's proof,
// and the fake's response shapes are identity's own, transcribed from
// internal/httpapi/agents.go on master with the source line beside each.

// recordedCall is one call the client made against the fake identity: the
// method, the path, the body, the bearer's LENGTH and the idempotency key.
// The bearer's length is recorded rather than its value for the same reason the
// CLI's own tests record it: the assertion needs "a token was presented",
// never which token it was.
type recordedCall struct {
	Method string
	Path   string
	Body   string
	Auth   string
	IDKey  string
}

// authLen reports the length of a bearer header, never its value.
func authLen(s string) string { return strings.Repeat("*", len(s)) }

// fakeIdentityServer is identity as mounted on master, minus the database.
// It answers with identity's own response shapes and records what it was
// asked. refuseStatus, when non-zero, makes every route answer with that
// status and refuseBody instead — the refusal cases.
type fakeIdentityServer struct {
	calls []recordedCall

	refuseStatus int
	refuseBody   string
}

func newFakeIdentityServer(t *testing.T) (*fakeIdentityServer, *httptest.Server) {
	t.Helper()
	fake := &fakeIdentityServer{}
	srv := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(srv.Close)
	return fake, srv
}

func (f *fakeIdentityServer) serve(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read the body", http.StatusBadRequest)
		return
	}
	f.calls = append(f.calls, recordedCall{
		Method: r.Method,
		Path:   r.URL.Path,
		Body:   string(raw),
		Auth:   authLen(r.Header.Get("Authorization")),
		IDKey:  r.Header.Get("Idempotency-Key"),
	})

	status := http.StatusOK
	var body string
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/agents":
		// identity's registerAgentResponse (agents.go:592): the agentSummary,
		// the issued bootstrap token and the claim link.
		status = http.StatusCreated
		body = `{"agent":{"id":"agent_01HZX","name":"Noel","username":"noel","status":"unclaimed"},` +
			`"api_key":{"id":"tok_01BOOT","name":"bootstrap","expires_at":"2026-11-09T12:00:00Z","token":"caf_agent_boot"},` +
			`"claim":{"url":"https://cafaye.test/agents/claim/abc","expires_at":"2026-10-09T13:00:00Z"}}`

	case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/home":
		// REPAIR-C's route, not yet on master; the shape its brief names —
		// the agent's identity, its status, and the account that claimed it.
		body = `{"agent":{"id":"agent_01HZX","name":"Noel","username":"noel","status":"claimed"},` +
			`"status":"claimed","claimed_by_account_id":"acct_01OWN"}`

	case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/tokens":
		// identity's agentTokenResponse array (agents.go:811), newest first,
		// revoked included.
		body = `[{"id":"tok_01CLI","name":"cli-issued","expires_at":"2026-11-09T12:00:00Z","created_at":"2026-10-09T12:00:00Z"},` +
			`{"id":"tok_01BOOT","name":"bootstrap","expires_at":"2026-11-09T12:00:00Z","created_at":"2026-10-09T12:00:00Z"}]`

	case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/tokens":
		// identity's issuedAgentToken (agents.go:618): id, name, expires_at,
		// token — the token here and only here.
		status = http.StatusCreated
		body = `{"id":"tok_01NEW","name":"cli-issued","expires_at":"2026-11-09T12:00:00Z","token":"caf_agent_new"}`

	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rotate"):
		status = http.StatusCreated
		body = `{"id":"tok_01ROT","name":"cli-issued","expires_at":"2026-11-09T12:00:00Z","token":"caf_agent_rot"}`

	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/agents/tokens/"):
		status = http.StatusNoContent
		body = ""

	default:
		status = http.StatusNotFound
		body = `{"error":"not_found"}`
	}

	if f.refuseStatus != 0 {
		status = f.refuseStatus
		body = f.refuseBody
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprint(w, body)
}

// oneCall asserts the fake was called exactly once and returns the call.
func oneCall(t *testing.T, fake *fakeIdentityServer) recordedCall {
	t.Helper()
	if len(fake.calls) != 1 {
		t.Fatalf("identity was called %d times, want 1", len(fake.calls))
	}
	return fake.calls[0]
}

// Register is the one UNAUTHENTICATED call: POST /v1/agents, no bearer, and
// the answer is identity's registerAgentResponse — the agent's id (which the
// old table's `ref` never was), the bootstrap token and its id, the claim URL.
func TestRegisterPostsToThePublicRouteWithNoBearer(t *testing.T) {
	fake, srv := newFakeIdentityServer(t)
	e := &HTTPEndpoint{BaseURL: srv.URL, Token: "caf_agent_caller"}

	res, err := e.Register(context.Background(), RegisterInput{Name: "Noel", Username: "noel"})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	call := oneCall(t, fake)
	if call.Method != http.MethodPost || call.Path != "/v1/agents" {
		t.Errorf("register called %s %s, want POST /v1/agents", call.Method, call.Path)
	}
	if call.Auth != "" {
		t.Errorf("register presented a bearer (%s); it is the one unauthenticated call", call.Auth)
	}
	if !strings.Contains(call.Body, `"name":"Noel"`) || !strings.Contains(call.Body, `"username":"noel"`) {
		t.Errorf("register's body did not carry the name and username: %s", call.Body)
	}
	if res.Agent.ID != "agent_01HZX" {
		t.Errorf("agent id = %q, want agent_01HZX", res.Agent.ID)
	}
	if res.Agent.Username != "noel" || res.Agent.Status != "unclaimed" {
		t.Errorf("agent = %+v, want noel/unclaimed", res.Agent)
	}
	if res.KeyID != "tok_01BOOT" {
		t.Errorf("key id = %q, want tok_01BOOT", res.KeyID)
	}
	if res.Token != "caf_agent_boot" {
		t.Errorf("token = %q, want the bootstrap credential", res.Token)
	}
	if res.ClaimURL != "https://cafaye.test/agents/claim/abc" {
		t.Errorf("claim URL = %q, want the one identity issued", res.ClaimURL)
	}
}

// Home is GET /v1/agents/home with the bearer — REPAIR-C's route, built
// against the route its brief names. The answer passes through as a map
// because the shape is identity's to finalize; the one fact read is status.
func TestHomeGetsTheHomeRouteWithTheBearer(t *testing.T) {
	fake, srv := newFakeIdentityServer(t)
	e := &HTTPEndpoint{BaseURL: srv.URL, Token: "caf_agent_caller"}

	home, err := e.Home(context.Background())
	if err != nil {
		t.Fatalf("home failed: %v", err)
	}
	call := oneCall(t, fake)
	if call.Method != http.MethodGet || call.Path != "/v1/agents/home" {
		t.Errorf("home called %s %s, want GET /v1/agents/home", call.Method, call.Path)
	}
	if call.Auth != authLen("Bearer caf_agent_caller") {
		t.Errorf("home presented %s, want the caller's bearer", call.Auth)
	}
	if (*home)["status"] != "claimed" {
		t.Errorf("home status = %v, want claimed", (*home)["status"])
	}
}

// Tokens is GET /v1/agents/tokens — an ARRAY, not one key. The old table's
// GET /v1/agents/key 404'd and the shape it decoded does not exist.
func TestTokensGetsTheListRouteAndDecodesTheArray(t *testing.T) {
	fake, srv := newFakeIdentityServer(t)
	e := &HTTPEndpoint{BaseURL: srv.URL, Token: "caf_agent_caller"}

	keys, err := e.Tokens(context.Background())
	if err != nil {
		t.Fatalf("tokens failed: %v", err)
	}
	call := oneCall(t, fake)
	if call.Method != http.MethodGet || call.Path != "/v1/agents/tokens" {
		t.Errorf("tokens called %s %s, want GET /v1/agents/tokens", call.Method, call.Path)
	}
	if call.Auth != authLen("Bearer caf_agent_caller") {
		t.Errorf("tokens presented %s, want the caller's bearer", call.Auth)
	}
	if len(keys) != 2 {
		t.Fatalf("tokens decoded %d keys, want the 2 in the array", len(keys))
	}
	if keys[0].ID != "tok_01CLI" || keys[0].Name != "cli-issued" {
		t.Errorf("keys[0] = %+v, want the newest token first", keys[0])
	}
	if keys[0].CreatedAt != "2026-10-09T12:00:00Z" {
		t.Errorf("keys[0].CreatedAt = %q, want the created_at identity sent", keys[0].CreatedAt)
	}
}

// CreateToken is POST /v1/agents/tokens with the name identity requires, and
// the answer is the issued token: id, name, expires_at and the token itself.
func TestCreateTokenPostsTheNameToTheCollectionRoute(t *testing.T) {
	fake, srv := newFakeIdentityServer(t)
	e := &HTTPEndpoint{BaseURL: srv.URL, Token: "caf_agent_caller"}

	fresh, err := e.CreateToken(context.Background(), "cli-issued")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	call := oneCall(t, fake)
	if call.Method != http.MethodPost || call.Path != "/v1/agents/tokens" {
		t.Errorf("create called %s %s, want POST /v1/agents/tokens", call.Method, call.Path)
	}
	if call.Body != `{"name":"cli-issued"}` {
		t.Errorf("create's body = %s, want the name identity requires", call.Body)
	}
	if call.IDKey == "" {
		t.Error("create sent no Idempotency-Key; a mutating call must")
	}
	if fresh.ID != "tok_01NEW" || fresh.Name != "cli-issued" {
		t.Errorf("fresh = %+v, want the issued token's metadata", fresh.KeyInfo)
	}
	if fresh.Token != "caf_agent_new" {
		t.Errorf("fresh token = %q, want the credential identity minted", fresh.Token)
	}
}

// RotateToken is POST /v1/agents/tokens/{tokenID}/rotate — the id in the path,
// which the old table's key/rotate had no way to carry.
func TestRotateTokenPostsToTheIDRoute(t *testing.T) {
	fake, srv := newFakeIdentityServer(t)
	e := &HTTPEndpoint{BaseURL: srv.URL, Token: "caf_agent_caller"}

	fresh, err := e.RotateToken(context.Background(), "tok_01CLI")
	if err != nil {
		t.Fatalf("rotate failed: %v", err)
	}
	call := oneCall(t, fake)
	if call.Method != http.MethodPost || call.Path != "/v1/agents/tokens/tok_01CLI/rotate" {
		t.Errorf("rotate called %s %s, want POST /v1/agents/tokens/tok_01CLI/rotate", call.Method, call.Path)
	}
	if call.Body != "{}" {
		t.Errorf("rotate's body = %s, want the empty object (identity 400s an empty body)", call.Body)
	}
	if fresh.ID != "tok_01ROT" || fresh.Token != "caf_agent_rot" {
		t.Errorf("rotate answered with %q/%q, want the fresh credential", fresh.ID, fresh.Token)
	}
}

// RevokeToken is DELETE /v1/agents/tokens/{tokenID} — the method and route the
// old table's key/revoke (POST …/key/revoke) had both wrong.
func TestRevokeTokenDeletesTheIDRoute(t *testing.T) {
	fake, srv := newFakeIdentityServer(t)
	e := &HTTPEndpoint{BaseURL: srv.URL, Token: "caf_agent_caller"}

	if err := e.RevokeToken(context.Background(), "tok_01CLI"); err != nil {
		t.Fatalf("revoke failed: %v", err)
	}
	call := oneCall(t, fake)
	if call.Method != http.MethodDelete || call.Path != "/v1/agents/tokens/tok_01CLI" {
		t.Errorf("revoke called %s %s, want DELETE /v1/agents/tokens/tok_01CLI", call.Method, call.Path)
	}
	if call.Body != "" {
		t.Errorf("revoke's body = %s, want none (identity reads a reason only when one is sent)", call.Body)
	}
}

// The 403 an unclaimed agent gets on the authenticated surface is an
// instruction, not a status — and it must survive the route change, because
// the remedy is the one thing a person needs from it.
func TestTheUnclaimedRefusalOnTheListRouteNamesTheRemedy(t *testing.T) {
	fake, srv := newFakeIdentityServer(t)
	fake.refuseStatus = http.StatusForbidden
	fake.refuseBody = `{"error":"agent_unclaimed"}`
	e := &HTTPEndpoint{BaseURL: srv.URL, Token: "caf_agent_caller"}

	_, err := e.Tokens(context.Background())
	if err == nil {
		t.Fatal("an unclaimed agent's call should have been refused")
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("the error is %T, want a StatusError", err)
	}
	if se.Status != http.StatusForbidden || se.Code != "agent_unclaimed" {
		t.Errorf("StatusError = %d/%q, want 403/agent_unclaimed", se.Status, se.Code)
	}
	if !strings.Contains(se.Hint(), "claim URL") {
		t.Errorf("the hint does not name the remedy:\n%s", se.Hint())
	}
}
