package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The update tests build real template/fork repository pairs in temporary
// directories and drive the real command over them. There is no network —
// every source is a local path — no container, and no new dependency: the
// merge git performs in a test is the same three-way merge the command runs
// anywhere else, which is the only reason a fixture here proves anything.

// updateGitEnv makes the test's git commits hermetic: no global config is
// read for identity, and nothing the developer configured leaks in.
func updateGitEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "cafe test")
	t.Setenv("GIT_AUTHOR_EMAIL", "caf-test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "cafe test")
	t.Setenv("GIT_COMMITTER_EMAIL", "caf-test@example.com")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("git is not on PATH: the update tests merge real repositories and cannot run without it")
	}
}

// runUpdateGit runs git in dir and fails the test on any error, returning
// stdout trimmed. Fixture setup must succeed; the cases assert on the
// command's own failures instead.
func runUpdateGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeUpdateFile writes path, creating its directory.
func writeUpdateFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// commitUpdateAll stages everything and commits. Every fixture commit goes
// through this, so a dirty tree in a case is always the case's own doing.
func commitUpdateAll(t *testing.T, dir, msg string) {
	t.Helper()
	runUpdateGit(t, dir, "add", "-A")
	runUpdateGit(t, dir, "commit", "-m", msg)
}

// updateManifest renders a template.yml pinning pin with the given remotes
// block. The added/diverged/removed lists are present so the parse is proven
// against a full Site-shaped file rather than a minimal one.
func updateManifest(repository, branch, pin, remotes string) string {
	return "version: 1\n\ntemplate:\n" +
		"  name: parlor\n" +
		"  repository: " + repository + "\n" +
		"  branch: " + branch + "\n" +
		"  commit: " + pin + "\n" +
		remotes +
		"  diverged:\n    - custom.txt\n" +
		"  removed:\n    - gone.txt\n" +
		"  added:\n    - custom.txt\n"
}

// updateFixture is one template/fork pair: upstream holds the pin commit A
// and one commit past it, downstream is a clone on branch "work" carrying its
// own customization commit and a template.yml pinning A.
type updateFixture struct {
	root       string
	upstream   string
	downstream string
	manifest   string
	pin        string
	tip        string
}

// seedUpdateFixture builds the pair. Upstream's seed is app.txt v1; its second
// commit moves app.txt to v2 and adds new.txt. Downstream's customization is
// custom.txt, which no upstream commit touches, so the merge is clean.
func seedUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()
	updateGitEnv(t)
	root := t.TempDir()
	upstream := filepath.Join(root, "upstream")
	if err := os.MkdirAll(upstream, 0o755); err != nil {
		t.Fatal(err)
	}
	runUpdateGit(t, upstream, "init")
	writeUpdateFile(t, filepath.Join(upstream, "app.txt"), "v1\n")
	writeUpdateFile(t, filepath.Join(upstream, "shared.txt"), "base\n")
	commitUpdateAll(t, upstream, "seed")
	runUpdateGit(t, upstream, "branch", "-M", "master")
	pin := runUpdateGit(t, upstream, "rev-parse", "HEAD")

	downstream := filepath.Join(root, "downstream")
	runUpdateGit(t, root, "clone", upstream, downstream)
	runUpdateGit(t, downstream, "checkout", "-b", "work")
	manifest := filepath.Join(downstream, "template.yml")
	remotes := "  remotes:\n    - name: tmpl-up\n      url: " + upstream + "\n      kind: local\n"
	writeUpdateFile(t, manifest, updateManifest(upstream, "master", pin, remotes))
	writeUpdateFile(t, filepath.Join(downstream, "custom.txt"), "ours\n")
	commitUpdateAll(t, downstream, "downstream customizations")

	writeUpdateFile(t, filepath.Join(upstream, "app.txt"), "v2\n")
	writeUpdateFile(t, filepath.Join(upstream, "new.txt"), "new\n")
	commitUpdateAll(t, upstream, "upstream work")
	tip := runUpdateGit(t, upstream, "rev-parse", "HEAD")

	return &updateFixture{root: root, upstream: upstream, downstream: downstream, manifest: manifest, pin: pin, tip: tip}
}

// runUpdateIn runs `cafe update` with args in dir and returns the exit code
// and both streams, the way main would.
func runUpdateIn(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	t.Chdir(dir)
	return runCLI(t, testVersion, append([]string{"update"}, args...)...)
}

// headOf returns HEAD's sha in dir.
func headOf(t *testing.T, dir string) string {
	t.Helper()
	return runUpdateGit(t, dir, "rev-parse", "HEAD")
}

// pinOf reads the commit: pin back out of a template.yml.
func pinOf(t *testing.T, manifest string) string {
	t.Helper()
	file, _, err := loadUpdateFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return file.Template.Commit
}

// TestUpdateRejectsABadPin is the pin rule without any repository: a branch
// name, a short sha and the other malformed pins are refused before git is
// ever asked, and an unknown version is refused rather than misread.
func TestUpdateRejectsABadPin(t *testing.T) {
	tests := []struct {
		name    string
		commit  string
		version string
		want    string
	}{
		{name: "a branch name is not a pin", commit: "master", want: "not a full 40-character sha"},
		{name: "a short sha is not a pin", commit: "1a3cde1", want: "not a full 40-character sha"},
		{name: "an empty pin is not a pin", commit: "", want: "not a full 40-character sha"},
		{name: "a non-hex pin is not a pin", commit: "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", want: "not a full 40-character sha"},
		{name: "a 39-character sha is not a pin", commit: "1a3cde1ec80c2faade6fefe9a6d5fff7465a127", want: "not a full 40-character sha"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			manifest := filepath.Join(dir, "template.yml")
			version := "1"
			if tt.version != "" {
				version = tt.version
			}
			remotes := "  remotes:\n    - name: tmpl-up\n      url: ../parlor\n      kind: local\n"
			writeUpdateFile(t, manifest, "version: "+version+"\ntemplate:\n  name: parlor\n  repository: x\n  branch: master\n  commit: "+tt.commit+"\n"+remotes)

			if _, _, err := loadUpdateFile(manifest); err == nil {
				t.Fatalf("loadUpdateFile accepted commit %q", tt.commit)
			} else if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %q, want it to mention %q", err, tt.want)
			}
		})
	}

	t.Run("an unknown version is refused rather than misread", func(t *testing.T) {
		dir := t.TempDir()
		manifest := filepath.Join(dir, "template.yml")
		remotes := "  remotes:\n    - name: tmpl-up\n      url: ../parlor\n      kind: local\n"
		writeUpdateFile(t, manifest, "version: 2\ntemplate:\n  name: parlor\n  repository: x\n  branch: master\n  commit: 1a3cde1ec80c2faade6fefe9a6d5fff7465a127b\n"+remotes)

		if _, _, err := loadUpdateFile(manifest); err == nil {
			t.Fatal("loadUpdateFile accepted version 2")
		} else if !strings.Contains(err.Error(), "unsupported version") {
			t.Errorf("err = %q, want it to refuse the version", err)
		}
	})

	t.Run("a remote without a url is refused", func(t *testing.T) {
		dir := t.TempDir()
		manifest := filepath.Join(dir, "template.yml")
		writeUpdateFile(t, manifest, "version: 1\ntemplate:\n  name: parlor\n  repository: x\n  branch: master\n  commit: 1a3cde1ec80c2faade6fefe9a6d5fff7465a127b\n  remotes:\n    - name: tmpl-up\n")

		if _, _, err := loadUpdateFile(manifest); err == nil {
			t.Fatal("loadUpdateFile accepted a remote with no url")
		} else if !strings.Contains(err.Error(), "no name or no url") {
			t.Errorf("err = %q, want it to name the missing url", err)
		}
	})
}

// TestUpdateRefusesTheDefaultBranch holds the branch guard: neither master
// nor main takes an update merge, and the refusal names the way out.
func TestUpdateRefusesTheDefaultBranch(t *testing.T) {
	for _, branch := range []string{"master", "main"} {
		t.Run("on "+branch, func(t *testing.T) {
			f := seedUpdateFixture(t)
			runUpdateGit(t, f.downstream, "checkout", "-B", branch)

			code, _, stderr := runUpdateIn(t, f.downstream)

			if code != exitFailure {
				t.Errorf("exit code = %d, want %d (stderr: %s)", code, exitFailure, stderr)
			}
			if !strings.Contains(stderr, "refusing to merge a template update on "+branch) {
				t.Errorf("stderr does not refuse the branch:\n%s", stderr)
			}
			if got := headOf(t, f.downstream); got == f.tip {
				t.Error("the merge ran on the default branch")
			}
		})
	}
}

// TestUpdateCheckIsAllowedOnTheDefaultBranch holds the preview exception to
// the branch guard: --check runs on master and main, reports the selected
// source and its tip, and moves nothing — HEAD, the branch, the manifest
// bytes and the worktree status are all unchanged.
func TestUpdateCheckIsAllowedOnTheDefaultBranch(t *testing.T) {
	for _, branch := range []string{"master", "main"} {
		t.Run("on "+branch, func(t *testing.T) {
			f := seedUpdateFixture(t)
			runUpdateGit(t, f.downstream, "checkout", "-B", branch)
			beforeHEAD := headOf(t, f.downstream)
			beforeManifest, err := os.ReadFile(f.manifest)
			if err != nil {
				t.Fatal(err)
			}
			beforeTree := runUpdateGit(t, f.downstream, "status", "--porcelain")

			code, stdout, stderr := runUpdateIn(t, f.downstream, "--check")

			if code != exitSuccess {
				t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr)
			}
			for _, want := range []string{"tmpl-up", f.tip, "re-run without --check"} {
				if !strings.Contains(stdout, want) {
					t.Errorf("preview missing %q:\n%s", want, stdout)
				}
			}
			if got := headOf(t, f.downstream); got != beforeHEAD {
				t.Error("a preview moved HEAD")
			}
			if got := runUpdateGit(t, f.downstream, "branch", "--show-current"); got != branch {
				t.Errorf("a preview moved the branch to %s", got)
			}
			afterManifest, err := os.ReadFile(f.manifest)
			if err != nil {
				t.Fatal(err)
			}
			if string(afterManifest) != string(beforeManifest) {
				t.Error("a preview rewrote template.yml")
			}
			if got := runUpdateGit(t, f.downstream, "status", "--porcelain"); got != beforeTree {
				t.Errorf("a preview changed the worktree:\n%s", got)
			}
			if got := pinOf(t, f.manifest); got != f.pin {
				t.Errorf("a preview moved the pin to %s", got)
			}
		})
	}
}

// TestUpdateRefusesADirtyWorktree holds the clean-tree guard: a mutation
// refuses a dirty checkout and leaves HEAD alone, while --check still
// previews over the same dirt without touching it.
func TestUpdateRefusesADirtyWorktree(t *testing.T) {
	t.Run("a real run refuses", func(t *testing.T) {
		f := seedUpdateFixture(t)
		before := headOf(t, f.downstream)
		writeUpdateFile(t, filepath.Join(f.downstream, "app.txt"), "uncommitted\n")

		code, _, stderr := runUpdateIn(t, f.downstream)

		if code != exitFailure {
			t.Errorf("exit code = %d, want %d (stderr: %s)", code, exitFailure, stderr)
		}
		if !strings.Contains(stderr, "dirty worktree") {
			t.Errorf("stderr does not name the dirt:\n%s", stderr)
		}
		if got := headOf(t, f.downstream); got != before {
			t.Error("a refused merge moved HEAD")
		}
		if got := pinOf(t, f.manifest); got != f.pin {
			t.Errorf("the pin moved to %s on a refused merge", got)
		}
	})

	t.Run("a preview is still allowed", func(t *testing.T) {
		f := seedUpdateFixture(t)
		writeUpdateFile(t, filepath.Join(f.downstream, "app.txt"), "uncommitted\n")

		code, stdout, stderr := runUpdateIn(t, f.downstream, "--check")

		if code != exitSuccess {
			t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr)
		}
		if !strings.Contains(stdout, f.tip) {
			t.Errorf("the preview does not name the tip:\n%s", stdout)
		}
		if got := headOf(t, f.downstream); got == f.tip {
			t.Error("a preview merged")
		}
	})
}

// TestUpdatePreviewChangesNothing is the check-mode contract: no tracked file
// moves, the branch stays, and the pin stays, while the report still names
// the selected source and its tip.
func TestUpdatePreviewChangesNothing(t *testing.T) {
	f := seedUpdateFixture(t)
	beforeHEAD := headOf(t, f.downstream)
	beforeBranch := runUpdateGit(t, f.downstream, "branch", "--show-current")
	beforeManifest, err := os.ReadFile(f.manifest)
	if err != nil {
		t.Fatal(err)
	}
	beforeTree := runUpdateGit(t, f.downstream, "status", "--porcelain")

	code, stdout, stderr := runUpdateIn(t, f.downstream, "--check")

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr)
	}
	for _, want := range []string{"tmpl-up", f.tip, "re-run without --check"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("preview missing %q:\n%s", want, stdout)
		}
	}
	if got := headOf(t, f.downstream); got != beforeHEAD {
		t.Error("a preview moved HEAD")
	}
	if got := runUpdateGit(t, f.downstream, "branch", "--show-current"); got != beforeBranch {
		t.Errorf("a preview moved the branch to %s", got)
	}
	afterManifest, err := os.ReadFile(f.manifest)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterManifest) != string(beforeManifest) {
		t.Error("a preview rewrote template.yml")
	}
	if got := runUpdateGit(t, f.downstream, "status", "--porcelain"); got != beforeTree {
		t.Errorf("a preview changed the worktree:\n%s", got)
	}
}

// TestUpdateMergesUpstreamAndAdvancesThePin is the happy path: a real
// three-way merge, so the merge commit has two parents, upstream's work
// arrives, downstream's customization survives, and the pin names the tip in
// that same merge commit.
func TestUpdateMergesUpstreamAndAdvancesThePin(t *testing.T) {
	f := seedUpdateFixture(t)

	code, stdout, stderr := runUpdateIn(t, f.downstream)

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)\nstdout:\n%s", code, stderr, stdout)
	}
	// Upstream arrived.
	if got, err := os.ReadFile(filepath.Join(f.downstream, "app.txt")); err != nil || string(got) != "v2\n" {
		t.Errorf("app.txt = %q, want upstream v2", got)
	}
	if got, err := os.ReadFile(filepath.Join(f.downstream, "new.txt")); err != nil || string(got) != "new\n" {
		t.Errorf("new.txt = %q, want the upstream addition", got)
	}
	// Downstream survived.
	if got, err := os.ReadFile(filepath.Join(f.downstream, "custom.txt")); err != nil || string(got) != "ours\n" {
		t.Errorf("custom.txt = %q, want the downstream customization intact", got)
	}
	// A real merge commit, not a fast-forward and not a squash.
	parents := strings.Fields(runUpdateGit(t, f.downstream, "rev-list", "--parents", "-n", "1", "HEAD"))
	if len(parents) != 3 {
		t.Errorf("HEAD has %d parent(s), want a two-parent merge commit", len(parents)-1)
	}
	// The pin moved, in that commit.
	if got := pinOf(t, f.manifest); got != f.tip {
		t.Errorf("the pin is %s, want the tip %s", got, f.tip)
	}
	if out := runUpdateGit(t, f.downstream, "show", "--name-only", "--pretty=format:", "HEAD"); !strings.Contains(out, "template.yml") {
		t.Errorf("the merge commit does not carry template.yml:\n%s", out)
	}
	// Comments survived the rewrite: the file still says what it said.
	raw, err := os.ReadFile(f.manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"diverged:", "removed:", "added:", "version: 1"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the rewritten template.yml lost %q:\n%s", want, raw)
		}
	}
}

// TestUpdateReportsAConflictWithoutMovingThePin is the honest-failure path:
// both sides edit one line, the merge stops, MERGE_HEAD is left for recovery,
// the pin still names the old commit, and the report says how to get out.
func TestUpdateReportsAConflictWithoutMovingThePin(t *testing.T) {
	f := seedUpdateFixture(t)
	writeUpdateFile(t, filepath.Join(f.downstream, "shared.txt"), "downstream\n")
	commitUpdateAll(t, f.downstream, "downstream edits the shared line")
	writeUpdateFile(t, filepath.Join(f.upstream, "shared.txt"), "upstream\n")
	commitUpdateAll(t, f.upstream, "upstream edits the shared line")
	tip := headOf(t, f.upstream)

	code, _, stderr := runUpdateIn(t, f.downstream)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d (stderr: %s)", code, exitFailure, stderr)
	}
	for _, want := range []string{"shared.txt", "git merge --abort", "pin is unchanged"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr)
		}
	}
	// The recovery path is real: the merge is in progress.
	if _, err := func() (string, error) {
		cmd := exec.Command("git", "rev-parse", "--verify", "MERGE_HEAD")
		cmd.Dir = f.downstream
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}(); err != nil {
		t.Error("MERGE_HEAD is gone: a conflicted merge that was aborted for the operator has no recovery")
	}
	if got := pinOf(t, f.manifest); got != f.pin {
		t.Errorf("the pin moved to %s on a conflicted merge; want %s", got, f.pin)
	}
	_ = tip
}

// TestUpdateSkipsAnUnavailableLocalSource holds the sibling-clone rule: a
// relative local candidate with no checkout behind it is skipped with a note,
// and the next candidate is taken.
func TestUpdateSkipsAnUnavailableLocalSource(t *testing.T) {
	f := seedUpdateFixture(t)
	remotes := "  remotes:\n    - name: ghost\n      url: ../no-such-clone\n      kind: local\n    - name: tmpl-up\n      url: " + f.upstream + "\n      kind: local\n"
	writeUpdateFile(t, f.manifest, updateManifest(f.upstream, "master", f.pin, remotes))
	commitUpdateAll(t, f.downstream, "a ghost remote first")

	code, stdout, stderr := runUpdateIn(t, f.downstream)

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)\nstdout:\n%s", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "skipping unavailable local source ghost") {
		t.Errorf("the skip was not reported:\n%s", stdout)
	}
	if got := pinOf(t, f.manifest); got != f.tip {
		t.Errorf("the pin is %s, want the tip %s", got, f.tip)
	}
}

// TestUpdateRefusesDivergedSources holds the no-silent-choice rule: two
// candidates whose tips neither contain the other are refused by name, while
// a candidate that contains the other is simply the newer state of the same
// template and is taken.
func TestUpdateRefusesDivergedSources(t *testing.T) {
	t.Run("diverged tips are refused", func(t *testing.T) {
		updateGitEnv(t)
		root := t.TempDir()
		base := filepath.Join(root, "base")
		if err := os.MkdirAll(base, 0o755); err != nil {
			t.Fatal(err)
		}
		runUpdateGit(t, base, "init")
		writeUpdateFile(t, filepath.Join(base, "app.txt"), "v1\n")
		commitUpdateAll(t, base, "seed")
		runUpdateGit(t, base, "branch", "-M", "master")
		pin := runUpdateGit(t, base, "rev-parse", "HEAD")

		first := filepath.Join(root, "first")
		runUpdateGit(t, root, "clone", base, first)
		writeUpdateFile(t, filepath.Join(first, "app.txt"), "first\n")
		commitUpdateAll(t, first, "first direction")

		second := filepath.Join(root, "second")
		runUpdateGit(t, root, "clone", base, second)
		writeUpdateFile(t, filepath.Join(second, "app.txt"), "second\n")
		commitUpdateAll(t, second, "second direction")

		downstream := filepath.Join(root, "downstream")
		runUpdateGit(t, root, "clone", base, downstream)
		runUpdateGit(t, downstream, "checkout", "-b", "work")
		remotes := "  remotes:\n    - name: first\n      url: " + first + "\n      kind: local\n    - name: second\n      url: " + second + "\n      kind: local\n"
		manifest := filepath.Join(downstream, "template.yml")
		writeUpdateFile(t, manifest, updateManifest(base, "master", pin, remotes))
		commitUpdateAll(t, downstream, "downstream")
		before := headOf(t, downstream)

		code, _, stderr := runUpdateIn(t, downstream)

		if code != exitFailure {
			t.Errorf("exit code = %d, want %d (stderr: %s)", code, exitFailure, stderr)
		}
		for _, want := range []string{"disagree", "first", "second"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr missing %q:\n%s", want, stderr)
			}
		}
		if got := headOf(t, downstream); got != before {
			t.Error("a refused selection moved HEAD")
		}
	})

	t.Run("a tip containing the other is taken", func(t *testing.T) {
		updateGitEnv(t)
		root := t.TempDir()
		base := filepath.Join(root, "base")
		if err := os.MkdirAll(base, 0o755); err != nil {
			t.Fatal(err)
		}
		runUpdateGit(t, base, "init")
		writeUpdateFile(t, filepath.Join(base, "app.txt"), "v1\n")
		commitUpdateAll(t, base, "seed")
		runUpdateGit(t, base, "branch", "-M", "master")
		pin := runUpdateGit(t, base, "rev-parse", "HEAD")

		behind := filepath.Join(root, "behind")
		runUpdateGit(t, root, "clone", base, behind)
		ahead := filepath.Join(root, "ahead")
		runUpdateGit(t, root, "clone", base, ahead)
		writeUpdateFile(t, filepath.Join(ahead, "app.txt"), "v2\n")
		commitUpdateAll(t, ahead, "newer template work")
		newer := headOf(t, ahead)

		downstream := filepath.Join(root, "downstream")
		runUpdateGit(t, root, "clone", base, downstream)
		runUpdateGit(t, downstream, "checkout", "-b", "work")
		remotes := "  remotes:\n    - name: behind\n      url: " + behind + "\n      kind: local\n    - name: ahead\n      url: " + ahead + "\n      kind: local\n"
		manifest := filepath.Join(downstream, "template.yml")
		writeUpdateFile(t, manifest, updateManifest(base, "master", pin, remotes))
		commitUpdateAll(t, downstream, "downstream")

		code, _, stderr := runUpdateIn(t, downstream)

		if code != exitSuccess {
			t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr)
		}
		if got := pinOf(t, manifest); got != newer {
			t.Errorf("the pin is %s, want the newer tip %s", got, newer)
		}
	})
}

// TestUpdateRefusesAnUpstreamThatDoesNotDescendFromThePin closes the
// history-rewrite hole: a tip from another history — the wrong template, or a
// rebased one — is refused even though it fetched cleanly.
func TestUpdateRefusesAnUpstreamThatDoesNotDescendFromThePin(t *testing.T) {
	updateGitEnv(t)
	f := seedUpdateFixture(t)
	stranger := filepath.Join(f.root, "stranger")
	if err := os.MkdirAll(stranger, 0o755); err != nil {
		t.Fatal(err)
	}
	runUpdateGit(t, stranger, "init")
	runUpdateGit(t, stranger, "branch", "-M", "master")
	writeUpdateFile(t, filepath.Join(stranger, "app.txt"), "unrelated\n")
	commitUpdateAll(t, stranger, "another history entirely")

	remotes := "  remotes:\n    - name: stranger\n      url: " + stranger + "\n      kind: local\n"
	writeUpdateFile(t, f.manifest, updateManifest(stranger, "master", f.pin, remotes))
	commitUpdateAll(t, f.downstream, "point at a stranger")
	before := headOf(t, f.downstream)

	code, _, stderr := runUpdateIn(t, f.downstream)

	if code != exitFailure {
		t.Errorf("exit code = %d, want %d (stderr: %s)", code, exitFailure, stderr)
	}
	if !strings.Contains(stderr, "does not descend from the recorded pin") {
		t.Errorf("stderr does not name the ancestry refusal:\n%s", stderr)
	}
	if got := headOf(t, f.downstream); got != before {
		t.Error("a refused merge moved HEAD")
	}
	if got := pinOf(t, f.manifest); got != f.pin {
		t.Errorf("the pin moved to %s on a refused merge", got)
	}
}

// TestUpdateReportsAlreadyUpToDate is the no-op: the tip is already behind
// HEAD, so there is no merge commit and no pin movement to report.
func TestUpdateReportsAlreadyUpToDate(t *testing.T) {
	updateGitEnv(t)
	root := t.TempDir()
	upstream := filepath.Join(root, "upstream")
	if err := os.MkdirAll(upstream, 0o755); err != nil {
		t.Fatal(err)
	}
	runUpdateGit(t, upstream, "init")
	writeUpdateFile(t, filepath.Join(upstream, "app.txt"), "v1\n")
	commitUpdateAll(t, upstream, "seed")
	runUpdateGit(t, upstream, "branch", "-M", "master")
	pin := runUpdateGit(t, upstream, "rev-parse", "HEAD")

	downstream := filepath.Join(root, "downstream")
	runUpdateGit(t, root, "clone", upstream, downstream)
	runUpdateGit(t, downstream, "checkout", "-b", "work")
	manifest := filepath.Join(downstream, "template.yml")
	remotes := "  remotes:\n    - name: tmpl-up\n      url: " + upstream + "\n      kind: local\n"
	writeUpdateFile(t, manifest, updateManifest(upstream, "master", pin, remotes))
	commitUpdateAll(t, downstream, "downstream")
	before := headOf(t, downstream)

	code, stdout, stderr := runUpdateIn(t, downstream)

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stdout, "already up to date") {
		t.Errorf("stdout does not say so:\n%s", stdout)
	}
	if got := headOf(t, downstream); got != before {
		t.Error("a no-op moved HEAD")
	}
	if got := pinOf(t, manifest); got != pin {
		t.Errorf("a no-op moved the pin to %s", got)
	}
}

// TestUpdateTakesAtMostOneArgument is the arity guard: the one optional
// argument is the project directory, and anything more is a usage error.
func TestUpdateTakesAtMostOneArgument(t *testing.T) {
	c := lookupCommandOrFail(t, "update")

	if err := c.Execute(newTestEnv(), []string{"a", "b"}); !errors.Is(err, errUsage) {
		t.Errorf("Execute(a b) = %v, want a usage error", err)
	}
	if code, _, _ := runCLI(t, testVersion, "update", "a", "b"); code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
}

// TestUpdateNeedsATemplateFileInAGitCheckout covers the two preconditions
// before any git question is asked: a directory with no template.yml, and a
// template.yml that is not inside a checkout.
func TestUpdateNeedsATemplateFileInAGitCheckout(t *testing.T) {
	t.Run("no template.yml", func(t *testing.T) {
		code, _, stderr := runUpdateIn(t, t.TempDir())

		if code != exitFailure {
			t.Errorf("exit code = %d, want %d (stderr: %s)", code, exitFailure, stderr)
		}
		if !strings.Contains(stderr, "no template.yml") {
			t.Errorf("stderr does not name the missing file:\n%s", stderr)
		}
	})

	t.Run("not a git checkout", func(t *testing.T) {
		updateGitEnv(t)
		dir := t.TempDir()
		remotes := "  remotes:\n    - name: tmpl-up\n      url: ../parlor\n      kind: local\n"
		writeUpdateFile(t, filepath.Join(dir, "template.yml"),
			updateManifest("x", "master", "1a3cde1ec80c2faade6fefe9a6d5fff7465a127b", remotes))

		code, _, stderr := runUpdateIn(t, dir)

		if code != exitFailure {
			t.Errorf("exit code = %d, want %d (stderr: %s)", code, exitFailure, stderr)
		}
		if !strings.Contains(stderr, "not inside a git checkout") {
			t.Errorf("stderr does not name the missing checkout:\n%s", stderr)
		}
	})
}
