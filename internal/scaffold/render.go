package scaffold

import (
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"github.com/cafaye/cafe/internal/contract"
)

// serviceNamePattern mirrors core's serviceName $defs
// (schemas/manifest-0.2.json): lowercase kebab-case, starting with a letter,
// no trailing separator.
//
// It is a mirror and not the rule. The rule is the schema, and the gate below
// validates every rendered manifest against the vendored copy of it — so a
// core release that tightens the pattern turns the rendered manifest red
// rather than letting a bad name through a stale Go regexp. The mirror exists
// so the refusal can name what is wrong with a name before a whole manifest is
// rendered, and the agreement between the two is pinned by a test that renders
// a table of names through both.
var serviceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// serviceNameMin and serviceNameMax are the schema's own bounds on the same
// $defs.
const (
	serviceNameMin = 2
	serviceNameMax = 40
)

// reservedNames are the four names core's serviceName $defs reserves. They
// pass the pattern — "cafe" and "core" are legal kebab-case — so only a check
// here keeps a scaffolded app from taking the CLI's own namespace.
var reservedNames = []string{"cafaye", "cafe", "kit", "core"}

// coreConstraint is the core contract version a scaffolded app is written
// against. It tracks the schema this package validates against (manifest-0.2),
// and the agreement is pinned by a test: a render against a schema that moved
// without this constant is a red suite.
const coreConstraint = "^0.2.0"

// manifestPath is the one file every template writes and the one file every
// project is identified by. `cafe contract lint` looks for exactly this name at
// the root of a repository, so the catalog cannot offer a project without it.
const manifestPath = "cafaye.yml"

// defaultRepositoryOrg is the organization a scaffolded app's repository URL
// is guessed under. It is a starting point the manifest's own comment says to
// edit, not a fact: cafe cannot know where a stranger's app will live.
const defaultRepositoryOrg = "cafaye"

// templateData is what a template may substitute. It is small on purpose: a
// template that read the whole manifest would be a second place the manifest's
// fields are written down. Everything here is derived from the name except the
// core constraint, which is the contract version.
type templateData struct {
	Name           string
	OwnerTeam      string
	RepositoryURL  string
	CoreConstraint string
}

// dataFor builds the substitution data for a name. The owner team defaults to
// the name because a scaffolded app has exactly one accountable party — its
// author — and inventing a team name would be a worse guess. Both are edited
// in the rendered manifest, which says so where they appear.
func dataFor(name string) templateData {
	return templateData{
		Name:           name,
		OwnerTeam:      name,
		RepositoryURL:  fmt.Sprintf("git@github.com:%s/%s.git", defaultRepositoryOrg, name),
		CoreConstraint: coreConstraint,
	}
}

// ValidateName reports whether a name is a cafaye namespace name: the schema's
// pattern, its length bounds, and the four reserved names. The error names
// every rule, because a refusal that says only "invalid name" sends the reader
// to the schema to find out what valid means.
func ValidateName(name string) error {
	switch {
	case len(name) < serviceNameMin || len(name) > serviceNameMax:
		return fmt.Errorf("%q is not a cafaye namespace name: it must be %d-%d characters", name, serviceNameMin, serviceNameMax)
	case !serviceNamePattern.MatchString(name):
		return fmt.Errorf("%q is not a cafaye namespace name: lowercase kebab-case, starting with a letter, no trailing separator", name)
	}
	for _, reserved := range reservedNames {
		if name == reserved {
			return fmt.Errorf("%q is reserved by the platform and cannot be an app's name (reserved: %s)", name, strings.Join(reservedNames, ", "))
		}
	}
	return nil
}

// File is one file a scaffold writes: its path relative to the project root
// and its bytes. It exists so rendering can be tested without a filesystem —
// a render is a pure function from a template and a name to bytes.
type File struct {
	Path    string
	Content []byte
}

// render turns a template and a name into the project's files, in the
// template's own lexical order. It writes nothing.
func render(t Template, data templateData) ([]File, error) {
	paths, err := t.Files()
	if err != nil {
		return nil, err
	}
	files := make([]File, 0, len(paths))
	for _, relative := range paths {
		source, err := templatesFS.ReadFile(t.pathOf(relative))
		if err != nil {
			return nil, fmt.Errorf("read template file %s: %w", relative, err)
		}
		rendered, err := executeTemplate(t.Name, relative, source, data)
		if err != nil {
			return nil, err
		}
		files = append(files, File{Path: relative, Content: rendered})
	}
	return files, nil
}

// executeTemplate renders one file. Missing keys are an error rather than an
// empty string: a template that grew a field nobody supplies must fail at
// render time, because the alternative is a scaffolded app with a hole in its
// Dockerfile that nobody notices until the build.
func executeTemplate(templateName, relative string, source []byte, data templateData) ([]byte, error) {
	tmpl, err := template.New(templateName).Option("missingkey=error").Parse(string(source))
	if err != nil {
		return nil, fmt.Errorf("template %s's %s does not parse: %w", templateName, relative, err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		return nil, fmt.Errorf("template %s's %s did not render: %w", templateName, relative, err)
	}
	return []byte(out.String()), nil
}

// validateManifest is the gate: the rendered manifest goes through
// contract.CheckData — core's schema and the cross-field conventions, the same
// code `cafe contract lint` runs — before anything is written. A template that
// renders an invalid manifest is a defect in this repository, not in the
// scaffolded app, and the error says so.
func validateManifest(templateName string, files []File) error {
	for _, file := range files {
		if file.Path != manifestPath {
			continue
		}
		_, finding := contract.CheckData(manifestPath, file.Content)
		if finding.OK() {
			return nil
		}
		first, _ := finding.First()
		return fmt.Errorf("template %s renders a %s that fails the contract: %s", templateName, manifestPath, first)
	}
	return fmt.Errorf("template %s has no %s: every template scaffolds a manifest", templateName, manifestPath)
}

// manifestOf is the manifest file out of a rendered set. Callers that write
// only the manifest (cafe init) need it, and a second walk of the files is
// cheaper than a second field on File.
func manifestOf(files []File) (File, bool) {
	for _, file := range files {
		if file.Path == manifestPath {
			return file, true
		}
	}
	return File{}, false
}
