package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cafaye/cafe/internal/agent"
	"github.com/cafaye/cafe/internal/resolve"
)

// resolveCallerToken is a string no real credential looks like and every fake
// in this file answers with it. The assertions are that it reaches tier 2 and
// tier 3 and reaches NEITHER stream. A test that only checked "the command
// worked" would pass against a CLI that printed the token in its success path,
// which is the one failure this packet exists to prevent.
var resolveCallerToken = "cafaye_" + strings.Repeat("k", 40)

// fakeResolvePantry is the tiered catalog, injected. It records which tier was
// asked and with what token, because "the resolver walked the tiers in order"
// is a claim about calls rather than about output.
type fakeResolvePantry struct {
	project    *resolve.Project
	projectErr error
	coords     *resolve.Coordinates
	coordsErr  error

	address    *resolve.Project
	addressErr error

	calls     []string
	lastOwner string
	lastToken string
}

func (f *fakeResolvePantry) Project(_ context.Context, name string) (*resolve.Project, error) {
	f.calls = append(f.calls, "tier1:"+name)
	if f.projectErr != nil {
		return nil, f.projectErr
	}
	return f.project, nil
}

func (f *fakeResolvePantry) ProjectByAddress(_ context.Context, owner, name string) (*resolve.Project, error) {
	f.calls = append(f.calls, "tier1addr:"+owner+"/"+name)
	f.lastOwner = owner
	if f.addressErr != nil {
		return nil, f.addressErr
	}
	return f.address, nil
}

func (f *fakeResolvePantry) Coordinates(_ context.Context, name, token string) (*resolve.Coordinates, error) {
	f.calls = append(f.calls, "tier2:"+name)
	f.lastToken = token
	if f.coordsErr != nil {
		return nil, f.coordsErr
	}
	return f.coords, nil
}

// fakeResolveControlPlane is the entitlement check, injected. It records the
// triple the check was asked about.
type fakeResolveControlPlane struct {
	grant    *resolve.FetchGrant
	err      error
	calls    int
	artifact string
	version  string
	token    string
}

func (f *fakeResolveControlPlane) ArtifactFetch(_ context.Context, _, artifact, versionRange, token string) (*resolve.FetchGrant, error) {
	f.calls++
	f.artifact = artifact
	f.version = versionRange
	f.token = token
	return f.grant, f.err
}

// resolveTestProject writes a cafaye.yml into a directory the test owns and
// returns the directory. The manifest is written by hand rather than scaffolded
// because the point is the dependency list, not the scaffold.
func resolveTestProject(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cafaye.yml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write the manifest: %v", err)
	}
	return dir
}

// resolveTestEnv builds the deps one command needs: a directory it owns, an
// in-memory keyring holding the caller's token, and a pantry and a control plane
// that answer without a network. Nothing here touches the machine's config
// directory, its keychain or the network.
func resolveTestEnv(t *testing.T, ring *agentTestRing, pantry resolve.Pantry, controlPlane resolve.ControlPlane) resolveDeps {
	t.Helper()
	dir := t.TempDir()
	deps := resolveDeps{dir: dir, ring: ring, pantry: pantry, controlPlane: controlPlane}
	// A registered session with a token in the ring is what a claimed agent
	// looks like; the command reads the token out of the ring and never prints
	// it, which is the invariant the token assertions below check.
	cfg := agent.Config{
		AgentSessions:      map[string]agent.Session{"noel": {Name: "noel", TokenRef: "noel"}},
		ActiveAgentSession: "noel",
	}
	if err := cfg.Save(dir); err != nil {
		t.Fatalf("save the session: %v", err)
	}
	if err := ring.Set(agent.KeyringService, "noel", resolveCallerToken); err != nil {
		t.Fatalf("put the token in the ring: %v", err)
	}
	return deps
}

// runResolveCLI drives ONE cafe resolve invocation against injected deps and
// hands back the exit code plus both streams — exactly what main() does with
// the real process, minus the process.
func runResolveCLI(t *testing.T, deps resolveDeps, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion, Context: context.Background()}
	err := newResolveCommand(deps).Execute(env, args)
	return codeFor(*env, err), out.String(), errOut.String()
}

// A project whose dependencies all resolve exits 0, writes one JSON document on
// stdout, and says one line per dependency on stderr. The token is presented to
// tier 2 and tier 3 and reaches NEITHER stream.
func TestResolveAllResolvedIsOneJSONDocument(t *testing.T) {
	ring := newAgentTestRing()
	pantry := &fakeResolvePantry{
		project: &resolve.Project{Name: "gateway", Repository: resolve.PublicRepo{Visibility: "public"}},
		coords:  &resolve.Coordinates{Name: "gateway", Repository: resolve.Repository{URL: "git@github.com:cafaye/gateway.git"}},
	}
	cp := &fakeResolveControlPlane{grant: &resolve.FetchGrant{
		Purpose: "artifact.fetch", Artifact: "identity", VersionRange: "^0.1.0", ExpiresAt: "2026-10-09T16:00:00Z",
	}}
	deps := resolveTestEnv(t, ring, pantry, cp)

	project := resolveTestProject(t, `name: app
description: An application.
language: go
core: ^0.2.0
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
`)
	code, stdout, stderr := runResolveCLI(t, deps, "--account", "11111111-1111-4111-8111-111111111111", project)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr:\n%s", code, stderr)
	}
	if !json.Valid([]byte(stdout)) {
		t.Fatalf("stdout is not one JSON document:\n%s", stdout)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("decode the document: %v", err)
	}
	services, _ := doc["services"].([]any)
	if len(services) != 2 {
		t.Fatalf("document names %d services, want 2\n%s", len(services), stdout)
	}
	if pantry.lastToken != resolveCallerToken {
		t.Errorf("tier 2 token is not the caller's token (len %d, want %d)", len(pantry.lastToken), len(resolveCallerToken))
	}
	if cp.token != resolveCallerToken {
		t.Errorf("tier 3 token is not the caller's token (len %d, want %d)", len(cp.token), len(resolveCallerToken))
	}
	// The control half, and it is load-bearing: a build that redacted
	// everything would pass the assertions above by leaking nothing at all.
	// The dependency names ARE printed, so the assertions above say something.
	for _, want := range []string{"gateway", "identity"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not name %q:\n%s", want, stderr)
		}
	}
	for _, stream := range []string{stdout, stderr} {
		if strings.Contains(stream, resolveCallerToken) {
			t.Errorf("the caller's token reached a stream:\n%s", stream)
		}
	}
}

// The named failure the brief leads with. A required paid dependency the
// account does not hold BLOCKS: the sentence names the code, the product and
// where to start one, and the exit code is 1.
func TestResolveEntitlementRequiredBlocksWithTheNamedFailure(t *testing.T) {
	ring := newAgentTestRing()
	pantry := &fakeResolvePantry{
		project: &resolve.Project{Name: "identity"},
		coords:  &resolve.Coordinates{Name: "identity"},
	}
	cp := &fakeResolveControlPlane{err: &resolve.ControlPlaneError{Status: 403, Code: "entitlement_required",
		Message: "product `identity` needs an entitlement — start one at https://cafaye.com"}}
	deps := resolveTestEnv(t, ring, pantry, cp)

	project := resolveTestProject(t, `name: app
description: An application.
language: go
core: ^0.2.0
dependencies:
  - name: identity
    version: ^0.1.0
repository:
  url: git@github.com:cafaye/app.git
  defaultBranch: master
  visibility: public
owner:
  team: app
  contact: app@cafaye.com
`)
	code, _, stderr := runResolveCLI(t, deps, "--account", "11111111-1111-4111-8111-111111111111", project)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (a required dependency blocks)\nstderr:\n%s", code, stderr)
	}
	for _, want := range []string{"entitlement_required", "`identity`", "https://cafaye.com", "BLOCKED"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not carry %q:\n%s", want, stderr)
		}
	}
}

// The second named failure: an entitlement that does not cover the range the
// project asks for. Both ranges are in the sentence, and a required dependency
// that fails this way blocks.
func TestResolveVersionNotGrantedNamesBothRanges(t *testing.T) {
	ring := newAgentTestRing()
	pantry := &fakeResolvePantry{
		project: &resolve.Project{Name: "identity"},
		coords:  &resolve.Coordinates{Name: "identity"},
	}
	cp := &fakeResolveControlPlane{err: &resolve.ControlPlaneError{Status: 403, Code: "version_not_granted",
		Message: "your entitlement covers `^0.2.0`, this project asks for `^0.1.0`"}}
	deps := resolveTestEnv(t, ring, pantry, cp)

	project := resolveTestProject(t, `name: app
description: An application.
language: go
core: ^0.2.0
dependencies:
  - name: identity
    version: ^0.1.0
repository:
  url: git@github.com:cafaye/app.git
  defaultBranch: master
  visibility: public
owner:
  team: app
  contact: app@cafaye.com
`)
	code, _, stderr := runResolveCLI(t, deps, "--account", "11111111-1111-4111-8111-111111111111", project)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr:\n%s", code, stderr)
	}
	for _, want := range []string{"version_not_granted", "`^0.2.0`", "`^0.1.0`"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not carry %q:\n%s", want, stderr)
		}
	}
}

// THE DEGRADATION, and it is the reason `required` is a schema field rather
// than a convention. The same unentitled product that BLOCKS above DEGRADES
// here: a warning on stderr, the failure still named in it, and exit 0 — the
// service runs without it, which is exactly what the schema says.
func TestResolveOptionalDependencyDegradesWithAWarning(t *testing.T) {
	ring := newAgentTestRing()
	pantry := &fakeResolvePantry{
		project: &resolve.Project{Name: "identity"},
		coords:  &resolve.Coordinates{Name: "identity"},
	}
	cp := &fakeResolveControlPlane{err: &resolve.ControlPlaneError{Status: 403, Code: "entitlement_required",
		Message: "product `identity` needs an entitlement — start one at https://cafaye.com"}}
	deps := resolveTestEnv(t, ring, pantry, cp)

	project := resolveTestProject(t, `name: app
description: An application.
language: go
core: ^0.2.0
dependencies:
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
`)
	code, stdout, stderr := runResolveCLI(t, deps, "--account", "11111111-1111-4111-8111-111111111111", project)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (an optional dependency does not block)\nstderr:\n%s", code, stderr)
	}
	for _, want := range []string{"DEGRADED", "optional", "entitlement_required"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not carry %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "BLOCKED") {
		t.Errorf("an optional dependency was reported as blocked:\n%s", stderr)
	}
	// The document still records the failure, so a machine parsing stdout
	// learns the same thing a person reading stderr does. Degrading is a
	// statement about the exit code, not about hiding the fact.
	if !strings.Contains(stdout, "entitlement_required") {
		t.Errorf("the document hides the failure:\n%s", stdout)
	}
}

// An unknown name is a tier-1 failure and it never reaches tier 2 or tier 3: a
// service that does not exist has no coordinates and no entitlement.
func TestResolveUnknownNameStopsAtTierOne(t *testing.T) {
	ring := newAgentTestRing()
	pantry := &fakeResolvePantry{projectErr: &resolve.PantryError{Status: 404, Code: "not_found",
		Detail: "no official cafaye service is registered under that name"}}
	cp := &fakeResolveControlPlane{}
	deps := resolveTestEnv(t, ring, pantry, cp)

	project := resolveTestProject(t, `name: app
description: An application.
language: go
core: ^0.2.0
dependencies:
  - name: nope
    version: ^0.1.0
repository:
  url: git@github.com:cafaye/app.git
  defaultBranch: master
  visibility: public
owner:
  team: app
  contact: app@cafaye.com
`)
	code, _, stderr := runResolveCLI(t, deps, project)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "not_found") {
		t.Errorf("stderr does not name the failure:\n%s", stderr)
	}
	for _, call := range pantry.calls {
		if strings.HasPrefix(call, "tier2:") {
			t.Errorf("an unknown name reached tier 2 (%v)", pantry.calls)
		}
	}
	if cp.calls != 0 {
		t.Errorf("controlPlane calls = %d, want 0 (tier 1 failed first)", cp.calls)
	}
}

// A project with no manifest is refused with the command that creates one,
// rather than a bare "no such file".
func TestResolveWithoutAManifestNamesTheCommand(t *testing.T) {
	ring := newAgentTestRing()
	deps := resolveTestEnv(t, ring, &fakeResolvePantry{}, &fakeResolveControlPlane{})

	code, _, stderr := runResolveCLI(t, deps, t.TempDir())

	if code == 0 {
		t.Fatalf("a project with no manifest exited 0")
	}
	if !strings.Contains(stderr, "cafe init") {
		t.Errorf("stderr does not name the command that creates one:\n%s", stderr)
	}
}

// A caller with no registered session is told how to get one, and tier 1 still
// runs: advertising is anonymous, and a missing credential is not a refusal to
// try.
func TestResolveWithoutASessionSaysSoAndStillRuns(t *testing.T) {
	pantry := &fakeResolvePantry{
		project: &resolve.Project{Name: "gateway"},
		coords:  &resolve.Coordinates{Name: "gateway", Repository: resolve.Repository{URL: "git@github.com:cafaye/gateway.git"}},
	}
	deps := resolveDeps{dir: t.TempDir(), ring: newAgentTestRing(), pantry: pantry, controlPlane: &fakeResolveControlPlane{}}

	project := resolveTestProject(t, `name: app
description: An application.
language: go
core: ^0.2.0
dependencies:
  - name: gateway
    version: ^0.1.0
repository:
  url: git@github.com:cafaye/app.git
  defaultBranch: master
  visibility: public
owner:
  team: app
  contact: app@cafaye.com
`)
	code, stdout, stderr := runResolveCLI(t, deps, project)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "cafe agent register") {
		t.Errorf("stderr does not name the command that gets a credential:\n%s", stderr)
	}
	if !json.Valid([]byte(stdout)) {
		t.Errorf("stdout is not one JSON document:\n%s", stdout)
	}
	// Tier 2 was still asked, with an empty token: pantry answers the named
	// 401, which is the honest response to a caller with no credential.
	if pantry.lastToken != "" {
		t.Errorf("tier 2 presented a token to a caller with no session (len %d)", len(pantry.lastToken))
	}
}

// A paid dependency with no --account is a cafe-side refusal that names the
// flag, not a silent skip and not a call to the control plane with an empty account.
func TestResolvePaidDependencyWithoutAccountNamesTheFlag(t *testing.T) {
	ring := newAgentTestRing()
	pantry := &fakeResolvePantry{
		project: &resolve.Project{Name: "identity"},
		coords:  &resolve.Coordinates{Name: "identity"},
	}
	cp := &fakeResolveControlPlane{}
	deps := resolveTestEnv(t, ring, pantry, cp)

	project := resolveTestProject(t, `name: app
description: An application.
language: go
core: ^0.2.0
dependencies:
  - name: identity
    version: ^0.1.0
repository:
  url: git@github.com:cafaye/app.git
  defaultBranch: master
  visibility: public
owner:
  team: app
  contact: app@cafaye.com
`)
	code, _, stderr := runResolveCLI(t, deps, project)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "--account") {
		t.Errorf("stderr does not name the flag:\n%s", stderr)
	}
	if cp.calls != 0 {
		t.Errorf("controlPlane calls = %d, want 0 (the account is missing, so the question is unaskable)", cp.calls)
	}
}

// The manifest schema makes `version` OPTIONAL and box's check makes it
// REQUIRED — an unbounded request is a 422 the endpoint refuses. The
// disagreement is settled on cafe's side rather than in a round trip that
// answers 422 and blames the reader for it.
func TestResolvePaidDependencyWithoutAVersionIsNamed(t *testing.T) {
	ring := newAgentTestRing()
	pantry := &fakeResolvePantry{
		project: &resolve.Project{Name: "identity"},
		coords:  &resolve.Coordinates{Name: "identity"},
	}
	cp := &fakeResolveControlPlane{}
	deps := resolveTestEnv(t, ring, pantry, cp)

	project := resolveTestProject(t, `name: app
description: An application.
language: go
core: ^0.2.0
dependencies:
  - name: identity
repository:
  url: git@github.com:cafaye/app.git
  defaultBranch: master
  visibility: public
owner:
  team: app
  contact: app@cafaye.com
`)
	code, _, stderr := runResolveCLI(t, deps, "--account", "11111111-1111-4111-8111-111111111111", project)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "version_required") {
		t.Errorf("stderr does not name the failure:\n%s", stderr)
	}
	if !strings.Contains(stderr, "version") || !strings.Contains(stderr, "^X.Y.Z") {
		t.Errorf("stderr does not say what to add:\n%s", stderr)
	}
	if cp.calls != 0 {
		t.Errorf("controlPlane calls = %d, want 0 (the question is unaskable without a range)", cp.calls)
	}
}

// An --account that is not a UUID is a 404 from box, and a 404 about a
// string the reader typed is a sentence that sends them nowhere. Refusing here
// names what was typed instead.
func TestResolveAccountThatIsNotAUUIDIsNamed(t *testing.T) {
	ring := newAgentTestRing()
	pantry := &fakeResolvePantry{
		project: &resolve.Project{Name: "identity"},
		coords:  &resolve.Coordinates{Name: "identity"},
	}
	cp := &fakeResolveControlPlane{}
	deps := resolveTestEnv(t, ring, pantry, cp)

	project := resolveTestProject(t, `name: app
description: An application.
language: go
core: ^0.2.0
dependencies:
  - name: identity
    version: ^0.1.0
repository:
  url: git@github.com:cafaye/app.git
  defaultBranch: master
  visibility: public
owner:
  team: app
  contact: app@cafaye.com
`)
	code, _, stderr := runResolveCLI(t, deps, "--account", "not-a-uuid", project)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "account_invalid") {
		t.Errorf("stderr does not name the failure:\n%s", stderr)
	}
	if !strings.Contains(stderr, "not-a-uuid") {
		t.Errorf("stderr does not say what was typed:\n%s", stderr)
	}
	if cp.calls != 0 {
		t.Errorf("controlPlane calls = %d, want 0 (the account is not an id)", cp.calls)
	}
}

// The two flags that name the origins, proven end to end against the REAL
// HTTP clients rather than injected fakes. This is the test that would have
// caught the first version, where withDefaults() built the clients out of the
// URL and the flag overrides ran afterwards — so the flags were honoured
// nowhere and the command dialled the environment's origin while looking
// configurable. No injected fake can see that: a fake is the client, and
// there is nobody to ask which URL it was given.
func TestResolveFlagsNameTheOriginsEndToEnd(t *testing.T) {
	const account = "11111111-1111-4111-8111-111111111111"
	pantryHits := 0
	pantry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pantryHits++
		switch r.URL.Path {
		case "/v1/projects/identity":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"identity","description":"Authentication","language":"go"}`))
		case "/v1/projects/identity/coordinates":
			if got := r.Header.Get("Authorization"); got != "Bearer "+resolveCallerToken {
				t.Errorf("tier 2 bearer = %q, want the caller's token", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"identity","repository":{"url":"git@github.com:cafaye/identity.git","defaultBranch":"master"}}`))
		default:
			t.Errorf("pantry saw an unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer pantry.Close()
	controlPlaneHits := 0
	controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controlPlaneHits++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/accounts/" + account + "/artifact_fetches":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"capability":{"token":"cap_abc","purpose":"artifact.fetch","account_id":"` + account + `","artifact":"identity","artifact_version_range":"^0.1.0","expires_at":"2026-10-09T16:00:00Z"}}`))
		case "/v1/capabilities/redeem":
			_, _ = w.Write([]byte(`{"capability":{"purpose":"artifact.fetch","account_id":"` + account + `","artifact":"identity","artifact_version_range":"^0.1.0","expires_at":"2026-10-09T16:00:00Z"}}`))
		default:
			t.Errorf("control plane saw an unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer controlPlane.Close()

	// Nil pantry and nil control plane, so withDefaults() builds the real clients
	// out of the URLs the flags name.
	dir := t.TempDir()
	ring := newAgentTestRing()
	cfg := agent.Config{
		AgentSessions:      map[string]agent.Session{"noel": {Name: "noel", TokenRef: "noel"}},
		ActiveAgentSession: "noel",
	}
	if err := cfg.Save(dir); err != nil {
		t.Fatalf("save the session: %v", err)
	}
	if err := ring.Set(agent.KeyringService, "noel", resolveCallerToken); err != nil {
		t.Fatalf("put the token in the ring: %v", err)
	}
	deps := resolveDeps{dir: dir, ring: ring}

	project := resolveTestProject(t, `name: app
description: An application.
language: go
core: ^0.2.0
dependencies:
  - name: identity
    version: ^0.1.0
repository:
  url: git@github.com:cafaye/app.git
  defaultBranch: master
  visibility: public
owner:
  team: app
  contact: app@cafaye.com
`)
	code, stdout, stderr := runResolveCLI(t, deps,
		"--pantry", pantry.URL, "--box", controlPlane.URL, "--account", account, project)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if pantryHits != 2 {
		t.Errorf("pantry answered %d requests, want 2 (tiers 1 and 2)", pantryHits)
	}
	if controlPlaneHits != 2 {
		t.Errorf("control plane answered %d requests, want 2 (the mint and the redemption)", controlPlaneHits)
	}
	if !strings.Contains(stdout, "artifact.fetch") {
		t.Errorf("the document carries no fetch grant:\n%s", stdout)
	}
	if strings.Contains(stdout, "cap_abc") {
		t.Errorf("the capability token reached stdout, and a spent token is not a deliverable:\n%s", stdout)
	}
	if strings.Contains(stderr, "cap_abc") {
		t.Errorf("the capability token reached stderr:\n%s", stderr)
	}
}

// The usage error and the argument count, checked through the command rather
// than around it.
func TestResolveUsage(t *testing.T) {
	ring := newAgentTestRing()
	deps := resolveTestEnv(t, ring, &fakeResolvePantry{}, &fakeResolveControlPlane{})

	code, _, _ := runResolveCLI(t, deps)
	if code == 0 {
		t.Fatalf("no argument exited 0, want a usage error")
	}
}

// THE ADDRESS FORM. `cafe resolve <owner>/<name>` asks about one project
// addressed unambiguously, not about a directory: a community project whose
// bare name another publisher also holds, or whose owner is the only thing
// the caller knows. The command parses the slash form and hands the resolver
// the pair, and the report names the address it resolved.
func TestResolveAcceptsTheAddressForm(t *testing.T) {
	ring := newAgentTestRing()
	pantry := &fakeResolvePantry{
		project: &resolve.Project{Name: "widget", OwnerSlug: "alice"},
		coords:  &resolve.Coordinates{Name: "widget", Repository: resolve.Repository{URL: "git@github.com:alice/widget.git"}},
	}
	cp := &fakeResolveControlPlane{}
	deps := resolveTestEnv(t, ring, pantry, cp)

	code, stdout, stderr := runResolveCLI(t, deps, "alice/widget")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	// The fast path is the bare name, and the row the resolver came back with
	// is what the address was asked for. The document names the address: the
	// owner travels with the resolution so a machine reading it can tell an
	// addressed resolution from a bare one.
	var document struct {
		Services []struct {
			Name  string `json:"name"`
			Owner string `json:"owner"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("decode the document: %v\n%s", err, stdout)
	}
	if len(document.Services) != 1 {
		t.Fatalf("the document holds %d resolutions, want 1", len(document.Services))
	}
	got := document.Services[0]
	if got.Owner != "alice" || got.Name != "widget" {
		t.Errorf("the document resolved %s/%s, want alice/widget", got.Owner, got.Name)
	}
}

// An address that names the wrong owner is refused with pantry's own sentence,
// which names the address — the difference between "there is no `widget`" and
// "there is no `widget` at alice/", which is the question that was asked.
func TestResolveAddressFormRefusesTheWrongOwner(t *testing.T) {
	ring := newAgentTestRing()
	pantry := &fakeResolvePantry{
		project: &resolve.Project{Name: "widget", OwnerSlug: "bob"},
		addressErr: &resolve.PantryError{Status: 404, Code: "not_found",
			Detail: "no official cafaye project is registered under the address alice/widget"},
	}
	deps := resolveTestEnv(t, ring, pantry, &fakeResolveControlPlane{})

	code, _, stderr := runResolveCLI(t, deps, "alice/widget")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "alice/widget") {
		t.Errorf("stderr does not name the address that was asked for:\n%s", stderr)
	}
	if strings.Contains(stderr, "git@github.com:bob") {
		t.Errorf("the refusal leaked another publisher's coordinates:\n%s", stderr)
	}
}

// A directory that happens to be spelled `owner/name` is still a directory:
// the address form is the fallback for a path with no manifest in it, never a
// rule that swallows a real project whose directory has a slash in it.
func TestResolveTreatsADirectoryWithASlashAsADirectory(t *testing.T) {
	ring := newAgentTestRing()
	pantry := &fakeResolvePantry{
		project: &resolve.Project{Name: "gateway", OwnerSlug: "cafaye"},
		coords:  &resolve.Coordinates{Name: "gateway", Repository: resolve.Repository{URL: "git@github.com:cafaye/gateway.git"}},
	}
	deps := resolveTestEnv(t, ring, pantry, &fakeResolveControlPlane{})

	base := t.TempDir()
	project := filepath.Join(base, "alice", "widget")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `name: gateway
description: An application.
language: go
core: ^0.2.0
dependencies:
  - name: gateway
    version: ^0.1.0
repository:
  url: git@github.com:cafaye/app.git
  defaultBranch: master
  visibility: public
owner:
  team: app
  contact: app@cafaye.com
`
	if err := os.WriteFile(filepath.Join(project, "cafaye.yml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write the manifest: %v", err)
	}

	code, stdout, stderr := runResolveCLI(t, deps, project)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "gateway") {
		t.Errorf("the document does not name the dependency:\n%s", stdout)
	}
	for _, call := range pantry.calls {
		if strings.HasPrefix(call, "tier1addr:") {
			t.Errorf("a directory was read as an address (%v)", pantry.calls)
		}
	}
}
