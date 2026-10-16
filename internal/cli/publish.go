package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cafaye/cafe/internal/agent"
	"github.com/cafaye/cafe/internal/box"
	"github.com/cafaye/cafe/internal/publish"
)

// publishDeps is what `cafe publish` needs from the world, and it is a struct of
// parameters for the reason resolveDeps and deployDeps are: a test can hand
// the command a publisher that records calls and answers without a network, a
// tag derivation that does not need a git repository, and a directory it owns
// — which is the only reason a dry run, a 409 and a successful publish are
// testable in this repository.
//
// There is deliberately NO confirmation seam. Since the 2026-10-11 ruling
// ("for cafaye i think we should make everything non interactive by default
// as it shall mostly be used by agents") the default path publishes without
// asking, and no code path in this command may block on a TTY. A seam for a
// prompt would be a seam for a prompt that must never be asked.
type publishDeps struct {
	// dir is where config.json and the keyring live. Empty means the resolved
	// default, and a test always sets it.
	dir string
	// ring holds the caller's cafe token. Nil means a file ring under the
	// config directory, and a test always sets it.
	ring agent.Keyring
	// publisher is pantry's write surface. Nil means a real HTTP client,
	// which is why a test always sets it: the whole point of the seam is the
	// answer.
	publisher publish.Publisher
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
	if d.tags == nil {
		d.tags = latestSemverTag
	}
	return d
}

// publishOptions is `cafe publish`'s flag state. yes is parsed and ignored:
// publishing is non-interactive by default since the 2026-10-11 ruling, and
// the flag is retained as an accepted no-op so scripts written while it was
// the escape hatch keep working.
type publishOptions struct {
	dryRun bool
	yes    bool
	tag    string
	kind   string
	json   bool
	pantry string
}

// publishLongHelp is the prose under `cafe publish`'s usage line. It is the
// answer to "what do I type", written down where a person reads it before
// running a command that writes to a public catalog.
const publishLongHelp = `Publishes a project to the cafaye catalog. Publishing is non-interactive
by default: an agent runs cafe publish, it publishes. That shape is the
user's ruling of 2026-10-11 — everything non-interactive by default, since
cafe is mostly used by agents — and it is OPS-D7's boundary at the level of
a command: the customer's own agent reads, proposes and mints freely, and
reviewability is after the fact, in pantry's audit trail (the publisher.minted
companion packet). The proposal — the registration, the version tag, the
dependency edges — is always printed first, so the mint is never a surprise.

  --dry-run   print the proposal and write nothing
  --yes       accepted for compatibility; publishing is non-interactive by default since 2026-10-11
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
// newDeployCommand do: a test can hand the command a publisher and a tag
// derivation it controls, which is the only reason the decision table — dry
// run, a clean publish, 409 tolerance — is testable in this repository
// without a network or a git repository.
func newPublishCommand(deps publishDeps) *Command {
	opts := &publishOptions{}
	c := &Command{
		Name:     "publish",
		Summary:  "publish a project to the cafaye catalog: propose the registration, then publish the three writes — non-interactive by default",
		Usage:    "cafe publish [--dry-run] [--yes] [--tag <git-tag>] [--kind <api|worker|both|cli>] [--json] [--pantry <url>] <project>",
		LongHelp: publishLongHelp,
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&opts.dryRun, "dry-run", false, "print the proposal and write nothing")
			fs.BoolVar(&opts.yes, "yes", false, "accepted for compatibility; publishing is non-interactive by default since 2026-10-11")
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
// print it, then write — unless --dry-run, which prints the proposal and
// writes nothing. There is no confirmation step: publishing is
// non-interactive by default since the 2026-10-11 ruling, and --yes is an
// accepted no-op kept so scripts written while it was the escape hatch keep
// working.
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
			return fmt.Errorf("cafe publish: %w", err)
		}
	}

	proposal, err := publish.ProposalFor(manifest, tag, opts.kind)
	if err != nil {
		return fmt.Errorf("cafe publish: %w", err)
	}

	// The proposal is printed before anything is written: the mint is never
	// a surprise. Under --json nothing is printed here — the single report
	// at the end is the document, and a second one before it would be two
	// documents where the contract is one.
	if !opts.json {
		printProposal(env.Stdout, proposal)
	}

	if opts.dryRun {
		if opts.json {
			if err := writeJSON(publish.Report{Proposal: proposal, Refusal: "dry run: nothing was published"}, env.Stdout); err != nil {
				return fmt.Errorf("write the proposal: %w", err)
			}
		}
		sayPublish(env.Stderr, "dry run: nothing was published. Run it without --dry-run to publish.")
		return nil
	}

	token := callerToken("cafe publish", deps.dir, deps.ring, env)

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
		// detail; the exit code is the verdict — the same rule `cafe resolve`
		// and `cafe contract lint` follow.
		return errReported
	}
	return nil
}

// printProposal is the human-readable proposal: the registration's facts, the
// tag, the edges. It is printed before anything is written, so the mint is
// never a surprise — it names what will be written and nothing about what
// will happen after.
func printProposal(w io.Writer, proposal publish.Proposal) {
	reg := proposal.Registration
	_, _ = fmt.Fprintf(w, "cafe publish: proposal for `%s`\n", reg.Name)
	_, _ = fmt.Fprintf(w, "  register     %s (%s, %s) — manifest sha256 %s…\n",
		reg.Name, reg.Language, reg.Kind, reg.ManifestSHA256[:12])
	_, _ = fmt.Fprintf(w, "  version      %s\n", proposal.Tag)
	if len(proposal.Requirements) == 0 {
		_, _ = fmt.Fprintf(w, "  require      none — this project declares no dependencies\n")
	}
	for _, req := range proposal.Requirements {
		_, _ = fmt.Fprintf(w, "  require      %s %s (%s)\n", req.Name, req.VersionRange, req.Dependency)
	}
}

// printSteps is the stderr half after the writes: one line per step, for a
// person. The report on stdout is the document; these are the sentences.
func printSteps(w io.Writer, report publish.Report) {
	for _, step := range report.Steps {
		sayPublish(w, "%s", renderStep(step))
	}
	if report.Published {
		if report.Address != "" {
			sayPublish(w, "%s is in the catalog, addressable as %s",
				report.Proposal.Registration.Name, report.Address)
			return
		}
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
	_, _ = fmt.Fprintf(w, "cafe publish: "+format+"\n", args...)
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
// rather than guessing. cafe does not create tags: the tag is the publisher's
// act, and a tag cafe minted would be a tag the publisher never made.
func latestSemverTag(dir string) (string, error) {
	return latestSemverTagIn(dir, "--tag")
}

// latestSemverTagIn is the derivation with the flag that overrides it named
// by the caller: `cafe publish` derives a registry tag (--tag) and
// `cafe docs publish` derives a content version (--version), and a refusal
// that named the other command's flag would send the reader to the wrong
// help. The derivation is one function because "the highest semver tag git
// knows" is one question, and two copies of it is two places to disagree
// about what a tag is.
func latestSemverTagIn(dir, flag string) (string, error) {
	out, err := gitOutput(dir, "tag", "--list", "--sort=-v:refname")
	if err != nil {
		return "", fmt.Errorf("read the git tags in %s: %w\n"+
			"  Pass %s with the version to ship — cafe does not create tags", dir, err, flag)
	}
	for _, line := range strings.Split(out, "\n") {
		tag := strings.TrimSpace(line)
		if semverTag.MatchString(tag) {
			return tag, nil
		}
	}
	return "", fmt.Errorf("no semver tag in %s — pass %s with the version to ship\n"+
		"  The tag is stored verbatim, leading `v` included: `v1.2.3` and `1.2.3` are two real tags\n"+
		"  and this registry does not guess that somebody meant the same thing", dir, flag)
}

// gitOutput runs one git command in a directory and returns its stdout,
// trimmed. It is the same helper shape deploy's argo engine uses, because the
// question — "what did git say" — is one question.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
