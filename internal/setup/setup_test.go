package setup

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

	"github.com/cafaye/cafe/internal/boxd"
)

// kernelSpelling is what a unix calls its own kernel. It is a table rather
// than a capitalise because the two kernels this package cares about spell
// themselves differently and a third is not a thing cafe ships a box for.
var kernelSpelling = map[string]string{"linux": "Linux", "darwin": "Darwin"}

// unameSpelling is what a real host answers `uname -sm` with for a platform.
// Linux says aarch64 where macOS says arm64 for the same silicon, so the
// mapping needs the OS as well as the architecture: a fake that answered in
// Go's spelling for both would never exercise the translation, which is the
// part of this packet that could be wrong.
func unameSpelling(goos, goarch string) string {
	kernel, ok := kernelSpelling[goos]
	if !ok {
		kernel = goos
	}
	machine := goarch
	switch goarch {
	case "amd64":
		machine = "x86_64"
	case "arm64":
		if goos == "darwin" {
			machine = "arm64"
		} else {
			machine = "aarch64"
		}
	case "arm":
		machine = "armv7l"
	case "386":
		machine = "i686"
	}
	return kernel + " " + machine
}

// The fake host: a machine the plan can be driven against, that keeps the state
// a real host keeps and answers the checks the way a real host answers them.
//
// This is not a mock that returns canned strings. A mock that answered
// "docker -v" with a version would prove nothing about whether the check is the
// check kamal runs, and would keep proving it after somebody changed it. What
// this holds is a filesystem, a package list, a systemd state and a group
// membership table, and it implements each command by looking at that state —
// so the idempotence proof is real: the second setup passes the checks because
// the first setup changed the state, and for no other reason.

type fakeHost struct {
	mu sync.Mutex

	// fs is the host's filesystem: path to bytes.
	fs map[string][]byte
	// exec is the host's mode bits.
	mode map[string]os.FileMode

	// installed is what `apt list --installed`/the docker convenience script
	// has put on the box.
	installed map[string]bool
	// running is whether the daemon's socket answers.
	running bool
	// groups is the login user's group membership.
	groups map[string]bool
	// systemctl is unit state: enabled, active.
	units map[string]unitState
	// boxJSON is where `cafe box enroll` writes, i.e. /root/.config/cafe.
	boxJSON string
	// capabilities presented by an enroll run on this host.
	redemptions int
	// enrolledAt is the host's clock reading when enrollment happened.
	enrolledAt string

	// log is every command the walk ran, in order, for the assertions about
	// what was NOT run.
	commands []fakeCommand
	// writes is every file put, for the assertion that no secret is written.
	writes []fakeWrite
	// stdin is what each Run was handed.
	stdins []string

	// controlPlaneURL is what the host's cafe calls out to.
	controlPlaneURL string
	// observeReport is what `cafe boxd --report` prints.
	observeReport *boxd.Observation
	// reportErr is what `cafe boxd --report` answers with, for the refusal case.
	reportErr string
	// failDocker is a step the fake refuses to make converge, for the table.
	failDocker bool
	// superuser is what the superuser probe answers with. "" means the
	// default, which is "sudo" for a non-root login user — the case every
	// ordinary box is in.
	superuser string
	// sudoNeeded says whether the login user is not root, so the walk's prefix
	// is "sudo -n -H".
	sudoNeeded bool
	// binaryHash is the hash of the binary the fake thinks is installed.
	binaryHash string
	// binaryRuns is whether this host's kernel can execute what sits at
	// CafeBinPath. A darwin binary on a linux box is `-x`, and it hashes to
	// something: the hash check alone would report "a different version" over
	// a file that is not a program at all. This field is the kernel's answer,
	// and the executable probe is the question.
	binaryRuns bool

	// goos/goarch is what `uname -sm` answers: the host's platform, in the
	// kernel's own spelling. It defaults to the CONTROLLER's, because that is
	// the ordinary case — a laptop setting up a box of its own kind — and the
	// arch tests move it. The arch refusal tests set it to a platform this
	// machine is not, which is the case the packet exists for.
	goos   string
	goarch string

	// transportFail makes Run answer with a transport error, which is the one
	// error the walk must not treat as "the check failed".
	transportFail bool
}

type unitState struct {
	enabled bool
	active  bool
}

type fakeCommand struct {
	Command string
	Stdin   string
}

type fakeWrite struct {
	Path string
	Mode os.FileMode
	Data []byte
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		fs:         map[string][]byte{},
		mode:       map[string]os.FileMode{},
		installed:  map[string]bool{},
		groups:     map[string]bool{},
		units:      map[string]unitState{},
		sudoNeeded: true,
		binaryRuns: true,
		goos:       runtime.GOOS,
		goarch:     runtime.GOARCH,
	}
}

// Run answers one remote command. It is the interpreter, and it is where the
// checks are kept honest: each branch is the thing the plan asked, and the
// answer is a function of the state, not of the number of times it was called.
func (h *fakeHost) Run(ctx context.Context, command string, stdin []byte) (Output, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.commands = append(h.commands, fakeCommand{Command: command, Stdin: string(stdin)})
	h.stdins = append(h.stdins, string(stdin))
	if h.transportFail {
		return Output{}, errors.New("ssh: connect to host: connection refused")
	}

	// The superuser probe, then which spelling.
	if strings.Contains(command, "sudo -nl usermod") {
		if h.superuser == "none" {
			return Output{Code: 1, Stderr: "sudo: a password is required"}, nil
		}
		return Output{Code: 0, Stdout: "yes"}, nil
	}
	if strings.Contains(command, `[ "${EUID:-$(id -u)}" -eq 0 ]`) {
		switch {
		case h.superuser == "none":
			return Output{Code: 0, Stdout: "no"}, nil
		case h.superuser == "root":
			return Output{Code: 0, Stdout: "root"}, nil
		case h.sudoNeeded:
			return Output{Code: 0, Stdout: "sudo"}, nil
		default:
			return Output{Code: 0, Stdout: "root"}, nil
		}
	}

	// Reading the enrollment back.
	if strings.Contains(command, "cat "+EnrolledJSON) {
		if data, ok := h.fs[h.boxJSON]; ok {
			return Output{Code: 0, Stdout: string(data)}, nil
		}
		return Output{Code: 1, Stderr: "cat: " + EnrolledJSON + ": No such file or directory"}, nil
	}

	// The architecture preflight. The answer is in the KERNEL's spelling,
	// because that is what a real host gives, and the translation is what this
	// packet is about — a fake that answered "arm64" where Linux says
	// "aarch64" would be lying to the resolver about the very thing it is
	// supposed to be checking.
	if strings.Contains(command, archProbe) {
		return Output{Code: 0, Stdout: unameSpelling(h.goos, h.goarch)}, nil
	}

	// The install and start of Docker.
	switch {
	case strings.HasPrefix(command, "docker -v"):
		if h.installed["docker"] {
			return Output{Code: 0, Stdout: "Docker version 27.3.1, build 41f4c1c"}, nil
		}
		return Output{Code: 127, Stderr: "sh: 1: docker: not found"}, nil
	case strings.HasPrefix(command, "docker version"):
		if h.running {
			return Output{Code: 0, Stdout: "Server: Docker Engine\n Version: 27.3.1"}, nil
		}
		return Output{Code: 1, Stderr: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock."}, nil
	case strings.Contains(command, "get.docker.com"):
		if h.failDocker {
			return Output{Code: 1, Stderr: "curl: (7) Failed to connect to get.docker.com port 443"}, nil
		}
		// This is the install: it changes the host's state, which is what
		// makes the next check pass.
		h.installed["docker"] = true
		h.running = true
		return Output{Code: 0, Stdout: "# Executing docker install script"}, nil
	}

	// The docker group.
	if strings.HasPrefix(command, `id -nG`) {
		if h.groups["docker"] {
			return Output{Code: 0, Stdout: "root docker"}, nil
		}
		return Output{Code: 1, Stdout: "root"}, nil
	}
	// The docker group. Kamal asks root? OR in_docker_group?, so a root login
	// passes without the group.
	if strings.HasPrefix(command, `[ "${EUID:-$(id -u)}" -eq 0 ] || id -nG`) {
		if !h.sudoNeeded || h.groups["docker"] {
			return Output{Code: 0}, nil
		}
		return Output{Code: 1, Stdout: "deploy"}, nil
	}
	if strings.Contains(command, "usermod -aG docker") {
		h.groups["docker"] = true
		return Output{Code: 0}, nil
	}

	// The executable probe: can THIS host's kernel run what is at CafeBinPath?
	// It is asked before the hash because the two answer different questions,
	// and only one of them can tell a wrong-architecture binary from a
	// different build. sha256sum reads bytes and does not care what they are.
	if strings.Contains(command, CafeBinPath+" version") {
		if _, ok := h.fs[CafeBinPath]; !ok {
			return Output{Code: 127, Stderr: "sh: 1: " + CafeBinPath + ": not found"}, nil
		}
		if !h.binaryRuns {
			// What a Linux kernel answers a Mach-O file with. It is not a
			// permission problem and it is not a corrupt download: it is the
			// architecture, which is the whole reason the probe exists.
			return Output{Code: 126, Stderr: "sh: 1: " + CafeBinPath + ": cannot execute binary file: Exec format error"}, nil
		}
		return Output{Code: 0, Stdout: "cafe 1.2.3 (commit test)"}, nil
	}

	// The binary's hash check.
	if strings.Contains(command, "sha256sum "+CafeBinPath) {
		data, ok := h.fs[CafeBinPath]
		if !ok {
			return Output{Code: 1, Stderr: "sha256sum: " + CafeBinPath + ": No such file or directory"}, nil
		}
		if sha256Hex(data) == h.binaryHash {
			return Output{Code: 0}, nil
		}
		return Output{Code: 1, Stderr: "the installed binary is not the one this setup holds"}, nil
	}

	// The enrollment's own file, which is the check that makes a re-run present
	// no capability.
	if strings.Contains(command, "test -f "+EnrolledJSON) {
		if _, ok := h.fs[h.boxJSON]; ok {
			return Output{Code: 0}, nil
		}
		return Output{Code: 1}, nil
	}
	if strings.Contains(command, "box enroll --token -") {
		if _, ok := h.fs[h.boxJSON]; ok {
			// Reaching the apply at all would mean the check above is not
			// being run, which is the idempotence failure this table is for.
			return Output{Code: 0, Stdout: enrolledDocument}, nil
		}
		h.redemptions++
		h.enrolledAt = "2026-10-12T10:00:00Z"
		h.fs[h.boxJSON] = []byte(enrolledDocument)
		doc := map[string]any{
			"action":      "enrolled",
			"redemptions": 1,
			"identity": map[string]any{
				"box_id":      "box_fake_01",
				"conductor":   h.controlPlaneURL,
				"enrolled_at": h.enrolledAt,
				"public_key":  "ed25519:AAAA",
				"phase":       "enrolled",
				"account_id":  "acct_1",
				"status":      "active",
			},
		}
		raw, _ := json.Marshal(doc)
		return Output{Code: 0, Stdout: string(raw)}, nil
	}

	// The daemon's identity document and unit.
	if strings.Contains(command, "test -s "+IdentityPath) {
		if _, ok := h.fs[IdentityPath]; ok {
			return Output{Code: 0}, nil
		}
		return Output{Code: 1}, nil
	}
	if strings.Contains(command, "test -f "+UnitPath) {
		if data, ok := h.fs[UnitPath]; ok {
			if sha256Hex(data) == h.unitHash() && h.units[UnitName].enabled {
				return Output{Code: 0}, nil
			}
		}
		return Output{Code: 1}, nil
	}
	if strings.Contains(command, "systemctl daemon-reload") {
		h.units[UnitName] = unitState{enabled: true, active: true}
		return Output{Code: 0}, nil
	}

	// The heartbeat: the box's own observe.report.
	if strings.Contains(command, "boxd --report") {
		if h.reportErr != "" {
			return Output{Code: 1, Stderr: h.reportErr}, nil
		}
		report := h.observeReport
		if report == nil {
			report = healthyReport()
		}
		raw, _ := json.Marshal(report)
		return Output{Code: 0, Stdout: string(raw)}, nil
	}

	return Output{Code: 127, Stderr: "sh: 1: " + firstWord(command) + ": not found"}, nil
}

func (h *fakeHost) unitHash() string {
	o := Options{Range: "^1.0.0"}
	return sha256Hex(unitDocument(o))
}

// Write puts a file on the fake host. It refuses to write into the controller's
// world, because the whole claim being tested is that the controller keeps no
// copy: a Write of an identity or a key to anywhere but the host's own paths is
// the failure the test is looking for.
func (h *fakeHost) Write(_ context.Context, path string, data []byte, mode os.FileMode) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.writes = append(h.writes, fakeWrite{Path: path, Mode: mode, Data: append([]byte(nil), data...)})
	h.fs[path] = append([]byte(nil), data...)
	h.mode[path] = mode
	if path == CafeBinPath {
		// A binary this setup has just written is a binary this setup built
		// for this host, so the kernel can run it. Flipping it here is what
		// makes the wrong-architecture test able to assert that a SECOND
		// setup now finds the box in order — otherwise the fake would keep
		// answering "Exec format error" over a file that was just replaced.
		h.binaryRuns = true
	}
	return nil
}

func (h *fakeHost) ran(sub string) int {
	n := 0
	for _, c := range h.commands {
		if strings.Contains(c.Command, sub) {
			n++
		}
	}
	return n
}

func (h *fakeHost) commandCount() int { return len(h.commands) }

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}

// enrolledDocument is the box.json a host's own enroll leaves behind. Its shape
// is internal/box's, and the facts in it are non-secrets — the private half is
// in the keyring on the host and is not in this document.
const enrolledDocument = `{
  "box_id": "box_fake_01",
  "account_id": "acct_1",
  "public_key": "ed25519:AAAA",
  "status": "active",
  "phase": "enrolled",
  "enrolled_at": "2026-10-12T10:00:00Z",
  "conductor": "https://box.test"
}`

// healthyReport is an observation a box in good order answers with: health ok,
// certificate with a month on it, disk measured.
func healthyReport() *boxd.Observation {
	days := 90
	return &boxd.Observation{
		SchemaVersion:   boxd.ObservationSchemaVersion,
		BoxID:           "box_fake_01",
		DeployedVersion: "1.2.3",
		Health:          boxd.Health{Status: "healthy", Phase: 0},
		Certificate:     boxd.Certificate{Status: "ok", DaysLeft: &days},
		Disk:            boxd.Disk{Status: "ok", Path: "/", TotalBytes: 1 << 40, FreeBytes: 1 << 39},
	}
}

// writeFakeBinary writes the cafe binary the setup installs, so the hash the
// check compares against is a hash of something that exists.
func writeFakeBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cafe")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec cafe-real \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func setupOptions(t *testing.T, host Host, binary string) Options {
	t.Helper()
	return Options{
		Host:         host,
		Name:         "box.test",
		Capability:   "cap_box_setup_once",
		BinaryPath:   binary,
		Range:        "^1.0.0",
		ControlPlane: "https://box.test",
		Version:      "1.2.3",
	}
}

// ---------------------------------------------------------------------------
// the whole flow
// ---------------------------------------------------------------------------

// The first setup on a bare host does every step, in order, and presents the
// capability exactly once. This is the table the brief's "one command" is: SSH
// in, bootstrap Docker the kamal way, install + enroll boxd, start it, verify
// the heartbeat.
func TestTheFirstSetupOnABareHostDoesEverythingInOrder(t *testing.T) {
	host := newFakeHost()
	host.binaryHash = mustBinaryHash(t)
	binary := writeFakeBinary(t)
	host.controlPlaneURL = "https://box.test"

	res, err := Setup(context.Background(), setupOptions(t, host, binary))
	if err != nil {
		t.Fatalf("Setup: %v\n%+v", err, res)
	}
	if !res.OK() {
		t.Fatalf("the run refused: %s", res.FailedStep())
	}

	want := []string{
		StepSuperuser, StepHostArch, StepDockerInstalled, StepDockerGroup, StepDockerRunning,
		StepCafeInstalled, StepBoxEnrolled, StepBoxdIdentity, StepBoxdUnit, StepBoxdReport,
	}
	if len(res.Steps) != len(want) {
		t.Fatalf("got %d steps, want %d: %+v", len(res.Steps), len(want), res.Steps)
	}
	for i, name := range want {
		if res.Steps[i].Name != name {
			t.Errorf("step %d = %s, want %s", i, res.Steps[i].Name, name)
		}
	}

	// Kamal's precedence, exactly: superuser? and installed? are asked BEFORE
	// the install runs, and in_docker_group? is asked after it because that is
	// where kamal's bootstrap asks it — a root box needs neither the group nor
	// the usermod. The order is not "whatever works".
	order := []string{"sudo -nl usermod", "docker -v", "get.docker.com", "id -nG", "docker version"}
	var at []int
	for _, c := range order {
		at = append(at, indexOfCommand(host, c))
	}
	for i := 1; i < len(at); i++ {
		if at[i-1] < 0 || at[i] < 0 || at[i-1] > at[i] {
			t.Errorf("kamal's precedence was not followed: positions %v for %v", at, order)
		}
	}
	if host.ran("get.docker.com") != 1 {
		t.Errorf("the Docker install ran %d times, want 1", host.ran("get.docker.com"))
	}

	// The daemon was installed, written and started.
	if _, ok := host.fs[CafeBinPath]; !ok {
		t.Errorf("the cafe binary was not installed at %s", CafeBinPath)
	}
	if got := host.mode[CafeBinPath]; got != 0o755 {
		t.Errorf("the binary's mode = %o, want 755", got)
	}
	if _, ok := host.fs[IdentityPath]; !ok {
		t.Errorf("the daemon identity was not written at %s", IdentityPath)
	}
	if _, ok := host.fs[UnitPath]; !ok {
		t.Errorf("the systemd unit was not written at %s", UnitPath)
	}
	if st := host.units[UnitName]; !st.enabled || !st.active {
		t.Errorf("the unit's state = %+v, want enabled and active", st)
	}

	// The heartbeat: the report arrived and the three fields were read.
	if res.Report == nil {
		t.Fatal("the run carries no observation document")
	}
	if res.Report.Health.Status != "healthy" {
		t.Errorf("health = %q, want healthy", res.Report.Health.Status)
	}
	if res.Report.Certificate.Status != "ok" {
		t.Errorf("certificate = %q, want ok", res.Report.Certificate.Status)
	}
	if res.Report.Disk.Status != "ok" {
		t.Errorf("disk = %q, want ok", res.Report.Disk.Status)
	}
	if res.Redemptions != 1 {
		t.Errorf("redemptions = %d, want 1", res.Redemptions)
	}
	if res.Enrolled == nil || res.Enrolled.BoxID != "box_fake_01" {
		t.Errorf("the enrolled box = %+v, want box_fake_01", res.Enrolled)
	}
}

func mustBinaryHash(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(writeFakeBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	return sha256Hex(raw)
}

func indexOfCommand(host *fakeHost, sub string) int {
	for i, c := range host.commands {
		if strings.Contains(c.Command, sub) {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// idempotence, proved twice
// ---------------------------------------------------------------------------

// Idempotence is the claim the packet is for, so it is asserted TWICE in one
// test: the second setup runs nothing, and the third setup also runs nothing.
// A single successful re-run can be luck — a step that happens to be absent from
// the table — and a table that only converges once is a table that will stop
// converging.
func TestASecondSetupChangesNothing(t *testing.T) {
	host := newFakeHost()
	host.binaryHash = mustBinaryHash(t)
	binary := writeFakeBinary(t)
	host.controlPlaneURL = "https://box.test"

	opts := setupOptions(t, host, binary)

	if _, err := Setup(context.Background(), opts); err != nil {
		t.Fatalf("the first setup: %v", err)
	}
	afterFirst := host.commandCount()
	redemptionsAfterFirst := host.redemptions

	res2, err := Setup(context.Background(), opts)
	if err != nil {
		t.Fatalf("the second setup: %v\n%+v", err, res2)
	}
	res3, err := Setup(context.Background(), opts)
	if err != nil {
		t.Fatalf("the third setup: %v\n%+v", err, res3)
	}

	for i, res := range []Result{res2, res3} {
		if !res.OK() {
			t.Fatalf("re-run %d refused: %s", i+2, res.FailedStep())
		}
		for _, step := range res.Steps {
			if step.Name == StepSuperuser {
				continue
			}
			if step.Action != ActionAlreadyDone {
				t.Errorf("re-run %d: step %s = %s, want %s", i+2, step.Name, step.Action, ActionAlreadyDone)
			}
		}
		if res.Redemptions != 0 {
			t.Errorf("re-run %d presented %d capabilities, want 0", i+2, res.Redemptions)
		}
		if host.redemptions != redemptionsAfterFirst {
			t.Errorf("the host was asked to redeem again: %d, want %d", host.redemptions, redemptionsAfterFirst)
		}
	}
	if host.ran("box enroll --token -") != 1 {
		t.Errorf("the enroll command ran %d times across three setups, want 1", host.ran("box enroll --token -"))
	}
	if host.ran("get.docker.com") != 1 {
		t.Errorf("the Docker install ran %d times, want 1", host.ran("get.docker.com"))
	}
	if got := host.commandCount(); got <= afterFirst {
		t.Errorf("the re-runs ran %d commands, want more than the first run's %d", got, afterFirst)
	}

	// The box the host holds is the one it had, not a second one.
	if res2.Enrolled == nil || res2.Enrolled.BoxID != "box_fake_01" {
		t.Errorf("the re-run's box = %+v, want box_fake_01", res2.Enrolled)
	}
	if res2.Report == nil || res2.Report.Health.Status != "healthy" {
		t.Errorf("the re-run carries no report: %+v", res2.Report)
	}
}

// The bootstrap log is checkpointed and re-readable: one line per step, in the
// order the steps ran, and a re-run changes none of them.
func TestTheBootstrapLogIsCheckpointedAndStable(t *testing.T) {
	host := newFakeHost()
	host.binaryHash = mustBinaryHash(t)
	binary := writeFakeBinary(t)
	opts := setupOptions(t, host, binary)

	first, err := Setup(context.Background(), opts)
	if err != nil {
		t.Fatalf("the first setup: %v", err)
	}
	second, err := Setup(context.Background(), opts)
	if err != nil {
		t.Fatalf("the second setup: %v", err)
	}

	if len(first.Log) != len(second.Log) {
		t.Fatalf("the log grew from %d lines to %d on a re-run:\n%v\n%v",
			len(first.Log), len(second.Log), first.Log, second.Log)
	}
	for i := range first.Log {
		if first.Log[i] == second.Log[i] {
			continue
		}
		// The re-run's lines say "already done" where the first said
		// "applied", and both are checkpointed in order.
		if !strings.Contains(second.Log[i], "already done") {
			t.Errorf("log line %d changed shape: %q -> %q", i, first.Log[i], second.Log[i])
		}
	}
	wantFirst := "  " + StepBoxEnrolled + ": applied"
	if !containsLine(first.Log, wantFirst) {
		t.Errorf("the first run's log has no %q line:\n%v", wantFirst, first.Log)
	}
	wantSecond := "  " + StepBoxEnrolled + ": already done"
	if !containsLine(second.Log, wantSecond) {
		t.Errorf("the re-run's log has no %q line:\n%v", wantSecond, second.Log)
	}
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// secret hygiene
// ---------------------------------------------------------------------------

// The capability is presented exactly once, on stdin, and appears nowhere else:
// not in a command's argv, not in a file's bytes, not in an answer the host gave
// back. This is the assertion the whole design rests on, and it is checked over
// EVERY call the run made rather than the one it was supposed to be in.
func TestTheCapabilityTravelsOnAPipeAndNowhereElse(t *testing.T) {
	const token = "cap_box_setup_secret_value"
	host := newFakeHost()
	host.binaryHash = mustBinaryHash(t)
	opts := setupOptions(t, host, writeFakeBinary(t))
	opts.Capability = token

	if _, err := Setup(context.Background(), opts); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	seen := 0
	for _, c := range host.commands {
		if strings.Contains(c.Command, token) {
			t.Errorf("the capability appeared in a COMMAND ARGUMENT: %s", c.Command)
		}
		if strings.Contains(c.Stdin, token) {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("the capability appeared on %d stdins, want exactly 1", seen)
	}
	for _, w := range host.writes {
		if strings.Contains(string(w.Data), token) {
			t.Errorf("the capability was WRITTEN to %s", w.Path)
		}
	}
	// The one file the setup does write on the host is the daemon's identity,
	// and it is a non-secret document by construction.
	if data, ok := host.fs[IdentityPath]; ok {
		if strings.Contains(string(data), "private") {
			t.Errorf("the daemon identity carries a private key: %s", data)
		}
	}
	// The controller's own world is untouched: no box.json, no keyring.
	if _, err := os.Stat(filepath.Join(t.TempDir(), "box.json")); err == nil {
		t.Error("the controller wrote a box identity")
	}
}

// The unit file carries CAF_BOX_IDENTITY and, when the operator gave a range,
// CAF_BOX_RANGE. A unit that did not carry them would start a daemon that
// could not find its own enrollment.
func TestTheUnitWiresTheDaemonEnvironment(t *testing.T) {
	host := newFakeHost()
	host.binaryHash = mustBinaryHash(t)
	opts := setupOptions(t, host, writeFakeBinary(t))
	opts.Range = "^1.4.0"

	if _, err := Setup(context.Background(), opts); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	unit := string(host.fs[UnitPath])
	for _, want := range []string{
		"User=root",
		"Environment=CAF_BOX_IDENTITY=" + IdentityPath,
		"Environment=CAF_BOX_RANGE=^1.4.0",
		"ExecStart=" + CafeBinPath + " boxd",
		"Restart=always",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("the unit is missing %q:\n%s", want, unit)
		}
	}
}

// ---------------------------------------------------------------------------
// the dry run
// ---------------------------------------------------------------------------

// A dry run prints the plan and runs none of it. "Prints the plan" means the
// steps in order with the titles a person reads; "runs none of it" means no
// package was installed, no keypair was made, and no capability was presented.
func TestADryRunPrintsThePlanAndRunsNothing(t *testing.T) {
	host := newFakeHost()
	host.binaryHash = mustBinaryHash(t)
	opts := setupOptions(t, host, writeFakeBinary(t))
	opts.DryRun = true

	res, err := Setup(context.Background(), opts)
	if err != nil {
		t.Fatalf("a dry run must not fail: %v", err)
	}
	if !res.OK() || !res.DryRun {
		t.Fatalf("res = %+v, want an OK dry run", res)
	}
	if host.installed["docker"] {
		t.Error("a dry run installed Docker")
	}
	if len(host.writes) != 0 {
		t.Errorf("a dry run wrote %d files", len(host.writes))
	}
	if host.redemptions != 0 {
		t.Errorf("a dry run presented %d capabilities", host.redemptions)
	}
	for _, s := range res.Steps {
		if s.Action != ActionPlanned {
			t.Errorf("step %s = %s, want %s", s.Name, s.Action, ActionPlanned)
		}
	}
	// The plan is the real table, not a summary: the same nine steps in the
	// same order a real run walks.
	want := []string{
		StepSuperuser, StepHostArch, StepDockerInstalled, StepDockerGroup, StepDockerRunning,
		StepCafeInstalled, StepBoxEnrolled, StepBoxdIdentity, StepBoxdUnit, StepBoxdReport,
	}
	for i, name := range want {
		if res.Steps[i].Name != name {
			t.Errorf("step %d = %s, want %s", i, res.Steps[i].Name, name)
		}
	}
}

// ---------------------------------------------------------------------------
// refusals, named
// ---------------------------------------------------------------------------

// A host with no root is refused by the first step, and the refusal names the
// remedy rather than dying on a sudo prompt.
func TestAHostWithoutRootIsRefusedByName(t *testing.T) {
	host := newFakeHost()
	host.binaryHash = mustBinaryHash(t)
	host.superuser = "none"
	opts := setupOptions(t, host, writeFakeBinary(t))

	res, err := Setup(context.Background(), opts)
	if err == nil {
		t.Fatal("a host with no root must be refused")
	}
	if res.FailedStep() != StepSuperuser {
		t.Errorf("failed step = %q, want %q", res.FailedStep(), StepSuperuser)
	}
	if host.installed["docker"] {
		t.Error("the walk continued past the superuser refusal")
	}
}

// A Docker install that fails is a refusal that names the step, and the walk
// stops there instead of enrolling a box that cannot run a container.
func TestAFailedDockerInstallIsARefusalThatNamesTheStep(t *testing.T) {
	host := newFakeHost()
	host.binaryHash = mustBinaryHash(t)
	host.failDocker = true
	opts := setupOptions(t, host, writeFakeBinary(t))

	res, err := Setup(context.Background(), opts)
	if err == nil {
		t.Fatal("a failed Docker install must be refused")
	}
	if res.FailedStep() != StepDockerInstalled {
		t.Errorf("failed step = %q, want %q", res.FailedStep(), StepDockerInstalled)
	}
	if host.redemptions != 0 {
		t.Errorf("the capability was presented on a run that failed at Docker: %d", host.redemptions)
	}
}

// A daemon that cannot report is a refusal that names the step and keeps the
// daemon's own words, because "the heartbeat failed" with nothing else in it
// sends a reader to a log that says nothing.
func TestASilentDaemonIsARefusalThatKeepsItsWords(t *testing.T) {
	host := newFakeHost()
	host.binaryHash = mustBinaryHash(t)
	host.reportErr = "cafe boxd: the enrolled identity at " + IdentityPath + " has no box_id"
	opts := setupOptions(t, host, writeFakeBinary(t))

	res, err := Setup(context.Background(), opts)
	if err == nil {
		t.Fatal("a daemon that cannot report must be refused")
	}
	if res.FailedStep() != StepBoxdReport {
		t.Errorf("failed step = %q, want %q", res.FailedStep(), StepBoxdReport)
	}
	detail := res.Steps[len(res.Steps)-1].Detail
	if !strings.Contains(detail, "has no box_id") {
		t.Errorf("the refusal does not keep the daemon's words: %q", detail)
	}
}

// A host that cannot be reached at all is a transport error, and the walk does
// not mistake it for a failed check — the difference between "ssh failed" and
// "the check said no" is the difference between diagnosing a network and
// reinstalling Docker.
func TestATransportFailureIsNotAFailedCheck(t *testing.T) {
	host := newFakeHost()
	host.binaryHash = mustBinaryHash(t)
	host.transportFail = true
	opts := setupOptions(t, host, writeFakeBinary(t))

	_, err := Setup(context.Background(), opts)
	if err == nil {
		t.Fatal("an unreachable host must fail")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("err = %v, want it to name the transport failure", err)
	}
	if host.ran("get.docker.com") != 0 {
		t.Error("the walk tried to install Docker on a host it could not reach")
	}
}

// A capability is required, and its absence is a usage mistake rather than a
// failure: nothing was attempted, so nothing could have gone wrong.
func TestSetupWithoutACapabilityRefusesToStart(t *testing.T) {
	host := newFakeHost()
	opts := setupOptions(t, host, writeFakeBinary(t))
	opts.Capability = "  "
	if _, err := Setup(context.Background(), opts); !errors.Is(err, ErrNoCapability) {
		t.Errorf("err = %v, want ErrNoCapability", err)
	}
	if len(host.commands) != 0 {
		t.Errorf("the host was contacted %d times without a capability", len(host.commands))
	}
}

// ---------------------------------------------------------------------------
// the plan, as data
// ---------------------------------------------------------------------------

// The plan is a table and the table is pure: it is a function of its options
// and nothing else, so a change to it shows up as a change to this document
// rather than as a surprise on a host.
func TestThePlanIsPureData(t *testing.T) {
	opts := setupOptions(t, newFakeHost(), writeFakeBinary(t))
	steps := plan(opts, "sudo -n -H", "deadbeef", []byte("binary"), &identityRead{BoxID: "box_1"},
		archFact{Answer: unameSpelling(runtime.GOOS, runtime.GOARCH), Refusal: "refused"})

	if len(steps) == 0 {
		t.Fatal("the plan is empty")
	}
	for _, s := range steps {
		if s.Check == "" {
			t.Errorf("step %s has no check", s.Name)
		}
		if s.Apply.Command == "" && s.Apply.Path == "" && s.Refusal == "" {
			t.Errorf("step %s has neither an apply nor a refusal", s.Name)
		}
	}
	// The steps that run a command under a root prefix carry it; the ones that
	// belong to the login user do not.
	for _, s := range steps {
		if s.Name == StepDockerGroup {
			if strings.HasPrefix(s.Check, "sudo") {
				t.Errorf("the docker group belongs to the login user, not to root: %s", s.Check)
			}
			continue
		}
		if s.Name == StepSuperuser || s.Name == StepDockerInstalled || s.Name == StepDockerRunning {
			continue
		}
		if s.Name == StepHostArch {
			// `uname` is nobody's secret and needs no root to answer. The step
			// runs it bare for the same reason the docker group's does, and a
			// prefix here would be a claim that reading /proc/version is a
			// privileged act.
			continue
		}
		if !strings.HasPrefix(s.Check, "sudo -n -H") {
			t.Errorf("step %s does not run as root: %s", s.Name, s.Check)
		}
	}
}
