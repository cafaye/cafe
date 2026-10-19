package version

import (
	"fmt"
	"strings"
)

// Current is the CLI version. Set via ldflags at build time:
// -X github.com/cafaye/cafe/internal/version.Current=1.2.3
// Left empty, it defaults to "dev".
var Current = "dev"

// ManagedBy is the marker in the header of a file cafe writes. A file that
// carries it is cafe's to overwrite; a file that does not is the person's, and
// overwriting it destroys work the CLI cannot see.
const ManagedBy = "managed-by: cafe-cli"

// SkillHeader is the header the installed agent skill carries: the tool that
// manages the file, and the version that wrote it.
//
// It lives here and not in internal/skills because this is the only place that
// knows what a stamped header looks like, and two places that know is two
// answers to "is this file ours to replace".
func SkillHeader() string {
	return fmt.Sprintf("<!-- %s | cli_version: %s -->\n\n", ManagedBy, Current)
}

// IsManaged reports whether content carries cafe's management header.
//
// The marker has to be in the FIRST line: a file that merely mentions
// "managed-by: cafe-cli" somewhere in its body is not a file cafe wrote, and
// treating it as one would overwrite a person's file on the strength of a
// sentence inside it.
func IsManaged(content []byte) bool {
	first, _, _ := strings.Cut(string(content), "\n")
	return strings.Contains(first, ManagedBy)
}
