package gate

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Measured is one run of the suite, as the gate's own summary line reports it,
// plus the one split that line does not carry: how many of the failures were
// top-level tests rather than subtests.
type Measured struct {
	Packages int
	// TopLevel is `--- PASS:` lines at column 0: the top-level tests that ran
	// and passed.
	TopLevel int
	// Subtests is every `--- PASS:` line minus the top-level ones.
	Subtests int
	// Failures is every `--- FAIL:` line, at any depth.
	Failures int
	// Skips is every `--- SKIP:` line, at any depth.
	Skips int
	// TopLevelFailures is `--- FAIL:` lines at column 0.
	TopLevelFailures int
}

// Floors is what this run implies the floors should be.
//
// The floor is the number of tests that RAN, not the number that passed, and
// that distinction is what makes the writer safe to run while the gate is red
// for exactly the reason it exists to fix. Add a test and the ratchet test
// itself fails; the summary line then reports one fewer top-level pass and one
// more top-level failure, and `TopLevel + TopLevelFailures` is unchanged — the
// writer still derives the number the merged tree needs, rather than the number
// a red run happened to report. Counting only passes would have derived a floor
// one short, written it, and left the gate red.
func (m Measured) Floors() Floors {
	return Floors{
		Packages: m.Packages,
		Suite:    m.TopLevel + m.TopLevelFailures,
		Subtests: m.Subtests + (m.Failures - m.TopLevelFailures),
	}
}

// summaryLine matches `bin/prime`'s accounting line, anchored at both ends and
// including the literal `0 failures` and the skip count. It is the same shape
// `gate.yml`'s `suite` proof matches, for the same reason: a floor is only ever
// read out of a line a real run printed.
//
// The failures field is read but NOT required to be zero, unlike the proof's
// pattern. The proof refuses a red run because a floor must come from a green
// one; the writer deliberately does not, because the common reason to run it is
// a gate that is red on the stale floor, and the number it needs is still on the
// line.
var summaryLine = regexp.MustCompile(`^cafe: (\d+) packages, (\d+) top-level passes, (\d+) subtest passes, (\d+) failures, (\d+) skips$`)

// ParseOutput reads a Measured out of a gate run's output.
//
// It is pure and takes bytes rather than running anything, so the writer's
// arithmetic is tested against a captured run instead of against a suite the
// test would have to invoke — and invoking it would recurse, because the test is
// part of the suite the writer measures.
func ParseOutput(out []byte) (Measured, error) {
	var m Measured
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		if match := summaryLine.FindStringSubmatch(line); match != nil {
			m.Packages = atoi(match[1])
			m.TopLevel = atoi(match[2])
			m.Subtests = atoi(match[3])
			m.Failures = atoi(match[4])
			m.Skips = atoi(match[5])
			found = true
		}
		// `bin/prime` counts every `--- FAIL:` line, so the total comes from the
		// summary above and only the top-level split is counted here. A
		// top-level failure is the one go test prints at column 0; a subtest's is
		// indented under its parent.
		if strings.HasPrefix(line, "--- FAIL: ") {
			m.TopLevelFailures++
		}
	}
	if !found {
		return Measured{}, fmt.Errorf("the gate's summary line did not appear in the output. The writer " +
			"reads the floors out of `cafe: N packages, N top-level passes, …`, so output without it is a " +
			"run whose numbers cannot be derived from")
	}
	return m, nil
}

// Measure runs the gate and reads its numbers.
//
// It runs `bin/prime` rather than `go test -v ./...` so the counts are the
// gate's own — the same command `gate.yml` declares, the same accounting
// `internal/ci` pins — and not a second implementation of "count the tests" that
// could disagree with the line the floors are read from.
//
// A non-zero exit is NOT an error here, and that is the important case: the
// writer is run on a gate that is red precisely because the floor is stale, and
// the summary line is printed either way. The error is reserved for output with
// no summary line at all, which is a run that proved nothing.
func Measure(root string) (Measured, error) {
	cmd := exec.CommandContext(context.Background(), filepath.Join(root, "bin", "prime"))
	cmd.Dir = root
	out, err := cmd.CombinedOutput()

	m, parseErr := ParseOutput(out)
	if parseErr != nil {
		if err != nil {
			return Measured{}, fmt.Errorf("run bin/prime: %w\n%s", err, out)
		}
		return Measured{}, parseErr
	}
	return m, nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
