package secret

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// StateFileName is the file the rotation schedule lives in.
//
// It holds NO secret and NO credential: entry names, timestamps, and the reason
// a rotation was refused. That is the same split internal/agent's config.json
// makes and for the same reason — a file that carried a credential would make
// every dump, every transcript and every backup of the state directory a leak.
const StateFileName = "secret-rotations.json"

// State is the schedule: when each entry was last rotated, and what the last run
// of the not-rotatable class answered.
//
// It is this machine's own record, not the control plane's. Boundary 8 lets box hold
// installation state and capabilities, and a rotation record IS installation
// state — but nothing in this packet needs a round trip to know whether a
// rotation is due, and a rotation that could not run offline could not run on a
// box whose control plane is unreachable. The record is written where the rotation
// happened.
type State struct {
	// Last is when each entry was last rotated, keyed by the entry's own
	// state key. An entry that is not in the map has never been rotated on
	// this box, which is the ordinary first state and is due.
	Last map[string]time.Time `json:"last_rotated,omitempty"`
	// Refused is the last refusal per entry, recorded so a report can say
	// "this is still not rotatable" rather than rediscovering it. The text is
	// the class and the reason, never a value.
	Refused map[string]string `json:"refused,omitempty"`
}

// NewState is the state of a box that has never rotated anything. Every
// rotatable entry is due, which is the honest answer on a box that has been up
// longer than the interval.
func NewState() State {
	return State{Last: map[string]time.Time{}, Refused: map[string]string{}}
}

// StateKey is the key an entry is recorded under. It is the
// `namespace/secret/key` spelling where the entry has one, and the bare name
// otherwise, because DATABASE_URL exists in two namespaces and a schedule keyed
// by name alone would let one box's two identities share one timestamp.
func (e Entry) StateKey() string {
	if s := e.String(); s != "" {
		return s
	}
	return e.Name
}

// Load reads the state file. A missing file is a fresh state rather than an
// error: a box that has never rotated is the ordinary first state, and a caller
// that had to special-case os.ErrNotExist would do it differently in each of the
// two commands. An unreadable or malformed one IS an error, because a schedule
// nobody can read is a schedule that will silently never run again.
func Load(dir string) (State, error) {
	state := NewState()
	raw, err := os.ReadFile(StatePath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, err
	}
	if len(raw) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, err
	}
	if state.Last == nil {
		state.Last = map[string]time.Time{}
	}
	if state.Refused == nil {
		state.Refused = map[string]string{}
	}
	return state, nil
}

// StatePath is where the state lives in one directory.
func StatePath(dir string) string { return filepath.Join(dir, StateFileName) }

// Save writes the state, creating the directory. It is written atomically
// through a sibling temporary file and a rename, because a schedule that was
// half-written by a rotation interrupted mid-write would forget the rotation it
// had just performed and run it again — the same failure a half-written journal
// has, answered the same way.
func Save(dir string, state State) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := StatePath(dir) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, StatePath(dir))
}

// MarkRotated records one rotation, keyed by the entry's own state key.
func (s *State) MarkRotated(entry Entry, at time.Time) {
	if s.Last == nil {
		s.Last = map[string]time.Time{}
	}
	s.Last[entry.StateKey()] = at.UTC()
	delete(s.Refused, entry.StateKey())
}

// MarkRefused records one refusal, so the next report says it rather than
// leaving a gap where a timestamp should be.
func (s *State) MarkRefused(entry Entry) {
	if s.Refused == nil {
		s.Refused = map[string]string{}
	}
	s.Refused[entry.StateKey()] = string(entry.Class) + ": " + entry.Reason
}
