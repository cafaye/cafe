package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cafaye/caf/internal/report"
)

// runReport drives the whole path: a document a test writes, the verb over it,
// and the page out. The deps carry a reader the test owns so the stdin path is
// exercised without a terminal.
func runReport(t *testing.T, deps reportDeps, args ...string) (string, string, error) {
	t.Helper()
	// A scratch state directory, so no test on any machine reads the caf state
	// that machine happens to have. A test that filled its gaps from the
	// developer's own drill history would be green on one laptop and red on
	// the next.
	deps = deps.withDefaults()
	deps.drillDir = filepath.Join(t.TempDir(), "drills")
	deps.secretStateDir = filepath.Join(t.TempDir(), "secrets")
	env := newTestEnv()
	err := newReportCommand(deps).Execute(env, append([]string{"render"}, args...))
	return written(t, env.Stdout), written(t, env.Stderr), err
}

// fixtureDocument is one box's month as the packets would print it, and it is
// written as JSON rather than decoded from testdata so a reader sees the shape
// the report consumes. It carries every section except the two that come from
// local state, which the tests exercise separately.
const fixtureDocument = `{
  "box": "acme-prod",
  "period": {"start": "2026-09-01T00:00:00Z", "end": "2026-10-01T00:00:00Z"},
  "uptime": [
    {"service": "gateway", "uptime_per_mille": 9995, "downtime_ns": 1260000000000, "longest_outage_ns": 840000000000},
    {"service": "courier", "uptime_per_mille": 10000}
  ],
  "patches": [
    {"component": "redis", "from": "7.2.4", "to": "7.2.5", "applied_at": "2026-09-03T00:00:00Z", "reason": "GHSA-1"}
  ],
  "capacity": {"resource": "disk", "unit": "%", "points": [
    {"at": "2026-09-01T00:00:00Z", "value": 41},
    {"at": "2026-09-20T00:00:00Z", "value": 70}
  ]},
  "incidents": [
    {"started_at": "2026-09-04T00:00:00Z", "severity": "sev2", "summary": "courier's mail queue backed up"}
  ],
  "upstream": [
    {"name": "cafaye", "version": "1.14.0", "decision": "applied"},
    {"name": "cafaye", "version": "1.15.0", "decision": "held", "held_reason": "identity's signing key cutover needs a window the customer picks"}
  ],
  "backup_drills": [
    {"service": "courier", "engine": "kamal", "outcome": "passed", "reason": "caf-backup/drill-passed",
     "started_at": "2026-09-11T10:00:00Z", "finished_at": "2026-09-11T10:07:00Z", "duration_ms": 420000},
    {"service": "gateway", "engine": "kamal", "outcome": "failed", "reason": "caf-backup/drill-failed",
     "detail": "restic: unable to create lock", "started_at": "2026-09-21T10:00:00Z",
     "finished_at": "2026-09-21T10:15:00Z", "duration_ms": 900000}
  ],
  "scan": {
    "box": "acme-prod", "scanned_at": "2026-09-30T09:30:00Z", "schedule": "1d",
    "staleness": "next scan in 22 hours", "duration_seconds": 11.2,
    "counts": {"affected": 3, "no_fix": 1, "components": 17, "advisories": 42},
    "findings": [
      {"component": "redis", "version": "7.2.4", "kind": "addon", "severity": "high",
       "advisory": "GHSA-1", "class": "affected", "release_to": "7.2.5", "why": "a restart of redis and gateway"}
    ]
  },
  "failure_modes": {"rules": 10, "findings": []}
}`

// The page a customer reads is the whole point, so the verb is driven end to
// end against a document carrying every section, and each section is looked for
// in the output. A section missing from the page is a section nobody reads.
func TestReportRenderWritesTheWholePage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.json")
	writeFile(t, path, fixtureDocument)

	out, _, err := runReport(t, reportDeps{stdin: strings.NewReader("")}, path)
	if err != nil {
		t.Fatalf("caf report render: %v", err)
	}

	for _, want := range []string{
		"# acme-prod",
		"## Availability",
		"99.95%",
		"## Patches applied",
		"7.2.4 → 7.2.5",
		"## Backup drill",
		"**1 of 2 drill(s) passed",
		"## Incidents",
		"courier's mail queue backed up",
		"## Capacity",
		"disk: rising",
		"## Upstream releases",
		"**held** cafaye 1.15.0",
		"identity's signing key cutover needs a window the customer picks",
		"## Security scan",
		"17 components checked against 42 advisories",
		"## Failure modes",
		"## What this report could not say",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the page is missing %q:\n%s", want, out)
		}
	}
	// The two drills are both inside the window the document carries, so the
	// verdict line counts them. A drill outside the window is the boundary
	// rule, and it is asserted in the package's own tests.
	if !strings.Contains(out, "**1 of 2 drill(s) passed — 1 failed, 0 refused.**") {
		t.Errorf("the verdict over the window is not on the page:\n%s", out)
	}
}

// The JSON form is one document on stdout — the contract internal/agent holds —
// and it is decodable back into a report, because the box agent and the website
// consume it without a parser guessing where the words end.
func TestReportRenderWritesOneJSONDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.json")
	writeFile(t, path, fixtureDocument)

	out, _, err := runReport(t, reportDeps{stdin: strings.NewReader("")}, "--json", path)
	if err != nil {
		t.Fatalf("caf report render --json: %v", err)
	}

	dec := json.NewDecoder(strings.NewReader(out))
	var rep report.Report
	if err := dec.Decode(&rep); err != nil {
		t.Fatalf("stdout is not one decodable JSON document: %v\n%s", err, out)
	}
	if rep.Box != "acme-prod" {
		t.Errorf("box = %q, want acme-prod", rep.Box)
	}
	if len(rep.Availability.Services) != 2 {
		t.Errorf("services = %d, want 2", len(rep.Availability.Services))
	}
	if len(rep.Upstream) != 2 || rep.Upstream[1].Decision != report.DecisionHeld {
		t.Errorf("upstream = %+v, want the applied release and the held one", rep.Upstream)
	}
	if !dec.More() {
		t.Error("the stream holds more than one document; a script cannot pipe it")
	}
}

// The gap count goes to stderr as a sentence, so a script reading stdout still
// gets one clean document and a person still learns the page was honest about
// what it did not know. This is the assertion that keeps those two promises
// from drifting into each other.
func TestReportRenderSaysNothingFalseOnStdoutAboutItsGaps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.json")
	writeFile(t, path, fixtureDocument)

	stdout, stderr, err := runReport(t, reportDeps{stdin: strings.NewReader("")}, "--json", path)
	if err != nil {
		t.Fatalf("caf report render --json: %v", err)
	}
	if !strings.Contains(stderr, "section(s) could not be filled") {
		t.Errorf("stderr does not report the gaps it named in the JSON:\n%s", stderr)
	}
	// And the JSON carries the gaps as data, which is the only way a consumer
	// counting sections can tell which ones were empty on purpose.
	if !strings.Contains(stdout, "\"gaps\"") {
		t.Errorf("the JSON has no gaps field, so a zero would be indistinguishable from a fact:\n%s", stdout)
	}
}

// A month with no document at all is still a page: the state directories this
// machine has (none, in a temp dir with no CAF state) supply nothing, and every
// section is a gap. The page must say so rather than printing zeros — the
// control that makes this command honest on a box nobody has measured.
func TestReportRenderWithNoDocumentIsAPageOfGaps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	writeFile(t, path, "{}")

	out, _, err := runReport(t, reportDeps{stdin: strings.NewReader("")}, "--period", "2026-09", path)
	if err != nil {
		t.Fatalf("caf report render over an empty document: %v", err)
	}
	for _, want := range []string{
		"No availability data",
		"No patches were applied",
		"No drill has ever run",
		"No capacity trend",
		"No upstream releases were received",
		"No scan in this report",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("an empty section read as a fact, missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "2026-09-01 to 2026-10-01") {
		t.Errorf("the window from --period 2026-09 is not on the page:\n%s", out)
	}
}

// The drill records are the one packet whose output lives on this machine, so
// the verb fills that section from the local store rather than leaving it a gap.
// The service name is the filename, and a journal per service is flattened into
// one list for the page.
func TestReportRenderFillsTheDrillSectionFromTheLocalStore(t *testing.T) {
	dir := t.TempDir()
	drills := filepath.Join(dir, "drills")
	if err := os.MkdirAll(drills, 0o700); err != nil {
		t.Fatal(err)
	}
	defer func() { t.Setenv("CAF_DRILL_DIR", drills) }()
	writeFile(t, filepath.Join(drills, "courier.json"), `{"service":"courier","records":[
		{"service":"courier","engine":"kamal","outcome":"passed","reason":"caf-backup/drill-passed",
		 "started_at":"2026-09-11T10:00:00Z","finished_at":"2026-09-11T10:07:00Z","duration_ms":420000}
	]}`)

	input := filepath.Join(dir, "input.json")
	writeFile(t, input, fixtureDocument)

	env := newTestEnv()
	err := newReportCommand(reportDeps{
		stdin:          strings.NewReader(""),
		drillDir:       drills,
		secretStateDir: filepath.Join(t.TempDir(), "secrets"),
	}).Execute(env, append([]string{"render"}, input))
	if err != nil {
		t.Fatalf("caf report render: %v", err)
	}
	out := written(t, env.Stdout)
	if !strings.Contains(out, "courier") {
		t.Errorf("the local drill journal is not on the page:\n%s", out)
	}
	// Three records: two from the document and one from the store. A store read
	// that dropped the document's own would be a report that disagreed with the
	// document the caller handed it.
	if !strings.Contains(out, "3") {
		t.Errorf("the page does not carry the combined drill count:\n%s", out)
	}
}

// The period flag has four spellings, and the parse is a table because each one
// has an off-by-one waiting at its edge: a month range is end-exclusive, and a
// duration is measured from now rather than from midnight.
func TestReportPeriodParsing(t *testing.T) {
	for _, tt := range []struct {
		text  string
		start string
		end   string
	}{
		{"2026-09", "2026-09-01", "2026-10-01"},
		{"2026-09-01..2026-09-30", "2026-09-01", "2026-09-30"},
		{"30d", "", ""},
		{"month", "", ""},
	} {
		t.Run(tt.text, func(t *testing.T) {
			p, err := parseReportPeriod(tt.text)
			if err != nil {
				t.Fatalf("parse %q: %v", tt.text, err)
			}
			if tt.start != "" && !p.Start.Equal(mustDay(t, tt.start)) {
				t.Errorf("start = %s, want %s", p.Start, tt.start)
			}
			if tt.end != "" && !p.End.Equal(mustDay(t, tt.end)) {
				t.Errorf("end = %s, want %s", p.End, tt.end)
			}
			// Every spelling ends after it starts, which is the only invariant
			// that holds across the four.
			if !p.End.After(p.Start) {
				t.Errorf("%q produced a window %v that ends before it starts", tt.text, p)
			}
		})
	}

	if _, err := parseReportPeriod("2026-13"); err == nil {
		t.Error("a month nobody has must be refused with its own sentence")
	}
	if _, err := parseReportPeriod("last tuesday"); err == nil {
		t.Error("nonsense must be refused rather than silently defaulting")
	}
}

// The verb is the only one of its group, so bare `caf report` is a question
// ("what can this do?") and answers with its help, and an unknown verb is a
// usage error — the same two calls `caf failure` makes.
func TestReportGroupsItsVerb(t *testing.T) {
	env := newTestEnv()
	if err := newReportCommand(reportDeps{}).Execute(env, nil); err != nil {
		t.Fatalf("bare `caf report` must answer, not fail: %v", err)
	}
	if out := written(t, env.Stdout); !strings.Contains(out, "caf report render") {
		t.Errorf("the help does not show the verb:\n%s", out)
	}

	env = newTestEnv()
	err := newReportCommand(reportDeps{}).Execute(env, []string{"graph"})
	if err == nil {
		t.Fatal("an unknown verb must be a usage error")
	}
	if !strings.Contains(err.Error(), "unknown caf report verb") {
		t.Errorf("err = %v, want it to name the verb the user typed", err)
	}
}

func mustDay(t *testing.T, day string) time.Time {
	t.Helper()
	at, err := time.Parse("2006-01-02", day)
	if err != nil {
		t.Fatalf("parse %q: %v", day, err)
	}
	return at
}
