package dev

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The registry is the seam `cafe dev` and `cafe deploy` share and the one place a
// service catalog can arrive from outside this binary. pantry-01 owns the real
// answer; cafe owns the question. These tests pin the question — the shape of
// the document and the behaviour of the catalog — so that when pantry serves it
// the only thing left is a transport.
func TestCatalogResolvesTheServicesItHolds(t *testing.T) {
	registry := alphaRegistry()

	entry, found := registry.Resolve("alpha")
	if !found {
		t.Fatal("Resolve(alpha) not found, want the entry the catalog holds")
	}
	if entry.Image != "ghcr.io/cafaye/alpha:1.2.3" {
		t.Errorf("Image = %q, want the catalog's", entry.Image)
	}
	if _, found := registry.Resolve("nowhere"); found {
		t.Error("Resolve(nowhere) = found, want not found: an empty catalog is not a guess")
	}
}

// A catalog document is the same JSON a registry serves, so a file written by
// `cafe dev -registry-dump` and a response fetched from a registry are the same
// bytes. That is the whole contract between this package and pantry, and it is
// worth a round trip.
func TestCatalogRoundTripsThroughJSON(t *testing.T) {
	want := alphaRegistry()
	want["alpha"] = withDeps(want["alpha"], "cache")
	want["cache"] = Entry{
		Name:        "cache",
		Image:       "ghcr.io/cafaye/cache:0.4.0",
		Port:        6379,
		Publish:     true,
		Command:     []string{"redis-server", "--appendonly", "no"},
		Environment: []Env{{Name: "MAXMEMORY", Value: "64mb"}},
		Volumes:     []string{"cache-data:/data"},
		Healthcheck: &Healthcheck{
			Test: []string{"CMD", "redis-cli", "ping"}, Interval: "2s",
			Timeout: "3s", Retries: 30, StartPeriod: "2s",
		},
		Dependencies: []string{"alpha"},
	}

	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got, err := ReadCatalog(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("ReadCatalog: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("ReadCatalog read %d services, want %d", len(got), len(want))
	}
	if !reflectEqual(t, got["cache"], want["cache"]) {
		t.Errorf("cache round-tripped as %+v, want %+v", got["cache"], want["cache"])
	}
}

// The catalog document is read by a person as often as by cafe, so its keys are
// the ones a registry would use. This test pins the wire shape: a pantry
// response that used `health_check` or `env` would be silently read as a
// service with no healthcheck and no environment, which is the kind of quiet
// divergence that costs an afternoon.
func TestCatalogWireShape(t *testing.T) {
	document := `{
	  "identity": {
	    "name": "identity",
	    "image": "ghcr.io/cafaye/identity:0.4.2",
	    "command": ["/app/identity", "serve"],
	    "port": 8080,
	    "publish": true,
	    "environment": {"DATABASE_URL": "postgres://identity:identity@postgres:5432/identity"},
	    "connections": {"postgres": "IDENTITY_DATABASE_URL"},
	    "volumes": ["identity-data:/var/lib/identity"],
	    "healthcheck": {
	      "test": ["CMD", "curl", "-fsS", "http://localhost:8080/healthz"],
	      "interval": "5s", "timeout": "3s", "retries": 20, "startPeriod": "10s"
	    },
	    "dependencies": ["postgres", "redis"]
	  }
	}`

	catalog, err := ReadCatalog(strings.NewReader(document))
	if err != nil {
		t.Fatalf("ReadCatalog: %v", err)
	}

	entry, found := catalog.Resolve("identity")
	if !found {
		t.Fatal("identity not read from the document")
	}
	if !entry.Publish {
		t.Error("Publish = false, want true")
	}
	if len(entry.Command) != 2 || entry.Command[1] != "serve" {
		t.Errorf("Command = %v, want the two words from the document", entry.Command)
	}
	if got := envOf(entry2Service(entry)); got["DATABASE_URL"] == "" {
		t.Errorf("environment did not survive: %+v", entry.Environment)
	}
	if entry.Healthcheck == nil || len(entry.Healthcheck.Test) != 4 {
		t.Fatalf("Healthcheck = %+v, want the four words from the document", entry.Healthcheck)
	}
	if entry.Healthcheck.StartPeriod != "10s" {
		t.Errorf("startPeriod = %q, want 10s", entry.Healthcheck.StartPeriod)
	}
	if strings.Join(entry.Dependencies, ",") != "postgres,redis" {
		t.Errorf("Dependencies = %v, want postgres and redis", entry.Dependencies)
	}
	if got := entry.Connections["postgres"]; got != "IDENTITY_DATABASE_URL" {
		t.Errorf("Connections[postgres] = %q, want IDENTITY_DATABASE_URL: a field nothing reads on the wire is a field that only works in a test", got)
	}
	if strings.Join(entry.Volumes, ",") != "identity-data:/var/lib/identity" {
		t.Errorf("Volumes = %v, want the named volume", entry.Volumes)
	}
}

func TestReadCatalogRejectsGarbage(t *testing.T) {
	tests := []struct {
		name     string
		document string
		want     string
	}{
		{name: "not json", document: "identity: true", want: "read"},
		{name: "a list rather than a map", document: `["identity"]`, want: "keyed by service name"},
		{
			name:     "an entry that names a different service",
			document: `{"identity": {"name": "billing"}}`,
			want:     "name",
		},
		{
			name:     "a port that is not a number",
			document: `{"identity": {"name": "identity", "port": "8080"}}`,
			want:     "port",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadCatalog(strings.NewReader(tt.document))
			if err == nil {
				t.Fatalf("ReadCatalog(%q) = nil error, want a failure", tt.document)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

// An entry whose key and `name` disagree is a document written by two programs,
// and picking a winner would mean running one service's image under another's
// name. The entry that names itself is the one that runs.
func TestReadCatalogRejectsAnEntryThatNamesAnotherService(t *testing.T) {
	_, err := ReadCatalog(strings.NewReader(`{"alpha": {"name": "beta", "image": "x"}}`))

	if !errors.Is(err, ErrBadCatalog) {
		t.Fatalf("err = %v, want it to wrap ErrBadCatalog", err)
	}
	if !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "beta") {
		t.Errorf("err = %q, want it to name both the key and the service", err)
	}
}

// A catalog file is a developer-facing input, so a missing one is a sentence
// about where cafe looked and what to do, not a bare ENOENT.
func TestReadCatalogFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	writeFile(t, path, `{"alpha": {"name": "alpha", "image": "ghcr.io/cafaye/alpha:1.2.3"}}`)

	catalog, err := ReadCatalogFile(path)
	if err != nil {
		t.Fatalf("ReadCatalogFile: %v", err)
	}
	if _, found := catalog.Resolve("alpha"); !found {
		t.Error("the catalog read from disk does not hold alpha")
	}

	if _, err := ReadCatalogFile(filepath.Join(dir, "absent.json")); err == nil {
		t.Error("ReadCatalogFile(absent) = nil error, want a failure naming the path")
	}
}

// An empty catalog is a legitimate value — it is what `cafe dev` has before
// pantry ships one — so it reads without complaint and plans nothing extra.
func TestReadCatalogAcceptsAnEmptyDocument(t *testing.T) {
	catalog, err := ReadCatalog(strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("ReadCatalog: %v", err)
	}
	if len(catalog) != 0 {
		t.Errorf("catalog = %v, want empty", catalog)
	}
}

func entry2Service(entry Entry) Service {
	return Service{Name: entry.Name, Environment: entry.Environment}
}

func reflectEqual(t *testing.T, got, want any) bool {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Errorf("got  %s\nwant %s", gotJSON, wantJSON)
		return false
	}
	return true
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// The defect this whole block exists for, reproduced rather than described.
//
// A catalog is the one cafe input a person writes by hand, and encoding/json
// drops a key it does not recognise without a sound. So `imag` — a typo, one
// character from the real key — left the entry with no image, and the catalog
// was accepted. The developer was not shown that. They were shown, three layers
// later and from a different command, that "the local registry names no image
// for identity": a claim about the catalog's contents, when the catalog has an
// image in it and the document is misspelled. Sent to edit a file that was
// already correct.
func TestReadCatalogRefusesAMisspelledKey(t *testing.T) {
	_, err := ReadCatalog(strings.NewReader(`{"identity": {"name": "identity", "imag": "cafaye/identity:dev", "port": 8080}}`))

	if !errors.Is(err, ErrBadCatalog) {
		t.Fatalf("err = %v, want it to wrap ErrBadCatalog", err)
	}
	for _, want := range []string{"imag", "identity", "image"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to mention %q", err, want)
		}
	}
}

// The three things the error has to say, and the reason each is asserted
// separately rather than as one substring: `imag` says the typo, `identity` says
// which of the catalog's services holds it, and `image` says what the key should
// have been. An error carrying only the first sends the reader to grep; only the
// first two sends them to edit.
func TestReadCatalogNamesTheServiceAndTheKeysThatWouldHaveWorked(t *testing.T) {
	_, err := ReadCatalog(strings.NewReader(`{
		"courier": {"name": "courier", "image": "cafaye/courier:dev"},
		"identity": {"name": "identity", "imagee": "cafaye/identity:dev"}
	}`))

	if err == nil {
		t.Fatal("ReadCatalog = nil error, want a failure")
	}
	if strings.Contains(err.Error(), "courier") {
		t.Errorf("err = %q, want it to name identity and not the entry that is fine", err)
	}
	if !strings.Contains(err.Error(), "imagee") {
		t.Errorf("err = %q, want it to name the key that is wrong", err)
	}
}

// A catalog holding nine services has typos that come from one mistake, so it
// reports them together. Fixing them one `cafe dev` at a time is one run per
// typo, and the second run's message is about a file the first run already said
// was wrong.
func TestReadCatalogReportsEveryMisspelledKeyAtOnce(t *testing.T) {
	_, err := ReadCatalog(strings.NewReader(`{"identity": {"name": "identity", "imag": "x", "helthcheck": {"test": []}}} `))

	if err == nil {
		t.Fatal("ReadCatalog = nil error, want a failure")
	}
	for _, want := range []string{"imag", "helthcheck"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name both %q — one run should list every typo", err, want)
		}
	}
	if !strings.Contains(err.Error(), "2 keys") {
		t.Errorf("err = %q, want it to agree with its own count", err)
	}
}

// `healthcheck` is the one struct nested inside an entry, so it is the one place
// a typo can hide from the check over entry keys. And a typo there is worse than
// a typo at the top: a healthcheck that lost its `retries` is a container
// reported ready the first time it answers at all, which is the failure a
// healthcheck exists to prevent.
func TestReadCatalogRefusesAMisspelledHealthcheckKey(t *testing.T) {
	_, err := ReadCatalog(strings.NewReader(
		`{"identity": {"name": "identity", "image": "x", "healthcheck": {"test": ["CMD", "true"], "retires": 3}}}`))

	if !errors.Is(err, ErrBadCatalog) {
		t.Fatalf("err = %v, want it to wrap ErrBadCatalog", err)
	}
	for _, want := range []string{"retires", "healthcheck", "identity", "retries"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to mention %q", err, want)
		}
	}
}

// The escape hatch, and the reason refusing unknown keys is safe to do at all.
//
// Without a way to say something cafe does not model, "refuse what you do not
// know" and "let the document grow" are the same decision, and the first one
// wins by accident: the next person to add a field they need has to work around
// cafe instead of adding to it. An `x-` key is dropped on purpose, and the
// difference between that and a typo is one prefix a human chose to type.
func TestReadCatalogDropsAnExtensionKey(t *testing.T) {
	catalog, err := ReadCatalog(strings.NewReader(
		`{"identity": {"name": "identity", "image": "cafaye/identity:dev", "x-maintainer": "cafe"}}`))

	if err != nil {
		t.Fatalf("ReadCatalog: %v", err)
	}
	entry, found := catalog.Resolve("identity")
	if !found {
		t.Fatal("the catalog does not hold identity")
	}
	if entry.Image != "cafaye/identity:dev" {
		t.Errorf("Image = %q, want the image to survive the extension key", entry.Image)
	}
}

// The extension prefix has to work INSIDE a healthcheck too, or the escape hatch
// is only half an escape hatch and the next nested object needs a new rule.
func TestReadCatalogDropsAnExtensionKeyInsideAHealthcheck(t *testing.T) {
	catalog, err := ReadCatalog(strings.NewReader(
		`{"identity": {"name": "identity", "image": "x", "healthcheck": {"test": ["CMD", "true"], "x-why": "copied from compose"}}}`))

	if err != nil {
		t.Fatalf("ReadCatalog: %v", err)
	}
	entry, _ := catalog.Resolve("identity")
	if entry.Healthcheck == nil || len(entry.Healthcheck.Test) != 2 {
		t.Errorf("Healthcheck = %+v, want the real fields to survive the extension key", entry.Healthcheck)
	}
}

// The list of valid keys is read off the struct, so it cannot drift. If it were
// written out beside the struct instead, renaming a field would leave the error
// telling developers to fix a key that is already correct — this same lie, one
// layer down.
//
// THE EXPECTATION IS WRITTEN OUT HERE, ON PURPOSE, and that is the one mirror in
// this file. The first version of this test built its expected list by calling
// `fieldOrder[Entry]()` — the function under test. Mutation 4 (drop `publish`
// from the list `fieldOrder` builds) left it GREEN, because the expectation
// shrank by exactly as much as the thing it was checking. A test that asks the
// code what it should have said, and then compares, cannot fail.
//
// The prohibition is on the ERROR MESSAGE being a second copy of the shape,
// because that copy is what a developer reads when they are already stuck. A
// copy in a test is a different thing: it is the only thing that can notice the
// first copy going stale, and it costs one line to update when a field is
// renamed — which is the update the error message must never need.
func TestTheKeyListNamesEveryFieldTheStructActuallyHas(t *testing.T) {
	_, err := ReadCatalog(strings.NewReader(`{"identity": {"nope": 1}}`))
	if err == nil {
		t.Fatal("ReadCatalog = nil error, want a failure")
	}

	// Compared as whole keys, not as substrings. The first version of this test
	// used Contains, and asserted that the list does NOT offer `env` — which
	// `environment` contains. The assertion was red for a document with no
	// defect in it at all, which is the cheapest kind of test to ignore: it
	// fails, so it is obviously broken, so it gets deleted rather than fixed.
	entryKeys := keysOffered(t, err.Error(), "An entry takes ", "; a healthcheck takes ")
	healthKeys := keysOffered(t, err.Error(), "a healthcheck takes ", "; and any key may be prefixed")

	// A key list that has drifted from the struct tells a developer to fix a
	// key that is already correct, so both directions are checked: every field
	// the struct has is offered, and nothing else is.
	for _, want := range []string{"name", "image", "command", "port", "publish", "environment", "connections", "volumes", "healthcheck", "dependencies"} {
		if !slices.Contains(entryKeys, want) {
			t.Errorf("the entry's key list offers %v, want it to include %q — a list that has drifted from the struct sends people to edit a file that is already right", entryKeys, want)
		}
	}
	if extra := len(entryKeys) - 10; extra != 0 {
		t.Errorf("the entry's key list offers %d keys, want exactly the 10 the struct has: %v", len(entryKeys), entryKeys)
	}

	for _, want := range []string{"test", "interval", "timeout", "retries", "startPeriod"} {
		if !slices.Contains(healthKeys, want) {
			t.Errorf("the healthcheck's key list offers %v, want it to include %q", healthKeys, want)
		}
	}
	if extra := len(healthKeys) - 5; extra != 0 {
		t.Errorf("the healthcheck's key list offers %d keys, want exactly the 5 the struct has: %v", len(healthKeys), healthKeys)
	}
}

// ---------------------------------------------------------------------------
// `connections` — the names a service reads its addresses from, and the two ways
// a catalog can get that wrong without anything complaining.
// ---------------------------------------------------------------------------

// A declaration cafe cannot act on is REFUSED, not ignored.
//
// It is a map, so a misspelled key is silent by construction: nothing in the
// decoder complains about a map key it does not recognise. `{"postgresql": …}`
// decodes into a perfectly good entry and cafe then hands the service the
// conventional `DATABASE_URL` — the exact defect the declaration was added to
// remove, back again, in a catalog that reads as though it were fixed. A key
// that changes nothing and says it is load-bearing is worse than a key cafe
// refused, because the catalog is the file somebody opens to check.
//
// The names are derived from `infra` rather than written out here, so a third
// piece of local infrastructure is covered on the day it is added rather than on
// the day this test is next read.
func TestReadCatalogRefusesAConnectionsKeyThatNamesNoInfrastructure(t *testing.T) {
	names := infraNames()
	if len(names) == 0 {
		t.Fatal("infraNames() is empty; this test refuses a key not in it and would pass for any input")
	}

	// Every real name is accepted, and a name one edit away from a real one is
	// not. The pairs below are derived from the table rather than listed, so the
	// test cannot rot into asserting about infrastructure cafe has removed.
	for _, name := range names {
		document := `{"pantry": {"name": "pantry", "image": "x:1", "connections": {` + quote(name) + `: "P_DATABASE_URL"}}}`
		if _, err := ReadCatalog(strings.NewReader(document)); err != nil {
			t.Errorf("ReadCatalog with connections[%q] = %v, want it accepted: it names a piece of local infrastructure", name, err)
		}
	}

	// `postgresql` is the mistake this exists for and it is spelled that way on
	// purpose; a misspelling that is not a near-miss is not what a person types.
	_, err := ReadCatalog(strings.NewReader(`{"pantry": {"name": "pantry", "image": "x:1", "connections": {"postgresql": "PANTRY_DATABASE_URL"}}}`))
	if err == nil {
		t.Fatal("ReadCatalog = nil error for connections[postgresql], want a refusal")
	}
	if !errors.Is(err, ErrBadCatalog) {
		t.Errorf("err = %v, want it to wrap ErrBadCatalog", err)
	}
	for _, want := range []string{"pantry", "postgresql"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err, want)
		}
	}
}

// The same silence, from the other direction: the value has to be a variable
// name. The mistake is pasting a whole URL into the field, and the failure that
// produces is several layers away — Docker's own environment parser refusing a
// key called `postgres://…`, in a sentence about the document rather than about
// the catalog that wrote it.
func TestReadCatalogRefusesAConnectionsValueThatIsNotAVariableName(t *testing.T) {
	for _, value := range []string{
		"postgres://pantry:pantry@postgres:5432/pantry",
		"PANTRY DATABASE URL",
		"PANTRY-DATABASE-URL",
		"9PANTRY",
		"",
	} {
		document := `{"pantry": {"name": "pantry", "image": "x:1", "connections": {"postgres": ` + quote(value) + `}}}`
		if _, err := ReadCatalog(strings.NewReader(document)); err == nil {
			t.Errorf("ReadCatalog = nil error for connections[postgres] = %q, want a refusal", value)
		}
	}
}

// The refusal names the service and every mistake in one sentence, and it says
// what cafe would have done instead — because "nothing" is the sentence that
// sends a developer looking for the bug somewhere else.
func TestTheConnectionsErrorNamesTheServiceAndEveryMistake(t *testing.T) {
	_, err := ReadCatalog(strings.NewReader(`{"pantry": {"name": "pantry", "image": "x:1", "connections": {"postgresql": "A_URL", "postgres": "postgres://x/y"}}}`))
	if err == nil {
		t.Fatal("ReadCatalog = nil error, want a refusal")
	}
	for _, want := range []string{"pantry", "postgresql", "postgres://x/y", "2 entries"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err, want)
		}
	}
}

// And the count agrees, which is the same grammar rule the misspelled-key check
// follows: a counted noun takes the number and a pronoun does not. One offender
// and two are the two sentences a reader will actually meet.
func TestTheConnectionsErrorReadsAsASentenceAtEveryCount(t *testing.T) {
	one := `{"pantry": {"name": "pantry", "image": "x:1", "connections": {"postgresql": "A_URL"}}}`
	two := `{"pantry": {"name": "pantry", "image": "x:1", "connections": {"postgresql": "A_URL", "mysql": "B_URL"}}}`

	_, oneErr := ReadCatalog(strings.NewReader(one))
	_, twoErr := ReadCatalog(strings.NewReader(two))
	if oneErr == nil || twoErr == nil {
		t.Fatalf("ReadCatalog errors = %v and %v, want both to be refusals", oneErr, twoErr)
	}
	if !strings.Contains(oneErr.Error(), "carries 1 entry cafe") {
		t.Errorf("one offender reads %q, want \"carries 1 entry cafe\"", oneErr)
	}
	if !strings.Contains(twoErr.Error(), "carries 2 entries cafe") {
		t.Errorf("two offenders reads %q, want \"carries 2 entries cafe\"", twoErr)
	}
	if strings.Contains(oneErr.Error(), "cafe would have ignored them") {
		t.Errorf("one offender reads %q, want \"ignored it\": a pronoun must not take a count", oneErr)
	}
	if !strings.Contains(twoErr.Error(), "cafe would have ignored them") {
		t.Errorf("two offenders read %q, want \"ignored them\"", twoErr)
	}
}

// An entry that declares nothing is the common case and must stay free: three of
// the four services in the platform with a database declare nothing, and a check
// that refused the undeclared shape would be refusing most of the fleet.
func TestReadCatalogAcceptsAnEntryThatDeclaresNoConnections(t *testing.T) {
	for _, document := range []string{
		`{"pantry": {"name": "pantry", "image": "x:1"}}`,
		`{"pantry": {"name": "pantry", "image": "x:1", "connections": {}}}`,
	} {
		if _, err := ReadCatalog(strings.NewReader(document)); err != nil {
			t.Errorf("ReadCatalog = %v, want an undeclared entry accepted:\n%s", err, document)
		}
	}
}

// quote is a JSON string literal, so the fixtures above build their documents
// from the same names the code derives rather than pasting them. Hand-rolled
// rather than json.Marshal because the documents above are deliberately
// malformed-looking for a reader — a reader who sees them has to be able to see
// the mistake in them.
func quote(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// keysOffered reads the comma-separated key list a message prints between two
// markers. Parsed rather than pattern-matched so that the assertions in the test
// above are about keys, not about letters that happen to appear in them.
func keysOffered(t *testing.T, message, after, before string) []string {
	t.Helper()
	_, tail, found := strings.Cut(message, after)
	if !found {
		t.Fatalf("err = %q, want it to contain %q", message, after)
	}
	listed, _, found := strings.Cut(tail, before)
	if !found {
		t.Fatalf("err = %q, want the list after %q to be closed by %q", message, after, before)
	}
	return strings.Split(listed, ", ")
}

// Read at one typo and at three, because the message is built by counting and
// every count-dependent message is wrong at one count or the other.
//
// This test exists because the message WAS wrong at both. It read "cafe would
// have dropped 1 it" — a counted pronoun, which is a sentence about nothing —
// and then, once that was fixed by routing the pronoun through a format string,
// it read "cafe would have dropped them%!(EXTRA int=3)". Nothing caught either
// one: the other assertions here look for substrings, and both defects are in
// the parts of the sentence around the substrings. So the whole message is
// matched, at both counts, against text written out in full.
func TestTheCatalogErrorReadsAsASentenceAtEveryCount(t *testing.T) {
	tests := []struct {
		name     string
		document string
		want     []string
		notWant  []string
	}{
		{
			name:     "one typo",
			document: `{"identity": {"name": "identity", "imag": "x"}}`,
			want: []string{
				"1 key no entry has:",
				"cafe would have dropped it without saying so.",
			},
			notWant: []string{"1 it", "%!", "keys"},
		},
		{
			name:     "three typos",
			document: `{"identity": {"name": "identity", "imag": "x", "prot": 1, "volums": []}}`,
			want: []string{
				"3 keys no entry has:",
				"cafe would have dropped them without saying so.",
			},
			notWant: []string{"1 key", "%!", "3 them"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadCatalog(strings.NewReader(tt.document))
			if err == nil {
				t.Fatalf("ReadCatalog(%q) = nil error, want a failure", tt.document)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %q, want it to read %q", err, want)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(err.Error(), notWant) {
					t.Errorf("err = %q, want it NOT to contain %q — a message that is wrong about its own count is worse than a terse one", err, notWant)
				}
			}
		})
	}
}
