package report

import (
	"time"

	"github.com/anywaye/cafe/internal/backup"
	"github.com/anywaye/cafe/internal/cve"
	"github.com/anywaye/cafe/internal/failure"
	"github.com/anywaye/cafe/internal/secret"
)

// Input is what the assembler is handed: the four packets' outputs in their own
// shapes, plus the five facts nothing in the tree produces yet.
//
// The four are the packets' own types and not copies of them. A report that
// re-declared "a drill record is a service, a verdict and a duration" would
// eventually disagree with backup.DrillRecord about what a verdict is, and the
// disagreement would be found in a customer's inbox. Same argument
// internal/resolve's Dependency import makes.
//
// Every field is optional. An absent section is rendered as the gap it is — see
// doc.go — because a zero here is a sentence about the box rather than a
// sentence about the record.
type Input struct {
	// Box is the enrolled identity this is about.
	Box string `json:"box"`
	// Period is the window the report covers. It is a fact about the report
	// and not about the box: a monthly report is monthly because the window
	// says so.
	Period Period `json:"period"`
	// Uptime is availability per service over the window. OWED BY the box's
	// observation surface (internal/boxd's World, which carries service names
	// and no probe answers today).
	Uptime []ServiceUptime `json:"uptime,omitempty"`
	// Patches is what was applied in the window. OWED BY the packet that
	// applies a fix; internal/cve answers what a box is exposed to, not what
	// was done about it.
	Patches []Patch `json:"patches,omitempty"`
	// Capacity is the trend over the window. OWED BY the same observation
	// surface as Uptime.
	Capacity *Capacity `json:"capacity,omitempty"`
	// Incidents is what happened. OWED BY the same observation surface; the
	// failure library's Conditions Identified is a different fact and lives
	// in its own section below.
	Incidents []Incident `json:"incidents,omitempty"`
	// Upstream is every release the box received and what it did with it.
	// OWED BY the box's apply path (internal/box's lifecycle).
	Upstream []Release `json:"upstream,omitempty"`
	// Drills is the backup drill history (OPS-P9), in backup's own shape.
	// Every service's journal flattened into one list, which is what a
	// one-page report over several services needs.
	Drills []backup.DrillRecord `json:"backup_drills,omitempty"`
	// Inventory is the secret inventory (OPS-P10), in secret's own shape.
	Inventory []secret.Entry `json:"secrets,omitempty"`
	// Rotations is the rotation schedule (OPS-P10). It is what makes "due"
	// and "still not rotatable" facts about THIS box.
	Rotations secret.State `json:"secret_rotations"`
	// Scan is the CVE surface (OPS-P11), a value so it can be rendered twice.
	Scan cve.Report `json:"scan"`
	// Modes is the failure-mode report (OPS-P12). Its Rules are what the
	// library was handed, and a Report over zero of them is not a clean bill
	// of health.
	Modes failure.Report `json:"failure_modes"`

	// Now is the clock the report is assembled against, and it is a field
	// rather than a time.Now call inside Assemble for the reason every other
	// pure function in this tree is a function of its arguments: two runs over
	// one input produce one page, and a test asserts ages without sleeping.
	Now time.Time `json:"-"`
}

// Period is the window the report covers, half-open at the end in the way
// every other interval in this tree is: a record whose timestamp equals End is
// in the NEXT period, which is what stops a drill landing exactly on the
// boundary from being counted twice by two monthly reports.
type Period struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Contains reports whether at falls inside the window. The zero End means the
// window is closed, which is the only shape a caller assembling a report with
// no recorded end can honestly have.
func (p Period) Contains(at time.Time) bool {
	if at.IsZero() {
		return false
	}
	if !p.Start.IsZero() && at.Before(p.Start) {
		return false
	}
	if !p.End.IsZero() && !at.Before(p.End) {
		return false
	}
	return true
}

// ServiceUptime is one service's availability over the window.
//
// OWED BY the box's observation surface. Uptime is per-mille rather than a
// float because "99.95%" and "99.9493%" are different claims and a float
// invites the second one. Downtime is stored rather than derived from the
// percentage so a reader does not have to multiply to find out how long the
// service was actually down.
type ServiceUptime struct {
	Service        string        `json:"service"`
	UptimePerMille int           `json:"uptime_per_mille"`
	Downtime       time.Duration `json:"downtime_ns"`
	// LongestOutage is the single worst stretch, which is the number a
	// customer actually remembers and the one a monthly mean hides.
	LongestOutage time.Duration `json:"longest_outage_ns,omitempty"`
	// Notes is why — the cause of the longest outage, when it is known.
	Notes string `json:"notes,omitempty"`
}

// Capacity is one resource's trend over the window: a series of dated readings
// rather than a single current one.
//
// OWED BY the box's observation surface. A trend is the only thing here that
// cannot be answered by a probe at this instant, which is why it is a slice and
// not a scalar: "the box is at 80% disk" is a fact about a moment and "the box
// has been climbing for six weeks" is the fact a monthly report exists to
// deliver.
type Capacity struct {
	// Resource names what is measured: "disk", "memory", "cpu".
	Resource string `json:"resource"`
	// Unit is how a reading is spelled: "percent", "GiB", "cores".
	Unit string `json:"unit"`
	// Points are in ascending time order. The report renders the direction and
	// the two ends, not every point — a one-page report with forty readings on
	// it is not a one-page report.
	Points []CapacityPoint `json:"points,omitempty"`
}

// CapacityPoint is one dated reading.
type CapacityPoint struct {
	At     time.Time `json:"at"`
	Value  float64   `json:"value"`
	Detail string    `json:"detail,omitempty"`
}

// Incident is something that happened, in the words the customer reads.
//
// OWED BY the box's observation surface. Severity is free text rather than an
// enum because the packet that supplies these does not exist yet and an enum
// invented here would be a third opinion about what a page is.
type Incident struct {
	ID         string     `json:"id,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	Severity   string     `json:"severity,omitempty"`
	Summary    string     `json:"summary"`
	// Resolution is what closed it, and it is separate from Summary because
	// "the disk filled" and "the old snapshots were pruned" are two sentences
	// and only one of them is a fix.
	Resolution string `json:"resolution,omitempty"`
	// Ongoing is true when ResolvedAt is absent. It is a field rather than a
	// nil check at the render site so a caller cannot accidentally write a
	// record that says both.
	Ongoing bool `json:"ongoing,omitempty"`
}

// Normalize fills the derived fields on an Incident. A record whose ResolvedAt
// is absent is ongoing, and calling this once at the boundary means the render
// site and the counters cannot disagree about whether it resolved.
func (i *Incident) Normalize() {
	if i.ResolvedAt == nil {
		i.Ongoing = true
	}
}

// Patch is one fix applied to the box in the window.
//
// OWED BY the packet that applies a fix. internal/cve names what a box is
// exposed to (OPS-P11); this names what was done about it, and the difference
// is the difference between a security report and a service.
type Patch struct {
	Component string    `json:"component"`
	From      string    `json:"from,omitempty"`
	To        string    `json:"to,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	AppliedAt time.Time `json:"applied_at"`
	// Reason names the advisory or the instruction this patch answers, so a
	// patch with no traceable cause can be seen as one.
	Reason string `json:"reason,omitempty"`
	// Restarted is what the patch took down or bounced, which is the half of
	// a fix the availability section has to agree with.
	Restarted []string `json:"restarted,omitempty"`
}

// Release is one upstream release the box received.
//
// OWED BY the box's apply path (internal/box's lifecycle, whose phase table
// owns what a box does when upstream moves). Decision is applied or held — a
// third word for "never received" would be a lie about the record, since a
// release the box was never offered is not one it chose to hold.
type Release struct {
	Name       string    `json:"name"`
	Version    string    `json:"version"`
	ReceivedAt time.Time `json:"received_at,omitempty"`
	Decision   Decision  `json:"decision"`
	// HeldReason is why a held release was held, and it is required exactly
	// when Decision is DecisionHeld: a hold with no reason is a hold nobody
	// can review.
	HeldReason string `json:"held_reason,omitempty"`
}

// Decision is what the box did with a release it was offered.
type Decision string

const (
	// DecisionApplied means the release was applied in the window.
	DecisionApplied Decision = "applied"
	// DecisionHeld means it was received and deliberately kept out, with a
	// reason. A held release is the Attended tier's product and not a defect:
	// the customer decides, and this is the record of the decision.
	DecisionHeld Decision = "held"
)

// drillsInPeriod is the drill history restricted to the window.
//
// It is a method on Input rather than a filter written at the call site so
// one rule about the boundary serves the whole section — the same argument
// Period.Contains makes for itself.
func (in Input) drillsInPeriod() []backup.DrillRecord {
	var out []backup.DrillRecord
	for _, d := range in.Drills {
		if in.Period.Contains(d.FinishedAt) {
			out = append(out, d)
		}
	}
	return out
}
