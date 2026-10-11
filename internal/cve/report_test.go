package cve

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fixed is a moment, so every assertion about the schedule is about a
// duration rather than about the clock.
var fixed = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// TestTheScheduleAcceptsBothSpellings covers the grammar and the one bug the
// grammar can hide.
//
// "1d" parses as a zero duration under time.ParseDuration, so a day-form
// schedule accepted through the duration path would silently become an
// interval of zero — a scan that is always due and never waits. Checking the
// day form first is what stops that, and the zero and negative rows are the
// control.
func TestTheScheduleAcceptsBothSpellings(t *testing.T) {
	ok := []struct {
		in   string
		want time.Duration
	}{
		{"", 24 * time.Hour},
		{"24h", 24 * time.Hour},
		{"90m", 90 * time.Minute},
		{"1d", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
	}
	for _, tc := range ok {
		got, err := ParseSchedule(tc.in)
		if err != nil {
			t.Errorf("ParseSchedule(%q) = %v, want %s", tc.in, err, tc.want)
			continue
		}
		if got.Every != tc.want {
			t.Errorf("ParseSchedule(%q) = %s, want %s", tc.in, got.Every, tc.want)
		}
	}
	bad := []string{"0d", "-1d", "0", "-6h", "daily", "1w"}
	for _, in := range bad {
		if _, err := ParseSchedule(in); err == nil {
			t.Errorf("ParseSchedule(%q) succeeded, want a refusal naming both spellings", in)
		}
	}
}

// TestTheScheduleStringRoundTrips keeps what the caller wrote rather than what
// cafe parsed: a report that printed "24h0m0s" for a schedule somebody wrote as
// "1d" would be a report about a different setting than the one configured.
func TestTheScheduleStringRoundTrips(t *testing.T) {
	for _, in := range []string{"1d", "7d", "6h", "90m"} {
		s, err := ParseSchedule(in)
		if err != nil {
			t.Fatalf("ParseSchedule(%q): %v", in, err)
		}
		if got := s.String(); got != in {
			t.Errorf("ParseSchedule(%q).String() = %q, want it back", in, got)
		}
	}
}

// TestDueDistinguishesNeverScannedFromNotYetDue is the point of the schedule
// state.
//
// A box nobody has scanned and a box that is four hours from its next run are
// different sentences, and only one of them is a gap in the product. A Due
// that returned the same answer for both would make "not due" and "never
// looked" indistinguishable in the report.
func TestDueDistinguishesNeverScannedFromNotYetDue(t *testing.T) {
	s := DefaultSchedule
	due, _, why := s.Due(fixed, time.Time{})
	if !due {
		t.Error("a box that has never been scanned is due, always")
	}
	if why != "never scanned" {
		t.Errorf("why = %q, want %q", why, "never scanned")
	}

	due, _, why = s.Due(fixed, fixed.Add(-23*time.Hour))
	if due {
		t.Error("23 hours into a 24-hour schedule is not due")
	}
	if !strings.Contains(why, "next scan in") {
		t.Errorf("why = %q, want it to name when the next scan is", why)
	}

	due, _, why = s.Due(fixed, fixed.Add(-25*time.Hour))
	if !due {
		t.Error("25 hours into a 24-hour schedule is due")
	}
	if !strings.Contains(why, "last scanned") {
		t.Errorf("why = %q, want it to name how stale the last answer is", why)
	}
}

// TestTheCronLineIsRenderedNotInstalled is the boundary this package stays
// inside.
//
// The line is printed for an operator to install, not written into a
// crontab: a cafe that edited root's crontab would be a cafe that changed how a
// box behaves without being asked, which is the same property the Attended
// tier promises in the other direction — recommendations the customer
// applies.
func TestTheCronLineIsRenderedNotInstalled(t *testing.T) {
	for _, in := range []string{"6h", "1d", "7d"} {
		s, err := ParseSchedule(in)
		if err != nil {
			t.Fatalf("ParseSchedule(%q): %v", in, err)
		}
		line := CronLine(s, "/usr/local/bin/cafe")
		if !strings.HasPrefix(line, "0") && !strings.HasPrefix(line, "*/") {
			t.Errorf("CronLine(%q) = %q, want a cron schedule field first", in, line)
		}
		if !strings.Contains(line, "cafe cve scan") {
			t.Errorf("CronLine(%q) = %q, want it to name the command it runs", in, line)
		}
	}
	// The fleet-wide reason the hour is not pinned: a fixed minute would put
	// every box on the advisory feed in the same second.
	line := CronLine(mustSchedule(t, "1d"), "/usr/local/bin/cafe")
	if strings.HasPrefix(line, "0 0 ") {
		t.Errorf("a daily schedule pins midnight (%q), which collides every box in the fleet on the feed at once", line)
	}
}

func mustSchedule(t *testing.T, in string) Schedule {
	t.Helper()
	s, err := ParseSchedule(in)
	if err != nil {
		t.Fatalf("ParseSchedule(%q): %v", in, err)
	}
	return s
}

// TestTheStateRoundTripsAndAMissingFileIsNotAnError covers the continuity
// mechanism: a box that has never been scanned has no state, and that is the
// zero state rather than a failure.
func TestTheStateRoundTripsAndAMissingFileIsNotAnError(t *testing.T) {
	path := t.TempDir() + "/nested/cve-state.json"
	if _, err := LoadState(path); err != nil {
		t.Fatalf("LoadState on a path that does not exist = %v, want the zero state", err)
	}
	want := State{Box: "box-1", LastRun: fixed, LastOutcome: "1 affected", Findings: 1, ScanSeconds: 4.5}
	if err := SaveState(path, want); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.Box != want.Box || got.Findings != want.Findings || got.ScanSeconds != want.ScanSeconds {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	if !got.LastRun.Equal(want.LastRun) {
		t.Errorf("LastRun = %s, want %s", got.LastRun, want.LastRun)
	}
}

// TestRenderNamesTheFourFactsTheBriefAsksFor is the output test, and it is
// the one the packet is judged on.
//
// A report that says "14 advisories" is the failure this exists to replace,
// so the assertion is not that the output mentions a component — it is that
// every row carries all four: the component, the advisory, the version that
// fixes it, and what applying it touches. A report missing any one of them
// sends the operator somewhere else to find it, which is the cost the brief
// is naming.
func TestRenderNamesTheFourFactsTheBriefAsksFor(t *testing.T) {
	r := Report{
		Box:       "box-1",
		ScannedAt: fixed,
		Schedule:  "1d",
		Staleness: "last scanned 25 hours",
		Counts:    Counts{Components: 17, Advisories: 14, Affected: 1, NoFix: 1},
		Findings: []Finding{
			{
				Component: "redis", Version: "7.4.1-alpine", Kind: KindWorkload,
				Severity: SeverityHigh, AdvisoryID: "CVE-2025-1234", Summary: "a heap overflow in the reply encoder",
				Class: ClassAffected, Fixed: "7.4.2", ReleaseTo: "7.4.2",
				Impact: Blast{Apply: "bump the tag in redis.yaml", Restarts: []string{"the redis deployment in namespace data"}},
				Why:    "redis 7.4.1-alpine is affected; 7.4.2 fixes it",
			},
			{
				Component: "gateway", Version: "0.1.0", Kind: KindChart,
				Severity: SeverityCritical, AdvisoryID: "CVE-2025-9999", Summary: "an auth bypass in the proxy filter",
				Class: ClassNoFix,
				Why:   "gateway 0.1.0 is affected and no fix is published",
			},
		},
	}
	var b bytes.Buffer
	if err := Render(&b, r); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()
	for _, want := range []string{
		// the counts, so "14 advisories" is never the whole answer
		"17 components checked against 14 advisories",
		"1 affected, 1 with no fix",
		// row one: all four facts
		"redis 7.4.1-alpine", "CVE-2025-1234", "release 7.4.2", "bump the tag in redis.yaml",
		"the redis deployment in namespace data",
		// row two: the no-fix sentence, which is a decision and not an upgrade
		"CVE-2025-9999", "no fix published", "gateway 0.1.0",
		// the schedule and its staleness, so the freshness is in the document
		"1d", "last scanned 25 hours",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not contain %q\n---\n%s", want, out)
		}
	}
	// A component with no characterised blast radius says so rather than
	// printing an empty impact block that reads as "touches nothing".
	if !strings.Contains(out, "unknown") {
		t.Error("the no-fix gateway row says nothing about an unknown blast radius — an empty impact block reads as \"touches nothing\"")
	}
}

// TestRenderReportsAnUnreadableVersionAsARowNotASilence is the report half of
// the classification's central rule.
func TestRenderReportsAnUnreadableVersionAsARowNotASilence(t *testing.T) {
	r := Report{
		Counts: Counts{Components: 1, Unknown: 1},
		Findings: []Finding{{
			Component: "redis", Version: "latest", Kind: KindImage, Severity: SeverityUnknown,
			AdvisoryID: "CVE-2025-1234", Class: ClassUnknown, ImpactUnknown: true,
			Why: "redis latest could not be compared",
		}},
	}
	var b bytes.Buffer
	if err := Render(&b, r); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(b.String(), "UNRATED") {
		t.Errorf("an advisory with no severity must print UNRATED, not LOW:\n%s", b.String())
	}
	if !strings.Contains(b.String(), "1 unreadable") {
		t.Errorf("the summary must count the unreadable one:\n%s", b.String())
	}
	if strings.Contains(b.String(), "nothing to act on") {
		t.Error("an unreadable version is not a clean box — the report must not say it is")
	}
}

// TestRenderNamesWhatTheBoxRunsThatTheSubstrateDoesNot covers the divergence
// block: a box running a version nobody pinned is a security fact even with no
// advisories against it.
func TestRenderNamesWhatTheBoxRunsThatTheSubstrateDoesNot(t *testing.T) {
	r := Report{
		Box:    "box-1",
		Counts: Counts{Components: 17},
		Divergences: []Divergence{{
			Component: "caddy", Observed: "2.8.4",
			Why: "running on the box and not in the substrate catalogue",
		}},
	}
	var b bytes.Buffer
	if err := Render(&b, r); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "caddy") || !strings.Contains(out, "2.8.4") {
		t.Errorf("the divergence block does not name the component and its version:\n%s", out)
	}
	if !strings.Contains(out, "not in the substrate catalogue") {
		t.Errorf("the divergence block does not say why it matters:\n%s", out)
	}
	if strings.Contains(out, "nothing to act on") {
		t.Error("a divergence is not a clean box — the report must not say it is")
	}
}

// TestTheFeedSilentListIsNotAnEmptyList is the vacuity control over the
// advisory fetch.
//
// "Checked and clean" and "not indexed" are different answers, and only the
// first is worth anything. A source that answered nothing for fifteen of
// seventeen components must produce a report that says so, and the
// Unmatched list is where that is said.
func TestTheFeedSilentListIsNotAnEmptyList(t *testing.T) {
	components := []Component{
		{Name: "redis", Version: "7.4.1-alpine"},
		{Name: "k3s", Version: "v1.37.1+k3s1"},
		{Name: "postgres", Version: ""},
	}
	silent := feedSilent(components, []Advisory{{Component: "redis"}})
	if len(silent) != 1 || silent[0] != "k3s" {
		t.Errorf("feedSilent = %v, want [k3s] — postgres has no readable version and is already an unknown classification", silent)
	}
}

// TestEncodeIsTheSameDocument is the two-renderers-over-one-value claim: the
// JSON document is not a second opinion about what was found.
func TestEncodeIsTheSameDocument(t *testing.T) {
	r := Report{Box: "box-1", ScannedAt: fixed, Counts: Counts{Components: 2, Affected: 1},
		Findings: []Finding{{Component: "redis", AdvisoryID: "CVE-2025-1234", Class: ClassAffected, Fixed: "7.4.2", ReleaseTo: "7.4.2"}}}
	var b bytes.Buffer
	if err := Encode(&b, r); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var back Report
	if err := json.Unmarshal(b.Bytes(), &back); err != nil {
		t.Fatalf("the machine document is not JSON: %v", err)
	}
	if back.Counts.Affected != r.Counts.Affected || len(back.Findings) != 1 || back.Findings[0].ReleaseTo != "7.4.2" {
		t.Errorf("round trip = %+v, want %+v", back, r)
	}
	if !strings.Contains(b.String(), "\"scanned_at\"") {
		t.Error("the machine document carries no timestamp — a report whose freshness is only knowable from a file's mtime is a report whose freshness nobody checks")
	}
}
