package verify

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestALedgerRemembersAMintWithoutTheValue is the whole reason the ledger is
// shaped the way it is. The value arrives, is fingerprinted, and is dropped:
// the file on disk carries the name, the box that minted it, when, and a
// handle — and the CONTROL half asserts the name and the box are present,
// because a ledger that redacted everything would pass by leaking nothing at
// all.
func TestALedgerRemembersAMintWithoutTheValue(t *testing.T) {
	dir := t.TempDir()
	st := &State{SchemaVersion: StateSchemaVersion}

	const value = "the-minted-SECRET_KEY_BASE-value"
	at := time.Date(2026, 10, 11, 9, 30, 0, 0, time.UTC)
	rec, err := st.Mint("SECRET_KEY_BASE", "rotatable", "box-under-test", []byte(value), at)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if rec.Fingerprint == "" || rec.Fingerprint == value {
		t.Fatalf("the record's fingerprint is not a fingerprint: %q", rec.Fingerprint)
	}
	if err := st.Save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, StateFile))
	if err != nil {
		t.Fatalf("read the ledger back: %v", err)
	}
	body := string(raw)
	if strings.Contains(body, value) {
		t.Fatalf("the ledger on disk carries the value:\n%s", body)
	}
	for _, want := range []string{"SECRET_KEY_BASE", "box-under-test", rec.Fingerprint, "sealed_to"} {
		if !strings.Contains(body, want) {
			t.Errorf("the ledger does not carry %q:\n%s", want, body)
		}
	}

	// And the round trip holds the same facts, so a build that reads the
	// ledger gets the record the mint wrote.
	back, err := LoadState(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got, ok := back.Minted("SECRET_KEY_BASE")
	if !ok {
		t.Fatal("the ledger does not answer the enclave build's question")
	}
	if got.BoxID != rec.BoxID || got.Fingerprint != rec.Fingerprint || !got.MintedAt.Equal(at) {
		t.Errorf("record moved across the round trip:\nwant %+v\ngot  %+v", rec, got)
	}
}

// TestTheEnclaveBuildsQuestionIsAnsweredBeforeItMints is the build path: the
// ledger is asked, the answer is the record, and the second mint is refused
// rather than performed. A second SECRET_KEY_BASE forks every session cookie
// every user holds, so the refusal is the product.
func TestTheEnclaveBuildsQuestionIsAnsweredBeforeItMints(t *testing.T) {
	st := &State{SchemaVersion: StateSchemaVersion}
	if _, err := st.Mint("SECRET_KEY_BASE", "rotatable", "box-one", []byte("the-one-value"), time.Now()); err != nil {
		t.Fatalf("mint: %v", err)
	}

	if _, ok := st.Minted("SECRET_KEY_BASE"); !ok {
		t.Fatal("the ledger does not say the name is already minted, so a build would mint a second value")
	}

	_, err := st.Mint("SECRET_KEY_BASE", "rotatable", "box-two", []byte("a-second-value"), time.Now())
	if !errors.Is(err, ErrMintedByAnotherBox) {
		t.Fatalf("a second box minting the same name is not refused: %v", err)
	}
	for _, want := range []string{"SECRET_KEY_BASE", "box-one", "box-two"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s: %v", want, err)
		}
	}
	if len(st.Records) != 1 {
		t.Fatalf("the refused mint still moved the ledger: %+v", st.Records)
	}
	if !st.Proven("SECRET_KEY_BASE", []byte("the-one-value")) {
		t.Error("the recorded value does not prove against the ledger")
	}
	if st.Proven("SECRET_KEY_BASE", []byte("a-second-value")) {
		t.Error("an unrecorded value proves against the ledger")
	}
}

// TestTheMintingBoxMayRotateItsOwnValue is the one second mint the ledger
// permits: the same box, a fresh value, one record. A rotation by the box
// that minted it is the secret package's path and not the build's.
func TestTheMintingBoxMayRotateItsOwnValue(t *testing.T) {
	st := &State{SchemaVersion: StateSchemaVersion}
	first, err := st.Mint("COURIER_SECRET_BOX_KEY", "rotatable", "box-one", []byte("one"), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("first mint: %v", err)
	}
	second, err := st.Mint("COURIER_SECRET_BOX_KEY", "rotatable", "box-one", []byte("two"), time.Now())
	if err != nil {
		t.Fatalf("the minting box cannot rotate its own value: %v", err)
	}
	if len(st.Records) != 1 {
		t.Fatalf("a rotation left two records: %+v", st.Records)
	}
	if second.Fingerprint == first.Fingerprint {
		t.Error("the rotation kept the old fingerprint")
	}
	if st.Proven("COURIER_SECRET_BOX_KEY", []byte("one")) {
		t.Error("the rotated-out value still proves")
	}
	if !st.Proven("COURIER_SECRET_BOX_KEY", []byte("two")) {
		t.Error("the rotated-in value does not prove")
	}
}

// TestAnAbsentLedgerIsTheOrdinaryFirstState: no box has minted anything, and
// that is not an error. A ledger that refused to load when absent would make
// every first verify fail on a fresh box.
func TestAnAbsentLedgerIsTheOrdinaryFirstState(t *testing.T) {
	st, err := LoadState(t.TempDir())
	if err != nil {
		t.Fatalf("an absent ledger is a refusal: %v", err)
	}
	if len(st.Records) != 0 {
		t.Errorf("an absent ledger carries records: %+v", st.Records)
	}
	if _, ok := st.Minted("SECRET_KEY_BASE"); ok {
		t.Error("an absent ledger answers that a name was minted")
	}
}

// TestALedgerThisBuildDoesNotUnderstandIsRefused: half-reading a newer
// ledger's rows into a shape they were never written as is the failure that
// answers "already minted" for a name nobody read.
func TestALedgerThisBuildDoesNotUnderstandIsRefused(t *testing.T) {
	dir := t.TempDir()
	newer := State{SchemaVersion: StateSchemaVersion + 1, Records: []Record{{Name: "SECRET_KEY_BASE", BoxID: "box-future"}}}
	raw, err := json.Marshal(newer)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, StateFile), raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err = LoadState(dir)
	if err == nil {
		t.Fatal("a ledger from a newer build is read rather than refused")
	}
	if !strings.Contains(err.Error(), "schema version") {
		t.Errorf("the refusal does not name the version: %v", err)
	}
}

// TestTwoRunsOverOneLedgerProduceTheSameBytes is the determinism rule the
// whole tree holds: no timestamp of the file's own, no hostname, no path. A
// diff between two ledgers is a diff between two trees.
func TestTwoRunsOverOneLedgerProduceTheSameBytes(t *testing.T) {
	at := time.Date(2026, 10, 11, 9, 30, 0, 0, time.UTC)
	write := func(dir string) []byte {
		st := &State{SchemaVersion: StateSchemaVersion}
		if _, err := st.Mint("SECRET_KEY_BASE", "rotatable", "box-one", []byte("one"), at); err != nil {
			t.Fatalf("mint: %v", err)
		}
		if _, err := st.Mint("COURIER_SECRET_BOX_KEY", "rotatable", "box-one", []byte("two"), at); err != nil {
			t.Fatalf("mint: %v", err)
		}
		if err := st.Save(dir); err != nil {
			t.Fatalf("save: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, StateFile))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return raw
	}
	first := write(t.TempDir())
	second := write(t.TempDir())
	if string(first) != string(second) {
		t.Errorf("two runs over one ledger disagree:\n%s\n---\n%s", first, second)
	}
}
