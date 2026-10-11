package scaffold

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Write puts the files under root, refusing any that already exist unless
// force is set.
//
// The refusal is checked for every file before the first one is written, and
// that ordering is the whole point: a writer that stopped halfway would leave
// a half-scaffolded project whose next run refuses the half it had already
// done, which is a project a human has to reason about. This is the same
// contract `cafe gen` holds — refused before anything is written, so the report
// is about every file at once rather than about whichever happened to be
// first.
func Write(root string, files []File, force bool) error {
	existing := existingFiles(files, root)
	if len(existing) > 0 && !force {
		return errRefusedExisting(existing)
	}
	for _, file := range files {
		if err := writeOne(root, file); err != nil {
			return err
		}
	}
	return nil
}

// existingFiles is the subset of paths already on disk.
//
// os.Stat and not os.ReadFile: the question is whether the file is there, and
// a directory is a refusal rather than a write error, so both come back from
// the same check.
func existingFiles(files []File, root string) []string {
	var found []string
	for _, file := range files {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(file.Path))); err == nil {
			found = append(found, file.Path)
		} else if !errors.Is(err, os.ErrNotExist) {
			// A path that cannot be stated is treated as existing: the honest
			// answer to "can I write here?" when the filesystem will not say is
			// no, and the write below reports the real error if there is one.
			found = append(found, file.Path)
		}
	}
	return found
}

// writeOne writes one file, creating the directories above it.
func writeOne(root string, file File) error {
	path := filepath.Join(root, filepath.FromSlash(file.Path))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, file.Content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", file.Path, err)
	}
	return nil
}

// errRefusedExisting says what is in the way and what to do about it.
//
// It says more than "-force", because "-force" alone assumes the reader knows
// the file is generated and it may not be: `<name>/cafaye.yml` could be a
// manifest somebody wrote by hand under that name. Overwriting that would
// destroy work, and a scaffolder that can do it silently is a scaffolder
// nobody runs twice.
func errRefusedExisting(existing []string) error {
	return fmt.Errorf("nothing was written; %s already exist. cafe does not overwrite a scaffolded project "+
		"without being told to, and it cannot tell a generated one from one somebody wrote under the same "+
		"name — so look at %s first, then re-run with -force if they are cafe's",
		plural(len(existing), "file"), strings.Join(existing, ", "))
}

// plural is the singular/plural helper the CLI has, kept local so this package
// stays independent of internal/cli.
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
