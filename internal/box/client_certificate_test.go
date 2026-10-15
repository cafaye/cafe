package box

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"
)

// ClientCertificate is the store's OWN accessor for the credential enrollment
// filed, and it is what makes "boxd reads the store" one source of truth
// rather than the daemon re-deriving box.crt and box.key by hand. These cases
// pin the two facts that matter: it loads what the store wrote, and it names
// the two absences distinctly so a refusal can say whether the box is
// unenrolled or merely has no credential to dial with.

func TestClientCertificateLoadsWhatTheStoreFiled(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, newTestRing(), nil)
	certPEM, keyPEM := testBoxKeypair(t, "box-store")
	if err := writeFile(s.paths(CertificateFileName), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(s.paths(PrivateKeyFileName), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.writeIdentity(&Identity{
		BoxID: "box-store", ControlPlane: "box:443",
		Certificate: CertificateFileName, Key: PrivateKeyFileName,
	}); err != nil {
		t.Fatal(err)
	}

	pair, err := s.ClientCertificate()
	if err != nil {
		t.Fatalf("ClientCertificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parse the loaded certificate: %v", err)
	}
	if leaf.Subject.CommonName != "box-store" {
		t.Errorf("certificate subject = %q, want box-store: the store's own credential", leaf.Subject.CommonName)
	}
	if pair.PrivateKey == nil {
		t.Error("the loaded pair carries no private key")
	}
}

func TestClientCertificateNamesTheAbsenceDistinctly(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, newTestRing(), nil)

	// No identity at all: the box is not enrolled, and the remedy is enroll.
	if _, err := s.ClientCertificate(); !errors.Is(err, ErrNotEnrolled) {
		t.Errorf("ClientCertificate with no identity = %v, want ErrNotEnrolled", err)
	}

	// An identity that names no certificate: the box exists and its credential
	// is missing, and the remedy is a freshly minted token — a different fact.
	if err := s.writeIdentity(&Identity{BoxID: "box-x", ControlPlane: "box:443"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClientCertificate(); !errors.Is(err, ErrNoClientCredential) {
		t.Errorf("ClientCertificate with no stored credential = %v, want ErrNoClientCredential", err)
	}
}

// testBoxKeypair generates a self-signed certificate and key with the given
// subject, in the PEM form tls.LoadX509KeyPair reads. In-process so the test is
// hermetic and the certificate never expires.
func testBoxKeypair(t *testing.T, cn string) (certPEM, keyPEM []byte) {
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
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}
