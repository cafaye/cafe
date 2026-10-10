package secret

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// recordingRunner records every step it was handed and answers with whatever the
// case planted. There is no kubectl in this file, which is the point of the seam.
type recordingRunner struct {
	steps []Step
	// out is what kubectl is pretended to have said, per step, in order.
	out []string
	// failAt is the step index that fails, or -1 for none.
	failAt int
	// argv is every argv handed over, so a test can search it for a value.
	argv [][]string
}

func (r *recordingRunner) Run(_ context.Context, step Step) (string, error) {
	i := len(r.steps)
	r.steps = append(r.steps, step)
	r.argv = append(r.argv, step.Argv)
	out := ""
	if i < len(r.out) {
		out = r.out[i]
	}
	if r.failAt == i {
		return out, errors.New("kubectl: something went wrong")
	}
	return out, nil
}

func TestARefusalNamesTheReasonAndTheCitation(t *testing.T) {
	for _, name := range []string{"MUSE_VAULT_KEY", "OIDC_SIGNING_KEY", "COURIER_SMTP_PASSWORD"} {
		t.Run(name, func(t *testing.T) {
			entry, err := Find(name)
			if err != nil {
				t.Fatal(err)
			}
			_, err = PlanRotation(entry)
			var refusal Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("PlanRotation(%s) err = %v, want a Refusal", name, err)
			}
			message := refusal.Error()
			if !strings.Contains(message, refusal.Reason) {
				t.Errorf("the refusal does not carry its own reason:\n%s", message)
			}
			if !strings.Contains(message, refusal.Source) {
				t.Errorf("the refusal does not carry its citation %q:\n%s", refusal.Source, message)
			}
			if !strings.Contains(message, "refusing to rotate") {
				t.Errorf("the refusal does not say it refused:\n%s", message)
			}
		})
	}
}

// THE LOAD-BEARING ONE: a generated value never reaches argv.
//
// argv is visible to every other process on the box through /proc, so a rotation
// that put a fresh SECRET_KEY_BASE in argv would have published it to the box it
// was run to protect. The value is generated, the plan is built, and the argv of
// every step is searched for it.
func TestThePlanNeverPutsAValueInArgv(t *testing.T) {
	for _, name := range []string{"COURIER_SECRET_BOX_KEY", "SECRET_KEY_BASE", "password"} {
		t.Run(name, func(t *testing.T) {
			runner := &recordingRunner{failAt: -1}
			if _, err := Rotate(context.Background(), name, runner, nil); err != nil {
				t.Fatalf("Rotate(%s) = %v", name, err)
			}
			if len(runner.steps) == 0 {
				t.Fatal("no steps ran, so there was nothing to search")
			}
			for _, step := range runner.steps {
				joined := strings.Join(step.Argv, " ")
				if step.value == "" && step.Stdin == "" {
					continue // a step that writes nothing
				}
				if strings.Contains(joined, string(step.value)) {
					t.Errorf("argv %q carries the generated value", joined)
				}
				// And the redaction marker is what a dry run would print.
				if step.Redacted == step.Stdin && step.Stdin != "" {
					t.Errorf("step %q prints its document unredacted", step.Name)
				}
			}
		})
	}
}

// The presence check is templated to answer with key NAMES only. The template is
// asserted rather than trusted, because a template that had to be carefully
// written not to leak would be a discipline, and this one is written so that it
// cannot.
func TestThePresenceCheckAnswersWithKeyNamesOnly(t *testing.T) {
	entry, err := Find("COURIER_SECRET_BOX_KEY")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanRotation(entry)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, step := range plan.Steps {
		for _, a := range step.Argv {
			if strings.Contains(a, presenceTemplate) {
				found = true
				// A ACTION prints $k and never $v: the range binds $v because
				// go-template's two-variable form requires it, and the binding
				// prints nothing. What reaches this process is the set of key
				// names, and this template is written so that it could not
				// print anything else.
				if strings.Contains(a, "{{$v}}") {
					t.Errorf("the presence template prints a value: %s", a)
				}
				if !strings.Contains(a, "{{$k}}") {
					t.Errorf("the presence template prints no key: %s", a)
				}
			}
		}
	}
	if !found {
		t.Error("no step carries the presence template")
	}
}

// The Redis password is rotatable, and the second write is what makes it one: a
// rotation that wrote redis-password and not gateway's REDIS_URL would take
// gateway down rather than rotate anything.
func TestTheRedisPasswordRotationWritesBothPlaces(t *testing.T) {
	runner := &recordingRunner{failAt: -1}
	if _, err := Rotate(context.Background(), "password", runner, nil); err != nil {
		t.Fatalf("Rotate(the Redis password) = %v", err)
	}
	var wrote, restarted []string
	for _, step := range runner.steps {
		switch {
		case strings.HasPrefix(step.Name, "replace "):
			wrote = append(wrote, step.Name)
		case strings.HasPrefix(step.Name, "restart "):
			restarted = append(restarted, step.Name)
		}
	}
	if len(wrote) != 2 {
		t.Fatalf("wrote %v, want two places", wrote)
	}
	if !strings.Contains(wrote[0], "data/redis-password") {
		t.Errorf("first write is %q, want the redis-password secret", wrote[0])
	}
	if !strings.Contains(wrote[1], "gateway/gateway-secrets key REDIS_URL") {
		t.Errorf("second write is %q, want gateway's REDIS_URL", wrote[1])
	}
	if len(restarted) != 2 {
		t.Fatalf("restarted %v, want two deployments", restarted)
	}
	// The second write's document carries the password inside the URL, and the
	// first write's carries it bare — so the two agree, which is the whole point.
	first := runner.steps[1].Stdin
	second := runner.steps[2].Stdin
	if !strings.Contains(first, string(runner.steps[1].value)) {
		t.Error("the redis-password document does not carry the generated value")
	}
	if !strings.Contains(second, "redis://:"+string(runner.steps[2].value)+"@redis.data.svc.cluster.local:6379") {
		t.Errorf("the REDIS_URL document does not embed the generated value:\n%s", second)
	}
}

// A dry run prints the redacted document, and the redacted one carries the marker
// and not the value.
func TestADryRunPrintsTheMarkerAndNotTheValue(t *testing.T) {
	entry, err := Find("SECRET_KEY_BASE")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanRotation(entry)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range plan.Steps {
		if step.Stdin == "" {
			continue
		}
		if strings.Contains(step.Redacted, redacted) == false {
			t.Errorf("step %q has no redaction marker:\n%s", step.Name, step.Redacted)
		}
	}
}

func TestRotateRefusesBeforeItRunsAnything(t *testing.T) {
	runner := &recordingRunner{failAt: -1}
	_, err := Rotate(context.Background(), "MUSE_VAULT_KEY", runner, nil)
	var refusal Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want a Refusal", err)
	}
	if len(runner.steps) != 0 {
		t.Errorf("%d steps ran before the refusal, want 0", len(runner.steps))
	}
}

func TestRotateAnswersWithTheReportOfWhatChanged(t *testing.T) {
	entry, err := Find("COURIER_SECRET_BOX_KEY")
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{failAt: -1}
	plan, err := Rotate(context.Background(), entry.Name, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Report, "courier/courier-secrets key COURIER_SECRET_BOX_KEY") {
		t.Errorf("report = %q, want it to name what was written", plan.Report)
	}
	if !strings.Contains(plan.Report, "courier/deployment/courier") {
		t.Errorf("report = %q, want it to name what was restarted", plan.Report)
	}
}

func TestARotatedEntryIsRecordedInTheState(t *testing.T) {
	dir := t.TempDir()
	state, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := Find("SECRET_KEY_BASE")
	if err != nil {
		t.Fatal(err)
	}
	state.MarkRotated(entry, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC))
	if err := Save(dir, state); err != nil {
		t.Fatal(err)
	}
	again, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.Last[entry.StateKey()]; !ok {
		t.Fatalf("the rotation was not recorded under %q", entry.StateKey())
	}
	// And the state file holds no credential.
	raw := readFileString(dir)
	if strings.Contains(raw, "SECRET_KEY_BASE") == false {
		t.Error("the state does not name the entry")
	}
	for _, word := range []string{"base64", "==", "bytes"} {
		if strings.Contains(raw, word) {
			t.Errorf("the state file carries %q, which looks like a value", word)
		}
	}
}

// readFileString reads the state file, for the "it holds no credential" half.
func readFileString(dir string) string {
	raw, err := os.ReadFile(StatePath(dir))
	if err != nil {
		return ""
	}
	return string(raw)
}

func writeFile(dir, content string) error {
	return os.WriteFile(StatePath(dir), []byte(content), 0o600)
}

func TestAMalformedStateFileIsAnErrorRatherThanAFreshState(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir, "{ not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("Load(a malformed state) = nil error, want one: a schedule nobody can read never runs again")
	}
}
