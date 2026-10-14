package verify

import (
	"fmt"
	"strings"
	"time"

	"github.com/cafaye/cafe/internal/boxd"
)

// The three answers the verifier gives, and the vocabulary every section of
// its document shares. Three rather than two, because "the box did not look"
// is a different fact from "the box looked and the answer is bad" — a
// verifier that collapsed them would be the green light over a blind spot
// internal/failure's checker exists to refuse.
const (
	StatusOK       = "ok"
	StatusDegraded = "degraded"
	StatusUnknown  = "unknown"
)

// VerificationSchemaVersion is the version of the verification document. It
// is a field rather than implied by the wire so a reader holding one in a
// year can tell which shape it is without knowing which cafe build produced
// it — the same reason every cafaye artifact carries its own version.
const VerificationSchemaVersion = 1

// diskWarnPercent is where a root filesystem stops being a number and starts
// being a sentence. Ninety is a shape rather than a measurement: the box
// writes its own images, its drill restores and its logs to the same
// filesystem, so a box at 94% has no room for the restore that proves its
// backups work — which is the failure this threshold exists to catch before
// the drill fails for a reason nobody can read.
const diskWarnPercent = 90

// Probe is every fact the verifier is allowed to know, handed over as values.
// It is a value rather than an interface for the reason internal/failure's
// Observation is: Check is then a pure function, and a test can build the
// exact box a rule was written for without a cluster, a clock or a network
// anywhere in sight.
//
// Nothing here is a secret. The secrets arrive as names with booleans; the
// health signals arrive as the observe.report document the box answered with;
// the ledger arrives as records that hold fingerprints and not values.
type Probe struct {
	// BoxID names the box being verified. It is what the human's part of a
	// degraded answer is addressed to, so a report nobody can attribute is
	// not a report this type can produce.
	BoxID string
	// Now is the clock the verifier reads, injected rather than read from
	// the process so a test drives the age of a drill without waiting a day.
	Now time.Time
	// Observation is the observe.report document this box answered with.
	// The zero value means the box never answered, and every health signal
	// is then unknown with that detail rather than assumed healthy.
	Observation boxd.Observation
	// Secrets is what the caller could establish about each secret the box
	// must hold before it answers what the operator typed.
	Secrets []SecretInput
	// Mint is the minted-secret ledger — the one piece of state this
	// verifier owns. Nil means no box has ever minted anything here, which
	// is the ordinary first state and not an error.
	Mint *State
}

// answered is whether the observation document is a real answer rather than
// the zero value: a box that has answered always stamps its own identity and
// the moment it assembled the report. The distinction is load-bearing — a
// box that never answered has no drill history and that is UNKNOWN, not
// "no drill has ever run", because calling it degraded would send an operator
// to chase a drill that never had the chance to happen.
func (p Probe) answered() bool {
	return !p.Observation.CollectedAt.IsZero()
}

// SecretInput is what the verifier is told about one secret the box must
// hold. NAMES AND BOOLEANS ONLY: the value never crosses this boundary, and
// the load-bearing test asserts the rendered document never carries one.
type SecretInput struct {
	// Name is the key's own name, as the kit installer's SECRET RECORD
	// spells it: COURIER_SECRET_BOX_KEY, SECRET_KEY_BASE, password.
	Name string
	// Present is whether the box holds the value — a Kubernetes Secret
	// exists on the box with this key in it.
	Present bool
	// Sealed is whether the control plane holds the value as an envelope
	// sealed to this box's public key. It answers where the value IS, for
	// the operator who has to go and get it, and it is not the same
	// question as whether the box can open it.
	Sealed bool
	// MintedBy names the box that minted this value rather than a human
	// typing one. Empty when a human pushed it at deploy time.
	MintedBy string
	// Elsewhere names where the value lives when it is NOT on the box and
	// NOT sealed for the control plane, and where it lives is still a fact
	// worth having. muse's vault key is the case: the inventory records no
	// namespace and no Secret object for it, because muse reads it per
	// request out of its own vault and it never becomes a cluster Secret at
	// all. A verifier that reported that as "missing" would demand an object
	// that cannot exist, which is the false alarm that teaches people to
	// ignore the report.
	Elsewhere string
	// Class is the inventory's word for the entry: rotatable, conditional
	// or not-rotatable. It is carried so the answer can say which class a
	// missing value belongs to without the verifier holding the table.
	Class string
}

// Signal is one health signal the edge frets about before it routes to a box.
// Each is answered ok / degraded / unknown, and unknown carries the detail of
// what would fill it.
type Signal struct {
	// Name is disk, certificate or health — the three the observe.report
	// carries per machine.
	Name string `json:"name"`
	// Status is one of the three answers.
	Status string `json:"status"`
	// Detail is the one sentence for a status that is not ok: what was
	// measured, or what the box did not supply.
	Detail string `json:"detail,omitempty"`
}

// SecretFact is one secret's answer: on the box, sealed for the control plane
// but not installed, absent, or never looked at. It carries the NAME and the
// location and never the value — the type has no field a value could be
// written into, which is the property internal/box.Identity holds for the
// private key.
type SecretFact struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// SealedForControlPlane is true when the value exists as an envelope
	// sealed to this box's public key, wherever the box itself stands.
	SealedForControlPlane bool `json:"sealed_for_control_plane,omitempty"`
	// MintedBy names the one box that minted and encrypted the value,
	// empty when a human pushed it at deploy time.
	MintedBy string `json:"minted_by,omitempty"`
	// Class is the inventory's word for the entry, when the caller knows it.
	Class string `json:"class,omitempty"`
	// Detail is the sentence for a status that is not on_box.
	Detail string `json:"detail,omitempty"`
}

// Reason is one degraded fact, with the person's part of it attached. A
// reason and its remedy are one value rather than two lists that could
// disagree, because a report that names a fault and leaves the human to work
// out what to do is the report that sends them to grep a log.
type Reason struct {
	// Key is the stable machine-matchable name: certificate_expired,
	// disk_full, secret_missing.
	Key string `json:"key"`
	// Detail is the human sentence, and Detail is NOT what a script
	// matches on — Key is.
	Detail string `json:"detail"`
	// Human is the next line: who reads it, where, and what changes the
	// answer to ok. Never the zero value on a reason this package renders.
	Human Human `json:"human"`
}

// Human is the person's part of a degraded answer, named rather than elided.
// The three fields answer the three questions a degraded status raises and
// that nobody answers: who is responsible, where they find out without a
// shell, and what they do that the next report will confirm.
type Human struct {
	// Owner is the role that reads the next line.
	Owner string `json:"owner"`
	// Where is the surface it is read on — not a log file on the box.
	Where string `json:"where"`
	// Action is what changes the answer to ok, runnable from anywhere.
	Action string `json:"action"`
}

// Verification is the answer: the box's secrets, the signals the edge frets
// about, and — when the answer is degraded — the person's next line.
type Verification struct {
	SchemaVersion int          `json:"schema_version"`
	BoxID         string       `json:"box_id"`
	Status        string       `json:"status"`
	CheckedAt     time.Time    `json:"checked_at"`
	Signals       []Signal     `json:"signals"`
	Secrets       []SecretFact `json:"secrets,omitempty"`
	// Reasons are the degraded facts, each with the human attached. Empty
	// when the status is not degraded, and never empty when it is.
	Reasons []Reason `json:"reasons,omitempty"`
	// Minted counts the ledger records behind this answer: how many values
	// on this box were minted by a box rather than typed by a human, and
	// are therefore not an enclave build's to mint again.
	Minted int `json:"minted,omitempty"`
}

// Check is the verifier. It is a pure function of the Probe, which is why
// every failure below is a field in the answer rather than an error return:
// a box with one missing secret and one expiring certificate verifies with
// two named reasons, and an error would destroy that.
//
// The precedence is deliberate: degraded outranks unknown, because a box
// that is one way wrong and another way unread is a box that needs a person,
// and a person who is told only "unknown" goes looking. Unknown outranks ok
// for the same reason the failure library refuses a pass over a blind spot.
func Check(p Probe) Verification {
	v := Verification{
		SchemaVersion: VerificationSchemaVersion,
		BoxID:         p.BoxID,
		CheckedAt:     p.Now,
		Signals:       []Signal{},
	}
	obs := p.Observation

	// The three signals the edge frets about, judged against the document
	// the box answered with rather than against a fresh reading: the edge is
	// routing on that report, so the verifier checks the SAME report — a
	// verifier that re-measured would eventually disagree with the thing it
	// is verifying.
	disk := diskSignal(obs.Disk)
	cert := certificateSignal(obs.Certificate)
	health := healthSignal(obs.Health)
	v.Signals = append(v.Signals, disk, cert, health)

	// A degraded SIGNAL is a degraded reason, because the person's next line
	// is attached to a reason and not to a signal — a status nobody can act
	// on is the reason people SSH in and grep a log.
	if disk.Status == StatusDegraded {
		v.add(p, "disk_full", disk.Detail)
	}
	if cert.Status == StatusDegraded {
		key := "certificate_expired"
		if obs.Certificate.Status == "expiring" {
			key = "certificate_expiring"
		}
		v.add(p, key, cert.Detail)
	}
	if p.answered() && cert.Status == StatusUnknown {
		// A certificate that does not exist is not a fact to paper over: the
		// daemon refuses to dial without it, so the box cannot do its whole
		// job and the verification says so with the box's own detail and the
		// packet that fills the gap. It is a degraded reason rather than an
		// unread one because the box HAS answered — the answer is that it
		// cannot connect, which is a fault with a named fix, not a blind spot.
		// The answered() guard is the whole distinction from the unread box,
		// whose zero-value certificate is a blind spot and stays unknown.
		v.add(p, "certificate_absent", cert.Detail)
	}

	if p.answered() {
		// A drill that failed or never ran is a health fact the edge reads
		// through Health, but the reason belongs to the drill, because the
		// remedy is `cafe backup drill` and not a certificate.
		switch {
		case obs.LastDrill == nil:
			v.add(p, "drill_absent", "no backup drill has ever run on this box")
		case obs.LastDrill.Outcome != "passed":
			v.add(p, "drill_failed", "the last backup drill did not pass ("+obs.LastDrill.Outcome+")")
		}
	} else {
		v.add(p, "observation_unread", "this box has not answered observe.report, so its health is not a fact the verifier has")
	}

	for _, s := range p.Secrets {
		v.Secrets = append(v.Secrets, secretFact(s))
		if key, detail, ok := secretReason(p, s); ok {
			v.add(p, key, detail)
		}
	}

	// The box's own word for itself, last: when it says degraded and nothing
	// this verifier named explains it, its reasons are carried through —
	// "the probe timeout is the bug" is a sentence nobody else is going to
	// produce. When something above already explains it, this stays quiet
	// rather than saying the same thing twice.
	if health.Status == StatusDegraded && !explainedBy(v.Reasons) {
		v.add(p, "health_degraded", health.Detail)
	}

	if p.Mint != nil {
		v.Minted = len(p.Mint.Records)
	}

	// The status is folded from BOTH sections, and the signals are what make
	// it so: a disk at 94% is degraded even though no reason row names it,
	// because the reason rows are for the facts that need a person and a
	// signal is the edge's own question. A verifier whose summary disagreed
	// with its own signals would be a document that contradicts itself in
	// the one field a machine reads first.
	v.Status = StatusOK
	for _, s := range v.Signals {
		switch s.Status {
		case StatusDegraded:
			v.Status = StatusDegraded
		case StatusUnknown:
			if v.Status == StatusOK {
				v.Status = StatusUnknown
			}
		}
	}
	for _, r := range v.Reasons {
		switch r.Key {
		case "observation_unread", "secrets_unread", "mint_unrecorded":
			// A fact the verifier could not read is not a fault: the box is
			// unread, not broken, and saying "degraded" would send someone
			// to fix the wrong thing.
			if v.Status == StatusOK {
				v.Status = StatusUnknown
			}
		default:
			v.Status = StatusDegraded
		}
	}
	return v
}

// add records one reason with the person's part of it attached, so a degraded
// answer can never leave the report without the next line. It is a method
// rather than an append because the alternative is a verification whose
// reasons and whose humans are two lists that can disagree.
func (v *Verification) add(p Probe, key, detail string) {
	v.Reasons = append(v.Reasons, Reason{Key: key, Detail: detail, Human: remedy(key, p.BoxID)})
}

// diskSignal judges the root filesystem. A reading that failed is unknown
// with the error's own words — a zero where a free-byte number belongs is a
// claim that the box is empty, and that is the worst possible failure of the
// field, so the verifier would rather say it does not know.
func diskSignal(d boxd.Disk) Signal {
	switch {
	case d.Status != StatusOK:
		return Signal{
			Name:   "disk",
			Status: StatusUnknown,
			Detail: firstLine(d.Detail, "the box's root filesystem was not measured"),
		}
	case d.UsedPercent >= diskWarnPercent:
		return Signal{
			Name:   "disk",
			Status: StatusDegraded,
			Detail: fmt.Sprintf("the root filesystem at %s is %.0f%% full (%d of %d bytes free)", d.Path, d.UsedPercent, d.FreeBytes, d.TotalBytes),
		}
	default:
		return Signal{
			Name:   "disk",
			Status: StatusOK,
			Detail: fmt.Sprintf("the root filesystem is %.0f%% full", d.UsedPercent),
		}
	}
}

// certificateSignal judges the credential closest to a hard stop there is: an
// expired enrollment is a box that can no longer dial anywhere, including the
// place it would report that fact to.
func certificateSignal(c boxd.Certificate) Signal {
	switch c.Status {
	case "expired":
		return Signal{Name: "certificate", Status: StatusDegraded, Detail: firstLine(c.Detail, "the client certificate has expired; this box can no longer dial box")}
	case "expiring":
		return Signal{Name: "certificate", Status: StatusDegraded, Detail: firstLine(c.Detail, "the client certificate is close to expiry")}
	case StatusOK:
		detail := "the client certificate is valid"
		if c.DaysLeft != nil {
			detail = fmt.Sprintf("the client certificate is valid for %d more day(s)", *c.DaysLeft)
		}
		return Signal{Name: "certificate", Status: StatusOK, Detail: detail}
	default:
		return Signal{Name: "certificate", Status: StatusUnknown, Detail: firstLine(c.Detail, "the box's own certificate could not be read")}
	}
}

// healthSignal is the box's own summary, passed through rather than
// recomputed: the verifier judges the answer the box gave, and a second
// derivation of "what is healthy" is a second opinion nobody asked for. The
// reasons Health already names are carried into the verification, so the
// report says WHY rather than only that.
//
// Note the vocabulary: boxd.Health answers "healthy" where Disk answers "ok"
// — the box's own word for itself is not the edge's word for a signal. The
// signal below speaks the verifier's one vocabulary, and this function is the
// only place the two are reconciled, so a consumer reading the verification
// does not have to know which document a status came from.
func healthSignal(h boxd.Health) Signal {
	switch h.Status {
	case "healthy":
		return Signal{Name: "health", Status: StatusOK, Detail: fmt.Sprintf("the box is healthy at install phase %d", h.Phase)}
	case "degraded":
		detail := "the box reports itself degraded"
		if len(h.Reasons) > 0 {
			detail = "the box reports itself degraded: " + strings.Join(h.Reasons, "; ")
		}
		return Signal{Name: "health", Status: StatusDegraded, Detail: detail}
	default:
		return Signal{Name: "health", Status: StatusUnknown, Detail: "this box has not reported its health"}
	}
}

// secretFact answers where one secret stands, in the vocabulary the report
// renders: on the box, sealed for the control plane but not installed on it,
// absent everywhere, or never looked at.
func secretFact(s SecretInput) SecretFact {
	fact := SecretFact{
		Name:                  s.Name,
		SealedForControlPlane: s.Sealed,
		MintedBy:              s.MintedBy,
		Class:                 s.Class,
	}
	switch {
	case s.Present:
		fact.Status = "on_box"
		if s.Sealed {
			fact.Detail = "held on the box, and sealed for the control plane as an envelope only this box's key opens"
		} else {
			fact.Detail = "held on the box"
		}
	case s.Elsewhere != "":
		// The value is somewhere the box's Secret surface does not reach,
		// and that is where it is SUPPOSED to be. Reported, not remedied.
		fact.Status = "held_elsewhere"
		fact.Detail = s.Elsewhere
	case s.Sealed:
		fact.Status = "sealed_only"
		fact.Detail = "sealed for the control plane against this box's public key, and not installed on the box, so a command that needs it fails"
	default:
		fact.Status = "missing"
		fact.Detail = "neither on the box nor sealed for the control plane"
	}
	return fact
}

// secretReason is one secret's degraded reason, when it has one. A secret
// that is not on the box is a degraded fact whether or not the value exists
// elsewhere: the box answers what the operator typed, and an answer that
// cannot send mail is the answer the operator asked about.
//
// The one cross-check a present secret earns is against the ledger: a box
// reporting a mint by a box the ledger never heard of is two boxes with one
// name, and that fork is the thing the ledger exists to catch.
func secretReason(p Probe, s SecretInput) (Key, Detail string, _ bool) {
	switch {
	case s.Present:
		if s.MintedBy == "" {
			return "", "", false
		}
		if p.Mint == nil {
			return "mint_unrecorded", fmt.Sprintf("the box reports %s was minted by box %s, and the minted-secret ledger holds no record of it", s.Name, s.MintedBy), true
		}
		rec, ok := p.Mint.Minted(s.Name)
		if !ok {
			return "mint_unrecorded", fmt.Sprintf("the box reports %s was minted by box %s, and the minted-secret ledger holds no record of it", s.Name, s.MintedBy), true
		}
		if rec.BoxID != s.MintedBy {
			return "secret_forked", fmt.Sprintf("%s was minted by box %s, and this box reports it was minted by box %s: two boxes hold one name", s.Name, rec.BoxID, s.MintedBy), true
		}
		return "", "", false
	case s.Elsewhere != "":
		// Nowhere a Secret could be, and that is not a fault: see
		// SecretInput.Elsewhere.
		return "", "", false
	case s.Sealed:
		return "secret_not_installed", fmt.Sprintf("%s is sealed for the control plane against this box's key and is not installed on the box", s.Name), true
	default:
		return "secret_missing", fmt.Sprintf("%s is neither on the box nor sealed for the control plane", s.Name), true
	}
}

// explainedBy is whether a reason above already accounts for the box's own
// degraded health. The box's summary is DERIVED from the facts a verifier
// checks independently — healthStatus's certificate and drill reasons are
// the same facts — so reporting both would be the same sentence twice, and a
// report that says one thing two ways is a report a reader discounts.
func explainedBy(reasons []Reason) bool {
	explains := map[string]bool{
		"drill_absent": true, "drill_failed": true,
		"certificate_expired": true, "certificate_expiring": true,
		"certificate_absent": true,
	}
	for _, r := range reasons {
		if explains[r.Key] {
			return true
		}
	}
	return false
}

// remedy is the person's part of a reason, and it is a table rather than a
// switch at each call site because the next hour of a degraded answer is a
// sentence somebody has to be able to correct without editing Go.
//
// Where is the same for every reason and that is the point: the answer is
// read on the observe.report the edge polls and on the line under this one,
// which is to say on the surface the operator is already looking at, and not
// in a log on the box that only a shell can reach.
func remedy(key, boxID string) Human {
	owner := "the operator on call for box " + firstLine(boxID, "(unnamed)")
	where := "the observe.report the edge polls for this box, and the line under this one"
	action := "read the reason above; the next observe.report answers whether it still holds"
	switch key {
	case "certificate_expired", "certificate_expiring":
		action = "re-enroll the box (`cafe box enroll --token <cap>`); issuance is bound to enrollment, so the fresh enrollment is what the Phase C certificate packet signs — the days-left number in this report is what says whether it worked"
	case "certificate_absent":
		action = "nothing on this box is broken and there is nothing to fix on it: client-credential issuance lands with the Phase C packet named in boxd's package doc, and until then the daemon refuses loudly after three rounds, which is this report's degraded answer being the honest one"
	case "disk_full":
		action = "`cafe reclaim` releases what a failed session left behind, and the free-byte number in this report is what says it worked"
	case "drill_absent", "drill_failed":
		action = "run `cafe backup drill <service>`: the outcome is recorded, and the next observe.report reports it without anybody grepping a log"
	case "health_degraded":
		action = "`cafe failure check` against this observation names which documented failure mode holds; the reasons under this one are the box's own words for it"
	case "secret_missing":
		action = "push the secret at deploy time (`cafe deploy <service>`) — never bake it into the image — and this report's secret section says when the box holds it"
	case "secret_not_installed":
		action = "the value is sealed to this box's key; installs what the control plane posts to it, so re-run `cafe provider connect <provider>` and re-read this report"
	case "secret_forked":
		action = "two boxes hold one minted name; `cafe secret inventory` names the class and the entry, and the ledger in this report names the box that minted it first"
	case "observation_unread", "secrets_unread":
		action = "ask the box for its report (`cafe boxd` answers observe.report over the mTLS channel) and run this verification again"
	case "mint_unrecorded":
		action = "the ledger holds no record of this mint; record it with `cafe box verify --mint <name>` before anything rebuilds and mints a second value"
	}
	return Human{Owner: owner, Where: where, Action: action}
}

// firstLine is the answer's own words when it has any, and the fallback when
// it does not. An empty detail on an unknown answer is an answer that tells
// a reader nothing, so every unknown carries a sentence even when the
// substrate that would have supplied one is silent.
func firstLine(detail, fallback string) string {
	if strings.TrimSpace(detail) == "" {
		return fallback
	}
	return detail
}
