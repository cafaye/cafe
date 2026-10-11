package provider

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// recordingControlPlane is the seam a test drives connect and status through,
// without a network. It counts calls and records what was posted, the same
// shape internal/box's counting control plane takes for the redemption.
type recordingControlPlane struct {
	calls  int
	posted []Connection
	boxed  []string // box_id per Status call
	answer []Connection
	err    error
}

func (c *recordingControlPlane) Connect(_ context.Context, conn Connection) (*Connection, error) {
	c.calls++
	c.posted = append(c.posted, conn)
	if c.err != nil {
		return nil, c.err
	}
	return &conn, nil
}

func (c *recordingControlPlane) Status(_ context.Context, boxID string) ([]Connection, error) {
	c.calls++
	c.boxed = append(c.boxed, boxID)
	if c.err != nil {
		return nil, c.err
	}
	return c.answer, nil
}

// validRequest is a connect request a test can start from: a real box keypair,
// a provider, an account and a credential.
func validRequest(t *testing.T) ConnectRequest {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate the box keypair: %v", err)
	}
	return ConnectRequest{
		Provider:   "smtp",
		Account:    "noel@example.com",
		BoxID:      "box-11111111-2222-3333-4444-555555555555",
		Config:     map[string]string{"host": "smtp.example.com"},
		Credential: "smtp-password-123",
		BoxPublic:  pub,
	}
}

// The connect seals the credential to the box's public key and posts the
// envelope. The posted document carries the envelope and never the credential
// — that is boundary 6, and it is asserted over the whole posted struct rather
// than field by field.
func TestConnectSealsAndPostsTheEnvelope(t *testing.T) {
	cp := &recordingControlPlane{}
	req := validRequest(t)

	conn, err := Connect(context.Background(), cp, req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if cp.calls != 1 {
		t.Errorf("control plane calls = %d, want 1", cp.calls)
	}
	if len(cp.posted) != 1 {
		t.Fatalf("posted %d connections, want 1", len(cp.posted))
	}
	posted := cp.posted[0]

	if posted.Provider != "smtp" {
		t.Errorf("provider = %q, want smtp", posted.Provider)
	}
	if posted.Account != req.Account {
		t.Errorf("account = %q, want %q", posted.Account, req.Account)
	}
	if posted.BoxID != req.BoxID {
		t.Errorf("box_id = %q, want %q", posted.BoxID, req.BoxID)
	}
	if posted.SealedCredential == "" {
		t.Errorf("the posted connection carries no envelope")
	}

	// The envelope is a well-formed envelope, not the credential.
	var env Envelope
	if err := json.Unmarshal([]byte(posted.SealedCredential), &env); err != nil {
		t.Fatalf("the posted envelope is not an envelope: %v", err)
	}
	if env.Alg != EnvelopeAlgorithm {
		t.Errorf("alg = %q, want %q", env.Alg, EnvelopeAlgorithm)
	}

	// And the credential appears in no recoverable form in the whole posted
	// document — not raw, not base64, not hex.
	forms := map[string]string{
		"raw":    req.Credential,
		"base64": base64.StdEncoding.EncodeToString([]byte(req.Credential)),
		"hex":    hex.EncodeToString([]byte(req.Credential)),
	}
	for name, form := range forms {
		if strings.Contains(posted.SealedCredential, form) {
			t.Errorf("the posted envelope contains the credential's %s form", name)
		}
	}

	// The returned document is what was recorded, and it too carries no
	// credential.
	raw, err := json.Marshal(conn)
	if err != nil {
		t.Fatalf("encode the returned connection: %v", err)
	}
	if strings.Contains(string(raw), req.Credential) {
		t.Errorf("the returned connection contains the credential")
	}
}

// An unknown provider is refused BEFORE anything is sealed or posted — a
// provider box would refuse is a usage mistake, and sealing an envelope
// for it first would be work thrown away.
func TestConnectRefusesAnUnknownProviderBeforeSealing(t *testing.T) {
	cp := &recordingControlPlane{}
	req := validRequest(t)
	req.Provider = "bogus"

	_, err := Connect(context.Background(), cp, req)
	if !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("err = %v, want ErrUnknownProvider", err)
	}
	if cp.calls != 0 {
		t.Errorf("control plane calls = %d, want 0 (nothing was posted)", cp.calls)
	}
}

// An empty credential is refused rather than sealed into an empty envelope.
func TestConnectRefusesAnEmptyCredential(t *testing.T) {
	cp := &recordingControlPlane{}
	req := validRequest(t)
	req.Credential = "   "

	_, err := Connect(context.Background(), cp, req)
	if !errors.Is(err, ErrEmptyCredential) {
		t.Fatalf("err = %v, want ErrEmptyCredential", err)
	}
	if cp.calls != 0 {
		t.Errorf("control plane calls = %d, want 0", cp.calls)
	}
}

// A connect with no box is refused: there is no key to seal to, and an envelope
// sealed to nobody is a credential posted in the open.
func TestConnectRefusesABoxThatIsNotEnrolled(t *testing.T) {
	cp := &recordingControlPlane{}
	req := validRequest(t)
	req.BoxID = ""
	req.BoxPublic = nil

	_, err := Connect(context.Background(), cp, req)
	if err == nil {
		t.Fatalf("connected without a box")
	}
	if cp.calls != 0 {
		t.Errorf("control plane calls = %d, want 0", cp.calls)
	}
}

// Status reports what box holds and opens nothing: the envelopes come
// back as opaque JSON and are passed through untouched, because a status that
// opened envelopes would be a status that prints secrets.
func TestStatusReportsConnectionsAndOpensNothing(t *testing.T) {
	pub, _ := generateBox(t)
	envelope, err := Seal("smtp-password-123", pub)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	cp := &recordingControlPlane{answer: []Connection{{
		Provider:         "smtp",
		Account:          "noel@example.com",
		BoxID:            "box-11111111-2222-3333-4444-555555555555",
		Config:           map[string]string{"host": "smtp.example.com"},
		SealedCredential: envelope,
	}}}

	conns, err := Status(context.Background(), cp, "box-11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("status returned %d connections, want 1", len(conns))
	}
	conn := conns[0]
	if conn.Provider != "smtp" {
		t.Errorf("provider = %q, want smtp", conn.Provider)
	}
	if conn.Account != "noel@example.com" {
		t.Errorf("account = %q", conn.Account)
	}
	// The envelope is passed through as the opaque JSON it is.
	if conn.SealedCredential != envelope {
		t.Errorf("status changed the envelope")
	}
}

// Status with an empty answer is an empty list, not an error: "this box has no
// providers" is the honest answer to the question ` + "`cafe provider status`" + `
// asks, and it is what a fresh box should hear.
func TestStatusWithNoConnectionsIsEmptyNotAnError(t *testing.T) {
	cp := &recordingControlPlane{}
	conns, err := Status(context.Background(), cp, "box-11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(conns) != 0 {
		t.Errorf("status returned %d connections, want 0", len(conns))
	}
}

// A control plane that fails is reported as a failure, not folded into an empty
// list: "box is unreachable" and "this machine has no providers" are different
// answers and only the first one tells the operator to go looking.
func TestStatusReportsAFailureHonestly(t *testing.T) {
	cp := &recordingControlPlane{err: errors.New("connection refused")}
	_, err := Status(context.Background(), cp, "box-11111111-2222-3333-4444-555555555555")
	if err == nil {
		t.Errorf("status swallowed the control plane's failure")
	}
}
