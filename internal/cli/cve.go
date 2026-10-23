package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/anywaye/cafe/internal/cve"
)

// cveDeps is what `cafe cve` needs from the world, and it is a struct of
// parameters for the reason resolveDeps and providerDeps are: a test can hand
// the command an observer that needs no kubectl, an advisory source that
// answers without a network, and a state file it owns, and drive a whole scan
// end to end. The seams are the whole point — the classification tests are
// pure, and the command tests are neither network nor cluster.
type cveDeps struct {
	// observer reads the box. Nil means the kubectl observer, which is why a
	// test always sets it.
	observer cve.Observer
	// source is where advisories come from. Nil means the real HTTP source.
	source cve.Source
	// statePath is where the schedule state lives. Empty means the default
	// resolution through cve.StatePath.
	statePath string
	// kubeconfig and context are handed to the kubectl observer untouched.
	kubeconfig string
	context    string
}

// cveFlags is the flags every verb of `cafe cve` shares. `--json` is on all of
// them for the reason internal/cli/box.go gives: a shell reads one stream and
// a person reads the other, and there is no flag that chooses which stream
// gets the document because one of the two would then be wrong in some
// script.
type cveFlags struct {
	json     bool
	box      string
	schedule string
	state    string
	noWrite  bool
}

func (f *cveFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&f.json, "json", false, "print the machine document on stdout instead of the human one")
	fs.StringVar(&f.box, "box", "", "the enrolled identity of the box this is about; $CAF_BOX_ID")
	fs.StringVar(&f.schedule, "schedule", "", "how often to scan (6h, 1d, 7d); empty means the daily default")
	fs.StringVar(&f.state, "state", "", "where the schedule state lives; $CAF_CVE_STATE or the user state directory")
	fs.BoolVar(&f.noWrite, "no-write-state", false, "do not record that this scan ran")
}

// boxOf resolves the box's name the way internal/cli/boxd.go resolves the
// identity path: the flag, then the environment, and never a default, because
// a report that answered for "default" would be a report about a box nobody
// enrolled.
func (f cveFlags) boxOf() string {
	if f.box != "" {
		return f.box
	}
	return os.Getenv("CAF_BOX_ID")
}

// scheduleOf resolves the cadence the same way, and the environment variable
// rather than a flag default for the reason pantryURLEnv is one: a default
// baked into a binary goes stale when the fleet's practice moves and nobody
// rebuilds cafe to find out.
func (f cveFlags) scheduleOf() (cve.Schedule, error) {
	if f.schedule != "" {
		return cve.ParseSchedule(f.schedule)
	}
	if fromEnv := os.Getenv("CAF_CVE_SCHEDULE"); fromEnv != "" {
		return cve.ParseSchedule(fromEnv)
	}
	return cve.DefaultSchedule, nil
}

// cveVerb pairs the word a person types with the command that runs it. The two
// are different strings on purpose, and it is the same pair contractVerb is:
// the word is what the dispatcher looks up, and the command is named with its
// full invocation so its help and its usage errors read as `cafe cve scan`
// rather than `cafe scan`.
type cveVerb struct {
	name string
	cmd  *Command
}

func cveVerbs(deps cveDeps) []cveVerb {
	return []cveVerb{
		{name: "inventory", cmd: newCveInventoryCommand(deps)},
		{name: "scan", cmd: newCveScanCommand(deps)},
		{name: "schedule", cmd: newCveScheduleCommand()},
	}
}

func newCveCommand(deps cveDeps) *Command {
	verbs := cveVerbs(deps)
	c := &Command{
		Name:    "cve",
		Summary: "the CVE surface of a box: what is installed, what has an advisory, what a fix touches",
		Usage:   "cafe cve <inventory|scan|schedule> [flags]",
		LongHelp: "OPS-P11.\n\n" +
			"Every item in the Attended tier is a schedule rather than an answer to a\n" +
			"question, and this is the one that says what on the box has a known advisory and\n" +
			"what applying the fix would touch. The report names the component, the advisory,\n" +
			"the version that releases out of it, and what applying it touches — because\n" +
			"\"you have 14 advisories\" is not an answer anybody can act on.\n\n" +
			cveVerbTable(verbs),
	}
	// Bare `cafe cve` is a question ("what can this do?"), not a mistake, so it
	// answers instead of exiting 2 — the same decision
	// internal/cli/contract.go makes for a bare `cafe contract`.
	c.Run = func(args []string, env *Env) error {
		if len(args) == 0 {
			printHelp(env.Stdout, c)
			return nil
		}
		for _, verb := range verbs {
			if verb.name == args[0] {
				return verb.cmd.Execute(env, args[1:])
			}
		}
		names := make([]string, 0, len(verbs))
		for _, verb := range verbs {
			names = append(names, verb.name)
		}
		return fmt.Errorf("%w: unknown cve subcommand %q (usage: cafe cve <%s>)",
			errUsage, args[0], strings.Join(names, "|"))
	}
	return c
}

// cveVerbTable is the verb list in a command's help. It is one string rather
// than a rendering hook so `cafe cve`, `cafe cve --help` and `cafe help cve`
// cannot drift into three different answers.
func cveVerbTable(verbs []cveVerb) string {
	var b strings.Builder
	b.WriteString("Commands:\n")
	for _, verb := range verbs {
		_, _ = fmt.Fprintf(&b, "  %-16s %s\n", verb.name, verb.cmd.Summary)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// cve inventory
// ---------------------------------------------------------------------------

func newCveInventoryCommand(deps cveDeps) *Command {
	var f cveFlags
	return &Command{
		Name:    "cve inventory",
		Summary: "every component on the box, against what the substrate pins",
		Usage:   "cafe cve inventory [--json]",
		LongHelp: "Two halves, kept apart. The declared half is what the substrate pins, with the\n" +
			"file and line each version was read from. The observed half is what the box is\n" +
			"actually running.\n\n" +
			"They are not folded into one list, because a pin nobody applied and an applied\n" +
			"version nobody pinned are both facts, and one list loses one of them.",
		Flags: func(fs *flag.FlagSet) { f.register(fs) },
		Run: func(args []string, env *Env) error {
			if len(args) != 0 {
				return wantArgs("cve inventory", "cafe cve inventory [--json]", 0, len(args))
			}
			observer := deps.observer
			if observer == nil {
				observer = cve.KubectlObserver{Kubeconfig: deps.kubeconfig, Context: deps.context}
			}
			observed, err := observer.Observe(context.Background(), cve.Discard())
			if err != nil {
				return fmt.Errorf("cafe cve inventory: read the box: %w", err)
			}
			inv := cve.Inventory{Box: f.boxOf(), Declared: append([]cve.Component(nil), cve.Substrate...), Observed: observed}
			inv.Compare()
			if f.json {
				doc := struct {
					Box         string           `json:"box"`
					Declared    []cve.Component  `json:"declared"`
					Observed    []cve.Component  `json:"observed"`
					Divergences []cve.Divergence `json:"divergences,omitempty"`
				}{Box: inv.Box, Declared: inv.Declared, Observed: inv.Observed, Divergences: inv.Divergences}
				enc := json.NewEncoder(env.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(doc)
			}
			var b strings.Builder
			b.WriteString("  declared by the substrate:\n")
			for _, c := range inv.Declared {
				_, _ = fmt.Fprintf(&b, "    %-16s %-16s %-9s %s\n", c.Name, dash(c.Version), c.Kind, c.Source)
			}
			if len(inv.Observed) > 0 {
				b.WriteString("\n  observed on the box:\n")
				for _, c := range inv.Observed {
					_, _ = fmt.Fprintf(&b, "    %-16s %-16s %-9s %s\n", c.Name, dash(c.Version), c.Kind, c.Source)
				}
			}
			if len(inv.Divergences) > 0 {
				b.WriteString("\n  where they disagree:\n")
				for _, d := range inv.Divergences {
					_, _ = fmt.Fprintf(&b, "    %-16s declared %-14s running %-14s %s\n",
						d.Component, dash(d.Declared), dash(d.Observed), d.Why)
				}
			}
			_, err = io.WriteString(env.Stdout, b.String())
			return err
		},
	}
}

// ---------------------------------------------------------------------------
// cve scan
// ---------------------------------------------------------------------------

func newCveScanCommand(deps cveDeps) *Command {
	var f cveFlags
	return &Command{
		Name:    "cve scan",
		Summary: "compute the box's CVE surface and print the report",
		Usage:   "cafe cve scan [--json] [--schedule 1d] [--no-write-state]",
		LongHelp: "Observe the box, ask the advisory feed, classify, report.\n\n" +
			"Every network call is bounded, and every phase emits a checkpoint before and\n" +
			"after it runs, so the last line printed is the phase that is running. A scan that\n" +
			"stops printing is a scan that stopped, which is the only way a stalled fetch is\n" +
			"distinguishable from a slow one.",
		Flags: func(fs *flag.FlagSet) { f.register(fs) },
		Run: func(args []string, env *Env) error {
			if len(args) != 0 {
				return wantArgs("cve scan", "cafe cve scan [--json]", 0, len(args))
			}
			schedule, err := f.scheduleOf()
			if err != nil {
				return fmt.Errorf("cafe cve scan: %w", err)
			}
			source := deps.source
			if source == nil {
				source = cve.HTTPSource{}
			}
			state := deps.statePath
			if f.state != "" {
				state = f.state
			}
			outcome, err := cve.Scan{
				Box:        f.boxOf(),
				Observer:   deps.observer,
				Source:     source,
				Schedule:   schedule,
				StatePath:  state,
				WriteState: !f.noWrite,
				Output:     env.Stdout,
				JSON:       f.json,
				Emit: cve.EmitterFunc(func(phase, detail string) {
					// Stderr, so the machine document on stdout stays one
					// JSON document and a cron run that stalls leaves the
					// phase it stopped in in the mail cron sends. This is
					// the whole answer to "a network call that hangs is an
					// invisible stall": not a faster network, a line.
					_, _ = fmt.Fprintf(env.Stderr, "cafe cve scan: %s: %s\n", phase, detail)
				}),
			}.Run(context.Background())
			if err != nil {
				return fmt.Errorf("cafe cve scan: %w", err)
			}
			// The state write is bookkeeping, not the scan: it happens after
			// the report is in front of the operator, and it is said out loud
			// rather than swallowed, because the only real loss is that the
			// next run may be early.
			if outcome.Err != nil {
				_, _ = fmt.Fprintf(env.Stderr, "cafe cve scan: %v\n", outcome.Err)
			}
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// cve schedule
// ---------------------------------------------------------------------------

func newCveScheduleCommand() *Command {
	var f cveFlags
	return &Command{
		Name:    "cve schedule",
		Summary: "print the cron line that puts the scan on a schedule",
		Usage:   "cafe cve schedule [--schedule 1d]",
		LongHelp: "The line is printed, not installed. cafe does not edit a crontab: that would be cafe\n" +
			"changing how a box behaves without being asked, which is the same property the\n" +
			"Attended tier holds in the other direction — upgrade recommendations the\n" +
			"customer applies.\n\n" +
			"The hour is deliberately not pinned. A fixed minute would put every box in the\n" +
			"fleet on the advisory feed in the same second; cron's step syntax already spreads\n" +
			"it, and spreading it is free.",
		Flags: func(fs *flag.FlagSet) { f.register(fs) },
		Run: func(args []string, env *Env) error {
			if len(args) != 0 {
				return wantArgs("cve schedule", "cafe cve schedule [--schedule 1d]", 0, len(args))
			}
			schedule, err := f.scheduleOf()
			if err != nil {
				return fmt.Errorf("cafe cve schedule: %w", err)
			}
			binary := os.Args[0]
			if binary == "" {
				binary = "cafe"
			}
			_, err = fmt.Fprintf(env.Stdout, "%s\n", cve.CronLine(schedule, binary))
			return err
		},
	}
}

// dash is the package's own (internal/cli/doctor.go): the empty-version mark,
// used here rather than redeclared so the report prints the same "-" the
// doctor report prints for a missing tool.
