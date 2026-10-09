package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cafaye/caf/internal/agent"
	"github.com/cafaye/caf/internal/box"
	"github.com/cafaye/caf/internal/contract"
	"github.com/cafaye/caf/internal/resolve"
)

// resolveDeps is what `caf resolve` needs from the world, and it is a struct of
// parameters for the reason providerDeps and agentDeps are: a test can hand the
// command a keyring it owns, a pantry and a conductor that answer without a
// network, and a directory it controls, and drive a whole resolution without a
// server, a keychain or a token.
type resolveDeps struct {
	// dir is where config.json and the keyring live. Empty means the resolved
	// default, and a test always sets it.
	dir string
	// ring holds the caller's caf token. Nil means a file ring under the
	// config directory, and a test always sets it.
	ring agent.Keyring
	// pantry is the tiered catalog. Nil means a real HTTP client, which is why
	// a test always sets it: the whole point of the seam is the answer.
	pantry resolve.Pantry
	// conductor is the entitlement check. Nil means a real HTTP client, for the
	// same reason.
	conductor resolve.Conductor
	// pantryURL is pantry's origin. Empty means the CAF_PANTRY_URL environment
	// variable, and an empty environment variable is a refusal rather than a
	// guess.
	pantryURL string
	// conductorURL is conductor's origin. Empty means the CAF_CONDUCTOR_URL
	// environment variable, the same rule box and provider already hold.
	conductorURL string
}

// pantryURLEnv is where pantry's origin comes from when no flag names one. An
// environment variable rather than a flag default, for the reason
// conductorURLEnv is: a flag default baked into a binary is a default that goes
// stale when the service moves and nobody rebuilds caf to find out.
const pantryURLEnv = "CAF_PANTRY_URL"

func (d resolveDeps) withDefaults() resolveDeps {
	if d.dir == "" {
		if dir, err := defaultConfigDir(); err == nil {
			d.dir = dir
		}
	}
	if d.ring == nil {
		// The same file ring the agent verbs default to: an agent's token and
		// a box's key are two kinds of secret in one secret store, and a
		// second implementation of "put a secret somewhere" is a second place
		// for the mode bits to be wrong.
		d.ring = box.FileRing{Dir: filepath.Join(d.dir, "keyring")}
	}
	if d.pantryURL == "" {
		d.pantryURL = strings.TrimSpace(os.Getenv(pantryURLEnv))
	}
	if d.conductorURL == "" {
		d.conductorURL = strings.TrimSpace(os.Getenv(conductorURLEnv))
	}
	if d.pantry == nil {
		d.pantry = resolve.HTTPPantry{BaseURL: d.pantryURL}
	}
	if d.conductor == nil {
		d.conductor = resolve.HTTPConductor{BaseURL: d.conductorURL}
	}
	return d
}

// resolveLongHelp is the prose under `caf resolve`'s usage line.
const resolveLongHelp = `Resolves a project's ` + "`cafaye.yml`" + ` dependencies through the three tiers of
the cafaye catalog, and names every failure with its remedy.

  TIER 1  the listing        the name exists, free and paid alike (anonymous)
  TIER 2  the metadata      where the code lives (the caller's caf token)
  TIER 3  the artifact       the entitlement check and the fetch grant

A dependency that is a paid product must clear tier 3: conductor asks whether
the named account holds the product at the range the project asks for, and
mints a single-use artifact.fetch capability when it does. The two refusals are
named, never silent:

  403 entitlement_required  the product and where to start one
  403 version_not_granted   the granted range and the requested one

A dependency the manifest marks ` + "`required: false`" + ` is a soft dependency: when it
fails, the service runs without it, degraded, and this command says so with a
warning and still exits 0. A required dependency that fails blocks: the named
error goes to stderr and the exit code is 1.

STDOUT IS THE DOCUMENT, STDERR IS THE SENTENCE. The report is one JSON document
on stdout; the warnings and the named errors are plain lines on stderr. The
caller's token is read from the keyring, presented to tier 2 and tier 3, and
never printed.`

func newResolveCommand(deps resolveDeps) *Command {
	opts := &resolveOptions{}
	c := &Command{
		Name:     "resolve",
		Summary:  "resolve a project's cafaye.yml dependencies through the catalog's tiers, naming every failure",
		Usage:    "caf resolve [--account <uuid>] [--pantry <url>] [--conductor <url>] <project>",
		LongHelp: resolveLongHelp,
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.account, "account", "", "the caf account the tier-3 entitlement check runs against; required only when a dependency is a paid product")
			fs.StringVar(&opts.pantry, "pantry", "", "pantry's origin; without it, "+pantryURLEnv+" is used")
			fs.StringVar(&opts.conductor, "conductor", "", "conductor's origin; without it, the identity's conductor or "+conductorURLEnv+" is used")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 1, len(args)); err != nil {
			return err
		}
		return runResolve(args[0], opts, deps, env)
	}
	return c
}

// resolveOptions is `caf resolve`'s flag state.
type resolveOptions struct {
	account   string
	pantry    string
	conductor string
}

// runResolve reads the project's manifest, resolves every declared dependency
// through the tiers, and splits the answer: the report as one JSON document on
// stdout, the warnings and named errors as plain lines on stderr.
//
// The exit code is the verdict: 1 when a required dependency failed, 0 when
// every required one resolved — an optional dependency that degraded is a
// warning, not a block, which is the schema's `required: false` doing its job.
func runResolve(project string, opts *resolveOptions, deps resolveDeps, env *Env) error {
	deps = deps.withDefaults()
	if opts.pantry != "" {
		deps.pantryURL = opts.pantry
	}
	if opts.conductor != "" {
		deps.conductorURL = opts.conductor
	}

	manifest, err := readResolveManifest(project)
	if err != nil {
		return err
	}

	token := deps.callerToken(env)
	report := resolve.NewResolver(deps.pantry, deps.conductor).Resolve(
		env.Context, project, manifest.Dependencies(), token, opts.account)

	blocked := 0
	for _, res := range report.Services {
		if res.Failure == nil {
			sayResolve(env.Stderr, "%s — resolved (%s)", res.Name, res.Summary())
			continue
		}
		// The failure is named with the server's own code and detail, and the
		// sentence a person reads is the code plus the detail: "403
		// entitlement_required: product `identity` needs an entitlement —
		// start one at https://cafaye.com" is a sentence with the remedy in it.
		line := fmt.Sprintf("%d %s: %s", res.Failure.Tier, res.Failure.Code, res.Failure.Message)
		if res.Required {
			sayResolve(env.Stderr, "%s — BLOCKED: %s", res.Name, line)
			blocked++
			continue
		}
		sayResolve(env.Stderr, "%s — DEGRADED (optional): %s", res.Name, line)
	}

	if blocked > 0 {
		// The report on stdout is the whole answer; stderr already carries each
		// named failure. Nothing more is said here, and the exit code is the
		// verdict — the same rule `caf contract lint` follows.
		_ = writeJSON(report, env.Stdout)
		return errReported
	}
	_ = writeJSON(report, env.Stdout)
	return nil
}

// readResolveManifest reads and validates one project's cafaye.yml. The
// validation is the contract linter's own, because caf has no second opinion
// about what a valid manifest is: a command that validated with its own copy
// of the rules would eventually disagree with `caf contract lint` about one
// file, and that is the one divergence a single CLI cannot have.
func readResolveManifest(project string) (contract.Manifest, error) {
	path := filepath.Join(project, contract.ManifestFileName())
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return contract.Manifest{}, fmt.Errorf("caf resolve: no %s in %s; a project declares its service in one (run %q to create it, or pass the project directory as the argument)",
				contract.ManifestFileName(), project, "caf init")
		}
		return contract.Manifest{}, fmt.Errorf("caf resolve: read %s: %w", path, err)
	}
	manifest, finding := contract.CheckData(path, data)
	if !finding.OK() {
		return contract.Manifest{}, fmt.Errorf("caf resolve: %s", finding.String())
	}
	return manifest, nil
}

// callerToken reads the active agent session's token out of the keyring. It is
// a courier call: the token is read for the length of exactly this resolution
// and kept nowhere — never printed, never logged, never put in a document.
//
// A machine with no token is not a failure. Tier 1 is anonymous and tier 2
// answers a named unauthorized, which is the honest response to a caller with
// no credential; the warning says so rather than leaving the reader to infer
// it from a 401 two steps later.
func (d resolveDeps) callerToken(env *Env) string {
	cfg, err := agent.LoadConfig(d.dir)
	if err != nil {
		sayResolve(env.Stderr, "could not read the agent config: %v", err)
		return ""
	}
	sess, err := cfg.Select("", "")
	if err != nil {
		if errors.Is(err, agent.ErrNotRegistered) {
			sayResolve(env.Stderr, "no agent session is registered, so tier 1 runs anonymous and tier 2 will answer unauthorized; run `caf agent register` and have a human claim it")
			return ""
		}
		sayResolve(env.Stderr, "could not select the agent session: %v", err)
		return ""
	}
	token, err := d.ring.Get(agent.KeyringService, sess.TokenRef)
	if err != nil || strings.TrimSpace(token) == "" {
		sayResolve(env.Stderr, "no token in the keyring for session %s, so tier 1 runs anonymous and tier 2 will answer unauthorized; run `caf agent register`", sess.Name)
		return ""
	}
	return token
}

// sayResolve is the stderr half of the split: one sentence per dependency, for
// a person.
func sayResolve(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "caf resolve: "+format+"\n", args...)
}
