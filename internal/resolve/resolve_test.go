package resolve

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cafaye/cafe/internal/contract"
)

// fakePantry is the seam a test drives the resolver's tier 1 and tier 2
// through, without a network. It records what it was asked so a test can
// assert the token was presented and the tiers were consulted in order.
type fakePantry struct {
	project    *Project
	projectErr error
	coords     *Coordinates
	coordsErr  error

	// address is what the ADDRESS read (tier 1's second route) answers, and
	// addressErr is what it refuses with. The wrong-owner 404 is the case
	// that matters: the bare name resolves, the address does not.
	address    *Project
	addressErr error

	projectCalls  int
	coordsCalls   int
	addressCalls  int
	lastName      string
	lastOwner     string
	lastAddressBy string
	lastToken     string
}

func (f *fakePantry) Project(_ context.Context, name string) (*Project, error) {
	f.projectCalls++
	f.lastName = name
	return f.project, f.projectErr
}

// ProjectByAddress is tier 1's address read. It records the pair it was asked
// for, because the retry semantics are a claim about which read was made: a
// resolver that merged the two reads into one wrapper would pass every other
// test in this file while being unable to tell them apart.
func (f *fakePantry) ProjectByAddress(_ context.Context, owner, name string) (*Project, error) {
	f.addressCalls++
	f.lastOwner = owner
	f.lastName = name
	f.lastAddressBy = owner + "/" + name
	if f.addressErr != nil {
		return nil, f.addressErr
	}
	return f.address, nil
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

// The happy path for a free project: tier 1 confirms the name, tier 2 hands
// over the coordinates, and tier 3 is never consulted — a free service is not
// a product, so the entitlement check is not asked.
func TestAFreeProjectResolvesThroughTier1AndTier2(t *testing.T) {
	pantry := &fakePantry{
		project: &Project{Name: "gateway", Repository: PublicRepo{Visibility: "public"}},
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

// The happy path for a paid project: all three tiers, and the fetch grant is
// the proof the chain closed.
func TestAPaidProjectResolvesThroughAllThreeTiers(t *testing.T) {
	pantry := &fakePantry{
		project: &Project{Name: "identity", Repository: PublicRepo{Visibility: "private"}},
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
	// pantry's own sentence from internal/httpapi/httpapi.go's
	// `catalog.ErrNoSuchService` branch. It deliberately does not echo the
	// name, which is what keeps a 404 from being an existence oracle.
	pantry := &fakePantry{projectErr: &PantryError{Status: 404, Code: "not_found", Detail: "no official cafaye service is registered under that name"}}
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
	if res.Failure.Status != 404 {
		t.Errorf("status = %d, want 404", res.Failure.Status)
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
		project:   &Project{Name: "gateway"},
		coordsErr: &PantryError{Status: 401, Code: "unauthorized", Detail: "this operation requires a cafe token and none was presented"},
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
		project: &Project{Name: "identity"},
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
		project: &Project{Name: "identity"},
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
		project: &Project{Name: "identity"},
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

// The manifest schema makes `version` OPTIONAL; conductor's check makes it
// REQUIRED. The disagreement is settled on cafe's side rather than in a round
// trip that answers 422 and blames the reader for it, and it is a failure the
// resolver records like any other — so an OPTIONAL paid dependency with no
// range degrades instead of blocking.
func TestPaidDependencyWithoutAVersionIsNamedAndNeverAsks(t *testing.T) {
	pantry := &fakePantry{
		project: &Project{Name: "identity"},
		coords:  &Coordinates{Name: "identity"},
	}
	cond := &fakeConductor{}
	r := NewResolver(pantry, cond)

	dep := contract.Dependency{Name: "identity", Version: "", Required: true}
	report := r.Resolve(context.Background(), ".", []contract.Dependency{dep}, "cafaye_token", "11111111-1111-4111-8111-111111111111")

	res := report.Services[0]
	if res.Failure == nil {
		t.Fatalf("a paid dependency with no range resolved without a failure")
	}
	if res.Failure.Code != "version_required" {
		t.Errorf("code = %q, want version_required", res.Failure.Code)
	}
	if !strings.Contains(res.Failure.Message, "version") {
		t.Errorf("message = %q, want it to name the field", res.Failure.Message)
	}
	if cond.calls != 0 {
		t.Errorf("conductor calls = %d, want 0 (the question is unaskable without a range)", cond.calls)
	}
}

// An --account that is not a UUID is a 404 from conductor, and a 404 about a
// string the reader typed is a sentence that sends them nowhere. Caf refuses
// it with the string echoed back.
func TestAccountThatIsNotAUUIDIsNamed(t *testing.T) {
	pantry := &fakePantry{
		project: &Project{Name: "identity"},
		coords:  &Coordinates{Name: "identity"},
	}
	cond := &fakeConductor{}
	r := NewResolver(pantry, cond)

	dep := contract.Dependency{Name: "identity", Version: "^0.1.0", Required: true}
	report := r.Resolve(context.Background(), ".", []contract.Dependency{dep}, "cafaye_token", "not-a-uuid")

	res := report.Services[0]
	if res.Failure == nil || res.Failure.Code != "account_invalid" {
		t.Fatalf("failure = %+v, want an account_invalid failure", res.Failure)
	}
	if !strings.Contains(res.Failure.Message, "not-a-uuid") {
		t.Errorf("message = %q, want it to echo what was typed", res.Failure.Message)
	}
	if cond.calls != 0 {
		t.Errorf("conductor calls = %d, want 0 (the account is not an id)", cond.calls)
	}
}

// A version the schema accepts is a version conductor accepts, and the two
// patterns are the same one: ^X.Y.Z, ~X.Y.Z, >=X.Y.Z and an exact X.Y.Z. This
// table is the mirror of core's `semverRange` $defs and conductor's
// VersionRange::PATTERN, and a row added to one must be added to the other
// three — which is why it is a table rather than one example.
func TestAVersionTheSchemaAcceptsIsAVersionConductorAccepts(t *testing.T) {
	for _, v := range []string{"^0.1.0", "~0.1.0", ">=0.1.0", "0.1.0"} {
		pantry := &fakePantry{project: &Project{Name: "identity"}, coords: &Coordinates{Name: "identity"}}
		cond := &fakeConductor{grant: &FetchGrant{Artifact: "identity", VersionRange: v}}
		r := NewResolver(pantry, cond)

		dep := contract.Dependency{Name: "identity", Version: v, Required: true}
		report := r.Resolve(context.Background(), ".", []contract.Dependency{dep}, "cafaye_token", "11111111-1111-4111-8111-111111111111")

		if report.Services[0].Failure != nil {
			t.Errorf("%s: failed: %+v", v, report.Services[0].Failure)
			continue
		}
		if cond.calls != 1 {
			t.Errorf("%s: conductor calls = %d, want 1", v, cond.calls)
		}
		if cond.version != v {
			t.Errorf("%s: the range sent was %q", v, cond.version)
		}
	}
}

// A resolver with no conductor configured refuses tier 3 for a paid product
// with a named failure, after tiers 1 and 2 have succeeded.
func TestNoConductorIsANamedFailureForPaidOnly(t *testing.T) {
	pantry := &fakePantry{
		project: &Project{Name: "identity"},
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
	pantry := &fakePantry{projectErr: errors.New("dial tcp: connection refused")}
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
		project: &Project{Name: "gateway"},
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
		project:   &Project{Name: "identity"},
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

// THE AMBIGUITY RETRY. Pantry's bare-name route is documented to answer 404
// naming the addresses the day `projects_name_key` is relaxed, and until then
// the state is unreachable — which makes this test a test of a rule the client
// holds in advance rather than of a server behaviour it can observe. The
// resolver's job is the retry: an owner is known, so ask the address.
func TestAnAmbiguousBareNameRetriesByAddressWhenAnOwnerIsKnown(t *testing.T) {
	pantry := &fakePantry{
		projectErr: &PantryError{Status: 404, Code: "not_found",
			Detail: "the name `widget` is registered by more than one owner; address it as `alice/widget` or `bob/widget`"},
		address: &Project{Name: "widget", OwnerSlug: "alice"},
		coords:  &Coordinates{Name: "widget", Repository: Repository{URL: "git@github.com:alice/widget.git"}},
	}

	report := NewResolver(pantry, &fakeConductor{}).ResolveAddress(
		context.Background(), "alice", "widget", "cafaye_token", "")

	if pantry.addressCalls != 1 {
		t.Fatalf("address reads = %d, want 1 (the ambiguity retry)", pantry.addressCalls)
	}
	if pantry.lastAddressBy != "alice/widget" {
		t.Errorf("read the address %q, want alice/widget", pantry.lastAddressBy)
	}
	res := report.Services[0]
	if res.Failure != nil {
		t.Fatalf("the retry failed: %+v", res.Failure)
	}
	if res.Owner != "alice" {
		t.Errorf("owner = %q, want alice — the report names the address it resolved", res.Owner)
	}
	if res.Coordinates == nil || res.Coordinates.Repository.URL != "git@github.com:alice/widget.git" {
		t.Errorf("coordinates = %+v, want the addressed row's", res.Coordinates)
	}
}

// A 404 that is only "no such project" does NOT retry: there is nothing an
// address could resolve that the bare name did not, and a second read would be
// a round trip spent on a question the server already answered.
func TestAPlainNotFoundNeverRetriesByAddress(t *testing.T) {
	pantry := &fakePantry{
		projectErr: &PantryError{Status: 404, Code: "not_found",
			Detail: "no official cafaye project is registered under that name"},
	}

	report := NewResolver(pantry, &fakeConductor{}).ResolveAddress(
		context.Background(), "alice", "widget", "cafaye_token", "")

	if pantry.addressCalls != 0 {
		t.Errorf("address reads = %d, want 0 — a plain 404 stays a 404", pantry.addressCalls)
	}
	res := report.Services[0]
	if res.Failure == nil || res.Failure.Code != "not_found" {
		t.Fatalf("failure = %+v, want pantry's own not_found", res.Failure)
	}
	if res.Failure.Status != 404 {
		t.Errorf("status = %d, want 404", res.Failure.Status)
	}
}

// The same plain 404 with NO owner supplied: there is nobody to retry as, so
// the failure is the 404 and nothing else. This is the fleet's path — a
// manifest dependency names a bare name and the resolver asks nothing more.
func TestABareNotFoundWithoutAnOwnerDoesNotRetry(t *testing.T) {
	pantry := &fakePantry{
		projectErr: &PantryError{Status: 404, Code: "not_found",
			Detail: "no official cafaye project is registered under that name"},
	}

	report := NewResolver(pantry, &fakeConductor{}).Resolve(
		context.Background(), ".", deps("widget"), "cafaye_token", "")

	if pantry.addressCalls != 0 {
		t.Errorf("address reads = %d, want 0", pantry.addressCalls)
	}
	res := report.Services[0]
	if res.Failure == nil || res.Failure.Code != "not_found" {
		t.Fatalf("failure = %+v, want not_found", res.Failure)
	}
	if res.Owner != "" {
		t.Errorf("owner = %q, want empty — nobody addressed this", res.Owner)
	}
}

// An address that names the WRONG owner is a refusal, not a resolution. The
// bare name `widget` exists — under `bob` — so the fast path succeeds with a
// row that is not the one that was asked for. The owner_slug the row carries
// is what catches it: answering the bob row would be serving a project at an
// address that is not its own, which is the thing pantry's own handler
// refuses to do.
func TestAnAddressNamingTheWrongOwnerIsARefusal(t *testing.T) {
	pantry := &fakePantry{
		project: &Project{Name: "widget", OwnerSlug: "bob"},
		coords:  &Coordinates{Name: "widget", Repository: Repository{URL: "git@github.com:bob/widget.git"}},
		addressErr: &PantryError{Status: 404, Code: "not_found",
			Detail: "no official cafaye project is registered under the address alice/widget"},
	}

	report := NewResolver(pantry, &fakeConductor{}).ResolveAddress(
		context.Background(), "alice", "widget", "cafaye_token", "")

	res := report.Services[0]
	if res.Failure == nil {
		t.Fatalf("alice/widget resolved from bob's row; the coordinates returned are %+v", res.Coordinates)
	}
	if res.Failure.Code != "not_found" {
		t.Errorf("code = %q, want pantry's own not_found", res.Failure.Code)
	}
	if !strings.Contains(res.Failure.Message, "alice/widget") {
		t.Errorf("message = %q, want the address that was asked for", res.Failure.Message)
	}
	if res.Coordinates != nil {
		t.Errorf("coordinates = %+v, want none — the refused address resolved nothing", res.Coordinates)
	}
}

// The address that IS right resolves through the same tiers, and the fast path
// is enough to prove it: the bare name is globally unique, so a row whose
// owner_slug matches the address that was asked for is the row the address
// read would have returned.
func TestAnAddressThatMatchesResolvesWithoutASecondRead(t *testing.T) {
	pantry := &fakePantry{
		project: &Project{Name: "widget", OwnerSlug: "alice"},
		coords:  &Coordinates{Name: "widget", Repository: Repository{URL: "git@github.com:alice/widget.git"}},
	}

	report := NewResolver(pantry, &fakeConductor{}).ResolveAddress(
		context.Background(), "alice", "widget", "cafaye_token", "")

	res := report.Services[0]
	if res.Failure != nil {
		t.Fatalf("failed: %+v", res.Failure)
	}
	if pantry.addressCalls != 0 {
		t.Errorf("address reads = %d, want 0 — the fast path answered and the owner matched", pantry.addressCalls)
	}
	if res.Owner != "alice" || res.Name != "widget" {
		t.Errorf("resolution = %s/%s, want alice/widget", res.Owner, res.Name)
	}
}

// A paid product addressed by a community owner still reaches tier 3 — the
// product vocabulary is about the bare name, not about who owns it — and it
// hits the same range requirement a manifest dependency does: the address form
// carries no version constraint, so conductor is never asked a question it
// would answer 422. The failure names what a manifest would have supplied.
func TestAnAddressedPaidProductStillNeedsARange(t *testing.T) {
	pantry := &fakePantry{
		project: &Project{Name: "identity", OwnerSlug: "alice"},
		coords:  &Coordinates{Name: "identity"},
	}
	cond := &fakeConductor{}

	report := NewResolver(pantry, cond).ResolveAddress(
		context.Background(), "alice", "identity", "cafaye_token", "11111111-1111-4111-8111-111111111111")

	res := report.Services[0]
	if res.Failure == nil || res.Failure.Code != "version_required" {
		t.Fatalf("failure = %+v, want the named version_required refusal", res.Failure)
	}
	if res.Failure.Tier != 3 {
		t.Errorf("tier = %d, want 3 — the entitlement check is where the range is needed", res.Failure.Tier)
	}
	if cond.calls != 0 {
		t.Errorf("conductor calls = %d, want 0 — an unbounded request is a 422 nobody should spend", cond.calls)
	}
	if res.Coordinates == nil {
		t.Errorf("the refusal destroyed the coordinates the first two tiers answered")
	}
}

// The code is matched beside the detail, because the day pantry names the
// collision in a `code` the sentence may say anything at all — and a detector
// that only read the prose would silently stop matching on the very release
// that made the state reachable.
func TestAnAmbiguitySpelledInTheCodeIsStillAmbiguity(t *testing.T) {
	pantry := &fakePantry{
		projectErr: &PantryError{Status: 404, Code: "name_ambiguous",
			Detail: "no official cafaye project is registered under that name"},
		address: &Project{Name: "widget", OwnerSlug: "alice"},
		coords:  &Coordinates{Name: "widget"},
	}

	report := NewResolver(pantry, &fakeConductor{}).ResolveAddress(
		context.Background(), "alice", "widget", "cafaye_token", "")

	if pantry.addressCalls != 1 {
		t.Fatalf("address reads = %d, want 1 — the code is the signal", pantry.addressCalls)
	}
	if report.Services[0].Failure != nil {
		t.Fatalf("failed: %+v", report.Services[0].Failure)
	}
}
