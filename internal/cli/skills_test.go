package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `cafe skills install` writes the agent's own skill file, so its output is a
// promise about a file on disk: where it went, and whether it changed.

// The install says where the skill went and whether it wrote it.
func TestSkillsInstallSaysWhereItWrote(t *testing.T) {
	root := t.TempDir()

	code, stdout, stderr := runCLI(t, testVersion, "skills", "install", "--root", root)

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}
	want := filepath.Join(root, ".agents", "skills", "cafaye", "SKILL.md")
	if !strings.Contains(stdout, "skill_path: "+want) {
		t.Errorf("stdout does not name the installed file\ngot:\n%s", stdout)
	}
	if !strings.Contains(stdout, "updated: true") {
		t.Errorf("stdout does not say the file was written\ngot:\n%s", stdout)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("the skill was not written: %v", err)
	}
}

// A flag the command does not declare is a mistake in how cafe was invoked:
// exit 2, not exit 1. It used to be a bare flag error, so a typo exited 1
// and the exit-code contract did not say what it should have.
func TestSkillsInstallRefusesAnUnknownFlag(t *testing.T) {
	code, _, stderr := runCLI(t, testVersion, "skills", "install", "--nope")

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitUsage, stderr)
	}
	if !strings.Contains(stderr, "not defined") {
		t.Errorf("stderr does not name the unknown flag\ngot:\n%s", stderr)
	}
}

// `--help` is not a mistake, and the group help is printed instead of an
// error: the exit code says the command succeeded, and the help says what
// the command does.
func TestSkillsInstallHelpIsNotAFailure(t *testing.T) {
	code, stdout, stderr := runCLI(t, testVersion, "skills", "install", "--help")

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}
	if !strings.Contains(stdout, "cafe skills") {
		t.Errorf("help was not printed\ngot:\n%s", stdout)
	}
}
