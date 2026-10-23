package cli

// What `cafe new` and `cafe init` share: the catalog call, the report, and the
// no-clobber rule behind both.
//
// This file exists for the reason confirm.go gives — the subtle part is not
// the flag surface but that a scaffold prints every file in full and refuses
// to overwrite without being told to. Two copies of that report is two places
// for the output to be wrong, and the second one is the one nobody reads. The
// commands keep their own flags, their own help and their own run functions,
// the way every subcommand here does.

import (
	"fmt"
	"io"

	"github.com/anywaye/cafe/internal/scaffold"
)

// printScaffolded prints every file in full, under a header naming the
// template it came from.
//
// In full, not a summary, for the same reason `cafe dev` prints the compose
// document and `cafe gen` prints what it generated: a scaffold nobody can
// inspect is a scaffold nobody can debug, and a document that only lives
// inside a tool is a document nobody can diff. A path and a byte count would
// tell a reader that five files exist and nothing about what is in them.
func printScaffolded(w io.Writer, verb string, out scaffold.Output) {
	_, _ = fmt.Fprintf(w, "cafe %s wrote %d file(s) from template %s:\n", verb, len(out.Files), out.Template)
	for _, file := range out.Files {
		_, _ = fmt.Fprintf(w, "\n===== %s =====\n%s", file.Path, file.Content)
	}
}
