package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cafaye/caf/internal/agent"
	"github.com/cafaye/caf/internal/box"
	"github.com/cafaye/caf/internal/publish"
)

// publishDeps is what `caf publish` needs from the world, and it is a struct of
// parameters for the reason resolveDeps and deployDeps are: a test can hand
// the command a publisher that records calls and answers without a network, a
// confirmation it controls, a tag derivation that does not need a git
// repository, and a directory it owns — which is the only reason a dry run, a
// refusal, a 409 and a successful publish are testable in this repository.
type publishDeps struct {
	// dir is where config.json and the keyring live. Empty means the resolved
	// default, and a test always sets it.
	dir string
	// ring holds the caller's caf token. Nil means a file ring under the
	// config directory, and a test always sets it.
	ring agent.Keyring
	// publisher is pantry's write surface. Nil means a real HTTP client,
	// which is why a test always sets it: the whole point of the seam is the
	// answer.
	publisher publish.Publisher
	// confirm asks the operator. It is a seam because a confirmation prompt
	// read from os.Stdin is a prompt no test can answer and no CI job can
	// satisfy: `caf publish` in a pipeline must either be given --yes or
	// refuse, and both of those have to be assertable.
	confirm func(prompt string) (bool, error)
	// tags derives the version tag from the project's git repository. Nil
	// means the real derivation, which is why a test always sets it: a test
	// that shells out to git is a test that needs a repository to exist.
	tags func(dir string) (string, error)
	// pantryURL is pantry's origin. Empty means the CAF_PANTRY_URL
	// environment variable, the same rule resolve holds.
	pantryURL string
}

func (d publishDeps) withDefaults() publishDeps {
	if d.dir == "" {
		if dir, err := defaultConfigDir(); err == nil {
			d.dir = dir
		}
	}
	if d.ring == nil {
		// The same file ring the agent verbs and resolve default to: an
		// agent's token and a box's key are two kinds of secret in one secret
		// store, and a second implementation of "put a secret somewhere" is a
		// second place for the mode bits to be wrong.
		d.ring = box.FileRing{Dir: filepath.Join(d.dir, "keyring")}
	}
	if d.pantryURL == "" {
		d.pantryURL = strings.TrimSpace(os.Getenv(pantryURLEnv))
	}
	if d.publisher == nil {
		d.publisher = publish.HTTPPublisher{BaseURL: d.pantryURL}
	}
	if d.confirm == nil {
		d.confirm = func(prompt string) (bool, error) { return askOnStdin("caf publish", prompt) }
	}
	if d.tags == nil {
		d.tags = latestSemverTag
	}
	return d
}

// publishOptions is `caf publish`'s flag state.
type publishOptions struct {
	dryRun bool
	yes    bool
	tag    string
	kind   string
	json   bool
	pantry string
}

// publishLongHelp is the prose under `caf publish`'s usage line. It is the
// answer to "what do I type", written down where a person reads it before
// running a command that writes to a public catalog.
const publishLongHelp = `Publishes a project to the cafaye catalog. The tool proposes; a human
confirms. That shape is the approval-card pattern, and it is OPS-D7's boundary
at the level of a command: the customer's own agent reads and proposes freely,
and only a human approval mints the act. So the proposal is always printed
first — the registration, the version tag, the dependency edges — and nothing
is written until somebody says yes.

  --dry-run   print the proposal and write nothing
  --yes       publish without asking; required where nobody can answer
  --tag       the git tag to publish, verbatim; without it the repository's
              latest semver tag is derived
  --kind      the project's kind when the manifest declares no contract
              surface (api, worker, both, cli)
  --json      one JSON document: the proposal, every step's result, the state

The three writes run in order — register the project, publish the version,
declare each dependency edge — and each is idempotent-tolerant: a 409 that
means the write already happened is a report line, not a crash. Any other
failure names itself with the server's own code and detail, and the sequence
stops, because the steps after a 401 would be three copies of the same
sentence and the first one already says what to fix.

STDOUT IS THE DOCUMENT, STDERR IS THE SENTENCE. The proposal and the report
are on stdout; the warnings and the step results are plain lines on stderr.
The caller's token is read from the keyring, presented to pantry, and never
printed.`

// newPublishCommand takes its seams, for the same reason newResolveCommand and
// newDeployCommand do: a test can hand the command a publisher, a confirmation
// and a tag derivation it controls, which is the only reason the decision
// table — dry run, refusal, 409 tolerance, a clean publish — is testable in
// this repository without a network or a git repository.
func newPublishCommand(deps publishDeps) *Command {
	opts := &publishOptions{}
	c := &Command{
		Name:     "publish",
		Summary:  "publish a project to the cafaye catalog: propose the registration, a human confirms, the three writes run",
		Usage:    "caf publish [--dry-run] [--yes] [--tag <git-tag>] [--kind <api|worker|both|cli>] [--json] [--pantry <url>] <project>",
		LongHelp: publishLongHelp,
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&opts.dryRun, "dry-run", false, "print the proposal and write nothing")
			fs.BoolVar(&opts.yes, "yes", false, "publish without asking; required where nobody can answer the prompt")
			fs.StringVar(&opts.tag, "tag", "", "the git tag to publish, verbatim; without it the repository's latest semver tag is derived")
			fs.StringVar(&opts.kind, "kind", "", "the project's kind when the manifest declares no contract surface (api, worker, both, cli)")
			fs.BoolVar(&opts.json, "json", false, "print the proposal, every step's result and the final state as one JSON document")
			fs.StringVar(&opts.pantry, "pantry", "", "pantry's origin; without it, "+pantryURLEnv+" is used")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 1, len(args)); err != nil {
			return err
		}
		return runPublish(args[0], opts, deps, env)
	}
	return c
}

// runPublish is the command's decision table, in the order the decisions are
// made: read and validate the manifest, derive the tag, build the proposal,
// print it, then — and only then — decide whether anything is written.
func runPublish(project string, opts *publishOptions, deps publishDeps, env *Env) error {
	// The flag overrides run BEFORE withDefaults, because withDefaults is
	// what builds the HTTP client out of the URL. A client built first and a
	// URL written after it is a flag that changes nothing — the same rule
	// resolve holds.
	if opts.pantry != "" {
		deps.pantryURL = opts.pantry
	}
	deps = deps.withDefaults()

	manifest, err := readManifest(project)
	if err != nil {
		return err
	}

	tag := strings.TrimSpace(opts.tag)
	if tag == "" {
		// The derivation is a convenience for a re-publish; a first publish
		// has no tags yet, and the refusal names --tag rather than guessing.
		tag, err = deps.tags(project)
		if err != nil {
			return fmt.Errorf("caf publish: %w", err)
		}
	}

	proposal, err := publish.ProposalFor(manifest, tag, opts.kind)
	if err != nil {
		return fmt.Errorf("caf publish: %w", err)
	}

	// The proposal is printed before the question is asked and before
	// anything is written: the human confirms a document, not a vibe. Under
	// --json nothing is printed here — the single report at the end is the
	// document, and a second one before it would be two documents where the
	// contract is one.
	if !opts.json {
		printProposal(env.Stdout, proposal)
	}

	if opts.dryRun {
		if opts.json {
			if err := writeJSON(publish.Report{Proposal: proposal, Refusal: "dry run: nothing was published"}, env.Stdout); err != nil {
				return fmt.Errorf("write the proposal: %w", err)
			}
		}
		sayPublish(env.Stderr, "dry run: nothing was published. Run it with --yes when you mean it.")
		return nil
	}

	token := callerToken("caf publish", deps.dir, deps.ring, env)

	ok, err := confirmPublish(deps, proposal, opts)
	if err != nil {
		// There was nobody to ask. The proposal is already on stdout for a
		// human; under --json the refusal document is the agent-readable
		// artifact — the proposal, no steps, published false, and the reason
		// an agent can act on.
		if opts.json {
			if werr := writeJSON(publish.Report{Proposal: proposal,
				Refusal: "no terminal to confirm on — pass --yes to publish without asking"}, env.Stdout); werr != nil {
				return fmt.Errorf("write the refusal: %w", werr)
			}
		}
		sayPublish(env.Stderr, "nothing was published — there was no terminal to confirm on. Pass --yes to publish without asking.")
		return errReported
	}
	if !ok {
		sayPublish(env.Stderr, "nothing was published. Run it with --yes when you mean it.")
		return errReported
	}

	report := publish.Run(env.Context, deps.publisher, token, proposal)

	if opts.json {
		if err := writeJSON(report, env.Stdout); err != nil {
			return fmt.Errorf("write the publish report: %w", err)
		}
	} else {
		printSteps(env.Stderr, report)
	}
	if !report.Published {
		// The report already names the failed step and the server's own
		// detail; the exit code is the verdict — the same rule `caf resolve`
		// and `caf contract lint` follow.
		return errReported
	}
	return nil
}

// confirmPublish is the confirmation, and it is the only interactive thing
// this command does. --yes skips it; everything else asks once, naming the
// project and the tag, because a prompt that does not say what it is about is
// a prompt people approve without reading.
func confirmPublish(deps publishDeps, proposal publish.Proposal, opts *publishOptions) (bool, error) {
	if opts.yes {
		return true, nil
	}
	return deps.confirm(fmt.Sprintf("caf publish: publish %s at %s to the cafaye catalog? [y/N] ",
		proposal.Registration.Name, proposal.Tag))
}

// printProposal is the human-readable proposal: the registration's facts, the
// tag, the edges. It is the document a human confirms, so it names what will
// be written and nothing about what will happen after.
func printProposal(w io.Writer, proposal publish.Proposal) {
	reg := proposal.Registration
	fmt.Fprintf(w, "caf publish: proposal for `%s`\n", reg.Name)
	fmt.Fprintf(w, "  register     %s (%s, %s) — manifest sha256 %s…\n",
		reg.Name, reg.Language, reg.Kind, reg.ManifestSHA256[:12])
	fmt.Fprintf(w, "  version      %s\n", proposal.Tag)
	if len(proposal.Requirements) == 0 {
		fmt.Fprintf(w, "  require      none — this project declares no dependencies\n")
	}
	for _, req := range proposal.Requirements {
		fmt.Fprintf(w, "  require      %s %s (%s)\n", req.Name, req.VersionRange, req.Dependency)
	}
}

// printSteps is the stderr half after the writes: one line per step, for a
// person. The report on stdout is the document; these are the sentences.
func printSteps(w io.Writer, report publish.Report) {
	for _, step := range report.Steps {
		sayPublish(w, "%s", renderStep(step))
	}
	if report.Published {
		sayPublish(w, "%s is in the catalog", report.Proposal.Registration.Name)
	}
}

// renderStep is one step as a sentence. The operation's verb is a map rather
// than a switch because the machine vocabulary (publish_version) and the
// human one (publish) are two spellings of one fact, and the map is where they
// are kept together.
func renderStep(step publish.Step) string {
	subject := map[string]string{
		publish.OpRegister:    "register",
		publish.OpVersion:     "publish",
		publish.OpRequirement: "declare edge to",
	}[step.Operation]
	if step.Name != "" {
		subject += " `" + step.Name + "`"
	}
	switch step.Status {
	case publish.StatusAlreadyExists:
		return subject + ": already done — " + step.Detail
	case publish.StatusFailed:
		return subject + ": FAILED — " + step.Detail
	}
	return subject + ": done"
}

// sayPublish is the stderr half of the split: one sentence per event, for a
// person.
func sayPublish(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "caf publish: "+format+"\n", args...)
}

// semverTag is the shape of a tag the derivation accepts: an optional leading
// `v` and three numeric parts. A tag that is not semver is accepted by the
// registry and parses to NULL in its version columns — a fact with a
// consequence (the project cannot be the target of a requires edge) — so the
// derivation does not filter on it, it just will not invent one.
var semverTag = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

// latestSemverTag derives the tag from the repository's own tags: the highest
// semver tag git knows, verbatim. The derivation is a convenience for a
// re-publish; a first publish has no tags yet, and the refusal names --tag
// rather than guessing. caf does not create tags: the tag is the publisher's
// act, and a tag caf minted would be a tag the publisher never made.
func latestSemverTag(dir string) (string, error) {
	out, err := gitOutput(dir, "tag", "--list", "--sort=-v:refname")
	if err != nil {
		return "", fmt.Errorf("read the git tags in %s: %w\n"+
			"  Pass --tag with the tag to publish — caf does not create tags", dir, err)
	}
	for _, line := range strings.Split(out, "\n") {
		tag := strings.TrimSpace(line)
		if semverTag.MatchString(tag) {
			return tag, nil
		}
	}
	return "", fmt.Errorf("no semver tag in %s — pass --tag with the tag to publish\n"+
		"  The tag is stored verbatim, leading `v` included: `v1.2.3` and `1.2.3` are two real tags\n"+
		"  and this registry does not guess that somebody meant the same thing", dir)
}

// gitOutput runs one git command in a directory and returns its stdout,
// trimmed. It is the same helper shape deploy's argo engine uses, because the
// question — "what did git say" — is one question.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
