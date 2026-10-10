// Package publish is `caf publish`: the client half of pantry's write surface —
// the three operations that take a project from a developer's checkout into the
// public catalog.
//
// The design is the approval-card pattern (PLAN-pantry-registry.md Phase 5):
// the tool PROPOSES — a registration built from the local cafaye.yml, a version
// tag, the dependency edges — and a human confirms before any write runs.
// OPS-D7 is the same shape one layer up: the customer's own agent reads and
// proposes freely; only a human approval mints an act capability. The package
// therefore holds the proposal as a first-class value (Proposal) and keeps the
// writes behind one Publisher seam, so a test can drive the whole decision
// table — dry run, refusal, 409 tolerance — without a network.
//
// The three writes run in order (register → versions → requirements) and each
// is idempotent-tolerant: pantry answers a re-run with 409 conflict, and a 409
// that means "already done" is a report line, not a crash. The distinction
// between an idempotent 409 and a real one (the ordering rule — an edge over a
// target with no published semver release) is made on the server's own detail
// text, and an unrecognized 409 detail is a FAILURE: the safe direction,
// because a false "already declared" is worse than a false "failed".
package publish
