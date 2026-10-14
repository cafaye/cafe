package cve

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Schedule is how often the CVE surface is computed. It is a duration rather
// than a cron expression because the substrate has no cron daemon cafe can
// drive, and the mechanism that puts this on a schedule is a line the
// operator installs (see CronLine), which cron itself interprets.
//
// The spelling accepts both a Go duration ("6h", "90m") and the day form
// kit's own backup configuration uses (`backup.schedule: 1d`,
// internal/backup/config.go keeps it as written). Two spellings for one fact
// is a small cost; a schedule a reader cannot recognize because they learned
// the other one is a larger one.
type Schedule struct {
	Every time.Duration
}

// DefaultSchedule is the cadence this package assumes when nothing says
// otherwise. Daily, for the same reason kit's backup accessory runs daily:
// an advisory published at 09:00 is worth knowing about before the next
// working day ends, and a scan every five minutes would spend the fleet's
// egress on a feed that changes a few times a week.
var DefaultSchedule = Schedule{Every: 24 * time.Hour}

// ParseSchedule reads a schedule. The empty string is the default rather than
// an error, for the same reason `backup.schedule`'s absence is not a
// malformed document: a caller that wanted the default should not have to
// write it down, and a caller that wrote garbage gets a sentence naming both
// spellings.
func ParseSchedule(text string) (Schedule, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return DefaultSchedule, nil
	}
	// The day form first: "1d" is a valid suffix parse but a zero duration
	// under Go's own ParseDuration, which would silently turn a daily scan
	// into one that runs forever. Checking it first is what stops that.
	if n, err := strconv.Atoi(strings.TrimSuffix(text, "d")); err == nil && strings.HasSuffix(text, "d") {
		if n <= 0 {
			return Schedule{}, fmt.Errorf("schedule %q wants a positive number of days", text)
		}
		return Schedule{Every: time.Duration(n) * 24 * time.Hour}, nil
	}
	d, err := time.ParseDuration(text)
	if err != nil {
		return Schedule{}, fmt.Errorf("schedule %q is neither a duration (6h, 90m) nor a number of days (1d, 7d): %w", text, err)
	}
	if d <= 0 {
		return Schedule{}, fmt.Errorf("schedule %q wants a positive interval", text)
	}
	return Schedule{Every: d}, nil
}

// String is the schedule as written, so a report prints what the caller asked
// for rather than what cafe parsed.
//
// It expresses the interval in the largest unit that divides it evenly, so a
// 90-minute schedule prints "90m" rather than Go's "1h30m0s". That is not
// pedantry: a report is read next to the configuration that set it, and a
// reader who wrote `1d` and sees `24h0m0s` has to work out whether those are
// the same setting before they can trust the rest of the line.
func (s Schedule) String() string {
	switch {
	case s.Every == 0:
		return ""
	case s.Every%(24*time.Hour) == 0 && s.Every >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(s.Every.Hours()/24))
	case s.Every%time.Hour == 0:
		return fmt.Sprintf("%dh", int(s.Every.Hours()))
	case s.Every%time.Minute == 0:
		return fmt.Sprintf("%dm", int(s.Every.Minutes()))
	default:
		return s.Every.String()
	}
}

// Due reports whether a scan is due, and when the next one is.
//
// It answers three things rather than one because the third is the one that
// matters to an operator reading a report: not "is it due" but "how stale is
// what I am looking at". A box nobody has scanned has never been scanned, and
// that is a different sentence from a box that is two hours from its next
// run — the first is a gap in the product, the second is the product working.
func (s Schedule) Due(now time.Time, last time.Time) (bool, time.Time, string) {
	if last.IsZero() {
		return true, now, "never scanned"
	}
	next := last.Add(s.Every)
	if !now.Before(next) {
		return true, next, "last scanned " + humanSince(now.Sub(last))
	}
	return false, next, "next scan in " + humanSince(next.Sub(now))
}

// humanSince renders a duration the way an operator reads one. It stops at
// hours rather than days because a daily schedule that reports "1d" tells the
// reader nothing about whether they are looking at today's scan.
func humanSince(d time.Duration) string {
	if d < time.Minute {
		return "less than a minute"
	}
	if d < time.Hour {
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	if d < 48*time.Hour {
		return fmt.Sprintf("%d hours", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

// State is what a box remembers between runs: when it last computed its CVE
// surface, and what the answer was.
//
// It is a file rather than a fact carried in the process because the whole
// point is continuity — the value is that the answer is computed without
// anybody asking — and a schedule whose state died with its process would be
// a schedule that recomputed from scratch every time, which is the same as
// no schedule at all with more egress.
type State struct {
	// Box is the enrolled identity this state is about, so one state file on
	// a machine that has run two boxes cannot answer for the wrong one.
	Box string `json:"box"`
	// LastRun is when the last completed scan started.
	LastRun time.Time `json:"last_run"`
	// LastOutcome is one sentence about what the last run found, so the next
	// report can say whether anything changed.
	LastOutcome string `json:"last_outcome,omitempty"`
	// Findings is the count the last run reported, so "0 of 0" and "nothing
	// scanned" are distinguishable in the report.
	Findings int `json:"findings"`
	// ScanSeconds is how long the last run took. It is the honest number
	// behind a schedule: a scan that takes eleven minutes is not a daily
	// scan, and a report that hid that would be a report whose cadence was
	// fiction.
	ScanSeconds float64 `json:"scan_seconds,omitempty"`
}

// StatePath is where the state lives, beside the other cafe state. It follows
// internal/cli/boxd.go's own resolution: the flag, then the environment, then
// the user state directory.
func StatePath(flagPath string) (string, error) {
	if flagPath != "" {
		return flagPath, nil
	}
	if fromEnv := os.Getenv("CAF_CVE_STATE"); fromEnv != "" {
		return fromEnv, nil
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find the home directory: %w", err)
	}
	return filepath.Join(dir, ".config", "cafe", "cve-state.json"), nil
}

// LoadState reads the schedule state. A missing file is the zero state, not an
// error: a box that has never been scanned has no state, and "cannot read
// state" would be a report about a file rather than about the box.
func LoadState(path string) (State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, fmt.Errorf("read the cve state at %s: %w", path, err)
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		// A torn state file is not a reason to refuse to scan. cafe starts
		// over, which recomputes once, rather than failing forever on a file
		// it cannot read — and it says so, because a schedule that silently
		// restarted would be a schedule whose staleness nobody could see.
		return State{}, nil
	}
	return s, nil
}

// Save writes the state atomically. Rename, for the reason internal/run's
// writeAtomic gives: a state file torn by a process that stopped mid-write
// would lose the only record of when this box last looked at itself.
func SaveState(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create the cve state directory: %w", err)
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the cve state: %w", err)
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), "cve-state-*.tmp")
	if err != nil {
		return fmt.Errorf("write the cve state: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write the cve state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write the cve state: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("write the cve state: %w", err)
	}
	return os.Rename(tmp.Name(), path)
}

// CronLine is the line an operator installs to put this on a schedule.
//
// It is rendered rather than written to a crontab, and the reason is the
// boundary this package stays inside: cafe runs on the box's operator machine
// and on the box, and a cafe that edited root's crontab would be a cafe that
// changed how a box boots without being asked. The line is printed; the
// operator installs it. That is also the Attended tier's own promise —
// upgrade RECOMMENDATIONS the customer applies — applied to the schedule
// itself.
//
// The hour is not pinned: a cron entry with a fixed hour would collide with
// every other box in a fleet that installed it at the same minute, and the
// advisory feed would see one request per box in one second. Spreading the
// hour across the day is what a fleet-wide schedule costs, and it is free
// because cron's step syntax already does it.
//
// No `--quiet` is rendered, and that is deliberate: the checkpoints go to
// stderr, so a cron run that stalls leaves the phase it stopped in in the
// mail cron sends. A line that suppressed them would be a line whose only
// output on a stall was nothing at all.
func CronLine(s Schedule, binary string) string {
	every := s.Every
	if every < time.Hour {
		return fmt.Sprintf("*/%d * * * * %s cve scan  # every %d minutes", int(every.Minutes()), binary, int(every.Minutes()))
	}
	hours := int(every.Hours())
	if hours < 24 {
		return fmt.Sprintf("0 */%d * * * %s cve scan  # every %d hours", hours, binary, hours)
	}
	if hours == 24 {
		return fmt.Sprintf("0 */6 * * * %s cve scan  # daily, spread across the fleet's egress", binary)
	}
	return fmt.Sprintf("0 0 */%d * * %s cve scan  # every %d days", hours/24, binary, hours/24)
}
