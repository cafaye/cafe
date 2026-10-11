package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cafaye/cafe/internal/backup"
	"github.com/cafaye/cafe/internal/cve"
	"github.com/cafaye/cafe/internal/failure"
	"github.com/cafaye/cafe/internal/secret"
)

// Report is the one page: a value assembled once and rendered twice.
//
// The shape follows cve.Report's argument — a value, not a stream, because the
// whole point is that two renderers over it cannot disagree — and adds the one
// field a monthly report needs that a scan does not: Gaps. A section whose
// source supplied nothing is named there, and the section renders as the gap it
// is. A page that quietly rendered a zero for a section with no data would be a
// page that told a customer their uptime was perfect because nobody had
// measured it.
type Report struct {
	Box         string    `json:"box"`
	Period      Period    `json:"period"`
	GeneratedAt time.Time `json:"generated_at"`

	Availability Availability `json:"availability"`
	// PatchesApplied is what was fixed in the window, oldest first — the order
	// a reader follows a month in.
	PatchesApplied []Patch        `json:"patches_applied,omitempty"`
	Backups        BackupSection  `json:"backups"`
	Incidents      []Incident     `json:"incidents,omitempty"`
	Capacity       *CapacityTrend `json:"capacity,omitempty"`
	Upstream       []Release      `json:"upstream,omitempty"`

	Security     cve.Report    `json:"security"`
	Secrets      SecretSection `json:"secrets"`
	FailureModes ModeSection   `json:"failure_modes"`

	// Gaps names every section this report could not fill, with the fact that
	// is missing. It is a first-class field rather than a rendering decision so
	// the machine form carries it too: a consumer that counts sections has to
	// be able to see which ones were empty on purpose.
	Gaps []string `json:"gaps,omitempty"`
}

// Availability is the window's uptime, per service and across the fleet.
type Availability struct {
	// Services is in the input's order, which is the order the observation
	// surface names them in and not an invented one.
	Services []ServiceUptime `json:"services,omitempty"`
	// FleetPerMille is the mean across the services reported. It is a mean and
	// not a weighted one because the only weighting that would be honest —
	// traffic — is a fact this report does not have.
	FleetPerMille int `json:"fleet_per_mille"`
	// Worst is the service that was down longest, which is the sentence the
	// header carries when there is one.
	Worst string `json:"worst,omitempty"`
}

// BackupSection is the drill result for the window and for the service as it
// stands.
//
// Two lists and not one, because they answer different questions. InWindow is
// "what did this month prove". Latest is "when was this service last proven
// restorable", which is the question a passing drill from last March answers
// badly — see backup.DrillRecord.Age, written for exactly this.
type BackupSection struct {
	// InWindow is every record whose finish fell inside the period, ordered by
	// service then time so a multi-service month reads as one table.
	InWindow []backup.DrillRecord `json:"in_window,omitempty"`
	// Latest is the most recent record per service across the whole history,
	// including services not drilled in the window.
	Latest []backup.DrillRecord `json:"latest,omitempty"`
	// Passed, Failed and Refused partition InWindow. Refused is separate
	// because backup.DrillOutcome says a refusal and a failure are different
	// facts with different fixes: one is the operator's file to edit.
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Refused int `json:"refused"`
	// NeverDrilled is every service the availability section names that has no
	// drill record at all. It exists because "the service was up" and "the
	// service's backup restores" are different claims, and a report that
	// printed only the first would be the failure this packet exists to close.
	NeverDrilled []string `json:"never_drilled,omitempty"`
}

// CapacityTrend is the window's shape: where the resource started, where it
// finished and which way it is going.
//
// The points stay in the document — the website's screen draws them — but the
// direction is computed here rather than left to each renderer, so the markdown
// and the JSON cannot disagree about whether the box is filling up.
type CapacityTrend struct {
	Resource string  `json:"resource"`
	Unit     string  `json:"unit"`
	First    float64 `json:"first"`
	Last     float64 `json:"last"`
	// Change is Last minus First, in the unit.
	Change float64 `json:"change"`
	// Direction is "rising", "falling" or "flat". Flat is a band rather than
	// an equality because a resource that moved 0.4% is a resource that did not
	// move, and saying so is the honest answer.
	Direction string `json:"direction"`
	// FirstAt and LastAt bound the series so a reader can see whether the
	// trend is over a month or over a week.
	FirstAt time.Time `json:"first_at"`
	LastAt  time.Time `json:"last_at"`
	// Detail carries the newest point's own note, which is where a driving
	// fact lives ("the muse image cache is 60% of it").
	Detail string `json:"detail,omitempty"`
}

// ModeSection is the failure-mode library's answer, projected.
//
// The projection is deliberate. failure.Rule carries no json tags, so a
// document round-trip of a failure.Report spells its keys in Go's own casing
// ("ID", "Failure") — that is a fact OPS-P12 owes this packet and it is named
// in doc.go. Rather than forward a struct whose wire shape is an accident, this
// report carries the four fields it prints, in this repository's own spelling.
type ModeSection struct {
	// Rules is how many entries were evaluated. It is in the document because
	// a report over zero of them is not a clean bill of health.
	Rules int `json:"rules"`
	// Holds is the conditions identified on this box, in library order.
	Holds []ModeFinding `json:"holds,omitempty"`
	// Unknown is what the observation could not answer, in library order.
	Unknown []ModeFinding `json:"unknown,omitempty"`
	// DoesNotHold is how many conditions were evaluated and ruled out.
	DoesNotHold int `json:"does_not_hold"`
	// Missing is the deduplicated set of facts the library needed, which is
	// the specification of what the box's observation surface owes this packet.
	Missing []string `json:"missing,omitempty"`
}

// ModeFinding is one rule's answer in the words the report prints.
type ModeFinding struct {
	ID       string   `json:"id"`
	Failure  string   `json:"failure"`
	Remedy   string   `json:"remedy,omitempty"`
	Contract string   `json:"contract,omitempty"`
	Matched  []string `json:"matched,omitempty"`
	// Missing is the fact an Unknown needed, from the finding of the same name.
	Missing string `json:"missing,omitempty"`
}

// SecretSection is the inventory's shape in numbers, plus the two lists a
// customer acts on: what is due, and what cannot be rotated at all.
type SecretSection struct {
	Counts map[secret.Class]int `json:"counts,omitempty"`
	// Due is the rotatable entries past their interval on this box. It comes
	// from secret.Due, which is why the report cannot disagree with
	// `caf secret inventory` about what is due.
	Due []string `json:"due,omitempty"`
	// Refused is the not-rotatable entries with the class and the reason, read
	// out of the rotation state — the sentence that says "this is still not
	// rotatable" rather than repeating the discovery.
	Refused []string `json:"refused,omitempty"`
	// RotatedInWindow is the entries rotated inside the period, with the date.
	RotatedInWindow []string `json:"rotated_in_window,omitempty"`
}

// Assemble is the whole thing: an Input in, one page out. It is a pure function
// — no clock beyond the one the caller hands in, no file, no socket — which is
// why the assembly logic below can be a table in a test rather than an
// explanation in prose.
func Assemble(in Input) Report {
	rep := Report{
		Box:         in.Box,
		Period:      in.Period,
		GeneratedAt: in.Now,
	}

	rep.Availability = assembleAvailability(in)
	rep.PatchesApplied = patchesInPeriod(in)
	rep.Backups = assembleBackups(in)
	rep.Incidents = incidentsInPeriod(in)
	rep.Capacity = assembleCapacity(in.Capacity)
	rep.Upstream = in.Upstream
	rep.Security = in.Scan
	rep.Secrets = assembleSecrets(in)
	rep.FailureModes = assembleModes(in.Modes)
	rep.Gaps = gaps(rep, in)
	return rep
}

func assembleAvailability(in Input) Availability {
	out := Availability{Services: in.Uptime}
	if len(in.Uptime) == 0 {
		return out
	}
	total := 0
	var worst string
	var worstDown time.Duration
	for _, u := range in.Uptime {
		total += u.UptimePerMille
		if u.Downtime > worstDown {
			worst, worstDown = u.Service, u.Downtime
		}
	}
	out.FleetPerMille = total / len(in.Uptime)
	out.Worst = worst
	return out
}

// patchesInPeriod is the window's fixes, oldest first. It is a filter rather
// than a sort key on the render site because two renderers over one value is
// the whole argument this package makes.
func patchesInPeriod(in Input) []Patch {
	var out []Patch
	for _, p := range in.Patches {
		if in.Period.Contains(p.AppliedAt) {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].AppliedAt.Before(out[j].AppliedAt) })
	return out
}

// incidentsInPeriod normalizes and orders the window's incidents. Normalizing
// once here means the count of ongoing incidents and the render of an ongoing
// incident cannot disagree.
func incidentsInPeriod(in Input) []Incident {
	var out []Incident
	for _, inc := range in.Incidents {
		if !in.Period.Contains(inc.StartedAt) {
			continue
		}
		inc.Normalize()
		out = append(out, inc)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

func assembleBackups(in Input) BackupSection {
	section := BackupSection{InWindow: in.drillsInPeriod()}
	for _, d := range section.InWindow {
		switch d.Outcome {
		case backup.DrillPassed:
			section.Passed++
		case backup.DrillRefused:
			section.Refused++
		default:
			// Failed and anything caf could not classify. A record whose
			// outcome is neither of the three is a record that did not prove
			// a restore, and counting it as passed would be the quietest kind
			// of wrong — the same reason backup.DrillReasonNotRun exists.
			section.Failed++
		}
	}

	// Latest per service, over the whole history and not the window: the
	// question is when the service was last proven restorable, and the answer
	// is not scoped to the month a reader happens to be holding.
	latest := map[string]backup.DrillRecord{}
	for _, d := range in.Drills {
		cur, ok := latest[d.Service]
		if !ok || d.FinishedAt.After(cur.FinishedAt) {
			latest[d.Service] = d
		}
	}
	for _, d := range latest {
		section.Latest = append(section.Latest, d)
	}
	sort.SliceStable(section.Latest, func(i, j int) bool { return section.Latest[i].Service < section.Latest[j].Service })

	// A service the availability section names with no drill record anywhere
	// is the gap the whole backup packet exists to close, so it is named here
	// rather than left to a reader to notice.
	for _, u := range in.Uptime {
		if _, ok := latest[u.Service]; !ok {
			section.NeverDrilled = append(section.NeverDrilled, u.Service)
		}
	}
	return section
}

// assembleCapacity turns a series into a direction, and the band it calls flat
// is the argument: a resource that moved a fraction of a percent moved nothing,
// and "rising" would send a reader to look at a graph that is a straight line.
func assembleCapacity(c *Capacity) *CapacityTrend {
	if c == nil || len(c.Points) < 2 {
		return nil
	}
	first, last := c.Points[0], c.Points[len(c.Points)-1]
	change := last.Value - first.Value
	direction := "flat"
	// One percent of the reading's own scale. A resource that moved less than
	// that did not move, and saying "rising" over it would send a reader to a
	// graph that is a straight line.
	band := (first.Value + last.Value) / 2 * 0.01
	if change > band || change < -band {
		if change > 0 {
			direction = "rising"
		} else {
			direction = "falling"
		}
	}
	return &CapacityTrend{
		Resource:  c.Resource,
		Unit:      c.Unit,
		First:     first.Value,
		Last:      last.Value,
		Change:    change,
		Direction: direction,
		FirstAt:   first.At,
		LastAt:    last.At,
		Detail:    last.Detail,
	}
}

// dueEntries is the schedule half of the report: which of the inventory's
// rotatable class is past its interval on this box.
//
// It is the rule secret.Due (rotate.go:406) states, applied to the inventory the
// caller handed in rather than to secret.Inventory(), because this report is a
// function of its arguments and a caller supplies the inventory as data. A
// second copy of a rule is a second thing that can drift, so
// TestTheDueRuleIsTheSecretPacketsOwn pins it against the real inventory, entry
// for entry — that is the control, and it is why the copy is here at all.
func dueEntries(entries []secret.Entry, state secret.State, now time.Time) []string {
	var out []string
	for _, e := range entries {
		if e.Class != secret.Rotatable || e.Interval <= 0 {
			continue
		}
		last, ok := state.Last[e.StateKey()]
		if !ok || now.Sub(last) >= e.Interval {
			out = append(out, e.Name)
		}
	}
	return out
}

// assembleSecrets reads the inventory through the schedule state, which is the
// only way "due" is a fact about this box and not about the fleet.
func assembleSecrets(in Input) SecretSection {
	section := SecretSection{Counts: map[secret.Class]int{}}
	if len(in.Inventory) > 0 {
		for _, e := range in.Inventory {
			section.Counts[e.Class]++
			if at, ok := in.Rotations.Last[e.StateKey()]; ok && in.Period.Contains(at) {
				section.RotatedInWindow = append(section.RotatedInWindow,
					fmt.Sprintf("%s — %s", e.Name, at.UTC().Format("2006-01-02")))
			}
		}
		section.Due = dueEntries(in.Inventory, in.Rotations, in.Now)
	}
	for key, refused := range in.Rotations.Refused {
		section.Refused = append(section.Refused, fmt.Sprintf("%s — %s", key, refused))
	}
	sort.Strings(section.Refused)
	return section
}

func assembleModes(modes failure.Report) ModeSection {
	section := ModeSection{Rules: modes.Rules}
	for _, f := range modes.Holds() {
		section.Holds = append(section.Holds, ModeFinding{
			ID:       f.Rule.ID,
			Failure:  f.Rule.Failure,
			Remedy:   f.Rule.Remedy,
			Contract: f.Rule.Contract,
			Matched:  f.Matched,
		})
	}
	for _, f := range modes.Unknown() {
		section.Unknown = append(section.Unknown, ModeFinding{
			ID:      f.Rule.ID,
			Failure: f.Rule.Failure,
			Missing: f.Missing,
		})
	}
	section.Missing = modes.SortedMissing()
	section.DoesNotHold = modes.Rules - len(section.Holds) - len(section.Unknown)
	return section
}

// gaps names every section that could not be filled, and the fact that is
// missing. It walks the assembled report rather than the input so the two
// cannot drift: a section that a caller filled with an empty list is a gap, and
// the way to find that out is to look at what was assembled.
func gaps(rep Report, in Input) []string {
	var out []string
	if len(rep.Availability.Services) == 0 {
		out = append(out, "uptime: no availability data — owed by the box's observation surface (internal/boxd's World carries service names and no probe answers)")
	}
	if len(rep.PatchesApplied) == 0 {
		out = append(out, "patches applied: no patch records in the window — owed by the packet that applies a fix; internal/cve reports exposure, not application")
	}
	if len(rep.Backups.InWindow) == 0 && len(rep.Backups.Latest) == 0 {
		out = append(out, "backup drill: no drill records at all — this box has never proven a restore (internal/backup, OPS-P9)")
	}
	if len(rep.Incidents) == 0 {
		out = append(out, "incidents: none recorded in the window")
	}
	if rep.Capacity == nil {
		out = append(out, "capacity: fewer than two dated readings — a trend needs two points, and one reading is a fact about a moment")
	}
	if len(rep.Upstream) == 0 {
		out = append(out, "upstream releases: none recorded in the window — owed by the box's apply path (internal/box's lifecycle)")
	}
	if in.Scan.ScannedAt.IsZero() {
		out = append(out, "security scan: no scan in this report — owed by internal/cve's schedule state (OPS-P11)")
	}
	if rep.FailureModes.Rules == 0 {
		out = append(out, "failure modes: the library was never evaluated — a report over zero rules is not a clean bill of health")
	}
	if len(rep.FailureModes.Unknown) > 0 {
		// One line and not ten. The missing facts are deduplicated by the
		// library's own SortedMissing, so a reader sees the shape of what the
		// box owes rather than the same three fields named nine times — the
		// argument lock/verify.go made about forty copies of one remedy.
		out = append(out, fmt.Sprintf("failure modes: %d rule(s) not evaluated — the observation does not carry %s",
			len(rep.FailureModes.Unknown), strings.Join(rep.FailureModes.Missing, ", ")))
	}
	return out
}

// Summary is the one line a person reads first and the one line a website can
// put under a heading. It is the report's own and not any packet's, which is
// why it is here rather than delegated: it has to name the four things a
// customer is buying in the order they feel them.
func (r Report) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s — %d service(s)", dashIf(r.Box), r.Period.String(), len(r.Availability.Services))
	if r.Availability.FleetPerMille > 0 {
		fmt.Fprintf(&b, ", %.2f%% uptime", float64(r.Availability.FleetPerMille)/100)
	}
	if len(r.Incidents) > 0 {
		fmt.Fprintf(&b, ", %d incident(s)", len(r.Incidents))
	} else {
		b.WriteString(", no incidents")
	}
	if n := len(r.PatchesApplied); n > 0 {
		fmt.Fprintf(&b, ", %d patch(es) applied", n)
	}
	if held := r.heldReleases(); held > 0 {
		fmt.Fprintf(&b, ", %d upstream release(s) held", held)
	}
	if b2 := r.Backups; len(b2.InWindow) == 0 {
		b.WriteString(", no backup drill this period")
	} else if b2.Failed+b2.Refused > 0 {
		fmt.Fprintf(&b, ", backup drill %d/%d passed", b2.Passed, len(b2.InWindow))
	} else {
		b.WriteString(", backup drill passed")
	}
	if held := len(r.FailureModes.Holds); held > 0 {
		fmt.Fprintf(&b, ", %d failure mode(s) identified", held)
	}
	return b.String()
}

// heldReleases counts the releases the box received and kept out, which is the
// number the Attended tier is judged on: a customer who never holds anything
// has never made a decision.
func (r Report) heldReleases() int {
	n := 0
	for _, rel := range r.Upstream {
		if rel.Decision == DecisionHeld {
			n++
		}
	}
	return n
}

// String is the period as a reader writes it: "2026-09-01 to 2026-09-30". The
// dates are in the caller's own location rather than forced to UTC, because a
// monthly report is read by a person who thinks in their own calendar.
func (p Period) String() string {
	if p.Start.IsZero() && p.End.IsZero() {
		return "the whole record"
	}
	start, end := "the beginning", "now"
	if !p.Start.IsZero() {
		start = p.Start.Local().Format("2006-01-02")
	}
	if !p.End.IsZero() {
		end = p.End.Local().Format("2006-01-02")
	}
	return start + " to " + end
}

// dashIf is cve's own helper, restated rather than imported because it is
// unexported there and a second copy of a string check is cheaper than an
// exported helper for an em dash.
func dashIf(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}
