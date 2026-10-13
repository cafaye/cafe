package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cafaye/cafe/internal/gate"
)

// TestGateFloorsDryRunPrintsTheDeltaAndWritesNothing drives the command through
// its measure seam, so the whole path runs without invoking the suite the writer
// would otherwise run — and without recursing, because this test is part of that
// suite.
//
// The delta is the product: a floor that MOVES is the one thing a reviewer has
// to see, and this pins that the command says which floor moved and by how much,
// and that the default run writes nothing.
func TestGateFloorsDryRunPrintsTheDeltaAndWritesNothing(t *testing.T) {
	root := t.TempDir()
	body := []byte("gate:\n  proof:\n" +
		"    - id: packages\n      match: 'x'\n      minimum: 32\n" +
		"    - id: suite\n      match: 'x'\n      minimum: 1370\n" +
		"    - id: subtests\n      match: 'x'\n      minimum: 1248\n")
	path := filepath.Join(root, "gate.yml")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	c := newGateCommand(gateDeps{measure: func(string) (gate.Measured, error) {
		return gate.Measured{Packages: 32, TopLevel: 1375, Subtests: 1250}, nil
	}})

	env := newTestEnv()
	if err := c.Execute(env, []string{"floors", root}); err != nil {
		t.Fatalf("cafe gate floors: %v", err)
	}

	out := written(t, env.Stdout)
	for _, want := range []string{"floors moved 1370→1375", "subtests 1248→1250"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not name %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "packages 32→32") {
		t.Errorf("output names a floor that did not move:\n%s", out)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read %s: %v", path, err)
	}
	if string(after) != string(body) {
		t.Error("the dry run modified gate.yml; without --write the command must only report")
	}
}
