package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cafaye/caf/internal/agent"
	"github.com/cafaye/caf/internal/box"
)

// agentDeps is what `caf agent` needs from the world, and it is a struct of
// parameters rather than a set of package-level variables for the reason
// boxDeps and envDeps are: a test can hand the command a keyring it owns and an
// identity endpoint that answers without a network, which is the only way a
// registration can be driven through the real router without a server, a
// keychain or a token.
type agentDeps struct {
	// dir is where config.json and the keyring live. Empty means the resolved
	// default, and a test always sets it.
	dir string
	// ring holds the token. Nil means the file ring under the config
	// directory, and a test always sets it.
	ring agent.Keyring
	// dial opens the identity endpoint. Nil means the real HTTP one, which is
	// why a test always sets it: the whole point of the seam is the answer.
	dial agent.Dial
	// baseURL is the identity origin. Empty means the CAF_IDENTITY_URL
	// environment variable, and an empty environment variable is a refusal
	// rather than a guess.
	baseURL string
}

// identityURLEnv is where the endpoint comes from when no flag names one. An
// environment variable rather than a flag default, because a flag default baked
// into the binary is a default that goes stale when identity moves and nobody
// rebuilds caf to find out. The same argument CAF_CONDUCTOR_URL already makes
// for the box.
const identityURLEnv = "CAF_IDENTITY_URL"

func (d agentDeps) withDefaults() agentDeps {
	if d.dir == "" {
		if dir, err := defaultConfigDir(); err == nil {
			d.dir = dir
		}
	}
	if d.baseURL == "" {
		d.baseURL = strings.TrimSpace(os.Getenv(identityURLEnv))
	}
	if d.ring == nil {
		// The one 0600 secret store in this tree, under the same keyring
		// directory the box's private key is filed in: an agent's token and a
		// box's key are two kinds of secret in one secret store, and a second
		// implementation of "put a secret somewhere" is a second place for the
		// mode bits to be wrong.
		d.ring = box.FileRing{Dir: filepath.Join(d.dir, "keyring")}
	}
	if d.dial == nil {
		d.dial = agent.DefaultDial()
	}
	return d
}

// store builds the store the verbs share. It is a method so every verb gets the
// same defaults, and so a test that injects a ring sees it in every one of them.
func (d agentDeps) store() *agent.Store {
	d = d.withDefaults()
	return agent.NewStore(d.dir, d.ring, d.dial)
}

// agentVerb is one word under `caf agent`, paired with the command that runs it.
// The same shape `caf box` and `caf contract` use: the word is what the
// dispatcher looks up, and the command is named with its full invocation so its
// help and its usage errors read as the words the person typed.
type agentVerb struct {
	name string
	cmd  *Command
}

func agentVerbs(deps agentDeps) []agentVerb {
	return []agentVerb{
		{name: "login", cmd: newAgentLoginCommand(deps)},
		{name: "register", cmd: newAgentRegisterCommand(deps)},
		{name: "token", cmd: newAgentTokenCommand(deps)},
		{name: "whoami", cmd: newAgentWhoAmICommand(deps)},
	}
}

// agentLongHelp is the prose under `caf agent`'s usage line. It is here because
// a person reads it before running the one command in this tree that asks a
// remote service to mint a credential on their behalf, and the two things it has
// to say are the split with `caf box` and the claim step.
const agentLongHelp = `The agent is a PRINCIPAL: it has a username, it becomes
claimed by a human account, and it holds a token. ` + "`caf box …`" + ` is a
MACHINE — it has a keypair and it enrols. Two subjects, two verbs, and the split
is the one moon/AGENTS-FIRST.md chose so that ` + "`caf agent register`" + ` never
reads as "the agent is registering a box".

REGISTER is the whole of it. It asks identity for an unclaimed agent, moves the
token straight into the OS keyring, writes the non-secrets to
~/.config/caf/config.json, and prints the claim URL on STDERR. A human opens
that URL and approves; until they do, identity answers every authenticated call
with 403 agent_unclaimed and every command here says so with the remedy rather
than with a status code.

THE TOKEN IS NEVER PRINTED. Not on stdout, not on stderr, not in an error, not
in a report, not in a JSON document. What the documents carry is whether a token
is stored, and its non-secret metadata. If it ever appears in a transcript it is
a bug in this file and not a feature of it.

STDOUT IS THE DOCUMENT, STDERR IS THE SENTENCE. Every verb prints one JSON
document on stdout — the thing an agent pipes into a parser — and one plain
prose line on stderr, the thing a person reads. There is no --json flag to
toggle between them: a flag that chooses which stream gets the document is a
flag that guarantees one of the two is wrong in some script.

THE ENDPOINT IS NEVER GUESSED. Pass --base-url <url> or set ` + identityURLEnv + `;
an unset one is a refusal, because a default baked into a binary is a default
that goes stale when identity moves.`

func newAgentCommand(deps agentDeps) *Command {
	verbs := agentVerbs(deps)
	c := &Command{
		Name:     "agent",
		Summary:  "the agent lifecycle: register, log in, and manage the token identity issued",
		Usage:    "caf agent <register|login|whoami|token> [flags] [arguments]",
		LongHelp: agentLongHelp,
	}
	c.Run = func(args []string, env *Env) error {
		// Bare `caf agent` is a question, not a mistake.
		if len(args) == 0 {
			printHelp(env.Stdout, c)
			return nil
		}
		verb, found := lookupAgentVerb(verbs, args[0])
		if !found {
			return unknownAgentVerb(args[0], verbs)
		}
		return verb.cmd.Execute(env, args[1:])
	}
	return c
}

func lookupAgentVerb(verbs []agentVerb, name string) (agentVerb, bool) {
	for _, v := range verbs {
		if v.name == name {
			return v, true
		}
	}
	return agentVerb{}, false
}

// unknownAgentVerb is a usage error: the person typed a word caf does not have,
// and the answer is the list rather than a stack trace.
func unknownAgentVerb(name string, verbs []agentVerb) error {
	names := make([]string, len(verbs))
	for i, v := range verbs {
		names[i] = v.name
	}
	return fmt.Errorf("%w: caf agent %q is not a command; the verbs are %s", errUsage, name, strings.Join(names, ", "))
}

// say is the stderr half of the split: one sentence, for a person.
func sayAgent(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "caf agent: "+format+"\n", args...)
}

// agentBaseURL resolves the origin: the flag wins, the environment variable is
// next, and neither is a refusal. The priority is the flag's because a person
// who typed one meant that one.
func agentBaseURL(deps agentDeps, flagURL string) (string, error) {
	if url := strings.TrimSpace(flagURL); url != "" {
		return url, nil
	}
	if url := strings.TrimSpace(deps.baseURL); url != "" {
		return url, nil
	}
	return "", agent.ErrNoBaseURL
}

// agentError is how a command reports a failure it cannot act on. A StatusError
// carries its own actionable half — the 403 agent_unclaimed answer is an
// instruction, not a status — and both go to stderr beside the error, so the
// sentence a person reads names the cause and the remedy.
func agentError(env *Env, err error) error {
	fmt.Fprintf(env.Stderr, "caf agent: %v\n", err)
	var status *agent.StatusError
	if errors.As(err, &status) {
		if hint := status.Hint(); hint != "" {
			fmt.Fprintf(env.Stderr, "caf agent: %s\n", hint)
		}
	}
	return errReported
}

// --- register -------------------------------------------------------------

type agentRegisterOptions struct {
	name     string
	username string
	baseURL  string
	logIn    bool
}

func newAgentRegisterCommand(deps agentDeps) *Command {
	opts := &agentRegisterOptions{}
	c := &Command{
		Name:    "agent register",
		Summary: "register a new agent with identity and store its token in the OS keyring",
		Usage:   "caf agent register --name <display-name> [--username <name>] [--base-url <url>] [--log-in]",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.name, "name", "", "the agent's display name; required")
			fs.StringVar(&opts.username, "username", "", "the agent's username; generated from the display name when omitted")
			fs.StringVar(&opts.baseURL, "base-url", "", "identity's origin; without it, "+identityURLEnv+" is read and an unset variable is a refusal")
			fs.BoolVar(&opts.logIn, "log-in", false, "make this the active agent session even when another one already is")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		if strings.TrimSpace(opts.name) == "" {
			return fmt.Errorf("%w: caf agent register wants --name <display-name> (usage: %s)", errUsage, c.Usage)
		}
		url, err := agentBaseURL(deps, opts.baseURL)
		if err != nil {
			// A missing endpoint is a misconfigured MACHINE, not a mistyped
			// command, so it is a failure and not a usage error — the same
			// answer `caf box enroll` gives for a missing CAF_CONDUCTOR_URL.
			// The remedy names both the flag and the variable.
			return agentError(env, err)
		}
		res, err := deps.store().Register(env.Context, agent.RegisterOptions{
			BaseURL:  url,
			Name:     opts.name,
			Username: opts.username,
			LogIn:    opts.logIn,
		})
		if err != nil {
			return agentError(env, err)
		}
		// The sentence, then the URL, then the document. The URL is on stderr
		// because it is the one thing a person must act on, and it is NOT on
		// stdout because a machine parsing this document has no use for a
		// browser link and every extra field is a field a parser must ignore.
		sayAgent(env.Stderr, "%s", res.Message)
		if res.ClaimURL != "" {
			sayAgent(env.Stderr, "claim this agent by opening this URL and approving it: %s", res.ClaimURL)
			sayAgent(env.Stderr, "until a human claims it, identity answers every authenticated call with 403 agent_unclaimed")
		}
		sayAgent(env.Stderr, "token_stored: true (the OS keyring; the token itself is never printed)")
		return writeJSONDoc(env.Stdout, res)
	}
	return c
}

// --- login ----------------------------------------------------------------

type agentLoginOptions struct {
	agent   string
	baseURL string
}

func newAgentLoginCommand(deps agentDeps) *Command {
	opts := &agentLoginOptions{}
	c := &Command{
		Name:    "agent login",
		Summary: "make one saved agent session the active one",
		Usage:   "caf agent login [--agent <username>] [--base-url <url>]",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.agent, "agent", "", "the agent username to activate; the active session by default")
			fs.StringVar(&opts.baseURL, "base-url", "", "which saved session when one username has several")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		res, err := deps.store().Login(opts.agent, opts.baseURL)
		if err != nil {
			return agentError(env, err)
		}
		sayAgent(env.Stderr, "%s", res.Message)
		return writeJSONDoc(env.Stdout, res)
	}
	return c
}

// --- whoami ---------------------------------------------------------------

type agentWhoAmIOptions struct {
	agent   string
	baseURL string
}

func newAgentWhoAmICommand(deps agentDeps) *Command {
	opts := &agentWhoAmIOptions{}
	c := &Command{
		Name:    "agent whoami",
		Summary: "who this machine is to identity: the session, the agent and the status",
		Usage:   "caf agent whoami [--agent <username>] [--base-url <url>]",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.agent, "agent", "", "the agent username to ask about; the active session by default")
			fs.StringVar(&opts.baseURL, "base-url", "", "which saved session when one username has several")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		res, err := deps.store().WhoAmI(env.Context, opts.agent, opts.baseURL)
		if err != nil {
			return agentError(env, err)
		}
		sayAgent(env.Stderr, "%s", res.Message)
		// An unclaimed agent is not an error and not a success: the session is
		// real, the token is real, and the one thing missing is a human click.
		// The command says that on stderr and still prints the document, so a
		// machine can read "unclaimed" rather than inferring it from an exit
		// code.
		if strings.EqualFold(res.Agent.Status, "unclaimed") {
			sayAgent(env.Stderr, "this agent is unclaimed; run `caf agent register` for a claim URL and have a human approve it")
		}
		return writeJSONDoc(env.Stdout, res)
	}
	return c
}

// --- token ----------------------------------------------------------------

// agentTokenVerb is one word under `caf agent token`, paired with the command
// that runs it. The same shape `caf box` uses, and the reason is the same: the
// flags belong on the LEAF, because the standard flag package stops at the
// first positional argument and a flag declared on the parent would have to be
// typed before the verb that uses it.
type agentTokenVerb struct {
	name string
	cmd  *Command
}

func agentTokenVerbs(deps agentDeps) []agentTokenVerb {
	return []agentTokenVerb{
		{name: "create", cmd: newAgentTokenCreateCommand(deps)},
		{name: "revoke", cmd: newAgentTokenRevokeCommand(deps)},
		{name: "rotate", cmd: newAgentTokenRotateCommand(deps)},
		{name: "show", cmd: newAgentTokenShowCommand(deps)},
	}
}

// agentTokenOptions is the flag state the four verbs share: one struct, because
// it is one decision — which session to act on — and a fresh one per command, so
// a parsed value can never leak from one verb into the next.
type agentTokenOptions struct {
	agent   string
	baseURL string
	yes     bool
}

func (o *agentTokenOptions) declare(fs *flag.FlagSet) {
	fs.StringVar(&o.agent, "agent", "", "the agent username to act on; the active session by default")
	fs.StringVar(&o.baseURL, "base-url", "", "which saved session when one username has several")
	fs.BoolVar(&o.yes, "yes", false, "confirm revoke without a prompt; revoke is refused without it")
}

const agentTokenLongHelp = "Four verbs over the tokens identity issued for this agent. SHOW lists them —\n" +
	"newest first, revoked included — or shows the one LIVE token with a name, because\n" +
	"identity's one_live_name_per_agent index means a name identifies a live token.\n" +
	"CREATE mints a fresh named token and stores it. ROTATE replaces one, which\n" +
	"invalidates it. REVOKE destroys one and needs --yes.\n" +
	"\n" +
	"EVERY VERB CAN NAME ITS TOKEN: show and create take a name, rotate and revoke\n" +
	"take the token's id, which " + "`caf agent token show`" + " prints. With no argument, rotate and revoke act\n" +
	"on the session's own token — the one in the keyring.\n" +
	"\n" +
	"NONE OF THEM PRINTS A TOKEN'S VALUE. They answer with the keys' metadata and\n" +
	"with whether a token is stored, and the value goes to the OS keyring and\n" +
	"nowhere else. That is invariant 1 in moon/AGENTS-FIRST.md: an agent's\n" +
	"transcript is a log, so nothing long-lived ever enters one."

func newAgentTokenCommand(deps agentDeps) *Command {
	verbs := agentTokenVerbs(deps)
	c := &Command{
		Name:     "agent token",
		Summary:  "the token identity issued for this agent: show, create, rotate and revoke it",
		Usage:    "caf agent token <show|create|rotate|revoke> [flags]",
		LongHelp: agentTokenLongHelp,
	}
	c.Run = func(args []string, env *Env) error {
		// Bare `caf agent token` is a question, not a mistake.
		if len(args) == 0 {
			printHelp(env.Stdout, c)
			return nil
		}
		for _, v := range verbs {
			if v.name == args[0] {
				return v.cmd.Execute(env, args[1:])
			}
		}
		return fmt.Errorf("%w: caf agent token %q is not a command; the verbs are show, create, rotate, revoke", errUsage, args[0])
	}
	return c
}

// tokenVerb is one store method behind the token leaves: the two session
// selectors plus the verb's own argument — a token name for show and create, a
// token id for rotate and revoke. An empty argument is the verb's own default:
// the whole list for show, the CLI's default name for create, the session's own
// token for rotate and revoke.
type tokenVerb func(ctx context.Context, agentUsername, baseURL, arg string) (*agent.TokenDoc, error)

// newAgentTokenLeaf builds one of the token leaves that take at most one
// positional argument: show, create and rotate. It is one constructor because
// they are one command with one shape, and their names are passed in so each
// command's help and its usage errors read as the words the person typed.
// Revoke is separate because it is the only verb that destroys a credential.
func newAgentTokenLeaf(name, summary string, verb tokenVerb) *Command {
	opts := &agentTokenOptions{}
	c := &Command{
		Name:    name,
		Summary: summary,
		Usage:   "caf " + name + " [--agent <username>] [--base-url <url>] [<name-or-token-id>]",
		Flags:   func(fs *flag.FlagSet) { opts.declare(fs) },
	}
	c.Run = func(args []string, env *Env) error {
		arg, err := optionalTokenArg(c.Name, c.Usage, args)
		if err != nil {
			return err
		}
		res, err := verb(env.Context, opts.agent, opts.baseURL, arg)
		if err != nil {
			return agentError(env, err)
		}
		sayAgent(env.Stderr, "%s", res.Message)
		return writeJSONDoc(env.Stdout, res)
	}
	return c
}

// optionalTokenArg is the at-most-one positional argument the token leaves
// take. Two arguments is a usage error: the second would be a second name or
// id, and a verb that silently picked one would be a verb that guessed.
func optionalTokenArg(name, usage string, args []string) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("%w: caf %s wants at most 1 argument, got %d (usage: %s)", errUsage, name, len(args), usage)
	}
	if len(args) == 0 {
		return "", nil
	}
	return strings.TrimSpace(args[0]), nil
}

func newAgentTokenShowCommand(deps agentDeps) *Command {
	return newAgentTokenLeaf("agent token show", "list this agent's tokens, or show one live token by name; the values are never printed", deps.store().TokenShow)
}

func newAgentTokenCreateCommand(deps agentDeps) *Command {
	return newAgentTokenLeaf("agent token create", "mint a fresh named token on identity and store it in the OS keyring", deps.store().TokenCreate)
}

func newAgentTokenRotateCommand(deps agentDeps) *Command {
	return newAgentTokenLeaf("agent token rotate", "replace a token with a fresh one; the session's own when no id is given", deps.store().TokenRotate)
}

// newAgentTokenRevokeCommand is separate from the other three for one reason: it
// is the only verb in this whole tree that destroys a credential, and it refuses
// without an explicit --yes. That is invariant 5 — a human approves irreversible
// things out of band — applied to the one thing this command group can make
// irreversible.
func newAgentTokenRevokeCommand(deps agentDeps) *Command {
	opts := &agentTokenOptions{}
	c := &Command{
		Name:    "agent token revoke",
		Summary: "destroy a token and remove it from the OS keyring; the session's own when no id is given",
		Usage:   "caf agent token revoke --yes [--agent <username>] [--base-url <url>] [<token-id>]",
		Flags:   func(fs *flag.FlagSet) { opts.declare(fs) },
	}
	c.Run = func(args []string, env *Env) error {
		tokenID, err := optionalTokenArg(c.Name, c.Usage, args)
		if err != nil {
			return err
		}
		if !opts.yes {
			return fmt.Errorf("%w: caf agent token revoke refuses to destroy a token without --yes; it cannot be undone from here (usage: %s)", errUsage, c.Usage)
		}
		res, err := deps.store().TokenRevoke(env.Context, opts.agent, opts.baseURL, tokenID)
		if err != nil {
			return agentError(env, err)
		}
		sayAgent(env.Stderr, "%s", res.Message)
		return writeJSONDoc(env.Stdout, res)
	}
	return c
}
