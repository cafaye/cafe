package secret

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A citation is a fleet path and the CONTENT it is about, and this file is the
// grammar and the guard over it.
//
// # Why content and not a line range
//
// The citations used to be `install.sh:1324-1330` and the test read those lines
// and asserted they mentioned what the citation claimed. kit moves: today's
// substrate packets shifted install.sh by more than a hundred lines, and the
// test went red twice in one day for a reason that had nothing to do with its
// subject. The "fix" was a human re-counting lines in another repository, which
// is not a fix — it is the same friction waiting for the next edit above the
// line. So the stored form is the content, the test searches for it, and the
// line number is derived when it is found and never stored.
//
// # What it still catches
//
// The guard is "the cited content is still in the file". A citation whose
// statement is deleted, renamed or reworded fails here and the failure names
// what it looked for and where. What it no longer catches — and must not — is a
// statement that merely MOVED: the anchor resolves to the new line and the test
// passes, which is the entire point.

// citation is one `path: "anchor"` out of an Entry.Source.
type citation struct {
	Path   string
	Anchor string
}

func (c citation) String() string { return fmt.Sprintf("%s: %q", c.Path, c.Anchor) }

// parseSource splits an Entry.Source into its citations.
//
// The grammar is `path: "anchor"`, several joined by `, `. It is parsed by
// scanning rather than by splitting on `, ` because an anchor is prose and prose
// holds commas: the double quotes are what make the boundaries unambiguous.
func parseSource(source string) ([]citation, error) {
	var out []citation
	rest := source
	for {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			break
		}
		i := strings.Index(rest, `: "`)
		if i < 0 {
			return nil, fmt.Errorf("citation %q is not `path: \"anchor\"`; a citation has to name a file and "+
				"the content it is about", rest)
		}
		path := strings.TrimSpace(rest[:i])
		after := rest[i+len(`: "`):]
		j := strings.Index(after, `"`)
		if j < 0 {
			return nil, fmt.Errorf("citation %q opens a quote it never closes", rest)
		}
		anchor := after[:j]
		if path == "" || anchor == "" {
			return nil, fmt.Errorf("citation %q has an empty path or an empty anchor", rest)
		}
		out = append(out, citation{Path: path, Anchor: anchor})
		rest = strings.TrimPrefix(strings.TrimSpace(after[j+1:]), ",")
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("source %q holds no citation", source)
	}
	return out, nil
}

// resolveCitation finds an anchor in a file's lines and returns its 1-based line
// number. The number is derived output — it is logged when the fleet is present
// and never written back into the table.
func resolveCitation(lines []string, anchor string) (int, bool) {
	for i, line := range lines {
		if strings.Contains(line, anchor) {
			return i + 1, true
		}
	}
	return 0, false
}

// citationFixture is a stand-in for a fleet file. The real install.sh lives in
// another repository and moves under this test; a fixture makes the two claims
// below — survives a move, fails on a deletion — deterministic and independent
// of any other checkout.
const citationFixture = "install-fixture.sh"

// TestACitationSurvivesAMoveAboveIt is the friction, demonstrated.
//
// The same anchor, pushed a hundred lines down the file, still resolves — and it
// resolves to the NEW line, because the line number is derived from where the
// content is and not stored beside it. A line-range citation fails this by
// construction: the number it stored is now wrong.
func TestACitationSurvivesAMoveAboveIt(t *testing.T) {
	lines := fixtureLines(t, citationFixture)
	anchor := "RECORD courier-secrets:"

	at, ok := resolveCitation(lines, anchor)
	if !ok {
		t.Fatalf("%q is not in the fixture %s, so this test is not exercising the move", anchor, citationFixture)
	}

	// Move it: prepend a hundred lines above it, the way a kit packet moves the
	// block down the file.
	moved := append(make([]string, 100), lines...)
	after, ok := resolveCitation(moved, anchor)
	if !ok {
		t.Fatalf("%q stopped resolving after the file grew above it. A content citation must survive an edit "+
			"above it — that is the whole reason it is content and not a line range", anchor)
	}
	if after != at+100 {
		t.Errorf("%q resolved to line %d after a 100-line insertion above it, want %d", anchor, after, at+100)
	}
}

// TestACitationFailsWhenItsContentIsGone is the guard. The content deleted, the
// citation resolves nowhere, and the failure names what it looked for and where.
func TestACitationFailsWhenItsContentIsGone(t *testing.T) {
	lines := fixtureLines(t, citationFixture)
	gone := "RECORD courier-secrets:"

	// The control: it is there to begin with.
	if _, ok := resolveCitation(lines, gone); !ok {
		t.Fatalf("%q is not in the fixture %s, so the deletion below proves nothing", gone, citationFixture)
	}

	// Delete the line the anchor is on.
	var kept []string
	for _, line := range lines {
		if !strings.Contains(line, gone) {
			kept = append(kept, line)
		}
	}
	if _, ok := resolveCitation(kept, gone); ok {
		t.Fatalf("%q still resolves after its line was deleted; the guard does not fire on a deletion", gone)
	}
}

// TestEverySourceParsesAsCitations keeps the grammar honest over the real table:
// every entry's source has to split into citations with a path and an anchor, or
// the check below is checking nothing.
func TestEverySourceParsesAsCitations(t *testing.T) {
	for _, e := range Inventory() {
		cites, err := parseSource(e.Source)
		if err != nil {
			t.Errorf("%s: %v", e.Name, err)
			continue
		}
		for _, c := range cites {
			if !strings.HasSuffix(c.Path, ".sh") && !strings.HasSuffix(c.Path, ".md") {
				t.Errorf("%s cites %q, which is neither a shell script nor a markdown file", e.Name, c.Path)
			}
		}
	}
}

// fixtureLines reads a fixture under testdata.
func fixtureLines(t *testing.T, name string) []string {
	t.Helper()
	path := filepath.Join("testdata", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
}
