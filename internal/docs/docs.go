package docs

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cafaye/cafe/internal/contract"
)

// The two ingest kinds. They are press's vocabulary, and they are INFERRED
// from the repository's own `cafaye.yml` (`repository.visibility`) — the
// same field the docs sync's anti-leak guard reads — rather than typed:
// a public repository's pages are public docs, a private one's are private
// notes. There is deliberately no `cafe notes publish`: one command that
// knows where it is, because two commands is two helps to keep in sync.
const (
	KindDocs  = "docs"
	KindNotes = "notes"
)

// The two visibilities the schema admits. They are the only values
// `--visibility` accepts, and the only values the inference trusts: an
// empty or unknown visibility is a refusal rather than a guess, because a
// guess in the public direction is a leak and a guess in the private
// direction is a page that never ships.
const (
	VisibilityPublic  = "public"
	VisibilityPrivate = "private"
)

// DefaultPressURL is press's origin. It is a default rather than an
// environment read for the reason pantry's clients refuse a default: a URL
// baked into a binary goes stale when the service moves. So the default is
// the fleet's address and PressURLEnv overrides it — the same env-override
// rule resolve holds.
const DefaultPressURL = "https://press.cafaye.com"

// PressURLEnv is where press's origin comes from when no flag names one.
const PressURLEnv = "CAF_PRESS_URL"

// InferKind answers which ingest kind a repository is: docs for a public
// repository, notes for a private one. The manifest is already validated by
// the contract linter's own gate before this is asked, so a visibility that
// is empty or outside the schema's enum is a caller error — a hand-built
// manifest or a field the schema stopped owning — and it is refused loudly
// rather than guessed, because a guess is the anti-leak guard failing open.
func InferKind(manifest contract.Manifest) (string, error) {
	switch strings.TrimSpace(manifest.RepositoryVisibility()) {
	case VisibilityPublic:
		return KindDocs, nil
	case VisibilityPrivate:
		return KindNotes, nil
	default:
		return "", fmt.Errorf("repository.visibility is %q: cafe docs publish infers the ingest kind from it "+
			"(public ships as docs, private as notes) and will not guess — declare `repository.visibility: public` "+
			"or `private` in cafaye.yml, or pass --visibility with what this repository is",
			manifest.RepositoryVisibility())
	}
}

// ParseVisibility validates a `--visibility` override. An empty string is
// not an error here — it means "no override, infer" — because the flag and
// the inference are two spellings of one decision and the empty flag is the
// spelling that defers. Anything else must be one of the schema's two
// values, verbatim: a third spelling would be a kind press never serves.
func ParseVisibility(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	switch trimmed {
	case VisibilityPublic, VisibilityPrivate:
		return trimmed, nil
	default:
		return "", fmt.Errorf("visibility %q is not public or private: the override names what the repository is, "+
			"and press serves no third kind", value)
	}
}

// KindForVisibility is the override applied: an explicit visibility wins,
// an empty one defers to the inference. It is a function rather than an
// inline conditional so the precedence — flag over file — is stated once,
// where a test pins it, rather than once per caller that could drift.
func KindForVisibility(override string, manifest contract.Manifest) (string, error) {
	if strings.TrimSpace(override) != "" {
		visibility, err := ParseVisibility(override)
		if err != nil {
			return "", err
		}
		if visibility == VisibilityPublic {
			return KindDocs, nil
		}
		return KindNotes, nil
	}
	return InferKind(manifest)
}

// Plan is what the command will send, built and printed BEFORE anything is.
// The plan is the publish made reviewable: an agent (or a human with
// --dry-run) reads the document first, and the ingest follows by default —
// the same split the pantry publisher holds between its proposal and its
// three writes.
type Plan struct {
	// Project is the manifest's service name: the name press files this
	// ingest under.
	Project string
	// Version is the content version, verbatim — the flag or the derived
	// git tag.
	Version string
	// Kind is the inferred (or overridden) ingest kind.
	Kind string
	// Files are the page files in the tarball, relative to the project
	// directory, sorted. They are in the plan so a dry run names exactly
	// what would be sent.
	Files []string
	// SHA256 is the tarball's digest, and Size its bytes. They are the
	// receipt's fields computed before the send, so a dry run can say
	// what press would answer about the bytes without sending them.
	SHA256 string
	Size   int64
}

// BuildTar packs the repository's page files into a tarball and returns the
// bytes with the plan describing them. The content is built from the
// repository's own tree, and it holds markdown and frontmatter only — no
// build output:
//
//   - kind docs (a public repository): the pages under src/content/docs/.
//   - kind notes (a private repository): the page files at the root plus
//     projects/.
//
// The selection is an allowlist, not a denylist: dist/, node_modules/ and
// everything else are excluded by never being named, because a denylist is
// a list of the build outputs somebody remembered and the next framework's
// output directory is the one nobody did. Only .md and .mdx files are
// packed — a page is markdown with frontmatter, and anything else in those
// directories is an asset the ingest API does not take.
//
// The tarball is deterministic: the file list is sorted and no header
// carries a clock, a uid or a mode beyond 0644. Two builds of one tree
// produce the same bytes, so the sha256 in the plan is a fact about the
// tree rather than about the moment.
func BuildTar(project, kind string) ([]byte, Plan, error) {
	files, err := pageFiles(project, kind)
	if err != nil {
		return nil, Plan{}, err
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(project, rel))
		if err != nil {
			return nil, Plan{}, fmt.Errorf("read page %s: %w", rel, err)
		}
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     filepath.ToSlash(rel),
			Size:     int64(len(data)),
			Mode:     0o644,
		}); err != nil {
			return nil, Plan{}, fmt.Errorf("pack page %s: %w", rel, err)
		}
		if _, err := tw.Write(data); err != nil {
			return nil, Plan{}, fmt.Errorf("pack page %s: %w", rel, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, Plan{}, fmt.Errorf("finish the content tarball: %w", err)
	}
	sum := sha256.Sum256(buf.Bytes())
	plan := Plan{
		Files:  files,
		SHA256: hex.EncodeToString(sum[:]),
		Size:   int64(buf.Len()),
		Kind:   kind,
	}
	return buf.Bytes(), plan, nil
}

// pageFiles lists the page files for a kind, relative to the project
// directory and sorted. The roots differ by kind — the docs tree is a site
// source, the notes tree is a flat collection — and the extension rule is
// shared: markdown pages only, because the tarball contains markdown and
// frontmatter and nothing else.
func pageFiles(project, kind string) ([]string, error) {
	type root struct {
		dir       string
		recursive bool
	}
	var roots []root
	switch kind {
	case KindDocs:
		// The pages live under src/content/docs/: the site source, whose
		// build output (dist/) is a sibling the walk never enters.
		roots = []root{{dir: filepath.Join("src", "content", "docs"), recursive: true}}
	case KindNotes:
		// The pages live at the root plus projects/: a flat collection
		// with one subdirectory, not a site source. The root itself is
		// flat — a page file at the root ships, a subdirectory's (other
		// than projects/) does not — while projects/ is walked whole.
		roots = []root{{dir: ".", recursive: false}, {dir: "projects", recursive: true}}
	default:
		return nil, fmt.Errorf("ingest kind %q is not docs or notes: the kind is inferred from repository.visibility, not typed", kind)
	}
	var files []string
	for _, root := range roots {
		abs := filepath.Join(project, root.dir)
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read the %s tree: %w", root.dir, err)
		}
		if !info.IsDir() {
			continue
		}
		err = fs.WalkDir(os.DirFS(abs), ".", func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				// A flat root ships its own files and no subdirectory's:
				// anything below the top is skipped whole. A recursive
				// root ships everything under it.
				if !root.recursive && path != "." {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			if !isPage(path) {
				return nil
			}
			rel := filepath.Join(root.dir, path)
			if root.dir == "." {
				rel = path
			}
			files = append(files, rel)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("read the %s tree: %w", root.dir, err)
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		names := make([]string, 0, len(roots))
		for _, root := range roots {
			names = append(names, root.dir)
		}
		return nil, fmt.Errorf("no pages to ship: kind %s reads %s, and the tree holds no markdown there — "+
			"a publish with no pages is a version with nothing in it", kind, strings.Join(names, " and "))
	}
	return files, nil
}

// isPage answers whether a file is a page: markdown with frontmatter, by
// extension. The tarball contains markdown and frontmatter only, so the
// extension is the whole rule — and it is one function so the tar builder
// and its tests cannot disagree about what a page is.
func isPage(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".md" || ext == ".mdx"
}

// ListTar reads the entry names out of a tarball, in order. It is the
// assertion half of the builder: a test packs a tree and reads back exactly
// the page files, which is what makes "the tarball holds exactly the page
// files and nothing else" a fact about the bytes rather than about the walk.
func ListTar(tarball []byte) ([]string, error) {
	var names []string
	tr := tar.NewReader(bytes.NewReader(tarball))
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return names, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read the content tarball: %w", err)
		}
		names = append(names, header.Name)
	}
}
