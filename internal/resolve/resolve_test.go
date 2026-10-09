package resolve

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cafaye/caf/internal/contract"
)

// fakePantry is the seam a test drives the resolver's tier 1 and tier 2
// through, without a network. It records what it was asked so a test can
// assert the token was presented and the tiers were consulted in order.
type fakePantry struct {
	service    *Service
	serviceErr error
	coords     *Coordinates
	coordsErr  error

	serviceCalls int
	coordsCalls  int
	lastName     string
	lastToken    string
}

func (f *fakePantry) Service(_ context.Context, name string) (*Service, error) {
	f.serviceCalls++
	f.lastName = name
	return f.service, f.serviceErr
}

func (f *fakePantry) Coordinates(_ context.Context, name, token string) (*Coordinates, error) {
	f.coordsCalls++
	f.lastName = name
	f.lastToken = token
	return f.coords, f.coordsErr
}

// fakeConductor is the seam a test drives tier 3 through. It records the
// triple the check was asked about, so a test can assert the account, the
// artifact and the version range reached conductor unchanged.
type fakeConductor struct {
	grant    *FetchGrant
	err      error
	calls    int
	account  string
	artifact string
	version  string
	token    string
}

func (f *fakeConductor) ArtifactFetch(_ context.Context, accountID, artifact, versionRange, token string) (*FetchGrant, error) {
	f.calls++
	f.account = accountID
	f.artifact = artifact
	f.version = versionRange
	f.token = token
	return f.grant, f.err
}

// deps is a shorthand for the dependency list a test resolves.
func deps(names ...string) []contract.Dependency {
	out := make([]contract.Dependency, 0, len(names))
	for _, n := range names {
		out = append(out, contract.Dependency{Name: n, Version: "^0.1.0", Required: true})
	}
	return out
}

// The happy path for a free service: tier 1 confirms the name, tier 2 hands
// over the coordinates, and tier 3 is never consulted — a free service is not
// a product, so the entitlement check is not asked.
func TestFreeServiceResolvesThroughTier1AndTier2(t *testing.T) {
	pantry := &fakePantry{
		service: &Service{Name: "gateway", Visibility: "public"},
		coords:  &Coordinates{Name: "gateway", Repository: Repository{URL: "git@github.com:cafaye/gateway.git", DefaultBranch: "master"}},
	}
	cond := &fakeConductor{}
	r := NewResolver(pantry, cond)

	report := r.Resolve(context.Background(), ".", deps("gateway"), "cafaye_token", "")

	if len(report.Services) != 1 {
		t.Fatalf("resolved %d services, want 1", len(report.Services))
	}
	res := report.Services[0]
	if res.Failure != nil {
		t.Fatalf("free service failed: %+v", res.Failure)
	}
	if res.Fetch != nil {
		t.Errorf("a free service reached tier 3 and got a fetch grant")
	}
	if res.Coordinates == nil {
		t.Fatalf("free service resolved without coordinates")
	}
	if res.Coordinates.Repository.URL != "git@github.com:cafaye/gateway.git" {
		t.Errorf("repository = %q", res.Coordinates.Repository.URL)
	}
	if cond.calls != 0 {
		t.Errorf("conductor calls = %d, want 0 (a free service never reaches tier 3)", cond.calls)
	}
	if pantry.lastToken != "cafaye_token" {
		t.Errorf("tier 2 token = %q, want the caller's token", pantry.lastToken)
	}
}

// The happy path for a paid service: all three tiers, and the fetch grant is
// the proof the chain closed.
func TestPaidServiceResolvesThroughAllThreeTiers(t *testing.T) {
	pantry := &fakePantry{
		service: &Service{Name: "identity", Visibility: "private"},
		coords:  &Coordinates{Name: "identity", Repository: Repository{URL: "git@github.com:cafaye/identity.git", DefaultBranch: "master"}},
	}
	cond := &fakeConductor{grant: &FetchGrant{
		Purpose:      "artifact.fetch",
		AccountID:    "11111111-1111-4111-8111-111111111111",
		Artifact:     "identity",
		VersionRange: "^0.1.0",
		ExpiresAt:    "2026-10-09T16:00:00Z",
	}}
	r := NewResolver(pantry, cond)

	report := r.Resolve(context.Background(), ".", deps("identity"), "cafaye_token", "11111111-1111-4111-8111-111111111111")

	res := report.Services[0]
	if res.Failure != nil {
		t.Fatalf("paid service failed: %+v", res.Failure)
	}
	if res.Fetch == nil {
		t.Fatalf("paid service resolved without a fetch grant")
	}
	if res.Fetch.Purpose != "artifact.fetch" {
		t.Errorf("purpose = %q", res.Fetch.Purpose)
	}
	if res.Fetch.VersionRange != "^0.1.0" {
		t.Errorf("version range = %q", res.Fetch.VersionRange)
	}
	if cond.calls != 1 {
		t.Errorf("conductor calls = %d, want 1", cond.calls)
	}
	if cond.account != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("account = %q", cond.account)
	}
	if cond.artifact != "identity" {
		t.Errorf("artifact = %q", cond.artifact)
	}
	if cond.version != "^0.1.0" {
		t.Errorf("version range = %q", cond.version)
	}
	if cond.token != "cafaye_token" {
		t.Errorf("tier 3 token = %q, want the caller's token", cond.token)
	}
}

// A name that is not in the catalog is a named tier-1 failure, and the walk
// stops there: no coordinates are fetched and no entitlement is checked,
// because a service that does not exist has neither.
func TestUnknownNameIsANamedTier1Failure(t *testing.T) {
	pantry := &fakePantry{serviceErr: &PantryError{Status: 404, Code: "not_found", Detail: "no official cafaye service is registered under that name"}}
	cond := &fakeConductor{}
	r := NewResolver(pantry, cond)

	report := r.Resolve(context.Background(), ".", deps("nope"), "cafaye_token", "")

	res := report.Services[0]
	if res.Failure == nil {
		t.Fatalf("an unknown name resolved without a failure")
	}
	if res.Failure.Tier != 1 {
		t.Errorf("tier = %d, want 1", res.Failure.Tier)
	}
	if res.Failure.Code != "not_found" {
		t.Errorf("code = %q, want not_found", res.Failure.Code)
	}
	if !strings.Contains(res.Failure.Message, "no official cafaye service") {
		t.Errorf("message = %q", res.Failure.Message)
	}
	if res.Coordinates != nil {
		t.Errorf("an unknown name produced coordinates")
	}
	if cond.calls != 0 {
		t.Errorf("conductor calls = %d, want 0 (tier 1 failed first)", cond.calls)
	}
}

// A caller with no credential is refused at tier 2 with pantry's own detail,
// and the walk stops before tier 3.
func TestNoCredentialIsANamedTier2Failure(t *testing.T) {
	pantry := &fakePantry{
		service:   &Service{Name: "gateway"},
		coordsErr: &PantryError{Status: 401, Code: "unauthorized", Detail: "this operation requires a caf token and none was presented"},
	}
	r := NewResolver(pantry, &fakeConductor{})

	report := r.Resolve(context.Background(), ".", deps("gateway"), "", "")

	res := report.Services[0]
	if res.Failure == nil {
		t.Fatalf("a caller with no credential resolved without a failure")
	}
	if res.Failure.Tier != 2 {
		t.Errorf("tier = %d, want 2", res.Failure.Tier)
	}
	if res.Failure.Code != "unauthorized" {
		t.Errorf("code = %q, want unauthorized", res.Failure.Code)
	}
}

// The named failure the brief leads with: an entitled product the account
// does not hold. The code is conductor's own, the message names the product
// and where to start one, and the test asserts both.
func TestEntitlementRequiredIsNamedWithProductAndRemedy(t *testing.T) {
	pantry := &fakePantry{
		service: &Service{Name: "identity"},
		coords:  &Coordinates{Name: "identity", Repository: Repository{URL: "git@github.com:cafaye/identity.git"}},
	}
	cond := &fakeConductor{err: &ConductorError{Status: 403, Code: "entitlement_required",
		Message: "product `identity` needs an entitlement — start one at https://cafaye.com"}}
	r := NewResolver(pantry, cond)

	report := r.Resolve(context.Background(), ".", deps("identity"), "cafaye_token", "11111111-1111-4111-8111-111111111111")

	res := report.Services[0]
	if res.Failure == nil {
		t.Fatalf("an unentitled product resolved without a failure")
	}
	if res.Failure.Code != "entitlement_required" {
		t.Errorf("code = %q, want entitlement_required", res.Failure.Code)
	}
	if !strings.Contains(res.Failure.Message, "`identity`") {
		t.Errorf("message = %q, want it to name the product", res.Failure.Message)
	}
	if !strings.Contains(res.Failure.Message, "https://cafaye.com") {
		t.Errorf("message = %q, want it to say where to start one", res.Failure.Message)
	}
	// Tier 2 succeeded — the coordinates are real — and tier 3 is what failed.
	if res.Coordinates == nil {
		t.Errorf("tier 2 succeeded but the coordinates are not reported")
	}
}

// The second named failure: an entitlement that does not cover the range the
// project asks for. Both ranges are spelled out, and the test asserts both.
func TestVersionNotGrantedNamesBothRanges(t *testing.T) {
	pantry := &fakePantry{
		service: &Service{Name: "identity"},
		coords:  &Coordinates{Name: "identity"},
	}
	cond := &fakeConductor{err: &ConductorError{Status: 403, Code: "version_not_granted",
		Message: "your entitlement covers `^0.2.0`, this project asks for `^0.1.0`"}}
	r := NewResolver(pantry, cond)

	report := r.Resolve(context.Background(), ".", deps("identity"), "cafaye_token", "11111111-1111-4111-8111-111111111111")

	res := report.Services[0]
	if res.Failure == nil {
		t.Fatalf("a non-covering entitlement resolved without a failure")
	}
	if res.Failure.Code != "version_not_granted" {
		t.Errorf("code = %q, want version_not_granted", res.Failure.Code)
	}
	if !strings.Contains(res.Failure.Message, "`^0.2.0`") {
		t.Errorf("message = %q, want it to spell out the granted range", res.Failure.Message)
	}
	if !strings.Contains(res.Failure.Message, "`^0.1.0`") {
		t.Errorf("message = %q, want it to spell out the requested range", res.Failure.Message)
	}
}

// A paid dependency with no account named is a caf-side failure, and the
// remedy is the flag that names it.
func TestPaidDependencyWithoutAccountIsNamed(t *testing.T) {
	pantry := &fakePantry{
		service: &Service{Name: "identity"},
		coords:  &Coordinates{Name: "identity"},
	}
	r := NewResolver(pantry, &fakeConductor{})

	report := r.Resolve(context.Background(), ".", deps("identity"), "cafaye_token", "")

	res := report.Services[0]
	if res.Failure == nil {
		t.Fatalf("a paid dependency with no account resolved without a failure")
	}
	if res.Failure.Code != "account_required" {
		t.Errorf("code = %q, want account_required", res.Failure.Code)
	}
	if !strings.Contains(res.Failure.Message, "--account") {
		t.Errorf("message = %q, want it to name the remedy", res.Failure.Message)
	}
}

// A resolver with no pantry configured refuses tier 1 with a named failure
// rather than panicking — a resolver that cannot read the catalog says so.
func TestNoPantryIsANamedFailure(t *testing.T) {
	r := NewResolver(nil, &fakeConductor{})

	report := r.Resolve(context.Background(), ".", deps("gateway"), "cafaye_token", "")

	res := report.Services[0]
	if res.Failure == nil || res.Failure.Code != "no_catalog" {
		t.Fatalf("failure = %+v, want a no_catalog failure", res.Failure)
	}
}

// A resolver with no conductor configured refuses tier 3 for a paid product
// with a named failure, after tiers 1 and 2 have succeeded.
func TestNoConductorIsANamedFailureForPaidOnly(t *testing.T) {
	pantry := &fakePantry{
		service: &Service{Name: "identity"},
		coords:  &Coordinates{Name: "identity"},
	}
	r := NewResolver(pantry, nil)

	report := r.Resolve(context.Background(), ".", deps("identity"), "cafaye_token", "11111111-1111-4111-8111-111111111111")

	res := report.Services[0]
	if res.Failure == nil || res.Failure.Code != "no_conductor" {
		t.Fatalf("failure = %+v, want a no_conductor failure", res.Failure)
	}
	if res.Coordinates == nil {
		t.Errorf("tiers 1 and 2 succeeded but the coordinates are not reported")
	}
}

// A transport failure is a named failure, not a panic and not a silent skip:
// the resolver reports it and the dependency is recorded as failed.
func TestTransportFailureIsNamed(t *testing.T) {
	pantry := &fakePantry{serviceErr: errors.New("dial tcp: connection refused")}
	r := NewResolver(pantry, &fakeConductor{})

	report := r.Resolve(context.Background(), ".", deps("gateway"), "cafaye_token", "")

	res := report.Services[0]
	if res.Failure == nil {
		t.Fatalf("a transport failure resolved without a failure")
	}
	if res.Failure.Code != "resolution_failed" {
		t.Errorf("code = %q, want resolution_failed", res.Failure.Code)
	}
	if !strings.Contains(res.Failure.Message, "connection refused") {
		t.Errorf("message = %q, want the cause", res.Failure.Message)
	}
}

// Every dependency is resolved independently: one failure does not stop the
// walk for the others, so a project sees all its named failures at once.
func TestEveryDependencyIsResolvedIndependently(t *testing.T) {
	pantry := &fakePantry{
		service: &Service{Name: "gateway"},
		coords:  &Coordinates{Name: "gateway"},
	}
	cond := &fakeConductor{err: &ConductorError{Status: 403, Code: "entitlement_required", Message: "product `identity` needs an entitlement"}}
	r := NewResolver(pantry, cond)

	report := r.Resolve(context.Background(), ".", deps("gateway", "identity"), "cafaye_token", "11111111-1111-4111-8111-111111111111")

	if len(report.Services) != 2 {
		t.Fatalf("resolved %d services, want 2", len(report.Services))
	}
	if report.Services[0].Failure != nil {
		t.Errorf("gateway failed: %+v", report.Services[0].Failure)
	}
	if report.Services[1].Failure == nil {
		t.Errorf("identity did not fail")
	}
}

// The required flag travels with the resolution so the caller can decide
// warn-vs-block. The resolver does not decide it — conductor never sees it.
func TestRequiredFlagTravelsWithTheResolution(t *testing.T) {
	pantry := &fakePantry{
		service:   &Service{Name: "identity"},
		coordsErr: &PantryError{Status: 401, Code: "unauthorized", Detail: "no token"},
	}
	r := NewResolver(pantry, &fakeConductor{})

	dep := contract.Dependency{Name: "identity", Version: "^0.1.0", Required: false}
	report := r.Resolve(context.Background(), ".", []contract.Dependency{dep}, "", "")

	res := report.Services[0]
	if res.Required {
		t.Errorf("required = true, want false (the schema's soft dependency)")
	}
	if res.Failure == nil {
		t.Errorf("a soft dependency still records its failure — the caller decides what to do with it")
	}
}
