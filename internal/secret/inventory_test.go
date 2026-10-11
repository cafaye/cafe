package secret

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The SECRET RECORD block in the kit installer, transcribed.
//
// This is cafe's half of kit's own drift tripwire: kit's substrate test parses
// the block out of install.sh and fails if a fixture removes a name, and this is
// the same claim on the Go side — every name the installer's record declares is
// enumerated here. The transcription is deliberate: a copy that drifted would be
// a second source of truth, and this test is what fails when it does. When the
// installer gains a key, this table gains it here, in the same commit.
var recordIdentity = []string{"DATABASE_URL", "SIGNUP_SIGNING_KEY", "MFA_ENCRYPTION_KEY"}

var recordCourier = []string{
	"DATABASE_URL", "SECRET_KEY_BASE", "COURIER_IDENTITY_TOKEN", "COURIER_SECRET_BOX_KEY",
	"COURIER_MAIL_ADAPTER", "COURIER_SMTP_HOST", "COURIER_SMTP_PORT", "COURIER_SMTP_USERNAME",
	"COURIER_SMTP_PASSWORD", "COURIER_INBOUND_RESEND_SECRET", "COURIER_ERROR_RELAY_TOKEN",
}

func TestEveryEntryHasAClassAReasonAndACitation(t *testing.T) {
	for _, e := range Inventory() {
		t.Run(e.Name, func(t *testing.T) {
			switch e.Class {
			case Rotatable, Conditional, NotRotatable:
			default:
				t.Fatalf("class = %q, want one of the three", e.Class)
			}
			if strings.TrimSpace(e.Reason) == "" {
				t.Error("no reason: a class with no reason is an opinion")
			}
			if strings.TrimSpace(e.Source) == "" {
				t.Error("no source: a reason with no citation is an opinion nobody can check")
			}
			// The citation has to name a file and a line, because "see the
			// runbook" is not a citation.
			if !strings.Contains(e.Source, ".md:") && !strings.Contains(e.Source, ".sh:") {
				t.Errorf("source = %q, want a file:line citation", e.Source)
			}
			switch e.Provenance {
			case Generated, Minted, External, Derived, Configuration:
			default:
				t.Errorf("provenance = %q, want one of the installer's five", e.Provenance)
			}
		})
	}
}

// The installer's SECRET RECORD block is the source of truth for what exists,
// so every name it declares is enumerated here. Three of them are absent from
// this test on purpose: DATABASE_URL appears in two namespaces and is counted
// by the namespace walk below, and OIDC_ISSUER is declared in the record's
// header rather than its RECORD line.
func TestEveryNameInTheInstallerRecordIsEnumerated(t *testing.T) {
	inventory := Inventory()
	have := map[string]bool{}
	for _, e := range inventory {
		have[e.Name] = true
	}

	for _, want := range recordIdentity {
		if !have[want] {
			t.Errorf("identity-secrets declares %s and the inventory does not hold it", want)
		}
	}
	for _, want := range recordCourier {
		if !have[want] {
			t.Errorf("courier-secrets declares %s and the inventory does not hold it", want)
		}
	}
	if !have["REDIS_URL"] {
		t.Error("gateway-secrets declares REDIS_URL and the inventory does not hold it")
	}

	// The two DATABASE_URLs are one name in two namespaces, so the count is
	// the assertion and not the presence.
	var databaseURLs int
	for _, e := range inventory {
		if e.Name == "DATABASE_URL" {
			databaseURLs++
		}
	}
	if databaseURLs != 2 {
		t.Errorf("DATABASE_URL appears %d times, want 2 (identity and parlor)", databaseURLs)
	}
}

// The packet names the rotatable class, and the enumeration has to agree with
// it. If this test fails because a fourth entry was added to the rotatable
// class, that is a decision, and it belongs in a commit that says so rather than
// in a table that grew quietly.
func TestTheRotatableClassIsThePacketThree(t *testing.T) {
	var got []string
	for _, e := range Inventory() {
		if e.Class == Rotatable {
			got = append(got, e.StateKey())
		}
	}
	want := map[string]bool{
		"courier/courier-secrets/SECRET_KEY_BASE":        true,
		"courier/courier-secrets/COURIER_SECRET_BOX_KEY": true,
		"data/redis-password/password":                   true,
	}
	if len(got) != len(want) {
		t.Fatalf("rotatable class = %v, want %d entries", got, len(want))
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("%s is rotatable and is not in the packet's class", name)
		}
	}
}

// The frozen class is the reason the packet exists, so it is asserted by name
// and by citation rather than by count.
func TestTheNotRotatableClassNamesTheForkAndTheCutover(t *testing.T) {
	frozen := map[string]string{}
	for _, e := range Inventory() {
		if e.Class == NotRotatable {
			frozen[e.Name] = e.Source
		}
	}
	for _, name := range []string{"MUSE_VAULT_KEY", "OIDC_SIGNING_KEY", "MFA_ENCRYPTION_KEY", "OAUTH_ENCRYPTION_KEY"} {
		source, ok := frozen[name]
		if !ok {
			t.Errorf("%s is not in the not-rotatable class; a rotation of it is a fork or a cutover", name)
			continue
		}
		if source == "" {
			t.Errorf("%s is not-rotatable with no citation", name)
		}
	}
}

// A rotatable entry carries a generation shape, because a rotation that minted
// a value the service would refuse at boot would be an outage disguised as a
// rotation. Phoenix refuses a SECRET_KEY_BASE shorter than 64 bytes.
func TestEveryRotatableEntryHasAShape(t *testing.T) {
	for _, e := range Inventory() {
		if e.Class != Rotatable {
			continue
		}
		if e.Shape == "" {
			t.Errorf("%s is rotatable with no shape", e.Name)
		}
		if e.Interval <= 0 {
			t.Errorf("%s is rotatable with no interval, so it is never due", e.Name)
		}
		if err := func() error { _, err := Generate(e.Shape); return err }(); err != nil {
			t.Errorf("%s has shape %q that Generate refuses: %v", e.Name, e.Shape, err)
		}
	}
}

// A conditional entry never carries an interval: a schedule for something that
// cannot be rotated by this command is a schedule that will fail.
func TestNoConditionalEntryHasAnInterval(t *testing.T) {
	for _, e := range Inventory() {
		if e.Class == Rotatable {
			continue
		}
		if e.Interval != 0 {
			t.Errorf("%s is %s with an interval of %s", e.Name, e.Class, e.Interval)
		}
	}
}

func TestFindMatchesOnTheSpellingAKubectlUserTypes(t *testing.T) {
	for _, name := range []string{
		"MUSE_VAULT_KEY", "OIDC_SIGNING_KEY", "SECRET_KEY_BASE",
		"data/redis-password/password", "courier/courier-secrets/SECRET_KEY_BASE",
	} {
		if _, err := Find(name); err != nil {
			t.Errorf("Find(%q) = %v, want the entry", name, err)
		}
	}
}

func TestFindNamesWhatCouldHaveBeenTyped(t *testing.T) {
	_, err := Find("NOT_A_SECRET")
	if err == nil {
		t.Fatal("Find(an unknown name) = nil error, want a refusal that names the inventory")
	}
	if !strings.Contains(err.Error(), "SECRET_KEY_BASE") {
		t.Errorf("err = %q, want it to name what the inventory holds", err)
	}
}

func TestGenerateUsesTheInstallersSpelling(t *testing.T) {
	base, err := Generate("base64 of 32 random bytes")
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 44 { // 32 bytes, base64, unpadded standard is 44 with padding
		t.Errorf("a 32-byte key generated as %d characters, want 44", len(base))
	}
	hexv, err := Generate("hex of 24 random bytes")
	if err != nil {
		t.Fatal(err)
	}
	if len(hexv) != 48 {
		t.Errorf("24 bytes of hex generated as %d characters, want 48", len(hexv))
	}
}

// The state key is the `namespace/secret/key` spelling, and that is what keeps
// one box's two DATABASE_URLs from sharing a timestamp.
func TestStateKeyDistinguishesTheTwoDatabaseURLs(t *testing.T) {
	var keys []string
	for _, e := range Inventory() {
		if e.Name == "DATABASE_URL" {
			keys = append(keys, e.StateKey())
		}
	}
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Errorf("the two DATABASE_URL entries have state keys %v, want two distinct ones", keys)
	}
}

func TestDueIsAFunctionOfTheStateAndTheClock(t *testing.T) {
	state := NewState()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

	// A box that has never rotated anything has everything due.
	if got := len(Due(state, now)); got != 3 {
		t.Errorf("a fresh state has %d due, want 3", got)
	}

	entry, err := Find("COURIER_SECRET_BOX_KEY")
	if err != nil {
		t.Fatal(err)
	}
	state.MarkRotated(entry, now)
	if got := len(Due(state, now)); got != 2 {
		t.Errorf("after one rotation %d are due, want 2", got)
	}
	// Ninety days on the dot is not yet late; the interval is a floor.
	if got := len(Due(state, now.Add(DefaultInterval-time.Minute))); got != 2 {
		t.Errorf("just inside the interval %d are due, want 2", got)
	}
	if got := len(Due(state, now.Add(DefaultInterval))); got != 3 {
		t.Errorf("at the interval %d are due, want 3", got)
	}
}

// Every citation names a fleet file and a line range, and this walks them so a
// citation that drifts is caught here rather than by an operator who follows it
// and finds the wrong sentence. The files are not in this repository — doc.go
// says why — so the walk skips silently when the fleet is not checked out
// beside this one, which is the ordinary case on a CI runner and the reason
// this is a walk rather than a fetch.
func TestEveryCitationPointsAtSomething(t *testing.T) {
	roots := []string{
		os.Getenv("HOME") + "/Code/any/moon",
		"../../moon",
	}
	var root string
	for _, candidate := range roots {
		if st, err := os.Stat(filepath.Join(candidate, "cafaye")); err == nil && st.IsDir() {
			root = candidate
			break
		}
	}
	if root == "" {
		t.Skip("the fleet is not checked out beside this worktree; the citations are checkable by hand")
	}

	// install.sh line ranges, keyed by the citation string the table uses.
	ranges := map[string][2]int{
		"cafaye/kit/templates/substrate/install.sh:1088-1093":   {1088, 1093},
		"cafaye/kit/templates/substrate/install.sh:1084-1093":   {1084, 1093},
		"cafaye/kit/templates/substrate/install.sh:1094-1100":   {1094, 1100},
		"cafaye/kit/templates/substrate/install.sh:1101-1105":   {1101, 1105},
		"cafaye/kit/templates/substrate/install.sh:1239-1245":   {1239, 1245},
		"cafaye/kit/templates/substrate/install.sh:1228-1232":   {1228, 1232},
		"cafaye/kit/templates/substrate/install.sh:1239-1244":   {1239, 1244},
		"cafaye/kit/templates/substrate/install.sh:1106-1109":   {1106, 1109},
		"cafaye/kit/templates/substrate/install.sh:933-939":     {933, 939},
		"cafaye/kit/templates/substrate/install.sh:887,892-909": {887, 909},
	}
	installer := filepath.Join(root, ranges0Path(ranges))
	if !fileReadable(installer) {
		t.Skipf("the fleet's install.sh is not at %s", installer)
	}
	installerLines := fileLines(t, installer)
	for citation, span := range ranges {
		lo, hi := span[0], span[1]
		if hi > len(installerLines) {
			t.Errorf("%s ends past the end of install.sh (%d lines)", citation, len(installerLines))
			continue
		}
		block := strings.ToLower(strings.Join(installerLines[lo-1:hi], "\n"))
		if !strings.Contains(block, "secret") && !strings.Contains(block, "password") {
			t.Errorf("%s does not mention a secret or a password:\n%s", citation, block)
		}
	}

	// The runbook line citations, one by one, each named by a word that has to
	// be on the line it claims.
	lineWords := map[string]string{
		filepath.Join(root, "cafaye/identity/README.md:299"):       "SIGNUP_SIGNING_KEY",
		filepath.Join(root, "cafaye/identity/README.md:310"):       "OAUTH_ENCRYPTION_KEY",
		filepath.Join(root, "cafaye/identity/README.md:356"):       "expires one hour",
		filepath.Join(root, "cafaye/identity/README.md:1348"):      "One key",
		filepath.Join(root, "cafaye/muse/README.md:332-333"):       "per request",
		filepath.Join(root, "cafaye/identity/README.md:1035-1039"): "MFA_ENCRYPTION_KEY",
	}
	for citation, want := range lineWords {
		path, span, ok := splitRange(citation)
		if !ok {
			continue
		}
		if !fileReadable(path) {
			continue
		}
		lines := fileLines(t, path)
		if span[1] > len(lines) {
			t.Errorf("%s ends past the end of the file (%d lines)", citation, len(lines))
			continue
		}
		block := strings.Join(lines[span[0]-1:span[1]], "\n")
		if !strings.Contains(block, want) {
			t.Errorf("%s does not contain %q", citation, want)
		}
	}
}

// The path install.sh lives at under every root, taken once so the two halves
// above agree.
func ranges0Path(ranges map[string][2]int) string {
	for citation := range ranges {
		if path, _, ok := splitRange(citation); ok {
			return path
		}
	}
	return ""
}

// splitRange splits a "path:lo-hi" or "path:lo" citation.
func splitRange(citation string) (string, [2]int, bool) {
	i := strings.LastIndex(citation, ":")
	if i < 0 {
		return "", [2]int{}, false
	}
	path, span := citation[:i], citation[i+1:]
	var lo, hi int
	if strings.Contains(span, "-") {
		if _, err := fmt.Sscanf(span, "%d-%d", &lo, &hi); err != nil {
			return "", [2]int{}, false
		}
	} else {
		if _, err := fmt.Sscanf(span, "%d", &lo); err != nil {
			return "", [2]int{}, false
		}
		hi = lo
	}
	return path, [2]int{lo, hi}, true
}

func fileReadable(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func fileLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
}
