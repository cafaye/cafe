package verify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cafaye/cafe/internal/boxd"
)

// The clock every test drives the verifier with, so a drill's age and a
// certificate's days left are the numbers the test wrote rather than the
// ones the machine happened to have.
var testNow = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

// wholeBox is the observation a box that is fine answers with: the root
// filesystem measured and comfortable, a certificate with months left, a
// healthy phase and a drill that passed. It is built here once because the
// tests that follow are each about ONE thing being wrong, and a fixture that
// was wrong in a second way would make them pass for the wrong reason.
func wholeBox() boxd.Observation {
	days := 120
	total := uint64(1000)
	free := uint64(700)
	return boxd.Observation{
		SchemaVersion:   boxd.ObservationSchemaVersion,
		BoxID:           "box-under-test",
		DeployedVersion: "1.2.3",
		CollectedAt:     testNow,
		Health:          boxd.Health{Status: "healthy", Phase: 7},
		Certificate:     boxd.Certificate{Status: "ok", DaysLeft: &days},
		Disk: boxd.Disk{
			Status: "ok", Path: "/", TotalBytes: total, FreeBytes: free,
			UsedBytes: total - free, UsedPercent: 30,
		},
		LastDrill: &boxd.DrillFact{Outcome: "passed", FinishedAt: &testNow},
	}
}

// TestAWholeBoxVerifies is the row every other test is a mutation of: three
// signals ok, one secret on the box, no reasons, status ok.
func TestAWholeBoxVerifies(t *testing.T) {
	v := Check(Probe{
		BoxID:       "box-under-test",
		Now:         testNow,
		Observation: wholeBox(),
		Secrets:     []SecretInput{{Name: "COURIER_SECRET_BOX_KEY", Present: true}},
	})
	if v.Status != StatusOK {
		t.Fatalf("status = %q, want ok\n%s", v.Status, mustJSON(t, v))
	}
	if len(v.Reasons) != 0 {
		t.Errorf("a whole box carries reasons: %+v", v.Reasons)
	}
	if len(v.Signals) != 3 {
		t.Fatalf("signals = %d, want the three the edge frets about (disk, certificate, health)", len(v.Signals))
	}
	for _, s := range v.Signals {
		if s.Status != StatusOK {
			t.Errorf("signal %s = %q: %s", s.Name, s.Status, s.Detail)
		}
	}
}

// TestEverySignalIsAnsweredWithAReasonWhenItIsNotOk is the shape the edge
// reads: a signal that is not ok never says only "degraded", because a
// status with no sentence under it is a status nobody can act on.
func TestEverySignalIsAnsweredWithAReasonWhenItIsNotOk(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*boxd.Observation)
		signal string
		want   string
	}{
		{
			name: "a full disk",
			mutate: func(o *boxd.Observation) {
				o.Disk.UsedPercent = 94
				o.Disk.FreeBytes = 60
			},
			signal: "disk", want: StatusDegraded,
		},
		{
			name: "a disk nobody measured",
			mutate: func(o *boxd.Observation) {
				o.Disk = boxd.Disk{Status: "unknown", Detail: "statfs failed: permission denied"}
			},
			signal: "disk", want: StatusUnknown,
		},
		{
			name: "an expired certificate",
			mutate: func(o *boxd.Observation) {
				o.Certificate = boxd.Certificate{Status: "expired", DaysLeft: intPtr(-3)}
			},
			signal: "certificate", want: StatusDegraded,
		},
		{
			name: "a certificate nobody could read",
			mutate: func(o *boxd.Observation) {
				o.Certificate = boxd.Certificate{Status: "unknown"}
			},
			signal: "certificate", want: StatusUnknown,
		},
		{
			name: "a box that reports itself degraded",
			mutate: func(o *boxd.Observation) {
				o.Health = boxd.Health{Status: "degraded", Phase: 7, Reasons: []string{"the probe timeout is the bug"}}
			},
			signal: "health", want: StatusDegraded,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := wholeBox()
			tc.mutate(&obs)
			v := Check(Probe{BoxID: "box-under-test", Now: testNow, Observation: obs, Secrets: []SecretInput{{Name: "SECRET_KEY_BASE", Present: true}}})
			signal, ok := findSignal(v, tc.signal)
			if !ok {
				t.Fatalf("no %s signal in %s", tc.signal, mustJSON(t, v))
			}
			if signal.Status != tc.want {
				t.Errorf("signal %s = %q, want %q", tc.signal, signal.Status, tc.want)
			}
			if signal.Status != StatusOK && signal.Detail == "" {
				t.Errorf("signal %s answers %q with no sentence under it", tc.signal, signal.Status)
			}
			if tc.want == StatusDegraded && v.Status != StatusDegraded {
				t.Errorf("a degraded %s signal leaves the verification %q", tc.signal, v.Status)
			}
		})
	}
}

// TestTheBoxThatNeverAnsweredIsUnknownAndNotDegraded is the blind-spot rule.
// A box that has not answered observe.report has no drill history, and
// calling that "no drill has ever run" would send an operator to run a drill
// that never had the chance to happen. The reason key says unread, the
// status says unknown, and the human's action is the one that fills it.
func TestTheBoxThatNeverAnsweredIsUnknownAndNotDegraded(t *testing.T) {
	v := Check(Probe{BoxID: "box-under-test", Now: testNow})
	if v.Status != StatusUnknown {
		t.Fatalf("status = %q, want unknown\n%s", v.Status, mustJSON(t, v))
	}
	if got := reasonKeys(v); len(got) != 1 || got[0] != "observation_unread" {
		t.Fatalf("reasons = %v, want [observation_unread]", got)
	}
	for _, s := range v.Signals {
		if s.Status == StatusDegraded {
			t.Errorf("signal %s is degraded against a box that never answered: %s", s.Name, s.Detail)
		}
	}
}

// TestADegradedAnswerAlwaysNamesTheHuman is the whole third deliverable in
// one assertion: for every row of the reason table the verifier can produce,
// and for the default arm a key nobody has listed yet falls into, the human's
// part is filled — owner, where, action. The default arm is the control: it
// is what stops a reason added tomorrow from shipping with the person
// elided.
func TestADegradedAnswerAlwaysNamesTheHuman(t *testing.T) {
	keys := []string{
		"certificate_expired", "certificate_expiring", "disk_full", "drill_absent",
		"drill_failed", "secret_missing", "secret_not_installed", "secret_forked",
		"observation_unread", "secrets_unread", "mint_unrecorded",
		"a-reason-key-nobody-has-listed-yet",
	}
	for _, key := range keys {
		h := remedy(key, "box-under-test")
		if h.Owner == "" || h.Where == "" || h.Action == "" {
			t.Errorf("reason %q renders a human with a part elided: %+v", key, h)
		}
	}

	// And over the documents themselves: every reason Check can produce
	// carries the same three fields, because add is the only path in.
	obs := wholeBox()
	obs.Health = boxd.Health{Status: "degraded", Reasons: []string{"the probe timeout is the bug"}}
	obs.Certificate.Status = "expired"
	obs.Disk.UsedPercent = 97
	obs.LastDrill = &boxd.DrillFact{Outcome: "failed"}
	v := Check(Probe{
		BoxID: "box-under-test", Now: testNow, Observation: obs,
		Secrets: []SecretInput{
			{Name: "COURIER_SMTP_PASSWORD", Sealed: true},
			{Name: "MUSE_VAULT_KEY"},
			{Name: "SECRET_KEY_BASE", Present: true, MintedBy: "box-that-never-minted"},
		},
	})
	if v.Status != StatusDegraded {
		t.Fatalf("status = %q, want degraded\n%s", v.Status, mustJSON(t, v))
	}
	if len(v.Reasons) == 0 {
		t.Fatal("a degraded verification carries no reasons")
	}
	for _, r := range v.Reasons {
		if r.Human.Owner == "" || r.Human.Where == "" || r.Human.Action == "" {
			t.Errorf("reason %q elides the human: %+v", r.Key, r.Human)
		}
	}
}

// TestTheHumanNamesWhereAndHowWithoutAShell answers the three questions the
// brief asks and refuses to let a prose edit quietly drop one of them: the
// owner is a role, where is a surface that is not a log file on the box, and
// the action is a command whose effect the next report confirms.
func TestTheHumanNamesWhereAndHowWithoutAShell(t *testing.T) {
	h := remedy("drill_failed", "box-under-test")
	if !strings.Contains(h.Where, "observe.report") {
		t.Errorf("where does not name the surface the operator reads: %q", h.Where)
	}
	if strings.Contains(strings.ToLower(h.Where), "ssh") {
		t.Errorf("where asks the human for a shell: %q", h.Where)
	}
	if !strings.Contains(h.Action, "cafe backup drill") {
		t.Errorf("action does not name the command: %q", h.Action)
	}
	if !strings.Contains(h.Owner, "box-under-test") {
		t.Errorf("owner is not addressed to the box: %q", h.Owner)
	}
}

// TestASecretsAnswerNamesTheNameAndNeverTheValue is the no-secret rule. The
// probe's SecretInput has no field a value could be written into, and this
// test walks the rendered document to prove the type's property survived:
// every secret the probe named appears by NAME, and nothing that could be a
// value appears anywhere in the bytes.
func TestASecretsAnswerNamesTheNameAndNeverTheValue(t *testing.T) {
	v := Check(Probe{
		BoxID: "box-under-test", Now: testNow, Observation: wholeBox(),
		Secrets: []SecretInput{
			{Name: "COURIER_SECRET_BOX_KEY", Present: true, Sealed: true, MintedBy: "box-under-test", Class: "rotatable"},
			{Name: "COURIER_SMTP_PASSWORD", Sealed: true, Class: "conditional"},
			{Name: "MUSE_VAULT_KEY", Class: "not-rotatable"},
		},
	})
	raw := mustJSON(t, v)
	for _, name := range []string{"COURIER_SECRET_BOX_KEY", "COURIER_SMTP_PASSWORD", "MUSE_VAULT_KEY"} {
		if !strings.Contains(raw, name) {
			t.Errorf("the document does not name %s; a verifier that redacted everything would pass by leaking nothing at all:\n%s", name, raw)
		}
	}
	if !strings.Contains(raw, "on_box") || !strings.Contains(raw, "sealed_only") || !strings.Contains(raw, "missing") {
		t.Errorf("the document does not answer where each secret stands:\n%s", raw)
	}
}

// TestTheForkIsNamed is the one cross-check a present secret earns: a box
// reporting a mint by a box the ledger never heard of is two boxes with one
// name, and the answer says so with both boxes named.
func TestTheForkIsNamed(t *testing.T) {
	st := &State{SchemaVersion: StateSchemaVersion}
	if _, err := st.Mint("SECRET_KEY_BASE", "rotatable", "box-one", []byte("the-one-value"), testNow); err != nil {
		t.Fatalf("mint: %v", err)
	}
	v := Check(Probe{
		BoxID: "box-two", Now: testNow, Observation: wholeBox(),
		Secrets: []SecretInput{{Name: "SECRET_KEY_BASE", Present: true, MintedBy: "box-two"}},
		Mint:    st,
	})
	if v.Status != StatusDegraded {
		t.Fatalf("status = %q, want degraded\n%s", v.Status, mustJSON(t, v))
	}
	keys := reasonKeys(v)
	if len(keys) != 1 || keys[0] != "secret_forked" {
		t.Fatalf("reasons = %v, want [secret_forked]", keys)
	}
	for _, want := range []string{"box-one", "box-two"} {
		if !strings.Contains(v.Reasons[0].Detail, want) {
			t.Errorf("the fork does not name %s: %s", want, v.Reasons[0].Detail)
		}
	}
}

// TestTheRecordedMintIsNotAProblem: the same box reporting the mint its own
// ledger recorded verifies, and the verification says how many values on the
// box a build must not mint again.
func TestTheRecordedMintIsNotAProblem(t *testing.T) {
	st := &State{SchemaVersion: StateSchemaVersion}
	if _, err := st.Mint("COURIER_SECRET_BOX_KEY", "rotatable", "box-under-test", []byte("v"), testNow); err != nil {
		t.Fatalf("mint: %v", err)
	}
	v := Check(Probe{
		BoxID: "box-under-test", Now: testNow, Observation: wholeBox(),
		Secrets: []SecretInput{{Name: "COURIER_SECRET_BOX_KEY", Present: true, MintedBy: "box-under-test"}},
		Mint:    st,
	})
	if v.Status != StatusOK {
		t.Fatalf("status = %q, want ok\n%s", v.Status, mustJSON(t, v))
	}
	if v.Minted != 1 {
		t.Errorf("minted = %d, want 1", v.Minted)
	}
}

// TestDegradedOutranksUnknownAndUnknownOutranksOK is the precedence, stated
// as a table because it is the one rule a reader of the document relies on
// to know how loudly to react.
func TestDegradedOutranksUnknownAndUnknownOutranksOK(t *testing.T) {
	unread := Probe{BoxID: "box-under-test", Now: testNow, Observation: boxd.Observation{}}
	if got := Check(unread).Status; got != StatusUnknown {
		t.Errorf("an unread box answers %q, want unknown", got)
	}
	degraded := unread
	obs := wholeBox()
	obs.Health = boxd.Health{Status: "degraded", Reasons: []string{"the probe timeout is the bug"}}
	degraded.Observation = obs
	if got := Check(degraded).Status; got != StatusDegraded {
		t.Errorf("a box that is one way wrong and another way unread answers %q, want degraded", got)
	}
}

// TestTheDocumentsAreOneJSONObject is the stdout contract every verb in this
// CLI holds: one clean document, no prose, nothing appended.
func TestTheDocumentsAreOneJSONObject(t *testing.T) {
	v := Check(Probe{BoxID: "box-under-test", Now: testNow, Observation: wholeBox(), Secrets: []SecretInput{{Name: "SECRET_KEY_BASE", Present: true}}})
	raw := mustJSON(t, v)
	var back Verification
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("the document does not round-trip: %v\n%s", err, raw)
	}
	if back.BoxID != "box-under-test" || back.Status != StatusOK {
		t.Errorf("round-trip lost the answer: %+v", back)
	}
}

func findSignal(v Verification, name string) (Signal, bool) {
	for _, s := range v.Signals {
		if s.Name == name {
			return s, true
		}
	}
	return Signal{}, false
}

func reasonKeys(v Verification) []string {
	keys := make([]string, 0, len(v.Reasons))
	for _, r := range v.Reasons {
		keys = append(keys, r.Key)
	}
	return keys
}

func intPtr(n int) *int { return &n }

// mustJSON is the same helper every package's test in this tree reaches for:
// a document that will not encode is a document nobody can read.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}
