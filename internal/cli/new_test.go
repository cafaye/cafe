package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anywaye/cafe/internal/contract"
)

// `cafe new` scaffolds into ./<name>, so every case here runs in a directory
// the test owns. t.Chdir is Go 1.24's and restores on cleanup; the cases are
// serial as a result, which is the price of a command whose target directory
// is relative to the working one.

// The happy path, and the packet's whole claim at the command surface: the
// scaffolded project's manifest validates against core's schema AND passes
// `cafe contract lint`. The scaffold test in internal/scaffold proves the same
// pair over the package; this one proves the CLI wrote the bytes that lint
// then reads off disk.
func TestNewScaffoldsAProjectThatLints(t *testing.T) {
	t.Chdir(t.TempDir())

	code, stdout, stderr := runCLI(t, testVersion, "new", "--template", "app", "acme")

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}
	for _, want := range []string{"anywaye.yml", "main.go", "README.md", "docker/Dockerfile"} {
		if _, err := os.Stat(filepath.Join("acme", filepath.FromSlash(want))); err != nil {
			t.Errorf("%s was not written: %v", want, err)
		}
	}
	// Every file is printed in full: a scaffold nobody can inspect is a
	// scaffold nobody can debug.
	if !strings.Contains(stdout, "===== anywaye.yml =====") || !strings.Contains(stdout, "name: acme") {
		t.Errorf("the scaffold was not printed in full:\n%s", stdout)
	}

	report, err := contract.Lint("acme")
	if err != nil {
		t.Fatalf("contract.Lint: %v", err)
	}
	if !report.OK() {
		t.Errorf("the scaffolded project does not lint:\n%s", report)
	}
}

// A second scaffold does not clobber the first: the refusal names the file in
// the way, writes nothing, and -force is the only way past it.
func TestNewRefusesToClobberAnExistingProject(t *testing.T) {
	t.Chdir(t.TempDir())
	if code, _, stderr := runCLI(t, testVersion, "new", "acme"); code != exitSuccess {
		t.Fatalf("first scaffold exited %d (stderr: %s)", code, stderr)
	}
	edited := []byte("# hand-edited by a person\n")
	if err := os.WriteFile(filepath.Join("acme", "anywaye.yml"), edited, 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runCLI(t, testVersion, "new", "acme")

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "anywaye.yml") {
		t.Errorf("the refusal does not name the file in the way: %s", stderr)
	}
	after, err := os.ReadFile(filepath.Join("acme", "anywaye.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(edited) {
		t.Error("a refused scaffold still wrote: the edit is gone")
	}

	if code, _, stderr := runCLI(t, testVersion, "new", "-force", "acme"); code != exitSuccess {
		t.Fatalf("scaffold with -force exited %d (stderr: %s)", code, stderr)
	}
	forced, err := os.ReadFile(filepath.Join("acme", "anywaye.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(forced) == string(edited) {
		t.Error("-force left the original bytes: the overwrite did not happen")
	}
}

// An illegal name is refused by name, with the rules stated, and nothing is
// written — a person who typed `cafe new My_App` gets a sentence, not a
// directory. (A name beginning with a dash is a flag to the flag package and
// is rejected as one; the pattern's remaining cases live in
// internal/scaffold's own table, which drives them through ValidateName
// directly.)
func TestNewRefusesAnIllegalName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "My_App", want: "anywaye namespace name"},
		{name: "anywaye", want: "reserved by the platform"},
		{name: strings.Repeat("x", 41), want: "anywaye namespace name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())

			code, _, stderr := runCLI(t, testVersion, "new", tt.name)

			if code != exitFailure {
				t.Errorf("exit code = %d, want %d", code, exitFailure)
			}
			if !strings.Contains(stderr, tt.want) {
				t.Errorf("the refusal does not name the rule (%q): %s", tt.want, stderr)
			}
			if _, err := os.Stat(tt.name); !os.IsNotExist(err) {
				t.Error("a refused scaffold still created the directory")
			}
		})
	}
}

// -name overrides the directory: a repository whose directory is not its
// service name is exactly that case, and the manifest carries the name while
// the directory carries the directory.
func TestNewHonoursTheNameOverride(t *testing.T) {
	t.Chdir(t.TempDir())

	code, _, stderr := runCLI(t, testVersion, "new", "-name", "acme", "acme-worktree")

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}
	manifest, err := os.ReadFile(filepath.Join("acme-worktree", "anywaye.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), "name: acme") {
		t.Errorf("the manifest does not carry the override:\n%s", manifest)
	}
}
