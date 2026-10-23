package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/anywaye/cafe/internal/contract"
	"github.com/goccy/go-yaml"
)

type addDeps struct {
	// dir is the project directory. Empty means current directory.
	dir string
}

// addDoc is the machine document on stdout.
type addDoc struct {
	Action     string `json:"action"`
	File       string `json:"file"`
	Dependency string `json:"dependency"`
	Message    string `json:"message"`
}

var pantryRefPattern = regexp.MustCompile(`^pantry/([a-z][a-z0-9]*(-[a-z0-9]+)*)$`)
var serviceNamePat = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

func newAddCommand(deps addDeps) *Command {
	c := &Command{
		Name:    "add",
		Summary: "add a pantry service dependency to the current project's anywaye.yml",
		Usage:   "cafe add pantry/<name> [--dir <path>]",
		LongHelp: "Adds a pantry service dependency to the current project's anywaye.yml.\n\n" +
			"The manifest's `dependencies` field is validated against the schema core\n" +
			"owns, vendored at internal/contract/schemas — the dependency entry's exact\n" +
			"shape is that schema's `serviceRef`: `name` (required, serviceName),\n" +
			"optional `version` (semverRange), optional `required` (bool, defaults true).\n" +
			"`additionalProperties: false` means a field core does not declare is a lint\n" +
			"error under `cafe contract lint`, so this command writes only `name`.\n\n" +
			"Idempotent: adding what is already there says so and changes nothing.\n" +
			"A bad or missing ref is refused with a named, actionable error.\n" +
			"The result still passes `cafe contract lint`'s rules: the updated file is\n" +
			"re-validated before it is written, and a file that would not pass is not\n" +
			"written.",
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) != 1 {
			return fmt.Errorf("%w: cafe add wants exactly one argument pantry/<name> (usage: %s)", errUsage, c.Usage)
		}
		ref := strings.TrimSpace(args[0])
		name, err := parsePantryRef(ref)
		if err != nil {
			_, _ = fmt.Fprintf(env.Stderr, "cafe add: %v\n", err)
			return errReported
		}
		dir := deps.dir
		if dir == "" {
			dir = "."
		}
		manifestPath := filepath.Join(dir, contract.ManifestFileName())
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			_, _ = fmt.Fprintf(env.Stderr, "cafe add: no %s at %s: %v\n", contract.ManifestFileName(), manifestPath, err)
			_, _ = fmt.Fprintf(env.Stderr, "cafe add: run `cafe init` or `cafe new` first, or pass a directory that holds a anywaye.yml\n")
			return errReported
		}
		manifest, finding := contract.CheckData(manifestPath, data)
		if !finding.OK() {
			first, _ := finding.First()
			_, _ = fmt.Fprintf(env.Stderr, "cafe add: the existing %s is not valid: %s\n", manifestPath, first)
			return errReported
		}
		_ = manifest
		// decode raw map to preserve fields
		var raw map[string]any
		if err := yaml.Unmarshal(data, &raw); err != nil {
			_, _ = fmt.Fprintf(env.Stderr, "cafe add: the manifest is not readable YAML: %v\n", err)
			return errReported
		}
		// check idempotency
		depsRaw, _ := raw["dependencies"].([]any)
		for _, entry := range depsRaw {
			if m, ok := entry.(map[string]any); ok {
				if m["name"] == name {
					sayAdd(env.Stderr, "%s already depends on %s; nothing changed", manifestPath, name)
					doc := addDoc{Action: "already-present", File: manifestPath, Dependency: name, Message: manifestPath + " already depends on " + name}
					return writeJSONDoc(env.Stdout, doc)
				}
			}
		}
		// append dependency with only name (version omitted, required defaults true)
		newEntry := map[string]any{"name": name}
		if depsRaw == nil {
			raw["dependencies"] = []any{newEntry}
		} else {
			raw["dependencies"] = append(depsRaw, newEntry)
		}
		// marshal to YAML via JSON roundtrip is safest; use yaml.Marshal
		out, err := yaml.Marshal(raw)
		if err != nil {
			_, _ = fmt.Fprintf(env.Stderr, "cafe add: encode the updated manifest: %v\n", err)
			return errReported
		}
		// validate before writing
		_, after := contract.CheckData(manifestPath, mustYAMLToJSON(out))
		if !after.OK() {
			first, _ := after.First()
			_, _ = fmt.Fprintf(env.Stderr, "cafe add: the updated %s would not pass lint: %s\n", manifestPath, first)
			return errReported
		}
		if err := os.WriteFile(manifestPath, out, 0o644); err != nil {
			_, _ = fmt.Fprintf(env.Stderr, "cafe add: write %s: %v\n", manifestPath, err)
			return errReported
		}
		sayAdd(env.Stderr, "added %s to %s", name, manifestPath)
		doc := addDoc{Action: "added", File: manifestPath, Dependency: name, Message: "added " + name + " to " + manifestPath}
		return writeJSONDoc(env.Stdout, doc)
	}
	return c
}

func sayAdd(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, "cafe add: "+format+"\n", args...)
}

func parsePantryRef(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("cafe add: a dependency ref is required: pantry/<name> (e.g. pantry/billing)")
	}
	m := pantryRefPattern.FindStringSubmatch(strings.TrimSpace(ref))
	if m == nil {
		// actionable: explain shape
		if strings.HasPrefix(ref, "pantry/") {
			name := strings.TrimPrefix(ref, "pantry/")
			if name == "" {
				return "", fmt.Errorf("cafe add: pantry/<name> is missing the name: got %q; try pantry/billing", ref)
			}
			return "", fmt.Errorf("cafe add: %q is not a anywaye namespace name: lowercase kebab-case, 2-40 chars, start with letter (got %q)", name, name)
		}
		return "", fmt.Errorf("cafe add: a pantry dependency is written as pantry/<name> (e.g. pantry/billing), got %q", ref)
	}
	name := m[1]
	if len(name) < 2 || len(name) > 40 {
		return "", fmt.Errorf("cafe add: %q is not a anywaye namespace name: must be 2-40 characters", name)
	}
	if !serviceNamePat.MatchString(name) {
		return "", fmt.Errorf("cafe add: %q is not a anywaye namespace name: lowercase kebab-case", name)
	}
	return name, nil
}

func mustYAMLToJSON(yamlData []byte) []byte {
	j, err := yaml.YAMLToJSON(yamlData)
	if err != nil {
		// fallback: return empty that will fail validation loudly
		b, _ := json.Marshal(map[string]any{})
		return b
	}
	return j
}
