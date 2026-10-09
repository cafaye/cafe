package boxd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// This file is the proof that "outbound-only mTLS" is a property of the code
// rather than a sentence in a doc comment. A real TLS listener speaks the real
// line protocol to the real dialer over loopback, requiring and verifying the
// box's client certificate — so the handshake, the framing, the redemption and
// the ack are all exercised against the same crypto a conductor would use.
//
// It is the one place in this package that binds a socket, and it is a
// LOOPBACK listener inside the test process: no container, no network
// interface the machine did not already have, and nothing that survives the
// test. The AGENTS.md rule — no test needs a network unless the question is
// about the network — is exactly this case.

// testPKI is a certificate authority, a server certificate for 127.0.0.1 and
// a client certificate for the box. Generated in-process per test run so the
// test is hermetic: no fixture to check in, no certificate to expire.
type testPKI struct {
	pool   *x509.CertPool
	server tls.Certificate
	box    tls.Certificate
	// wrongBox is a certificate for a DIFFERENT box: a handshake that
	// presents it to a server that requires the box's own CA still succeeds
	// cryptographically, which is why the binding check in Redeem exists on
	// top of the transport.
	other tls.Certificate
}

func newTestPKI(t *testing.T) testPKI {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate the CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "caf-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("sign the CA: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse the CA: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	issue := func(name string) tls.Certificate {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generate a key for %s: %v", name, err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()),
			Subject:      pkix.Name{CommonName: name},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
			DNSNames:     []string{name},
			IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatalf("sign the certificate for %s: %v", name, err)
		}
		return tls.Certificate{
			Certificate: [][]byte{der},
			PrivateKey:  key,
			// Leaf is what tls.LoadX509KeyPair would have parsed; filled here
			// so the dialer sees a fully-formed certificate.
			Leaf: mustParseCert(t, der),
		}
	}
	return testPKI{pool: pool, server: issue("conductor.test"), box: issue("box-test"), other: issue("box-other")}
}

func mustParseCert(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse a certificate: %v", err)
	}
	return cert
}

// instrumentedKeys is a KeySource that counts its calls, so a test can prove
// the key is loaded per dial rather than once per daemon.
type instrumentedKeys struct {
	cert  tls.Certificate
	calls int32
}

func (k *instrumentedKeys) Certificate(context.Context) (tls.Certificate, error) {
	atomic.AddInt32(&k.calls, 1)
	return k.cert, nil
}

// fakeServer is one conductor: a loopback TLS listener that REQUIRES a client
// certificate, and answers the line protocol with a queued capability.
type fakeServer struct {
	// cap is the capability the next request receives.
	cap Capability
	// acked records the ack line, decoded.
	acked Ack
	// peerCN records the client certificate's common name the server saw,
	// which is how the test asserts the mutual half of mutual TLS.
	peerCN    atomic.Value
	listener  net.Listener
	serveDone chan struct{}
	// handshakes connects.
	handshakes int32
}

func newFakeServer(t *testing.T, pki testPKI, cap Capability) *fakeServer {
	t.Helper()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{pki.server},
		// This is the "mutual" in mutual TLS: the listener requires a client
		// certificate and verifies it against the CA that signed the box's.
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  pki.pool,
		MinVersion: tls.VersionTLS13,
	}
	s := &fakeServer{cap: cap, listener: tls.NewListener(raw, cfg), serveDone: make(chan struct{})}
	go s.serve(t)
	return s
}

// addr is the listener's address, for the identity's conductor field.
func (s *fakeServer) addr() string { return s.listener.Addr().String() }

// serve accepts ONE connection and speaks the protocol: a capability for the
// next request, an ok for the ack. It exists for the life of one test.
func (s *fakeServer) serve(t *testing.T) {
	defer close(s.serveDone)
	conn, err := s.listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	atomic.AddInt32(&s.handshakes, 1)

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return
	}
	// The handshake completes only when the client's certificate verifies, so
	// this is the gate the mutual-TLS assertion reads.
	if err := tlsConn.HandshakeContext(context.Background()); err != nil {
		return
	}
	if peer := tlsConn.ConnectionState().PeerCertificates; len(peer) > 0 {
		s.peerCN.Store(peer[0].Subject.CommonName)
	}

	dec := json.NewDecoder(conn)
	for {
		var req request
		if err := dec.Decode(&req); err != nil {
			return
		}
		switch req.Op {
		case opNext:
			line, _ := json.Marshal(s.cap)
			conn.Write(append(line, '\n'))
		case opAck:
			s.acked = *req.Ack
			conn.Write([]byte(`{"ok":true}` + "\n"))
			return
		default:
			conn.Write([]byte(`{"ok":false}` + "\n"))
			return
		}
	}
}

func (s *fakeServer) close() { s.listener.Close(); <-s.serveDone }

// TestTheChannelIsMutualTLS is the whole channel in one test: the real dialer
// against a real listener that requires the box's client certificate. Three
// assertions, and each one is a property the AGENTS-FIRST diagram claims:
//
//   - the handshake COMPLETES, which means conductor verified the box's
//     certificate and the box verified conductor's — the channel is mutual;
//   - the server saw the box's own certificate as the peer, so the identity
//     the box authenticated as is the one the enrollment says it is;
//   - the protocol round trip works: a capability arrives, is redeemed by the
//     daemon over the real connection, and the ack comes back accepted.
func TestTheChannelIsMutualTLS(t *testing.T) {
	pki := newTestPKI(t)
	cap := oneCapability("box.status")
	server := newFakeServer(t, pki, cap)
	defer server.close()

	keys := &instrumentedKeys{cert: pki.box}
	dialer := tlsDialer{keys: keys, RootCAs: pki.pool}
	id := Identity{BoxID: "box-test", Conductor: server.addr(), Version: "test"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	d, err := Open(Options{
		Store:    &fakeStore{id: id},
		Keys:     keys,
		Dial:     dialer,
		WorldFor: testWorldFor,
		Once:     true,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := d.Run(ctx); err != nil {
		t.Fatalf("the daemon could not redeem a capability over mTLS: %v", err)
	}

	if got := server.peerCN.Load(); got != "box-test" {
		t.Fatalf("the server saw peer %v; the box must authenticate as box-test", got)
	}
	if got := atomic.LoadInt32(&keys.calls); got < 1 {
		t.Fatalf("the key source was called %d times; the dial never loaded the box key", got)
	}
	if server.acked.Outcome.Status != Accepted {
		t.Fatalf("the server recorded status %q, want %q (message: %s)",
			server.acked.Outcome.Status, Accepted, server.acked.Outcome.Message)
	}
	if server.acked.Answer == nil {
		t.Error("the ack carried no answer; the route's answer must travel back over the channel")
	}
}

// TestTheKeyIsLoadedPerDial is the memory-lifetime rule asserted at the real
// dialer: a second dial loads the box key again rather than reusing the first
// connection's. A dialer that cached the certificate between connections would
// hold the box key for the life of the process, which doc.go rule three
// forbids.
func TestTheKeyIsLoadedPerDial(t *testing.T) {
	pki := newTestPKI(t)
	keys := &instrumentedKeys{cert: pki.box}
	dialer := tlsDialer{keys: keys, RootCAs: pki.pool}
	id := Identity{BoxID: "box-test", Conductor: "conductor.invalid:443", Version: "test"}

	// The dial is expected to fail — nothing is listening on that address —
	// but the key is loaded BEFORE the dial attempt, which is the ordering
	// the rule cares about: the key exists exactly as long as the connection
	// it is dialled for.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = dialer.Dial(ctx, id)
	_, _ = dialer.Dial(ctx, id)

	if got := atomic.LoadInt32(&keys.calls); got != 2 {
		t.Fatalf("key source calls = %d, want 2 — one per dial", got)
	}
}

// TestAConnectionWithoutTheBoxKeyIsRefused: the listener requires a client
// certificate, so a dial whose key source has none fails at the handshake.
// This is the negative proof that the channel is authenticated on BOTH sides:
// it is not enough for the box to reach conductor, it must be the box.
func TestAConnectionWithoutTheBoxKeyIsRefused(t *testing.T) {
	pki := newTestPKI(t)
	server := newFakeServer(t, pki, oneCapability("box.status"))
	defer server.close()

	empty := &instrumentedKeys{cert: tls.Certificate{}}
	dialer := tlsDialer{keys: empty, RootCAs: pki.pool}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := dialer.Dial(ctx, Identity{BoxID: "box-test", Conductor: server.addr()})
	if err != nil {
		// The dial itself failed. That is one acceptable refusal, and the
		// stronger case — a connection that dialled and was refused at the
		// handshake — is below.
		return
	}
	defer ch.Close()
	// The dial connected; the server's ClientAuth refuses the handshake with
	// no certificate, and the first read on the channel sees the failure.
	if _, err := ch.Next(ctx); err == nil {
		t.Fatal("a channel with no client certificate was accepted; the listener must require one")
	}
}

// TestTheProtocolRefusesAnUnnamedCapability: a conductor that sends a
// capability with no id gets an error rather than a silent accept — a
// capability nobody can name cannot be acked, and acking it anyway would make
// conductor believe something happened.
func TestTheProtocolRefusesAnUnnamedCapability(t *testing.T) {
	pki := newTestPKI(t)
	server := newFakeServer(t, pki, Capability{BoxID: "box-test", Route: "box.status"})
	defer server.close()

	keys := &instrumentedKeys{cert: pki.box}
	ch, err := (tlsDialer{keys: keys, RootCAs: pki.pool}).Dial(context.Background(), Identity{Conductor: server.addr()})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ch.Close()
	if _, err := ch.Next(context.Background()); err == nil {
		t.Fatal("an unnamed capability was accepted")
	} else if !strings.Contains(err.Error(), "no id") {
		t.Errorf("err = %v; want it to name the missing id", err)
	}
}
