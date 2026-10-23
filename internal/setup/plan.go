package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/anywaye/cafe/internal/boxd"
)

// The paths this package owns on the host. They are constants rather than
// options because there is exactly one cafe on a box, and a second spelling of
// "where the daemon's identity lives" is how a unit file ends up pointing at a
// file nothing wrote.
const (
	// CafeBinPath is where the binary is installed. /usr/local/bin is in root's
	// PATH on every distribution this command targets and needs no profile
	// edit, which is the whole reason it is not ~/.local/bin.
	CafeBinPath = "/usr/local/bin/cafe"
	// UnitPath is the systemd unit. /etc/systemd/system rather than
	// /lib/systemd/system because this machine wrote the file: a package's
	// directory is a package's to overwrite.
	UnitPath = "/etc/systemd/system/cafe-boxd.service"
	// IdentityPath is the enrolled identity the daemon reads. It is in
	// /etc/cafe, not beside enrollment's box.json, because the daemon runs as
	// root and the operator who enrolled may not be root — and a unit file
	// that depends on which human happened to run the setup is a unit file
	// that breaks the first time somebody else does.
	IdentityPath = "/etc/cafe/box-identity.json"
	// EnrolledJSON is where `cafe box enroll` writes the box's non-secrets when
	// it runs as root. Enrollment resolves the config directory from the
	// environment it is given, so every cafe command this package runs is
	// given `-H`, which pins HOME to the target user's — root, here, because
	// the daemon is root.
	EnrolledJSON = "/root/.config/cafe/box.json"
	// UnitName is the systemd unit's name.
	UnitName = "cafe-boxd"
)

// The step names. They are exported because a caller that wants to look at one
// row of a report — the enrollment, the heartbeat — should not have to spell
// the string a second time.
const (
	StepSuperuser       = "superuser"
	StepHostArch        = "host-arch"
	StepDockerInstalled = "docker-installed"
	StepDockerRunning   = "docker-running"
	StepDockerGroup     = "docker-group"
	StepCafeInstalled   = "cafe-installed"
	StepBoxEnrolled     = "box-enrolled"
	StepBoxdIdentity    = "boxd-identity"
	StepBoxdUnit        = "boxd-unit"
	StepBoxdReport      = "boxd-report"
)

// The three things a step can conclude. They are strings because they go into a
// document a machine reads, and because "the host already had this" and "this
// command did it" are different facts to an operator reading a log.
const (
	// ActionAlreadyDone means the check passed and nothing was run.
	ActionAlreadyDone = "already-done"
	// ActionApplied means the check failed and the apply ran and succeeded.
	ActionApplied = "applied"
	// ActionRefused means the check failed and there was nothing to apply, or
	// the apply failed. The setup stops here and names the step.
	ActionRefused = "refused"
	// ActionPlanned means a dry run printed this step and ran nothing. It is a
	// third word rather than a reuse of "already-done" because a document that
	// cannot tell "nothing was needed" from "nothing was attempted" is a
	// document that lies about the one thing an operator reads it for.
	ActionPlanned = "planned"
)

// apply is what a step does when its check says the thing is missing.
type apply struct {
	// Command is the shell to run on the host.
	Command string
	// Stdin is handed to Command on standard input. This is the ONE place a
	// capability is allowed to travel to the host.
	Stdin []byte
	// Path is a file to write on the host, and Data is what it receives. Zero
	// Mode means 0644. A step may carry both a Path and a Command — the unit
	// file has to exist before systemctl can reload it — and when it does, the
	// write happens first, so a command never runs against a file that is not
	// there yet.
	Path string
	// Data is what Path receives.
	Data []byte
	// Mode is the permission Path is written with. Zero means 0644.
	Mode os.FileMode
}

// Step is one thing the host needs, with the question that decides whether it
// needs doing. The table is DATA — see plan — and this is its row.
type Step struct {
	Name  string
	Title string
	// Check is a shell command whose exit status answers "is this already
	// done?". It must be read-only: it runs on EVERY setup, including the
	// second one on a host that needs nothing.
	Check string
	// Apply is what runs when Check fails. The zero value means there is
	// nothing to apply, and a failed check is then a refusal rather than a
	// skip — see Refusal.
	Apply apply
	// Refusal is the sentence a check failure answers with when Apply is the
	// zero value. It names what is wrong and what fixes it.
	Refusal string
	// Report marks the step whose check output is the box's observation
	// document, which the verification half of the run reads.
	Report bool
}

// superuserRow is the first step, hoisted out of the table for one reason:
// refuseRoot needs it before a plan exists. The superuser refusal is the only
// one that happens in phase 1, when the architecture has not been asked and
// there is no binary to name, so building a whole plan to read one row off it
// would be a plan built from facts nobody had.
func superuserRow() Step {
	return Step{
		Name:  StepSuperuser,
		Title: "this operator can install packages and write to /etc",
		// Kamal's superuser?, spelled the way its test pins it: root, or a
		// sudo that may run usermod without a password. It asks about
		// `usermod` rather than `true` because that is the command the
		// install path actually needs, and a check that asked a different
		// question would pass on a host the install then failed on.
		Check: superuserCheck,
		Refusal: "installing Docker, writing to /usr/local/bin and enabling a systemd " +
			"unit all need root; this operator is not root and has no passwordless sudo. " +
			"Grant passwordless sudo for the install commands, or run this as root.",
	}
}

// archFact is what the preflight learned, as the plan needs it: the host's own
// answer to `uname -sm`, and the remedy for the case where nothing here can run
// there. It is the only fact in the plan that came from a command run OUTSIDE
// the walk, which is why it is a row of its own rather than a closure over a
// probe the table cannot do for itself.
type archFact struct {
	// Answer is the host's own words, e.g. "Linux x86_64".
	Answer string
	// Refusal names the controller's arch, the host's, and the one command
	// that produces the right binary.
	Refusal string
}

// archRow is the architecture preflight as a step. Its check asks the SAME
// question the preflight asked and compares against the answer the host
// already gave: not because a host changes its architecture mid-run, but
// because a row that could not fail would be a row that was not asking
// anything — and the table's own rule is that every row is a question.
func archRow(arch archFact) Step {
	return Step{
		Name:  StepHostArch,
		Title: "this host's architecture has a cafe binary this controller can ship",
		Check: `[ "$(` + archProbe + `)" = "` + arch.Answer + `" ]`,
		// No apply: there is nothing to install here. The refusal is the
		// whole content of the step, and the preflight that resolved the bytes
		// writes it. A step that had an apply would be a step that tried to
		// make the host a different machine.
		Refusal: arch.Refusal,
	}
}

// plan is the whole setup as an ordered table. It is a function of the facts
// that vary — which sudo spelling the host needs, the local binary's hash, the
// box already on the host, what the architecture preflight resolved — and of
// nothing else, which is why a dry run can print it without running anything
// and why a test can build one and read it.
//
// The order is the dependency order and it is not negotiable: Docker before the
// daemon, the daemon after its identity, the identity after the enrollment, the
// heartbeat last because it is the only step that proves all the others landed.
// The architecture row sits between the superuser probe and cafe-installed
// because it is the last question whose answer changes WHICH BYTES cross — a
// setup that installed Docker first and then refused the architecture would
// have spent a package install learning something it could have asked before
// touching the host.
func plan(o Options, sudo, binaryHash string, binary []byte, enrolled *identityRead, arch archFact) []Step {
	root := func(cmd string) string { return rooted(sudo, cmd) }
	return []Step{
		superuserRow(),
		archRow(arch),
		{
			Name:  StepDockerInstalled,
			Title: "Docker is installed",
			// Kamal's installed?: the CLIENT. `docker -v` succeeds on a box whose
			// socket is not mounted, which is why the daemon is a separate
			// question below.
			Check: "docker -v",
			// Kamal's install, with wget as the fallback and `sh` as the
			// interpreter: the convenience script is upstream's, and this
			// command is not the place to second-guess it.
			Apply: apply{Command: "{ curl -fsSL https://get.docker.com || wget -qO - https://get.docker.com; } | sh"},
		},
		{
			Name:  StepDockerGroup,
			Title: "the login user can use Docker without sudo",
			// Kamal's bootstrap order: root? OR in_docker_group?, and
			// add_to_docker_group only when neither holds. Root does not need
			// the docker group — it can already reach the socket — so a check
			// that asked only "is the login user in the group" would edit
			// /etc/group on every root box.
			Check: `[ "${EUID:-$(id -u)}" -eq 0 ] || ` + dockerGroupCheck,
			Apply: apply{Command: `sudo -n usermod -aG docker "${USER:-$(id -un)}"`},
			// Kamal follows this with refresh_session (`kill -HUP $PPID`),
			// which refreshes the CALLING session's group membership. Over SSH
			// every command is its own session and each reads /etc/group at
			// login, so there is no session of ours to refresh: the group takes
			// effect on the next connection, by itself.
		},
		{
			Name:  StepDockerRunning,
			Title: "the Docker daemon is running",
			// `docker -v` is the client; `docker version` asks the daemon as
			// well, and the difference between them is the one kamal's
			// docker.rb draws. Kamal asks this at deploy time, when it creates
			// the kamal network; asking it here means a box that reports
			// success cannot be a box whose socket never came up.
			Check: "docker version",
			Apply: apply{Command: "systemctl enable --now docker 2>/dev/null || service docker start"},
		},
		{
			Name:  StepCafeInstalled,
			Title: "cafe is installed at " + CafeBinPath,
			// The hash of the BYTES, not a version string: two builds of one
			// version are not the same binary, and the thing this check is for
			// is "is the binary on this host the one I am holding". It runs
			// under the root prefix because the write does, and a check that
			// could read a file the apply could not write would be a check
			// that passed on a host the install then failed on.
			//
			// The probe BEFORE the hash is the architecture guard's second
			// half. `sha256sum` reads bytes and does not care what they are:
			// a darwin binary on a linux box is `-x` and it hashes to
			// something, so the hash check alone reports "a different
			// version" — a sentence that sends an operator hunting for a
			// build artifact when the truth is that there is no usable cafe
			// on this box at all. `<bin> version` asks the only authority
			// that can answer — the host's own kernel — and when it fails,
			// the file reads as ABSENT, which is what it is.
			Check: root(`test -x ` + CafeBinPath + ` && ` + CafeBinPath + ` version >/dev/null 2>&1` +
				` && [ "$(sha256sum ` + CafeBinPath + ` | cut -c1-64)" = "` + binaryHash + `" ]`),
			Apply: apply{Path: CafeBinPath, Data: binary, Mode: 0o755},
		},
		{
			Name:  StepBoxEnrolled,
			Title: "the box is enrolled",
			// The check is the enrollment's own file rather than a `cafe box
			// status` call: a status call would have to speak JSON over a
			// remote shell, and the file is the same fact for one fewer
			// process. When it is there, the enrollment already happened and
			// the capability is NOT presented — which is the idempotence the
			// whole packet turns on.
			Check: root(`test -f ` + EnrolledJSON),
			// `--token -` reads the capability from stdin. It is never an
			// argument: argv is world-readable through /proc on every host
			// this command targets, and a capability in argv is a capability
			// in `ps aux` for as long as the process lives.
			Apply: apply{
				Command: root(CafeBinPath + ` box enroll --token - --box ` + shellQuote(o.ControlPlane)),
				Stdin:   []byte(o.Capability),
			},
		},
		{
			Name:  StepBoxdIdentity,
			Title: "the daemon's identity document is written",
			Check: root(`test -s ` + IdentityPath),
			// The apply is a WRITE, not a command: setup derives the daemon's
			// document from what enrollment left behind and puts it where the
			// unit will point. It carries the box's non-secret facts and no
			// credential of any kind — the private key stays on this host, in
			// the keyring and in the 0600 file beside box.json, and never
			// crosses the seam as a value. The Command copies the certificate
			// and key to this document's directory, because a relative name in
			// the document resolves against that directory; the walk fills
			// both Path's data and this command with the POST-enrollment
			// identity, which is the only point at which a first enrollment
			// knows the certificate at all.
			Apply: apply{
				Path:    IdentityPath,
				Data:    identityDocument(enrolled, o),
				Mode:    0o644,
				Command: placeCertificateCommand(sudo, enrolled),
			},
		},
		{
			Name:  StepBoxdUnit,
			Title: "the box daemon is enabled and running",
			Check: root(`test -f ` + UnitPath + ` && [ "$(sha256sum ` + UnitPath + ` | cut -c1-64)" = "` +
				sha256Hex(unitDocument(o)) + `" ] && systemctl is-enabled ` + UnitName + ` >/dev/null 2>&1`),
			Apply: apply{
				Path:    UnitPath,
				Data:    unitDocument(o),
				Mode:    0o644,
				Command: root(`systemctl daemon-reload && systemctl enable --now ` + UnitName),
			},
		},
		{
			Name:  StepBoxdReport,
			Title: "the daemon answers on itself",
			// This is the heartbeat, and it is deliberately a read. The daemon
			// opens no listener and dials out over mTLS; what a controller can
			// prove from here is that the daemon loads its enrollment and
			// answers an observe.report — health, certificate expiry and root
			// disk — which is the same document the monthly report consumes.
			// The outbound round itself lands with the client certificate
			// enrollment does not yet issue, and the report names that rather
			// than papering over it.
			Check:  root(`env CAF_BOX_IDENTITY=` + IdentityPath + ` ` + CafeBinPath + ` boxd --report`),
			Report: true,
			Refusal: "the box daemon did not answer an observe.report. Its own words are " +
				"above; the usual cause is an identity document it cannot read, or a cafe " +
				"binary that will not start.",
		},
	}
}

// rooted prefixes a command with the sudo spelling the host needs. It is the
// one place that prefix is spelled, so the plan's rows and the certificate
// placement cannot disagree about how this run gets root.
func rooted(sudo, cmd string) string {
	if sudo == "" {
		return cmd
	}
	return sudo + " " + cmd
}

// identityDocument is what the daemon reads. It carries the facts the daemon
// cannot discover for itself and no credential: the box's name, its control
// plane, the cafe build, and the RELATIVE paths of the client certificate and
// key the daemon presents on its dial.
//
// The paths are relative because the daemon resolves them against this document
// (fileIdentityStore.resolve), so the certificate and key have to sit beside it.
// Setup puts them there: enrollment writes them beside box.json, and the
// identity step copies them into this document's directory — see
// placeCertificateCommand for why that is a host-side copy and not a Write from
// here. An identity that names no certificate (the recovery path, where the
// redemption's certificate was lost with its response) omits the two fields
// rather than naming a path that does not exist, so the daemon reports an
// unknown certificate with its own named reason instead of a confusing load
// failure.
func identityDocument(enrolled *identityRead, o Options) []byte {
	doc := struct {
		BoxID        string `json:"box_id"`
		ControlPlane string `json:"box_url"`
		Version      string `json:"version"`
		Certificate  string `json:"certificate,omitempty"`
		Key          string `json:"key,omitempty"`
	}{
		ControlPlane: o.ControlPlane,
		Version:      o.Version,
	}
	if enrolled != nil {
		doc.BoxID = enrolled.BoxID
		if enrolled.ControlPlane != "" {
			doc.ControlPlane = enrolled.ControlPlane
		}
		doc.Certificate = enrolled.Certificate
		doc.Key = enrolled.Key
	}
	return mustJSON(doc)
}

// placeCertificateCommand copies the enrolled certificate and key from beside
// box.json to beside the daemon's identity document, ON THE HOST.
//
// It is a command rather than two Writes because the private key must not cross
// the seam: the controller holds the two relative NAMES and never the bytes, so
// only the host can perform the copy. The destination is the identity document's
// own directory, which is the one fact that makes the relative names in the
// document resolve — enrollment writes to root's config directory while the
// daemon reads /etc/cafe, and a relative name is meaningful only against the
// reader's directory.
//
// Empty when the identity names no certificate: the recovery path, where a
// redemption was recorded but its certificate is gone, and there is nothing to
// copy. The daemon then reports the certificate as unknown with its own named
// reason, which is the honest answer.
func placeCertificateCommand(sudo string, id *identityRead) string {
	if id == nil || id.Certificate == "" || id.Key == "" {
		return ""
	}
	src := path.Dir(EnrolledJSON)
	dst := path.Dir(IdentityPath)
	copyOne := func(name string, mode string) string {
		return rooted(sudo, `install -m `+mode+` -D `+shellQuote(path.Join(src, name))+` `+shellQuote(path.Join(dst, name)))
	}
	return copyOne(id.Key, "0600") + ` && ` + copyOne(id.Certificate, "0644")
}

// unitDocument is the systemd unit. It is written by hand rather than generated
// because a unit file is a contract with the init system and there are four
// lines in it that matter: run as root, point at the identity, carry the range,
// restart on failure. A generator would be a second thing that could disagree
// with the check's hash.
func unitDocument(o Options) []byte {
	body := "[Unit]\n" +
		"Description=anywaye box daemon (outbound-only mTLS to the control plane)\n" +
		"After=docker.service network-online.target\n" +
		"Wants=network-online.target\n" +
		"\n" +
		"[Service]\n" +
		"Type=simple\n" +
		"User=root\n" +
		"Environment=CAF_BOX_IDENTITY=" + IdentityPath + "\n"
	if o.Range != "" {
		body += "Environment=CAF_BOX_RANGE=" + o.Range + "\n"
	}
	body += "ExecStart=" + CafeBinPath + " boxd\n" +
		"Restart=always\n" +
		"RestartSec=5\n" +
		"\n" +
		"[Install]\n" +
		"WantedBy=multi-user.target\n"
	return []byte(body)
}

// StepResult is what one step concluded. The output is the command's own
// words, kept because the checkout log is read by a person trying to work out
// what a host did.
type StepResult struct {
	Name   string  `json:"name"`
	Title  string  `json:"title"`
	Action string  `json:"action"`
	Detail string  `json:"detail,omitempty"`
	Output *Output `json:"output,omitempty"`
}

// Enrollment is what setup read back off the host about the box it enrolled:
// non-secrets, every one of them, which is why a controller may print this
// document in full.
type Enrollment struct {
	// BoxID is enrollment's own name for this box.
	BoxID string `json:"box_id"`
	// ControlPlane is the origin the box enrolled against. The JSON key is
	// `box_url`, for the same reason box.Identity's is: it is the word the
	// operator's flag and CAF_BOX_URL use, so one fact has one name.
	ControlPlane string `json:"box_url"`
	// Action is the host's own `cafe box enroll` answer — "enrolled",
	// "already-enrolled" or "refused". A retry that finds an already-enrolled
	// box reports that word rather than synthesizing its own.
	Action string `json:"action"`
	// EnrolledAt is when the enrollment completed, from the box's own clock.
	EnrolledAt string `json:"enrolled_at,omitempty"`
}

// Result is the whole run. It is a document a machine reads and a log a person
// reads, and there is no field in it that could carry a credential.
type Result struct {
	Host   string `json:"host"`
	DryRun bool   `json:"dry_run"`
	// Arch is the preflight's answer: which binary crossed and why it was the
	// right one. It is in the document because the one thing an operator
	// reading a log needs after a setup is "which architecture is installed on
	// that box", and because a field that is present is a field that cannot be
	// quietly forgotten.
	Arch *Arch `json:"arch,omitempty"`
	// Steps is one row per thing the host needed, in the order they ran.
	Steps []StepResult `json:"steps"`
	// Sudo names how the run got root: "", "root" or "sudo". Empty on a dry
	// run that never asked the host.
	Sudo string `json:"sudo,omitempty"`
	// Enrolled is the box the host holds, read back after the walk. Nil when
	// the host is not enrolled, which after a successful setup cannot happen
	// but after a refused one can.
	Enrolled *Enrollment `json:"enrolled,omitempty"`
	// Report is the box's own observation document — Health, Certificate,
	// Disk — as the daemon assembled it. It is the heartbeat's evidence.
	Report *boxd.Observation `json:"report,omitempty"`
	// Redemptions is how many capabilities the HOST presented, counted by the
	// host's own `cafe box enroll` answer. It is 0 on a re-run, and that zero
	// is the point of the whole packet.
	Redemptions int `json:"redemptions"`
	// Log is the human-readable checkout, one line per step, in order.
	Log []string `json:"log,omitempty"`
}

// OK reports whether the host ended up in the state the operator asked for.
func (r Result) OK() bool {
	for _, s := range r.Steps {
		if s.Action == ActionRefused {
			return false
		}
	}
	return true
}

// FailedStep names the first step that refused, so the error is one sentence
// and not a table.
func (r Result) FailedStep() string {
	for _, s := range r.Steps {
		if s.Action == ActionRefused {
			return s.Name
		}
	}
	return ""
}

// stepByName finds one row of a result. It returns nil rather than a zero value
// so a caller cannot mistake an absent step for a refused one.
func (r Result) stepByName(name string) *StepResult {
	for i := range r.Steps {
		if r.Steps[i].Name == name {
			return &r.Steps[i]
		}
	}
	return nil
}

// BoxID is the box this host holds, or "" — a convenience for the CLI's stderr
// sentence, which has to say something before the document is read.
func (r Result) BoxID() string {
	if r.Enrolled == nil {
		return ""
	}
	return r.Enrolled.BoxID
}

// enrollmentOf decodes the host's own `cafe box enroll` document. It is a local
// shape rather than box.EnrollResult because the only facts that cross back are
// the three in Enrollment, and a decoder that accepted the whole document would
// be a decoder that could one day print a field this package promised not to.
type enrollmentOf struct {
	Action      string        `json:"action"`
	Redemptions int           `json:"redemptions"`
	Identity    *identityRead `json:"identity,omitempty"`
}

// enrollment is the decoded form of the host's own answer, carrying the
// redemption count in an unexported field so it cannot be marshaled into a
// document by accident, and the two relative certificate paths the host's
// enrollment recorded.
type enrollment struct {
	Enrollment
	redemptions int
	certificate string
	key         string
}

// asIdentity turns the enrollment's host facts back into the shape the daemon's
// identity document is written from, so the walk can build that document from
// the POST-enrollment answer rather than from the pre-walk read (which is nil
// on the one run that matters).
func (e *enrollment) asIdentity() *identityRead {
	if e == nil {
		return nil
	}
	return &identityRead{
		BoxID:        e.BoxID,
		ControlPlane: e.ControlPlane,
		EnrolledAt:   e.EnrolledAt,
		Certificate:  e.certificate,
		Key:          e.key,
	}
}

// identityRead is what the host's box.json holds. Only the facts this package
// reports are named; the rest of the document is read by the host and never
// crosses the seam. Certificate and Key are the RELATIVE paths the host's
// enrollment wrote, which is what the daemon's identity document has to carry.
type identityRead struct {
	BoxID        string `json:"box_id"`
	ControlPlane string `json:"box_url"`
	EnrolledAt   string `json:"enrolled_at"`
	Certificate  string `json:"certificate"`
	Key          string `json:"key"`
}

// mustJSON encodes a document or dies. It is used only for the two documents
// this package writes, both of which are fixed structs of strings: an encode
// failure there is a programming error, and routing it through every caller
// would buy nothing.
func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("cafe box setup: encode a document: %v", err))
	}
	return raw
}

// sha256Hex is the hash of a document, hex, lower case — the spelling
// `sha256sum | cut -c1-64` produces on the host.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// clip keeps the first n non-empty lines of a command's output and bounds the
// total, so a checkout log stays a log. The bound is here rather than at the
// print site so a caller that forgets cannot paste ten megabytes of `curl`
// progress into a report.
func clip(s string, n int) string {
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		kept = append(kept, line)
		if len(kept) == n {
			break
		}
	}
	return strings.Join(kept, "\n")
}
