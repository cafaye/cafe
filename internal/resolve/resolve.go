package resolve

import (
	"context"
	"errors"
	"strings"

	"github.com/cafaye/caf/internal/contract"
)

// Products is the vocabulary conductor's entitlement check accepts
// (Entitlement::PRODUCTS): the paid units, D8's prices. It is repeated here
// because the resolver has to decide which dependencies reach tier 3, and the
// only way to decide is to know the list — conductor exposes no "what are the
// products" operation, so a client that did not carry the list would have to
// ask the tier-3 question about every dependency and read the 422, which is a
// round trip that answers a question the server already answered at boot.
//
// The drift risk is real and named: a product added to conductor and not here
// resolves as a free service (tier 1/2 only, no entitlement check) — the safe
// direction, because the alternative is a product that never resolves. The
// mirror of this list lives in conductor's own tests; a change to either is a
// change to both.
var Products = []string{"kit", "platform", "identity", "courier"}

// Coordinates is tier 2: where a service's code lives. It is the whole of what
// pantry's coordinates operation serves — the name echoed from the path and
// the repository — because that is the whole of what tier 2 reveals.
//
// The shape is pantry's wire shape, nested repository and all: a client that
// flattened it into `repository_url` would be a second copy of the document
// that could drift from the one pantry serves.
type Coordinates struct {
	// Name is the cafaye namespace name, echoed from the path.
	Name string `json:"name"`
	// Repository is the service's repository — the one thing tier 2 reveals
	// that tier 1 does not.
	Repository Repository `json:"repository"`
}

// Repository is where a service's code lives. core's schema rejects an HTTPS
// remote for a cafaye repository, so `url` is an SSH remote or not a cafaye
// repository.
type Repository struct {
	// URL is the SSH remote.
	URL string `json:"url"`
	// DefaultBranch is the repository's default branch. core pins it to
	// `master`, so it is a fact rather than a choice.
	DefaultBranch string `json:"defaultBranch"`
}

// FetchGrant is tier 3's deliverable: the artifact.fetch grant that redeeming
// the minted capability returns. It is the proof that the entitlement check
// passed and the capability was real and spendable — the chain from capability
// to artifact, closed.
//
// It carries the capability's SCOPE and never its token. The token is a
// bearer credential, shown once by conductor and spent by the redemption; the
// grant is what a fetch is authorised by, and it is a fact about the
// entitlement rather than a secret.
type FetchGrant struct {
	// Purpose is always "artifact.fetch".
	Purpose string `json:"purpose"`
	// AccountID is the account the grant is bound to.
	AccountID string `json:"account_id"`
	// Artifact is the product the grant covers.
	Artifact string `json:"artifact"`
	// VersionRange is the cafaye semver constraint the grant is scoped to.
	VersionRange string `json:"version_range"`
	// ExpiresAt is RFC3339, the capability's minutes-lived expiry.
	ExpiresAt string `json:"expires_at"`
}

// Failure is one named, actionable resolution failure. It carries the server's
// own code and detail rather than a paraphrase, because the detail is the
// actionable half: "product `identity` needs an entitlement — start one at
// https://cafaye.com" is a sentence a reader can act on, and "forbidden" is a
// sentence that sends them to a log.
//
// Tier says which tier failed (1 listing, 2 metadata, 3 artifact), so a
// reader can tell "the name is wrong" from "you are not entitled" without
// parsing the code.
type Failure struct {
	// Tier is the tier that failed: 1, 2 or 3.
	Tier int `json:"tier"`
	// Code is the server's own error code: not_found, unauthorized,
	// unavailable, entitlement_required, version_not_granted, … It is
	// surfaced verbatim so a grep for the code finds both this report and
	// the server's log.
	Code string `json:"code"`
	// Message is the server's detail, verbatim. For a caf-side failure (a
	// missing account, a transport error) it is caf's own sentence.
	Message string `json:"message"`
}

// Resolution is one dependency's resolution: what was asked, what was found,
// and the named failure if it did not resolve.
//
// Exactly one of Coordinates/Fetch/Failure is the interesting field. A
// free service that resolves has Coordinates and no Fetch and no Failure. A
// paid service that resolves has all three. A dependency that fails has a
// Failure and whatever the tiers before the failure produced.
type Resolution struct {
	// Name is the dependency's cafaye namespace name.
	Name string `json:"name"`
	// Version is the constraint on the dependency's own contract version, as
	// the manifest declared it. Empty means the manifest declared no
	// constraint, which the schema permits.
	Version string `json:"version"`
	// Required is the schema's `required` field with its default applied:
	// absent and true are the same thing, because a JSON Schema default is
	// an annotation no validator applies.
	Required bool `json:"required"`
	// Coordinates is tier 2's answer, or nil if tier 2 was not reached or
	// failed.
	Coordinates *Coordinates `json:"coordinates,omitempty"`
	// Fetch is tier 3's answer for a paid product, or nil for a free service
	// or a dependency that did not reach tier 3.
	Fetch *FetchGrant `json:"fetch,omitempty"`
	// Failure is the named failure, or nil if the dependency resolved.
	Failure *Failure `json:"failure,omitempty"`
}

// Summary is the human half of a resolved dependency: which tiers answered,
// in one clause for the stderr line a person reads. It is empty for a
// dependency that failed — the failure's own sentence says what happened.
func (r Resolution) Summary() string {
	switch {
	case r.Failure != nil:
		return ""
	case r.Fetch != nil:
		return "tiers 1-3, fetch grant until " + r.Fetch.ExpiresAt
	case r.Coordinates != nil:
		return "tiers 1-2, coordinates " + r.Coordinates.Repository.URL
	default:
		return "tier 1"
	}
}

// Report is the whole project's resolution: the project directory and one
// Resolution per declared dependency, in the manifest's order.
type Report struct {
	// Project is the directory the manifest was read from, as the caller
	// named it.
	Project string `json:"project"`
	// Services is one Resolution per dependency, in declaration order.
	Services []Resolution `json:"services"`
}

// Pantry is the tiered catalog this resolver consumes (W4-P1). It is an
// interface so a test can drive a resolution without a network — the same
// seam internal/provider uses for conductor, for the same reason: a test that
// needs a server to prove the client is a test that cannot run on a bare CI
// runner.
type Pantry interface {
	// Service is tier 1: one service's listing entry, anonymous. A 404 is
	// an error carrying the not_found code.
	Service(ctx context.Context, name string) (*Service, error)
	// Coordinates is tier 2: one service's repository coordinates, gated on
	// the caller's caf token. The token is presented, never stored.
	Coordinates(ctx context.Context, name, token string) (*Coordinates, error)
}

// Conductor is the entitlement check this resolver consumes (W4-P2). One
// method because the resolver's tier-3 question is one question — "does this
// account hold this product at this range, and may I fetch it?" — whose
// answer is the fetch grant.
type Conductor interface {
	// ArtifactFetch is tier 3: the entitlement check, the mint of the
	// artifact.fetch capability, and its redemption, in one call. The
	// returned grant is the proof; the capability token is spent and never
	// leaves this method.
	ArtifactFetch(ctx context.Context, accountID, artifact, versionRange, token string) (*FetchGrant, error)
}

// Resolver resolves a project's dependencies through the tiers. It holds the
// two clients and nothing else: it is a pure function of the manifest, the
// caller's token and the two services, which is what makes it testable.
type Resolver struct {
	pantry    Pantry
	conductor Conductor
}

// NewResolver builds a Resolver over the two clients. Either may be nil, in
// which case the corresponding tier is refused with a named failure rather
// than a panic — a resolver with no conductor configured is a resolver that
// reports "no conductor" for every paid dependency, which is the honest
// answer.
func NewResolver(pantry Pantry, conductor Conductor) *Resolver {
	return &Resolver{pantry: pantry, conductor: conductor}
}

// Resolve walks every declared dependency through the tiers and returns the
// whole report. It never returns an error: a per-dependency failure is a
// Failure in the Report, not an error, because the question "does this
// project resolve?" is answered by the report as a whole — a project with one
// unentitled optional dependency resolves, and that is a fact the report
// carries and an error would destroy.
//
// The token is the caller's caf credential, read from the keyring by the
// caller. It is presented to tier 2 and tier 3 and kept for the length of
// exactly this call. Empty is a valid value: tier 1 is anonymous, and tier 2
// and tier 3 answer a named unauthorized, which is the honest response to a
// caller with no credential.
//
// accountID names the caf account the tier-3 check runs for. It is required
// only when a paid product is encountered; see resolveOne.
func (r *Resolver) Resolve(ctx context.Context, project string, deps []contract.Dependency, token, accountID string) Report {
	report := Report{Project: project, Services: make([]Resolution, 0, len(deps))}
	for _, dep := range deps {
		report.Services = append(report.Services, r.resolveOne(ctx, dep, token, accountID))
	}
	return report
}

// resolveOne walks one dependency through the tiers in order and stops at the
// first failure. The order is the semantics: a name that does not exist has no
// coordinates to fetch and no entitlement to check, so a tier-1 failure ends
// the walk; a caller with no credential cannot read coordinates, so a tier-2
// failure ends the walk before tier 3.
func (r *Resolver) resolveOne(ctx context.Context, dep contract.Dependency, token, accountID string) Resolution {
	res := Resolution{Name: dep.Name, Version: dep.Version, Required: dep.Required}

	// Tier 1: the name exists. Anonymous — advertising is free (D5).
	if r.pantry == nil {
		res.Failure = &Failure{Tier: 1, Code: "no_catalog", Message: "no pantry is configured, so the catalog cannot be read"}
		return res
	}
	if _, err := r.pantry.Service(ctx, dep.Name); err != nil {
		res.Failure = failureFrom(1, err)
		return res
	}

	// Tier 2: the coordinates, for a caller presenting a caf token.
	coords, err := r.pantry.Coordinates(ctx, dep.Name, token)
	if err != nil {
		res.Failure = failureFrom(2, err)
		return res
	}
	res.Coordinates = coords

	// Tier 3: the entitlement check, for a paid product only. A free service
	// never reaches this tier — it is not a product, and conductor's check
	// would refuse it with 422.
	if !r.isProduct(dep.Name) {
		return res
	}
	if strings.TrimSpace(accountID) == "" {
		res.Failure = &Failure{Tier: 3, Code: "account_required",
			Message: "dependency `" + dep.Name + "` is a paid product, and the entitlement check runs against an account; pass --account <uuid> with the account that holds it"}
		return res
	}
	if r.conductor == nil {
		res.Failure = &Failure{Tier: 3, Code: "no_conductor", Message: "no conductor is configured, so the entitlement check cannot be asked"}
		return res
	}
	grant, err := r.conductor.ArtifactFetch(ctx, accountID, dep.Name, dep.Version, token)
	if err != nil {
		res.Failure = failureFrom(3, err)
		return res
	}
	res.Fetch = grant
	return res
}

// isProduct reports whether a service name is one of conductor's paid products,
// and therefore a dependency that must clear the tier-3 entitlement check.
func (r *Resolver) isProduct(name string) bool {
	for _, p := range Products {
		if p == name {
			return true
		}
	}
	return false
}

// failureFrom maps a client error to a Failure, surfacing the server's own
// code and detail. An error that is neither a PantryError nor a ConductorError
// is a transport failure or a caf-side refusal, and it becomes a failure whose
// code names that, so a network outage is never reported as "the service
// said no".
func failureFrom(tier int, err error) *Failure {
	var pe *PantryError
	if errors.As(err, &pe) {
		return &Failure{Tier: tier, Code: pe.Code, Message: pe.Detail}
	}
	var ce *ConductorError
	if errors.As(err, &ce) {
		return &Failure{Tier: tier, Code: ce.Code, Message: ce.Message}
	}
	return &Failure{Tier: tier, Code: "resolution_failed", Message: err.Error()}
}
