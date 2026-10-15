package boxd

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
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
	queue []Capability
	// idle is how many `{}` answers box gives before the queue is consulted:
	// a next_wait that expired with nothing queued while a capability was on
	// its way. It counts down as the answers are given.
	idle    int
	acks    []Ack
	nextErr error
	ackErr  error
	// closed and nextCalls are touched by the daemon's own goroutine while
	// the test goroutine reads them, so they are atomics rather than plain
	// fields: the daemon's concurrency is the thing under test, and a
	// fixture that races it reports verdicts nobody can trust.
	closed    atomic.Bool
	nextCalls atomic.Int64
}

// idleOrDone is the fake's "nothing queued" answer: ErrIdle, the same sentinel
// the real client returns when box replies `{}` after its next_wait. The real
// client only answers idle after that wait has elapsed — the poll is a long
// poll — so returning it at once compresses time without changing what the
// daemon must do; ctx still ends the round, and the daemon's idle branch is
// where cancellation is observed.
func (f *fakeControlPlane) idleOrDone(ctx context.Context) (Capability, error) {
	select {
	case <-ctx.Done():
		return Capability{}, ctx.Err()
	default:
		return Capability{}, ErrIdle
	}
}

func (f *fakeControlPlane) Next(ctx context.Context) (Capability, error) {
	f.nextCalls.Add(1)
	if f.nextErr != nil {
		return Capability{}, f.nextErr
	}
	if f.idle > 0 {
		f.idle--
		return f.idleOrDone(ctx)
	}
	if len(f.queue) == 0 {
		// An empty channel does not block forever: box answers `{}` after its
		// next_wait and the daemon polls again on the same connection. That
		// was measured on the deployed channel — see ErrIdle — and a fake
		// that blocked here would test the protocol as it was before that
		// measurement rather than as the server speaks it.
		return f.idleOrDone(ctx)
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
	f.closed.Store(true)
	return nil
}

// fakeDialer hands the daemon one prepared control plane, and counts the dials so
// a test can watch a reconnect happen. The count is an atomic: Dial runs on the
// daemon's goroutine while the test polls the count on its own.
type fakeDialer struct {
	ch     *fakeControlPlane
	err    error
	dials  atomic.Int64
	lastID Identity
}

func (d *fakeDialer) Dial(_ context.Context, id Identity) (ControlPlane, error) {
	d.dials.Add(1)
	d.lastID = id
	if d.err != nil {
		return nil, d.err
	}
	return d.ch, nil
}

// fakeStore is an IdentityStore returning one identity, and a KeySource
// returning a self-signed certificate generated in the test. The load count
// is an atomic: Load runs on the daemon's goroutine while the test polls the
// count on its own.
type fakeStore struct {
	id   Identity
	err  error
	load atomic.Int64
}

func (s *fakeStore) Load(context.Context) (Identity, error) {
	s.load.Add(1)
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
	if !ch.closed.Load() {
		t.Error("the connection was not closed; a round that ends with the channel open leaks it")
	}
	if store.load.Load() != 1 {
		t.Errorf("loaded the identity %d times, want 1 per connection", store.load.Load())
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
	for dialer.dials.Load() < 2 {
		select {
		case <-deadline:
			cancel()
			t.Fatalf("the daemon dialed %d times and never reconnected", dialer.dials.Load())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run returned %v after cancellation, want context.Canceled", err)
	}
	if dialer.dials.Load() < 2 {
		t.Fatalf("dials = %d, want at least one reconnect", dialer.dials.Load())
	}
	if !ch.closed.Load() {
		t.Error("the dropped connection was never closed")
	}
}

// TestAnIdleAnswerKeepsTheDaemonOnOneConnection is the live-defect regression,
// deterministic: box answers `{}` three times — nothing queued, next_wait
// expired — and then the capability it had queued meanwhile. The daemon must
// hold ONE connection for all of it and still redeem the capability, because
// idle is a healthy channel rather than a round end. The assertion that
// matters is the dial count: a response-only count would pass against a
// daemon that reconnected between every idle answer.
func TestAnIdleAnswerKeepsTheDaemonOnOneConnection(t *testing.T) {
	ch := &fakeControlPlane{idle: 3, queue: []Capability{oneCapability("box.status")}}
	dialer := &fakeDialer{ch: ch}
	var log lockedBuffer
	d, err := Open(Options{
		Store:    &fakeStore{id: testIdentity()},
		Keys:     &testKeys{},
		Dial:     dialer,
		WorldFor: testWorldFor,
		Once:     true,
		Log:      &log,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if dialer.dials.Load() != 1 {
		t.Fatalf("dials = %d, want exactly 1: an idle answer is not a round end, and each new dial is a fresh TLS connection and a fresh client-cert load", dialer.dials.Load())
	}
	if got := ch.nextCalls.Load(); got != 4 {
		t.Errorf("polls = %d, want 4: three idle answers and then the capability", got)
	}
	if len(ch.acks) != 1 {
		t.Fatalf("acked %d capabilities, want the one behind the idle answers", len(ch.acks))
	}
	if got := ch.acks[0].Outcome.Status; got != Accepted {
		t.Errorf("status = %q, want %q — idle must not disturb the redemption that follows it", got, Accepted)
	}
	if strings.Contains(log.String(), "round ended") {
		t.Errorf("an idle channel logged a round end:\n%s", log.String())
	}
}

// TestAnIdleChannelHoldsTheConnectionForever is the same property driven the
// other way: nothing is ever queued, so the daemon keeps polling on the one
// connection it has. The run is canceled once the poll count has climbed,
// and the two assertions are that the connection survived (one dial) and that
// nothing was logged as an error — the measured live symptom was a
// protocol-violation line and a reconnect every next_wait.
func TestAnIdleChannelHoldsTheConnectionForever(t *testing.T) {
	ch := &fakeControlPlane{}
	dialer := &fakeDialer{ch: ch}
	var log lockedBuffer
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
	// Wait on the EVENT (the poll count) rather than a sleep: the question is
	// whether the daemon keeps asking, and the count is that fact.
	deadline := time.After(2 * time.Second)
	for ch.nextCalls.Load() < 5 {
		select {
		case <-deadline:
			cancel()
			t.Fatalf("the daemon polled %d times on a channel with nothing queued; an idle channel must keep polling", ch.nextCalls.Load())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run returned %v after cancellation, want context.Canceled", err)
	}
	if dialer.dials.Load() != 1 {
		t.Fatalf("dials = %d, want 1: a healthy idle channel must not drop its connection", dialer.dials.Load())
	}
	if logged := strings.TrimSpace(log.String()); logged != "" {
		// The enrollment line is expected; anything else — a round end, an
		// error — is the live defect logging again.
		for _, line := range strings.Split(logged, "\n") {
			if !strings.Contains(line, "outbound to") {
				t.Errorf("an idle channel logged: %q", line)
			}
		}
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
	for store.load.Load() < 2 {
		select {
		case <-deadline:
			cancel()
			t.Fatalf("the identity was loaded %d times; a reconnect did not reload it", store.load.Load())
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
// line and on what it did NOT do (connect), and the run is canceled once the
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
	if dialer.dials.Load() != 0 {
		t.Errorf("dialed %d times with no identity; an unenrolled box must not connect", dialer.dials.Load())
	}
}

// TestTheDaemonRefusesAfterABoundOfPermanentFailures is the shape this packet
// exists for. The enrolled identity names no client credential, waiting
// cannot fix it, and after PermanentRounds consecutive rounds the daemon
// stops and returns a refusal naming the missing fact and what fixes it —
// rather than retry-looping forever, which is the defect where systemd's
// is-active says "running" over a daemon that can do nothing. The run is
// driven on a deadline: a daemon that has not refused within two seconds of
// its bound is still looping, and the test says so rather than hanging.
func TestTheDaemonRefusesAfterABoundOfPermanentFailures(t *testing.T) {
	dialer := &fakeDialer{err: ErrNoClientCredential}
	var log lockedBuffer
	d, err := Open(Options{
		Store:           &fakeStore{id: testIdentity()},
		Keys:            &testKeys{},
		Dial:            dialer,
		WorldFor:        testWorldFor,
		Log:             &log,
		PermanentRounds: 3,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Run(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNoClientCredential) {
			t.Errorf("err = %v, want it to wrap ErrNoClientCredential", err)
		}
		if !strings.Contains(err.Error(), "what fixes it") {
			t.Errorf("the refusal does not say what fixes it: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("the daemon dialed %d times and never refused; it is retry-looping over a permanent failure", dialer.dials.Load())
	}
	if dialer.dials.Load() != 3 {
		t.Errorf("dials = %d, want 3: the bound is the whole point", dialer.dials.Load())
	}
	if !strings.Contains(log.String(), "refusing to retry") {
		t.Errorf("the refusal was not logged where an operator reads it: %q", log.String())
	}
}

// TestTheDaemonKeepsRetryingATransientFailurePastTheBound is the control for
// the test above: a failure waiting MIGHT fix keeps the backoff-and-retry
// loop running past the bound, because the box's whole job is to be
// reachable by the control plane. The bound is specific to the permanent
// error — a daemon that gave up on a down control plane would be a daemon
// the supervisor has to babysit.
func TestTheDaemonKeepsRetryingATransientFailurePastTheBound(t *testing.T) {
	dialer := &fakeDialer{err: errors.New("connection reset")}
	d, err := Open(Options{
		Store:           &fakeStore{id: testIdentity()},
		Keys:            &testKeys{},
		Dial:            dialer,
		WorldFor:        testWorldFor,
		PermanentRounds: 3,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	// Four dials is past the bound of three, and the wait is on the event
	// (the dialer's count) rather than on a sleep — the backoff between dials
	// is the daemon's own, and the assertion is about the dials happening.
	deadline := time.After(5 * time.Second)
	for dialer.dials.Load() < 4 {
		select {
		case <-deadline:
			cancel()
			t.Fatalf("a transient failure stopped the daemon after %d dials; the bound must not fire on errors waiting can fix", dialer.dials.Load())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run returned %v after cancellation, want context.Canceled", err)
	}
}

// TestTheDaemonOnceModeRefusesAfterOnePermanentRound: one round is the whole
// life in once mode, so one permanent failure is the whole bound — an
// operator watching a single round under their own eye gets the refusal
// rather than a daemon that asked not to run.
func TestTheDaemonOnceModeRefusesAfterOnePermanentRound(t *testing.T) {
	dialer := &fakeDialer{err: ErrNoClientCredential}
	d, err := Open(Options{
		Store:    &fakeStore{id: testIdentity()},
		Keys:     &testKeys{},
		Dial:     dialer,
		WorldFor: testWorldFor,
		Once:     true,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	err = d.Run(context.Background())
	if !errors.Is(err, ErrNoClientCredential) {
		t.Fatalf("err = %v, want the permanent failure", err)
	}
	if dialer.dials.Load() != 1 {
		t.Errorf("dials = %d, want 1: one round is the whole life in once mode", dialer.dials.Load())
	}
}

// TestOpenDefaultsTheRefusalBound: the bound is three consecutive permanent
// rounds when the caller says nothing, so a daemon started with no opinion
// about the bound still refuses rather than loops.
func TestOpenDefaultsTheRefusalBound(t *testing.T) {
	d, err := Open(Options{
		Store:    &fakeStore{id: testIdentity()},
		Keys:     &testKeys{},
		Dial:     &fakeDialer{ch: &fakeControlPlane{}},
		WorldFor: testWorldFor,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if d.opts.PermanentRounds != 3 {
		t.Errorf("PermanentRounds = %d, want the default 3", d.opts.PermanentRounds)
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
