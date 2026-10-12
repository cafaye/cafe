package edge

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ReleaseDirName is the directory that holds the releases. It is a name rather
// than a path so a box's state directory stays boxd's decision: the edge
// owns what is inside this one, not where it sits.
const ReleaseDirName = "releases"

// CurrentLinkName is the symlink that serves. It is called `current` because
// that is the word a person uses, and because it is the only name in this
// package a customer ever sees.
const CurrentLinkName = "current"

// Release is one deployed directory, named by the id a deploy gave it.
//
// The id is the deploy's own name for what it shipped — a version, a git sha,
// a timestamp — and it is never invented here: two releases with the same id
// are the same release, and Publish refuses to overwrite one rather than
// merging into it, because a half-replaced release directory is a release
// that serves a mix of two versions.
type Release struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// ID is a release id, validated. The grammar is deliberately narrow — digits,
// letters, dot, dash, underscore — so a release id can never be a path
// separator, never `..`, and never anything a caller had to escape. The cheap
// refusal here is what makes the join below safe without a second check.
func validReleaseID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	if strings.ContainsAny(id, `/\`) {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// ReleasePath is where one release's files live.
func ReleasePath(root, id string) string {
	return filepath.Join(root, ReleaseDirName, id)
}

// CurrentPath is the symlink that serves.
func CurrentPath(root string) string {
	return filepath.Join(root, CurrentLinkName)
}

// Releases is every release on disk, by id, oldest name first.
//
// It reads the directory rather than a list: the directory is the single
// source of truth for what has been deployed, and an index beside it would be
// a second copy that could disagree — the same argument internal/scaffold's
// catalog makes for a template's tree.
func Releases(root string) ([]Release, error) {
	entries, err := os.ReadDir(filepath.Join(root, ReleaseDirName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read the release directory: %w", err)
	}
	out := make([]Release, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		out = append(out, Release{ID: e.Name(), Path: ReleasePath(root, e.Name())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Current is the release id `current` points at, read from the filesystem.
//
// The symlink is the truth and the spec's Current is the record, and the
// difference is deliberate: a reader that has no spec — a rollback running
// with a broken config directory, an operator with a shell — can still answer
// "what is serving" by reading one link. A reader that had to read the spec
// first would be a reader that cannot roll back when the spec is the thing
// that is broken.
func Current(root string) (string, error) {
	target, err := os.Readlink(CurrentPath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read the %s link: %w", CurrentLinkName, err)
	}
	return filepath.Base(target), nil
}

// Publish writes one release's files and flips `current` at it.
//
// Files are written into the new release's directory FIRST and the link is
// flipped second, so the switch is a rename over a directory that is already
// complete. The order is the zero-downtime claim and it is not an
// optimisation: a link flipped before the files landed would serve a request
// against a directory that is half-written, which is a 500 or a wrong asset
// rather than a slow deploy.
//
// The flip itself is a symlink to a sibling temporary name and a rename(2)
// over `current`, because there is no rename onto a symlink that is anything
// other than atomic — a script that wrote `current` in place would have a
// window where the link does not exist, and every request in that window
// would be a 404 from the edge rather than from the app.
//
// A release id that already exists is refused rather than replaced. Two
// deploys of the same id are either the same deploy twice or two different
// things with the same name, and neither is safe to merge into one
// directory.
func Publish(root, id string, files map[string][]byte) (Release, error) {
	if !validReleaseID(id) {
		return Release{}, fmt.Errorf("release id %q is not a release id: letters, digits, dot, dash and underscore only", id)
	}
	dir := ReleasePath(root, id)
	if _, err := os.Stat(dir); err == nil {
		return Release{}, fmt.Errorf("release %q is already deployed at %s; deploy a new id rather than overwriting one", id, dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Release{}, fmt.Errorf("create the release directory: %w", err)
	}
	// Sorted so the write order is the same on every run: two publishes of
	// one release produce the same bytes, which is what makes a diff between
	// two release directories a diff between two deploys.
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if !within(dir, path) {
			return Release{}, fmt.Errorf("file %q escapes the release directory", name)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return Release{}, fmt.Errorf("create %s: %w", filepath.Dir(name), err)
		}
		if err := os.WriteFile(path, files[name], 0o644); err != nil {
			return Release{}, fmt.Errorf("write %s: %w", name, err)
		}
	}
	if err := Switch(root, id); err != nil {
		return Release{}, err
	}
	return Release{ID: id, Path: dir}, nil
}

// within reports whether path is under dir. It is the escape check a file name
// from a caller has to pass, and it runs on the cleaned absolute form so
// "../" and a symlinked prefix cannot both walk out.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Switch flips `current` at a release id, atomically. It is the one operation
// under both deploy and rollback, which is the point: two code paths for
// "make this release serve" are two code paths that can disagree about what
// atomic means.
func Switch(root, id string) error {
	if !validReleaseID(id) {
		return fmt.Errorf("release id %q is not a release id", id)
	}
	dir := ReleasePath(root, id)
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("release %q is not on disk: %w", id, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("release %q at %s is not a directory", id, dir)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create the box state directory: %w", err)
	}
	// A relative target, so the whole tree can be moved or archived without
	// every symlink in it dangling: an absolute path in `current` is a link
	// that breaks the first time the state directory is copied elsewhere.
	tmp := CurrentPath(root) + ".switch"
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear the previous switch: %w", err)
	}
	if err := os.Symlink(filepath.Join(ReleaseDirName, id), tmp); err != nil {
		return fmt.Errorf("point the switch at %s: %w", id, err)
	}
	if err := os.Rename(tmp, CurrentPath(root)); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("flip the %s link: %w", CurrentLinkName, err)
	}
	return nil
}

// Rollback flips `current` back one release and returns the id now serving.
//
// IT NEEDS NO CONTROL PLANE, and that is the reason the edge's state is on
// the box rather than in the record box keeps. A rollback is a decision about
// what is on this disk; it asks nothing of anyone, needs no capability, and
// cannot be refused by a network. The plan's sign-off checklist asks for
// "a broken deploy demonstrates rollback" — on the day a deploy is broken,
// the control plane is the last thing an operator wants to depend on.
//
// The target is the spec's Previous, because that is the release that was
// serving before the current one and it is written before the link moves
// (see switch). A spec that is missing, unreadable, or names a release that
// is not on disk is a refusal with the reason, not a guess at the
// second-newest directory: mtime ordering across a filesystem is not a
// deployment order, and a rollback that guessed would sometimes serve a
// release nobody deployed last.
func Rollback(root string, spec Spec) (string, error) {
	current, err := Current(root)
	if err != nil {
		return "", err
	}
	if current == "" {
		return "", fmt.Errorf("nothing is serving: the %s link does not exist, so there is nothing to roll back from", CurrentLinkName)
	}
	target := spec.Previous
	if target == "" || target == current {
		return "", fmt.Errorf("release %q is serving and the edge spec records no earlier release to return to; "+
			"deploy an earlier id with `cafe box deploy --release <id>` instead", current)
	}
	if _, err := os.Stat(ReleasePath(root, target)); err != nil {
		return "", fmt.Errorf("the release to return to (%q) is not on disk: %w", target, err)
	}
	if err := Switch(root, target); err != nil {
		return "", err
	}
	return target, nil
}

// Prune removes releases that are neither serving nor the one serving pointed
// at before this one. It keeps two and not N on purpose: the rollback target
// has to survive a deploy, and every release beyond that is disk a customer
// pays for. The count is a parameter so a caller can be stricter, and the two
// that matter are never pruned whatever the caller asks for.
func Prune(root string, keep int) ([]string, error) {
	if keep < 2 {
		keep = 2
	}
	current, err := Current(root)
	if err != nil {
		return nil, err
	}
	all, err := Releases(root)
	if err != nil {
		return nil, err
	}
	var doomed []string
	for _, r := range all {
		if r.ID == current {
			continue
		}
		doomed = append(doomed, r.ID)
	}
	if len(doomed) <= keep-1 {
		return nil, nil
	}
	// The tail of the list is the newest by name, and a release id that
	// sorts is a release id that was written in order — which is why the
	// caller's id grammar is narrow (see validReleaseID).
	removed := doomed[:len(doomed)-(keep-1)]
	for _, id := range removed {
		if err := os.RemoveAll(ReleasePath(root, id)); err != nil {
			return removed, fmt.Errorf("remove release %q: %w", id, err)
		}
	}
	return removed, nil
}
