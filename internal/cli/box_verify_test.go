package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cafaye/cafe/internal/boxd"
	"github.com/cafaye/cafe/internal/secret"
	"github.com/cafaye/cafe/internal/verify"
)

// The ops half of `cafe box verify`, driven the way a person drives it: an
// enrolled box, an observe.report document on disk, a minted-value ledger in a
// scratch directory. The verification the CONTROL PLANE sees is the document
// this verb prints on stdout, and the human's next line is the prose on
// stderr — so both streams are asserted, and the value that was minted is
// searched for in every byte this command wrote.

// wholeObservation is the observe.report a box that is fine answers with. It
// carries the settings NAMES the substrate reads off the box's cluster, which
// is the only thing a caller may know about a secret.
func wholeObservation(t *testing.T) string {
	t.Helper()
	days := 120
	obs := boxd.Observation{
		SchemaVersion:   boxd.ObservationSchemaVersion,
		BoxID:           "box-under-test",
		DeployedVersion: "1.2.3",
		CollectedAt:     testObservedAt,
		Health:          boxd.Health{Status: "healthy", Phase: 7},
		Certificate:     boxd.Certificate{Status: "ok", DaysLeft: &days},
		Disk: boxd.Disk{
			Status: "ok", Path: "/", TotalBytes: 1000, FreeBytes: 700,
			UsedBytes: 300, UsedPercent: 30,
		},
		LastDrill: &boxd.DrillFact{Outcome: "passed"},
		Settings:  settingsForEveryServiceTheInventoryNames(),
	}
	path := filepath.Join(t.TempDir(), "observe.report.json")
	raw, err := json.Marshal(obs)
	if err != nil {
		t.Fatalf("marshal the observation: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write the observation: %v", err)
	}
	return path
}

// settingsForEveryServiceTheInventoryNames is what a box that holds
// everything looks like: every service the inventory names an entry for,
// carrying every key that service is expected to set. Building it from the
// inventory rather than writing it out is what stops the fixture going stale
// the day the installer's SECRET RECORD grows a key — a fixture that missed
// one would fail as "missing secret" on a box that is fine.
func settingsForEveryServiceTheInventoryNames() map[string][]string {
	out := map[string][]string{}
	for _, entry := range secret.Inventory() {
		switch {
		case entry.Deployment != "":
			out[entry.Deployment] = append(out[entry.Deployment], entry.Name)
		case entry.Namespace != "":
			// An entry that owns no Deployment but names a namespace is a
			// Secret the substrate reads out of that namespace — the
			// Postgres role passwords, which rotate an ALTER ROLE rather
			// than restarting anything.
			out[entry.Namespace] = append(out[entry.Namespace], entry.Name)
		}
		// The rest is on neither surface: muse's vault key is not a cluster
		// Secret, and the verifier reports where it lives instead.
	}
	return out
}

var testObservedAt = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

// TestOpsVerifyOnAWholeBox is the row every other case is a mutation of: the
// box holds what the inventory expects, no signal is off, the answer is ok on
// both streams and the exit is 0.
func TestOpsVerifyOnAWholeBox(t *testing.T) {
	deps, _, _ := boxTestEnv(t)
	enrollForVerify(t, deps)
	stateDir := t.TempDir()

	code, stdout, stderr := runBoxCLIWithDeps(t, deps, "verify", "--observe", wholeObservation(t), "--state-dir", stateDir)
	if code != 0 {
		t.Fatalf("verify exited %d (stderr: %s)", code, stderr)
	}
	var doc struct {
		OK           bool                 `json:"ok"`
		Verification *verify.Verification `json:"verification"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	if doc.Verification == nil {
		t.Fatalf("no verification section in the document:\n%s", stdout)
	}
	if doc.Verification.Status != verify.StatusOK {
		t.Errorf("status = %q, want ok\n%s", doc.Verification.Status, stdout)
	}
	if !doc.OK {
		t.Error("the box's own report on itself did not verify")
	}
	if len(doc.Verification.Signals) != 3 {
		t.Errorf("signals = %d, want the three the edge frets about", len(doc.Verification.Signals))
	}
	if len(doc.Verification.Reasons) != 0 {
		t.Errorf("a whole box carries reasons: %+v", doc.Verification.Reasons)
	}
	if !strings.Contains(stderr, "verifies") {
		t.Errorf("stderr does not say the box verifies:\n%s", stderr)
	}
}

// TestOpsVerifyNamesTheHumanWhenDegraded is the third deliverable at the
// command layer: a degraded answer prints, on stderr, who reads the next
// line, where, and what changes it — and it is the report's OWN next line, so
// nobody has to SSH into the box to find out.
func TestOpsVerifyNamesTheHumanWhenDegraded(t *testing.T) {
	deps, _, _ := boxTestEnv(t)
	enrollForVerify(t, deps)

	// A box whose root filesystem is full and whose SMTP password is
	// nowhere: both signals and one secret are wrong.
	obs := boxd.Observation{
		SchemaVersion: boxd.ObservationSchemaVersion, BoxID: "box-under-test",
		CollectedAt: testObservedAt,
		Health:      boxd.Health{Status: "healthy", Phase: 7},
		Certificate: boxd.Certificate{Status: "ok"},
		Disk:        boxd.Disk{Status: "ok", Path: "/", TotalBytes: 1000, FreeBytes: 40, UsedBytes: 960, UsedPercent: 96},
		LastDrill:   &boxd.DrillFact{Outcome: "passed"},
		Settings:    map[string][]string{"courier": {"REDIS_PASSWORD"}},
	}
	path := filepath.Join(t.TempDir(), "observe.report.json")
	raw, _ := json.Marshal(obs)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write the observation: %v", err)
	}

	code, stdout, stderr := runBoxCLIWithDeps(t, deps, "verify", "--observe", path, "--state-dir", t.TempDir())
	if code == 0 {
		t.Fatalf("a degraded verify exits 0 (stderr: %s)", stderr)
	}
	if !strings.Contains(stderr, "health: degraded") {
		t.Errorf("stderr does not answer degraded:\n%s", stderr)
	}
	for _, want := range []string{
		"the operator on call for box",      // who reads the next line
		"the observe.report the edge polls", // where they read it
		"cafe reclaim",                      // what changes the disk signal
		"cafe deploy",                       // what changes a missing secret
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not name %q:\n%s", want, stderr)
		}
	}
	var doc struct {
		Verification *verify.Verification `json:"verification"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	if doc.Verification == nil || doc.Verification.Status != verify.StatusDegraded {
		t.Fatalf("the document does not answer degraded:\n%s", stdout)
	}
	for _, r := range doc.Verification.Reasons {
		if r.Human.Owner == "" || r.Human.Where == "" || r.Human.Action == "" {
			t.Errorf("reason %q elides the human in the document: %+v", r.Key, r.Human)
		}
	}
}

// TestAMintedValueIsRecordedAndNeverPrinted is the ledger at the command
// layer. The value arrives on stdin — never argv — and the assertion searches
// every byte this command wrote, both streams and the ledger file, for it.
// The control half asserts the NAME is present, because a command that
// redacted everything would pass by leaking nothing at all.
func TestAMintedValueIsRecordedAndNeverPrinted(t *testing.T) {
	deps, _, _ := boxTestEnv(t)
	enrollForVerify(t, deps)
	stateDir := t.TempDir()

	const value = "the-minted-SECRET_KEY_BASE-value"
	deps.stdin = strings.NewReader(value + "\n")
	code, stdout, stderr := runBoxCLIWithDeps(t, deps, "verify", "--mint", "SECRET_KEY_BASE", "--state-dir", stateDir)
	if code != 0 {
		t.Fatalf("mint exited %d (stderr: %s)", code, stderr)
	}

	written := stdout + stderr
	ledgerRaw, err := os.ReadFile(filepath.Join(stateDir, "minted-secrets.json"))
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	written += string(ledgerRaw)
	if strings.Contains(written, value) {
		t.Fatalf("the minted value reached a stream or the ledger:\n%s", written)
	}
	for _, want := range []string{"SECRET_KEY_BASE", "fingerprint"} {
		if !strings.Contains(written, want) {
			t.Errorf("the ledger does not carry %q:\n%s", want, ledgerRaw)
		}
	}

	// And a second box minting the same name is refused by name, which is
	// the whole point of the ledger.
	deps2, _, _ := boxTestEnv(t)
	other := enrollForVerify(t, deps2)
	if other == "" {
		t.Fatal("the second enrollment produced no box id")
	}
	deps2.dir = t.TempDir()
	// Re-enroll in a fresh directory so the box id is a different one, then
	// point the same ledger at it.
	deps2.stdin = strings.NewReader("a-second-value\n")
	code, _, stderr = runBoxCLIWithDeps(t, deps2, "verify", "--mint", "SECRET_KEY_BASE", "--state-dir", stateDir)
	_ = other
	if code == 0 {
		t.Fatalf("a second box minting the same name is not refused (stderr: %s)", stderr)
	}
	if !strings.Contains(stderr, "minted by another box") {
		t.Errorf("the refusal does not name the rule: %s", stderr)
	}
}

// TestOpsVerifyWithoutTheFlagsIsTheVerbItWas: no --observe, no --mint, no
// state-dir environment, and the document is the box's report on itself with
// no verification section — which is what keeps a script that already reads
// this verb's output reading it.
func TestOpsVerifyWithoutTheFlagsIsTheVerbItWas(t *testing.T) {
	t.Setenv("CAF_BOX_STATE_DIR", "")
	deps, _, _ := boxTestEnv(t)
	enrollForVerify(t, deps)
	code, stdout, stderr := runBoxCLI(t, deps, "verify")
	if code != 0 {
		t.Fatalf("verify exited %d (stderr: %s)", code, stderr)
	}
	if strings.Contains(stdout, "verification") {
		t.Errorf("the document grew a verification section nobody asked for:\n%s", stdout)
	}
	if !strings.Contains(stderr, "verifies") {
		t.Errorf("stderr changed shape:\n%s", stderr)
	}
}

// TestOpsVerifyOnABoxThatNeverAnsweredIsUnknown is the blind-spot rule at the
// command layer: an absent observation is not a failed one, so the verb exits
// 0 and says the box is unread rather than broken.
func TestOpsVerifyOnABoxThatNeverAnsweredIsUnknown(t *testing.T) {
	deps, _, _ := boxTestEnv(t)
	enrollForVerify(t, deps)
	code, stdout, stderr := runBoxCLIWithDeps(t, deps, "verify", "--state-dir", t.TempDir())
	if code != 0 {
		t.Fatalf("an unread box exits %d (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stderr, "unread rather than broken") {
		t.Errorf("stderr does not say the box is unread:\n%s", stderr)
	}
	var doc struct {
		Verification *verify.Verification `json:"verification"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	if doc.Verification == nil || doc.Verification.Status != verify.StatusUnknown {
		t.Fatalf("the document does not answer unknown:\n%s", stdout)
	}
}

// enrollForVerify enrolls through the real dispatcher and hands back the box
// id, so every case above runs against a box the enrollment flow actually
// wrote rather than a document a test assembled by hand.
func enrollForVerify(t *testing.T, deps boxDeps) string {
	t.Helper()
	code, stdout, stderr := runBoxCLI(t, deps, "enroll", "--token", boxTestToken)
	if code != 0 {
		t.Fatalf("enroll exited %d (stderr: %s)", code, stderr)
	}
	res := enrollResult(t, stdout)
	if res.Identity == nil || res.Identity.BoxID == "" {
		t.Fatalf("the enrollment wrote no identity: %s", stdout)
	}
	return res.Identity.BoxID
}

// runBoxCLIWithDeps drives the verb tree with an injected stdin, which is how
// a value reaches the command without ever touching argv.
func runBoxCLIWithDeps(t *testing.T, deps boxDeps, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut strings.Builder
	env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion, Context: context.Background()}
	err := newBoxCommand(deps).Execute(env, args)
	return codeFor(*env, err), out.String(), errOut.String()
}
