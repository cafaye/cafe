package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cafaye/caf/internal/agent"
	"github.com/cafaye/caf/internal/resolve"
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
	service    *resolve.Service
	serviceErr error
	coords     *resolve.Coordinates
	coordsErr  error

	calls     []string
	lastToken string
}

func (f *fakeResolvePantry) Service(_ context.Context, name string) (*resolve.Service, error) {
	f.calls = append(f.calls, "tier1:"+name)
	if f.serviceErr != nil {
		return nil, f.serviceErr
	}
	return f.service, nil
}

func (f *fakeResolvePantry) Coordinates(_ context.Context, name, token string) (*resolve.Coordinates, error) {
	f.calls = append(f.calls, "tier2:"+name)
	f.lastToken = token
	if f.coordsErr != nil {
		return nil, f.coordsErr
	}
	return f.coords, nil
}

// fakeResolveConductor is the entitlement check, injected. It records the
// triple the check was asked about.
type fakeResolveConductor struct {
	grant    *resolve.FetchGrant
	err      error
	calls    int
	artifact string
	version  string
	token    string
}

func (f *fakeResolveConductor) ArtifactFetch(_ context.Context, _, artifact, versionRange, token string) (*resolve.FetchGrant, error) {
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
// in-memory keyring holding the caller's token, and a pantry and a conductor
// that answer without a network. Nothing here touches the machine's config
// directory, its keychain or the network.
func resolveTestEnv(t *testing.T, ring *agentTestRing, pantry resolve.Pantry, conductor resolve.Conductor) resolveDeps {
	t.Helper()
	dir := t.TempDir()
	deps := resolveDeps{dir: dir, ring: ring, pantry: pantry, conductor: conductor}
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

// runResolveCLI drives ONE caf resolve invocation against injected deps and
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
		service: &resolve.Service{Name: "gateway", Visibility: "public"},
		coords:  &resolve.Coordinates{Name: "gateway", Repository: resolve.Repository{URL: "git@github.com:cafaye/gateway.git"}},
	}
	cond := &fakeResolveConductor{grant: &resolve.FetchGrant{
		Purpose: "artifact.fetch", Artifact: "identity", VersionRange: "^0.1.0", ExpiresAt: "2026-10-09T16:00:00Z",
	}}
	deps := resolveTestEnv(t, ring, pantry, cond)

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
	if cond.token != resolveCallerToken {
		t.Errorf("tier 3 token is not the caller's token (len %d, want %d)", len(cond.token), len(resolveCallerToken))
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
		service: &resolve.Service{Name: "identity"},
		coords:  &resolve.Coordinates{Name: "identity"},
	}
	cond := &fakeResolveConductor{err: &resolve.ConductorError{Status: 403, Code: "entitlement_required",
		Message: "product `identity` needs an entitlement — start one at https://cafaye.com"}}
	deps := resolveTestEnv(t, ring, pantry, cond)

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
		service: &resolve.Service{Name: "identity"},
		coords:  &resolve.Coordinates{Name: "identity"},
	}
	cond := &fakeResolveConductor{err: &resolve.ConductorError{Status: 403, Code: "version_not_granted",
		Message: "your entitlement covers `^0.2.0`, this project asks for `^0.1.0`"}}
	deps := resolveTestEnv(t, ring, pantry, cond)

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
		service: &resolve.Service{Name: "identity"},
		coords:  &resolve.Coordinates{Name: "identity"},
	}
	cond := &fakeResolveConductor{err: &resolve.ConductorError{Status: 403, Code: "entitlement_required",
		Message: "product `identity` needs an entitlement — start one at https://cafaye.com"}}
	deps := resolveTestEnv(t, ring, pantry, cond)

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
	pantry := &fakeResolvePantry{serviceErr: &resolve.PantryError{Status: 404, Code: "not_found",
		Detail: "no official cafaye service is registered under that name"}}
	cond := &fakeResolveConductor{}
	deps := resolveTestEnv(t, ring, pantry, cond)

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
	if cond.calls != 0 {
		t.Errorf("conductor calls = %d, want 0 (tier 1 failed first)", cond.calls)
	}
}

// A project with no manifest is refused with the command that creates one,
// rather than a bare "no such file".
func TestResolveWithoutAManifestNamesTheCommand(t *testing.T) {
	ring := newAgentTestRing()
	deps := resolveTestEnv(t, ring, &fakeResolvePantry{}, &fakeResolveConductor{})

	code, _, stderr := runResolveCLI(t, deps, t.TempDir())

	if code == 0 {
		t.Fatalf("a project with no manifest exited 0")
	}
	if !strings.Contains(stderr, "caf init") {
		t.Errorf("stderr does not name the command that creates one:\n%s", stderr)
	}
}

// A caller with no registered session is told how to get one, and tier 1 still
// runs: advertising is anonymous, and a missing credential is not a refusal to
// try.
func TestResolveWithoutASessionSaysSoAndStillRuns(t *testing.T) {
	pantry := &fakeResolvePantry{
		service: &resolve.Service{Name: "gateway"},
		coords:  &resolve.Coordinates{Name: "gateway", Repository: resolve.Repository{URL: "git@github.com:cafaye/gateway.git"}},
	}
	deps := resolveDeps{dir: t.TempDir(), ring: newAgentTestRing(), pantry: pantry, conductor: &fakeResolveConductor{}}

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
	if !strings.Contains(stderr, "caf agent register") {
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

// A paid dependency with no --account is a caf-side refusal that names the
// flag, not a silent skip and not a call to conductor with an empty account.
func TestResolvePaidDependencyWithoutAccountNamesTheFlag(t *testing.T) {
	ring := newAgentTestRing()
	pantry := &fakeResolvePantry{
		service: &resolve.Service{Name: "identity"},
		coords:  &resolve.Coordinates{Name: "identity"},
	}
	cond := &fakeResolveConductor{}
	deps := resolveTestEnv(t, ring, pantry, cond)

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
	if cond.calls != 0 {
		t.Errorf("conductor calls = %d, want 0 (the account is missing, so the question is unaskable)", cond.calls)
	}
}

// The usage error and the argument count, checked through the command rather
// than around it.
func TestResolveUsage(t *testing.T) {
	ring := newAgentTestRing()
	deps := resolveTestEnv(t, ring, &fakeResolvePantry{}, &fakeResolveConductor{})

	code, _, _ := runResolveCLI(t, deps)
	if code == 0 {
		t.Fatalf("no argument exited 0, want a usage error")
	}
}
