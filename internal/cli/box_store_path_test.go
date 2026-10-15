package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cafaye/cafe/internal/box"
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

// A box enrolled before the box-owned default migrates itself on first use:
// the first defaulted `cafe box status` moves the legacy identity, names the
// move on stderr, and answers enrolled — and the second run finds nothing.
func TestBoxStatusMigratesLegacyOnFirstUse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	legacy, err := legacyBoxDir()
	if err != nil {
		t.Fatalf("resolve the legacy box dir: %v", err)
	}
	boxID := "box-11111111-2222-3333-4444-555555555555"
	identity := `{"box_id":"` + boxID + `","account_id":"acct-x","public_key":"p","status":"active","phase":"enrolled","enrolled_at":"2026-10-15T00:00:00Z","box_url":"https://box.test"}`
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "box.json"), []byte(identity), 0o600); err != nil {
		t.Fatal(err)
	}

	boxDir := t.TempDir()
	t.Setenv(boxDirEnv, boxDir)
	cp := &countingBoxControlPlane{answer: fakeBoxInstallation(), cert: fakeBoxClientCertificate()}

	code, stdout, stderr := runBoxCLI(t, boxDeps{controlPlane: cp}, "status", "--json")
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, boxID) {
		t.Fatalf("migrated status names no box: %s", stdout)
	}
	if !strings.Contains(stderr, "migrated") || !strings.Contains(stderr, legacy) {
		t.Fatalf("migration sentence names neither the move nor the source: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(legacy, "box.json")); !os.IsNotExist(err) {
		t.Fatal("legacy identity still present: the migration copied instead of moving")
	}

	code, _, stderr = runBoxCLI(t, boxDeps{controlPlane: cp}, "status", "--json")
	if code != 0 {
		t.Fatalf("second status exited %d: %s", code, stderr)
	}
	if strings.Contains(stderr, "migrated") {
		t.Fatalf("second status migrated again: %s", stderr)
	}
}

// Both locations holding different identities refuses through the verbs too:
// the command exits non-zero and the legacy store is untouched.
func TestBoxStatusRefusesDivergedStores(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	legacy, err := legacyBoxDir()
	if err != nil {
		t.Fatalf("resolve the legacy box dir: %v", err)
	}
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyIdentity := `{"box_id":"box-legacy-0000-0000-000000000000","account_id":"a","public_key":"p","status":"active","phase":"enrolled","enrolled_at":"t","box_url":"u"}`
	if err := os.WriteFile(filepath.Join(legacy, "box.json"), []byte(legacyIdentity), 0o600); err != nil {
		t.Fatal(err)
	}

	boxDir := t.TempDir()
	t.Setenv(boxDirEnv, boxDir)
	newIdentity := `{"box_id":"box-new-ffff-ffff-ffffffffffff","account_id":"a","public_key":"p","status":"active","phase":"enrolled","enrolled_at":"t","box_url":"u"}`
	if err := os.WriteFile(filepath.Join(boxDir, "box.json"), []byte(newIdentity), 0o600); err != nil {
		t.Fatal(err)
	}

	cp := &countingBoxControlPlane{answer: fakeBoxInstallation(), cert: fakeBoxClientCertificate()}
	code, _, _ := runBoxCLI(t, boxDeps{controlPlane: cp}, "status", "--json")
	if code == 0 {
		t.Fatal("status over diverged stores exited 0; want a refusal")
	}
	if raw, err := os.ReadFile(filepath.Join(legacy, "box.json")); err != nil || string(raw) != legacyIdentity {
		t.Fatal("refusal moved the legacy identity it refused to prefer")
	}

	// And the refusal is the named sentinel, not a wrapped surprise.
	if _, err := boxStoreFor(nil, &Env{Stderr: io.Discard}, boxDeps{controlPlane: cp}); !errors.Is(err, box.ErrBoxStoreDiverged) {
		t.Fatalf("boxStoreFor = %v, want %v", err, box.ErrBoxStoreDiverged)
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
