package boxd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// The reconnect backoff. It is bounded on both sides on purpose: a daemon
// whose conductor is down for an hour must not hammer it every millisecond,
// and must not take thirty seconds to notice it is back either. The numbers
// are a shape rather than a measurement — this fleet's daemons have not run
// long enough for a real distribution to mean anything, and a claim
// otherwise would be decoration.
const (
	minBackoff = 500 * time.Millisecond
	maxBackoff = 30 * time.Second
)

// Dialer opens the outbound channel to conductor. It is a seam for the same
// reason World is: the daemon's loop is provable without a network, and the
// TLS half is provable against a real listener (see tls_test.go), so neither
// half needs the other to be real to be tested.
type Dialer interface {
	Dial(ctx context.Context, id Identity) (Conductor, error)
}

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
}

// Daemon is one running box daemon. It is a value with no package-level
// state, per the house rule: two caf boxd processes on one box are two
// Daemons, and neither can reach the other's connection.
type Daemon struct {
	opts Options
	log  io.Writer
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
	return &Daemon{opts: opts, log: log}, nil
}

// Run serves until ctx is cancelled or, with Once set, one capability has been
// redeemed. It returns ctx.Err() on cancellation and an error on a round it
// could not complete, and it reconnects on its own: a daemon that exited
// because conductor restarted would be a daemon the box's supervisor has to
// babysit, and the reconnect is bounded rather than immediate so a down
// conductor is not a busy loop.
//
// With Once, a round that completed without the context ending is the
// daemon's whole life: `caf boxd -once` returns after one capability, so an
// operator can watch one round trip happen rather than signalling a daemon
// they cannot see.
func (d *Daemon) Run(ctx context.Context) error {
	backoff := minBackoff
	for {
		err := d.round(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.opts.Once && err == nil {
			return nil
		}
		// A round that ends without the context ending is a connection that
		// went away or a round that could not start; either way the box keeps
		// trying, because the box's whole job is to be reachable by conductor.
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
		return fmt.Errorf("boxd: connect to %s: %w", id.Conductor, err)
	}
	// The connection is closed whatever the round does, and the key it was
	// dialled with is dropped with it — the box key is held for the life of a
	// connection and no longer (doc.go, rule three).
	defer ch.Close()

	d.logf("boxd: enrolled as %s, outbound to %s", id.BoxID, id.Conductor)
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
// refusal conductor never heard about is a capability conductor will re-send
// until its TTL burns down.
func (d *Daemon) redeem(ctx context.Context, ch Conductor, world World, cap Capability) error {
	answer, outcome, err := Redeem(ctx, d.opts.Table, world, cap)
	if err != nil {
		// The route ran and failed. The message is already in the outcome;
		// this line is the operator's, and it names the capability so it can
		// be found in conductor's audit log.
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
		// Nothing more can be done for this capability: conductor will either
		// see the failure from the dropped connection or wait out the TTL.
		return fmt.Errorf("boxd: ack capability %s: %w", cap.ID, err)
	}
	return nil
}

func (d *Daemon) logf(format string, args ...any) {
	fmt.Fprintf(d.log, format+"\n", args...)
}
