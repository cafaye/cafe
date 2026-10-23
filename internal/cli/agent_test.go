package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anywaye/cafe/internal/agent"
)

// agentRegisterAttemptsForTest is the retry budget the store is allowed to
// spend on a generated username. It is named here rather than inlined so a
// change to the budget fails a test instead of quietly passing.
const agentRegisterAttemptsForTest = 6

// agentTestToken is a string no real credential looks like and every fake in
// this file answers with it. The assertions are that it reaches the keyring and
// reaches NEITHER stream. A test that only checked "the command worked" would
// pass against a CLI that printed the token in its success path, which is the
// one failure this packet exists to prevent.
var agentTestToken = "caf_agent_" + strings.Repeat("t", 40)

// agentRotatedToken and agentCreatedToken are distinct strings for the same
// reason: an assertion that searched for one literal would miss a leak of the
// other.
var (
	agentRotatedToken = "caf_agent_" + strings.Repeat("r", 40)
	agentCreatedToken = "caf_agent_" + strings.Repeat("c", 40)
)

// fakeIdentity is the identity endpoint, injected. It answers with what the
// case planted and records which routes were asked, because "the token verb
// called rotate and not revoke" is a claim about calls. The planted shapes are
// identity's own wire shapes, not the plan doc's: the token list is an array,
// and a token's metadata carries id and dates rather than a prefix and scopes.
type fakeIdentity struct {
	register   *agent.RegisterResult
	registerEr error

	home   *agent.Home
	homeEr error

	tokens   []agent.KeyInfo
	tokensEr error

	create     *agent.NewKey
	createEr   error
	createName string

	rotate   *agent.NewKey
	rotateEr error
	rotateID string

	revokeEr error
	revokeID string

	// calls is every method name that was invoked, in order.
	calls []string
}

func (f *fakeIdentity) Register(_ context.Context, in agent.RegisterInput) (*agent.RegisterResult, error) {
	f.calls = append(f.calls, "register:"+in.Username)
	if f.registerEr != nil {
		return nil, f.registerEr
	}
	return f.register, nil
}

func (f *fakeIdentity) Home(_ context.Context) (*agent.Home, error) {
	f.calls = append(f.calls, "home")
	if f.homeEr != nil {
		return nil, f.homeEr
	}
	return f.home, nil
}

func (f *fakeIdentity) Tokens(_ context.Context) ([]agent.KeyInfo, error) {
	f.calls = append(f.calls, "tokens")
	if f.tokensEr != nil {
		return nil, f.tokensEr
	}
	return f.tokens, nil
}

func (f *fakeIdentity) CreateToken(_ context.Context, name string) (*agent.NewKey, error) {
	f.calls = append(f.calls, "create:"+name)
	f.createName = name
	if f.createEr != nil {
		return nil, f.createEr
	}
	return f.create, nil
}

func (f *fakeIdentity) RotateToken(_ context.Context, tokenID string) (*agent.NewKey, error) {
	f.calls = append(f.calls, "rotate:"+tokenID)
	f.rotateID = tokenID
	if f.rotateEr != nil {
		return nil, f.rotateEr
	}
	return f.rotate, nil
}

func (f *fakeIdentity) RevokeToken(_ context.Context, tokenID string) error {
	f.calls = append(f.calls, "revoke:"+tokenID)
	f.revokeID = tokenID
	return f.revokeEr
}

// unclaimed answers the way identity answers an agent no human has approved:
// 403, and the error code is in the body rather than the status line.
func unclaimed() error {
	return &agent.StatusError{
		Op:     "cafe agent whoami",
		Route:  "GET /v1/agents/home",
		Status: http.StatusForbidden,
		Code:   "agent_unclaimed",
		Body:   `{"error":"agent_unclaimed"}`,
	}
}

// agentTestEnv is the deps every case below wires a command tree to: a
// directory the test owns, a keyring in memory and an identity endpoint that
// answers without a network. Nothing here touches the machine's config
// directory, its keychain or the network, which is the only reason these tests
// can run on a bare CI runner — and the reason a keychain prompt can never hang
// one.
func agentTestEnv(t *testing.T) (deps agentDeps, id *fakeIdentity, ring *agentTestRing) {
	t.Helper()
	id = &fakeIdentity{
		register: &agent.RegisterResult{
			Agent:    agent.Agent{ID: "agent_01HZX", Username: "noel", Status: "unclaimed"},
			KeyID:    "tok_01BOOT",
			Token:    agentTestToken,
			ClaimURL: "https://anywaye.test/agents/claim/abcdef0123456789",
		},
		home: &agent.Home{
			"agent":  map[string]any{"id": "agent_01HZX", "username": "noel", "status": "unclaimed"},
			"status": "unclaimed",
		},
		tokens: []agent.KeyInfo{
			{ID: "tok_01BOOT", Name: "bootstrap", ExpiresAt: "2026-11-09T12:00:00Z", CreatedAt: "2026-10-09T12:00:00Z"},
			{ID: "tok_01CLI", Name: "cli-issued", ExpiresAt: "2026-11-09T12:00:00Z", CreatedAt: "2026-10-09T12:00:00Z"},
		},
		create: &agent.NewKey{KeyInfo: agent.KeyInfo{ID: "tok_01NEW", Name: "cli-issued", ExpiresAt: "2026-11-09T12:00:00Z"}, Token: agentCreatedToken},
		rotate: &agent.NewKey{KeyInfo: agent.KeyInfo{ID: "tok_01ROT", Name: "cli-issued", ExpiresAt: "2026-11-09T12:00:00Z"}, Token: agentRotatedToken},
	}
	ring = newAgentTestRing()
	deps = agentDeps{dir: t.TempDir(), ring: ring, baseURL: "https://anywaye.test"}
	deps.dial = func(baseURL, token string) agent.Endpoint {
		// The dial is where the token is proven to be per-call: it is recorded
		// so a case can assert what was sent without the value being kept
		// anywhere a later assertion could print it.
		id.calls = append(id.calls, "dial:"+baseURL+"/"+strconvLen(token))
		return id
	}
	return deps, id, ring
}

// strconvLen reports how many characters a token had, never which ones: the
// dial assertion needs "a token was sent", not what it was.
func strconvLen(s string) string {
	return strings.Repeat("*", len(s))
}

// agentTestRing is the in-memory keyring. Three lines, and the whole point of
// the Keyring seam: a test of this package runs on a runner with no keychain,
// no D-Bus and no home directory to speak of — so it can never hang on a
// keychain prompt.
type agentTestRing struct{ entries map[string]string }

func newAgentTestRing() *agentTestRing { return &agentTestRing{entries: map[string]string{}} }

func (r *agentTestRing) Get(service, account string) (string, error) {
	if v, ok := r.entries[service+"/"+account]; ok {
		return v, nil
	}
	return "", errors.New("keyring: no such entry " + service + "/" + account)
}

func (r *agentTestRing) Set(service, account, value string) error {
	r.entries[service+"/"+account] = value
	return nil
}

func (r *agentTestRing) Delete(service, account string) error {
	delete(r.entries, service+"/"+account)
	return nil
}

var _ agent.Keyring = (*agentTestRing)(nil)

// runAgentCLI drives ONE cafe agent command tree against the injected deps and
// hands back the exit code plus both streams — exactly what main() does with the
// real process, minus the process. It goes through the parent's dispatcher
// rather than at a verb directly, so the verb lookup and its usage errors are
// what is being tested and not something beside them.
func runAgentCLI(t *testing.T, deps agentDeps, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion, Context: context.Background()}
	err := newUserAgentCommand(deps).Execute(env, args)
	return codeFor(*env, err), out.String(), errOut.String()
}

// parseAgentDoc decodes the document a command wrote on stdout. It is strict:
// the WHOLE buffer must be one JSON document, because a machine parsing it has
// no way to skip a prose line somebody helpfully added.
func parseAgentDoc(t *testing.T, stdout string) map[string]any {
	t.Helper()
	if !json.Valid([]byte(stdout)) {
		t.Fatalf("stdout is not one clean JSON document:\n%s", stdout)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not the document this CLI promises: %v\n%s", err, stdout)
	}
	return doc
}

// THE REGISTER TEST: the whole sequence, and the two streams it uses.
func TestAgentRegisterSplitsTheStreamsAndStoresTheToken(t *testing.T) {
	deps, id, ring := agentTestEnv(t)

	code, stdout, stderr := runAgentCLI(t, deps, "register", "--name", "Noel")
	if code != 0 {
		t.Fatalf("register exited %d (stderr: %s)", code, stderr)
	}

	doc := parseAgentDoc(t, stdout)
	if doc["action"] != "registered" {
		t.Errorf("action = %v, want \"registered\"", doc["action"])
	}
	// An existence, never a value.
	if doc["token_stored"] != true {
		t.Errorf("token_stored = %v, want true", doc["token_stored"])
	}
	if strings.Contains(stdout, agentTestToken) {
		t.Error("stdout carried the token")
	}
	// The claim URL is the one thing a PERSON must act on, so the sentence on
	// stderr names it. It is also a field of the document, and deliberately so:
	// invariant 5 says the agent may SURFACE the link, which it can only do
	// through the channel it can parse.
	if !strings.Contains(stderr, id.register.ClaimURL) {
		t.Errorf("stderr does not name the claim URL:\n%s", stderr)
	}
	if doc["claim_url"] != id.register.ClaimURL {
		t.Errorf("claim_url = %v, want %q", doc["claim_url"], id.register.ClaimURL)
	}

	// The token is in the keyring, under the service and account this package
	// names — and that is the only place in the tree it exists.
	if got := ring.entries["cafe/agent/noel-anywaye-test"]; got != agentTestToken {
		t.Errorf("keyring holds %q, want the token identity minted", got)
	}
	// The config file on disk holds the non-secrets and no credential.
	raw, err := os.ReadFile(filepath.Join(deps.dir, "config.json"))
	if err != nil {
		t.Fatalf("no config file after a register: %v", err)
	}
	if strings.Contains(string(raw), agentTestToken) {
		t.Error("config.json carried the token; it is a non-secrets file")
	}
	if !strings.Contains(string(raw), "noel") {
		t.Errorf("config.json does not record the agent:\n%s", raw)
	}
	if len(id.calls) == 0 {
		t.Fatal("identity was never called")
	}
}

// THE INVARIANT, END TO END: every verb of the normal flow, both streams, three
// different tokens. This is the test the brief names, and its control half is
// the last block — the agent's username IS printed, so a build that redacted
// everything would fail here rather than pass silently.
func TestNoTokenEverReachesEitherStream(t *testing.T) {
	deps, _, ring := agentTestEnv(t)
	tokens := []string{agentTestToken, agentCreatedToken, agentRotatedToken}

	flow := [][]string{
		{"register", "--name", "Noel"},
		{"login", "--agent", "noel"},
		{"whoami"},
		{"token", "show"},
		{"token", "create"},
		{"token", "rotate"},
	}
	for _, args := range flow {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, stdout, stderr := runAgentCLI(t, deps, args...)
			if code != 0 {
				t.Fatalf("%v exited %d (stderr: %s)", args, code, stderr)
			}
			parseAgentDoc(t, stdout)
			for _, token := range tokens {
				if strings.Contains(stdout, token) {
					t.Errorf("%v printed a token on stdout", args)
				}
				if strings.Contains(stderr, token) {
					t.Errorf("%v printed a token on stderr", args)
				}
			}
			// The keyring, not the streams, is where a credential lives.
			if got := ring.entries["cafe/agent/noel-anywaye-test"]; got == "" {
				t.Errorf("%v left the keyring empty", args)
			}
		})
	}

	// The control: a document that redacted EVERYTHING would pass the loop
	// above by printing nothing at all. The username must be there.
	code, stdout, _ := runAgentCLI(t, deps, "whoami")
	if code != 0 {
		t.Fatalf("whoami exited %d", code)
	}
	if !strings.Contains(stdout, "noel") {
		t.Errorf("the document names no agent, so the redaction above proves nothing:\n%s", stdout)
	}

	// And the ring ends up holding the NEWEST token, not the first.
	if got := ring.entries["cafe/agent/noel-anywaye-test"]; got != agentRotatedToken {
		t.Errorf("keyring holds the pre-rotation token; rotate did not replace it")
	}

	// Revoke is the one destructive verb, and it is the one that takes the
	// entry out of the ring.
	code, stdout, stderr := runAgentCLI(t, deps, "token", "revoke", "--yes")
	if code != 0 {
		t.Fatalf("revoke exited %d (stderr: %s)", code, stderr)
	}
	if got := ring.entries["cafe/agent/noel-anywaye-test"]; got != "" {
		t.Errorf("revoke left a token in the keyring")
	}
	for _, token := range tokens {
		if strings.Contains(stdout, token) || strings.Contains(stderr, token) {
			t.Error("revoke printed a token")
		}
	}
	if doc := parseAgentDoc(t, stdout); doc["token_stored"] != false {
		t.Errorf("revoke reported token_stored=%v, want false", doc["token_stored"])
	}
}

// Revoke is the one verb here that destroys a credential, so it refuses without
// an explicit --yes: a mistyped command must not be able to end an agent's
// session.
func TestAgentTokenRevokeRefusesWithoutYes(t *testing.T) {
	deps, id, _ := agentTestEnv(t)
	if code, _, _ := runAgentCLI(t, deps, "register", "--name", "Noel"); code != 0 {
		t.Fatal("register did not seed a session")
	}

	code, _, stderr := runAgentCLI(t, deps, "token", "revoke")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d (a refusal is a usage error)", code, exitUsage)
	}
	if !strings.Contains(stderr, "--yes") {
		t.Errorf("the refusal does not name --yes:\n%s", stderr)
	}
	for _, call := range id.calls {
		if strings.HasPrefix(call, "revoke") {
			t.Error("revoke was called without confirmation")
		}
	}
}

// The 403 an unclaimed agent gets is not a status code, it is an instruction —
// and a client that reported "403 Forbidden" would send the reader to a server
// log that says nothing useful.
func TestAgentWhoAmisUnclaimedAnswerNamesTheRemedy(t *testing.T) {
	deps, id, _ := agentTestEnv(t)
	if code, _, _ := runAgentCLI(t, deps, "register", "--name", "Noel"); code != 0 {
		t.Fatal("register did not seed a session")
	}
	id.homeEr = unclaimed()

	code, stdout, stderr := runAgentCLI(t, deps, "whoami")
	if code != exitFailure {
		t.Errorf("exit = %d, want %d", code, exitFailure)
	}
	if stdout != "" {
		t.Errorf("a failure must not write a document on stdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, "agent_unclaimed") {
		t.Errorf("stderr does not name the error code identity gave:\n%s", stderr)
	}
	if !strings.Contains(stderr, "claim URL") {
		t.Errorf("stderr does not name the remedy:\n%s", stderr)
	}
}

// A machine with no endpoint configured is a misconfigured machine, not a
// mistyped command: exit 1, and the message names both the flag and the
// variable.
func TestAgentRegisterWithoutAnEndpointIsARefusal(t *testing.T) {
	deps, _, _ := agentTestEnv(t)
	deps.baseURL = ""
	t.Setenv(identityURLEnv, "")

	code, _, stderr := runAgentCLI(t, deps, "register", "--name", "Noel")
	if code != exitFailure {
		t.Errorf("exit = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, identityURLEnv) {
		t.Errorf("the refusal does not name the environment variable:\n%s", stderr)
	}
	if !strings.Contains(stderr, "--base-url") {
		t.Errorf("the refusal does not name the flag:\n%s", stderr)
	}
}

// Registration is not free: identity mints an unclaimed principal per POST, so a
// caller who never chose a username gets one generated and retried on
// collision, and a caller who DID choose one is told about the collision
// instead of being silently registered as something else.
func TestAgentRegisterGeneratesAUsernameAndRetriesOnConflict(t *testing.T) {
	deps, id, _ := agentTestEnv(t)
	// A generated username is retried; a chosen one is not.
	id.registerEr = &agent.StatusError{
		Op: "cafe agent register", Route: "POST /v1/agents",
		Status: http.StatusUnprocessableEntity, Code: "invalid_agent",
		Body: `{"error":"invalid_agent","message":"username has already been taken"}`,
	}

	// A username THIS CLIENT generated is retried on collision: nobody chose
	// it, so a different one is not a different decision.
	code, _, _ := runAgentCLI(t, deps, "register", "--name", "Noel")
	if code == 0 {
		t.Fatal("a conflict on every attempt should not have succeeded")
	}
	if got := strings.Count(strings.Join(id.calls, ","), "register:"); got != agentRegisterAttemptsForTest {
		t.Errorf("a generated username was attempted %d times, want %d", got, agentRegisterAttemptsForTest)
	}

	// A username the CALLER chose is not retried: a person who typed
	// `--username noel` and was told it is taken is being told something they
	// can act on, and a client that silently registered `noel-a3f9` instead has
	// done something they did not ask for.
	id.calls = nil
	code, _, _ = runAgentCLI(t, deps, "register", "--name", "Noel", "--username", "noel")
	if code == 0 {
		t.Fatal("a chosen username that is taken should not have succeeded")
	}
	if got := strings.Count(strings.Join(id.calls, ","), "register:"); got != 1 {
		t.Errorf("a chosen username was attempted %d times, want exactly 1", got)
	}

	id.registerEr = nil
	code, stdout, stderr := runAgentCLI(t, deps, "register", "--name", "Noel")
	if code != 0 {
		t.Fatalf("register exited %d (stderr: %s)", code, stderr)
	}
	if doc := parseAgentDoc(t, stdout); doc["token_stored"] != true {
		t.Errorf("token_stored = %v, want true", doc["token_stored"])
	}
}

// Login is a local decision, and `cafe agent whoami` is the verb that proves a
// token works — so a login against a session whose token is gone is refused
// with the remedy rather than silently becoming the active session.
func TestAgentLoginRefusesASessionWithNoToken(t *testing.T) {
	deps, _, _ := agentTestEnv(t)
	if code, _, _ := runAgentCLI(t, deps, "register", "--name", "Noel"); code != 0 {
		t.Fatal("register did not seed a session")
	}
	deps.ring.(*agentTestRing).entries["cafe/agent/noel-anywaye-test"] = ""

	code, _, stderr := runAgentCLI(t, deps, "login", "--agent", "noel")
	if code != exitFailure {
		t.Errorf("exit = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "cafe agent register") {
		t.Errorf("the refusal does not name the remedy:\n%s", stderr)
	}
}

// A word cafe does not have is a usage mistake: the answer is the list of verbs
// rather than a stack trace.
func TestAgentUnknownVerbListsTheVerbs(t *testing.T) {
	deps, _, _ := agentTestEnv(t)
	code, _, stderr := runAgentCLI(t, deps, "frobnicate")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	for _, want := range []string{"register", "login", "whoami", "token"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the answer does not list %q:\n%s", want, stderr)
		}
	}
}

// A bad argument count is the user's mistake, so it exits 2 rather than 1 —
// checked on every verb, because a verb that accepted a stray argument would
// have to decide what it meant. The token leaves take at most one argument (a
// name or a token id), so the usage error is two of them; one is a legal
// argument the verb acts on.
func TestAgentVerbsRejectBadArgumentCounts(t *testing.T) {
	deps, _, _ := agentTestEnv(t)
	for _, args := range [][]string{
		{"register", "extra"},
		{"login", "extra"},
		{"whoami", "extra"},
		{"token", "show", "extra", "more"},
		{"token", "create", "extra", "more"},
		{"token", "rotate", "extra", "more"},
		{"token", "revoke", "extra", "more"},
		{"token", "frobnicate"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, _, _ := runAgentCLI(t, deps, args...)
			if code != exitUsage {
				t.Errorf("%v exited %d, want %d", args, code, exitUsage)
			}
		})
	}
}

// SHOW renders the array identity returns, and a name selects the one live
// token with that name — the CLI's own contract, decided because identity's
// mounted route is a list and its one_live_name_per_agent index means a name
// identifies a live token.
func TestAgentTokenShowListsAndSelectsOneByName(t *testing.T) {
	deps, id, _ := agentTestEnv(t)
	if code, _, _ := runAgentCLI(t, deps, "register", "--name", "Noel"); code != 0 {
		t.Fatal("register did not seed a session")
	}

	// Bare show lists every token, revoked included.
	code, stdout, stderr := runAgentCLI(t, deps, "token", "show")
	if code != 0 {
		t.Fatalf("token show exited %d (stderr: %s)", code, stderr)
	}
	doc := parseAgentDoc(t, stdout)
	keys, ok := doc["keys"].([]any)
	if !ok {
		t.Fatalf("the document carries no keys array:\n%s", stdout)
	}
	if len(keys) != len(id.tokens) {
		t.Errorf("token show listed %d tokens, want %d", len(keys), len(id.tokens))
	}

	// A name selects the one live token with that name.
	code, stdout, stderr = runAgentCLI(t, deps, "token", "show", "cli-issued")
	if code != 0 {
		t.Fatalf("token show cli-issued exited %d (stderr: %s)", code, stderr)
	}
	doc = parseAgentDoc(t, stdout)
	keys, ok = doc["keys"].([]any)
	if !ok {
		t.Fatalf("the document carries no keys array:\n%s", stdout)
	}
	if len(keys) != 1 {
		t.Errorf("token show cli-issued listed %d tokens, want the 1 live one", len(keys))
	}
	if keys[0].(map[string]any)["id"] != "tok_01CLI" {
		t.Errorf("the selected token is %v, want tok_01CLI", keys[0].(map[string]any)["id"])
	}

	// A name no live token holds is a failure with the remedy, not an empty
	// list: an empty list would read as "this agent has no tokens".
	code, _, stderr = runAgentCLI(t, deps, "token", "show", "nosuch")
	if code != exitFailure {
		t.Errorf("exit = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "cafe agent token show") {
		t.Errorf("the failure does not name the verb:\n%s", stderr)
	}
}

// Rotate and revoke carry a token id. Acting on some OTHER token must not
// disturb the session's own: the keyring holds the bootstrap credential, and
// overwriting it with a rotated deploy token would strand the session.
func TestAgentTokenRotateAndRevokeByIdLeaveTheSessionsTokenAlone(t *testing.T) {
	deps, id, ring := agentTestEnv(t)
	if code, _, _ := runAgentCLI(t, deps, "register", "--name", "Noel"); code != 0 {
		t.Fatal("register did not seed a session")
	}

	code, stdout, stderr := runAgentCLI(t, deps, "token", "rotate", "tok_01CLI")
	if code != 0 {
		t.Fatalf("token rotate tok_01CLI exited %d (stderr: %s)", code, stderr)
	}
	if got := ring.entries["cafe/agent/noel-anywaye-test"]; got != agentTestToken {
		t.Errorf("rotating another token overwrote the session's keyring entry: holds %q", got)
	}
	if id.rotateID != "tok_01CLI" {
		t.Errorf("rotate asked for %q, want tok_01CLI", id.rotateID)
	}
	if doc := parseAgentDoc(t, stdout); doc["token_stored"] != true {
		t.Errorf("token_stored = %v, want true (the session's own token is untouched)", doc["token_stored"])
	}

	code, stdout, stderr = runAgentCLI(t, deps, "token", "revoke", "--yes", "tok_01CLI")
	if code != 0 {
		t.Fatalf("token revoke tok_01CLI exited %d (stderr: %s)", code, stderr)
	}
	if got := ring.entries["cafe/agent/noel-anywaye-test"]; got != agentTestToken {
		t.Errorf("revoking another token disturbed the session's keyring entry: holds %q", got)
	}
	if id.revokeID != "tok_01CLI" {
		t.Errorf("revoke asked for %q, want tok_01CLI", id.revokeID)
	}
	if doc := parseAgentDoc(t, stdout); doc["token_stored"] != true {
		t.Errorf("token_stored = %v, want true (the session's own token is untouched)", doc["token_stored"])
	}
}

// A bare rotate or revoke acts on the session's own token — the one in the
// keyring — whose id the config has carried since registration. That is the
// invariant that makes the no-argument form mean one thing.
func TestAgentTokenRotateAndRevokeWithNoIdActOnTheSessionsOwnToken(t *testing.T) {
	deps, id, ring := agentTestEnv(t)
	if code, _, _ := runAgentCLI(t, deps, "register", "--name", "Noel"); code != 0 {
		t.Fatal("register did not seed a session")
	}

	code, _, stderr := runAgentCLI(t, deps, "token", "rotate")
	if code != 0 {
		t.Fatalf("token rotate exited %d (stderr: %s)", code, stderr)
	}
	if id.rotateID != "tok_01BOOT" {
		t.Errorf("bare rotate asked for %q, want the session's own tok_01BOOT", id.rotateID)
	}
	if got := ring.entries["cafe/agent/noel-anywaye-test"]; got != agentRotatedToken {
		t.Errorf("the rotated token was not stored: keyring holds %q", got)
	}

	code, _, stderr = runAgentCLI(t, deps, "token", "revoke", "--yes")
	if code != 0 {
		t.Fatalf("token revoke exited %d (stderr: %s)", code, stderr)
	}
	if id.revokeID != "tok_01ROT" {
		t.Errorf("bare revoke asked for %q, want the post-rotation tok_01ROT", id.revokeID)
	}
	if got := ring.entries["cafe/agent/noel-anywaye-test"]; got != "" {
		t.Errorf("revoke left a token in the keyring")
	}
}

// Create mints a named token: identity requires a name, and the CLI's own
// default is what a bare create sends.
func TestAgentTokenCreateSendsItsName(t *testing.T) {
	deps, id, _ := agentTestEnv(t)
	if code, _, _ := runAgentCLI(t, deps, "register", "--name", "Noel"); code != 0 {
		t.Fatal("register did not seed a session")
	}

	code, _, stderr := runAgentCLI(t, deps, "token", "create")
	if code != 0 {
		t.Fatalf("token create exited %d (stderr: %s)", code, stderr)
	}
	if id.createName != "cli-issued" {
		t.Errorf("create asked for a token named %q, want the CLI's default", id.createName)
	}

	code, _, stderr = runAgentCLI(t, deps, "token", "create", "deploy")
	if code != 0 {
		t.Fatalf("token create deploy exited %d (stderr: %s)", code, stderr)
	}
	if id.createName != "deploy" {
		t.Errorf("create asked for a token named %q, want deploy", id.createName)
	}
}

// Bare `cafe agent` is a question, not a mistake: it prints help and succeeds.
func TestBareAgentPrintsHelp(t *testing.T) {
	deps, _, _ := agentTestEnv(t)
	code, stdout, stderr := runAgentCLI(t, deps)
	if code != exitSuccess {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}
	for _, want := range []string{"register", "login", "whoami", "token"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help does not list %q:\n%s", want, stdout)
		}
	}
}
