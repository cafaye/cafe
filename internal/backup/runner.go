package backup

import (
	"context"
	"io"
)

// Runner is the backup engine for one service: it owns which commands run, in
// what order, and what each is for.
//
// # Why the plan is the engine's and not the caller's
//
// The caller builds argv today, and that is the defect this seam removes. A
// `kubectl cnpg` runner handed `["kamal", "backup", …]` is nonsense, and a
// runner that reinterprets Kamal argv is a second engine that can disagree with
// the first about what a backup is — the disagreement would be found in a
// restore nobody rehearsed. So the engine owns the plan: the caller asks for it,
// prints it for a dry run, and executes it step by step, but never builds argv
// itself.
//
// # What the caller keeps
//
// The parts either side of the engine, which internal/deploy/doc.go says are
// caf's job: preflight, the refusal, the dry run, the confirmation and the
// after-the-fact report.
//
// The interface is declared HERE and satisfied by the engines in this package
// (kamal.go's KamalRunner today), wired in internal/cli. This package imports
// neither deploy nor cli, and that is deliberate: a package that imports another
// to reach an interface has an import edge where none is needed, and the two
// commands make different claims about different engines.
type Runner interface {
	// Plan returns the engine's plan for one service: which commands run, in
	// what order, and what each is for.
	//
	// It is pure: it reads no file, opens no socket and runs no command, which
	// is what lets a test assert the exact argv of a dry run with no container
	// runtime, no restic repository and no cluster anywhere in sight.
	Plan(cover Cover, cfg Config, opts Options) (Plan, error)
	// Run executes one step of the plan in dir, with the process's output
	// attached to the two writers so a person watching a terminal sees the
	// cycle happen.
	Run(ctx context.Context, dir string, step Step, stdout, stderr io.Writer) error
}
