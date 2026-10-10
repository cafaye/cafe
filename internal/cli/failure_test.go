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

	out, err := runFailureCheck(t, []string{path})
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
	// variables are set, so the condition does not hold — and the report
	// renders holds and unknowns only, because a healthy box's nine quiet rows
	// are noise and the count line says how many there were.
	if strings.Contains(out, "oidc-not-mounted") {
		t.Error("the report names oidc-not-mounted against a document with all three variables set")
	}
	// The MFA row's fact IS in the document — identity's settings carry the
	// three OIDC names and no MFA key — so it holds, which is the whole shape
	// of the doc's row: absent, not broken.
	if !strings.Contains(out, "identity-mfa-routes-absent") {
		t.Errorf("the MFA row is not reported as holding:\n%s", out)
	}
	if !strings.Contains(out, "10 rule(s) evaluated") {
		t.Errorf("the report does not say how many rules it evaluated:\n%s", out)
	}
	if !strings.Contains(out, "caf failure: 10 rule(s), 9 condition(s) hold, 0 unknown") {
		t.Errorf("the summary line is not the count the report produced:\n%s", out)
	}
}

// Stdin is the default input, so the verb composes with the box-side half that
// does not exist yet: `caf box observe | caf failure check` is the shape the
// substrate packet completes. The reader is a seam parameter for the reason
// deployDeps.confirm is — a test that read os.Stdin would hang.
func TestFailureCheckReadsStdinByDefault(t *testing.T) {
	const doc = `{"services": ["gateway"], "replicas": {"gateway": 3}}`

	env := newTestEnv()
	c := newFailureCommand(failureDeps{stdin: strings.NewReader(doc)})
	if err := c.Execute(env, []string{"check", "-"}); err != nil {
		t.Fatalf("caf failure check -: %v", err)
	}
	out := written(t, env.Stdout)
	if !strings.Contains(out, "gateway-sessions-per-process") {
		t.Errorf("the document exhibits gateway on three replicas and the report does not say so:\n%s", out)
	}
}

// A document that cannot be read is a refusal naming the file, and one that is
// not JSON is a refusal naming the decode. "Your observation is wrong" is a
// sentence that sends a reader to a file they cannot fix.
func TestFailureCheckRefusesWhatItCannotRead(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent.json")
	if _, err := runFailureCheck(t, []string{absent}); err == nil {
		t.Fatal("an absent observation document produced a report")
	} else if !strings.Contains(err.Error(), "read the observation") {
		t.Errorf("err = %v, want it to name the read that failed", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "broken.json")
	writeFile(t, path, `{"services": `)
	if _, err := runFailureCheck(t, []string{path}); err == nil {
		t.Fatal("an observation document that is not JSON produced a report")
	} else if !strings.Contains(err.Error(), "decode the observation") {
		t.Errorf("err = %v, want it to name the decode that failed", err)
	}
}

// Bare `caf failure` answers rather than exiting 2, and an unknown verb names
// the one that exists. A parent with no verbs a reader can discover is a
// parent nobody uses.
func TestFailureAnswersItsOwnHelpAndNamesItsVerb(t *testing.T) {
	env := newTestEnv()
	if err := newFailureCommand(failureDeps{}).Execute(env, nil); err != nil {
		t.Fatalf("bare caf failure: %v", err)
	}
	if out := written(t, env.Stdout); !strings.Contains(out, "caf failure check") {
		t.Errorf("bare `caf failure` did not print the verb:\n%s", out)
	}

	err := newFailureCommand(failureDeps{}).Execute(newTestEnv(), []string{"lint", "-"})
	if err == nil || !strings.Contains(err.Error(), "unknown caf failure verb") {
		t.Errorf("err = %v, want it to name the verb that does not exist", err)
	}
}

// runFailureCheck runs the verb through the parent, the way a person types it,
// so the dispatch is under test rather than assumed.
func runFailureCheck(t *testing.T, args []string) (string, error) {
	t.Helper()
	env := newTestEnv()
	err := newFailureCommand(failureDeps{}).Execute(env, append([]string{"check"}, args...))
	return written(t, env.Stdout), err
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
