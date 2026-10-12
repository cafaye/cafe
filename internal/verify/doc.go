// Package verify is the verifier the box runs UNDER: the check that runs
// before the box answers, rather than a test somebody runs afterwards.
//
// WHY THE WORD "VERIFIER" AND NOT "TEST". `cafe box verify` (W2-P6) is the
// box's report on itself — is there an identity, is there a key that goes with
// it, is the journal clear. It answers "did anything happen". This package
// answers the two questions the box is asked to answer ON ITS OWN BEHALF,
// every time, before it says anything back:
//
//   - WHICH SECRETS ARE ON THE BOX. The box answers commands an operator
//     typed, and the answers depend on credentials that were pushed to the
//     box at deploy time and never baked into an image. A box that answers
//     without them answers with a stack that cannot send mail, cannot sign a
//     token, cannot reach its database. So the verifier names, per secret,
//     whether the box holds it and where the value is sealed for the control
//     plane — an envelope sealed to THIS box's public key, which box can post
//     and store and cannot read (`cafe provider connect`, invariant 3:
//     secrets are born where they are used).
//   - WHICH HEALTH SIGNALS THE EDGE FRETS ABOUT. The edge routes to a box it
//     believes is whole, and the three signals it has are the three
//     `observe.report` carries per machine: Disk, Health, Certificate. This
//     package is where a 94%-full root filesystem becomes a sentence rather
//     than a float, and where an expiring certificate becomes a named
//     refusal to keep quiet about it.
//
// NO SECRET IS EVER A FACT. Every secret reaches this package as a NAME and a
// boolean. The load-bearing test renders every document `Check` can produce
// and searches both streams for a value it was given — and its control half
// asserts the NAME is present, because a verifier that redacted everything
// would pass by leaking nothing at all. The one place a value appears is
// internal/verify/state.go's Mint, which keeps a FINGERPRINT and drops the
// bytes on the floor: the ledger remembers that a value was minted, by which
// box, and never what it was.
//
// THE ONE PIECE OF STATE. state.go is the minted-secret ledger, and it is the
// answer to a build question: an enclave build — one that reproduces a box's
// image from source with no credentials anywhere near it — must not mint a
// second COURIER_SECRET_BOX_KEY. A second SECRET_KEY_BASE forks every session
// cookie every user holds, and the only thing standing between "we rebuilt"
// and "everybody is logged out" is a record saying the value was already
// minted by the one box that encrypted it. That record is this package's
// state: one file, no credential in it, and a fingerprint a build can compare
// against without ever being told the value.
//
// A DEGRADED ANSWER NAMES THE HUMAN. `health: degraded` on its own is a
// status nobody can act on, and a status nobody can act on is the reason
// people SSH into the box and grep a log. So a degraded Verification carries
// a Human: who reads the next line, where they read it, and what changes the
// answer to `health: ok` — a command, run from anywhere, whose effect the
// next observe.report reports on its own. The rule is a test: no document
// this package renders may say degraded with the human's part elided, and the
// test walks the whole table to prove it.
//
// WHAT THIS PACKAGE IS NOT. It does not read the box: Probe is a value, Check
// is a pure function of it, and the facts arrive from the caller — the same
// seam shape internal/failure's Observation uses, for the same reason. It
// does not alert or schedule; OPS-P13's monthly report consumes the answer.
// It imports internal/boxd for the three fact types it judges (Disk, Health,
// Certificate) and nothing else, so the verifier stays buildable for every
// release target with nothing but the standard library — the daemon's own
// rule, applied to the check the daemon runs.
package verify
