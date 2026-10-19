package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cafaye/cafe/internal/version"
)

// The skill file is the PERSON'S file: it lives in their agent's directory
// and their agent reads it. So the install has to be careful in three ways —
// it must not write through a symlink, it must not throw away an edit it did
// not make, and it must not leave half a file behind. Each of those is a case
// below.

// skillPath is where the skill lands under a root.
func skillPath(root string) string {
	return filepath.Join(root, skillRelativePath)
}

// A fresh root gets the skill, with the header that says who wrote it.
func TestAFreshRootGetsTheSkill(t *testing.T) {
	root := t.TempDir()

	res, err := InstallForRoot(root)
	if err != nil {
		t.Fatalf("InstallForRoot: %v", err)
	}
	if !res.Updated {
		t.Error("Updated = false, want true for a fresh root")
	}
	if res.BackupPath != "" {
		t.Errorf("BackupPath = %q, want empty: there was nothing to back up", res.BackupPath)
	}
	if res.Path != skillPath(root) {
		t.Errorf("Path = %q, want %q", res.Path, skillPath(root))
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("read the installed skill: %v", err)
	}
	if !version.IsManaged(data) {
		t.Errorf("the installed skill does not carry the management header:\n%s", string(data)[:80])
	}
}

// A file that is already what the CLI would write is left alone. The install
// says so, and it does not touch the file: a rewrite of an unchanged file is
// a write for nothing, and on a machine where the skill is read-only it is a
// failure.
func TestAnIdenticalFileIsNotRewritten(t *testing.T) {
	root := t.TempDir()
	first, err := InstallForRoot(root)
	if err != nil {
		t.Fatalf("InstallForRoot: %v", err)
	}
	before, err := os.Stat(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	// Long enough that a rewrite would move the timestamp on any filesystem
	// with a coarse clock.
	time.Sleep(20 * time.Millisecond)

	second, err := InstallForRoot(root)
	if err != nil {
		t.Fatalf("InstallForRoot: %v", err)
	}
	if second.Updated {
		t.Error("Updated = true, want false: the file was already current")
	}
	after, err := os.Stat(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("the file was rewritten even though it was already current")
	}
}

// A file the person edited is THEIRS. It is backed up before it is replaced,
// and the backup is named in the result so the CLI can say where it went.
func TestAUserEditedFileIsBackedUp(t *testing.T) {
	root := t.TempDir()
	target := skillPath(root)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	user := "# my own notes\n\ndo not lose this\n"
	if err := os.WriteFile(target, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := InstallForRoot(root)
	if err != nil {
		t.Fatalf("InstallForRoot: %v", err)
	}
	if !res.Updated {
		t.Error("Updated = false, want true: the file did not carry our header")
	}
	if res.BackupPath == "" {
		t.Fatal("BackupPath is empty: the person's file was overwritten with nothing kept")
	}
	backup, err := os.ReadFile(res.BackupPath)
	if err != nil {
		t.Fatalf("read the backup: %v", err)
	}
	if string(backup) != user {
		t.Errorf("the backup holds %q, want %q", string(backup), user)
	}
	installed, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !version.IsManaged(installed) {
		t.Error("the installed file does not carry the management header")
	}
}

// A file that IS ours is replaced without a backup: the backup exists to keep
// the person's work, and there is none to keep.
func TestAManagedFileIsReplacedWithoutABackup(t *testing.T) {
	root := t.TempDir()
	target := skillPath(root)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	old := "<!-- managed-by: cafe-cli | cli_version: 0.9.0 -->\n\nthe old one\n"
	if err := os.WriteFile(target, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := InstallForRoot(root)
	if err != nil {
		t.Fatalf("InstallForRoot: %v", err)
	}
	if !res.Updated {
		t.Error("Updated = false, want true")
	}
	if res.BackupPath != "" {
		t.Errorf("BackupPath = %q, want empty: the file was ours to replace", res.BackupPath)
	}
}

// A symlinked destination is refused. os.ReadFile and os.WriteFile both
// follow symlinks, so the old install read through the link and wrote through
// it: whoever ran the command chose where the link pointed, and the CLI
// destroyed that file.
func TestASymlinkedTargetIsRefused(t *testing.T) {
	root := t.TempDir()
	target := skillPath(root)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(root, "important.txt")
	const contents = "do not touch"
	if err := os.WriteFile(elsewhere, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, target); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	_, err := InstallForRoot(root)
	if err == nil {
		t.Fatal("InstallForRoot succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("err = %v, want it to name the symlink", err)
	}
	got, err := os.ReadFile(elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != contents {
		t.Errorf("the link's target now holds %q, want %q", string(got), contents)
	}
}

// A directory in the chain that is a symlink is refused for the same reason:
// writing through it puts the file somewhere other than where the caller said.
func TestASymlinkedDirectoryIsRefused(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(root, ".agents")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	_, err := InstallForRoot(root)
	if err == nil {
		t.Fatal("InstallForRoot succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("err = %v, want it to name the symlink", err)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "skills")); err == nil {
		t.Error("the install wrote through the symlinked directory")
	}
}

// A directory that cannot be written to is a clean refusal, and nothing is
// half-written.
func TestAReadOnlyDirectoryIsACleanError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Dir(skillPath(root))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	readOnlyDir(t, dir)

	_, err := InstallForRoot(root)
	if err == nil {
		t.Fatal("InstallForRoot succeeded, want a refusal")
	}
	if _, err := os.Lstat(filepath.Join(dir, "SKILL.md")); err == nil {
		t.Error("a file was written into a directory that could not be written to")
	}
}

// A failure part-way through leaves the previous file whole. The write goes
// through a temporary file and a rename, so the old file is either there or
// replaced — never truncated.
func TestAFailedInstallLeavesThePreviousFileIntact(t *testing.T) {
	root := t.TempDir()
	target := skillPath(root)
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// An OLD managed file: cafe owns it, so the install would replace it, and
	// the replacement is the thing that has to be atomic.
	old := "<!-- managed-by: cafe-cli | cli_version: 0.9.0 -->\n\nthe old one\n"
	if err := os.WriteFile(target, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	readOnlyDir(t, dir)

	if _, err := InstallForRoot(root); err == nil {
		t.Fatal("InstallForRoot succeeded, want a failure")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("the previous file is gone: %v", err)
	}
	if string(got) != old {
		t.Errorf("the previous file now holds %q, want %q", string(got), old)
	}
}

// A relative root becomes an absolute one, so the path the CLI prints is a
// path a script can use.
func TestARelativeRootBecomesAnAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Mkdir("workspace", 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := InstallForRoot("workspace")
	if err != nil {
		t.Fatalf("InstallForRoot: %v", err)
	}
	if !filepath.IsAbs(res.Path) {
		t.Errorf("Path = %q, want an absolute path", res.Path)
	}
	want := filepath.Join(dir, "workspace", skillRelativePath)
	if res.Path != want {
		t.Errorf("Path = %q, want %q", res.Path, want)
	}
}

// A root that is not there is a typo, not an instruction to build a tree.
func TestARootThatDoesNotExistIsRefused(t *testing.T) {
	_, err := InstallForRoot(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("InstallForRoot succeeded, want a refusal")
	}
}

// A root that is a file is not a root.
func TestARootThatIsNotADirectoryIsRefused(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := InstallForRoot(file)
	if err == nil {
		t.Fatal("InstallForRoot succeeded, want a refusal")
	}
}

// The stamp on the installed skill is the version that wrote it. It used to
// be "dev" in every release, because the release build never injected one.
func TestTheRenderedSkillCarriesTheVersion(t *testing.T) {
	old := version.Current
	version.Current = "9.9.9"
	t.Cleanup(func() { version.Current = old })

	root := t.TempDir()
	res, err := InstallForRoot(root)
	if err != nil {
		t.Fatalf("InstallForRoot: %v", err)
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "cli_version: 9.9.9") {
		t.Errorf("the installed skill does not carry the version:\n%s", string(data)[:80])
	}
}

// readOnlyDir makes dir read-only and reports whether the mode bits actually
// deny writes. On a machine where the test runs as root they do not, and a
// case built on them would prove nothing.
func readOnlyDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	probe := filepath.Join(dir, ".probe")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err == nil {
		_ = os.Remove(probe)
		t.Skip("this user can write to a read-only directory; the case would prove nothing")
	}
}
