// Package box is the box half of the CLI: the machine that enrolls itself
// against conductor and then runs the install, phase by phase, to a box that
// serves an app.
//
// The split is the command vocabulary in moon/AGENTS-FIRST.md: `caf agent …`
// is a *principal* (an identity that becomes claimed), `caf box …` is a
// *machine* (it has a keypair, it enrolls, it runs the substrate). A box does
// not log in; it presents a capability that was minted for it and for nobody
// else, and that is worth exactly one enrollment for a few minutes.
//
// FOUR SEAMS, and each one is there so a test can drive this package without a
// network, a keychain or a subprocess:
//
//	Keyring    where the box's private key lives. Nothing else in this
//	           package can hold it, and nothing here ever returns it.
//	Conductor  the one HTTP call caf makes in this package: redeeming a
//	           capability. Injected, so the redemption can be counted.
//	Store      the two files on disk: `box.json` (non-secrets, ~/.config/caf)
//	           and the enrollment journal beside it.
//	Phase      the install table, pure data, so a plan is a function.
//
// THE ONE INVARIANT, stated once because everything else in this file is
// downstream of it: **the box's private key is never printed.** Not on stdout,
// not on stderr, not in an error, not in a report, not in a JSON document. It
// lives in the Keyring and the only thing that ever leaves this package about
// it is its *existence*, which `verify` reports as a fact rather than a value.
// A test asserts the absence by rendering every document every command
// produces and searching it for the key's bytes.
//
// The non-secrets are a separate file and a separate store on purpose. A
// box.json that also held the private key would make every dump, every
// transcript and every upload of the config file a credential leak; the split
// means the file that gets shared carries no key, and the thing that holds the
// key is not a file anybody reads.
//
// WHAT THIS PACKET DOES NOT DO, and deliberately: it does not run the
// installer. `kit` owns the substrate install (W2-P4) and `caf boxd` owns the
// daemon that runs it (W2-P7). What `apply --phase N` does here is own the
// *record* — which phase was reached, when, by what evidence, and what the
// next handoff is — so that when a real driver arrives it has a state machine
// to write into rather than a second idea of what a phase is. A record that
// claims a phase completed when nothing ran it is the defect this package is
// shaped to avoid, so `apply` records the request and the handoff and says
// plainly that the installer is not here.
//
// The journal is the ordering rule, and it is `internal/ledger`'s rule applied
// to a capability: **write the entry before you consume the resource.** A run
// that dies between the record and the redemption leaves a journal naming a
// pending keypair, which costs nothing; the reverse order spends a
// single-use capability and loses the installation it bought, which cannot be
// recovered by any retry.
package box
