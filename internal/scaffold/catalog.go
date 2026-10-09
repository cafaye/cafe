package scaffold

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// templatesFS is the catalog, embedded. `all:` rather than a bare directory
// name because a template may hold a dotfile — .gitignore is the obvious one —
// and the embed rules exclude those silently without it. A catalog that loses
// a file between the author's tree and the binary is the failure the exclusion
// exists to prevent.
//
//go:embed all:templates
var templatesFS embed.FS

// templatesRoot is where the catalog lives inside the binary.
const templatesRoot = "templates"

// indexFile is the catalog's index: one entry per template, in catalog order.
// It is a file rather than Go so that adding a template is adding a directory
// and a row, not a recompile of a table written beside it.
const indexFile = "index.json"

// Template is one entry in the catalog: the name a person types after
// -template and the one line `caf new --help` lists.
type Template struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

// Catalog is every template this binary carries, in the order the index
// declares. It is read once per command run and passed down; nothing in this
// package holds a package-level copy, per the no-globals rule.
type Catalog struct {
	templates []Template
}

// LoadCatalog reads the embedded index. An index that cannot be read is a
// build error rather than a runtime state: the embed directive puts the file
// in the binary, so the only way this fails is a malformed index, and that is
// a defect in the tree rather than a condition to degrade through.
func LoadCatalog() (Catalog, error) {
	data, err := templatesFS.ReadFile(path.Join(templatesRoot, indexFile))
	if err != nil {
		return Catalog{}, fmt.Errorf("read the template catalog: %w", err)
	}
	var index struct {
		Templates []Template `json:"templates"`
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return Catalog{}, fmt.Errorf("the template catalog is not JSON: %w", err)
	}
	return Catalog{templates: index.Templates}, nil
}

// Names are the template names, in catalog order. They are what an unknown
// name is answered with and what `caf new -template` offers.
func (c Catalog) Names() []string {
	names := make([]string, 0, len(c.templates))
	for _, t := range c.templates {
		names = append(names, t.Name)
	}
	return names
}

// Resolve turns the word a person typed into a Template. An unknown name is
// answered with the ones that exist, because "unknown template" alone leaves
// the reader guessing what they could have typed.
func (c Catalog) Resolve(name string) (Template, error) {
	for _, t := range c.templates {
		if t.Name == name {
			return t, nil
		}
	}
	return Template{}, fmt.Errorf("unknown template %q (available: %s)", name, strings.Join(c.Names(), ", "))
}

// Files is every path the template writes, relative to the project root, in
// lexical order. The walk is over the template's directory in the embedded
// catalog, so the tree is the single source of truth for what a template
// contains: an index that listed the files would be a second copy that could
// disagree with the tree, and the failure mode is a catalog entry that names a
// file nobody wrote.
//
// The .tmpl suffix is a template's business and never reaches the project: it
// is stripped on the way out, so docker/Dockerfile.tmpl in the catalog is
// docker/Dockerfile in the scaffolded app.
func (t Template) Files() ([]string, error) {
	root := path.Join(templatesRoot, t.Name)
	var files []string
	err := fs.WalkDir(templatesFS, root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
		files = append(files, strings.TrimSuffix(relative, ".tmpl"))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk template %q: %w", t.Name, err)
	}
	slices.Sort(files)
	return files, nil
}

// pathOf is the template's file for one project-relative path, as the embedded
// catalog spells it. Paths inside the catalog are slash-separated — the
// embedded filesystem is read with the io/fs interface, which is — so the
// conversion happens once, where the file is written, and never here.
func (t Template) pathOf(relative string) string {
	return path.Join(templatesRoot, t.Name, relative+".tmpl")
}
