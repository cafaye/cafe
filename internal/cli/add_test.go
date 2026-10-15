package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cafaye/cafe/internal/contract"
)

func writeMinimalManifest(t *testing.T, dir string) {
	t.Helper()
	content := "name: demo\n" +
		"description: demo service\n" +
		"language: go\n" +
		"core: ^0.2.0\n" +
		"repository:\n  url: git@github.com:cafaye/demo.git\n" +
		"owner:\n  team: demo\n" +
		"  contact: demo@cafaye.com\n"
	if err := os.WriteFile(filepath.Join(dir, "cafaye.yml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runAddCLI(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion, Context: context.Background()}
	err := newAddCommand(addDeps{dir: dir}).Execute(env, args)
	return codeFor(*env, err), out.String(), errOut.String()
}

func TestAddRoundTripAndLint(t *testing.T) {
	dir := t.TempDir()
	writeMinimalManifest(t, dir)
	code, stdout, stderr := runAddCLI(t, dir, "pantry/billing")
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr)
	}
	if !json.Valid([]byte(stdout)) {
		t.Fatalf("stdout not JSON: %s", stdout)
	}
	if !strings.Contains(stderr, "billing") || !strings.Contains(stderr, "cafaye.yml") {
		t.Errorf("stderr missing file or dependency: %s", stderr)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "cafaye.yml"))
	_, finding := contract.CheckData("cafaye.yml", mustYAMLToJSON(data))
	// CheckData expects JSON via YAMLToJSON path? but we wrote YAML, need CheckData with raw YAML
	// So use contract.Parse path: CheckData handles YAML->JSON internally, so pass raw YAML.
	_, finding2 := contract.CheckData("cafaye.yml", data)
	if !finding2.OK() {
		t.Fatalf("result fails lint: %v", finding2)
	}
	_ = finding
}

func TestAddIdempotent(t *testing.T) {
	dir := t.TempDir()
	writeMinimalManifest(t, dir)
	if code, _, _ := runAddCLI(t, dir, "pantry/billing"); code != 0 {
		t.Fatal("first add failed")
	}
	secondData, _ := os.ReadFile(filepath.Join(dir, "cafaye.yml"))
	code, stdout, _ := runAddCLI(t, dir, "pantry/billing")
	if code != 0 {
		t.Fatalf("second add exit %d", code)
	}
	var doc map[string]any
	_ = json.Unmarshal([]byte(stdout), &doc)
	if doc["action"] != "already-present" {
		t.Errorf("action %v want already-present", doc["action"])
	}
	afterData, _ := os.ReadFile(filepath.Join(dir, "cafaye.yml"))
	if string(secondData) != string(afterData) {
		t.Error("idempotent add changed file")
	}
}

func TestAddRefusesBadRef(t *testing.T) {
	dir := t.TempDir()
	writeMinimalManifest(t, dir)
	for _, ref := range []string{"", "billing", "pantry/", "pantry/BAD", "pantry/a"} {
		code, _, stderr := runAddCLI(t, dir, ref)
		if code == 0 {
			t.Errorf("ref %q should have failed", ref)
		}
		if !strings.Contains(stderr, "cafe add") {
			t.Errorf("ref %q stderr missing cafe add: %s", ref, stderr)
		}
	}
	// ensure file unchanged
	data, _ := os.ReadFile(filepath.Join(dir, "cafaye.yml"))
	if strings.Contains(string(data), "billing") {
		t.Error("bad ref wrote file")
	}
}

func TestAddSaysWhatItDid(t *testing.T) {
	dir := t.TempDir()
	writeMinimalManifest(t, dir)
	code, stdout, stderr := runAddCLI(t, dir, "pantry/payments")
	if code != 0 {
		t.Fatalf("exit %d %s", code, stderr)
	}
	var doc addDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.File == "" || doc.Dependency != "payments" {
		t.Errorf("doc %+v", doc)
	}
	if !strings.Contains(stderr, "payments") {
		t.Errorf("stderr %s", stderr)
	}
}
