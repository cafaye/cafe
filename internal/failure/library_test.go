package failure

import (
	"strings"
	"testing"
)

// The seed is the packet's first claim, so it is asserted rather than trusted:
// ten entries, in the doc's order, each with every field that makes it an
// entry rather than a line in a table. A library row with an empty Remedy is a
// row that tells a reader what has happened and nothing about what to do, which
// is trivia — and trivia in the Attended tier's core product is a thing the
// library exists to avoid.
func TestTheSeedIsTheTenDocumentedEntries(t *testing.T) {
	rules := Library()
	if got, want := len(rules), 10; got != want {
		t.Fatalf("the library has %d entries; the doc's table at CAFAYE-OPS.md:731-748 has %d", got, want)
	}

	seen := map[string]bool{}
	for i, rule := range rules {
		if rule.ID == "" {
			t.Errorf("entry %d has no id", i)
		}
		if seen[rule.ID] {
			t.Errorf("entry %q is in the library twice", rule.ID)
		}
		seen[rule.ID] = true

		// The four columns, plus the remedy derived from the third. An empty
		// Sees is the one that fails silently: a library that did not say what
		// a monitor sees is a library nobody can compare their dashboard to.
		for field, value := range map[string]string{
			"failure": rule.Failure,
			"sees":    rule.Sees,
			"known":   rule.Known,
			"remedy":  rule.Remedy,
		} {
			if strings.TrimSpace(value) == "" {
				t.Errorf("entry %q has an empty %s", rule.ID, field)
			}
		}
		if len(rule.Condition.All) == 0 && len(rule.Condition.Any) == 0 {
			t.Errorf("entry %q has no condition; it could never be evaluated", rule.ID)
		}
	}
}

// Every entry cites the doc. This is the claim the whole packet rests on — the
// seed is the ten documented entries and not ten invented ones — so it is the
// assertion, and the line numbers in Known are the evidence.
func TestEveryEntryCitesTheDoc(t *testing.T) {
	for _, rule := range Library() {
		if !strings.Contains(rule.Known, "CAFAYE-OPS.md:") {
			t.Errorf("entry %q does not cite CAFAYE-OPS.md, which is where its fact came from", rule.ID)
		}
	}
}

// Every predicate in every entry reads a fact the Observation actually carries.
// This is the check that makes the library more than prose: a rule whose
// predicate named a field that does not exist would evaluate to Unknown forever
// and nobody would notice, because Unknown looks like a rule that is quiet.
//
// It is driven through a fully populated observation so the walk is against the
// real evaluator rather than against a list of names kept beside it — a second
// list would be a second thing that can drift.
func TestEveryConditionReadsAFactTheObservationCarries(t *testing.T) {
	rules := Library()
	for _, rule := range rules {
		obs := fullyPopulated()
		_, missing := rule.Condition.evaluate(obs)
		if missing != "" {
			t.Errorf("entry %q reads %s, which the Observation does not supply", rule.ID, missing)
		}
	}
}

// The library is not silently a library of one shape: every entry's condition
// renders as a sentence. A condition nobody can read is a condition nobody
// checks by hand, and the hand check is what a reader does before believing the
// report.
func TestEveryConditionRendersAsASentence(t *testing.T) {
	for _, rule := range Library() {
		got := rule.Condition.describe()
		if got == "" {
			t.Errorf("entry %q has a condition that renders as nothing", rule.ID)
			continue
		}
		if strings.Contains(got, "unknown predicate") {
			t.Errorf("entry %q renders a predicate the checker does not know: %s", rule.ID, got)
		}
	}
}

// No rule carries a configured value. The observation surface is names and
// counts — the whole of what the ten conditions need — and this is the
// assertion that keeps it that way. It walks the seed's predicates for a
// setting NAME and refuses anything that looks like it could be a value.
func TestNoRuleCarriesAConfiguredValue(t *testing.T) {
	// The settings the seed names. Every one is a variable name a service
	// reads, which is what makes the observation safe to carry: `identity`'s
	// MFA routes are absent because MFA_ENCRYPTION_KEY is UNSET
	// (CAFAYE-OPS.md:741), so the fact is the name's presence.
	names := map[string]bool{}
	for _, rule := range Library() {
		for _, p := range append(append([]Predicate{}, rule.Condition.All...), rule.Condition.Any...) {
			if p.Kind == PredSettingSet || p.Kind == PredSettingUnset {
				names[p.Name] = true
			}
		}
	}
	if len(names) == 0 {
		t.Fatal("the seed names no setting at all, which means the four rules that depend on one are not expressing it")
	}
	for name := range names {
		if name != strings.ToUpper(name) {
			t.Errorf("%q is not an environment variable name; the observation carries names, never values", name)
		}
	}
}

// fullyPopulated is the box every rule in the seed was written against: every
// fact present, and present in the shape that makes every condition answerable.
// It is the fixture the other tests drive, and it is written once rather than
// per test so a new fact is added in one place.
func fullyPopulated() Observation {
	return Observation{
		Services: []string{"courier", "identity", "gateway", "kit", "muse"},
		Replicas: map[string]int{"gateway": 2},
		Settings: map[string][]string{
			"courier":  {"COURIER_SECRET_BOX_KEY"},
			"identity": {"OIDC_ISSUER", "OIDC_SIGNING_KEY", "OIDC_SIGNING_KEY_ID"},
			"muse":     {"MUSE_VAULT_KEY"},
		},
		Probes: []Probe{
			{Service: "courier", Path: "/readyz", Status: 503, LatencyMS: 4400},
			{Service: "identity", Path: "/readyz", Status: 200, LatencyMS: 12},
		},
		Healthchecks: map[string]string{"kit": "/up", "courier": "/healthz"},
		Databases: []Database{
			{Name: "kit", Service: "courier"},
			{Name: "kit", Service: "identity"},
			{Name: "muse", Service: "muse"},
		},
		Outbox:       map[string]int{"courier": 900, "identity": 0},
		Publishers:   []string{},
		Collector:    "",
		Instrumented: []string{"courier", "identity", "gateway", "kit"},
	}
}
