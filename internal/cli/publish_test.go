package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cafaye/caf/internal/agent"
	"github.com/cafaye/caf/internal/publish"
	"github.com/cafaye/caf/internal/resolve"
)

// publishCallerToken is a string no real credential looks like and every fake
// in this file answers with it. The assertions are that it reaches the writes
// and reaches NEITHER stream — the same courier rule resolve's tests hold.
var publishCallerToken = "cafaye_" + strings.Repeat("p", 40)

// A manifest with a full contract surface: HTTP, events out, events in, two
// dependencies. The kind derives (both), so --kind is not needed.
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

// A manifest with no contract surface at all — no exposes, no consumes — but
// with the schema-required repository and owner: the kind is curated, so the
// command refuses without --kind and takes it when it is given.
const publishBareManifest = `name: app
language: go
core: ^0.2.0
repository:
  url: git@github.com:cafaye/app.git
  defaultBranch: master
  visibility: public
owner:
  team: app
  contact: app@cafaye.com
`

// fakePublishPublisher is the seam the decision-table tests drive: it records
// the order it was asked in, the token it was handed and the bodies it was
// given, and answers with whatever the case planted. Nothing here touches a
// network, a git repository or a terminal.
type fakePublishPublisher struct {
	calls []string
	token string

	reg          publish.Registration
	name         string
	tag          string
	requirements []publish.Requirement

	registerErr error
	publishErr  error
	declareErr  error
	// declareErrs answers the i-th edge call with the i-th error, for a
	// sequence that fails in the middle.
	declareErrs []error
}

func (f *fakePublishPublisher) Register(_ context.Context, token string, reg publish.Registration) (publish.Registered, error) {
	f.calls = append(f.calls, publish.OpRegister)
	f.token = token
	f.reg = reg
	return publish.Registered{Name: reg.Name}, f.registerErr
}

func (f *fakePublishPublisher) PublishVersion(_ context.Context, token, name, tag string) (publish.Published, error) {
	f.calls = append(f.calls, publish.OpVersion)
	f.token = token
	f.name = name
	f.tag = tag
	return publish.Published{Tag: tag, Version: "1.0.0"}, f.publishErr
}

func (f *fakePublishPublisher) DeclareRequirement(_ context.Context, token, name string, req publish.Requirement) (publish.Declared, error) {
	f.calls = append(f.calls, publish.OpRequirement)
	f.token = token
	f.name = name
	f.requirements = append(f.requirements, req)
	if f.declareErr != nil {
		return publish.Declared{}, f.declareErr
	}
	if len(f.declareErrs) >= len(f.requirements) && f.declareErrs[len(f.requirements)-1] != nil {
		return publish.Declared{}, f.declareErrs[len(f.requirements)-1]
	}
	return publish.Declared{Name: req.Name}, nil
}

var _ publish.Publisher = (*fakePublishPublisher)(nil)

// publishTestProject writes a cafaye.yml into a directory the test owns and
// returns the directory.
func publishTestProject(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cafaye.yml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write the manifest: %v", err)
	}
	return dir
}

// publishTestEnv builds the deps one command needs: a directory it owns, an
// in-memory keyring holding the caller's token under a registered session, a
// publisher that answers without a network, a confirmation the test controls
// and a tag derivation that needs no git repository. Nothing here touches the
// machine's config directory, its keychain, the network or a repository.
func publishTestEnv(t *testing.T, pub *fakePublishPublisher) publishDeps {
	t.Helper()
	dir := t.TempDir()
	ring := newAgentTestRing()
	deps := publishDeps{
		dir:       dir,
		ring:      ring,
		publisher: pub,
		confirm:   func(string) (bool, error) { return true, nil },
		tags:      func(string) (string, error) { return "v1.0.0", nil },
	}
	cfg := agent.Config{
		AgentSessions:      map[string]agent.Session{"noel": {Name: "noel", TokenRef: "noel"}},
		ActiveAgentSession: "noel",
	}
	if err := cfg.Save(dir); err != nil {
		t.Fatalf("save the session: %v", err)
	}
	if err := ring.Set(agent.KeyringService, "noel", publishCallerToken); err != nil {
		t.Fatalf("put the token in the ring: %v", err)
	}
	return deps
}

// runPublishCLI drives ONE caf publish invocation against injected deps and
// hands back the exit code plus both streams — exactly what main() does with
// the real process, minus the process.
func runPublishCLI(t *testing.T, deps publishDeps, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion, Context: context.Background()}
	err := newPublishCommand(deps).Execute(env, args)
	return codeFor(*env, err), out.String(), errOut.String()
}

// decodePublishReport parses the one JSON document a --json run must print.
func decodePublishReport(t *testing.T, stdout string) publish.Report {
	t.Helper()
	var report publish.Report
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	return report
}

// The approval-card pattern's first half: a dry run prints the proposal — the
// registration, the tag, the edges — and writes nothing. The publisher's call
// list is the assertion: an empty list is the whole proof.
func TestDryRunWritesNothing(t *testing.T) {
	pub := &fakePublishPublisher{}
	deps := publishTestEnv(t, pub)
	project := publishTestProject(t, publishFullManifest)

	code, stdout, stderr := runPublishCLI(t, deps, "--dry-run", project)

	if code != exitSuccess {
		t.Errorf("exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}
	if len(pub.calls) != 0 {
		t.Errorf("a dry run wrote to the catalog: %v", pub.calls)
	}
	for _, want := range []string{"proposal for `app`", "v1.0.0", "gateway ^0.1.0", "identity ^0.1.0"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the proposal does not name %q\ngot:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "dry run") {
		t.Errorf("stderr does not say the run was a dry run\ngot:\n%s", stderr)
	}
}

// Under --json the dry run is ONE document: the proposal, no steps, published
// false, and the refusal that says what this run was. A second document before
// it would be two documents where the contract is one.
func TestDryRunJSONIsOneDocument(t *testing.T) {
	pub := &fakePublishPublisher{}
	deps := publishTestEnv(t, pub)
	project := publishTestProject(t, publishFullManifest)

	code, stdout, _ := runPublishCLI(t, deps, "--dry-run", "--json", project)

	if code != exitSuccess {
		t.Errorf("exit code = %d, want %d", code, exitSuccess)
	}
	report := decodePublishReport(t, stdout)
	if report.Published {
		t.Error("a dry run reports itself as published")
	}
	if len(report.Steps) != 0 {
		t.Errorf("a dry run has steps: %v", report.Steps)
	}
	if report.Refusal == "" {
		t.Error("the dry-run document carries no refusal saying what it was")
	}
	if report.Proposal.Tag != "v1.0.0" {
		t.Errorf("tag = %q, want v1.0.0", report.Proposal.Tag)
	}
	if len(report.Proposal.Requirements) != 2 {
		t.Errorf("proposed %d edges, want 2", len(report.Proposal.Requirements))
	}
	if len(pub.calls) != 0 {
		t.Errorf("a dry run wrote to the catalog: %v", pub.calls)
	}
}

// The user ruling of 2026-10-11 ("for cafaye i think we should make
// everything non interactive by default as it shall mostly be used by
// agents") removed the confirmation from the default path: an agent runs
// `caf publish`, it publishes. No prompt anywhere in the default path —
// not even a TTY-gated one. The test hands a confirmation that fails the
// run if it is ever called, so any code path that can block on a TTY is
// this test going red.
func TestDefaultPublishesWithoutPrompting(t *testing.T) {
	pub := &fakePublishPublisher{}
	deps := publishTestEnv(t, pub)
	deps.confirm = func(string) (bool, error) {
		t.Error("the default path asked for a confirmation — publishing is non-interactive by default")
		return false, errNoPrompt
	}
	project := publishTestProject(t, publishFullManifest)

	code, stdout, _ := runPublishCLI(t, deps, "--json", project)

	if code != exitSuccess {
		t.Errorf("exit code = %d, want %d", code, exitSuccess)
	}
	want := []string{publish.OpRegister, publish.OpVersion, publish.OpRequirement, publish.OpRequirement}
	if strings.Join(pub.calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want %v — the default run must publish the three writes in order", pub.calls, want)
	}
	report := decodePublishReport(t, stdout)
	if !report.Published {
		t.Errorf("a default publish is not published: %v", report.Steps)
	}
}

// `--yes` is retained as an accepted no-op, so any script written while the
// flag was the escape hatch keeps working. It publishes exactly what the
// default publishes — no more, no less.
func TestYesIsAnAcceptedNoOp(t *testing.T) {
	pub := &fakePublishPublisher{}
	deps := publishTestEnv(t, pub)
	deps.confirm = func(string) (bool, error) {
		t.Error("--yes asked for a confirmation — the flag is an accepted no-op")
		return false, errNoPrompt
	}
	project := publishTestProject(t, publishFullManifest)

	code, stdout, _ := runPublishCLI(t, deps, "--yes", "--json", project)

	if code != exitSuccess {
		t.Errorf("exit code = %d, want %d", code, exitSuccess)
	}
	want := []string{publish.OpRegister, publish.OpVersion, publish.OpRequirement, publish.OpRequirement}
	if strings.Join(pub.calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want %v", pub.calls, want)
	}
	report := decodePublishReport(t, stdout)
	if !report.Published {
		t.Errorf("a --yes publish is not published: %v", report.Steps)
	}
}

// --yes is the escape hatch a non-interactive run must use: the three writes
// run in order — register, publish the version, declare each edge — the token
// is presented on every call, and the report says published.
func TestYesPublishesTheThreeWritesInOrder(t *testing.T) {
	pub := &fakePublishPublisher{}
	deps := publishTestEnv(t, pub)
	project := publishTestProject(t, publishFullManifest)

	code, stdout, _ := runPublishCLI(t, deps, "--yes", "--json", project)

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d", code, exitSuccess)
	}
	want := []string{publish.OpRegister, publish.OpVersion, publish.OpRequirement, publish.OpRequirement}
	if strings.Join(pub.calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want %v", pub.calls, want)
	}
	if pub.token != publishCallerToken {
		t.Errorf("the token reached the writes (len %d, want %d)", len(pub.token), len(publishCallerToken))
	}
	if pub.tag != "v1.0.0" {
		t.Errorf("version write got tag %q, want v1.0.0", pub.tag)
	}
	if len(pub.requirements) != 2 || pub.requirements[0].Name != "gateway" || pub.requirements[1].Name != "identity" {
		t.Errorf("edges declared = %v, want gateway then identity", pub.requirements)
	}
	report := decodePublishReport(t, stdout)
	if !report.Published {
		t.Errorf("a clean publish is not published: %v", report.Steps)
	}
	if len(report.Steps) != 4 {
		t.Fatalf("report has %d steps, want 4", len(report.Steps))
	}
	for _, step := range report.Steps {
		if step.Status != publish.StatusCreated {
			t.Errorf("step %s `%s` = %q, want created", step.Operation, step.Name, step.Status)
		}
	}
}

// The courier rule, asserted over the whole run: the token reaches the writes
// and appears in NEITHER stream. A build that printed it would pass every
// other assertion in this file.
func TestTheTokenIsNeverPrinted(t *testing.T) {
	pub := &fakePublishPublisher{}
	deps := publishTestEnv(t, pub)
	project := publishTestProject(t, publishFullManifest)

	_, stdout, stderr := runPublishCLI(t, deps, "--yes", "--json", project)

	if strings.Contains(stdout, publishCallerToken) || strings.Contains(stderr, publishCallerToken) {
		t.Error("the caller's token reached a stream")
	}
}

// A 409 carrying pantry's own idempotent phrase is "the write already
// happened": a report line and the sequence continues. The re-publish is the
// common case this exists for.
func TestAnIdempotent409IsAReportLine(t *testing.T) {
	pub := &fakePublishPublisher{}
	pub.registerErr = &resolve.PantryError{
		Status: http.StatusConflict,
		Code:   "conflict",
		Detail: "app is already registered",
	}
	deps := publishTestEnv(t, pub)
	project := publishTestProject(t, publishFullManifest)

	code, stdout, _ := runPublishCLI(t, deps, "--yes", "--json", project)

	if code != exitSuccess {
		t.Errorf("exit code = %d, want %d — an idempotent 409 is not a failure", code, exitSuccess)
	}
	if len(pub.calls) != 4 {
		t.Errorf("the sequence stopped after the 409: %v", pub.calls)
	}
	report := decodePublishReport(t, stdout)
	if !report.Published {
		t.Errorf("a fully idempotent re-publish is not published: %v", report.Steps)
	}
	if report.Steps[0].Status != publish.StatusAlreadyExists {
		t.Errorf("register step = %q, want already_exists", report.Steps[0].Status)
	}
	if !strings.Contains(report.Steps[0].Detail, "already registered") {
		t.Errorf("the step does not carry the server's own detail:\n%s", report.Steps[0].Detail)
	}
}

// The 409 that is a REFUSAL arrives under the same status: the ordering rule
// ("no published semver version") is not the write having happened. The step
// is failed, the sequence stops, and the report says not published.
func TestAnOrdering409StopsTheSequence(t *testing.T) {
	pub := &fakePublishPublisher{}
	pub.publishErr = &resolve.PantryError{
		Status: http.StatusConflict,
		Code:   "conflict",
		Detail: "no published semver version of app to declare an edge from",
	}
	deps := publishTestEnv(t, pub)
	project := publishTestProject(t, publishFullManifest)

	code, stdout, _ := runPublishCLI(t, deps, "--yes", "--json", project)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d — the ordering rule is a refusal", code, exitFailure)
	}
	if len(pub.calls) != 2 {
		t.Errorf("the edges ran after the version failed: %v", pub.calls)
	}
	report := decodePublishReport(t, stdout)
	if report.Published {
		t.Error("a refused publish reports itself as published")
	}
	if report.Steps[1].Status != publish.StatusFailed {
		t.Errorf("version step = %q, want failed", report.Steps[1].Status)
	}
	if !strings.Contains(report.Steps[1].Detail, "no published semver version") {
		t.Errorf("the step does not carry the server's own detail:\n%s", report.Steps[1].Detail)
	}
}

// Any other failure names itself with the server's own code and detail — an
// agent reading the report must be able to grep pantry's log with it.
func TestA401NamesTheServer(t *testing.T) {
	pub := &fakePublishPublisher{}
	pub.registerErr = &resolve.PantryError{
		Status: http.StatusUnauthorized,
		Code:   "unauthorized",
		Detail: "the bearer token is not accepted for publishing",
	}
	deps := publishTestEnv(t, pub)
	project := publishTestProject(t, publishFullManifest)

	code, stdout, _ := runPublishCLI(t, deps, "--yes", "--json", project)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if len(pub.calls) != 1 {
		t.Errorf("the sequence continued past the 401: %v", pub.calls)
	}
	report := decodePublishReport(t, stdout)
	step := report.Steps[0]
	if step.Status != publish.StatusFailed {
		t.Errorf("step = %q, want failed", step.Status)
	}
	if !strings.Contains(step.Detail, "unauthorized") || !strings.Contains(step.Detail, "bearer token") {
		t.Errorf("the step does not carry the server's code and detail:\n%s", step.Detail)
	}
}

// The manifest is validated by the contract linter's own gate BEFORE anything
// is proposed or written: a broken cafaye.yml is a refusal that names the
// file, and the publisher is never reached.
func TestAnInvalidManifestIsRefusedBeforeAnyWrite(t *testing.T) {
	pub := &fakePublishPublisher{}
	deps := publishTestEnv(t, pub)
	project := publishTestProject(t, "name: app\ncore: ^0.2.0\n")

	code, _, stderr := runPublishCLI(t, deps, "--dry-run", project)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "cafaye.yml") {
		t.Errorf("the refusal does not name the manifest\ngot:\n%s", stderr)
	}
	if len(pub.calls) != 0 {
		t.Errorf("an invalid manifest reached the catalog: %v", pub.calls)
	}
}

// A directory with no manifest is a refusal that names the way out: caf init
// creates one. The proposal is not printed, because there is nothing to
// propose.
func TestADirectoryWithNoManifestNamesCafInit(t *testing.T) {
	pub := &fakePublishPublisher{}
	deps := publishTestEnv(t, pub)

	code, stdout, stderr := runPublishCLI(t, deps, "--dry-run", t.TempDir())

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "caf init") {
		t.Errorf("the refusal does not name caf init\ngot:\n%s", stderr)
	}
	if strings.Contains(stdout, "proposal") {
		t.Errorf("a missing manifest produced a proposal:\n%s", stdout)
	}
	if len(pub.calls) != 0 {
		t.Errorf("a missing manifest reached the catalog: %v", pub.calls)
	}
}

// --tag is the publisher's own act, verbatim: the flag reaches the version
// write unchanged, leading v included.
func TestTheTagFlagReachesTheVersionWrite(t *testing.T) {
	pub := &fakePublishPublisher{}
	deps := publishTestEnv(t, pub)
	project := publishTestProject(t, publishFullManifest)

	code, stdout, _ := runPublishCLI(t, deps, "--yes", "--json", "--tag", "v2.3.4", project)

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d", code, exitSuccess)
	}
	if pub.tag != "v2.3.4" {
		t.Errorf("version write got tag %q, want v2.3.4", pub.tag)
	}
	if report := decodePublishReport(t, stdout); report.Proposal.Tag != "v2.3.4" {
		t.Errorf("proposal tag = %q, want v2.3.4", report.Proposal.Tag)
	}
}

// Without --tag the command derives the tag from the repository's git tags.
// The derivation is injected here; what is asserted is that its answer is the
// proposal's tag, verbatim.
func TestTheTagIsDerivedWhenNoFlagIsGiven(t *testing.T) {
	pub := &fakePublishPublisher{}
	deps := publishTestEnv(t, pub)
	deps.tags = func(dir string) (string, error) {
		if dir == "" {
			t.Error("the derivation was handed no project directory")
		}
		return "v3.1.0", nil
	}
	project := publishTestProject(t, publishFullManifest)

	code, stdout, _ := runPublishCLI(t, deps, "--yes", "--json", project)

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d", code, exitSuccess)
	}
	if report := decodePublishReport(t, stdout); report.Proposal.Tag != "v3.1.0" {
		t.Errorf("proposal tag = %q, want the derived v3.1.0", report.Proposal.Tag)
	}
}

// The kind is derived from the contract surface when the manifest declares
// one: HTTP and events in is `both`, and --kind is not asked for.
func TestTheKindIsDerivedFromTheSurface(t *testing.T) {
	pub := &fakePublishPublisher{}
	deps := publishTestEnv(t, pub)
	project := publishTestProject(t, publishFullManifest)

	code, stdout, _ := runPublishCLI(t, deps, "--yes", "--json", project)

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d", code, exitSuccess)
	}
	if report := decodePublishReport(t, stdout); report.Proposal.Registration.Kind != publish.KindBoth {
		t.Errorf("kind = %q, want both", report.Proposal.Registration.Kind)
	}
}

// The curated case: a manifest with no contract surface cannot say whether it
// is a binary, a server or an event worker, so the command refuses and names
// --kind rather than guessing a claim the publisher never made.
func TestTheKindFlagIsRequiredWithoutASurface(t *testing.T) {
	pub := &fakePublishPublisher{}
	deps := publishTestEnv(t, pub)
	project := publishTestProject(t, publishBareManifest)

	code, _, stderr := runPublishCLI(t, deps, "--dry-run", project)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "--kind") {
		t.Errorf("the refusal does not name --kind\ngot:\n%s", stderr)
	}
	if len(pub.calls) != 0 {
		t.Errorf("a surface-less manifest reached the catalog: %v", pub.calls)
	}
}

// And the flag fills it: the proposal carries exactly what the publisher
// stated, verbatim.
func TestTheKindFlagFillsTheCuratedCase(t *testing.T) {
	pub := &fakePublishPublisher{}
	deps := publishTestEnv(t, pub)
	project := publishTestProject(t, publishBareManifest)

	code, stdout, _ := runPublishCLI(t, deps, "--yes", "--json", "--kind", publish.KindCLI, project)

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d", code, exitSuccess)
	}
	if report := decodePublishReport(t, stdout); report.Proposal.Registration.Kind != publish.KindCLI {
		t.Errorf("kind = %q, want cli", report.Proposal.Registration.Kind)
	}
}

// The registration caf sends is the manifest's own facts — name, language,
// core, repository, owner — not a restatement of them.
func TestTheRegistrationCarriesTheManifestsFacts(t *testing.T) {
	pub := &fakePublishPublisher{}
	deps := publishTestEnv(t, pub)
	project := publishTestProject(t, publishFullManifest)

	code, _, _ := runPublishCLI(t, deps, "--yes", project)

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d", code, exitSuccess)
	}
	reg := pub.reg
	for _, tc := range []struct{ name, got, want string }{
		{"name", reg.Name, "app"},
		{"language", reg.Language, "go"},
		{"core", reg.Core, "^0.2.0"},
		{"repository url", reg.Repository.URL, "git@github.com:cafaye/app.git"},
		{"owner team", reg.Owner.Team, "app"},
		{"owner contact", reg.Owner.Contact, "app@cafaye.com"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	if reg.ManifestSHA256 == "" || len(reg.ManifestSHA256) != 64 {
		t.Errorf("manifest_sha256 = %q, want a sha256 digest", reg.ManifestSHA256)
	}
	if reg.State != publish.StatePublished {
		t.Errorf("state = %q, want published — Docker Hub's model: push and it is public", reg.State)
	}
}

// No project argument is a usage error and exits 2, like its siblings.
func TestNoProjectArgumentIsAUsageError(t *testing.T) {
	deps := publishTestEnv(t, &fakePublishPublisher{})

	code, _, stderr := runPublishCLI(t, deps)

	if code != exitUsage {
		t.Errorf("exit code = %d, want %d (stderr: %s)", code, exitUsage, stderr)
	}
}

// The command's help is the answer to "what do I type": it renders, succeeds,
// and names the flags that make the decision table possible.
func TestPublishHelpRenders(t *testing.T) {
	var out bytes.Buffer
	env := &Env{Stdout: &out, Version: testVersion, Context: context.Background()}
	err := newPublishCommand(publishDeps{}).Execute(env, []string{"--help"})
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	for _, want := range []string{"caf publish", "--dry-run", "--yes", "--json", "--tag", "--kind"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help does not name %q\ngot:\n%s", want, out.String())
		}
	}
}

// The registry lists publish between its siblings, and the router reaches it
// by the word the user types — the wiring a pipeline depends on.
func TestPublishIsWiredInTheRegistry(t *testing.T) {
	c, found := lookupCommand(Commands(), "publish")
	if !found {
		t.Fatal("publish is not in the registry")
	}
	if c.Name != "publish" {
		t.Errorf("Name = %q, want publish", c.Name)
	}
	if !strings.Contains(c.Usage, "--yes") {
		t.Errorf("the usage line does not name --yes:\n%s", c.Usage)
	}
}
