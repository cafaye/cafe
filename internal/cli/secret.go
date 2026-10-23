package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/anywaye/cafe/internal/secret"
)

// `cafe secret` is a verb group, for the same reason `cafe contract` and `cafe gen`
// are: the words a person types and the command that runs them are different
// strings, and a help line or a usage error that reads `cafe inventory` instead
// of `cafe secret inventory` has made a person go and look for a command that
// does not exist.
//
// Each verb below is a Command named with its full invocation. They are
// dispatched from the parent's Run and are never registered in `Commands()`,
// which is why `stub_test.go`'s `workingCommands` lists `secret` and not
// `secret inventory`.

// secretVerb pairs the word a person types with the command that runs it.
type secretVerb struct {
	name string
	cmd  *Command
}

func secretVerbs(deps secretDeps) []secretVerb {
	verbs := []secretVerb{
		{name: "inventory", cmd: newSecretInventoryCommand(deps)},
		{name: "rotate", cmd: newSecretRotateCommand(deps)},
	}
	// Dev-secret verbs (list/get/set) use FileRing, not kubectl. They are
	// always available — every box has a local keyring.
	verbs = append(verbs,
		secretVerb{name: "list", cmd: newSecretListCommand(deps.dev)},
		secretVerb{name: "get", cmd: newSecretGetCommand(deps.dev)},
		secretVerb{name: "set", cmd: newSecretSetCommand(deps.dev)},
	)
	return verbs
}

func newSecretCommand(deps secretDeps) *Command {
	verbs := secretVerbs(deps)
	c := &Command{
		Name:     "secret",
		Summary:  "what secrets this box holds, which can be rotated, and rotate them",
		Usage:    "cafe secret <inventory|rotate> [flags] [arguments]",
		LongHelp: secretVerbTable(verbs) + secretLongHelp,
	}
	c.Run = func(args []string, env *Env) error {
		// Bare `cafe secret` is a question ("what can this do?"), not a mistake,
		// so it answers instead of exiting 2 — the same call `cafe contract` and
		// `cafe gen` make.
		if len(args) == 0 {
			printHelp(env.Stdout, c)
			return nil
		}
		verb, found := lookupSecretVerb(verbs, args[0])
		if !found {
			return unknownSecretVerb(args[0], verbs)
		}
		return verb.cmd.Execute(env, args[1:])
	}
	return c
}

func lookupSecretVerb(verbs []secretVerb, name string) (secretVerb, bool) {
	for _, verb := range verbs {
		if verb.name == name {
			return verb, true
		}
	}
	return secretVerb{}, false
}

// unknownSecretVerb names the verbs, because "unknown subcommand" alone leaves
// the person guessing what they could have typed.
func unknownSecretVerb(name string, verbs []secretVerb) error {
	names := make([]string, 0, len(verbs))
	for _, verb := range verbs {
		names = append(names, verb.name)
	}
	return fmt.Errorf("%w: unknown secret subcommand %q (usage: cafe secret <%s>)",
		errUsage, name, strings.Join(names, "|"))
}

// secretVerbTable is the verb list in a command's help. It is one string rather
// than a rendering hook so `cafe secret`, `cafe secret --help` and `cafe help
// secret` cannot drift into three different answers.
func secretVerbTable(verbs []secretVerb) string {
	var table strings.Builder
	table.WriteString("Commands:\n")
	tw := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
	for _, verb := range verbs {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", verb.cmd.Name, verb.cmd.Summary)
	}
	_ = tw.Flush()
	return table.String()
}

const secretLongHelp = `
WHY IT EXISTS. Every box holds the same secrets, because the kit substrate
installer creates the same ones in the same namespaces. What is NOT the same is
which of them can be rotated: muse's vault key seals every stored credential, so
a rotation forks all of them, and identity publishes exactly one key in its
JWKS, so a rotation is a cutover with no overlap window. An operator who does
not know the difference spends a weekend discovering it.

THE THREE CLASSES. rotatable — replace the key in the Kubernetes Secret and
restart the deployment, which is the posture the installer itself records.
conditional — the rotation needs a second write, a re-mint against identity's
API, an ALTER ROLE in Postgres, or a change at the provider. not-rotatable — the
rotation is a fork or a cutover.

A REFUSAL IS NOT A SKIP. cafe secret rotate names every entry it did not touch,
with the reason and the file and line the reason comes from, and exits non-zero.
An inventory that answered "nothing to do" would be how the weekend happens.

WHAT IT NEVER READS. A secret's VALUE. The presence check is templated to print
key names only — it ranges over the secret's data and prints $k, never $v — and
a fresh value travels on stdin to "kubectl apply -f -", never in argv where /proc
would publish it. Nothing in this command can print, log or base64-decode a
value, because no field holds one.

THE SCHEDULE IS A STATE FILE, NOT A CRON LINE. --state-dir records when each
entry was last rotated, and cafe secret inventory reports what is due from it. A
timer nobody can see is not a schedule.

`

// secretDeps is what both verbs need from the world.
//
// The runner is a parameter for the reason backupDeps' and reclaimDeps' are: a
// test can hand the command a runner that records what it ran, which is the only
// reason a rotation, a dry run and a refusal are testable here without a cluster
// anywhere in sight.
type secretDeps struct {
	runner secret.Runner
	// now is the clock. It is a seam because "what is due" is a fact about
	// today, and a test that had to wait ninety days to prove a rotation was
	// due would be a test nobody runs.
	now func() time.Time
	// stateDir is where the schedule lives. Empty means the project directory
	// the command was run from.
	stateDir string
	// boxID is the box the inventory is of, and it rides along in the document
	// so a report about one box is never mistaken for one about another.
	boxID string
	// dev is the wiring for the dev-secret verbs (list/get/set).
	dev secretDevDeps
}

func (d secretDeps) withDefaults(env *Env) secretDeps {
	if d.now == nil {
		d.now = time.Now
	}
	if d.stateDir == "" {
		d.stateDir = "."
	}
	return d
}

// --- cafe secret inventory --------------------------------------------------

func newSecretInventoryCommand(deps secretDeps) *Command {
	opts := &secretInventoryOptions{}
	c := &Command{
		Name:    "secret inventory",
		Summary: "every secret this box holds, its class, and why — as JSON",
		Usage:   "cafe secret inventory [flags]",
		LongHelp: `WHAT IT PRINTS. One JSON document on stdout, with an entry per secret the box holds: its
name, the Kubernetes Secret and key it lives at, its class, the reason for that
class, the file and line the reason comes from, and when this box last rotated
it. A configuration key the installer's record carries is enumerated too, marked
as configuration, because an inventory that quietly dropped a third of the
record would be a second source of truth.

WHAT IT NEVER PRINTS. A value. No field holds one, and no step in this command
reads one.`,
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.stateDir, "state-dir", "", "directory holding the rotation schedule; default the current directory")
			fs.StringVar(&opts.boxID, "box", "", "box id this inventory is of, recorded in the document")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) != 0 {
			return fmt.Errorf("%w: cafe secret inventory takes no arguments (usage: %s)", errUsage, c.Usage)
		}
		d := deps.withDefaults(env)
		d.stateDir = opts.stateDir
		d.boxID = opts.boxID
		state, err := secret.Load(d.stateDir)
		if err != nil {
			return err
		}
		return secret.Render(env.Stdout, d.boxID, state, d.now())
	}
	return c
}

type secretInventoryOptions struct {
	stateDir string
	boxID    string
}

// --- cafe secret rotate -----------------------------------------------------

func newSecretRotateCommand(deps secretDeps) *Command {
	opts := &secretRotateOptions{}
	c := &Command{
		Name:    "secret rotate",
		Summary: "rotate the rotatable class on its schedule, and refuse the rest by name",
		Usage:   "cafe secret rotate [flags] [name]",
		LongHelp: `WHAT IT DOES. Rotates the entries whose class is rotatable: it replaces the key
in the Kubernetes Secret with a freshly generated value in the installer's own
spelling and restarts the deployment that reads it, then records the rotation in
the schedule state so the next run knows when it is due again.

WITH NO ARGUMENT it rotates everything in the rotatable class that is DUE, and
reports what it changed.

WITH ONE ARGUMENT it rotates that entry — and refuses, naming the reason and the
citation, for any other class. A refusal is the answer, not a failure to answer:
MUSE_VAULT_KEY is refused because a rotation forks every stored credential, and
OIDC_SIGNING_KEY is refused because identity's JWKS publishes one key and a
rotation is therefore a cutover. Nothing is skipped silently.

--dry-run prints every step and changes nothing, and it refuses exactly what the
real run refuses, because a dry run more permissive than the real one is worse
than no dry run at all.`,
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&opts.dryRun, "dry-run", false, "print every step that would run; changes nothing")
			fs.BoolVar(&opts.all, "all", false, "rotate the whole rotatable class, not only what is due")
			fs.BoolVar(&opts.yes, "yes", false, "rotate without asking; required where nobody can answer the prompt")
			fs.DurationVar(&opts.timeout, "timeout", 5*time.Minute, "budget for each kubectl command, so a hung apply is a failure rather than a hang")
			fs.StringVar(&opts.stateDir, "state-dir", "", "directory holding the rotation schedule; default the current directory")
			fs.StringVar(&opts.boxID, "box", "", "box id this rotation is of, recorded in the report")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) > 1 {
			return fmt.Errorf("%w: cafe secret rotate wants at most one name (usage: %s)", errUsage, c.Usage)
		}
		return (secretRotateRun{opts: *opts, name: argAt(args, 0), deps: deps, env: env}).run()
	}
	return c
}

func argAt(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

type secretRotateOptions struct {
	dryRun   bool
	all      bool
	yes      bool
	stateDir string
	boxID    string
	timeout  time.Duration
}

type secretRotateRun struct {
	opts secretRotateOptions
	name string
	deps secretDeps
	env  *Env
}

func (r secretRotateRun) run() error {
	r.deps = r.deps.withDefaults(r.env)
	if r.opts.stateDir != "" {
		r.deps.stateDir = r.opts.stateDir
	}
	if r.opts.boxID != "" {
		r.deps.boxID = r.opts.boxID
	}
	state, err := secret.Load(r.deps.stateDir)
	if err != nil {
		return err
	}

	targets, refused, err := r.targets(state)
	if err != nil {
		return err
	}

	// The refusal prose goes to stderr and the JSON report goes to stdout,
	// which is internal/agent's split: a person reads stderr, a script parses
	// stdout, and there is no flag that chooses between them because a flag
	// that chose would guarantee one of the two was wrong in some script.
	for _, refusal := range refused {
		_, _ = fmt.Fprint(r.errOut(), refusal.Human())
	}

	// NOTHING IS DUE and NOTHING IS REFUSED is the one quiet answer, and it is
	// quiet because there is nothing to say: the schedule is not behind on
	// anything.
	if len(targets) == 0 && len(refused) == 0 {
		_, _ = fmt.Fprint(r.out(), "cafe secret: nothing is due. cafe secret inventory says what is held and when each was last rotated.\n")
		return nil
	}

	if r.opts.dryRun {
		for _, entry := range targets {
			plan, err := secret.PlanRotation(entry)
			if err != nil {
				return err
			}
			if err := r.printPlan(plan); err != nil {
				return err
			}
		}
		return r.report(nil, refused)
	}

	ok, err := r.ask(targets)
	if err != nil {
		return err
	}
	if !ok {
		_, _ = fmt.Fprint(r.out(), "cafe secret: nothing was rotated. Run it with --yes when you mean it.\n")
		return errReported
	}

	runner := r.runner()
	var rotated []string
	for _, entry := range targets {
		if _, err := secret.Rotate(r.ctx(), entry.Name, runner, r.errOut()); err != nil {
			var refusal secret.Refusal
			if errors.As(err, &refusal) {
				refused = append(refused, refusal)
				continue
			}
			return err
		}
		state.MarkRotated(entry, r.deps.now())
		rotated = append(rotated, entry.Name)
	}
	for _, refusal := range refused {
		if entry, err := secret.Find(refusal.Name); err == nil {
			state.MarkRefused(entry)
		}
	}
	if err := secret.Save(r.deps.stateDir, state); err != nil {
		return err
	}
	return r.report(rotated, refused)
}

// targets is which entries this invocation rotates, and which it refuses.
//
// With a name, the answer is one entry or one refusal — there is no schedule to
// consult, because naming an entry IS asking for it. With no name it is the
// rotatable class filtered by the schedule, and every entry outside that class
// is named in the refusal report, because an operator who asked "rotate what is
// due" is owed the whole answer — including what will never rotate.
func (r secretRotateRun) targets(state secret.State) ([]secret.Entry, []secret.Refusal, error) {
	var refused []secret.Refusal
	if r.name != "" {
		entry, err := secret.Find(r.name)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %w", errUsage, err)
		}
		if entry.Class != secret.Rotatable {
			return nil, []secret.Refusal{{
				Name: entry.Name, Class: entry.Class, Reason: entry.Reason, Source: entry.Source,
			}}, nil
		}
		return []secret.Entry{entry}, nil, nil
	}

	var out []secret.Entry
	if r.opts.all {
		for _, e := range secret.Inventory() {
			if e.Class == secret.Rotatable {
				out = append(out, e)
			}
		}
	} else {
		out = secret.Due(state, r.deps.now())
	}
	for _, e := range secret.Inventory() {
		if e.Class == secret.Rotatable {
			continue
		}
		refused = append(refused, secret.Refusal{
			Name: e.Name, Class: e.Class, Reason: e.Reason, Source: e.Source,
		})
	}
	return out, refused, nil
}

// report is the JSON answer on stdout. It names what was rotated and what was
// refused, and the exit code carries the verdict: a run that rotated something
// and refused something is a partial run, and a script has to be able to tell.
func (r secretRotateRun) report(rotated []string, refused []secret.Refusal) error {
	// An empty document is noise: a dry run of one rotatable entry has nothing
	// rotated and nothing refused, and "{}" on stdout is a parser's problem
	// rather than an answer. The prose above it is the answer.
	if len(rotated) == 0 && len(refused) == 0 {
		return nil
	}
	doc := struct {
		Rotated []string         `json:"rotated,omitempty"`
		Refused []secret.Refusal `json:"refused,omitempty"`
	}{Rotated: rotated, Refused: refused}
	enc := json.NewEncoder(r.out())
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	if len(refused) > 0 {
		return errReported
	}
	return nil
}

func (r secretRotateRun) runner() secret.Runner {
	if r.deps.runner != nil {
		return r.deps.runner
	}
	k := secret.NewKubectl(r.errOut(), r.errOut())
	k.Timeout = r.opts.timeout
	return k
}

func (r secretRotateRun) ctx() context.Context { return context.Background() }

// printPlan is the dry run: every step, and the document it pipes with the
// generated value replaced by a marker. A dry run that printed the document
// unredacted would print a fresh secret to the terminal, which is exactly the
// thing the value-on-stdin discipline exists to prevent.
func (r secretRotateRun) printPlan(plan secret.Plan) error {
	for _, step := range plan.Steps {
		if _, err := fmt.Fprintf(r.out(), "cafe secret: %s\n", step.Name); err != nil {
			return err
		}
		if step.Stdin == "" {
			_, _ = fmt.Fprintf(r.out(), "  %s\n", strings.Join(step.Argv, " "))
			continue
		}
		_, _ = fmt.Fprintf(r.out(), "  kubectl apply -f - with:\n")
		for _, line := range strings.Split(strings.TrimRight(step.Redacted, "\n"), "\n") {
			_, _ = fmt.Fprintf(r.out(), "    %s\n", line)
		}
	}
	_, _ = fmt.Fprintf(r.out(), "cafe secret: %s (dry run — nothing was changed)\n", plan.Report)
	return nil
}

// ask is the confirmation, and it is the only interactive thing this command
// does. --yes skips it. A dry run never reaches it.
//
// It is here for the reason it is in `cafe backup`: a rotation replaces a live
// secret and restarts the deployment that reads it, which is worth one question
// about. The prompt names the entries, because a prompt that does not say what it
// is about is a prompt people approve without reading.
func (r secretRotateRun) ask(targets []secret.Entry) (bool, error) {
	if r.opts.yes || r.opts.dryRun {
		return true, nil
	}
	if len(targets) == 0 {
		return true, nil
	}
	names := make([]string, 0, len(targets))
	for _, e := range targets {
		names = append(names, e.Name)
	}
	return askOnStdin("cafe secret", fmt.Sprintf(
		"cafe secret: rotate %s, replacing the secret and restarting the deployment? [y/N] ",
		strings.Join(names, ", ")))
}

func (r secretRotateRun) out() io.Writer    { return outOrDiscard(r.env) }
func (r secretRotateRun) errOut() io.Writer { return errOrDiscard(r.env) }
