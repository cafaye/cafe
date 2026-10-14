package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cafaye/cafe/internal/backup"
)

// backupDeps is what `cafe backup` needs from the world: the backup engine.
//
// It is a parameter rather than a package-level variable for the same reason
// `deployDeps`, `devDeps` and `reclaimDeps` are: a test can hand the command a
// runner that records what it ran and answers with whatever the case planted,
// which is the only reason a refusal, a dry run, a failed drill and a
// successful cycle are testable in this repository without a container
// runtime.
//
// The runner is internal/backup's own engine, which is why this file imports
// both packages and internal/backup imports neither. The engine owns the plan
// (which commands run, in what order); the command owns everything either side
// of it. backup's KamalRunner delegates process execution to internal/deploy's
// KamalRunner, which stays the only place cafe knows how to run kamal.
type backupDeps struct {
	runner backup.Runner
	// confirm asks the operator. It is a seam because a prompt read from os.Stdin
	// is a prompt no test can answer and no CI job can satisfy: `cafe backup` in a
	// pipeline must either be given -yes or fail, and both of those have to be
	// assertable.
	confirm func(prompt string) (bool, error)
	// drills is where the outcome is recorded. Empty means the resolved default,
	// and a test always sets it. It is a parameter rather than a package-level
	// variable for the reason the runner is: the store is where the printed
	// verdict stops being forgotten, and a store nobody can redirect is a store
	// no test can assert on.
	drills string
}

func (b backupDeps) withDefaults() backupDeps {
	if b.confirm == nil {
		b.confirm = confirmBackupOnStdin
	}
	if b.drills == "" {
		// A machine with no home directory and no CAF_DRILL_DIR cannot say
		// where the record goes, which is a refusal rather than a reason to
		// write it somewhere nobody will look. The error surfaces at the
		// command, which names the variable that fixes it.
		b.drills, _ = backup.DefaultDrillDir()
	}
	return b
}

// runner returns the engine's runner. A test injects a runner to record what
// would run; the wiring selects by the --engine flag, and an unknown engine is
// a usage error rather than a silent default — a typo is not a reason to run a
// drill against the wrong substrate.
//
// The engine is validated even when a runner is injected: the injection is a
// test seam and must not bypass the flag's validation, or a typo in --engine
// would silently run the default engine under a test's fake — which is the one
// outcome the validation exists to prevent.
func (r backupRun) runner() (backup.Runner, error) {
	resolved, err := backup.RunnerFor(r.opts.engine)
	if err != nil {
		return nil, err
	}
	if r.deps.runner != nil {
		return r.deps.runner, nil
	}
	return resolved, nil
}

// backupOptions is the parsed flag state for `cafe backup`.
type backupOptions struct {
	environment string
	scratch     string
	tables      tableList
	dryRun      bool
	yes         bool
	timeout     time.Duration
	// engine selects the backup engine: backup.EngineKamal (the default, the
	// engine the fleet runs) or backup.EngineCNPG (the launch substrate's).
	engine string
}

// tableList is a repeatable flag.
//
// `flag` has no native repeatable flag, and the alternative — one comma-separated
// value — cannot express a table name containing a comma, which is a legal table
// name. So this is the smallest thing that can be one.
type tableList []string

func (t *tableList) String() string { return strings.Join(*t, " ") }

func (t *tableList) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("a --table with no name names no table")
	}
	*t = append(*t, strings.TrimSpace(value))
	return nil
}

// backupLongHelp is the prose `cafe backup` prints under its usage line. It is the
// answer to "what do I type", written down where a person reads it before running
// a command that writes a database dump and then drops a database.
//
// It is a raw string with no backticks in it, for the reason deploy's is: a
// backtick cannot appear inside one, and the words that would be in backticks are
// quoted instead. That is a formatting constraint, not a style choice, and it is
// worth a reader knowing before they spend an afternoon adding one.
const backupLongHelp = `One command takes a real backup of a service and then proves it, and the proof
is a restore into a scratch database that is dropped afterwards. It boots the service's backup
accessory, takes one snapshot through kamal-backup into the service's own restic repository,
restores that snapshot into a scratch database, asserts the tables you name hold rows, and
drops the scratch database on every path — including the failing one.

WHY IT EXISTS. A backup that has never been drilled is not a backup. config/kamal-backup.yml
is a specification until the accessory that runs it is booted, and a specification has never
restored anything. This command is the drill.

WHAT IT NEEDS. A project with config/deploy.yml and config/kamal-backup.yml, which are two
files that are ONE contract: the backup configuration names its accessory and its credentials,
and the deploy configuration's env.secret list is the only place those credentials come from.
The KIT_* variables config/deploy.yml interpolates must be exported in the shell that runs
this, because the accessory has to be booted against the deployment cafe deploy would build.
Credentials are NAMES in .kamal/secrets; cafe never reads a value.

WHAT IT REFUSES, BEFORE ANYTHING IS BOOTS. The accessory the backup configuration names is
not in the deploy configuration. The accessory does not mount the backup configuration
read-only. A secret the backup configuration names is not declared by that accessory — which
is the failure that otherwise deploys cleanly and then fails validation with a message about
RESTIC_REPOSITORY rather than about the forgotten name. And "app:" disagreeing with the
service, which nothing inside the accessory checks and which makes every snapshot unfindable.
Each refusal carries a caf-backup/… reason, so a script can match a refusal instead of
grepping for prose.

WHAT IT RUNS. "kamal version" and "kamal config" change nothing. "kamal accessory boot all"
starts the backup accessory and skips one already running. "kamal accessory exec" runs
"kamal-backup backup --force" inside it, because a schedule of 1d otherwise answers "no backup
due" and the drill would restore a snapshot nobody just took. Then "kamal-backup drill
production" restores into the scratch database and the gem decides pass or fail by the exit
status of the check cafe hands it. Every command is bounded by -timeout, so a hung pull or a
hung dump is a named failure rather than a hang.

THE RESTORE PROOF IS NOT CAF'S TO IMPLEMENT. kamal-backup does not create the scratch database,
does not drop it, and does not assert anything, so cafe supplies those two things. The refusal
to drill into a production-looking name is NOT duplicated here: it is kamal-backup's, and in
operator form it is cafaye/kit's templates/kamal/drill.sh's. cafe hands the gem a name and
reports what the gem said.

THE ENGINE. --engine selects the backup engine: kamal (the default, the engine the fleet
runs, driven by kamal-backup and a restic repository) or cnpg (the launch substrate's, where
there is no Kamal — driven by kubectl cnpg, a CloudNativePG Backup object and a declarative
recovery Cluster). The CNPG engine reads no kamal-backup.yml and checks no two-file contract:
the cluster is a fact about the Kubernetes namespace, and its dry run executes nothing.

ONE PATH. cafe backup <service> --table <name> --yes, from the project directory. --dry-run
runs the two read-only steps, checks the contract against what kamal resolved, prints every
command and changes nothing — and it refuses exactly what the real run refuses, because a dry
run more permissive than the real one is worse than no dry run.`

// newBackupCommand takes its seams. The router passes the zero value and gets the
// real wiring.
//
// `cafe backup` is a parent with one verb besides itself: `cafe backup drill` is
// the same cycle with the confirmation taken away and the outcome recorded and
// reported as a machine document. The parent dispatches the way `cafe contract`
// does, and the drill verb is a Command of its own named with its full
// invocation so its usage errors read as the words the user typed.
func newBackupCommand(deps backupDeps) *Command {
	opts := &backupOptions{}
	c := &Command{
		Name:     "backup",
		Summary:  "take one real backup and prove it restores, into a scratch database it then drops",
		Usage:    "cafe backup [flags] <service>",
		LongHelp: backupLongHelp,
		Flags: func(fs *flag.FlagSet) {
			declareBackupFlags(fs, opts)
		},
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) > 0 && args[0] == "drill" {
			return newBackupDrillCommand(deps).Execute(env, args[1:])
		}
		if err := wantArgs(c.Name, c.Usage, 1, len(args)); err != nil {
			return err
		}
		// The options are copied into the run and the seams are filled in inside
		// it, so a Command executed twice — which the router does not do but a test
		// does — cannot carry a flag value or a recorded argv from one run into the
		// next.
		return (backupRun{opts: *opts, service: args[0], deps: deps, env: env}).run()
	}
	return c
}

// declareBackupFlags is the whole flag surface both backup invocations take.
//
// One function rather than two copies, because the cycle and the drill differ in
// exactly one thing — whether an operator is asked — and seven duplicated
// declarations is seven places for the two to drift apart on. --yes is declared
// on both and not only the cycle: a script that always passes it works against
// either form, and the drill simply never asks.
func declareBackupFlags(fs *flag.FlagSet, opts *backupOptions) {
	fs.StringVar(&opts.environment, "env", "", "environment overlay, as config/deploy.<env>.yml merged over config/deploy.yml")
	fs.StringVar(&opts.scratch, "scratch", "", "database to drill the restore into; dropped on every path. Default <service>_drill")
	fs.Var(&opts.tables, "table", "a table that must hold rows after the restore. Repeatable, and required: a drill with no table asserts nothing")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "print every command that would run; changes nothing")
	fs.BoolVar(&opts.yes, "yes", false, "back up without asking; required where nobody can answer the prompt")
	fs.DurationVar(&opts.timeout, "timeout", backup.DefaultTimeout, "budget for each command, so a hung pull or dump is a failure rather than a hang")
	fs.StringVar(&opts.engine, "engine", "", "backup engine: kamal (the default, the engine the fleet runs) or cnpg (the launch substrate's, where there is no Kamal)")
}

// backupRun is one `cafe backup` invocation, gathered so the steps read top to
// bottom and can be tested without a closure capturing five variables.
type backupRun struct {
	opts    backupOptions
	service string
	deps    backupDeps
	env     *Env
	// drill is the verb that runs the cycle unattended: it is the form a
	// schedule uses, so it never asks and it always records.
	drill bool
	// asJSON emits the outcome as one machine document on stdout and moves the
	// engine's own report to stderr. It is the same split `cafe agent` makes and
	// for the same reason: an agent pipes stdout into a parser, and a report
	// interleaved with the document is a document nobody can decode.
	asJSON bool
}

func (r backupRun) run() error {
	r.deps = r.deps.withDefaults()
	runner, err := r.runner()
	if err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}

	// The dry run happens before the prompt, and that order is the point: a
	// refusal or a plan that says "this is wrong" is more useful than a question
	// about whether to go ahead and do it. Somebody who typed `cafe backup` to see
	// what it would do gets the answer, not a prompt.
	if r.opts.dryRun {
		if r.asJSON {
			return fmt.Errorf("%w: cafe backup drill --json with --dry-run: a dry run runs nothing, so there is no outcome to emit; run it for real, or drop --json", errUsage)
		}
		return r.runCycle(runner, true)
	}

	// A drill on a schedule has nobody to answer a prompt, and a command that
	// hung waiting for one would be a schedule that never ran. So the drill
	// skips the question the cycle asks — which is the whole difference between
	// the two, and the reason there are two forms at all.
	if r.drill {
		return r.runCycle(runner, false)
	}

	ok, err := r.ask()
	if err != nil {
		return err
	}
	if !ok {
		_, _ = fmt.Fprint(r.out(), "cafe backup: nothing was taken. Run it with --yes when you mean it.\n")
		return errReported
	}
	return r.runCycle(runner, false)
}

// runCycle runs the cycle and, unless it was a dry run, records what it proved.
//
// The record is written AFTER the engine's report and BEFORE the exit is
// decided, and that order is the packet: the verdict used to be printed here
// and forgotten, and a store written before the verdict would be a store
// guessing at it.
func (r backupRun) runCycle(runner backup.Runner, dryRun bool) error {
	// The report is teed into a buffer while it is written, because the record's
	// detail field has to carry the sentence that named the failure and the
	// engine's sentinels deliberately do not: ErrCycleFailed's comment says the
	// caller must not print the report a second time, and reading it back out of
	// the buffer is the way to record it without printing it again.
	var report bytes.Buffer
	stdout := r.reportOut()
	if !dryRun {
		stdout = io.MultiWriter(stdout, &report)
	}

	started := time.Now().UTC()
	err := backup.Run(ctxOrBackground(r.env), runner, backup.Request{
		Service: r.service,
		Engine:  r.opts.engine,
		Env:     r.opts.environment,
		Scratch: r.scratchName(),
		Tables:  r.opts.tables,
		DryRun:  dryRun,
		Timeout: r.opts.timeout,
	}, stdout, r.errOut())
	if dryRun {
		// Nothing ran, so there is nothing to remember. A dry run that wrote a
		// record would be a dry run with a side effect, which is the one thing a
		// dry run must never be.
		return r.classify(err)
	}
	return r.recordAndReport(started, err, report.String())
}

// classify is the three classes, three exits, and the class is the sentinel
// rather than the message — so a reworded refusal cannot quietly stop being
// recognized as one.
//
//	ErrUsage      cafe was invoked wrongly. Exit 2, the message on stderr.
//	ErrRefused    a committed configuration is wrong. Exit 1, and the refusal
//	              is ALREADY the report on stdout, so stderr stays empty: a
//	              `cafe: ...` line there puts a truncated copy in the place a
//	              CI log greps, and the truncated copy is the one without the
//	              advice in it.
//	anything else the cycle ran and something broke. Exit 1, the cause on
//	              stderr, and the report already printed on stdout.
func (r backupRun) classify(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, backup.ErrUsage):
		return fmt.Errorf("%w: %w", errUsage, err)
	case errors.Is(err, backup.ErrRefused):
		return errReported
	default:
		return err
	}
}

// recordAndReport persists the outcome, emits the machine document when asked
// for it, and puts the one line a human has to see on stderr when the drill
// failed.
func (r backupRun) recordAndReport(started time.Time, err error, report string) error {
	rec := r.drillRecord(started, err, report)
	// The write is a failure of its own and not a warning. The gap this packet
	// closes is a verdict that was printed and forgotten, and a drill whose
	// record did not land has been forgotten exactly as thoroughly — so it
	// exits nonzero with the path in the sentence rather than exiting 0 on the
	// strength of a restore nobody will be able to point at.
	if writeErr := r.writeRecord(rec); writeErr != nil {
		return fmt.Errorf("cafe backup: the drill %s, and cafe could not record it: %w\n"+
			"  The result is only in the report above. Set CAF_DRILL_DIR to a writable directory and run it again",
			rec.Outcome, writeErr)
	}

	if r.asJSON {
		// One document, on stdout, and nothing else: a consumer that has to skip
		// a prose line to reach its JSON is a consumer that will one day skip the
		// wrong line.
		if err := writeJSONDoc(r.out(), rec); err != nil {
			return err
		}
	}
	if rec.Failed() && r.drill {
		// The alert, and it is the DRILL's rather than the cycle's. The cycle's
		// contract is that a refusal is a report on stdout and stderr stays
		// empty — a line there is a truncated copy of a paragraph in the place a
		// CI log greps — and that decision is not this packet's to revisit. A
		// drill is the other case: nobody is watching a terminal, so the line is
		// the only thing a human will ever see of the failure.
		//
		// It is not a copy of the report either. The report is several sentences
		// about a mechanism; this is one line naming the machine-matchable reason,
		// the service and where the record went.
		//
		// cafe has no notification transport — the tier copy states that nobody
		// is paged (OPS-P20) — so the convention this uses is the one cafe already
		// has: exit nonzero and a named reason. A monitor that wants more reads
		// --json.
		_, _ = fmt.Fprintf(r.errOut(), "cafe backup drill: %s: %s did not prove a restore (%s)\n",
			rec.Reason, rec.Service, rec.Outcome)
		if rec.Detail != "" {
			_, _ = fmt.Fprintf(r.errOut(), "  %s\n", rec.Detail)
		}
		_, _ = fmt.Fprintf(r.errOut(), "  recorded in %s\n", r.deps.drills)
	}
	return r.classify(err)
}

// drillRecord is one cycle, as a fact about a service.
func (r backupRun) drillRecord(started time.Time, err error, report string) backup.DrillRecord {
	finished := time.Now().UTC()
	rec := backup.DrillRecord{
		Service:    r.service,
		Engine:     r.engineName(),
		Env:        r.opts.environment,
		Scratch:    r.scratchName(),
		Tables:     append([]string{}, r.opts.tables...),
		StartedAt:  started,
		FinishedAt: finished,
		DurationMS: finished.Sub(started).Milliseconds(),
	}
	switch {
	case err == nil:
		rec.Outcome, rec.Reason = backup.DrillPassed, backup.DrillReasonPassed
		rec.Detail = firstNonEmptyLine(report)
	case errors.Is(err, backup.ErrRefused):
		rec.Outcome = backup.DrillRefused
		// The clause that broke, when the report named one. It is better than the
		// class: a script matching caf-backup/drill-refused learns that the config
		// is wrong, and one matching the clause learns which line to edit.
		if reason := backup.ReportReason(report); reason != "" {
			rec.Reason = reason
		} else {
			rec.Reason = backup.DrillReasonRefused
		}
		rec.Detail = firstNonEmptyLine(report)
	case errors.Is(err, backup.ErrCycleFailed):
		rec.Outcome, rec.Reason = backup.DrillFailed, backup.DrillReasonFailed
		rec.Detail = firstNonEmptyLine(report)
		if rec.Detail == "" && err != nil {
			rec.Detail = firstNonEmptyLine(err.Error())
		}
	default:
		// The cycle never got going: the engine is not installed, the project
		// cannot be read, the plan cannot be built. ErrCycleFailed is the ONE
		// sentinel a cycle that actually ran returns (run.go's failureReport is
		// its only source), so its absence is the signal — and "cafe could not
		// start" has to be a different fact from "the restore did not hold",
		// because one is a machine to fix and the other is a backup to fix.
		rec.Outcome, rec.Reason = backup.DrillFailed, backup.DrillReasonNotRun
		// The error, not the report: before the cycle starts the report's first
		// line is the cycle's own header, and a record whose detail is
		// "backing up from … with kamal 2.12.0" says nothing about why it stopped.
		rec.Detail = firstNonEmptyLine(errorLine(err))
	}
	return rec
}

// errorLine is an error's first line, and "" for a nil one.
func errorLine(err error) string {
	if err == nil {
		return ""
	}
	return firstNonEmptyLine(err.Error())
}

// writeRecord appends to the store the command was wired with.
func (r backupRun) writeRecord(rec backup.DrillRecord) error {
	if r.deps.drills == "" {
		return errors.New("no drill directory was resolved: set CAF_DRILL_DIR to say where drill records go")
	}
	store, err := backup.OpenDrillStore(r.deps.drills)
	if err != nil {
		return err
	}
	_, err = store.Append(rec)
	return err
}

// engineName is the engine that ran, named. An empty flag means the default and
// the record has to say so, because a report about the wrong engine is a report
// about a drill that never happened.
func (r backupRun) engineName() string {
	if r.opts.engine == "" {
		return backup.EngineKamal
	}
	return r.opts.engine
}

// scratchName is the database the restore is drilled into, resolved once so the
// prompt, the request and the record cannot name three different ones.
func (r backupRun) scratchName() string {
	if r.opts.scratch != "" {
		return r.opts.scratch
	}
	return r.service + "_drill"
}

// reportOut is where the engine's own report goes. A machine consumes stdout,
// so the report moves to stderr when the outcome is a machine document — the
// same split `cafe agent` makes.
func (r backupRun) reportOut() io.Writer {
	if r.asJSON {
		return r.errOut()
	}
	return r.out()
}

// firstNonEmptyLine is the first line of a report that says anything.
//
// It is not doctor.go's firstLine, which takes an error and splits its first
// line whatever it holds: a report opens with a blank line by convention (every
// report in internal/backup does), so a record's detail field built from that
// would be empty on exactly the records that need it. Taking a string rather
// than an error is the same difference — the thing being summarized here is a
// paragraph cafe printed, not an error.
func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// confirmBackupOnStdin is confirm.go's reader under this command's name. The
// prompt reader is shared because its subtle part — a closed stdin must answer
// "no" rather than block — is the same for both commands, and the name is a
// parameter because the message has to read as the words the user typed.
func confirmBackupOnStdin(prompt string) (bool, error) { return askOnStdin("cafe backup", prompt) }

// ask is the confirmation, and it is the only interactive thing cafe backup does.
//
// A dry run never reaches it. --yes skips it. Everything else asks once, naming
// the service, the scratch database and the tables, because a prompt that does
// not say what it is about is a prompt people approve without reading — and the
// thing being approved here creates a database and drops it.
func (r backupRun) ask() (bool, error) {
	if r.opts.yes {
		return true, nil
	}
	scratch := r.scratchName()
	tables := strings.Join(r.opts.tables, ", ")
	if r.opts.engine == backup.EngineCNPG {
		return r.deps.confirm(fmt.Sprintf(
			"cafe backup: back the %s cluster up, restore it into the %s cluster, and drop it? asserting %s [y/N] ",
			r.service, scratch, tables))
	}
	where := "config/deploy.yml"
	if r.opts.environment != "" {
		where = "config/deploy." + r.opts.environment + ".yml over config/deploy.yml"
	}
	return r.deps.confirm(fmt.Sprintf(
		"cafe backup: back %s up from %s, restore it into %s, and drop it? asserting %s [y/N] ",
		r.service, where, scratch, tables))
}

func (r backupRun) out() io.Writer    { return outOrDiscard(r.env) }
func (r backupRun) errOut() io.Writer { return errOrDiscard(r.env) }
