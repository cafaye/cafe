// Package ci is the test that keeps this repository's CI honest. It holds no
// code: `ci/ci.yml` is the artifact under test, and a workflow
// file nobody has executed is a file nobody has tested — the same defect as a
// test suite nobody runs, one level further out.
//
// Four things rot silently in a caller of kit's reusable workflow, and three of
// them have already cost this fleet something:
//
//   - the `uses:` path. kit shipped the file at `workflows/ci.reusable.yml`,
//     which no caller can resolve, because GitHub documents that
//     subdirectories of the workflows directory are not supported. Every
//     service README documented a `uses:` string that did not exist and nobody
//     caught it, because a layout bug and a documentation bug agree with each
//     other perfectly.
//   - the toolchain pin. `versions` is a literal, because a call to a reusable
//     workflow takes no steps and so cannot read go.mod. It drifts the moment
//     somebody raises the go directive.
//   - the coverage threshold. It defaults to 0 so that adoption never blocks a
//     repository on day one, and 0 is also the value that fails nothing.
//   - the gate itself. kit's shared `go` job runs `go mod download`,
//     `go build ./...` and `go test ./...` as three steps of its own. That is
//     not the same command a developer runs, so the two can disagree, and a
//     gate nobody runs locally is a gate nobody can trust.
package ci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// kitWorkflow is the one call this repository makes. It is written out in full
// rather than assembled from parts because the value of comparing it is that a
// reader can check it against kit's tree with one `ls`.
const kitWorkflow = "cafaye/kit/ci/ci.reusable.yml@master"

// The gate and the lockfile, named once. `bin/prime` is what a developer runs
// and what CI must run; `go.sum` is the lockfile it can move.
const (
	gateCommand     = "./bin/prime"
	lockfileCommand = "git diff --exit-code"
)

type step struct {
	Name string         `yaml:"name"`
	Run  string         `yaml:"run"`
	Uses string         `yaml:"uses"`
	With map[string]any `yaml:"with"`
}

type job struct {
	Uses  string         `yaml:"uses"`
	With  map[string]any `yaml:"with"`
	Steps []step         `yaml:"steps"`
}

type workflow struct {
	Jobs map[string]job `yaml:"jobs"`
}

// loadWorkflow reads `ci/ci.yml` from the repository root. The
// root comes from this file's own path and not from the working directory:
// `go test` sets the working directory to the package, and a test that depends
// on where it was invoked from is a test that passes for the wrong reason
// somewhere.
func loadWorkflow(t *testing.T) workflow {
	t.Helper()

	path := filepath.Join(repoRoot(t), "ci", "ci.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var wf workflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(wf.Jobs) == 0 {
		t.Fatalf("%s declares no jobs", path)
	}
	return wf
}

func repoRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed, so the repository root cannot be found")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// with reads one input of a job. A workflow input is a string, but a YAML
// scalar that nobody quoted is not one, and `coverage-fail-under: 0` is exactly
// the mistake this is here to survive — so the value is taken as whatever the
// parser made of it and reported, rather than assumed.
func with(t *testing.T, j job, name string) string {
	t.Helper()

	v, found := j.With[name]
	if !found {
		t.Fatalf("the job does not pass the %q input", name)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("input %q = %#v, want a string; an unquoted YAML scalar reaches Actions as a different type", name, v)
	}
	return s
}

// jobCallingKit is the one job that calls the shared workflow. The jobs that
// do not are cafe's own, and the difference is the whole point of this file.
func jobCallingKit(t *testing.T, wf workflow) (string, job) {
	t.Helper()

	for name, j := range wf.Jobs {
		if strings.HasPrefix(j.Uses, "cafaye/kit/") {
			return name, j
		}
	}
	t.Fatalf("no job calls kit's reusable workflow. This repository is supposed to call %s", kitWorkflow)
	return "", job{}
}

// jobRunning is the job that runs one shell command. Named rather than indexed
// so a failure says which job moved.
func jobRunning(wf workflow, command string) (string, job, bool) {
	for name, j := range wf.Jobs {
		for _, s := range j.Steps {
			if strings.Contains(s.Run, command) {
				return name, j, true
			}
		}
	}
	return "", job{}, false
}

func stepRunning(j job, command string) (string, bool) {
	for _, s := range j.Steps {
		if strings.Contains(s.Run, command) {
			return s.Run, true
		}
	}
	return "", false
}

// TestTheSharedJobCallsKitAtAPathThatResolves is the one that would have
// caught the six months of callers pointing at a file nobody could reach.
func TestTheSharedJobCallsKitAtAPathThatResolves(t *testing.T) {
	wf := loadWorkflow(t)

	calls := 0
	for name, j := range wf.Jobs {
		if !strings.HasPrefix(j.Uses, "cafaye/kit/") {
			continue
		}
		calls++
		if j.Uses != kitWorkflow {
			t.Errorf("job %q calls %q, want %q", name, j.Uses, kitWorkflow)
		}
		assertCallablePath(t, j.Uses)
	}
	if calls == 0 {
		t.Fatalf("no job calls %s. kit's workflow is the shared half of this CI; without the call this file is two local jobs and a claim", kitWorkflow)
	}
}

// assertCallablePath checks the shape rather than the string, because the
// shape is the rule: GitHub resolves a reusable workflow at
// `{owner}/{repo}/ci/{file}@{ref}` and documents that
// subdirectories of the workflows directory are not supported. A path with one
// fewer directory in it is not a path that needs fixing later, it is a path
// that cannot resolve at all.
func assertCallablePath(t *testing.T, uses string) {
	t.Helper()

	repo, ref, ok := strings.Cut(uses, "@")
	if !ok {
		t.Errorf("uses %q has no @ref, so it names no commit", uses)
		return
	}
	if ref == "" {
		t.Errorf("uses %q pins no ref", uses)
	}
	_, file, ok := strings.Cut(repo, "/ci/")
	if !ok {
		t.Errorf("uses %q is not under .github/workflows/, so it resolves to nothing", uses)
		return
	}
	if strings.Contains(file, "/") {
		t.Errorf("uses %q puts the file in a subdirectory of workflows/, which GitHub does not support", uses)
	}
	if !strings.HasSuffix(file, ".yml") && !strings.HasSuffix(file, ".yaml") {
		t.Errorf("uses %q does not name a .yml or .yaml workflow", uses)
	}
}

// TestTheToolchainPinIsTheOneGoModDeclares is the check for the one input that
// cannot be derived from the tree. `versions` is a JSON string on a call to a
// reusable workflow, and a call takes no steps, so the value is a literal — and
// kit's go job falls back to `stable` when the key is absent. A literal that
// drifts from go.mod is a suite that passes on one toolchain and is claimed on
// another.
func TestTheToolchainPinIsTheOneGoModDeclares(t *testing.T) {
	wf := loadWorkflow(t)
	_, shared := jobCallingKit(t, wf)

	raw := with(t, shared, "versions")
	var versions struct {
		Go string `json:"go"`
	}
	if err := json.Unmarshal([]byte(raw), &versions); err != nil {
		t.Fatalf("versions %q is not JSON: %v", raw, err)
	}
	if versions.Go == "" {
		t.Fatalf("versions %q pins no go; kit's go job then uses `stable`, and a suite that passes today must not depend on which image was cached", raw)
	}

	// The two floating values kit would otherwise be given, named so the error
	// says which one it is rather than just that it is wrong.
	for _, floating := range []string{"stable", "latest"} {
		if versions.Go == floating {
			t.Errorf("the go pin is %q. Pin the version go.mod declares; a cached image is not a pin", versions.Go)
		}
	}

	want := goDirective(t)
	if versions.Go != want {
		t.Errorf("the go pin is %q but go.mod declares `go %s`. Raise both together or neither", versions.Go, want)
	}
}

// goDirective reads the `go` line out of go.mod. Hand-parsed rather than
// imported: this repository is the standard library plus the three
// dependencies internal/contract and internal/mcp document, and a module for
// one line of a file that is four lines long is not a trade anybody makes.
func goDirective(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "go" {
			return fields[1]
		}
	}
	t.Fatal("go.mod has no `go` directive, so there is no version to pin")
	return ""
}

// TestTheCoverageGateIsAboveZero guards the input whose default is the one
// value that cannot fail. kit ships 0 on purpose so adoption never blocks a
// repository on day one, and this repository is past day one.
func TestTheCoverageGateIsAboveZero(t *testing.T) {
	wf := loadWorkflow(t)
	_, shared := jobCallingKit(t, wf)

	raw := with(t, shared, "coverage-fail-under")
	percent, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("coverage-fail-under %q is not a number: %v", raw, err)
	}
	if percent <= 0 {
		t.Errorf("coverage-fail-under is %d. A threshold of 0 fails nothing, so the coverage step is a report rather than a gate", percent)
	}
}

// TestTheGateJobRunsTheGateAndTheLockfileGuard is the one that keeps CI and a
// developer from disagreeing. kit's shared go job runs its own three steps; the
// repository's gate is `bin/prime`; and the two are not the same command. So
// cafe runs the gate itself, and the lockfile guard has to be in the *same* job —
// a `git diff --exit-code` on a different runner is diffing a different tree,
// which is a guard that can never fail and looks exactly like one that passed.
func TestTheGateJobRunsTheGateAndTheLockfileGuard(t *testing.T) {
	wf := loadWorkflow(t)

	name, j, ok := jobRunning(wf, gateCommand)
	if !ok {
		t.Fatalf("no job runs %s, so CI is running something a developer never runs. If the two can disagree, one of them is lying", gateCommand)
	}

	guard, found := stepRunning(j, lockfileCommand)
	if !found {
		t.Fatalf("job %q runs %s but has no `%s` step. bin/prime runs `go mod download`, which writes go.sum when the tree has none, and drift that lands in the lockfile is otherwise invisible", name, gateCommand, lockfileCommand)
	}
	for _, file := range []string{"go.mod", "go.sum"} {
		if !strings.Contains(guard, file) {
			t.Errorf("the lockfile guard in job %q does not name %s", name, file)
		}
	}
}

// TestTheGateRunsTheSameLintCIRuns is the check roll-c5-cafe-ci exists for.
// kit's shared `go` job lints with golangci-lint at a pinned version over
// kit's `lint/golangci.yml`, and for seventeen hours that lint failed on
// master while `bin/prime` stayed green — because the gate never ran the
// linter at all. So the gate runs it now, and this test keeps the two from
// parting company again:
//
//   - `bin/prime` runs `golangci-lint run` with an explicit `--config`
//     (never GOLANGCI_LINT_CONFIG, which v2.6.2 reads as nothing) resolved
//     from kit's checkout, and refuses rather than skips when the tool or
//     the config is absent.
//   - `mise.toml` pins the linter version, and the pin is the one version
//     in this tree: `bin/prime` reads it rather than repeating it.
//   - the CI job that runs the gate provides both halves: a step installing
//     the linter at mise.toml's pin, and kit's `lint/` checked out where
//     `bin/prime` looks for it.
//
// On the tree as it stands on master this test fails at the first assertion:
// `bin/prime` there never mentions the linter, and the lint step is what
// discriminates. That is the RED control, and it is load-bearing: revert the
// lint section of `bin/prime` and a finding injected into a temp file passes
// silently.
func TestTheGateRunsTheSameLintCIRuns(t *testing.T) {
	root := repoRoot(t)

	prime, err := os.ReadFile(filepath.Join(root, "bin", "prime"))
	if err != nil {
		t.Fatalf("read bin/prime: %v", err)
	}
	gate := string(prime)
	if !strings.Contains(gate, "golangci-lint run") {
		t.Fatalf("bin/prime never runs golangci-lint, so the local gate cannot catch what CI's lint step catches")
	}
	if !strings.Contains(gate, "--config") {
		t.Errorf("bin/prime runs the linter without --config, and without it golangci-lint v2 falls back to its built-in default set and exits 0 on a file kit's config rejects")
	}
	if !strings.Contains(gate, ".kit/lint/golangci.yml") || !strings.Contains(gate, "../kit/lint/golangci.yml") {
		t.Errorf("bin/prime does not resolve kit's lint config from .kit (CI) or ../kit (workspace); a config found neither place is a lint that never runs")
	}
	if strings.Contains(gate, "GOLANGCI_LINT_CONFIG") {
		t.Errorf("bin/prime sets GOLANGCI_LINT_CONFIG, which measured v2.6.2 reads as nothing — the run proceeds on the default set either way")
	}

	pin := lintPin(t, root)
	if !strings.Contains(gate, "mise.toml") {
		t.Errorf("bin/prime never reads mise.toml, so its linter version is a second copy of the pin that can drift")
	}

	wf := loadWorkflow(t)
	name, j, ok := jobRunning(wf, gateCommand)
	if !ok {
		t.Fatalf("no job runs %s, so there is no CI side to compare the gate against", gateCommand)
	}
	installer, found := stepRunning(j, "mise.toml")
	if !found || !strings.Contains(installer, "golangci-lint") {
		t.Errorf("job %q runs the gate but no step installs golangci-lint at mise.toml's pin — bin/prime exits 127 there, and the gate was right", name)
	}
	checkout := false
	for _, s := range j.Steps {
		if !strings.HasPrefix(s.Uses, "actions/checkout") {
			continue
		}
		path, _ := s.With["path"].(string)
		sparse, _ := s.With["sparse-checkout"].(string)
		if path == ".kit" && sparse == "lint" {
			checkout = true
		}
	}
	if !checkout {
		t.Errorf("job %q runs the gate but never checks out kit's lint/ to .kit, so bin/prime finds no config and fails — or, worse, a future edit makes it pass on the default set", name)
	}

	if kitVersion, found := kitLintVersion(t, root); found && kitVersion != pin {
		t.Errorf("mise.toml pins golangci-lint %s but kit's reusable workflow runs %s — one bump, two files, same commit", pin, kitVersion)
	}
}

// lintPin reads the golangci-lint pin out of mise.toml. One pin, read where it
// lives: a literal here would be the second copy this test exists to forbid.
func lintPin(t *testing.T, root string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(root, "mise.toml"))
	if err != nil {
		t.Fatalf("read mise.toml: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, `"golangci-lint"`) {
			pin := strings.Trim(strings.TrimPrefix(line, `"golangci-lint"`), " =\"")
			if pin == "" {
				t.Fatalf("mise.toml pins no golangci-lint version, and bin/prime refuses to run without the binary")
			}
			return pin
		}
	}
	t.Fatalf("mise.toml holds no golangci-lint pin, and bin/prime refuses to run without the binary")
	return ""
}

// kitLintVersion reads the linter version out of kit's reusable workflow, from
// the checkout the gate itself resolves: `.kit` in CI, `../kit` in a workspace
// checkout. No checkout beside the tree means no comparison rather than a
// failure — but CI always has `.kit`, so the comparison is real exactly where
// a drift would ship.
func kitLintVersion(t *testing.T, root string) (string, bool) {
	t.Helper()

	for _, dir := range []string{".kit", "../kit"} {
		raw, err := os.ReadFile(filepath.Join(root, dir, "ci", "ci.reusable.yml"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "version:") {
				version := strings.Trim(strings.TrimPrefix(line, "version:"), " v\"'")
				if version != "" && version[0] >= '0' && version[0] <= '9' {
					return version, true
				}
			}
		}
	}
	t.Logf("no kit checkout beside the tree, so the pin comparison is skipped here; CI always checks out .kit, so it runs there")
	return "", false
}
