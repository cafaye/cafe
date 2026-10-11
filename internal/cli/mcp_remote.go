package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/cafaye/cafe/internal/agent"
	"github.com/cafaye/cafe/internal/box"
	"github.com/cafaye/cafe/internal/mcp"
)

// # The remote MCP surface, wired
//
// `internal/mcp` owns the tools, the scopes and the transport auth. This file
// is the other half of the boundary: what the tools read, and who a caller's
// bearer is checked against. Both are seams here for the same reason they are
// there — a test that needed a real box, a real identity or a real keyring
// would be a test that needed a fleet.
//
// TWO SEAMS, and what stands behind them today:
//
//	mcp.Authority  identity. The real one forwards the presented bearer to
//	               identity's own key route, so a token's scopes are answered
//	               by the thing that minted them rather than inferred here.
//	               Nothing in this file decides whether a token is good.
//
//	mcp.World      the box's own recorded state: what internal/box's journal
//	               and store have written down. The reads are real and
//	               file-backed. The two writes are deliberately NOT — see
//	               ErrNotWired below.

// principalTTL is how long one verified bearer is trusted for.
//
// This is bound to the resource-server side of the check, not to identity's
// token: after `principalTTL` the next request forwards the bearer again. The
// alternative — trusting a token forever once accepted — is a credential that
// never lapses at this end, which is the thing invariant 2 exists to refuse,
// and it is why `mcp.Principal.Expires` has no zero case.
//
// It is short on purpose. The cost of re-verifying is one round trip to
// identity; the property bought is that a token revoked thirty seconds ago is
// refused within half a minute.
const principalTTL = 5 * time.Minute

// identityAuthority verifies a bearer by asking identity who it is.
//
// The bearer goes to `GET /v1/agents/tokens` as the caller's own credential —
// the authenticated read on the agent's own surface, and the mounted route a
// live token resolves against — so the acceptance is identity's, not this
// file's. A token that is not accepted comes back as a non-2xx and becomes a
// refusal, which `internal/mcp` turns into the 401 naming
// `cafe agent token create`.
//
// THE SCOPES, STATED HONESTLY. Identity's agent tokens carry no scope
// vocabulary yet — "the agent-token scope vocabulary arrives with the routes it
// will gate", identity's own comment at the mint — so there is no scope set to
// read off the answer. What this file grants a bearer identity accepts is the
// default scope cafe's own table says a claimed agent is granted: read. The
// write scopes (deploy.propose, deploy.apply) are granted by the claim screen,
// which does not exist yet; until it does, a verified agent token reads and
// cannot write, which is the fail-closed direction. The principal's subject is
// the username of the saved session on this machine.
type identityAuthority struct {
	// dir is the config directory this authority resolves the session from.
	// Recorded at construction so the keyring lookup cannot disagree with the
	// one that found the config.
	dir string
	// config is read per Select call inside Authenticate, so the active session
	// is resolved at the moment of the request rather than when the server
	// started — a session rotated out from under a long-lived server is still
	// refused.
	config agent.Config
	// baseURL is identity's origin.
	baseURL string
	// username names the session to resolve. Empty is the active one.
	username string
	// now is the clock. A field so a test can drive an expiry without waiting.
	now func() time.Time
}

var _ mcp.Authority = (*identityAuthority)(nil)

// newIdentityAuthority builds the Authority for one config directory: the
// config, the session inside it, the origin it talks to, and the directory the
// keyring lives beside. A machine with no saved agent session is not an error
// here — it is a nil authority, and see attachAuth for what that means.
func newIdentityAuthority(dir string) *identityAuthority {
	config, err := agent.LoadConfig(dir)
	if err != nil {
		return nil
	}
	session, err := config.Session()
	if err != nil {
		return nil
	}
	return &identityAuthority{dir: dir, config: config, username: session.Name, baseURL: session.BaseURL}
}

func (a *identityAuthority) Authenticate(ctx context.Context, token string) (*mcp.Principal, error) {
	session, err := a.config.Session()
	if err != nil {
		return nil, fmt.Errorf("no agent session is saved on this machine (%v); run `cafe agent register`", err)
	}
	// The bearer goes to identity as its OWN credential. A server that verified
	// the keyring's token instead would be a server that accepted its
	// operator's credential for any caller's, which is the whole thing this
	// packet exists to stop.
	endpoint := agent.DefaultDial()(session.BaseURL, token)
	if _, err := endpoint.Tokens(ctx); err != nil {
		return nil, fmt.Errorf("identity did not accept this token: %w", err)
	}
	now := a.now
	if now == nil {
		now = time.Now
	}
	return &mcp.Principal{
		Subject: session.AgentUsername,
		Scopes:  []string{mcp.ScopeRead},
		Expires: now().Add(principalTTL),
	}, nil
}

// ---------------------------------------------------------------------------
// the world: what a remote tool may read
// ---------------------------------------------------------------------------

// mcpBoxWorld is the mcp.World over this box's own recorded state.
//
// Every read is a read of something cafe already wrote down: the enrollment
// journal, the phase table, the event log and the run store. Nothing here makes
// a network call, and nothing here holds a key — `box.Store`'s identity carries
// none, which is the shape `internal/box` was built to.
type mcpBoxWorld struct {
	// dir is the box state directory: box.json, the journal and events.jsonl.
	dir string
	// clock is a field so a test can drive a time. Nil is time.Now.
	clock func() time.Time
}

var _ mcp.World = (*mcpBoxWorld)(nil)

// newMCPBoxWorld builds the world a `cafe mcp` serves from: the box state under
// the same config directory `cafe box enroll` wrote it.
func newMCPBoxWorld() *mcpBoxWorld {
	dir := ""
	if home, err := defaultConfigDir(); err == nil {
		dir = filepath.Join(home, "box")
	}
	return &mcpBoxWorld{dir: dir}
}

// store is one box store for the state directory. A ring of nil is safe here:
// none of the reads below touch the keyring, and a box that was never enrolled
// answers "not enrolled" rather than an error, because a machine with no box is
// a state and not a fault.
func (w *mcpBoxWorld) store() *box.Store {
	return box.NewStore(w.dir, nil, nil)
}

func (w *mcpBoxWorld) Health(ctx context.Context) (mcp.Health, error) {
	status, err := w.store().Status()
	if err != nil {
		return mcp.Health{}, err
	}
	health := mcp.Health{
		Up:         status.Enrolled,
		Phase:      box.PhaseIndex(status.Phase()),
		PhaseTitle: phaseTitle(status.Phase()),
		Detail:     statusDetail(status),
		CheckedAt:  w.now().Format(time.RFC3339),
	}
	// The phase names the box has COMPLETED, in order — read out of its own
	// event log. Names, never their environment: a service's configuration is
	// where credentials live and this tool answers to whatever agent asked.
	health.Services = append(health.Services, status.Completed...)
	if !status.Enrolled && status.Journal != "" {
		// The one thing a box that looks unenrolled needs to know: an
		// enrollment was started and did not finish, and the journal says so.
		health.Note = "this machine has no enrolled box, and an enrollment was started and did not " +
			"finish (" + status.Journal + "); `cafe box enroll --token …` resumes it."
		return health, nil
	}
	if !status.Enrolled {
		health.Note = "this machine has no enrolled box; `cafe box enroll --token …` is what makes " +
			"there be one, and it is run on the box itself."
	}
	_ = ctx
	return health, nil
}

func (w *mcpBoxWorld) Logs(ctx context.Context, in mcp.LogsArgs) ([]mcp.LogLine, error) {
	events, err := w.store().Logs()
	if err != nil {
		return nil, err
	}
	lines := make([]mcp.LogLine, 0, len(events))
	for _, event := range events {
		lines = append(lines, mcp.LogLine{
			At:      event.At.Format(time.RFC3339),
			Phase:   event.Phase,
			Action:  event.Action,
			Message: event.Message,
		})
	}
	// The phase filter is applied here rather than in the tool, and the tool
	// applies its own as well: narrowing the world's answer means a box with a
	// large log is never fully materialised to answer one phase, and the two
	// filters are not duplicates so much as the world's and the question's.
	if phase := in.Phase; phase != "" {
		filtered := make([]mcp.LogLine, 0, len(lines))
		for _, line := range lines {
			if line.Phase == phase {
				filtered = append(filtered, line)
			}
		}
		lines = filtered
	}
	_ = ctx
	return lines, nil
}

func (w *mcpBoxWorld) Rollout(ctx context.Context) (mcp.Rollout, error) {
	status, err := w.store().Status()
	if err != nil {
		return mcp.Rollout{}, err
	}
	rollout := mcp.Rollout{
		Phase:      box.PhaseIndex(status.Phase()),
		PhaseTitle: phaseTitle(status.Phase()),
		Detail:     statusDetail(status),
		// Applied is the phase the box has reached, read out of its own table.
		// `proposed` is what a human is being asked to approve and neither this
		// build nor this box holds it: the release and approval records are
		// conductor's (W5-P1). The read says so rather than answering with an
		// empty list a caller would read as "nothing is pending".
		Applied: status.Phase(),
	}
	if status.Next != nil {
		rollout.Next = status.Next.Name
	}
	for _, phase := range box.Phases {
		rollout.Steps = append(rollout.Steps, mcp.StatusStep{
			Name:  phase.Name,
			Title: phase.Title,
			Human: phase.Human,
		})
	}
	rollout.Note = notWired("the release a human approves and its approval record live in conductor " +
		"and are not in this build, so `proposed` is empty by construction and not because nothing " +
		"is pending.")
	_ = ctx
	return rollout, nil
}

// Propose and Apply are the two writes, and both are refused until the release
// surface they need exists. Conductor's approvals are W5-P1 and this packet
// consumes them rather than building them — which means a tool that pretended
// to apply a release here would be a tool that changed a box without a human's
// approval, and that is exactly the convenience the wave's rule 5 calls a
// defect.
func (w *mcpBoxWorld) Propose(context.Context, mcp.ProposeArgs) (mcp.Proposal, error) {
	return mcp.Proposal{}, ErrNotWired
}

func (w *mcpBoxWorld) Apply(context.Context, mcp.ApplyArgs) (mcp.Applied, error) {
	return mcp.Applied{}, ErrNotWired
}

// ErrNotWired is what a mutating remote tool answers with until conductor's
// releases and approvals exist (W5-P1). It is a named error rather than a string
// so a caller can ask which of the two halves it is missing, and its message is
// the actionable half: it names the packet and what the caller can do
// meanwhile.
var ErrNotWired = fmt.Errorf("cafe mcp: proposing and applying a release need conductor's releases and " +
	"approvals, which are W5-P1 and are not in this build. The reads (caf_health, caf_logs, " +
	"caf_rollout, caf_status) are live, and `cafe box plan` shows the install phases a human can " +
	"drive directly")

func (w *mcpBoxWorld) now() time.Time {
	if w.clock != nil {
		return w.clock()
	}
	return time.Now()
}

// notWired wraps the reason a release-shaped field is empty, so a caller reads
// a sentence about the gap instead of filling the silence in itself.
func notWired(reason string) string {
	return "not wired yet: " + reason
}

// phaseTitle is the one-line sentence for a phase name, from the same table
// `cafe box plan` reads.
func phaseTitle(name string) string {
	for _, phase := range box.Phases {
		if phase.Name == name {
			return phase.Title
		}
	}
	return ""
}

// statusDetail is one sentence about where the install is, for the answer a
// person reads.
func statusDetail(status *box.Status) string {
	if status == nil || !status.Enrolled {
		return "no box is enrolled on this machine."
	}
	if status.Phase() == "" {
		return "this box is enrolled and has not begun the install."
	}
	return "the install is at phase " + status.Phase() + "."
}

// ---------------------------------------------------------------------------
// the auth, attached per transport
// ---------------------------------------------------------------------------

// attachAuth wires the transport auth onto a built server.
//
// WHY IT IS SEPARATE FROM newMCPServer, and why the two transports get
// different halves rather than one: a `cafe mcp` child process an agent host
// spawns has no HTTP request to carry a bearer, so on stdio the caller is this
// machine's own agent session, verified ONCE at startup. Over HTTP the caller
// is the request, and the bearer in it is verified per request. The same
// `identityAuthority` backs both — the difference is where the credential comes
// from, and that difference is the whole of invariant 2's "bound to a target".
//
// A nil authority is not a failure. A machine with no saved agent session still
// serves its six local tools, because those need no scope, and the six remote
// ones refuse with a message naming `cafe agent register`. A server that refused
// to boot without an identity would break `cafe mcp` on every machine that had
// not registered yet, which is a worse failure than the one it prevents.
func attachAuth(server *mcp.Server, env *Env, opts *mcpOptions, transport mcp.Transport) {
	dir, err := defaultConfigDir()
	if err != nil {
		dir = ""
	}
	authority := &identityAuthority{dir: dir, username: opts.agent}
	if dir != "" {
		if config, err := agent.LoadConfig(dir); err == nil {
			authority.config = config
		}
	}

	switch transport {
	case mcp.HTTP:
		// The transport authenticates every request. Without this the server
		// would serve `caf_dev_up` to any caller that could reach the port,
		// which is the unauthenticated remote shell this repository's own doc
		// warns about.
		server.SetAuthority(authority)
	case mcp.Stdio:
		server.SetStanding(standingPrincipal(env, authority))
	}
}

// standingPrincipal is who a stdio process speaks as: the saved agent
// session's token, verified against identity and given a bounded TTL.
//
// Verified rather than read out of config.json, because a scope is a fact
// identity owns and a file on this machine is a cache of one. A session whose
// token identity will not accept answers with no standing principal, and the
// gate then refuses the remote tools rather than letting an unverified caller
// reach them.
func standingPrincipal(env *Env, authority *identityAuthority) *mcp.Principal {
	if authority == nil || authority.dir == "" || authority.config.AgentSessions == nil {
		return nil
	}
	session, err := authority.config.Select(authority.username, "")
	if err != nil {
		return nil
	}
	token, err := ringToken(authority.dir, session)
	if err != nil {
		return nil
	}
	// Bounded, so a hung identity cannot hang `cafe mcp` on its way up. The
	// server starts either way; only the scoped tools depend on the answer.
	ctx, cancel := context.WithTimeout(env.Context, 10*time.Second)
	defer cancel()
	principal, err := authority.Authenticate(ctx, token)
	if err != nil {
		// A diagnostic on stderr and never stdout: on stdio stdout is the wire.
		fmt.Fprintf(env.Stderr, "cafe mcp: the saved agent session was not accepted by identity: %v\n", err)
		return nil
	}
	return principal
}

// ringToken reads the token out of the keyring for one call and no longer. A
// token held in a variable for the length of the process would be a credential
// a process carries around, which is invariant 1.
func ringToken(dir string, session agent.Session) (string, error) {
	// The agent store's own ring, and the same 0600 file ring `cafe agent` uses:
	// an agent's token and a box's key are two kinds of secret in one secret
	// store, and a second implementation of "put a secret somewhere" is a
	// second place for the mode bits to be wrong.
	ring := box.FileRing{Dir: filepath.Join(dir, "keyring")}
	return ring.Get(agent.KeyringService, ringAccountFor(session))
}

// ringAccountFor is the keyring account a session's token is filed under: the
// agent package's own account prefix plus the session's name. It is called out
// rather than inlined because the service name and the account prefix are the
// two halves an agent token is filed under, and getting either wrong reads as
// "no token stored" rather than as the bug it is.
func ringAccountFor(session agent.Session) string {
	return "agent/" + session.Name
}
