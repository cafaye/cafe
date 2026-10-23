package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anywaye/cafe/internal/backup"
	"github.com/anywaye/cafe/internal/box"
	"github.com/anywaye/cafe/internal/boxd"
)

// `cafe boxd` — the box daemon.
//
// This is the one cafe subcommand that is a long-running process rather than a
// command: it runs on the customer's box (as root, because the substrate runs
// as root), dials out to box over mTLS, and redeems capabilities against
// the whitelisted route table in internal/boxd. It is invariant 4's home: the
// enforcement of what the box can be told to do lives here, on the box, in
// code — not in an agent's instructions, which are advisory.
//
// The daemon opens no listener. Nothing about the box is reachable from
// outside; the only connection is the one this process makes outbound. That is
// the same sentence AGENTS-FIRST says about it — "outbound-only mTLS" — and it
// is why the flags below name where the enrolled identity is and nothing else:
// there is no -listen, and adding one would be a different daemon.

// boxdDeps is what `cafe boxd` needs from the world, as a parameter so a test
// can hand the command a recording identity store, an in-memory key, a fake
// control plane and a scratch state file — and drive the whole daemon without a
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
	report   bool
}

func newBoxdCommand(deps boxdDeps) *Command {
	opts := &boxdOptions{}
	c := &Command{
		Name:    "boxd",
		Summary: "run the box daemon: outbound-only mTLS to box, redeeming whitelisted capabilities",
		Usage:   "cafe boxd [flags]",
		LongHelp: "Runs the box daemon. It is a long-running process: it loads the enrolled\n" +
			"identity, dials box outbound over mTLS, and waits for capabilities.\n" +
			"Each capability names a route in the box's whitelisted table and is redeemed\n" +
			"against it — or refused by name, if the route is not in the table.\n" +
			"\n" +
			"The daemon opens no listener and accepts no inbound connection. The only\n" +
			"channel is the one it makes, outbound, to the control plane named in the enrolled\n" +
			"identity — which is what \"outbound-only mTLS\" means here, and what makes the\n" +
			"box unreachable from the network even if something wanted to reach it.\n" +
			"\n" +
			"The route table is the security boundary (AGENTS-FIRST invariant 4). There is\n" +
			"no arbitrary-exec route and no secret-read route, and a route that could\n" +
			"answer with a Secret-shaped value is refused by the test that renders the\n" +
			"table — so an agent operating the box cannot escalate by asking the box to\n" +
			"do something; it can only ask for what the box can do.\n" +
			"\n" +
			"Needs an enrolled box: run `cafe box enroll --token …` first. Without the\n" +
			"identity it prints that and exits rather than running unenrolled.\n" +
			"\n" +
			"The identity comes from the box-owned store (/var/lib/cafe/box, or $CAF_BOX_DIR)\n" +
			"when one is enrolled there — the same store `cafe box enroll` writes and every\n" +
			"`cafe box` verb reads. $CAF_BOX_IDENTITY is still honored for a box that has not\n" +
			"been migrated yet, but when both exist the STORE WINS and this daemon says so;\n" +
			"with only the old file, the daemon runs as it always did and names the store.",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.identity, "identity", "", "path to the enrolled box identity; overrides the box store and $CAF_BOX_IDENTITY")
			fs.BoolVar(&opts.once, "once", false, "redeem at most one capability and exit; one round under an operator's eye")
			fs.BoolVar(&opts.report, "report", false, "print this box's observe.report document and exit; no dial, no capability, nothing changed")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		if opts.report {
			return reportBoxd(opts, env, deps)
		}
		return runBoxd(opts, env, deps)
	}
	return c
}

// reportBoxd is the one boxd verb that is not a long-running process: it
// assembles the observation this box would send over the wire and prints it,
// then exits.
//
// It exists because the daemon is outbound-only and a controller therefore has
// nothing to ask. `cafe box setup` needs to prove the heartbeat — that the
// daemon loads its enrollment and answers an observe.report — and the only
// honest place to assemble that document is where the world already can, from
// the same code the wire path uses. A second reporter would be a second thing
// that could disagree with the first about what the box's health is.
//
// It dials nothing, mints nothing and changes nothing: it is a read, which is
// why it is safe for a bootstrap log to run it on every setup.
func reportBoxd(opts *boxdOptions, env *Env, deps boxdDeps) error {
	src, err := resolveBoxdIdentity(opts.identity, env.Version.Semver)
	if err != nil {
		return fmt.Errorf("cafe boxd: %w", err)
	}
	if src.notice != "" {
		_, _ = fmt.Fprintf(env.Stderr, "boxd: %s\n", src.notice)
	}
	id, err := src.enrolled(env.Context)
	if err != nil {
		return err
	}
	statePath := deps.statePath
	if statePath == "" {
		statePath = filepath.Join(src.dir, defaultBoxStateFile)
	}
	var world boxd.World
	if deps.worldFor != nil {
		world = deps.worldFor(id)
	} else {
		world = newBoxWorldWithKeys(id, src.keys, statePath)
	}
	report, err := world.Observe(env.Context, boxd.ObserveParams{})
	if err != nil {
		return fmt.Errorf("cafe boxd: assemble the box's report: %w", err)
	}
	return writeJSONDoc(env.Stdout, report)
}

func runBoxd(opts *boxdOptions, env *Env, deps boxdDeps) error {
	deps = deps.withDefaults()
	ctx := envOrBackground(*env)

	src, err := resolveBoxdIdentity(opts.identity, env.Version.Semver)
	if err != nil {
		return fmt.Errorf("cafe boxd: %w", err)
	}
	if src.notice != "" {
		_, _ = fmt.Fprintf(env.Stderr, "boxd: %s\n", src.notice)
	}
	if _, err := src.enrolled(ctx); err != nil {
		// An unenrolled box is a box that was never told what it is. The
		// message names the one command that fixes it, because a daemon that
		// cannot say what it lacks is a daemon nobody can act on.
		return err
	}
	if deps.store == nil {
		deps.store = src.store
	}
	if deps.keys == nil {
		deps.keys = src.keys
	}
	if deps.dial == nil {
		deps.dial = boxd.NewTLSDialer(deps.keys)
	}
	if deps.worldFor == nil {
		keys := deps.keys
		statePath := deps.statePath
		if statePath == "" {
			statePath = filepath.Join(src.dir, defaultBoxStateFile)
		}
		deps.worldFor = func(id boxd.Identity) boxd.World { return newBoxWorldWithKeys(id, keys, statePath) }
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
		return fmt.Errorf("cafe boxd: %w", err)
	}
	return daemon.Run(ctx)
}

// boxdIdentityEnv is the tunnel-era variable that named where an enrolled
// identity document lived. It is still honored, but the box store outranks it;
// see resolveBoxdIdentity for the whole argument.
const boxdIdentityEnv = "CAF_BOX_IDENTITY"

// boxdIdentity is where the daemon's identity comes from, resolved once at
// startup. The two seams boxd.Open takes are a PAIR — the identity to dial as
// and the client certificate to dial with — and resolving them together is
// what keeps them from being the box store's identity and the env-file's
// certificate. That mismatch is the measured defect this packet fixes: a
// re-enroll wrote a new keypair and certificate to the store while the daemon
// kept presenting the old certificate from /etc/cafe, and the server answered
// x509: unknown certificate authority.
type boxdIdentity struct {
	store boxd.IdentityStore
	keys  boxd.KeySource
	// path is the identity document when the source is a file; empty for the
	// store. where() is what a refusal names.
	path string
	// dir is the source's directory: the store's, or the document's. It is
	// where the daemon's own state file defaults to.
	dir string
	// fromStore is true when the box-owned store answered. It is a field
	// rather than a comparison so a test asserts the decision, not a path.
	fromStore bool
	// notice is one sentence for the daemon's log: the store winning over an
	// env-file, or the env-file's deprecation. Empty when there is nothing to
	// say, and the caller prints it exactly once.
	notice string
}

func (b boxdIdentity) where() string {
	if b.fromStore {
		return b.dir
	}
	return b.path
}

// enrolled loads the identity or refuses with the sentence that names the one
// command that fixes it. It is the read both the daemon path and the report
// path share, so both refuse an unenrolled box with the same words.
func (b boxdIdentity) enrolled(ctx context.Context) (boxd.Identity, error) {
	id, err := b.store.Load(ctx)
	if err != nil {
		return boxd.Identity{}, fmt.Errorf("cafe boxd: no enrolled identity at %s; run `cafe box enroll --token …` "+
			"on this box first (enrollment is the W2-P6 packet): %w", b.where(), err)
	}
	return id, nil
}

// resolveBoxdIdentity finds the identity the daemon runs as. The box-owned
// STORE wins over the tunnel-era $CAF_BOX_IDENTITY file: the store is what
// enrollment writes, what the migration owns, and what every `cafe box` verb
// reads, so a daemon that read the env-file first would present a certificate
// the store had already replaced — the exact divergence measured on Contabo
// 2026-10-15. An explicit -identity still overrides both, because that is the
// operator saying which file they mean.
//
// version is the running cafe build; the store's identity does not record the
// build a box enrolled as, so the store path carries the running one (see
// storeIdentity).
func resolveBoxdIdentity(flagPath, version string) (boxdIdentity, error) {
	if flagPath != "" {
		return newFileBoxdIdentity(flagPath)
	}
	storeDir := defaultBoxDir()
	if _, err := box.NewStore(storeDir, nil, nil).Identity(); err == nil {
		src := newStoreBoxdIdentity(storeDir, version)
		if envPath := os.Getenv(boxdIdentityEnv); envPath != "" {
			src.notice = fmt.Sprintf("the box store at %s is the source of truth; %s=%s is also set and is ignored",
				storeDir, boxdIdentityEnv, envPath)
		}
		return src, nil
	}
	if envPath := os.Getenv(boxdIdentityEnv); envPath != "" {
		src, err := newFileBoxdIdentity(envPath)
		if err != nil {
			return boxdIdentity{}, err
		}
		src.notice = fmt.Sprintf("%s=%s is deprecated; the box store at %s is the source of truth",
			boxdIdentityEnv, envPath, storeDir)
		return src, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return boxdIdentity{}, fmt.Errorf("find the home directory: %w", err)
	}
	return newFileBoxdIdentity(filepath.Join(home, ".config", "cafe", "box-identity.json"))
}

// newFileBoxdIdentity resolves the identity from an identity document: the
// same reader boxd has always used, unchanged, so a deployed unit that names a
// file keeps working.
func newFileBoxdIdentity(path string) (boxdIdentity, error) {
	store, err := newFileIdentityStore(path)
	if err != nil {
		return boxdIdentity{}, err
	}
	return boxdIdentity{store: store, keys: store, path: store.path, dir: store.dir}, nil
}

// newStoreBoxdIdentity resolves the identity from the box-owned store — the
// same store `cafe box enroll` writes and every `cafe box` verb reads.
func newStoreBoxdIdentity(dir, version string) boxdIdentity {
	store := storeIdentity{store: box.NewStore(dir, nil, nil), version: version}
	return boxdIdentity{store: store, keys: store, dir: dir, fromStore: true}
}

// storeIdentity adapts the box's own store to boxd's two seams. It is the
// whole of "boxd reads the store": Load answers the identity the store holds,
// and Certificate answers the client certificate the store filed, THROUGH THE
// STORE'S OWN ACCESSOR rather than a path re-derived here — which is what "one
// source of truth" means in code.
type storeIdentity struct {
	store *box.Store
	// version is the build running as the daemon. The store's identity does
	// not record the build a box enrolled as, and an observation whose
	// deployed version is empty is a fact consumers cannot act on, so the
	// running build is the honest stand-in — it is also what setup installed
	// at enrollment.
	version string
}

func (s storeIdentity) Load(context.Context) (boxd.Identity, error) {
	id, err := s.store.Identity()
	if err != nil {
		return boxd.Identity{}, err
	}
	return boxd.Identity{BoxID: id.BoxID, ControlPlane: id.ControlPlane, Version: s.version}, nil
}

func (s storeIdentity) Certificate(context.Context) (tls.Certificate, error) {
	cert, err := s.store.ClientCertificate()
	if err != nil {
		return tls.Certificate{}, boxClientCredentialError(err)
	}
	return cert, nil
}

// boxClientCredentialError turns the store's "nothing to dial with" answers
// into the sentinel the daemon counts as permanent, so a store-enrolled box
// with no client certificate refuses after its bound exactly as a file-based
// one does — rather than retry-looping over a fact waiting cannot change.
func boxClientCredentialError(err error) error {
	if errors.Is(err, box.ErrNoClientCredential) || errors.Is(err, box.ErrNotEnrolled) {
		return fmt.Errorf("%w: %w", boxd.ErrNoClientCredential, err)
	}
	return err
}

// ---------------------------------------------------------------------------
// the enrolled identity, on disk
// ---------------------------------------------------------------------------

// identityDocument is what `cafe box enroll` (W2-P6) writes and this daemon
// reads: the box's name, where its control plane is, and where its certificate
// and key live. The paths are relative to the document, so an enrollment is
// one directory a backup can carry.
//
// The certificate and key fields are the Phase C packet's to fill: nothing
// in the fleet issues a client certificate yet, and until that lands a
// document naming none is the honest stub the daemon reads as the permanent
// failure it is (see boxd.ErrNoClientCredential and the package doc's
// decision). The document is the SEAM between the packets. Enrollment owns
// the keyring and the format going forward; this is the shape this daemon
// reads, written down here rather than discovered from a file that does not
// exist yet, so both sides have a contract to be checked against.
type identityDocument struct {
	BoxID        string `json:"box_id"`
	ControlPlane string `json:"box_url"`
	Version      string `json:"version"`
	Certificate  string `json:"certificate"`
	Key          string `json:"key"`
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
	if doc.ControlPlane == "" {
		return identityDocument{}, fmt.Errorf("the enrolled identity at %s has no control plane", s.path)
	}
	return doc, nil
}

// Load implements boxd.IdentityStore.
func (s *fileIdentityStore) Load(context.Context) (boxd.Identity, error) {
	doc, err := s.read()
	if err != nil {
		return boxd.Identity{}, err
	}
	return boxd.Identity{BoxID: doc.BoxID, ControlPlane: doc.ControlPlane, Version: doc.Version}, nil
}

// Certificate implements boxd.KeySource: the client certificate for one
// handshake, read from disk at the dial. The key is never cached here — a
// store that held it between dials would hold the box key for the life of the
// process, which is the memory-lifetime rule this daemon exists to keep.
//
// An identity that names no certificate is the permanent failure the daemon's
// bound exists for: waiting cannot fix it, issuance is deferred to the Phase
// C packet named in boxd's package doc. The error wraps boxd.ErrNoClientCredential
// so the daemon classifies it as permanent and refuses after its bound
// rather than retry-looping — which is what makes the live box's daemon
// honestly refusing instead of failing the same way every 30 seconds.
func (s *fileIdentityStore) Certificate(context.Context) (tls.Certificate, error) {
	doc, err := s.read()
	if err != nil {
		return tls.Certificate{}, err
	}
	if doc.Certificate == "" || doc.Key == "" {
		return tls.Certificate{}, fmt.Errorf("the enrolled identity names no certificate and key: %w", boxd.ErrNoClientCredential)
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
//     the world cannot fulfill yet answers `failed` with that reason — never a
//     silent success, which would be the daemon lying to the control plane about a
//     credential being installed.
//
// The world is built per connection with the identity that connection loaded,
// so Redeem's box binding compares the capability against the enrollment this
// box is running as right now.
type boxWorld struct {
	id boxd.Identity
	// keys loads the box's own client certificate, so Observe can read its
	// expiry — the one credential-adjacent fact that decides whether the box
	// can dial at all. It is the SAME source the dialer uses, box store or
	// identity file, so the certificate an observation reports is never a
	// different one than the certificate the handshake presents.
	keys boxd.KeySource
	path string
}

// newBoxWorld builds a world whose certificate comes from an identity
// document, which is how the file path and the tests that already hold one
// build it.
func newBoxWorld(id boxd.Identity, identityPath, statePath string) boxWorld {
	return newBoxWorldWithKeys(id, fileKeySource(identityPath), statePath)
}

// newBoxWorldWithKeys builds a world over whatever key source the daemon
// resolved — the box store's or an identity file's.
func newBoxWorldWithKeys(id boxd.Identity, keys boxd.KeySource, statePath string) boxWorld {
	return boxWorld{id: id, keys: keys, path: statePath}
}

// fileKeySource is the identity file as a KeySource, for the world's
// certificate read. The dialer's file store is the same reader; this exists
// because the world holds the SEAM rather than a path, so a store-based daemon
// reads the store's certificate and not a file's.
type fileKeySource string

func (p fileKeySource) Certificate(context.Context) (tls.Certificate, error) {
	store, err := newFileIdentityStore(string(p))
	if err != nil {
		return tls.Certificate{}, err
	}
	return store.Certificate(context.Background())
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
// whitelist and the capability for it is valid; the world cannot fulfill it
// yet, and saying so is the difference between a capability that comes back
// `failed` with a reason and a capability that looks like it worked.
func (w boxWorld) Envelope(context.Context, boxd.Envelope) (boxd.EnvelopeResult, error) {
	return boxd.EnvelopeResult{}, fmt.Errorf("opening a sealed envelope and installing a Kubernetes Secret needs the substrate; " +
		"that lands with the substrate packet, until then this route cannot be fulfilled")
}

// Observe assembles the machine-readable report this box makes about itself —
// the read half of the observe stream the observe.report route answers with.
// It gathers the facts v0 can honestly read: the deployed version (the
// enrolled identity), the install phase (the daemon's own state file), the
// client certificate's expiry (the enrollment's own certificate), the root
// filesystem's usage (one statfs, made here because boxd itself may not import
// syscall) and the most recent backup drill (the drill directory this box
// writes). It computes which of box's candidate releases are pending
// against the box's own version range.
//
// ctx is honored at each bounded read: there is no long-running loop here —
// every gather is one read of a local file and one syscall — but a canceled
// context stops the assembly rather than returning a stale report, because a
// report assembled for a connection that has already gone away is a report
// nobody will ack.
func (w boxWorld) Observe(ctx context.Context, params boxd.ObserveParams) (boxd.Observation, error) {
	now := time.Now().UTC()

	state, err := w.load()
	if err != nil {
		// A state file that cannot be read is a real problem, but the box can
		// still report everything else; phase 0 is the honest stand-in and the
		// health summary carries the gap only if it matters.
		state = boxState{}
	}
	if err := ctx.Err(); err != nil {
		return boxd.Observation{}, err
	}

	cert := w.certificate(now)
	drill := w.lastDrill(now)

	obs := boxd.Observation{
		SchemaVersion:   boxd.ObservationSchemaVersion,
		BoxID:           w.id.BoxID,
		DeployedVersion: w.id.Version,
		ControlPlane:    w.id.ControlPlane,
		CollectedAt:     now,
		Certificate:     cert,
		Disk:            diskFact(rootDiskPath()),
		LastDrill:       drill,
		Drills:          w.drills(now),
	}
	if rangeText, ok := observeRange(); ok {
		obs.VersionRange = boxd.VersionRange{Range: rangeText, Status: "ok"}
		obs.PendingReleases = pendingAgainstRange(rangeText, w.id.Version, params.Releases)
	} else {
		obs.VersionRange = boxd.VersionRange{
			Status: "unknown",
			Detail: "the core constraint this box tracks is not set; enrollment writes CAF_BOX_RANGE when it lands",
		}
	}
	obs.Health = healthStatus(state.Phase, cert, drill)
	return obs, nil
}

// certificate reads the box's own client certificate for its expiry. The
// certificate is public — its dates are not a secret — but its NotAfter is
// the one date that decides whether this box can still dial box, which
// is why an observation carries it rather than leaving it to be discovered
// when the handshake starts failing.
func (w boxWorld) certificate(now time.Time) boxd.Certificate {
	if w.keys == nil {
		return boxd.Certificate{Status: "unknown", Detail: "the daemon resolved no client certificate source"}
	}
	pair, err := w.keys.Certificate(context.Background())
	if err != nil {
		return boxd.Certificate{Status: "unknown", Detail: err.Error()}
	}
	leaf := pair.Leaf
	if leaf == nil && len(pair.Certificate) > 0 {
		// tls.LoadX509KeyPair fills Leaf on recent Go, but a leaf that is not
		// parsed here would be a nil dereference, so the parse is done
		// explicitly when it is absent rather than assumed.
		parsed, perr := x509.ParseCertificate(pair.Certificate[0])
		if perr != nil {
			return boxd.Certificate{Status: "unknown", Detail: fmt.Sprintf("parse the box certificate: %v", perr)}
		}
		leaf = parsed
	}
	if leaf == nil {
		return boxd.Certificate{Status: "unknown", Detail: "the box's client certificate could not be read"}
	}
	notAfter := leaf.NotAfter
	status, days := certStatus(notAfter, now, certWarnWindow)
	c := boxd.Certificate{Status: status, ExpiresAt: &notAfter, DaysLeft: &days}
	switch status {
	case "expiring":
		c.Detail = fmt.Sprintf("the client certificate expires in %d day(s)", days)
	case "expired":
		c.Detail = fmt.Sprintf("the client certificate expired %d day(s) ago", -days)
	}
	return c
}

// lastDrill reads the most recent backup drill across every service this box
// keeps a journal for, and flattens it to the observation's DrillFact. A box
// with no drill history reports nil, which healthStatus turns into its own
// named reason rather than a hole nobody notices.
func (w boxWorld) lastDrill(now time.Time) *boxd.DrillFact {
	rec, ok := newestDrill(w.drillRecords())
	if !ok {
		return nil
	}
	return drillFact(rec, now)
}

// drills is the same history at the per-service grain: the newest record for
// every service this box has a journal for, so a box running four services
// reports four restores and names which of them were never proved.
func (w boxWorld) drills(now time.Time) []boxd.ServiceDrill {
	return serviceDrills(w.drillRecords(), now)
}

// drillRecords is the newest record for each service this box has drilled,
// in no guaranteed order — the callers that care sort, for their own reason.
func (w boxWorld) drillRecords() []backup.DrillRecord {
	dir, err := backup.DefaultDrillDir()
	if err != nil {
		return nil
	}
	return latestDrillRecords(dir)
}

// latestDrillRecords finds the newest drill record in each journal in a drill
// directory. It reuses backup.DrillStore.Latest for the per-service newest
// (which sorts and trims the journal on read) and returns them all, so
// "the drills on this box" is a fact about the journals that exist rather than
// about a single winner across them.
func latestDrillRecords(dir string) []backup.DrillRecord {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	store, err := backup.OpenDrillStore(dir)
	if err != nil {
		return nil
	}
	var recs []backup.DrillRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		rec, ok, err := store.Latest(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil || !ok {
			continue
		}
		recs = append(recs, rec)
	}
	return recs
}

// newestDrill picks the single newest record out of a set, which is what the
// box-level LastDrill field is: a summary of the whole box, next to the
// per-service rows that say which service it was.
func newestDrill(recs []backup.DrillRecord) (backup.DrillRecord, bool) {
	var best backup.DrillRecord
	found := false
	for _, rec := range recs {
		if !found || rec.FinishedAt.After(best.FinishedAt) {
			best, found = rec, true
		}
	}
	return best, found
}
