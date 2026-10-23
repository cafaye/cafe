package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/anywaye/cafe/internal/cve"
	"github.com/anywaye/cafe/internal/secret"
)

// Render writes the human page: markdown, because the three places this goes —
// a customer's inbox, a GitHub-ish runbook, the website's screen — all read it
// and none of them parse it.
//
// One page, and the shape is the argument. Every section is a heading, one
// sentence of answer, and the rows that back the sentence up; a reader who
// stops after the headings still knows what happened last month. What is NOT
// here is a wall of prose — the same argument internal/lock's verify made about
// forty copies of one remedy under forty filenames.
func Render(w io.Writer, r Report) error {
	var b strings.Builder

	_, _ = fmt.Fprintf(&b, "# %s — %s\n\n", dashIf(r.Box), r.Period)
	_, _ = fmt.Fprintf(&b, "%s\n\n", r.Summary())

	renderAvailability(&b, r)
	renderPatches(&b, r)
	renderBackups(&b, r)
	renderIncidents(&b, r)
	renderCapacity(&b, r)
	renderUpstream(&b, r)
	renderSecurity(&b, r)
	renderSecrets(&b, r)
	renderModes(&b, r)
	renderGaps(&b, r)

	_, err := io.WriteString(w, b.String())
	return err
}

func renderAvailability(b *strings.Builder, r Report) {
	b.WriteString("## Availability\n\n")
	if len(r.Availability.Services) == 0 {
		b.WriteString("No availability data for this window.\n\n")
		return
	}
	if r.Availability.FleetPerMille > 0 {
		_, _ = fmt.Fprintf(b, "Fleet uptime **%.2f%%** across %d service(s).\n\n",
			float64(r.Availability.FleetPerMille)/100, len(r.Availability.Services))
	}
	b.WriteString("| service | uptime | downtime | longest outage |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, u := range r.Availability.Services {
		_, _ = fmt.Fprintf(b, "| %s | %.2f%% | %s | %s |\n",
			u.Service, float64(u.UptimePerMille)/100,
			humanDuration(u.Downtime), humanDuration(u.LongestOutage))
	}
	b.WriteString("\n")
}

func renderPatches(b *strings.Builder, r Report) {
	b.WriteString("## Patches applied\n\n")
	if len(r.PatchesApplied) == 0 {
		b.WriteString("No patches were applied in this window.\n\n")
		return
	}
	for _, p := range r.PatchesApplied {
		_, _ = fmt.Fprintf(b, "- **%s**: %s\n", p.Component, arrowIf(p.From, p.To))
		rest := []string{p.AppliedAt.Local().Format("2006-01-02")}
		if p.Kind != "" {
			rest = append(rest, p.Kind)
		}
		if p.Reason != "" {
			rest = append(rest, p.Reason)
		}
		_, _ = fmt.Fprintf(b, "  - %s\n", strings.Join(rest, " · "))
		if len(p.Restarted) > 0 {
			_, _ = fmt.Fprintf(b, "  - restarts: %s\n", strings.Join(p.Restarted, ", "))
		}
	}
	b.WriteString("\n")
}

// renderBackups is the section the whole subscription turns on, so it leads
// with the verdict and puts the age under it: a passing drill from before the
// last schema migration is a passing drill about a database that no longer
// exists (backup.DrillRecord.Age).
func renderBackups(b *strings.Builder, r Report) {
	bk := r.Backups
	b.WriteString("## Backup drill\n\n")
	if len(bk.Latest) == 0 {
		b.WriteString("**No drill has ever run on this box.** A backup that has never been drilled is not a backup.\n")
		renderNeverDrilled(b, bk)
		b.WriteString("\n")
		return
	}
	if len(bk.InWindow) == 0 {
		b.WriteString("**No drill ran in this window.** The most recent one is below.\n\n")
	} else if bk.Failed+bk.Refused == 0 {
		_, _ = fmt.Fprintf(b, "**%d drill(s) ran and every one passed.**\n\n", len(bk.InWindow))
	} else {
		_, _ = fmt.Fprintf(b, "**%d of %d drill(s) passed — %d failed, %d refused.**\n\n",
			bk.Passed, len(bk.InWindow), bk.Failed, bk.Refused)
	}
	for _, d := range bk.Latest {
		_, _ = fmt.Fprintf(b, "- **%s** (%s): %s %s ago — %s\n",
			d.Service, d.Engine, d.Outcome, humanDuration(r.GeneratedAt.Sub(d.FinishedAt)),
			dashIf(string(d.Reason)))
		if d.Detail != "" {
			_, _ = fmt.Fprintf(b, "  - %s\n", d.Detail)
		}
		if d.DurationMS > 0 {
			_, _ = fmt.Fprintf(b, "  - took %s\n", humanDuration(time.Duration(d.DurationMS)*time.Millisecond))
		}
	}
	renderNeverDrilled(b, bk)
	b.WriteString("\n")
}

// renderNeverDrilled names the services that report availability and have no
// drill record at all. It is a separate function so the two branches above
// cannot accidentally drop it — the one-line early return is exactly where a
// call like this gets lost.
func renderNeverDrilled(b *strings.Builder, bk BackupSection) {
	if len(bk.NeverDrilled) == 0 {
		return
	}
	_, _ = fmt.Fprintf(b, "\nServices reporting availability with no drill record at all: %s\n",
		strings.Join(bk.NeverDrilled, ", "))
}

func renderIncidents(b *strings.Builder, r Report) {
	b.WriteString("## Incidents\n\n")
	if len(r.Incidents) == 0 {
		b.WriteString("No incidents were recorded in this window.\n\n")
		return
	}
	for _, inc := range r.Incidents {
		state := "resolved"
		if inc.Ongoing {
			state = "**ONGOING**"
		}
		head := inc.Summary
		if inc.Severity != "" {
			head = inc.Severity + " — " + head
		}
		_, _ = fmt.Fprintf(b, "- %s (%s)\n", head, state)
		_, _ = fmt.Fprintf(b, "  - %s\n", inc.StartedAt.Local().Format("2006-01-02 15:04"))
		if inc.Resolution != "" {
			_, _ = fmt.Fprintf(b, "  - closed by: %s\n", inc.Resolution)
		}
	}
	b.WriteString("\n")
}

func renderCapacity(b *strings.Builder, r Report) {
	b.WriteString("## Capacity\n\n")
	if r.Capacity == nil {
		b.WriteString("No capacity trend for this window.\n\n")
		return
	}
	c := r.Capacity
	_, _ = fmt.Fprintf(b, "%s: %s from %.1f%s to %.1f%s (%s%.1f%s) between %s and %s.\n\n",
		c.Resource, c.Direction, c.First, c.Unit, c.Last, c.Unit,
		sign(c.Change), abs(c.Change), c.Unit,
		c.FirstAt.Local().Format("2006-01-02"), c.LastAt.Local().Format("2006-01-02"))
	if c.Detail != "" {
		_, _ = fmt.Fprintf(b, "%s\n\n", c.Detail)
	}
}

func renderUpstream(b *strings.Builder, r Report) {
	b.WriteString("## Upstream releases\n\n")
	if len(r.Upstream) == 0 {
		b.WriteString("No upstream releases were received in this window.\n\n")
		return
	}
	applied, held := 0, 0
	for _, rel := range r.Upstream {
		if rel.Decision == DecisionHeld {
			held++
			_, _ = fmt.Fprintf(b, "- **held** %s %s — %s\n", rel.Name, rel.Version, rel.HeldReason)
			continue
		}
		applied++
		_, _ = fmt.Fprintf(b, "- **applied** %s %s\n", rel.Name, rel.Version)
	}
	_, _ = fmt.Fprintf(b, "\n%d applied, %d held. A held release is a decision staying the customer's: it is recorded with its reason rather than applied and written up afterwards.\n\n", applied, held)
}

// renderSecurity forwards cve's own summary sentence rather than writing a
// second one over the same facts, and then prints the rows that matter. The
// report is not a second scan: it is the scan's verdict, quoted.
func renderSecurity(b *strings.Builder, r Report) {
	scan := r.Security
	b.WriteString("## Security scan\n\n")
	if scan.ScannedAt.IsZero() {
		b.WriteString("No scan in this report.\n\n")
		return
	}
	_, _ = fmt.Fprintf(b, "%s\n\n", scan.Summary())
	if scan.Schedule != "" {
		_, _ = fmt.Fprintf(b, "- schedule: %s (%s)\n", scan.Schedule, scan.Staleness)
	}
	if len(scan.Divergences) > 0 {
		_, _ = fmt.Fprintf(b, "- %d component(s) running a version the substrate does not declare\n", len(scan.Divergences))
	}
	if len(scan.Unmatched) > 0 {
		_, _ = fmt.Fprintf(b, "- the advisory feed was asked about and did not answer for: %s\n", strings.Join(scan.Unmatched, ", "))
	}
	if len(scan.Findings) > 0 {
		b.WriteString("\n")
		for _, f := range scan.Findings {
			_, _ = fmt.Fprintf(b, "- %s %s — %s %s\n", f.Severity, f.AdvisoryID, f.Component, dashIf(f.Version))
			if f.Summary != "" {
				_, _ = fmt.Fprintf(b, "  - %s\n", f.Summary)
			}
			if f.Class == cve.ClassAffected {
				_, _ = fmt.Fprintf(b, "  - fix: release %s\n", f.ReleaseTo)
			}
			_, _ = fmt.Fprintf(b, "  - %s\n", f.Why)
		}
	}
	b.WriteString("\n")
}

func renderSecrets(b *strings.Builder, r Report) {
	s := r.Secrets
	b.WriteString("## Secrets\n\n")
	if len(s.Counts) == 0 {
		b.WriteString("No secret inventory in this report.\n\n")
		return
	}
	parts := make([]string, 0, len(s.Counts))
	for _, class := range []secret.Class{secret.Rotatable, secret.Conditional, secret.NotRotatable} {
		if n := s.Counts[class]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, class))
		}
	}
	_, _ = fmt.Fprintf(b, "%d secret(s) on this box: %s.\n\n", total(s.Counts), strings.Join(parts, ", "))
	if len(s.RotatedInWindow) > 0 {
		_, _ = fmt.Fprintf(b, "Rotated this window: %s.\n\n", strings.Join(s.RotatedInWindow, ", "))
	}
	if len(s.Due) > 0 {
		_, _ = fmt.Fprintf(b, "Due for rotation: %s.\n\n", strings.Join(s.Due, ", "))
	}
	if len(s.Refused) > 0 {
		b.WriteString("Not rotatable on this box, and why — a rotation forks or cuts over:\n\n")
		for _, refused := range s.Refused {
			_, _ = fmt.Fprintf(b, "- %s\n", refused)
		}
		b.WriteString("\n")
	}
}

// renderModes prints the failure-mode library's answer in its own order. The
// unknowns are printed rather than dropped, and that is the section's whole
// argument: a rule that could not be evaluated is not a rule that passed.
func renderModes(b *strings.Builder, r Report) {
	m := r.FailureModes
	b.WriteString("## Failure modes\n\n")
	if m.Rules == 0 {
		b.WriteString("The failure-mode library was never evaluated against this report.\n\n")
		return
	}
	_, _ = fmt.Fprintf(b, "%d rule(s) evaluated: %d identified, %d ruled out, %d could not be answered.\n\n",
		m.Rules, len(m.Holds), m.DoesNotHold, len(m.Unknown))
	if len(m.Holds) > 0 {
		b.WriteString("Conditions identified on this box:\n\n")
		for _, f := range m.Holds {
			_, _ = fmt.Fprintf(b, "- **%s** — %s\n", f.ID, f.Failure)
			if f.Remedy != "" {
				_, _ = fmt.Fprintf(b, "  - remedy: %s\n", f.Remedy)
			}
			if f.Contract != "" {
				_, _ = fmt.Fprintf(b, "  - contract: %s\n", f.Contract)
			}
			for _, matched := range f.Matched {
				_, _ = fmt.Fprintf(b, "  - observed: %s\n", matched)
			}
		}
		b.WriteString("\n")
	}
	if len(m.Unknown) > 0 {
		b.WriteString("The observation could not answer these, and they are not passes:\n\n")
		for _, f := range m.Unknown {
			_, _ = fmt.Fprintf(b, "- %s — needs %s\n", f.ID, f.Missing)
		}
		b.WriteString("\n")
	}
}

// renderGaps is the section that keeps the rest of them honest. It is last
// because it is the part a reader acts on when the page above is quiet, and it
// is present even when it is empty — a report that omitted it when there was
// nothing wrong would be indistinguishable from one that had not looked.
func renderGaps(b *strings.Builder, r Report) {
	b.WriteString("## What this report could not say\n\n")
	if len(r.Gaps) == 0 {
		b.WriteString("Nothing. Every section above was filled from a source.\n\n")
		return
	}
	for _, g := range r.Gaps {
		_, _ = fmt.Fprintf(b, "- %s\n", g)
	}
	b.WriteString("\n")
}

// Encode writes the machine form: one indented JSON document on stdout, the
// contract internal/agent and internal/cve both hold — a shell can read it and
// so can a person.
func Encode(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// humanDuration renders a duration the way an operator reads one. It stops at
// days rather than months because a monthly report's own window bounds the
// top end, and "35d" for a drill that has never run is a lie about a number.
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 48*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// arrowIf is the before → after of a patch, and it says which of the two is
// missing rather than printing a bare arrow. A patch with no recorded "to" is a
// patch whose result nobody wrote down.
func arrowIf(from, to string) string {
	if from == "" && to == "" {
		return "no version recorded"
	}
	if from == "" {
		return "→ " + to
	}
	if to == "" {
		return from + " → (no version recorded)"
	}
	return from + " → " + to
}

func sign(v float64) string {
	if v > 0 {
		return "+"
	}
	return ""
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// total is the inventory's size, which is the number of entries and not the
// number of classes — "3 secrets" is a fact and "2 classes" is not one.
func total(counts map[secret.Class]int) int {
	n := 0
	for _, c := range counts {
		n += c
	}
	return n
}
