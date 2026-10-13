package setup

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// seedBox puts a full box on the fake host: the cafe binary, cafaye's state,
// the daemon's unit, a running Docker, a k3s install — and, deliberately, the
// two things the wipe KEEPS (the operator's authorized keys and the releases
// directory) and one file no cafaye command created. The tests below assert
// that the first two survive and the third is untouched, which is the whole
// safety argument of the command.
func seedBox(h *fakeHost) {
	h.fs[CafeBinPath] = []byte("cafe binary")
	h.fs[IdentityPath] = []byte(`{"box_id":"box_wipe_01"}`)
	h.fs[EnrolledJSON] = []byte(`{"box_id":"box_wipe_01"}`)
	h.fs["/root/.config/cafe/keyring/box/box_wipe_01"] = []byte("private key")
	h.fs["/var/lib/kit/substrate-receipt.json"] = []byte(`{}`)
	h.fs["/var/lib/cafaye/bin/deploy"] = []byte("binary")
	h.fs["/var/lib/cafaye/substrate/pods/x"] = []byte("pod")
	h.fs["/var/lib/cafaye/edge/spec.json"] = []byte(`{}`)
	h.fs[UnitPath] = unitDocument(Options{Range: "^1.0.0"})

	// Kept: the operator's access, the deployed artifacts, and a stranger's file.
	h.fs["/root/.ssh/authorized_keys"] = []byte("ssh-ed25519 AAAA operator")
	h.fs["/var/lib/cafaye/releases/rel-1/app"] = []byte("release")
	h.fs["/srv/customer/data"] = []byte("not cafaye's")

	// k3s, as a live box holds it: the binary, the installer's scripts, and the
	// four directories from the measured box. `/usr/local/bin/kubectl` is
	// deliberately absent from the seed: it is a name every cluster tool writes
	// into, the wipe does not name it, and the installer's own script is what
	// removes it if it created it.
	h.fs[k3sBinary] = []byte("k3s")
	h.fs[k3sKillall] = []byte("killall")
	h.fs[k3sUninstall] = []byte("uninstall")
	h.fs[RancherEtcDir+"/k3s/k3s.yaml"] = []byte("kubeconfig")
	h.fs[RancherLibDir+"/k3s/server/db/state.db"] = []byte("db")
	h.fs[KubeletDir+"/pods/x"] = []byte("pod")
	h.fs[KubeConfigDir+"/config"] = []byte("kubeconfig")

	// Docker, running.
	h.installed["docker"] = true
	h.running = true
	h.fs[DockerEtcDir+"/daemon.json"] = []byte(`{}`)
	h.fs[DockerDataDir+"/overlay2/x"] = []byte("layer")
	h.fs[ContainerdDataDir+"/io.containerd.content.v1/x"] = []byte("content")

	// The daemon the wipe has to stop first.
	h.units[UnitName] = unitState{enabled: true, active: true}
}

func wipeOptions(host Host) WipeOptions {
	return WipeOptions{Host: host, Name: "box.test", Confirm: func(string) (bool, error) { return true, nil }}
}

// The first wipe does every step, in order, and the order is the table's.
func TestTheFirstWipeOnABoxRemovesEverythingInOrder(t *testing.T) {
	host := newFakeHost()
	seedBox(host)

	res, err := Wipe(context.Background(), wipeOptions(host))
	if err != nil {
		t.Fatalf("Wipe: %v\n%+v", err, res)
	}
	if !res.OK() {
		t.Fatalf("the run refused: %s", res.FailedStep())
	}

	want := []string{
		StepWipeFingerprint, StepSuperuser, StepWipeDaemon, StepWipeUnit,
		StepWipeBinary, StepWipeState, StepWipeK3s, StepWipeDocker,
	}
	if len(res.Steps) != len(want) {
		t.Fatalf("got %d steps, want %d: %+v", len(res.Steps), len(want), res.Steps)
	}
	for i, name := range want {
		if res.Steps[i].Name != name {
			t.Errorf("step %d = %s, want %s", i, res.Steps[i].Name, name)
		}
	}

	// The daemon was stopped before its binary went: the check table's own
	// dependency order, asserted by position rather than trusted.
	if a, b := indexOfCommand(host, wipeDaemonApply), indexOfCommand(host, wipeBinaryApply); a < 0 || b < 0 || a > b {
		t.Errorf("the daemon was not stopped before the binary was removed: %d > %d", a, b)
	}

	// Everything cafaye installed is gone.
	for _, p := range []string{
		CafeBinPath, IdentityPath, EnrolledJSON, UnitPath,
		"/var/lib/kit/substrate-receipt.json", "/var/lib/cafaye/bin/deploy",
		"/var/lib/cafaye/substrate/pods/x", "/var/lib/cafaye/edge/spec.json",
		k3sBinary, k3sUninstall,
		RancherEtcDir + "/k3s/k3s.yaml", RancherLibDir + "/k3s/server/db/state.db",
		KubeletDir + "/pods/x", KubeConfigDir + "/config",
		DockerEtcDir + "/daemon.json", DockerDataDir + "/overlay2/x",
		ContainerdDataDir + "/io.containerd.content.v1/x",
	} {
		if _, ok := host.fs[p]; ok {
			t.Errorf("the wipe left %s behind", p)
		}
	}
	if host.installed["docker"] {
		t.Error("the wipe left Docker installed")
	}
	if st := host.units[UnitName]; st.enabled || st.active {
		t.Errorf("the unit is still %+v", st)
	}

	// The two things it KEEPS are still there, and so is the stranger's file.
	for _, p := range []string{
		"/root/.ssh/authorized_keys",
		"/var/lib/cafaye/releases/rel-1/app",
		"/srv/customer/data",
	} {
		if _, ok := host.fs[p]; !ok {
			t.Errorf("the wipe removed a path it must keep: %s", p)
		}
	}
	if !contains(res.Kept, SSHAuthorizedKeys) || !contains(res.Kept, CafayeReleases) {
		t.Errorf("the document's kept list = %v, want the keys and the releases", res.Kept)
	}

	// No command the run issued touched the operator's access, the releases,
	// or a bare /var/lib/cafaye. This is the assertion the "never a rm -rf over
	// a directory somebody else may share" rule rests on.
	for _, c := range host.commands {
		if strings.Contains(c.Command, SSHAuthorizedKeys) {
			t.Errorf("a command names the operator's SSH directory: %s", c.Command)
		}
		if strings.Contains(c.Command, CafayeReleases) {
			t.Errorf("a command names the releases directory: %s", c.Command)
		}
		if strings.Contains(c.Command, "rm -rf -- "+CafayeStateDir+" ") ||
			strings.HasSuffix(c.Command, "rm -rf -- "+CafayeStateDir) {
			t.Errorf("a command removes /var/lib/cafaye wholesale: %s", c.Command)
		}
	}
}

// Idempotence, proved twice: the second and third wipes run nothing destructive.
// A passing check is the whole mechanism, and the count of destructive commands
// is what makes the claim measurable rather than a reading of the log.
func TestASecondWipeChangesNothing(t *testing.T) {
	host := newFakeHost()
	seedBox(host)

	opts := wipeOptions(host)
	if _, err := Wipe(context.Background(), opts); err != nil {
		t.Fatalf("the first wipe: %v", err)
	}
	destructiveAfterFirst := countDestructive(host)
	if destructiveAfterFirst == 0 {
		t.Fatal("the first wipe ran no destructive command, so the control below proves nothing")
	}

	for i := 2; i <= 3; i++ {
		res, err := Wipe(context.Background(), opts)
		if err != nil {
			t.Fatalf("wipe %d: %v\n%+v", i, err, res)
		}
		if !res.OK() {
			t.Fatalf("wipe %d refused: %s", i, res.FailedStep())
		}
		for _, step := range res.Steps {
			if step.Action != ActionAlreadyDone {
				t.Errorf("wipe %d: step %s = %s, want %s", i, step.Name, step.Action, ActionAlreadyDone)
			}
		}
		if got := countDestructive(host); got != destructiveAfterFirst {
			t.Errorf("wipe %d ran %d destructive commands in total, want the first wipe's %d",
				i, got, destructiveAfterFirst)
		}
	}
}

// countDestructive counts the commands that change the host — every step's
// apply — so "the re-run ran nothing" is a number rather than an impression.
func countDestructive(h *fakeHost) int {
	n := 0
	for _, c := range h.commands {
		switch {
		case strings.Contains(c.Command, "rm -rf"),
			strings.Contains(c.Command, "rm -f"),
			strings.Contains(c.Command, "systemctl disable --now"),
			strings.Contains(c.Command, "apt-get purge"):
			n++
		}
	}
	return n
}

// A dry run prints the plan and removes nothing. The plan is the real table,
// and the probes are read-only, so it is the same code path.
func TestAWipeDryRunPrintsThePlanAndRemovesNothing(t *testing.T) {
	host := newFakeHost()
	seedBox(host)

	res, err := Wipe(context.Background(), WipeOptions{Host: host, Name: "box.test", DryRun: true})
	if err != nil {
		t.Fatalf("a dry run must not fail: %v", err)
	}
	if !res.OK() || !res.DryRun {
		t.Fatalf("res = %+v, want an OK dry run", res)
	}
	for _, step := range res.Steps {
		if step.Action != ActionPlanned {
			t.Errorf("step %s = %s, want %s", step.Name, step.Action, ActionPlanned)
		}
	}
	if got := countDestructive(host); got != 0 {
		t.Errorf("a dry run ran %d destructive commands", got)
	}
	if _, ok := host.fs[CafeBinPath]; !ok {
		t.Error("a dry run removed the cafe binary")
	}
	if !host.installed["docker"] {
		t.Error("a dry run removed Docker")
	}
	want := []string{
		StepWipeFingerprint, StepSuperuser, StepWipeDaemon, StepWipeUnit,
		StepWipeBinary, StepWipeState, StepWipeK3s, StepWipeDocker,
	}
	for i, name := range want {
		if res.Steps[i].Name != name {
			t.Errorf("step %d = %s, want %s", i, res.Steps[i].Name, name)
		}
	}
}

// A host that was never a box is refused, named, and left completely alone:
// this is the refusal that keeps `cafe box wipe` from uninstalling a stranger's
// k3s.
func TestAWipeOnAHostThatIsNotABoxRefusesAndRemovesNothing(t *testing.T) {
	host := newFakeHost()
	// The stranger's machine: a k3s and a Docker cafe did not install, and not
	// one cafaye artifact among them.
	host.fs[k3sBinary] = []byte("k3s")
	host.fs["/var/lib/rancher/k3s/server/db/state.db"] = []byte("db")
	host.installed["docker"] = true
	host.running = true

	res, err := Wipe(context.Background(), wipeOptions(host))
	if err == nil {
		t.Fatal("a host that is not a box must be refused")
	}
	if res.FailedStep() != StepWipeFingerprint {
		t.Fatalf("failed step = %q, want %q", res.FailedStep(), StepWipeFingerprint)
	}
	refusal := res.Steps[len(res.Steps)-1].Detail
	if !strings.Contains(refusal, "not a box") {
		t.Errorf("the refusal does not say this is not a box: %q", refusal)
	}
	if got := countDestructive(host); got != 0 {
		t.Errorf("a refused wipe ran %d destructive commands", got)
	}
	if sum := host.ran("rm -rf"); sum != 0 {
		t.Errorf("a refused wipe ran %d rm -rf commands", sum)
	}
	if _, ok := host.fs[k3sBinary]; !ok {
		t.Error("a refused wipe removed the stranger's k3s")
	}
	if !host.installed["docker"] {
		t.Error("a refused wipe removed the stranger's Docker")
	}
}

// The control over the refusal: the same host WITH one cafaye artifact is a box
// and proceeds. A refusal that fired on every host would pass the test above
// and be useless.
func TestTheNotABoxRefusalCanFail(t *testing.T) {
	host := newFakeHost()
	// One artifact is enough — the releases directory the wipe keeps.
	host.fs[CafayeReleases+"/rel-1/app"] = []byte("release")

	res, err := Wipe(context.Background(), wipeOptions(host))
	if err != nil {
		t.Fatalf("a host holding a cafaye artifact must not be refused: %v\n%+v", err, res)
	}
	if !res.OK() {
		t.Fatalf("the run refused: %s", res.FailedStep())
	}
	if _, ok := host.fs[CafayeReleases+"/rel-1/app"]; !ok {
		t.Error("the wipe removed the releases directory it keeps")
	}
}

// The confirmation: the operator is asked once, after the probes, and a "no"
// removes nothing and exits non-zero. The prompt names what is kept, because a
// prompt that does not say what survives is a prompt people approve without
// reading.
func TestTheOperatorIsAskedBeforeAnythingIsRemoved(t *testing.T) {
	host := newFakeHost()
	seedBox(host)

	var prompt string
	asked := 0
	res, err := Wipe(context.Background(), WipeOptions{
		Host: host, Name: "box.test",
		Confirm: func(p string) (bool, error) {
			asked++
			prompt = p
			return false, nil
		},
	})
	if !errors.Is(err, ErrWipeDeclined) {
		t.Fatalf("err = %v, want ErrWipeDeclined", err)
	}
	if asked != 1 {
		t.Errorf("the operator was asked %d times, want 1", asked)
	}
	for _, want := range []string{"box.test", SSHAuthorizedKeys, CafayeReleases} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt does not name %q: %q", want, prompt)
		}
	}
	if got := countDestructive(host); got != 0 {
		t.Errorf("a declined wipe ran %d destructive commands", got)
	}
	if _, ok := host.fs[CafeBinPath]; !ok {
		t.Error("a declined wipe removed the cafe binary")
	}
	for _, step := range res.Steps {
		if step.Action != ActionPlanned {
			t.Errorf("step %s = %s, want %s", step.Name, step.Action, ActionPlanned)
		}
	}
}

// --yes is the same walk with a confirmation that says yes, and it is the only
// difference: an approved wipe runs to completion.
func TestAConfirmedWipeRunsToCompletion(t *testing.T) {
	host := newFakeHost()
	seedBox(host)

	res, err := Wipe(context.Background(), WipeOptions{
		Host: host, Name: "box.test",
		Confirm: func(string) (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatalf("a confirmed wipe failed: %v\n%+v", err, res)
	}
	if !res.OK() || res.FailedStep() != "" {
		t.Fatalf("the run refused: %s", res.FailedStep())
	}
	if _, ok := host.fs[CafeBinPath]; ok {
		t.Error("a confirmed wipe did not remove the cafe binary")
	}
}

// A wipe with NO confirmation seam is not a wipe: the destructive half needs an
// answer, and "nobody was asked" is a no. This is the library-level half of the
// CLI's --yes rule.
func TestAWipeWithNoConfirmationSeamDeclines(t *testing.T) {
	host := newFakeHost()
	seedBox(host)

	res, err := Wipe(context.Background(), WipeOptions{Host: host, Name: "box.test"})
	if !errors.Is(err, ErrWipeDeclined) {
		t.Fatalf("err = %v, want ErrWipeDeclined", err)
	}
	if got := countDestructive(host); got != 0 {
		t.Errorf("a wipe with no confirmation ran %d destructive commands", got)
	}
	if _, ok := host.fs[CafeBinPath]; !ok {
		t.Error("a wipe with no confirmation removed the cafe binary")
	}
	for _, step := range res.Steps {
		if step.Action != ActionPlanned {
			t.Errorf("step %s = %s, want %s", step.Name, step.Action, ActionPlanned)
		}
	}
}

// A removal that reports success and leaves the artifact in place is a refusal,
// not a green run. The control: a host that lies about one step turns it red.
func TestARemovalThatLeavesTheArtifactBehindIsRefused(t *testing.T) {
	host := newFakeHost()
	seedBox(host)
	// The binary's apply will delete nothing: the fake keeps answering the
	// binary check as "still there", which is what a `rm` that lied looks like.
	host.leaveBinaryBehind = true

	res, err := Wipe(context.Background(), wipeOptions(host))
	if err == nil {
		t.Fatalf("a removal that left the artifact must be refused: %+v", res)
	}
	if res.FailedStep() != StepWipeBinary {
		t.Errorf("failed step = %q, want %q", res.FailedStep(), StepWipeBinary)
	}
}

// The plan is a table and the table is pure: every row asks a question, and
// every destructive row runs under the root prefix.
func TestTheWipePlanIsPureData(t *testing.T) {
	steps := wipePlan("sudo -n -H")
	if len(steps) == 0 {
		t.Fatal("the plan is empty")
	}
	for _, s := range steps {
		if s.Check == "" {
			t.Errorf("step %s has no check", s.Name)
		}
		if s.Apply.Command == "" && s.Refusal == "" {
			t.Errorf("step %s has neither an apply nor a refusal", s.Name)
		}
	}
	for _, s := range steps {
		switch s.Name {
		case StepWipeFingerprint:
			// The fingerprint is a read and needs no root to answer.
			if strings.HasPrefix(s.Check, "sudo") {
				t.Errorf("the fingerprint read runs as root: %s", s.Check)
			}
		case StepSuperuser:
			if strings.HasPrefix(s.Check, "sudo") {
				t.Errorf("the superuser probe runs prefixed with its own answer: %s", s.Check)
			}
		default:
			if !strings.HasPrefix(s.Check, "sudo -n -H") || !strings.HasPrefix(s.Apply.Command, "sudo -n -H") {
				t.Errorf("step %s does not run as root: check=%q apply=%q", s.Name, s.Check, s.Apply.Command)
			}
		}
	}
}

// A wipe with no host is refused before the walk, with the sentinel the CLI
// reads to tell a usage mistake from a host refusal.
func TestWipeWithoutAHostRefusesToStart(t *testing.T) {
	if _, err := Wipe(context.Background(), WipeOptions{Name: "box.test"}); !errors.Is(err, ErrNoWipeHost) {
		t.Errorf("err = %v, want ErrNoWipeHost", err)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// Every path any removal step names is in the CLOSED SET. This is the answer to
// "how do you know the wipe never touched anything else": a step added later
// with a path of its own cannot widen the blast radius without also widening the
// list this test reads.
func TestEveryRemovalIsWithinTheClosedSet(t *testing.T) {
	allowed := map[string]bool{}
	for _, p := range wipeRemovablePaths() {
		allowed[p] = true
	}
	if len(allowed) == 0 {
		t.Fatal("the closed set is empty, so this test proves nothing")
	}
	seen := 0
	for _, step := range wipePlan("sudo -n -H") {
		if step.Apply.Command == "" {
			continue
		}
		for _, arg := range rmArguments(step.Apply.Command) {
			seen++
			if !allowed[arg] {
				t.Errorf("step %s removes %s, which is not in wipeRemovablePaths()", step.Name, arg)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no rm arguments were found, so the parser proves nothing")
	}
	// The negative control: the paths the wipe must never name are not in the
	// set, so a step that started naming one would fail the assertions above.
	for _, forbidden := range []string{SSHAuthorizedKeys, CafayeReleases, "/usr/local/bin/kubectl", "/", CafayeStateDir} {
		if allowed[forbidden] {
			t.Errorf("the closed set contains %s, which the wipe must never remove", forbidden)
		}
	}
}

// rmArguments pulls the paths out of every `rm` in a shell command: the tokens
// after `--`, up to the next command separator. It is a small parser for the one
// shape the plan uses, and the test above fails loudly if the shape changes and
// it finds nothing.
func rmArguments(command string) []string {
	var out []string
	for {
		i := strings.Index(command, "rm ")
		if i < 0 {
			break
		}
		command = command[i:]
		j := strings.Index(command, "-- ")
		if j < 0 {
			break
		}
		rest := command[j+3:]
		end := len(rest)
		for _, sep := range []string{";", "&", "|"} {
			if k := strings.Index(rest, sep); k >= 0 && k < end {
				end = k
			}
		}
		out = append(out, strings.Fields(rest[:end])...)
		command = rest[end:]
	}
	return out
}
