package box

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// redeemServer answers one redemption with a fixed body, so the decode can be
// tested against box's real response SHAPE rather than against a Go struct
// this package also wrote. The shape is what box's capabilities_controller.rb
// renders for a box.enroll: the installation object, and the certificate fields
// as its siblings.
func redeemServer(body string, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

const enrolledWithCertificate = `{
  "installation": {"id": 1, "box_id": "box_1", "account_id": "acct_1", "status": "active", "current_phase": "enrolled"},
  "certificate": "-----BEGIN CERTIFICATE-----\nMIIleaf\n-----END CERTIFICATE-----\n",
  "certificate_key": "-----BEGIN PRIVATE KEY-----\nMIIkey\n-----END PRIVATE KEY-----\n",
  "certificate_expires_at": "2027-10-13T00:00:00Z",
  "certificate_fingerprint": "aa:bb:cc"
}`

// The redemption carries the certificate out of Redeem in a value whose key is
// unexported, so no document this package emits can hold it.
func TestRedeemCarriesTheIssuedCertificateOutWithoutADocument(t *testing.T) {
	srv := redeemServer(enrolledWithCertificate, http.StatusOK)
	defer srv.Close()

	inst, cert, err := HTTPControlPlane{BaseURL: srv.URL}.Redeem(context.Background(), "cap_x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if inst.BoxID != "box_1" {
		t.Errorf("box_id = %q, want box_1", inst.BoxID)
	}
	if cert == nil {
		t.Fatal("the redemption returned no certificate")
	}
	if cert.Certificate == "" || cert.key == "" {
		t.Fatal("the certificate lost a half")
	}
	if strings.Contains(mustJSON(t, cert), cert.key) {
		t.Error("marshaling the certificate carried the private key")
	}
	// And the printable type stays printable and credential-free.
	if strings.Contains(mustJSON(t, inst), cert.key) {
		t.Error("the Installation carries the private key")
	}
}

// A redemption box answered without a certificate — a recovery, an older box,
// or a non-box.enroll purpose — is not an error: the enrollment happened, and
// the caller gets a nil certificate rather than an invented one.
func TestRedeemWithoutACertificateReturnsNil(t *testing.T) {
	srv := redeemServer(`{"installation": {"id": 1, "box_id": "box_1", "current_phase": "enrolled"}}`, http.StatusOK)
	defer srv.Close()

	inst, cert, err := HTTPControlPlane{BaseURL: srv.URL}.Redeem(context.Background(), "cap_x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if inst.BoxID != "box_1" {
		t.Errorf("box_id = %q, want box_1", inst.BoxID)
	}
	if cert != nil {
		t.Errorf("a response with no certificate produced %+v", cert)
	}
}

// Exactly one half of the keypair is refused by name: half a keypair presents
// nothing, and silently dropping the stray half would hide a broken control
// plane behind an enrollment that looked fine.
func TestRedeemRefusesHalfAKeypair(t *testing.T) {
	for _, body := range []string{
		`{"installation": {"box_id": "box_1"}, "certificate": "-----BEGIN CERTIFICATE-----"}`,
		`{"installation": {"box_id": "box_1"}, "certificate_key": "-----BEGIN PRIVATE KEY-----"}`,
	} {
		srv := redeemServer(body, http.StatusOK)
		_, cert, err := HTTPControlPlane{BaseURL: srv.URL}.Redeem(context.Background(), "cap_x", nil)
		srv.Close()
		if err == nil {
			t.Fatalf("half a keypair was accepted: %s", body)
		}
		if cert != nil {
			t.Error("a refused redemption returned a certificate")
		}
		if !strings.Contains(err.Error(), "one half") {
			t.Errorf("the refusal does not name the cause: %v", err)
		}
	}
}
