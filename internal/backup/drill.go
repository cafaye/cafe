package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DrillOutcome is what one drill proved.
//
// Three words and not two, because "the restore worked" and "caf would not
// touch the service" are different facts with different fixes, and a boolean
// would merge them into one that means neither: a refused drill is a service
// whose backup is broken, and it is the operator's file to edit rather than a
// log to read.
type DrillOutcome string

const (
	// DrillPassed is a snapshot that was taken and restored into the scratch
	// database, and whose assertion held.
	DrillPassed DrillOutcome = "passed"
	// DrillFailed is a cycle that ran and did not restore. The scratch database
	// was still dropped; see drill.go's caller for why that ordering is fixed.
	DrillFailed DrillOutcome = "failed"
	// DrillRefused is caf checking the service and declining to touch it: the
	// two-file contract is broken, the backup configuration is unreadable, or
	// the deploy engine is not installed. Nothing was booted.
	DrillRefused DrillOutcome = "refused"
)

// Failed reports whether this outcome is one a human has to see. A passed drill
// is the only quiet one, and the alert path is exactly this predicate — which
// is why it is here and not spelled out twice in the caller.
func (o DrillOutcome) Failed() bool { return o != DrillPassed }

// The drill reasons, and the whole set.
//
// They are a separate list from contract.go's allReasons and deliberately so:
// those four name a broken CLAUSE between two files, and these name what
// happened to a DRILL. A reason nothing can reach is a reason a reader has to
// imagine, so each one here has a test that produces it — see drill_test.go.
const (
	// DrillReasonPassed is the one quiet outcome, named so a record with no
	// reason is a record caf could not classify rather than one that passed.
	DrillReasonPassed Reason = "caf-backup/drill-passed"
	// DrillReasonFailed is a cycle that ran and the restore proof did not hold.
	// The failing command's own words are in Detail, because a drill can fail
	// at five different steps and the step is the diagnosis.
	DrillReasonFailed Reason = "caf-backup/drill-failed"
	// DrillReasonRefused is a refusal. The specific clause is in the report and
	// in Detail; this says the class.
	DrillReasonRefused Reason = "caf-backup/drill-refused"
	// DrillReasonNotRun is caf being unable to start: no deploy engine on PATH,
	// or a project caf could not read. It is a FAILURE and not a fourth quiet
	// state, because a drill that never ran has proven nothing and a record
	// that said otherwise would be the gap this package exists to close.
	DrillReasonNotRun Reason = "caf-backup/drill-not-run"
)

// drillReasons is every reason, for the test that keeps the set honest.
var drillReasons = []Reason{DrillReasonPassed, DrillReasonFailed, DrillReasonRefused, DrillReasonNotRun}

// DrillRecord is one drill, as a fact about a service rather than a paragraph
// on a terminal.
//
// Every field is a non-secret: the service, the engine, the scratch database's
// NAME, the tables that were asserted, the verdict and how long it took. No
// credential appears here and none can, because nothing in the cycle's result
// is one — caf never reads a credential's value, and a record that could carry
// one would make every upload of the state directory a leak. That is the same
// split box.json and config.json make, for the same reason.
type DrillRecord struct {
	Service string `json:"service"`
	// Engine is the one that ran: EngineKamal or EngineCNPG.
	Engine string `json:"engine"`
	// Env is the Kamal destination overlay, empty when there was none.
	Env string `json:"environment,omitempty"`
	// Scratch is the database the restore was drilled into. It is dropped on
	// every path, so this names something that no longer exists — which is the
	// point of recording it: it says WHICH database the verdict is about.
	Scratch string `json:"scratch"`
	// Tables are the ones that had to hold rows.
	Tables  []string     `json:"tables,omitempty"`
	Outcome DrillOutcome `json:"outcome"`
	// Reason is the machine-matchable name. See drillReasons.
	Reason Reason `json:"reason"`
	// Detail is the failing command's own words, or the refusal's, trimmed to
	// something a person can read in one line. It is prose and it is NOT what a
	// script matches on — Reason is.
	Detail string `json:"detail,omitempty"`
	// StartedAt and FinishedAt bound the drill. They are UTC so that two boxes
	// in two zones produce records that sort.
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	// DurationMS is FinishedAt minus StartedAt in milliseconds. It is stored
	// rather than derived so a consumer does not have to parse two timestamps
	// to answer "how long did the drill take", which is the question a
	// capacity trend asks.
	DurationMS int64 `json:"duration_ms"`
}

// Failed is DrillOutcome.Failed on the record's own outcome, so the alert path
// reads as the sentence it is: this record failed.
func (r DrillRecord) Failed() bool { return r.Outcome.Failed() }

// Age is how long ago the drill finished, against now.
//
// It is here because "the last drill was six months ago" is the other half of
// the claim a restore drill makes: a passing drill from before the last schema
// migration is a passing drill about a database that no longer exists, and the
// monthly report has to be able to say so without a second clock.
func (r DrillRecord) Age(now time.Time) time.Duration {
	return now.Sub(r.FinishedAt)
}

// drillHistoryLimit is how many records one service keeps.
//
// Twelve is a year of monthly drills, which is the cadence the fleet's tier
// copy states and the window a report is written against. It is a bound rather
// than a full history because this is a state directory and not an archive:
// an unbounded file is one that grows until somebody has to decide what to
// delete, and that decision is always made at the worst moment.
const drillHistoryLimit = 12

// DrillJournal is one service's drill history, held in one JSON file.
//
// It is a file per service rather than a directory of files per drill because
// the question it answers — "what did the last drill on this service prove" —
// is a question about ONE record, and a layout that made that a directory
// listing would make the ordinary read the expensive one.
type DrillJournal struct {
	Service string        `json:"service"`
	Records []DrillRecord `json:"records"`
}

// Latest is the most recent record, and false when the service has never been
// drilled. A service with no history is the state every service was in before
// this package existed, and it is an empty answer rather than an error.
func (j *DrillJournal) Latest() (DrillRecord, bool) {
	if len(j.Records) == 0 {
		return DrillRecord{}, false
	}
	return j.Records[len(j.Records)-1], true
}

// DrillStore is a directory of drill journals. Two Stores over one directory
// are two drills, and every append takes the store's lock so their
// read-modify-write cycles cannot interleave — the same idiom internal/ledger
// and internal/run use, for the same reason: the arbiter has to be the
// filesystem and not a process.
type DrillStore struct{ dir string }

// DefaultDrillDir is where drill records live when nothing says otherwise.
//
// The order is ledger.DefaultDir's, and it is that order for that reason: an
// environment variable first so a test — and a developer running two
// experiments — can point the whole tool at a scratch directory without a flag
// on every verb, then XDG_STATE_HOME, then the home directory.
//
// It is STATE and not config: these are records caf wrote about work it did,
// not decisions a person made, and a record that lived in the config directory
// would be a record that looked editable.
func DefaultDrillDir() (string, error) {
	if dir := os.Getenv("CAF_DRILL_DIR"); dir != "" {
		return dir, nil
	}
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, "caf", "drills"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve the drill directory: %w (set CAF_DRILL_DIR to say where drill records go)", err)
	}
	return filepath.Join(home, ".local", "state", "caf", "drills"), nil
}

// OpenDrillStore makes a store in a directory, creating the layout. It is Open
// and not New because opening a store that is already there is the normal
// case: the second drill of the month opens the one the first wrote.
func OpenDrillStore(dir string) (*DrillStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("drill store: the store needs a directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("drill store: create %s: %w", dir, err)
	}
	return &DrillStore{dir: dir}, nil
}

// Dir is where the store is held. A report prints it, because "no drill is
// recorded" and "the drill was recorded somewhere else" are different
// sentences.
func (s *DrillStore) Dir() string { return s.dir }

// validService is the guard on a service name becoming a path. A name with a
// separator in it would write outside the directory, and a store that let a
// caller do that has given every caller a write to the box.
func validService(service string) error {
	switch {
	case service == "":
		return errors.New("drill store: a record needs a service")
	case service == "." || service == "..":
		return fmt.Errorf("drill store: %q is not a service name", service)
	case strings.ContainsAny(service, "/\\\x00"):
		return fmt.Errorf("drill store: %q is not a service name: it contains a path separator", service)
	}
	return nil
}

func (s *DrillStore) path(service string) string {
	return filepath.Join(s.dir, service+".json")
}

// Append records one drill and returns the journal as it now stands.
//
// It reads, appends, bounds and rewrites inside the store lock, so two drills
// that overlap cannot each drop the other's record. The bound is applied here
// rather than on read: an unbounded file is the failure mode described at
// drillHistoryLimit, and a read that trimmed would trim a different amount on
// each reader.
func (s *DrillStore) Append(rec DrillRecord) (*DrillJournal, error) {
	if err := validService(rec.Service); err != nil {
		return nil, err
	}
	release, err := s.hold()
	if err != nil {
		return nil, err
	}
	defer func() { _ = release() }()

	journal, err := s.load(rec.Service)
	if err != nil {
		return nil, err
	}
	journal.Records = append(journal.Records, rec)
	if len(journal.Records) > drillHistoryLimit {
		journal.Records = append([]DrillRecord{}, journal.Records[len(journal.Records)-drillHistoryLimit:]...)
	}
	if err := writeDrillFile(s.path(rec.Service), journal); err != nil {
		return nil, err
	}
	return journal, nil
}

// Read is one service's whole journal. A service that has never been drilled
// is an empty journal and no error: a machine that has just been built is the
// ordinary first state, and a caller that had to special-case os.ErrNotExist
// would do it differently in each of the commands.
func (s *DrillStore) Read(service string) (*DrillJournal, error) {
	release, err := s.hold()
	if err != nil {
		return nil, err
	}
	defer func() { _ = release() }()
	return s.load(service)
}

// Latest is the newest record for one service, and false when there is none.
func (s *DrillStore) Latest(service string) (DrillRecord, bool, error) {
	journal, err := s.Read(service)
	if err != nil {
		return DrillRecord{}, false, err
	}
	rec, ok := journal.Latest()
	return rec, ok, nil
}

func (s *DrillStore) load(service string) (*DrillJournal, error) {
	raw, err := readDrillFile(s.path(service))
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return &DrillJournal{Service: service}, nil
	}
	var journal DrillJournal
	if err := json.Unmarshal(raw, &journal); err != nil {
		return nil, fmt.Errorf("drill store: the journal for %s at %s is not readable: %w; remove it once no drill is running, and the next drill starts a fresh history", service, s.path(service), err)
	}
	if journal.Service == "" {
		journal.Service = service
	}
	// Sorted on read as well as on write, because a record whose order came
	// from the file rather than from the facts is a record a reader cannot
	// trust — and the one mutation that would reorder them is a clock stepping
	// backwards, which happens.
	sort.SliceStable(journal.Records, func(i, j int) bool {
		return journal.Records[i].FinishedAt.Before(journal.Records[j].FinishedAt)
	})
	return &journal, nil
}

func readDrillFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("drill store: read %s: %w", path, err)
	}
	return raw, nil
}

// writeDrillFile writes by rename, and 0600.
//
// The rename is run/files.go's writeAtomic and the reason is the same one: a
// journal rewritten on every drill has to be either the old document or the
// new one, never half of each. The mode is 0600 because this is the box's own
// state and a state file a stray ls reads is a state file a stray ls learns the
// shape of.
func writeDrillFile(path string, journal *DrillJournal) error {
	raw, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return fmt.Errorf("drill store: encode %s: %w", path, err)
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("drill store: write %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("drill store: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("drill store: write %s: %w", path, err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("drill store: write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("drill store: write %s: %w", path, err)
	}
	return nil
}

// hold serialises this store's own mutations. It is the same primitive
// internal/run uses — an advisory lock on a file in the store — and it is
// unexported here for the same reason: the arbiter is an implementation detail
// and the surface is Append and Read.
func (s *DrillStore) hold() (func() error, error) {
	lock, err := s.lock()
	if err != nil {
		return nil, err
	}
	return lock.Release, nil
}
