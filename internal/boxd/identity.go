package boxd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"time"
)

// Identity is the enrolled box identity — what `cafe box enroll` (W2-P6) writes
// and this daemon reads. It is facts and addresses, never key material: the
// box's private key lives behind the KeySource seam and is loaded per
// connection, because a long-lived secret held in a daemon's memory longer
// than a request needs is exactly the thing invariant 1 forbids the agent and
// this package extends to the box.
type Identity struct {
	// BoxID is the enrollment's own name for this box. A capability is bound
	// to it, and Redeem refuses one that is not.
	BoxID string
	// ControlPlane is the address the daemon dials out to. It is in the identity
	// rather than in a flag because enrollment is what establishes which
	// control plane this box answers to — a flag that overrode it would be a
	// second authority about the box's owner. It is stored in the URL form a
	// human typed (`http://host:port`, the `--box`/CAF_BOX_URL spelling) and
	// the dialer normalises it to host:port at the dial (see dialTarget in
	// client.go), so identities written before the dialer learned the URL
	// form connect without being rewritten.
	ControlPlane string
	// Version is the cafe build this box enrolled as, which is what
	// box.status reports rather than the version currently running; when the
	// two differ, `cafe box update` is the honest answer for why.
	Version string
}

// KeySource loads the box's client certificate for one connection. It is a
// seam so the private key is the enrollment flow's business — the keyring it
// chose, the format it chose — and so a test can drive the daemon with a
// self-signed certificate and no keyring anywhere in sight.
type KeySource interface {
	// Certificate returns the box's client certificate and key, ready for one
	// TLS handshake. The returned certificate is used and dropped; an
	// implementation that caches it across calls is caching the box key, which
	// the package doc's third rule forbids.
	Certificate(ctx context.Context) (tls.Certificate, error)
}

// IdentityStore reads the enrolled identity off the box. Like KeySource it is
// a seam for the same two reasons: the enrollment flow owns where the identity
// lives, and a test substitutes a directory it can see.
type IdentityStore interface {
	Load(ctx context.Context) (Identity, error)
}

// Capability is one instruction from box, and the only thing that
// arrives over the channel. It names a route, carries that route's parameters
// as bytes, and expires: box mints these single-use, minutes TTL, bound
// to this box, which is invariant 2 — possession of a capability away from its
// target buys nothing, because the target is the box and the TTL is minutes.
type Capability struct {
	// ID is the capability's own name, single-use. The outcome carries it so
	// box can retire it.
	ID string
	// BoxID is the box this capability is bound to. Redeem refuses any other.
	BoxID string
	// Route is the whitelisted route it names. Redeem refuses anything else.
	Route string
	// Params are the route's parameters, encoded. They are decoded into the
	// ROUTE's own input type, so a capability cannot smuggle a shape the route
	// never declared.
	Params json.RawMessage
	// ExpiresAt is the TTL's end. A capability alive past it is refused.
	ExpiresAt time.Time
}

// alive reports whether the capability's TTL has not passed at the given
// moment. The moment is a parameter rather than read from the clock so the
// test drives expiry at both sides of the boundary without a sleep.
func (c Capability) alive(now time.Time) bool {
	return now.Before(c.ExpiresAt)
}

// Status is what the daemon tells box became of a capability.
type Status string

const (
	// Accepted means the route ran and the box reports success.
	Accepted Status = "accepted"
	// Failed means the route ran and failed — the box's own error is in the
	// outcome's message, and box may re-mint.
	Failed Status = "failed"
	// RouteRefused means the capability named a route not in this box's table.
	// This is the whitelist firing, and it is worth its own status because
	// "the box was asked to do something it physically cannot" is the most
	// interesting line in the audit log.
	RouteRefused Status = "route_refused"
	// WrongBox means the capability was bound to a different box.
	WrongBox Status = "wrong_box"
	// Expired means the TTL passed before the daemon saw it.
	Expired Status = "expired"
)

// Outcome is what goes back to box for every capability: which one, what
// the daemon decided, and why in a sentence. It is the classification and
// never the data — the answer travels beside it inside an Ack, and it is
// guarded by the same walk every route's output goes through.
type Outcome struct {
	CapabilityID string `json:"capability_id"`
	Route        string `json:"route"`
	Status       Status `json:"status"`
	// Message is the human sentence: the refusal's reason, or the route's own
	// error. It is never the parameters and never the answer.
	Message string `json:"message,omitempty"`
}

// Ack is one capability's full report to box: the decision about it,
// and the answer the route produced.
//
// The answer is carried because a capability is a request — `stack.status`
// exists to be answered, and a daemon that dropped the answer would make the
// route pointless. It is guarded rather than trusted: every route's answer
// type is walked by the load-bearing test before this package ships, so what
// travels in an Ack is status-shaped by construction — names, phases,
// booleans — and a Secret-shaped answer is a red suite rather than a field
// somebody forgot to redact.
type Ack struct {
	Outcome Outcome
	Answer  any
}

// ---------------------------------------------------------------------------
// what the world is made of
// ---------------------------------------------------------------------------

// StackStatus is what the substrate is running, as World.Stack reports it.
type StackStatus struct {
	// Services are the names of the running services — names, never their
	// environment, because a service's environment is its configuration and
	// its configuration is where credentials live.
	Services []string
	// Phase is the install phase reached: 0 means not installed, and the
	// recorded plan's length is "done".
	Phase int
	// Detail is one sentence about the phase, for the answer a human reads.
	Detail string
}

// ApplyResult is what one applied phase produced.
type ApplyResult struct {
	Applied int    `json:"applied"`
	Phase   int    `json:"phase"`
	Detail  string `json:"detail,omitempty"`
}

// Envelope is a provider credential sealed to this box's public key. The
// ciphertext is opaque to everything except the box's key: this daemon
// receives it, hands it to the world, and never records what was inside.
type Envelope struct {
	Provider   string          `json:"provider"`
	Ciphertext json.RawMessage `json:"ciphertext"`
}

// EnvelopeResult is the status of installing an envelope. Installed and a
// sentence — never the value, which stays where it was opened.
type EnvelopeResult struct {
	Installed bool   `json:"installed"`
	Detail    string `json:"detail,omitempty"`
}

// ControlPlane is the outbound channel. Two methods, both outbound: the daemon
// asks for the next capability and reports what it decided. There is no method
// on this interface that would let the box be driven inbound — no listener in
// the type system any more than in the process.
type ControlPlane interface {
	// Next blocks until box sends a capability for this box or ctx ends,
	// with one quiet exception: box may answer with the empty object `{}` —
	// "nothing queued" — in which case Next returns ErrIdle and the daemon
	// polls again on the same connection. The daemon holds the connection
	// open; a box that polls is a box whose failure mode is a slow capability
	// rather than an open port.
	Next(ctx context.Context) (Capability, error)
	// Ack reports one capability's outcome and answer. The daemon acks every
	// capability, including the ones it refused — a refusal box did not
	// hear about is a capability box will re-send until its TTL burns
	// down.
	Ack(ctx context.Context, ack Ack) error
	// Close ends the connection.
	Close() error
}
