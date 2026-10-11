package cve

import (
	"strings"
	"testing"
)

// TestEverySubstrateComponentCarriesWhereItsVersionCameFrom is the control
// over the catalogue's honesty.
//
// Every row of Substrate is a transcription of a pin in another repository,
// and a transcription nobody can check is a transcription nobody has checked.
// So every row names the file:line it was read from, and this asserts it —
// not the specific lines, which would be a test that breaks when kit moves a
// pin for a reason that has nothing to do with cafe, but the shape: a path
// with a colon and a number in it.
//
// The negative control is the row that gets the citation removed: the test
// fails by name, which is the only kind of failure anybody acts on.
func TestEverySubstrateComponentCarriesWhereItsVersionCameFrom(t *testing.T) {
	for _, c := range Substrate {
		if c.Source == "" {
			t.Errorf("%s carries no source — a component whose version nobody can check is a component nobody can trust", c.Name)
			continue
		}
		colon := strings.LastIndexByte(c.Source, ':')
		if colon < 0 || colon == len(c.Source)-1 {
			t.Errorf("%s's source %q is not a file:line citation", c.Name, c.Source)
			continue
		}
		line := c.Source[colon+1:]
		for _, r := range line {
			if r < '0' || r > '9' {
				t.Errorf("%s's source %q does not end in a line number", c.Name, c.Source)
				break
			}
		}
	}
}

// TestTheCatalogueNamesOnlyWhatTheSubstrateInstalls pins the set, so adding a
// component is a decision that has to be made in a test rather than in a
// package file.
//
// The brief's rule is "do not invent components", and this is where that rule
// is enforced against future-me: a row appears here because the installer
// installs it, and nothing appears here because it seems plausible.
func TestTheCatalogueNamesOnlyWhatTheSubstrateInstalls(t *testing.T) {
	want := map[string]string{
		"k3s":            "v1.37.1+k3s1",
		"cert-manager":   "v1.21.2",
		"cloudnative-pg": "v1.30.1",
		"argocd":         "3.5.4",
		"redis":          "7.4.1-alpine",
		"nats":           "2.10.24-alpine",
		"nats-box":       "0.20.0",
		"identity":       "0.1.0",
		"gateway":        "0.1.0",
		"courier":        "0.1.0",
		"parlor":         "0.1.0",
		"alloy":          "1.13.0",
		"grafana":        "10.5.15",
		"loki":           "7.3.0",
		"prometheus":     "29.36.0",
		// postgres and traefik carry no pinned version, on purpose, and both
		// say why in their comment.
		"postgres": "",
		"traefik":  "",
	}
	got := make(map[string]string, len(Substrate))
	for _, c := range Substrate {
		got[c.Name] = c.Version
	}
	if len(got) != len(want) {
		t.Errorf("the catalogue has %d components, want %d — an added row is a decision, and this is where it is made", len(got), len(want))
	}
	for name, version := range want {
		if got[name] != version {
			t.Errorf("%s is pinned %q, want %q", name, got[name], version)
		}
	}
}

// TestEveryPinnedComponentNamesWhatAFixTouches is the other half of the
// brief's requirement: output that names what applying a fix would touch.
//
// A component with a version and no blast radius is a row that says "upgrade
// this" and nothing else, which is the useless answer the packet exists to
// replace. The two rows that legitimately have none are the ones the report
// marks ImpactUnknown.
func TestEveryPinnedComponentNamesWhatAFixTouches(t *testing.T) {
	for _, c := range Substrate {
		if c.Blast.Apply == "" {
			t.Errorf("%s names no blast radius — the report could say \"upgrade this\" and nothing about what it touches", c.Name)
		}
	}
}

// TestClassification covers the matcher against the three answers and the one
// it must never give.
//
// The last row is the load-bearing one. A version cafe cannot read is unknown,
// and rendering it as unaffected is the single failure that leaves a box
// unpatched: the report says "we do not know" and a person goes and looks.
func TestClassification(t *testing.T) {
	cases := []struct {
		name       string
		version    string
		introduced string
		fixed      string
		want       Class
	}{
		{"in the range, with a fix", "7.4.1-alpine", "7.4.0", "7.4.2", ClassAffected},
		{"the fixed version itself is not affected", "7.4.2-alpine", "7.4.0", "7.4.2", ClassUnaffected},
		{"before the introduction is not affected", "7.3.9-alpine", "7.4.0", "7.4.2", ClassUnaffected},
		{"no introduction means everything before the fix", "1.0.0-alpine", "", "7.4.2", ClassAffected},
		{"no fix published", "7.4.1-alpine", "7.4.0", "", ClassNoFix},
		{"an advisory with neither bound affects everything", "7.4.1-alpine", "", "", ClassNoFix},
		{"ten is newer than nine, which a string comparison gets backwards", "10.0.0-alpine", "9.0.0", "9.0.1", ClassUnaffected},
		{"the substrate's own k3s spelling", "v1.37.1+k3s1", "v1.30.0+k3s1", "v1.37.2+k3s1", ClassAffected},
		{"a distro suffix is the same version: 7.4.1-alpine is 7.4.1", "7.4.1-alpine", "0.0.0", "7.4.1", ClassUnaffected},
		{"a fixed version written as a distro revision", "7.4.1-alpine", "0.0.0", "7.4.2-r1", ClassAffected},
		{"an image reference with no version is not readable", "latest", "7.4.0", "7.4.2", ClassUnknown},
		{"a digest is not a version", "", "7.4.0", "7.4.2", ClassUnknown},
		{"a two-component version is not readable", "7.4", "7.4.0", "7.4.2", ClassUnknown},
		{"an opaque reference is not readable", "sha256:9f86d08", "7.4.0", "7.4.2", ClassUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := Advisory{ID: "CVE-2025-0001", Component: "redis", Introduced: tc.introduced, Fixed: tc.fixed}
			if got := a.Affects(tc.version); got != tc.want {
				t.Errorf("Affects(%q) with introduced %q and fixed %q = %q, want %q",
					tc.version, tc.introduced, tc.fixed, got, tc.want)
			}
		})
	}
}

// TestClassificationNeverAnswersUnaffectedWhenItCannotReadTheVersion is the
// negative control over the rule above.
//
// It is a separate test rather than a row above because it asserts the
// property rather than an instance of it: for EVERY unreadable spelling, the
// answer is never ClassUnaffected. A test with one row asserts one case; this
// asserts the rule, and it is the one a future edit could quietly break.
func TestClassificationNeverAnswersUnaffectedWhenItCannotReadTheVersion(t *testing.T) {
	unreadable := []string{"", "latest", "sha256:abc123", "7.4", "v1.x", "1.2.3.4", "alpine", "debian:12"}
	for _, version := range unreadable {
		a := Advisory{ID: "CVE-2025-0002", Component: "redis", Introduced: "1.0.0", Fixed: "9.9.9"}
		if got := a.Affects(version); got == ClassUnaffected {
			t.Errorf("Affects(%q) = unaffected — an unreadable version must be unknown, because "+
				"calling it unaffected is how a box stays unpatched", version)
		}
	}
}

// TestClassifySortsSeverityFirst covers the ordering argument: the top of the
// report is what the customer reads before they stop reading.
func TestClassifySortsSeverityFirst(t *testing.T) {
	components := []Component{
		{Name: "redis", Version: "7.4.1-alpine", Kind: KindWorkload,
			Blast: Blast{Apply: "bump the tag in redis.yaml", Restarts: []string{"the redis deployment in namespace data"}}},
		{Name: "k3s", Version: "v1.37.1+k3s1", Kind: KindNode,
			Blast: Blast{Apply: "re-run the substrate installer", Restarts: []string{"every workload on the box"}}},
	}
	advisories := []Advisory{
		{ID: "CVE-2025-0001", Component: "redis", Introduced: "1.0.0", Fixed: "7.4.2", Severity: SeverityLow},
		{ID: "CVE-2025-0002", Component: "k3s", Introduced: "1.30.0", Fixed: "1.37.2", Severity: SeverityCritical},
		{ID: "CVE-2025-0003", Component: "redis", Introduced: "1.0.0", Fixed: "", Severity: SeverityHigh},
		{ID: "CVE-2025-0004", Component: "gateway", Introduced: "0.1.0", Fixed: "0.1.1", Severity: SeverityCritical},
	}
	findings, counts := Classify(components, advisories)

	if len(findings) != 3 {
		t.Fatalf("got %d findings, want 3 — the gateway advisory has no component on the box and is not a finding", len(findings))
	}
	wantOrder := []string{"CVE-2025-0002", "CVE-2025-0003", "CVE-2025-0001"}
	for i, want := range wantOrder {
		if findings[i].AdvisoryID != want {
			t.Errorf("finding %d is %s, want %s — severity first, then component, then advisory id", i, findings[i].AdvisoryID, want)
		}
	}
	if counts.Components != 2 || counts.Advisories != 4 {
		t.Errorf("counts = %+v, want 2 components against 4 advisories — the counts are the denominator, not the rows", counts)
	}
	if counts.Affected != 2 || counts.NoFix != 1 {
		t.Errorf("counts = %+v, want 2 affected and 1 with no fix", counts)
	}
	// The buyer's row: the fixed version and what applying it touches, on the
	// critical one.
	if findings[0].Fixed != "1.37.2" || findings[0].ReleaseTo != "1.37.2" {
		t.Errorf("the k3s finding names fixed %q / release %q, want 1.37.2 for both", findings[0].Fixed, findings[0].ReleaseTo)
	}
	if !strings.Contains(findings[0].Impact.Apply, "substrate installer") {
		t.Errorf("the k3s finding's blast radius says %q, which does not name how a node agent is upgraded", findings[0].Impact.Apply)
	}
	if len(findings[0].Impact.Restarts) == 0 {
		t.Error("the k3s finding names nothing that restarts — a node-agent upgrade drains the box and the report must say so")
	}
}

// TestClassifyCountsUnaffectedButDoesNotReportThem covers the asymmetry that
// makes the rest of the report believable.
func TestClassifyCountsUnaffectedButDoesNotReportThem(t *testing.T) {
	components := []Component{{Name: "redis", Version: "7.4.2-alpine", Kind: KindWorkload}}
	advisories := []Advisory{{ID: "CVE-2025-0009", Component: "redis", Introduced: "1.0.0", Fixed: "7.4.2"}}
	findings, counts := Classify(components, advisories)
	if len(findings) != 0 {
		t.Errorf("got %d findings, want 0 — a fixed version is not a row in a report about what to fix", len(findings))
	}
	if counts.Unaffected != 1 || counts.Components != 1 {
		t.Errorf("counts = %+v, want 1 unaffected out of 1 component — the count is what makes \"0 findings\" mean something", counts)
	}
}

// TestAnUnreadableVersionIsAReportRowNotASilence is the other asymmetry: the
// unknown case must reach the report.
func TestAnUnreadableVersionIsAReportRowNotASilence(t *testing.T) {
	components := []Component{{Name: "redis", Version: "latest", Kind: KindImage}}
	advisories := []Advisory{{ID: "CVE-2025-0010", Component: "redis", Introduced: "1.0.0", Fixed: "7.4.2", Severity: SeverityHigh}}
	findings, counts := Classify(components, advisories)
	if counts.Unknown != 1 {
		t.Errorf("counts.Unknown = %d, want 1", counts.Unknown)
	}
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1 — an unreadable version is a row a person has to look at", len(findings))
	}
	if findings[0].Class != ClassUnknown {
		t.Errorf("class = %q, want %q", findings[0].Class, ClassUnknown)
	}
	if findings[0].ReleaseTo != "" {
		t.Errorf("release_to = %q, want empty — cafe cannot tell an operator to move to a version it cannot compare", findings[0].ReleaseTo)
	}
	if findings[0].ImpactUnknown != true {
		t.Error("a component with no characterised blast radius must say so rather than printing an empty impact block")
	}
}

// TestInventoryCompareNamesBothDirectionsOfDrift is the control over the
// catalogue/observation split.
//
// A declared pin nobody applied and an applied version nobody pinned are the
// two facts this split exists to keep apart, and folding them into one list
// of components would lose both.
func TestInventoryCompareNamesBothDirectionsOfDrift(t *testing.T) {
	inv := Inventory{
		Declared: []Component{
			{Name: "redis", Version: "7.4.1-alpine"},
			{Name: "nats", Version: "2.10.24-alpine"},
		},
		Observed: []Component{
			{Name: "redis", Version: "7.4.3-alpine"},
			{Name: "caddy", Version: "2.8.4"},
		},
	}
	divergences := inv.Compare()
	if len(divergences) != 3 {
		t.Fatalf("got %d divergences, want 3 (redis drifted, nats missing, caddy undeclared)", len(divergences))
	}
	var redis, nats, caddy Divergence
	for _, d := range divergences {
		switch d.Component {
		case "redis":
			redis = d
		case "nats":
			nats = d
		case "caddy":
			caddy = d
		}
	}
	if redis.Declared != "7.4.1-alpine" || redis.Observed != "7.4.3-alpine" {
		t.Errorf("redis divergence = %+v, want both spellings — a report that named only one is a report that hides which side moved", redis)
	}
	if nats.Observed != "" {
		t.Errorf("nats divergence observed %q, want empty — nothing was observed, and that is a different fact from an observed zero", nats.Observed)
	}
	if caddy.Declared != "" {
		t.Errorf("caddy divergence declared %q, want empty — nothing is declared, and the report must not pretend the substrate knows about it", caddy.Declared)
	}
}

// TestAnEmptyObservationIsNotASixteenComponentDrift is the vacuity control.
//
// "Nobody looked" must not render as "the box lost everything": a report
// that did would be the loudest wrong answer in the package, and it would
// send an operator to rebuild a box that was never down.
func TestAnEmptyObservationIsNotASixteenComponentDrift(t *testing.T) {
	inv := Inventory{Declared: Substrate}
	if got := inv.Compare(); len(got) != 0 {
		t.Errorf("got %d divergences with nothing observed, want 0", len(got))
	}
	// And the observation that does happen still reports the catalogue in
	// full, so an empty observer is a missing run rather than an empty box.
	if got := len(inv.Components()); got != len(Substrate) {
		t.Errorf("components = %d, want %d — the declared half is the inventory's floor", got, len(Substrate))
	}
}

// TestSameVersionTreatsTheSubstratesOwnSpellingAsOneFact is the identity rule
// for the k3s spelling, where `v1.37.1+k3s1` and `1.37.1` are one node agent.
func TestSameVersionTreatsTheSubstratesOwnSpellingAsOneFact(t *testing.T) {
	same := [][2]string{
		{"v1.37.1+k3s1", "1.37.1"},
		{"v1.37.1+k3s1", "v1.37.1+k3s1"},
		{"7.4.1-alpine", "7.4.1-alpine"},
	}
	for _, pair := range same {
		if !sameVersion(pair[0], pair[1]) {
			t.Errorf("sameVersion(%q, %q) = false, want true", pair[0], pair[1])
		}
	}
	different := [][2]string{
		{"v1.37.1+k3s1", "v1.37.2+k3s1"},
		{"7.4.1-alpine", "7.4.2-alpine"},
	}
	for _, pair := range different {
		if sameVersion(pair[0], pair[1]) {
			t.Errorf("sameVersion(%q, %q) = true, want false", pair[0], pair[1])
		}
	}
}
