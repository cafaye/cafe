package setup

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Wipe is the inverse of Setup: it takes a box back to the state `cafe box
// setup` expects a bare host to be in, and it is built with the same three
// rules.
//
//   - A step is a CHECK and an APPLY, and the check runs first every time. A
//     second `cafe box wipe` on a host this one has already wiped runs no
//     destructive command at all: every check passes, and the document says so.
//   - The wipe is a LIST of anywaye's own artifacts, never an `rm -rf` over a
//     directory somebody else may share. `/var/lib/anywaye` is removed
//     subdirectory by subdirectory — `bin`, `substrate`, `edge` — and never
//     wholesale, because `releases` lives under it and the deploy path owns it.
//   - The operator's own access is never touched. `/root/.ssh` and
//     `/var/lib/anywaye/releases` are named in the result as KEPT, and there is
//     no step in the table that could remove either: a wipe that can lock the
//     operator out is worse than no wipe.
//
// It refuses, rather than guesses, when the host does not look like a box this
// command set up: the box-fingerprint step is the first read-only question, and
// a host with none of anywaye's artifacts is refused before a single removal
// runs. That refusal is the whole reason this is a command and not a script: a
// hand-rolled `rm -rf` has no way to tell a decommissioned box from a
// stranger's k3s.

// ErrNoWipeHost is what a wipe answers when there is nothing to talk to. It is
// a sentinel rather than a nil-panic for the same reason Setup's is: the
// caller's exit code has to distinguish "you did not give me a host" from "the
// host refused".
var ErrNoWipeHost = errors.New("cafe box wipe: no host to wipe")

// ErrWipeDeclined is what the command returns when the operator is asked and
// says no. The sentinel exists so a caller can tell "the operator declined"
// from "the run refused": both exit non-zero, and only one of them is fixed by
// passing --yes.
var ErrWipeDeclined = errors.New("cafe box wipe: the operator did not confirm")

// The step names. They are exported for the same reason setup's are: a caller
// reading one row of a report should not have to spell the string a second
// time.
const (
	// StepWipeFingerprint is the "is this a box cafe set up" question, and it
	// is a refusal rather than a removal.
	StepWipeFingerprint = "box-fingerprint"
	StepWipeDaemon      = "daemon-stopped"
	StepWipeUnit        = "unit-removed"
	StepWipeBinary      = "cafe-binary-removed"
	StepWipeState       = "anywaye-state-removed"
	StepWipeK3s         = "k3s-removed"
	StepWipeDocker      = "docker-removed"
)

// The paths the wipe owns, named once. They are constants rather than string
// literals in the table because the CHECK, the APPLY and the KEPT list all
// have to agree about them, and a second spelling of "/var/lib/anywaye/bin" is
// how a step removes one directory and reports another.
const (
	// EtcCafeDir holds the daemon's enrolled identity. IdentityPath lives
	// inside it, and the whole directory is anywaye's: nothing else writes there.
	EtcCafeDir = "/etc/cafe"
	// EnrolledDir holds the box's non-secret enrollment and its keyring:
	// box.json, box-events.jsonl and the private key. The private key is a
	// credential, so this is the one directory whose removal is also a secret's.
	EnrolledDir = "/root/.config/cafe"
	// KitStatePath is the substrate installer's receipt directory.
	KitStatePath = "/var/lib/kit"
	// AnywayeStateDir is the box's own state root. It is NOT removed wholesale:
	// AnywayeReleases lives under it and is kept, so the wipe names the three
	// subdirectories it owns instead.
	AnywayeStateDir = "/var/lib/anywaye"
	// AnywayeBinDir, AnywayeSubstrate and AnywayeEdge are the three subdirectories
	// of AnywayeStateDir the wipe removes.
	AnywayeBinDir    = "/var/lib/anywaye/bin"
	AnywayeSubstrate = "/var/lib/anywaye/substrate"
	AnywayeEdge      = "/var/lib/anywaye/edge"
	// AnywayeReleases is the deploy path's release directories, and it is KEPT.
	// A release is the operator's deployed artifact, not an installation fact:
	// a wipe that removed it would delete the thing the box was serving.
	AnywayeReleases = "/var/lib/anywaye/releases"
	// SSHAuthorizedKeys is the operator's own access. It is named here only so
	// the KEPT list and the help have one place to point at; no step references
	// it, and a test asserts no command does.
	SSHAuthorizedKeys = "/root/.ssh"
)

// The k3s paths, from a live box: the binary, the three helper scripts the
// installer writes, and the four directories it creates. The kernel's own
// `kubepods-*.slice` units are transient and are gone with k3s; nothing here
// names them.
//
// `/usr/local/bin/kubectl` is DELIBERATELY ABSENT. It is a name every cluster
// tool writes into, and a wipe that removed it would be removing a tool it did
// not install — the same category as the operator's SSH keys. k3s's own killall
// script knows whether it created that symlink and removes it if it did, so the
// uninstall scripts run first and the explicit list stops at names that belong
// to exactly one product.
const (
	k3sBinary         = "/usr/local/bin/k3s"
	k3sKillall        = "/usr/local/bin/k3s-killall.sh"
	k3sUninstall      = "/usr/local/bin/k3s-uninstall.sh"
	k3sAgentUninstall = "/usr/local/bin/k3s-agent-uninstall.sh"

	// RancherEtcDir is k3s's configuration (the cluster's kubeconfig) and
	// RancherLibDir is its data store. The uninstall script removes
	// `/var/lib/rancher/k3s` and leaves the directory above it, which is why
	// the wipe names the directory too.
	RancherEtcDir = "/etc/rancher"
	RancherLibDir = "/var/lib/rancher"
	// KubeletDir is the node's own runtime state.
	KubeletDir = "/var/lib/kubelet"
	// KubeConfigDir is root's kubectl configuration, which k3s writes on its
	// first start. It is k3s's own file, not the operator's: the operator's
	// credentials live under SSHAuthorizedKeys.
	KubeConfigDir = "/root/.kube"

	// DockerDataDir and ContainerdDataDir are the container runtime's state,
	// DockerEtcDir its configuration, and DockerSourceList the apt source the
	// convenience script wrote — so a host cannot reinstall Docker from a stale
	// entry after a wipe.
	DockerDataDir     = "/var/lib/docker"
	ContainerdDataDir = "/var/lib/containerd"
	DockerEtcDir      = "/etc/docker"
	DockerSourceList  = "/etc/apt/sources.list.d/docker.list"
)

// wipeKeptPaths is what the wipe deliberately does not remove, and the help
// says so in as many words. It is a variable rather than a constant so the
// result can copy it without a caller reaching into a package-level slice.
var wipeKeptPaths = []string{SSHAuthorizedKeys, AnywayeReleases}

// wipeRemovablePaths is the CLOSED SET of paths any destructive command in this
// package is allowed to name. It is the answer to "how do you know the wipe
// never touched anything else": a test walks every apply command, takes the
// arguments of every `rm`, and asserts each one is in this list — so a step
// added later with a path of its own cannot quietly widen the blast radius
// without also widening the list the test reads.
//
// It is a function rather than a variable so a caller cannot append to it, and
// so every path in it is spelled by a constant above rather than by a second
// copy of the table.
func wipeRemovablePaths() []string {
	return []string{
		// anywaye's own: the binary, the unit, and the three state directories.
		CafeBinPath, UnitPath, EtcCafeDir, EnrolledDir, KitStatePath,
		AnywayeBinDir, AnywayeSubstrate, AnywayeEdge,
		// k3s's, and only the paths named for k3s.
		k3sBinary, k3sKillall, k3sUninstall, k3sAgentUninstall,
		RancherEtcDir, RancherLibDir, KubeletDir, KubeConfigDir,
		// Docker's.
		DockerDataDir, ContainerdDataDir, DockerEtcDir, DockerSourceList,
	}
}

// WipeOptions is one `cafe box wipe`. It is a struct of parameters for the
// reason Setup's is: a test hands the command a host it owns, and nothing else
// in the process can see it.
type WipeOptions struct {
	// Host is the box, reached over SSH. Nil is refused: a wipe with no host is
	// a wipe that would report success having done nothing.
	Host Host
	// Name is the address as the operator typed it.
	Name string
	// DryRun prints the plan and runs none of it. The read-only probes still
	// run, exactly as setup's do.
	DryRun bool
	// Confirm is asked once, AFTER the read-only probes and BEFORE the first
	// removal, and only when this is not a dry run. NIL MEANS NO CONFIRMATION
	// WAS GIVEN, and the run then refuses: a WIPE with no gate is the shape of
	// the hand-rolled script this command exists to replace, and a library
	// default that destroyed a host because nobody passed a callback would be
	// the same defect with a Go signature on it.
	//
	// The CLI always supplies one. `--yes` supplies one that answers true, and
	// an interactive run supplies one that asks on stdin — the same gate either
	// way, so a pipeline and a person walk the same table.
	//
	// It is here rather than in the CLI because the probes are what tell a box
	// from a stranger's machine, and a prompt that appeared before the
	// fingerprint question would ask an operator to approve wiping a host the
	// command was about to refuse anyway.
	Confirm func(prompt string) (bool, error)
}

// WipeResult is the whole run. It carries no secret: the only thing it says
// about the box's private key is that the directory it lived in is gone.
type WipeResult struct {
	Host   string `json:"host"`
	DryRun bool   `json:"dry_run"`
	// Steps is one row per thing the wipe removed, in the order they ran.
	Steps []StepResult `json:"steps"`
	// Kept is what the wipe deliberately left alone, spelled in the document so
	// an operator reading the output knows the SSH keys and the releases are
	// still there without having to read the help.
	Kept []string `json:"kept"`
	// Sudo names how the run got root: "", "root" or "sudo". Empty on a refused
	// run.
	Sudo string `json:"sudo,omitempty"`
	// Log is the human-readable checkout, one line per step, in order.
	Log []string `json:"log,omitempty"`
}

// OK reports whether every step the wipe attempted concluded.
func (r WipeResult) OK() bool {
	for _, s := range r.Steps {
		if s.Action == ActionRefused {
			return false
		}
	}
	return true
}

// FailedStep names the first step that refused, so the error is one sentence
// and not a table.
func (r WipeResult) FailedStep() string {
	for _, s := range r.Steps {
		if s.Action == ActionRefused {
			return s.Name
		}
	}
	return ""
}

// Wipe takes one box back to a bare host.
//
// It runs in the same three phases as Setup, and the split is the same idea:
//
//  1. PROBE. Can this operator get root, and is this a box cafe set up? Both
//     are read-only, which is why a dry run may run them, and the second is the
//     refusal that keeps a stranger's k3s safe.
//  2. CONFIRM. Once, naming what will be removed and what will be kept. A run
//     with no confirmation gate is refused rather than trusted: a WIPE is the
//     hand-rolled SSH script this command replaces, and the difference between
//     the two is that this one will not run without being asked.
//  3. WALK. The table, in order. Every step checks first; a check that passes
//     runs nothing, which is what makes a second wipe a no-op.
func Wipe(ctx context.Context, o WipeOptions) (WipeResult, error) {
	if o.Host == nil {
		return WipeResult{}, ErrNoWipeHost
	}
	result := WipeResult{Host: o.Name, DryRun: o.DryRun, Kept: append([]string(nil), wipeKeptPaths...)}

	// Phase 1a. Root, the same question setup asks first.
	sudo, canRoot, err := probeSuperuser(ctx, o.Host)
	if err != nil {
		return result, fmt.Errorf("cafe box wipe %s: %w", o.Name, err)
	}
	if !canRoot {
		return refuseWipe(result, superuserRow(), superuserRow().Refusal)
	}
	result.Sudo = sudo

	// Phase 1b. Is this a box cafe set up? A host with none of anywaye's
	// artifacts is refused here, before the confirmation and before any
	// removal, because the alternative is uninstalling a k3s this command did
	// not install.
	found, err := probeBoxFingerprint(ctx, o.Host)
	if err != nil {
		return result, fmt.Errorf("cafe box wipe %s: %w", o.Name, err)
	}
	if len(found) == 0 {
		row := wipeFingerprintRow()
		return refuseWipe(result, row, row.Refusal)
	}
	// The two probes, in the plan's order so the document a dry run prints and
	// the document a real run prints line up row for row.
	result.Steps = append(result.Steps,
		StepResult{Name: StepWipeFingerprint, Title: wipeFingerprintRow().Title,
			Action: ActionAlreadyDone, Detail: "anywaye's own artifacts on this host: " + strings.Join(found, ", ")},
		StepResult{Name: StepSuperuser, Title: superuserRow().Title,
			Action: ActionAlreadyDone, Detail: "this operator can remove root-owned state; got root with " + sudoOrRoot(sudo)},
	)
	result.Log = append(result.Log, "cafe box wipe "+o.Name+": root is "+sudoOrRoot(sudo))
	result.Log = append(result.Log, "  "+StepWipeFingerprint+": this is a box cafe set up: "+strings.Join(found, ", "))

	steps := wipePlan(sudo)
	if o.DryRun {
		return wipeDryRun(result, steps), nil
	}

	// Phase 2. Confirm, once, now that the plan is known to be a plan for a
	// box rather than for a stranger's machine.
	if o.Confirm == nil {
		// No gate, no wipe. The sentinel is the same one a "no" answers with
		// because the operator's response is not the interesting difference:
		// in both cases nothing was removed.
		result.Log = append(result.Log, "cafe box wipe "+o.Name+": nothing was removed; no confirmation was given")
		return wipeDryRun(result, steps), ErrWipeDeclined
	}
	ok, err := o.Confirm(wipePrompt(o.Name, steps))
	if err != nil {
		return result, err
	}
	if !ok {
		return wipeDryRun(result, steps), ErrWipeDeclined
	}

	// Phase 3. The walk. A refusal stops the run: the steps after a failed one
	// are steps whose preconditions are not established. A re-run converges,
	// because every step re-checks.
	for _, step := range steps {
		if step.Name == StepWipeFingerprint || step.Name == StepSuperuser {
			// Already answered in phase 1; asking twice would put a second
			// row of the same question in the log.
			continue
		}
		out, err := o.Host.Run(ctx, step.Check, nil)
		if err != nil {
			return refuseWipe(result, step, "could not ask the host: "+err.Error())
		}
		if out.OK() {
			result.Steps = append(result.Steps, StepResult{
				Name: step.Name, Title: step.Title, Action: ActionAlreadyDone,
				Detail: "the host did not have this; nothing was removed for this step",
			})
			result.Log = append(result.Log, "  "+step.Name+": already gone")
			continue
		}
		applied, err := o.Host.Run(ctx, step.Apply.Command, nil)
		if err != nil {
			return refuseWipe(result, step, "could not run the removal: "+err.Error())
		}
		if !applied.OK() {
			return refuseWipe(result, step, "the removal failed:\n"+clip(applied.Stderr+applied.Stdout, 10))
		}
		// The step's own question, asked again. `rm -f` exits 0 on a path it
		// could not remove, and `apt-get purge` exits 0 on a package that was
		// never there, so an exit status is the command's OPINION about what
		// happened. The check is the host's state, and it is the only authority
		// that can say the artifact is gone. A removal that reported success
		// and left the thing in place is the failure this catches, and it is
		// the reason a second wipe is an honest no-op rather than a run of
		// commands that were always going to be skipped.
		again, err := o.Host.Run(ctx, step.Check, nil)
		if err != nil {
			return refuseWipe(result, step, "removed, but could not ask the host again: "+err.Error())
		}
		if !again.OK() {
			// The check SUCCEEDS when the artifact is gone, so a check that
			// still fails means the removal did not happen.
			return refuseWipe(result, step, "the removal reported success and the artifact is still there:\n"+
				clip(again.Stdout+again.Stderr, 10))
		}
		result.Steps = append(result.Steps, StepResult{
			Name: step.Name, Title: step.Title, Action: ActionApplied,
			Detail: "the host had this; cafe removed it",
		})
		result.Log = append(result.Log, "  "+step.Name+": removed")
	}
	return result, nil
}

// wipePrompt is the sentence the operator is asked to answer. It names every
// path the wipe will remove and every path it will keep, because a prompt that
// says "remove this host?" is a prompt people approve without reading — and the
// one thing an operator must not approve unread is the half that deletes a
// cluster.
//
// The paths are the plan's own constants rather than a sentence written beside
// them, so a step added to the table with a path of its own shows up in the
// question it changed.
func wipePrompt(name string, steps []Step) string {
	return "cafe box wipe " + name + ": remove anywaye's own artifacts, k3s and Docker with their data " +
		"(" + strings.Join(wipeRemovablePaths(), ", ") + ") " +
		"— keeping " + strings.Join(wipeKeptPaths, ", ") + " and everything anywaye did not create. Remove? [y/N] "
}

// wipeFingerprintRow is the first read-only question: is this a box cafe set
// up? It is a row of the plan and not a removal, with a Refusal rather than an
// Apply, for the same reason setup's superuser row has one: the answer decides
// whether the rest of the table is allowed to run at all.
func wipeFingerprintRow() Step {
	return Step{
		Name:  StepWipeFingerprint,
		Title: "this host is a box cafe set up, not a stranger's machine",
		Check: wipeFingerprintProbe,
		Refusal: "no cafe binary, no anywaye state and no enrollment were found on this host, " +
			"so it is not a box `cafe box setup` set up. cafe will not uninstall a k3s or a " +
			"Docker it did not install. NOTHING WAS REMOVED.",
	}
}

// wipePlan is the whole wipe as an ordered table, a function of how the run got
// root and of nothing else — which is why a dry run can print it without
// removing anything and a test can build one and read it.
//
// The order is the dependency order of a teardown: stop the daemon before its
// binary goes, remove anywaye's own state before the substrate directories it
// sits beside, and take Docker last because a half-removed Docker is the one
// state that leaves processes behind. Every step's check makes the order
// forgiving of a re-run: a wipe that died at k3s converges on the next attempt
// because the steps before it pass their checks.
func wipePlan(sudo string) []Step {
	root := func(cmd string) string {
		if sudo == "" {
			return cmd
		}
		return sudo + " " + cmd
	}
	return []Step{
		wipeFingerprintRow(),
		superuserRow(),
		{
			Name:  StepWipeDaemon,
			Title: "the box daemon is stopped and disabled",
			// The check passes when the unit is neither enabled nor active,
			// which is also true when the unit file is already gone — so a
			// re-run passes here without running systemctl against a unit that
			// does not exist.
			Check: root(wipeDaemonCheck),
			Apply: apply{Command: root(wipeDaemonApply)},
		},
		{
			Name:  StepWipeUnit,
			Title: "the daemon's systemd unit is removed",
			Check: root(wipeUnitCheck),
			// daemon-reload is hygiene, not the removal: the file is what the
			// next boot reads, and it is gone either way. It is folded into
			// this apply so the unit cannot be left loaded over a file that no
			// longer exists.
			Apply: apply{Command: root(wipeUnitApply)},
		},
		{
			Name:  StepWipeBinary,
			Title: "the cafe binary at " + CafeBinPath + " is removed",
			Check: root(wipeBinaryCheck),
			Apply: apply{Command: root(wipeBinaryApply)},
		},
		{
			Name:  StepWipeState,
			Title: "anywaye's state is removed, keeping " + AnywayeReleases,
			Check: root(wipeStateCheck),
			// The exact list, never `rm -rf /var/lib/anywaye`: releases lives
			// under it and is the operator's deployed artifact.
			Apply: apply{Command: root(wipeStateApply)},
		},
		{
			Name:  StepWipeK3s,
			Title: "k3s is uninstalled and its directories removed",
			Check: root(wipeK3sCheck),
			// The installer's own uninstall script first — it knows about the
			// data store, the units and the CNI the directories alone do not —
			// then the directories and the four files it left in /usr/local/bin.
			Apply: apply{Command: root(wipeK3sApply)},
		},
		{
			Name:  StepWipeDocker,
			Title: "Docker is uninstalled and its data removed",
			Check: root(wipeDockerCheck),
			Apply: apply{Command: root(wipeDockerApply)},
		},
	}
}

// wipeFingerprintProbe answers "is this a box cafe set up?" by PRINTING every
// anywaye artifact it finds, one path per line, and nothing when there are none.
// It counts the artifacts the wipe would remove plus the releases directory it
// keeps: a box whose only remaining anywaye artifact is its release history is
// still a box, which is what makes the second wipe of an already-wiped host an
// idempotent no-op rather than a refusal.
//
// It prints the paths rather than a yes/no because the fingerprint row reports
// them: "not a box" is a better sentence when it can also say "and I looked for
// exactly these five things". The leading comment is a marker the CLI-level
// simulator matches on, so the two cannot drift onto different commands.
const wipeFingerprintProbe = `# cafe-box-wipe-fingerprint
for p in ` + CafeBinPath + ` ` + EtcCafeDir + ` ` + EnrolledDir + ` ` + KitStatePath + ` ` + AnywayeStateDir + `; do [ -e "$p" ] && echo "$p"; done
true`

// The destructive steps. The check succeeds when the thing is ALREADY gone, so
// the walk's "a passing check runs nothing" rule reads the same way it does in
// setup: pass means done.
const (
	wipeDaemonCheck = `! systemctl is-enabled ` + UnitName + ` >/dev/null 2>&1 && ! systemctl is-active ` + UnitName + ` >/dev/null 2>&1`
	wipeDaemonApply = `systemctl disable --now ` + UnitName

	wipeUnitCheck = `test ! -e ` + UnitPath
	wipeUnitApply = `rm -f -- ` + UnitPath + ` && systemctl daemon-reload`

	wipeBinaryCheck = `test ! -e ` + CafeBinPath
	wipeBinaryApply = `rm -f -- ` + CafeBinPath

	wipeStateCheck = `test ! -e ` + EtcCafeDir + ` && test ! -e ` + EnrolledDir + ` && test ! -e ` + KitStatePath +
		` && test ! -e ` + AnywayeBinDir + ` && test ! -e ` + AnywayeSubstrate + ` && test ! -e ` + AnywayeEdge
	wipeStateApply = `rm -rf -- ` + EtcCafeDir + ` ` + EnrolledDir + ` ` + KitStatePath + ` ` +
		AnywayeBinDir + ` ` + AnywayeSubstrate + ` ` + AnywayeEdge

	// The k3s check asks whether k3s is present at all, and it deliberately
	// does NOT ask about `~/.kube`: a stray kubeconfig on a host with no k3s is
	// the operator's own file, and a check that named it would make a host
	// whose only leftover is one config file look like a host with a cluster on
	// it. When k3s IS present its kubeconfig goes with it.
	wipeK3sCheck = `test ! -e ` + k3sBinary + ` && test ! -e ` + RancherEtcDir +
		` && test ! -e ` + RancherLibDir + ` && test ! -e ` + KubeletDir
	// The installer's own uninstall script first — it knows about the service
	// units, the data store and the CNI that the directories alone do not —
	// then the directories and the k3s files the installer left in
	// /usr/local/bin. Each script is run only when it is there and executable,
	// and a failure in one does not stop the removals after it: the guarantee
	// is that nothing k3s wrote is left, and the step's own re-check answers
	// for it.
	wipeK3sApply = `if [ -x ` + k3sUninstall + ` ]; then ` + k3sUninstall + `; fi; ` +
		`if [ -x ` + k3sAgentUninstall + ` ]; then ` + k3sAgentUninstall + `; fi; ` +
		`rm -rf -- ` + RancherEtcDir + ` ` + RancherLibDir + ` ` + KubeletDir + ` ` + KubeConfigDir + ` ` +
		k3sBinary + ` ` + k3sKillall + ` ` + k3sUninstall + ` ` + k3sAgentUninstall

	// The Docker the setup command installs is the convenience script's, which
	// is apt on every distribution this command targets; the purge names the
	// packages that script installs, including the plugins. A `|| true` on
	// each half is deliberate: the guarantee is the rm that follows, and a
	// purge that fails because a package was never installed must not stop the
	// data directories from going. The service stop is first so the daemon is
	// not writing into the directories as they are removed.
	wipeDockerCheck = `! command -v docker >/dev/null 2>&1 && test ! -e ` + DockerEtcDir +
		` && test ! -e ` + DockerDataDir + ` && test ! -e ` + ContainerdDataDir
	wipeDockerApply = `systemctl disable --now docker docker.socket containerd >/dev/null 2>&1 || true; ` +
		`if command -v apt-get >/dev/null 2>&1; then ` +
		`apt-get purge -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin docker-model-plugin >/dev/null 2>&1 || true; ` +
		`fi; ` +
		`rm -rf -- ` + DockerDataDir + ` ` + ContainerdDataDir + ` ` + DockerEtcDir + `; ` +
		`rm -f -- ` + DockerSourceList
)

// probeBoxFingerprint asks the host the one question that decides whether the
// rest of the table is allowed to run, and returns the artifact paths it found.
// An empty answer is a host that is not a box this command set up.
func probeBoxFingerprint(ctx context.Context, host Host) ([]string, error) {
	out, err := host.Run(ctx, wipeFingerprintProbe, nil)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, line := range strings.Split(out.Stdout, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			found = append(found, line)
		}
	}
	return found, nil
}

// wipeDryRun renders the plan as a result without running any of it, and it is
// the document a DECLINED confirmation returns too: in both cases nothing was
// removed and every step is "planned" rather than "already-done", because a
// document that cannot tell "nothing was needed" from "nothing was attempted"
// is a document that lies about the one thing an operator reads it for.
func wipeDryRun(result WipeResult, steps []Step) WipeResult {
	result.Steps = result.Steps[:0]
	for _, step := range steps {
		result.Steps = append(result.Steps, StepResult{
			Name: step.Name, Title: step.Title, Action: ActionPlanned,
			Detail: "nothing was removed for this step",
		})
	}
	result.Log = append(result.Log, "cafe box wipe "+result.Host+": nothing was removed")
	return result
}

// refuseWipe records the step that stopped the run and returns the result
// beside the error, so a caller can print the document AND the sentence.
func refuseWipe(result WipeResult, step Step, detail string) (WipeResult, error) {
	result.Steps = append(result.Steps, StepResult{
		Name: step.Name, Title: step.Title, Action: ActionRefused, Detail: detail,
	})
	result.Log = append(result.Log, "  "+step.Name+": REFUSED — "+clip(detail, 3))
	return result, fmt.Errorf("cafe box wipe %s: %s", result.Host, step.Name)
}
