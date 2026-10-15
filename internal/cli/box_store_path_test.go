package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// The measured defect: a box enrolled under one HOME read as "not enrolled"
// from another, because the store resolved under os.UserConfigDir. The box
// subject's default must not move when HOME does.
func TestBoxStoreDefaultIsBoxOwnedAndHomeIndependent(t *testing.T) {
	t.Setenv(boxDirEnv, "")
	homeA := t.TempDir()
	homeB := t.TempDir()

	t.Setenv("HOME", homeA)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(homeA, ".config"))
	dirA := boxDeps{}.withDefaults().dir

	t.Setenv("HOME", homeB)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(homeB, ".config"))
	dirB := boxDeps{}.withDefaults().dir

	if dirA == "" || dirB == "" {
		t.Fatalf("box default dir resolved empty: %q vs %q", dirA, dirB)
	}
	if dirA != dirB {
		t.Fatalf("box default dir moves with HOME: %q vs %q", dirA, dirB)
	}
	if strings.Contains(dirA, homeA) || strings.Contains(dirA, homeB) {
		t.Fatalf("box default dir %q is HOME-derived", dirA)
	}
	want := filepath.Join(defaultBoxStateDir(), "box")
	if dirA != want {
		t.Fatalf("box default dir = %q, want %q (beside the release state)", dirA, want)
	}
}

// The end-to-end half of the same defect: enroll under one HOME, status under
// another, through the real default resolution (CAF_BOX_DIR names the
// box-owned root the way /var/lib/cafe/box would on a host). Both answers
// must name the same enrolled box.
func TestBoxEnrollAndStatusAgreeAcrossHomes(t *testing.T) {
	boxDir := t.TempDir()
	t.Setenv(boxDirEnv, boxDir)
	homeA := t.TempDir()
	homeB := t.TempDir()

	cp := &countingBoxControlPlane{answer: fakeBoxInstallation(), cert: fakeBoxClientCertificate()}

	t.Setenv("HOME", homeA)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(homeA, ".config"))
	code, _, stderr := runBoxCLI(t, boxDeps{controlPlane: cp, url: "https://box.test"}, "enroll", "--token", boxTestToken)
	if code != 0 {
		t.Fatalf("enroll under HOME=%s exited %d: %s", homeA, code, stderr)
	}
	if cp.calls != 1 {
		t.Fatalf("enroll presented %d capabilities, want 1", cp.calls)
	}

	for _, home := range []string{homeA, homeB} {
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		code, stdout, stderr := runBoxCLI(t, boxDeps{controlPlane: cp, url: "https://box.test"}, "status", "--json")
		if code != 0 {
			t.Fatalf("status under HOME=%s exited %d: %s", home, code, stderr)
		}
		if !strings.Contains(stdout, `"enrolled": true`) && !strings.Contains(stdout, `"enrolled":true`) {
			t.Fatalf("status under HOME=%s does not read enrolled: %s", home, stdout)
		}
		if !strings.Contains(stdout, fakeBoxInstallation().BoxID) {
			t.Fatalf("status under HOME=%s names no box id: %s", home, stdout)
		}
	}
	if cp.calls != 1 {
		t.Fatalf("status runs presented capabilities: %d total, want 1", cp.calls)
	}
}

// The other subject is untouched: an agent principal belongs to whoever runs
// it, so its default stays under the user's config dir — and is never the
// box-owned root.
func TestAgentStoreStillResolvesUnderUserConfigDir(t *testing.T) {
	t.Setenv(boxDirEnv, "")
	agentDir := agentDeps{}.withDefaults().dir
	userDir, err := defaultConfigDir()
	if err != nil {
		t.Fatalf("resolve the user config dir: %v", err)
	}
	if agentDir == "" {
		t.Fatal("agent default dir resolved empty")
	}
	if agentDir != userDir {
		t.Fatalf("agent default dir = %q, want the user config dir %q", agentDir, userDir)
	}
	if agentDir == defaultBoxDir() {
		t.Fatalf("agent default dir %q is the box-owned root; the subjects must differ", agentDir)
	}
}

// The override is for tests and unusual layouts: when set, both box-subject
// readers resolve to it.
func TestBoxDirEnvOverrideIsHonored(t *testing.T) {
	custom := t.TempDir()
	t.Setenv(boxDirEnv, custom)
	if got := (boxDeps{}.withDefaults().dir); got != custom {
		t.Fatalf("box dir = %q, want override %q", got, custom)
	}
	if got := (providerDeps{}.withDefaults().dir); got != custom {
		t.Fatalf("provider dir = %q, want override %q", got, custom)
	}
}

// A provider verb reads the BOX's identity, so it resolves the BOX subject's
// directory — the same one `cafe box` resolves — and never the user's.
func TestProviderFollowsTheBoxSubject(t *testing.T) {
	t.Setenv(boxDirEnv, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if got := (providerDeps{}.withDefaults().dir); got != defaultBoxDir() {
		t.Fatalf("provider dir = %q, want the box-owned default %q", got, defaultBoxDir())
	}
}
