package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anywaye/cafe/internal/agent"
	"github.com/anywaye/cafe/internal/docs"
)

// docsCallerToken is a string no real credential looks like and every fake
// in this file answers with it. The assertions are that it reaches the
// exchange and reaches NEITHER stream — the same courier rule the pantry
// publisher's tests hold.
var docsCallerToken = "anywaye_" + strings.Repeat("d", 40)

// docsExchangedToken is what the identity fake mints for press: a JWS-shaped
// string distinct from the session, so a test can tell which credential
// reached press. It is asserted to reach the ingest AND neither stream.
var docsExchangedToken = "eyJhbGciOiJSUzI1NiJ9." + strings.Repeat("e", 40) + ".sig"

// fakeDocsIdentity is identity's half of the flow, recorded: the two calls
// made before press — the accounts list and the session-to-token exchange.
// Each answer and each failure is plantable, which is what makes the whole
// decision table (one account, many, a foreign id, a dead session) testable
// without a network.
type fakeDocsIdentity struct {
	accounts    []docs.Account
	accountsErr error
	exchange    docs.Exchange
	exchangeErr error

	accountsCalls int
	exchangeCalls int
	gotSession    string
	gotAccount    string
	gotAudience   string
}

func (f *fakeDocsIdentity) Accounts(_ context.Context, session string) ([]docs.Account, error) {
	f.accountsCalls++
	f.gotSession = session
	if f.accountsErr != nil {
		return nil, f.accountsErr
	}
	return f.accounts, nil
}

func (f *fakeDocsIdentity) ExchangeToken(_ context.Context, session, accountID, audience string) (docs.Exchange, error) {
	f.exchangeCalls++
	f.gotSession = session
	f.gotAccount = accountID
	f.gotAudience = audience
	if f.exchangeErr != nil {
		return docs.Exchange{}, f.exchangeErr
	}
	return f.exchange, nil
}

var _ docs.Identity = (*fakeDocsIdentity)(nil)

// docsOneAccount is the fresh-signup case: a session that belongs to exactly
// one account, and it is personal.
func docsOneAccount() []docs.Account {
	return []docs.Account{{ID: "acct_0001", Name: "Noel", Slug: "noel", Personal: true, Role: "owner"}}
}

// fakeDocsIdentityFor is the default identity fake: one personal account and
// a minted token.
func fakeDocsIdentityFor() *fakeDocsIdentity {
	return &fakeDocsIdentity{
		accounts: docsOneAccount(),
		exchange: docs.Exchange{Token: docsExchangedToken},
	}
}

// A manifest for a public repository: the pages ship as docs. The shape is
// the schema-valid minimum — name, language, core, repository, owner —
// because a docs tree is not a service and carries no contract surface.
const docsPublicManifest = `name: pages
description: The public pages.
language: go
core: ^0.2.0
repository:
  url: git@github.com:anywaye/pages.git
  defaultBranch: master
  visibility: public
owner:
  team: pages
  contact: pages@anywaye.com
`

// A manifest for a private repository: the pages ship as notes.
const docsPrivateManifest = `name: fieldnotes
description: The private notes.
language: go
core: ^0.2.0
repository:
  url: git@github.com:anywaye/fieldnotes.git
  defaultBranch: master
  visibility: private
owner:
  team: pages
  contact: pages@anywaye.com
`

// fakeDocsPublisher is the seam the decision-table tests drive: it records
// the ingest it was handed and the token it was handed it with, and answers
// with whatever the case planted. Nothing here touches a network, a git
// repository or a terminal.
type fakeDocsPublisher struct {
	calls   int
	token   string
	ingests []docs.Ingest

	receipt docs.Receipt
	err     error
}

func (f *fakeDocsPublisher) Publish(_ context.Context, token string, ingest docs.Ingest) (docs.Receipt, error) {
	f.calls++
	f.token = token
	f.ingests = append(f.ingests, ingest)
	if f.err != nil {
		return docs.Receipt{}, f.err
	}
	return f.receipt, nil
}

var _ docs.Publisher = (*fakeDocsPublisher)(nil)

// docsTestProject writes a anywaye.yml plus the page files into a directory
// the test owns and returns the directory.
func docsTestProject(t *testing.T, manifest string, pages map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "anywaye.yml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write the manifest: %v", err)
	}
	for rel, body := range pages {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// docsTestPages is the shared page body: markdown with frontmatter, which is
// all the tarball ever holds.
const docsTestPage = `---
title: Hello
---

Hello.
`

// docsTestEnv builds the deps one command needs: a directory it owns, an
// in-memory keyring holding the caller's session under a registered session,
// an identity that answers the accounts list and the exchange without a
// network, a publisher that answers without a network and a version
// derivation that needs no git repository. Nothing here touches the machine's
// config directory, its keychain, the network, a repository — or a terminal:
// since the 2026-10-11 ruling there is no confirmation to inject, and a test
// that could answer a prompt would be a test for a prompt that must not
// exist.
func docsTestEnv(t *testing.T, pub *fakeDocsPublisher) docsDeps {
	t.Helper()
	deps := docsTestEnvWithIdentity(t, pub, fakeDocsIdentityFor())
	return deps
}

// docsTestEnvWithIdentity is docsTestEnv with the identity fake supplied, for
// the cases that plant a different accounts list or a different answer.
func docsTestEnvWithIdentity(t *testing.T, pub *fakeDocsPublisher, identity *fakeDocsIdentity) docsDeps {
	t.Helper()
	dir := t.TempDir()
	ring := newAgentTestRing()
	deps := docsDeps{
		dir:       dir,
		ring:      ring,
		identity:  identity,
		publisher: pub,
		tags:      func(string) (string, error) { return "v1.0.0", nil },
	}
	cfg := agent.Config{
		AgentSessions:      map[string]agent.Session{"noel": {Name: "noel", TokenRef: "noel"}},
		ActiveAgentSession: "noel",
	}
	if err := cfg.Save(dir); err != nil {
		t.Fatalf("save the session: %v", err)
	}
	if err := ring.Set(agent.KeyringService, "noel", docsCallerToken); err != nil {
		t.Fatalf("put the token in the ring: %v", err)
	}
	return deps
}

// docsTestEnvWithoutSession is docsTestEnv with no registered session: the
// keyring is empty and the config holds nothing, which is the machine that
// has never logged in.
func docsTestEnvWithoutSession(t *testing.T, pub *fakeDocsPublisher, identity *fakeDocsIdentity) docsDeps {
	t.Helper()
	dir := t.TempDir()
	return docsDeps{
		dir:       dir,
		ring:      newAgentTestRing(),
		identity:  identity,
		publisher: pub,
		tags:      func(string) (string, error) { return "v1.0.0", nil },
	}
}

// runDocsPublishCLI drives ONE cafe docs publish invocation against injected
// deps and hands back the exit code plus both streams — exactly what main()
// does with the real process, minus the process. It goes through the verb's
// own command rather than the group's dispatcher, so the cases below are
// about the publish decision table; the dispatcher has its own cases at the
// foot of this file.
func runDocsPublishCLI(t *testing.T, deps docsDeps, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion, Context: context.Background()}
	err := newDocsPublishCommand(deps).Execute(env, args)
	return codeFor(*env, err), out.String(), errOut.String()
}

// decodeDocsReceipt parses the receipt JSON the command printed on stdout.
// The plan precedes it on the same stream, so the decode starts at the
// first line that opens a document — a machine parsing it has no way to
// skip a prose line somebody helpfully added, which is why the receipt is
// the stream's last value and the only JSON in it.
func decodeDocsReceipt(t *testing.T, stdout string) docs.Receipt {
	t.Helper()
	start := strings.Index(stdout, "{")
	if start < 0 {
		t.Fatalf("stdout holds no JSON document:\n%s", stdout)
	}
	var receipt docs.Receipt
	decoder := json.NewDecoder(strings.NewReader(stdout[start:]))
	if err := decoder.Decode(&receipt); err != nil {
		t.Fatalf("the receipt is not one JSON document: %v\n%s", err, stdout)
	}
	return receipt
}

// A docs repository ships as docs: run in a public repository and the kind
// is docs, the project is the manifest's name, the version is the derived
// tag, the SESSION is exchanged for a press token and THAT token is presented
// to press, and the receipt is JSON on stdout.
func TestADocsRepoShipsAsDocs(t *testing.T) {
	pub := &fakeDocsPublisher{receipt: docs.Receipt{ID: "ing_01", Kind: "docs", SHA256: "abc", Size: 12, State: "ready"}}
	identity := fakeDocsIdentityFor()
	deps := docsTestEnvWithIdentity(t, pub, identity)
	project := docsTestProject(t, docsPublicManifest, map[string]string{
		"src/content/docs/index.md": docsTestPage,
	})

	code, stdout, stderr := runDocsPublishCLI(t, deps, project)

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}
	if pub.calls != 1 {
		t.Fatalf("the ingest ran %d times, want exactly once", pub.calls)
	}
	ingest := pub.ingests[0]
	if ingest.Kind != docs.KindDocs {
		t.Errorf("kind = %q, want docs: a public repository ships as docs", ingest.Kind)
	}
	if ingest.Project != "pages" {
		t.Errorf("project = %q, want the manifest's name", ingest.Project)
	}
	if ingest.Version != "v1.0.0" {
		t.Errorf("version = %q, want the derived tag", ingest.Version)
	}
	// The exchange is the whole point: the session goes to identity, and the
	// token identity mints goes to press. A run that sent the session to press
	// would pass every other assertion here.
	if identity.accountsCalls != 1 || identity.exchangeCalls != 1 {
		t.Errorf("identity calls = %d accounts, %d exchanges; want exactly one each",
			identity.accountsCalls, identity.exchangeCalls)
	}
	if identity.gotSession != docsCallerToken {
		t.Error("the session was not presented to identity")
	}
	if identity.gotAccount != "acct_0001" {
		t.Errorf("identity was asked to mint for %q, want the session's one account", identity.gotAccount)
	}
	if identity.gotAudience != docs.PressAudience {
		t.Errorf("audience = %q, want %q: the audience is the receiver's, not a preference",
			identity.gotAudience, docs.PressAudience)
	}
	if pub.token != docsExchangedToken {
		t.Error("the exchanged token did not reach the ingest: press must see the mint, never the session")
	}
	receipt := decodeDocsReceipt(t, stdout)
	if receipt.ID != "ing_01" || receipt.State != "ready" {
		t.Errorf("the receipt is not press's answer: %+v", receipt)
	}
	if !strings.Contains(stderr, "ing_01") {
		t.Errorf("stderr does not name the ingest:\n%s", stderr)
	}
}

// A notes repository ships as notes: run in a private repository and the
// kind is notes, and the tarball holds exactly the root pages plus
// projects/ — asserted on the bytes the fake received, which is what makes
// "exactly the page files and nothing else" a fact about the send rather
// than about a walk.
func TestANotesRepoShipsAsNotes(t *testing.T) {
	pub := &fakeDocsPublisher{receipt: docs.Receipt{ID: "ing_02", Kind: "notes", SHA256: "def", Size: 9, State: "ready"}}
	deps := docsTestEnv(t, pub)
	project := docsTestProject(t, docsPrivateManifest, map[string]string{
		"a.md":                  docsTestPage,
		"main.go":               "package main",
		"drafts/c.md":           docsTestPage,
		"projects/p1.md":        docsTestPage,
		"node_modules/x/index":  "js",
		"projects/nested/d.md":  docsTestPage,
		"src/content/docs/z.md": docsTestPage,
	})

	code, _, stderr := runDocsPublishCLI(t, deps, project)

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}
	if pub.calls != 1 {
		t.Fatalf("the ingest ran %d times, want exactly once", pub.calls)
	}
	ingest := pub.ingests[0]
	if ingest.Kind != docs.KindNotes {
		t.Errorf("kind = %q, want notes: a private repository ships as notes", ingest.Kind)
	}
	names, err := docs.ListTar(ingest.Content)
	if err != nil {
		t.Fatalf("read the sent tarball: %v", err)
	}
	want := []string{"a.md", "projects/nested/d.md", "projects/p1.md"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("the sent tarball holds %v, want exactly %v", names, want)
	}
}

// The override wins: a private repository with --visibility public ships as
// docs. The flag is the operator stating what the repository is, and the
// file defers to it.
func TestTheVisibilityFlagOverridesTheFile(t *testing.T) {
	pub := &fakeDocsPublisher{receipt: docs.Receipt{ID: "ing_03", Kind: "docs", State: "ready"}}
	deps := docsTestEnv(t, pub)
	project := docsTestProject(t, docsPrivateManifest, map[string]string{
		"src/content/docs/index.md": docsTestPage,
	})

	code, _, stderr := runDocsPublishCLI(t, deps, "--visibility", "public", project)

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}
	if pub.ingests[0].Kind != docs.KindDocs {
		t.Errorf("kind = %q, want docs: the flag overrides the file", pub.ingests[0].Kind)
	}
}

// A third spelling is refused before anything is packed or sent: press
// serves no third kind.
func TestAThirdVisibilityIsRefused(t *testing.T) {
	pub := &fakeDocsPublisher{}
	deps := docsTestEnv(t, pub)
	project := docsTestProject(t, docsPublicManifest, map[string]string{
		"src/content/docs/index.md": docsTestPage,
	})

	code, _, stderr := runDocsPublishCLI(t, deps, "--visibility", "internal", project)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "internal") {
		t.Errorf("the refusal does not name what was typed\ngot:\n%s", stderr)
	}
	if pub.calls != 0 {
		t.Errorf("a refused visibility reached press: %d calls", pub.calls)
	}
}

// An unreadable anywaye.yml fails loudly rather than guessing: the command
// never infers a kind without the manifest, because a guess in the public
// direction is a leak. A path that is a directory where the manifest should
// be is unreadable on every machine — unlike a mode bit, which root
// ignores — so the refusal is asserted on that.
func TestAnUnreadableManifestFailsLoudly(t *testing.T) {
	pub := &fakeDocsPublisher{}
	deps := docsTestEnv(t, pub)
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "anywaye.yml"), 0o755); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runDocsPublishCLI(t, deps, project)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "anywaye.yml") {
		t.Errorf("the refusal does not name the manifest\ngot:\n%s", stderr)
	}
	if strings.Contains(stdout, "plan for") {
		t.Errorf("an unreadable manifest produced a plan:\n%s", stdout)
	}
	if pub.calls != 0 {
		t.Errorf("an unreadable manifest reached press: %d calls", pub.calls)
	}
}

// A directory with no manifest is a refusal that names the way out: cafe
// init creates one. The plan is not printed, because there is nothing to
// plan.
func TestADirectoryWithNoManifestNamesCafeInit(t *testing.T) {
	pub := &fakeDocsPublisher{}
	deps := docsTestEnv(t, pub)

	code, stdout, stderr := runDocsPublishCLI(t, deps, t.TempDir())

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "cafe init") {
		t.Errorf("the refusal does not name cafe init\ngot:\n%s", stderr)
	}
	if strings.Contains(stdout, "plan for") {
		t.Errorf("a missing manifest produced a plan:\n%s", stdout)
	}
	if pub.calls != 0 {
		t.Errorf("a missing manifest reached press: %d calls", pub.calls)
	}
}

// The proposal mode: a dry run prints the plan — the kind, the version, the
// pages — and sends nothing. The publisher's call count is the assertion:
// zero calls is the whole proof.
func TestDocsDryRunSendsNothing(t *testing.T) {
	pub := &fakeDocsPublisher{}
	deps := docsTestEnv(t, pub)
	project := docsTestProject(t, docsPublicManifest, map[string]string{
		"src/content/docs/index.md": docsTestPage,
		"src/content/docs/g/a.md":   docsTestPage,
	})

	code, stdout, stderr := runDocsPublishCLI(t, deps, "--dry-run", project)

	if code != exitSuccess {
		t.Errorf("exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}
	if pub.calls != 0 {
		t.Errorf("a dry run sent to press: %d calls", pub.calls)
	}
	for _, want := range []string{"plan for `pages`", "docs", "v1.0.0", "src/content/docs/index.md", "src/content/docs/g/a.md"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the plan does not name %q\ngot:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "dry run") {
		t.Errorf("stderr does not say the run was a dry run\ngot:\n%s", stderr)
	}
	if strings.Contains(stdout, `"id"`) {
		t.Errorf("a dry run printed a receipt:\n%s", stdout)
	}
}

// A 401 from press surfaces as "run cafe login", not a stack trace: the
// token is missing or press does not accept it, and the remedy is to log
// in. The server's own message rides along, because a failure that cannot
// be grepped against press's log is a failure nobody can act on.
func TestA401SurfacesTheLoginHint(t *testing.T) {
	pub := &fakeDocsPublisher{err: &docs.PressError{
		Status: 401,
		Code:   "unauthorized",
		Detail: "the bearer token is not accepted",
	}}
	deps := docsTestEnv(t, pub)
	project := docsTestProject(t, docsPublicManifest, map[string]string{
		"src/content/docs/index.md": docsTestPage,
	})

	code, _, stderr := runDocsPublishCLI(t, deps, project)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "cafe login") {
		t.Errorf("stderr does not say to run cafe login\ngot:\n%s", stderr)
	}
	if !strings.Contains(stderr, "unauthorized") || !strings.Contains(stderr, "bearer token") {
		t.Errorf("stderr does not carry the server's message\ngot:\n%s", stderr)
	}
}

// Any other refusal names itself with the server's own message and stops.
// The plan was already printed — it is not the failure — and no receipt
// follows it.
func TestAnotherRefusalNamesTheServer(t *testing.T) {
	pub := &fakeDocsPublisher{err: &docs.PressError{
		Status: 409,
		Code:   "conflict",
		Detail: "version v1.0.0 is already ingested",
	}}
	deps := docsTestEnv(t, pub)
	project := docsTestProject(t, docsPublicManifest, map[string]string{
		"src/content/docs/index.md": docsTestPage,
	})

	code, stdout, stderr := runDocsPublishCLI(t, deps, project)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "already ingested") {
		t.Errorf("stderr does not carry the server's detail\ngot:\n%s", stderr)
	}
	if strings.Contains(stdout, `"id"`) {
		t.Errorf("a refused publish printed a receipt:\n%s", stdout)
	}
}

// The courier rule, asserted over the whole run: neither the session nor the
// exchanged token appears in EITHER stream. A build that printed the session
// or the mint would pass every other assertion in this file.
func TestDocsTheTokenIsNeverPrinted(t *testing.T) {
	pub := &fakeDocsPublisher{receipt: docs.Receipt{ID: "ing_01", State: "ready"}}
	deps := docsTestEnv(t, pub)
	project := docsTestProject(t, docsPublicManifest, map[string]string{
		"src/content/docs/index.md": docsTestPage,
	})

	_, stdout, stderr := runDocsPublishCLI(t, deps, project)

	for _, secret := range []string{docsCallerToken, docsExchangedToken} {
		if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
			t.Errorf("a credential reached a stream: %s", secret[:8]+"…")
		}
	}
}

// --version is the publisher's own act, verbatim: the flag reaches the
// ingest unchanged.
func TestDocsTheVersionFlagReachesTheIngest(t *testing.T) {
	pub := &fakeDocsPublisher{receipt: docs.Receipt{ID: "ing_01", State: "ready"}}
	deps := docsTestEnv(t, pub)
	project := docsTestProject(t, docsPublicManifest, map[string]string{
		"src/content/docs/index.md": docsTestPage,
	})

	code, stdout, _ := runDocsPublishCLI(t, deps, "--version", "2026.10", project)

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d", code, exitSuccess)
	}
	if pub.ingests[0].Version != "2026.10" {
		t.Errorf("ingest version = %q, want 2026.10", pub.ingests[0].Version)
	}
	if !strings.Contains(stdout, "2026.10") {
		t.Errorf("the plan does not name the version:\n%s", stdout)
	}
}

// Without --version the command derives it from the repository's git tags.
// The derivation is injected here; what is asserted is that its answer is
// the ingest's version, verbatim.
func TestDocsTheVersionIsDerivedWhenNoFlagIsGiven(t *testing.T) {
	pub := &fakeDocsPublisher{receipt: docs.Receipt{ID: "ing_01", State: "ready"}}
	deps := docsTestEnv(t, pub)
	deps.tags = func(dir string) (string, error) {
		if dir == "" {
			t.Error("the derivation was handed no project directory")
		}
		return "v9.9.9", nil
	}
	project := docsTestProject(t, docsPublicManifest, map[string]string{
		"src/content/docs/index.md": docsTestPage,
	})

	code, stdout, _ := runDocsPublishCLI(t, deps, project)

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d", code, exitSuccess)
	}
	if pub.ingests[0].Version != "v9.9.9" {
		t.Errorf("ingest version = %q, want the derived v9.9.9", pub.ingests[0].Version)
	}
	if !strings.Contains(stdout, "v9.9.9") {
		t.Errorf("the plan does not name the derived version:\n%s", stdout)
	}
}

// No project argument is a usage error and exits 2, like its siblings.
func TestDocsNoProjectArgumentIsAUsageError(t *testing.T) {
	deps := docsTestEnv(t, &fakeDocsPublisher{})

	code, _, _ := runDocsPublishCLI(t, deps)

	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
}

// The account decision. A --account flag naming one of several accounts is
// honored: the session is exchanged for THAT account's id, and the minted
// token is what reaches press. The choice is never guessed, so the explicit
// flag is the only way to publish from a session that belongs to more than
// one account.
func TestTheAccountFlagSelectsAmongMany(t *testing.T) {
	pub := &fakeDocsPublisher{receipt: docs.Receipt{ID: "ing_multi", State: "ready"}}
	identity := &fakeDocsIdentity{
		accounts: []docs.Account{
			{ID: "acct_aaaa", Name: "Noel", Slug: "noel", Personal: true, Role: "owner"},
			{ID: "acct_bbbb", Name: "Acme", Slug: "acme", Role: "admin"},
		},
		exchange: docs.Exchange{Token: docsExchangedToken},
	}
	deps := docsTestEnvWithIdentity(t, pub, identity)
	project := docsTestProject(t, docsPublicManifest, map[string]string{
		"src/content/docs/index.md": docsTestPage,
	})

	code, _, stderr := runDocsPublishCLI(t, deps, "--account", "acme", project)

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}
	if identity.gotAccount != "acct_bbbb" {
		t.Errorf("identity minted for %q, want the slug's account id acct_bbbb", identity.gotAccount)
	}
	if pub.token != docsExchangedToken {
		t.Error("the exchanged token did not reach the ingest")
	}
}

// An id is accepted just as a slug is: --account names by either, because the
// caller may have one and not the other.
func TestTheAccountFlagAcceptsAnID(t *testing.T) {
	pub := &fakeDocsPublisher{receipt: docs.Receipt{ID: "ing_multi", State: "ready"}}
	identity := &fakeDocsIdentity{
		accounts: []docs.Account{
			{ID: "acct_aaaa", Name: "Noel", Slug: "noel", Personal: true, Role: "owner"},
			{ID: "acct_bbbb", Name: "Acme", Slug: "acme", Role: "admin"},
		},
		exchange: docs.Exchange{Token: docsExchangedToken},
	}
	deps := docsTestEnvWithIdentity(t, pub, identity)
	project := docsTestProject(t, docsPublicManifest, map[string]string{
		"src/content/docs/index.md": docsTestPage,
	})

	code, _, stderr := runDocsPublishCLI(t, deps, "--account", "acct_bbbb", project)

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}
	if identity.gotAccount != "acct_bbbb" {
		t.Errorf("identity minted for %q, want acct_bbbb", identity.gotAccount)
	}
}

// A session that belongs to several accounts and names none is refused, and
// the refusal renders the list so the next invocation is one flag away. The
// choice between two accounts is a guess about where these pages ship, so
// nothing is exchanged and press hears nothing.
func TestAnAmbiguousAccountIsRefusedAndPressHearsNothing(t *testing.T) {
	pub := &fakeDocsPublisher{}
	identity := &fakeDocsIdentity{
		accounts: []docs.Account{
			{ID: "acct_aaaa", Name: "Noel", Slug: "noel", Personal: true, Role: "owner"},
			{ID: "acct_bbbb", Name: "Acme", Slug: "acme", Role: "admin"},
		},
		exchange: docs.Exchange{Token: docsExchangedToken},
	}
	deps := docsTestEnvWithIdentity(t, pub, identity)
	project := docsTestProject(t, docsPublicManifest, map[string]string{
		"src/content/docs/index.md": docsTestPage,
	})

	code, _, stderr := runDocsPublishCLI(t, deps, project)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	for _, want := range []string{"noel", "acme", "--account"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not name %q\ngot:\n%s", want, stderr)
		}
	}
	if identity.exchangeCalls != 0 {
		t.Errorf("an ambiguous account still exchanged: %d mints", identity.exchangeCalls)
	}
	if pub.calls != 0 {
		t.Errorf("an ambiguous account reached press: %d calls", pub.calls)
	}
}

// A --account naming an account the session is not a member of is refused by
// identity's own mint (404, indistinguishable from non-existent by design),
// and cafe surfaces that refusal and adds the list. Press hears nothing: the
// account was refused before any ingest.
func TestANonMemberAccountIsRefusedWithIdentitysOwnWords(t *testing.T) {
	pub := &fakeDocsPublisher{}
	identity := &fakeDocsIdentity{
		accounts: docsOneAccount(),
		exchangeErr: &docs.IdentityError{
			Status: 404,
			Code:   "not_found",
			Detail: "no such account",
		},
	}
	deps := docsTestEnvWithIdentity(t, pub, identity)
	project := docsTestProject(t, docsPublicManifest, map[string]string{
		"src/content/docs/index.md": docsTestPage,
	})

	code, _, stderr := runDocsPublishCLI(t, deps, "--account", "acct_someone_else", project)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "that account is not one of yours") {
		t.Errorf("the refusal does not say the account is not the session's\ngot:\n%s", stderr)
	}
	if !strings.Contains(stderr, "noel") {
		t.Errorf("the refusal does not render the accounts the session DOES own\ngot:\n%s", stderr)
	}
	if pub.calls != 0 {
		t.Errorf("a refused account reached press: %d calls", pub.calls)
	}
}

// A machine with no session is a refusal that names the way out: run cafe
// login. Nothing is exchanged and press hears nothing — the fake identity and
// the fake press are both asserted at zero calls.
func TestAPublishWithNoSessionNamesCafeLogin(t *testing.T) {
	pub := &fakeDocsPublisher{}
	identity := fakeDocsIdentityFor()
	deps := docsTestEnvWithoutSession(t, pub, identity)
	project := docsTestProject(t, docsPublicManifest, map[string]string{
		"src/content/docs/index.md": docsTestPage,
	})

	code, _, stderr := runDocsPublishCLI(t, deps, project)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "cafe login") {
		t.Errorf("the refusal does not say to run cafe login\ngot:\n%s", stderr)
	}
	if identity.accountsCalls != 0 || identity.exchangeCalls != 0 {
		t.Errorf("a sessionless run called identity: %d accounts, %d exchanges",
			identity.accountsCalls, identity.exchangeCalls)
	}
	if pub.calls != 0 {
		t.Errorf("a sessionless run reached press: %d calls", pub.calls)
	}
}

// An exchange that fails is a failed publish. The identity refusal is named
// verbatim, no fallback session token is sent to press, and — the load-bearing
// assertion — the fake press saw NOTHING.
func TestAFailedExchangeNeverReachesPress(t *testing.T) {
	pub := &fakeDocsPublisher{}
	identity := &fakeDocsIdentity{
		accounts: docsOneAccount(),
		exchangeErr: &docs.IdentityError{
			Status: 401,
			Code:   "unauthorized",
			Detail: "the session is no longer valid",
		},
	}
	deps := docsTestEnvWithIdentity(t, pub, identity)
	project := docsTestProject(t, docsPublicManifest, map[string]string{
		"src/content/docs/index.md": docsTestPage,
	})

	code, _, stderr := runDocsPublishCLI(t, deps, project)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "the session is no longer valid") {
		t.Errorf("the refusal does not carry identity's own detail\ngot:\n%s", stderr)
	}
	if !strings.Contains(stderr, "cafe login") {
		t.Errorf("a dead session does not name the login remedy\ngot:\n%s", stderr)
	}
	if pub.calls != 0 {
		t.Errorf("a failed exchange reached press: %d calls — press must see nothing", pub.calls)
	}
}

// A session whose accounts list is empty cannot act anywhere, and the refusal
// says so rather than minting for an empty id. This is the shape a session
// that outlived its tenancy answers.
func TestASessionWithNoAccountsIsRefused(t *testing.T) {
	pub := &fakeDocsPublisher{}
	identity := &fakeDocsIdentity{accounts: nil, exchange: docs.Exchange{Token: docsExchangedToken}}
	deps := docsTestEnvWithIdentity(t, pub, identity)
	project := docsTestProject(t, docsPublicManifest, map[string]string{
		"src/content/docs/index.md": docsTestPage,
	})

	code, _, stderr := runDocsPublishCLI(t, deps, project)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "cafe login") {
		t.Errorf("the refusal does not name the remedy\ngot:\n%s", stderr)
	}
	if identity.exchangeCalls != 0 {
		t.Errorf("an accountless session exchanged: %d mints", identity.exchangeCalls)
	}
	if pub.calls != 0 {
		t.Errorf("an accountless session reached press: %d calls", pub.calls)
	}
}

// The group: bare `cafe docs` is a question, not a mistake — it prints the
// verbs and succeeds. An unknown verb is a usage error that names the one
// that exists.
func TestDocsGroupDispatch(t *testing.T) {
	newGroup := func() *Command { return newDocsCommand(docsDeps{}) }

	t.Run("bare docs prints the verbs", func(t *testing.T) {
		var out bytes.Buffer
		env := &Env{Stdout: &out, Stderr: &bytes.Buffer{}, Version: testVersion, Context: context.Background()}
		if err := newGroup().Execute(env, nil); err != nil {
			t.Fatalf("bare docs: %v", err)
		}
		if !strings.Contains(out.String(), "docs publish") {
			t.Errorf("the group help does not list docs publish:\n%s", out.String())
		}
	})

	t.Run("an unknown verb names publish", func(t *testing.T) {
		var errOut bytes.Buffer
		env := &Env{Stdout: &bytes.Buffer{}, Stderr: &errOut, Version: testVersion, Context: context.Background()}
		err := newGroup().Execute(env, []string{"serve"})
		if err == nil || !strings.Contains(err.Error(), "publish") {
			t.Errorf("err = %v, want it to name the verb that exists", err)
		}
	})
}

// The wiring a pipeline depends on: the registry reaches the group by the
// word the user types, the group's help lists `docs publish`, and the root
// help lists `docs`.
func TestDocsIsWiredInTheRegistry(t *testing.T) {
	c, found := lookupCommand(Commands(), "docs")
	if !found {
		t.Fatal("docs is not in the registry")
	}
	if c.Name != "docs" {
		t.Errorf("Name = %q, want docs", c.Name)
	}

	code, stdout, _ := runCLI(t, testVersion, "help", "docs")
	if code != exitSuccess {
		t.Fatalf("cafe help docs exited %d", code)
	}
	if !strings.Contains(stdout, "docs publish") {
		t.Errorf("cafe help docs does not list docs publish:\n%s", stdout)
	}

	_, rootOut, _ := runCLI(t, testVersion)
	if !strings.Contains(rootOut, "docs") {
		t.Errorf("the root help does not list docs:\n%s", rootOut)
	}
}

// The verb's help is the answer to "what do I type": it renders, succeeds,
// and names the flags that make the decision table possible.
func TestDocsPublishHelpRenders(t *testing.T) {
	var out bytes.Buffer
	env := &Env{Stdout: &out, Version: testVersion, Context: context.Background()}
	err := newDocsPublishCommand(docsDeps{}).Execute(env, []string{"--help"})
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	for _, want := range []string{"cafe docs publish", "--dry-run", "--visibility", "--version", "--press", "--account"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help does not name %q\ngot:\n%s", want, out.String())
		}
	}
}
