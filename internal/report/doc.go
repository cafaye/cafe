// Package report assembles the one-page monthly report — the artifact that
// makes a subscription feel like a product rather than a favour.
//
// # What this is, and what it is not
//
// It is an ASSEMBLER, not a collector. Every fact it prints comes from one of
// four packets that already exist, plus a small number of shapes this packet
// defines because nothing in the tree defines them yet. It opens no socket,
// runs no command and reads no state directory: `caf report` takes a document
// in and writes a page out, the same boundary `caf failure check` holds (see
// internal/cli/failure.go for why that boundary is where it is — a verb that
// read the box directly would be a verb that only worked on a box).
//
// # The two forms, and why both are the product
//
// One value, two renderers, and the same argument cve/report.go:11-19 already
// made for itself: a report written straight to a terminal could not be
// summarised without re-deriving it, and a second derivation is a second thing
// that can disagree. Markdown for the customer's inbox and the website's
// screen; JSON for the box agent and anything else that consumes it.
//
// # The four packets this stands on
//
//   - internal/backup — the drill record (OPS-P9). backup.DrillRecord is the
//     verdict, the duration and the reason, and its Age was written for
//     exactly this report (drill.go:138-146).
//   - internal/secret — the inventory and the rotation schedule (OPS-P10).
//     secret.Due is what makes "3 credentials due, 1 still not rotatable" a
//     fact rather than a paragraph.
//   - internal/cve — the component inventory and the advisory surface
//     (OPS-P11). cve.Report is a value rather than a stream, and its doc says
//     why: "for OPS-P13's monthly report" (report.go:11-19).
//   - internal/failure — the failure-mode library (OPS-P12). A rule that HOLDs
//     is a condition identified on this box, which is the most expensive
//     sentence in the report and the one a generic monitor cannot produce.
//
// # The shapes this packet defines, and who owes them
//
// Five facts the report needs have no producer in the tree, and the honest
// answer to "where do they come from" is a name. They are declared in
// input.go with the owing packet named in the comment, and the report consumes
// them as data rather than as a fiction:
//
//   - availability per service — owed by the box's observation surface
//     (internal/boxd's World and the probe answer behind it). What exists
//     today carries the service names and nothing else, which is why
//     internal/failure's box observer answers UNKNOWN for all ten rules.
//   - capacity trend — owed by the same surface. It is deliberately a series
//     of points rather than a single now-reading: "the box is at 80%" is a
//     fact, "the box has been climbing for six weeks" is the fact a report is
//     for.
//   - incidents — owed by the same surface. `caf failure`'s observation
//     document and this one are two producers of two different facts, and
//     merging them would make both weaker.
//   - patches applied — owed by the packet that applies a fix. `caf cve` can
//     say what a box is EXPOSED to (OPS-P11); nothing yet records what was
//     actually applied and when.
//   - upstream releases received, applied or held — owed by the box's apply
//     path (internal/box's lifecycle, whose phase table owns what a box does
//     on an upstream release).
//
// A report section whose source supplied nothing is rendered as the gap it is:
// "no data", with the fact named, never as a zero. A zero would be read as a
// fact about the box and would be the loudest kind of wrong, which is the
// argument internal/failure made about UNKNOWN and internal/lock about a
// half-landed pin.
package report
