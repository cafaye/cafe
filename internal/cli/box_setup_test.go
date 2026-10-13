package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
		//
		// And the sim runs the HOST'S OWN `cafe box enroll` — the real command,
		// through the real flag parser, against the store — rather than calling
		// the store directly. A sim that skipped the command would pass on the
		// tree where the command never opened the pipe: the store would have
		// been handed the literal dash, box would have answered 404 for a
		// token it never minted, and the count below would still have been 1.
		out := runHostEnroll(s, hostEnrollArgs(command), stdin)
		return out, nil
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

// runHostEnroll performs the enrollment the controller asked for by running the
// HOST'S OWN command: `cafe box enroll --token - --box <url>`, with the
// controller's capability on its stdin and the host's own ring, config
// directory and control plane. It answers the way a remote shell would — exit
// code, stdout, stderr — so the walk cannot tell it from an ssh round trip.
func runHostEnroll(s *boxHostSim, args []string, stdin []byte) setup.Output {
	var out, errOut strings.Builder
	env := &Env{
		Stdout:  &out,
		Stderr:  &errOut,
		Stdin:   strings.NewReader(string(stdin)),
		Version: testVersion,
		Context: context.Background(),
	}
	host := boxDeps{dir: s.boxDir, ring: s.ring, controlPlane: s.controlPlane, url: "https://box.test"}
	if err := newBoxCommand(host).Execute(env, args); err != nil {
		// Both streams, because the exit code alone is what a failed walk
		// refuses with and a refusal the operator cannot read is a refusal
		// nobody can act on.
		return setup.Output{Code: 1, Stderr: err.Error() + "\n" + errOut.String()}
	}
	return setup.Output{Code: 0, Stdout: out.String()}
}

// hostEnrollArgs pulls the invocation out of the command the controller built,
// so the sim runs what the plan actually wrote — under whatever root prefix the
// walk carried — rather than a copy of it that could drift from plan.go's
// spelling. The URL is quoted the way a shell wants it, so it is unquoted the
// same way.
func hostEnrollArgs(command string) []string {
	const verb = "box enroll"
	at := strings.Index(command, verb)
	if at < 0 {
		return nil
	}
	// Everything after the word "box", so the array starts at the verb — which
	// is what Execute takes.
	fields := strings.Fields(command[at+len("box "):])
	args := make([]string, 0, len(fields))
	for _, f := range fields {
		args = append(args, unquoteShell(f))
	}
	return args
}

// unquoteShell reverses the one quoting the plan applies to an argument. It
// lives here rather than in setup because shellQuote is setup's and the
// controller's quoting is the controller's business.
func unquoteShell(s string) string {
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		return s
	}
	return strings.ReplaceAll(s[1:len(s)-1], `'\''`, "'")
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

// The pipe the controller writes carries the capability and nothing else — not
// even the newline the operator's `echo` left on it, and never the flag value.
//
// This is the one place readSetupToken's own trim is observable. The host's
// enroll trims what it is handed as well, so an assertion about what box was
// told would pass either way; only the bytes that crossed the seam say anything
// about which side did the trimming, and a controller that left a newline on the
// pipe is a controller whose "nothing else" claim is one string comparison away
// from being false.
func TestTheSetupPipeCarriesTheCapabilityAndNothingElse(t *testing.T) {
	cp := &countingBoxControlPlane{answer: &box.Installation{
		BoxID: "box_pipe_01", AccountID: "acct_1", Status: "active", Phase: "enrolled",
	}}
	sim := newBoxHostSim(t, cp)
	deps, _ := setupDepsFor(t, sim, cp)

	var out, errOut strings.Builder
	env := &Env{Stdout: &out, Stderr: &errOut, Stdin: strings.NewReader("  cap_pipe\n"), Version: testVersion}
	if err := newBoxCommand(deps).Execute(env, []string{"setup", "--token", "-", "--json", "box.test"}); err != nil {
		if !errors.Is(err, errReported) {
			t.Fatalf("cafe box setup: %v", err)
		}
	}

	ran := false
	for _, c := range sim.commands {
		if !strings.Contains(c.Command, "box enroll --token -") {
			continue
		}
		ran = true
		if c.Stdin != "cap_pipe" {
			t.Errorf("the enroll pipe carried %q, want exactly the capability", c.Stdin)
		}
		if strings.Contains(c.Command, "cap_pipe") {
			t.Errorf("the capability appeared in a COMMAND ARGUMENT: %s", c.Command)
		}
	}
	if !ran {
		t.Fatal("the enroll step never ran, so no pipe carried the capability")
	}
	if len(cp.tokens) != 1 || cp.tokens[0] != "cap_pipe" {
		t.Errorf("the host's control plane was presented %v, want [cap_pipe]", cp.tokens)
	}
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
	// The host's own enroll READ the pipe. The capability the host's control
	// plane was presented is the one the controller wrote on it — the assertion
	// the packet is for, because the value that crossed was the literal dash
	// until the host's command opened the stream: box answered 404 for a token
	// it had never minted, and a count of redemptions looked correct the whole
	// time.
	if len(cp.tokens) == 0 || cp.tokens[0] != "cap_e2e_once" {
		t.Errorf("the host's control plane was presented %v, want [cap_e2e_once]", cp.tokens)
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

// ---------------------------------------------------------------------------
// the transfer's progress, which is the operator's only window into a write
// that takes minutes
// ---------------------------------------------------------------------------

// The two lines an operator sees while a 29 MB binary crosses a slow link,
// plus the byte counts spelled in units a person reads while waiting. "27.9
// MiB" is a number they hold; "29250507" is a number they have to parse.
func TestTheTransferProgressIsReadable(t *testing.T) {
	var out strings.Builder
	sink := setupProgress(&out, "box.test")
	sink(setup.WriteChunk, 1<<20, 29_250_507)
	sink(setup.WriteVerified, 29_250_507, 29_250_507)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d progress lines, want one per chunk and one for the match", len(lines))
	}
	if !strings.Contains(lines[0], "1.0 MiB of 27.9 MiB written to box.test") {
		t.Errorf("the chunk line reads %q", lines[0])
	}
	if !strings.Contains(lines[1], "27.9 MiB on box.test written and verified") ||
		!strings.Contains(lines[1], "sha256 matches the local binary") {
		t.Errorf("the verified line reads %q", lines[1])
	}
}

// ---------------------------------------------------------------------------
// the flags may come after the host — the form the Circle-3 run was refused
// with
// ---------------------------------------------------------------------------

// recordingReader fails whoever reads from it. It is the seam that makes
// "refused before anything is read from stdin" assertable: a real pipe would
// simply block, and a test that blocked would say nothing about the order.
type recordingReader struct{ read bool }

func (r *recordingReader) Read(p []byte) (int, error) {
	r.read = true
	return 0, io.EOF
}

// setupThroughTheRouter drives `cafe box setup` through the real verb tree,
// against a simulator that performs every step on itself. It is here rather
// than inline in each case because the interesting part of the flag question is
// that the two forms must produce the SAME document and the same commands, and
// a helper is what makes "the same" comparable.
func setupThroughTheRouter(t *testing.T, stdin io.Reader, deps boxDeps, args ...string) (setup.Result, *boxHostSim, string, error) {
	t.Helper()
	var out, errOut strings.Builder
	env := &Env{Stdout: &out, Stderr: &errOut, Stdin: stdin, Version: testVersion}
	err := newBoxCommand(deps).Execute(env, append([]string{"setup"}, args...))
	var doc setup.Result
	// The document is parsed even on the refusal paths, because the shape of
	// what was printed is part of what these cases assert.
	_ = json.Unmarshal([]byte(out.String()), &doc)
	sim, _ := deps.setup.host.(*boxHostSim)
	return doc, sim, errOut.String(), err
}

// The measured friction, as a green test: `cafe box setup contabo
// -control-plane …` is the SAME COMMAND as `cafe box setup -control-plane …
// contabo`. The document is the same, the host is the same, and the flag
// reaches the host's own `cafe box enroll` — which is the observable that
// distinguishes "the flag was parsed" from "the flag was swallowed as an
// argument count".
func TestFlagsAfterTheHostAreTheSameCommandAsFlagsBefore(t *testing.T) {
	const cpURL = "http://127.0.0.1:4003"
	newDeps := func(t *testing.T) (boxDeps, *boxHostSim, *countingBoxControlPlane) {
		cp := &countingBoxControlPlane{answer: &box.Installation{
			BoxID: "box_flags_01", AccountID: "acct_1", Status: "active", Phase: "enrolled",
		}}
		sim := newBoxHostSim(t, cp)
		deps, _ := setupDepsFor(t, sim, cp)
		return deps, sim, cp
	}

	firstDeps, firstSim, _ := newDeps(t)
	before, _, _, errBefore := setupThroughTheRouter(t, strings.NewReader("cap_flags\n"), firstDeps,
		"--token", "-", "--control-plane", cpURL, "--json", "box.test")
	if errBefore != nil && !errors.Is(errBefore, errReported) {
		t.Fatalf("flags before the host: %v", errBefore)
	}

	secondDeps, secondSim, _ := newDeps(t)
	after, _, _, errAfter := setupThroughTheRouter(t, strings.NewReader("cap_flags\n"), secondDeps,
		"box.test", "-control-plane", cpURL, "--token", "-", "--json")
	if errAfter != nil && !errors.Is(errAfter, errReported) {
		t.Fatalf("flags after the host: %v", errAfter)
	}

	if !before.OK() || !after.OK() {
		t.Fatalf("the two forms did not both succeed: before=%v after=%v", before.FailedStep(), after.FailedStep())
	}
	if after.Host != "box.test" || before.Host != "box.test" {
		t.Errorf("host = %q then %q, want box.test both times", before.Host, after.Host)
	}
	if after.Redemptions != 1 || before.Redemptions != 1 {
		t.Errorf("redemptions = %d then %d, want 1 both times", before.Redemptions, after.Redemptions)
	}
	if before.Enrolled == nil || after.Enrolled == nil {
		t.Fatalf("one form did not enroll: before=%v after=%v", before.Enrolled, after.Enrolled)
	}
	// The flag reached the host's own command: `cafe box enroll --token - --box
	// '<origin>'`. A parse that had counted it as an argument would never have
	// written it there — and this is the only observable that says the flag was
	// PARSED rather than merely tolerated.
	wantEnroll := "box enroll --token - --box '" + cpURL + "'"
	for _, sim := range []*boxHostSim{firstSim, secondSim} {
		if sim.ran(wantEnroll) != 1 {
			t.Errorf("the host ran the enrollment %d times; want once with --box %q:\n%s",
				sim.ran(wantEnroll), cpURL, strings.Join(commandList(sim), "\n"))
		}
	}
	// And the same steps ran, in the same order: the flag position changed
	// nothing else about the walk.
	beforeSteps, afterSteps := stepNames(before), stepNames(after)
	if strings.Join(beforeSteps, ",") != strings.Join(afterSteps, ",") {
		t.Errorf("the two forms walked different steps:\n%s\n%s", beforeSteps, afterSteps)
	}
}

func stepNames(doc setup.Result) []string {
	names := make([]string, 0, len(doc.Steps))
	for _, s := range doc.Steps {
		names = append(names, s.Name+":"+s.Action)
	}
	return names
}

func commandList(sim *boxHostSim) []string {
	out := make([]string, 0, len(sim.commands))
	for _, c := range sim.commands {
		out = append(out, c.Command)
	}
	return out
}

// What is refused is refused as itself. One letter short of the flag name —
// `-controlplane` — used to be answered with "wants 1 argument, got 3": a count
// that names a shape rather than the typo, and a count is the one thing that
// cannot send anybody to the fix.
func TestAMisspelledFlagAfterTheHostIsRefusedAsAFlag(t *testing.T) {
	cp := &countingBoxControlPlane{answer: &box.Installation{BoxID: "box_flags_02", Phase: "enrolled"}}
	sim := newBoxHostSim(t, cp)
	deps, _ := setupDepsFor(t, sim, cp)

	_, _, _, err := setupThroughTheRouter(t, strings.NewReader("cap_flags\n"), deps,
		"box.test", "-controlplane", "http://127.0.0.1:4003", "--token", "-", "--json")
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
	if !strings.Contains(err.Error(), "-controlplane") {
		t.Errorf("the refusal does not name the flag it did not recognise: %v", err)
	}
	if len(sim.commands) != 0 {
		t.Errorf("the host was contacted %d times by a refused invocation", len(sim.commands))
	}
}

// A second subject is still a usage error, and it is the one case the count
// answers correctly: two boxes were named, and no flag position can explain
// that away.
func TestASecondSubjectIsStillAUsageError(t *testing.T) {
	cp := &countingBoxControlPlane{}
	sim := newBoxHostSim(t, cp)
	deps, _ := setupDepsFor(t, sim, cp)

	_, _, _, err := setupThroughTheRouter(t, strings.NewReader("cap_flags\n"), deps,
		"box.test", "other.test", "--token", "-", "--json")
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("the refusal does not say how many subjects were named: %v", err)
	}
	if len(sim.commands) != 0 {
		t.Errorf("the host was contacted %d times by a refused invocation", len(sim.commands))
	}
}

// An empty host is refused before stdin is read. It is reachable only now the
// flags may follow the subject — `cafe box setup "" -control-plane …` used to be
// caught by the argument count, which was the right answer for the wrong
// reason — and a command that read the pipe first would block a terminal
// forever on a run that had already failed.
func TestAnEmptyHostIsRefusedBeforeAnythingIsReadFromStdin(t *testing.T) {
	cp := &countingBoxControlPlane{}
	sim := newBoxHostSim(t, cp)
	deps, _ := setupDepsFor(t, sim, cp)
	stdin := &recordingReader{}

	_, _, _, err := setupThroughTheRouter(t, stdin, deps, "", "-control-plane", "http://127.0.0.1:4003", "--json")
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("the refusal does not say what was missing: %v", err)
	}
	if stdin.read {
		t.Error("the command read stdin for a capability it was never going to use")
	}
	if len(sim.commands) != 0 {
		t.Errorf("the host was contacted %d times by a refused invocation", len(sim.commands))
	}
}

func TestBytesHumanSpellsWhatAnOperatorReads(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{2048, "2.0 KiB"},
		{29_250_507, "27.9 MiB"},
	} {
		if got := bytesHuman(tc.n); got != tc.want {
			t.Errorf("bytesHuman(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
