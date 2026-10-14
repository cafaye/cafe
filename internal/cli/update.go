package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"
)

// `cafe update` merges template work into a checkout that was seeded from it,
// the way Site's README "Pulling template updates" does by hand, and moves
// the pin in template.yml when the merge commits.
//
// The file it reads is Site's template.yml: version, template.name, the
// canonical template.repository, the tracked template.branch, the immutable
// template.commit pin, and the ordered candidate template.remotes. The
// added/diverged/removed lists are carried in the parse and then ignored on
// purpose: they are a hint for the reader about where collisions will be,
// and git decides the merge. A generator that second-guessed git with a path
// list would be a merge strategy wearing a hint's name.
//
// What it refuses, in order: a pin that is not a full sha (a branch name is a
// different answer every time it is asked), a merge on master or main, a dirty
// worktree, candidate sources that disagree without one containing the other,
// and a tip that does not descend from the recorded pin. It never passes -X
// ours/-X theirs, never overwrites the tree, never pushes, and never opens a
// PR. The pin moves in the same commit as the merge, and only after the merge
// commits: a conflicted merge leaves MERGE_HEAD in place and the pin where it
// was, with the recovery spelled out.

// updateOptions is the parsed flag state for `cafe update`.
type updateOptions struct {
	check bool
}

// updateLongHelp is the prose under the usage line: the procedure, the
// guards, and the recovery, where a person reads them before running a
// command that rewrites their checkout.
const updateLongHelp = `Reads template.yml — the current directory by default — and merges the
template branch it pins into this checkout, the way "Pulling template
updates" does by hand:

  cafe update [path]          merge the selected upstream and move the pin
  cafe update --check [path]  preview only: the source, the tip and what it
                             carries. Changes no tracked file and no branch.

Safety, in order: no merge on master or main (--check may still preview),
never with a dirty worktree,
never with -X ours/-X theirs and never by overwriting the tree — a real
three-way merge, so both histories stay reviewable. A relative local
candidate that is absent is skipped; an absolute local one and a hosted
one are not, because those were deliberate. Two sources that disagree
without one containing the other are refused rather than chosen between.
The tip must descend from the recorded pin, and the pin moves — in the
same commit as the merge — only after the merge commits. A conflict
leaves the merge in progress and the pin where it was: resolve it,
commit it, move the commit: line to the tip in a follow-up commit, or
git merge --abort to return with nothing changed.

The added/diverged/removed lists in template.yml are hints for the
reader; git decides the merge. Nothing is pushed and no PR is opened.
`

func newUpdateCommand() *Command {
	opts := &updateOptions{}
	c := &Command{
		Name:     "update",
		Summary:  "merge the template's updates into this checkout and advance the template pin",
		Usage:    "cafe update [flags] [path]",
		LongHelp: updateLongHelp,
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&opts.check, "check", false, "preview the merge without changing the worktree or the branch")
		},
	}
	c.Run = func(args []string, env *Env) error {
		dir := "."
		switch len(args) {
		case 0:
		case 1:
			dir = args[0]
		default:
			return fmt.Errorf("%w: cafe %s wants at most 1 argument, a project directory, got %d (usage: %s)",
				errUsage, c.Name, len(args), c.Usage)
		}
		return runUpdate(opts, dir, env)
	}
	return c
}

// updateRemote is one candidate source from template.yml's remotes list,
// tried in the order it is written.
type updateRemote struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
	Kind string `yaml:"kind"`
}

// updateTemplate is the template: block of template.yml. Added, Diverged and
// Removed are parsed so a full Site file validates, and then deliberately
// unused: see the package note above.
type updateTemplate struct {
	Name       string         `yaml:"name"`
	Repository string         `yaml:"repository"`
	Branch     string         `yaml:"branch"`
	Commit     string         `yaml:"commit"`
	Remotes    []updateRemote `yaml:"remotes"`
	Added      []string       `yaml:"added"`
	Diverged   []string       `yaml:"diverged"`
	Removed    []string       `yaml:"removed"`
}

// updateFile is template.yml.
type updateFile struct {
	Version  int            `yaml:"version"`
	Template updateTemplate `yaml:"template"`
}

// fullSHA is what makes a pin a pin: one immutable commit, never a branch and
// never a prefix. Short shas are refused rather than expanded, because an
// expansion is a lookup and a pin is not a query.
var fullSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// loadUpdateFile reads and validates template.yml. It returns the parsed file
// and its raw bytes; the bytes are what the pin advance rewrites, so the
// comments survive a merge that nothing else in this command understands.
func loadUpdateFile(path string) (updateFile, []byte, error) {
	var file updateFile
	raw, err := os.ReadFile(path)
	if err != nil {
		return file, nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return file, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if file.Version != 1 {
		return file, nil, fmt.Errorf("%s: unsupported version %d, want 1: refusing to misread a newer file", path, file.Version)
	}
	t := file.Template
	if t.Name == "" {
		return file, nil, fmt.Errorf("%s: template.name is empty: the file does not say which template this checkout follows", path)
	}
	if t.Repository == "" {
		return file, nil, fmt.Errorf("%s: template.repository is empty: the file names no canonical upstream", path)
	}
	if t.Branch == "" {
		return file, nil, fmt.Errorf("%s: template.branch is empty: the file does not say which branch to merge", path)
	}
	if !fullSHA.MatchString(t.Commit) {
		return file, nil, fmt.Errorf("%s: template.commit %q is not a full 40-character sha: a pin that names a branch or a prefix is a different answer every time it is asked", path, t.Commit)
	}
	if len(t.Remotes) == 0 {
		return file, nil, fmt.Errorf("%s: template.remotes names no candidate source: there is nothing to update from", path)
	}
	for _, r := range t.Remotes {
		if r.Name == "" || r.URL == "" {
			return file, nil, fmt.Errorf("%s: a remote names no name or no url: every candidate must be identifiable", path)
		}
		if r.Kind != "" && r.Kind != "local" && r.Kind != "hosted" {
			return file, nil, fmt.Errorf("%s: remote %q has kind %q, want local or hosted", path, r.Name, r.Kind)
		}
	}
	return file, raw, nil
}

// commitLine matches the `commit:` line whose value the pin advance rewrites.
// The replacement is by byte offset of the first match, so every comment,
// blank line and ordering in the file survives the merge that carries it.
var commitLine = regexp.MustCompile(`(?m)^([ \t]*commit:[ \t]*)[^\s#]+([ \t]*(?:#.*)?)$`)

// advancePin rewrites the commit: line to tip and returns the new bytes. It
// refuses a file with no such line rather than appending one, because a pin
// that was deleted is a fact worth noticing rather than a default to restore.
func advancePin(raw []byte, tip string) ([]byte, error) {
	loc := commitLine.FindSubmatchIndex(raw)
	if loc == nil {
		return nil, fmt.Errorf("template.yml has no commit: line to advance: refusing to invent one")
	}
	// Group 1 is the `commit:` prefix, group 2 the trailing comment: the
	// value is the bytes between them, and only those bytes move.
	out := make([]byte, 0, len(raw)+len(tip))
	out = append(out, raw[:loc[3]]...)
	out = append(out, tip...)
	out = append(out, raw[loc[4]:]...)
	return out, nil
}

// updateGit runs git in dir and returns its stdout. A failure carries the
// combined output, because git's reason is usually the whole diagnosis and a
// bare "exit status 1" sends nobody anywhere.
func updateGit(dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// gitOK runs git in dir and reports only whether it succeeded. Merge-base
// answers "no" with exit 1, which is an answer and not a failure, so its
// callers need this rather than updateGit.
func gitOK(dir string, args ...string) bool {
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	return cmd.Run() == nil
}

// updateSource is one reachable candidate: its remote entry, the absolute
// source it resolved to, and the tip of the tracked branch there.
type updateSource struct {
	remote updateRemote
	source string
	tip    string
}

// resolveUpdateSource turns a remote's url into something fetchable. A
// relative url is resolved from the manifest's repository root — the sibling
// checkout Site documents — and is the one case that may be skipped when
// absent. Absolute paths and remote-looking urls are used as written.
func resolveUpdateSource(root string, r updateRemote) (source string, relative bool) {
	if filepath.IsAbs(r.URL) || looksRemote(r.URL) {
		return r.URL, false
	}
	return filepath.Join(root, r.URL), true
}

// looksRemote is whether url names a network source rather than a path: a
// scheme, or the scp-like host:path form with the colon before any slash.
func looksRemote(url string) bool {
	if strings.Contains(url, "://") {
		return true
	}
	colon, slash := strings.Index(url, ":"), strings.Index(url, "/")
	return colon >= 0 && (slash < 0 || colon < slash)
}

// effectiveKind is local for a relative path and hosted otherwise, unless the
// file says. The default matters because Site's parlor-local entry is how a
// same-machine clone is recognized as skippable rather than canonical.
func effectiveKind(r updateRemote, relative bool) string {
	if r.Kind != "" {
		return r.Kind
	}
	if relative {
		return "local"
	}
	return "hosted"
}

// updateRef is where a candidate's branch is fetched to. One namespace, one
// ref per remote name, so two runs cannot mistake each other's tips and a
// reader can see what was merged.
func updateRef(name string) string {
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
			return r
		}
		return '_'
	}, name)
	return "refs/remotes/caf-update/" + safe
}

// shortSHA is seven characters for a human, and it is only ever display: the
// pin, the merge and every check use the full sha.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// runUpdate is the whole command: read the file, establish the guards, fetch
// the candidates, select one, and either report (--check) or merge.
func runUpdate(opts *updateOptions, dir string, env *Env) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("cafe update: git is not on PATH: the merge is git's and there is no other one")
	}
	manifest, root, err := findUpdateManifest(dir)
	if err != nil {
		return err
	}
	file, raw, err := loadUpdateFile(manifest)
	if err != nil {
		return fmt.Errorf("cafe update: %w", err)
	}
	workRoot, err := updateGit(root, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("cafe update: %s is not inside a git checkout: %w", root, err)
	}
	if inside, err := updateGit(workRoot, "rev-parse", "--is-inside-work-tree"); err != nil || inside != "true" {
		return fmt.Errorf("cafe update: %s is not a git worktree: the merge needs files to work on", workRoot)
	}
	branch, err := updateGit(workRoot, "branch", "--show-current")
	if err != nil || branch == "" {
		return fmt.Errorf("cafe update: this checkout is not on a branch: `git switch -c template/update-<date>` first, then re-run")
	}
	// A preview changes no tracked file and no branch — the fetch
	// selectUpdateSource does touches remote-tracking refs only — so it is
	// allowed where a merge is refused.
	if (branch == "master" || branch == "main") && !opts.check {
		return fmt.Errorf("cafe update: refusing to merge a template update on %s: updates land on a branch, the way \"Pulling template updates\" says — `git switch -c template/update-<date>` first. --check may still preview", branch)
	}
	pin := file.Template.Commit
	if !gitOK(workRoot, "merge-base", "--is-ancestor", pin, "HEAD") {
		return fmt.Errorf("cafe update: the recorded pin %s is not an ancestor of HEAD: this checkout was not built on the commit it claims, so no tip descending from that pin is a safe merge base", pin)
	}

	selected, skipped, err := selectUpdateSource(workRoot, file)
	if err != nil {
		return err
	}
	for _, note := range skipped {
		_, _ = fmt.Fprintf(env.Stdout, "%s\n", note)
	}
	// The selected tip is an ancestor of HEAD already: there is nothing to
	// take, and in particular nothing to move the pin to.
	if gitOK(workRoot, "merge-base", "--is-ancestor", selected.tip, "HEAD") {
		_, _ = fmt.Fprintf(env.Stdout, "already up to date: %s from %s is an ancestor of HEAD; the pin stays %s\n", selected.tip, selected.remote.Name, pin)
		return nil
	}

	if opts.check {
		return previewUpdate(workRoot, file, branch, selected, env)
	}

	if status, err := updateGit(workRoot, "status", "--porcelain"); err != nil {
		return fmt.Errorf("cafe update: %w", err)
	} else if status != "" {
		lines := strings.Split(status, "\n")
		if len(lines) > 5 {
			lines = append(lines[:5], "...")
		}
		return fmt.Errorf("cafe update: refusing to merge with a dirty worktree (%d path(s)):\n%s\ncommit or stash first; --check previews without touching the tree", len(strings.Split(status, "\n")), strings.Join(lines, "\n"))
	}

	return mergeUpdate(workRoot, manifest, file, raw, branch, selected, env)
}

// findUpdateManifest resolves dir to a template.yml and the git root around
// it. A file may be named directly; a directory means its template.yml.
func findUpdateManifest(dir string) (manifest, root string, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", fmt.Errorf("cafe update: resolve %s: %w", dir, err)
	}
	info, statErr := os.Stat(abs)
	if statErr == nil && !info.IsDir() {
		return abs, filepath.Dir(abs), nil
	}
	manifest = filepath.Join(abs, "template.yml")
	if _, statErr := os.Stat(manifest); statErr != nil {
		return "", "", fmt.Errorf("cafe update: no template.yml in %s: %w", abs, statErr)
	}
	return manifest, abs, nil
}

// selectUpdateSource fetches every candidate's tracked branch and picks one.
// A relative local source that is absent is skipped with a note; anything
// else that cannot be read is an error. Tips that are all one commit agree;
// tips where one contains every other select that one, with the superseded
// named; anything else is a divergence this command will not vote on.
func selectUpdateSource(workRoot string, file updateFile) (selected updateSource, skipped []string, err error) {
	t := file.Template
	type fetched struct {
		remote updateRemote
		source string
		tip    string
	}
	var have []fetched
	for _, r := range t.Remotes {
		source, relative := resolveUpdateSource(workRoot, r)
		kind := effectiveKind(r, relative)
		if kind == "local" && relative {
			if _, statErr := os.Stat(source); statErr != nil {
				skipped = append(skipped, fmt.Sprintf("skipping unavailable local source %s (%s): no checkout there; taking the next candidate", r.Name, r.URL))
				continue
			}
		}
		if kind == "local" {
			if _, revErr := updateGit(source, "rev-parse", "--verify", t.Branch); revErr != nil {
				return selected, skipped, fmt.Errorf("cafe update: local source %s (%s) has no branch %q: %w", r.Name, r.URL, t.Branch, revErr)
			}
		}
		ref := updateRef(r.Name)
		if _, fetchErr := updateGit(workRoot, "fetch", source, "+"+t.Branch+":"+ref); fetchErr != nil {
			if kind == "local" && relative {
				skipped = append(skipped, fmt.Sprintf("skipping unavailable local source %s (%s): the fetch failed; taking the next candidate", r.Name, r.URL))
				continue
			}
			return selected, skipped, fmt.Errorf("cafe update: fetch %s (%s) branch %q: %w", r.Name, r.URL, t.Branch, fetchErr)
		}
		tip, revErr := updateGit(workRoot, "rev-parse", "--verify", ref)
		if revErr != nil {
			return selected, skipped, fmt.Errorf("cafe update: fetch %s (%s) left no %s: %w", r.Name, r.URL, ref, revErr)
		}
		have = append(have, fetched{remote: r, source: source, tip: tip})
	}
	if len(have) == 0 {
		return selected, skipped, fmt.Errorf("cafe update: no usable candidate source: every remote was skipped or failed, so there is nothing to merge")
	}
	best := have[0]
	agree := true
	for _, f := range have[1:] {
		if f.tip != best.tip {
			agree = false
			break
		}
	}
	if agree {
		return updateSource(best), skipped, nil
	}
	// One tip containing every other is not a disagreement: it is the newer
	// state of the same template, the case parlor-local exists for.
	for _, f := range have {
		contains := true
		for _, g := range have {
			if f.tip != g.tip && !gitOK(workRoot, "merge-base", "--is-ancestor", g.tip, f.tip) {
				contains = false
				break
			}
		}
		if contains {
			return updateSource(f), skipped, nil
		}
	}
	var parts []string
	for _, f := range have {
		parts = append(parts, fmt.Sprintf("%s=%s", f.remote.Name, shortSHA(f.tip)))
	}
	return selected, skipped, fmt.Errorf("cafe update: candidate sources disagree and neither contains the other: %s. refusing to choose: merge one into the other, or drop it from template.yml remotes, and re-run", strings.Join(parts, " "))
}

// previewUpdate reports what a merge would take, changing no tracked file and
// no branch. The fetch selectUpdateSource already did is the only mutation,
// and it touches remote-tracking refs rather than the worktree.
func previewUpdate(workRoot string, file updateFile, branch string, selected updateSource, env *Env) error {
	t := file.Template
	count, err := updateGit(workRoot, "rev-list", "--count", "HEAD.."+selected.tip)
	if err != nil {
		return fmt.Errorf("cafe update: %w", err)
	}
	_, _ = fmt.Fprintf(env.Stdout, "template pin %s (tracking %s#%s)\n", t.Commit, t.Repository, t.Branch)
	_, _ = fmt.Fprintf(env.Stdout, "branch %s in %s\n", branch, workRoot)
	_, _ = fmt.Fprintf(env.Stdout, "selected source %s (%s): %s\n", selected.remote.Name, selected.remote.URL, selected.tip)
	_, _ = fmt.Fprintf(env.Stdout, "%s is %s commit(s) ahead of HEAD\n", shortSHA(selected.tip), count)
	if log, err := updateGit(workRoot, "log", "--oneline", "-30", "HEAD.."+selected.tip); err == nil && log != "" {
		_, _ = fmt.Fprintf(env.Stdout, "what it carries:\n%s\n", log)
	}
	if stat, err := updateGit(workRoot, "diff", "--shortstat", t.Commit+".."+selected.tip); err == nil && stat != "" {
		_, _ = fmt.Fprintf(env.Stdout, "since the pin: %s\n", stat)
	}
	_, _ = fmt.Fprintf(env.Stdout, "the added/diverged/removed lists in template.yml are hints; git decides the merge. re-run without --check to merge.\n")
	return nil
}

// mergeUpdate runs the real three-way merge and, only after it commits,
// advances the pin in the same commit. No -X flag anywhere: a global
// ours/theirs choice is how customizations are silently thrown away.
func mergeUpdate(workRoot, manifest string, file updateFile, raw []byte, branch string, selected updateSource, env *Env) error {
	t := file.Template
	if !gitOK(workRoot, "merge-base", "--is-ancestor", t.Commit, selected.tip) {
		return fmt.Errorf("cafe update: selected %s from %s does not descend from the recorded pin %s: the pin names work this checkout has not taken, or the source is not this template. refusing", shortSHA(selected.tip), selected.remote.Name, t.Commit)
	}
	_, _ = fmt.Fprintf(env.Stdout, "merging %s from %s (%s) into %s\n", selected.tip, selected.remote.Name, selected.remote.URL, branch)
	if _, err := updateGit(workRoot, "merge", "--no-ff", "--no-edit", selected.tip); err != nil {
		return reportConflict(workRoot, manifest, selected, err)
	}
	merged, err := os.ReadFile(manifest)
	if err != nil {
		head, _ := updateGit(workRoot, "rev-parse", "--short", "HEAD")
		return fmt.Errorf("cafe update: the merge committed as %s but %s cannot be read: %w. move the commit: line to %s by hand, stage it and `git commit --amend --no-edit`", head, manifest, err, selected.tip)
	}
	advanced, err := advancePin(merged, selected.tip)
	if err != nil {
		head, _ := updateGit(workRoot, "rev-parse", "--short", "HEAD")
		return fmt.Errorf("cafe update: the merge committed as %s but %w. move the commit: line in %s to %s by hand, stage it and `git commit --amend --no-edit`", head, err, manifest, selected.tip)
	}
	if err := os.WriteFile(manifest, advanced, 0o644); err != nil {
		return fmt.Errorf("cafe update: write %s: %w. the merge is committed; move the commit: line to %s by hand and amend", manifest, err, selected.tip)
	}
	if _, err := updateGit(workRoot, "add", "--", manifest); err != nil {
		return fmt.Errorf("cafe update: %w. the merge is committed; stage %s and `git commit --amend --no-edit` by hand", err, manifest)
	}
	if _, err := updateGit(workRoot, "commit", "--amend", "--no-edit"); err != nil {
		return fmt.Errorf("cafe update: %w. the merge is committed with a stale pin; move the commit: line to %s, stage it and `git commit --amend --no-edit` by hand", err, selected.tip)
	}
	head, _ := updateGit(workRoot, "rev-parse", "HEAD")
	_, _ = fmt.Fprintf(env.Stdout, "merged %s; the pin moved %s -> %s in the merge commit %s\n", selected.tip, t.Commit, selected.tip, shortSHA(head))
	return nil
}

// reportConflict leaves the merge in progress and the pin where it was. The
// worktree git left behind IS the recovery path — abort it or finish it — so
// this reports the state rather than cleaning it up, and touches nothing. A
// merge that failed before it started (no MERGE_HEAD) is reported as exactly
// that, because "a merge is in progress" would then be a lie.
func reportConflict(workRoot, manifest string, selected updateSource, mergeErr error) error {
	if !gitOK(workRoot, "rev-parse", "--verify", "MERGE_HEAD") {
		return fmt.Errorf("cafe update: the merge of %s from %s failed before it started: %w. the tree is unchanged and the pin is still what it was", shortSHA(selected.tip), selected.remote.Name, mergeErr)
	}
	unmerged, _ := updateGit(workRoot, "diff", "--name-only", "--diff-filter=U")
	files := strings.Fields(unmerged)
	head, _ := updateGit(workRoot, "rev-parse", "--short", "HEAD")
	var sb strings.Builder
	_, _ = fmt.Fprintf(&sb, "cafe update: the merge of %s from %s stopped on %d conflicted file(s)", shortSHA(selected.tip), selected.remote.Name, len(files))
	if len(files) > 0 {
		_, _ = fmt.Fprintf(&sb, ": %s", strings.Join(files, ", "))
	}
	_, _ = fmt.Fprintf(&sb, "\nthe merge is still in progress and the pin is unchanged: resolve the conflicts, `git commit` the merge, then move the commit: line in %s to %s in a follow-up commit — or `git merge --abort` to return to %s with nothing changed", manifest, selected.tip, head)
	return errors.New(sb.String())
}
