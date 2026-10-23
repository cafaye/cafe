package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anywaye/cafe/internal/edge"
)

// ---------------------------------------------------------------------------
// what this file is
// ---------------------------------------------------------------------------
//
// The three verbs under `cafe box` that make the box's edge a product rather
// than a mechanism: `edge` reads what the box serves, `publish` deploys a
// release and flips `current` at it, `rollback` flips it back.
//
// THE WORDS ARE THE PLAN'S OWN. anywaye/notes/reference/names-urls-rollout-plan.md §4 lists what
// box ships for its phase — boxd's embedded edge, host-based routing, automatic
// TLS, release directories with an atomic symlink switch — and names no
// ingress, no chart and no namespace. Neither does this file. A customer who
// has to learn a Kubernetes word to deploy their app has been failed by the
// product, which is why the verbs are publish and rollback and not `apply` and
// `revert`.
//
// NOTHING HERE TALKS TO A CONTROL PLANE. The three verbs read and write the
// box's own filesystem: the release directories, the `current` symlink, and the
// edge's routing record. That is what makes a rollback work on the day a
// deploy is broken and the network is the problem — and it is asserted in the
// tests by injecting a state directory and nothing else.

// boxEdgeStatusCommand is the read-only half: what the edge is serving, over
// which hostnames, and what it last observed about each one.
//
// It reads the record off the box and nothing else — no daemon, no control
// plane, no capability — because "what is serving" is a fact about this
// filesystem and a command that had to ask a server for it could not answer on
// a box whose network is gone.
func boxEdgeStatusCommand(deps boxDeps) *Command {
	opts := &boxEdgeOptions{}
	c := &Command{
		Name:    "box edge",
		Summary: "what this box's edge serves: hostnames, upstreams, health and the release serving",
		Usage:   "cafe box edge [--json] [--box <id>]",
		Flags:   func(fs *flag.FlagSet) { opts.declare(fs) },
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		dir := edgeStateDir(deps)
		spec, err := edge.Load(dir)
		if err != nil {
			return fmt.Errorf("cafe box edge: %w", err)
		}
		current, err := edge.Current(dir)
		if err != nil {
			return fmt.Errorf("cafe box edge: %w", err)
		}
		// The symlink is the truth and the spec is the record, and the verb
		// reports both so a reader can see when they disagree — which is the
		// one state that means a deploy died halfway and is worth saying out
		// loud rather than resolving silently in either direction.
		if current != spec.Current {
			say(env.Stderr, "the current link points at %q but the edge record says %q; a deploy did not finish",
				current, spec.Current)
		}
		return writeDoc(env.Stdout, edgeDocument(spec, current), opts.json)
	}
	return c
}

// boxRollbackCommand flips `current` back one release.
//
// THE CONTROL PLANE IS NOT ASKED, and that is the design rather than a
// simplification: on the day a deploy is broken, the control plane is the last
// thing an operator wants to depend on. The target is the release the record
// names as Previous, which is written before the link moves, so a crash
// between the two leaves an honest record of a switch that did not happen.
func boxRollbackCommand(deps boxDeps) *Command {
	opts := &boxEdgeOptions{}
	c := &Command{
		Name:    "box rollback",
		Summary: "flip current back to the release that was serving before this one",
		Usage:   "cafe box rollback [--json] [--box <id>]",
		Flags:   func(fs *flag.FlagSet) { opts.declare(fs) },
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		dir := edgeStateDir(deps)
		spec, err := edge.Load(dir)
		if err != nil {
			return fmt.Errorf("cafe box rollback: %w", err)
		}
		back, err := edge.Rollback(dir, spec)
		if err != nil {
			// The refusal names the release that is serving and the fact that
			// no earlier one is recorded. A rollback that guessed at the
			// second-newest directory would sometimes serve a release nobody
			// deployed last, which is worse than a command that says no.
			_, _ = fmt.Fprintf(env.Stderr, "cafe box rollback: %v\n", err)
			return errReported
		}
		// The record is moved to match the link, so the two do not disagree
		// afterwards — the verb that reports both would otherwise report a
		// mismatch that is only a half-finished write.
		spec.Previous = spec.Current
		spec.Current = back
		if err := edge.Save(dir, spec); err != nil {
			return fmt.Errorf("cafe box rollback: %w", err)
		}
		say(env.Stderr, "current now points at %s", back)
		return writeJSONDoc(env.Stdout, edgeDocument(spec, back))
	}
	return c
}

// boxPublishCommand deploys one release: its files on disk, the `current` link
// flipped, and the routing record written last.
//
// The order is the zero-downtime claim (edge.Publish's comment has the full
// argument), and the record is written last because a spec naming a release
// that is not on disk is a spec a rollback would refuse to act on.
func boxPublishCommand(deps boxDeps) *Command {
	opts := &boxEdgeOptions{}
	c := &Command{
		Name:    "box publish",
		Summary: "deploy a release directory to this box and flip current at it",
		Usage:   "cafe box publish <release-id> <directory>",
		// Interspersed, because this verb takes two positional subjects — the
		// release id and the directory — and a person who types them first and
		// then reaches for `--json` must not be told they named three
		// arguments. It is the one verb here whose positionals are not a host,
		// and the reason the sweep is over "every verb with a positional
		// subject" rather than over "every verb with a host". See
		// box_setup.go for the whole argument.
		Interspersed: true,
		Flags:        func(fs *flag.FlagSet) { opts.declare(fs) },
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 2, len(args)); err != nil {
			return err
		}
		id, src := args[0], args[1]
		if err := validReleaseIDArg(id); err != nil {
			return fmt.Errorf("%w: cafe box publish: %w", errUsage, err)
		}
		dir := edgeStateDir(deps)
		files, err := readTree(src)
		if err != nil {
			return fmt.Errorf("cafe box publish: %w", err)
		}
		rel, err := edge.Publish(dir, id, files)
		if err != nil {
			_, _ = fmt.Fprintf(env.Stderr, "cafe box publish: %v\n", err)
			return errReported
		}
		spec, err := edge.Load(dir)
		if err != nil {
			return fmt.Errorf("cafe box publish: %w", err)
		}
		spec.Previous = spec.Current
		spec.Current = rel.ID
		if err := edge.Save(dir, spec); err != nil {
			return fmt.Errorf("cafe box publish: %w", err)
		}
		say(env.Stderr, "current now points at %s", rel.ID)
		return writeJSONDoc(env.Stdout, edgeDocument(spec, rel.ID))
	}
	return c
}

// validReleaseIDArg is the usage half of edge's release-id grammar: a bad id is
// a mistake on the command line, not a failure partway through a deploy, and
// the difference is exit 2 versus exit 1 in every script that calls this.
func validReleaseIDArg(id string) error {
	if strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("%q is not a release id: letters, digits, dot, dash and underscore only", id)
	}
	return nil
}

// edgeDocument is the verb's answer: the record, plus the one fact only the
// filesystem has — which release the `current` link names.
//
// It carries no certificate and no key — the record's whole shape excludes
// them — so the document is safe to paste into a support ticket, which is the
// same argument internal/edge's health answer makes for itself.
func edgeDocument(spec edge.Spec, current string) map[string]any {
	hosts := make([]map[string]any, 0, len(spec.Hosts))
	for _, h := range spec.SortedHosts() {
		hosts = append(hosts, map[string]any{
			"name":     h.Name,
			"upstream": h.Upstream,
			"service":  h.Service,
			"health":   string(h.Health),
			"detail":   h.Detail,
		})
	}
	return map[string]any{
		"box":      spec.Box,
		"mode":     string(spec.Mode),
		"current":  spec.Current,
		"link":     current,
		"previous": spec.Previous,
		"hosts":    hosts,
	}
}

// boxEdgeVerbs is the edge's word under `cafe box`. `edge` is the noun and the
// verbs are the deploy path's own: publish, edge, rollback — the three words
// the plan's Phase B uses, and nothing that has to be explained by naming an
// ingress.
func boxEdgeVerbs(deps boxDeps) []boxVerb {
	deps = deps.withDefaults()
	return []boxVerb{
		{name: "edge", cmd: boxEdgeStatusCommand(deps)},
		{name: "publish", cmd: boxPublishCommand(deps)},
		{name: "rollback", cmd: boxRollbackCommand(deps)},
	}
}

// edgeStateDir is the edge's own directory under the box's state directory.
func edgeStateDir(deps boxDeps) string { return filepath.Join(deps.stateDir, "edge") }

// boxEdgeOptions is the flag state the three verbs share.
type boxEdgeOptions struct {
	// json is on all three rather than on one, for the reason boxFlags is: a
	// flag that appears on one verb and not its siblings is a flag a caller
	// has to look up every time.
	json bool
	// box names the box the edge belongs to, for a caller that has more than
	// one state directory to choose between. Empty means the enrolled
	// identity's.
	box string
}

func (o *boxEdgeOptions) declare(fs *flag.FlagSet) {
	fs.BoolVar(&o.json, "json", false, "print the machine document on stdout instead of the human one")
	fs.StringVar(&o.box, "box", "", "the box this edge belongs to; empty means the enrolled identity's")
}

// boxIDFor resolves which box the edge belongs to. The flag wins; otherwise it
// is the enrolled identity, because a spec carrying the wrong box is the one
// readTree reads a directory into the release's file set. Symlinks and
// anything that is not a regular file are skipped rather than followed: a
// release directory is a set of bytes, and following a link out of it would put
// a path outside the release into a directory the edge serves.
func readTree(src string) (map[string][]byte, error) {
	info, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		body, err := os.ReadFile(src)
		if err != nil {
			return nil, err
		}
		return map[string][]byte{filepath.Base(src): body}, nil
	}
	out := map[string][]byte{}
	err = filepath.Walk(src, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = body
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
