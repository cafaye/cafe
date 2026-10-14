package cve

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// Report is the whole answer for one box at one moment.
//
// It is a value rather than a stream because the whole point is that the same
// document is rendered twice — human prose for the operator, JSON for the
// schedule's own state and for OPS-P13's monthly report — and two renderers
// over one value cannot disagree about what was found. A report that was
// written straight to a terminal could not be summarized without re-running
// the scan, and a scan that has to be re-run to be summarized is a scan whose
// numbers drift between the two prints.
type Report struct {
	// Box is the enrolled identity this is about.
	Box string `json:"box"`
	// ScannedAt is when the scan ran.
	ScannedAt time.Time `json:"scanned_at"`
	// Schedule is the cadence the run was under, printed as written.
	Schedule string `json:"schedule,omitempty"`
	// Staleness is the sentence about when this run happened relative to the
	// schedule: "never scanned", "last scanned 2 hours", "next scan in 22
	// hours". It is in the document because a report whose freshness is only
	// knowable from the file's mtime is a report whose freshness nobody
	// checks.
	Staleness string `json:"staleness,omitempty"`
	// Duration is how long the scan took, reported because a scan that takes
	// eleven minutes is not a daily scan and the cadence would otherwise be
	// fiction.
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	// Counts is the shape of the answer in numbers.
	Counts Counts `json:"counts"`
	// Findings are the rows that matter, sorted severity-first.
	Findings []Finding `json:"findings,omitempty"`
	// Divergences are the places the box disagrees with the substrate
	// catalog. They are in the report even when there are no findings,
	// because a box running a version the substrate does not pin is a
	// security fact whether or not an advisory covers it.
	Divergences []Divergence `json:"divergences,omitempty"`
	// Unmatched are components the advisory feed was asked about and answered
	// nothing for. This is not "no advisories" — it is the feed being asked
	// about a component it does not index, and a report that hid the
	// difference would be claiming a clean box it had not checked.
	Unmatched []string `json:"feed_silent,omitempty"`
}

// Clean reports whether there is nothing to act on. It is not "no findings";
// an unreadable version and a divergence from the catalog both mean
// somebody has to look, and a report that said clean in either case would be
// the loudest kind of wrong.
func (r Report) Clean() bool {
	return len(r.Findings) == 0 && len(r.Divergences) == 0 && r.Counts.Unknown == 0
}

// Summary is the one line a person reads first, and the one line a monthly
// report can quote. It names the box, the counts and the fact that the
// advisories were asked about rather than proven absent.
func (r Report) Summary() string {
	box := r.Box
	if box == "" {
		box = "this box"
	}
	line := fmt.Sprintf("%s: %d components checked against %d advisories — %d affected, %d with no fix, %d unreadable",
		box, r.Counts.Components, r.Counts.Advisories, r.Counts.Affected, r.Counts.NoFix, r.Counts.Unknown)
	if r.Clean() {
		line += "; nothing to act on"
	}
	return line
}

// Encode writes the machine document. Indented, for the reason
// internal/cli/box.go gives: a shell can read it and so can a person, and
// `cafe cve scan --json | jq` is a supported path.
func Encode(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Render writes the human report.
//
// The shape is the argument, and it is the shape the brief describes: the
// component, the advisory, the fixed version, and what applying it would
// touch, one block per finding. "You have 14 advisories" is the failure this
// exists to avoid, so the header carries the counts and nothing else, and
// every row below it carries the four facts.
//
// The header is shorter than the body on purpose. A reader who stops after
// the first line still knows what they are looking at; a reader who continues
// gets the part they can act on.
func Render(w io.Writer, r Report) error {
	var b strings.Builder
	b.WriteString(r.Summary())
	b.WriteString("\n")
	if r.Schedule != "" {
		fmt.Fprintf(&b, "  schedule: %s (%s)\n", r.Schedule, r.Staleness)
	}
	if r.DurationSeconds > 0 {
		fmt.Fprintf(&b, "  scan took %.1fs\n", r.DurationSeconds)
	}
	if len(r.Unmatched) > 0 {
		fmt.Fprintf(&b, "  the advisory feed was asked about and did not answer for: %s\n", strings.Join(r.Unmatched, ", "))
	}
	if len(r.Divergences) > 0 {
		b.WriteString("\n")
		b.WriteString("  what the box is running that the substrate does not declare:\n")
		for _, d := range r.Divergences {
			fmt.Fprintf(&b, "    %-16s declared %-14s running %-14s %s\n",
				d.Component, dashIf(d.Declared), dashIf(d.Observed), d.Why)
		}
	}
	if len(r.Findings) == 0 {
		if r.Clean() {
			b.WriteString("\n  nothing to act on.\n")
		} else {
			b.WriteString("\n  no advisories matched, but the notes above are not a clean box.\n")
		}
		_, err := io.WriteString(w, b.String())
		return err
	}
	for _, f := range r.Findings {
		b.WriteString("\n")
		fmt.Fprintf(&b, "  %s  %s  %s\n", f.Severity.up(), f.AdvisoryID, f.Class)
		fmt.Fprintf(&b, "    component: %s %s (%s)\n", f.Component, dashIf(f.Version), f.Kind)
		if f.Summary != "" {
			fmt.Fprintf(&b, "    advisory:  %s\n", f.Summary)
		}
		if f.URL != "" {
			fmt.Fprintf(&b, "    read it:   %s\n", f.URL)
		}
		if f.Class == ClassAffected {
			fmt.Fprintf(&b, "    fix:       release %s\n", f.ReleaseTo)
		}
		if f.ImpactUnknown {
			b.WriteString("    touches:   unknown — this component is not in the substrate catalog, so nobody has said what installing it restarts\n")
		} else {
			fmt.Fprintf(&b, "    touches:   %s\n", blastOf(Component{Name: f.Component, Blast: f.Impact}))
		}
		fmt.Fprintf(&b, "    why:       %s\n", f.Why)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// up is the severity as printed: uppercase, because it is the first word on
// the row and the reader is scanning rather than reading.
func (s Severity) up() string {
	if s == SeverityUnknown {
		return "UNRATED"
	}
	return strings.ToUpper(string(s))
}

func dashIf(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}
