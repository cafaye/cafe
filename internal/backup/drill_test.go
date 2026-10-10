package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The drill record's whole point is that it outlives the terminal it was
// printed on, so the store is asserted the way a store has to be: a record
// written is a record read back with every field intact, across a second
// handle over the same directory.

func drillStore(t *testing.T) *DrillStore {
	t.Helper()
	store, err := OpenDrillStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenDrillStore: %v", err)
	}
	return store
}

func record(service string, outcome DrillOutcome, finished time.Time) DrillRecord {
	started := finished.Add(-90 * time.Second)
	return DrillRecord{
		Service:    service,
		Engine:     EngineKamal,
		Env:        "production",
		Scratch:    service + "_drill",
		Tables:     []string{"users", "sessions"},
		Outcome:    outcome,
		Reason:     DrillReasonPassed,
		Detail:     "the snapshot restored",
		StartedAt:  started,
		FinishedAt: finished,
		DurationMS: finished.Sub(started).Milliseconds(),
	}
}

// The record survives the round trip with every field intact, and it survives
// it as JSON on disk: a consumer that is not this Go program reads the file,
// and the file's field names are the contract.
func TestADrillRecordSurvivesTheRoundTrip(t *testing.T) {
	store := drillStore(t)
	when := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	rec := record("identity", DrillPassed, when)

	if _, err := store.Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, ok, err := store.Latest("identity")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if !ok {
		t.Fatal("Latest answered ok=false after a record was appended")
	}
	if got.Service != "identity" || got.Engine != EngineKamal || got.Env != "production" {
		t.Errorf("the identity fields did not survive: %+v", got)
	}
	if got.Scratch != "identity_drill" {
		t.Errorf("scratch = %q, want identity_drill: the record has to name the database the verdict is about", got.Scratch)
	}
	if strings.Join(got.Tables, ",") != "users,sessions" {
		t.Errorf("tables = %v, want users and sessions", got.Tables)
	}
	if got.Outcome != DrillPassed || got.Reason != DrillReasonPassed {
		t.Errorf("the verdict did not survive: %+v", got)
	}
	if !got.FinishedAt.Equal(when) {
		t.Errorf("finished_at = %s, want %s", got.FinishedAt, when)
	}
	if got.DurationMS != 90000 {
		t.Errorf("duration_ms = %d, want 90000", got.DurationMS)
	}
	// The document on disk is the contract a non-Go consumer reads, so the
	// keys are asserted rather than assumed: a rename here would be a silent
	// break of the monthly report and the box agent at once.
	var doc map[string]any
	raw, err := os.ReadFile(filepath.Join(store.Dir(), "identity.json"))
	if err != nil {
		t.Fatalf("read the journal: %v", err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the journal is not JSON: %v", err)
	}
	for _, key := range []string{"service", "records"} {
		if _, present := doc[key]; !present {
			t.Errorf("the journal has no %q key: %s", key, raw)
		}
	}
	first := doc["records"].([]any)[0].(map[string]any)
	for _, key := range []string{"service", "engine", "environment", "scratch", "tables", "outcome", "reason", "started_at", "finished_at", "duration_ms"} {
		if _, present := first[key]; !present {
			t.Errorf("the record has no %q key: %s", key, raw)
		}
	}
}

// A service that has never been drilled is an empty answer and not an error.
// That was the state of every service before this package existed, and a store
// that reported it as a failure would make the first read of a fresh machine
// look like a broken one.
func TestAServiceNeverDrilledIsAnEmptyAnswer(t *testing.T) {
	store := drillStore(t)

	journal, err := store.Read("billing")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, ok := journal.Latest(); ok {
		t.Error("a service with no history answered with a record")
	}
	if len(journal.Records) != 0 {
		t.Errorf("records = %v, want none", journal.Records)
	}
}

// The history is bounded, and the bound keeps the NEWEST. A bound that kept the
// oldest would make a monthly report about a drill from a year ago, which is
// the exact claim this packet exists to stop being made.
func TestTheHistoryIsBoundedAndKeepsTheNewest(t *testing.T) {
	store := drillStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range drillHistoryLimit + 4 {
		when := base.AddDate(0, i, 0)
		if _, err := store.Append(record("identity", DrillPassed, when)); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	journal, err := store.Read("identity")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(journal.Records) != drillHistoryLimit {
		t.Fatalf("the journal holds %d records, want the bound of %d", len(journal.Records), drillHistoryLimit)
	}
	latest, ok := journal.Latest()
	if !ok {
		t.Fatal("the journal has no latest record")
	}
	want := base.AddDate(0, drillHistoryLimit+3, 0)
	if !latest.FinishedAt.Equal(want) {
		t.Errorf("the newest record is from %s, want %s: the bound dropped the wrong end", latest.FinishedAt, want)
	}
}

// The order is the facts and not the order of the appends. A clock that steps
// backwards — an NTP correction, a machine that slept — must not make a reader
// see a stale drill as the last one, because the one thing the record is
// consulted for is "when did we last prove a restore".
func TestTheOrderIsTheFactsNotTheAppendOrder(t *testing.T) {
	store := drillStore(t)
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	for _, offset := range []int{2, 0, 1} {
		if _, err := store.Append(record("identity", DrillPassed, base.AddDate(0, offset, 0))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	latest, ok, err := store.Latest("identity")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if !ok {
		t.Fatal("no record")
	}
	want := base.AddDate(0, 2, 0)
	if !latest.FinishedAt.Equal(want) {
		t.Errorf("latest finished at %s, want %s", latest.FinishedAt, want)
	}
}

// Every outcome except a pass is a failure a human has to see, and the
// predicate is the alert path — so it is asserted rather than trusted.
func TestOnlyAPassedDrillIsQuiet(t *testing.T) {
	for _, c := range []struct {
		outcome DrillOutcome
		failed  bool
	}{
		{DrillPassed, false},
		{DrillFailed, true},
		{DrillRefused, true},
	} {
		if got := c.outcome.Failed(); got != c.failed {
			t.Errorf("%s.Failed() = %v, want %v", c.outcome, got, c.failed)
		}
		if got := record("identity", c.outcome, time.Now()).Failed(); got != c.failed {
			t.Errorf("a %s record's Failed() = %v, want %v", c.outcome, got, c.failed)
		}
	}
}

// Every drill reason is a name and not a sentence, for the reason contract.go's
// reasons are: a script that matches a refusal greps for prose, and prose moves.
func TestEveryDrillReasonIsANameRatherThanASentence(t *testing.T) {
	if len(drillReasons) != 4 {
		t.Errorf("drillReasons holds %d reasons, want 4: %v", len(drillReasons), drillReasons)
	}
	for _, r := range drillReasons {
		if r == "" || strings.Contains(string(r), " ") {
			t.Errorf("reason %q is a sentence; reasons are the machine-matchable part of a verdict", r)
		}
		if !strings.HasPrefix(string(r), "caf-backup/") {
			t.Errorf("reason %q does not start with caf-backup/, so it cannot be attributed to this command", r)
		}
	}
	// And it is not the contract's list: those name a broken CLAUSE, these name
	// what happened to a DRILL, and a merged list would be a list where a
	// refusal's cause and a drill's verdict were the same fact.
	for _, r := range drillReasons {
		for _, contract := range allReasons {
			if r == contract {
				t.Errorf("%q is in both lists; a drill reason and a contract reason are different facts", r)
			}
		}
	}
}

// A service name is used to build a path, so a name that cannot survive that is
// refused rather than written somewhere else. A record about service
// "../../etc/cron.d/x" is a write outside the store.
func TestAServiceNameThatCannotBecomeAPathIsRefused(t *testing.T) {
	store := drillStore(t)
	for _, name := range []string{"", ".", "..", "a/b", `a\b`, "a\x00b"} {
		if _, err := store.Append(record(name, DrillPassed, time.Now())); err == nil {
			t.Errorf("Append accepted service %q, which would write outside the store", name)
		}
	}
}

// A file that is not readable is a failure and not an empty journal. A damaged
// record that read as "never drilled" would report a service as unproven when
// it was proven — or worse, as proven when nobody looked.
func TestADamagedJournalIsRefusedByName(t *testing.T) {
	store := drillStore(t)
	path := filepath.Join(store.Dir(), "identity.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := store.Read("identity")
	if err == nil {
		t.Fatal("a damaged journal read as an empty one")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the error does not name the file a person has to look at: %v", err)
	}
}

// Two drills over one directory serialise, and the loser is told rather than
// dropped. caf is one binary a schedule runs and an operator also runs, so two
// appends can genuinely overlap — and the one that lost has to be able to say
// so instead of quietly overwriting the other's history.
// Two appends both land, and the store's read-modify-write cycle is what makes
// that true: a journal rewritten from a stale read is a journal that dropped
// the other drill's record. The lock is the mechanism, and it is exercised here
// through the outcome rather than through a wait — see the lock test below for
// why a second blocking Append is not a thing to observe from one process.
func TestEveryAppendLands(t *testing.T) {
	store := drillStore(t)
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	for i := range 3 {
		if _, err := store.Append(record("identity", DrillPassed, base.AddDate(0, i, 0))); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	// A second handle over the same directory, which is the two-process
	// situation with no process in it.
	other, err := OpenDrillStore(store.Dir())
	if err != nil {
		t.Fatalf("OpenDrillStore: %v", err)
	}
	if _, err := other.Append(record("identity", DrillPassed, base.AddDate(0, 9, 0))); err != nil {
		t.Fatalf("Append through the second handle: %v", err)
	}

	journal, err := other.Read("identity")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(journal.Records) != 4 {
		t.Fatalf("the journal holds %d records, want all 4: one append was dropped", len(journal.Records))
	}
}

// The lock is what serialises them, and a caller that finds it held is TOLD
// rather than left waiting: a drill takes minutes, so a second one that blocked
// behind the first would be a command that looked broken for the length of it.
func TestTheStoreLockReportsWhenAnotherCafHoldsIt(t *testing.T) {
	store := drillStore(t)
	hold, err := store.tryLock()
	if err != nil {
		t.Fatalf("tryLock: %v", err)
	}

	if _, err := store.tryLock(); err == nil {
		t.Error("a second caf took the store's lock while the first held it")
	} else if !strings.Contains(err.Error(), "held") {
		t.Errorf("the loser was not told it was held: %v", err)
	}

	if err := hold.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	again, err := store.tryLock()
	if err != nil {
		t.Errorf("after the release the lock was still held: %v", err)
	}
	if err := again.Release(); err != nil {
		t.Errorf("release: %v", err)
	}
}

// The store's own file is 0600 and its directory 0700: this is the box's state,
// and a state file a stray ls reads is a state file a stray ls learns the shape
// of.
func TestTheStoreIsWrittenTightly(t *testing.T) {
	store := drillStore(t)
	if _, err := store.Append(record("identity", DrillPassed, time.Now())); err != nil {
		t.Fatalf("Append: %v", err)
	}
	for _, path := range []string{filepath.Join(store.Dir(), "identity.json")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s is %#o, want 0600", path, perm)
		}
	}
}

// The directory is resolved the way ledger.DefaultDir's is: the environment
// variable first, so a test and a developer running two experiments can point
// the whole tool at a scratch directory without a flag on every verb.
func TestTheDrillDirectoryFollowsTheEnvironment(t *testing.T) {
	want := filepath.Join(t.TempDir(), "drills")
	t.Setenv("CAF_DRILL_DIR", want)
	dir, err := DefaultDrillDir()
	if err != nil {
		t.Fatalf("DefaultDrillDir: %v", err)
	}
	if dir != want {
		t.Errorf("CAF_DRILL_DIR was not honoured: got %s, want %s", dir, want)
	}
}

// With no variable it is under the state directory and not the config
// directory: these are records caf wrote about work it did, not decisions a
// person made, and a record that lived beside config.json would be a record
// that looked editable.
func TestTheDrillDirectoryIsStateAndNotConfig(t *testing.T) {
	t.Setenv("CAF_DRILL_DIR", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir, err := DefaultDrillDir()
	if err != nil {
		t.Fatalf("DefaultDrillDir: %v", err)
	}
	if !strings.Contains(filepath.ToSlash(dir), "caf/drills") {
		t.Errorf("the drill directory is %s, want it under <state>/caf/drills", dir)
	}
}

// A store with no directory is a caller error, not a directory caf invents.
func TestAStoreWithNoDirectoryIsRefused(t *testing.T) {
	if _, err := OpenDrillStore("   "); err == nil {
		t.Error("OpenDrillStore accepted a blank directory")
	}
}
