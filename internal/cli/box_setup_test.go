package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/cafaye/cafe/internal/box"
	"github.com/cafaye/cafe/internal/setup"
)

// The end of the packet: `cafe box setup <host>` driven through the real
// router, with a host that is a small simulator of a box rather than a mock.
//
// The simulator is the interesting half. It holds a filesystem and a package
// list, it answers `docker -v` from the package list, and it performs the
// enrollment ON ITSELF — it runs the real internal/box store against a control
// plane that counts. So the assertions below are about what a real setup does:
// the keypair is made on the host, the capability is presented once, and a
// second run presents it zero times.

// boxHostSim is a box, minus the SSH. It keeps only the state a host keeps and
// implements the commands the plan runs, in the plan's own vocabulary.
type boxHostSim struct {
	mu sync.Mutex

	fs        map[string][]byte
	installed map[string]bool
	running   bool
	groups    map[string]bool
	enabled   map[string]bool

	// boxDir is where this host's own cafe keeps its state: the keyring, the
	// journal, box.json. It is the HOST's directory, and the controller has no
	// path into it.
	boxDir string
	// controlPlane counts redemptions, and is the same object the host's own
	// enroll calls.
	controlPlane *countingBoxControlPlane
	// ring holds the host's private key.
	ring *boxTestRing

	commands []simCommand
	writes   []simWrite
}

type simCommand struct {
	Command string
	Stdin   string
}

type simWrite struct {
	Path string
	Data []byte
}

func newBoxHostSim(t *testing.T, cp *countingBoxControlPlane) *boxHostSim {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "root", ".config", "cafe"), 0o700); err != nil {
		t.Fatal(err)
	}
	return &boxHostSim{
		fs:           map[string][]byte{},
		installed:    map[string]bool{},
		groups:       map[string]bool{},
		enabled:      map[string]bool{},
		boxDir:       filepath.Join(dir, "root", ".config", "cafe"),
		controlPlane: cp,
		ring:         newBoxTestRing(),
	}
}

// unameSm is what a host answers the arch preflight with, for the platform the
// sim is running on. Linux says aarch64 where Go says arm64, which is the
// whole translation the preflight exists to perform.
func unameSm() string {
	machine := runtime.GOARCH
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return "Darwin arm64"
	}
	if runtime.GOARCH == "amd64" {
		machine = "x86_64"
	} else if runtime.GOARCH == "arm64" {
		machine = "aarch64"
	}
	return "Linux " + machine
}

// Run answers one command. The interesting branch is the enrollment: rather
// than returning a canned document, it runs the REAL store, which is what makes
// the count below a count of real redemptions rather than of a fake's calls.
func (s *boxHostSim) Run(_ context.Context, command string, stdin []byte) (setup.Output, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, simCommand{Command: command, Stdin: string(stdin)})

	switch {
	case strings.Contains(command, "sudo -nl usermod"):
		return setup.Output{Code: 0, Stdout: "yes"}, nil
	case strings.Contains(command, "uname -sm"):
		// The arch preflight's question. The sim answers with the platform it
		// is running on, in the kernel's own spelling, because that is the
		// one case a controller setting up a box of its own kind is in.
		return setup.Output{Code: 0, Stdout: unameSm()}, nil
	case strings.Contains(command, setup.CafeBinPath+" version"):
		// The executable probe: can this host's kernel run what is installed.
		// The sim is a linux box, so a sim binary runs.
		return setup.Output{Code: 0, Stdout: "cafe sim"}, nil
	case strings.Contains(command, `[ "${EUID:-$(id -u)}" -eq 0 ]`):
		return setup.Output{Code: 0, Stdout: "sudo"}, nil
	case strings.HasPrefix(command, "docker -v"):
		if s.installed["docker"] {
			return setup.Output{Code: 0, Stdout: "Docker version 27.3.1"}, nil
		}
		return setup.Output{Code: 127, Stderr: "docker: not found"}, nil
	case strings.HasPrefix(command, "docker version"):
		if s.running {
			return setup.Output{Code: 0, Stdout: "Server: 27.3.1"}, nil
		}
		return setup.Output{Code: 1, Stderr: "Cannot connect to the Docker daemon"}, nil
	case strings.Contains(command, "get.docker.com"):
		s.installed["docker"] = true
		s.running = true
		return setup.Output{Code: 0}, nil
	case strings.HasPrefix(command, `[ "${EUID:-$(id -u)}" -eq 0 ] || id -nG`):
		if s.groups["docker"] {
			return setup.Output{Code: 0}, nil
		}
		return setup.Output{Code: 1, Stdout: "deploy"}, nil
	case strings.Contains(command, "usermod -aG docker"):
		s.groups["docker"] = true
		return setup.Output{Code: 0}, nil
	case strings.Contains(command, "sha256sum /usr/local/bin/cafe"):
		data, ok := s.fs[setup.CafeBinPath]
		if !ok {
			return setup.Output{Code: 1, Stderr: "no such file"}, nil
		}
		return setup.Output{Code: 0, Stdout: "sha " + string(data)}, nil
	case strings.Contains(command, "test -f /root/.config/cafe/box.json"):
		if _, err := os.Stat(filepath.Join(s.boxDir, "box.json")); err == nil {
			return setup.Output{Code: 0}, nil
		}
		return setup.Output{Code: 1}, nil
	case strings.Contains(command, "cat /root/.config/cafe/box.json"):
		raw, err := os.ReadFile(filepath.Join(s.boxDir, "box.json"))
		if err != nil {
			return setup.Output{Code: 1, Stderr: err.Error()}, nil
		}
		return setup.Output{Code: 0, Stdout: string(raw)}, nil
	case strings.Contains(command, "box enroll --token -"):
		// The enrollment happens HERE, on the host, with the host's own ring
		// and the host's own config directory. The token arrives on stdin and
		// exists nowhere else.
		store := box.NewStore(s.boxDir, s.ring, s.controlPlane)
		res, err := store.Enroll(context.Background(), string(stdin), "https://box.test")
		if err != nil {
			return setup.Output{Code: 1, Stderr: err.Error()}, nil
		}
		raw, err := json.Marshal(res)
		if err != nil {
			return setup.Output{Code: 1, Stderr: err.Error()}, nil
		}
		return setup.Output{Code: 0, Stdout: string(raw)}, nil
	case strings.Contains(command, "test -s /etc/cafe/box-identity.json"):
		if _, ok := s.fs[setup.IdentityPath]; ok {
			return setup.Output{Code: 0}, nil
		}
		return setup.Output{Code: 1}, nil
	case strings.Contains(command, "test -f /etc/systemd/system/cafe-boxd.service"):
		if _, ok := s.fs[setup.UnitPath]; ok && s.enabled[setup.UnitName] {
			return setup.Output{Code: 0}, nil
		}
		return setup.Output{Code: 1}, nil
	case strings.Contains(command, "systemctl daemon-reload"):
		s.enabled[setup.UnitName] = true
		return setup.Output{Code: 0}, nil
	case strings.Contains(command, "boxd --report"):
		report := map[string]any{
			"schema_version":   1,
			"box_id":           "box_e2e_01",
			"deployed_version": "1.2.3",
			"health":           map[string]any{"status": "healthy", "phase": 0},
			"certificate":      map[string]any{"status": "ok", "days_left": 90},
			"disk":             map[string]any{"status": "ok", "path": "/", "total_bytes": 1024, "free_bytes": 512},
		}
		raw, _ := json.Marshal(report)
		return setup.Output{Code: 0, Stdout: string(raw)}, nil
	}
	return setup.Output{Code: 127, Stderr: "not found: " + command}, nil
}

func (s *boxHostSim) Write(_ context.Context, path string, data []byte, _ os.FileMode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, simWrite{Path: path, Data: append([]byte(nil), data...)})
	s.fs[path] = append([]byte(nil), data...)
	return nil
}

func (s *boxHostSim) ran(sub string) int {
	n := 0
	for _, c := range s.commands {
		if strings.Contains(c.Command, sub) {
			n++
		}
	}
	return n
}

// setupDepsFor wires the verb tree to a simulated host and a host-side box
// store the test can inspect.
func setupDepsFor(t *testing.T, host *boxHostSim, cp *countingBoxControlPlane) (boxDeps, *boxTestRing) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "cafe")
	if err := os.WriteFile(binary, []byte("the binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	ring := newBoxTestRing()
	return boxDeps{
		dir: t.TempDir(),
		setup: boxSetupDeps{
			host: host,
			selfBinary: func() (string, error) {
				return binary, nil
			},
		},
		// The controller's own box state is empty on purpose: if the setup
		// put anything there, the assertions below fail.
		ring:         ring,
		controlPlane: cp,
		url:          "https://box.test",
	}, ring
}

// The whole command, end to end, and then again. The second run presents no
// capability and writes no keypair — asserted over BOTH runs, and the count is
// the host's own control plane counting real redemptions.
func TestBoxSetupEndToEndIsIdempotent(t *testing.T) {
	cp := &countingBoxControlPlane{answer: &box.Installation{
		BoxID: "box_e2e_01", AccountID: "acct_1", Status: "active", Phase: "enrolled",
	}}
	sim := newBoxHostSim(t, cp)
	deps, ring := setupDepsFor(t, sim, cp)

	run := func() (int, string, string) {
		var out, errOut strings.Builder
		env := &Env{Stdout: &out, Stderr: &errOut, Stdin: strings.NewReader("cap_e2e_once\n"), Version: testVersion}
		code := 0
		if err := newBoxCommand(deps).Execute(env, []string{"setup", "--token", "-", "--json", "box.test"}); err != nil {
			if errors.Is(err, errReported) {
				code = 1
			} else {
				t.Fatalf("cafe box setup: %v", err)
			}
		}
		return code, out.String(), errOut.String()
	}

	code, stdout, stderr := run()
	if code != 0 {
		t.Fatalf("the first setup exited %d:\n%s\n%s", code, stdout, stderr)
	}

	var doc setup.Result
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not the JSON document: %v\n%s", err, stdout)
	}
	if !doc.OK() {
		t.Fatalf("the first setup refused at %s", doc.FailedStep())
	}
	if doc.Redemptions != 1 {
		t.Errorf("the first setup presented %d capabilities, want 1", doc.Redemptions)
	}
	if doc.Enrolled == nil || doc.Enrolled.BoxID != "box_e2e_01" {
		t.Errorf("the document's box = %+v, want box_e2e_01", doc.Enrolled)
	}
	if doc.Report == nil {
		t.Fatal("the document carries no observation")
	}
	if doc.Report.Health.Status != "healthy" || doc.Report.Certificate.Status != "ok" || doc.Report.Disk.Status != "ok" {
		t.Errorf("the report's health/certificate/disk = %q/%q/%q, want ok/ok/ok",
			doc.Report.Health.Status, doc.Report.Certificate.Status, doc.Report.Disk.Status)
	}
	// The controller kept nothing: no box identity, no keyring entry.
	if _, err := os.Stat(filepath.Join(deps.dir, "box.json")); err == nil {
		t.Error("the controller holds a copy of the box identity")
	}
	if len(ring.entries) != 0 {
		t.Errorf("the controller's keyring holds %d entries, want 0: %v", len(ring.entries), ring.entries)
	}

	// The second run: the same host, the same controller state, a fresh token
	// the setup must not need.
	code, stdout, stderr = run()
	if code != 0 {
		t.Fatalf("the second setup exited %d:\n%s\n%s", code, stdout, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("the second stdout is not JSON: %v\n%s", err, stdout)
	}
	if !doc.OK() {
		t.Fatalf("the second setup refused at %s", doc.FailedStep())
	}
	if doc.Redemptions != 0 {
		t.Errorf("the second setup presented %d capabilities, want 0", doc.Redemptions)
	}
	if cp.calls != 1 {
		t.Errorf("the host's control plane counted %d redemptions across two setups, want 1", cp.calls)
	}
	if sim.ran("box enroll --token -") != 1 {
		t.Errorf("the enroll command ran %d times across two setups, want 1", sim.ran("box enroll --token -"))
	}
	if len(ring.entries) != 0 {
		t.Errorf("the controller's keyring holds %d entries after two setups, want 0", len(ring.entries))
	}
	// The host holds exactly one keypair, filed under the box it enrolled as,
	// and no pending one left behind.
	if got := len(sim.ring.entries); got != 1 {
		t.Errorf("the host's keyring holds %d keys, want 1: %v", got, sim.ring.entries)
	}
	if _, ok := sim.ring.entries[box.KeyringService+"/"+box.KeyringAccountPending]; ok {
		t.Error("the host still has a pending enrollment key")
	}
	if _, ok := sim.ring.entries[box.KeyringService+"/box/box_e2e_01"]; !ok {
		t.Errorf("the host has no key filed under the enrolled box: %v", sim.ring.entries)
	}
}

// The dry run prints the plan and asks the host to do nothing. It is driven
// through the same verb, so "asks the host to do nothing" means the simulator
// never received an install, a write or a redemption.
func TestBoxSetupDryRunThroughTheRouter(t *testing.T) {
	cp := &countingBoxControlPlane{answer: &box.Installation{BoxID: "box_e2e_01", Phase: "enrolled"}}
	sim := newBoxHostSim(t, cp)
	deps, _ := setupDepsFor(t, sim, cp)

	var out, errOut strings.Builder
	env := &Env{Stdout: &out, Stderr: &errOut, Stdin: strings.NewReader("cap_dry\n"), Version: testVersion}
	if err := newBoxCommand(deps).Execute(env, []string{"setup", "--dry-run", "--token", "-", "--json", "box.test"}); err != nil {
		t.Fatalf("cafe box setup --dry-run: %v\n%s", err, errOut.String())
	}

	var doc setup.Result
	if err := json.Unmarshal([]byte(out.String()), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out.String())
	}
	if !doc.DryRun || !doc.OK() {
		t.Fatalf("doc = %+v, want an OK dry run", doc)
	}
	if sim.ran("get.docker.com") != 0 {
		t.Error("a dry run installed Docker")
	}
	if len(sim.writes) != 0 {
		t.Errorf("a dry run wrote %d files to the host", len(sim.writes))
	}
	if cp.calls != 0 {
		t.Errorf("a dry run presented %d capabilities", cp.calls)
	}
	for _, s := range doc.Steps {
		if s.Action != setup.ActionPlanned {
			t.Errorf("step %s = %s, want %s", s.Name, s.Action, setup.ActionPlanned)
		}
	}
	if !strings.Contains(errOut.String(), "dry run") {
		t.Errorf("stderr does not say this was a dry run:\n%s", errOut.String())
	}
}

// The token is read from the environment when it is not on stdin, and a setup
// with neither is a USAGE error — exit 2, nothing attempted — rather than a
// failure, because nothing was tried that could have gone wrong.
func TestBoxSetupWithoutATokenIsAUsageError(t *testing.T) {
	cp := &countingBoxControlPlane{}
	sim := newBoxHostSim(t, cp)
	deps, _ := setupDepsFor(t, sim, cp)

	var out, errOut strings.Builder
	env := &Env{Stdout: &out, Stderr: &errOut, Stdin: strings.NewReader(""), Version: testVersion}
	err := newBoxCommand(deps).Execute(env, []string{"setup", "--token", "-", "--json", "box.test"})
	if err == nil {
		t.Fatal("a setup with no capability must fail")
	}
	if !errors.Is(err, errUsage) {
		t.Errorf("err = %v, want errUsage", err)
	}
	if len(sim.commands) != 0 {
		t.Errorf("the host was contacted %d times without a capability", len(sim.commands))
	}
	if cp.calls != 0 {
		t.Errorf("the control plane counted %d redemptions", cp.calls)
	}
}

// The setup verb is one of `cafe box`'s verbs, it takes exactly one argument,
// and it is listed in the help alongside the others.
func TestBoxSetupIsAVerbOfBox(t *testing.T) {
	want := "box setup"
	found := false
	for _, v := range boxVerbs(boxDeps{}) {
		if v.name == "setup" {
			found = true
			if v.cmd.Name != want {
				t.Errorf("the verb's command is named %q, want %q", v.cmd.Name, want)
			}
		}
	}
	if !found {
		t.Fatal("cafe box has no setup verb")
	}
	// A wrong argument count is a usage error, not a run.
	var out, errOut strings.Builder
	env := &Env{Stdout: &out, Stderr: &errOut, Stdin: strings.NewReader("cap\n"), Version: testVersion}
	if err := newBoxCommand(boxDeps{}).Execute(env, []string{"setup"}); !errors.Is(err, errUsage) {
		t.Errorf("err = %v, want errUsage for a missing host", err)
	}
}

// The negative control: a check nobody has watched fail is a check that might
// not work. Breaking the enrollment's own idempotence — by making the host's
// enroll answer a second enrollment as a fresh one — must turn the count red.
func TestTheSingleRedemptionCountCanFail(t *testing.T) {
	cp := &countingBoxControlPlane{answer: &box.Installation{
		BoxID: "box_ctrl_01", AccountID: "acct_1", Status: "active", Phase: "enrolled",
	}}
	sim := newBoxHostSim(t, cp)
	deps, _ := setupDepsFor(t, sim, cp)

	run := func() setup.Result {
		var out, errOut strings.Builder
		env := &Env{Stdout: &out, Stderr: &errOut, Stdin: strings.NewReader("cap_ctrl\n"), Version: testVersion}
		_ = newBoxCommand(deps).Execute(env, []string{"setup", "--token", "-", "--json", "box.test"})
		var doc setup.Result
		if err := json.Unmarshal([]byte(out.String()), &doc); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, out.String())
		}
		return doc
	}

	first := run()
	if first.Redemptions != 1 {
		t.Fatalf("the first setup presented %d capabilities, want 1", first.Redemptions)
	}

	// Now delete the host's enrollment file, which is what a broken setup
	// would leave behind, and watch the count go to two. If this test cannot
	// be made to fail, the assertion above is not measuring anything.
	if err := os.Remove(filepath.Join(sim.boxDir, "box.json")); err != nil {
		t.Fatal(err)
	}
	second := run()
	if second.Redemptions != 1 {
		t.Errorf("after the host lost its enrollment the setup presented %d capabilities, want 1 again", second.Redemptions)
	}
	if cp.calls != 2 {
		t.Errorf("the control plane counted %d redemptions, want 2 — the control can fail", cp.calls)
	}
}
