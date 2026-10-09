package scaffold

import (
	"fmt"
	"path/filepath"
)

// Options is what both entry points take. The defaults are the empty strings:
// a template of "" means the catalog's default, a name of "" means derive it
// from the target directory, and a caller that wants the defaults passes
// Options{Dir: "."}.
type Options struct {
	// Template is the catalog template name. "app" is the default and the
	// only one until W6-P2 composes the platform app.
	Template string
	// Name is the cafaye namespace name. Empty means derive it from the base
	// of Dir, which is right for `caf init` inside an existing repository and
	// for `caf new <name>` where the directory IS the name.
	Name string
	// Dir is the directory the project is written into.
	Dir string
	// Force overwrites files that already exist. Without it a second scaffold
	// is a refusal, because a half-written project is a project nobody can
	// reason about.
	Force bool
}

// DefaultTemplate is the template `caf new` and `caf init` use when none is
// named.
const DefaultTemplate = "app"

// Output is what a scaffold produced: the template it came from and the files
// it would write, in the template's own order. It is what the command prints
// — every file, in full — because a scaffold nobody can inspect is a scaffold
// nobody can debug, and the precedent is `caf dev` and `caf gen` printing
// what they generated.
type Output struct {
	Template string
	Files    []File
}

// New scaffolds a whole project into opts.Dir from a catalog template.
//
// The name is validated first, because everything downstream — the directory,
// the manifest, the repository URL, the owner team — is derived from it, and a
// refusal after rendering would be a refusal about a project that exists only
// in memory. The manifest is then validated against the vendored contract
// before anything is written, so `caf new` cannot produce a project that
// `caf contract lint` would reject.
func New(opts Options) (Output, error) {
	catalog, err := LoadCatalog()
	if err != nil {
		return Output{}, err
	}
	template, err := resolveTemplate(catalog, opts.Template)
	if err != nil {
		return Output{}, err
	}

	name := opts.Name
	if name == "" {
		name = filepath.Base(absPath(opts.Dir))
	}
	if err := ValidateName(name); err != nil {
		return Output{}, err
	}

	files, err := render(template, dataFor(name))
	if err != nil {
		return Output{}, err
	}
	if err := validateManifest(template.Name, files); err != nil {
		return Output{}, err
	}
	if err := Write(opts.Dir, files, opts.Force); err != nil {
		return Output{}, err
	}
	return Output{Template: template.Name, Files: files}, nil
}

// Init writes only the manifest into opts.Dir: `caf init` is for a project
// that already exists, so the rest of the app template would be files laid
// over somebody's working tree.
//
// The name is derived from the directory unless -name overrides it, which is
// the case for a repository whose directory is not its service name — a
// worktree called caf-w6p1 holding the service caf would derive a name that
// is both illegal and reserved.
func Init(opts Options) (Output, error) {
	catalog, err := LoadCatalog()
	if err != nil {
		return Output{}, err
	}
	template, err := resolveTemplate(catalog, opts.Template)
	if err != nil {
		return Output{}, err
	}

	name := opts.Name
	if name == "" {
		name = filepath.Base(absPath(opts.Dir))
	}
	if err := ValidateName(name); err != nil {
		return Output{}, err
	}

	files, err := render(template, dataFor(name))
	if err != nil {
		return Output{}, err
	}
	if err := validateManifest(template.Name, files); err != nil {
		return Output{}, err
	}
	manifest, found := manifestOf(files)
	if !found {
		return Output{}, fmt.Errorf("template %s has no %s: every template scaffolds a manifest", template.Name, manifestPath)
	}
	if err := Write(opts.Dir, []File{manifest}, opts.Force); err != nil {
		return Output{}, err
	}
	return Output{Template: template.Name, Files: []File{manifest}}, nil
}

// resolveTemplate applies the default and resolves the name. The default is
// here rather than in a flag's default value so a caller that passes an empty
// template gets the same answer a flag the user never touched does.
func resolveTemplate(catalog Catalog, name string) (Template, error) {
	if name == "" {
		name = DefaultTemplate
	}
	return catalog.Resolve(name)
}

// absPath resolves a directory to an absolute one for the name derivation.
// A relative "." must derive the working directory's name rather than ".".
func absPath(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		// filepath.Abs fails only when the working directory cannot be
		// determined, in which case the base name of what was given is the
		// best answer available and the write will report the real failure.
		return filepath.Clean(dir)
	}
	return abs
}
