// Package deps detects a project's dependencies from its source tree, so
// auto-subscribe can find publisher channels without the user listing them.
package deps

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/cafaye/cafe/internal/contract"
)

// Detect scans dir for known manifest files and returns the dependency names
// it finds. Names are deduplicated and sorted. A directory with no
// recognizable manifest yields an empty list (not an error).
func Detect(dir string) []string {
	set := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		// strip version suffixes / path segments for go modules etc.
		set[name] = true
	}

	// cafaye.yml — canonical source.
	if m, err := readCafayeDeps(filepath.Join(dir, contract.ManifestFileName())); err == nil {
		for _, d := range m {
			add(d)
		}
	}

	// Gemfile (Ruby).
	if data, err := os.ReadFile(filepath.Join(dir, "Gemfile")); err == nil {
		re := regexp.MustCompile(`(?m)^\s*gem\s+['"]([^'"]+)['"]`)
		for _, m := range re.FindAllStringSubmatch(string(data), -1) {
			add(m[1])
		}
	}

	// composer.json (PHP).
	if data, err := os.ReadFile(filepath.Join(dir, "composer.json")); err == nil {
		var doc struct {
			Require    map[string]string `json:"require"`
			RequireDev map[string]string `json:"require-dev"`
		}
		if json.Unmarshal(data, &doc) == nil {
			for k := range doc.Require {
				if !isPhpPlatform(k) {
					add(k)
				}
			}
			for k := range doc.RequireDev {
				if !isPhpPlatform(k) {
					add(k)
				}
			}
		}
	}

	// package.json (Node).
	if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var doc struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(data, &doc) == nil {
			for k := range doc.Dependencies {
				add(k)
			}
			for k := range doc.DevDependencies {
				add(k)
			}
		}
	}

	// go.mod (Go).
	if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
		re := regexp.MustCompile(`(?m)^\s*require\s+(\S+)\s`)
		for _, m := range re.FindAllStringSubmatch(string(data), -1) {
			add(m[1])
		}
		// also handle require block lines
		inBlock := false
		for _, line := range strings.Split(string(data), "\n") {
			t := strings.TrimSpace(line)
			if t == "require (" {
				inBlock = true
				continue
			}
			if inBlock && t == ")" {
				inBlock = false
				continue
			}
			if inBlock {
				fields := strings.Fields(t)
				if len(fields) >= 1 && !strings.HasPrefix(fields[0], "//") {
					add(fields[0])
				}
			}
		}
	}

	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func readCafayeDeps(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	manifest, finding := contract.CheckData(path, data)
	if !finding.OK() {
		return nil, fmt.Errorf("invalid manifest")
	}
	var out []string
	for _, d := range manifest.Dependencies() {
		out = append(out, d.Name)
	}
	return out, nil
}

func isPhpPlatform(name string) bool {
	return strings.HasPrefix(name, "php") || strings.HasPrefix(name, "ext-") || strings.HasPrefix(name, "lib-")
}
