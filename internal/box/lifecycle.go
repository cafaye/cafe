package box

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Phase is one step of a box's install. The table is pure data so that a plan
// is a function of the stored state and nothing else: no test has to boot a
// box to know what the next step is, and no future driver can invent a step
// this table does not name.
//
// The steps come from the launch substrate the box is installing (moon's
// DEPLOYMENT.md: k3s + Traefik + cert-manager + ArgoCD + CloudNativePG + Redis
// + Alloy/Loki, then identity, gateway, courier and parlor on top), and from
// invariant 3 — the secrets are BORN on the box — which is a phase rather than
// a note because a box that skipped it is a box whose keys came from somewhere
// else.
type Phase struct {
	Name string `json:"name"`
	// Title is the one-line human sentence.
	Title string `json:"title"`
	// Summary is the paragraph a plan prints.
	Summary string `json:"summary"`
	// Handoff is what actually runs this phase: kit's installer (W2-P4) under
	// `cafe boxd` (W2-P7). It is recorded here, as a string, so the plan can
	// name the work without pretending to run it.
	Handoff string `json:"handoff"`
	// Human marks the phases invariant 5 requires a person for. The install is
	// not one of them; accepting an upstream release is, and it is not a box
	// phase at all.
	Human bool `json:"human,omitempty"`
}

// phaseEnrolled is phase zero, the state `cafe box enroll` leaves behind. It is
// in the table because a plan that starts at the first thing you still have to
// do reads wrong on a box that has done nothing else.
const phaseEnrolled = "enrolled"

// Phases is the install table. Order is the sequence: a box cannot reach phase
// N without N-1, because each phase is the previous one's precondition and a
// plan that allowed skipping is a plan that would produce a box reporting a
// state it has not reached.
var Phases = []Phase{
	{
		Name: phaseEnrolled, Title: "Enrolled",
		Summary: "The box has a keypair, has redeemed its capability and is recognised by conductor.",
		Handoff: "cafe box enroll --token …",
	},
	{
		Name: "substrate", Title: "Kubernetes substrate",
		Summary: "k3s, kubelet networking, Traefik as the ingress controller and cert-manager for ACME.",
		Handoff: "kit substrate install --phase substrate",
	},
	{
		Name: "data", Title: "Data plane",
		Summary: "CloudNativePG for Postgres, Redis, and NATS with its streaming bootstrap job.",
		Handoff: "kit substrate install --phase data",
	},
	{
		Name: "stack", Title: "Launch stack",
		Summary: "identity, gateway, courier and parlor, the four services the platform runs for itself.",
		Handoff: "kit substrate install --phase stack",
	},
	{
		Name: "secrets", Title: "Box-born secrets",
		Summary: "The platform secrets are generated on the box and stored as Kubernetes Secrets: Postgres role passwords, the Redis password, the OIDC signing key and id, and the courier's relay token. Provider credentials arrive sealed to the box's public key and are opened here.",
		Handoff: "kit substrate install --phase secrets",
	},
	{
		Name: "app", Title: "The customer's app",
		Summary: "cafe new scaffolds the service and cafe deploy ships it behind the box's ingress.",
		Handoff: "cafe new && cafe deploy --engine argo",
	},
	{
		Name: "ready", Title: "Serving",
		Summary: "The box answers on its domain with a certificate it issued itself, and the app is live.",
		Handoff: "cafe box verify",
	},
}

// PhaseIndex is the position of a phase name in the table, or -1. It is -1
// rather than 0 for "not found" because phase zero is real.
func PhaseIndex(name string) int {
	for i, p := range Phases {
		if p.Name == name {
			return i
		}
	}
	return -1
}

// PhaseNames is the table's names, for a usage sentence and a `--help`.
func PhaseNames() []string {
	out := make([]string, len(Phases))
	for i, p := range Phases {
		out[i] = p.Name
	}
	return out
}

// Status is what `cafe box status` reports: the box, where it is, what is next,
// and the facts `verify` checks — presented together because an agent asking
// "is this box healthy" should not have to ask twice.
type Status struct {
	Enrolled bool      `json:"enrolled"`
	Identity *Identity `json:"identity,omitempty"`
	Current  *Phase    `json:"current,omitempty"`
	Next     *Phase    `json:"next,omitempty"`
	// Journal is the enrollment journal's state when one exists. A non-empty
	// value means an enrollment was started and did not finish, which is the
	// single most important thing on a box that looks unenrolled.
	Journal string `json:"journal,omitempty"`
	// Steps done, in order, read out of the event log.
	Completed []string `json:"completed,omitempty"`
	Events    int      `json:"events"`
}

// Phase returns the phase a status is at. An unenrolled box has none.
func (st Status) Phase() string {
	if st.Current == nil {
		return ""
	}
	return st.Current.Name
}

// Status reads the stored state. It touches the network for nothing and
// subprocess for nothing: the question "where is this box" is answered by what
// the box recorded, and a status that asked a server would be a status that
// hangs when the network does.
func (s *Store) Status() (*Status, error) {
	id, ok := s.loadIdentity()
	if !ok {
		st := &Status{Enrolled: false, Completed: []string{}}
		if jr, found, err := s.loadJournal(); err != nil {
			return nil, err
		} else if found {
			st.Journal = jr.State
		}
		events, err := s.events()
		if err != nil {
			return nil, err
		}
		st.Events = len(events)
		return st, nil
	}
	cur := PhaseIndex(id.Phase)
	if cur < 0 {
		// An identity naming a phase this build does not know is a box that
		// enrolled against a newer substrate. It is reported as enrolled with no
		// current phase rather than being clamped onto phase zero, which would
		// make a re-install look like the thing to do.
		return &Status{Enrolled: true, Identity: id, Completed: []string{}, Current: nil, Next: nil}, nil
	}
	st := &Status{Enrolled: true, Identity: id, Completed: []string{}, Current: &Phases[cur]}
	if cur+1 < len(Phases) {
		st.Next = &Phases[cur+1]
	}
	events, err := s.events()
	if err != nil {
		return nil, err
	}
	st.Events = len(events)
	for _, e := range events {
		if e.Action == "apply" {
			st.Completed = append(st.Completed, e.Phase)
		}
	}
	return st, nil
}

// PlanStep is one row of `cafe box plan`: a phase, where it stands, and what
// would run it.
type PlanStep struct {
	Phase
	// Index is the phase's position, so a plan row is addressable by
	// `apply --phase`'s name and by number alike.
	Index int `json:"index"`
	// State is done, current or pending.
	State string `json:"state"`
	// Appliable is true when `apply --phase <name>` would change something.
	Appliable bool `json:"appliable"`
}

// Plan is the whole install with the box's position marked on it. It is the
// difference between "what you asked for" and "the eight things in between".
type Plan struct {
	BoxID  string     `json:"box_id"`
	Steps  []PlanStep `json:"steps"`
	Next   string     `json:"next,omitempty"`
	Reason string     `json:"reason,omitempty"`
}

func (s *Store) Plan() (*Plan, error) {
	id, ok := s.loadIdentity()
	if !ok {
		return nil, ErrNotEnrolled
	}
	cur := PhaseIndex(id.Phase)
	if cur < 0 {
		cur = 0
	}
	plan := &Plan{BoxID: id.BoxID, Steps: make([]PlanStep, len(Phases))}
	for i, p := range Phases {
		state := "pending"
		switch {
		case i < cur:
			state = "done"
		case i == cur:
			state = "current"
		}
		plan.Steps[i] = PlanStep{Phase: p, Index: i, State: state, Appliable: i == cur+1}
	}
	if cur+1 < len(Phases) {
		plan.Next = Phases[cur+1].Name
	} else {
		plan.Reason = "every phase has been reached; run `cafe box verify` to check the box's own record of itself"
	}
	return plan, nil
}

// ApplyResult is what one `cafe box apply` did.
type ApplyResult struct {
	Action  string `json:"action"`
	BoxID   string `json:"box_id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Next    string `json:"next,omitempty"`
	Handoff string `json:"handoff,omitempty"`
	Message string `json:"message"`
	Changed bool   `json:"changed"`
	Human   bool   `json:"human,omitempty"`
}

// The three Apply actions. "unchanged" is a success on purpose: an agent that
// retries a command must be able to tell "it did not need doing" from "it did
// something", and only the second is a reason to keep a log.
const (
	ApplyAdvanced  = "advanced"
	ApplyUnchanged = "unchanged"
	ApplyRefused   = "refused"
)

// ErrNoSuchPhase is a usage error's payload: the phase named is not in the
// table. It is returned as an error, and the CLI maps it to exit 2.
var ErrNoSuchPhase = errors.New("no such phase")

// PhaseError is ErrNoSuchPhase with the table's names attached, so the message
// a person reads is the message that tells them what to type instead.
func PhaseError(name string) error {
	return fmt.Errorf("%w %q; the phases are %s", ErrNoSuchPhase, name, strings.Join(PhaseNames(), ", "))
}

// Apply takes the box to a phase. It is the record of the install, not the
// installer: kit's install.sh and `cafe boxd` run the phases, and this writes
// down that one was reached.
//
// Three rules, and each is a refusal rather than a warning:
//
//   - The box must be enrolled. An install on a box with no identity is an
//     install nobody can account for.
//   - The phase must be the next one or the one already reached. Skipping is
//     refused because a box at `stack` that never ran `data` is a box that will
//     fail later, in a place where the failure names Postgres rather than the
//     phase that was skipped.
//   - Re-applying the current phase is a success that changes nothing. That is
//     the idempotence clause: the command an agent retries is the command that
//     must converge.
func (s *Store) Apply(phase string) (*ApplyResult, error) {
	id, ok := s.loadIdentity()
	if !ok {
		return nil, ErrNotEnrolled
	}
	want := PhaseIndex(strings.TrimSpace(phase))
	if want < 0 {
		return nil, PhaseError(phase)
	}
	cur := PhaseIndex(id.Phase)
	if cur < 0 {
		cur = 0
	}
	res := &ApplyResult{BoxID: id.BoxID, From: id.Phase, To: Phases[want].Name, Handoff: Phases[want].Handoff, Human: Phases[want].Human}

	switch {
	case want < cur:
		res.Action = ApplyUnchanged
		res.To = Phases[cur].Name
		res.Message = "the box is already past " + Phases[want].Name + " (at " + id.Phase + "); nothing changed"
		return res, nil
	case want == cur:
		res.Action = ApplyUnchanged
		res.Message = "the box is already at " + id.Phase + "; nothing changed"
		return res, nil
	case want > cur+1:
		res.Action = ApplyRefused
		return nil, fmt.Errorf("cafe box apply: %s is not the next phase — the box is at %s, so the next one is %s; the install is sequential and skipping is not a shortcut, it is a box that reports a state it has not reached", Phases[want].Name, id.Phase, Phases[cur+1].Name)
	}

	id.Phase = Phases[want].Name
	if err := s.writeIdentity(id); err != nil {
		return nil, err
	}
	res.Action = ApplyAdvanced
	res.Changed = true
	res.Message = "recorded " + id.Phase + "; " + Phases[want].Handoff + " is what performs it"
	if want+1 < len(Phases) {
		res.Next = Phases[want+1].Name
	}
	s.appendEvent("apply", id.Phase, "recorded phase "+id.Phase+" ("+Phases[want].Title+")")
	return res, nil
}

// Event is one line of the box's log. It is the install's own record of itself:
// what happened, when, and to which phase. No event carries a credential, a
// token or a key — the log is the one thing most likely to be pasted into an
// issue.
type Event struct {
	At      time.Time `json:"at"`
	Action  string    `json:"action"`
	Phase   string    `json:"phase,omitempty"`
	Message string    `json:"message"`
}

func (s *Store) eventsPath() string { return s.paths(eventsFile) }

// events reads the log. A missing log is an empty log rather than an error: a
// box that has just enrolled has none, and a command that failed on the first
// read of a file nobody has written yet would be a command nobody could run.
func (s *Store) events() ([]Event, error) {
	raw, err := os.ReadFile(s.eventsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cafe box: read the event log: %w", err)
	}
	var out []Event
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // a corrupt line is not a reason to lose the log
		}
		out = append(out, e)
	}
	return out, nil
}

// Logs is the event log, oldest first.
func (s *Store) Logs() ([]Event, error) { return s.events() }

func (s *Store) appendEvent(action, phase, message string) {
	e := Event{At: s.stamp(), Action: action, Phase: phase, Message: message}
	raw, err := json.Marshal(e)
	if err != nil {
		return // a log line that will not encode is a log line cafe does not have
	}
	f, err := os.OpenFile(s.eventsPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(raw, '\n'))
}

// VerifyFact is one thing `cafe box verify` checked, reported as a fact rather
// than a verdict, so the output is a document an agent can branch on.
type VerifyFact struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
	// Secret is true for the one fact that must report a boolean and never a
	// value. It is in the type so a future fact cannot accidentally be written
	// as "here is the key, and by the way it passed".
	Secret bool `json:"secret,omitempty"`
}

// Verify is the box's own report on itself: is there an identity, is there a
// key that goes with it, and does the key match what conductor was told.
type Verify struct {
	BoxID  string       `json:"box_id"`
	OK     bool         `json:"ok"`
	Facts  []VerifyFact `json:"facts"`
	Reason string       `json:"reason,omitempty"`
}

func (s *Store) Verify() (*Verify, error) {
	id, ok := s.loadIdentity()
	if !ok {
		return &Verify{OK: false, Reason: ErrNotEnrolled.Error()}, nil
	}
	v := &Verify{BoxID: id.BoxID, Facts: []VerifyFact{}}
	add := func(name string, pass bool, detail string, secret bool) {
		v.Facts = append(v.Facts, VerifyFact{Name: name, Pass: pass, Detail: detail, Secret: secret})
	}

	add("identity_present", true, "box "+id.BoxID+" is enrolled", false)

	stored, err := s.Ring.Get(ringService, ringAccount(id.BoxID))
	switch {
	case err == nil:
		key, kerr := parsePrivateKey(stored)
		if kerr != nil {
			add("private_key_present", false, "the keyring entry is not a usable key", true)
		} else if got := publicKeyText(key.Public().(ed25519.PublicKey)); got != id.PublicKey {
			add("private_key_matches_identity", false, "the key in the keyring derives a different public key than the identity records", true)
		} else {
			add("private_key_present", true, "the box's private key is in the keyring", true)
			add("private_key_matches_identity", true, "the keyring key derives the identity's public key", true)
		}
	case errors.Is(err, ErrNotFound):
		add("private_key_present", false, "no keyring entry for box "+id.BoxID, true)
	default:
		add("private_key_present", false, "the keyring could not be read: "+err.Error(), true)
	}

	jr, found, jerr := s.loadJournal()
	if jerr != nil {
		add("journal_clear", false, jerr.Error(), false)
	} else if found {
		add("journal_clear", false, "an enrollment is unfinished (state "+jr.State+")", false)
	} else {
		add("journal_clear", true, "no unfinished enrollment", false)
	}

	if PhaseIndex(id.Phase) < 0 {
		add("phase_known", false, "phase "+id.Phase+" is not one this build knows", false)
	} else {
		add("phase_known", true, "at phase "+id.Phase, false)
	}

	if id.AccountID == "" {
		add("account_known", false, "the identity records no account", false)
	} else {
		add("account_known", true, "account "+id.AccountID, false)
	}

	v.OK = len(v.Facts) > 0
	for _, f := range v.Facts {
		if !f.Pass {
			v.OK = false
		}
	}
	if !v.OK {
		v.Reason = "the box's record of itself is incomplete; see the facts"
	}
	return v, nil
}
