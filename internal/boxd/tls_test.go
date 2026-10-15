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
	"errors"
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
// the ack are all exercised against the same crypto box would use.
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
	return testPKI{pool: pool, server: issue("box.test"), box: issue("box-test"), other: issue("box-other")}
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

// fakeServer is one control plane: a loopback TLS listener that REQUIRES a client
// certificate, and answers the line protocol with a queued capability.
type fakeServer struct {
	// cap is the capability the next request receives.
	cap Capability
	// answers, when non-nil, replaces the queued capability as the reply to
	// every next: the given lines verbatim, in order, the last one repeating
	// once they run out. It is how a test asks what the client does with a
	// PARTICULAR line — `{}`, a capability with no id, a malformed one —
	// rather than with the one capability shape.
	answers []string
	// acked records the ack line, decoded.
	acked Ack
	// peerCN records the client certificate's common name the server saw,
	// which is how the test asserts the mutual half of mutual TLS.
	peerCN    atomic.Value
	listener  net.Listener
	serveDone chan struct{}
	// handshakes connects; nexts counts next requests, which is how a test
	// proves a re-poll happened on ONE connection rather than many.
	handshakes int32
	nexts      int32
}

func newFakeServer(t *testing.T, pki testPKI, cap Capability) *fakeServer {
	t.Helper()
	s := newFakeServerListener(t, pki)
	s.cap = cap
	go s.serve(t)
	return s
}

// newFakeServerLines is a fakeServer that answers every next with the given
// lines, in order, the last repeating. See the answers field.
func newFakeServerLines(t *testing.T, pki testPKI, answers ...string) *fakeServer {
	t.Helper()
	s := newFakeServerListener(t, pki)
	s.answers = answers
	go s.serve(t)
	return s
}

// newFakeServerListener is the half both constructors share: a loopback TLS
// listener that requires the box's client certificate. The serve goroutine is
// started by the caller, so no field is written after a goroutine can see it.
func newFakeServerListener(t *testing.T, pki testPKI) *fakeServer {
	t.Helper()
	raw, err := new(net.ListenConfig).Listen(context.Background(), "tcp", "127.0.0.1:0")
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
	return &fakeServer{listener: tls.NewListener(raw, cfg), serveDone: make(chan struct{})}
}

// addr is the listener's address, for the identity's control-plane field.
func (s *fakeServer) addr() string { return s.listener.Addr().String() }

// nextLine is the reply to one next request: the next scripted answer, the
// last one repeating, or the queued capability when nothing was scripted.
func (s *fakeServer) nextLine() []byte {
	if len(s.answers) > 0 {
		line := s.answers[0]
		if len(s.answers) > 1 {
			s.answers = s.answers[1:]
		}
		return []byte(line)
	}
	line, _ := json.Marshal(s.cap)
	return line
}

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
			atomic.AddInt32(&s.nexts, 1)
			conn.Write(append(s.nextLine(), '\n'))
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
//   - the handshake COMPLETES, which means box verified the daemon's
//     certificate and the daemon verified box's — the channel is mutual;
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
	id := Identity{BoxID: "box-test", ControlPlane: server.addr(), Version: "test"}

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
	id := Identity{BoxID: "box-test", ControlPlane: "box.invalid:443", Version: "test"}

	// The dial is expected to fail — nothing is listening on that address —
	// but the key is loaded BEFORE the dial attempt, which is the ordering
	// the rule cares about: the key exists exactly as long as the connection
	// it is dialed for.
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
// it is not enough for the daemon to reach box, it must be the enrolled box.
func TestAConnectionWithoutTheBoxKeyIsRefused(t *testing.T) {
	pki := newTestPKI(t)
	server := newFakeServer(t, pki, oneCapability("box.status"))
	defer server.close()

	empty := &instrumentedKeys{cert: tls.Certificate{}}
	dialer := tlsDialer{keys: empty, RootCAs: pki.pool}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := dialer.Dial(ctx, Identity{BoxID: "box-test", ControlPlane: server.addr()})
	if err != nil {
		// The dial itself failed. That is one acceptable refusal, and the
		// stronger case — a connection that dialed and was refused at the
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

// TestTheProtocolRefusesAnUnnamedCapability: a box that sends a
// capability with no id gets an error rather than a silent accept — a
// capability nobody can name cannot be acked, and acking it anyway would make
// box believe something happened.
func TestTheProtocolRefusesAnUnnamedCapability(t *testing.T) {
	pki := newTestPKI(t)
	server := newFakeServer(t, pki, Capability{BoxID: "box-test", Route: "box.status"})
	defer server.close()

	keys := &instrumentedKeys{cert: pki.box}
	ch, err := (tlsDialer{keys: keys, RootCAs: pki.pool}).Dial(context.Background(), Identity{ControlPlane: server.addr()})
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

// TestTheIdleAnswerIsDistinguishedFromTheNoIdAnomaly pins the discriminator
// the live defect turned on, and it is one decision across five lines: the
// empty object is the server's own "nothing now" (ErrIdle, the daemon
// re-polls); anything that names fields but cannot be named is still the
// refusal it always was; a real capability still arrives; and a malformed
// line is still an error rather than a quiet re-poll. The discriminator lives
// on the LINE rather than the decoded value because `{}` and a capability
// with its fields missing decode identically — which is exactly the confusion
// that made a healthy idle channel log a violation every next_wait.
func TestTheIdleAnswerIsDistinguishedFromTheNoIdAnomaly(t *testing.T) {
	pki := newTestPKI(t)
	line := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %T: %v", v, err)
		}
		return string(b)
	}
	tests := []struct {
		name     string
		answer   string
		wantID   string
		wantIdle bool
		wantErr  string
	}{
		{
			name:     "the empty object is box saying nothing now",
			answer:   `{}`,
			wantIdle: true,
		},
		{
			name:    "a capability-shaped line with no id is still the anomaly",
			answer:  line(Capability{BoxID: "box-test", Route: "box.status"}),
			wantErr: "no id",
		},
		{
			name:    "a line that names the id field but leaves it empty is the anomaly too, not idle",
			answer:  `{"id":""}`,
			wantErr: "no id",
		},
		{
			name:   "a real capability still comes through — the discriminator does not swallow it",
			answer: line(oneCapability("box.status")),
			wantID: "cap-box.status",
		},
		{
			name:    "a malformed line is still an error rather than a quiet re-poll",
			answer:  `not json at all`,
			wantErr: "read the response",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newFakeServerLines(t, pki, tt.answer)
			defer server.close()

			keys := &instrumentedKeys{cert: pki.box}
			ch, err := (tlsDialer{keys: keys, RootCAs: pki.pool}).Dial(context.Background(), Identity{ControlPlane: server.addr()})
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer ch.Close()
			got, err := ch.Next(context.Background())
			switch {
			case tt.wantIdle:
				if !errors.Is(err, ErrIdle) {
					t.Fatalf("err = %v, want ErrIdle — the empty object is the server's own answer to a next with nothing queued", err)
				}
			case tt.wantErr != "":
				if err == nil {
					t.Fatalf("Next accepted %+v, want an error containing %q", got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				if errors.Is(err, ErrIdle) {
					t.Errorf("err = %v; idle must not absorb a line that names fields but cannot be named", err)
				}
			default:
				if err != nil {
					t.Fatalf("next: %v", err)
				}
				if got.ID != tt.wantID {
					t.Errorf("capability = %q, want %q", got.ID, tt.wantID)
				}
			}
		})
	}
}

// TestTheDaemonIdlesOverTheRealChannelOnOneConnection is the live defect's
// exact shape end to end — the real dialer, the real client, a real loopback
// TLS listener, and a server that answers `{}` twice before the capability,
// which is what the deployed box does between polls. One handshake and one
// key load for the whole thing, and the capability still redeemed and acked:
// the idle answer is protocol, not a fault. The assertions that would catch a
// regression are the connection and key counts, not the response.
func TestTheDaemonIdlesOverTheRealChannelOnOneConnection(t *testing.T) {
	pki := newTestPKI(t)
	capLine, err := json.Marshal(oneCapability("box.status"))
	if err != nil {
		t.Fatalf("marshal the capability: %v", err)
	}
	server := newFakeServerLines(t, pki, `{}`, `{}`, string(capLine))
	defer server.close()

	keys := &instrumentedKeys{cert: pki.box}
	dialer := tlsDialer{keys: keys, RootCAs: pki.pool}
	d, err := Open(Options{
		Store:    &fakeStore{id: Identity{BoxID: "box-test", ControlPlane: server.addr(), Version: "test"}},
		Keys:     keys,
		Dial:     dialer,
		WorldFor: testWorldFor,
		Once:     true,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Run(ctx); err != nil {
		t.Fatalf("the daemon could not idle and then redeem over mTLS: %v", err)
	}
	if got := atomic.LoadInt32(&server.handshakes); got != 1 {
		t.Fatalf("handshakes = %d, want 1: an idle answer must not cost a connection", got)
	}
	if got := atomic.LoadInt32(&server.nexts); got != 3 {
		t.Errorf("nexts = %d, want 3: two idle answers and then the capability", got)
	}
	if got := atomic.LoadInt32(&keys.calls); got != 1 {
		t.Errorf("key source calls = %d, want 1: the box key is loaded per connection, and idle is not a new one", got)
	}
	if server.acked.Outcome.Status != Accepted {
		t.Fatalf("the server recorded status %q, want %q (message: %s)",
			server.acked.Outcome.Status, Accepted, server.acked.Outcome.Message)
	}
}
