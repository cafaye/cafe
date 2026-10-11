package stack

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The layout, as names rather than as strings scattered through the code. A
// directory name that appears in two spellings is how `current` ends up
// pointing at something nothing reads.
const (
	releasesDir = "releases"
	currentLink = "current"
	historyFile = "history.json"
	logsDir     = "logs"
)

// historyVersion is the format of history.json. It exists so a box running an
// older cafe can be told its history is in a shape it does not understand
// rather than silently reading a field that moved.
const historyVersion = 1

// DefaultKeep is how many releases stay on the box when nothing says
// otherwise. Five is Kamal's number for the same reason: enough to roll back
// more than once, small enough that the disk cost is a rounding error.
const DefaultKeep = 5

// Outcome is what a release turned out to be. It is a closed set because the
// history is read by a human looking for the one line that went wrong, and a
// vocabulary that grows per caller makes that impossible.
type Outcome string

const (
	// Live is the release `current` points at right now.
	Live Outcome = "live"
	// Staged is a release on disk that is not in front of traffic: it has been
	// written, and either has not been switched yet or never passed its gate.
	Staged Outcome = "staged"
	// Good is a release that served and was displaced by a newer good one.
	Good Outcome = "good"
	// RolledBack is a release that served, and was switched away from by a
	// rollback.
	RolledBack Outcome = "rolled_back"
	// Failed is a release that never served: it was staged and switched, did
	// not pass its health gate, and was switched back away from.
	Failed Outcome = "failed"
)

// Release is one deploy. It is written once, at stage time, and corrected in
// place only in its outcome fields — which is why those are the only mutable
// part of it: an image digest that could change after the fact would make
// "the release that is serving" a sentence with no referent.
type Release struct {
	// ID is the directory name under releases/, and it is sortable, because
	// the history's order is the lexicographic order of the ids and the
	// timestamp prefix is what makes that true.
	ID string `json:"id"`
	// Service is the service this release belongs to. A box runs several,
	// and a release id that did not name one would make `logs --release` a
	// question rather than an answer.
	Service string `json:"service"`
	// Version is the tag the build carried. Cosmetic: the digest is the
	// identity, and this is what a person recognises.
	Version string `json:"version,omitempty"`
	// Image is the pinned OCI reference, name@digest. Never a tag alone —
	// see Build.Validate.
	Image string `json:"image"`
	// Host is the name this release answers on through the box's edge.
	Host string `json:"host"`
	// Secrets is the NAME of the secret set installed into this release at
	// deploy time. Never a value: see the package comment.
	Secrets string `json:"secrets,omitempty"`
	// StartedAt is when the release was staged.
	StartedAt time.Time `json:"started_at"`
	// EndedAt is when it stopped being current. Zero while it is live.
	EndedAt time.Time `json:"ended_at,omitempty"`
	// Outcome is what became of it.
	Outcome Outcome `json:"outcome"`
}

// History is the one file the stack owns. It is the reason a rollback needs
// nothing but local disk, and it is deliberately readable rather than
// protected: an operator on the box reads it with `cat`.
type History struct {
	Version int    `json:"version"`
	Current string `json:"current"`
	// LastGood is the release rollback flips back to when nothing names a
	// target. Empty means nothing has ever passed a health gate, and a
	// rollback then has nothing to go back to.
	LastGood string    `json:"last_good"`
	Releases []Release `json:"releases"`
	// Keep is how many releases the box retains, recorded so that a `prune`
	// run on a different machine does not disagree with the one that wrote
	// the history.
	Keep int `json:"keep,omitempty"`
}

// find is the lookup the switch performs. It walks the newest-first list and
// returns the first match, which is the same release the directories hold
// because ids are unique by construction.
func (h History) find(id string) (Release, bool) {
	for _, r := range h.Releases {
		if r.ID == id {
			return r, true
		}
	}
	return Release{}, false
}

// count reports how many releases of one service the history holds, for the
// prune: the keep count is per service, not per box, or one busy service
// would evict every release another one ever had.
func (h History) count(service string) int {
	n := 0
	for _, r := range h.Releases {
		if r.Service == service {
			n++
		}
	}
	return n
}

// Store is the stack rooted at one directory. It is a value, not a
// connection, because the whole point is that a rollback is a file operation
// and not a session: there is nothing to connect to and nothing to time out
// of.
type Store struct {
	Root string
	Keep int
}

// Open is the store over root, creating the two directories that always
// exist. It refuses an empty root — a stack rooted at the working directory
// is how a box ends up with `current` in someone's home folder — and refuses
// a root that is a file rather than a directory.
func Open(root string) (*Store, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("the stack wants a root directory, and an empty one is the working directory")
	}
	if fi, err := os.Stat(root); err == nil && !fi.IsDir() {
		return nil, fmt.Errorf("the stack root %s is a file, not a directory", root)
	}
	for _, dir := range []string{root, filepath.Join(root, releasesDir), filepath.Join(root, logsDir)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	keep := DefaultKeep
	if s, err := OpenStoreKeep(root); err == nil && s > 0 {
		keep = s
	}
	return &Store{Root: root, Keep: keep}, nil
}

// OpenStoreKeep reads the keep count out of an existing history, so a box
// that was set up with a different number keeps using it. A missing history
// is not an error: the default applies until somebody writes one.
func OpenStoreKeep(root string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(root, historyFile))
	if err != nil {
		return 0, err
	}
	var h History
	if err := json.Unmarshal(raw, &h); err != nil {
		return 0, err
	}
	return h.Keep, nil
}

// releaseDir is where one release lives.
func (s *Store) releaseDir(id string) string { return filepath.Join(s.Root, releasesDir, id) }

// ReleasePath is the record on disk for one release. Exported because
// `boxd`'s observe route reports where a release's record is, and a caller
// that has to rebuild the path is a caller that can build it wrong.
func (s *Store) ReleasePath(id string) string {
	return filepath.Join(s.releaseDir(id), "release.json")
}

// LogPath is one release's own log. Exported for `cafe box logs --release`,
// which is the verb that names this file so a person does not have to.
func (s *Store) LogPath(id string) string { return filepath.Join(s.Root, logsDir, id+".log") }

// CurrentPath is the pointer. Exported so the edge can be told what it is
// serving without opening the history.
func (s *Store) CurrentPath() string { return filepath.Join(s.Root, currentLink) }

// NewID is the release id: a UTC timestamp, then the first twelve hex of the
// image digest. The timestamp makes ids sort in deploy order, which is the
// only ordering the history has; the digest makes two releases of the same
// service in the same second distinguishable, and makes the id a fact about
// the artifact rather than a counter somebody has to keep.
func NewID(now time.Time, digest string) (string, error) {
	if !digestOK(digest) {
		return "", fmt.Errorf("release id wants a pinned digest, got %q", digest)
	}
	return now.UTC().Format("20060102T150405Z") + "-" + shortDigest(digest), nil
}

// shortDigest is the twelve hex characters after the algorithm. It is a label
// for a person, not a security boundary: the full digest is in the record.
func shortDigest(digest string) string {
	d := strings.TrimPrefix(digest, "sha256:")
	d = strings.TrimPrefix(d, ":")
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}

var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// digestOK is the pin check. It answers "is this an immutable artifact",
// which is the whole question a rollback depends on: `latest` is a name that
// means something different tomorrow, so a release built from it cannot be
// gone back to.
func digestOK(digest string) bool { return digestRE.MatchString(digest) }

// imageOK is the same check applied to the whole reference. `name@digest` and
// not `name:tag`, because the tag is the half of a reference that moves.
func imageOK(image string) bool {
	at := strings.Index(image, "@")
	if at < 0 {
		return false
	}
	return digestOK(image[at+1:])
}

// ErrExists is the refusal for a re-stage. It is a sentinel because a caller
// that got it should say "this release is already on the box" and stop, and
// not treat it as an I/O failure worth retrying.
var ErrExists = errors.New("release is already staged")

// Stage writes one release directory and records it in the history. The
// record's create is exclusive, so a second stage of the same id is a
// refusal that names the path rather than an overwrite: the record on disk
// is the one and only description of what that release is. The directory and
// the history are written in that order, so a crash between them leaves an
// unreferenced directory — which Prune's keep count reclaims — rather than
// a history entry pointing at nothing.
func (s *Store) Stage(rel Release) (Release, error) {
	if strings.TrimSpace(rel.Service) == "" {
		return Release{}, errors.New("a release wants a service name")
	}
	if !imageOK(rel.Image) {
		return Release{}, fmt.Errorf("release %s wants an image pinned as name@sha256:…, got %q", rel.ID, rel.Image)
	}
	if rel.Host == "" {
		return Release{}, errors.New("a release wants the host it answers on")
	}
	if rel.Outcome == "" {
		rel.Outcome = Staged
	}
	dir := s.releaseDir(rel.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Release{}, fmt.Errorf("create %s: %w", dir, err)
	}
	path := s.ReleasePath(rel.ID)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return Release{}, fmt.Errorf("%w: %s", ErrExists, path)
		}
		return Release{}, fmt.Errorf("write %s: %w", path, err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rel); err != nil {
		f.Close()
		return Release{}, fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return Release{}, fmt.Errorf("close %s: %w", path, err)
	}
	h, err := s.History()
	if err != nil {
		return Release{}, err
	}
	h.Releases = append(h.Releases, rel)
	sortNewestFirst(h.Releases)
	if err := s.save(h); err != nil {
		return Release{}, err
	}
	return rel, nil
}

// mark rewrites one release in the history, leaving the pointer alone. It is
// how a release that never served gets recorded as failed: the release has to
// be on the record, and must not be in front of traffic.
func (s *Store) mark(rel Release) error {
	h, err := s.History()
	if err != nil {
		return err
	}
	for i := range h.Releases {
		if h.Releases[i].ID == rel.ID {
			h.Releases[i] = rel
			return s.save(h)
		}
	}
	h.Releases = append(h.Releases, rel)
	sortNewestFirst(h.Releases)
	return s.save(h)
}

// ErrNoSuchRelease is the refusal for an id that is not in the history. It is
// separate from an I/O error because the remedy is different: the answer is
// "no such release on this box", not "try again".
var ErrNoSuchRelease = errors.New("no such release on this box")

// History reads the stack's one state file. A missing file is an empty
// history rather than an error — a box that has never deployed anything is a
// box with no history, not a broken one.
func (s *Store) History() (History, error) {
	raw, err := os.ReadFile(filepath.Join(s.Root, historyFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return History{Version: historyVersion, Keep: s.keep()}, nil
		}
		return History{}, fmt.Errorf("read the release history: %w", err)
	}
	var h History
	if err := json.Unmarshal(raw, &h); err != nil {
		return History{}, fmt.Errorf("read the release history: %w", err)
	}
	if h.Version == 0 {
		h.Version = historyVersion
	}
	if h.Version != historyVersion {
		return History{}, fmt.Errorf("the release history at %s is version %d and this cafe reads version %d",
			filepath.Join(s.Root, historyFile), h.Version, historyVersion)
	}
	if h.Keep == 0 {
		h.Keep = s.keep()
	}
	sortNewestFirst(h.Releases)
	return h, nil
}

func (s *Store) keep() int {
	if s.Keep > 0 {
		return s.Keep
	}
	return DefaultKeep
}

// sortNewestFirst is the one ordering the history has. It is derived from
// sortable ids rather than from timestamps carried beside them, so a clock
// that jumped backwards cannot reorder the releases.
func sortNewestFirst(rels []Release) {
	sort.Slice(rels, func(i, j int) bool { return rels[i].ID > rels[j].ID })
}

// save writes the history the way the release record is written — to a
// temporary name and a rename — so a crash mid-write leaves the previous
// history intact rather than a truncated file that reads as "no releases
// ever deployed".
func (s *Store) save(h History) error {
	h.Version = historyVersion
	if h.Keep == 0 {
		h.Keep = s.keep()
	}
	path := filepath.Join(s.Root, historyFile)
	tmp, err := os.CreateTemp(s.Root, ".history-*.json")
	if err != nil {
		return fmt.Errorf("write the release history: %w", err)
	}
	defer os.Remove(tmp.Name())
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(h); err != nil {
		tmp.Close()
		return fmt.Errorf("write the release history: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write the release history: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return fmt.Errorf("write the release history: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write the release history: %w", err)
	}
	return nil
}

// Current is the release traffic is going to. A stack that has never
// deployed has no current release and an empty answer, not an error: the
// question "what is serving" is well-formed before anything has been
// deployed, and answering it with a failure would make the first deploy's
// own read of the previous state a refusal.
func (s *Store) Current() (Release, error) {
	h, err := s.History()
	if err != nil {
		return Release{}, err
	}
	if h.Current == "" {
		return Release{}, nil
	}
	rel, ok := h.find(h.Current)
	if !ok {
		return Release{}, fmt.Errorf("%w: the pointer names %s and the history does not hold it", ErrNoSuchRelease, h.Current)
	}
	return rel, nil
}

// Release is one release by id, read from the history.
func (s *Store) Release(id string) (Release, error) {
	h, err := s.History()
	if err != nil {
		return Release{}, err
	}
	rel, ok := h.find(id)
	if !ok {
		return Release{}, fmt.Errorf("%w: %s", ErrNoSuchRelease, id)
	}
	return rel, nil
}

// Releases is the history, newest first, for `cafe box status` and for the
// operator reading the file directly.
func (s *Store) Releases() ([]Release, error) {
	h, err := s.History()
	if err != nil {
		return nil, err
	}
	return h.Releases, nil
}

// Switch flips the pointer. The link is written under a temporary name and
// renamed over `current`, and that rename is the atomic step: at no instant
// does the pointer name a directory that does not exist, which is what makes
// a deploy have no window in which requests hit nothing.
//
// An empty id removes the pointer, which is a real state and not a failure:
// a first deploy that failed its health gate leaves the box serving nothing
// rather than serving the thing that just failed.
func (s *Store) Switch(id string) (Release, error) {
	h, err := s.History()
	if err != nil {
		return Release{}, err
	}
	link := s.CurrentPath()
	if id == "" {
		if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Release{}, fmt.Errorf("clear the current pointer: %w", err)
		}
		h.Current = ""
		return Release{}, s.save(h)
	}
	rel, ok := h.find(id)
	if !ok {
		return Release{}, fmt.Errorf("%w: %s", ErrNoSuchRelease, id)
	}
	if err := switchLink(link, filepath.Join(releasesDir, id)); err != nil {
		return Release{}, err
	}
	now := time.Now().UTC()
	for i := range h.Releases {
		switch h.Releases[i].ID {
		case id:
			h.Releases[i].Outcome = Live
			// A release that is serving again has not ended, whatever it was
			// marked when it was switched away from.
			h.Releases[i].EndedAt = time.Time{}
		default:
			if h.Releases[i].Outcome == Live {
				h.Releases[i].Outcome = Good
				h.Releases[i].EndedAt = now
			}
		}
	}
	h.Current = id
	if err := s.save(h); err != nil {
		return Release{}, err
	}
	return rel, nil
}

// switchLink is the atomic pointer write. The temporary name is in the same
// directory as the pointer so the rename is within one filesystem — a
// cross-device rename is not atomic and, on Linux, not permitted at all.
func switchLink(link, target string) error {
	dir := filepath.Dir(link)
	tmp, err := os.CreateTemp(dir, ".current-*")
	if err != nil {
		return fmt.Errorf("switch the current pointer: %w", err)
	}
	tmpName := tmp.Name()
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("switch the current pointer: %w", err)
	}
	if err := os.Remove(tmpName); err != nil {
		return fmt.Errorf("switch the current pointer: %w", err)
	}
	if err := os.Symlink(target, tmpName); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("switch the current pointer: %w", err)
	}
	if err := os.Rename(tmpName, link); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("switch the current pointer to %s: %w", target, err)
	}
	return nil
}

// SwitchTarget is what the pointer says, read through the symlink rather
// than the history. It exists so a test can prove the two agree: a history
// that names one release while the symlink names another is the exact
// disagreement that would serve traffic nobody asked for.
func (s *Store) SwitchTarget() (string, error) {
	dst, err := os.Readlink(s.CurrentPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read the current pointer: %w", err)
	}
	return filepath.Base(dst), nil
}

// Prune drops releases of one service beyond the keep count, newest-first,
// and never the current one or the last-good one. The two exclusions are the
// whole rule: pruning the release rollback goes back to would turn a
// rollback into a deploy, and pruning the live one would unpoint a serving
// release out from under the edge.
func (s *Store) Prune() ([]string, error) {
	h, err := s.History()
	if err != nil {
		return nil, err
	}
	keep := h.Keep
	if keep <= 0 {
		keep = s.keep()
	}
	seen := map[string]int{}
	var removed []string
	for _, rel := range h.Releases {
		if rel.ID == h.Current || rel.ID == h.LastGood {
			continue
		}
		seen[rel.Service]++
		if seen[rel.Service] <= keep {
			continue
		}
		if err := os.RemoveAll(s.releaseDir(rel.ID)); err != nil {
			return removed, fmt.Errorf("prune release %s: %w", rel.ID, err)
		}
		removed = append(removed, rel.ID)
	}
	if len(removed) == 0 {
		return nil, nil
	}
	kept := make([]Release, 0, len(h.Releases))
	for _, rel := range h.Releases {
		gone := false
		for _, id := range removed {
			if rel.ID == id {
				gone = true
				break
			}
		}
		if !gone {
			kept = append(kept, rel)
		}
	}
	h.Releases = kept
	return removed, s.save(h)
}
