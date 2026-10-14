package boxd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// The reconnect backoff. It is bounded on both sides on purpose: a daemon
// whose control plane is down for an hour must not hammer it every millisecond,
// and must not take thirty seconds to notice it is back either. The numbers
// are a shape rather than a measurement — this fleet's daemons have not run
// long enough for a real distribution to mean anything, and a claim
// otherwise would be decoration.
const (
	minBackoff = 500 * time.Millisecond
	maxBackoff = 30 * time.Second
)

// Dialer opens the outbound channel to the control plane. It is a seam for the
// same reason World is: the daemon's loop is provable without a network, and
// the TLS half is provable against a real listener (see tls_test.go), so
// neither half needs the other to be real to be tested.
type Dialer interface {
	Dial(ctx context.Context, id Identity) (ControlPlane, error)
}

// ErrNoClientCredential marks a round failure that waiting cannot fix: the
// enrolled identity names no credential the daemon can dial with — today a
// client certificate and key, whose issuance is deferred to Phase C (see the
// package doc for the decision and its trigger). The daemon counts
// consecutive rounds that fail with it and refuses loudly at the bound rather
// than retrying silently forever; every other round failure is transient and
// keeps the backoff-and-retry behavior, because the box's whole job is to be
// reachable by the control plane.
var ErrNoClientCredential = errors.New("the enrolled identity names no client credential")

// Options is what the daemon needs before it will run. Every field is
// required: a daemon started with a nil part of itself would be a daemon that
// fails at the first capability rather than at startup, which is the failure
// mode where the cause and the symptom live in different hours.
type Options struct {
	// Store reads the enrolled identity, once per connection.
	Store IdentityStore
	// Keys loads the box key for one handshake.
	Keys KeySource
	// Dial opens the outbound channel.
	Dial Dialer
	// WorldFor builds the world a connection's routes see. It is a function
	// rather than a value because enrollment is refreshed per connection: a
	// re-enrolled box's routes must answer as the NEW identity, and a world
	// built once at startup would answer as the old one until the daemon
	// restarted.
	WorldFor func(id Identity) World
	// Table is the whitelist. Nil is the default table, so a caller that
	// wants the real whitelist says nothing.
	Table Table
	// Log receives the daemon's progress. Nil discards.
	Log io.Writer
	// Once makes the daemon redeem at most one capability and return. It is
	// how a test drives a whole round trip, and how an operator runs one
	// round under their own eye without a signal.
	Once bool
	// PermanentRounds is how many consecutive rounds may fail with a
	// permanent error — the enrolled identity naming no client credential —
	// before Run stops retrying and returns the refusal. Zero means three.
	// The bound exists because a daemon whose identity cannot dial is either
	// connecting or honestly refusing; a silent retry loop that makes
	// systemd's is-active say "running" over a daemon that can do nothing
	// is the third, worst shape.
	PermanentRounds int
}

// Daemon is one running box daemon. It is a value with no package-level
// state, per the house rule: two cafe boxd processes on one box are two
// Daemons, and neither can reach the other's connection.
type Daemon struct {
	opts    Options
	log     io.Writer
	backoff time.Duration
}

// Open validates the options and returns the daemon.
func Open(opts Options) (*Daemon, error) {
	if opts.Store == nil {
		return nil, errors.New("boxd: no identity store; the enrolled identity is what the daemon authenticates as")
	}
	if opts.Keys == nil {
		return nil, errors.New("boxd: no key source; without the box key there is no mTLS")
	}
	if opts.Dial == nil {
		return nil, errors.New("boxd: no dialer; a daemon with no outbound channel has nothing to do")
	}
	if opts.WorldFor == nil {
		return nil, errors.New("boxd: no world; the routes would have nothing to act on")
	}
	log := opts.Log
	if log == nil {
		log = io.Discard
	}
	if opts.Table == nil {
		opts.Table = Register()
	}
	if opts.PermanentRounds <= 0 {
		opts.PermanentRounds = 3
	}
	return &Daemon{opts: opts, log: log, backoff: minBackoff}, nil
}

// Run serves until ctx is canceled or, with Once set, one capability has been
// redeemed. It returns ctx.Err() on cancellation and an error on a round it
// could not complete, and it reconnects on its own: a daemon that exited
// because box restarted would be a daemon the box's supervisor has to
// babysit, and the reconnect is bounded rather than immediate so a down
// control plane is not a busy loop.
//
// The one failure that does not reconnect forever is the permanent one: the
// enrolled identity naming no client credential. Waiting cannot fix it, so
// after PermanentRounds consecutive rounds that failed with it the daemon
// stops and returns the refusal — a box's daemon is either connecting or
// honestly refusing, and a silent retry loop over a daemon that can do
// nothing is the shape this bound exists to end (see ErrNoClientCredential
// and the package doc). Transient errors leave the count alone: a permanent
// problem does not stop being permanent because the network also flapped,
// and a daemon that is connected is not failing rounds at all — the count
// is about dial failures, and a round that blocks in Next is none.
//
// With Once, a round that completed without the context ending is the
// daemon's whole life: `cafe boxd -once` returns after one capability, so an
// operator can watch one round trip happen rather than signaling a daemon
// they cannot see. A round that FAILED is also the daemon's whole life in
// once mode: it returns the round's error rather than becoming a daemon the
// operator asked not to run.
func (d *Daemon) Run(ctx context.Context) error {
	backoff := d.backoff
	permanent := 0
	for {
		err := d.round(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil && errors.Is(err, ErrNoClientCredential) {
			// A permanent failure is a failure waiting cannot fix. Count
			// the consecutive ones and refuse at the bound; in once mode one
			// round is the whole bound, because one round is the whole life.
			permanent++
			if d.opts.Once || permanent >= d.opts.PermanentRounds {
				return d.refuse(permanent, err)
			}
		}
		if d.opts.Once {
			if err != nil {
				return err
			}
			return nil
		}
		// A round that ends without the context ending is a connection that
		// went away or a round that could not start; either way the box keeps
		// trying, because the box's whole job is to be reachable by the control plane.
		d.logf("boxd: round ended (%v); reconnecting in %s", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// refuse is the daemon's loud exit after a bound of permanent failures. The
// error it returns is the operator's, and it answers the two questions the
// bound exists for: what is wrong, and what will fix it. The last sentence
// is the state the rest of the fleet reads while the fix is pending: the
// observation says the certificate is unknown, and the verification carries
// that fact as a degraded reason with this same sentence behind it.
func (d *Daemon) refuse(permanent int, err error) error {
	d.logf("boxd: refusing to retry after %d rounds: %v", permanent, err)
	return fmt.Errorf("boxd: %d consecutive rounds ended the same permanent way: %w; "+
		"what fixes it: client-credential issuance at enrollment, which lands with the Phase C packet "+
		"named in the package doc — until then this box answers `cafe boxd --report` with the "+
		"certificate unknown and `cafe box verify` reports the box degraded with the reason this refusal names",
		permanent, err)
}

// round is one connection's life: load the identity, dial out, redeem until
// the channel ends. The identity is loaded HERE rather than at Open so a
// re-enrollment takes effect on the next connection, and so a box whose
// enrollment is missing fails with the enrollment's own error naming the
// command that fixes it.
func (d *Daemon) round(ctx context.Context) error {
	id, err := d.opts.Store.Load(ctx)
	if err != nil {
		return fmt.Errorf("boxd: load the enrolled identity: %w", err)
	}
	ch, err := d.opts.Dial.Dial(ctx, id)
	if err != nil {
		return fmt.Errorf("boxd: connect to %s: %w", id.ControlPlane, err)
	}
	// The connection is closed whatever the round does, and the key it was
	// dialed with is dropped with it — the box key is held for the life of a
	// connection and no longer (doc.go, rule three).
	defer func() { _ = ch.Close() }()

	d.logf("boxd: enrolled as %s, outbound to %s", id.BoxID, id.ControlPlane)
	world := d.opts.WorldFor(id)
	for {
		cap, err := ch.Next(ctx)
		if err != nil {
			// A channel that ends between capabilities is a channel that went
			// away; the round is over and Run reconnects.
			return fmt.Errorf("boxd: wait for a capability: %w", err)
		}
		if err := d.redeem(ctx, ch, world, cap); err != nil {
			return err
		}
		if d.opts.Once {
			return nil
		}
	}
}

// redeem is one capability's whole life on the box: the whitelist decision,
// the handler, and the ack — including for capabilities refused, because a
// refusal box never heard about is a capability box will re-send
// until its TTL burns down.
func (d *Daemon) redeem(ctx context.Context, ch ControlPlane, world World, cap Capability) error {
	answer, outcome, err := Redeem(ctx, d.opts.Table, world, cap)
	if err != nil {
		// The route ran and failed. The message is already in the outcome;
		// this line is the operator's, and it names the capability so it can
		// be found in box's audit log.
		d.logf("boxd: capability %s (%s) failed: %v", cap.ID, cap.Route, err)
	} else if outcome.Status != Accepted {
		// The whitelist fired. This is the most interesting line the daemon
		// prints, and it is printed verbatim: "the box was asked to do
		// something it physically cannot" is a sentence worth reading in full.
		d.logf("boxd: capability %s refused: %s: %s", cap.ID, cap.Route, outcome.Status, outcome.Message)
	} else {
		d.logf("boxd: capability %s (%s) accepted", cap.ID, cap.Route)
	}
	if err := ch.Ack(ctx, Ack{Outcome: outcome, Answer: answer}); err != nil {
		// Nothing more can be done for this capability: box will either
		// see the failure from the dropped connection or wait out the TTL.
		return fmt.Errorf("boxd: ack capability %s: %w", cap.ID, err)
	}
	return nil
}

func (d *Daemon) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(d.log, format+"\n", args...)
}
