package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anywaye/cafe/internal/box"
	"github.com/anywaye/cafe/internal/provider"
)

// The seams a provider test needs, in one place and for one reason: the whole
// point of the packet is that the credential never crosses a boundary it was
// not sealed for, and the only way to prove that at the command level is to
// drive the command against a control plane that captures what was posted and a
// directory the test owns. Nothing here touches the network, the machine's
// config directory or a real box.
type providerTestControlPlane struct {
	calls  int
	posted []provider.Connection
	boxed  []string // box_id per Status call
	answer []provider.Connection
	err    error
}

func (c *providerTestControlPlane) Connect(_ context.Context, conn provider.Connection) (*provider.Connection, error) {
	c.calls++
	c.posted = append(c.posted, conn)
	if c.err != nil {
		return nil, c.err
	}
	return &conn, nil
}

func (c *providerTestControlPlane) Status(_ context.Context, boxID string) ([]provider.Connection, error) {
	c.calls++
	c.boxed = append(c.boxed, boxID)
	if c.err != nil {
		return nil, c.err
	}
	return c.answer, nil
}

// providerTestEnv wires a command tree to a planted identity with a REAL
// keypair, so the round-trip proof — the envelope the command posts opens with
// the private half the test holds — is a proof about the same bytes a cafe run
// would produce. The private key stays in the test and goes nowhere else.
func providerTestEnv(t *testing.T) (deps providerDeps, cp *providerTestControlPlane, priv ed25519.PrivateKey) {
	t.Helper()
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate the box keypair: %v", err)
	}
	dir := t.TempDir()
	plantProviderIdentity(t, dir, pub)
	cp = &providerTestControlPlane{}
	deps = providerDeps{dir: dir, ring: newBoxTestRing(), controlPlane: cp, url: "https://box.test"}
	return deps, cp, private
}

// plantProviderIdentity writes the box.json a provider verb reads the public
// key out of. Only the public half is planted — the private half lives in the
// test, which is the same split the box's own Identity type enforces.
func plantProviderIdentity(t *testing.T, dir string, pub ed25519.PublicKey) {
	t.Helper()
	id := box.Identity{
		BoxID:        "box-11111111-2222-3333-4444-555555555555",
		AccountID:    "acct-aaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		PublicKey:    base64.RawStdEncoding.EncodeToString(pub),
		Status:       "active",
		Phase:        "enrolled",
		EnrolledAt:   "2026-10-09T12:00:00Z",
		ControlPlane: "https://box.test",
	}
	raw, err := json.Marshal(id)
	if err != nil {
		t.Fatalf("encode the planted identity: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, box.IdentityFileName), raw, 0600); err != nil {
		t.Fatalf("plant the identity: %v", err)
	}
}

// runProviderCLI drives ONE cafe provider command tree against the injected
// deps and hands back the exit code plus both streams — exactly what main()
// does with the real process, minus the process. It goes through the parent's
// dispatcher rather than at a verb directly, so the verb lookup and its usage
// errors are what is being tested and not something beside them.
func runProviderCLI(t *testing.T, deps providerDeps, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion, Context: context.Background()}
	err := newProviderCommand(deps).Execute(env, args)
	return codeFor(*env, err), out.String(), errOut.String()
}

// credentialForms is every encoding a leak would most plausibly take. The
// tripwire is asserted over the whole observable surface rather than field by
// field, because a value that reached a field nobody thought to check is
// exactly the case it exists for.
func credentialForms(credential string) map[string]string {
	return map[string]string{
		"raw":    credential,
		"base64": base64.StdEncoding.EncodeToString([]byte(credential)),
		"hex":    hex.EncodeToString([]byte(credential)),
	}
}

// assertNoCredentialForm fails if the credential's bytes reached any of the
// command's three observable surfaces: stdout, stderr, or what was posted to
// the control plane. "Logs" in the brief is stderr here — the command keeps no log of
// its own, and a secret in either stream is a secret in the transcript.
func assertNoCredentialForm(t *testing.T, credential string, stdout, stderr string, posted provider.Connection) {
	t.Helper()
	postedJSON, err := json.Marshal(posted)
	if err != nil {
		t.Fatalf("encode the posted connection: %v", err)
	}
	surfaces := map[string]string{
		"stdout":  stdout,
		"stderr":  stderr,
		"payload": string(postedJSON),
	}
	for name, form := range credentialForms(credential) {
		for surface, got := range surfaces {
			if strings.Contains(got, form) {
				t.Errorf("%s carries the credential's %s form:\n%s", surface, name, got)
			}
		}
	}
}

// THE test the brief is about: the agent hands over an envelope, never the
// value. The credential is sealed to the box's public key inside the command
// and the envelope is the only thing that crosses the wire — so the command's
// whole observable surface (both streams and the posted payload) is searched
// for the credential in every encoding a leak would take.
//
// The control half is that the non-secret facts ARE printed and the posted
// envelope DOES open with the box's private key: a build that redacted
// everything would pass the first half by leaking nothing at all, and a build
// that sealed to the wrong key would produce an envelope nobody can open.
func TestNoProviderCommandEverPrintsTheCredential(t *testing.T) {
	deps, cp, priv := providerTestEnv(t)
	const credential = "super-secret-smtp-password-42"

	code, stdout, stderr := runProviderCLI(t, deps, "connect",
		"--account", "noel@example.com",
		"--credential", credential,
		"--config", "host=smtp.example.com",
		"--config", "port=587",
		"smtp",
	)
	if code != 0 {
		t.Fatalf("connect exited %d (stderr: %s)", code, stderr)
	}
	if cp.calls != 1 {
		t.Fatalf("control plane was called %d times, want 1", cp.calls)
	}
	if len(cp.posted) != 1 {
		t.Fatalf("posted %d connections, want 1", len(cp.posted))
	}
	posted := cp.posted[0]

	assertNoCredentialForm(t, credential, stdout, stderr, posted)

	// The control half, part one: the facts that are not secrets are all
	// there, in the document a machine reads.
	var doc ConnectResult
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("connect stdout is not the connect document: %v\n%s", err, stdout)
	}
	if !doc.Connected {
		t.Errorf("connected = false, want true")
	}
	for _, want := range []string{doc.Provider, doc.Account, doc.BoxID} {
		if want == "" {
			t.Errorf("the document names no provider, account or box: %+v", doc)
		}
	}
	if doc.Provider != "smtp" || doc.Account != "noel@example.com" {
		t.Errorf("document = %s/%s, want smtp/noel@example.com", doc.Provider, doc.Account)
	}
	if doc.Config["host"] != "smtp.example.com" || doc.Config["port"] != "587" {
		t.Errorf("config = %v, want the two --config flags", doc.Config)
	}

	// The control half, part two: the envelope the command posted is openable
	// by the box's private key and by nothing else — which is the proof it was
	// sealed to THIS box, and a real envelope rather than a one-way hash.
	opened, err := provider.Open(posted.SealedCredential, priv)
	if err != nil {
		t.Fatalf("the box cannot open the envelope the command posted: %v", err)
	}
	if opened != credential {
		t.Errorf("the envelope opened to %q, want the credential that was sealed", opened)
	}
}

// The stdin path is the more secure one — a flag is visible in the process
// list, a pipe is not — so it carries the same tripwire: the credential rides
// in on stdin, and neither stream nor the posted payload may carry it out.
func TestProviderConnectThroughStdinNeverPrintsTheCredential(t *testing.T) {
	deps, cp, priv := providerTestEnv(t)
	const credential = "piped-secret-9876"
	withStdin(t, credential+"\n")

	code, stdout, stderr := runProviderCLI(t, deps, "connect",
		"--account", "zone-42.example.com",
		"cloudflare",
	)
	if code != 0 {
		t.Fatalf("connect exited %d (stderr: %s)", code, stderr)
	}
	if len(cp.posted) != 1 {
		t.Fatalf("posted %d connections, want 1", len(cp.posted))
	}
	posted := cp.posted[0]
	assertNoCredentialForm(t, credential, stdout, stderr, posted)

	opened, err := provider.Open(posted.SealedCredential, priv)
	if err != nil {
		t.Fatalf("the box cannot open the envelope: %v", err)
	}
	if opened != credential {
		t.Errorf("the envelope opened to %q, want %q", opened, credential)
	}
	if posted.Provider != "cloudflare" || posted.Account != "zone-42.example.com" {
		t.Errorf("posted %s/%s, want cloudflare/zone-42.example.com", posted.Provider, posted.Account)
	}
}

// withStdin points os.Stdin at a pipe the test controls, which is the shape a
// person pasting a secret into a terminal produces and a terminal does not.
func withStdin(t *testing.T, content string) {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "stdin"))
	if err != nil {
		t.Fatalf("create the stdin pipe: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write the stdin pipe: %v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatalf("rewind the stdin pipe: %v", err)
	}
	old := os.Stdin
	os.Stdin = f
	t.Cleanup(func() {
		os.Stdin = old
		f.Close()
	})
}

// An unknown provider is a usage mistake, and it is refused BEFORE anything is
// sealed or posted — sealing an envelope for a provider box would refuse
// is work thrown away, and the count is what proves it.
func TestProviderConnectRefusesAnUnknownProvider(t *testing.T) {
	deps, cp, _ := providerTestEnv(t)

	code, stdout, stderr := runProviderCLI(t, deps, "connect", "--account", "x@y.z", "--credential", "s", "bogus")
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "bogus") || !strings.Contains(stderr, "smtp") || !strings.Contains(stderr, "cloudflare") {
		t.Errorf("the refusal names neither the provider nor the vocabulary:\n%s", stderr)
	}
	if stdout != "" {
		t.Errorf("a usage error wrote to stdout:\n%s", stdout)
	}
	if cp.calls != 0 {
		t.Errorf("control plane was called %d times, want 0 (nothing was sealed or posted)", cp.calls)
	}
}

// An unenrolled box is not a failure, it is a fact with a remedy: there is no
// key to seal to, and an envelope sealed to nobody is a credential posted in
// the open. The count is the assertion — nothing reached the control plane.
func TestProviderConnectRefusesAnUnenrolledBox(t *testing.T) {
	deps, cp, _ := providerTestEnv(t)
	// Unenroll the box the only honest way: remove the identity it reads.
	if err := os.Remove(filepath.Join(deps.dir, box.IdentityFileName)); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runProviderCLI(t, deps, "connect", "--account", "x@y.z", "--credential", "s", "smtp")
	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "cafe box enroll") {
		t.Errorf("the refusal does not name the remedy:\n%s", stderr)
	}
	if cp.calls != 0 {
		t.Errorf("control plane was called %d times, want 0 (no key, no envelope, nothing posted)", cp.calls)
	}
}

// A connect with no --account is a usage mistake: the account is the fact
// box records alongside the envelope, and a connection with no account
// is a credential nobody can attribute.
func TestProviderConnectWantsAnAccount(t *testing.T) {
	deps, cp, _ := providerTestEnv(t)

	code, _, stderr := runProviderCLI(t, deps, "connect", "--credential", "s", "smtp")
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "--account") {
		t.Errorf("the refusal does not name the flag:\n%s", stderr)
	}
	if cp.calls != 0 {
		t.Errorf("control plane was called %d times, want 0", cp.calls)
	}
}

// The arity guard on both verbs, checked on both sides of the count they
// want. A provider name is one argument; a status report takes none.
func TestProviderVerbsRejectBadArgumentCounts(t *testing.T) {
	deps, _, _ := providerTestEnv(t)

	cases := []struct {
		name string
		args []string
	}{
		{"connect with no provider", []string{"connect"}},
		{"connect with two providers", []string{"connect", "smtp", "cloudflare"}},
		{"status with an argument", []string{"status", "smtp"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			code, _, stderr := runProviderCLI(t, deps, tt.args...)
			if code != exitUsage {
				t.Errorf("exit code = %d, want %d (stderr: %s)", code, exitUsage, stderr)
			}
			if !strings.Contains(stderr, "usage:") {
				t.Errorf("the refusal does not carry the usage line:\n%s", stderr)
			}
		})
	}
}

// A word under `cafe provider` that is not a verb is a usage error, and the
// answer is the list of the ones that are.
func TestProviderUnknownVerbIsAUsageError(t *testing.T) {
	deps, _, _ := providerTestEnv(t)

	code, _, stderr := runProviderCLI(t, deps, "bogus")
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	for _, verb := range []string{"connect", "status"} {
		if !strings.Contains(stderr, verb) {
			t.Errorf("the refusal does not name the %q verb:\n%s", verb, stderr)
		}
	}
}

// A control plane that fails is reported as a failure, not folded into a success:
// "the control plane is unreachable" and "connected" are different answers and only the
// first one tells the operator to go looking. The credential was sealed and
// the envelope was NOT stored, and the command says so.
func TestProviderConnectReportsAFailureHonestly(t *testing.T) {
	deps, cp, _ := providerTestEnv(t)
	cp.err = context.DeadlineExceeded

	code, _, stderr := runProviderCLI(t, deps, "connect", "--account", "x@y.z", "--credential", "s", "smtp")
	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "cafe provider connect") {
		t.Errorf("the report does not name the command:\n%s", stderr)
	}
	if !strings.Contains(stderr, "NOT posted in plaintext") {
		t.Errorf("the report does not say the credential was not posted in plaintext:\n%s", stderr)
	}
	if cp.calls != 1 {
		t.Errorf("control plane was called %d times, want 1 (the envelope was presented, the storage failed)", cp.calls)
	}
}

// Status reports what box holds and opens nothing: the envelopes come
// back as opaque JSON and are passed through untouched, because a status that
// opened envelopes would be a status that prints secrets. The tripwire is the
// envelope bytes themselves — they must not reach either stream.
func TestProviderStatusReportsConnectionsAndOpensNothing(t *testing.T) {
	deps, cp, priv := providerTestEnv(t)
	envelope, err := provider.Seal("the-credential", priv.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatalf("seal the fixture envelope: %v", err)
	}
	cp.answer = []provider.Connection{{
		Provider:         "smtp",
		Account:          "noel@example.com",
		BoxID:            "box-11111111-2222-3333-4444-555555555555",
		Config:           map[string]string{"host": "smtp.example.com"},
		SealedCredential: envelope,
	}}

	code, stdout, stderr := runProviderCLI(t, deps, "status", "--json")
	if code != 0 {
		t.Fatalf("status exited %d (stderr: %s)", code, stderr)
	}
	if cp.calls != 1 {
		t.Fatalf("control plane was called %d times, want 1", cp.calls)
	}
	if len(cp.boxed) != 1 || cp.boxed[0] != "box-11111111-2222-3333-4444-555555555555" {
		t.Errorf("status asked about %v, want this box", cp.boxed)
	}

	var doc StatusResult
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("status stdout is not the status document: %v\n%s", err, stdout)
	}
	if !doc.Connected || len(doc.Connections) != 1 {
		t.Errorf("status = %+v, want one connected provider", doc)
	}
	if doc.Connections[0].Provider != "smtp" || doc.Connections[0].Account != "noel@example.com" {
		t.Errorf("connection = %s/%s, want smtp/noel@example.com", doc.Connections[0].Provider, doc.Connections[0].Account)
	}

	// The envelope is the credential's sealed form: it must not reach either
	// stream, in any encoding.
	assertNoCredentialForm(t, "the-credential", stdout, stderr, provider.Connection{SealedCredential: envelope})
	if strings.Contains(stdout, envelope) || strings.Contains(stderr, envelope) {
		t.Errorf("status echoed the envelope box holds:\n%s%s", stdout, stderr)
	}
}

// A box with no connections is the honest answer to the question status asks,
// not a failure: "connected: false" and an empty list, on a fresh box, is what
// an agent should hear.
func TestProviderStatusReportsNotConnectedHonestly(t *testing.T) {
	deps, _, _ := providerTestEnv(t)

	code, stdout, stderr := runProviderCLI(t, deps, "status", "--json")
	if code != 0 {
		t.Fatalf("status exited %d (stderr: %s)", code, stderr)
	}
	var doc StatusResult
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("status stdout is not the status document: %v\n%s", err, stdout)
	}
	if doc.Connected {
		t.Errorf("connected = true, want false on a box with no connections")
	}
	if len(doc.Connections) != 0 {
		t.Errorf("connections = %v, want none", doc.Connections)
	}
	if !strings.Contains(stderr, "no provider connections") {
		t.Errorf("stderr does not say the box has no connections:\n%s", stderr)
	}
}

// An unenrolled box has no connections, and that is the honest report — not a
// failure. The remedy is named, and the document says connected: false.
func TestProviderStatusOnAnUnenrolledBox(t *testing.T) {
	deps, cp, _ := providerTestEnv(t)
	if err := os.Remove(filepath.Join(deps.dir, box.IdentityFileName)); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runProviderCLI(t, deps, "status", "--json")
	if code != 0 {
		t.Errorf("exit code = %d, want 0 (an unenrolled box has no connections, which is not a failure)", code)
	}
	if !strings.Contains(stderr, "not enrolled") || !strings.Contains(stderr, "cafe box enroll") {
		t.Errorf("the report does not name the state and the remedy:\n%s", stderr)
	}
	var doc StatusResult
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("status stdout is not the status document: %v\n%s", err, stdout)
	}
	if doc.Connected || len(doc.Connections) != 0 {
		t.Errorf("status = %+v, want connected false and no connections", doc)
	}
	if cp.calls != 0 {
		t.Errorf("control plane was called %d times, want 0 (there is nothing to ask)", cp.calls)
	}
}

// The stderr/stdout split is a contract, not a habit: stdout is the document,
// stderr is exactly one sentence, and --json is what selects the machine
// document.
func TestProviderVerbsSplitProseFromTheDocument(t *testing.T) {
	deps, _, _ := providerTestEnv(t)

	code, stdout, stderr := runProviderCLI(t, deps, "status")
	if code != 0 {
		t.Fatalf("status exited %d (stderr: %s)", code, stderr)
	}
	if lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n"); len(lines) != 1 {
		t.Errorf("stderr is not one sentence:\n%s", stderr)
	}
	if json.Valid([]byte(stdout)) {
		t.Errorf("the human document of status is JSON: %s", stdout)
	}
	if !strings.Contains(stdout, "box-11111111-2222-3333-4444-555555555555") {
		t.Errorf("the human document does not name the box:\n%s", stdout)
	}
}

// boxTestRing is a fake keyring retained for provider tests after `cafe box`
// command removal.
type boxTestRing struct{ entries map[string]string }

func newBoxTestRing() *boxTestRing { return &boxTestRing{entries: map[string]string{}} }

func (r *boxTestRing) Get(service, account string) (string, error) {
	if v, ok := r.entries[service+"/"+account]; ok {
		return v, nil
	}
	return "", box.ErrNotFound
}

func (r *boxTestRing) Set(service, account, value string) error {
	r.entries[service+"/"+account] = value
	return nil
}

func (r *boxTestRing) Delete(service, account string) error {
	delete(r.entries, service+"/"+account)
	return nil
}
