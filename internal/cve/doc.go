// Package cve computes the CVE surface of a box: what is installed on it,
// which of those components carries a known advisory, and what applying the
// fix would touch.
//
// # Why this exists at all, given that osv-scanner and trivy exist
//
// A scanner answers "this lockfile has these advisories". That is a question
// about a repository, and this is a question about a BOX: the thing the
// Attended customer pays us to keep alive has a k3s node agent, four Argo
// Applications, a Redis, a NATS and a Postgres that no application lockfile in
// their repo has ever named. A repo scan is silent about every one of those,
// and the substrate is where an unpatched node agent sits for eighteen months.
//
// # The claim the fleet is buying
//
// "You have 14 advisories" is a number nobody can act on. The answer that is
// worth a subscription names the component, the advisory, the version that
// fixes it, and what applying that version touches — because the answer to
// "can you upgrade k3s on my box tonight" is a different sentence depending
// on whether the fix rolls one Deployment or drains the node, and the
// customer is the one who has to decide. So the report is per-advisory and
// per-component, and the impact is derived from what the substrate is rather
// than guessed.
//
// # What the inventory is, and where it comes from
//
// The component catalog is a transcription of the substrate installer's own
// pins — kit/templates/substrate/install.sh and the manifests it applies —
// and every entry carries the file and line it was read from. It is a copy,
// and a copy can drift; the answer to that is not a fetch (cafaye/kit is a
// different repository, and a cafe that reached across to it at run time would
// be a cafe whose answer changed with someone else's checkout) but a
// comparison: the catalog is what a substrate box is EXPECTED to be
// running, the observation is what it IS running, and the inventory reports
// the two side by side and names every divergence. A pinned version nobody
// applied, and an applied version nobody pinned, are both reported rather
// than reconciled into one list that hides one of them.
//
// # The three seams
//
// Observe is how the box is read — one method, satisfied on a box by the
// kubectl-driven collector and in a test by a fake, for the same reason
// internal/boxd's World is an interface: a test that needed a cluster would
// not be run, and a check nobody runs is a check nobody has.
//
// Source is where advisories come from. It is one method wide for the same
// reason, and it is the seam that keeps the network out of the classification
// tests: the classifier takes a list of advisories and says nothing about
// where they came from.
//
// Emit is where a report goes — a checkpoint per phase, so a scan that is
// slow says which phase it is in instead of appearing to hang. That is not
// decoration: an advisory fetch over a network that has stopped answering is
// an invisible stall, and the predicate the brief states is that a network
// call that hangs is an invisible stall.
//
// # What this package deliberately is not
//
// It is not a scanner for application dependencies. The customer's own
// repository has lockfiles and their CI already has osv-scanner, govet and a
// dependency audit in it; a second, weaker copy of that inside cafe would
// disagree with the first about what a vulnerable dependency is. The subject
// here is the box.
//
// It is not a fixer. Every advisory in the report is answered with what
// applying the fix would TOUCH, never with a command run: the Attended tier's
// promise is upgrade *recommendations* the customer applies, and a cafe that
// applied them would be a cafe that rolled a node agent without being asked.
//
// It is not a vulnerability database. The database is the other side of a
// network call; this package is the matching, the classification and the
// report, and it says so about every fact it does not own.
package cve
