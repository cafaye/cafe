package box

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plantLegacyEnrollment writes a legacy store the way a pre-box-owned enroll
// left it: the identity, the journal, the credential beside it, the event log
// and the keyring — plus one entry that is NOT the box's, which the migration
// must never touch.
func plantLegacyEnrollment(t *testing.T, dir string) (boxID string) {
	t.Helper()
	boxID = "box-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	for name, data := range map[string]string{
		identityFile:        `{"box_id":"` + boxID + `","account_id":"acct-x","public_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","status":"active","phase":"enrolled","enrolled_at":"2026-10-15T00:00:00Z","box_url":"https://box.test"}`,
		journalFile:         `{"state":"prepared","public_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","started_at":"2026-10-15T00:00:00Z"}`,
		CertificateFileName: "-----BEGIN CERTIFICATE-----\nlegacy leaf\n-----END CERTIFICATE-----\n",
		PrivateKeyFileName:  "-----BEGIN PRIVATE KEY-----\nlegacy key\n-----END PRIVATE KEY-----\n",
		eventsFile:          "{\"action\":\"enroll\",\"message\":\"legacy line\"}\n",
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ring := filepath.Join(dir, "keyring")
	if err := os.MkdirAll(ring, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"cafe-box_" + strings.ReplaceAll(boxID, "-", "_"): "legacy private key",
		"cafe-box_enrollment-pending":                     "legacy pending key",
		"cafe-agent_someone":                              "not the box's; do not move",
	} {
		if err := os.WriteFile(filepath.Join(ring, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return boxID
}

// The migration MOVES, never copies: after it, the private key exists in
// exactly one place — and the agent's entry is still where it was.
func TestMigrateMovesNeverCopies(t *testing.T) {
	legacy := t.TempDir()
	boxID := plantLegacyEnrollment(t, legacy)
	dir := t.TempDir()

	msg, err := MigrateStore(dir, legacy)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !strings.Contains(msg, legacy) || !strings.Contains(msg, dir) {
		t.Fatalf("sentence %q names neither source nor destination", msg)
	}
	for _, name := range []string{identityFile, journalFile, CertificateFileName, PrivateKeyFileName, eventsFile} {
		if _, err := os.Stat(filepath.Join(legacy, name)); !os.IsNotExist(err) {
			t.Fatalf("legacy %s still present after migration", name)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("migrated %s missing: %v", name, err)
		}
	}
	for _, name := range []string{"cafe-box_" + strings.ReplaceAll(boxID, "-", "_"), "cafe-box_enrollment-pending"} {
		if _, err := os.Stat(filepath.Join(legacy, "keyring", name)); !os.IsNotExist(err) {
			t.Fatalf("legacy key %s still present: the key exists in two places", name)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "keyring", name))
		if err != nil || len(raw) == 0 {
			t.Fatalf("migrated key %s missing: %v", name, err)
		}
	}
	// The agent's entry is untouched, in the legacy ring where it belongs.
	if raw, err := os.ReadFile(filepath.Join(legacy, "keyring", "cafe-agent_someone")); err != nil || string(raw) != "not the box's; do not move" {
		t.Fatalf("agent keyring entry moved or changed: %v", err)
	}
	// A moved key keeps its mode: nothing in the move loosens it.
	if st, err := os.Stat(filepath.Join(dir, "keyring", "cafe-box_enrollment-pending")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("migrated key mode = %v, want 0600", st.Mode())
	}
}

// A second run finds nothing to do: the sentence appears once.
func TestMigrateIsIdempotent(t *testing.T) {
	legacy := t.TempDir()
	plantLegacyEnrollment(t, legacy)
	dir := t.TempDir()

	if _, err := MigrateStore(dir, legacy); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	msg, err := MigrateStore(dir, legacy)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if msg != "" {
		t.Fatalf("second migrate said %q, want silence", msg)
	}
}

// Both sides holding DIFFERENT identities is a refusal, not a preference —
// and the refusal moves nothing.
func TestMigrateBothPresentAndDisagreeingRefuses(t *testing.T) {
	legacy := t.TempDir()
	plantLegacyEnrollment(t, legacy)
	dir := t.TempDir()
	plantLegacyEnrollment(t, dir)
	other := `{"box_id":"box-ffffffff-ffff-ffff-ffff-ffffffffffff","account_id":"acct-y"}`
	if err := os.WriteFile(filepath.Join(dir, identityFile), []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := MigrateStore(dir, legacy)
	if !errors.Is(err, ErrBoxStoreDiverged) {
		t.Fatalf("migrate = %v, want %v", err, ErrBoxStoreDiverged)
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, identityFile)) || !strings.Contains(err.Error(), filepath.Join(legacy, identityFile)) {
		t.Fatalf("refusal %q names neither path", err)
	}
	if _, serr := os.Stat(filepath.Join(legacy, identityFile)); serr != nil {
		t.Fatal("refusal moved the legacy identity it refused to prefer")
	}
}

// Both sides holding the SAME identity converges: the legacy duplicate is
// removed, so the key exists in one place again.
func TestMigrateBothPresentAndAgreeingConverges(t *testing.T) {
	legacy := t.TempDir()
	boxID := plantLegacyEnrollment(t, legacy)
	dir := t.TempDir()
	raw, err := os.ReadFile(filepath.Join(legacy, identityFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, identityFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := MigrateStore(dir, legacy); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(legacy, identityFile)); !os.IsNotExist(err) {
		t.Fatal("legacy duplicate identity still present after convergence")
	}
	key := "cafe-box_" + strings.ReplaceAll(boxID, "-", "_")
	if _, err := os.Stat(filepath.Join(legacy, "keyring", key)); !os.IsNotExist(err) {
		t.Fatalf("legacy key %s still present: the key exists in two places", key)
	}
}

// Nothing anywhere is nothing to do — a fresh box enrolls rather than
// migrates.
func TestMigrateEmptyStoresAreSilent(t *testing.T) {
	msg, err := MigrateStore(t.TempDir(), t.TempDir())
	if err != nil || msg != "" {
		t.Fatalf("migrate = %q, %v; want silence", msg, err)
	}
}

// An override pointing at the legacy location migrates nothing: there is
// nowhere to move to, and a rename onto itself must never eat the file.
func TestMigrateSameDirIsSilent(t *testing.T) {
	legacy := t.TempDir()
	plantLegacyEnrollment(t, legacy)
	msg, err := MigrateStore(legacy, legacy)
	if err != nil || msg != "" {
		t.Fatalf("migrate = %q, %v; want silence", msg, err)
	}
	if _, err := os.Stat(filepath.Join(legacy, identityFile)); err != nil {
		t.Fatal("same-dir migration ate the identity")
	}
}

// A prepared-but-unredeemed enrollment (journal, no identity yet) migrates
// too: the retry resumes from the new store with the key it already filed.
func TestMigrateJournalWithoutIdentityMoves(t *testing.T) {
	legacy := t.TempDir()
	plantLegacyEnrollment(t, legacy)
	if err := os.Remove(filepath.Join(legacy, identityFile)); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	msg, err := MigrateStore(dir, legacy)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if msg == "" {
		t.Fatal("migrate said nothing about a journal it moved")
	}
	if _, err := os.Stat(filepath.Join(dir, journalFile)); err != nil {
		t.Fatalf("migrated journal missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(legacy, "keyring", "cafe-box_enrollment-pending")); !os.IsNotExist(err) {
		t.Fatal("legacy pending key still present: the key exists in two places")
	}
}
