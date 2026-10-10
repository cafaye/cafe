package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cafaye/caf/internal/backup"
	"github.com/cafaye/caf/internal/boxd"
	"github.com/cafaye/caf/internal/contract"
)

// The observe stream, box side: the pure facts `caf boxd`'s observe.report
// route assembles. This file holds the helpers that are functions of their
// inputs — which releases are pending against a range, what a set of facts
// says about health, how a backup.DrillRecord flattens to the boxd DrillFact —
// so each is a table a test drives without a box, a clock or a network.
//
// They live here, in internal/cli, rather than in internal/boxd, because two
// of them import internal/contract and internal/backup and boxd may not:
// boxd runs on the box and is stdlib-only by the house rule (AGENTS.md — the
// daemon must build for every release target with nothing but the standard
// library, and internal/boxd's own test fails the build if it imports os/exec
// or syscall). The world is assembled in cli alongside the other commands'
// own helpers (backup_drill.go, boxd.go); the version-range match is a call
// into the SAME constraint grammar caf contract resolve uses, not a second
// copy of it.

// pendingAgainstRange classifies conductor's candidate releases against this
// box's own version range and deployed version. A release is pending when its
// version is inside the range AND strictly newer than what is deployed — the
// two conditions together: already-deployed releases are not pending, and
// releases outside the range are not this box's to apply.
//
// It reuses contract's constraint grammar and version comparator rather than
// reimplementing them: a second parser of `^1.2.3` is a second thing that can
// disagree with the first about what a caret over a 0.x service means.
//
// A candidate whose version does not parse is skipped rather than failing the
// whole observation: one malformed candidate from conductor must not stop the
// box reporting everything else it knows, which is the resolve principle that
// "store every failure as a field, return no error" (internal/resolve/doc.go).
//
// An empty or unparseable RANGE yields no pending releases: without a range
// the box genuinely cannot classify, and answering "everything is pending"
// would be the lie a stale report tells.
func pendingAgainstRange(rangeText, deployed string, candidates []boxd.CandidateRelease) []boxd.PendingRelease {
	constraint, err := contract.ParseConstraint(rangeText)
	if err != nil {
		// A range the box cannot parse is a range the box does not have; the
		// caller reports the gap in VersionRange, and nothing is pending.
		return nil
	}
	deployedVersion, err := contract.ParseVersion(deployed)
	if err != nil {
		// A deployed version that is not a version means the box is not
		// measuring against anything it can compare. Same honest empty.
		return nil
	}
	var pending []boxd.PendingRelease
	for _, c := range candidates {
		v, err := contract.ParseVersion(c.Version)
		if err != nil {
			continue
		}
		if constraint.Satisfies(v) && v.Compare(deployedVersion) > 0 {
			pending = append(pending, boxd.PendingRelease{Name: c.Name, Version: c.Version})
		}
	}
	return pending
}

// healthStatus derives the box's health summary from the facts already
// gathered. It is a classification, not a measurement: healthy is the absence
// of any degraded reason, and every degraded reason is named so the monthly
// report can say WHY a box needs attention rather than only that it does.
//
// A pending release is NOT a degraded reason. A box running an in-range
// version with a newer release available is the steady state the release flow
// exists for — "an update is available" is a fact for the report, not a cry
// for help. Only a drill that failed, an absent drill, or an expired/expiring
// certificate are health events.
func healthStatus(phase int, cert boxd.Certificate, drill *boxd.DrillFact) boxd.Health {
	var reasons []string
	switch cert.Status {
	case "expired":
		reasons = append(reasons, "the client certificate has expired; this box can no longer dial conductor")
	case "expiring":
		reasons = append(reasons, "the client certificate is close to expiry")
	}
	if drill == nil {
		reasons = append(reasons, "no backup drill has ever run on this box")
	} else if drill.Outcome != string(backup.DrillPassed) {
		reasons = append(reasons, "the last backup drill did not pass ("+drill.Outcome+")")
	}
	status := "healthy"
	if len(reasons) > 0 {
		status = "degraded"
	}
	return boxd.Health{Status: status, Phase: phase, Reasons: reasons}
}

// drillFact flattens a backup.DrillRecord to the boxd DrillFact the
// observation carries, computing age in whole days against now. It is nil for
// a service with no history: a box that has never drilled reports no drill,
// and healthStatus turns that nil into its own named reason rather than a hole
// nobody notices.
func drillFact(rec backup.DrillRecord, now time.Time) *boxd.DrillFact {
	finished := rec.FinishedAt
	age := ageDays(rec.FinishedAt, now)
	return &boxd.DrillFact{
		Outcome:    string(rec.Outcome),
		Reason:     string(rec.Reason),
		Detail:     rec.Detail,
		FinishedAt: &finished,
		AgeDays:    &age,
	}
}

// ageDays is whole days from finished to now, floored at 0 — an age is never
// negative, even if a clock stepped backwards between the drill and the
// report, which happens (a rotate, a clockstep, a re-imaged box).
func ageDays(finished, now time.Time) int {
	d := int(now.Sub(finished).Hours() / 24)
	if d < 0 {
		return 0
	}
	return d
}

// certStatus classifies a client certificate's remaining life against now. A
// certificate inside the warn window is "expiring", past NotAfter is
// "expired", and both are health events; otherwise "ok". DaysLeft is whole
// days, negative once expired — the number is allowed to go negative because a
// consumer wants to know HOW expired, not merely that it is.
func certStatus(notAfter, now time.Time, warnWindow time.Duration) (status string, daysLeft int) {
	daysLeft = int(notAfter.Sub(now).Hours() / 24)
	switch {
	case now.After(notAfter):
		return "expired", daysLeft
	case notAfter.Sub(now) <= warnWindow:
		return "expiring", daysLeft
	default:
		return "ok", daysLeft
	}
}

// observeRange is the box's tracked version range. The range is a fact about
// what the box tracks, and where it comes from is named: CAF_BOX_RANGE on the
// daemon's environment is the seam in v0, the same env-var-over-config idiom
// internal/backup (CAF_DRILL_DIR) and internal/ledger already use. Enrollment
// will write it into the identity document when that lands; until then this is
// the source, and an unset one is reported as unknown rather than guessed.
//
// It returns the range and whether the box has one. An empty result is not an
// error — a box that has not been told its range is the ordinary first state,
// and the observation says so.
func observeRange() (string, bool) {
	r := os.Getenv("CAF_BOX_RANGE")
	return r, r != ""
}

// diskFact measures the box's root filesystem for the observation. The reading
// is made here, in internal/cli, rather than in internal/boxd: boxd is
// stdlib-only by the house rule and the standard library exposes no disk usage
// on any platform, while this package already owns the platform probes
// `caf doctor` reads and is permitted syscall behind them. The observation's
// type lives in boxd next to the document it belongs to; the measurement lives
// where the platform is allowed to be named.
//
// It takes the path rather than reading "/" inline so a test measures a real
// filesystem it created, and so the report can name what it measured. A
// measurement that fails is reported as `unknown` with the error's own words —
// the pantry principle, applied to a number: a zero where a capacity belongs is
// a claim that the box is empty, and that is the worst possible failure of this
// field.
func diskFact(path string) boxd.Disk {
	reading, err := diskUsage(path)
	if err != nil {
		return boxd.Disk{Status: "unknown", Detail: err.Error(), Path: path}
	}
	if reading.Total == 0 {
		return boxd.Disk{
			Status: "unknown",
			Detail: "the filesystem at " + path + " reports no size",
			Path:   path,
		}
	}
	used := reading.Total - reading.Free
	return boxd.Disk{
		Status:      "ok",
		Path:        path,
		TotalBytes:  reading.Total,
		FreeBytes:   reading.Free,
		UsedBytes:   used,
		UsedPercent: 100 * float64(used) / float64(reading.Total),
		Detail:      fmt.Sprintf("measured with statfs against %s; free is what the box can write, excluding the filesystem's reserved blocks", path),
	}
}

// rootDiskPath is what diskFact measures on a box. The absolute spelling of "/"
// rather than the literal, because the Win32 call refuses a path with no drive
// in it and the root of the current drive is the same question on all three
// release targets.
func rootDiskPath() string {
	abs, err := filepath.Abs("/")
	if err != nil {
		return "/"
	}
	return abs
}

// certWarnWindow is how close to expiry a certificate is called "expiring".
// Thirty days is a shape rather than a measurement: a box that reports
// monthly has a month of chances to be re-issued before the certificate
// stops it dialing, which is the point of warning before the wall.
const certWarnWindow = 30 * 24 * time.Hour
