package box

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testRing is the in-memory keyring: three lines, and the whole point of the
// Keyring seam. A test of this package runs on a runner with no keychain, no
// D-Bus and no home directory to speak of.
type testRing struct{ entries map[string]string }

func newTestRing() *testRing { return &testRing{entries: map[string]string{}} }

func (r *testRing) key(service, account string) string { return service + "/" + account }

func (r *testRing) Get(service, account string) (string, error) {
	v, ok := r.entries[r.key(service, account)]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (r *testRing) Set(service, account, value string) error {
	r.entries[r.key(service, account)] = value
	return nil
}

func (r *testRing) Delete(service, account string) error {
	delete(r.entries, r.key(service, account))
	return nil
}

// countingControlPlane counts redemptions and answers with what the case planted.
//
// The count is the assertion this whole table exists for: a retried enrollment
// that redeems twice has not been proved idempotent, it has merely been lucky,
// because box would have refused the second one anyway.
type countingControlPlane struct {
	calls        int
	installation *Installation
	err          error
	// failIfCalled turns the fake into a tripwire: any attempt to redeem fails
	// the test. It is how the "already recorded" rows are proved — they must
	// reach an enrolled box WITHOUT asking the server anything.
	tripwire bool
}

func (c *countingControlPlane) Redeem(_ context.Context, _ string, _ ed25519.PublicKey) (*Installation, error) {
	c.calls++
	if c.tripwire {
		panic("unreachable: a recovered enrollment must never present the capability again")
	}
	if c.err != nil {
		return nil, c.err
	}
	return c.installation, nil
}

func fakeInstallation() *Installation {
	return &Installation{
		BoxID:     "box-11111111-2222-3333-4444-555555555555",
		AccountID: "acct-aaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Status:    "active",
		Phase:     phaseEnrolled,
	}
}

// openTestStore is a store in a directory the test owns, with a clock the test
// controls so a timestamp is never the reason an assertion failed.
func openTestStore(t *testing.T) (*Store, *countingControlPlane) {
	t.Helper()
	cp := &countingControlPlane{installation: fakeInstallation()}
	s := NewStore(t.TempDir(), newTestRing(), cp)
	s.now = func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }
	return s, cp
}

// testToken is a capability in box's own shape: `cap_` plus 64 hex
// characters. It is not a real one and it is never accepted by anything.
var testToken = "cap_" + strings.Repeat("a", 64)

// The enrollment's three stages are each drivable into "a retry converges",
// and the convergence is measured as the number of redemptions performed.
//
// The stages are not hypothetical: each one is a place a cafe process can stop.
// `prepared` is a run that died with a keypair on disk and nothing spent;
// `redeemed` is a run that died after presenting the capability and before
// writing box.json, which is the interesting one because the capability is
// gone and only the journal says so; and the third row is the ordinary case of
// an agent re-running the whole command.
func TestARetriedEnrollmentConvergesAndNeverRedeemsTwice(t *testing.T) {
	tests := []struct {
		name string
		// stage is the state the store is put in before the retry runs: nothing,
		// a journal at `prepared`, or a journal at `redeemed` with the key filed
		// the way a completed redemption leaves it.
		stage string
		// tripwire makes any redemption attempt fail the test, which is what the
		// rows that must not spend the capability look like.
		tripwire       bool
		wantAction     string
		wantRedemption int
		wantRecovered  bool
	}{
		{
			name:           "nothing yet: one keypair, one redemption",
			stage:          "",
			wantAction:     ActionEnrolled,
			wantRedemption: 1,
		},
		{
			name:           "a journal at prepared replays the redemption",
			stage:          journalPrepared,
			wantAction:     ActionEnrolled,
			wantRedemption: 1,
		},
		{
			name:           "a journal at redeemed finishes without presenting the capability again",
			stage:          journalRedeemed,
			tripwire:       true,
			wantAction:     ActionEnrolled,
			wantRedemption: 0,
			wantRecovered:  true,
		},
		{
			name:           "an already enrolled box refuses to be enrolled again",
			stage:          "complete",
			tripwire:       true,
			wantAction:     ActionAlreadyEnrolled,
			wantRedemption: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, cp := openTestStore(t)
			cp.tripwire = tt.tripwire
			if tt.stage != "" && tt.stage != "complete" {
				prepare(t, s, tt.stage)
			}

			// A run at the stage is performed first, so the `complete` row is a
			// completed enrollment rather than an assumption about one.
			if tt.stage == "complete" {
				cp.tripwire = false
				if _, err := s.Enroll(context.Background(), testToken, "https://box.test"); err != nil {
					t.Fatalf("first enroll: %v", err)
				}
				cp.tripwire = tt.tripwire
			}

			// The count is measured across the RETRY, not across the stage setup:
			// the `complete` row performs a real enrollment first, and the
			// question is whether the second attempt spends anything.
			before := cp.calls
			got, err := s.Enroll(context.Background(), testToken, "https://box.test")
			if err != nil {
				t.Fatalf("enroll: %v", err)
			}
			if got, want := cp.calls-before, tt.wantRedemption; got != want {
				t.Errorf("the retry presented the capability %d times, want %d", got, want)
			}
			if got.Action != tt.wantAction {
				t.Errorf("action = %q, want %q", got.Action, tt.wantAction)
			}
			if got.Redemptions != tt.wantRedemption {
				t.Errorf("redemptions = %d, want %d", got.Redemptions, tt.wantRedemption)
			}
			if got.Recovered != tt.wantRecovered {
				t.Errorf("recovered = %v, want %v", got.Recovered, tt.wantRecovered)
			}
			// Whatever the stage, the end state is the same: an identity on
			// disk, no journal, and the key filed under its permanent account.
			if _, err := s.Identity(); err != nil {
				t.Fatalf("no identity after a converging retry: %v", err)
			}
			if _, found, err := s.loadJournal(); err != nil || found {
				t.Errorf("journal after a converging retry: found=%v err=%v, want neither", found, err)
			}
		})
	}
}

// prepare puts the store into `stage` the way a cafe process that died there
// would have left it. It is written out rather than mocked because the state
// being tested is the state on disk, and a helper that crafted it some other
// way would be testing a different store.
func prepare(t *testing.T, s *Store, stage string) {
	t.Helper()
	box, err := generateBox()
	if err != nil {
		t.Fatal(err)
	}
	pub := publicKeyText(box.private.Public().(ed25519.PublicKey))
	if err := s.Ring.Set(ringService, journalPending, privateKeyText(box.private)); err != nil {
		t.Fatal(err)
	}
	jr := &journal{
		State:        stage,
		PublicKey:    pub,
		StartedAt:    "2026-10-09T11:59:00Z",
		ControlPlane: "https://box.test",
	}
	if stage == journalRedeemed {
		inst := fakeInstallation()
		jr.BoxID, jr.AccountID, jr.Status, jr.Phase = inst.BoxID, inst.AccountID, inst.Status, inst.Phase
		jr.RedeemedAt = "2026-10-09T12:00:00Z"
	}
	if err := s.writeJournal(jr); err != nil {
		t.Fatal(err)
	}
}

// The whole sequence, run twice, is the retry an agent actually performs. It is
// asserted at the CLI level in internal/cli because the number that matters is
// the number of redemptions across TWO PROCESSES, and a process is the only
// thing that forgets.
func TestTheWholeSequenceIsReplayable(t *testing.T) {
	s, cp := openTestStore(t)
	first, err := s.Enroll(context.Background(), "cap_first", "https://box.test")
	if err != nil {
		t.Fatalf("first enroll: %v", err)
	}
	if first.Action != ActionEnrolled || first.Redemptions != 1 {
		t.Fatalf("first = %+v", first)
	}
	second, err := s.Enroll(context.Background(), "cap_second", "https://box.test")
	if err != nil {
		t.Fatalf("second enroll: %v", err)
	}
	if second.Action != ActionAlreadyEnrolled {
		t.Errorf("second action = %q, want %q", second.Action, ActionAlreadyEnrolled)
	}
	if cp.calls != 1 {
		t.Errorf("control plane called %d times across two enrolls, want 1", cp.calls)
	}
	// The identity is byte-identical: a retry that rewrote it would be a retry
	// that lost the enrolled_at the first run recorded.
	raw, err := os.ReadFile(filepath.Join(s.Dir, identityFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), first.Identity.BoxID) {
		t.Errorf("identity does not name the box the first run enrolled:\n%s", raw)
	}
}

// A redemption that is refused must leave the box unenrolled and the journal
// at `prepared`, so that the NEXT attempt — with a fresh capability — can
// converge. The alternative is a client that clears its journal on failure and
// strands the keypair it had already filed.
func TestARefusedRedemptionLeavesARetryableState(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "already redeemed", err: ErrCapabilityRedeemed, want: "capability has already been redeemed"},
		{name: "expired", err: ErrCapabilityExpired, want: "capability has expired"},
		{name: "unknown", err: ErrCapabilityUnknown, want: "box does not know this capability"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, cp := openTestStore(t)
			cp.err = tt.err
			got, err := s.Enroll(context.Background(), testToken, "https://box.test")
			if err == nil {
				t.Fatal("a refused redemption must not report success")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to name %q", err, tt.want)
			}
			if got.Action != ActionRefused {
				t.Errorf("action = %q, want %q", got.Action, ActionRefused)
			}
			if _, err := s.Identity(); !errors.Is(err, ErrNotEnrolled) {
				t.Errorf("identity err = %v, want ErrNotEnrolled", err)
			}
			jr, found, jerr := s.loadJournal()
			if jerr != nil || !found || jr.State != journalPrepared {
				t.Errorf("journal = %+v found=%v err=%v, want a journal at %q", jr, found, jerr, journalPrepared)
			}
			// The keypair survived the refusal, so the retry converges rather
			// than starting a second one.
			if _, err := s.Ring.Get(ringService, journalPending); err != nil {
				t.Errorf("pending key after a refusal: %v", err)
			}

			// And the retry with a fresh capability does converge.
			cp.err = nil
			retry, err := s.Enroll(context.Background(), "cap_fresh", "https://box.test")
			if err != nil {
				t.Fatalf("retry after a refusal: %v", err)
			}
			if retry.Action != ActionEnrolled {
				t.Errorf("retry action = %q, want %q", retry.Action, ActionEnrolled)
			}
			if cp.calls != 2 {
				t.Errorf("control plane called %d times across the refusal and the retry, want 2", cp.calls)
			}
		})
	}
}

// The enrollment journal carries no token. A capability is a credential, and a
// credential in a JSON file under ~/.config is a credential in every backup,
// every archive and every transcript of a directory listing.
func TestTheJournalCarriesNoCredential(t *testing.T) {
	s, _ := openTestStore(t)
	if _, err := s.Enroll(context.Background(), testToken, "https://box.test"); err != nil {
		t.Fatal(err)
	}
	names, err := filepath.Glob(filepath.Join(s.Dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), testToken) {
			t.Errorf("%s carries the capability token", filepath.Base(name))
		}
		if strings.Contains(string(raw), "cap_") {
			t.Errorf("%s carries something that looks like a capability", filepath.Base(name))
		}
	}
}

// The private key is in the keyring and nowhere else, and no document any
// command produces carries it. This is invariant 1, and the assertion is over
// every JSON document the store can emit plus every file it wrote, because a
// value that reached a field nobody thought to check is exactly the case this
// test exists for.
func TestNoDocumentOrFileEverCarriesThePrivateKey(t *testing.T) {
	s, cp := openTestStore(t)
	ring := s.Ring.(*testRing)

	if _, err := s.Enroll(context.Background(), testToken, "https://box.test"); err != nil {
		t.Fatal(err)
	}
	id, err := s.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply("substrate"); err != nil {
		t.Fatal(err)
	}

	secret := ring.entries[ringService+"/"+ringAccount(id.BoxID)]
	if secret == "" {
		t.Fatal("no private key in the ring to assert about")
	}
	// The public key IS publishable; only the private half is not.
	if strings.Contains(secret, id.PublicKey) {
		t.Fatal("the ring entry is the public key, not the private one; the fixture is wrong")
	}

	var documents []string
	if st, err := s.Status(); err == nil {
		documents = append(documents, mustJSON(t, st))
	} else {
		t.Fatal(err)
	}
	if pl, err := s.Plan(); err == nil {
		documents = append(documents, mustJSON(t, pl))
	} else {
		t.Fatal(err)
	}
	evs, err := s.Logs()
	if err != nil {
		t.Fatal(err)
	}
	documents = append(documents, mustJSON(t, evs))
	if v, err := s.Verify(); err == nil {
		documents = append(documents, mustJSON(t, v))
	} else {
		t.Fatal(err)
	}
	if res, err := s.Apply("data"); err == nil {
		documents = append(documents, mustJSON(t, res))
	} else {
		t.Fatal(err)
	}

	for _, doc := range documents {
		if strings.Contains(doc, secret) {
			t.Fatalf("a document carries the box private key:\n%s", doc)
		}
	}

	// And the files. The identity must carry the public key, which is the
	// control half: a store that redacted everything would pass the assertion
	// above by leaking nothing at all.
	files, err := filepath.Glob(filepath.Join(s.Dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	var sawPublicKey bool
	for _, name := range files {
		raw, _ := os.ReadFile(name)
		if strings.Contains(string(raw), id.PublicKey) {
			sawPublicKey = true
		}
		if strings.Contains(string(raw), secret) {
			t.Errorf("%s carries the box private key", filepath.Base(name))
		}
	}
	if !sawPublicKey {
		t.Error("no file carries the public key, so the control half of this test proved nothing")
	}
	_ = cp
}

// Verify is the command that reports a boolean about the private key. Its
// detail field must say the key is present and must not contain the key, and a
// box whose key is missing must say so without inventing one.
func TestVerifyReportsFactsAndNeverTheKey(t *testing.T) {
	s, _ := openTestStore(t)
	ring := s.Ring.(*testRing)
	if _, err := s.Enroll(context.Background(), testToken, "https://box.test"); err != nil {
		t.Fatal(err)
	}
	id, _ := s.Identity()
	secret := ring.entries[ringService+"/"+ringAccount(id.BoxID)]

	v, err := s.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if !v.OK {
		t.Fatalf("a freshly enrolled box does not verify: %+v", v)
	}
	for _, f := range v.Facts {
		if strings.Contains(f.Detail, secret) {
			t.Errorf("fact %q carries the private key", f.Name)
		}
		if f.Secret && strings.Contains(f.Detail, "PRIVATE KEY") {
			t.Errorf("secret fact %q quotes a key header: %q", f.Name, f.Detail)
		}
	}

	// The key is removed the way a machine that lost its keychain would: the
	// identity remains, the key does not. Verify must report the fact and
	// nothing else.
	_ = ring.Delete(ringService, ringAccount(id.BoxID))
	v, err = s.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if v.OK {
		t.Error("a box with no key must not verify")
	}
	if !strings.Contains(mustJSON(t, v), "private_key_present") {
		t.Errorf("verify does not name the missing key:\n%s", mustJSON(t, v))
	}
	for _, f := range v.Facts {
		if strings.Contains(f.Detail, secret) {
			t.Errorf("fact %q carries a private key that is gone", f.Name)
		}
	}
}

// The lifecycle verbs on a box that has never enrolled. Each must refuse with
// the sentence that says what to run, and none of them may write anything or
// start an install.
func TestEveryLifecycleVerbRefusesAnUnenrolledBox(t *testing.T) {
	s, cp := openTestStore(t)

	// status ANSWERS rather than refusing: "this box is not enrolled, and here
	// is whether an enrollment is half-done" is the question an agent asks a
	// fresh box, and a refusal would throw away the half that is interesting.
	st, err := s.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Enrolled {
		t.Error("status reports an enrolled box on a store with no identity")
	}
	// A fresh box's log is empty, and reading it is not a failure.
	if evs, err := s.Logs(); err != nil || len(evs) != 0 {
		t.Errorf("logs = %v err = %v, want empty", evs, err)
	}

	// An unfinished enrollment is the one fact worth having in a status, so it
	// is asserted by putting the store into that state: a refused redemption
	// leaves exactly it.
	cp.err = ErrCapabilityRedeemed
	if _, err := s.Enroll(context.Background(), testToken, "https://box.test"); err == nil {
		t.Fatal("a refused capability must not enroll a box")
	}
	st, err = s.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Journal != journalPrepared {
		t.Errorf("status journal = %q, want %q", st.Journal, journalPrepared)
	}
	if st.Events == 0 {
		t.Error("a refused redemption wrote no event, so the box has no record of it")
	}

	if _, err := s.Plan(); !errors.Is(err, ErrNotEnrolled) {
		t.Errorf("plan err = %v, want ErrNotEnrolled", err)
	}
	if _, err := s.Apply("substrate"); !errors.Is(err, ErrNotEnrolled) {
		t.Errorf("apply err = %v, want ErrNotEnrolled", err)
	}
	// verify is the other one that ANSWERS rather than refuses: a box with no
	// identity is a fact to report, not a crash, because the question an agent
	// asks on a fresh box is exactly "did anything happen here".
	v, err := s.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if v.OK {
		t.Error("verify on an unenrolled box must not pass")
	}
}

// The install is sequential and apply is where that is enforced. Skipping is
// refused with a sentence that names the phase that IS next, because a box
// that reports a state it has not reached is the failure this table exists to
// prevent.
func TestApplyIsSequentialAndIdempotent(t *testing.T) {
	s, _ := openTestStore(t)
	if _, err := s.Enroll(context.Background(), testToken, "https://box.test"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Apply("data"); err == nil {
		t.Fatal("applying the phase after next must be refused")
	} else if !strings.Contains(err.Error(), "substrate") {
		t.Errorf("the refusal does not name the next phase: %v", err)
	}

	if _, err := s.Apply("bogus"); !errors.Is(err, ErrNoSuchPhase) {
		t.Errorf("unknown phase err = %v, want ErrNoSuchPhase", err)
	}

	// Re-applying the current phase converges.
	res, err := s.Apply("substrate")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("the first apply of the next phase must change the record")
	}
	res, err = s.Apply("substrate")
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed || res.Action != ApplyUnchanged {
		t.Errorf("re-applying the current phase: action=%q changed=%v, want %q/false", res.Action, res.Changed, ApplyUnchanged)
	}

	// Going backwards is a no-op that says so, not a rewind.
	res, err = s.Apply(phaseEnrolled)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Error("re-applying an earlier phase must not change the record")
	}

	// The last phase is reachable and names no next step.
	for _, name := range PhaseNames()[2:] {
		if _, err := s.Apply(name); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	plan, err := s.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if plan.Next != "" {
		t.Errorf("the final plan names a next phase: %q", plan.Next)
	}
	if plan.Reason == "" {
		t.Error("the final plan does not say why there is no next step")
	}
}

// A plan is a function of the stored state: it changes when the state changes
// and for no other reason.
func TestAPlanIsAFunctionOfTheStoredState(t *testing.T) {
	s, _ := openTestStore(t)
	if _, err := s.Enroll(context.Background(), testToken, "https://box.test"); err != nil {
		t.Fatal(err)
	}
	before, err := s.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Steps) != len(Phases) {
		t.Fatalf("plan has %d steps, want %d", len(before.Steps), len(Phases))
	}
	if before.Next != "substrate" {
		t.Errorf("next = %q, want substrate", before.Next)
	}
	if before.Steps[1].State != "pending" || !before.Steps[1].Appliable {
		t.Errorf("substrate row = %+v, want pending and appliable", before.Steps[1])
	}
	if before.Steps[0].State != "current" {
		t.Errorf("enrolled row = %+v, want current", before.Steps[0])
	}
	again, err := s.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if mustJSON(t, before) != mustJSON(t, again) {
		t.Error("two plans over one state differ")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
