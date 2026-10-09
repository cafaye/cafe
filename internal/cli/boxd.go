package cli

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cafaye/caf/internal/boxd"
)

// `caf boxd` — the box daemon.
//
// This is the one caf subcommand that is a long-running process rather than a
// command: it runs on the customer's box (as root, because the substrate runs
// as root), dials out to conductor over mTLS, and redeems capabilities against
// the whitelisted route table in internal/boxd. It is invariant 4's home: the
// enforcement of what the box can be told to do lives here, on the box, in
// code — not in an agent's instructions, which are advisory.
//
// The daemon opens no listener. Nothing about the box is reachable from
// outside; the only connection is the one this process makes outbound. That is
// the same sentence AGENTS-FIRST says about it — "outbound-only mTLS" — and it
// is why the flags below name where the enrolled identity is and nothing else:
// there is no -listen, and adding one would be a different daemon.

// boxdDeps is what `caf boxd` needs from the world, as a parameter so a test
// can hand the command a recording identity store, an in-memory key, a fake
// conductor and a scratch state file — and drive the whole daemon without a
// network, a box, or a certificate anywhere outside the test.
type boxdDeps struct {
	store    boxd.IdentityStore
	keys     boxd.KeySource
	dial     boxd.Dialer
	worldFor func(id boxd.Identity) boxd.World
	// statePath is where the daemon's own recorded phase lives. Empty means
	// beside the identity, and a test always sets it.
	statePath string
}

func (d boxdDeps) withDefaults() boxdDeps {
	if d.statePath == "" {
		d.statePath = defaultBoxStateFile
	}
	return d
}

// defaultBoxStateFile is the daemon's own state, beside the enrolled identity
// it belongs to: the recorded plan phase, and the phases already applied. It
// is a file because the box is one machine and the resumable-install model
// (W5-P4) is file-backed for exactly that reason — and because a daemon that
// kept its state in memory would restart every connection at phase 0.
const defaultBoxStateFile = "boxd-state.json"

type boxdOptions struct {
	identity string
	once     bool
}

func newBoxdCommand(deps boxdDeps) *Command {
	opts := &boxdOptions{}
	c := &Command{
		Name:    "boxd",
		Summary: "run the box daemon: outbound-only mTLS to conductor, redeeming whitelisted capabilities",
		Usage:   "caf boxd [flags]",
		LongHelp: "Runs the box daemon. It is a long-running process: it loads the enrolled\n" +
			"identity, dials conductor outbound over mTLS, and waits for capabilities.\n" +
			"Each capability names a route in the box's whitelisted table and is redeemed\n" +
			"against it — or refused by name, if the route is not in the table.\n" +
			"\n" +
			"The daemon opens no listener and accepts no inbound connection. The only\n" +
			"channel is the one it makes, outbound, to the conductor named in the enrolled\n" +
			"identity — which is what \"outbound-only mTLS\" means here, and what makes the\n" +
			"box unreachable from the network even if something wanted to reach it.\n" +
			"\n" +
			"The route table is the security boundary (AGENTS-FIRST invariant 4). There is\n" +
			"no arbitrary-exec route and no secret-read route, and a route that could\n" +
			"answer with a Secret-shaped value is refused by the test that renders the\n" +
			"table — so an agent operating the box cannot escalate by asking the box to\n" +
			"do something; it can only ask for what the box can do.\n" +
			"\n" +
			"Needs an enrolled box: run `caf box enroll --token …` first. Without the\n" +
			"identity it prints that and exits rather than running unenrolled.",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.identity, "identity", "", "path to the enrolled box identity; $CAF_BOX_IDENTITY or the user state directory")
			fs.BoolVar(&opts.once, "once", false, "redeem at most one capability and exit; one round under an operator's eye")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		return runBoxd(opts, env, deps)
	}
	return c
}

func runBoxd(opts *boxdOptions, env *Env, deps boxdDeps) error {
	deps = deps.withDefaults()
	ctx := envOrBackground(*env)

	identityPath, err := resolveBoxIdentityPath(opts.identity)
	if err != nil {
		return fmt.Errorf("caf boxd: %w", err)
	}
	if _, err := os.Stat(identityPath); err != nil {
		// An unenrolled box is a box that was never told what it is. The
		// message names the one command that fixes it, because a daemon that
		// cannot say what it lacks is a daemon nobody can act on.
		return fmt.Errorf("caf boxd: no enrolled identity at %s; run `caf box enroll --token …` "+
			"on this box first (enrollment is the W2-P6 packet)", identityPath)
	}

	store, err := newFileIdentityStore(identityPath)
	if err != nil {
		return fmt.Errorf("caf boxd: %w", err)
	}
	if deps.store == nil {
		deps.store = store
	}
	if deps.keys == nil {
		deps.keys = store
	}
	if deps.dial == nil {
		deps.dial = boxd.NewTLSDialer(store)
	}
	if deps.worldFor == nil {
		statePath := deps.statePath
		if statePath == "" {
			statePath = boxStatePath(identityPath)
		}
		deps.worldFor = func(id boxd.Identity) boxd.World { return newBoxWorld(id, statePath) }
	}

	daemon, err := boxd.Open(boxd.Options{
		Store:    deps.store,
		Keys:     deps.keys,
		Dial:     deps.dial,
		WorldFor: deps.worldFor,
		Log:      env.Stderr,
		Once:     opts.once,
	})
	if err != nil {
		return fmt.Errorf("caf boxd: %w", err)
	}
	return daemon.Run(ctx)
}

// resolveBoxIdentityPath finds the enrolled identity: the flag, then the
// environment, then the user state directory.
func resolveBoxIdentityPath(flagPath string) (string, error) {
	if flagPath != "" {
		return flagPath, nil
	}
	if fromEnv := os.Getenv("CAF_BOX_IDENTITY"); fromEnv != "" {
		return fromEnv, nil
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find the home directory: %w", err)
	}
	return filepath.Join(dir, ".config", "caf", "box-identity.json"), nil
}

// ---------------------------------------------------------------------------
// the enrolled identity, on disk
// ---------------------------------------------------------------------------

// identityDocument is what `caf box enroll` (W2-P6) writes and this daemon
// reads: the box's name, where its conductor is, and where its certificate
// and key live. The paths are relative to the document, so an enrollment is
// one directory a backup can carry.
//
// The document is the SEAM between the two packets. Enrollment owns the
// keyring and the format going forward; this is the shape this daemon reads,
// written down here rather than discovered from a file that does not exist
// yet, so both sides have a contract to be checked against.
type identityDocument struct {
	BoxID       string `json:"box_id"`
	Conductor   string `json:"conductor"`
	Version     string `json:"version"`
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
}

// fileIdentityStore reads the enrolled identity, and is the KeySource too:
// the box's certificate and key are loaded from disk per connection, which is
// what keeps the key's lifetime the connection's rather than the daemon's.
type fileIdentityStore struct {
	path string
	dir  string
}

func newFileIdentityStore(path string) (*fileIdentityStore, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", path, err)
	}
	return &fileIdentityStore{path: abs, dir: filepath.Dir(abs)}, nil
}

func (s *fileIdentityStore) read() (identityDocument, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return identityDocument{}, fmt.Errorf("read the enrolled identity: %w", err)
	}
	var doc identityDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return identityDocument{}, fmt.Errorf("the enrolled identity at %s is not the JSON this daemon reads: %w", s.path, err)
	}
	if doc.BoxID == "" {
		return identityDocument{}, fmt.Errorf("the enrolled identity at %s has no box_id", s.path)
	}
	if doc.Conductor == "" {
		return identityDocument{}, fmt.Errorf("the enrolled identity at %s has no conductor", s.path)
	}
	return doc, nil
}

// Load implements boxd.IdentityStore.
func (s *fileIdentityStore) Load(context.Context) (boxd.Identity, error) {
	doc, err := s.read()
	if err != nil {
		return boxd.Identity{}, err
	}
	return boxd.Identity{BoxID: doc.BoxID, Conductor: doc.Conductor, Version: doc.Version}, nil
}

// Certificate implements boxd.KeySource: the client certificate for one
// handshake, read from disk at the dial. The key is never cached here — a
// store that held it between dials would hold the box key for the life of the
// process, which is the memory-lifetime rule this daemon exists to keep.
func (s *fileIdentityStore) Certificate(context.Context) (tls.Certificate, error) {
	doc, err := s.read()
	if err != nil {
		return tls.Certificate{}, err
	}
	if doc.Certificate == "" || doc.Key == "" {
		return tls.Certificate{}, fmt.Errorf("the enrolled identity names no certificate and key")
	}
	cert, err := tls.LoadX509KeyPair(s.resolve(doc.Certificate), s.resolve(doc.Key))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("load the box keypair: %w", err)
	}
	return cert, nil
}

// resolve makes a path from the identity document relative to the document
// itself unless it is absolute.
func (s *fileIdentityStore) resolve(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(s.dir, p)
}

// boxStatePath is the state file beside a given identity file.
func boxStatePath(identityPath string) string {
	return filepath.Join(filepath.Dir(identityPath), "boxd-state.json")
}

// ---------------------------------------------------------------------------
// the world the routes touch
// ---------------------------------------------------------------------------

// boxWorld is the v0 world the routes see. It is honest about what the box
// can do in v0 and refuses the rest by name:
//
//   - Stack and Apply are real, backed by the daemon's own state file: the
//     recorded phase of the install plan, and an idempotent apply.
//   - Envelope is refused with the sentence that says why: opening a sealed
//     provider credential and installing it as a Kubernetes Secret needs the
//     substrate on the box, and that lands with the substrate packet. A route
//     the world cannot fulfil yet answers `failed` with that reason — never a
//     silent success, which would be the box lying to conductor about a
//     credential being installed.
//
// The world is built per connection with the identity that connection loaded,
// so Redeem's box binding compares the capability against the enrollment this
// box is running as right now.
type boxWorld struct {
	id   boxd.Identity
	path string
}

func newBoxWorld(id boxd.Identity, statePath string) boxWorld {
	return boxWorld{id: id, path: statePath}
}

// boxState is the daemon's own record: which phase the plan reached and which
// phases have been applied. Idempotence is the point — a retried capability
// for an applied phase applies nothing and says so.
type boxState struct {
	Phase   int   `json:"phase"`
	Applied []int `json:"applied"`
}

func (w boxWorld) load() (boxState, error) {
	data, err := os.ReadFile(w.path)
	if errors.Is(err, os.ErrNotExist) {
		return boxState{}, nil
	}
	if err != nil {
		return boxState{}, fmt.Errorf("read the box state: %w", err)
	}
	var state boxState
	if err := json.Unmarshal(data, &state); err != nil {
		return boxState{}, fmt.Errorf("the box state at %s is not JSON: %w", w.path, err)
	}
	return state, nil
}

func (w boxWorld) save(state boxState) error {
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return fmt.Errorf("create the box state directory: %w", err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := os.WriteFile(w.path, data, 0o600); err != nil {
		return fmt.Errorf("write the box state: %w", err)
	}
	return nil
}

func (w boxWorld) applied(phase int) bool {
	state, err := w.load()
	if err != nil {
		return false
	}
	for _, p := range state.Applied {
		if p == phase {
			return true
		}
	}
	return false
}

// Identity implements boxd.World's first question: the enrollment this
// connection loaded. The daemon built this world from the identity at dial
// time, so the answer is the one Redeem compares a capability's binding
// against.
func (w boxWorld) Identity() boxd.Identity { return w.id }

// Stack reports the recorded phase of the install plan. Service liveness is
// the substrate's business and is not claimed here: a stack.status that
// named services nothing is watching would be a claim rather than a fact,
// which is the pantry principle and it holds.
func (w boxWorld) Stack(context.Context) (boxd.StackStatus, error) {
	state, err := w.load()
	if err != nil {
		return boxd.StackStatus{}, err
	}
	detail := "the recorded install plan phase; the substrate's own service list lands with the substrate packet"
	if len(state.Applied) > 0 {
		detail = "the recorded install plan phase, from the daemon's state file"
	}
	return boxd.StackStatus{Phase: state.Phase, Detail: detail}, nil
}

// Apply records one applied phase. Idempotent by construction — agents retry,
// so a capability replayed for an already-applied phase applies nothing and
// reports zero applications rather than failing.
func (w boxWorld) Apply(_ context.Context, phase int) (boxd.ApplyResult, error) {
	state, err := w.load()
	if err != nil {
		return boxd.ApplyResult{}, err
	}
	if phase < 0 {
		return boxd.ApplyResult{}, fmt.Errorf("phase %d is not a phase", phase)
	}
	for _, p := range state.Applied {
		if p == phase {
			return boxd.ApplyResult{Applied: 0, Phase: state.Phase, Detail: "already applied"}, nil
		}
	}
	state.Applied = append(state.Applied, phase)
	state.Phase = phase
	if err := w.save(state); err != nil {
		return boxd.ApplyResult{}, err
	}
	return boxd.ApplyResult{Applied: 1, Phase: phase, Detail: "applied"}, nil
}

// Envelope refuses, by name, what v0 cannot do. The route exists in the
// whitelist and the capability for it is valid; the world cannot fulfil it
// yet, and saying so is the difference between a capability that comes back
// `failed` with a reason and a capability that looks like it worked.
func (w boxWorld) Envelope(context.Context, boxd.Envelope) (boxd.EnvelopeResult, error) {
	return boxd.EnvelopeResult{}, fmt.Errorf("opening a sealed envelope and installing a Kubernetes Secret needs the substrate; " +
		"that lands with the substrate packet, until then this route cannot be fulfilled")
}
