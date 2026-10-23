// Package failure is the anywaye failure-mode library: a machine-readable set of
// the documented conditions of OUR OWN stack, and a checker that evaluates them
// against what the box daemon can observe.
//
// WHY THIS EXISTS, and why it is not monitoring. ANYWAYE-OPS.md:731-748 carries
// the seed — ten rows, each a condition a generic monitor cannot see, because
// seeing it requires knowing anywaye. A monitor sees `unhealthy → restart` and
// restarts a healthy courier; the library knows the probe timeout is the bug
// (ANYWAYE-OPS.md:739, :804-806). Grafana and Datadog do monitoring better than
// we will; this package is the half that exists because we documented the stack
// (ANYWAYE-OPS.md:758-763), and it is the Attended tier's actual product
// (ANYWAYE-OPS.md:816).
//
// THE ELEVENTH ENTRY is the box's edge, and it is here because it is the one
// failure mode this tree can produce a fact for rather than only a sentence:
// internal/edge is code in this repository, its health path is the one thing on
// the box that answers for the edge itself, and nothing anywhere probes it yet.
// The rule reads an observation field nothing populates — which the checker
// answers UNKNOWN and names, the same honest answer the other ten get from the
// box daemon. See Library's comment for why the floor stopped moving at eleven.
//
// WHAT A RULE IS. One entry per failure mode, with the four things the doc's
// table carries and one it does not:
//
//	Condition    the facts that identify it, as DATA — see condition.go
//	Sees         what a generic monitor sees (the doc's second column)
//	Known        what we know (the doc's third column)
//	Remedy       the sentence the known fact implies
//	Contract     the core contract the rule leans on, cited and never forked
//
// The doc's table has three columns; the remedy is derived from the third,
// because a failure mode nobody is told what to do about is trivia. Where the
// doc's own sentence already implies the action (run one replica), the remedy
// is that sentence; where it only rules something out (the probe timeout is the
// bug, not the service), the remedy says what is ruled out rather than
// prescribing a change the doc did not decide.
//
// THE CONDITION IS DATA, and that is the extensibility the library's value
// depends on. A row is a predicate and its arguments — no closure, no code, no
// registry to edit — so adding an entry is a table row in rule.go and a fixture
// row in the test. The price is a fixed vocabulary of predicates over a fixed
// set of facts, and the price is worth it twice over: a rule set that is data is
// a rule set a test can walk (library_test.go asserts every entry's predicates
// read facts the Observation actually carries), and it is a rule set that does
// not need a release to grow.
//
// A RULE THAT CANNOT BE EVALUATED SAYS SO. The library's first consumer is the
// box daemon, whose World sees the services that run and the phase the install
// reached — and nothing else yet (internal/boxd/route.go:26-28 deliberately
// carries names and phase, never a service's configuration, because a service's
// configuration is where credentials live). So a checker driven by the box's
// own observation surface answers "unknown" for the ten rules that need more,
// and names the fact each one needs. That is the honest answer and it is also
// the useful one: it is the specification for the facts the substrate packet
// owes this package, and a false "does not hold" would be a green light over a
// blind spot — the same report lock/verify.go:112-114 refuses to produce.
//
// NO SECRET IS EVER A FACT. An Observation carries the NAMES of the settings a
// service has set, never their values, and nothing else about a service's
// configuration. That is the third column's whole shape: `identity`'s MFA
// routes are absent because `MFA_ENCRYPTION_KEY` is UNSET, so the fact the rule
// needs is a name's presence and not a value's content. library_test.go asserts
// the rendered report carries no configured value.
//
// WHAT THIS PACKAGE DOES NOT DO. It does not read the box: Observer is the seam,
// BoxObserver adapts internal/boxd's World into the facts it has, and the rest
// is the substrate packet's. It does not alert, schedule or report to a
// customer — OPS-P13's monthly report consumes it. It does not fork a core
// contract: every probe path, endpoint variable and event name a rule needs is
// cited from core's schemas and repeated nowhere else, the same rule
// internal/contract/schemas/README.md:4 applies to the vendored copies.
package failure
