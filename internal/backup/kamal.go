package backup

import (
	"context"
	"io"

	"github.com/cafaye/caf/internal/deploy"
)

// KamalRunner is the Kamal engine: it owns the Kamal plan and runs it through
// internal/deploy's KamalRunner, which stays the only place caf knows how to
// execute kamal — including the "kamal is not installed" sentence, which both
// `caf deploy` and `caf backup` must say the same way.
type KamalRunner struct {
	delegate *deploy.KamalRunner
}

// NewKamalRunner is the wiring the router uses.
func NewKamalRunner() *KamalRunner {
	return &KamalRunner{delegate: deploy.NewKamalRunner()}
}

// Plan is the Kamal plan, built by PlanFor — the pure function whose tests are
// the proof of what a Kamal backup is. The engine owns it; the caller asks.
func (k *KamalRunner) Plan(cover Cover, cfg Config, opts Options) (Plan, error) {
	return PlanFor(cover, cfg, opts)
}

// Run executes one step of the plan in dir.
func (k *KamalRunner) Run(ctx context.Context, dir string, step Step, stdout, stderr io.Writer) error {
	return k.delegate.Run(ctx, dir, step.Args(), stdout, stderr)
}
