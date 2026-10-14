package failure

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cafaye/cafe/internal/boxd"
)

// The whole seed against one box — the box the doc's table describes, where
// nine of the ten conditions are present and the tenth is not. This is the
// test that says the library is a rule set and not a pile of prose: each entry
// fires against the facts it was written for.
func TestTheSeedEvaluatesAgainstTheBoxItWasWrittenFor(t *testing.T) {
	report := Evaluate(Library(), fullyPopulated())

	want := map[string]Verdict{
		"courier-readyz-slow-when-db-gone": Holds,
		"kit-proxy-healthchecks-up":        Holds,
		"identity-mfa-routes-absent":       Holds,
		"oidc-not-mounted":                 DoesNotHold,
		"postgres-two-pockets":             Holds,
		"traces-going-nowhere":             Holds,
		"outbox-rows-piling-up":            Holds,
		"gateway-sessions-per-process":     Holds,
		"courier-password-reset-503":       Holds,
		"secrets-frozen-cannot-rotate":     Holds,
		// The eleventh is the edge's own, and in this fixture the edge is
		// serving — so the condition the entry identifies (an edge that is
		// ABSENT while the box is up) is evaluated and does not hold. That is
		// the honest answer rather than an unknown: the fixture supplies the
		// fact, so the checker is entitled to answer.
		"edge-down-while-every-readyz-is-green": DoesNotHold,
	}
	if len(report.Findings) != len(want) {
		t.Fatalf("the report has %d findings for %d rules", len(report.Findings), len(want))
	}
	for _, f := range report.Findings {
		if got := f.Verdict; got != want[f.Rule.ID] {
			t.Errorf("%s: verdict %s, want %s (matched %v)", f.Rule.ID, got, want[f.Rule.ID], f.Matched)
		}
	}

	// Every hold carries the evidence a reader checks by hand. A hold with no
	// matched predicates is a hold nobody can confirm.
	for _, f := range report.Holds() {
		if len(f.Matched) == 0 {
			t.Errorf("%s holds but names nothing it observed", f.Rule.ID)
		}
	}
}

// A healthy box holds none of them. This is the other half of the test above
// and it is the one that stops the library being an alarm that is always on:
// every service healthchecked on core's pinned paths, one replica, a collector
// running, a publisher running, every setting present.
func TestAHealthyBoxHoldsNothing(t *testing.T) {
	obs := fullyPopulated()
	obs.Healthchecks = map[string]string{"kit": "/healthz", "courier": "/healthz"}
	obs.Replicas = map[string]int{"gateway": 1}
	obs.Collectors = []string{"http://collector:4317"}
	obs.Publishers = []string{"courier", "identity", "billing", "gateway"}
	obs.Settings["identity"] = []string{
		"MFA_ENCRYPTION_KEY", "OIDC_ISSUER", "OIDC_SIGNING_KEY", "OIDC_SIGNING_KEY_ID",
	}
	obs.Settings["courier"] = []string{"COURIER_MAIL_ADAPTER", "COURIER_SECRET_BOX_KEY"}
	obs.Databases = []Database{
		{Name: "courier", Service: "courier"},
		{Name: "identity", Service: "identity"},
	}
	obs.Probes = []Probe{{Service: "courier", Path: "/readyz", Status: 200, LatencyMS: 30}}

	report := Evaluate(Library(), obs)
	// The frozen-key rule is deliberately not in this list: it is a statement
	// about the stack rather than about a fault, so a box running muse at all
	// holds it and the only remedy is to name it.
	if got := len(report.Holds()); got != 1 {
		t.Errorf("a healthy box holds %d condition(s), want 1 (the frozen-key advisory): %v",
			got, ids(report.Holds()))
	}
	if got := report.Holds()[0].Rule.ID; got != "secrets-frozen-cannot-rotate" {
		t.Errorf("the one condition a healthy box holds is %s; the library's advisory row is expected there", got)
	}
}

// The box as its daemon can see it today holds NOTHING and cannot answer
// anything, and the report says so. This is the load-bearing test of the whole
// design: boxd's World carries service names and the install phase
// (internal/boxd/identity.go:130-135), so a checker driven by it must answer
// Unknown for the seven rules that need more and must not answer "does not
// hold" for any of them.
//
// A false "does not hold" is the failure this test exists to prevent. It is
// the same report lock/verify.go:112-114 refuses to produce: a green light
// over something nobody checked.
func TestTheBoxDaemonsObservationAnswersNothingAndSaysSo(t *testing.T) {
	world := &recordingWorld{stack: boxd.StackStatus{
		Services: []string{"courier", "identity", "gateway", "kit", "muse"},
		Phase:    3,
		Detail:   "applied through phase 3",
	}}

	obs, err := BoxObserver{World: world}.Observe(context.Background())
	if err != nil {
		t.Fatalf("observe the box: %v", err)
	}
	report := Evaluate(Library(), obs)

	if got := len(report.Holds()); got != 0 {
		t.Errorf("the box's own observation identifies %d condition(s) — %v — which would mean the "+
			"checker is answering questions nobody asked it", got, ids(report.Holds()))
	}
	if got := len(report.Findings) - len(report.Unknown()); got != 0 {
		t.Errorf("%d condition(s) were answered does-not-hold against a box that reported only its "+
			"service names; a rule nobody could evaluate must not pass", got)
	}
	if got := len(report.Unknown()); got != len(Library()) {
		t.Errorf("%d of %d rules are unknown, want all of them", got, len(Library()))
	}

	// The missing facts are named, and they are the specification of what the
	// substrate packet owes this library. Eight distinct fields: nine facts
	// exist in the Observation and Publishers is not among them, because rule
	// 7's FIRST predicate already went unknown and the walk stops at the
	// first fact it cannot answer rather than naming all of them at once.
	// Edge is the tenth and it is the one the edge packet owes.
	want := []string{"Databases", "Edge", "Healthchecks", "Instrumented", "Outbox", "Probes", "Replicas", "Settings"}
	if got := report.SortedMissing(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the observation is missing %v, want %v — the library's facts moved, or a rule "+
			"started reading something the surface does not carry", got, want)
	}
}

// An observer that cannot read the box is an error, not an empty observation.
// A box whose stack cannot be reported is a box whose install did not finish,
// and a report that said "0 conditions hold" for one would be the false green.
func TestAnObserverThatCannotReadTheBoxIsAnError(t *testing.T) {
	world := &recordingWorld{err: errors.New("no substrate reachable")}
	if _, err := (BoxObserver{World: world}).Observe(context.Background()); err == nil {
		t.Fatal("a world that cannot report its stack produced an observation")
	}
}

// A predicate the checker does not know is unanswerable, not false. This is the
// negative control over the vocabulary: a rule whose predicate kind was
// misspelled, removed or never written must go Unknown and name itself, because
// the alternative is a rule that quietly stops firing and looks identical to a
// rule that is quiet.
func TestAnUnknownPredicateIsUnanswerableRatherThanFalse(t *testing.T) {
	rules := []Rule{{
		ID:        "control-a-predicate-nobody-knows",
		Failure:   "a predicate kind that does not exist",
		Sees:      "nothing",
		Known:     "the control",
		Remedy:    "fix the vocabulary",
		Condition: Condition{All: []Predicate{{Kind: PredicateKind("no-such-kind")}}},
	}}

	report := Evaluate(rules, fullyPopulated())
	if got := len(report.Unknown()); got != 1 {
		t.Fatalf("%d of 1 rule(s) went unknown, want the one", got)
	}
	if got := report.Unknown()[0].Missing; got != "no such predicate kind no-such-kind" {
		t.Errorf("the unknown verdict names %q; it must name the thing that is wrong", got)
	}
	if report.Findings[0].Verdict == DoesNotHold {
		t.Error("an unknown predicate answered does-not-hold, which is how a rule silently stops firing")
	}
}

// The disjunction's other half: a condition with several legs is unknown only
// when NOT ONE of them could be answered. One answerable leg that does not hold
// is a real does-not-hold, and conflating the two would turn every partially
// observable box into a library that fires on everything.
func TestAnUnanswerableLegDoesNotStopAnAnswerableOne(t *testing.T) {
	rule := Rule{
		ID:        "control-one-of-two-legs",
		Condition: Condition{Any: []Predicate{{Kind: PredSettingSet, Service: "muse", Name: "MUSE_VAULT_KEY"}}},
	}
	obs := fullyPopulated()
	delete(obs.Settings, "muse")
	obs.Replicas = nil

	// One leg, unanswerable: the condition is unknown and names the fact.
	if _, missing := rule.Condition.evaluate(obs); missing != "Settings" {
		t.Errorf("a single unanswerable leg returned missing %q, want Settings", missing)
	}

	// Now a rule with one leg that answers false and one that cannot be
	// answered: the condition is genuinely false.
	two := Condition{Any: []Predicate{
		{Kind: PredSettingSet, Service: "identity", Name: "A_KEY_THAT_IS_NOT_SET"},
		{Kind: PredSettingUnset, Service: "muse", Name: "NEVER_LOOKED"},
	}}
	holds, missing := two.evaluate(obs)
	if holds || missing != "" {
		t.Errorf("a condition with an answerable leg that does not hold returned (%v, %q); one "+
			"unanswerable leg must not turn a real no into an unknown", holds, missing)
	}

	// And a condition with two unanswerable legs is unknown, naming the fact
	// rather than passing. This is the half of the control that proves the
	// disjunction cannot be used to make a rule look quiet.
	none := Condition{Any: []Predicate{
		{Kind: PredSettingUnset, Service: "muse", Name: "NEVER_LOOKED"},
		{Kind: PredSettingUnset, Service: "parlor", Name: "ALSO_NEVER_LOOKED"},
	}}
	if holds, missing := none.evaluate(obs); holds || missing != "Settings" {
		t.Errorf("two unanswerable legs returned (%v, %q), want (false, Settings)", holds, missing)
	}
}

// The predicates are tested at both sides of every boundary, because a
// threshold read at one side is a threshold that could be off by one and
// nobody would know. Each row is a fact, the value on each side of it, and what
// the checker must say.
func TestEveryPredicateIsEvaluatedAtBothSidesOfItsBoundary(t *testing.T) {
	base := fullyPopulated()
	cases := []struct {
		name      string
		predicate Predicate
		obs       Observation
		want      bool
	}{
		{"service running", Predicate{Kind: PredServiceRunning, Service: "kit"}, base, true},
		{"service absent", Predicate{Kind: PredServiceRunning, Service: "parlor"}, base, false},
		{"replicas over", Predicate{Kind: PredReplicasOver, Service: "gateway", Value: 1}, base, true},
		{"replicas at the floor", Predicate{Kind: PredReplicasOver, Service: "gateway", Value: 2}, base, false},
		{"setting set", Predicate{Kind: PredSettingSet, Service: "muse", Name: "MUSE_VAULT_KEY"}, base, true},
		{"setting unset", Predicate{Kind: PredSettingUnset, Service: "muse", Name: "MUSE_VAULT_KEY"}, base, false},
		{"healthcheck is the wrong path", Predicate{Kind: PredHealthcheckPathIs, Service: "kit", Name: "/up"}, base, true},
		{"healthcheck is core's path", Predicate{Kind: PredHealthcheckPathIs, Service: "kit", Name: "/healthz"}, base, false},
		{
			"probe over the floor", Predicate{Kind: PredProbeLatencyOver, Service: "courier", Name: "/readyz", Value: readyzLatencyFloor},
			base, true,
		},
		{
			"probe at the floor", Predicate{Kind: PredProbeLatencyOver, Service: "courier", Name: "/readyz", Value: 4500},
			base, false,
		},
		{
			"probe answered 503", Predicate{Kind: PredProbeStatusIs, Service: "courier", Name: "/readyz", Value: 503},
			base, true,
		},
		{
			"probe answered 200", Predicate{Kind: PredProbeStatusIs, Service: "courier", Name: "/readyz", Value: 200},
			base, false,
		},
		{"database declared twice", Predicate{Kind: PredDatabaseDeclaredTwice, Name: "kit"}, base, true},
		{"database declared once", Predicate{Kind: PredDatabaseDeclaredTwice, Name: "muse"}, base, false},
		{"outbox rows over zero", Predicate{Kind: PredOutboxRowsOver, Service: "courier", Value: 0}, base, true},
		{"outbox empty", Predicate{Kind: PredOutboxRowsOver, Service: "identity", Value: 0}, base, false},
		{"publisher absent", Predicate{Kind: PredPublisherAbsent, Service: "courier"}, base, true},
		{"collector absent", Predicate{Kind: PredCollectorAbsent}, base, true},
		{"instrumented", Predicate{Kind: PredInstrumented}, base, true},
	}
	for _, tc := range cases {
		got, known := tc.predicate.evaluate(tc.obs)
		if !known {
			t.Errorf("%s: the fully populated observation does not carry %s", tc.name, tc.predicate.fact())
			continue
		}
		if got != tc.want {
			t.Errorf("%s: predicate evaluated to %v, want %v", tc.name, got, tc.want)
		}
	}

	// And the same predicate against an observation that carries no fact at
	// all is unknown rather than false — the whole reason evaluate returns two
	// booleans.
	empty := Observation{}
	for _, tc := range cases {
		if _, known := tc.predicate.evaluate(empty); known {
			t.Errorf("%s: a predicate answered against an observation that carries nothing", tc.name)
		}
	}
}

// The empty-but-carried list is a fact and the nil list is not. Without this
// the library could not tell "the box has no publisher" from "nobody looked",
// and rule 7's whole condition is the first of those two.
func TestAnEmptyListIsAFactAndANilListIsNot(t *testing.T) {
	obs := fullyPopulated()
	obs.Publishers = nil
	if _, known := (Predicate{Kind: PredPublisherAbsent, Service: "courier"}).evaluate(obs); known {
		t.Error("a nil Publishers list was read as a fact")
	}
	obs.Publishers = []string{}
	if _, known := (Predicate{Kind: PredPublisherAbsent, Service: "courier"}).evaluate(obs); !known {
		t.Error("an empty Publishers list was read as nobody having looked")
	}
}

// The report is the product, so its shape is asserted rather than eyeballed:
// every hold names the remedy, the unknowns name the fact, and the count line
// says what it counted. A report that reads as a wall is a report nobody acts
// on — the argument lock/verify.go:195-201 already made for its own.
func TestTheReportRendersEveryHoldAndNamesEveryUnknown(t *testing.T) {
	report := Evaluate(Library(), fullyPopulated())
	out := report.String()

	for _, f := range report.Holds() {
		for _, part := range []string{f.Rule.ID, f.Rule.Failure, f.Rule.Sees, f.Rule.Known, f.Rule.Remedy} {
			if !strings.Contains(out, part) {
				t.Errorf("the report does not carry %s's %q", f.Rule.ID, part)
			}
		}
	}

	// The box daemon's observation: the unknowns are rendered and the facts
	// named, and the header says how many of each verdict there were. Nothing
	// passed and nothing was claimed to have passed.
	thin := Evaluate(Library(), Observation{Services: []string{"courier", "identity", "gateway", "kit", "muse"}})
	got := thin.String()
	if !strings.Contains(got, "11 rule(s) evaluated: 0 hold, 0 do not hold, 11 unknown") {
		t.Errorf("the header does not say what it counted:\n%s", got)
	}
	if !strings.Contains(got, "CONDITIONS THAT COULD NOT BE ANSWERED") {
		t.Error("the unknowns are not rendered as their own section")
	}
	if !strings.Contains(got, "the observation does not carry Settings") {
		t.Errorf("the unknowns do not name the missing fact:\n%s", got)
	}

	// No configured value appears anywhere in the report. The observation
	// carries setting NAMES, and a report that printed one of their values
	// would be the Attended tier leaking a credential into a monthly report.
	for _, value := range []string{"s3cret", "hunter2", "-----BEGIN"} {
		if strings.Contains(got, value) {
			t.Errorf("the report carries %q, which is a configured value and not a name", value)
		}
	}
}

// ids is the finding ids, for a failure message that says which rules fired.
func ids(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Rule.ID)
	}
	return out
}

// recordingWorld is a boxd.World with a recorded stack and nothing else. It is
// the same dozen-line fake internal/boxd's own tests use, and it exists so this
// package can prove what a checker driven by the box daemon's REAL seam
// answers — no network, no cluster, no mock of a seam that is not the seam.
type recordingWorld struct {
	stack    boxd.StackStatus
	identity boxd.Identity
	err      error
}

func (w *recordingWorld) Identity() boxd.Identity { return w.identity }

// Observe satisfies boxd.World after OPS-P15 added it to the interface. The
// failure rules in check.go read the stack state, not the observation, so this
// recorder answers with the same error the Stack path honors and otherwise a
// zero Observation: nothing here needs the report, and inventing one would make
// the test assert on data the rules never read.
func (w *recordingWorld) Observe(ctx context.Context, params boxd.ObserveParams) (boxd.Observation, error) {
	if w.err != nil {
		return boxd.Observation{}, w.err
	}
	return boxd.Observation{}, nil
}

func (w *recordingWorld) Stack(context.Context) (boxd.StackStatus, error) {
	if w.err != nil {
		return boxd.StackStatus{}, w.err
	}
	return w.stack, nil
}

func (w *recordingWorld) Apply(context.Context, int) (boxd.ApplyResult, error) {
	return boxd.ApplyResult{}, errors.New("the failure library never applies a phase")
}

func (w *recordingWorld) Envelope(context.Context, boxd.Envelope) (boxd.EnvelopeResult, error) {
	return boxd.EnvelopeResult{}, errors.New("the failure library never installs an envelope")
}
