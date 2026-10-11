package publish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cafaye/cafe/internal/contract"
	"github.com/cafaye/cafe/internal/resolve"
)

// publishTestManifest parses one cafaye.yml the way the command does, so the
// tests exercise the same contract.Manifest the command will hand ProposalFor.
func publishTestManifest(t *testing.T, body string) contract.Manifest {
	t.Helper()
	manifest, err := contract.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse the manifest: %v", err)
	}
	return manifest
}

// A manifest with a full contract surface: HTTP, events out, events in.
const publishFullManifest = `name: app
description: An application.
language: go
core: ^0.2.0
exposes:
  api: openapi/v1.yaml
  events:
    - app.user.created
consumes:
  - gateway.request.received
dependencies:
  - name: gateway
    version: ^0.1.0
  - name: identity
    version: ^0.1.0
    required: false
repository:
  url: git@github.com:cafaye/app.git
  defaultBranch: master
  visibility: public
owner:
  team: app
  contact: app@cafaye.com
`

// The kind is derived from the contract surface, and the derivation is pantry's
// ProjectKind table: api, both, worker — and the curated case is refused with
// the flag that fills it, because a manifest with no surface cannot say what it
// is.
func TestTheKindIsDerivedFromTheContractSurface(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		want     string
	}{
		{"http only", "name: app\nlanguage: go\ncore: ^0.2.0\nexposes:\n  api: openapi/v1.yaml\n", KindAPI},
		{"http and events in", "name: app\nlanguage: go\ncore: ^0.2.0\nexposes:\n  api: openapi/v1.yaml\nconsumes:\n  - gateway.x.y\n", KindBoth},
		{"events in only", "name: app\nlanguage: go\ncore: ^0.2.0\nconsumes:\n  - gateway.x.y\n", KindWorker},
		{"events out only", "name: app\nlanguage: go\ncore: ^0.2.0\nexposes:\n  events:\n    - app.x.y\n", KindWorker},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proposal, err := ProposalFor(publishTestManifest(t, tc.manifest), "v1.0.0", "")
			if err != nil {
				t.Fatalf("ProposalFor: %v", err)
			}
			if proposal.Registration.Kind != tc.want {
				t.Errorf("kind = %q, want %q", proposal.Registration.Kind, tc.want)
			}
		})
	}
}

// The curated case: no contract surface at all. cafe cannot derive the kind, and
// the refusal names the flag rather than guessing — a guessed kind is a claim
// the publisher never made, and the registry stores claims.
func TestAManifestWithNoSurfaceNamesTheKindFlag(t *testing.T) {
	_, err := ProposalFor(publishTestManifest(t, "name: app\nlanguage: go\ncore: ^0.2.0\n"), "v1.0.0", "")
	if err == nil {
		t.Fatal("a manifest with no contract surface proposed a kind")
	}
	if !strings.Contains(err.Error(), "--kind") {
		t.Errorf("the refusal does not name --kind:\n%s", err)
	}
	// And the flag fills it: the proposal carries what the publisher stated.
	proposal, err := ProposalFor(publishTestManifest(t, "name: app\nlanguage: go\ncore: ^0.2.0\n"), "v1.0.0", KindCLI)
	if err != nil {
		t.Fatalf("ProposalFor with --kind: %v", err)
	}
	if proposal.Registration.Kind != KindCLI {
		t.Errorf("kind = %q, want %q", proposal.Registration.Kind, KindCLI)
	}
}

// `spec` is core's language for specification-only repositories, and it is
// deliberately absent from the registry's vocabulary: a specification is not a
// project. The refusal is caf-side, because a 400 from the server would blame
// the publisher for a rule the manifest schema already states.
func TestLanguageSpecIsRefused(t *testing.T) {
	_, err := ProposalFor(publishTestManifest(t, "name: app\nlanguage: spec\ncore: ^0.2.0\n"), "v1.0.0", "")
	if err == nil {
		t.Fatal("a specification repository was proposed for registration")
	}
	if !strings.Contains(err.Error(), "spec") {
		t.Errorf("the refusal does not name the language:\n%s", err)
	}
}

// A dependency with no version cannot become an edge: the registry resolves a
// range against the target's releases at write time, and an unbounded edge is
// refused there. cafe refuses here, naming the field to add, rather than
// sending a request whose only possible answer is a 400.
func TestADependencyWithoutAVersionIsRefused(t *testing.T) {
	withUnbounded := strings.Replace(publishFullManifest,
		"  - name: identity\n    version: ^0.1.0\n    required: false",
		"  - name: identity\n    required: false", 1)
	_, err := ProposalFor(publishTestManifest(t, withUnbounded), "v1.0.0", "")
	if err == nil {
		t.Fatal("an unbounded dependency proposed an edge")
	}
	if !strings.Contains(err.Error(), "version") || !strings.Contains(err.Error(), "identity") {
		t.Errorf("the refusal does not name the dependency and the field:\n%s", err)
	}
}

// The manifest is the claim and the hash is the anchor: the registration
// carries the manifest as JSON, and manifest_sha256 is the SHA-256 of the exact
// bytes the registry will re-encode to check it. The test recomputes the
// server's check — decode the map, marshal it, hash it — so a client whose
// normalization diverges from the server's is caught here rather than as a
// 400 that blames the publisher.
func TestTheHashIsOfTheBytesTheRegistryChecks(t *testing.T) {
	proposal, err := ProposalFor(publishTestManifest(t, publishFullManifest), "v1.0.0", "")
	if err != nil {
		t.Fatalf("ProposalFor: %v", err)
	}
	encoded, err := json.Marshal(proposal.Registration.Manifest)
	if err != nil {
		t.Fatalf("re-encode the manifest: %v", err)
	}
	sum := sha256.Sum256(encoded)
	if got := hex.EncodeToString(sum[:]); got != proposal.Registration.ManifestSHA256 {
		t.Errorf("manifest_sha256 = %s, want the SHA-256 of the re-encoded document (%s)",
			proposal.Registration.ManifestSHA256, got)
	}
}

// The registration carries the manifest's facts: the name, the language, the
// core constraint, the repository coordinates, the owner. A field the
// registration dropped is a claim the publisher made that the catalog never
// heard.
func TestTheRegistrationCarriesTheManifestsFacts(t *testing.T) {
	proposal, err := ProposalFor(publishTestManifest(t, publishFullManifest), "v1.0.0", "")
	if err != nil {
		t.Fatalf("ProposalFor: %v", err)
	}
	reg := proposal.Registration
	for _, tc := range []struct{ name, got, want string }{
		{"name", reg.Name, "app"},
		{"language", reg.Language, "go"},
		{"core", reg.Core, "^0.2.0"},
		{"repository url", reg.Repository.URL, "git@github.com:cafaye/app.git"},
		{"default branch", reg.Repository.DefaultBranch, "master"},
		{"owner team", reg.Owner.Team, "app"},
		{"owner contact", reg.Owner.Contact, "app@cafaye.com"},
		{"state", reg.State, StatePublished},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	if reg.Kind != KindBoth {
		t.Errorf("kind = %q, want both (http and events in)", reg.Kind)
	}
	if reg.Exposes == nil || reg.Exposes.API != "openapi/v1.yaml" {
		t.Errorf("exposes.api = %+v, want openapi/v1.yaml", reg.Exposes)
	}
	if len(reg.Exposes.Events) != 1 || reg.Exposes.Events[0] != "app.user.created" {
		t.Errorf("exposes.events = %v, want the manifest's events", reg.Exposes.Events)
	}
	if len(reg.Consumes) != 1 || reg.Consumes[0] != "gateway.request.received" {
		t.Errorf("consumes = %v, want the manifest's consumes", reg.Consumes)
	}
}

// The edges are the manifest's dependencies: the target, the range verbatim,
// and the dependency class — `required: false` is the soft edge, the registry's
// own word for a project that starts degraded.
func TestTheEdgesAreTheManifestsDependencies(t *testing.T) {
	proposal, err := ProposalFor(publishTestManifest(t, publishFullManifest), "v1.0.0", "")
	if err != nil {
		t.Fatalf("ProposalFor: %v", err)
	}
	if len(proposal.Requirements) != 2 {
		t.Fatalf("proposed %d edges, want 2", len(proposal.Requirements))
	}
	first := proposal.Requirements[0]
	if first.Name != "gateway" || first.VersionRange != "^0.1.0" || first.Dependency != DepRequired {
		t.Errorf("first edge = %+v, want gateway ^0.1.0 required", first)
	}
	second := proposal.Requirements[1]
	if second.Name != "identity" || second.Dependency != DepSoft {
		t.Errorf("second edge = %+v, want identity ^0.1.0 soft (required: false)", second)
	}
}

// fakePublisher is the seam the sequence tests drive: it records the order it
// was asked in and answers with whatever the case planted.
type fakePublisher struct {
	calls []string
	reg   Registration
	token string

	registerErr  error
	publishErr   error
	declareErr   error
	declareErrs  []error
	declareCalls int
}

func (f *fakePublisher) Register(_ context.Context, token string, reg Registration) (Registered, error) {
	f.calls = append(f.calls, OpRegister)
	f.reg = reg
	f.token = token
	return Registered{Name: reg.Name}, f.registerErr
}

func (f *fakePublisher) PublishVersion(_ context.Context, token, name, tag string) (Published, error) {
	f.calls = append(f.calls, OpVersion)
	f.token = token
	return Published{Tag: tag, Version: "1.0.0"}, f.publishErr
}

func (f *fakePublisher) DeclareRequirement(_ context.Context, token, name string, req Requirement) (Declared, error) {
	f.calls = append(f.calls, OpRequirement)
	f.token = token
	f.declareCalls++
	if f.declareErr != nil {
		return Declared{}, f.declareErr
	}
	if f.declareErrs != nil && f.declareCalls-1 < len(f.declareErrs) {
		return Declared{}, f.declareErrs[f.declareCalls-1]
	}
	return Declared{Name: req.Name}, nil
}

// The three writes run in order — register, version, then each edge — and the
// token is presented on every call. The order is the semantics: a version
// cannot be published for a project that is not registered.
func TestRunExecutesTheThreeWritesInOrder(t *testing.T) {
	fake := &fakePublisher{}
	proposal, err := ProposalFor(publishTestManifest(t, publishFullManifest), "v1.0.0", "")
	if err != nil {
		t.Fatalf("ProposalFor: %v", err)
	}

	report := Run(context.Background(), fake, "cafaye_token", proposal)

	want := []string{OpRegister, OpVersion, OpRequirement, OpRequirement}
	if strings.Join(fake.calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want %v", fake.calls, want)
	}
	if fake.token != "cafaye_token" {
		t.Errorf("the token reached the writes (len %d, want %d)", len(fake.token), len("cafaye_token"))
	}
	if !report.Published {
		t.Errorf("a clean run is not published: %+v", report.Steps)
	}
	if len(report.Steps) != 4 {
		t.Fatalf("report has %d steps, want 4", len(report.Steps))
	}
	for _, step := range report.Steps {
		if step.Status != StatusCreated {
			t.Errorf("step %s has status %q, want created", step.Operation, step.Status)
		}
	}
}

// A 409 on re-register is a report line, not a crash: the project is already
// in the catalog, the version and the edges still run, and the run is published.
// The idempotent phrase is pantry's own detail text — the classification is on
// the server's words, never a paraphrase.
func TestA409OnReRegisterIsAReportLine(t *testing.T) {
	fake := &fakePublisher{registerErr: &resolve.PantryError{Status: 409, Code: "conflict",
		Detail: "a project is already registered under that name. Names are unique across the whole registry"}}
	proposal, err := ProposalFor(publishTestManifest(t, publishFullManifest), "v1.0.0", "")
	if err != nil {
		t.Fatalf("ProposalFor: %v", err)
	}

	report := Run(context.Background(), fake, "", proposal)

	if report.Steps[0].Status != StatusAlreadyExists {
		t.Errorf("register step = %q, want already_exists", report.Steps[0].Status)
	}
	if !strings.Contains(report.Steps[0].Detail, "already registered") {
		t.Errorf("the step does not carry the server's detail:\n%s", report.Steps[0].Detail)
	}
	if len(report.Steps) != 4 {
		t.Fatalf("the sequence stopped after the 409: %v", report.Steps)
	}
	if !report.Published {
		t.Error("an idempotent re-run is not published")
	}
}

// The same tolerance for the version: a tag that is already published is
// already published, and the edges still run.
func TestA409OnTheVersionIsAReportLine(t *testing.T) {
	fake := &fakePublisher{publishErr: &resolve.PantryError{Status: 409, Code: "conflict",
		Detail: "that tag is already published for this service"}}
	proposal, err := ProposalFor(publishTestManifest(t, publishFullManifest), "v1.0.0", "")
	if err != nil {
		t.Fatalf("ProposalFor: %v", err)
	}

	report := Run(context.Background(), fake, "", proposal)

	if report.Steps[1].Status != StatusAlreadyExists {
		t.Errorf("version step = %q, want already_exists", report.Steps[1].Status)
	}
	if len(report.Steps) != 4 {
		t.Fatalf("the sequence stopped after the 409: %v", report.Steps)
	}
	if !report.Published {
		t.Error("an idempotent re-run is not published")
	}
}

// And for the edges: an edge that is already declared is already declared.
func TestA409OnAnEdgeIsAReportLine(t *testing.T) {
	fake := &fakePublisher{declareErr: &resolve.PantryError{Status: 409, Code: "conflict",
		Detail: "that edge is already declared from this service to that target"}}
	proposal, err := ProposalFor(publishTestManifest(t, publishFullManifest), "v1.0.0", "")
	if err != nil {
		t.Fatalf("ProposalFor: %v", err)
	}

	report := Run(context.Background(), fake, "", proposal)

	if report.Steps[2].Status != StatusAlreadyExists {
		t.Errorf("edge step = %q, want already_exists", report.Steps[2].Status)
	}
	if !report.Published {
		t.Error("an idempotent re-run is not published")
	}
}

// THE 409 THAT IS NOT IDEMPOTENT. The ordering rule — an edge over a target
// with no published semver release — arrives under the same status and the same
// code, and its detail begins "no published semver version". Treating it as
// already-declared would tell the publisher their edge was stored when the
// database had rolled it back. It is a named failure and the run is not
// published.
func TestA409ThatIsNotIdempotentIsANamedFailure(t *testing.T) {
	fake := &fakePublisher{declareErr: &resolve.PantryError{Status: 409, Code: "conflict",
		Detail: `no published semver version of "gateway" satisfies "^0.1.0". Publish one first with POST /v1/projects/gateway/versions`}}
	proposal, err := ProposalFor(publishTestManifest(t, publishFullManifest), "v1.0.0", "")
	if err != nil {
		t.Fatalf("ProposalFor: %v", err)
	}

	report := Run(context.Background(), fake, "", proposal)

	if report.Steps[2].Status != StatusFailed {
		t.Errorf("edge step = %q, want failed", report.Steps[2].Status)
	}
	if !strings.Contains(report.Steps[2].Detail, "no published semver version") {
		t.Errorf("the step does not carry the server's detail:\n%s", report.Steps[2].Detail)
	}
	if report.Published {
		t.Error("a run whose edge was refused is published")
	}
}

// A failure that is not a 409 stops the sequence: the steps after a 401 would
// be three copies of the same sentence, and the first one already says what to
// fix. The report names it and the run is not published.
func TestANamedFailureStopsTheSequence(t *testing.T) {
	fake := &fakePublisher{registerErr: &resolve.PantryError{Status: 401, Code: "unauthorized",
		Detail: "this operation requires a verified publisher and no principal assertion was presented"}}
	proposal, err := ProposalFor(publishTestManifest(t, publishFullManifest), "v1.0.0", "")
	if err != nil {
		t.Fatalf("ProposalFor: %v", err)
	}

	report := Run(context.Background(), fake, "", proposal)

	if len(report.Steps) != 1 {
		t.Fatalf("the sequence continued past the failure: %v", report.Steps)
	}
	if report.Steps[0].Status != StatusFailed {
		t.Errorf("register step = %q, want failed", report.Steps[0].Status)
	}
	for _, want := range []string{"401", "unauthorized", "principal assertion"} {
		if !strings.Contains(report.Steps[0].Detail, want) {
			t.Errorf("the step does not carry %q:\n%s", want, report.Steps[0].Detail)
		}
	}
	if report.Published {
		t.Error("a run that never registered is published")
	}
}

// A failure on the version stops the edges too — an edge from a project whose
// version never published is a claim about a release that is not there. The
// distinction from the 409 tolerance is the whole point: only "already done"
// continues.
func TestAVersionFailureStopsTheEdges(t *testing.T) {
	fake := &fakePublisher{publishErr: errors.New("call pantry: connection refused")}
	proposal, err := ProposalFor(publishTestManifest(t, publishFullManifest), "v1.0.0", "")
	if err != nil {
		t.Fatalf("ProposalFor: %v", err)
	}

	report := Run(context.Background(), fake, "", proposal)

	if len(report.Steps) != 2 {
		t.Fatalf("the edges ran after the version failed: %v", report.Steps)
	}
	if report.Published {
		t.Error("a run whose version failed is published")
	}
}

// One edge failing does not stop the others: each edge is its own write with
// its own outcome, and a report that stopped at the first would hide the edges
// that landed.
func TestOneFailingEdgeDoesNotStopTheOthers(t *testing.T) {
	fake := &fakePublisher{declareErrs: []error{
		nil,
		&resolve.PantryError{Status: 404, Code: "not_found", Detail: "no project named identity is visible to you"},
	}}
	proposal, err := ProposalFor(publishTestManifest(t, publishFullManifest), "v1.0.0", "")
	if err != nil {
		t.Fatalf("ProposalFor: %v", err)
	}

	report := Run(context.Background(), fake, "", proposal)

	if len(report.Steps) != 4 {
		t.Fatalf("report has %d steps, want 4", len(report.Steps))
	}
	if report.Steps[2].Status != StatusCreated {
		t.Errorf("first edge = %q, want created", report.Steps[2].Status)
	}
	if report.Steps[3].Status != StatusFailed {
		t.Errorf("second edge = %q, want failed", report.Steps[3].Status)
	}
	if report.Published {
		t.Error("a run with a failed edge is published")
	}
}

// The HTTP publisher speaks pantry's wire: the three routes, the bearer, the
// body fields. This is the test that would catch a client whose request shape
// drifted from the server's — the fake above can only prove cafe asked its own
// fake correctly.
func TestTheHTTPPublisherPostsPantrysWireShape(t *testing.T) {
	var paths []string
	var auth string
	var bodies []map[string]any
	handler := &fakeWriteServer{
		onPost: func(path string, token string, body map[string]any) int {
			paths = append(paths, path)
			auth = token
			bodies = append(bodies, body)
			return http.StatusCreated
		},
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	publisher := HTTPPublisher{BaseURL: server.URL}
	proposal, err := ProposalFor(publishTestManifest(t, publishFullManifest), "v1.0.0", "")
	if err != nil {
		t.Fatalf("ProposalFor: %v", err)
	}

	report := Run(context.Background(), publisher, "cafaye_token", proposal)

	if strings.Join(paths, ",") != "/v1/projects,/v1/projects/app/versions,/v1/projects/app/requirements,/v1/projects/app/requirements" {
		t.Errorf("paths = %v", paths)
	}
	if auth != "Bearer cafaye_token" {
		t.Errorf("the bearer reached the server as %q, want %q", auth, "Bearer cafaye_token")
	}
	reg := bodies[0]
	for _, tc := range []struct{ field, want string }{
		{"name", "app"},
		{"language", "go"},
		{"kind", "both"},
		{"core", "^0.2.0"},
		{"manifest_sha256", proposal.Registration.ManifestSHA256},
		{"state", "published"},
	} {
		if reg[tc.field] != tc.want {
			t.Errorf("registration %s = %v, want %q", tc.field, reg[tc.field], tc.want)
		}
	}
	manifest, _ := reg["manifest"].(map[string]any)
	if manifest["name"] != "app" {
		t.Errorf("the registration's manifest = %v, want the manifest document", manifest)
	}
	repo, _ := reg["repository"].(map[string]any)
	if repo["url"] != "git@github.com:cafaye/app.git" {
		t.Errorf("repository = %v, want the manifest's coordinates", repo)
	}
	if bodies[1]["tag"] != "v1.0.0" {
		t.Errorf("version body = %v, want the tag", bodies[1])
	}
	edge := bodies[2]
	if edge["name"] != "gateway" || edge["version_range"] != "^0.1.0" || edge["dependency"] != "required" {
		t.Errorf("edge body = %v, want gateway ^0.1.0 required", edge)
	}
	if !report.Published {
		t.Errorf("a clean run against the wire is not published: %+v", report.Steps)
	}
}

// A non-2xx answer from the real client is the problem envelope, carrying the
// server's code and detail — the same shape the read path decodes, because the
// envelope is pantry's.
func TestTheHTTPPublisherDecodesTheProblemEnvelope(t *testing.T) {
	handler := &fakeWriteServer{
		onPost: func(path string, _ string, _ map[string]any) int {
			if path == "/v1/projects" {
				return http.StatusUnauthorized
			}
			return http.StatusCreated
		},
		body: `{"code":"unauthorized","detail":"the principal assertion is absent, expired, or not signed by a key this service holds"}`,
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	publisher := HTTPPublisher{BaseURL: server.URL}
	proposal, err := ProposalFor(publishTestManifest(t, publishFullManifest), "v1.0.0", "")
	if err != nil {
		t.Fatalf("ProposalFor: %v", err)
	}

	report := Run(context.Background(), publisher, "", proposal)

	if report.Steps[0].Status != StatusFailed {
		t.Fatalf("register step = %q, want failed", report.Steps[0].Status)
	}
	for _, want := range []string{"401", "unauthorized", "principal assertion"} {
		if !strings.Contains(report.Steps[0].Detail, want) {
			t.Errorf("the step does not carry %q:\n%s", want, report.Steps[0].Detail)
		}
	}
}

// fakeWriteServer is a fixture pantry: it records every POST and answers with
// the status and body the case planted. It stands in for the server so the
// client's wire shape is assertable without a network.
type fakeWriteServer struct {
	onPost func(path, token string, body map[string]any) int
	body   string
}

// ServeHTTP is the httptest server's handler, split out so the fixture reads
// as one value rather than a closure inside a test.
func (f *fakeWriteServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	status := http.StatusCreated
	if f.onPost != nil {
		status = f.onPost(r.URL.Path, r.Header.Get("Authorization"), body)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if f.body != "" {
		_, _ = w.Write([]byte(f.body))
		return
	}
	_, _ = w.Write([]byte(`{"name":"app","tag":"v1.0.0","version":"1.0.0","yanked":false,"dependency":"required"}`))
}

// Since 1.7.0 pantry's 201 is the Project read back, and it carries the
// derived `owner_slug`. The report's address is that slug and the name,
// because "app is in the catalog" is a name and not an address: the whole
// point of the derived namespace is that a project is ADDRESSED as
// `<owner>/<name>` from birth, and a publisher that registered a community
// project needs to be told what it is addressable as.
func TestThe201BodyCarriesTheAddressTheProjectRegisteredUnder(t *testing.T) {
	handler := &fakeWriteServer{
		onPost: func(path, _ string, _ map[string]any) int {
			if path == "/v1/projects" {
				return http.StatusCreated
			}
			return http.StatusCreated
		},
		body: `{"name":"app","owner_slug":"cafaye","tag":"v1.0.0","version":"1.0.0","yanked":false,"dependency":"required"}`,
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	proposal, err := ProposalFor(publishTestManifest(t, publishFullManifest), "v1.0.0", "")
	if err != nil {
		t.Fatalf("ProposalFor: %v", err)
	}

	report := Run(context.Background(), HTTPPublisher{BaseURL: server.URL}, "cafaye_token", proposal)

	if report.Address != "cafaye/app" {
		t.Errorf("address = %q, want cafaye/app — the 201 body's owner_slug and the name", report.Address)
	}
	if !report.Published {
		t.Errorf("the run is not published: %+v", report.Steps)
	}
}

// A re-register answered 409 carries no 201 body, so there is no owner_slug to
// read and the report says nothing rather than inventing an address from the
// name. A report that guessed `cafaye/app` would be a report naming an address
// the server never confirmed.
func TestAnIdempotentReRegisterHasNoAddressToName(t *testing.T) {
	fake := &fakePublisher{registerErr: &resolve.PantryError{Status: 409, Code: "conflict",
		Detail: "a project is already registered under that name. Names are unique across the whole registry"}}
	proposal, err := ProposalFor(publishTestManifest(t, publishFullManifest), "v1.0.0", "")
	if err != nil {
		t.Fatalf("ProposalFor: %v", err)
	}

	report := Run(context.Background(), fake, "", proposal)

	if report.Address != "" {
		t.Errorf("address = %q, want empty — the 409 answered no 201 body", report.Address)
	}
	if !report.Published {
		t.Errorf("an idempotent re-run is not published: %+v", report.Steps)
	}
}
