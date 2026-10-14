package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// # The remote tools, and the world they read
//
// `internal/cli/mcp.go` serves the six local tools: what a machine and a
// project look like from where the command runs. They are reads over cafe's own
// files and two of them start containers, which is why that server was
// loopback-only.
//
// This is the other half of the surface: the tools that answer about a BOX
// that is running something somewhere else. The landing page already promises
// them — read-only health, logs and rollout state are the one thing a customer
// is told they get on day one — so this packet ships them with the transport
// auth that makes them reachable, and it ships the two mutating ones behind
// the scopes that gate them.
//
// # The world, and why it is an interface
//
// `World` is what a remote tool is handed, and it is an interface for the same
// reason `internal/boxd`'s is: the answers must be drivable into a table
// without a box, a container runtime or a network. Every method returns a value
// a test can render, and no method returns a secret — the health tool reports
// which services exist and never their configuration, because a service's
// environment is where credentials live and `internal/cli`'s own `caf_registry`
// already holds that rule.
//
// The two writes are on the interface too, because a `World` that only read
// would be a `World` whose write path was somewhere else, unguarded by
// whatever this file says about it.

// World is what the remote tools read and write through.
type World interface {
	// Health is whether the box is up, and what it is running. Read-only.
	Health(ctx context.Context) (Health, error)
	// Logs are the box's own recorded events, oldest first. Read-only.
	Logs(ctx context.Context, in LogsArgs) ([]LogLine, error)
	// Rollout is the release currently being applied, if any: what is
	// proposed, what is applied, and what is next. Read-only.
	Rollout(ctx context.Context) (Rollout, error)
	// Propose puts a release in front of a human. It mints nothing and
	// approves nothing — invariant 5 says only a click in the browser does
	// that — so the effect is a record and an audit event.
	Propose(ctx context.Context, in ProposeArgs) (Proposal, error)
	// Apply advances the box to a release a human has approved. This is the
	// one tool here that changes a box, and it is why it has its own scope.
	Apply(ctx context.Context, in ApplyArgs) (Applied, error)
}

// AddRemote registers the six remote tools on a server.
//
// It is a function rather than six `mcp.Add` calls at the call site because
// the scope each tool needs is written once, in `RequiredScope`, and the
// registration must not be a second place that has to agree with it. A test
// walks the served table and asserts every scoped tool is registered by this
// function and nothing else — see `TestTheRemoteTableIsExactlyWhatAddRemoteRegisters`.
func AddRemote(s *Server, w World) {
	tools := remoteTools{w: w}
	Add(s, tools.health())
	Add(s, tools.logs())
	Add(s, tools.rollout())
	Add(s, tools.status())
	Add(s, tools.propose())
	Add(s, tools.apply())
}

// remoteTools holds the one thing the handlers need, for the same reason
// `mcpTools` in internal/cli does: the world is substituted in a test and
// wired in production.
type remoteTools struct {
	w World
}

// ---------------------------------------------------------------------------
// health — the read-only promise the landing page already makes
// ---------------------------------------------------------------------------

// HealthArgs is what a caller may narrow the health read by. It is a filter
// rather than a command: nothing here can start, stop or select anything, and
// the filter exists because "is the gateway healthy" and "is the box healthy"
// are different questions with the same answer shape.
type HealthArgs struct {
	Service string `json:"service,omitempty" jsonschema:"one service name to report on; omit for the whole box"`
}

// Health is the box as a read: whether it is up, and what it is running.
//
// `Services` are NAMES. Not their environment, not their image's registry
// credentials, not their URLs — a service's configuration is where credentials
// live, and a tool that returned it would be handing them to whatever agent
// asked. The same rule `caf_registry` already holds, stated here because this
// is the tool a remote caller has.
type Health struct {
	Up       bool     `json:"up"`
	Services []string `json:"services"`
	// Phase is the install phase the box reached, as the boxd World names it.
	Phase int `json:"phase"`
	// PhaseTitle is the one-line human sentence for that phase.
	PhaseTitle string `json:"phase_title,omitempty"`
	// Detail is one sentence about the state, for the answer a person reads.
	Detail string `json:"detail,omitempty"`
	// CheckedAt is when this read happened, so an answer is attributable to a
	// moment rather than being read as the live state forever.
	CheckedAt string `json:"checked_at,omitempty"`
	// Note explains a read that could not be completed: a nil error and a note
	// is a partial answer, which is more use than a refusal and less honest
	// than a silent gap.
	Note string `json:"note,omitempty"`
}

func (t *remoteTools) health() Tool[HealthArgs, Health] {
	return Tool[HealthArgs, Health]{
		Name: "caf_health",
		Description: "Report whether a box is up, which install phase it reached, and which services are " +
			"running. " +
			"Needs nothing beyond an agent token carrying the read scope; the box this server answers " +
			"about is the one its operator enrolled. " +
			"Returns the state, the phase and its title, the service NAMES — never their environment, " +
			"which is their configuration and where credentials live — and when the read was taken. " +
			"Does not start, stop, restart or reconfigure anything, and does not read a log: use " +
			"caf_logs for that. Pass a service name to report on one service.",
		ReadOnly:   true,
		Idempotent: true,
		Input:      HealthArgs{},
		Handler:    t.runHealth,
	}
}

func (t *remoteTools) runHealth(ctx context.Context, in HealthArgs) (Health, error) {
	health, err := t.w.Health(ctx)
	if err != nil {
		return Health{}, errRefused("caf_health", err)
	}
	if name := strings.TrimSpace(in.Service); name != "" {
		filtered := Health{
			Up:         health.Up,
			Phase:      health.Phase,
			PhaseTitle: health.PhaseTitle,
			Detail:     health.Detail,
			CheckedAt:  health.CheckedAt,
			Note:       health.Note,
		}
		for _, svc := range health.Services {
			if svc == name {
				filtered.Services = append(filtered.Services, svc)
			}
		}
		if len(filtered.Services) == 0 {
			filtered.Note = joinNote(health.Note, "the box is not running a service called "+name+
				"; the services it IS running are named in an unfiltered caf_health call.")
			// Not an error: the question was about this box, and the answer is
			// "not that one". A caller asking about a service that is not there
			// is a caller with a typo, and an error would send it looking for
			// a fault instead.
			return filtered, nil
		}
		return filtered, nil
	}
	return health, nil
}

// ---------------------------------------------------------------------------
// logs — the box's own recorded events
// ---------------------------------------------------------------------------

// LogsArgs is a filter and a page, never a command: no amount of narrowing it
// can do anything to a box.
type LogsArgs struct {
	// Phase narrows to one install phase's events. Empty is all of them.
	Phase string `json:"phase,omitempty" jsonschema:"one install phase to filter on, e.g. substrate; omit for all"`
	// Since is an RFC3339 timestamp; only events at or after it are returned.
	Since string `json:"since,omitempty" jsonschema:"RFC3339 timestamp; only events at or after it are returned"`
	// Limit bounds the number of lines. Zero is the default of 200, and a
	// negative value is refused rather than treated as unbounded — an agent
	// asking for "everything" is an agent that will fill its context with it.
	Limit int `json:"limit,omitempty" jsonschema:"maximum number of lines to return; 0 is 200, and a negative value is refused"`
}

// LogLine is one recorded event. The message is the event's own, and it is
// never a credential: the box writes its own events, and anything this line
// carried would be something a transcript outlives.
type LogLine struct {
	At    string `json:"at"`
	Phase string `json:"phase,omitempty"`
	// Action is what happened: `apply`, `verify`, `enroll`. The message is its
	// own one sentence.
	Action  string `json:"action,omitempty"`
	Message string `json:"message"`
}

const defaultLogLimit = 200

func (t *remoteTools) logs() Tool[LogsArgs, LogsFacts] {
	return Tool[LogsArgs, LogsFacts]{
		Name: "caf_logs",
		Description: "Report the events a box has recorded: enrollment, each phase it applied, and what " +
			"each one said. " +
			"Needs an agent token carrying the read scope. Returns the lines oldest first, each with its " +
			"timestamp, the phase it belongs to and its message. " +
			"Does not read a container's stdout, does not follow, and does not start or stop anything: " +
			"these are the box's own recorded lifecycle events, not a process's output. " +
			"Filter by phase or by an RFC3339 timestamp, and bound the number of lines — an unbounded read " +
			"fills the caller's context and answers no question it had.",
		ReadOnly:   true,
		Idempotent: true,
		Input:      LogsArgs{},
		Handler:    t.runLogs,
	}
}

// LogsFacts is the answer, and it says how many lines it did not return
// rather than quietly cutting at the limit. A caller that reads 200 lines and
// assumes it read the log is a caller with a false model of the box.
type LogsFacts struct {
	Lines    []LogLine `json:"lines"`
	Total    int       `json:"total"`
	Returned int       `json:"returned"`
	// Truncated is true when lines exists that this answer does not carry.
	Truncated bool   `json:"truncated,omitempty"`
	Note      string `json:"note,omitempty"`
}

// runLogs is the log read: the world's lines, narrowed by the two filters the
// arguments carry and bounded by the limit.
//
// The filters are applied here rather than in the world because a filter is a
// property of the QUESTION and a world is a property of the box. A world that
// filtered would be a world that had to implement every filter any tool ever
// asked for, and the next filter would be a change in every implementation of
// it.
func (t *remoteTools) runLogs(ctx context.Context, in LogsArgs) (LogsFacts, error) {
	if in.Limit < 0 {
		return LogsFacts{}, errUsage("caf_logs", "limit %d is negative; pass 0 for the default of 200 "+
			"or a positive bound, because an unbounded read fills the caller's context")
	}
	lines, err := t.w.Logs(ctx, in)
	if err != nil {
		return LogsFacts{}, errRefused("caf_logs", err)
	}
	phase := strings.TrimSpace(in.Phase)
	facts := LogsFacts{Lines: []LogLine{}, Total: len(lines)}
	for _, line := range lines {
		if phase != "" && line.Phase != phase {
			continue
		}
		if !lineInWindow(line, in.Since) {
			continue
		}
		facts.Lines = append(facts.Lines, line)
	}
	limit := in.Limit
	if limit == 0 {
		limit = defaultLogLimit
	}
	if len(facts.Lines) > limit {
		facts.Lines = facts.Lines[:limit]
		facts.Truncated = true
		facts.Note = joinNote(facts.Note, "the box recorded more lines than this answer carries; "+
			"narrow it with phase or since, or raise limit")
	}
	facts.Returned = len(facts.Lines)
	if facts.Returned == 0 {
		// Which filter narrowed it to nothing, said in one sentence, because
		// an empty answer with no reason reads as "the box has done nothing".
		filtered := "no events match phase=" + phase
		if phase != "" && strings.TrimSpace(in.Since) != "" {
			filtered += " and since=" + in.Since
		} else if phase == "" && strings.TrimSpace(in.Since) != "" {
			filtered = "no events are at or after " + in.Since
		}
		facts.Note = joinNote(facts.Note, filtered+". Call caf_logs with no filter to see every line.")
	}
	return facts, nil
}

// lineInWindow applies the `since` filter. An unparseable timestamp is a filter
// the caller got wrong, and the answer is the unfiltered set plus a note rather
// than an empty one: an empty answer to a mistyped filter reads as "the box has
// done nothing", which is a different and much worse claim.
func lineInWindow(line LogLine, since string) bool {
	if strings.TrimSpace(since) == "" {
		return true
	}
	bound, err := time.Parse(time.RFC3339, since)
	if err != nil {
		return true
	}
	at, err := time.Parse(time.RFC3339, line.At)
	if err != nil {
		return true
	}
	return !at.Before(bound)
}

// ---------------------------------------------------------------------------
// rollout state — what is proposed, what is applied, what is next
// ---------------------------------------------------------------------------

// Rollout is the release lifecycle as a read. It is the tool the landing page's
// "see what is coming" promise rests on, and the one an agent reads before it
// asks a human for anything.
type Rollout struct {
	// Proposed is what is waiting for a human. It is a name and a version, not
	// a diff: an agent proposing a release is asking, and the thing asked for
	// is a decision a human makes in the browser.
	Proposed []string `json:"proposed,omitempty"`
	// Applied is what is running now.
	Applied string `json:"applied,omitempty"`
	// Next is the phase the next apply reaches.
	Next string `json:"next,omitempty"`
	// Phase is the install phase the box reached and PhaseTitle its one-line
	// human sentence. They are here rather than in a status-shaped struct
	// because the box knows one thing and the two tools read different parts
	// of it.
	Phase      int    `json:"phase"`
	PhaseTitle string `json:"phase_title,omitempty"`
	// Steps is the whole install table, in order. A caller reading rollout
	// state wants the shape of the job, not just its position in it.
	Steps []StatusStep `json:"steps,omitempty"`
	// AwaitingApproval is true when a release is proposed and no human has
	// approved it. It is a field rather than an inference from `proposed`
	// being non-empty, because the distinction is the whole of invariant 5 and
	// an agent that worked it out from a list would get it wrong the first
	// time two releases were proposed.
	AwaitingApproval bool   `json:"awaiting_approval,omitempty"`
	Detail           string `json:"detail,omitempty"`
	Note             string `json:"note,omitempty"`
}

func (t *remoteTools) rollout() Tool[NoArgs, Rollout] {
	return Tool[NoArgs, Rollout]{
		Name: "caf_rollout",
		Description: "Report the rollout state of a box: which release is applied and running, which one is " +
			"proposed and waiting for a human to approve it, and which install phase the next apply " +
			"reaches. " +
			"Needs an agent token carrying the read scope. Returns what is proposed, what is applied, and " +
			"whether something is awaiting approval. " +
			"Does not propose anything, does not apply anything, and does not mint or read an approval " +
			"token: a human clicks Approve in the browser and that click is the only thing that one " +
			"exists. Use caf_propose to put a release in front of a human, and caf_apply to advance to " +
			"one that has been approved.",
		ReadOnly:   true,
		Idempotent: true,
		Input:      NoArgs{},
		Handler:    t.runRollout,
	}
}

func (t *remoteTools) runRollout(ctx context.Context, _ NoArgs) (Rollout, error) {
	rollout, err := t.w.Rollout(ctx)
	if err != nil {
		return Rollout{}, errRefused("caf_rollout", err)
	}
	return rollout, nil
}

// ---------------------------------------------------------------------------
// status — the box, as one value
// ---------------------------------------------------------------------------

// statusFacts is `cafe box status` as a value: the enrolled identity's
// non-secrets, the phase reached and what the plan would do next. Every field is
// a non-secret, for the reason `internal/box.Identity` holds none.
type statusFacts struct {
	// Enrolled is whether a human has run `cafe box enroll` on this box.
	Enrolled bool `json:"enrolled"`
	// BoxRef is box's reference for this machine, which is how a human names
	// it in the website's `/boxes` screen.
	BoxRef string `json:"box_ref,omitempty"`
	// Status is the lifecycle word the journal recorded.
	Status string `json:"status,omitempty"`
	// Phase and PhaseTitle are where the install has reached.
	Phase      int    `json:"phase"`
	PhaseTitle string `json:"phase_title,omitempty"`
	// Next is the phase the next apply reaches, or empty when the plan is
	// exhausted.
	Next string `json:"next,omitempty"`
	// Steps is every phase in the table, in order, because an agent asking
	// "how far along is this box" wants the shape of the whole job and not
	// just its position in it.
	Steps []StatusStep `json:"steps"`
	Note  string       `json:"note,omitempty"`
}

// StatusStep is one phase of the install table: where it is and whether a human
// is required for it. `Human` is invariant 5 made visible — the phases a person
// must be present for are marked in the table rather than discovered by
// attempting one.
type StatusStep struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Reached bool   `json:"reached"`
	Next    bool   `json:"next,omitempty"`
	Human   bool   `json:"human,omitempty"`
}

func (t *remoteTools) status() Tool[NoArgs, statusFacts] {
	return Tool[NoArgs, statusFacts]{
		Name: "caf_status",
		Description: "Report a box's status: whether it is enrolled, box's reference for it, the " +
			"install phase it has reached, and every phase in the install table with the one a human is " +
			"required for marked. " +
			"Needs an agent token carrying the read scope. " +
			"Returns the whole table in order, so \"how far along is this box\" is answerable without a " +
			"second call. Does not enroll, apply, verify or read a log, and returns no key material: the " +
			"box's keypair is the box's and this tool never sees it.",
		ReadOnly:   true,
		Idempotent: true,
		Input:      NoArgs{},
		Handler:    t.runStatus,
	}
}

func (t *remoteTools) runStatus(ctx context.Context, _ NoArgs) (statusFacts, error) {
	rollout, err := t.w.Rollout(ctx)
	if err != nil {
		return statusFacts{}, errRefused("caf_status", err)
	}
	facts := statusFacts{
		Phase:      rollout.Phase,
		PhaseTitle: rollout.PhaseTitle,
		Next:       rollout.Next,
		Note:       joinNote(rollout.Detail, rollout.Note),
	}
	if len(rollout.Steps) > 0 {
		facts.Steps = rollout.Steps
	}
	if len(facts.Steps) == 0 {
		// A box whose world reports no table still gets an answer that says
		// so, because "no phases recorded" and "no box enrolled" are different
		// facts and one line is enough to tell them apart.
		facts.Note = joinNote(facts.Note, "the world reports no install table; this box has no phase to be at.")
		return facts, nil
	}
	return facts, nil
}

// ---------------------------------------------------------------------------
// propose — the right to ASK, which is not the right to act
// ---------------------------------------------------------------------------

// ProposeArgs names a release and says why. The reason is optional on the wire
// and load-bearing in practice: it is what a human reads on the approbutton and
// it is the only thing that makes "yes" mean anything.
type ProposeArgs struct {
	// Release is what is being proposed, e.g. `identity-0.2.0`.
	Release string `json:"release" jsonschema:"the release being proposed, e.g. identity-0.2.0"`
	// Reason is the one sentence a human reads before approving.
	Reason string `json:"reason,omitempty" jsonschema:"why this release should be applied, written for the human who will approve it"`
}

// Proposal is what proposing produced: a record and a URL. It carries no
// approval, because it cannot — only a human click mints one.
type Proposal struct {
	Proposed string `json:"proposed"`
	// ApprovalURL is where the human goes. It is returned to the caller rather
	// than sent anywhere, because the agent is the thing that surfaces it.
	ApprovalURL string `json:"approval_url,omitempty"`
	// AwaitingApproval is always true. It is a field rather than a comment
	// because a document that said so only in prose is a document a client
	// parses into the wrong thing.
	AwaitingApproval bool   `json:"awaiting_approval"`
	Detail           string `json:"detail,omitempty"`
}

func (t *remoteTools) propose() Tool[ProposeArgs, Proposal] {
	return Tool[ProposeArgs, Proposal]{
		Name: "caf_propose",
		Description: "Propose a release for a human to approve, and return the URL they approve it at. " +
			"Needs an agent token carrying the propose scope. " +
			"Records the proposal and says who it is waiting on. " +
			"Does NOT approve anything and mints no approval token — only a human's click in the browser " +
			"does that, which is the rule this tool exists to hold. So a caller that proposes and then " +
			"asks caf_apply for the same release will be told the release is not approved. " +
			"Write the reason for the human who will read it; it is the only thing that makes their yes " +
			"mean anything.",
		Input:   ProposeArgs{},
		Handler: t.runPropose,
	}
}

func (t *remoteTools) runPropose(ctx context.Context, in ProposeArgs) (Proposal, error) {
	if strings.TrimSpace(in.Release) == "" {
		return Proposal{}, errUsage("caf_propose", "name the release being proposed, e.g. `identity-0.2.0`; "+
			"a proposal with nothing named is a question nobody can answer")
	}
	proposal, err := t.w.Propose(ctx, in)
	if err != nil {
		return Proposal{}, errRefused("caf_propose", err)
	}
	proposal.AwaitingApproval = true
	return proposal, nil
}

// ---------------------------------------------------------------------------
// apply — the tool that changes a box
// ---------------------------------------------------------------------------

// ApplyArgs names what to advance to. It takes a release name and nothing
// else, because the alternative is a tool that could be pointed at an
// arbitrary artifact and there is no story in which that is safe.
type ApplyArgs struct {
	// Release is the release to apply. It must already be approved: this tool
	// does not approve, and an agent that calls it for an unapproved release
	// gets a refusal naming the approval it is missing.
	Release string `json:"release" jsonschema:"the approved release to apply, e.g. identity-0.2.0"`
}

// Applied is what one apply produced.
type Applied struct {
	Applied string `json:"applied"`
	// Detail is one sentence, for the answer a person reads.
	Detail string `json:"detail,omitempty"`
	// Next is the phase the next apply reaches, so a caller does not have to
	// ask twice to find out whether it is finished.
	Next string `json:"next,omitempty"`
}

func (t *remoteTools) apply() Tool[ApplyArgs, Applied] {
	return Tool[ApplyArgs, Applied]{
		Name: "caf_apply",
		Description: "Apply a release a HUMAN has already approved to the box. This is the one tool here " +
			"that changes a box, and it is why it has its own scope rather than the read scope everything " +
			"else shares. Needs an agent token carrying the apply scope. " +
			"Takes the release name. Does not approve anything: an unapproved release is refused, and the " +
			"refusal names the approval that is missing rather than applying it anyway — invariant 5 says a " +
			"human clicks Approve and this tool will not be the thing that decides for them. " +
			"Not idempotent in the sense of being free to repeat blindly: re-applying the same approved " +
			"release reconciles to it, but a first call with the wrong name is a call that changed " +
			"something.",
		Input:   ApplyArgs{},
		Handler: t.runApply,
	}
}

func (t *remoteTools) runApply(ctx context.Context, in ApplyArgs) (Applied, error) {
	if strings.TrimSpace(in.Release) == "" {
		return Applied{}, errUsage("caf_apply", "name the release being applied, e.g. `identity-0.2.0`; "+
			"an apply with nothing named is a change nobody chose")
	}
	applied, err := t.w.Apply(ctx, in)
	if err != nil {
		return Applied{}, errRefused("caf_apply", err)
	}
	return applied, nil
}

// ---------------------------------------------------------------------------
// the two failures, and why they are tool errors
// ---------------------------------------------------------------------------

// errRefused is what a world's failure becomes. The world's own message is
// kept verbatim, because the reason a box could not be read is a sentence
// somebody wrote for exactly this purpose and re-wording it here would lose
// what it says.
func errRefused(tool string, err error) error {
	return &Error{Tool: tool, Reason: err.Error()}
}

// errUsage is a bad argument, which is different from a failure: the call was
// malformed, not the box. It says so in the first word so a caller can tell
// which of the two it is dealing with without matching on prose.
func errUsage(tool, message string) error {
	return &Error{Tool: tool, Reason: message, Usage: true}
}

// Error is one tool error carrying which tool and why. It implements error so
// the SDK's typed handler turns it into a tool result with `isError`, which is
// the first rule in this package's doc: an agent reads it and carries on.
type Error struct {
	Tool   string
	Reason string
	Usage  bool
}

func (e *Error) Error() string {
	if e.Usage {
		return "cafe " + e.Tool + ": " + e.Reason
	}
	return "cafe " + e.Tool + " could not answer: " + e.Reason
}

// NoArgs is the input of a tool that takes nothing. The SDK needs a type, and a
// named one reads better than an inline `struct{}` in five signatures.
type NoArgs struct{}

// joinNote concatenates two notes with a space, ignoring the empty ones. It
// exists because five tools each append to a note they may not own, and five
// copies of `if a != "" { if b != "" { a += " " } ; a += b }` is five chances
// to get the spacing wrong.
func joinNote(parts ...string) string {
	var out []string
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			out = append(out, strings.TrimSpace(part))
		}
	}
	return strings.Join(out, " ")
}

// jsonNumber guards against a world that answers a phase as a string: the
// rollout facts are decoded from whatever a world returns, and a phase that
// arrives as "2" is still a phase.
func jsonNumber(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case string:
		var out int
		if err := json.Unmarshal([]byte(n), &out); err == nil {
			return out, true
		}
	}
	return 0, false
}
