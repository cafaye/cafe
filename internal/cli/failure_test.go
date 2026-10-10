package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The verb is driven end to end against a document a test writes, because the
// thing being proved is the whole path: a JSON observation in, the library's
// ten rules over it, and the report out — including the two claims the command
// makes in its help, that a rule it cannot answer goes UNKNOWN and that it is
// never reported as a pass.
func TestFailureCheckEvaluatesTheLibraryAgainstADocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observation.json")
	writeFile(t, path, `{
	  "services": ["courier", "identity", "gateway", "kit", "muse"],
	  "settings": {
	    "courier": ["COURIER_SECRET_BOX_KEY"],
	    "identity": ["OIDC_ISSUER", "OIDC_SIGNING_KEY", "OIDC_SIGNING_KEY_ID"]
	  },
	  "probes": [{"service": "courier", "path": "/readyz", "status": 503, "latency_ms": 4400}],
	  "healthchecks": {"kit": "/up"},
	  "databases": [{"name": "kit", "service": "courier"}, {"name": "kit", "service": "identity"}],
	  "replicas": {"gateway": 2},
	  "instrumented": ["courier", "identity", "gateway", "kit"],
	  "collectors": [],
	  "outbox": {"courier": 900},
	  "publishers": []
	}`)

	out, err := runFailureCheck(t, path)
	if err != nil {
		t.Fatalf("caf failure check: %v", err)
	}

	for _, want := range []string{
		"courier-readyz-slow-when-db-gone",
		"kit-proxy-healthchecks-up",
		"postgres-two-pockets",
		"traces-going-nowhere",
		"outbox-rows-piling-up",
		"gateway-sessions-per-process",
		"courier-password-reset-503",
		"secrets-frozen-cannot-rotate",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not name %s, which the document exhibits", want)
		}
	}
	// The OIDC row is the one the document does not exhibit: all three
	// variables are set, so the condition does not hold and is not listed.
	if strings.Contains(out, "oidc-not-mounted") {
		t.Error("the report names oidc-not-mounted against a document with all three variables set")
	}
	// The MFA row's observation fact is absent, so it is UNKNOWN and says so
	// rather than passing.
	if !strings.Contains(out, "identity-mfa-routes-absent") ||
		!strings.Contains(out, "the observation does not carry Settings") {
		t.Errorf("the MFA row is neither held nor named unanswerable:\n%s", out)
	}
	if !strings.Contains(out, "10 rule(s) evaluated") {
		t.Errorf("the report does not say how many rules it evaluated:\n%s", out)
	}
}

// A document that cannot be read is a refusal naming the file, and stdin is the
// default so the verb composes with the box-side half that does not exist yet.
func TestFailureCheckRefusesWhatItCannotRead(t *testing.T) {
	if _, err := runFailureCheck(t, filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("an absent observation document produced a report")
	} else if !strings.Contains(err.Error(), "read the observation") {
		t.Errorf("err = %v, want it to name the read that failed", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "broken.json")
	writeFile(t, path, "{\"services\": ")
	env := newTestEnv()
	env.Stdin = strings.NewReader("")
	_ = path
	c := lookupCommandOrFail(t, "failure check")
	if err := c.Execute(env, []string{path}); err == nil {
		t.Fatal("an observation document that is not JSON produced a report")
	} else if !strings.Contains(err.Error(), "decode the observation") {
		t.Errorf("err = %v, want it to name the decode that failed", err)
	}
}

func runFailureCheck(t *testing.T, path string) (string, error) {
	t.Helper()
	env := newTestEnv()
	c := lookupCommandOrFail(t, "failure check")
	err := c.Execute(env, []string{path})
	return env.Stdout.(*strings.Builder).String(), err
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
