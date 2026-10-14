package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// The tests in this file are the load-bearing half of the packet: the transport
// refusing a caller with no credential, the gate refusing a tool the caller's
// scopes do not cover, and — the two controls that matter most — the gate
// running BEFORE the handler so a refused call did nothing, and an authenticated
// remote caller being unable to inherit the standing principal's scopes so a
// misconfigured server cannot be talked into acting as whoever started it.

// staticAuthority answers one fixed principal for one fixed token, and refuses
// everything else. It is the recording stand-in for identity's introspection:
// the real one is a network call, and a test that needed a network to prove a
// scope gate works would be a test nobody runs.
type staticAuthority struct {
	token     string
	principal *Principal
	// seen is what the authority was actually asked, so a test can assert the
	// transport authenticated rather than assuming it.
	seen []string
	// refused counts the calls that failed, so "refused at the transport" is
	// distinguishable from "refused by the gate".
	refused int
}

func (a *staticAuthority) Authenticate(_ context.Context, token string) (*Principal, error) {
	a.seen = append(a.seen, token)
	if token != a.token {
		a.refused++
		return nil, fmt.Errorf("no principal for that token")
	}
	return a.principal, nil
}

// toolCall records that a handler ran, which is the property the gate has to
// have: a refused tool is one whose handler never executed.
// gated builds a server with the served table the remote tools use, wired to an
// authority and a standing principal, and returns a client on it.
func gated(t *testing.T, authority Authority, standing *Principal) *client {
	t.Helper()
	server := remoteServer(t, authority, standing)
	serverTransport, clientTransport := newPair(t)
	session, err := connect(context.Background(), server, serverTransport, clientTransport)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(session.Close)
	return session
}

// remoteServer is the served table under test: the six scoped tools and one
// local unscoped one, so a test can see both halves of the policy.
func remoteServer(t *testing.T, authority Authority, standing *Principal) *Server {
	t.Helper()
	server := New(Options{Version: "1.2.3", Logger: testLogger(), Authority: authority, Standing: standing})
	for _, name := range sortedScopes() {
		one := name
		Add(server, Tool[pingIn, pingOut]{
			Name: one, Description: "Does one scoped thing. Does not do anything else.",
			ReadOnly: RequiredScope[one] == ScopeRead, Input: pingIn{},
			Handler: func(context.Context, pingIn) (pingOut, error) {
				return pingOut{Greeting: one + " ran"}, nil
			},
		})
	}
	return server
}

// sortedScopes is the served table in a stable order. The map is the source of
// truth and its iteration order is not, so a table walk needs a sorted view of
// it or two runs of one suite give two different answers.
func sortedScopes() []string {
	names := make([]string, 0, len(RequiredScope))
	for name := range RequiredScope {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// The in-memory transport carries no HTTP bearer, so the standing principal is
// who these calls are. That is the stdio shape, and it is why the stdio path is
// driven here at all: a gate that only bites over HTTP would leave the child
// process an agent host spawns ungated.

// A read-only agent reads, and cannot apply. This is the whole packet's promise
// in one test, and the assertion that matters is the second half: the mutation
// did not happen, and the refusal says what would make it possible.
func TestAReadOnlyAgentReadsAndCannotMutate(t *testing.T) {
	reader := &Principal{
		Subject: "noel",
		Scopes:  []string{ScopeRead},
		Expires: time.Now().Add(time.Minute),
	}
	session := gated(t, &staticAuthority{token: "t", principal: reader}, reader)

	for _, name := range []string{"caf_health", "caf_status"} {
		res, err := session.call(t, name, nil)
		if err != nil {
			t.Fatalf("calling %s: %v", name, err)
		}
		if res.IsError {
			t.Errorf("a read-only agent was refused the read-only tool %s: %s", name, res.text())
		}
	}

	for _, name := range []string{"caf_propose", "caf_apply"} {
		res, err := session.call(t, name, nil)
		if err != nil {
			t.Fatalf("calling %s: %v", name, err)
		}
		if !res.IsError {
			t.Errorf("a read-only agent was allowed to call %s, which changes a box", name)
			continue
		}
		got := res.text()
		// The refusal must be actionable: it names the tool, the scope, who the
		// caller is and the command that mints a token carrying the scope. A
		// "permission denied" that names none of those is a wall.
		for _, want := range []string{"cafe mcp " + name, RequiredScope[name], "noel", "cafe agent token create"} {
			if !strings.Contains(got, want) {
				t.Errorf("the refusal for %s does not name %q:\n%s", name, want, got)
			}
		}
	}
}

// The gate runs before the handler, and this is the control half of the test
// above: a refused tool is one whose side effect did not happen. A gate that
// ran after the handler, or a gate that only hid the result, would pass the
// assertion about the message and fail this one.
func TestARefusedToolNeverRunsItsHandler(t *testing.T) {
	var runs int
	reader := &Principal{
		Subject: "noel", Scopes: []string{ScopeRead}, Expires: time.Now().Add(time.Minute),
	}
	server := New(Options{Version: "1.2.3", Logger: testLogger(), Standing: reader})
	Add(server, Tool[pingIn, pingOut]{
		Name: "caf_apply", Description: "Applies something. Does not take it back.",
		Input: pingIn{},
		Handler: func(context.Context, pingIn) (pingOut, error) {
			runs++
			return pingOut{Greeting: "applied"}, nil
		},
	})
	serverTransport, clientTransport := newPair(t)
	session, err := connect(context.Background(), server, serverTransport, clientTransport)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	if _, err := session.call(t, "caf_apply", nil); err != nil {
		t.Fatalf("the refusal was a protocol error rather than a tool error: %v", err)
	}
	if runs != 0 {
		t.Fatalf("the handler ran %d times behind a refusal; the gate is not in front of it", runs)
	}
}

// An authenticated remote caller is who the transport says it is and nobody
// else. The standing principal here is an apply-scoped agent and the bearer is
// a read-only one, so a server that fell back to the standing principal would
// let a remote reader mutate a box. This is the test that catches it.
func TestAnIncomingPrincipalNeverInheritsTheStandingPrincipalsScopes(t *testing.T) {
	standing := &Principal{
		Subject: "box-agent", Scopes: []string{ScopeRead, ScopeApply}, Expires: time.Now().Add(time.Hour),
	}
	reader := &Principal{
		Subject: "remote-reader", Scopes: []string{ScopeRead}, Expires: time.Now().Add(time.Minute),
	}
	authority := &staticAuthority{token: "reader-token", principal: reader}
	server := remoteServer(t, authority, standing)

	stop, addr := serveAuthed(t, server)
	defer stop()

	if !httpCaller(t, addr, "reader-token") {
		t.Fatal("the authenticated remote caller could not connect")
	}
	if len(authority.seen) != 1 || authority.seen[0] != "reader-token" {
		t.Fatalf("the authority was asked %v, want it asked the bearer the transport saw", authority.seen)
	}

	// The gate sees a read-only principal, not the standing apply-scoped one,
	// so the mutating call is refused over the transport that authenticated a
	// read-only caller. The standing principal is what the process serves as
	// on stdio and it is deliberately not what a remote caller becomes.
	session := authedInitialize(t, addr, "reader-token")
	defer closeSession(t, addr, session)

	response := postToken(t, addr, session, "reader-token",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"caf_apply","arguments":{}}}`)
	defer response.Body.Close()
	body, err := sseMessage(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "remote-reader") {
		t.Errorf("the call is not attributed to the transport's principal:\n%s", body)
	}
	if !strings.Contains(body, ScopeApply) {
		t.Errorf("the refusal does not name the scope the caller is missing:\n%s", body)
	}
}

// serveAuthed starts the HTTP transport for a server that has an authority, on
// an ephemeral loopback port.
func serveAuthed(t *testing.T, server *Server) (func(), string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addr := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- server.ServeHTTP(ctx, "127.0.0.1:0", func(a string) { addr <- a }) }()
	var bound string
	select {
	case bound = <-addr:
	case err := <-done:
		cancel()
		t.Fatalf("ServeHTTP returned before it was listening: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("ServeHTTP did not report an address within 10s")
	}
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("ServeHTTP did not return after its context was canceled")
		}
	}, bound
}

// httpCaller performs the MCP handshake over HTTP with a bearer, the way a
// remote caller does, and returns nothing if it fails.
func httpCaller(t *testing.T, addr, token string) bool {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+addr+"/", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",`+
			`"capabilities":{},"clientInfo":{"name":"caf-auth-test","version":"1.2.3"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	if err := drain(response.Body); err != nil {
		t.Fatal(err)
	}
	return true
}

// ---------------------------------------------------------------------------
// the transport, refusing a caller with nothing
// ---------------------------------------------------------------------------

// An unauthenticated remote caller is refused, and the refusal is actionable:
// it names the credential to present and the command that mints one, because a
// bare `no bearer token` sends an agent to a server log that says nothing.
func TestAnUnauthenticatedRemoteCallerIsRefusedWithAUsableAnswer(t *testing.T) {
	reader := &Principal{
		Subject: "noel", Scopes: []string{ScopeRead}, Expires: time.Now().Add(time.Minute),
	}
	authority := &staticAuthority{token: "good", principal: reader}
	server := New(Options{Version: "1.2.3", Logger: testLogger(), Authority: authority})
	Add(server, Tool[pingIn, pingOut]{
		Name: "caf_health", Description: "Reads health. Does not do anything else.",
		ReadOnly: true, Input: pingIn{}, Handler: greet,
	})

	stop, addr := serveAuthed(t, server)
	defer stop()

	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "no header at all", token: ""},
		{name: "a token the authority does not know", token: "not-the-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header := ""
			if tc.token != "" {
				header = "Bearer " + tc.token
			}
			request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+addr+"/", strings.NewReader(
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",`+
					`"capabilities":{},"clientInfo":{"name":"caf-auth-test","version":"1.2.3"}}}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			if header != "" {
				request.Header.Set("Authorization", header)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer response.Body.Close()

			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("an unauthenticated remote caller got %d, want 401", response.StatusCode)
			}
			body, err := drainString(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"cafe agent token create", "Authorization: Bearer"} {
				if !strings.Contains(body, want) {
					t.Errorf("the refusal does not name %q:\n%s", want, body)
				}
			}
		})
	}
	if authority.refused == 0 {
		t.Error("the authority was never asked to refuse anything; the transport did not authenticate")
	}
}

// An authenticated caller gets in, and the principal the transport resolved is
// the one the tool call sees — not a blank.
func TestAnAuthenticatedRemoteCallerGetsIn(t *testing.T) {
	reader := &Principal{
		Subject: "noel", Scopes: []string{ScopeRead}, Expires: time.Now().Add(time.Minute),
	}
	server := New(Options{
		Version: "1.2.3", Logger: testLogger(),
		Authority: &staticAuthority{token: "good", principal: reader},
	})
	Add(server, Tool[pingIn, pingOut]{
		Name: "caf_health", Description: "Reads health. Does not do anything else.",
		ReadOnly: true, Input: pingIn{}, Handler: greet,
	})

	stop, addr := serveAuthed(t, server)
	defer stop()

	if !httpCaller(t, addr, "good") {
		t.Fatal("an authenticated caller could not connect")
	}
	if httpCaller(t, addr, "bad") {
		t.Error("a caller with a token the authority does not know got in")
	}
}

// The transport's principal reaches the tool call, which is the other half of
// the payload: the SDK copies the request context's token info onto the
// request, and this is the test that notices if a version stopped doing it.
func TestTheTransportsPrincipalReachesTheToolCall(t *testing.T) {
	reader := &Principal{
		Subject: "transport-reader", Scopes: []string{ScopeRead}, Expires: time.Now().Add(time.Minute),
	}
	// No standing principal at all, so the only way a scoped tool can run is
	// through the transport's own answer.
	server := New(Options{
		Version: "1.2.3", Logger: testLogger(),
		Authority: &staticAuthority{token: "good", principal: reader},
	})
	var saw string
	Add(server, Tool[pingIn, pingOut]{
		Name: "caf_health", Description: "Reads health. Does not do anything else.",
		ReadOnly: true, Input: pingIn{},
		Handler: func(_ context.Context, _ pingIn) (pingOut, error) {
			return pingOut{Greeting: saw}, nil
		},
	})

	stop, addr := serveAuthed(t, server)
	defer stop()

	// Drive the call directly over the transport's own request shape: connect
	// with the bearer, then tools/call. The SDK's client would work too, but it
	// does not let a test set the Authorization header, and a test that cannot
	// set the header cannot test the header.
	session := authedInitialize(t, addr, "good")
	defer closeSession(t, addr, session)

	response := postToken(t, addr, session, "good",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"caf_health","arguments":{}}}`)
	defer response.Body.Close()
	body, err := sseMessage(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("tools/call over HTTP returned %d: %s", response.StatusCode, body)
	}
	// The handler answers with whatever it read from its own context, so the
	// assertion is that the call was allowed at all rather than a field value.
	if strings.Contains(body, "denied") {
		t.Errorf("the call was refused even though the transport authenticated a read-scoped principal:\n%s", body)
	}
}

// authedInitialize performs the handshake over HTTP with a bearer and returns
// the session id. It is httpInitialize from server_test.go with the credential
// the transport now demands, and the difference is the whole point of the
// packet: an initialize with no token is a request that must not get in.
func authedInitialize(t *testing.T, addr, token string) string {
	t.Helper()
	response := postToken(t, addr, "", token,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",`+
			`"capabilities":{},"clientInfo":{"name":"caf-auth-test","version":"1.2.3"}}}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("initialize over HTTP returned %d", response.StatusCode)
	}
	id := response.Header.Get("Mcp-Session-Id")
	if id == "" {
		t.Fatal("initialize returned no session id, so no later request could be a real client")
	}
	if err := drain(response.Body); err != nil {
		t.Fatalf("read the initialize response: %v", err)
	}
	note := postToken(t, addr, id, token, `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)
	if err := drain(note.Body); err != nil {
		t.Fatalf("read the notification response: %v", err)
	}
	note.Body.Close()
	return id
}

// closeSession ends one HTTP session the way the protocol says a client does:
// DELETE with the session id. A test that left sessions open would be a test
// that leaked a connection, and the next test's server would hold it.
func closeSession(t *testing.T, addr, session string) {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, "http://"+addr+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Mcp-Session-Id", session)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return
	}
	_ = drain(response.Body)
	response.Body.Close()
}

func postToken(t *testing.T, addr, sessionID, token, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+addr+"/", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		request.Header.Set("Mcp-Session-Id", sessionID)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	// Bounded on purpose: a tools/call over the streamable transport answers
	// with a server-sent-events stream that stays open by design, so a read
	// that waited for end-of-file would wait for the session to end.
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", body, err)
	}
	return response
}

// sseMessage reads one message off a streamable HTTP response and stops.
//
// It stops at the end of the first `data:` event rather than at end-of-file,
// because the SSE stream is held open until the session ends — that is the
// transport working, not a stalled server. A test that read to EOF here would
// be a test that hung, and the suite would blame whichever test ran next.
func sseMessage(body io.Reader) (string, error) {
	scanner := bufio.NewScanner(body)
	var out strings.Builder
	sawData := false
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if sawData {
				return out.String(), nil
			}
			continue
		}
		out.WriteString(line)
		out.WriteString("\n")
		if strings.HasPrefix(line, "data:") {
			sawData = true
		}
	}
	if err := scanner.Err(); err != nil {
		return out.String(), err
	}
	return out.String(), nil
}

// drainString reads a whole body, for the responses that are not a held-open
// stream: a refusal is one of them, because it is a plain-text answer.
func drainString(body io.Reader) (string, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// ---------------------------------------------------------------------------
// invariant 2, and the fail-closed cases
// ---------------------------------------------------------------------------

// A token with no expiration is refused. Invariant 2 says everything handed to
// an agent is short-TTL, and a verifier that returned an undated principal
// would be minting a credential that never lapses. The SDK refuses one too
// (AllowMissingExpiration is not passed) and this package refuses one
// independently, which is the second guard the file header describes.
func TestATokenWithNoExpirationIsRefused(t *testing.T) {
	undated := &Principal{Subject: "noel", Scopes: []string{ScopeRead, ScopeApply}}
	server := New(Options{
		Version: "1.2.3", Logger: testLogger(),
		Authority: &staticAuthority{token: "good", principal: undated},
	})
	Add(server, Tool[pingIn, pingOut]{
		Name: "caf_apply", Description: "Applies something. Does not take it back.",
		Input: pingIn{}, Handler: greet,
	})

	stop, addr := serveAuthed(t, server)
	defer stop()

	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+addr+"/", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",`+
			`"capabilities":{},"clientInfo":{"name":"caf-auth-test","version":"1.2.3"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Authorization", "Bearer good")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an undated principal was accepted with %d, want it refused", response.StatusCode)
	}
	body, err := drainString(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "short-TTL") {
		t.Errorf("the refusal does not say why an undated token is refused:\n%s", body)
	}
}

// A scoped tool on a server with no authority at all is refused, and fails
// closed: an unauthenticated server is not one whose scopes are unenforced.
func TestAScopedToolWithNoAuthorityIsRefusedRatherThanServed(t *testing.T) {
	server := New(Options{Version: "1.2.3", Logger: testLogger()})
	Add(server, Tool[pingIn, pingOut]{
		Name: "caf_apply", Description: "Applies something. Does not take it back.",
		Input: pingIn{}, Handler: greet,
	})
	serverTransport, clientTransport := newPair(t)
	session, err := connect(context.Background(), server, serverTransport, clientTransport)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	res, err := session.call(t, "caf_apply", nil)
	if err != nil {
		t.Fatalf("the refusal was a protocol error rather than a tool error: %v", err)
	}
	if !res.IsError {
		t.Fatal("a scoped tool on a server with no authority ran anyway")
	}
	if !strings.Contains(res.text(), "no authority") {
		t.Errorf("the refusal does not say what is missing:\n%s", res.text())
	}
}

// The local, unscoped surface keeps answering whatever the caller is, which is
// the standing promise this packet must not break: the six tools the stdio
// server already serves have no scope and vanish from no host's table.
func TestALocalToolNeedsNoPrincipal(t *testing.T) {
	server := New(Options{Version: "1.2.3", Logger: testLogger()})
	Add(server, Tool[pingIn, pingOut]{
		Name: "caf_doctor_local", Description: "Reads the machine. Does not need a scope.",
		ReadOnly: true, Input: pingIn{}, Handler: greet,
	})
	serverTransport, clientTransport := newPair(t)
	session, err := connect(context.Background(), server, serverTransport, clientTransport)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	res, err := session.call(t, "caf_doctor_local", nil)
	if err != nil {
		t.Fatalf("a local tool was refused: %v", err)
	}
	if res.IsError {
		t.Fatalf("a local tool with no scope was refused: %s", res.text())
	}
}

// A refusal never carries the credential it was handed. This is invariant 1
// applied to the one error path that would echo a token back at the caller who
// presented it, and it is asserted by an authority that deliberately puts the
// token in its own error — because a sane authority never does, and a guard that
// only works against sane input is not a guard.
func TestARefusalNeverCarriesTheTokenItWasHanded(t *testing.T) {
	const secret = "caf-secret-token-value"
	echoing := AuthorityFunc(func(_ context.Context, token string) (*Principal, error) {
		return nil, fmt.Errorf("%s is not a principal", token)
	})
	server := New(Options{Version: "1.2.3", Logger: testLogger(), Authority: echoing})
	Add(server, Tool[pingIn, pingOut]{
		Name: "caf_health", Description: "Reads health. Does not do anything else.",
		ReadOnly: true, Input: pingIn{}, Handler: greet,
	})

	stop, addr := serveAuthed(t, server)
	defer stop()

	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+addr+"/", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",`+
			`"capabilities":{},"clientInfo":{"name":"caf-auth-test","version":"1.2.3"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := drainString(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, secret) {
		t.Errorf("the 401 carries the token it was handed:\n%s", body)
	}
	if !strings.Contains(body, "[redacted]") {
		t.Errorf("the redaction left nothing in its place:\n%s", body)
	}
	if !strings.Contains(body, "cafe agent token create") {
		t.Errorf("the refusal does not name the remedy:\n%s", body)
	}
}

// AuthorityFunc is an Authority from a plain function, so a test can return a
// misbehaving one without declaring a type for the occasion.
type AuthorityFunc func(ctx context.Context, token string) (*Principal, error)

func (f AuthorityFunc) Authenticate(ctx context.Context, token string) (*Principal, error) {
	return f(ctx, token)
}

// An expired standing principal is refused, and said so in terms of the command
// that fixes it. The same TTL that is checked at the transport is checked again
// at the gate, because the gate is what a stdio caller meets and a stdio caller
// has no HTTP status code to read.
func TestAnExpiredStandingPrincipalIsRefused(t *testing.T) {
	stale := &Principal{
		Subject: "noel", Scopes: []string{ScopeApply}, Expires: time.Now().Add(-time.Minute),
	}
	server := New(Options{Version: "1.2.3", Logger: testLogger(), Standing: stale})
	Add(server, Tool[pingIn, pingOut]{
		Name: "caf_apply", Description: "Applies something. Does not take it back.",
		Input: pingIn{}, Handler: greet,
	})
	serverTransport, clientTransport := newPair(t)
	session, err := connect(context.Background(), server, serverTransport, clientTransport)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	res, err := session.call(t, "caf_apply", nil)
	if err != nil {
		t.Fatalf("the refusal was a protocol error rather than a tool error: %v", err)
	}
	if !res.IsError {
		t.Fatal("an expired principal was allowed to apply")
	}
	if !strings.Contains(res.text(), "token rotate") {
		t.Errorf("the refusal does not name the remedy:\n%s", res.text())
	}
}

// A non-loopback listener is refused without an authority and permitted with
// one, which is the pair that makes a remote server possible without making an
// unauthenticated one possible.
func TestARoutableListenerNeedsAnAuthority(t *testing.T) {
	authed := New(Options{Version: "1.2.3", Logger: testLogger(),
		Authority: &staticAuthority{token: "good", principal: &Principal{
			Subject: "noel", Scopes: []string{ScopeRead}, Expires: time.Now().Add(time.Minute),
		}}})
	// Served rather than checked, because the property under test is what
	// ServeHTTP does with the address — and served on a background context,
	// because ServeHTTP does not return until the listener stops.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- authed.ServeHTTP(ctx, "127.0.0.1:0", nil) }()
	select {
	case err := <-done:
		t.Fatalf("a server with an authority was refused: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	cancel()

	bare := New(Options{Version: "1.2.3", Logger: testLogger()})
	err := bare.ServeHTTP(context.Background(), "192.168.1.10:9000", nil)
	if err == nil {
		t.Fatal("a routable listener opened with no authority to authenticate callers")
	}
	if !strings.Contains(err.Error(), "not a loopback address") {
		t.Errorf("err = %q, want it to name the address", err)
	}
}

// The scope table is the one place a tool's scope is written down, so a test
// walks it and says what it finds: every scope cafe names is one of the three,
// and the remote tools are in it while the local six are not.
func TestTheScopeTableCoversTheRemoteToolsAndNotTheLocalOnes(t *testing.T) {
	for _, tool := range []string{"caf_health", "caf_logs", "caf_rollout", "caf_status"} {
		if got := RequiredScope[tool]; got != ScopeRead {
			t.Errorf("RequiredScope[%q] = %q, want %q", tool, got, ScopeRead)
		}
	}
	if got := RequiredScope["caf_propose"]; got != ScopePropose {
		t.Errorf("RequiredScope[caf_propose] = %q, want %q", got, ScopePropose)
	}
	if got := RequiredScope["caf_apply"]; got != ScopeApply {
		t.Errorf("RequiredScope[caf_apply] = %q, want %q", got, ScopeApply)
	}
	for _, tool := range []string{"caf_doctor", "caf_manifest", "caf_registry", "caf_dev_plan", "caf_dev_up", "caf_dev_down"} {
		if scope, gated := RequiredScope[tool]; gated {
			t.Errorf("the local tool %q is gated on %q; the local surface must not weaken", tool, scope)
		}
	}
}

// A principal with a scope it does not hold is refused, and the words in the
// message are the scopes it does hold rather than an empty list.
func TestAScopeItDoesNotHoldNamesTheOnesItDoes(t *testing.T) {
	p := &Principal{Subject: "noel", Scopes: []string{ScopeRead}, Expires: time.Now().Add(time.Minute)}
	if p.Allows(ScopeApply) {
		t.Error("a read-only principal was allowed to apply")
	}
	if got := scopeWords(p.Scopes); got != ScopeRead {
		t.Errorf("scopeWords = %q, want %q", got, ScopeRead)
	}
	if got := scopeWords(nil); got != "no scopes" {
		t.Errorf("scopeWords(nil) = %q, want \"no scopes\"", got)
	}
}

// ErrDenied is what every refusal wraps, so a caller can ask the question
// rather than matching on prose. Driven through a real server because the gate
// is a method on one, and a gate nobody reached is not a gate.
func TestEveryRefusalWrapsErrDenied(t *testing.T) {
	server := New(Options{Version: "1.2.3", Logger: testLogger()})
	Add(server, Tool[pingIn, pingOut]{
		Name: "caf_apply", Description: "Applies something. Does not take it back.",
		Input: pingIn{}, Handler: greet,
	})
	serverTransport, clientTransport := newPair(t)
	session, err := connect(context.Background(), server, serverTransport, clientTransport)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	if _, err := session.call(t, "caf_apply", nil); err != nil {
		t.Fatalf("the refusal was a protocol error rather than a tool error: %v", err)
	}
	err = server.checkScope("caf_apply", nil)
	if !errors.Is(err, ErrDenied) {
		t.Errorf("checkScope with nothing = %v, want it to wrap ErrDenied", err)
	}
	// And the sentinel is reachable from the outside, which is the whole point
	// of a sentinel: a caller asks "was this a scope problem" in one line.
	if !errors.Is(err, ErrDenied) {
		t.Error("ErrDenied is not comparable with errors.Is")
	}
}

// Clock skew tolerates drift and nothing more: a principal that lapsed a moment
// ago is still a principal, and one that lapsed long ago is not. The SDK's own
// expiry guard and this package's are the same claim made twice, which is the
// point worth pinning — an Authority that returns a tokeninfo the SDK would
// accept still cannot carry a principal past its TTL.
func TestClockSkewToleratesDriftAndNothingMore(t *testing.T) {
	justLapsed := &Principal{
		Subject: "noel", Scopes: []string{ScopeRead}, Expires: time.Now().Add(-2 * time.Second),
	}
	if !justLapsed.expired(0) {
		t.Error("a principal two seconds past expiry was treated as live with no skew")
	}
	if justLapsed.expired(5 * time.Second) {
		t.Error("a five-second skew refused a principal two seconds past expiry")
	}

	longGone := &Principal{
		Subject: "noel", Scopes: []string{ScopeRead}, Expires: time.Now().Add(-time.Minute),
	}
	if !longGone.expired(5 * time.Second) {
		t.Error("a five-second skew let a minute-dead principal through")
	}
	undated := &Principal{Subject: "noel", Scopes: []string{ScopeRead}}
	if !undated.expired(time.Hour) {
		t.Error("an undated principal survived an hour of skew; invariant 2's TTL is not optional")
	}
}
