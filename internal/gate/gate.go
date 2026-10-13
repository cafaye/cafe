// Package gate owns cafe's own gate declaration — `gate.yml` — and the part of
// it that is a FUNCTION of the tree rather than a judgement: the three
// countable floors.
//
// # Why a derived floor beats a hand-edited one
//
// A floor is a decrease detector. `gate.proof[].minimum` must not sit below what
// the suite actually runs, and `internal/ci`'s
// TestTheGateFloorIsNotBelowTheSuiteCafClaimsToHave holds the identity exactly:
//
//	declared - minimum == expectedSkips
//
// That makes the number a function of the tree — of how many
// `func TestX(t *testing.T)` the tree declares, of how many subtests `go test -v`
// reports — and a function a human retypes on every test-adding change is a
// function that goes stale. It did, twice in one day: two packets each raised
// the number, a rebase resolved the conflict mechanically, and the resolution
// left the floor one short of the merged tree. The number was correct in both
// branches and wrong in the merge, which is the signature of a value that should
// never have been written by hand.
//
// So this package derives it. RewriteFloors sets the three floors in gate.yml's
// bytes from a Floors value, preserving every comment and every other byte, and
// Measured.Floors turns one run of the suite into the floors the tree implies.
// `cafe gate floors --write` is the only caller that writes; the review signal is
// the DELTA it prints — "floors moved 1370→1375, subtests 1248→1250" — so a floor
// that moves is visible in the command's output and in the diff, and a floor that
// moves DOWN still needs a human to have run the writer and read the number.
//
// # What the guard still catches
//
// The assertion is unchanged and exactly as strong. A floor below the tree's
// counts is still red — `declared - minimum` stops equalling expectedSkips — and
// a test deleted without the floor following it down is still red, because the
// writer is the only thing that lowers a floor and running it is a deliberate
// act whose output is the delta. What the writer removes is the retyping, not the
// detector.
package gate

import (
	"fmt"
	"strconv"
	"strings"
)

// The three proofs in gate.yml whose `minimum` is a function of the tree. The
// fourth, `live-tier`, has no floor: its number is the tier's size, and it is
// pinned by name in `bin/prime` and checked against the tree there.
const (
	ProofPackages = "packages"
	ProofSuite    = "suite"
	ProofSubtests = "subtests"
)

// Floors is the three countable floors gate.yml declares.
type Floors struct {
	Packages int
	Suite    int
	Subtests int
}

// floorProofs is every proof this package writes, in gate.yml's order. One list,
// so ParseFloors and RewriteFloors cannot disagree about which proofs exist.
var floorProofs = []string{ProofPackages, ProofSuite, ProofSubtests}

// ParseFloors reads the three floors out of gate.yml's bytes.
//
// It reads by SHAPE — the `- id:` line that opens a proof, then the `minimum:`
// line under it — rather than with a YAML library, and that is deliberate: the
// writer has to preserve the file's comments byte for byte, and a parse into a
// struct followed by a marshal would drop every one of them. The bytes are the
// artifact; this reads the one fact it needs and touches nothing else.
func ParseFloors(raw []byte) (Floors, error) {
	var f Floors
	cur := ""
	found := map[string]bool{}

	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if id, ok := proofID(trimmed); ok {
			cur = id
			continue
		}
		if cur == "" {
			continue
		}
		v, ok := minimumOf(trimmed)
		if !ok {
			continue
		}
		switch cur {
		case ProofPackages:
			f.Packages = v
		case ProofSuite:
			f.Suite = v
		case ProofSubtests:
			f.Subtests = v
		}
		found[cur] = true
		// A proof carries one minimum; the next `- id:` starts the next proof.
		cur = ""
	}

	for _, id := range floorProofs {
		if !found[id] {
			return Floors{}, fmt.Errorf("gate.yml has no `minimum` under the proof %q. The writer reads the "+
				"floor from that line, so a proof that was restructured is a floor nobody can regenerate", id)
		}
	}
	return f, nil
}

// RewriteFloors returns gate.yml's bytes with the three floors set to want.
//
// The second return is whether anything actually moved, which is what makes the
// writer idempotent: on a tree whose floors are already right, a second
// `--write` returns the same bytes and `changed == false`, so it writes nothing
// and the diff stays empty. Every other byte — every comment, every blank line,
// the historical prose above each floor — is carried through untouched, which is
// the point: the comments explain WHY the floors exist, and the writer must not
// be the reason a reader stops learning that.
func RewriteFloors(raw []byte, want Floors) ([]byte, bool, error) {
	lines := strings.Split(string(raw), "\n")
	cur := ""
	seen := map[string]bool{}
	changed := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if id, ok := proofID(trimmed); ok {
			cur = id
			continue
		}
		if cur == "" {
			continue
		}
		if _, ok := minimumOf(trimmed); !ok {
			continue
		}
		wantVal, isFloor := floorOf(cur, want)
		if !isFloor {
			// A proof this package does not write (today only `live-tier`, which
			// has no minimum). Leave it exactly as it is.
			cur = ""
			continue
		}
		seen[cur] = true
		replaced := replaceMinimum(line, wantVal)
		if replaced != line {
			lines[i] = replaced
			changed = true
		}
		cur = ""
	}

	for _, id := range floorProofs {
		if !seen[id] {
			return nil, false, fmt.Errorf("gate.yml has no `minimum` under the proof %q, so the writer "+
				"cannot set it. Refusing rather than writing a document with one floor and two holes", id)
		}
	}
	return []byte(strings.Join(lines, "\n")), changed, nil
}

// Delta is the review signal: what the writer found and what it wrote.
//
// It is the product of the command and not a log line. A floor that MOVES is the
// one thing a reviewer has to see, because the writer is the only thing that can
// lower one, and a delta that said only "wrote gate.yml" would make the dangerous
// direction invisible.
type Delta struct {
	Before Floors
	After  Floors
}

// Changed is whether any floor moved.
func (d Delta) Changed() bool { return d.Before != d.After }

// String renders the delta the way the packet's brief spells it: the suite as
// "floors moved A→B", the other two by name. Only the floors that moved are
// named, so a reader sees the change and not the three numbers that did not.
func (d Delta) String() string {
	if !d.Changed() {
		return fmt.Sprintf("floors unchanged: packages %d, suite %d, subtests %d",
			d.After.Packages, d.After.Suite, d.After.Subtests)
	}
	var parts []string
	if d.Before.Packages != d.After.Packages {
		parts = append(parts, fmt.Sprintf("packages %d→%d", d.Before.Packages, d.After.Packages))
	}
	if d.Before.Suite != d.After.Suite {
		parts = append(parts, fmt.Sprintf("floors moved %d→%d", d.Before.Suite, d.After.Suite))
	}
	if d.Before.Subtests != d.After.Subtests {
		parts = append(parts, fmt.Sprintf("subtests %d→%d", d.Before.Subtests, d.After.Subtests))
	}
	return strings.Join(parts, ", ")
}

// proofID reads the id out of a `- id: <name>` line.
func proofID(trimmed string) (string, bool) {
	const prefix = "- id: "
	if !strings.HasPrefix(trimmed, prefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, prefix)), true
}

// minimumOf reads the integer out of a `minimum: <n>` line.
func minimumOf(trimmed string) (int, bool) {
	const prefix = "minimum:"
	if !strings.HasPrefix(trimmed, prefix) {
		return 0, false
	}
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(trimmed, prefix)))
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, false
	}
	return v, true
}

// floorOf is the value want holds for one proof id.
func floorOf(id string, want Floors) (int, bool) {
	switch id {
	case ProofPackages:
		return want.Packages, true
	case ProofSuite:
		return want.Suite, true
	case ProofSubtests:
		return want.Subtests, true
	}
	return 0, false
}

// replaceMinimum swaps the number on a `minimum:` line, leaving the line's
// indentation and any trailing text alone. A `minimum` line in this file is
// `      minimum: 1370`; the writer must not reformat it into something a reader
// of the diff has to re-learn.
func replaceMinimum(line string, want int) string {
	i := strings.Index(line, "minimum:")
	if i < 0 {
		return line
	}
	after := line[i+len("minimum:"):]
	token := strings.TrimSpace(after)
	if token == "" {
		return line
	}
	return line[:i+len("minimum:")] + strings.Replace(after, token, strconv.Itoa(want), 1)
}
