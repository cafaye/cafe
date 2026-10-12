package setup

import (
	"bytes"
	"context"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// The architecture preflight: the one question whose answer decides WHICH file
// crosses, asked after the superuser probe and before anything is written.
//
// The defect this file exists for, measured live. `cafe box setup contabo`
// from a Mac (darwin/arm64) ran its nine steps, wrote the DARWIN binary to the
// Linux host's /usr/local/bin/cafe, and refused at box-enrolled with "Exec
// format error". No capability was spent — the refusal happened before
// presentation, so the trust held — but the operator's log named a downstream
// symptom ("cannot execute binary file") instead of the cause, and the repair
// that followed was aimed at Docker.
//
// The rule this file implements: a controller that cannot install the right
// architecture SAYS SO, and names the one command that produces the right
// binary, before a byte crosses. Three answers, in order of preference:
//
//   - the controller's own build is the host's platform, so it ships;
//   - otherwise a `cafe-<goos>-<goarch>` beside the running binary ships, and
//     the log says which path was chosen and why;
//   - otherwise the run REFUSES, naming the controller's platform, the host's
//     and the build command. Nothing is written and no capability is
//     presented.
//
// It is a probe AND a row. The probe runs in phase 1 because it decides which
// BYTES cross — `plan` carries the binary and the hash of it, and neither is
// known until the question is answered — and the plan carries the row so the
// table still holds a question the walk can ask and refuse. A decision that
// lived only in the flow would be a decision the table could not refuse.

// archProbe is the one question the preflight asks the host: what is it.
// `-sm` is one word because it is one question, and both flags are in the GNU
// coreutils, BusyBox and Alpine spellings — the three shapes a box this
// command targets can be. The kernel name is asked for as well as the machine
// because the sentence this packet owes the operator names a GOOS and a
// GOARCH, and one sentence should not cost two connections.
const archProbe = `uname -sm`

// target is one GOOS/GOARCH pair: the platform a binary was built for, or the
// platform a host runs. It is a comparable value rather than two strings
// because every question this file asks is an equality — "can I ship this to
// that" — and a struct that could not be compared would invite a spelling of
// the same question as two conditions that could disagree.
type target struct {
	GOOS   string
	GOARCH string
}

// String is "linux/amd64", the spelling a build command uses and a log reads.
func (t target) String() string { return t.GOOS + "/" + t.GOARCH }

// controllerTarget is the platform the RUNNING binary was built for. It is a
// function rather than a constant because a constant would be a package-level
// fact a test could not vary, and the case this packet is about is precisely
// the one where the controller is not the host.
func controllerTarget() target { return target{runtime.GOOS, runtime.GOARCH} }

// What `uname` says, and what Go calls it. The two spellings differ for every
// architecture that matters here — a host says aarch64 where a build command
// says arm64 — and the translation has to be one table rather than a
// conditional chain in the middle of the flow, because a missing row must be a
// REFUSAL rather than a guess: a controller that invented "arm64" for an
// "armv5tel" host would ship a binary the kernel cannot run, which is the
// defect again in a quieter colour.
var unameToGOARCH = map[string]string{
	"x86_64":      "amd64",
	"i386":        "386",
	"i486":        "386",
	"i586":        "386",
	"i686":        "386",
	"aarch64":     "arm64",
	"arm64":       "arm64",
	"armv5tel":    "arm",
	"armv6l":      "arm",
	"armv7l":      "arm",
	"armv8l":      "arm64",
	"ppc64le":     "ppc64le",
	"s390x":       "s390x",
	"riscv64":     "riscv64",
	"loongarch64": "loong64",
	"mips64le":    "mips64le",
	"mipsle":      "mipsle",
}

// The operating system. Two rows, and what is NOT here matters: a box is
// linux, a controller is occasionally darwin, and anything else — FreeBSD,
// SunOS, the BSDs behind an Alpine chroot — is refused with its own name in
// the message rather than mapped onto the nearest thing. The keys are lower
// case because the lookup lower-cases what the host said: a host that answers
// "LINUX" is still Linux, and a table that only knew one capitalisation would
// refuse a box that was settable all along.
var unameToGOOS = map[string]string{
	"linux":  "linux",
	"darwin": "darwin",
}

// hostArch is the host's own answer, and what it means. Answer is kept
// verbatim because the refusal that has nothing else to quote is the one that
// needs it most: "the host did not say" and "the host said something cafe
// cannot build for" are different remedies.
type hostArch struct {
	Answer string
	Target target
	// OK says the answer mapped onto a Go release target. A host whose uname
	// this package has no row for is a host it cannot build for, and the
	// preflight says so rather than shipping its own architecture.
	OK bool
}

// probeArch asks the host what it is. It is read-only, it runs before a byte
// crosses, and a transport failure is an error rather than a refusal: "ssh
// refused" and "this is not a machine I can build for" are different
// sentences to an operator, and only one of them is fixed by building a
// binary.
func probeArch(ctx context.Context, host Host) (hostArch, error) {
	out, err := host.Run(ctx, archProbe, nil)
	if err != nil {
		return hostArch{}, err
	}
	answer := strings.TrimSpace(out.Stdout)
	if !out.OK() || answer == "" {
		return hostArch{Answer: answer}, fmt.Errorf(
			"the host did not answer `%s`: exit %d, %s",
			archProbe, out.Code, strings.TrimSpace(out.Stderr))
	}
	got := hostArch{Answer: answer}
	fields := strings.Fields(answer)
	if len(fields) == 2 {
		got.Target.GOOS = unameToGOOS[strings.ToLower(fields[0])]
		got.Target.GOARCH = unameToGOARCH[strings.ToLower(fields[1])]
		got.OK = got.Target.GOOS != "" && got.Target.GOARCH != ""
	}
	return got, nil
}

// The three things the preflight can conclude. They are strings because the
// one that reaches the document is the reason a binary was chosen, and a
// reason nobody can grep for is a reason nobody reads twice.
const (
	// archShipLocal means the running binary is built for this host.
	archShipLocal = "ship-local"
	// archShipSibling means a cross-built binary beside the running one is.
	archShipSibling = "ship-sibling"
	// archRefuse means neither is, and nothing crosses.
	archRefuse = "refuse"
)

// archPick is the preflight's decision: which bytes cross the SSH connection,
// and the sentence a person reads about why. It is a value rather than an
// error because two of its three outcomes are successes with different
// reasons, and an error type that had to carry "but also here are the bytes"
// would be an error that could be mistaken for a failure.
type archPick struct {
	Action string
	// Binary is the bytes that will be written to the host, and Hash their
	// sha256. Nil on a refusal, which is the property the whole packet is
	// for: a refusal carries no bytes at all.
	Binary []byte
	Hash   string
	// Path is the local file the bytes came from, for the log.
	Path string
	// Platform is the pair the bytes were built for. It is carried rather than
	// recomputed from the path because the path is a string and the pair is a
	// fact, and the two could disagree.
	Platform target
	// Reason is the log line: which path was chosen and why.
	Reason string
	// Refusal is the remedy sentence, set only for archRefuse.
	Refusal string
}

// Ships reports whether anything crosses at all. The negative control for
// this whole packet is the assertion that a refusal ships NOTHING, and this
// is the predicate that assertion is written against.
func (p archPick) Ships() bool { return p.Action == archShipLocal || p.Action == archShipSibling }

// Arch is what the preflight resolved, in the document: which binary crossed
// and why it was the right one. It is in the Result because the one thing an
// operator wants after a setup is "which architecture is on that box", and
// because a field that is present is a field that cannot be quietly
// forgotten.
type Arch struct {
	// Host is what the host answered, verbatim: "Linux x86_64".
	Host string `json:"host"`
	// Controller is what this controller's own build is, the pair `runtime`
	// reports. It is NOT necessarily what shipped, and that difference is the
	// whole packet.
	Controller string `json:"controller"`
	// Platform is the pair the shipped binary is built for. It is always the
	// host's: a file for any other platform is not shipped.
	Platform string `json:"platform"`
	// Path is the local file that crossed.
	Path string `json:"path"`
	// Sibling reports whether a cross-built binary beside the running one
	// shipped instead of the running binary. It is a field rather than an
	// inference from Path because Path is whatever os.Executable returned,
	// and a caller comparing strings would be re-implementing the decision.
	Sibling bool `json:"sibling"`
	// Reason is the log's own words, kept in the document so a machine has
	// the sentence a person read.
	Reason string `json:"reason"`
}

// document is the pick as the result carries it. Nil for a refusal, because a
// refusal is a document about what did NOT happen and the steps already say
// that.
func (p archPick) document(controller target, host hostArch) *Arch {
	if !p.Ships() {
		return nil
	}
	return &Arch{
		Host:       host.Answer,
		Controller: controller.String(),
		Platform:   p.Platform.String(),
		Path:       p.Path,
		Sibling:    p.Action == archShipSibling,
		Reason:     p.Reason,
	}
}

// siblingName is the one spelling a cross-built binary carries next to the
// running one. One spelling, not several: `cafe-linux-amd64` is the name the
// refusal tells the operator to build, and a second accepted spelling would
// be a second thing a build script could get wrong.
func siblingName(t target) string { return "cafe-" + t.GOOS + "-" + t.GOARCH }

// decideBinary decides which local file the setup installs.
//
// It is a FUNCTION of the controller's platform, the host's answer and the
// local path, with no runtime dependency on the machine it runs on, which is
// what makes the three outcomes a table a test drives without an SSH host, a
// container or a cross-compiler.
//
// The order is the brief's, and each step is exhausted before the next is
// tried:
//
//   - the running binary matches the host: ship it, nothing else to decide.
//   - otherwise, look beside it for `cafe-<goos>-<goarch>` and ship that if
//     its BYTES are for the host.
//   - otherwise REFUSE, naming the controller's arch, the host's, and the one
//     command that produces the right binary.
func decideBinary(controller target, host hostArch, localPath string) (archPick, error) {
	local, localHash, err := readBinary(localPath)
	if err != nil {
		return archPick{}, err
	}
	if !host.OK {
		return archPick{Action: archRefuse, Refusal: unreadableArchRefusal(controller, host, localPath)}, nil
	}
	if controller == host.Target {
		return archPick{
			Action:   archShipLocal,
			Binary:   local,
			Hash:     localHash,
			Path:     localPath,
			Platform: host.Target,
			Reason:   fmt.Sprintf("shipping %s (%s): it is built for this host", localPath, controller),
		}, nil
	}
	sibling := siblingName(host.Target)
	path := filepath.Join(filepath.Dir(localPath), sibling)
	raw, err := os.ReadFile(path)
	if err == nil && len(raw) > 0 {
		// The NAME says linux/amd64. These are the bytes' own words: a file
		// that declares one platform and carries another is the mistake this
		// packet exists for, and shipping it would reproduce the defect with
		// a plausible-looking log line over it.
		got, ok := fileTarget(raw)
		if ok && got == host.Target {
			return archPick{
				Action:   archShipSibling,
				Binary:   raw,
				Hash:     sha256Hex(raw),
				Path:     path,
				Platform: host.Target,
				Reason: fmt.Sprintf("shipping %s (%s): the running binary %s is built for %s, and this host runs %s",
					path, host.Target, localPath, controller, host.Target),
			}, nil
		}
	}
	return archPick{Action: archRefuse, Refusal: archRefusal(controller, host, localPath)}, nil
}

// archRefusal is the remedy sentence for the case this packet was measured on:
// the controller holds no binary for the host. It names the two platforms AND
// the one command that produces the right one, because "wrong architecture"
// on its own is a fact with no action in it.
func archRefusal(controller target, host hostArch, localPath string) string {
	sibling := siblingName(host.Target)
	where := filepath.Join(filepath.Dir(localPath), sibling)
	return fmt.Sprintf(
		"this controller's cafe is built for %s and the host runs %s, so there is no binary on this machine that can run there.\n\n"+
			"Build the host's own architecture and put it beside the running binary:\n\n"+
			"  GOOS=%s GOARCH=%s go build -o %s ./cmd/cafe\n\n"+
			"cafe box setup looks for %s next to %s and ships it instead of the running binary. "+
			"Nothing was installed on this host and no capability was presented.",
		controller, host.Target, host.Target.GOOS, host.Target.GOARCH, where, sibling, localPath)
}

// unreadableArchRefusal is the same shape for the host whose uname this
// package has no row for. It names what the host actually said, because the
// remedy for "loongarch64" (build one) and for a command that printed nothing
// at all (look at the transport) are not the same remedy.
func unreadableArchRefusal(controller target, host hostArch, localPath string) string {
	answer := host.Answer
	if answer == "" {
		answer = "(no answer)"
	}
	return fmt.Sprintf(
		"this controller's cafe is built for %s and could not work out what the host runs: `%s` answered %q.\n\n"+
			"cafe builds for the architectures in its uname table, and this one is not among them. "+
			"If the host is a platform cafe supports, the answer above is malformed — check how ssh reaches it. "+
			"Otherwise there is nothing to install: a binary built for %s will not run there either.\n\n"+
			"Nothing was installed on this host and no capability was presented.",
		controller, archProbe, answer, controller)
}

// fileTarget reads a binary's own bytes and reports what it was built for.
//
// This is the load-bearing half of the sibling check. A filename is a CLAIM —
// a person typing `GOOS=linux GOARCH=amd64 go build -o cafe-linux-amd64` and
// then overwriting that file with the wrong output is exactly the mistake this
// packet exists for — so the bytes are read and the header decides. If the
// header disagrees with the name, the sibling is not used at all.
//
// Three standard-library readers, because the three object formats are the
// three platforms cafe ships: ELF and Mach-O carry a machine field, and PE
// carries its own. Anything else — a shell script wrapper, a truncated
// download, a text file somebody left in the build directory — is not a
// binary and is not shipped.
func fileTarget(raw []byte) (target, bool) {
	switch {
	case bytes.HasPrefix(raw, []byte("\x7fELF")):
		return elfTarget(raw)
	case bytes.HasPrefix(raw, []byte("\xfe\xed\xfa\xce")),
		bytes.HasPrefix(raw, []byte("\xfe\xed\xfa\xcf")),
		bytes.HasPrefix(raw, []byte("\xce\xfa\xed\xfe")),
		bytes.HasPrefix(raw, []byte("\xcf\xfa\xed\xfe")):
		return machoTarget(raw)
	case bytes.HasPrefix(raw, []byte("MZ")):
		return peTarget(raw)
	}
	return target{}, false
}

// elfTarget reads an ELF header's machine word. The OS is linux because ELF
// carries no operating-system field Go sets — and because the SIBLING is named
// `cafe-linux-<arch>`, a non-linux ELF can never be reached here: the name was
// already required to match the host before its bytes were read.
func elfTarget(raw []byte) (target, bool) {
	f, err := elf.NewFile(bytes.NewReader(raw))
	if err != nil {
		return target{}, false
	}
	switch f.Machine {
	case elf.EM_X86_64:
		return target{"linux", "amd64"}, true
	case elf.EM_AARCH64:
		return target{"linux", "arm64"}, true
	case elf.EM_386:
		return target{"linux", "386"}, true
	case elf.EM_ARM:
		return target{"linux", "arm"}, true
	case elf.EM_S390:
		return target{"linux", "s390x"}, true
	case elf.EM_RISCV:
		return target{"linux", "riscv64"}, true
	case elf.EM_LOONGARCH:
		return target{"linux", "loong64"}, true
	}
	return target{}, false
}

// machoTarget reads a Mach-O header's CPU field. Mach-O is darwin's, on this
// platform and in this repository, so that is what it reports.
func machoTarget(raw []byte) (target, bool) {
	f, err := macho.NewFile(bytes.NewReader(raw))
	if err != nil {
		return target{}, false
	}
	switch f.Cpu {
	case macho.CpuAmd64:
		return target{"darwin", "amd64"}, true
	case macho.CpuArm64:
		return target{"darwin", "arm64"}, true
	case macho.CpuArm:
		return target{"darwin", "arm"}, true
	}
	return target{}, false
}

// peTarget reads a PE header's machine word. Windows has no box, but its
// header is recognised so that a `cafe-windows-amd64` on a build host is not
// mistaken for something shippable.
func peTarget(raw []byte) (target, bool) {
	f, err := pe.NewFile(bytes.NewReader(raw))
	if err != nil {
		return target{}, false
	}
	switch f.Machine {
	case pe.IMAGE_FILE_MACHINE_AMD64:
		return target{"windows", "amd64"}, true
	case pe.IMAGE_FILE_MACHINE_ARM64:
		return target{"windows", "arm64"}, true
	case pe.IMAGE_FILE_MACHINE_I386:
		return target{"windows", "386"}, true
	}
	return target{}, false
}

// refuseArch records the preflight's refusal as a STEP with ActionRefused and
// returns the error beside the result, so a caller can print the document AND
// the sentence. It uses the plan's own row for the step's title so the
// document reads the same whether the refusal came from the preflight or from
// the row in the walk — the same reason refuseRoot borrows the superuser row.
//
// The step is the point: a sentence on stderr is a sentence a script cannot
// act on, and this one has to be the row that makes `cafe box setup` exit
// non-zero before a byte crosses.
func refuseArch(result Result, arch archFact) (Result, error) {
	step := archRow(arch)
	result.Steps = append(result.Steps, StepResult{
		Name: step.Name, Title: step.Title, Action: ActionRefused, Detail: step.Refusal,
	})
	result.Log = append(result.Log, "  "+step.Name+": REFUSED — "+clip(step.Refusal, 3))
	return result, fmt.Errorf("cafe box setup %s: %s", result.Host, step.Name)
}
