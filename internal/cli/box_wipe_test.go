package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/cafaye/cafe/internal/setup"
)

// wipeSim is a box, minus the SSH, for the CLI-level tests. It records every
// command it is handed and answers the two read-only probes a wipe needs plus
// the applies it performs. It is deliberately smaller than internal/setup's
// fakeHost: the walk's mechanics are proved there against a real filesystem and
// a real unit table, and what is proved HERE is the CLI's shape — the
// confirmation, the refusal, the dry run — which is about which commands reach
// the host at all.
type wipeSim struct {
	mu       sync.Mutex
	isBox    bool
	applies  int
	cleared  map[string]bool
	commands []string
}

func newWipeSim() *wipeSim { return &wipeSim{isBox: true, cleared: map[string]bool{}} }

func (s *wipeSim) Run(_ context.Context, command string, _ []byte) (setup.Output, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, command)
	switch {
	case strings.Contains(command, "cafe-box-wipe-fingerprint"):
		if s.isBox {
			return setup.Output{Code: 0, Stdout: "/var/lib/cafaye"}, nil
		}
		return setup.Output{Code: 0, Stdout: ""}, nil
	case strings.Contains(command, "sudo -nl usermod"):
		return setup.Output{Code: 0, Stdout: "yes"}, nil
	case strings.Contains(command, `[ "${EUID:-$(id -u)}" -eq 0 ]`):
		return setup.Output{Code: 0, Stdout: "sudo"}, nil
	case isWipeApply(command):
		if tag := wipeStepTag(command); tag != "" {
			s.cleared[tag] = true
		}
		s.applies++
		return setup.Output{Code: 0}, nil
	}
	// A check: the artifact is gone if its apply has run, so the post-apply
	// re-check that a wipe performs agrees with the state the sim keeps.
	if tag := wipeStepTag(command); tag != "" && s.cleared[tag] {
		return setup.Output{Code: 0}, nil
	}
	return setup.Output{Code: 1}, nil
}

// wipeStepTag names the plan step a command belongs to, so the sim can answer
// the post-apply re-check from the state an apply left. It matches on the
// literals the plan uses rather than the plan's unexported constants, which is
// exactly why the plan's commands carry distinctive paths.
func wipeStepTag(command string) string {
	switch {
	case strings.Contains(command, "/usr/local/bin/k3s") || strings.Contains(command, "k3s-uninstall.sh"):
		return "k3s"
	case strings.Contains(command, "/etc/docker") || strings.Contains(command, "apt-get purge") || strings.Contains(command, "docker.socket"):
		return "docker"
	case strings.Contains(command, "cafe-boxd.service"):
		return "unit"
	case strings.Contains(command, "/usr/local/bin/cafe"):
		return "binary"
	case strings.Contains(command, "/etc/cafe"):
		return "state"
	case strings.Contains(command, "cafe-boxd"):
		return "daemon"
	}
	return ""
}

func (s *wipeSim) Write(context.Context, string, []byte, os.FileMode) error { return nil }

// isWipeApply recognises a destructive command by the verbs the plan uses. It is
// the CLI test's own predicate rather than the plan's, so a plan that stopped
// removing anything would show up as an applies count that went to zero.
func isWipeApply(command string) bool {
	for _, marker := range []string{"rm -f", "rm -rf", "systemctl disable --now", "apt-get purge"} {
		if strings.Contains(command, marker) {
			return true
		}
	}
	return false
}

func wipeDepsFor(host setup.Host, confirm func(string) (bool, error)) boxDeps {
	return boxDeps{wipe: boxWipeDeps{host: host, confirm: confirm}}
}

// A declined confirmation removes nothing, exits non-zero, and never reaches
// the host's destructive half. The prompt is asked once.
func TestBoxWipeDeclinedRemovesNothingThroughTheRouter(t *testing.T) {
	sim := newWipeSim()
	asked := 0
	deps := wipeDepsFor(sim, func(string) (bool, error) {
		asked++
		return false, nil
	})

	var out, errOut strings.Builder
	env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion}
	err := newBoxCommand(deps).Execute(env, []string{"wipe", "--json", "box.test"})
	if !errors.Is(err, errReported) {
		t.Fatalf("err = %v, want errReported", err)
	}
	if asked != 1 {
		t.Errorf("the operator was asked %d times, want 1", asked)
	}
	if sim.applies != 0 {
		t.Errorf("a declined wipe ran %d destructive commands", sim.applies)
	}
	if !strings.Contains(errOut.String(), "nothing was removed") {
		t.Errorf("stderr does not say nothing was removed:\n%s", errOut.String())
	}
	var doc setup.WipeResult
	if err := json.Unmarshal([]byte(out.String()), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out.String())
	}
	for _, step := range doc.Steps {
		if step.Action != setup.ActionPlanned {
			t.Errorf("step %s = %s, want %s", step.Name, step.Action, setup.ActionPlanned)
		}
	}
}

// --yes is the confirmation that says yes, and it is the only difference: the
// walk runs, the destructive commands reach the host, and the document carries
// the KEPT list.
func TestBoxWipeYesRemovesThroughTheRouter(t *testing.T) {
	sim := newWipeSim()
	deps := wipeDepsFor(sim, nil) // nil: never called because --yes wins

	var out, errOut strings.Builder
	env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion}
	if err := newBoxCommand(deps).Execute(env, []string{"wipe", "--yes", "--json", "box.test"}); err != nil {
		t.Fatalf("cafe box wipe --yes: %v\n%s", err, errOut.String())
	}
	if sim.applies != 6 {
		t.Errorf("the wipe ran %d destructive commands, want 6 (daemon, unit, binary, state, k3s, docker)", sim.applies)
	}
	var doc setup.WipeResult
	if err := json.Unmarshal([]byte(out.String()), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out.String())
	}
	if !doc.OK() {
		t.Fatalf("the wipe refused at %s", doc.FailedStep())
	}
	if len(doc.Kept) != 2 || doc.Kept[0] != setup.SSHAuthorizedKeys || doc.Kept[1] != setup.CafayeReleases {
		t.Errorf("the document's kept list = %v, want the keys and the releases", doc.Kept)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out.String()), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["kept"]; !ok {
		t.Errorf("the JSON document has no kept field:\n%s", out.String())
	}
}

// A dry run prints the plan and removes nothing, and it does not ask: the plan
// is the answer to "what would this do", and a prompt after it would be a
// question nobody wanted.
func TestBoxWipeDryRunThroughTheRouter(t *testing.T) {
	sim := newWipeSim()
	asked := 0
	deps := wipeDepsFor(sim, func(string) (bool, error) {
		asked++
		return true, nil
	})

	var out, errOut strings.Builder
	env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion}
	if err := newBoxCommand(deps).Execute(env, []string{"wipe", "--dry-run", "--json", "box.test"}); err != nil {
		t.Fatalf("cafe box wipe --dry-run: %v", err)
	}
	if asked != 0 {
		t.Errorf("a dry run asked the operator %d times", asked)
	}
	if sim.applies != 0 {
		t.Errorf("a dry run ran %d destructive commands", sim.applies)
	}
	var doc setup.WipeResult
	if err := json.Unmarshal([]byte(out.String()), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out.String())
	}
	if !doc.DryRun {
		t.Error("the document does not say this was a dry run")
	}
	for _, step := range doc.Steps {
		if step.Action != setup.ActionPlanned {
			t.Errorf("step %s = %s, want %s", step.Name, step.Action, setup.ActionPlanned)
		}
	}
}

// A host that is not a box is refused BEFORE the operator is asked, so a stray
// `cafe box wipe` cannot remove a stranger's k3s — and it is refused without
// --yes, which is the case that matters.
func TestBoxWipeOnAStrangerHostIsRefusedBeforeThePrompt(t *testing.T) {
	sim := newWipeSim()
	sim.isBox = false
	deps := wipeDepsFor(sim, func(string) (bool, error) {
		t.Error("the operator was asked to confirm a host that is not a box")
		return true, nil
	})

	var out, errOut strings.Builder
	env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion}
	err := newBoxCommand(deps).Execute(env, []string{"wipe", "--json", "box.test"})
	if !errors.Is(err, errReported) {
		t.Fatalf("err = %v, want errReported", err)
	}
	if sim.applies != 0 {
		t.Errorf("a refused wipe ran %d destructive commands", sim.applies)
	}
	var doc setup.WipeResult
	if err := json.Unmarshal([]byte(out.String()), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out.String())
	}
	if doc.FailedStep() != setup.StepWipeFingerprint {
		t.Errorf("failed step = %q, want %q", doc.FailedStep(), setup.StepWipeFingerprint)
	}
}

// The verb exists, takes exactly one argument, and its help says what it keeps
// and what it refuses — the three things a person needs before running a
// command that uninstalls a cluster.
func TestBoxWipeIsAVerbOfBox(t *testing.T) {
	found := false
	for _, v := range boxVerbs(boxDeps{}) {
		if v.name == "wipe" {
			found = true
			if v.cmd.Name != "box wipe" {
				t.Errorf("the verb's command is named %q, want %q", v.cmd.Name, "box wipe")
			}
			for _, want := range []string{
				"WHAT IT KEEPS", setup.SSHAuthorizedKeys, setup.CafayeReleases,
				"WHAT IT REFUSES", "--yes", "--dry-run",
			} {
				if !strings.Contains(v.cmd.LongHelp, want) {
					t.Errorf("the help does not mention %q", want)
				}
			}
		}
	}
	if !found {
		t.Fatal("cafe box has no wipe verb")
	}
	var out, errOut strings.Builder
	env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion}
	if err := newBoxCommand(boxDeps{}).Execute(env, []string{"wipe"}); !errors.Is(err, errUsage) {
		t.Errorf("err = %v, want errUsage for a missing host", err)
	}
}
