package secret

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cafaye/caf/internal/deploy"
)

// Step is one kubectl invocation a rotation makes, and the sentence a reader
// needs to trust it.
//
// # The value never reaches Argv
//
// Argv is the command as the operator would type it, and a generated value is
// NEVER in it: argv is visible to every other process on the box through /proc,
// and a rotation that put a fresh SECRET_KEY_BASE in argv would have published
// it to the box it was trying to protect. The value travels in the document on
// stdin instead, piped to `kubectl apply -f -`, which is the discipline the kit
// installer holds itself to — its own report says the redeemed credential
// "reaches no disk, no argv and no stdout"
// (cafaye/kit/REPORT-kit-w2p4-secret-input-surface.md).
//
// Redacted is the document with the generated value replaced by a marker, and
// it is the only one of the two a dry run or a failure report ever prints. That
// is what makes a rotation report safe to paste into a ticket.
type Step struct {
	Name string
	// Argv is the command, kubectl first, and never a value.
	Argv []string
	// Stdin is the document piped in, and it carries the value.
	Stdin string
	// Redacted is Stdin with the value replaced. Printed instead of Stdin.
	Redacted string
	Why      string

	// value is the one secret this package ever holds in memory. It is
	// unexported so that no caller, no renderer and no %v over a Step can
	// reach it, and it is set after the plan is built so the plan a dry run
	// printed and the plan that runs are the same object.
	value Value

	// write is the Target this step writes, and nil for a step that writes
	// nothing. It is set when the plan is BUILT rather than derived from the
	// step's index at run time, because an index is a coupling: a plan with a
	// presence check in front of the writes and one without it are the same
	// steps and different numbers, and a substitution that subtracted the
	// wrong one would put a fresh SECRET_KEY_BASE in the presence check's
	// argv. The step says what it writes.
	write *Target
}

// Runner is every kubectl invocation in this package, and it returns what
// kubectl said on stdout.
//
// It is an interface for the reason internal/backup's and internal/deploy's are:
// a test can hand the rotator a runner that records what it would run and
// answers with whatever the case planted, which is the only reason a rotation
// is testable in this repository without a cluster anywhere in sight. There is
// no kubectl in the tests.
type Runner interface {
	Run(ctx context.Context, step Step) (string, error)
}

// Kubectl is the real Runner: kubectl on PATH, with the kubeconfig the shell
// already selected.
//
// The binary is resolved through internal/deploy's own resolver rather than a
// second copy of exec.LookPath, and the missing-tool sentence is
// deploy.ErrKubectlMissing rather than a second copy of that one either. Two
// copies of "kubectl is not installed" is a drift waiting for the day exactly
// one of them is fixed.
type Kubectl struct {
	Binary  string
	Stdout  io.Writer
	Stderr  io.Writer
	Timeout time.Duration
}

// NewKubectl is the wiring. A nil writer is io.Discard, because a rotation that
// cannot write anywhere is still a rotation that must not fail.
func NewKubectl(stdout, stderr io.Writer) *Kubectl {
	return &Kubectl{Binary: deploy.ResolveKubectl(), Stdout: stdout, Stderr: stderr}
}

// Run executes one step and answers with kubectl's stdout, trimmed.
//
// stdout and stderr are TEE'd to the writers the wiring gave: a rotation prints
// what kubectl said, because "secret/configmap configured" and "error from
// server" are both messages an operator has to see, and the captured copy is
// what the presence check inspects without a value ever leaving it.
func (k *Kubectl) Run(ctx context.Context, step Step) (string, error) {
	binary := k.Binary
	if binary == "" {
		binary = "kubectl"
	}
	if k.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, k.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, binary, step.Argv[1:]...)
	var out, errOut bytes.Buffer
	if step.Stdin != "" {
		cmd.Stdin = strings.NewReader(step.Stdin)
	}
	cmd.Stdout = io.MultiWriter(&out, writerOrDiscard(k.Stdout))
	cmd.Stderr = io.MultiWriter(&errOut, writerOrDiscard(k.Stderr))
	if err := cmd.Run(); err != nil {
		if looksMissing(err) {
			return "", fmt.Errorf("%w\n%s", deploy.ErrKubectlMissing, binary)
		}
		return "", fmt.Errorf("kubectl %s: %w\n%s", strings.Join(step.Argv[1:], " "), err, errOut.String())
	}
	return strings.TrimSpace(out.String()), nil
}

func writerOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

func looksMissing(err error) bool {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return errors.Is(execErr.Err, exec.ErrNotFound)
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return errors.Is(pathErr.Err, os.ErrNotExist)
	}
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
}

// ErrUsage is the sentinel for "caf was invoked wrongly" — an unknown secret
// name, a rotation asked for by a name the inventory does not hold. It is a
// different class from a refusal and gets a different exit code, the same three
// exits `caf backup` makes.
var ErrUsage = errors.New("caf secret: invalid invocation")

// Refusal is the answer `caf secret rotate` gives for an entry that is not in
// the rotatable class, and it is a REFUSAL rather than a skip.
//
// The difference is the whole point of the packet. A rotation run that skipped
// MUSE_VAULT_KEY silently would answer "nothing to do", and the operator would
// conclude the inventory held nothing that needed rotating — which is the
// weekend the packet exists to prevent. So the command names every entry it
// would not touch, with the reason, and exits non-zero: the not-rotatable class
// is a report, not a silence.
type Refusal struct {
	Name   string `json:"name"`
	Class  Class  `json:"class"`
	Reason string `json:"reason"`
	Source string `json:"source"`
}

func (r Refusal) Error() string {
	return fmt.Sprintf("caf secret: refusing to rotate %s: it is %s.\n  %s\n  %s",
		r.Name, r.Class, r.Reason, r.Source)
}

// Human is the refusal as prose, for stderr. It is the same sentence as Error()
// with the class's citation on its own line, and it is separate from Error()
// because the two go to two different streams: a person reads stderr, and a
// caller matching a sentinel reads the return.
func (r Refusal) Human() string {
	return fmt.Sprintf("caf secret: refusing to rotate %s: it is %s.\n  %s\n  %s\n",
		r.Name, r.Class, r.Reason, r.Source)
}

// Plan is one rotation: the steps, in order, and what changed.
//
// A plan is built before anything runs so a dry run can print exactly what the
// real run would do. The steps are this package's and not the caller's, for the
// reason internal/deploy's doc gives: a caller that built argv would be a second
// idea of what a rotation is.
type Plan struct {
	Entry Entry
	Steps []Step
	// Report is the sentence the rotation hands back: what it wrote, where, and
	// what it restarted. No value, and no namespace/key pair a reader could
	// reassemble one from.
	Report string
}

// Value is the generated fresh value, and the one secret this package holds in
// memory. It exists for exactly as long as the rotation that made it: it is
// never printed, never returned to a caller and never written anywhere but the
// stdin of an apply step.
type Value string

// Generate makes a fresh value in the entry's own spelling.
//
// The spellings are the installer's, and they are load-bearing rather than
// cosmetic: Phoenix refuses a SECRET_KEY_BASE shorter than 64 bytes at boot
// (install.sh:1228-1232), so a rotation that minted a value the service would
// refuse would be an outage disguised as a rotation.
func Generate(shape string) (Value, error) {
	switch shape {
	case "base64 of 64 random bytes":
		return Value(base64.StdEncoding.EncodeToString(randomBytes(64))), nil
	case "base64 of 32 random bytes":
		return Value(base64.StdEncoding.EncodeToString(randomBytes(32))), nil
	case "hex of 24 random bytes":
		return Value(hex.EncodeToString(randomBytes(24))), nil
	case "":
		return "", fmt.Errorf("%w: the entry has no shape, so there is nothing to mint", ErrUsage)
	default:
		return "", fmt.Errorf("%w: unknown shape %q", ErrUsage, shape)
	}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		// crypto/rand failing is not a condition a rotation can answer: the
		// value would be predictable, and a predictable value is worse than no
		// rotation at all.
		panic("caf secret: crypto/rand is unavailable: " + err.Error())
	}
	return b
}

// The redaction marker. It is a fixed string and not the length of the value,
// because a marker whose length matched would leak how long the secret is.
const redacted = "<generated value, never printed>"

// The presence check's go-template. It ranges over the Secret's data and prints
// $k and never $v, so what reaches this process is the set of key NAMES and
// nothing else. It is the reason the invariant in doc.go is a property of the
// plan rather than a comment on it: a template that had to be carefully written
// not to leak would be a discipline, and this one is written so that it cannot.
const presenceTemplate = "{{range $k, $v := .data}}{{$k}} {{end}}"

// PlanRotation is the pure half: given an entry it answers either the steps or
// the refusal, and it executes nothing.
//
// It is pure so that both answers are assertable in a table with no cluster
// anywhere, which is the only way a refusal can be proved to name its reason
// rather than discovered to have done so by accident.
func PlanRotation(entry Entry) (Plan, error) {
	if entry.Class != Rotatable {
		return Plan{}, Refusal{Name: entry.Name, Class: entry.Class, Reason: entry.Reason, Source: entry.Source}
	}
	if entry.Secret == "" || entry.Deployment == "" {
		// A rotatable entry with no Secret to write to is a table error, and it
		// is refused naming the reason rather than planned into a kubectl call
		// with an empty name.
		return Plan{}, Refusal{
			Name:   entry.Name,
			Class:  entry.Class,
			Reason: entry.Reason + " (and the entry names no Secret to write to, which is a table error)",
			Source: entry.Source,
		}
	}

	var steps []Step
	steps = append(steps, Step{
		Name: "confirm the keys are there",
		Argv: []string{"kubectl", "get", "secret", entry.Secret, "-n", entry.Namespace, "-o", "go-template=" + presenceTemplate},
		Why:  "A read that answers with key NAMES only: the template ranges over .data and prints $k, never $v.",
	})
	for _, w := range entry.WriteTargets() {
		target := w
		steps = append(steps, Step{
			Name:     "replace " + w.Namespace + "/" + w.Name + " key " + w.Key,
			Argv:     []string{"kubectl", "apply", "-f", "-", "-n", w.Namespace},
			Stdin:    manifestFor(w, Value("")),
			Redacted: redactedManifestFor(w),
			Why: "the installer's own rotation posture: replace the secret and restart the pods. " +
				"The value is piped on stdin and never reaches argv, where /proc would publish it.",
			write: &target,
		})
	}
	for _, r := range entry.RestartTargets() {
		steps = append(steps, Step{
			Name: "restart " + r.Namespace + "/" + r.Name,
			Argv: []string{"kubectl", "rollout", "restart", "deployment/" + r.Name, "-n", r.Namespace},
			Why:  "a secret a running pod has already read does not change the pod: the restart is what makes the new value live.",
		})
		steps = append(steps, Step{
			Name: "wait for " + r.Namespace + "/" + r.Name,
			Argv: []string{"kubectl", "rollout", "status", "deployment/" + r.Name, "-n", r.Namespace, "--timeout=300s"},
			Why:  "a rotation that restarted and walked away is a rotation nobody knows finished.",
		})
	}

	var wrote []string
	for _, w := range entry.WriteTargets() {
		wrote = append(wrote, w.Namespace+"/"+w.Name+" key "+w.Key)
	}
	var restarted []string
	for _, r := range entry.RestartTargets() {
		restarted = append(restarted, r.Namespace+"/deployment/"+r.Name)
	}
	return Plan{
		Entry:  entry,
		Steps:  steps,
		Report: "rotated " + entry.Name + ": wrote " + joinComma(wrote) + ", restarted " + joinComma(restarted),
	}, nil
}

// manifestFor is the apply document. stringData rather than data, so no base64
// of a base64, and `kubectl apply` merges it into the existing Secret rather
// than replacing it — the other keys in that Secret survive, which is what makes
// this a rotation rather than a rebuild.
func manifestFor(w Target, v Value) string {
	return "apiVersion: v1\nkind: Secret\nmetadata:\n  name: " + w.Name +
		"\n  namespace: " + w.Namespace + "\nstringData:\n  " + w.Key + ": " + string(valueFor(w, v)) + "\ntype: Opaque\n"
}

func redactedManifestFor(w Target) string {
	return "apiVersion: v1\nkind: Secret\nmetadata:\n  name: " + w.Name +
		"\n  namespace: " + w.Namespace + "\nstringData:\n  " + w.Key + ": " + redacted + "\ntype: Opaque\n"
}

// valueFor derives a second write's value from the first. The Redis password is
// the only user: REDIS_URL is the password with a URL around it, and a rotation
// that wrote one and not the other would take gateway down rather than rotate
// anything.
func valueFor(w Target, primary Value) Value {
	if w.Template == "" {
		return primary
	}
	return Value(strings.ReplaceAll(w.Template, "%s", string(primary)))
}

// Rotate plans and runs one rotation.
//
// It refuses for every class that is not rotatable, and it refuses BEFORE it
// runs anything: a plan that was refused halfway would have written the first of
// two keys and not the second, which is the outage the packet exists to prevent.
//
// The progress stream is the caller's to choose, and this command hands it
// stderr: what kubectl said is a sentence for a person, not a document for a
// parser.
func Rotate(ctx context.Context, name string, runner Runner, progress io.Writer) (Plan, error) {
	// A nil writer is io.Discard, the rule NewKubectl already holds: a rotation
	// that cannot write anywhere is still a rotation that must not fail.
	progress = writerOrDiscard(progress)
	entry, err := Find(name)
	if err != nil {
		return Plan{}, err
	}
	plan, err := PlanRotation(entry)
	if err != nil {
		return plan, err
	}
	value, err := Generate(entry.Shape)
	if err != nil {
		return plan, err
	}
	// The generated value is substituted into the plan's documents here, after
	// the plan is built, so the plan a dry run printed and the plan that runs
	// are the same object with the same values. The step names the Target it
	// writes, so the presence check that precedes the writes — and the restarts
	// that follow them — are never given a document by an index that was one
	// off.
	for i := range plan.Steps {
		if plan.Steps[i].write == nil {
			continue
		}
		plan.Steps[i].Stdin = manifestFor(*plan.Steps[i].write, value)
		plan.Steps[i].value = value
	}
	for _, step := range plan.Steps {
		if _, err := fmt.Fprintf(progress, "caf secret: %s\n", step.Name); err != nil {
			return plan, err
		}
		if step.Stdin == "" {
			// Nothing sensitive: print the command as the operator would type it.
			fmt.Fprintf(progress, "  %s\n", strings.Join(step.Argv, " "))
		} else {
			fmt.Fprintf(progress, "  kubectl apply -f - (document on stdin, value never printed)\n")
		}
		if _, err := runner.Run(ctx, step); err != nil {
			return plan, err
		}
	}
	if _, err := fmt.Fprintf(progress, "caf secret: %s\n", plan.Report); err != nil {
		return plan, err
	}
	return plan, nil
}

// Due is the schedule half: which of the rotatable class is past its interval,
// given the last-rotated record.
//
// It is a function of a state and a clock rather than a timer because a timer is
// invisible: nothing on the box says when the last rotation happened, so nothing
// can say whether one is due. The state file is that fact, written after every
// rotation, which makes "on a schedule" a property of the last run rather than
// of a cron line.
func Due(state State, now time.Time) []Entry {
	out := []Entry{}
	for _, e := range Inventory() {
		if e.Class != Rotatable || e.Interval <= 0 {
			continue
		}
		last, ok := state.Last[e.StateKey()]
		if !ok || now.Sub(last) >= e.Interval {
			out = append(out, e)
		}
	}
	return out
}
