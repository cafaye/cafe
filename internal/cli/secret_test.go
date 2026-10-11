package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cafaye/cafe/internal/secret"
)

// fakeSecretRunner records the steps it was handed and answers with whatever
// the case planted. There is no kubectl in this file either, for the reason
// there is none in internal/secret's.
type fakeSecretRunner struct {
	steps []secret.Step
	fail  bool
}

func (f *fakeSecretRunner) Run(_ context.Context, step secret.Step) (string, error) {
	f.steps = append(f.steps, step)
	if f.fail {
		return "", errors.New("kubectl: something went wrong")
	}
	return "", nil
}

// testSecretDeps is the wiring a test drives: a clock that does not move, a
// runner that records, and a state directory the test owns.
func testSecretDeps(t *testing.T) (secretDeps, *fakeSecretRunner) {
	t.Helper()
	runner := &fakeSecretRunner{}
	return secretDeps{
		runner:   runner,
		now:      func() time.Time { return time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC) },
		stateDir: t.TempDir(),
	}, runner
}

func TestSecretInventoryEmitsOneJSONDocument(t *testing.T) {
	deps, _ := testSecretDeps(t)
	c := newSecretInventoryCommand(deps)
	env := newTestEnv()

	if err := c.Execute(env, nil); err != nil {
		t.Fatalf("caf secret inventory: %v", err)
	}
	var doc struct {
		Counts  map[string]int `json:"counts"`
		Entries []struct {
			Name   string `json:"name"`
			Class  string `json:"class"`
			Reason string `json:"reason"`
			Source string `json:"source"`
		} `json:"entries"`
		Due []string `json:"due"`
	}
	if err := json.Unmarshal([]byte(written(t, env.Stdout)), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, written(t, env.Stdout))
	}
	if doc.Counts["rotatable"] != 3 {
		t.Errorf("counts[rotatable] = %d, want 3", doc.Counts["rotatable"])
	}
	// A box that has never rotated anything has everything due, which is the
	// honest first answer rather than an empty list.
	if len(doc.Due) != 3 {
		t.Errorf("due = %v, want the three rotatable entries", doc.Due)
	}
	// Name, class, reason — and the reason is not a bare word.
	for _, e := range doc.Entries {
		if e.Name == "" || e.Class == "" || e.Reason == "" {
			t.Errorf("entry %+v is missing a name, a class or a reason", e)
		}
	}
}

// The inventory never prints a value, and the way that is proved is by the
// strongest version of the claim available: rotate an entry, take the value the
// rotation actually generated, and assert the inventory document does not
// contain it.
func TestSecretInventoryNeverPrintsAValue(t *testing.T) {
	deps, runner := testSecretDeps(t)
	rot := newSecretRotateCommand(deps)
	if err := rot.Execute(newTestEnv(), []string{"--yes", "COURIER_SECRET_BOX_KEY"}); err != nil {
		t.Fatal(err)
	}
	var generated string
	for _, step := range runner.steps {
		// The value travels in the document on stdin, so it is read back out of
		// there by the one party that ever had it: the runner.
		generated = valueFromManifest(step.Stdin)
		if generated != "" {
			break
		}
	}
	if generated == "" {
		t.Fatal("the rotation generated no value, so there is nothing to search for")
	}

	inv := newSecretInventoryCommand(deps)
	env := newTestEnv()
	if err := inv.Execute(env, nil); err != nil {
		t.Fatal(err)
	}
	out := written(t, env.Stdout)
	if strings.Contains(out, generated) {
		t.Errorf("the inventory carries the value the rotation generated:\n%s", out)
	}
	// The control half: the inventory DOES print what the operator came for,
	// so a build that redacted everything would not pass by leaking nothing.
	for _, want := range []string{"MUSE_VAULT_KEY", "not-rotatable", "OIDC_SIGNING_KEY", "rotatable", "reason", "source"} {
		if !strings.Contains(out, want) {
			t.Errorf("the inventory does not carry %q", want)
		}
	}
}

// THE NAMED REFUSAL. `caf secret rotate MUSE_VAULT_KEY` exits non-zero, names
// the entry, names the class, and carries the reason and the citation. This is
// the packet's "a named refusal for the rest".
func TestSecretRotateRefusesTheNotRotatableClassByName(t *testing.T) {
	for _, name := range []string{"MUSE_VAULT_KEY", "OIDC_SIGNING_KEY"} {
		t.Run(name, func(t *testing.T) {
			deps, runner := testSecretDeps(t)
			c := newSecretRotateCommand(deps)
			env := newTestEnv()

			err := c.Execute(env, []string{name})
			if !errors.Is(err, errReported) {
				t.Fatalf("err = %v, want errReported (exit 1, the refusal IS the report)", err)
			}
			out := written(t, env.Stdout)
			if !strings.Contains(out, `"name": "`+name+`"`) {
				t.Errorf("stdout does not name the entry:\n%s", out)
			}
			if !strings.Contains(out, "not-rotatable") {
				t.Errorf("stdout does not name the class:\n%s", out)
			}
			if !strings.Contains(out, "reason") || !strings.Contains(out, "source") {
				t.Errorf("stdout carries no reason or citation:\n%s", out)
			}
			// The human prose is on stderr, because a person reading a terminal
			// should not have to parse JSON.
			if !strings.Contains(written(t, env.Stderr), "refusing to rotate "+name) {
				t.Errorf("stderr has no prose:\n%s", written(t, env.Stderr))
			}
			if len(runner.steps) != 0 {
				t.Errorf("%d steps ran before the refusal, want 0", len(runner.steps))
			}
		})
	}
}

// The rotation runs the plan, records the rotation in the state, and reports
// what it changed. The value never reaches the report.
func TestSecretRotateRunsAndRecords(t *testing.T) {
	deps, runner := testSecretDeps(t)
	c := newSecretRotateCommand(deps)
	env := newTestEnv()

	if err := c.Execute(env, []string{"--yes", "COURIER_SECRET_BOX_KEY"}); err != nil {
		t.Fatalf("caf secret rotate: %v", err)
	}
	if len(runner.steps) == 0 {
		t.Fatal("no steps ran")
	}
	out := written(t, env.Stdout)
	if !strings.Contains(out, "COURIER_SECRET_BOX_KEY") {
		t.Errorf("the report does not name what was rotated:\n%s", out)
	}
	// The state now holds the rotation, which is what makes the next run's
	// "due" a fact rather than a guess.
	state, err := secret.Load(deps.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := secret.Find("COURIER_SECRET_BOX_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Last[entry.StateKey()]; !ok {
		t.Error("the rotation was not recorded")
	}
}

// A bare `caf secret rotate` rotates what is DUE and refuses everything else, so
// the run is partial and the exit says so.
func TestSecretRotateWithNoNameRotatesWhatIsDue(t *testing.T) {
	deps, runner := testSecretDeps(t)
	c := newSecretRotateCommand(deps)
	env := newTestEnv()

	if err := c.Execute(env, []string{"--yes"}); err == nil {
		t.Fatal("err = nil, want errReported: the run refused the not-rotatable class as well as rotating the due ones")
	}
	if len(runner.steps) == 0 {
		t.Fatal("nothing was rotated")
	}
	// The refusal report names every entry outside the rotatable class, which
	// is the inventory's second half and the answer to "what will never
	// rotate".
	out := written(t, env.Stdout)
	for _, want := range []string{"MUSE_VAULT_KEY", "OIDC_SIGNING_KEY", "conditional"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal report does not carry %q:\n%s", want, out)
		}
	}
}

func TestSecretRotateDryRunPrintsTheRedactedDocument(t *testing.T) {
	deps, runner := testSecretDeps(t)
	c := newSecretRotateCommand(deps)
	env := newTestEnv()

	if err := c.Execute(env, []string{"--dry-run", "COURIER_SECRET_BOX_KEY"}); err != nil {
		t.Fatalf("caf secret rotate --dry-run: %v", err)
	}
	if len(runner.steps) != 0 {
		t.Errorf("a dry run executed %d steps, want 0", len(runner.steps))
	}
	out := written(t, env.Stdout)
	if !strings.Contains(out, "<generated value, never printed>") {
		t.Errorf("the dry run does not print the redaction marker:\n%s", out)
	}
	if !strings.Contains(out, "nothing was changed") {
		t.Errorf("the dry run does not say it changed nothing:\n%s", out)
	}
}

// A dry run refuses exactly what the real run refuses. One that was more
// permissive would be worse than no dry run at all.
func TestSecretRotateDryRunRefusesWhatTheRealRunRefuses(t *testing.T) {
	deps, runner := testSecretDeps(t)
	c := newSecretRotateCommand(deps)
	env := newTestEnv()

	err := c.Execute(env, []string{"--dry-run", "MUSE_VAULT_KEY"})
	if !errors.Is(err, errReported) {
		t.Fatalf("err = %v, want errReported", err)
	}
	if len(runner.steps) != 0 {
		t.Error("the dry run executed something")
	}
	if !strings.Contains(written(t, env.Stdout), "MUSE_VAULT_KEY") {
		t.Errorf("the dry run does not name the refusal:\n%s", written(t, env.Stdout))
	}
}

func TestSecretRotateUnknownNameIsAUsageError(t *testing.T) {
	deps, _ := testSecretDeps(t)
	c := newSecretRotateCommand(deps)
	env := newTestEnv()

	err := c.Execute(env, []string{"NOT_A_SECRET"})
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage (exit 2: caf was invoked wrongly)", err)
	}
}

// Bare `caf secret` is a question, not a mistake, so it answers with its verbs
// instead of exiting 2.
func TestSecretWithNoVerbAnswers(t *testing.T) {
	c := newSecretCommand(secretDeps{})
	env := newTestEnv()
	if err := c.Execute(env, nil); err != nil {
		t.Fatalf("bare caf secret: %v", err)
	}
	out := written(t, env.Stdout)
	for _, want := range []string{"secret inventory", "secret rotate"} {
		if !strings.Contains(out, want) {
			t.Errorf("the verb table does not list %q:\n%s", want, out)
		}
	}
}

func TestSecretUnknownVerbNamesTheVerbs(t *testing.T) {
	c := newSecretCommand(secretDeps{})
	env := newTestEnv()
	err := c.Execute(env, []string{"nope"})
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
	if !strings.Contains(err.Error(), "inventory") || !strings.Contains(err.Error(), "rotate") {
		t.Errorf("err = %q, want it to name the verbs", err)
	}
}

// The whole point, in one case: the inventory names the frozen class, the
// rotation refuses it, and the two answers agree because they come from one
// table.
func TestTheInventoryAndTheRefusalComeFromOneTable(t *testing.T) {
	inv := newSecretInventoryCommand(secretDeps{stateDir: t.TempDir(), now: time.Now})
	env := newTestEnv()
	if err := inv.Execute(env, nil); err != nil {
		t.Fatal(err)
	}
	rot := newSecretRotateCommand(secretDeps{runner: &fakeSecretRunner{}, stateDir: t.TempDir(), now: time.Now})
	env2 := newTestEnv()
	if err := rot.Execute(env2, []string{"--dry-run", "MUSE_VAULT_KEY"}); err != nil && !errors.Is(err, errReported) {
		t.Fatal(err)
	}
	// The reason the refusal quotes is the reason the inventory printed.
	inventoryReason := extractJSONField(written(t, env.Stdout), "MUSE_VAULT_KEY", "reason")
	refusalReason := extractJSONField(written(t, env2.Stdout), "MUSE_VAULT_KEY", "reason")
	if inventoryReason == "" || refusalReason == "" {
		t.Fatalf("no reason found: inventory %q, refusal %q", inventoryReason, refusalReason)
	}
	if inventoryReason != refusalReason {
		t.Errorf("the two disagree:\ninventory: %s\nrefusal:  %s", inventoryReason, refusalReason)
	}
}

// valueFromManifest pulls the generated value back out of an apply document. It
// is the mirror of the discipline: the value only ever exists in that document,
// and only the runner that received it can read it back.
func valueFromManifest(document string) string {
	lines := strings.Split(document, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "stringData:" && i+1 < len(lines) {
			parts := strings.SplitN(lines[i+1], ": ", 2)
			if len(parts) == 2 {
				return parts[1]
			}
		}
	}
	return ""
}

// extractJSONField pulls one string field out of the first object whose "name"
// matches, so a test can compare what two commands said about one entry.
func extractJSONField(document, name, field string) string {
	var doc struct {
		Entries []map[string]any `json:"entries"`
		Refused []map[string]any `json:"refused"`
	}
	if err := json.Unmarshal([]byte(document), &doc); err != nil {
		return ""
	}
	for _, group := range [][]map[string]any{doc.Entries, doc.Refused} {
		for _, entry := range group {
			if entry["name"] == name {
				if v, ok := entry[field].(string); ok {
					return v
				}
			}
		}
	}
	return ""
}
