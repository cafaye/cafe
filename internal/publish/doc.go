// Package publish is `caf publish`: the client half of pantry's write surface —
// the three operations that take a project from a developer's checkout into the
// public catalog.
//
// The design was the approval-card pattern (PLAN-pantry-registry.md Phase 5):
// the tool PROPOSES — a registration built from the local cafaye.yml, a version
// tag, the dependency edges — and somebody confirmed before any write ran. The
// user's ruling of 2026-10-11 ("for cafaye i think we should make everything
// non interactive by default as it shall mostly be used by agents") flipped
// that second half: the agent path is THE path, so the default run publishes
// without asking. `--dry-run` remains the proposal mode — print the proposal,
// write nothing — and reviewability is after the fact, in pantry's audit
// trail (the publisher.minted companion packet). OPS-D7 still holds: the
// boundary is the mint, not the reach. What changed is that the mint's
// confirmation is the ruling, not a prompt.
// The package therefore holds the proposal as a first-class value (Proposal)
// and keeps the writes behind one Publisher seam, so a test can drive the
// whole decision table — dry run, a clean publish, 409 tolerance — without a
// network.
//
// The three writes run in order (register → versions → requirements) and each
// is idempotent-tolerant: pantry answers a re-run with 409 conflict, and a 409
// that means "already done" is a report line, not a crash. The distinction
// between an idempotent 409 and a real one (the ordering rule — an edge over a
// target with no published semver release) is made on the server's own detail
// text, and an unrecognized 409 detail is a FAILURE: the safe direction,
// because a false "already declared" is worse than a false "failed".
package publish
