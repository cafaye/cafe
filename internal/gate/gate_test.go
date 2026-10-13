package gate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot finds this repository's root from this file's own path, so the test
// reads the committed gate.yml and counts the tree wherever `go test` was
// invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed, so the repository root cannot be found")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func readGate(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "gate.yml"))
	if err != nil {
		t.Fatalf("read gate.yml: %v", err)
	}
	return raw
}

// TestTheWriterRoundTripsAndAHandLoweredFloorIsRed is the packet's central
// proof, and it drives the writer's pure core against the committed declaration.
//
// It is three claims in one test because they are one mechanism read from three
// sides, and splitting them would let one side drift while the others stayed
// green:
//
//  1. On the tree as committed, the writer derives the floors that are already
//     there and changes NOTHING — the idempotence that makes it safe to run at
//     every merge.
//  2. A hand-lowered suite floor breaks the ratchet identity
//     (`declared - minimum == expectedSkips`), which is the guard the packet
//     must not weaken.
//  3. Running the writer over that lowered declaration restores it BYTE FOR
//     BYTE — every comment above every floor survives, which is the whole reason
//     the writer edits lines rather than parsing and re-marshalling YAML.
func TestTheWriterRoundTripsAndAHandLoweredFloorIsRed(t *testing.T) {
	root := repoRoot(t)
	raw := readGate(t)

	committed, err := ParseFloors(raw)
	if err != nil {
		t.Fatalf("parse the committed floors: %v", err)
	}
	declared, err := CountTestFuncs(root)
	if err != nil {
		t.Fatalf("count the tree's top-level tests: %v", err)
	}
	if got := declared - committed.Suite; got != ExpectedSkips {
		t.Fatalf("the committed tree does not satisfy the ratchet: declared %d - suite %d = %d, want "+
			"expectedSkips %d. Fix the tree before trusting this test", declared, committed.Suite, got, ExpectedSkips)
	}

	// 1. Idempotence. The measurement that corresponds to the committed tree is
	// the committed floors themselves, because the tree is green on them.
	measured := Measured{Packages: committed.Packages, TopLevel: committed.Suite, Subtests: committed.Subtests}
	if got := measured.Floors(); got != committed {
		t.Fatalf("Measured.Floors() = %+v, want the committed floors %+v", got, committed)
	}
	same, changed, err := RewriteFloors(raw, measured.Floors())
	if err != nil {
		t.Fatalf("rewrite an already-correct declaration: %v", err)
	}
	if changed {
		t.Error("the writer reports a change on a declaration whose floors are already right; a second " +
			"--write would dirty the tree and idempotence is the property that makes it safe at every merge")
	}
	if string(same) != string(raw) {
		t.Error("the writer changed bytes even though no floor moved; it must preserve every other byte")
	}

	// 2. A hand-lowered floor must go red.
	lowered := committed
	lowered.Suite--
	loweredRaw, changed, err := RewriteFloors(raw, lowered)
	if err != nil {
		t.Fatalf("lower the suite floor: %v", err)
	}
	if !changed {
		t.Fatal("lowering the suite floor by one produced no change, so this test is not exercising the guard")
	}
	got, err := ParseFloors(loweredRaw)
	if err != nil {
		t.Fatalf("parse the lowered declaration: %v", err)
	}
	if got != lowered {
		t.Fatalf("parsed floors %+v after lowering, want %+v", got, lowered)
	}
	if declared-got.Suite == ExpectedSkips {
		t.Fatalf("a floor one below the tree (suite %d, declared %d) still satisfies the ratchet; the guard "+
			"this packet must not weaken is gone", got.Suite, declared)
	}

	// 3. The writer restores it exactly.
	fixed, changed, err := RewriteFloors(loweredRaw, measured.Floors())
	if err != nil {
		t.Fatalf("restore the lowered declaration: %v", err)
	}
	if !changed {
		t.Fatal("the writer did not restore a lowered floor")
	}
	fixedFloors, err := ParseFloors(fixed)
	if err != nil {
		t.Fatalf("parse the restored declaration: %v", err)
	}
	if fixedFloors != committed {
		t.Errorf("the writer restored %+v, want the committed %+v", fixedFloors, committed)
	}
	if declared-fixedFloors.Suite != ExpectedSkips {
		t.Errorf("the restored floor does not satisfy the ratchet: declared %d - suite %d = %d, want %d",
			declared, fixedFloors.Suite, declared-fixedFloors.Suite, ExpectedSkips)
	}
	if string(fixed) != string(raw) {
		t.Error("the writer's repair is not byte-identical to the committed declaration, so it dropped a " +
			"comment or a blank line. The floors' prose is why the floors exist and must survive a rewrite")
	}
}

// TestTheFloorIsWhatRanNotWhatPassed is the arithmetic that lets the writer run
// on a red gate.
//
// When a test is added and the floor is stale, the ratchet test FAILS — it does
// not skip — so the run reports one fewer top-level pass and one more top-level
// failure. The floor is the number of tests that RAN, and counting only passes
// would derive a number one short and leave the gate red after a `--write`.
func TestTheFloorIsWhatRanNotWhatPassed(t *testing.T) {
	m := Measured{
		Packages:         32,
		TopLevel:         1369,
		Subtests:         1247,
		Failures:         2,
		Skips:            8,
		TopLevelFailures: 1,
	}
	got := m.Floors()
	if got.Suite != 1370 {
		t.Errorf("suite floor = %d, want 1370 (1369 passes + 1 top-level failure)", got.Suite)
	}
	if got.Subtests != 1248 {
		t.Errorf("subtests floor = %d, want 1248 (1247 passes + 1 subtest failure)", got.Subtests)
	}
}

// TestParseOutputReadsTheGatesOwnLine pins the one format the writer depends on,
// so renaming the summary line in bin/prime is a failure here rather than a
// writer that silently derives nothing.
func TestParseOutputReadsTheGatesOwnLine(t *testing.T) {
	out := strings.Join([]string{
		"ok  \tgithub.com/cafaye/cafe/internal/example\t0.1s",
		"--- PASS: TestA (0.00s)",
		"    --- PASS: TestA/sub (0.00s)",
		"--- FAIL: TestB (0.00s)",
		"    --- FAIL: TestB/sub (0.00s)",
		"cafe: 32 packages, 1370 top-level passes, 1248 subtest passes, 2 failures, 8 skips",
		"cafe: live tier: 0 of 7 executed; not enabled, so the 7 live tests skipped rather than ran.",
	}, "\n")

	m, err := ParseOutput([]byte(out))
	if err != nil {
		t.Fatalf("parse the gate's output: %v", err)
	}
	if m.Packages != 32 || m.TopLevel != 1370 || m.Subtests != 1248 || m.Failures != 2 || m.Skips != 8 {
		t.Fatalf("parsed %+v, want 32/1370/1248/2/8", m)
	}
	if m.TopLevelFailures != 1 {
		t.Errorf("top-level failures = %d, want 1 (only TestB is at column 0)", m.TopLevelFailures)
	}
	if got := m.Floors(); got.Suite != 1371 || got.Subtests != 1249 {
		t.Errorf("floors = %+v, want suite 1371, subtests 1249", got)
	}
}

// TestParseOutputRefusesARunWithNoSummary is the other side: output without the
// line is a run whose numbers cannot be derived from, and it must be an error
// rather than a Measured of zeros that would write a floor of zero.
func TestParseOutputRefusesARunWithNoSummary(t *testing.T) {
	if _, err := ParseOutput([]byte("ok  \tgithub.com/cafaye/cafe/internal/example\t0.1s\n")); err == nil {
		t.Fatal("ParseOutput accepted output with no summary line; a floor derived from it would be zero")
	}
}

// TestRewriteRefusesADeclarationWithAProofMissing is the refusal that keeps the
// writer from writing a document with one floor and two holes: a restructured
// gate.yml fails loudly here rather than being silently half-written.
func TestRewriteRefusesADeclarationWithAProofMissing(t *testing.T) {
	raw := []byte("gate:\n  proof:\n    - id: packages\n      match: 'x'\n      minimum: 1\n")
	if _, _, err := RewriteFloors(raw, Floors{Packages: 1, Suite: 2, Subtests: 3}); err == nil {
		t.Fatal("RewriteFloors accepted a declaration with no suite or subtests proof")
	}
	if _, err := ParseFloors(raw); err == nil {
		t.Fatal("ParseFloors accepted a declaration with no suite or subtests proof")
	}
}

// TestDeltaNamesOnlyWhatMoved is the review signal's own shape: the suite reads
// as "floors moved", the other two by name, and a floor that did not move is not
// named at all.
func TestDeltaNamesOnlyWhatMoved(t *testing.T) {
	d := Delta{Before: Floors{Packages: 32, Suite: 1370, Subtests: 1248}, After: Floors{Packages: 32, Suite: 1375, Subtests: 1250}}
	got := d.String()
	for _, want := range []string{"floors moved 1370→1375", "subtests 1248→1250"} {
		if !strings.Contains(got, want) {
			t.Errorf("delta %q does not name %q", got, want)
		}
	}
	if strings.Contains(got, "packages") {
		t.Errorf("delta %q names a floor that did not move", got)
	}
	if same := (Delta{Before: Floors{1, 2, 3}, After: Floors{1, 2, 3}}); same.Changed() || !strings.Contains(same.String(), "unchanged") {
		t.Errorf("an unchanged delta = %q, want it to say unchanged", same.String())
	}
}
