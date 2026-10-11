package stack

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNoRoute is the refusal for a host the box's edge does not route. It is
// its own sentinel because the remedy is "point DNS at this box" or "add the
// route", and a caller that typed it into a generic failure message would
// send an operator to look at the release.
var ErrNoRoute = errors.New("the edge does not route")

// ErrNoCert is the refusal for a host routed by the edge whose certificate is
// not valid now. A deploy that served traffic over an expired certificate
// would be a deploy that proves rollback by breaking the site a second time.
var ErrNoCert = errors.New("the edge has no valid certificate")

// Build is one pantry build: what the box pulls, and the digest that pins it.
// A build without a digest is not a build, it is a hope.
type Build struct {
	// Registry is the OCI registry the box pulls from.
	Registry string `json:"registry"`
	// Name is the repository, and the service's image name in it.
	Name string `json:"name"`
	// Tag is what a person recognises. It is decoration: the digest is the
	// identity, and a tag is never what the box pulls.
	Tag string `json:"tag,omitempty"`
	// Digest is `sha256:…`, and it is required. See imageOK.
	Digest string `json:"digest"`
}

// Ref is the reference the box pulls. The digest always, and the tag only as
// a label for the pull log.
func (b Build) Ref() string {
	if b.Digest == "" {
		return b.Name
	}
	return b.Name + "@" + b.Digest
}

// Validate refuses a build that could not be rolled back to. The refusal
// names the reason, because "invalid build" sends an operator to the
// manifest and the problem is here.
func (b Build) Validate() error {
	if strings.TrimSpace(b.Name) == "" {
		return errors.New("the build names no image")
	}
	if !digestOK(b.Digest) {
		return fmt.Errorf("the build for %s is not pinned to a digest (%q): a tag is a name that means something different tomorrow, and rollback is instant only if the artifact it goes back to is still the same bytes", b.Name, b.Digest)
	}
	return nil
}

// Host is the live host and certificate state the box's edge holds for one
// name. It is what the deploy reads BEFORE it stages anything, which is the
// order the whole design rests on: a deploy pointed at a host the edge does
// not route is a deploy that would stage a release nobody can reach, and the
// only cheap moment to notice is before the work happens.
type Host struct {
	Name string `json:"name"`
	// Routed is whether the edge answers for this host at all.
	Routed bool `json:"routed"`
	// CertSubject is what the certificate the edge holds is issued to.
	CertSubject string `json:"cert_subject,omitempty"`
	// CertNotAfter is when that certificate stops being valid. Zero means the
	// edge holds no certificate for this host.
	CertNotAfter time.Time `json:"cert_not_after,omitempty"`
}

// ValidNow reports whether the edge can serve this host over TLS right now.
// It answers with a clock rather than asking the edge to decide, because a
// host with no certificate and a host with an expired one are the same state
// to the client that connects.
func (h Host) ValidNow(now time.Time) bool {
	if !h.Routed {
		return false
	}
	return !h.CertNotAfter.IsZero() && h.CertNotAfter.After(now)
}

// Box is what the box does that this package will not pretend to do: read
// the edge's live state, start a release, and answer whether it is healthy.
// It is an interface because the box's real substrate is a container
// runtime and this package is the decision logic; a test drives a fake that
// counts, and the count is the proof.
type Box interface {
	// Host is the live state for one name. The second answer is false when
	// the edge has never heard of it, which is a different refusal from
	// "routed but no certificate" and is reported differently.
	Host(name string) (Host, bool)
	// Serve starts the release on the box: pull the pinned build, install the
	// named secret set into the release directory, and run it. It returns
	// when the release is up, or when it could not be brought up at all.
	Serve(ctx context.Context, rel Release) error
	// Ready is the health gate. It answers whether the release is answering
	// correctly on its host, and it is called only after the pointer has
	// moved — because a release that is not in front of traffic is a release
	// nobody is testing.
	Ready(ctx context.Context, rel Release) error
}

// Request is one deploy. It carries a pantry build and the host it answers
// on, and it carries NO configuration values: the only reference to a secret
// is the name of the set the box already holds.
type Request struct {
	Service string `json:"service"`
	Build   Build  `json:"build"`
	// Host is the name this release answers on through the edge.
	Host string `json:"host"`
	// Secrets is the name of the secret set the box installs into the
	// release at deploy time. The set's VALUES never appear in a Request, a
	// Release, a History or a Report: they are written where the release
	// reads them, by the box, at the moment of the deploy.
	Secrets string `json:"secrets,omitempty"`
	// Now is injectable so a test can make two deploys in the same second
	// without their ids colliding by luck.
	Now time.Time `json:"-"`
}

// Step names the six things a deploy does, in the order it does them. They
// are constants because the report and the tests both name them, and a
// rename that missed one would be a report about a step nothing runs.
const (
	StepValidate = "validate"
	StepEdge     = "edge"
	StepStage    = "stage"
	StepServe    = "serve"
	StepSwitch   = "switch"
	StepGate     = "gate"
	StepPrune    = "prune"
)

// Report is what a deploy answers with, and it is the document
// `cafe box deploy` prints. On a failure it says what is serving NOW, which
// after a rollback is the last good release and not the one that failed —
// because the answer to "what is on my box" during an incident must be the
// truth about the box, not the truth about the attempt.
type Report struct {
	Service    string   `json:"service"`
	Host       string   `json:"host"`
	Build      Build    `json:"build"`
	Release    Release  `json:"release"`
	Previous   *Release `json:"previous,omitempty"`
	Failed     *Release `json:"failed,omitempty"`
	Outcome    string   `json:"outcome"`
	RolledBack bool     `json:"rolled_back"`
	Pruned     []string `json:"pruned,omitempty"`
	Detail     string   `json:"detail,omitempty"`
}

// Outcome values a report can carry. A deploy either holds the new release or
// puts the old one back; a report that hedged between the two would be a
// report nobody can act on.
const (
	OutcomeDeployed  = "deployed"
	OutcomeRolled    = "rolled_back"
	OutcomeNoCurrent = "no_current"
)

// ErrGateFailed is returned when a release was switched into traffic and did
// not pass its health gate, and has been switched back out again. It is a
// sentinel so the CLI can exit non-zero on purpose rather than on accident:
// the box is healthy and the deploy failed, and those two facts want
// different exit codes from a script.
var ErrGateFailed = errors.New("release did not pass its health gate")

// Deploy runs the six steps in order, and the order is the argument:
//
//  1. validate — the build is pinned by digest, the service and host are
//     named. Nothing has been touched, so a refusal here costs nothing.
//  2. edge — read the live host and certificate state. Refuse a host the
//     edge does not route, or whose certificate is not valid now. Still
//     nothing staged: this is the last moment the answer is cheap.
//  3. stage — write the release directory. Exclusive, so it cannot clobber.
//  4. serve — pull and start it on the box.
//  5. switch — flip the pointer, atomically.
//  6. gate — health-check what is now in front of traffic. If it fails, flip
//     back to last_good, mark the failed release, and return ErrGateFailed.
//
// The switch is before the gate on purpose. You cannot know whether a release
// works without putting it in front of traffic, and the alternative — a
// second "test" copy of everything — is a staging environment the box does
// not have and that would prove nothing about the release actually serving.
// What makes this safe is step 6's fixing: the pointer goes back.
func Deploy(ctx context.Context, s *Store, box Box, req Request) (Report, error) {
	rep := Report{Service: req.Service, Host: req.Host, Build: req.Build}
	if err := req.Build.Validate(); err != nil {
		return rep, fmt.Errorf("%s: %w", StepValidate, err)
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	host, known := box.Host(req.Host)
	if !known {
		return rep, fmt.Errorf("%s: %w %q — this box's edge has no route for it; add the route, or point the name at this box, before deploying", StepEdge, ErrNoRoute, req.Host)
	}
	if !host.ValidNow(now) {
		return rep, fmt.Errorf("%s: %w for %q (routed %v, certificate valid until %s) — a deploy over an expired certificate proves rollback by breaking the site a second time", StepEdge, ErrNoCert, req.Host, host.Routed, certUntil(host))
	}

	before, err := s.History()
	if err != nil {
		return rep, err
	}
	id, err := NewID(now, req.Build.Digest)
	if err != nil {
		return rep, fmt.Errorf("%s: %w", StepValidate, err)
	}
	rel := Release{
		ID:        id,
		Service:   req.Service,
		Version:   req.Build.Tag,
		Image:     req.Build.Ref(),
		Host:      req.Host,
		Secrets:   req.Secrets,
		StartedAt: now,
		Outcome:   Staged,
	}
	prev := before.Current
	if prev != "" {
		if p, ok := before.find(prev); ok {
			rep.Previous = &p
		}
	}
	if staged, err := s.Stage(rel); err != nil {
		return rep, fmt.Errorf("%s: %w", StepStage, err)
	} else {
		rel = staged
	}

	if err := box.Serve(ctx, rel); err != nil {
		// A release that could not even be started never went in front of
		// traffic, so there is nothing to roll back FROM — but it is recorded
		// as failed rather than deleted, because the operator's first
		// question is "what broke" and the answer is on disk.
		rel.Outcome = Failed
		rel.EndedAt = now
		if err := s.mark(rel); err != nil {
			return rep, err
		}
		rep.Failed = &rel
		cur, err := s.Current()
		if err != nil {
			return rep, err
		}
		if cur.ID == "" {
			rep.Outcome = OutcomeNoCurrent
			rep.Detail = fmt.Sprintf("release %s could not be started and nothing else is on this box, so %s is not serving anything", rel.ID, req.Service)
		} else {
			rep.Release = cur
			rep.Outcome = OutcomeRolled
			rep.RolledBack = true
			rep.Detail = fmt.Sprintf("release %s could not be started and %s is still serving", rel.ID, cur.ID)
		}
		return rep, fmt.Errorf("%s: %w: %v", StepServe, ErrGateFailed, err)
	}

	if _, err := s.Switch(rel.ID); err != nil {
		return rep, fmt.Errorf("%s: %w", StepSwitch, err)
	}
	if err := box.Ready(ctx, rel); err != nil {
		rep2, err2 := s.flipBack(rel, prev, err)
		return rep2, err2
	}

	h, err := s.History()
	if err != nil {
		return rep, err
	}
	h.LastGood = rel.ID
	if err := s.save(h); err != nil {
		return rep, err
	}
	pruned, err := s.Prune()
	if err != nil {
		return rep, err
	}
	rep.Release = rel
	rep.Outcome = OutcomeDeployed
	rep.Pruned = pruned
	rep.Detail = fmt.Sprintf("release %s is serving %s on %s", rel.ID, req.Service, req.Host)
	return rep, nil
}

// flipBack is the second half of the design, and the reason the switch is
// allowed to come before the gate. It puts the last-good release back,
// records the failed one as failed, and answers with a report that says what
// is serving now.
func (s *Store) flipBack(failed Release, previous string, why error) (Report, error) {
	failed.Outcome = Failed
	failed.EndedAt = time.Now().UTC()
	if err := s.mark(failed); err != nil {
		return Report{}, err
	}

	rep := Report{Service: failed.Service, Host: failed.Host, Failed: &failed, RolledBack: true}
	if previous == "" {
		// Nothing has ever passed a gate, so there is no last good release to
		// go back to. The box serves nothing rather than serving the release
		// that just failed, and the report says so in as many words.
		if _, err := s.Switch(""); err != nil {
			return rep, err
		}
		rep.Outcome = OutcomeNoCurrent
		rep.Detail = fmt.Sprintf("release %s failed its health gate and there was no previous release to go back to: %s is not serving anything", failed.ID, failed.Service)
		return rep, fmt.Errorf("%s: %w: %v (no previous release on this box to roll back to)", StepGate, ErrGateFailed, why)
	}
	back, err := s.Switch(previous)
	if err != nil {
		return rep, err
	}
	rep.Release = back
	rep.Outcome = OutcomeRolled
	rep.Detail = fmt.Sprintf("release %s failed its health gate and %s was put back: %v", failed.ID, previous, why)
	return rep, fmt.Errorf("%s: %w: release %s failed (%v) and the box is serving %s again", StepGate, ErrGateFailed, failed.ID, why, previous)
}

// previousGood is the release rollback goes back to when no target is named:
// the newest release that is not the current one and actually served. It skips
// the failed and the merely staged, because going back to a release that
// never served is a deploy wearing a rollback's clothes.
func previousGood(h History) string {
	for _, r := range h.Releases {
		if r.ID == h.Current {
			continue
		}
		if r.Outcome == Failed || r.Outcome == Staged {
			continue
		}
		return r.ID
	}
	return ""
}

// certUntil renders a certificate's expiry for a message, including the case
// where there is none. "no certificate" and "expired at X" are different
// sentences a reader acts on differently.
func certUntil(h Host) string {
	if h.CertNotAfter.IsZero() {
		return "the edge holds no certificate for it"
	}
	return h.CertNotAfter.UTC().Format(time.RFC3339)
}

// Rollback flips the pointer back, and that is the entire operation. It
// pulls nothing, starts nothing and rebuilds nothing: the release directory
// is already on disk and the symlink is the only thing that changes, which is
// why a rollback takes the time a rename takes.
//
// The target is a release id; empty means "the release that was serving
// before this one", which is the question an operator is actually asking when
// they type `cafe box rollback` after a deploy they regret. LastGood is a
// different pointer with a different job: it is the release a FAILED deploy
// flips back to, recorded at the moment of the failure so that the recovery
// reads one file on local disk and nothing else. Two pointers because two
// questions: "undo this" walks back one, and "the deploy broke" jumps to the
// last one that was known good.
func (s *Store) Rollback(target string, now time.Time) (Release, Report, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	h, err := s.History()
	if err != nil {
		return Release{}, Report{}, err
	}
	current := h.Current
	if target == "" {
		target = previousGood(h)
		if target == "" {
			return Release{}, Report{}, errors.New("there is no earlier good release on this box to roll back to; name a release with `cafe box rollback <id>`")
		}
	}
	rel, ok := h.find(target)
	if !ok {
		return Release{}, Report{}, fmt.Errorf("%w: %s", ErrNoSuchRelease, target)
	}
	if target == current {
		return rel, Report{Service: rel.Service, Host: rel.Host, Release: rel, Outcome: OutcomeDeployed,
			Detail: fmt.Sprintf("release %s is already current", rel.ID)}, nil
	}
	back, err := s.Switch(target)
	if err != nil {
		return Release{}, Report{}, err
	}
	// Re-read: the switch itself rewrote the history, and mutating the copy
	// taken before it would write back a file the pointer has already moved
	// on from — which is how a box ends up with a current pointer and a
	// history that disagree about what is serving.
	h, err = s.History()
	if err != nil {
		return Release{}, Report{}, err
	}
	for i := range h.Releases {
		if h.Releases[i].ID == current {
			h.Releases[i].Outcome = RolledBack
			h.Releases[i].EndedAt = now
		}
	}
	if err := s.save(h); err != nil {
		return Release{}, Report{}, err
	}
	return back, Report{
		Service:    rel.Service,
		Host:       rel.Host,
		Release:    back,
		Previous:   &Release{ID: current, Outcome: RolledBack},
		Outcome:    OutcomeRolled,
		RolledBack: true,
		Detail:     fmt.Sprintf("release %s was put back; %s stopped serving", target, current),
	}, nil
}
