package cli

import (
	"flag"
)

// backupDrillLongHelp is the prose `caf backup drill` prints under its usage
// line. It is a raw string with no backticks in it for the reason backup's own
// long help is — a backtick cannot appear inside one — and the words that would
// be in backticks are quoted instead.
//
// What it has to say is the thing the verb exists for: a backup that has never
// been drilled is not a backup, and before this verb the proof of that was a
// paragraph on a terminal nobody kept.
const backupDrillLongHelp = `The unattended form of the drill: the same cycle, with the confirmation taken
away and the outcome RECORDED.

WHY IT EXISTS. caf backup already proves a restore and then forgets it. The
proof was printed and gone — which is the difference between a drill that
happened and a drill anybody can point at. OPS-P9 is that gap, and this verb
closes it: every run appends a record of what it proved, how long it took and
why it failed when it did.

WHAT IT WRITES. One JSON file per service under the state directory:
CAF_DRILL_DIR, else $XDG_STATE_HOME/caf/drills, else
~/.local/state/caf/drills. That is internal/ledger's DefaultDir convention and
it is state rather than config — these are records caf wrote about work it did,
not decisions a person made, and an editable record is not evidence. The last
twelve drills are kept, which is a year of the monthly cadence.

WHAT IT EMITS. --json writes ONE JSON document to stdout and moves caf's own
report to stderr, so a script pipes stdout into a parser and a person reads
stderr. The document carries the service, the engine, the scratch database,
the tables, the verdict, a machine-matchable reason, the duration and both
timestamps. Nothing secret is in it and nothing can be: caf never reads a
credential's value, so it has none to record.

THE ALERT. A failed drill is not quiet. It exits 1, names a
caf-backup/drill-… reason a script can match, and prints one line on stderr
naming the service and where the record went. caf has no notification
transport — the tier copy states that nobody is paged — so the exit code and
the named reason are the convention, and a monitor that wants more reads
--json.

THE SCHEDULE IS NOT HERE. This verb is what a schedule RUNS; it is not a
scheduler. A drill takes minutes and a daemon that slept for a month is a
daemon nobody has watched run, so the cadence stays where the fleet already
keeps it — cron, a systemd timer, or the box's own — and the record is what
tells you whether it has been running.`

// newBackupDrillCommand is `caf backup drill`: the cycle, unattended, with the
// outcome recorded.
//
// It takes its seams from the parent rather than declaring its own, so a test
// that hands `caf backup` a recording runner and a scratch store hands the
// drill the same two — the drill is not a second command with a second way of
// being faked.
func newBackupDrillCommand(deps backupDeps) *Command {
	opts := &backupOptions{}
	jsonOut := false
	c := &Command{
		Name:     "backup drill",
		Summary:  "run the restore drill unattended, record the outcome, and fail loudly when it does not restore",
		Usage:    "caf backup drill [flags] <service>",
		LongHelp: backupDrillLongHelp,
		Flags: func(fs *flag.FlagSet) {
			declareBackupFlags(fs, opts)
			fs.BoolVar(&jsonOut, "json", false, "emit the outcome as one JSON document on stdout, and the report on stderr")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 1, len(args)); err != nil {
			return err
		}
		// --yes is accepted and ignored: the drill never asks, so a script that
		// always passes it works against both forms. It is declared by
		// declareBackupFlags for that reason.
		opts.yes = true
		return (backupRun{
			opts: *opts, service: args[0], deps: deps, env: env,
			drill: true, asJSON: jsonOut,
		}).run()
	}
	return c
}
