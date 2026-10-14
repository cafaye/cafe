package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cafaye/cafe/internal/gate"
	"github.com/cafaye/cafe/internal/lock"
)

// `cafe gate floors` regenerates the countable floors in `gate.yml` from the
// tree instead of leaving them for a human to retype.
//
// WHY THIS COMMAND EXISTS. The floors are a function of the tree — how many
// top-level tests it declares, how many subtests it runs — and a function a
// human retypes on every test-adding change goes stale. It did, twice in one day:
// two packets each raised the number, a rebase resolved the conflict
// mechanically, and the resolution left the floor one short of the merged tree.
// The guard (a floor below the tree's counts is red) is wanted; the hand
// maintenance is not. This is the writer, and `internal/gate` holds the argument
// in full.
//
// WHY IT IS A SUBCOMMAND OF `gate` AND NOT A SCRIPT. It has to read gate.yml's
// bytes, derive three numbers, write them back without touching a comment, and
// re-pin `caf.lock` over the new bytes — the same shape `cafe lock` already has,
// and the same reason: a second implementation of "the lock is what this tree
// hashes to" is a second answer to one question.
//
// It runs the real gate (`bin/prime`) rather than `go test` directly, so the
// numbers are the gate's own summary line — the line `gate.yml`'s proofs match
// and `internal/ci` pins — and not a second counter that could disagree with it.

// gateDeps is the seam a test drives the command through: the measurement is the
// one thing that would otherwise run the whole suite, and a test that did would
// recurse, because the test is part of the suite being measured.
type gateDeps struct {
	measure func(root string) (gate.Measured, error)
}

func (d gateDeps) withDefaults() gateDeps {
	if d.measure == nil {
		d.measure = gate.Measure
	}
	return d
}

func newGateCommand(deps gateDeps) *Command {
	deps = deps.withDefaults()
	floors := newGateFloorsCommand(deps)
	c := &Command{
		Name:     "gate",
		Summary:  "derive cafe's own gate floors from the tree instead of hand-editing them",
		Usage:    "cafe gate <floors> [flags] [path]",
		LongHelp: gateLongHelp,
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) == 0 {
			printHelp(env.Stdout, c)
			return nil
		}
		if args[0] != "floors" {
			return fmt.Errorf("%w: unknown gate subcommand %q (usage: cafe gate <floors>)", errUsage, args[0])
		}
		return floors.Execute(env, args[1:])
	}
	return c
}

const gateLongHelp = `Commands:
  floors   print the floors the tree implies, and with --write set them and re-pin caf.lock

The floors in gate.yml are a function of the tree, and a function a human
retypes goes stale. ` + "`cafe gate floors`" + ` runs the gate, derives the three countable
floors from what it reported, and prints the DELTA — "floors moved 1370→1375,
subtests 1248→1250" — so a floor that moves is visible without opening the file.

With ` + "`--write`" + ` it sets them in gate.yml, preserving every comment, and re-pins
` + "`caf.lock`" + ` over the new bytes in the same run. Running it twice on an unchanged
tree changes nothing, which is what makes it safe to run at every merge.`

// gateFloorsOptions is the parsed flag state.
type gateFloorsOptions struct {
	write bool
}

func newGateFloorsCommand(deps gateDeps) *Command {
	opts := &gateFloorsOptions{}
	c := &Command{
		Name:    "gate floors",
		Summary: "print the floors the tree implies, and with --write set them and re-pin caf.lock",
		Usage:   "cafe gate floors [--write] [path]",
		LongHelp: `Reads the three countable floors out of gate.yml, runs ` + "`bin/prime`" + `, derives the
floors the tree implies, and prints the delta. The floors are the number of
tests that RAN, not the number that passed, so the writer is safe to run while
the gate is red for exactly the stale floor it is about to fix.

` + "`--write`" + ` sets them in gate.yml and re-pins caf.lock. The path defaults to the
current directory; pass the repository root when running from elsewhere.`,
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&opts.write, "write", false,
				"set the derived floors in gate.yml and re-pin caf.lock (default: print the delta only)")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) > 1 {
			return fmt.Errorf("%w: cafe gate floors wants at most 1 argument (usage: %s)", errUsage, c.Usage)
		}
		root := "."
		if len(args) == 1 {
			root = args[0]
		}
		return runGateFloors(root, opts.write, deps, env)
	}
	return c
}

// runGateFloors is the whole command: read, measure, derive, and — only with
// --write — set and re-pin.
func runGateFloors(root string, write bool, deps gateDeps, env *Env) error {
	path := filepath.Join(root, "gate.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cafe gate floors: %w", err)
	}
	before, err := gate.ParseFloors(raw)
	if err != nil {
		return fmt.Errorf("cafe gate floors: %w", err)
	}

	measured, err := deps.measure(root)
	if err != nil {
		return fmt.Errorf("cafe gate floors: %w", err)
	}
	want := measured.Floors()
	delta := gate.Delta{Before: before, After: want}

	_, _ = fmt.Fprintf(env.Stdout, "cafe gate floors: the gate reported %d packages, %d top-level passes, %d subtest passes, %d failures, %d skips\n",
		measured.Packages, measured.TopLevel, measured.Subtests, measured.Failures, measured.Skips)
	_, _ = fmt.Fprintf(env.Stdout, "cafe gate floors: %s\n", delta)

	if !write {
		_, _ = fmt.Fprint(env.Stdout, "cafe gate floors: dry run; pass --write to set the floors and re-pin caf.lock\n")
		return nil
	}

	out, changed, err := gate.RewriteFloors(raw, want)
	if err != nil {
		return fmt.Errorf("cafe gate floors: %w", err)
	}
	if changed {
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return fmt.Errorf("cafe gate floors: write %s: %w", path, err)
		}
	}

	// Re-pin the lock in the same run, over the bytes just written. `cafe lock`
	// reads gate.yml as this tree's one rule-bundle, so a floor that moved and a
	// lock that still names the old hash is the half-landed bump this whole
	// packet is about.
	built, err := lock.Build(root, toolVersion(env))
	if err != nil {
		return fmt.Errorf("cafe gate floors: re-pin %s: %w", lock.FileName, err)
	}
	if _, err := lock.Write(root, built); err != nil {
		return fmt.Errorf("cafe gate floors: re-pin %s: %w", lock.FileName, err)
	}
	_, _ = fmt.Fprintf(env.Stdout, "cafe gate floors: re-pinned %s\n", lock.FileName)
	return nil
}
