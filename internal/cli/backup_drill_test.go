package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cafaye/caf/internal/backup"
)

// `caf backup drill` at the command layer: the outcome is recorded, the machine
// document is the machine document, and a failure is audible.
//
// The store itself is asserted in internal/backup; what is asserted here is
// everything the store cannot see — the flags, the dispatch, the exit code and
// which stream each half of the output lands on.

// drillEnv runs the drill verb against the project the test built, with the
// store pointed at a scratch directory.
func drillEnv(t *testing.T, dir string, deps backupDeps, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &errOut}
	c := newBackupCommand(deps)
	t.Chdir(dir)
	err := c.Execute(env, append([]string{"drill"}, args...))
	// codeFor first, and then the buffers are read: it is what prints the
	// router's `caf: ...` line, so a helper that snapshotted stderr before
	// calling it would assert on a stream that had not been written yet.
	code := codeFor(*env, err)
	return out.String(), errOut.String(), code
}

// drillDeps is a backupDeps with the drill store redirected. A test always sets
// it, for the reason every seam here is a parameter: a store nobody can point
// somewhere else is a store no test can assert on.
func drillDeps(runner backup.Runner, dir string) backupDeps {
	return backupDeps{runner: runner, drills: dir}
}

// loadJournal reads back what the drill recorded, so the assertions are about
// the bytes on disk rather than about what the command printed.
func loadJournal(t *testing.T, dir, service string) *backup.DrillJournal {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, service+".json"))
	if err != nil {
		t.Fatalf("no record was written at %s: %v", filepath.Join(dir, service+".json"), err)
	}
	journal := &backup.DrillJournal{}
	if err := json.Unmarshal(raw, journal); err != nil {
		t.Fatalf("the record is not JSON: %v", err)
	}
	return journal
}

// THE POINT OF THE PACKET. A drill that restores leaves a record of it, and the
// record says what the drill proved, how long it took and which database the
// verdict is about. Before this, the proof was a paragraph nobody kept.
func TestADrillThatRestoresLeavesARecordOfIt(t *testing.T) {
	dir := project(t)
	drills := filepath.Join(t.TempDir(), "drills")
	runner := &backupRunner{out: fixtureResolved(t)}

	stdout, stderr, code := drillEnv(t, dir, drillDeps(runner, drills), "--table", "users", "identity")
	if code != exitSuccess {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}
	if !strings.Contains(stdout, "the snapshot restored") {
		t.Errorf("the drill's own report is not on stdout:\n%s", stdout)
	}

	journal := loadJournal(t, drills, "identity")
	if len(journal.Records) != 1 {
		t.Fatalf("the journal holds %d records, want 1", len(journal.Records))
	}
	rec, ok := journal.Latest()
	if !ok {
		t.Fatal("no latest record")
	}
	if rec.Outcome != backup.DrillPassed || rec.Reason != backup.DrillReasonPassed {
		t.Errorf("outcome/reason = %s/%s, want passed/caf-backup/drill-passed", rec.Outcome, rec.Reason)
	}
	if rec.Service != "identity" || rec.Scratch != "identity_drill" {
		t.Errorf("the record does not name the service and scratch database: %+v", rec)
	}
	if strings.Join(rec.Tables, ",") != "users" {
		t.Errorf("tables = %v, want users", rec.Tables)
	}
	if rec.DurationMS < 0 {
		t.Errorf("duration_ms = %d, want a non-negative duration", rec.DurationMS)
	}
	if rec.StartedAt.IsZero() || rec.FinishedAt.IsZero() {
		t.Error("the record does not bound the drill in time")
	}
	// Nothing secret is in the record, and nothing can be. The scratch database
	// has been dropped, so its NAME is a fact and its contents are gone.
	if strings.Contains(strings.ToLower(jsonOf(t, rec)), "restic_password") {
		t.Error("the record carries a credential")
	}
}

// A drill that FAILS is recorded as a failure, with the reason the drill's own
// report named — the clause, when a clause broke, and not only the class. A
// script matching on the reason has to be able to tell which line to edit.
func TestADrillThatFailsIsRecordedWithTheReasonItFailedFor(t *testing.T) {
	dir := project(t)
	drills := filepath.Join(t.TempDir(), "drills")
	runner := &backupRunner{
		out:   fixtureResolved(t),
		fails: map[string]error{backup.Drill: errors.New("exit status 1")},
	}

	_, stderr, code := drillEnv(t, dir, drillDeps(runner, drills), "--table", "users", "identity")
	if code != exitFailure {
		t.Fatalf("exit = %d, want %d: a drill that did not restore must fail", code, exitFailure)
	}

	rec := latestRecord(t, drills, "identity")
	if !rec.Failed() {
		t.Fatalf("a failed drill was recorded as %s", rec.Outcome)
	}
	if rec.Reason != backup.DrillReasonFailed {
		t.Errorf("reason = %q, want %q", rec.Reason, backup.DrillReasonFailed)
	}
	if rec.Detail == "" {
		t.Error("a failed drill recorded no detail: the reason the record exists is to say WHY")
	}
	// The human half: one line naming the reason and where the record went.
	for _, want := range []string{
		"caf backup drill: caf-backup/drill-failed: identity did not prove a restore (failed)",
		"recorded in " + drills,
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the alert line is missing %q:\n%s", want, stderr)
		}
	}
}

// A refusal is a different fact from a failed restore — one is a file to edit
// and one is a log to read — and the record keeps them apart while still
// failing, because a service whose two files disagree has a broken backup.
func TestARefusedDrillIsRecordedAsARefusalAndNamesTheClause(t *testing.T) {
	dir := project(t)
	drills := filepath.Join(t.TempDir(), "drills")
	runner := &backupRunner{out: strings.Replace(fixtureResolved(t),
		"      - RESTIC_REPOSITORY\n", "", 1)}

	stdout, stderr, code := drillEnv(t, dir, drillDeps(runner, drills), "--table", "users", "identity")
	if code != exitFailure {
		t.Fatalf("exit = %d, want %d", code, exitFailure)
	}

	rec := latestRecord(t, drills, "identity")
	if rec.Outcome != backup.DrillRefused {
		t.Errorf("outcome = %s, want refused", rec.Outcome)
	}
	// The clause, not only the class: this is the reason the record extracts the
	// report's own vocabulary instead of writing a second one.
	if rec.Reason != backup.SecretNotDeclared {
		t.Errorf("reason = %q, want the clause %q the report named", rec.Reason, backup.SecretNotDeclared)
	}
	// The drill's own report still goes to stderr when --json is off... it does
	// not: with no --json the report is on stdout and stderr holds the alert.
	if !strings.Contains(stdout, string(backup.SecretNotDeclared)) {
		t.Errorf("the refusal report is not on stdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, "caf backup drill:") {
		t.Errorf("no alert line on stderr:\n%s", stderr)
	}
}

// --json writes ONE document to stdout and moves caf's report to stderr, so a
// script pipes stdout into a parser without skipping a line of prose. The
// document is the record, and every field a consumer needs is in it.
func TestJSONEmitsOneDocumentOnStdoutAndTheReportOnStderr(t *testing.T) {
	dir := project(t)
	drills := filepath.Join(t.TempDir(), "drills")
	runner := &backupRunner{out: fixtureResolved(t)}

	stdout, stderr, code := drillEnv(t, dir, drillDeps(runner, drills), "--table", "users", "--json", "identity")
	if code != exitSuccess {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}
	if !strings.Contains(stderr, "the snapshot restored") {
		t.Errorf("caf's report is not on stderr, where a person reads it:\n%s", stderr)
	}

	var doc backup.DrillRecord
	dec := json.NewDecoder(strings.NewReader(stdout))
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	// Exactly one document: a second decodable object means the stream has
	// something a parser will trip over.
	if dec.More() {
		t.Errorf("stdout holds more than one JSON document:\n%s", stdout)
	}
	if doc.Service != "identity" || doc.Outcome != backup.DrillPassed || doc.Reason != backup.DrillReasonPassed {
		t.Errorf("the document is not the record: %+v", doc)
	}
	if doc.Engine != backup.EngineKamal {
		t.Errorf("engine = %q, want kamal: the record has to name the engine that ran", doc.Engine)
	}
	for _, key := range []string{`"service"`, `"outcome"`, `"reason"`, `"duration_ms"`, `"finished_at"`} {
		if !strings.Contains(stdout, key) {
			t.Errorf("the document has no %s field, which a consumer needs:\n%s", key, stdout)
		}
	}
}

// --json on a dry run is a usage error, not a silent success and not an outcome
// invented out of nothing: a dry run runs nothing, so there is no outcome to
// emit and no record to write.
func TestJSONWithADryRunIsAUsageError(t *testing.T) {
	dir := project(t)
	drills := filepath.Join(t.TempDir(), "drills")
	runner := &backupRunner{out: fixtureResolved(t)}

	var out, errOut bytes.Buffer
	c := newBackupCommand(drillDeps(runner, drills))
	t.Chdir(dir)
	err := c.Execute(&Env{Stdout: &out, Stderr: &errOut}, []string{"drill", "--table", "users", "--dry-run", "--json", "identity"})

	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage (exit 2)", err)
	}
	if !strings.Contains(err.Error(), "--json") || !strings.Contains(err.Error(), "--dry-run") {
		t.Errorf("the error does not name the two flags that conflict: %v", err)
	}
}

// A dry run writes nothing. A drill that recorded an outcome without restoring
// anything would be the exact defect this packet exists to remove, arriving
// through the new code.
func TestADryRunWritesNoRecord(t *testing.T) {
	dir := project(t)
	drills := filepath.Join(t.TempDir(), "drills")
	runner := &backupRunner{out: fixtureResolved(t)}

	_, _, code := drillEnv(t, dir, drillDeps(runner, drills), "--table", "users", "--dry-run", "identity")
	if code != exitSuccess {
		t.Fatalf("exit = %d, want %d", code, exitSuccess)
	}
	if _, err := os.Stat(filepath.Join(drills, "identity.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a dry run wrote a record at %s", filepath.Join(drills, "identity.json"))
	}
}

// The drill never asks. A drill on a schedule has nobody to answer a prompt,
// and a command that hung waiting for one would be a schedule that never ran.
func TestADrillNeverAsks(t *testing.T) {
	dir := project(t)
	asked := false
	runner := &backupRunner{out: fixtureResolved(t)}

	_, _, code := drillEnv(t, dir, backupDeps{
		runner: runner,
		drills: filepath.Join(t.TempDir(), "drills"),
		confirm: func(string) (bool, error) {
			asked = true
			return false, nil
		},
	}, "--table", "users", "identity")

	if asked {
		t.Error("the drill asked for confirmation, so it would hang on a schedule with nobody to answer")
	}
	if code != exitSuccess {
		t.Errorf("exit = %d, want %d", code, exitSuccess)
	}
}

// A record that cannot be written is a failure and not a warning. The gap this
// packet closes is a verdict that was printed and forgotten, and a drill whose
// record did not land has been forgotten exactly as thoroughly — so it exits
// nonzero and the sentence names the variable that fixes it.
func TestARecordThatCannotBeWrittenFailsLoudly(t *testing.T) {
	dir := project(t)
	runner := &backupRunner{out: fixtureResolved(t)}
	// A path that cannot be a directory: the project's own deploy.yml. The
	// MkdirAll in OpenDrillStore will refuse it, which is the same refusal a
	// read-only home directory produces.
	blocked := filepath.Join(dir, "config", "deploy.yml", "drills")

	_, stderr, code := drillEnv(t, dir, drillDeps(runner, blocked), "--table", "users", "identity")
	if code != exitFailure {
		t.Fatalf("exit = %d, want %d: a drill whose record was lost must not exit 0", code, exitFailure)
	}
	if !strings.Contains(stderr, "CAF_DRILL_DIR") {
		t.Errorf("the error does not name the variable that fixes it:\n%s", stderr)
	}
}

// A plain `caf backup` records too, so a manual drill is not forgotten either
// — and it says nothing on stderr, because that contract is the cycle's and not
// this packet's to change.
func TestAPlainBackupRecordsAndKeepsStderrEmpty(t *testing.T) {
	dir := project(t)
	drills := filepath.Join(t.TempDir(), "drills")
	runner := &backupRunner{out: fixtureResolved(t)}

	var out, errOut bytes.Buffer
	c := newBackupCommand(drillDeps(runner, drills))
	t.Chdir(dir)
	err := c.Execute(&Env{Stdout: &out, Stderr: &errOut}, []string{"--yes", "--table", "users", "identity"})
	if err != nil {
		t.Fatalf("a passing cycle failed: %v", err)
	}
	if errOut.Len() != 0 {
		t.Errorf("a passing plain backup wrote to stderr:\n%s", errOut.String())
	}
	rec := latestRecord(t, drills, "identity")
	if rec.Outcome != backup.DrillPassed {
		t.Errorf("outcome = %s, want passed", rec.Outcome)
	}
	if rec.Service != "identity" {
		t.Errorf("the record is about %q, want identity", rec.Service)
	}
}

// The verb is dispatched by name, and the service is what is left after it — so
// `caf backup drill <service>` and `caf backup <service>` cannot be confused
// about which one ran.
func TestTheDrillVerbIsDispatchedByName(t *testing.T) {
	dir := project(t)
	runner := &backupRunner{out: fixtureResolved(t)}
	c := newBackupCommand(drillDeps(runner, filepath.Join(t.TempDir(), "drills")))
	t.Chdir(dir)
	var out bytes.Buffer

	// No service at all is a usage error naming the verb the user typed.
	err := c.Execute(&Env{Stdout: &out, Stderr: &out}, []string{"drill"})
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
	if !strings.Contains(err.Error(), "caf backup drill") {
		t.Errorf("the usage error does not read as the words the user typed: %v", err)
	}
}

// Two drills over one store both land, which is the property the store's lock
// exists for and the reason the record is not silently overwritten.
func TestTwoDrillsBothLand(t *testing.T) {
	dir := project(t)
	drills := filepath.Join(t.TempDir(), "drills")
	runner := &backupRunner{out: fixtureResolved(t)}

	for range 2 {
		_, _, code := drillEnv(t, dir, drillDeps(runner, drills), "--table", "users", "identity")
		if code != exitSuccess {
			t.Fatalf("exit = %d, want %d", code, exitSuccess)
		}
	}
	if got := len(loadJournal(t, drills, "identity").Records); got != 2 {
		t.Errorf("the journal holds %d records, want both drills", got)
	}
}

// The help a person reads before wiring a schedule has to say the four things
// that are not obvious from the flags: where the record goes, that it is state
// and not config, that caf is not a scheduler, and that the drill is what a
// schedule RUNS.
func TestTheDrillHelpSaysWhatIsNotObvious(t *testing.T) {
	for _, want := range []string{
		"CAF_DRILL_DIR",
		"state rather than config",
		"THE SCHEDULE IS NOT HERE",
		"caf-backup/drill-",
		"twelve",
	} {
		if !strings.Contains(backupDrillLongHelp, want) {
			t.Errorf("the drill's long help does not say %q", want)
		}
	}
	if strings.Contains(backupDrillLongHelp, "`") {
		t.Error("the long help contains a backtick, which cannot appear inside the raw string it is written as")
	}
}

// jsonOf is a record as JSON, for the assertion that nothing secret is in it.
func jsonOf(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// The drill's outcome is a time-bounded fact, and a consumer has to be able to
// answer "when did we last prove a restore" from the record alone — so the
// timestamps are real and the duration is consistent with them.
func TestTheRecordBoundsTheDrillInTime(t *testing.T) {
	dir := project(t)
	drills := filepath.Join(t.TempDir(), "drills")
	runner := &backupRunner{out: fixtureResolved(t)}

	before := time.Now().UTC()
	if _, _, code := drillEnv(t, dir, drillDeps(runner, drills), "--table", "users", "identity"); code != exitSuccess {
		t.Fatalf("exit = %d, want %d", code, exitSuccess)
	}
	after := time.Now().UTC()

	rec := latestRecord(t, drills, "identity")
	if rec.StartedAt.Before(before.Add(-time.Second)) || rec.FinishedAt.After(after.Add(time.Second)) {
		t.Errorf("the drill's window [%s, %s] is outside [%s, %s]", rec.StartedAt, rec.FinishedAt, before, after)
	}
	if rec.FinishedAt.Before(rec.StartedAt) {
		t.Error("the drill finished before it started")
	}
	if want := rec.FinishedAt.Sub(rec.StartedAt).Milliseconds(); rec.DurationMS != want {
		t.Errorf("duration_ms = %d, want %d from the timestamps", rec.DurationMS, want)
	}
}

// latestRecord is the newest record for one service, or a failure naming the
// service that has none. It is the call every assertion below makes, and it
// fails rather than answering a zero record — a test that read an absent file as
// an empty record would pass by proving nothing.
func latestRecord(t *testing.T, dir, service string) backup.DrillRecord {
	t.Helper()
	rec, ok := loadJournal(t, dir, service).Latest()
	if !ok {
		t.Fatalf("no drill is recorded for %s in %s", service, dir)
	}
	return rec
}
