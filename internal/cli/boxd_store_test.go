package cli

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anywaye/cafe/internal/boxd"
)

// `cafe boxd` reads the box STORE, and this file is the proof of the measured
// defect it fixes: on Contabo 2026-10-15 a re-enroll wrote a new keypair and
// certificate to /var/lib/cafe/box while the daemon kept presenting the old
// certificate from /etc/cafe/box-identity.json, and box answered
// "x509: unknown certificate authority". The store is the source of truth; the
// env-file is a tunnel-era artifact that still works, with a sentence that says
// so. Each test names the source it expects by its CERTIFICATE so a build that
// read the wrong one fails on the certificate and not merely on a path.

// TestABoxdOnAStoreEnrolledBoxUsesTheStoresCertificate is (a): with no
// CAF_BOX_IDENTITY set, a box enrolled in the store resolves its identity and
// its client certificate from the store — the identity the daemon dials as and
// the certificate it presents are the store's own.
func TestABoxdOnAStoreEnrolledBoxUsesTheStoresCertificate(t *testing.T) {
	boxDir := t.TempDir()
	isolateBoxDirs(t, boxDir)
	t.Setenv(boxdIdentityEnv, "")

	writeStoreBox(t, boxDir, "box-store", "box-store")

	src, err := resolveBoxdIdentity("", "9.9.9")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !src.fromStore {
		t.Fatal("a store-enrolled box resolved somewhere other than the store")
	}
	if src.notice != "" {
		t.Errorf("with no env-file there is nothing to say, got notice %q", src.notice)
	}

	id, err := src.store.Load(context.Background())
	if err != nil {
		t.Fatalf("load the identity: %v", err)
	}
	if id.BoxID != "box-store" {
		t.Errorf("identity box_id = %q, want the store's box-store", id.BoxID)
	}
	if id.Version != "9.9.9" {
		t.Errorf("version = %q, want the running build the store path carries", id.Version)
	}

	// The certificate, by its own subject: a build that presented the wrong
	// keypair would fail HERE and not on a path comparison.
	assertCertCN(t, src.keys, "box-store")
}

// TestTheStoreBeatsTheEnvFileAndSaysSoOnce is (b): both sources exist, the
// store wins, and the daemon says so in exactly one log line naming BOTH paths.
// The env-file carries its OWN distinguishable certificate, so a build that
// preferred the env-file would present the wrong certificate and fail on the
// subject rather than on a sentence.
func TestTheStoreBeatsTheEnvFileAndSaysSoOnce(t *testing.T) {
	boxDir := t.TempDir()
	isolateBoxDirs(t, boxDir)

	envDir := t.TempDir()
	envPath := writeEnvIdentity(t, envDir, "box-env", "box-env")
	t.Setenv(boxdIdentityEnv, envPath)

	writeStoreBox(t, boxDir, "box-store", "box-store")

	src, err := resolveBoxdIdentity("", "9.9.9")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !src.fromStore {
		t.Fatal("both sources present and the env-file won; the store must win")
	}
	id, err := src.store.Load(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if id.BoxID != "box-store" {
		t.Errorf("identity box_id = %q, want the store's box-store", id.BoxID)
	}
	assertCertCN(t, src.keys, "box-store")

	if !strings.Contains(src.notice, boxDir) || !strings.Contains(src.notice, envPath) {
		t.Errorf("notice %q does not name both paths (%s and %s)", src.notice, boxDir, envPath)
	}

	// One sentence, and it is in the daemon's OWN log: run a whole one-round
	// daemon with the store resolution live and an injected dialer, then count
	// the line. A build that printed it per connection or per round would fail
	// the count.
	ch := &queueControlPlane{queue: []boxd.Capability{{
		ID: "cap-1", BoxID: "box-store", Route: "box.status", ExpiresAt: time.Now().Add(time.Minute),
	}}}
	p := &recordingDialer{ch: ch}
	deps := boxdDeps{
		dial:      p,
		worldFor:  func(id boxd.Identity) boxd.World { return staticWorld{id: id} },
		statePath: filepath.Join(t.TempDir(), "boxd-state.json"),
	}
	env := newTestEnv()
	if err := newBoxdCommand(deps).Execute(env, []string{"-once"}); err != nil {
		t.Fatalf("run: %v (stderr: %s)", err, written(t, env.Stderr))
	}
	errOut := written(t, env.Stderr)
	if got := strings.Count(errOut, "source of truth"); got != 1 {
		t.Errorf("the store-wins line was printed %d times, want exactly 1:\n%s", got, errOut)
	}
	if !strings.Contains(errOut, boxDir) || !strings.Contains(errOut, envPath) {
		t.Errorf("the daemon's log names neither both paths:\n%s", errOut)
	}

	// The injected seam saw the STORE's identity, not the env-file's.
	if len(p.ids) != 1 || p.ids[0].BoxID != "box-store" {
		t.Errorf("the dialer saw identities %+v, want one for box-store", p.ids)
	}
}

// TestTheEnvFileStillWorksAndNamesItsDeprecation is (c): with only the old
// file, the daemon runs exactly as it did and prints one sentence naming the
// file and the store that supersedes it. Nothing about a deployed unit breaks.
func TestTheEnvFileStillWorksAndNamesItsDeprecation(t *testing.T) {
	boxDir := t.TempDir()
	isolateBoxDirs(t, boxDir)

	envDir := t.TempDir()
	envPath := writeEnvIdentity(t, envDir, "box-env", "box-env")
	t.Setenv(boxdIdentityEnv, envPath)

	src, err := resolveBoxdIdentity("", "9.9.9")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if src.fromStore {
		t.Fatal("no store is enrolled, yet the resolution said the store answered")
	}
	if src.path != envPath {
		t.Errorf("source path = %q, want the env-file %q", src.path, envPath)
	}
	id, err := src.store.Load(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if id.BoxID != "box-env" {
		t.Errorf("identity box_id = %q, want the env-file's box-env", id.BoxID)
	}
	assertCertCN(t, src.keys, "box-env")

	if !strings.Contains(src.notice, envPath) {
		t.Errorf("the deprecation does not name the file it is about: %q", src.notice)
	}
	if !strings.Contains(src.notice, boxDir) {
		t.Errorf("the deprecation does not name the store that supersedes it (%s): %q", boxDir, src.notice)
	}
	if !strings.Contains(strings.ToLower(src.notice), "deprecated") {
		t.Errorf("the env-file line is not a deprecation: %q", src.notice)
	}

	// And the daemon runs through it: a whole one-round run with only the
	// env-file, the injected seam seeing its identity.
	ch := &queueControlPlane{queue: []boxd.Capability{{
		ID: "cap-1", BoxID: "box-env", Route: "box.status", ExpiresAt: time.Now().Add(time.Minute),
	}}}
	p := &recordingDialer{ch: ch}
	deps := boxdDeps{
		dial:      p,
		worldFor:  func(id boxd.Identity) boxd.World { return staticWorld{id: id} },
		statePath: filepath.Join(t.TempDir(), "boxd-state.json"),
	}
	env := newTestEnv()
	if err := newBoxdCommand(deps).Execute(env, []string{"-once"}); err != nil {
		t.Fatalf("run: %v (stderr: %s)", err, written(t, env.Stderr))
	}
	errOut := written(t, env.Stderr)
	if !strings.Contains(errOut, "deprecated") {
		t.Errorf("the daemon did not print the deprecation:\n%s", errOut)
	}
	if len(p.ids) != 1 || p.ids[0].BoxID != "box-env" {
		t.Errorf("the dialer saw identities %+v, want one for box-env", p.ids)
	}
}

// recordingDialer records the identity each dial was handed, so a test can
// assert what the daemon actually resolved without a network.
type recordingDialer struct {
	ch  boxd.ControlPlane
	ids []boxd.Identity
}

func (d *recordingDialer) Dial(_ context.Context, id boxd.Identity) (boxd.ControlPlane, error) {
	d.ids = append(d.ids, id)
	return d.ch, nil
}

// isolateBoxDirs points every resolution at directories the test owns, so the
// real /var/lib/cafe/box and a real ~/.config/cafe can never decide an outcome.
func isolateBoxDirs(t *testing.T, boxDir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(boxDirEnv, boxDir)
}

// writeStoreBox writes a box enrolled in the box-owned store: box.json naming
// box.crt and box.key beside it, and a self-signed certificate whose subject
// is cn so a test can tell one source's certificate from another's.
func writeStoreBox(t *testing.T, dir, boxID, cn string) {
	t.Helper()
	writeBoxKeypair(t, dir, cn)
	doc := map[string]string{
		"box_id":      boxID,
		"box_url":     "box.store.example:443",
		"certificate": "box.crt",
		"key":         "box.key",
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "box.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeEnvIdentity writes the tunnel-era identity document — the shape
// `cafe box setup` writes to /etc/cafe/box-identity.json — with the
// certificate and key beside it, and returns the document's path.
func writeEnvIdentity(t *testing.T, dir, boxID, cn string) string {
	t.Helper()
	writeBoxKeypair(t, dir, cn)
	path := filepath.Join(dir, "box-identity.json")
	doc := identityDocument{
		BoxID:        boxID,
		ControlPlane: "box.env.example:443",
		Version:      "1.2.3",
		Certificate:  "box.crt",
		Key:          "box.key",
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeBoxKeypair writes box.crt and box.key, self-signed with the given
// subject, in the PEM form tls.LoadX509KeyPair reads. Generated in-process so
// the test is hermetic and nothing expires.
func writeBoxKeypair(t *testing.T, dir, cn string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("sign certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "box.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "box.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// assertCertCN fails unless the key source loads a certificate whose subject is
// want. The subject is the discriminator: it proves WHICH source's credential
// was used, not merely that some credential was.
func assertCertCN(t *testing.T, keys boxd.KeySource, want string) {
	t.Helper()
	pair, err := keys.Certificate(context.Background())
	if err != nil {
		t.Fatalf("load the client certificate: %v", err)
	}
	if len(pair.Certificate) == 0 {
		t.Fatal("the key source returned no certificate")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parse the certificate: %v", err)
	}
	if leaf.Subject.CommonName != want {
		t.Errorf("certificate subject = %q, want %q: the wrong source's credential was presented", leaf.Subject.CommonName, want)
	}
}
