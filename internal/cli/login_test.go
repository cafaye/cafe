package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var loginTestToken = "caf_human_" + strings.Repeat("t", 40)

type fakeHuman struct {
	token string
	email string
	err   error
	calls []string
}

func (f *fakeHuman) Login(_ context.Context, email, password string) (*humanLoginResult, error) {
	f.calls = append(f.calls, "login:"+email+":"+password)
	if f.err != nil {
		return nil, f.err
	}
	return &humanLoginResult{Token: f.token, Email: f.email}, nil
}

func loginTestEnv(t *testing.T) (loginDeps, *fakeHuman, *agentTestRing) {
	t.Helper()
	f := &fakeHuman{token: loginTestToken, email: "noel@cafaye.test"}
	ring := newAgentTestRing()
	deps := loginDeps{dir: t.TempDir(), ring: ring, baseURL: "https://cafaye.test"}
	deps.dial = func(baseURL string) humanEndpoint {
		f.calls = append(f.calls, "dial:"+baseURL)
		return f
	}
	return deps, f, ring
}

func runLoginCLI(t *testing.T, deps loginDeps, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion, Context: context.Background()}
	err := newLoginCommand(deps).Execute(env, args)
	return codeFor(*env, err), out.String(), errOut.String()
}

// runLoginCLIStdin is the same run with a reader the test owns on stdin, the way
// runBoxCLIStdin drives `cafe box enroll --token -`: the stdin path is provable
// without redirecting the test binary's own standard input.
func runLoginCLIStdin(t *testing.T, deps loginDeps, stdin io.Reader, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &errOut, Stdin: stdin, Version: testVersion, Context: context.Background()}
	err := newLoginCommand(deps).Execute(env, args)
	return codeFor(*env, err), out.String(), errOut.String()
}

func TestLoginSplitsStreamsAndStoresToken(t *testing.T) {
	deps, _, ring := loginTestEnv(t)
	code, stdout, stderr := runLoginCLI(t, deps, "--email", "noel@cafaye.test", "--password", "secret123")
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr)
	}
	if !json.Valid([]byte(stdout)) {
		t.Fatalf("stdout not JSON: %s", stdout)
	}
	var doc map[string]any
	_ = json.Unmarshal([]byte(stdout), &doc)
	if doc["token_stored"] != true {
		t.Errorf("token_stored %v", doc["token_stored"])
	}
	if strings.Contains(stdout, loginTestToken) || strings.Contains(stderr, loginTestToken) {
		t.Error("token leaked to stream")
	}
	if !strings.Contains(stderr, "noel@cafaye.test") {
		t.Errorf("stderr missing email: %s", stderr)
	}
	if got := ring.entries["cafe/human/noel@cafaye.test"]; got != loginTestToken {
		t.Errorf("ring holds %q", got)
	}
}

func TestLoginTokenNeverPrinted(t *testing.T) {
	deps, _, ring := loginTestEnv(t)
	code, stdout, stderr := runLoginCLI(t, deps, "--email", "noel@cafaye.test", "--password", "secret123")
	if code != 0 {
		t.Fatalf("exit %d %s", code, stderr)
	}
	for _, s := range []string{stdout, stderr} {
		if strings.Contains(s, loginTestToken) {
			t.Error("token in stream")
		}
	}
	if ring.entries["cafe/human/noel@cafaye.test"] == "" {
		t.Error("ring empty")
	}
	// control: email IS printed
	if !strings.Contains(stdout, "noel@cafaye.test") {
		t.Errorf("doc missing email: %s", stdout)
	}
}

func TestLoginWithoutEndpointIsRefusal(t *testing.T) {
	deps, _, _ := loginTestEnv(t)
	deps.baseURL = ""
	t.Setenv(identityURLEnv, "")
	// also need to clear deps via withDefaults, so baseURL empty and env empty
	// construct fresh deps with no baseURL and no dial that would hide it
	deps2 := loginDeps{dir: t.TempDir(), ring: newAgentTestRing(), baseURL: ""}
	deps2.dial = deps.dial
	code, _, stderr := runLoginCLI(t, deps2, "--email", "a@b.test", "--password", "x")
	if code != exitFailure {
		t.Errorf("exit %d want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, identityURLEnv) {
		t.Errorf("stderr missing env: %s", stderr)
	}
	if !strings.Contains(stderr, "--base-url") {
		t.Errorf("stderr missing flag: %s", stderr)
	}
}

func TestLoginRejectsBadArgs(t *testing.T) {
	deps, _, _ := loginTestEnv(t)
	for _, args := range [][]string{
		{},
		{"--email", "a@b.test"},
		{"--password", "x"},
		{"--email", "a@b.test", "--password", "x", "extra"},
	} {
		code, _, _ := runLoginCLI(t, deps, args...)
		if code != exitUsage {
			t.Errorf("args %v exit %d want usage", args, code)
		}
	}
}

// The stdin path and the flag path are the same login: the value the pipe
// carried is the value the endpoint was called with, and the token that lands
// in the keyring is the one the flag path would have stored. The negative half
// is the secret itself: neither stream carries the password.
func TestLoginPasswordStdinStoresTheSameTokenAsTheFlagPath(t *testing.T) {
	flagDeps, flagFake, flagRing := loginTestEnv(t)
	code, _, _ := runLoginCLI(t, flagDeps, "--email", "noel@cafaye.test", "--password", "secret123")
	if code != 0 {
		t.Fatalf("flag path exit %d", code)
	}

	stdinDeps, stdinFake, stdinRing := loginTestEnv(t)
	code, stdout, stderr := runLoginCLIStdin(t, stdinDeps, strings.NewReader("secret123\n"), "--email", "noel@cafaye.test", "--password-stdin")
	if code != 0 {
		t.Fatalf("stdin path exit %d stderr %s", code, stderr)
	}

	if got := stdinRing.entries["cafe/human/noel@cafaye.test"]; got != loginTestToken {
		t.Errorf("stdin path stored %q, want the token the flag path stores (%q)", got, loginTestToken)
	}
	if stdinRing.entries["cafe/human/noel@cafaye.test"] != flagRing.entries["cafe/human/noel@cafaye.test"] {
		t.Error("the two paths stored different tokens")
	}
	// The value the pipe carried, trimmed of the writer's newline, is the value
	// the endpoint was called with.
	want := "login:noel@cafaye.test:secret123"
	if !containsCall(stdinFake.calls, want) {
		t.Errorf("endpoint calls %v, want %q", stdinFake.calls, want)
	}
	_ = flagFake
	for _, s := range []string{stdout, stderr} {
		if strings.Contains(s, "secret123") {
			t.Error("the password leaked to a stream")
		}
		if strings.Contains(s, loginTestToken) {
			t.Error("the token leaked to a stream")
		}
	}
	// control: the email IS printed, so a run that redacted everything would
	// not pass by leaking nothing at all.
	if !strings.Contains(stdout, "noel@cafaye.test") {
		t.Errorf("stdout missing email: %s", stdout)
	}
}

func containsCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}

// The two flags are the same secret on two channels, and the refusal names
// both: a caller who passed both has two ideas about where the password lives,
// and neither deserves to win silently.
func TestLoginPasswordAndPasswordStdinTogetherIsARefusal(t *testing.T) {
	deps, fake, ring := loginTestEnv(t)
	code, _, stderr := runLoginCLIStdin(t, deps, strings.NewReader("secret123\n"), "--email", "noel@cafaye.test", "--password", "secret123", "--password-stdin")
	if code != exitUsage {
		t.Fatalf("exit %d want usage; stderr %s", code, stderr)
	}
	if !strings.Contains(stderr, "--password") || !strings.Contains(stderr, "--password-stdin") {
		t.Errorf("refusal does not name both flags: %s", stderr)
	}
	if len(fake.calls) != 0 {
		t.Errorf("the endpoint was dialed anyway: %v", fake.calls)
	}
	if len(ring.entries) != 0 {
		t.Errorf("something was stored: %v", ring.entries)
	}
}

// An empty pipe is a refusal, not a login attempt with an empty password, and
// it is named as the flag it came from: a reader who pipes nothing has a broken
// command, and the sentence they read should say which channel was empty.
func TestLoginPasswordStdinEmptyIsARefusal(t *testing.T) {
	for name, stdin := range map[string]string{"empty": "", "blank": "  \n\t\n"} {
		t.Run(name, func(t *testing.T) {
			deps, fake, ring := loginTestEnv(t)
			code, _, stderr := runLoginCLIStdin(t, deps, strings.NewReader(stdin), "--email", "noel@cafaye.test", "--password-stdin")
			if code != exitUsage {
				t.Fatalf("exit %d want usage; stderr %s", code, stderr)
			}
			if !strings.Contains(stderr, "--password-stdin") {
				t.Errorf("refusal does not name the flag: %s", stderr)
			}
			if len(fake.calls) != 0 {
				t.Errorf("a login was attempted with nothing: %v", fake.calls)
			}
			if len(ring.entries) != 0 {
				t.Errorf("something was stored: %v", ring.entries)
			}
		})
	}
}

// loginOverHTTP drives the real httpHumanEndpoint against an answer the test
// writes: the MFA refusal lives in the parse, so the parse is what is proven
// rather than a fake that could agree with the implementation by construction.
func loginOverHTTP(t *testing.T, answer string) (int, string, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	deps := loginDeps{dir: t.TempDir(), ring: newAgentTestRing(), baseURL: srv.URL, dial: defaultHumanDial()}
	return runLoginCLI(t, deps, "--email", "mfa@cafaye.test", "--password", "secret123")
}

// A 202 with mfa_required is identity minting a challenge rather than a session.
// The refusal names it verbatim and says the remedy — the challenge is completed
// in the browser before a session can be minted — and it does NOT say "identity
// did not mint one", the sentence that sends a human to retry a password that
// already worked.
func TestLoginMFARequiredNamesMFAAndTheRemedy(t *testing.T) {
	code, stdout, stderr := loginOverHTTP(t, `{"mfa_required":true,"challenge":"chal_9f1c2a","expires_at":"2026-10-15T12:00:00Z"}`)
	if code != exitFailure {
		t.Fatalf("exit %d want failure; stdout %s stderr %s", code, stdout, stderr)
	}
	if stdout != "" {
		t.Errorf("a failed login printed a document: %s", stdout)
	}
	if !strings.Contains(stderr, "mfa_required") {
		t.Errorf("the refusal does not name mfa_required: %s", stderr)
	}
	if !strings.Contains(stderr, "browser") {
		t.Errorf("the refusal does not name the remedy (complete the challenge in the browser): %s", stderr)
	}
	if strings.Contains(stderr, "did not mint one") {
		t.Errorf("the misleading sentence survived: %s", stderr)
	}
}

// The hard error for a genuinely empty token is unchanged: a 200 with no token
// and no mfa_required is identity answering a document that is not a session,
// and the old sentence is the honest one there.
func TestLoginEmptyTokenStillSaysIdentityDidNotMintOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"expires_at":"2026-10-15T12:00:00Z"}`))
	}))
	t.Cleanup(srv.Close)
	deps := loginDeps{dir: t.TempDir(), ring: newAgentTestRing(), baseURL: srv.URL, dial: defaultHumanDial()}
	code, _, stderr := runLoginCLI(t, deps, "--email", "noel@cafaye.test", "--password", "secret123")
	if code != exitFailure {
		t.Fatalf("exit %d want failure", code)
	}
	if !strings.Contains(stderr, "identity did not mint one") {
		t.Errorf("the old error did not survive: %s", stderr)
	}
	if strings.Contains(stderr, "mfa_required") {
		t.Errorf("a non-MFA answer was described as MFA: %s", stderr)
	}
}

// The help's example command is paste-safe only if the variable it sets is the
// one the code reads. Both sides are measured against the same constant: the
// expected name is derived from identityURLEnv rather than written down a second
// time, because a second literal is exactly how this help came to tell users to
// set IDENTITY_URL while cafe login reads CAF_IDENTITY_URL — a command the code
// then refuses by name.
func TestLoginHelpExampleNamesTheEnvVarTheCodeReads(t *testing.T) {
	var out bytes.Buffer
	printHelp(&out, newLoginCommand(loginDeps{}))
	help := out.String()

	// Every environment-variable assignment the help teaches must name the
	// variable login actually reads. This is the shape that matters: the help
	// and the constant can never disagree about the variable.
	assignments := regexp.MustCompile(`[A-Z][A-Z0-9_]*=`).FindAllString(help, -1)
	if len(assignments) == 0 {
		t.Fatalf("the help's example sets no environment variable at all:\n%s", help)
	}
	for _, assignment := range assignments {
		if name := strings.TrimSuffix(assignment, "="); name != identityURLEnv {
			t.Errorf("the help example sets %s but cafe login reads %s", assignment, identityURLEnv)
		}
	}

	// The positive half: the exact runnable example is present, built from the
	// constant so the URL it shows (a self-hosted box, not the hosted endpoint)
	// is anchored to the variable the code reads.
	want := identityURLEnv + "=https://identity.cafaye.com cafe login --email you@example.com --password-stdin"
	if !strings.Contains(help, want) {
		t.Errorf("the example command is not runnable as pasted; want a line containing:\n%s\ngot:\n%s", want, help)
	}
}
