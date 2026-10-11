package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cafaye/cafe/internal/contract"
)

// `cafe init` is for a project that already exists, so it writes the manifest
// and only the manifest — and the file it refuses to touch is the whole point
// of the command.

func TestInitWritesOnlyTheManifest(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	if err := os.Mkdir("acme", 0o755); err != nil {
		t.Fatal(err)
	}
	// A file the project's author wrote, which init must leave alone.
	existing := []byte("package main\n")
	if err := os.WriteFile(filepath.Join("acme", "main.go"), existing, 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCLI(t, testVersion, "init", "-dir", "acme", "-name", "acme")

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}
	if !strings.Contains(stdout, "===== cafaye.yml =====") {
		t.Errorf("the manifest was not printed:\n%s", stdout)
	}
	// Only the manifest was ADDED: the author's main.go stays untouched, and
	// no second file appeared beside it.
	entries, err := os.ReadDir("acme")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "cafaye.yml" && entry.Name() != "main.go" {
			t.Errorf("init wrote %s, want only cafaye.yml", entry.Name())
		}
	}
	after, err := os.ReadFile(filepath.Join("acme", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(existing) {
		t.Error("init touched a file it does not own")
	}

	report, err := contract.Lint("acme")
	if err != nil {
		t.Fatalf("contract.Lint: %v", err)
	}
	if !report.OK() {
		t.Errorf("the initialized project does not lint:\n%s", report)
	}
}

// The name is derived from the directory unless -name overrides it, and a
// directory that cannot hold a service name is refused rather than guessed at.
func TestInitDerivesTheNameFromTheDirectory(t *testing.T) {
	t.Run("the directory is the name", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := os.Mkdir("acme", 0o755); err != nil {
			t.Fatal(err)
		}

		code, _, stderr := runCLI(t, testVersion, "init", "-dir", "acme")

		if code != exitSuccess {
			t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
		}
		manifest, err := os.ReadFile(filepath.Join("acme", "cafaye.yml"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(manifest), "name: acme") {
			t.Errorf("the manifest does not carry the directory's name:\n%s", manifest)
		}
	})

	t.Run("the directory cannot hold a service name", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := os.Mkdir("001", 0o755); err != nil {
			t.Fatal(err)
		}

		code, _, stderr := runCLI(t, testVersion, "init", "-dir", "001")

		if code != exitFailure {
			t.Errorf("exit code = %d, want %d", code, exitFailure)
		}
		if !strings.Contains(stderr, "cafaye namespace name") {
			t.Errorf("the refusal does not name the rule: %s", stderr)
		}
	})
}

// An existing manifest is refused rather than overwritten — it may be one
// somebody wrote by hand — and -force is the deliberate override.
func TestInitRefusesAnExistingManifest(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	if err := os.Mkdir("acme", 0o755); err != nil {
		t.Fatal(err)
	}
	handWritten := []byte("name: acme\n# written by a person\n")
	if err := os.WriteFile(filepath.Join("acme", "cafaye.yml"), handWritten, 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runCLI(t, testVersion, "init", "-dir", "acme")

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "cafaye.yml") {
		t.Errorf("the refusal does not name the file in the way: %s", stderr)
	}
	after, err := os.ReadFile(filepath.Join("acme", "cafaye.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(handWritten) {
		t.Error("a refused init still wrote: the hand-written manifest is gone")
	}

	if code, _, stderr := runCLI(t, testVersion, "init", "-force", "-dir", "acme"); code != exitSuccess {
		t.Fatalf("init with -force exited %d (stderr: %s)", code, stderr)
	}
	forced, err := os.ReadFile(filepath.Join("acme", "cafaye.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(forced), "written by a person") {
		t.Error("-force left the original bytes: the overwrite did not happen")
	}
}

// An argument is a usage mistake: init takes flags only, like every command
// whose target is a directory rather than a positional.
func TestInitTakesNoArguments(t *testing.T) {
	code, _, _ := runCLI(t, testVersion, "init", "acme")

	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
}
