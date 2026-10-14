package run

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The event kinds. They are a closed set on purpose: a log with an open
// vocabulary is a log two tools read differently, and the whole value of this
// one is that a driver and a report agree on what a line means.
const (
	// EventRunCreated is the run's first line, written with it.
	EventRunCreated = "run_created"
	// EventRunClaimed is a driver taking the run: the holder, and the epoch
	// the claim minted.
	EventRunClaimed = "run_claimed"
	// EventStepBegun is the record made BEFORE the work. It is the line a
	// crash leaves as the last thing that happened.
	EventStepBegun = "step_begun"
	// EventStepDone is the record made AFTER the work.
	EventStepDone = "step_done"
	// EventStepFailed is a step that ran and did not succeed. The run's
	// attempt count moved, so one of EventRunRequeued or EventRunFailed
	// follows it in the same critical section.
	EventStepFailed = "step_failed"
	// EventRunRequeued is the run handed back for another attempt, with the
	// step that will be re-run still named on the step_failed line before it.
	EventRunRequeued = "run_requeued"
	// EventRunFailed is the dead letter: attempts are exhausted.
	EventRunFailed = "run_failed"
	// EventRunSucceeded is the last step completing.
	EventRunSucceeded = "run_succeeded"
	// EventLeaseReleased is a driver letting go on purpose, which is not a
	// failure: the run stays where it is and the next driver resumes it.
	EventLeaseReleased = "lease_released"
)

// Event is one line of a run's log. It carries no credential and no payload
// a caller cannot read in a terminal: the log is the artifact most likely to
// be pasted into an issue, and a step's failure code is a string the caller
// chose.
type Event struct {
	Seq    int64     `json:"seq"`
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	RunID  string    `json:"run_id"`
	Holder string    `json:"holder,omitempty"`
	Step   string    `json:"step,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// appendEvent writes one line and returns the event it wrote, with the
// sequence number the log gave it.
//
// It is called with the store lock held, in the same critical section as the
// state change it records, and the caller writes the state FIRST. That order
// is the whole reason the log is trustworthy: no other driver can ever see or
// produce an interleaving, and a crash between the two writes can cost a log
// line but can never cost a state — the run document is the authority and the
// log is its history, so the one window this package cannot close from the
// inside loses the history rather than inventing it. The Postgres original
// has no such ordering to choose, because there the event insert is a
// statement outside the run's transaction.
func (s *Store) appendEvent(id, kind, holder, step, detail string) (Event, error) {
	seq, err := s.nextSeq(id)
	if err != nil {
		return Event{}, err
	}
	e := Event{Seq: seq, At: s.stamp(), Kind: kind, RunID: id, Holder: holder, Step: step, Detail: detail}
	raw, err := json.Marshal(e)
	if err != nil {
		return Event{}, fmt.Errorf("run %s: encode the %s event: %w", id, kind, err)
	}
	f, err := openAppend(s.eventsPath(id))
	if err != nil {
		return Event{}, err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(raw, '\n')); err != nil {
		return Event{}, fmt.Errorf("run %s: append the %s event: %w", id, kind, err)
	}
	return e, nil
}

// nextSeq is the sequence number the next line gets: one past the log's last
// line, or one for a log that does not exist yet. It reads the whole file
// rather than tracking a counter, because a counter in the run document
// would be a second copy of the log's length that a crash could leave
// disagreeing with it — and this file is small by construction: it is one
// run's events, not the fleet's.
func (s *Store) nextSeq(id string) (int64, error) {
	events, err := s.readEvents(id)
	if err != nil {
		return 0, err
	}
	if len(events) == 0 {
		return 1, nil
	}
	return events[len(events)-1].Seq + 1, nil
}

// Events is the run's log, oldest first.
//
// A corrupt line is skipped rather than failing the read, for the reason
// internal/box's log gives: a log a reader cannot open is a log nobody can
// debug, and one unreadable line must not cost the whole history. The
// sequence numbers make the gap visible, which is the honest way to report
// it — a missing number in the sequence is a fact, and a refused command is
// not.
func (s *Store) Events(id string) ([]Event, error) {
	return s.readEvents(id)
}

func (s *Store) readEvents(id string) ([]Event, error) {
	raw, err := readFile(s.eventsPath(id))
	if err != nil {
		return nil, err
	}
	out := []Event{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}
