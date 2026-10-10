package report

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cafaye/caf/internal/backup"
	"github.com/cafaye/caf/internal/cve"
	"github.com/cafaye/caf/internal/failure"
	"github.com/cafaye/caf/internal/secret"
)

// fixture is one box's month, in every shape the report consumes. It is built
// here rather than decoded from testdata because the point of these tests is
// the ASSEMBLY — what the report says about a month — and a fixture that had to
// be decoded first would put the input's shape between the test and that.
func fixture() Input {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	resolved := now.Add(-20 * 24 * time.Hour)
	return Input{
		Box:    "acme-prod",
		Period: Period{Start: start, End: end},
		Now:    now,
		Uptime: []ServiceUptime{
			{Service: "gateway", UptimePerMille: 9995, Downtime: 21 * time.Minute, LongestOutage: 14 * time.Minute, Notes: "config reload"},
			{Service: "courier", UptimePerMille: 10000},
		},
		Patches: []Patch{
			{Component: "redis", From: "7.2.4", To: "7.2.5", Kind: "addon", AppliedAt: start.Add(48 * time.Hour), Reason: "GHSA-xxxx", Restarted: []string{"redis"}},
			{Component: "caddy", From: "2.7.6", To: "2.8.0", AppliedAt: start.AddDate(0, 0, -40)},
		},
		Capacity: &Capacity{Resource: "disk", Unit: "%", Points: []CapacityPoint{
			{At: start, Value: 41.0},
			{At: start.AddDate(0, 0, 15), Value: 55.5, Detail: "the muse image cache is 60% of it"},
			{At: end.Add(-time.Hour), Value: 70.25, Detail: "the muse image cache is 60% of it"},
		}},
		Incidents: []Incident{
			{ID: "INC-1", StartedAt: start.Add(72 * time.Hour), Severity: "sev2", Summary: "courier's mail queue backed up",
				ResolvedAt: &resolved, Resolution: "the outbox publisher was restarted"},
			{ID: "INC-2", StartedAt: end.Add(-26 * time.Hour), Severity: "sev3", Summary: "identity's JWKS fetch was slow"},
		},
		Upstream: []Release{
			{Name: "cafaye", Version: "1.14.0", ReceivedAt: start.Add(100 * time.Hour), Decision: DecisionApplied},
			{Name: "cafaye", Version: "1.15.0", ReceivedAt: start.Add(400 * time.Hour), Decision: DecisionHeld,
				HeldReason: "identity's signing key cutover needs a window the customer picks"},
		},
		Drills: []backup.DrillRecord{
			{
				Service: "courier", Engine: backup.EngineKamal, Env: "production", Scratch: "courier_drill",
				Tables: []string{"messages"}, Outcome: backup.DrillPassed, Reason: backup.DrillReasonPassed,
				StartedAt: start.Add(10 * time.Hour), FinishedAt: start.Add(11 * time.Hour), DurationMS: 4200,
			},
			{
				Service: "gateway", Engine: backup.EngineKamal, Env: "production", Scratch: "gateway_drill",
				Outcome: backup.DrillFailed, Reason: backup.DrillReasonFailed, Detail: "restic: unable to create lock",
				StartedAt: start.Add(20 * time.Hour), FinishedAt: start.Add(21 * time.Hour), DurationMS: 9000,
			},
			{
				Service: "identity", Engine: backup.EngineKamal,
				Outcome: backup.DrillRefused, Reason: backup.DrillReasonRefused, Detail: "accessory missing from the kamal config",
				StartedAt: start.Add(30 * time.Hour), FinishedAt: start.Add(30 * time.Hour),
			},
			// Outside the window on purpose: it is the record that proves the
			// section is scoped to the month and not to the history.
			{
				Service: "muse", Engine: backup.EngineKamal, Outcome: backup.DrillPassed, Reason: backup.DrillReasonPassed,
				StartedAt: start.AddDate(0, -3, 0), FinishedAt: start.AddDate(0, -3, 0), DurationMS: 3100,
			},
		},
		Inventory: []secret.Entry{
			{Name: "SIGNUP_SIGNING_KEY", Namespace: "identity", Secret: "identity-secrets", Key: "SIGNUP_SIGNING_KEY",
				Class: secret.NotRotatable, Reason: "identity publishes exactly one key", Source: "install.sh:1199", Shape: "32b64url"},
			{Name: "SMTP_PASSWORD", Namespace: "courier", Class: secret.Rotatable, Interval: 90 * 24 * time.Hour, Shape: "32"},
			{Name: "DATABASE_URL", Namespace: "courier", Class: secret.Conditional, Reason: "second write"},
		},
		Rotations: secret.State{
			Last:    map[string]time.Time{"SMTP_PASSWORD": start.Add(6 * 24 * time.Hour)},
			Refused: map[string]string{"SIGNUP_SIGNING_KEY": "not-rotatable: identity publishes exactly one key"},
		},
		Scan: cve.Report{
			Box: "acme-prod", ScannedAt: now.Add(-30 * time.Minute), Schedule: "1d", Staleness: "next scan in 23 hours",
			DurationSeconds: 11.2,
			Counts:          cve.Counts{Components: 17, Advisories: 42, Affected: 3, NoFix: 1, Unknown: 0},
			Findings: []cve.Finding{
				{Component: "redis", Version: "7.2.4", Kind: cve.KindAddon, Severity: cve.SeverityHigh, AdvisoryID: "GHSA-1",
					Class: cve.ClassAffected, ReleaseTo: "7.2.5", Impact: cve.Blast{Apply: "kubectl patch the addon", Restarts: []string{"redis", "gateway"}}, Why: "restart both"},
			},
			Divergences: []cve.Divergence{{Component: "muse", Why: "the catalogue does not declare 1.9"}},
			Unmatched:   []string{"caddy"},
		},
		Modes: failure.Evaluate(failure.Library(), failure.Observation{}),
	}
}

// The whole month must land on the page: every section filled, and no section
// claiming a gap it did not have.
func TestAssembleFillsEverySectionAndNamesTheGapsItHas(t *testing.T) {
	rep := Assemble(fixture())

	if rep.Summary() == "" {
		t.Fatal("an empty summary is a page nobody reads")
	}
	for _, want := range []string{"acme-prod", "uptime", "incident", "patch"} {
		if !strings.Contains(rep.Summary(), want) {
			t.Errorf("summary %q does not name %q", rep.Summary(), want)
		}
	}

	if len(rep.Availability.Services) != 2 {
		t.Errorf("services = %d, want 2", len(rep.Availability.Services))
	}
	// 9995 and 10000 average to 9997 per-mille, and the integer arithmetic is
	// the point: a float here would carry a fraction of a percent the box never
	// promised.
	if rep.Availability.FleetPerMille != 9997 {
		t.Errorf("fleet uptime = %d per-mille, want 9997", rep.Availability.FleetPerMille)
	}
	if rep.Availability.Worst != "gateway" {
		t.Errorf("worst = %q, want gateway", rep.Availability.Worst)
	}

	// The caddy patch predates the window and must not be in it, which is the
	// boundary rule working in the direction that under-counts rather than
	// double-counts.
	if len(rep.PatchesApplied) != 1 || rep.PatchesApplied[0].Component != "redis" {
		t.Fatalf("patches = %+v, want only redis", rep.PatchesApplied)
	}

	if rep.Backups.Passed != 1 || rep.Backups.Failed != 1 || rep.Backups.Refused != 1 {
		t.Errorf("drills = %d passed, %d failed, %d refused, want 1/1/1",
			rep.Backups.Passed, rep.Backups.Failed, rep.Backups.Refused)
	}
	// muse is in Latest and not in InWindow: the record is outside the window
	// and still the answer to "when was muse last proven restorable".
	if len(rep.Backups.InWindow) != 3 {
		t.Errorf("drills in window = %d, want 3", len(rep.Backups.InWindow))
	}
	if len(rep.Backups.Latest) != 4 {
		t.Errorf("latest per service = %d, want 4", len(rep.Backups.Latest))
	}
	// gateway reports availability and has a record, so nobody is named.
	if len(rep.Backups.NeverDrilled) != 0 {
		t.Errorf("never drilled = %v, want none", rep.Backups.NeverDrilled)
	}

	if len(rep.Incidents) != 2 {
		t.Fatalf("incidents = %d, want 2", len(rep.Incidents))
	}
	// INC-2 has no resolution time, and the report must say ongoing rather
	// than leaving a reader to work it out from an absent field.
	var ongoing int
	for _, inc := range rep.Incidents {
		if inc.Ongoing {
			ongoing++
		}
	}
	if ongoing != 1 {
		t.Fatalf("ongoing = %d, want 1 — an incident with no resolution time must render as ongoing", ongoing)
	}

	if rep.Capacity == nil {
		t.Fatal("a three-point series is a trend and must not be nil")
	}
	if rep.Capacity.Direction != "rising" {
		t.Errorf("direction = %q, want rising", rep.Capacity.Direction)
	}

	if got := rep.heldReleases(); got != 1 {
		t.Errorf("held releases = %d, want 1", got)
	}

	if rep.Security.Counts.Affected != 3 {
		t.Errorf("affected = %d, want 3", rep.Security.Counts.Affected)
	}
	if len(rep.Secrets.Due) != 1 || rep.Secrets.Due[0] != "SMTP_PASSWORD" {
		t.Errorf("due = %v, want [SMTP_PASSWORD]", rep.Secrets.Due)
	}
	if len(rep.Secrets.Refused) != 1 {
		t.Errorf("refused = %v, want the one not-rotatable entry", rep.Secrets.Refused)
	}
	if rep.FailureModes.Rules != len(failure.Library()) {
		t.Errorf("rules = %d, want %d", rep.FailureModes.Rules, len(failure.Library()))
	}
	// The fixture's observation is empty, so every rule is Unknown and every
	// unknown must be named as a gap rather than counted as a pass.
	if len(rep.FailureModes.Unknown) != rep.FailureModes.Rules {
		t.Errorf("unknown = %d, want %d", len(rep.FailureModes.Unknown), rep.FailureModes.Rules)
	}
	if len(rep.FailureModes.Missing) == 0 {
		t.Error("the missing facts are the specification of what the box owes; they must be listed")
	}
}

// A month with no data at all still has to be a page, and the gaps are the
// page. This is the control over the whole package: a report that rendered
// zeros for an empty month would tell a customer their box was perfect.
func TestAnEmptyMonthIsAPageOfGapsAndNotAPageOfZeros(t *testing.T) {
	rep := Assemble(Input{Box: "fresh-box", Period: Period{}, Now: time.Now()})

	got := render(t, rep, false)
	for _, want := range []string{
		"No availability data",
		"No patches were applied",
		"No drill has ever run",
		"No incidents were recorded",
		"No capacity trend",
		"No upstream releases were received",
		"No scan in this report",
		"The failure-mode library was never evaluated",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the rendered page is missing %q, so an empty section read as a fact:\n%s", want, got)
		}
	}
	if len(rep.Gaps) == 0 {
		t.Fatal("gaps is empty, so a consumer counting sections cannot tell which were empty on purpose")
	}
	for _, section := range []string{"uptime", "patches", "backup drill", "incidents", "capacity", "upstream", "scan"} {
		if !strings.Contains(strings.Join(rep.Gaps, "\n"), section) {
			t.Errorf("no gap names %q", section)
		}
	}
	// The one gap a reader must never be told as a zero.
	if strings.Contains(got, "0.00%") {
		t.Errorf("the page printed an uptime for a box nothing measured:\n%s", got)
	}
}

// The two forms over one value is the argument this package exists on, so the
// JSON must be the same month the markdown is: decodable, every section present,
// and the gaps carried rather than rendered away.
func TestTheTwoFormsAreOneMonth(t *testing.T) {
	rep := Assemble(fixture())

	var decoded Report
	if err := json.Unmarshal([]byte(render(t, rep, true)), &decoded); err != nil {
		t.Fatalf("the JSON form is not decodable back into a report: %v", err)
	}
	if decoded.Box != rep.Box || !decoded.GeneratedAt.Equal(rep.GeneratedAt) {
		t.Errorf("the round trip lost the header: %+v", decoded)
	}
	if len(decoded.Gaps) != len(rep.Gaps) {
		t.Errorf("gaps = %d after a round trip, want %d", len(decoded.Gaps), len(rep.Gaps))
	}
	if decoded.Availability.FleetPerMille != rep.Availability.FleetPerMille {
		t.Errorf("uptime = %d after a round trip, want %d", decoded.Availability.FleetPerMille, rep.Availability.FleetPerMille)
	}
	if len(decoded.FailureModes.Unknown) != len(rep.FailureModes.Unknown) {
		t.Errorf("failure unknowns = %d after a round trip, want %d", len(decoded.FailureModes.Unknown), len(rep.FailureModes.Unknown))
	}
	if !strings.Contains(render(t, rep, false), "## Availability") {
		t.Error("the markdown form is missing a section heading")
	}
}

// Assemble is a function of its arguments, so a box with no clock of its own
// must get the same page twice. The Input's Now is a field for exactly this.
func TestAssembleIsAFunctionOfItsArguments(t *testing.T) {
	in := fixture()
	a, b := Assemble(in), Assemble(in)
	if a.Summary() != b.Summary() {
		t.Errorf("two runs disagree:\n%s\n%s", a.Summary(), b.Summary())
	}
	if !a.GeneratedAt.Equal(b.GeneratedAt) {
		t.Error("GeneratedAt moved between two runs over one input")
	}
}

// A capacity series that has not moved is flat, and the band is the argument: a
// box that oscillated half a percent has not filled up.
func TestACapacitySeriesThatHasNotMovedIsFlat(t *testing.T) {
	now := time.Now()
	in := Input{
		Period: Period{},
		Now:    now,
		Capacity: &Capacity{Resource: "cpu", Unit: "%", Points: []CapacityPoint{
			{At: now.Add(-30 * 24 * time.Hour), Value: 20.0},
			{At: now, Value: 20.3},
		}},
	}
	if got := Assemble(in).Capacity.Direction; got != "flat" {
		t.Errorf("direction = %q, want flat", got)
	}

	falling := Assemble(Input{Now: now, Capacity: &Capacity{Resource: "cpu", Unit: "%", Points: []CapacityPoint{
		{At: now.Add(-30 * 24 * time.Hour), Value: 80.0},
		{At: now, Value: 40.0},
	}}})
	if falling.Capacity.Direction != "falling" {
		t.Errorf("direction = %q, want falling", falling.Capacity.Direction)
	}
}

// A service that reports availability and has never been drilled is the gap the
// backup packet exists to close, so the report names it instead of leaving a
// reader to notice it.
func TestAServiceThatWasNeverDrilledIsNamed(t *testing.T) {
	in := fixture()
	in.Drills = nil
	in.Uptime = []ServiceUptime{{Service: "gateway", UptimePerMille: 10000}}

	rep := Assemble(in)
	if len(rep.Backups.NeverDrilled) != 1 || rep.Backups.NeverDrilled[0] != "gateway" {
		t.Fatalf("never drilled = %v, want [gateway]", rep.Backups.NeverDrilled)
	}
	if !strings.Contains(render(t, rep, false), "no drill record at all") {
		t.Error("the page does not name the service that was never drilled")
	}
}

// The boundary is half-open at the end: a drill finishing exactly at End belongs
// to the next month's report, and the alternative counts one drill twice.
func TestADrillOnTheBoundaryBelongsToTheNextMonth(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	rec := backup.DrillRecord{
		Service: "courier", Outcome: backup.DrillPassed, Reason: backup.DrillReasonPassed,
		StartedAt: end.Add(-time.Minute), FinishedAt: end,
	}
	in := Input{Period: Period{Start: start, End: end}, Now: end, Drills: []backup.DrillRecord{rec}}

	rep := Assemble(in)
	if len(rep.Backups.InWindow) != 0 {
		t.Errorf("in window = %d, want 0 — the record finished at the boundary", len(rep.Backups.InWindow))
	}
	// It is still the latest record, because "when was this service last
	// proven restorable" is not scoped to the month.
	if len(rep.Backups.Latest) != 1 {
		t.Errorf("latest = %d, want 1", len(rep.Backups.Latest))
	}
}

// render runs both forms through one helper so a test cannot compare the
// markdown against the JSON by accident.
func render(t *testing.T, rep Report, machine bool) string {
	t.Helper()
	var b strings.Builder
	var err error
	if machine {
		err = Encode(&b, rep)
	} else {
		err = Render(&b, rep)
	}
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return b.String()
}
