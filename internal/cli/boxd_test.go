package cli

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cafaye/cafe/internal/boxd"
)

// `cafe boxd` at the command layer: the wiring, the refusals, and the file
// contract the enrollment flow (W2-P6) writes against. The daemon itself, the
// whitelist and the mTLS are proven in internal/boxd — these cases are about
// this binary behaving like every other subcommand: flags parsed, a usage
// error is exit 2, a failure names what to do next, and nothing is written to
// stdout that does not belong there.

// writeIdentity writes the identity document the enrollment flow writes, and
// returns its path. The paths inside are absolute, because a test's directory
// is not where a real box keeps its enrollment and the document must honor
// both.
func writeIdentity(t *testing.T, doc identityDocument) string {
	t.Helper()
	return writeIdentityIn(t, t.TempDir(), doc)
}

// writeIdentityIn writes the document into a named directory, which is how a
// test keeps a certificate, a key and the identity that names them together:
// the document's paths are relative to the document.
func writeIdentityIn(t *testing.T, dir string, doc identityDocument) string {
	t.Helper()
	path := filepath.Join(dir, "box-identity.json")
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// oneCapabilityStore is an IdentityStore that records every load, so a test
// can assert the identity is loaded per connection rather than once.
type oneCapabilityStore struct {
	id   boxd.Identity
	load int
}

func (s *oneCapabilityStore) Load(context.Context) (boxd.Identity, error) {
	s.load++
	return s.id, nil
}

// queueControlPlane is one connection with a queue and a record, standing in for
// the control plane. It is the command layer's fake, not the daemon's: the daemon's
// own fakes live in internal/boxd, and this one exists so the command can be
// driven without re-testing what it delegates.
type queueControlPlane struct {
	queue []boxd.Capability
	acks  []boxd.Ack
}

func (c *queueControlPlane) Next(_ context.Context) (boxd.Capability, error) {
	if len(c.queue) == 0 {
		// Blocks like a real connection waiting for the next capability.
		<-make(chan struct{})
		return boxd.Capability{}, context.Canceled
	}
	cap := c.queue[0]
	c.queue = c.queue[1:]
	return cap, nil
}

func (c *queueControlPlane) Ack(_ context.Context, ack boxd.Ack) error {
	c.acks = append(c.acks, ack)
	return nil
}

func (c *queueControlPlane) Close() error { return nil }

// counterDialer counts dials and always returns the one prepared control plane.
type counterDialer struct {
	ch    *queueControlPlane
	dials int
}

func (d *counterDialer) Dial(context.Context, boxd.Identity) (boxd.ControlPlane, error) {
	d.dials++
	return d.ch, nil
}

// staticWorld is the world the command's routes see in a test: enrolled
// identity, one applied phase.
type staticWorld struct {
	id boxd.Identity
}

func (w staticWorld) Identity() boxd.Identity { return w.id }
func (w staticWorld) Stack(context.Context) (boxd.StackStatus, error) {
	return boxd.StackStatus{Phase: 1, Detail: "test"}, nil
}
func (w staticWorld) Apply(_ context.Context, phase int) (boxd.ApplyResult, error) {
	return boxd.ApplyResult{Applied: 1, Phase: phase}, nil
}
func (w staticWorld) Envelope(context.Context, boxd.Envelope) (boxd.EnvelopeResult, error) {
	return boxd.EnvelopeResult{Installed: true}, nil
}
func (w staticWorld) Observe(_ context.Context, _ boxd.ObserveParams) (boxd.Observation, error) {
	return boxd.Observation{SchemaVersion: boxd.ObservationSchemaVersion, BoxID: w.id.BoxID, DeployedVersion: w.id.Version}, nil
}

// fakeDeps assembles the seam for a command driven by the test.
func fakeDeps(t *testing.T, id boxd.Identity, ch *queueControlPlane) (boxdDeps, *counterDialer) {
	t.Helper()
	dialer := &counterDialer{ch: ch}
	return boxdDeps{
		store:     &oneCapabilityStore{id: id},
		keys:      &staticKeys{},
		dial:      dialer,
		worldFor:  func(id boxd.Identity) boxd.World { return staticWorld{id: id} },
		statePath: filepath.Join(t.TempDir(), "boxd-state.json"),
	}, dialer
}

// staticKeys is a KeySource that returns the empty certificate. The command's
// wiring is under test, not the TLS: a real handshake is proven in
// internal/boxd's tls_test.go.
type staticKeys struct{}

func (staticKeys) Certificate(context.Context) (tls.Certificate, error) {
	return tls.Certificate{}, nil
}

// TestBoxdRedeemsOneCapabilityAndExitsZero is the command's happy path: the
// identity file exists, the daemon dials out, redeems the one capability
// against the whitelist, acks it, and `-once` returns 0. The log on stderr
// names the box and the capability, because a daemon whose only output is its
// exit code is a daemon nobody can watch work.
func TestBoxdRedeemsOneCapabilityAndExitsZero(t *testing.T) {
	id := boxd.Identity{BoxID: "box-test", ControlPlane: "box.invalid:443", Version: "test"}
	ch := &queueControlPlane{queue: []boxd.Capability{{
		ID: "cap-1", BoxID: "box-test", Route: "box.status", ExpiresAt: time.Now().Add(time.Minute),
	}}}
	deps, dialer := fakeDeps(t, id, ch)
	identityPath := writeIdentity(t, identityDocument{BoxID: id.BoxID, ControlPlane: id.ControlPlane})

	env := newTestEnv()
	err := newBoxdCommand(deps).Execute(env, []string{"-identity", identityPath, "-once"})

	if err != nil {
		t.Fatalf("err = %v (stderr: %s)", err, written(t, env.Stderr))
	}
	if dialer.dials != 1 {
		t.Errorf("dials = %d, want 1: an outbound-only daemon that dialed twice is two channels", dialer.dials)
	}
	if len(ch.acks) != 1 {
		t.Fatalf("acked %d capabilities, want 1", len(ch.acks))
	}
	if got := ch.acks[0].Outcome; got.Status != boxd.Accepted || got.CapabilityID != "cap-1" {
		t.Errorf("ack = %+v, want cap-1 accepted", got)
	}
	if out := written(t, env.Stdout); out != "" {
		t.Errorf("a daemon's progress is stderr's business; stdout got:\n%s", out)
	}
	if errOut := written(t, env.Stderr); !strings.Contains(errOut, "box-test") {
		t.Errorf("stderr does not name the box it is running as:\n%s", errOut)
	}
}

// TestBoxdRefusesAnUnenrolledBox is the refusal a person actually hits: they
// ran the daemon before enrolling. The message names the one command that
// fixes it, and stdout stays empty — the verdict is the error, not a report.
func TestBoxdRefusesAnUnenrolledBox(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-enrolled.json")
	env := newTestEnv()
	err := newBoxdCommand(boxdDeps{}).Execute(env, []string{"-identity", missing})

	if err == nil {
		t.Fatal("an unenrolled box was allowed to run the daemon")
	}
	if !strings.Contains(err.Error(), "cafe box enroll --token") {
		t.Errorf("err = %q; want it to name `cafe box enroll --token …` as the fix", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("err = %q; want it to name the identity path it looked for", err)
	}
	if out := written(t, env.Stdout); out != "" {
		t.Errorf("stdout got:\n%s", out)
	}
}

// TestBoxdBadInvocationsAreUsageErrors: a stray argument and an undeclared
// flag are the user's mistake, so they are usage errors before anything runs
// — the same contract every other subcommand holds. `-listen` is deliberately
// in the table: the daemon has no listener, so a flag that names one must not
// exist, and a flag that does not exist exits 2.
func TestBoxdBadInvocationsAreUsageErrors(t *testing.T) {
	identityPath := writeIdentity(t, identityDocument{BoxID: "box-test", ControlPlane: "c:443"})
	tests := []struct {
		name string
		args []string
	}{
		{"a stray positional argument", []string{"-identity", identityPath, "extra"}},
		{"an undeclared flag", []string{"-listen", "0.0.0.0:8080"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv()
			err := newBoxdCommand(boxdDeps{}).Execute(env, tt.args)

			if err == nil {
				t.Fatal("the invocation was accepted")
			}
			if !errors.Is(err, errUsage) {
				t.Errorf("err = %v, want a usage error (exit 2)", err)
			}
		})
	}
}

// TestTheIdentityFileIsTheSeam pins the document `cafe box enroll` writes and
// this daemon reads. It is the contract between the two packets: a document
// with a certificate and key beside it loads as the enrolled identity, and
// the key source serves the pair for one handshake. When W2-P6 lands, this is
// the shape it writes; when it writes a different one, this test fails and
// says so, which is the whole point of a file format with a test.
func TestTheIdentityFileIsTheSeam(t *testing.T) {
	dir := t.TempDir()
	testKeypairFiles(t, dir)
	path := writeIdentityIn(t, dir, identityDocument{
		BoxID:        "box-seam",
		ControlPlane: "box.svc.cafaye.com:443",
		Version:      "1.2.3",
		Certificate:  "box.crt",
		Key:          "box.key",
	})

	store, err := newFileIdentityStore(path)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	id, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if id.BoxID != "box-seam" || id.ControlPlane != "box.svc.cafaye.com:443" {
		t.Fatalf("identity = %+v, want the document's own facts", id)
	}
	cert, err := store.Certificate(context.Background())
	if err != nil {
		t.Fatalf("the certificate: %v", err)
	}
	if len(cert.Certificate) != 1 {
		t.Fatal("the keypair did not load as one certificate")
	}
	got, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse the loaded certificate: %v", err)
	}
	if got.Subject.CommonName != "box-seam" {
		t.Errorf("the certificate is for %q, want box-seam: the key must be the box's own", got.Subject.CommonName)
	}
}

// TestTheIdentityFileRefusesAnUnusableDocument: the three ways an identity
// document is wrong — not JSON, no box_id, no control plane — each with its own
// message. A document nobody can read is a daemon that cannot start, and its
// error is what the operator reads first.
func TestTheIdentityFileRefusesAnUnusableDocument(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"not JSON", "box-identity!", "not the JSON"},
		{"no box_id", `{"box_url":"c:443"}`, "no box_id"},
		{"no control plane", `{"box_id":"box-x"}`, "no control plane"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "box-identity.json")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := newFileIdentityStore(path)
			if err != nil {
				t.Fatalf("open the store: %v", err)
			}
			_, err = store.Load(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to say %q", err, tt.want)
			}
		})
	}
}

// TestTheIdentityWithoutACertificateIsAPermanentRefusal: an identity document
// that names no certificate — the recovery path, where a redemption was
// recorded but its certificate was lost with the response, or a redemption box
// issued none for — is the permanent failure the daemon's bound exists for:
// waiting cannot fix it, so the daemon refuses after its bound rather than
// retry-looping. This is the wiring that makes the live box's daemon honestly
// refusing instead of failing the same way every 30 seconds, and it fails on a
// tree where the store's error does not wrap the sentinel the daemon counts.
func TestTheIdentityWithoutACertificateIsAPermanentRefusal(t *testing.T) {
	path := writeIdentity(t, identityDocument{BoxID: "box-test", ControlPlane: "c:443"})
	store, err := newFileIdentityStore(path)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	_, err = store.Certificate(context.Background())
	if err == nil {
		t.Fatal("an identity naming no certificate loaded a client credential anyway")
	}
	if !errors.Is(err, boxd.ErrNoClientCredential) {
		t.Errorf("err = %v, want it to wrap boxd.ErrNoClientCredential so the daemon refuses after its bound", err)
	}
}

// TestTheBoxWorldAppliesIdempotently: the daemon's own state file records the
// applied phases, and re-applying one applies nothing. Agents retry, so the
// second capability for the same phase must be a no-op with a zero count
// rather than a failure or a double application.
func TestTheBoxWorldAppliesIdempotently(t *testing.T) {
	state := filepath.Join(t.TempDir(), "boxd-state.json")
	identity := filepath.Join(t.TempDir(), "box-identity.json")
	world := newBoxWorld(boxd.Identity{BoxID: "box-test"}, identity, state)

	first, err := world.Apply(context.Background(), 3)
	if err != nil || first.Applied != 1 {
		t.Fatalf("first apply = %+v, %v; want 1 applied", first, err)
	}
	again, err := world.Apply(context.Background(), 3)
	if err != nil {
		t.Fatalf("the idempotent apply failed: %v", err)
	}
	if again.Applied != 0 {
		t.Errorf("second apply = %+v, want 0 applied", again)
	}
	status, err := world.Stack(context.Background())
	if err != nil {
		t.Fatalf("stack: %v", err)
	}
	if status.Phase != 3 {
		t.Errorf("phase = %d, want 3: the state must survive the second apply", status.Phase)
	}
}

// TestTheBoxWorldRefusesTheEnvelopeHonestly: opening a sealed provider
// credential needs the substrate, which is not on the box yet. v0's world
// refuses rather than pretending — a silent success would be the daemon telling
// the control plane a credential was installed when it was not.
func TestTheBoxWorldRefusesTheEnvelopeHonestly(t *testing.T) {
	identity := filepath.Join(t.TempDir(), "box-identity.json")
	world := newBoxWorld(boxd.Identity{BoxID: "box-test"}, identity, filepath.Join(t.TempDir(), "boxd-state.json"))
	_, err := world.Envelope(context.Background(), boxd.Envelope{Provider: "cloudflare"})
	if err == nil || !strings.Contains(err.Error(), "substrate") {
		t.Fatalf("err = %v; want it to name the substrate as what is missing", err)
	}
}

// testKeypairFiles writes a self-signed certificate and key for a box, in PEM,
// the format tls.LoadX509KeyPair reads. Generated in-process so the test is
// hermetic and the certificate never expires.
func testKeypairFiles(t *testing.T, dir string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate the key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "box-seam"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("sign the certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal the key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "box.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "box.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestBoxdHelpRendersTheDaemonShape: the help a person reads before running
// the daemon says the three things they need — it is long-running, it opens no
// listener, and it needs an enrollment.
func TestBoxdHelpRendersTheDaemonShape(t *testing.T) {
	var out bytes.Buffer
	printHelp(&out, newBoxdCommand(boxdDeps{}))

	for _, want := range []string{"cafe boxd", "no listener", "cafe box enroll", "-identity", "-once"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help does not mention %q:\n%s", want, out.String())
		}
	}
}
