package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/anywaye/cafe/internal/agent"
	"github.com/anywaye/cafe/internal/box"
	"github.com/anywaye/cafe/internal/docs"
)

// docsDeps is what `cafe docs publish` needs from the world, and it is a
// struct of parameters for the reason publishDeps is: a test can hand the
// command a publisher that records calls and answers without a network, a
// version derivation that does not need a git repository, and a directory it
// owns — which is the only reason a dry run, a 401 and a successful publish
// are testable in this repository.
//
// There is deliberately NO confirmation seam. Since the 2026-10-11 ruling
// ("for anywaye i think we should make everything non interactive by default
// as it shall mostly be used by agents") the default path publishes without
// asking, and no code path in this command may block on a TTY. A seam for a
// prompt would be a seam for a prompt that must never be asked.
type docsDeps struct {
	// dir is where config.json and the keyring live. Empty means the resolved
	// default, and a test always sets it.
	dir string
	// ring holds the caller's cafe token. Nil means a file ring under the
	// config directory, and a test always sets it.
	ring agent.Keyring
	// identity is identity's account surface: the accounts list and the
	// session-to-token exchange. Nil means a real HTTP client, which is why a
	// test always sets it: the whole point of the seam is the answer.
	identity docs.Identity
	// publisher is press's ingest surface. Nil means a real HTTP client,
	// which is why a test always sets it: the whole point of the seam is the
	// answer.
	publisher docs.Publisher
	// tags derives the content version from the repository's git tags. Nil
	// means the real derivation, which is why a test always sets it: a test
	// that shells out to git is a test that needs a repository to exist.
	tags func(dir string) (string, error)
	// identityURL is identity's origin. Empty means the CAF_IDENTITY_URL
	// environment variable, the same rule `cafe login` and the agent verbs
	// hold.
	identityURL string
	// pressURL is press's origin. Empty means the CAF_PRESS_URL environment
	// variable, the same rule resolve holds.
	pressURL string
	// account is the --account flag: the id or slug of the account the
	// publish acts in. Empty means the session's own resolution — one account
	// is used, several are a refusal that names the list.
	account string
}

func (d docsDeps) withDefaults() docsDeps {
	if d.dir == "" {
		if dir, err := defaultConfigDir(); err == nil {
			d.dir = dir
		}
	}
	if d.ring == nil {
		// The same file ring the agent verbs and publish default to: an
		// agent's token and a box's key are two kinds of secret in one secret
		// store, and a second implementation of "put a secret somewhere" is a
		// second place for the mode bits to be wrong.
		d.ring = box.FileRing{Dir: filepath.Join(d.dir, "keyring")}
	}
	if d.identityURL == "" {
		d.identityURL = strings.TrimSpace(os.Getenv(identityURLEnv))
	}
	if d.identity == nil {
		d.identity = docs.HTTPIdentity{BaseURL: d.identityURL}
	}
	if d.pressURL == "" {
		d.pressURL = strings.TrimSpace(os.Getenv(docs.PressURLEnv))
	}
	if d.publisher == nil {
		d.publisher = docs.HTTPPublisher{BaseURL: d.pressURL}
	}
	if d.tags == nil {
		d.tags = func(dir string) (string, error) { return latestSemverTagIn(dir, "--version") }
	}
	return d
}

// docsPublishOptions is `cafe docs publish`'s flag state. yes is parsed and
// ignored: publishing is non-interactive by default since the 2026-10-11
// ruling, and the flag is retained as an accepted no-op so scripts written
// in the pantry publisher's shape keep working.
type docsPublishOptions struct {
	dryRun     bool
	yes        bool
	visibility string
	version    string
	press      string
	account    string
}

// docsPublishLongHelp is the prose under `cafe docs publish`'s usage line.
// It is the answer to "what do I type", written down where a person reads it
// before running a command that writes to a public service.
const docsPublishLongHelp = `Ships documentation to press, the documentation service. Publishing is
non-interactive by default: an agent runs cafe docs publish, it publishes.
That shape is the user's ruling of 2026-10-11 — everything non-interactive
by default, since cafe is mostly used by agents.

Visibility is inferred, not typed: run in a public repository and the pages
ship as docs; run in a private one and they ship as notes. The inference
reads the repository's own anywaye.yml (repository.visibility) — the same
field the docs sync's anti-leak guard reads. There is deliberately no
cafe notes publish: one command that knows where it is, because two commands
is two helps to keep in sync.

  --dry-run      print the plan and send nothing
  --yes          accepted for compatibility; publishing is non-interactive by default since 2026-10-11
  --visibility   override the inference with public or private; without it the manifest decides
  --version      the content version, verbatim; without it the repository's
                 latest semver tag is derived
  --press        press's origin; without it, CAF_PRESS_URL is used
  --account      the id or slug of the account to publish in; without it the
                 session's own account is used, and a session that belongs to
                 several is refused with the list rather than guessed

The content tarball is built from the repository's own tree: the pages under
src/content/docs/ for docs, the page files at the root plus projects/ for
notes — markdown and frontmatter only, no build output.

THE SESSION IS EXCHANGED BEFORE PRESS IS CALLED. The caller's stored token is
identity's session credential, which is user-scoped; press verifies a
short-lived, account-scoped JWT off identity's JWKS. So cafe resolves the
account, exchanges the session for a press-scoped token (POST
/v1/session/tokens), and presents THAT to press. The exchanged token is minted
per publish, presented for exactly one call, and never printed — the same rule
the session token holds.

STDOUT IS THE DOCUMENT, STDERR IS THE SENTENCE. The plan and the receipt are
on stdout; the warnings and the outcome are plain lines on stderr. A 401 names
the server's message and says to run cafe login.`

// docsVerbs pairs the word a person types with the command that runs it,
// the way contractVerbs does: the word is what the dispatcher looks up, and
// the command is named with its full invocation so that its help and its
// usage errors read as `cafe docs publish` rather than `cafe publish`.
type docsVerb struct {
	name string
	cmd  *Command
}

func docsVerbs(deps docsDeps) []docsVerb {
	return []docsVerb{
		{name: "publish", cmd: newDocsPublishCommand(deps)},
	}
}

func newDocsCommand(deps docsDeps) *Command {
	verbs := docsVerbs(deps)
	c := &Command{
		Name:     "docs",
		Summary:  "ship documentation to press: infer the kind from the repository, pack the pages, publish — non-interactive by default",
		Usage:    "cafe docs publish [--dry-run] [--visibility <public|private>] [--version <v>] [--press <url>] <project>",
		LongHelp: docsVerbTable(verbs),
	}
	c.Run = func(args []string, env *Env) error {
		// Bare `cafe docs` is a question ("what can this do?"), not a
		// mistake, so it answers instead of exiting 2 — the same rule the
		// contract group holds.
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
		return fmt.Errorf("%w: unknown docs subcommand %q (usage: cafe docs <%s>)",
			errUsage, args[0], strings.Join(names, "|"))
	}
	return c
}

// docsVerbTable is the verb list in the group's help. It is one string
// rather than a rendering hook so `cafe docs`, `cafe docs --help` and
// `cafe help docs` cannot drift into three different answers — the same
// reason contractVerbTable exists.
func docsVerbTable(verbs []docsVerb) string {
	var table strings.Builder
	table.WriteString("Commands:\n")
	for _, verb := range verbs {
		_, _ = fmt.Fprintf(&table, "  %s  %s\n", verb.cmd.Name, verb.cmd.Summary)
	}
	return table.String()
}

// newDocsPublishCommand takes its seams, for the same reason
// newPublishCommand does: a test can hand the command a publisher and a
// version derivation it controls, which is the only reason the decision
// table — dry run, a clean publish, a 401 — is testable in this repository
// without a network or a git repository.
func newDocsPublishCommand(deps docsDeps) *Command {
	opts := &docsPublishOptions{}
	c := &Command{
		Name:     "docs publish",
		Summary:  "ship this repository's pages to press, inferring docs from a public repository and notes from a private one",
		Usage:    "cafe docs publish [--dry-run] [--yes] [--visibility <public|private>] [--version <v>] [--press <url>] [--account <id-or-slug>] <project>",
		LongHelp: docsPublishLongHelp,
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&opts.dryRun, "dry-run", false, "print the plan and send nothing")
			fs.BoolVar(&opts.yes, "yes", false, "accepted for compatibility; publishing is non-interactive by default since 2026-10-11")
			fs.StringVar(&opts.visibility, "visibility", "", "override the inference with public or private; without it the manifest decides")
			fs.StringVar(&opts.version, "version", "", "the content version, verbatim; without it the repository's latest semver tag is derived")
			fs.StringVar(&opts.press, "press", "", "press's origin; without it, "+docs.PressURLEnv+" is used")
			fs.StringVar(&opts.account, "account", "", "the id or slug of the account to publish in; without it the session's own account is used, and several are a refusal that names the list")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 1, len(args)); err != nil {
			return err
		}
		return runDocsPublish(args[0], opts, deps, env)
	}
	return c
}

// runDocsPublish is the command's decision table, in the order the decisions
// are made: read and validate the manifest, resolve the kind, resolve the
// version, build the tarball, print the plan, then send — unless --dry-run,
// which prints the plan and sends nothing. There is no confirmation step:
// publishing is non-interactive by default since the 2026-10-11 ruling, and
// --yes is an accepted no-op.
func runDocsPublish(project string, opts *docsPublishOptions, deps docsDeps, env *Env) error {
	// The flag overrides run BEFORE withDefaults, because withDefaults is
	// what builds the HTTP client out of the URL. A client built first and a
	// URL written after it is a flag that changes nothing — the same rule
	// resolve and publish hold.
	if opts.press != "" {
		deps.pressURL = opts.press
	}
	// The account is the flag's, verbatim; the exchange resolves it against
	// identity's list, and an empty one defers to the session's own choice.
	deps.account = opts.account
	deps = deps.withDefaults()

	// The manifest is validated by the contract linter's own gate BEFORE
	// anything is planned or sent: an unreadable anywaye.yml is a refusal
	// that names the file, and the command never guesses the kind — a
	// guess in the public direction is a leak.
	manifest, err := readManifest(project)
	if err != nil {
		return err
	}

	kind, err := docs.KindForVisibility(opts.visibility, manifest)
	if err != nil {
		return fmt.Errorf("cafe docs publish: %w", err)
	}

	version := strings.TrimSpace(opts.version)
	if version == "" {
		// The derivation is a convenience for a re-publish; a first publish
		// has no tags yet, and the refusal names --version rather than
		// guessing.
		version, err = deps.tags(project)
		if err != nil {
			return fmt.Errorf("cafe docs publish: %w", err)
		}
	}

	content, plan, err := docs.BuildTar(project, kind)
	if err != nil {
		return fmt.Errorf("cafe docs publish: %w", err)
	}
	plan.Project = manifest.ServiceName()
	plan.Version = version

	printDocsPlan(env.Stdout, plan)

	if opts.dryRun {
		sayDocs(env.Stderr, "dry run: nothing was sent. Run it without --dry-run to publish.")
		return nil
	}

	// The caller's stored token is identity's SESSION credential, which is
	// user-scoped; press verifies an account-scoped JWT minted for its own
	// audience. So the session is exchanged for a press token BEFORE press is
	// called. A failed exchange is a failed publish: the session is never
	// presented to press as a fallback, because press's verifier refuses a
	// session token by design and a fallback would be this command guessing at
	// a credential contract it does not own.
	// A human's session first: `cafe login` is the flow this command is for,
	// and the session it stores is the one the exchange takes. An agent's
	// session is the fallback for a machine with no human logged in.
	session := humanSessionToken("cafe docs publish", deps.dir, deps.ring, env)
	if strings.TrimSpace(session) == "" {
		session = callerToken("cafe docs publish", deps.dir, deps.ring, env)
	}
	if strings.TrimSpace(session) == "" {
		sayDocs(env.Stderr, "no session token to exchange: run `cafe login` and try again")
		return errReported
	}

	exchanged, err := docs.ExchangeSession(env.Context, deps.identity, session, deps.account)
	if err != nil {
		// The exchange names identity's own refusal verbatim. A session
		// identity no longer accepts is the unauthenticated run, and the
		// remedy is to log in again — not a stack trace.
		var identityErr *docs.IdentityError
		sayDocs(env.Stderr, "%s", err.Error())
		if errors.As(err, &identityErr) && (identityErr.Status == 401 || identityErr.Status == 403) {
			sayDocs(env.Stderr, "run `cafe login` and try again")
		}
		return errReported
	}

	receipt, err := deps.publisher.Publish(env.Context, exchanged.Token, docs.Ingest{
		Project: plan.Project,
		Version: plan.Version,
		Kind:    plan.Kind,
		Content: content,
	})
	if err != nil {
		// The failure names itself with the server's own message, and the
		// exit code is the verdict. A 401 is the unauthenticated run: the
		// token is missing or press does not accept it, and the remedy is
		// to log in — not a stack trace.
		var pressErr *docs.PressError
		if errors.As(err, &pressErr) && pressErr.Status == 401 {
			sayDocs(env.Stderr, "press refused the call: %s", pressErr.Error())
			sayDocs(env.Stderr, "run `cafe login` and try again")
			return errReported
		}
		sayDocs(env.Stderr, "press refused the call: %s", err.Error())
		return errReported
	}

	if err := writeJSON(receipt, env.Stdout); err != nil {
		return fmt.Errorf("write the press receipt: %w", err)
	}
	sayDocs(env.Stderr, "%s %s is in press as %s (state %s)", plan.Kind, plan.Project, receipt.ID, receipt.State)
	return nil
}

// printDocsPlan is the human-readable plan: what would be sent, where from,
// and what it is. It is printed before anything is sent, so the ingest is
// never a surprise — it names what will be written and nothing about what
// will happen after.
func printDocsPlan(w io.Writer, plan docs.Plan) {
	_, _ = fmt.Fprintf(w, "cafe docs publish: plan for `%s`\n", plan.Project)
	_, _ = fmt.Fprintf(w, "  kind       %s\n", plan.Kind)
	_, _ = fmt.Fprintf(w, "  version    %s\n", plan.Version)
	_, _ = fmt.Fprintf(w, "  pages      %d files, %d bytes, sha256 %s\n", len(plan.Files), plan.Size, plan.SHA256)
	for _, file := range plan.Files {
		_, _ = fmt.Fprintf(w, "  page       %s\n", file)
	}
}

// sayDocs is the stderr half of the split: one sentence per event, for a
// person.
func sayDocs(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, "cafe docs publish: "+format+"\n", args...)
}
