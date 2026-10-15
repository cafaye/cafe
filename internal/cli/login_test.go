package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
