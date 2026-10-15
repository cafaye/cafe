package cli

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cafaye/cafe/internal/box"
	"github.com/cafaye/cafe/internal/provider"
)

// providerDeps is what `cafe provider` needs from the world, and it is a struct
// of parameters for the reason boxDeps is: a test can hand the command a
// keyring it owns, a control plane that counts and a directory it controls, and
// drive a whole connect without a network, a keychain or a real box.
type providerDeps struct {
	// dir is where box.json and the keyring live. Empty means the resolved
	// box-owned default — defaultBoxDir, the same directory `cafe box`
	// resolves — and a test always sets it. A provider verb reads the BOX's
	// identity, so it resolves the BOX subject's directory; an agent's config
	// dir would be a second opinion about where the box is.
	dir string
	// ring holds the box's private key. Nil means a file ring under the
	// box-owned store directory, and a test always sets it.
	ring box.Keyring
	// controlPlane connects and reports status. Nil means a real HTTP client, which
	// is why a test always sets it: the whole point of the seam is the count.
	controlPlane provider.ControlPlane
	// url is box's endpoint. Empty means the CAF_BOX_URL
	// environment variable, and an empty environment variable is a refusal
	// rather than a guess.
	url string
}

func (d providerDeps) withDefaults() providerDeps {
	if d.dir == "" {
		d.dir = defaultBoxDir()
	}
	if d.url == "" {
		d.url = os.Getenv(boxURLEnv)
	}
	if d.ring == nil {
		d.ring = box.FileRing{Dir: filepath.Join(d.dir, "keyring")}
	}
	if d.controlPlane == nil {
		d.controlPlane = provider.HTTPControlPlane{BaseURL: d.url}
	}
	return d
}

// store builds the box store the verbs read the identity from. The box's own
// ControlPlane (the capability redemption) is nil because no provider verb
// redeems anything: they read the identity and talk to box's connection
// endpoint, which is the provider.ControlPlane above. A store with no box
// ControlPlane is a store that cannot enroll, and that is exactly what a provider
// command must not be able to do.
func (d providerDeps) store() *box.Store {
	d = d.withDefaults()
	return box.NewStore(d.dir, d.ring, nil)
}

// providerVerb is one word under `cafe provider`, paired with the command that
// runs it. The same shape `cafe box` uses.
type providerVerb struct {
	name string
	cmd  *Command
}

func providerVerbs(deps providerDeps) []providerVerb {
	return []providerVerb{
		{name: "connect", cmd: newProviderConnectCommand(deps)},
		{name: "status", cmd: newProviderStatusCommand(deps)},
	}
}

// providerLongHelp is the prose under `cafe provider`'s usage line.
const providerLongHelp = `A provider connection is a credential SEALED TO THE BOX. The agent
that runs ` + "`cafe provider connect`" + ` is a courier of the envelope and never a
holder of the value: the credential is sealed to the box's public key in
memory at the moment it is read, and the envelope — ciphertext box
cannot open — is the only thing that crosses the wire. The credential is never
printed, never logged, and never stored anywhere but inside the envelope.

Box holds the envelope and cannot open it. The box, holding the private
half of its keypair, is the only thing that can. That is boundary 6, and it is
why the agent's transcript — which is a log, stored by the agent host and
replayed into model providers — never carries a provider secret.

` + "`cafe provider status`" + ` reports what box holds and opens nothing: the
envelopes come back as opaque JSON and are passed through untouched.

stdout carries the document, stderr carries the sentence. Neither carries a
credential.`

func newProviderCommand(deps providerDeps) *Command {
	verbs := providerVerbs(deps)
	c := &Command{
		Name:     "provider",
		Summary:  "connect an email provider to this box, sealed to the box's key, or report the connection state",
		Usage:    "cafe provider <connect|status> [flags] [arguments]",
		LongHelp: providerLongHelp,
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) == 0 {
			printHelp(env.Stdout, c)
			return nil
		}
		verb, found := lookupProviderVerb(verbs, args[0])
		if !found {
			return unknownProviderVerb(args[0], verbs)
		}
		return verb.cmd.Execute(env, args[1:])
	}
	return c
}

func lookupProviderVerb(verbs []providerVerb, name string) (providerVerb, bool) {
	for _, v := range verbs {
		if v.name == name {
			return v, true
		}
	}
	return providerVerb{}, false
}

func unknownProviderVerb(name string, verbs []providerVerb) error {
	names := make([]string, len(verbs))
	for i, v := range verbs {
		names[i] = v.name
	}
	return fmt.Errorf("%w: cafe provider %q is not a command; the verbs are %s", errUsage, name, strings.Join(names, ", "))
}

// --- connect ----------------------------------------------------------------

// providerConnectOptions is `cafe provider connect`'s flag state.
type providerConnectOptions struct {
	account    string
	credential string
	box        string
	config     configFlags
}

// configFlags collects repeated --config key=value flags into a map. It is a
// flag.Value so the flag package does the splitting and the command gets a
// map it can hand to box directly.
type configFlags struct {
	pairs map[string]string
}

func (c *configFlags) String() string {
	if len(c.pairs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(c.pairs))
	for k, v := range c.pairs {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

func (c *configFlags) Set(s string) error {
	if c.pairs == nil {
		c.pairs = map[string]string{}
	}
	k, v, found := strings.Cut(s, "=")
	if !found {
		return fmt.Errorf("--config wants key=value, got %q", s)
	}
	k = strings.TrimSpace(k)
	if k == "" {
		return fmt.Errorf("--config wants a non-empty key, got %q", s)
	}
	c.pairs[k] = v
	return nil
}

func newProviderConnectCommand(deps providerDeps) *Command {
	opts := &providerConnectOptions{}
	c := &Command{
		Name:    "provider connect",
		Summary: "seal a provider credential to the box's public key and post the envelope to box",
		Usage:   "cafe provider connect --account <id> [--credential <secret> | stdin] [--config key=value]… <provider>",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.account, "account", "", "the provider-side account identifier (an email address, a zone id, …)")
			fs.StringVar(&opts.credential, "credential", "", "the provider secret; sealed to the box and never printed. Read from stdin when omitted")
			fs.StringVar(&opts.box, "box", "", "box's origin; without it, the box identity's control plane or "+boxURLEnv+" is used")
			fs.Var(&opts.config, "config", "a non-secret provider setting, key=value; repeat for each")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 1, len(args)); err != nil {
			return err
		}
		prov := strings.TrimSpace(args[0])
		if !knownProvider(prov) {
			return fmt.Errorf("%w: cafe provider connect %q is not a provider; the providers are %s", errUsage, prov, strings.Join(provider.Providers, ", "))
		}
		if strings.TrimSpace(opts.account) == "" {
			return fmt.Errorf("%w: cafe provider connect wants --account <id>, the provider-side account identifier (usage: %s)", errUsage, c.Usage)
		}

		credential, err := opts.credentialValue(env)
		if err != nil {
			return err
		}

		store := deps.store()
		id, err := store.Identity()
		if err != nil {
			// An unenrolled box is not a failure, it is a fact with a remedy:
			// there is no key to seal to, and an envelope sealed to nobody is
			// a credential posted in the open.
			if errors.Is(err, box.ErrNotEnrolled) {
				return fmt.Errorf("%w: this box is not enrolled; run `cafe box enroll --token …` first so there is a key to seal to", err)
			}
			return err
		}

		// The control plane is the seam, and it is honored rather than overwritten:
		// a test injects one and the network is never touched, for the reason
		// boxDeps' control plane is. Nil means the real client, and the URL it
		// dials is the flag, then the identity the box enrolled against, then
		// the environment — the same order box enroll resolves it in.
		if deps.controlPlane == nil {
			url := opts.box
			if url == "" {
				url = id.ControlPlane
			}
			if url == "" {
				url = os.Getenv(boxURLEnv)
			}
			deps.controlPlane = provider.HTTPControlPlane{BaseURL: url}
		}

		// The credential is sealed to the box's public key and the envelope is
		// posted. The plaintext exists only inside provider.Connect's call
		// frame; nothing here keeps a copy, and nothing printed below can
		// carry one.
		public, err := decodeBoxPublic(id.PublicKey)
		if err != nil {
			return err
		}
		conn, err := provider.Connect(env.Context, deps.controlPlane, provider.ConnectRequest{
			Provider:   prov,
			Account:    opts.account,
			BoxID:      id.BoxID,
			Config:     opts.config.pairs,
			Credential: credential,
			BoxPublic:  public,
		})
		if err != nil {
			_, _ = fmt.Fprintf(env.Stderr, "cafe provider connect: %v\n", err)
			_, _ = fmt.Fprintf(env.Stderr, "cafe provider connect: the credential was NOT posted in plaintext and nothing was stored\n")
			return errReported
		}

		say(env.Stderr, "connected %s to %s (box %s); the credential is sealed to the box and box holds only the envelope", conn.Provider, conn.Account, conn.BoxID)
		return writeJSONDoc(env.Stdout, &ConnectResult{
			Provider:  conn.Provider,
			Account:   conn.Account,
			BoxID:     conn.BoxID,
			Config:    conn.Config,
			Connected: true,
			Message:   "the credential is sealed to the box; box holds the envelope and cannot open it",
		})
	}
	return c
}

// credentialValue resolves the credential from the flag or, when the flag is
// empty, from stdin. Reading from stdin is the more secure path — a flag is
// visible in the process list, a pipe is not — and it is the one a person
// pasting a secret into a terminal should take. Either way the value is
// returned once and dropped: it is sealed by the caller and never printed.
func (o *providerConnectOptions) credentialValue(env *Env) (string, error) {
	if strings.TrimSpace(o.credential) != "" {
		return o.credential, nil
	}
	// No flag: read from stdin, but only if stdin is not a terminal. A
	// terminal means a person is at the keyboard and the command is waiting on
	// them; refusing with the flag's name is a better answer than a hang.
	if stdinIsTerminal() {
		return "", fmt.Errorf("%w: cafe provider connect wants --credential <secret>, or pipe it on stdin (usage: %s)", errUsage, "cafe provider connect --account <id> --credential <secret> <provider>")
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("cafe provider connect: read the credential from stdin: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// stdinIsTerminal reports whether stdin is a character device, which is the
// shape a terminal has and a pipe or a redirect does not.
func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// decodeBoxPublic reads the box's public key out of the identity. It is base64
// raw (internal/box's publicKeyText), 32 bytes, and it is the key the
// credential is sealed TO.
func decodeBoxPublic(text string) (ed25519.PublicKey, error) {
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(text))
	if err != nil {
		return nil, fmt.Errorf("the box identity's public key is not base64: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("the box identity's public key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// knownProvider reports whether name is one box accepts.
func knownProvider(name string) bool {
	for _, p := range provider.Providers {
		if p == name {
			return true
		}
	}
	return false
}

// ConnectResult is what `cafe provider connect` reports. It carries every fact
// about the connection that is not a secret — which is all of them: the
// credential is inside the envelope box holds, and this document holds
// only the envelope's shadow.
type ConnectResult struct {
	Provider  string            `json:"provider"`
	Account   string            `json:"account"`
	BoxID     string            `json:"box_id"`
	Config    map[string]string `json:"config"`
	Connected bool              `json:"connected"`
	Message   string            `json:"message"`
}

// --- status -----------------------------------------------------------------

// providerStatusOptions is `cafe provider status`'s flag state.
type providerStatusOptions struct {
	json bool
	box  string
}

func newProviderStatusCommand(deps providerDeps) *Command {
	opts := &providerStatusOptions{}
	c := &Command{
		Name:    "provider status",
		Summary: "which providers this box is connected to, as box reports it",
		Usage:   "cafe provider status [--json] [--box <url>]",
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&opts.json, "json", false, "print the machine document on stdout instead of the human one")
			fs.StringVar(&opts.box, "box", "", "box's origin; without it, the box identity's control plane or "+boxURLEnv+" is used")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		store := deps.store()
		id, err := store.Identity()
		if err != nil {
			if errors.Is(err, box.ErrNotEnrolled) {
				// An unenrolled box has no connections, and that is the honest
				// answer — not a failure. The remedy is named.
				say(env.Stderr, "this box is not enrolled, so it has no provider connections; run `cafe box enroll --token …` first")
				return writeJSONDoc(env.Stdout, &StatusResult{Connections: []ConnectionSummary{}})
			}
			return err
		}

		if deps.controlPlane == nil {
			url := opts.box
			if url == "" {
				url = id.ControlPlane
			}
			if url == "" {
				url = os.Getenv(boxURLEnv)
			}
			deps.controlPlane = provider.HTTPControlPlane{BaseURL: url}
		}

		conns, err := provider.Status(env.Context, deps.controlPlane, id.BoxID)
		if err != nil {
			_, _ = fmt.Fprintf(env.Stderr, "cafe provider status: %v\n", err)
			return errReported
		}

		summaries := make([]ConnectionSummary, 0, len(conns))
		for _, conn := range conns {
			summaries = append(summaries, ConnectionSummary{
				Provider:  conn.Provider,
				Account:   conn.Account,
				Connected: true,
			})
		}
		connected := len(summaries) > 0
		if connected {
			say(env.Stderr, "box %s is connected to %d provider(s)", id.BoxID, len(summaries))
		} else {
			say(env.Stderr, "box %s has no provider connections", id.BoxID)
		}
		return writeProviderDoc(env.Stdout, &StatusResult{BoxID: id.BoxID, Connected: connected, Connections: summaries}, opts.json)
	}
	return c
}

// StatusResult is what `cafe provider status` reports. It opens no envelope and
// carries no credential: the connections are reported as the provider/account
// pairs they are, and the envelopes box holds stay opaque.
type StatusResult struct {
	BoxID       string              `json:"box_id"`
	Connected   bool                `json:"connected"`
	Connections []ConnectionSummary `json:"connections"`
}

// ConnectionSummary is one connection as a status report names it: which
// provider, which account. Never the envelope, never the credential.
type ConnectionSummary struct {
	Provider  string `json:"provider"`
	Account   string `json:"account"`
	Connected bool   `json:"connected"`
}

// writeProviderDoc renders a status result either as JSON or as a human block.
// It is here rather than in the shared writeDoc because the shared one switches
// on box types; this one is the provider's own.
func writeProviderDoc(w io.Writer, v *StatusResult, asJSON bool) error {
	if asJSON {
		return writeJSON(v, w)
	}
	if len(v.Connections) == 0 {
		return writeHuman(w, "box "+v.BoxID+" has no provider connections")
	}
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "box %s is connected to:\n", v.BoxID)
	for _, c := range v.Connections {
		_, _ = fmt.Fprintf(&b, "  %s\t%s\n", c.Provider, c.Account)
	}
	return writeHuman(w, b.String())
}
