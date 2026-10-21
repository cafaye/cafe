package cli

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// semverTag is the shape of a tag the derivation accepts: an optional leading
// `v` and three numeric parts.
var semverTag = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

// latestSemverTag derives the tag from the repository's own tags: the highest
// semver tag git knows, verbatim.
func latestSemverTag(dir string) (string, error) {
	return latestSemverTagIn(dir, "--tag")
}

// latestSemverTagIn is the derivation with the flag that overrides it named
// by the caller.
func latestSemverTagIn(dir, flag string) (string, error) {
	out, err := gitOutput(dir, "tag", "--list", "--sort=-v:refname")
	if err != nil {
		return "", fmt.Errorf("read the git tags in %s: %w\n"+
			"  Pass %s with the version to ship — cafe does not create tags", dir, err, flag)
	}
	for _, line := range strings.Split(out, "\n") {
		tag := strings.TrimSpace(line)
		if semverTag.MatchString(tag) {
			return tag, nil
		}
	}
	return "", fmt.Errorf("no semver tag in %s — pass %s with the version to ship\n"+
		"  The tag is stored verbatim, leading `v` included: `v1.2.3` and `1.2.3` are two real tags", dir, flag)
}

// gitOutput runs one git command in a directory and returns its stdout,
// trimmed.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
