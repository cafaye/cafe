package run

import "time"

// RunStatus is where a run stands. The four words are the prior art's four:
// queued (created, nobody has driven it), running (a driver holds it), and
// the two terminals. A run that has started is never queued again by the
// lease path — `requeued` after a failed attempt is the only way back, and it
// is a fresh attempt rather than an undone one.
type RunStatus string

const (
	RunQueued    RunStatus = "queued"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
)

// Terminal reports whether a run has reached a state no driver may write to.
func (s RunStatus) Terminal() bool {
	return s == RunSucceeded || s == RunFailed
}

// StepState is where one step stands. `running` is the load-bearing one: it
// is the state a crash leaves behind, and the state a resume must treat as
// "not done" rather than "in progress somewhere".
type StepState string

const (
	StepPending StepState = "pending"
	StepRunning StepState = "running"
	StepDone    StepState = "done"
	StepFailed  StepState = "failed"
)

// Step is one unit of recorded work. The times are pointers rather than
// values because "never begun" and "begun at the zero time" are different
// facts and a document that renders the second for the first is a document
// that lies about a box that never started.
type Step struct {
	Key        string     `json:"key"`
	State      StepState  `json:"state"`
	Attempts   int        `json:"attempts"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
}

// Run is one install: an ordered list of steps, the cursor over them, and the
// attempt count that says how many times a driver has tried and been
// interrupted.
type Run struct {
	ID          string    `json:"id"`
	Status      RunStatus `json:"status"`
	Steps       []Step    `json:"steps"`
	Attempt     int       `json:"attempt"`
	MaxAttempts int       `json:"max_attempts"`
	LastError   string    `json:"last_error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ResumeKey is the first step that is not done, in order, or "" when every
// step is done. It is the whole resume contract in one accessor: a driver
// that reads it and drives from there cannot replay work, and a driver that
// ignores it will find BeginStep refusing a step already done.
func (r *Run) ResumeKey() string {
	for _, s := range r.Steps {
		if s.State != StepDone {
			return s.Key
		}
	}
	return ""
}

// Done reports whether every step is done. A run can be Done without being
// Succeeded only if a driver is mid-CompleteStep, which the store lock makes
// unobservable from outside.
func (r *Run) Done() bool { return r.ResumeKey() == "" }

// Step looks a step up by key. The bool is false when the run has no such
// step, which is a caller error rather than a state: a driver that begins a
// step the run does not name is a driver with a second idea of the install.
func (r *Run) Step(key string) (Step, bool) {
	for _, s := range r.Steps {
		if s.Key == key {
			return s, true
		}
	}
	return Step{}, false
}

// Keys is the run's step keys, in order. It is what a document prints and
// what an error message names.
func (r *Run) Keys() []string {
	out := make([]string, len(r.Steps))
	for i, s := range r.Steps {
		out[i] = s.Key
	}
	return out
}
