package boxd

import (
	"context"
	"time"
)

// The observe stream: one box reporting on itself, machine-readable.
//
// OPS-P15. boxd already dials out to conductor over mTLS and redeems
// capabilities against a whitelisted route table. This is the READ half of
// that channel made real: a conductor mints an observe.report capability, the
// box answers with one Observation document, and the policy engine (OPS-P16)
// and the monthly report (OPS-P13) consume it as JSON rather than as prose.
//
// The document is deliberately SHAPE-FIRST. Every field the future consumers
// need is declared here even where the v0 world can only report it as
// `unknown` — the value lands when the substrate lands, and the schema a
// consumer is written against does not move under it when it does. That is
// the pantry principle applied to a wire format: a field the box cannot fill
// yet says `unknown` WITH A REASON, never a guess, because a zero where a
// measurement belongs is a claim and a claim nobody can act on is worse than a
// stated gap.
//
// The one deliberate asymmetry: WHICH releases are pending is the box's to
// answer, but the universe of releases is conductor's. Conductor knows every
// release it has recorded; only the box knows which of them fit the version
// range IT was enrolled under. So conductor sends the candidates as the
// capability's parameters (ObserveParams) and the box matches them against its
// own range and its own deployed version. Neither side has to trust the other
// with a fact it can compute itself, and there is no second network call for
// the release catalog to arrive over — it rides the capability that already
// carries every other instruction, so the observe path introduces no fetch of
// its own beyond the bounded mTLS channel (doc.go, dialTimeout; client.go set
// deadlines per request).

// ObservationSchemaVersion is the version of the observation document. It is a
// field rather than implied by the wire so a consumer reading one in a year
// can tell which shape it is holding without knowing which caf build produced
// it — the same reason every cafaye artifact carries its own version.
const ObservationSchemaVersion = 1

// Observation is one box's report on itself: the deployed facts, the facts in
// degraded status where v0 cannot yet measure them, and the releases that are
// pending for this box's version range. It is the answer observe.report gives
// conductor, marshalled for the wire, and it is a non-secret by construction
// — the load-bearing test in secret_test.go walks this type graph and fails if
// anything reachable from it is Secret-shaped.
type Observation struct {
	// SchemaVersion is ObservationSchemaVersion, so a reader knows the shape.
	SchemaVersion int `json:"schema_version"`
	// BoxID is which box this is — the enrollment a report no reader can
	// attribute is a report nobody can act on.
	BoxID string `json:"box_id"`
	// DeployedVersion is the caf build this box is running as. It is what a
	// pending release is compared against.
	DeployedVersion string `json:"deployed_version"`
	// VersionRange is the constraint the box tracks, and whether the box
	// knows it. Without a range the box cannot classify which releases are
	// pending, so this carries its own status rather than an empty string a
	// reader would mistake for "no constraint".
	VersionRange VersionRange `json:"version_range"`
	// Conductor is the conductor this box answers to, for the reader that
	// needs to route a question back to the control plane.
	Conductor string `json:"conductor,omitempty"`
	// CollectedAt is when the box assembled this. It is the box's clock, and
	// it is separate from any drill or expiry timestamp for that reason.
	CollectedAt time.Time `json:"collected_at"`
	// Health is the box's own summary of whether it needs attention, and the
	// phase the install reached.
	Health Health `json:"health"`
	// Certificate is the box's client certificate's expiry, which is the
	// credential closest to a hard stop there is — an expired enrollment is a
	// box that can no longer dial anywhere.
	Certificate Certificate `json:"certificate"`
	// Disk is the box's root filesystem. v0 reports it as unknown because
	// reading it needs the substrate; the field is here so the day it is
	// measurable does not move the schema.
	Disk Disk `json:"disk"`
	// LastDrill is the most recent backup drill for a service on this box,
	// and nil when this box has never run a drill — a box with no history is
	// the ordinary first state, not an error.
	LastDrill *DrillFact `json:"last_drill,omitempty"`
	// PendingReleases are the candidate releases that are both inside this
	// box's version range and strictly newer than the deployed version. The
	// applied-or-held question is a later packet's; this names what COULD be
	// applied.
	PendingReleases []PendingRelease `json:"pending_releases"`
}

// VersionRange is the constraint the box tracks, with its status. A Status of
// "unknown" with a Detail is the honest read of a box that has not been told
// its range — which is a fact about this build rather than about the box.
type VersionRange struct {
	// Range is the constraint text, e.g. "^1.2.3", empty when Status is
	// unknown.
	Range string `json:"range,omitempty"`
	// Status is "ok" or "unknown".
	Status string `json:"status"`
	// Detail is the one sentence a reader needs when Status is not "ok": where
	// the range would come from, or why it is absent.
	Detail string `json:"detail,omitempty"`
}

// Health is the box's own summary. It is derived from the other facts rather
// than measured directly: a box is degraded when a fact it can see is wrong —
// a drill that failed, a certificate about to expire — and healthy when
// nothing it can see needs attention.
type Health struct {
	// Status is "healthy", "degraded" or "unknown".
	Status string `json:"status"`
	// Phase is the install phase reached, mirroring StackStatus.Phase, so a
	// reader of the observation does not have to also ask stack.status to
	// know where the install stopped.
	Phase int `json:"phase"`
	// Reasons name the facts that set Status to degraded. Empty when healthy,
	// and never empty when degraded.
	Reasons []string `json:"reasons,omitempty"`
}

// Certificate is the box client certificate's expiry. The certificate is
// public — its existence and its dates are not secret — but its expiry is the
// one date that decides whether the box can dial at all.
type Certificate struct {
	// Status is "ok", "expiring" (inside the warn window), "expired" or
	// "unknown".
	Status string `json:"status"`
	// Detail is the one sentence for a status that is not "ok".
	Detail string `json:"detail,omitempty"`
	// ExpiresAt is the NotAfter of the box's client certificate, nil when the
	// box could not read its own certificate.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// DaysLeft is whole days to expiry, negative once expired, nil when the
	// certificate's date is unknown. Stored rather than derived so a consumer
	// does not parse two timestamps to answer "is this cert about to stop
	// this box reporting".
	DaysLeft *int `json:"days_left,omitempty"`
}

// Disk is the box's root filesystem. v0 has no portable way to read it — the
// daemon may not import syscall (AGENTS.md forbids it in this package, because
// the reclamation path must build for every release target) and the standard
// library exposes no disk usage — so the field is a stated gap, not a guess.
type Disk struct {
	// Status is "ok" or "unknown". v0 always reports "unknown".
	Status string `json:"status"`
	// Detail says why, and what would fill it.
	Detail string `json:"detail,omitempty"`
}

// DrillFact is the last backup drill, flattened to the facts a report needs.
// It is a mirror of internal/backup.DrillRecord's non-secret fields rather
// than that type itself, because this package is stdlib-only by the house rule
// (it runs on the box, ahead of any dependency being available) and importing
// backup would drag a YAML parser into the daemon for four fields.
type DrillFact struct {
	// Outcome is backup's own word: passed, failed or refused.
	Outcome string `json:"outcome"`
	// Reason is the machine-matchable name (caf-backup/<name>).
	Reason string `json:"reason,omitempty"`
	// Detail is the failing command's or refusal's words, trimmed. It is
	// prose and NOT what a script matches on — Reason is.
	Detail string `json:"detail,omitempty"`
	// FinishedAt is when the drill finished; nil if the record carried none.
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// AgeDays is whole days since the drill finished, nil when the date is
	// unknown. "The last drill passed, six months ago" is a different report
	// from one passed yesterday, and a consumer should not compute it twice.
	AgeDays *int `json:"age_days,omitempty"`
}

// PendingRelease is one release this box could apply: it is inside the box's
// version range and newer than what is deployed. Name and Version are all a
// policy decision needs to begin.
type PendingRelease struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ObserveParams is what an observe.report capability carries: the universe of
// releases conductor knows about, which the box matches against its own range.
// Conductor owns the catalog; the box owns the range; the match is the answer
// and neither side had to be told the other's fact.
type ObserveParams struct {
	Releases []CandidateRelease `json:"releases,omitempty"`
}

// CandidateRelease is one release conductor is offering. It carries only the
// two facts the match needs — a name and a version — and nothing else, so a
// capability that names a release cannot smuggle any other shape into the
// box's answer.
type CandidateRelease struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// observeReport is the read-only route that answers the observation stream. It
// takes the release universe and hands the world everything it needs to
// assemble the report; the answer is the whole Observation, marshalled into
// the Ack beside its Outcome so a capability that asked to observe is answered
// rather than merely acknowledged.
//
// It is READ-ONLY: observing changes nothing on the box. That is the whole
// reason it is safe to reach widely — OPS-D7 grants observation as widely as
// it grants anything, because the report is a window and not a lever.
func observeReportRoute() Route {
	h := Handler[ObserveParams, Observation](func(ctx context.Context, w World, in ObserveParams) (Observation, error) {
		return w.Observe(ctx, in)
	})
	return route("observe.report",
		"report the box's health, deployed version, certificate expiry, last drill and pending releases as JSON",
		true, ObserveParams{}, Observation{}, h)
}
