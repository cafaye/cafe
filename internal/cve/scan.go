package cve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// Scan is one computed CVE surface for one box: observe, ask, classify,
// report, remember.
//
// It is a struct of seams rather than a long function for the reason
// internal/resolve's Resolve is a function of two seam values: every field
// here is something a test can replace, and the network and the box are both
// outside the thing being tested. Run has no other job than to call them in
// order, and the order is the semantics — a box that cannot be observed has
// no components, and asking about advisories for a component nobody read is
// the wrong way round.
type Scan struct {
	// Box is the enrolled identity. It is only ever a name.
	Box string
	// Observer reads the box.
	Observer Observer
	// Source is where advisories come from.
	Source Source
	// Schedule is the cadence this run is under. Empty means the default.
	Schedule Schedule
	// Now is the clock. Nil means time.Now, and a test always sets it.
	Now func() time.Time
	// StatePath is where the schedule state lives. Empty means the default
	// resolution, and a test always sets it.
	StatePath string
	// WriteState is whether the state file is written after the run. A
	// --dry-run or a test that must not touch the home directory sets it
	// false, and the honest reason it exists is that a scan whose state
	// writes are unconditional cannot be run twice on one box without
	// moving the schedule forward twice.
	WriteState bool
	// Output is where the rendered report goes. Nil means io.Discard.
	Output io.Writer
	// Emit is where the phase checkpoints go. Nil means Discard, and the CLI
	// always sets it to stderr — see internal/cli/cve.go.
	Emit Emitter
	// JSON selects the machine document over the human one, the idiom every
	// verb in internal/cli/box.go already uses.
	JSON bool
}

// Report is one run's outcome, which is the Report plus the state decisions
// taken on the way.
type Outcome struct {
	Report Report
	// Saved is where the state was written, or empty when it was not.
	Saved string
	// Err is a failure that did not stop the run — a state file that could
	// not be written, an advisory batch the feed refused. The report is still
	// valid, and a run that threw the report away because the bookkeeping
	// failed would be a run that lost the scan to a bookkeeping problem.
	Err error
}

// Run computes the surface and writes the report.
//
// # The four phases, and why each is checkpointed
//
// observe, advisories, classify, render. Each emits a line before it starts
// and after it finishes, which is what makes a slow phase distinguishable
// from a hung one: the last checkpoint printed is the phase that is running,
// and a phase that prints nothing more is the phase that stopped. That is the
// answer to "a network call that hangs is an invisible stall" — not a faster
// network, a sentence.
//
// # What fails the run and what does not
//
// An observer that cannot read the box fails the run: there is no inventory,
// and an empty inventory would produce a clean report about a box nobody
// looked at. An advisory source that fails fails the run for the same reason.
// A state file that cannot be written does not: the scan happened, the
// answer is in front of the operator, and the only loss is that the next run
// may be early. It is reported on the Outcome rather than swallowed.
func (s Scan) Run(ctx context.Context) (Outcome, error) {
	emit := Discard()
	if s.Emit != nil {
		emit = s.Emit
	}
	start := time.Now()
	if s.Now != nil {
		start = s.Now()
	}
	if s.Output == nil {
		s.Output = io.Discard
	}

	inv := Inventory{Box: s.Box, Declared: append([]Component(nil), Substrate...)}
	if s.Schedule.Every == 0 {
		s.Schedule = DefaultSchedule
	}
	if s.StatePath == "" {
		path, err := StatePath("")
		if err != nil {
			return Outcome{}, err
		}
		s.StatePath = path
	}
	state, err := LoadState(s.StatePath)
	if err != nil {
		return Outcome{}, err
	}
	due, _, staleness := s.Schedule.Due(start, state.LastRun)

	emit.Emit("observe", "reading the box")
	if s.Observer == nil {
		return Outcome{}, fmt.Errorf("no observer is configured: the CVE surface needs a box to read")
	}
	observed, err := s.Observer.Observe(ctx, emit)
	if err != nil {
		return Outcome{}, fmt.Errorf("read the box: %w", err)
	}
	inv.Observed = observed
	inv.Compare()
	components := inv.Components()
	emit.Emit("observe", fmt.Sprintf("%d components, %d divergent from the substrate catalogue",
		len(components), len(inv.Divergences)))

	if s.Source == nil {
		return Outcome{}, fmt.Errorf("no advisory source is configured: the CVE surface needs somewhere to ask")
	}
	advisories, err := s.Source.Advisories(ctx, components, emit)
	if err != nil {
		return Outcome{}, fmt.Errorf("ask the advisory feed: %w", err)
	}
	emit.Emit("classify", fmt.Sprintf("matching %d components against %d advisories", len(components), len(advisories)))

	findings, counts := Classify(components, advisories)
	silent := feedSilent(components, advisories)

	r := Report{
		Box:         s.Box,
		ScannedAt:   start,
		Schedule:    s.Schedule.String(),
		Staleness:   staleness,
		Counts:      counts,
		Findings:    findings,
		Divergences: inv.Divergences,
		Unmatched:   silent,
	}
	if !due {
		// A run that was not due still answers, but it says so: the point of
		// the schedule is that the answer is fresh, and a stale answer
		// presented as a fresh one is the defect the schedule was built to
		// remove.
		r.Staleness = "not due yet — " + r.Staleness
	}
	r.DurationSeconds = time.Since(start).Seconds()

	if s.JSON {
		if err := Encode(s.Output, r); err != nil {
			return Outcome{}, err
		}
	} else {
		if err := Render(s.Output, r); err != nil {
			return Outcome{}, err
		}
	}

	out := Outcome{Report: r}
	if s.WriteState {
		if err := SaveState(s.StatePath, State{
			Box:         s.Box,
			LastRun:     start,
			LastOutcome: r.Summary(),
			Findings:    len(findings),
			ScanSeconds: r.DurationSeconds,
		}); err != nil {
			out.Err = err
		} else {
			out.Saved = s.StatePath
		}
	}
	return out, nil
}

// feedSilent names the components the feed was asked about and answered
// nothing for.
//
// It is the difference between "checked and clean" and "not indexed", and it
// is reported because the second is the common case for the substrate's own
// components: OSV indexes Go modules and distro packages, and a k3s release
// is neither of those under the name the substrate gives it. A report that
// said "17 components checked, nothing found" while the feed had answered for
// two of them would be the exact failure this package exists to prevent —
// not a wrong answer about an advisory, a wrong answer about whether anyone
// looked.
func feedSilent(components []Component, advisories []Advisory) []string {
	answered := make(map[string]bool, len(advisories))
	for _, a := range advisories {
		answered[a.Component] = true
	}
	var out []string
	for _, c := range components {
		if c.Version == "" {
			// An unreadable version was asked about with an empty version,
			// which the feed answers with nothing. That is already reported
			// as an unknown classification; saying it twice would be noise.
			continue
		}
		if !answered[c.Name] {
			out = append(out, c.Name)
		}
	}
	return out
}

// KubectlObserver reads a substrate box with kubectl(1).
//
// It exists because the substrate is a single-node k3s cluster and kubectl is
// the one tool on the box that can answer "what images are running and with
// what tags" — and because boxd's own World deliberately has no method that
// runs a process. This is NOT that seam: this is the operator's machine
// running a scan against a box it administrates, not a capability path, and
// the command list is closed — `kubectl version` and `kubectl get
// pods,deployments` — so nothing here is a general shell on the box.
//
// The two commands are chosen because they answer the two halves of the
// inventory: the node agent's version, which `kubectl version` reports in the
// spelling the substrate pins (`v1.37.1+k3s1`), and every running image,
// which is where a version nobody pinned still shows up.
type KubectlObserver struct {
	// Kubeconfig is the path passed with --kubeconfig. Empty means whatever
	// kubectl resolves itself.
	Kubeconfig string
	// Context is the kubectl context. Empty means the current one.
	Context string
	// Timeout bounds each command. Zero means KubectlCommandTimeout, and it
	// is never unbounded: a box that has stopped answering is a box that has
	// stopped answering, and a scan that waited forever would be the
	// invisible stall the checkpoints exist to make visible.
	Timeout time.Duration
	// Command is how a command is run. Nil means exec.CommandContext, and a
	// test sets it to a fake so no test needs kubectl on PATH.
	Command func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// KubectlCommandTimeout is the bound on a single kubectl call.
var KubectlCommandTimeout = 60 * time.Second

// Observe runs the two reads and turns them into components.
func (k KubectlObserver) Observe(ctx context.Context, e Emitter) ([]Component, error) {
	if k.Timeout == 0 {
		k.Timeout = KubectlCommandTimeout
	}
	run := k.Command
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			return cmd.Output()
		}
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, k.Timeout)
	defer cancel()

	e.Emit("observe", "asking kubectl for the node agent's version")
	raw, err := run(timeoutCtx, "kubectl", k.args("version", "-o", "json")...)
	if err != nil {
		return nil, fmt.Errorf("kubectl version: %w", err)
	}
	node, err := parseServerVersion(raw)
	if err != nil {
		return nil, err
	}

	e.Emit("observe", "asking kubectl for every running image")
	raw, err = run(timeoutCtx, "kubectl", k.args("get", "pods,deployments,daemonsets,statefulsets", "-A", "-o", "json")...)
	if err != nil {
		return nil, fmt.Errorf("kubectl get pods,deployments,daemonsets,statefulsets -A: %w", err)
	}
	workloads, err := parseImages(raw)
	if err != nil {
		return nil, err
	}
	e.Emit("observe", fmt.Sprintf("the box reports %d image(s) and a node agent at %s", len(workloads), node.Version))

	return append([]Component{node}, workloads...), nil
}

// args prefixes the closed argument list with the optional kubeconfig and
// context.
func (k KubectlObserver) args(sub ...string) []string {
	var out []string
	if k.Kubeconfig != "" {
		out = append(out, "--kubeconfig", k.Kubeconfig)
	}
	if k.Context != "" {
		out = append(out, "--context", k.Context)
	}
	return append(out, sub...)
}

// kubectlVersion is the half of `kubectl version -o json` this reads.
type kubectlVersion struct {
	ServerVersion struct {
		GitVersion string `json:"gitVersion"`
	} `json:"serverVersion"`
}

// parseServerVersion reads the node agent's version as kubectl reports it.
//
// It refuses a document with no server version rather than returning an empty
// component: a box whose kubectl cannot reach a cluster prints a
// clientVersion and no serverVersion, and a component with no version would
// be matched against advisories as though the box had nothing installed.
func parseServerVersion(raw []byte) (Component, error) {
	var doc kubectlVersion
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Component{}, fmt.Errorf("kubectl's version answer is not JSON: %w", err)
	}
	if strings.TrimSpace(doc.ServerVersion.GitVersion) == "" {
		return Component{}, fmt.Errorf("kubectl reached no cluster: no serverVersion in the answer")
	}
	return Component{
		Name:     "k3s",
		Version:  doc.ServerVersion.GitVersion,
		Kind:     KindNode,
		Pinned:   true,
		Declared: false,
		Source:   "observed with kubectl version",
	}, nil
}

// workloadList is the shape kubectl prints for -o json over any resource
// list: items, each with metadata naming it and spec naming its containers.
type workloadList struct {
	Items []struct {
		Metadata struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name  string `json:"name"`
						Image string `json:"image"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
			Containers []struct {
				Name  string `json:"name"`
				Image string `json:"image"`
			} `json:"containers"`
		} `json:"spec"`
	} `json:"items"`
}

// parseImages turns every container image on the box into a component.
//
// An image is `repo/name:tag`, and the component is the last segment of the
// repo path with the tag stripped — which is what the substrate catalogue
// names. That is a convention rather than a rule and it is stated as one: an
// image called `bitnami/redis:7.4.1` becomes `redis`, and if a feed indexes
// it under another name the match fails and the report says the feed was
// silent rather than saying the box was clean.
//
// Images with no tag are reported with an empty version rather than being
// dropped: `:latest` and a digest-only image are two of the ways a box ends
// up running something nobody can have an advisory against, and the report
// has to be able to say that.
func parseImages(raw []byte) ([]Component, error) {
	var doc workloadList
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("kubectl's image answer is not JSON: %w", err)
	}
	seen := make(map[string]bool)
	var out []Component
	for _, item := range doc.Items {
		for _, c := range append(append([]struct {
			Name  string `json:"name"`
			Image string `json:"image"`
		}{}, item.Spec.Containers...), item.Spec.Template.Spec.Containers...) {
			name, tag := splitImage(c.Image)
			if name == "" {
				continue
			}
			key := name + "@" + tag
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Component{
				Name:      name,
				Version:   tag,
				Kind:      KindImage,
				Namespace: item.Metadata.Namespace,
				Declared:  false,
				Source:    fmt.Sprintf("observed image %s on %s/%s", c.Image, item.Metadata.Namespace, item.Metadata.Name),
			})
		}
	}
	return out, nil
}

// splitImage is `repo/name:tag` -> (name, tag). A digest-only reference
// produces an empty tag, which is reported as an unreadable version rather
// than guessed at.
func splitImage(image string) (string, string) {
	if strings.TrimSpace(image) == "" {
		return "", ""
	}
	ref := image
	if at := strings.IndexByte(ref, '@'); at >= 0 {
		ref = ref[:at]
	}
	name, tag := ref, ""
	if colon := strings.LastIndexByte(ref, ':'); colon >= 0 {
		if slash := strings.LastIndexByte(ref, '/'); slash < colon {
			name, tag = ref[:colon], ref[colon+1:]
		}
	}
	if slash := strings.LastIndexByte(name, '/'); slash >= 0 {
		name = name[slash+1:]
	}
	return name, tag
}
