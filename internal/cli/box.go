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

	"github.com/cafaye/cafe/internal/box"
	"github.com/cafaye/cafe/internal/setup"
	"github.com/cafaye/cafe/internal/verify"
)

// boxDeps is what `cafe box` needs from the world, and it is a struct of
// parameters rather than a set of package-level variables for the reason
// deployDeps and envDeps are: a test can hand the command a keyring it owns and
// a control plane that counts, which is the only way an enrollment can be driven
// through the real router without a network, a keychain or a real capability.
type boxDeps struct {
	// dir is where box.json, the journal and the event log live. Empty means
	// the resolved box-owned default (defaultBoxDir), and a test always
	// sets it. It is box-owned — /var/lib/cafe/box — rather than the user's
	// config dir, because a box is a server: its identity must resolve the
	// same for every HOME that invokes it.
	dir string
	// stateDir is where the box's own state lives: the release directories,
	// the `current` symlink and the edge's routing record. Empty means the
	// resolved default, and a test always sets it.
	//
	// It is a separate field from dir rather than derived from it, because
	// the two are different things in different places: ~/.config holds the
	// operator's non-secret settings and /var/lib holds the machine's state,
	// and a release directory written into the first would be a deploy that
	// disappeared with a dotfile cleanup.
	stateDir string
	// ring holds the private key. Nil means a file ring under the box-owned
	// store directory, and a test always sets it.
	ring box.Keyring
	// controlPlane redeems the capability. Nil means a real HTTP client, which is why
	// a test always sets it: the whole point of the seam is the count.
	controlPlane box.ControlPlane
	// url is the box endpoint. Empty means the CAF_BOX_URL
	// environment variable, and an empty environment variable is a refusal
	// rather than a guess.
	url string
	// setup is what `cafe box setup` needs from the world: a host to reach and
	// a binary to install. It is a field rather than a second command's own
	// wiring because the box verbs share one set of seams, and a verb that
	// reached around its parent would be a verb no test could drive through
	// the real router.
	setup boxSetupDeps
	// wipe is the setup verb's mirror: a host to reach and a confirmation to
	// ask. It is separate from setup because the two carry different things —
	// setup needs a binary and a capability, wipe needs a confirmation — and a
	// struct that carried both would let a test that only wanted one hand the
	// other a nil it did not mean.
	wipe boxWipeDeps
	// stack is the deploy verbs' half of the deps: the box's own stack root
	// and the two seams a test injects. It is separate from the lifecycle
	// half because it is separate state — a stack root and a keyring have
	// nothing to do with each other — and a struct that carried both would
	// let a test that only wanted one hand back a nil for the other.
	stack boxStackDeps
	// stdin is where a value arrives when a verb needs one. It is a field
	// rather than os.Stdin for the reason failureDeps carries its own: a
	// secret never reaches argv, so it arrives on a stream, and a stream the
	// test cannot replace is a value the test cannot prove stayed off argv.
	stdin io.Reader
}

// defaultConfigDir is the config directory an AGENT's non-secrets live in. It
// is resolved here rather than read from a global so the only function that
// knows about the home directory is the one that resolves a default — and it
// is the USER subject's default only. An agent principal belongs to whoever
// runs it, so ~/.config is exactly right for it; the BOX subject's default is
// defaultBoxDir instead. One function per subject, because one function for
// both is how the box's identity ended up wherever the enrolling shell's HOME
// happened to point.
func defaultConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve the config directory: %w", err)
	}
	return filepath.Join(base, "cafe"), nil
}

// defaultBoxStateDir is where the box's own state lives when nothing names
// one: the release directories, the `current` symlink and the edge's routing
// record. It is under /var/lib rather than under the config directory for the
// reason boxDeps.stateDir gives, and it is a function rather than a constant so
// a test can see the shape without the path being baked into a comparison.
func defaultBoxStateDir() string {
	return filepath.Join(string(os.PathSeparator)+"var", "lib", "cafe")
}

// boxDirEnv overrides the box store directory the way CAF_BOX_URL overrides
// the endpoint: an environment variable rather than a flag default, because a
// flag default baked into the binary is a default that goes stale when the
// layout moves and nobody rebuilds cafe to find out. It is for tests and
// unusual layouts; an unset one means the box-owned default below.
const boxDirEnv = "CAF_BOX_DIR"

// defaultBoxDir is where a box's identity, journal, event log, client
// credential and keyring live when nothing names one: under /var/lib/cafe,
// beside the release state — never under a user's config dir. A box is a
// SERVER, not a user session: its identity must resolve the same for every
// HOME that invokes it, which a $HOME-derived path cannot do. It is a function
// rather than a constant so tests can see the shape without the path being
// baked into a comparison, and so the override above has one place to live.
func defaultBoxDir() string {
	if dir := strings.TrimSpace(os.Getenv(boxDirEnv)); dir != "" {
		return dir
	}
	return filepath.Join(defaultBoxStateDir(), "box")
}

// legacyBoxDir is where enrollments before the box-owned default wrote: the
// user's config dir, wherever the enrolling shell's HOME pointed. Nothing
// resolves a default to it anymore; only the migration reads it, once, to
// move what is there.
func legacyBoxDir() (string, error) {
	return defaultConfigDir()
}

// boxURLEnv is where the endpoint comes from when no flag names one.
// An environment variable rather than a flag default, because a flag default
// baked into the binary is a default that goes stale when box moves and
// nobody rebuilds cafe to find out.
const boxURLEnv = "CAF_BOX_URL"

func (d boxDeps) withDefaults() boxDeps {
	if d.dir == "" {
		d.dir = defaultBoxDir()
	}
	if d.stateDir == "" {
		d.stateDir = defaultBoxStateDir()
	}
	if d.url == "" {
		d.url = os.Getenv(boxURLEnv)
	}
	if d.ring == nil {
		d.ring = box.FileRing{Dir: filepath.Join(d.dir, "keyring")}
	}
	if d.controlPlane == nil {
		d.controlPlane = box.HTTPControlPlane{BaseURL: d.url}
	}
	d.stack = d.stack.withDefaults()
	if d.stdin == nil {
		d.stdin = os.Stdin
	}
	return d
}

// store builds the store the verbs share. It is a method so every verb gets
// the same defaults, and so a test that injects a ring sees it in every one of
// them.
func (d boxDeps) store() *box.Store {
	d = d.withDefaults()
	return box.NewStore(d.dir, d.ring, d.controlPlane)
}

// boxVerb is one word under `cafe box`, paired with the command that runs it.
// The same shape `cafe contract` uses: the word is what the dispatcher looks
// up, and the command is named with its full invocation so its help and its
// usage errors read as the words the person typed.
type boxVerb struct {
	name string
	cmd  *Command
}

// boxVerbs takes the deps so one injected keyring and one injected control plane
// reach every verb. The parent hands them down; the router hands the parent the
// zero value and gets the real wiring.
//
// The three edge verbs are appended rather than interleaved: `edge`, `publish`
// and `rollback` are the deploy path's own words (moon/PLAN-names-urls-rollout.md
// §4, "what box ships for this phase"), and a reader scanning the list should
// see the lifecycle verbs together and the deploy verbs together.
func boxVerbs(deps boxDeps) []boxVerb {
	return append([]boxVerb{
		{name: "apply", cmd: newBoxApplyCommand(deps)},
		{name: "deploy", cmd: newBoxDeployCommand(deps)},
		{name: "enroll", cmd: newBoxEnrollCommand(deps)},
		{name: "init", cmd: newBoxInitCommand()},
		{name: "logs", cmd: newBoxLogsCommand(deps)},
		{name: "plan", cmd: newBoxPlanCommand(deps)},
		{name: "setup", cmd: newBoxSetupCommand(deps.setup)},
		{name: "wipe", cmd: newBoxWipeCommand(deps.wipe)},
		{name: "rollback", cmd: newBoxRollbackCommand(deps)},
		{name: "status", cmd: newBoxStatusCommand(deps)},
		{name: "verify", cmd: newBoxVerifyCommand(deps)},
	}, boxEdgeVerbs(deps)...)
}

// boxLongHelp is the prose under `cafe box`'s usage line. It is here because a
// person reads it before typing a command that spends a single-use capability
// on their behalf, and the split it explains — the box is a machine, `cafe
// agent` is a principal — is the thing a newcomer gets wrong.
const boxLongHelp = `The box is a MACHINE: it has a keypair, it enrolls, it runs the substrate.
` + "`cafe agent …`" + ` is a principal — an identity that becomes claimed. Two
subjects, two verbs, and the split is the one moon/AGENTS-FIRST.md chose so
that ` + "`cafe box register`" + ` never reads as "the agent is registering a box".

ENROLL is the one command that spends anything. It generates the box's keypair,
stores the private half in the keyring, and presents the capability to
box. The capability is single-use, box-bound and lives minutes
(box's Capability::TTL_MINUTES), so the token is spent exactly once and
cannot be re-spent — which is why every step is written down BEFORE it is
taken, and why a retry that finds a recorded redemption finishes from the
record instead of presenting the token again.

` + "`cafe box enroll`" + ` is safe to re-run. An already-enrolled box reports
that it is already enrolled and touches nothing: no keypair is regenerated, no
capability is presented, the identity is not rewritten. A run that died
between presenting the capability and writing the identity is finished on the
next attempt without presenting it again.

THE CAPABILITY ARRIVES ON A PIPE, NOT ON A COMMAND LINE. A token typed as an
argument is a token in the shell's history, in the process table for as long
as the process lives, and in the scrollback of whoever looked over the
operator's shoulder. ` + "`--token -`" + ` reads it from stdin, and stdin is
what ` + "`cafe box setup`" + ` hands it to when it runs this command ON THE
HOST: the capability crosses the SSH connection inside that one command's
input and appears in nothing else — not in an argument, not in a file the
setup writes.

THE EDGE VERBS ARE THE DEPLOY PATH. ` + "`cafe box publish <id> <dir>`" + ` writes a
release into the box's own state directory and flips ` + "`current`" + ` at it with a
rename, so the switch is atomic: a request either reaches the old release or
the new one, never a directory that is half-written. ` + "`cafe box rollback`" + `
flips it back one release. ` + "`cafe box edge`" + ` reads what the box serves —
which hostnames, which upstreams, what health the edge last observed, and
which release is behind ` + "`current`" + `.

None of the three talks to box. They read and write the box's own filesystem,
which is what makes a rollback work on the day a deploy is broken and the
network is the problem. There is no ingress, no chart and no namespace in any
of it: the plan's Phase B answer to kamal's per-box proxy is boxd's embedded
edge, and a customer who has to learn a Kubernetes word to deploy their app
has been failed by the product rather than by the code.

THE PRIVATE KEY IS NEVER PRINTED. It lives in the keyring and the only thing
any of these commands says about it is whether it is there. The non-secrets
are in the box-owned store (/var/lib/cafe/box/box.json, or $CAF_BOX_DIR when
that names one) and are safe to read, share and paste.

stdout carries the document, stderr carries the sentence. A machine reads one;
a person reads the other, and the split is why ` + "`--json`" + ` exists on every
verb.`

func newBoxCommand(deps boxDeps) *Command {
	verbs := boxVerbs(deps)
	c := &Command{
		Name:     "box",
		Summary:  "the box lifecycle: enroll, then status, plan, apply, setup, wipe, deploy, rollback, logs, verify — and the edge: publish, edge",
		Usage:    "cafe box <enroll|status|plan|apply|setup|wipe|deploy|rollback|logs|verify|publish|edge> [flags] [arguments]",
		LongHelp: boxLongHelp,
	}
	c.Run = func(args []string, env *Env) error {
		// Bare `cafe box` is a question, not a mistake.
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

// unknownBoxVerb is a usage error: the person typed a word cafe does not have,
// and the answer is the list rather than a stack trace.
func unknownBoxVerb(name string, verbs []boxVerb) error {
	names := make([]string, len(verbs))
	for i, v := range verbs {
		names[i] = v.name
	}
	return fmt.Errorf("%w: cafe box %q is not a command; the verbs are %s", errUsage, name, strings.Join(names, ", "))
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

// boxEnrollOptions is `cafe box enroll`'s flag state.
type boxEnrollOptions struct {
	token string
	box   string
}

const (
	// stdinTokenDashes is the flag value that means "the value is on stdin". It
	// is the operator's own word for a pipe, and the same convention `cafe box
	// setup` uses for the capability it is given.
	stdinTokenDashes = "-"
)

// enrollCapability resolves the capability: `--token -` means "this comes on
// stdin", and every other value is the flag's own, trimmed.
//
// The dash is the operator's word for a pipe and it was never a capability.
// `cafe box setup` runs this command ON THE HOST with the capability on the pipe
// it hands the enroll step and nowhere else (internal/setup/plan.go: `cafe box
// enroll --token - --box <url>`, Stdin is the capability), so the host's own
// enroll has to read the stream it was handed. When it did not, it sent the
// literal "-" to box's redeem lookup, box answered 404 for a token it had never
// minted, and setup failed at box-enrolled with "box does not know this
// capability" — a sentence naming box's database rather than the host's command,
// which is why it took a measurement to find.
//
// readerOrStdin is the seam `Run` already fills in: a nil Stdin is the process's
// own standard input here exactly as it is there, and a test can hand the
// command a reader instead of redirecting the test binary's stdin.
func enrollCapability(env *Env, fromFlag string) (string, error) {
	if value := strings.TrimSpace(fromFlag); value != stdinTokenDashes {
		return value, nil
	}
	raw, err := io.ReadAll(readerOrStdin(env.Stdin))
	if err != nil {
		// A stream that failed is a failure, not a usage mistake: the command
		// was asked to read something and could not, and exit 1 says so.
		return "", fmt.Errorf("cafe box enroll: read the capability from stdin: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// boxEnrollUsage is the refusal for a capability that never arrived. It is a
// USAGE mistake rather than a failure because the command cannot be run at all
// without it, and the distinction is the difference between exit 2 and exit 1 in
// every script that calls this. An empty pipe is the same mistake with a stream
// attached: nothing was attempted, so nothing can have gone wrong.
func boxEnrollUsage(c *Command) error {
	return fmt.Errorf("%w: cafe box enroll wants --token <capability>, the single-use box.enroll capability minted for this box — or `--token -`, which reads it from stdin (usage: %s)", errUsage, c.Usage)
}

func newBoxEnrollCommand(deps boxDeps) *Command {
	opts := &boxEnrollOptions{}
	c := &Command{
		Name:    "box enroll",
		Summary: "enroll this box: generate its keypair, redeem the capability, store its identity",
		Usage:   "cafe box enroll --token <capability|-> [--box <url>]",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.token, "token", "", "the single-use box.enroll capability minted for this box; - reads it from stdin, which is a pipe rather than argv")
			fs.StringVar(&opts.box, "box", "", "box's origin; without it, "+boxURLEnv+" is read and an unset variable is a refusal")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		token, err := enrollCapability(env, opts.token)
		if err != nil {
			return err
		}
		if token == "" {
			return boxEnrollUsage(c)
		}
		url := opts.box
		if url == "" {
			url = os.Getenv(boxURLEnv)
		}
		deps.url = url
		store, err := boxStoreFor(c, env, deps)
		if err != nil {
			return err
		}
		res, err := store.Enroll(env.Context, token, url)
		if err != nil {
			// The refusal is the honest half of the design: a capability that
			// is gone is gone, and saying so is better than a client that
			// reported success on a box that did not enroll.
			_, _ = fmt.Fprintf(env.Stderr, "cafe box enroll: %v\n", err)
			_, _ = fmt.Fprintf(env.Stderr, "cafe box enroll: the capability was NOT spent twice and nothing was written; mint a fresh token and re-run\n")
			return errReported
		}
		say(env.Stderr, res.Message)
		return writeJSONDoc(env.Stdout, res)
	}
	return c
}

// boxStoreFor builds the store every verb runs against. The `box` parent hands
// its deps down through the verbs' constructors, so one injected ring and one
// injected control plane are what the whole tree of subcommands sees — which is
// the property the CLI-level retry test relies on, because the count it
// asserts is the count across TWO INVOCATIONS of the command.
//
// It also runs the legacy migration, once, when the caller left the directory
// to the default: a box enrolled under the old HOME-derived location moves
// itself on first use, with a sentence on stderr naming what moved and from
// where. An explicitly set directory (which is what every test hands in) is
// never migrated: it belongs to the caller, and a migration that reached into
// it would touch state the caller did not offer. A diverged store — both
// locations holding different identities — is a refusal, not a preference.
func boxStoreFor(_ *Command, env *Env, deps boxDeps) (*box.Store, error) {
	if deps.dir == "" {
		deps = deps.withDefaults()
		if legacy, err := legacyBoxDir(); err == nil {
			msg, merr := box.MigrateStore(deps.dir, legacy)
			if merr != nil {
				return nil, merr
			}
			if msg != "" {
				say(env.Stderr, "%s", msg)
			}
		}
	}
	return deps.store(), nil
}

// say is the stderr half of the split: one sentence, for a person.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, "cafe box: "+format+"\n", args...)
}

// writeJSONDoc is the stdout half: the document, one per line of JSON, so a
// shell can read it with `cafe box status --json | jq`. It is indented because a
// document a person has to read in a terminal is a document a person has to
// be able to see the shape of.
func writeJSONDoc(w io.Writer, v any) error {
	return writeDoc(w, v, true)
}

// writeDoc renders one document either as JSON or as a human block. The
// human block is deliberately plain: this repository has no TUI framework and
// `cafe dev` prints plain ordered progress for the same reason.
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
	case setup.Result:
		return writeSetupHuman(w, doc)
	case setup.WipeResult:
		return writeWipeHuman(w, doc)
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

// writeSetupHuman renders the human half: the steps and the box this host now
// holds. Plain text, because this repository has no TUI framework and `cafe
// dev` prints plain ordered progress for the same reason.
func writeSetupHuman(w io.Writer, res setup.Result) error {
	if _, err := fmt.Fprintf(w, "cafe box setup %s\n", res.Host); err != nil {
		return err
	}
	for _, step := range res.Steps {
		if _, err := fmt.Fprintf(w, "  %-18s %-13s %s\n", step.Name, step.Action, step.Title); err != nil {
			return err
		}
	}
	if res.Enrolled != nil {
		if _, err := fmt.Fprintf(w, "  box %s enrolled against %s\n", res.Enrolled.BoxID, res.Enrolled.ControlPlane); err != nil {
			return err
		}
	}
	if res.Report != nil {
		if _, err := fmt.Fprintf(w, "  report: health %s, certificate %s, disk %s\n",
			res.Report.Health.Status, res.Report.Certificate.Status, res.Report.Disk.Status); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "  capabilities presented: %d\n", res.Redemptions)
	return err
}

func newBoxStatusCommand(deps boxDeps) *Command {
	f := &boxFlags{}
	c := &Command{
		Name:    "box status",
		Summary: "where this box is: its identity, its phase and the next step",
		Usage:   "cafe box status [--json]",
		Flags:   func(fs *flag.FlagSet) { f.declare(fs) },
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		store, err := boxStoreFor(c, env, deps)
		if err != nil {
			return err
		}
		st, err := store.Status()
		if err != nil {
			return err
		}
		if !st.Enrolled {
			// An unenrolled box is not an error, it is a status, and the
			// journal is the one fact worth having.
			if st.Journal != "" {
				say(env.Stderr, "this box is not enrolled and an enrollment is unfinished (state %s); re-run `cafe box enroll` to finish it", st.Journal)
			} else {
				say(env.Stderr, "this box is not enrolled; run `cafe box enroll --token …`")
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
		Usage:   "cafe box plan [--json]",
		Flags:   func(fs *flag.FlagSet) { f.declare(fs) },
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		store, err := boxStoreFor(c, env, deps)
		if err != nil {
			return err
		}
		plan, err := store.Plan()
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

// boxApplyOptions is `cafe box apply`'s flag state. `--phase` takes a name or an
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
		Usage:   "cafe box apply [--json] [--phase <name|index>]",
		// Interspersed, because the phase is an optional positional subject:
		// `cafe box apply substrate --json` and `cafe box apply --json
		// substrate` are the same command, the way they are for setup and
		// wipe. See box_setup.go for the whole argument.
		Interspersed: true,
		Flags: func(fs *flag.FlagSet) {
			f.declare(fs)
			fs.StringVar(&opts.phase, "phase", "", "the phase to take the box to, by name or by index in `cafe box plan`")
		},
	}
	c.Run = func(args []string, env *Env) error {
		// One optional positional, because `cafe box apply substrate` is what a
		// person types and `cafe box apply --phase substrate` is what a script
		// writes. Both are the same command.
		if len(args) > 1 {
			return wantArgs(c.Name, c.Usage, 1, len(args))
		}
		phase := opts.phase
		if len(args) == 1 {
			if phase != "" {
				return fmt.Errorf("%w: cafe box apply got the phase twice: --phase %q and the argument %q (usage: %s)", errUsage, phase, args[0], c.Usage)
			}
			phase = args[0]
		}
		phase = resolvePhaseArg(phase)
		if phase == "" {
			return fmt.Errorf("%w: cafe box apply wants --phase <name|index> or one positional argument (usage: %s)", errUsage, c.Usage)
		}
		store, err := boxStoreFor(c, env, deps)
		if err != nil {
			return err
		}
		res, err := store.Apply(phase)
		if err != nil {
			// A phase that is not in the table is a usage mistake, not a failure:
			// the box was never asked to do anything it could misread.
			if errors.Is(err, box.ErrNoSuchPhase) {
				return fmt.Errorf("%w: %w", errUsage, err)
			}
			return err
		}
		say(env.Stderr, res.Message)
		if res.Handoff != "" && res.Changed {
			_, _ = fmt.Fprintf(env.Stderr, "cafe box: this records the phase; %s is what performs it\n", res.Handoff)
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

// logsOptions is `cafe box logs`' flag state. `--release` is one flag with
// two subjects, and that is deliberate: the box's own lifecycle log and a
// release's own log are both "what happened", and a person who typed
// `cafe box logs --release X` expects the log of the release they named
// rather than a lecture about which command to type next.
type logsOptions struct {
	boxStackFlags
	release string
	root    string
}

func newBoxLogsCommand(deps boxDeps) *Command {
	opts := &logsOptions{}
	c := &Command{
		Name:    "box logs",
		Summary: "the box's own log — and, with --release, the log of one release",
		Usage:   "cafe box logs [--json] [--release <id>]",
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&opts.json, "json", false, "print the machine document on stdout instead of the human one")
			fs.StringVar(&opts.release, "release", "", "follow ONE release's log instead of the box's own event log")
			fs.StringVar(&opts.root, "root", "", "where this box's stack lives (default $"+stackRootEnv+" or "+defaultStackRoot+")")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		if opts.release != "" {
			return writeReleaseLog(env, deps, opts)
		}
		store, err := boxStoreFor(c, env, deps)
		if err != nil {
			return err
		}
		events, err := store.Logs()
		if err != nil {
			return err
		}
		if len(events) == 0 {
			say(env.Stderr, "no events yet")
		} else {
			say(env.Stderr, "%d events", len(events))
		}
		return writeDoc(env.Stdout, events, opts.json)
	}
	return c
}

// writeReleaseLog prints one release's own log. It is the file the release
// writes while it runs, named by the release id, so following one release is
// a path rather than a grep through everything the box ever did.
func writeReleaseLog(env *Env, deps boxDeps, opts *logsOptions) error {
	sd := deps.stack
	if opts.root != "" {
		sd.root = opts.root
	}
	// A release's log is found by looking in the stack it is in, and the
	// release id is unique enough that the box can answer without being told
	// which service it belongs to: it walks the services under the root.
	path, err := releaseLogPath(sd, opts.release)
	if err != nil {
		// An unknown release id is a USAGE mistake: nothing was asked of the
		// box that it could misread, and the answer is the list of what is on
		// it rather than a failure.
		return fmt.Errorf("%w: cafe box logs --release %s: %w (usage: %s)", errUsage, opts.release, err, "cafe box logs --release <id>")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read release %s's log at %s: %w", opts.release, path, err)
	}
	_, _ = fmt.Fprintf(env.Stdout, "%s", raw)
	return nil
}

func newBoxVerifyCommand(deps boxDeps) *Command {
	f := &boxFlags{}
	ops := &opsVerifyOptions{}
	c := &Command{
		Name:    "box verify",
		Summary: "the box's report on itself: identity, key, journal and phase — and, with --observe, the secrets and health signals it answers under",
		Usage:   "cafe box verify [--observe <observe.report.json>] [--state-dir <dir>] [--mint <name>] [--json]",
		Flags: func(fs *flag.FlagSet) {
			f.declare(fs)
			ops.declare(fs)
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		store, err := boxStoreFor(c, env, deps)
		if err != nil {
			return err
		}
		v, err := store.Verify()
		if err != nil {
			return err
		}
		// The ops half runs over the same document and answers the same
		// verb, so `cafe box verify` is one command a person runs and not
		// two they have to know about. It is skipped wholesale when no flag
		// asks for it, which is what keeps the verb's existing output the
		// output a script already reads.
		opsDoc, run, err := runOpsVerify(ops, env, deps.withDefaults().stdin, v.BoxID)
		if err != nil {
			return err
		}
		if !run {
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

		renderOpsVerify(env, opsDoc)
		// Both halves, one document: the box's report on itself, and the
		// verification it runs under. A machine reads one JSON object; a
		// person reads the stderr lines, whose last is the next line.
		both := struct {
			*box.Verify
			Verification *verify.Verification `json:"verification,omitempty"`
		}{Verify: v, Verification: opsDoc}
		if err := writeDoc(env.Stdout, both, f.json); err != nil {
			return err
		}
		if !v.OK || (opsDoc != nil && opsDoc.Status == verify.StatusDegraded) {
			return errReported
		}
		return nil
	}
	return c
}
