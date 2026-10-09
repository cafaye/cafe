package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cafaye/caf/internal/box"
)

// boxDeps is what `caf box` needs from the world, and it is a struct of
// parameters rather than a set of package-level variables for the reason
// deployDeps and envDeps are: a test can hand the command a keyring it owns and
// a conductor that counts, which is the only way an enrollment can be driven
// through the real router without a network, a keychain or a real capability.
type boxDeps struct {
	// dir is where box.json, the journal and the event log live. Empty means
	// the resolved default, and a test always sets it.
	dir string
	// ring holds the private key. Nil means a file ring under the config
	// directory, and a test always sets it.
	ring box.Keyring
	// cond redeems the capability. Nil means a real HTTP client, which is why
	// a test always sets it: the whole point of the seam is the count.
	cond box.Conductor
	// url is the conductor endpoint. Empty means the CAF_CONDUCTOR_URL
	// environment variable, and an empty environment variable is a refusal
	// rather than a guess.
	url string
}

// defaultConfigDir is the config directory a box's non-secrets and an agent's
// non-secrets live in. It is resolved here rather than read from a global so
// the only function that knows about the home directory is the one that
// resolves a default — and it is ONE function for both subjects, because two
// of them is how the agent's config ends up in a different directory from the
// box's.
func defaultConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve the config directory: %w", err)
	}
	return filepath.Join(base, "caf"), nil
}

// conductorURLFromEnv is where the endpoint comes from when no flag names one.
// An environment variable rather than a flag default, because a flag default
// baked into the binary is a default that goes stale when conductor moves and
// nobody rebuilds caf to find out.
const conductorURLEnv = "CAF_CONDUCTOR_URL"

func (d boxDeps) withDefaults() boxDeps {
	if d.dir == "" {
		if dir, err := defaultConfigDir(); err == nil {
			d.dir = dir
		}
	}
	if d.url == "" {
		d.url = os.Getenv(conductorURLEnv)
	}
	if d.ring == nil {
		d.ring = box.FileRing{Dir: filepath.Join(d.dir, "keyring")}
	}
	if d.cond == nil {
		d.cond = box.HTTPConductor{BaseURL: d.url}
	}
	return d
}

// store builds the store the verbs share. It is a method so every verb gets
// the same defaults, and so a test that injects a ring sees it in every one of
// them.
func (d boxDeps) store() *box.Store {
	d = d.withDefaults()
	return box.NewStore(d.dir, d.ring, d.cond)
}

// boxVerb is one word under `caf box`, paired with the command that runs it.
// The same shape `caf contract` uses: the word is what the dispatcher looks
// up, and the command is named with its full invocation so its help and its
// usage errors read as the words the person typed.
type boxVerb struct {
	name string
	cmd  *Command
}

// boxVerbs takes the deps so one injected keyring and one injected conductor
// reach every verb. The parent hands them down; the router hands the parent the
// zero value and gets the real wiring.
func boxVerbs(deps boxDeps) []boxVerb {
	return []boxVerb{
		{name: "apply", cmd: newBoxApplyCommand(deps)},
		{name: "enroll", cmd: newBoxEnrollCommand(deps)},
		{name: "logs", cmd: newBoxLogsCommand(deps)},
		{name: "plan", cmd: newBoxPlanCommand(deps)},
		{name: "status", cmd: newBoxStatusCommand(deps)},
		{name: "verify", cmd: newBoxVerifyCommand(deps)},
	}
}

// boxLongHelp is the prose under `caf box`'s usage line. It is here because a
// person reads it before typing a command that spends a single-use capability
// on their behalf, and the split it explains — the box is a machine, `caf
// agent` is a principal — is the thing a newcomer gets wrong.
const boxLongHelp = `The box is a MACHINE: it has a keypair, it enrols, it runs the substrate.
` + "`caf agent …`" + ` is a principal — an identity that becomes claimed. Two
subjects, two verbs, and the split is the one moon/AGENTS-FIRST.md chose so
that ` + "`caf box register`" + ` never reads as "the agent is registering a box".

ENROL is the one command that spends anything. It generates the box's keypair,
stores the private half in the keyring, and presents the capability to
conductor. The capability is single-use, box-bound and lives minutes
(conductor's Capability::TTL_MINUTES), so the token is spent exactly once and
cannot be re-spent — which is why every step is written down BEFORE it is
taken, and why a retry that finds a recorded redemption finishes from the
record instead of presenting the token again.

` + "`caf box enroll`" + ` is safe to re-run. An already-enrolled box reports
that it is already enrolled and touches nothing: no keypair is regenerated, no
capability is presented, the identity is not rewritten. A run that died
between presenting the capability and writing the identity is finished on the
next attempt without presenting it again.

WHAT THE LIFECYCLE COMMANDS DO AND DO NOT DO. They own the RECORD — which
phase the box has reached, when, on what evidence, and what the next handoff
is. kit's installer performs the phases (W2-P4) and ` + "`caf boxd`" + ` drives it
(W2-P7); neither exists yet, and a command that claimed a phase completed when
nothing ran it would be a command nobody could trust. ` + "`caf box apply`" + `
records the phase and prints the handoff that performs it.

THE PRIVATE KEY IS NEVER PRINTED. It lives in the keyring and the only thing
any of these commands says about it is whether it is there. The non-secrets
are in ~/.config/caf/box.json and are safe to read, share and paste.

stdout carries the document, stderr carries the sentence. A machine reads one;
a person reads the other, and the split is why ` + "`--json`" + ` exists on every
verb.`

func newBoxCommand(deps boxDeps) *Command {
	verbs := boxVerbs(deps)
	c := &Command{
		Name:     "box",
		Summary:  "the box lifecycle: enroll, then status, plan, apply, logs and verify",
		Usage:    "caf box <enroll|status|plan|apply|logs|verify> [flags] [arguments]",
		LongHelp: boxLongHelp,
	}
	c.Run = func(args []string, env *Env) error {
		// Bare `caf box` is a question, not a mistake.
		if len(args) == 0 {
			printHelp(env.Stdout, c)
			return nil
		}
		verb, found := lookupBoxVerb(verbs, args[0])
		if !found {
			return unknownBoxVerb(args[0], verbs)
		}
		return verb.cmd.Execute(env, args[1:])
	}
	return c
}

func lookupBoxVerb(verbs []boxVerb, name string) (boxVerb, bool) {
	for _, v := range verbs {
		if v.name == name {
			return v, true
		}
	}
	return boxVerb{}, false
}

// unknownBoxVerb is a usage error: the person typed a word caf does not have,
// and the answer is the list rather than a stack trace.
func unknownBoxVerb(name string, verbs []boxVerb) error {
	names := make([]string, len(verbs))
	for i, v := range verbs {
		names[i] = v.name
	}
	return fmt.Errorf("%w: caf box %q is not a command; the verbs are %s", errUsage, name, strings.Join(names, ", "))
}

// boxFlags is the flags every lifecycle verb shares. `--json` is on all six
// rather than on the one the brief names, because a flag that appears on one
// verb and not its siblings is a flag a caller has to look up every time.
type boxFlags struct {
	json bool
}

func (f *boxFlags) declare(fs *flag.FlagSet) {
	fs.BoolVar(&f.json, "json", false, "print the machine document on stdout instead of the human one")
}

// boxEnrollOptions is `caf box enroll`'s flag state.
type boxEnrollOptions struct {
	token     string
	conductor string
}

func newBoxEnrollCommand(deps boxDeps) *Command {
	opts := &boxEnrollOptions{}
	c := &Command{
		Name:    "box enroll",
		Summary: "enroll this box: generate its keypair, redeem the capability, store its identity",
		Usage:   "caf box enroll --token <capability> [--conductor <url>]",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.token, "token", "", "the single-use box.enroll capability conductor minted for this box")
			fs.StringVar(&opts.conductor, "conductor", "", "conductor's origin; without it, "+conductorURLEnv+" is read and an unset variable is a refusal")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		// A missing token is a USAGE mistake, not a failure: the command cannot
		// be run at all without it, and the distinction is the difference between
		// exit 2 and exit 1 in every script that calls this.
		if strings.TrimSpace(opts.token) == "" {
			return fmt.Errorf("%w: caf box enroll wants --token <capability>, the single-use box.enroll capability conductor minted for this box (usage: %s)", errUsage, c.Usage)
		}
		url := opts.conductor
		if url == "" {
			url = os.Getenv(conductorURLEnv)
		}
		deps.url = url
		store := boxStoreFor(c, env, deps)
		res, err := store.Enroll(env.Context, opts.token, url)
		if err != nil {
			// The refusal is the honest half of the design: a capability that
			// is gone is gone, and saying so is better than a client that
			// reported success on a box that did not enroll.
			fmt.Fprintf(env.Stderr, "caf box enroll: %v\n", err)
			fmt.Fprintf(env.Stderr, "caf box enroll: the capability was NOT spent twice and nothing was written; mint a fresh token and re-run\n")
			return errReported
		}
		say(env.Stderr, res.Message)
		return writeJSONDoc(env.Stdout, res)
	}
	return c
}

// boxStoreFor builds the store every verb runs against. The `box` parent hands
// its deps down through the verbs' constructors, so one injected ring and one
// injected conductor are what the whole tree of subcommands sees — which is
// the property the CLI-level retry test relies on, because the count it
// asserts is the count across TWO INVOCATIONS of the command.
func boxStoreFor(_ *Command, _ *Env, deps boxDeps) *box.Store {
	return deps.store()
}

// say is the stderr half of the split: one sentence, for a person.
func say(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "caf box: "+format+"\n", args...)
}

// writeJSONDoc is the stdout half: the document, one per line of JSON, so a
// shell can read it with `caf box status --json | jq`. It is indented because a
// document a person has to read in a terminal is a document a person has to
// be able to see the shape of.
func writeJSONDoc(w io.Writer, v any) error {
	return writeDoc(w, v, true)
}

// writeDoc renders one document either as JSON or as a human block. The
// human block is deliberately plain: this repository has no TUI framework and
// `caf dev` prints plain ordered progress for the same reason.
func writeDoc(w io.Writer, v any, asJSON bool) error {
	if asJSON {
		if err := writeJSON(v, w); err != nil {
			return err
		}
		return nil
	}
	switch doc := v.(type) {
	case box.EnrollResult:
		return writeHuman(w, doc.Message)
	case *box.Status:
		return writeStatusHuman(w, doc)
	case *box.Plan:
		return writePlanHuman(w, doc)
	case *box.ApplyResult:
		return writeHuman(w, doc.Message)
	case []box.Event:
		return writeEventsHuman(w, doc)
	case *box.Verify:
		return writeVerifyHuman(w, doc)
	}
	return writeJSON(v, w)
}

func newBoxStatusCommand(deps boxDeps) *Command {
	f := &boxFlags{}
	c := &Command{
		Name:    "box status",
		Summary: "where this box is: its identity, its phase and the next step",
		Usage:   "caf box status [--json]",
		Flags:   func(fs *flag.FlagSet) { f.declare(fs) },
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		store := boxStoreFor(c, env, deps)
		st, err := store.Status()
		if err != nil {
			return err
		}
		if !st.Enrolled {
			// An unenrolled box is not an error, it is a status, and the
			// journal is the one fact worth having.
			if st.Journal != "" {
				say(env.Stderr, "this box is not enrolled and an enrollment is unfinished (state %s); re-run `caf box enroll` to finish it", st.Journal)
			} else {
				say(env.Stderr, "this box is not enrolled; run `caf box enroll --token …`")
			}
			return writeDoc(env.Stdout, st, f.json)
		}
		say(env.Stderr, "box %s is at phase %s", st.Identity.BoxID, st.Identity.Phase)
		return writeDoc(env.Stdout, st, f.json)
	}
	return c
}

func newBoxPlanCommand(deps boxDeps) *Command {
	f := &boxFlags{}
	c := &Command{
		Name:    "box plan",
		Summary: "the install, phase by phase, with this box's position marked",
		Usage:   "caf box plan [--json]",
		Flags:   func(fs *flag.FlagSet) { f.declare(fs) },
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		plan, err := boxStoreFor(c, env, deps).Plan()
		if err != nil {
			return err
		}
		if plan.Next != "" {
			say(env.Stderr, "next phase: %s", plan.Next)
		} else {
			say(env.Stderr, "every phase has been reached")
		}
		return writeDoc(env.Stdout, plan, f.json)
	}
	return c
}

// boxApplyOptions is `caf box apply`'s flag state. `--phase` takes a name or an
// index, because the brief says `apply --phase N` and a number is what a script
// counts with; the name is what a person types.
type boxApplyOptions struct {
	phase string
}

func newBoxApplyCommand(deps boxDeps) *Command {
	opts := &boxApplyOptions{}
	f := &boxFlags{}
	c := &Command{
		Name:    "box apply",
		Summary: "record that the box has reached a phase; refuses a phase that is not next",
		Usage:   "caf box apply [--json] [--phase <name|index>]",
		Flags: func(fs *flag.FlagSet) {
			f.declare(fs)
			fs.StringVar(&opts.phase, "phase", "", "the phase to take the box to, by name or by index in `caf box plan`")
		},
	}
	c.Run = func(args []string, env *Env) error {
		// One optional positional, because `caf box apply substrate` is what a
		// person types and `caf box apply --phase substrate` is what a script
		// writes. Both are the same command.
		if len(args) > 1 {
			return wantArgs(c.Name, c.Usage, 1, len(args))
		}
		phase := opts.phase
		if len(args) == 1 {
			if phase != "" {
				return fmt.Errorf("%w: caf box apply got the phase twice: --phase %q and the argument %q (usage: %s)", errUsage, phase, args[0], c.Usage)
			}
			phase = args[0]
		}
		phase = resolvePhaseArg(phase)
		if phase == "" {
			return fmt.Errorf("%w: caf box apply wants --phase <name|index> or one positional argument (usage: %s)", errUsage, c.Usage)
		}
		res, err := boxStoreFor(c, env, deps).Apply(phase)
		if err != nil {
			// A phase that is not in the table is a usage mistake, not a failure:
			// the box was never asked to do anything it could misread.
			if errors.Is(err, box.ErrNoSuchPhase) {
				return fmt.Errorf("%w: %v", errUsage, err)
			}
			return err
		}
		say(env.Stderr, res.Message)
		if res.Handoff != "" && res.Changed {
			fmt.Fprintf(env.Stderr, "caf box: this records the phase; %s is what performs it\n", res.Handoff)
		}
		return writeDoc(env.Stdout, res, f.json)
	}
	return c
}

// resolvePhaseArg turns an index into a name. A number is the shape the brief
// asks for and a name is the shape a person types; an index outside the table
// is a usage error rather than a silent clamp, because phase 99 silently
// becoming phase 0 is how a box ends up reporting a state nobody chose.
func resolvePhaseArg(phase string) string {
	phase = strings.TrimSpace(phase)
	if phase == "" {
		return ""
	}
	if n, err := strconv.Atoi(phase); err == nil {
		if n < 0 || n >= len(box.Phases) {
			return phase // left as-is: Apply reports the table's names
		}
		return box.Phases[n].Name
	}
	return phase
}

func newBoxLogsCommand(deps boxDeps) *Command {
	f := &boxFlags{}
	c := &Command{
		Name:    "box logs",
		Summary: "the box's own log: what happened, when, and to which phase",
		Usage:   "caf box logs [--json]",
		Flags:   func(fs *flag.FlagSet) { f.declare(fs) },
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		events, err := boxStoreFor(c, env, deps).Logs()
		if err != nil {
			return err
		}
		if len(events) == 0 {
			say(env.Stderr, "no events yet")
		} else {
			say(env.Stderr, "%d events", len(events))
		}
		return writeDoc(env.Stdout, events, f.json)
	}
	return c
}

func newBoxVerifyCommand(deps boxDeps) *Command {
	f := &boxFlags{}
	c := &Command{
		Name:    "box verify",
		Summary: "the box's report on itself: identity, key, journal and phase",
		Usage:   "caf box verify [--json]",
		Flags:   func(fs *flag.FlagSet) { f.declare(fs) },
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		v, err := boxStoreFor(c, env, deps).Verify()
		if err != nil {
			return err
		}
		if v.OK {
			say(env.Stderr, "box %s verifies", v.BoxID)
			return writeDoc(env.Stdout, v, f.json)
		}
		say(env.Stderr, "box %s does not verify: %s", v.BoxID, v.Reason)
		// The verdict is the document; printing it again on stderr would put
		// the same sentence in two places, one of which a CI log greps.
		if err := writeDoc(env.Stdout, v, f.json); err != nil {
			return err
		}
		return errReported
	}
	return c
}
