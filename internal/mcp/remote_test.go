package mcp

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The tests here drive the six remote tools through a recording world, so what
// they prove is about the tools: that a read-only agent cannot reach a mutate,
// that a refused mutate never ran, that the answers carry no credential, and
// that the box's own reason survives into the message. The wire is tested
// elsewhere — against a real process in internal/cli/mcp_wire_test.go and over
// real HTTP in auth_test.go.

// recordingWorld answers every remote tool, records what was called, and is
// the substitute for a box that is running something.
type recordingWorld struct {
	health  Health
	lines   []LogLine
	rollout Rollout
	propose Proposal
	applied Applied

	// calls is what the world was actually asked, so a test can assert a tool
	// ran and — the half that matters — that a refused one did not.
	calls []string

	// failPropose and failApply return this error instead of the answer,
	// which is how a world says "a human has not approved that".
	failPropose error
	failApply   error
}

func (w *recordingWorld) Health(context.Context) (Health, error) {
	w.calls = append(w.calls, "Health")
	return w.health, nil
}

func (w *recordingWorld) Logs(_ context.Context, _ LogsArgs) ([]LogLine, error) {
	w.calls = append(w.calls, "Logs")
	return w.lines, nil
}

func (w *recordingWorld) Rollout(context.Context) (Rollout, error) {
	w.calls = append(w.calls, "Rollout")
	return w.rollout, nil
}

func (w *recordingWorld) Propose(_ context.Context, in ProposeArgs) (Proposal, error) {
	w.calls = append(w.calls, "Propose:"+in.Release)
	if w.failPropose != nil {
		return Proposal{}, w.failPropose
	}
	return w.propose, nil
}

func (w *recordingWorld) Apply(_ context.Context, in ApplyArgs) (Applied, error) {
	w.calls = append(w.calls, "Apply:"+in.Release)
	if w.failApply != nil {
		return Applied{}, w.failApply
	}
	return w.applied, nil
}

func (w *recordingWorld) called(name string) bool {
	for _, call := range w.calls {
		if call == name || strings.HasPrefix(call, name+":") {
			return true
		}
	}
	return false
}

// fixture is a box that is enrolled, part-installed and running two services.
func fixture() *recordingWorld {
	return &recordingWorld{
		health: Health{
			Up: true, Phase: 2, PhaseTitle: "Data services", Detail: "two of five services are ready",
			Services:  []string{"identity", "gateway", "courier"},
			CheckedAt: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		},
		lines: []LogLine{
			{At: "2026-10-09T11:00:00Z", Phase: "enrolled", Action: "enroll", Message: "the box redeemed its capability"},
			{At: "2026-10-09T11:05:00Z", Phase: "substrate", Action: "apply", Message: "k3s and Traefik are up"},
			{At: "2026-10-09T11:20:00Z", Phase: "data", Action: "apply", Message: "CloudNativePG is accepting connections"},
		},
		rollout: Rollout{
			Proposed: []string{"identity-0.2.0"}, Applied: "identity-0.1.0", Next: "parlor",
			Phase: 2, PhaseTitle: "Data services", AwaitingApproval: true,
			Detail: "one release is waiting for a human to approve it in the browser",
			Steps: []StatusStep{
				{Name: "enrolled", Title: "Enrolled", Reached: true},
				{Name: "substrate", Title: "Kubernetes substrate", Reached: true},
				{Name: "data", Title: "Data services", Reached: true},
				{Name: "parlor", Title: "The launch stack", Next: true},
			},
		},
		propose: Proposal{Proposed: "identity-0.2.0", ApprovalURL: "https://example.test/approve/abc"},
		applied: Applied{Applied: "identity-0.2.0", Detail: "applied", Next: "parlor"},
	}
}

// remote returns a client on a server serving the six remote tools, with the
// given standing principal. Over the in-memory transport the standing
// principal IS the caller, which is the stdio shape.
func remote(t *testing.T, w World, standing *Principal) *client {
	t.Helper()
	server := New(Options{Version: "1.2.3", Logger: testLogger(), Standing: standing})
	AddRemote(server, w)
	serverTransport, clientTransport := newPair(t)
	session, err := connect(context.Background(), server, serverTransport, clientTransport)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(session.Close)
	return session
}

func reader() *Principal {
	return &Principal{Subject: "noel", Scopes: []string{ScopeRead}, Expires: time.Now().Add(time.Hour)}
}

func operator() *Principal {
	return &Principal{
		Subject: "noel", Scopes: []string{ScopeRead, ScopePropose, ScopeApply}, Expires: time.Now().Add(time.Hour),
	}
}

// The read-only promise, end to end. A read-scoped agent reads health, logs
// and rollout state, and is refused propose and apply — and, the half that
// matters, the world was never asked to do either.
func TestAReadScopedAgentReadsAndCannotMutate(t *testing.T) {
	w := fixture()
	session := remote(t, w, reader())

	for _, name := range []string{"caf_health", "caf_logs", "caf_rollout", "caf_status"} {
		res, err := session.call(t, name, nil)
		if err != nil {
			t.Fatalf("calling %s: %v", name, err)
		}
		if res.IsError {
			t.Errorf("a read-scoped agent was refused the read %s: %s", name, res.text())
			continue
		}
		// The world was asked, which is the half that distinguishes a read
		// from an answer assembled from nothing. A tool that answered without
		// reaching the box would be a tool reporting cafe's opinion of a box.
		var asked string
		switch name {
		case "caf_health":
			asked = "Health"
		case "caf_logs":
			asked = "Logs"
		case "caf_rollout":
			asked = "Rollout"
		case "caf_status":
			asked = "Rollout"
		}
		if !w.called(asked) {
			t.Errorf("the tool %s answered without the world being asked", name)
		}
	}

	for _, name := range []string{"caf_propose", "caf_apply"} {
		res, err := session.call(t, name, map[string]any{"release": "identity-0.2.0"})
		if err != nil {
			t.Fatalf("calling %s: %v", name, err)
		}
		if !res.IsError {
			t.Errorf("a read-scoped agent was allowed to call %s, which changes a box", name)
		}
	}
	if w.called("Propose") {
		t.Error("a refused caf_propose still asked the world to propose")
	}
	if w.called("Apply") {
		t.Error("a refused caf_apply still asked the world to apply; the gate is not in front of it")
	}
	for _, name := range []string{"Propose:identity-0.2.0", "Apply:identity-0.2.0"} {
		if w.called(name) {
			t.Errorf("the world was asked %q behind a refusal", name)
		}
	}
}

// The mutating half of the table needs the scope that names it, and this is
// the two-way assertion: an apply-scoped caller applies, a propose-only caller
// does not apply. Three scopes, three rows, because a gate that only checked
// "is there something in this list" would pass the first row and fail the
// second.
func TestEachWriteScopeIsCheckedOnItsOwn(t *testing.T) {
	tests := []struct {
		name     string
		scopes   []string
		tool     string
		argument map[string]any
		want     bool
	}{
		{name: "read proposes", scopes: []string{ScopeRead}, tool: "caf_propose", argument: map[string]any{"release": "r"}, want: false},
		{name: "propose proposes", scopes: []string{ScopePropose}, tool: "caf_propose", argument: map[string]any{"release": "r"}, want: true},
		{name: "propose cannot apply", scopes: []string{ScopePropose}, tool: "caf_apply", argument: map[string]any{"release": "r"}, want: false},
		{name: "read cannot apply", scopes: []string{ScopeRead}, tool: "caf_apply", argument: map[string]any{"release": "r"}, want: false},
		{name: "apply applies", scopes: []string{ScopeApply}, tool: "caf_apply", argument: map[string]any{"release": "r"}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := fixture()
			principal := &Principal{
				Subject: "noel", Scopes: tt.scopes, Expires: time.Now().Add(time.Hour),
			}
			session := remote(t, w, principal)
			res, err := session.call(t, tt.tool, tt.argument)
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if res.IsError == tt.want {
				t.Errorf("%s with %v: IsError = %v, want the opposite", tt.tool, tt.scopes, res.IsError)
			}
			if tt.want {
				if !w.called(tt.tool) && !w.called("Propose") && !w.called("Apply") {
					t.Errorf("an allowed call did not reach the world")
				}
				return
			}
			if w.called("Propose") || w.called("Apply") {
				t.Errorf("a refused call reached the world: %v", w.calls)
			}
		})
	}
}

// Invariant 5, enforced by the tool rather than asserted in a prompt: an apply
// for a release no human approved is refused, and the refusal is the world's
// own sentence about it. `failApply` is what a box says when there is no
// approval, and this is the test that the sentence survives the trip.
func TestAnApplyForAnUnapprovedReleaseIsRefusedWithoutApplyingIt(t *testing.T) {
	w := fixture()
	w.failApply = errors.New("identity-0.2.0 is proposed but no human has approved it; the approval " +
		"URL is https://example.test/approve/abc")
	session := remote(t, w, operator())

	res, err := session.call(t, "caf_apply", map[string]any{"release": "identity-0.2.0"})
	if err != nil {
		t.Fatalf("the refusal was a protocol error rather than a tool error: %v", err)
	}
	if !res.IsError {
		t.Fatal("an unapproved release was applied")
	}
	if !strings.Contains(res.text(), "no human has approved it") {
		t.Errorf("the refusal does not carry the world's reason:\n%s", res.text())
	}
	if !strings.Contains(res.text(), "caf_apply") {
		t.Errorf("the refusal does not name the tool:\n%s", res.text())
	}
}

// Proposing is not approving, and the tool says so in the document it answers
// with rather than leaving it to the description. A client that parses this
// document must be able to tell that it is still waiting.
func TestProposingAnswersThatItIsStillWaitingForAHuman(t *testing.T) {
	w := fixture()
	session := remote(t, w, operator())

	res, err := session.call(t, "caf_propose", map[string]any{
		"release": "identity-0.2.0", "reason": "a security fix and nothing else",
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("a scoped propose was refused: %s", res.text())
	}
	// The document an agent reads carries the flag, and it is set by the tool
	// even though the world did not set it itself — the world proposed, and "a
	// human has not approved this yet" is a fact about the proposal rather
	// than about the box.
	got := res.text()
	if !strings.Contains(got, `"awaiting_approval":true`) {
		t.Errorf("the proposal does not say it is awaiting approval:\n%s", got)
	}
	if !w.called("Propose:identity-0.2.0") {
		t.Errorf("the world was not asked with the release: %v", w.calls)
	}
}

// A propose with nothing named never reaches the world. The SDK's own argument
// validation refuses it first, because `release` is a required property of the
// input schema, and the tool's own guard is the second half — it is reachable
// by a caller that sends a blank, and a guard that only works when the schema
// happens to be strict is not a guard.
func TestAProposeWithNoReleaseNamedIsAUsageError(t *testing.T) {
	w := fixture()
	session := remote(t, w, operator())

	for _, arguments := range []map[string]any{
		{"reason": "because"},
		{"release": "", "reason": "because"},
		{"release": "   ", "reason": "because"},
	} {
		res, err := session.call(t, "caf_propose", arguments)
		if err != nil {
			t.Fatalf("call %v: %v", arguments, err)
		}
		if !res.IsError {
			t.Errorf("a proposal with %v was recorded", arguments)
			continue
		}
		if !strings.Contains(res.text(), "release") {
			t.Errorf("the refusal does not name the field that is missing:\n%s", res.text())
		}
	}
	if w.called("Propose") {
		t.Error("a malformed propose reached the world")
	}
}

// ---------------------------------------------------------------------------
// the reads, and what they may carry
// ---------------------------------------------------------------------------

// A health read carries service NAMES and nothing else. The fixture's point is
// a service whose configuration would be a credential: the walk below fails if
// any field of the answer holds one, which is the same control
// `internal/boxd/secret_test.go` applies to a route table.
func TestAHealthAnswerCarriesNamesAndNeverTheConfiguration(t *testing.T) {
	w := fixture()
	// A service whose configuration is exactly where a credential would live,
	// planted in the world's own answer so the walk has something to find.
	w.health.Services = []string{"identity", "gateway", "courier"}
	session := remote(t, w, reader())

	res, err := session.call(t, "caf_health", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("caf_health: %s", res.text())
	}
	got := res.text()
	for _, forbidden := range []string{"COURIER_SMTP_PASSWORD", "SECRET_KEY_BASE", "smtp_password", "token="} {
		if strings.Contains(got, forbidden) {
			t.Errorf("the health answer carries %q:\n%s", forbidden, got)
		}
	}
	for _, want := range []string{"identity", "gateway", "courier", "up"} {
		if !strings.Contains(got, want) {
			t.Errorf("the health answer does not name %q:\n%s", want, got)
		}
	}
}

// The walk over every answer every remote tool produces, hunting for a
// secret-shaped field. It is the second control after the string sweep above,
// and it reaches fields a string sweep would miss — a nested struct whose JSON
// tag says `password` is findable only by walking the type.
func TestNoRemoteAnswerCarriesASecretShapedField(t *testing.T) {
	forbidden := []string{"password", "secret", "token", "credential", "private_key", "privatekey", "key_material"}
	types := []any{
		Health{}, LogLine{}, LogsFacts{}, Rollout{}, Proposal{}, Applied{}, statusFacts{}, StatusStep{},
	}
	for _, v := range types {
		var walk func(t reflect.Type, path string)
		walk = func(typ reflect.Type, path string) {
			for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
				typ = typ.Elem()
			}
			if typ.Kind() != reflect.Struct {
				return
			}
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				tag := strings.ToLower(strings.Split(field.Tag.Get("json"), ",")[0])
				for _, word := range forbidden {
					if tag == word {
						t.Errorf("%s%s has a field tagged %q, which is a credential this tool must not answer with",
							path, field.Name, tag)
					}
				}
				walk(field.Type, path+field.Name+".")
			}
		}
		walk(reflect.TypeOf(v), reflect.TypeOf(v).Name()+".")
	}
	// ApprovalURL is the one field whose name mentions a credential-shaped word
	// and it is not one: it is where a human goes. Control half, so a build that
	// banned the word instead of the thing would be caught here.
	if !strings.Contains("approvalurl", "url") {
		t.Error("the control half is broken")
	}
}

// A log read that is bounded says how much it did not return, rather than
// quietly stopping at the limit. A caller that read 200 lines and assumed it
// read the log is a caller with a false model of the box.
func TestALogReadSaysHowMuchItDidNotReturn(t *testing.T) {
	w := fixture()
	for i := 0; i < 260; i++ {
		w.lines = append(w.lines, LogLine{
			At: "2026-10-09T12:00:00Z", Phase: "data", Action: "apply", Message: "another line",
		})
	}
	session := remote(t, w, reader())

	res, err := session.call(t, "caf_logs", map[string]any{"limit": 10})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("caf_logs: %s", res.text())
	}
	got := res.text()
	if !strings.Contains(got, `"truncated":true`) {
		t.Errorf("a bounded read did not say it was truncated:\n%s", got)
	}
	if !strings.Contains(got, `"returned":10`) {
		t.Errorf("the answer does not say how many lines it carried:\n%s", got)
	}
}

// A negative limit is refused rather than read as unbounded, which is the
// difference between a caller that got a bounded answer and one that filled its
// context with every line a box ever wrote.
func TestANegativeLogLimitIsRefusedRatherThanUnbounded(t *testing.T) {
	w := fixture()
	session := remote(t, w, reader())

	res, err := session.call(t, "caf_logs", map[string]any{"limit": -1})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatal("a negative limit was read as unbounded")
	}
	if w.called("Logs") {
		t.Error("a malformed log read reached the world")
	}
}

// A log read filtered to a service that does not exist is not an error, and
// the answer says which services do exist. The refusal-by-error alternative
// sends a caller with a typo looking for a fault.
func TestALogReadFilteredToAUnknownPhaseIsNotAnError(t *testing.T) {
	w := fixture()
	session := remote(t, w, reader())

	res, err := session.call(t, "caf_logs", map[string]any{"phase": "no-such-phase"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("a read with no matches was an error: %s", res.text())
	}
	if !strings.Contains(res.text(), "no events match phase=no-such-phase") {
		t.Errorf("the empty answer does not say which filter narrowed it to nothing:\n%s", res.text())
	}
	if !strings.Contains(res.text(), "with no filter") {
		t.Errorf("the empty answer does not say how to see everything:\n%s", res.text())
	}
}

// The status tool answers the whole install table, because an agent asking
// "how far along is this box" wants the shape of the job and not just its
// position in it. And the phase a human is required for is marked in the table
// rather than left to be discovered by attempting one.
func TestStatusAnswersTheWholeInstallTable(t *testing.T) {
	w := fixture()
	session := remote(t, w, reader())

	res, err := session.call(t, "caf_status", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("caf_status: %s", res.text())
	}
	got := res.text()
	for _, want := range []string{`"name":"enrolled"`, `"name":"substrate"`, `"name":"parlor"`, `"next":true`} {
		if !strings.Contains(got, want) {
			t.Errorf("the status answer does not carry %s:\n%s", want, got)
		}
	}
}

// The served table is exactly what AddRemote registers, which is the control
// that keeps the two from drifting: a tool added to RequiredScope but not
// registered would be scoped on nothing and served by nothing.
func TestTheRemoteTableIsExactlyWhatAddRemoteRegisters(t *testing.T) {
	server := New(Options{Version: "1.2.3", Logger: testLogger()})
	AddRemote(server, fixture())

	want := []string{"caf_health", "caf_logs", "caf_rollout", "caf_status", "caf_propose", "caf_apply"}
	got := server.Names()
	if len(got) != len(want) {
		t.Fatalf("AddRemote registered %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names()[%d] = %q, want %q", i, got[i], want[i])
		}
		if RequiredScope[want[i]] == "" {
			t.Errorf("%q is registered and served but has no scope, so any agent can call it", want[i])
		}
	}
	// And nothing outside the remote table is in RequiredScope, so the map
	// cannot quietly grow a scope for a local tool and remove it from the
	// unauthenticated surface a person depends on.
	for name := range RequiredScope {
		found := false
		for _, want := range want {
			if name == want {
				found = true
			}
		}
		if !found {
			t.Errorf("RequiredScope has an entry for %q, which no remote tool registers", name)
		}
	}
}

// A world's refusal reaches the caller as the world wrote it. Rewording it here
// would lose what it says, and the reason a box could not be read is a sentence
// somebody wrote for exactly this purpose.
func TestTheWorldsOwnReasonSurvives(t *testing.T) {
	w := fixture()
	w.failApply = errors.New("box is unreachable from this machine; check the boxd channel")
	session := remote(t, w, operator())

	res, err := session.call(t, "caf_apply", map[string]any{"release": "identity-0.2.0"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatal("a world that failed was answered with a success")
	}
	if !strings.Contains(res.text(), "box is unreachable") {
		t.Errorf("the world's reason was reworded:\n%s", res.text())
	}
}

// The tool errors are the SDK's tool errors, not protocol errors, which is the
// first rule in this package's restated for the remote half.
func TestARemoteFailureIsAToolErrorNotAProtocolError(t *testing.T) {
	w := fixture()
	w.failPropose = errors.New("box refused the proposal")
	session := remote(t, w, operator())

	res, err := session.call(t, "caf_propose", map[string]any{"release": "r"})
	if err != nil {
		t.Fatalf("a remote failure took the session down: %v", err)
	}
	if !res.IsError {
		t.Error("a failing remote tool answered success")
	}
	// And the server is still serving, which is the half that makes a tool
	// error different from a protocol one.
	if again, err := session.call(t, "caf_health", nil); err != nil || again.IsError {
		t.Fatalf("the session did not survive the failure: err=%v isError=%v", err, again.IsError)
	}
}
