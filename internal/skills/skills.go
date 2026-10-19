package skills

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cafaye/cafe/internal/version"
)

const (
	skillRelativePath = ".agents/skills/cafaye/SKILL.md"
	booksDirEnv       = "CAFAYE_BOOKS_DIR"
)

//go:embed assets/cafaye/SKILL.md
var cafayeSkillBody string

type InstallResult struct {
	Path    string
	Updated bool
	// BackupPath is where the previous file was kept, when there was one and
	// it was not ours to replace. Empty when nothing was backed up.
	BackupPath string
}

func defaultBooksDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv(booksDirEnv)); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return home, nil
}

func DefaultBooksDir() (string, error) {
	return defaultBooksDir()
}

func renderSkill() string {
	return version.SkillHeader() + strings.TrimSpace(cafayeSkillBody) + "\n"
}

// installAtRoot writes the skill under root.
//
// It is careful in three ways, and each of them is a defect that was here
// once:
//
//   - it refuses a destination that is a symlink, or that sits under one.
//     os.ReadFile and os.WriteFile both follow symlinks, so a SKILL.md linked
//     to an unrelated file meant the install read through the link and wrote
//     through it, destroying the target's content. Whoever ran the command
//     chose where the link pointed; the CLI did not.
//   - it refuses to replace a file that does not carry cafe's management
//     header without keeping a copy of it first. The file it writes is the
//     person's own agent skill, and "the person edited it" is not a reason to
//     throw their edits away.
//   - it writes through a temporary file in the same directory and renames it
//     into place, so an interrupted install cannot leave a half-written skill
//     where a whole one used to be.
func installAtRoot(root string) (InstallResult, error) {
	target := filepath.Join(root, skillRelativePath)
	dir := filepath.Dir(target)
	if err := refuseSymlinkedDir(root, dir); err != nil {
		return InstallResult{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return InstallResult{}, err
	}

	content := renderSkill()
	previous, existed, err := readManagedTarget(target)
	if err != nil {
		return InstallResult{}, err
	}
	if existed && string(previous) == content {
		return InstallResult{Path: target, Updated: false}, nil
	}

	// A file that is not ours is backed up before it is replaced. The backup
	// is the whole difference between "cafe updated the skill" and "cafe
	// deleted your notes", and it costs one file.
	backup := ""
	if existed && !version.IsManaged(previous) {
		backup = target + ".bak"
		if err := os.WriteFile(backup, previous, 0o644); err != nil {
			return InstallResult{}, fmt.Errorf("back up %s: %w", target, err)
		}
	}
	if err := writeFileAtomic(target, []byte(content)); err != nil {
		return InstallResult{}, err
	}
	return InstallResult{Path: target, Updated: true, BackupPath: backup}, nil
}

// readManagedTarget reads the destination, refusing anything that is not a
// plain file.
//
// os.ReadFile follows a symlink, so a SKILL.md that points somewhere else
// would be read through and then overwritten through: the install would write
// into a file the CLI was never pointed at.
func readManagedTarget(path string) (content []byte, exists bool, err error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("%s is a symlink; refusing to write through it", path)
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s is not a regular file; refusing to replace it", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// refuseSymlinkedDir refuses a destination whose directory chain contains a
// symlink below the root.
//
// Writing through one would put the file somewhere other than where the
// caller said, which is the same failure as the symlinked file itself. The
// root itself is the caller's business: a root that is a symlink is a root
// the caller chose.
func refuseSymlinkedDir(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return err
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// The rest of the chain does not exist yet, and the
				// directories that are made below are made by us.
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; refusing to install through it", current)
		}
	}
	return nil
}

// writeFileAtomic writes through a temporary file in the same directory and
// renames it into place.
//
// A crash mid-write leaves the previous file whole and a stray temporary
// file, which is a state somebody can see. The alternative is a truncated
// skill file, which is a state nobody can see until an agent reads it and
// acts on half of it.
func writeFileAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".SKILL.md.*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	// The remove is a no-op once the rename has happened, and it is the
	// difference between a failed install and a directory full of debris.
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func EnsureDefaultInstalled() (InstallResult, error) {
	root, err := defaultBooksDir()
	if err != nil {
		return InstallResult{}, err
	}
	abs, err := resolveRoot(root)
	if err != nil {
		return InstallResult{}, err
	}
	return installAtRoot(abs)
}

func InstallForRoot(root string) (InstallResult, error) {
	if strings.TrimSpace(root) == "" {
		return InstallResult{}, fmt.Errorf("root path cannot be empty")
	}
	abs, err := resolveRoot(root)
	if err != nil {
		return InstallResult{}, err
	}
	return installAtRoot(abs)
}

// resolveRoot turns the caller's root into an absolute path and insists that
// it is a directory that exists.
//
// A relative root makes the printed skill_path meaningful only from the
// directory the command ran in, which is a path a script cannot use. A root
// that does not exist is a typo, not an instruction to build a tree.
func resolveRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	return abs, nil
}
