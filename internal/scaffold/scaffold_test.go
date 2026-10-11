package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cafaye/cafe/internal/contract"
	"github.com/cafaye/cafe/internal/dev"
)

// The catalog resolves every entry it indexes. An index that names a template
// with no directory is a catalog entry that cannot be scaffolded, and the
// failure would otherwise meet a person as "unknown template" for a template
// `cafe new --help` advertised.
func TestTheCatalogResolvesEveryIndexedTemplate(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if len(catalog.Names()) == 0 {
		t.Fatal("the catalog is empty: cafe new has nothing to offer")
	}
	for _, name := range catalog.Names() {
		t.Run(name, func(t *testing.T) {
			template, err := catalog.Resolve(name)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", name, err)
			}
			files, err := template.Files()
			if err != nil {
				t.Fatalf("Files: %v", err)
			}
			if len(files) == 0 {
				t.Fatalf("template %q writes no files", name)
			}
			// Every template scaffolds a manifest: it is the one file a
			// project is identified by, and `cafe contract lint` refuses a
			// tree without one.
			if !hasFile(files, manifestPath) {
				t.Errorf("template %q does not write a %s", name, manifestPath)
			}
		})
	}
}

// An unknown template is answered with the ones that exist. "Unknown
// template" alone leaves the reader guessing what they could have typed.
func TestAnUnknownTemplateNamesWhatIsAvailable(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}

	_, err = catalog.Resolve("nope")

	if err == nil {
		t.Fatal("Resolve(\"nope\") succeeded; the catalog cannot resolve what it does not have")
	}
	for _, name := range catalog.Names() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the error is %q, want it to name %q", err, name)
		}
	}
}

// The generated project validates against core's manifest schema AND passes
// cafe contract lint — the packet's whole claim, tested rather than asserted.
//
// Two levels, because they are two different claims. CheckData validates the
// manifest bytes against the vendored schema and the cross-field conventions;
// contract.Lint walks the written tree the way the command does, which also
// resolves the OpenAPI document when one is declared. A template that passed
// the first and failed the second would be a scaffolded project that fails on
// the developer's machine, which is exactly the divergence a CLI cannot have.
func TestEveryTemplateScaffoldsAProjectThatLints(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	for _, template := range catalog.templates {
		t.Run(template.Name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "acme")

			out, err := New(Options{Template: template.Name, Name: "acme", Dir: dir})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			for _, file := range out.Files {
				if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(file.Path))); err != nil {
					t.Fatalf("%s was not written: %v", file.Path, err)
				}
			}

			report, err := contract.Lint(dir)
			if err != nil {
				t.Fatalf("contract.Lint: %v", err)
			}
			if !report.OK() {
				t.Errorf("the scaffolded project does not lint:\n%s", report)
			}
		})
	}
}

// The scaffolded manifest carries no credential, and the reason is structural
// rather than aspirational: core's schema is `additionalProperties: false`, so
// a field it has not declared — a token, a password, a key — makes the
// manifest invalid, and the generator validates before it writes. This case
// scans the rendered manifest for a credential-shaped KEY, so the property is
// watched rather than assumed: prose about secrets is fine, a secret-shaped
// key is not.
func TestTheScaffoldedManifestDeclaresNoCredentialShapedKey(t *testing.T) {
	manifest, err := renderedManifest(t, "acme", "app")
	if err != nil {
		t.Fatalf("renderedManifest: %v", err)
	}

	_, finding := contract.CheckData(manifestPath, manifest)
	if !finding.OK() {
		t.Fatalf("the scaffolded manifest does not validate:\n%s", finding)
	}
	lowered := strings.ToLower(string(manifest))
	for _, key := range []string{"password:", "token:", "secret:", "api_key:", "apikey:", "credential:"} {
		if strings.Contains(lowered, key) {
			t.Errorf("the scaffolded manifest declares a credential-shaped key (%q); a secret belongs in the deployment's store, referenced by name", key)
		}
	}
}

// Rendering is deterministic: two runs over one template and one name produce
// the same bytes. It is what makes a diff between two scaffolds a diff between
// two inputs rather than a diff between two runs — the same property
// `cafe dev`'s compose document holds.
func TestRenderingIsDeterministic(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	template, err := catalog.Resolve("app")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	first, err := render(template, dataFor("acme"))
	if err != nil {
		t.Fatalf("first render: %v", err)
	}
	second, err := render(template, dataFor("acme"))
	if err != nil {
		t.Fatalf("second render: %v", err)
	}

	if len(first) != len(second) {
		t.Fatalf("two renders produced %d and %d files", len(first), len(second))
	}
	for i := range first {
		if first[i].Path != second[i].Path || string(first[i].Content) != string(second[i].Content) {
			t.Errorf("%s differs between two renders of one input", first[i].Path)
		}
	}
}

// A second scaffold does not clobber the first. The refusal names what is in
// the way and writes nothing, and -force is the deliberate override — because
// the file in the way may be a manifest somebody wrote by hand under the same
// name.
func TestAScaffoldRefusesToClobberAnExistingProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acme")
	if _, err := New(Options{Template: "app", Name: "acme", Dir: dir}); err != nil {
		t.Fatalf("first New: %v", err)
	}
	edited := []byte("# hand-edited by somebody who is not cafe\n")
	if err := os.WriteFile(filepath.Join(dir, manifestPath), edited, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := New(Options{Template: "app", Name: "acme", Dir: dir})

	if err == nil {
		t.Fatal("a second New over an existing project succeeded")
	}
	if !strings.Contains(err.Error(), manifestPath) {
		t.Errorf("the refusal does not name the file in the way: %v", err)
	}
	after, readErr := os.ReadFile(filepath.Join(dir, manifestPath))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(edited) {
		t.Error("a refused scaffold still wrote: the edit is gone")
	}

	// With -force the overwrite is deliberate, and it is the only way one
	// happens.
	if _, err := New(Options{Template: "app", Name: "acme", Dir: dir, Force: true}); err != nil {
		t.Fatalf("New with -force: %v", err)
	}
	forced, err := os.ReadFile(filepath.Join(dir, manifestPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(forced) == string(edited) {
		t.Error("-force left the original bytes: the overwrite did not happen")
	}
}

// cafe init writes the manifest and only the manifest: the project already
// exists, and laying a main.go over somebody's working tree is the clobber
// this command exists to avoid.
func TestInitWritesOnlyTheManifest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acme")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A file the project's author wrote, which init must leave alone.
	existing := []byte("package main\n")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), existing, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := Init(Options{Name: "acme", Dir: dir})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(out.Files) != 1 || out.Files[0].Path != manifestPath {
		t.Fatalf("Init wrote %v, want exactly the manifest", pathsOf(out.Files))
	}
	after, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(existing) {
		t.Error("init touched a file it does not own")
	}

	report, err := contract.Lint(dir)
	if err != nil {
		t.Fatalf("contract.Lint: %v", err)
	}
	if !report.OK() {
		t.Errorf("the initialized project does not lint:\n%s", report)
	}
}

// The name is derived from the directory when none is given, which is what
// makes `cafe init` usable without flags — and a directory whose base name is
// not a service name is refused by name, with the rules stated.
func TestInitDerivesTheNameFromTheDirectory(t *testing.T) {
	t.Run("a directory that is a service name", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "acme")

		out, err := Init(Options{Dir: dir})
		if err != nil {
			t.Fatalf("Init: %v", err)
		}
		if !strings.Contains(string(out.Files[0].Content), "name: acme") {
			t.Errorf("the manifest does not carry the directory's name:\n%s", out.Files[0].Content)
		}
	})

	t.Run("a directory that is not a service name", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "001")

		if _, err := Init(Options{Dir: dir}); err == nil {
			t.Fatal("Init derived a service name from a directory that cannot hold one")
		} else if !strings.Contains(err.Error(), "cafaye namespace name") {
			t.Errorf("the refusal does not name the rule: %v", err)
		}
	})
}

// An illegal name is refused before anything is rendered, and the refusal
// names what a legal one looks like. The schema has the final word — the rows
// below render each name through CheckData as well, and the two agree, except
// for the reserved names, which the schema admits and cafe refuses. That
// divergence is deliberate and pinned here: the schema's serviceName $defs
// documents the reservation but cannot enforce it, so cafe refuses them at the
// only place it can.
func TestAnIllegalNameIsRefusedAndTheSchemaAgrees(t *testing.T) {
	tests := []struct {
		name string
		// legal is the schema's own answer.
		legal bool
		// reserved is a name the schema accepts and cafe refuses.
		reserved bool
	}{
		{name: "acme", legal: true},
		{name: "acme-billing", legal: true},
		{name: "a1", legal: true},
		{name: "Acme"},
		{name: "acme_billing"},
		{name: "-acme"},
		{name: "acme-"},
		{name: "a"},
		{name: strings.Repeat("a", 41)},
		{name: "cafaye", legal: true, reserved: true},
		{name: "cafe", legal: true, reserved: true},
		{name: "core", legal: true, reserved: true},
		{name: "kit", legal: true, reserved: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateName(tt.name)
			if (err == nil) != (tt.legal && !tt.reserved) {
				t.Fatalf("ValidateName(%q) = %v, want it accepted = %v", tt.name, err, tt.legal && !tt.reserved)
			}

			// The schema's own answer, reached by rendering a real manifest
			// and validating it — the mirror in ValidateName cannot drift
			// from the vendored copy without this going red.
			_, finding := contract.CheckData(manifestPath, mustRenderManifest(t, tt.name))
			if schemaSays := finding.OK(); schemaSays != tt.legal {
				t.Errorf("the schema accepts %q = %v, want %v:\n%s", tt.name, schemaSays, tt.legal, finding)
			}
		})
	}
}

// A scaffold that cannot produce a linting manifest never reaches the disk.
// This is the gate `cafe gen` already holds for its own output: generate into
// memory, refuse, then write.
func TestAnInvalidManifestIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	// The name is the one input a caller controls that reaches the manifest,
	// and the illegal-name rows above drive it through ValidateName first. So
	// the structural gate is driven here by a template whose manifest fails:
	// rendered bytes are checked directly, without a filesystem to mistake a
	// refusal for a write.
	manifest := mustRenderManifest(t, "acme")
	sabotaged := append(manifest, []byte("\nfuture_field: true\n")...)
	if err := validateManifest("app", []File{{Path: manifestPath, Content: sabotaged}}); err == nil {
		t.Error("a manifest with a field the schema does not declare was accepted")
	}
}

// The platform template composes the launch four: identity, gateway, courier,
// and parlor are all declared as required dependencies. This is the packet's
// whole claim — a scaffolded project that runs the platform — tested by
// reading the rendered manifest's dependencies rather than by trusting the
// template's prose.
func TestThePlatformTemplateComposesTheLaunchFour(t *testing.T) {
	manifest, err := renderedManifest(t, "acme", "platform")
	if err != nil {
		t.Fatalf("renderedManifest: %v", err)
	}

	parsed, finding := contract.CheckData(manifestPath, manifest)
	if !finding.OK() {
		t.Fatalf("the platform manifest does not validate:\n%s", finding)
	}

	deps := parsed.Dependencies()
	want := map[string]bool{"identity": true, "gateway": true, "courier": true, "parlor": true}
	if len(deps) != len(want) {
		t.Fatalf("the platform manifest declares %d dependencies, want %d: %v", len(deps), len(want), deps)
	}
	for _, dep := range deps {
		if !want[dep.Name] {
			t.Errorf("dependency %q is not one of the launch four", dep.Name)
		}
		if !dep.Required {
			t.Errorf("dependency %q is not required; the platform app is a starting product, not a degraded one", dep.Name)
		}
	}
}

// The platform app's `cafe dev` story: the manifest composes the four services
// into a runnable stack. This is the extent the story is testable headlessly —
// the compose document renders, the services are present, and the wiring
// (depends_on, connection environment) is correct. What it cannot prove is
// that the containers actually start: the registry entries below are fixtures
// with the four services' real shapes (language, port) and placeholder images,
// and a live `cafe dev` needs the real catalog from pantry and a container
// runtime.
func TestThePlatformTemplatePlansAStackWithTheLaunchFour(t *testing.T) {
	manifest, err := renderedManifest(t, "acme", "platform")
	if err != nil {
		t.Fatalf("renderedManifest: %v", err)
	}

	parsed, finding := contract.CheckData(manifestPath, manifest)
	if !finding.OK() {
		t.Fatalf("the platform manifest does not validate:\n%s", finding)
	}

	// A registry with the launch four, shaped the way the real catalog's
	// entries are: image, port, language, and the connections each service
	// needs. The images are placeholders — the real catalog pins the actual
	// tags — but the wiring is what this test is about.
	reg := dev.Catalog{
		"identity": {
			Name:  "identity",
			Image: "ghcr.io/cafaye/identity:latest",
			Port:  8080,
		},
		"gateway": {
			Name:  "gateway",
			Image: "ghcr.io/cafaye/gateway:latest",
			Port:  8080,
		},
		"courier": {
			Name:  "courier",
			Image: "ghcr.io/cafaye/courier:latest",
			Port:  4000,
		},
		"parlor": {
			Name:  "parlor",
			Image: "ghcr.io/cafaye/parlor:latest",
			Port:  3000,
		},
	}

	// The platform template includes a Dockerfile, so the app builds from the
	// working tree. We are testing the plan in memory rather than writing the
	// scaffolded files to disk, so the Build is passed directly — the same
	// shape `cafe dev` resolves when it finds docker/Dockerfile in the project.
	stack, err := dev.Plan(parsed, reg, dev.Options{
		Build: &dev.Build{Context: ".", Dockerfile: "docker/Dockerfile"},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	// The stack has the app plus the four plus the two pieces of local
	// infrastructure (postgres and redis) that cafe adds to every runnable
	// service.
	want := []string{"acme", "courier", "gateway", "identity", "parlor", "postgres", "redis"}
	if len(stack.Services) != len(want) {
		t.Fatalf("the stack has %d services, want %d: %v", len(stack.Services), len(want), stack.Names())
	}
	for _, name := range want {
		if !hasService(stack, name) {
			t.Errorf("the stack has no service %q; have %v", name, stack.Names())
		}
	}

	// The app depends on all four platform services.
	acme := mustService(t, stack, "acme")
	deps := serviceDeps(acme)
	for _, name := range []string{"identity", "gateway", "courier", "parlor"} {
		if _, found := deps[name]; !found {
			t.Errorf("the app does not depend on %q; dependencies: %v", name, deps)
		}
	}

	// The app is handed the local infrastructure's addresses.
	env := serviceEnv(acme)
	for _, variable := range []string{"DATABASE_URL", "REDIS_URL"} {
		if _, found := env[variable]; !found {
			t.Errorf("the app is not handed %s; environment: %v", variable, env)
		}
	}

	// Every service in the stack is wired to the infrastructure it needs.
	for _, name := range []string{"identity", "gateway", "courier", "parlor"} {
		svc := mustService(t, stack, name)
		svcEnv := serviceEnv(svc)
		if _, found := svcEnv["DATABASE_URL"]; !found {
			t.Errorf("%s is not handed DATABASE_URL; environment: %v", name, svcEnv)
		}
	}

	// The compose document renders — the artifact a developer reads.
	if stack.Compose == "" {
		t.Error("the stack's compose document is empty")
	}
	for _, name := range want {
		if !strings.Contains(stack.Compose, name+":") {
			t.Errorf("the compose document has no service %q", name)
		}
	}
}

// helpers

func hasFile(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

// hasService reports whether a stack has a named service, so an assertion
// reads as the fact it is about rather than as a missing-map-key zero value.
func hasService(stack dev.Stack, name string) bool {
	for _, svc := range stack.Services {
		if svc.Name == name {
			return true
		}
	}
	return false
}

// mustService finds one service in a plan, failing the test when it is absent.
func mustService(t *testing.T, stack dev.Stack, name string) dev.Service {
	t.Helper()
	for _, svc := range stack.Services {
		if svc.Name == name {
			return svc
		}
	}
	t.Fatalf("no service %q in the plan; have %v", name, stack.Names())
	return dev.Service{}
}

// serviceDeps is a service's dependency edges as a name → condition map.
func serviceDeps(svc dev.Service) map[string]string {
	deps := make(map[string]string, len(svc.DependsOn))
	for _, d := range svc.DependsOn {
		deps[d.Name] = d.Condition
	}
	return deps
}

// serviceEnv is a service's environment as a name → value map.
func serviceEnv(svc dev.Service) map[string]string {
	env := make(map[string]string, len(svc.Environment))
	for _, e := range svc.Environment {
		env[e.Name] = e.Value
	}
	return env
}

func pathsOf(files []File) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	return paths
}

func renderedManifest(t *testing.T, name, templateName string) ([]byte, error) {
	t.Helper()
	catalog, err := LoadCatalog()
	if err != nil {
		return nil, err
	}
	template, err := catalog.Resolve(templateName)
	if err != nil {
		return nil, err
	}
	files, err := render(template, dataFor(name))
	if err != nil {
		return nil, err
	}
	manifest, found := manifestOf(files)
	if !found {
		t.Fatalf("template %q has no manifest", templateName)
	}
	return manifest.Content, nil
}

func mustRenderManifest(t *testing.T, name string) []byte {
	t.Helper()
	manifest, err := renderedManifest(t, name, "app")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return manifest
}
