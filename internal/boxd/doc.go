// Package boxd is the daemon that runs on the customer's box.
//
// It is the only thing on the box that can act on a conductor capability, it
// dials outbound over mTLS and never listens, and its route table is the
// security boundary: AGENTS-FIRST invariant 4 says the enforcement lives on
// the box, not in the agent's instructions, and this package is where that
// sentence is code rather than a paragraph.
//
// # What a route is, and why the table is a whitelist
//
// A capability arrives from conductor naming a ROUTE — `stack.apply`,
// `provider.connect` — and parameters for it. The daemon looks the route up in
// a table that exists as a Go value, compiled into the binary, and dispatches
// to that route's handler or refuses the capability. There is no path from a
// capability to a shell: the table is the whole vocabulary of what the box can
// be told to do, and a route that does not exist cannot be added by anything
// that arrives over the wire.
//
// A handler is not handed the world either. Its second parameter is a World,
// an interface with one method per whitelisted capability the box has, and the
// interface is the rule: if a route cannot read a k8s Secret, that is because
// World has no method that returns one, and no amount of code inside the
// handler can conjure a way around an interface it was never given. The same
// interface answers "which services are running" and "install this envelope",
// and it cannot answer "run command X" or "read secret Y" because those
// methods do not exist to call.
//
// # The three rules this package holds
//
// A capability's route must be in the table, alive in time and bound to this
// box. Anything else is refused by name, before a handler runs, and the
// refusal is reported back to conductor rather than swallowed — a refused
// capability the daemon agreed to is the failure mode this exists to prevent.
//
// No route may return a Secret-shaped value. `cafe boxd`'s job on the box is
// to receive sealed envelopes and act on them, never to hand credentials back
// out over the channel — the envelope is opened on the box and installed as a
// Kubernetes Secret, and what goes back to conductor is a status saying that
// happened. This is asserted rather than promised: the test renders every
// route in the table through a recording world and walks each answer's type
// graph, failing if any reachable type is Secret-shaped. It is invariant 4 in
// the same sense a tripwire is: it has a negative control that proves it can
// go red, so a check that has never failed is not on offer here.
//
// The daemon holds a long-lived secret only for as long as a request needs
// it. The box private key is the one credential this process touches at all;
// it is loaded per connection, used for the handshake, and dropped with the
// connection rather than parked in a package-level variable for the life of
// the daemon. A daemon that caches the box key is a daemon whose core dump is
// a keyring.
//
// # What this package deliberately is not
//
// It is not the enrollment flow — `cafe box enroll` (W2-P6) creates the box
// identity and owns the keyring, and this package reads what that flow wrote
// through a seam so the two can be developed against each other rather than
// merged by hope. It is not a container runtime: the World seam is how the
// box's substrate is driven, and the real implementation lands with the
// substrate packet; a test substitutes a recording world, so nothing here
// needs a cluster to prove anything.
package boxd
