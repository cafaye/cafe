package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/cafaye/cafe/internal/boxd"
)

// Options is one `cafe box setup`. It is a struct of parameters rather than a
// set of package-level variables for the reason every other command in this
// repository takes one: a test hands the command a host it owns and a
// capability it can count, and nothing else in the process can see either.
type Options struct {
	// Host is the box, reached over SSH. Nil is refused: a setup with no host
	// is a setup that would report success having done nothing.
	Host Host
	// Name is the address as the operator typed it. It goes into the document
	// so a log line says which host it was about.
	Name string
	// Capability is the single-use box.enroll token. It is held in this struct
	// for exactly as long as it takes to reach the enrollment command's stdin,
	// and it is never written to a file, never put in an argument and never
	// put in an environment variable. See the package doc.
	Capability string
	// BinaryPath is the local cafe binary to install on the host. It is the
	// running binary by default, and a test points it at a scratch file.
	BinaryPath string
	// Range is the version constraint this box tracks, written into the unit as
	// CAF_BOX_RANGE. Empty is allowed and writes no line: a box with no
	// constraint reports its range as unknown rather than inheriting a guess.
	Range string
	// ControlPlane is the fallback origin for the daemon's identity document,
	// used only when the host's own box.json names none.
	ControlPlane string
	// Version is the cafe build this setup installs, recorded in the daemon's
	// identity document.
	Version string
	// DryRun prints the plan and runs none of it.
	DryRun bool
}

// ErrNoHost is what a setup answers when there is nothing to talk to. It is a
// sentinel rather than a nil-panic because the caller is a CLI whose exit code
// has to distinguish "you did not give me a host" from "the host refused".
var ErrNoHost = errors.New("cafe box setup: no host to set up")

// ErrNoCapability is the missing-token answer. The capability is the only thing
// this command spends, so a run without one is a usage mistake rather than a
// failure: nothing was attempted, and nothing could have been.
var ErrNoCapability = errors.New("cafe box setup: --token is required; it is the single-use box.enroll capability minted for this host")

// ErrNoBinary is the answer when the cafe binary to install cannot be read.
var ErrNoBinary = errors.New("cafe box setup: the cafe binary to install could not be read")

// Setup takes one host from bare to a running box daemon.
//
// It runs in three phases, and the split matters because it is the difference
// between a retry that converges and one that spends capability:
//
//  1. PROBE. Three read-only questions — can this operator get root, what
//     architecture is this host, and is it already enrolled — because the plan
//     is a function of their answers and a plan built on a guess is a plan
//     that runs the wrong commands. The architecture question is the one that
//     decides WHICH file crosses, so it is asked before the plan is built and
//     a byte is written: see arch.go.
//  2. WALK. The table, in order. Every step checks first; a check that passes
//     runs nothing.
//  3. VERIFY. Read the host's own enrollment document back and attach the
//     daemon's report, so a controller that never held a key can still say
//     which box it now has.
//
// A re-run on a host that has had one passes every check, runs nothing, and
// reports zero redemptions. That is the property the packet exists for, and it
// is asserted twice in one test because a single successful re-run can be luck.
func Setup(ctx context.Context, o Options) (Result, error) {
	if o.Host == nil {
		return Result{}, ErrNoHost
	}
	if strings.TrimSpace(o.Capability) == "" {
		return Result{}, ErrNoCapability
	}

	result := Result{Host: o.Name, DryRun: o.DryRun}

	// Phase 1. The superuser probe is kamal's superuser? asked before anything
	// is installed, and it is read-only, which is why a dry run may run it.
	sudo, canRoot, err := probeSuperuser(ctx, o.Host)
	if err != nil {
		return result, fmt.Errorf("cafe box setup %s: %w", o.Name, err)
	}
	if !canRoot {
		result, err := refuseRoot(result)
		return result, err
	}
	result.Sudo = sudo
	result.Steps = append(result.Steps, StepResult{
		Name: StepSuperuser, Title: "this operator can install packages and write to /etc",
		Action: ActionAlreadyDone,
	})
	result.Log = append(result.Log, "cafe box setup "+o.Name+": root is "+sudoOrRoot(sudo))

	// The architecture preflight, immediately after it: what architecture is
	// this host, and which local file can run there. It is a probe and not a
	// row of the table because the plan carries the answer — the binary's
	// bytes and the hash of them — and neither is known until this has been
	// asked. A transport failure is an error; a host the controller cannot
	// build for is a REFUSAL recorded as a step, and nothing crosses. See
	// arch.go for the whole argument.
	asked, err := probeArch(ctx, o.Host)
	if err != nil {
		return result, fmt.Errorf("cafe box setup %s: %w", o.Name, err)
	}
	pick, err := decideBinary(controllerTarget(), asked, o.BinaryPath)
	if err != nil {
		return Result{}, err
	}
	fact := archFact{Answer: asked.Answer, Refusal: pick.Refusal}
	if !pick.Ships() {
		return refuseArch(result, fact)
	}
	// The bytes that cross are the ones the preflight chose, which is not
	// necessarily the file o.BinaryPath named.
	result.Arch = pick.document(controllerTarget(), asked)
	result.Log = append(result.Log, "cafe box setup "+o.Name+": "+pick.Reason)

	enrolled, enrolledOut, err := readEnrollment(ctx, o.Host, sudo)
	if err != nil {
		return result, fmt.Errorf("cafe box setup %s: %w", o.Name, err)
	}
	_ = enrolledOut

	steps := plan(o, sudo, pick.Hash, pick.Binary, enrolled, fact)
	if o.DryRun {
		return dryRun(result, steps), nil
	}

	// Phase 2. The walk. A refusal stops the run: the steps after a failed one
	// are steps whose preconditions are not established, and continuing would
	// produce a log that says "done" over a box that cannot run a container.
	var enrollment *enrollment
	for _, step := range steps {
		if step.Name == StepSuperuser {
			// Already answered in phase 1; asking twice would put a second
			// row of the same question in the log.
			continue
		}
		out, err := o.Host.Run(ctx, step.Check, nil)
		if err != nil {
			result, err := refuse(result, step, "could not ask the host: "+err.Error())
			return result, err
		}
		if out.OK() {
			detail := "the host already had this; nothing was run"
			if step.Name == StepHostArch {
				// The arch step's check is the question the preflight already
				// asked, so its answer is the decision, not "the host had it".
				// The row says which binary shipped and why, because that is
				// the one thing about this run nobody can work out afterwards.
				detail = pick.Reason
			}
			result.Steps = append(result.Steps, StepResult{
				Name: step.Name, Title: step.Title, Action: ActionAlreadyDone, Detail: detail,
			})
			result.Log = append(result.Log, "  "+step.Name+": already done")
			if step.Report {
				result.Report = decodeReport(out.Stdout)
			}
			continue
		}
		if step.Apply.Command == "" && step.Apply.Path == "" {
			// No apply is not permission to continue. The refusal names the
			// step, the host's own words, and what would fix it.
			detail := step.Refusal
			if out.Stderr != "" {
				detail += "\n" + clip(out.Stderr, 5)
			}
			result, err := refuse(result, step, detail)
			return result, err
		}
		if step.Name == StepBoxdIdentity {
			// The daemon's identity is built HERE, after enrollment, not when
			// the plan was built. On a fresh box box.json does not exist until
			// the enrollment step has run, so the box id and the certificate
			// paths are only knowable now; a document built at plan time would
			// carry an empty box_id and no certificate on the one run that
			// matters. The pre-walk read is the fallback for a re-run, where
			// the enrollment step is already done and prints nothing.
			identity := enrolled
			if enrollment != nil {
				identity = enrollment.asIdentity()
			}
			step.Apply.Data = identityDocument(identity, o)
			step.Apply.Command = placeCertificateCommand(sudo, identity)
		}
		if step.Apply.Path != "" {
			mode := step.Apply.Mode
			if mode == 0 {
				mode = 0o644
			}
			if err := o.Host.Write(ctx, step.Apply.Path, step.Apply.Data, mode); err != nil {
				result, err := refuse(result, step, "could not write "+step.Apply.Path+": "+err.Error())
				return result, err
			}
		}
		if step.Apply.Command != "" {
			applied, err := o.Host.Run(ctx, step.Apply.Command, step.Apply.Stdin)
			if err != nil {
				result, err := refuse(result, step, "could not run the apply: "+err.Error())
				return result, err
			}
			if !applied.OK() {
				result, err := refuse(result, step, "the apply failed:\n"+clip(applied.Stderr+applied.Stdout, 10))
				return result, err
			}
			if step.Name == StepBoxEnrolled {
				enrollment = decodeEnrollment(applied.Stdout)
				if enrollment == nil {
					result, err := refuse(result, step, "the host enrolled but printed a document cafe could not read:\n"+clip(applied.Stdout, 10))
					return result, err
				}
				result.Redemptions += enrollment.redemptions
			}
			if step.Name == StepBoxdReport {
				result.Report = decodeReport(applied.Stdout)
			}
		}
		result.Steps = append(result.Steps, StepResult{
			Name: step.Name, Title: step.Title, Action: ActionApplied,
			Detail: "the host did not have this; cafe did it",
		})
		result.Log = append(result.Log, "  "+step.Name+": applied")
	}

	// Phase 3. Read the enrollment back off the host. This is a read and not a
	// copy: what crosses the seam is three non-secret strings, and the private
	// key that made them stays in the keyring on the host.
	result.Enrolled = (*Enrollment)(nil)
	if enrollment != nil {
		result.Enrolled = &enrollment.Enrollment
	}
	if fresh, _, err := readEnrollment(ctx, o.Host, sudo); err == nil && fresh != nil {
		result.Enrolled = &Enrollment{
			BoxID:        fresh.BoxID,
			ControlPlane: fresh.ControlPlane,
			EnrolledAt:   fresh.EnrolledAt,
			Action:       actionFor(enrollment),
		}
	} else if result.Enrolled != nil {
		result.Enrolled.Action = actionFor(enrollment)
	}
	return result, nil
}

// actionFor names what the host's own enroll said, or "already-enrolled" when this run
// was not the one that enrolled it — the already-enrolled short circuit, where
// the check passed and nothing ran.
func actionFor(e *enrollment) string {
	if e == nil || e.Action == "" {
		return "already-enrolled"
	}
	return e.Action
}

// refuse records the step that stopped the run and returns the result beside
// the error, so a caller can print the document AND the sentence. The error
// names the host and the step, because "setup failed" with no step in it is a
// message that sends a reader to a log that says nothing useful.
func refuse(result Result, step Step, detail string) (Result, error) {
	result.Steps = append(result.Steps, StepResult{
		Name: step.Name, Title: step.Title, Action: ActionRefused, Detail: detail,
	})
	result.Log = append(result.Log, "  "+step.Name+": REFUSED — "+clip(detail, 3))
	return result, fmt.Errorf("cafe box setup %s: %s", result.Host, step.Name)
}

// probeSuperuser asks the one question kamal asks before installing anything.
// It answers with the prefix every other command in the walk carries, so that
// "run this as root" is spelled exactly once rather than in nine commands, and
// with whether the answer was yes at all — a host that cannot get root is
// refused here rather than failing later on a write with a permission error
// that names no cause.
func probeSuperuser(ctx context.Context, host Host) (sudo string, ok bool, err error) {
	out, err := host.Run(ctx, `if `+superuserCheck+`; then echo yes; else echo no; fi`, nil)
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(out.Stdout) != "yes" {
		return "", false, nil
	}
	// Which spelling? A second, read-only question distinguishes root from
	// passwordless sudo, because the prefix the walk carries is different and
	// the difference is visible in every command it runs.
	who, err := host.Run(ctx, `if [ "${EUID:-$(id -u)}" -eq 0 ]; then echo root; else echo sudo; fi`, nil)
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(who.Stdout) == "root" {
		return "", true, nil
	}
	// -H pins HOME to the target user's, which is what makes EnrolledJSON a
	// constant: every cafe command this package runs is root, and root's
	// config directory is where enrollment will write.
	return "sudo -n -H", true, nil
}

// refuseRoot is the first refusal in the walk and the only one that happens
// before any step runs. It uses the plan's own row for the step's title so the
// document reads the same whether the refusal came from the probe or from a
// step.
func refuseRoot(result Result) (Result, error) {
	step := superuserRow()
	result.Steps = append(result.Steps, StepResult{
		Name: StepSuperuser, Title: step.Title, Action: ActionRefused, Detail: step.Refusal,
	})
	result.Log = append(result.Log, "  "+StepSuperuser+": REFUSED — "+clip(step.Refusal, 3))
	return result, fmt.Errorf("cafe box setup %s: %s", result.Host, StepSuperuser)
}

// The two checks kamal's bootstrap runs, spelled the way its own command test
// pins them. They are constants because the walk runs them and the tests assert
// on them, and a third spelling of either is how a check drifts from the
// precedence it is supposed to be.
const (
	superuserCheck   = `[ "${EUID:-$(id -u)}" -eq 0 ] || sudo -nl usermod >/dev/null 2>&1`
	dockerGroupCheck = `id -nG "${USER:-$(id -un)}" | grep -qw docker`
)

// readEnrollment reads the host's own box.json. It is the only fact about the
// box that ever crosses the seam, and it is read before the walk (so the plan
// can name the box) and after it (so the result can report it).
func readEnrollment(ctx context.Context, host Host, sudo string) (*identityRead, Output, error) {
	cmd := "cat " + EnrolledJSON
	if sudo != "" {
		cmd = sudo + " " + cmd
	}
	out, err := host.Run(ctx, cmd, nil)
	if err != nil {
		return nil, Output{}, err
	}
	if !out.OK() {
		return nil, out, nil
	}
	var id identityRead
	if err := json.Unmarshal([]byte(out.Stdout), &id); err != nil || id.BoxID == "" {
		return nil, out, nil
	}
	return &id, out, nil
}

// dryRun renders the plan as a result without running any of it. Every row is
// "planned", which is a third action rather than a reuse of "already-done",
// because a document that cannot tell "nothing was needed" from "nothing was
// attempted" is a document that lies about the one thing an operator reads it
// for.
func dryRun(result Result, steps []Step) Result {
	for _, step := range steps {
		if step.Name == StepSuperuser {
			// The probe already ran and its answer is the one fact a plan
			// needs, so the row is marked planned like the rest rather than
			// already-done: nothing was installed on this run either.
			result.Steps[0].Action = ActionPlanned
			result.Steps[0].Detail = "dry run: the operator can get root; nothing was installed"
			continue
		}
		result.Steps = append(result.Steps, StepResult{
			Name: step.Name, Title: step.Title, Action: ActionPlanned,
			Detail: "dry run: nothing was run on the host",
		})
	}
	result.Log = append(result.Log, "cafe box setup "+result.Host+": dry run; no command was run on the host")
	return result
}

// decodeEnrollment reads the host's own enroll document. A nil answer is a
// document cafe could not read, which the caller turns into a refusal rather
// than a silent success — the box may well be enrolled, but "probably" is not
// a word this command uses about a capability that was spent.
func decodeEnrollment(stdout string) *enrollment {
	var doc enrollmentOf
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		return nil
	}
	e := &enrollment{redemptions: doc.Redemptions}
	e.Action = doc.Action
	if doc.Identity != nil {
		e.BoxID = doc.Identity.BoxID
		e.ControlPlane = doc.Identity.ControlPlane
		e.EnrolledAt = doc.Identity.EnrolledAt
		e.certificate = doc.Identity.Certificate
		e.key = doc.Identity.Key
	}
	if e.Action == "" {
		return nil
	}
	return e
}

// decodeReport reads the daemon's observation document. A nil answer means the
// document did not parse, and the caller reports the daemon's own words rather
// than substituting a zero-valued report — a zero certificate status is a
// claim, and the whole point of the observation schema is that it never makes
// one.
func decodeReport(stdout string) *boxd.Observation {
	var report boxd.Observation
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		return nil
	}
	if report.BoxID == "" && report.Health.Status == "" {
		return nil
	}
	return &report
}

// readBinary reads the cafe binary to install and hashes it. The hash is of the
// BYTES, which is the only spelling that means the same thing on a checkout, a
// release tarball and a Docker build context.
func readBinary(path string) ([]byte, string, error) {
	if path == "" {
		return nil, "", fmt.Errorf("%w: no cafe binary to install was named", ErrNoBinary)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrNoBinary, err)
	}
	if len(raw) == 0 {
		return nil, "", fmt.Errorf("%w: %s is empty", ErrNoBinary, path)
	}
	return raw, sha256Hex(raw), nil
}

// sudoOrRoot names how the run got root, for the log line.
func sudoOrRoot(sudo string) string {
	switch sudo {
	case "":
		return "root"
	case "none":
		return "none"
	default:
		return "passwordless sudo"
	}
}
