// Package agent is the agent half of the CLI: the principal that registers
// itself, becomes claimed by a human, and holds a token.
//
// The split is the command vocabulary in anywaye/notes/agents-first.md: `cafe agent …`
// is a *principal* (an identity that exists, becomes claimed by a human
// account, and holds a token) and `cafe box …` is a *machine* (it has a keypair,
// it enrolls, it runs the substrate). A box does not log in; it presents a
// capability. An agent does not enroll; it registers. Two subjects, two verbs,
// and `cafe agent register` never reads as "the agent is registering a box".
//
// THREE SEAMS, and each one is there so a test can drive this package without a
// network, a keychain or a subprocess:
//
//	Keyring   where the agent's token lives. Nothing else in this package can
//	          hold it, and nothing here ever returns it.
//	Endpoint  the calls this CLI makes against identity. Injected, so a
//	          register is assertable and a 403 is drivable.
//	Dial      how an Endpoint is opened for one session's base URL and token.
//	          It is a function rather than a field because the token is read
//	          from the ring at the moment of the call and kept for the length
//	          of exactly one method call.
//
// THE ONE INVARIANT, stated once because everything else here is downstream of
// it: **the token is never printed.** Not on stdout, not on stderr, not in an
// error, not in a report, not in a JSON document. It goes to the Keyring and
// the only thing that ever leaves this package about it is its *existence*,
// which every document reports as a boolean. A test asserts the absence by
// rendering every document every command produces and searching it for the
// token's bytes — and its control half is that the agent's username IS printed,
// because a build that redacted everything would pass by printing nothing at
// all.
//
// THE OUTPUT CONTRACT, and it is the one the prior art established: human prose
// and the claim URL go to STDERR, machine-readable JSON goes to STDOUT. An
// agent pipes stdout into a parser; a person reads stderr. Mixing them breaks
// both, which is why the store returns documents and the CLI decides the
// streams — a store that wrote to os.Stderr would be a store no test could
// read.
//
// WHAT THIS PACKAGE DOES NOT DO, and deliberately: it does not claim anything.
// `POST /v1/accounts/{accountID}/agents/claim` is identity's, it needs a
// signed-in human and an account in the path, and it is a browser flow. What
// `cafe agent register` does is produce the URL that flow starts from and say,
// on stderr, that a human has to open it.
package agent
