package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cafaye/caf/internal/backup"
	"github.com/cafaye/caf/internal/report"
	"github.com/cafaye/caf/internal/secret"
)

// reportDeps is what `caf report render` needs from the world. It is a struct
// of parameters for the reason failureDeps is: a test hands the command a
// reader it owns and a scratch directory, and drives the whole path — a document
// or a state directory in, one page out — without a file the test cannot see.
type reportDeps struct {
	stdin io.Reader
}

func (d reportDeps) withDefaults() reportDeps {
	if d.stdin == nil {
		d.stdin = os.Stdin
	}
	return d
}

// reportLongHelp is the prose under `caf report`'s usage line.
//
// It says where the page comes from and what the page is honest about, because
// the second is the part a reader will ask about: a section with no source
// prints as a gap and not as a zero.
const reportLongHelp = `Assembles the one-page monthly report (OPS-P13) from the four wave-2 packets'
own outputs, and renders it in two forms: markdown for a reader, JSON for the
box agent and the website.

The report is an ASSEMBLER, not a collector. A caf process on a workstation
cannot reach the box — it lives behind the outbound mTLS channel
(internal/boxd/daemon.go:101-162) — so the page is a function of a document and
of the state directories this box has already written, the same boundary
` + "`caf failure check`" + ` holds. Pass a document on the command line or on stdin; every section the
document does not carry is filled from the local state when there is any, and
reported as a gap when there is not.

WHAT THE PAGE CARRIES. Availability, patches applied, the backup drill result,
incidents, the capacity trend, upstream releases received and whether each was
applied or held — and the three packets' own verdicts: the CVE scan
(OPS-P11), the secret inventory's due and not-rotatable entries (OPS-P10), and
the failure-mode library's conditions identified (OPS-P12).

WHAT IT WILL NOT DO. It will not print a zero for a section with no data. "No
availability data" is a true sentence about a report; "100% uptime" would be a
sentence about the box that nobody measured. Every gap is printed, in the
markdown and in the JSON, and the JSON carries them as a field so a consumer
counting sections can see which ones were empty on purpose.

Exit status: 0 when the page was written, including when sections are gaps.
Nonzero when the input could not be read or decoded.`

// newReportCommand is the parent. It holds one verb and dispatches to it the way
// `caf failure` and `caf gen` do, because a command that groups verbs never
// registers itself in Commands() as the verb — the parent does, and the parent's
// row is what stub_test.go's workingCommands carries.
func newReportCommand(deps reportDeps) *Command {
	deps = deps.withDefaults()

	c := &Command{
		Name:     "report",
		Summary:  "assemble the one-page monthly report",
		Usage:    "caf report render [flags] [<input.json>]",
		LongHelp: reportLongHelp,
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) == 0 {
			printHelp(env.Stdout, c)
			return nil
		}
		if args[0] != "render" {
			return fmt.Errorf("%w: unknown caf report verb %q (usage: caf report render)", errUsage, args[0])
		}
		return reportRenderCommand(deps).Execute(env, args[1:])
	}
	return c
}

// reportRenderCommand is the verb. It is named with its full invocation so its
// usage errors read as the words the person typed.
func reportRenderCommand(deps reportDeps) *Command {
	c := &Command{
		Name:    "report render",
		Summary: "assemble one month of facts and print the page",
		Usage:   "caf report render [--json] [--period <period>] [<input.json>]",
		LongHelp: "Reads the report input — the four packets' outputs plus the facts\n" +
			"the box's observation surface owes — and writes the page. With no path\n" +
			"(or \"-\") the document is read from stdin. Run `caf report` for what\n" +
			"the document carries, and what is honest about a section with no data.",
		Flags: func(fs *flag.FlagSet) {
			fs.Bool("json", false, "write the machine form (one JSON document) instead of markdown")
			fs.String("period", "", "the window: 2026-09, 30d, or a date range 2026-09-01..2026-09-30. Default: the last calendar month")
			fs.String("box", "", "the enrolled identity this report is about; the default is the input document's own")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 1, len(args)); err != nil {
			return err
		}
		fs := c.FlagSet()
		if err := fs.Parse(args); err != nil {
			return err
		}
		rest := fs.Args()
		if len(rest) != 1 {
			return fmt.Errorf("%w: %s: want one input document, or none to read stdin (usage: %s)",
				errUsage, c.Name, c.Usage)
		}

		period, err := parseReportPeriod(fs.Lookup("period").Value.String())
		if err != nil {
			return fmt.Errorf("caf report render: %w", err)
		}

		in, err := readReportInput(rest[0], deps.stdin)
		if err != nil {
			return fmt.Errorf("caf report render: %w", err)
		}
		// The flags are defaults and the document is the source, so a caller
		// who wrote the window into the document is not overridden by the
		// command's own. The one exception is --box, which is the identity the
		// report is filed under and a document with none is not a disagreement.
		if in.Period.Start.IsZero() && in.Period.End.IsZero() {
			in.Period = period
		}
		if box := fs.Lookup("box").Value.String(); box != "" {
			in.Box = box
		}

		// The state directories are the one thing a caf process on this box CAN
		// read, so the two packets that left files get filled rather than
		// gapped. Both are absent by default and neither is an error: a report
		// on a machine that has never drilled is a report that says so.
		if dir, err := backup.DefaultDrillDir(); err == nil {
			in.Drills = append(in.Drills, readDrillRecords(dir)...)
		}
		if dir, err := defaultSecretStateDir(); err == nil {
			if state, err := secret.Load(dir); err == nil {
				in.Rotations = state
				in.Inventory = secret.Inventory()
			}
		}
		if in.Now.IsZero() {
			in.Now = time.Now()
		}

		rep := report.Assemble(in)
		if fs.Lookup("json").Value.String() == "true" {
			// Machine JSON to stdout, one document, the contract internal/agent
			// holds — so a box agent can pipe it without a parser guessing
			// where the words end.
			return report.Encode(env.Stdout, rep)
		}
		if err := report.Render(env.Stdout, rep); err != nil {
			return fmt.Errorf("caf report render: %w", err)
		}
		// The gap count goes to stderr as a sentence, so a script reading
		// stdout gets one document and a person still learns the page was
		// honest about what it did not know.
		if len(rep.Gaps) > 0 {
			fmt.Fprintf(env.Stderr, "caf report: %d section(s) could not be filled; they are named in the page\n", len(rep.Gaps))
		}
		return nil
	}
	return c
}

// defaultSecretStateDir is where the rotation schedule lives when nothing says
// otherwise. It is the same resolution secret.StatePath assumes — a directory
// beside the other caf state — and the CLI resolves it so a report is not
// silently reading a different file from the one `caf secret rotate` wrote.
func defaultSecretStateDir() (string, error) {
	if dir := os.Getenv("CAF_SECRET_STATE_DIR"); dir != "" {
		return dir, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "caf", "secrets"), nil
}

// readDrillRecords flattens every service's journal in a directory into one list,
// which is what a one-page report over several services needs.
//
// The walk is here rather than in internal/backup because the package's store is
// keyed by service and has no reason to enumerate — a store that could list
// every service is a store that has an opinion about how many services a box
// runs. The filename is the service, and a file that is not a journal is skipped
// rather than fatal: one unreadable service must not cost a customer the other
// six.
func readDrillRecords(dir string) []backup.DrillRecord {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	store, err := backup.OpenDrillStore(dir)
	if err != nil {
		return nil
	}
	var out []backup.DrillRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		service := strings.TrimSuffix(e.Name(), ".json")
		if service == "" {
			continue
		}
		journal, err := store.Read(service)
		if err != nil {
			continue
		}
		out = append(out, journal.Records...)
	}
	return out
}

// readReportInput decodes the report input. A path of "" or "-" is stdin, so the
// verb composes: `caf cve scan --json | caf report render`-style pipelines have a
// document in the shape they already print.
//
// A document that does not decode is refused with the JSON error's own sentence
// and the path it came from, because "your input is wrong" is a sentence that
// sends a reader to a file they cannot fix.
func readReportInput(path string, stdin io.Reader) (report.Input, error) {
	var (
		src    io.Reader
		closer io.Closer
		name   string
	)
	switch strings.TrimSpace(path) {
	case "", "-":
		if stdin == nil {
			return report.Input{}, fmt.Errorf("no path was given and there is no stdin to read the report input from")
		}
		src, name = stdin, "<stdin>"
	default:
		f, err := os.Open(path)
		if err != nil {
			return report.Input{}, fmt.Errorf("read the report input: %w", err)
		}
		src, closer, name = f, f, path
	}
	if closer != nil {
		defer closer.Close()
	}

	var in report.Input
	if err := json.NewDecoder(src).Decode(&in); err != nil {
		return report.Input{}, fmt.Errorf("decode the report input in %s: %w", name, err)
	}
	return in, nil
}

// parseReportPeriod reads the window. Four spellings, and the reason there is
// not one is that a monthly report is written against a month and a
// throwaway is written against a duration:
//
//   - "" — the last calendar month, which is what a monthly report is and the
//     default for that reason. A caller who wants this month says "month".
//   - "month" — the calendar month containing now, so a report can be written
//     mid-month with a note that the month is not over.
//   - "2026-09" — one calendar month by name.
//   - "30d" — the last thirty days.
//   - "2026-09-01..2026-09-30" — an explicit range, for a customer whose month
//     is a billing window and not a calendar.
//
// The end is exclusive, which Period.Contains enforces: a drill landing exactly
// on midnight belongs to the next report rather than being counted twice by two.
func parseReportPeriod(text string) (report.Period, error) {
	text = strings.TrimSpace(text)
	now := time.Now()

	if i := strings.Index(text, ".."); i > 0 {
		start, err := time.Parse("2006-01-02", strings.TrimSpace(text[:i]))
		if err != nil {
			return report.Period{}, fmt.Errorf("period %q: the start is not a date (2026-01-02)", text)
		}
		end, err := time.Parse("2006-01-02", strings.TrimSpace(text[i+2:]))
		if err != nil {
			return report.Period{}, fmt.Errorf("period %q: the end is not a date (2006-01-02)", text)
		}
		return report.Period{Start: start, End: end}, nil
	}

	if text == "month" {
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
		return report.Period{Start: start, End: now}, nil
	}

	if strings.HasSuffix(text, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(text, "d"))
		if err != nil || days <= 0 {
			return report.Period{}, fmt.Errorf("period %q wants a positive number of days (30d)", text)
		}
		return report.Period{Start: now.AddDate(0, 0, -days), End: now}, nil
	}

	if len(text) == 7 && text[4] == '-' {
		at, err := time.Parse("2006-01", text)
		if err != nil {
			return report.Period{}, fmt.Errorf("period %q is not a month (2026-09)", text)
		}
		return report.Period{Start: at, End: at.AddDate(0, 1, 0)}, nil
	}

	if text != "" {
		return report.Period{}, fmt.Errorf("period %q is not 2026-09, 30d, month, or 2026-09-01..2026-09-30", text)
	}

	// The last calendar month, and the day-one-of-the-month boundary rather
	// than 30 days back: a monthly report against a calendar month is what a
	// customer reads next to their invoice.
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local).AddDate(0, -1, 0)
	end := start.AddDate(0, 1, 0)
	return report.Period{Start: start, End: end}, nil
}
