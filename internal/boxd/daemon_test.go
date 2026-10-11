package boxd

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// The daemon's loop is driven without a network: a fake control plane hands the
// daemon a queue of capabilities and records every ack, a store hands it an
// identity, and the world is the recording world secret_test.go already uses.
// No test in this file starts a listener — TestTheChannelIsMutualTLS in
// tls_test.go is the one that does, and it does so over loopback.

// fakeControlPlane is a ControlPlane with a queue and a record. It is the seam's
// fake: a dozen lines, per the house rule, and it records every Ack with the
// answer alongside so a test can assert the daemon acked what it redeemed.
type fakeControlPlane struct {
	queue   []Capability
	acks    []Ack
	nextErr error
	ackErr  error
	closed  bool
	// nextCalls counts how many times Next was called, which is how a test
	// sees that a daemon with an empty channel is waiting rather than looping.
	nextCalls int
}

func (f *fakeControlPlane) Next(context.Context) (Capability, error) {
	f.nextCalls++
	if f.nextErr != nil {
		return Capability{}, f.nextErr
	}
	if len(f.queue) == 0 {
		// An empty channel blocks in reality. A fake that returned here would
		// give the daemon a zero Capability and a wrong ack; a fake that
		// blocks until cancelled is the same shape as the connection it stands
		// in for, so it does exactly that.
		<-make(chan struct{})
		return Capability{}, context.Canceled
	}
	cap := f.queue[0]
	f.queue = f.queue[1:]
	return cap, nil
}

func (f *fakeControlPlane) Ack(_ context.Context, ack Ack) error {
	if f.ackErr != nil {
		return f.ackErr
	}
	f.acks = append(f.acks, ack)
	return nil
}

func (f *fakeControlPlane) Close() error {
	f.closed = true
	return nil
}

// fakeDialer hands the daemon one prepared control plane, and counts the dials so
// a test can watch a reconnect happen.
type fakeDialer struct {
	ch     *fakeControlPlane
	err    error
	dials  int
	lastID Identity
}

func (d *fakeDialer) Dial(_ context.Context, id Identity) (ControlPlane, error) {
	d.dials++
	d.lastID = id
	if d.err != nil {
		return nil, d.err
	}
	return d.ch, nil
}

// fakeStore is an IdentityStore returning one identity, and a KeySource
// returning a self-signed certificate generated in the test.
type fakeStore struct {
	id   Identity
	err  error
	load int
}

func (s *fakeStore) Load(context.Context) (Identity, error) {
	s.load++
	if s.err != nil {
		return Identity{}, s.err
	}
	return s.id, nil
}

// testIdentity is the identity every daemon test runs as.
func testIdentity() Identity {
	return Identity{BoxID: "box-test", ControlPlane: "box.invalid:443", Version: "test"}
}

// testWorldFor builds the recording world with a BoxID so Redeem's binding
// check passes for box-test capabilities.
func testWorldFor(id Identity) World {
	return recordingWorld{identity: id, stack: StackStatus{Services: []string{"k3s"}, Phase: 2}}
}

// testKeys is a KeySource returning a certificate from testKeypair. It counts
// calls, which is how a test proves the key is loaded per dial rather than
// once for the daemon's life.
type testKeys struct {
	cert    tls.Certificate
	calls   int
	loadErr error
}

func (k *testKeys) Certificate(context.Context) (tls.Certificate, error) {
	k.calls++
	if k.loadErr != nil {
		return tls.Certificate{}, k.loadErr
	}
	return k.cert, nil
}

// oneCapability is a live capability for box-test naming a route.
func oneCapability(route string) Capability {
	return Capability{ID: "cap-" + route, BoxID: "box-test", Route: route, ExpiresAt: time.Now().Add(time.Minute)}
}

// TestTheDaemonRedeemsOneCapabilityAndStops is the daemon's contract in one
// case: it loads the identity, dials out, redeems a capability against the
// whitelist, acks it, and — because the daemon was opened with Once — returns.
// Every seam is a fake, so the whole lifecycle is provable with no network.
func TestTheDaemonRedeemsOneCapabilityAndStops(t *testing.T) {
	ch := &fakeControlPlane{queue: []Capability{oneCapability("box.status")}}
	store := &fakeStore{id: testIdentity()}
	d, err := Open(Options{
		Store:    store,
		Keys:     &testKeys{},
		Dial:     &fakeDialer{ch: ch},
		WorldFor: testWorldFor,
		Once:     true,
		Log:      io.Discard,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(ch.acks) != 1 {
		t.Fatalf("acked %d capabilities, want 1", len(ch.acks))
	}
	if got := ch.acks[0]; got.Outcome.Status != Accepted || got.Outcome.CapabilityID != "cap-box.status" {
		t.Fatalf("ack = %+v, want an accepted ack for cap-box.status", got.Outcome)
	}
	if ch.acks[0].Answer == nil {
		t.Error("an accepted capability's ack carried no answer; the route declares a return type for a reason")
	}
	if !ch.closed {
		t.Error("the connection was not closed; a round that ends with the channel open leaks it")
	}
	if store.load != 1 {
		t.Errorf("loaded the identity %d times, want 1 per connection", store.load)
	}
}

// TestTheDaemonAcksARefusal: the whitelist fires on a capability naming a
// route not in the table, and the daemon ACKS the refusal — a refusal
// box never hears about is a capability it will re-send until the TTL
// burns down.
func TestTheDaemonAcksARefusal(t *testing.T) {
	ch := &fakeControlPlane{queue: []Capability{{ID: "cap-evil", BoxID: "box-test", Route: "shell.exec"}}}
	d, err := Open(Options{
		Store:    &fakeStore{id: testIdentity()},
		Keys:     &testKeys{},
		Dial:     &fakeDialer{ch: ch},
		WorldFor: testWorldFor,
		Once:     true,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(ch.acks) != 1 {
		t.Fatalf("acked %d capabilities, want the refusal acked once", len(ch.acks))
	}
	if got := ch.acks[0].Outcome.Status; got != RouteRefused {
		t.Fatalf("status = %q, want %q", got, RouteRefused)
	}
	if ch.acks[0].Answer != nil {
		t.Error("a refused capability must not carry an answer")
	}
}

// TestTheDaemonReconnectsAfterTheChannelEnds: the connection drops mid-round,
// and the daemon dials again rather than exiting — the box's whole job is to
// be reachable, and a supervisor should not have to babysit it.
func TestTheDaemonReconnectsAfterTheChannelEnds(t *testing.T) {
	ch := &fakeControlPlane{nextErr: errors.New("connection reset")}
	dialer := &fakeDialer{ch: ch}
	var log strings.Builder
	d, err := Open(Options{
		Store:    &fakeStore{id: testIdentity()},
		Keys:     &testKeys{},
		Dial:     dialer,
		WorldFor: testWorldFor,
		Log:      &log,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	// Cancel after the daemon has had time to reconnect at least once: the
	// dialer counts its dials, so this waits on the EVENT rather than on a
	// sleep — the difference AGENTS.md draws between a deadline and a wait.
	deadline := time.After(2 * time.Second)
	for dialer.dials < 2 {
		select {
		case <-deadline:
			cancel()
			t.Fatalf("the daemon dialed %d times and never reconnected", dialer.dials)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run returned %v after cancellation, want context.Canceled", err)
	}
	if dialer.dials < 2 {
		t.Fatalf("dials = %d, want at least one reconnect", dialer.dials)
	}
	if !ch.closed {
		t.Error("the dropped connection was never closed")
	}
}

// TestOpenRefusesAHalfConfiguredDaemon: every field is required, because a
// daemon missing a part of itself fails at the first capability rather than at
// startup — the failure where the cause and the symptom live in different
// hours. Each row names the field and says what it is for.
func TestOpenRefusesAHalfConfiguredDaemon(t *testing.T) {
	full := Options{
		Store:    &fakeStore{id: testIdentity()},
		Keys:     &testKeys{},
		Dial:     &fakeDialer{ch: &fakeControlPlane{}},
		WorldFor: testWorldFor,
	}
	tests := []struct {
		name string
		zero func(o *Options)
		want string
	}{
		{"store", func(o *Options) { o.Store = nil }, "identity store"},
		{"keys", func(o *Options) { o.Keys = nil }, "key source"},
		{"dial", func(o *Options) { o.Dial = nil }, "dialer"},
		{"world", func(o *Options) { o.WorldFor = nil }, "world"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := full
			tt.zero(&opts)
			_, err := Open(opts)
			if err == nil {
				t.Fatal("a daemon with no " + tt.name + " was opened anyway")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %q, want it to name the %s", err, tt.want)
			}
		})
	}
}

// TestTheDaemonLoadsTheIdentityPerConnection: the enrolled identity is loaded
// once per ROUND rather than once at Open, so a re-enrollment takes effect on
// the next connection. The daemon's own rule; the key's per-connection
// lifetime is the real dialer's, and it is asserted in tls_test.go.
func TestTheDaemonLoadsTheIdentityPerConnection(t *testing.T) {
	ch := &fakeControlPlane{nextErr: errors.New("connection reset")}
	store := &fakeStore{id: testIdentity()}
	d, err := Open(Options{Store: store, Keys: &testKeys{}, Dial: &fakeDialer{ch: ch}, WorldFor: testWorldFor})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	// Wait on the EVENT (the store's second load), not on a sleep: the
	// connection dropping is a fact the store reports, so the assertion is
	// about that fact rather than about the scheduler.
	deadline := time.After(2 * time.Second)
	for store.load < 2 {
		select {
		case <-deadline:
			cancel()
			t.Fatalf("the identity was loaded %d times; a reconnect did not reload it", store.load)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run returned %v after cancellation, want context.Canceled", err)
	}
}

// TestTheDaemonReportsAMissingEnrollment: the store fails with the
// enrollment's own error, and the daemon surfaces it rather than dialing
// unenrolled — a box with no identity has nothing to authenticate as. Run's
// contract is to retry forever, so the assertion is on the daemon's own log
// line and on what it did NOT do (connect), and the run is cancelled once the
// line has appeared.
func TestTheDaemonReportsAMissingEnrollment(t *testing.T) {
	dialer := &fakeDialer{ch: &fakeControlPlane{}}
	var log lockedBuffer
	d, err := Open(Options{
		Store:    &fakeStore{err: errors.New("no enrolled identity: run cafe box enroll")},
		Keys:     &testKeys{},
		Dial:     dialer,
		WorldFor: testWorldFor,
		Log:      &log,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	deadline := time.After(2 * time.Second)
	for !strings.Contains(log.String(), "enrolled identity") {
		select {
		case <-deadline:
			cancel()
			t.Fatalf("the daemon never reported the missing enrollment; log so far:\n%s", log.String())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	<-done
	if dialer.dials != 0 {
		t.Errorf("dialed %d times with no identity; an unenrolled box must not connect", dialer.dials)
	}
}

// lockedBuffer is a writer several goroutines can race on: the daemon logs
// from its own goroutine while the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
