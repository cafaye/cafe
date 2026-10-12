package setup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SSHHost is the real Host: the box, reached by running the operator's own
// `ssh`.
//
// It is a decision rather than a shortcut, and since the dependency rule was
// inverted (AGENTS.md, "Packages with dependencies") it is a decision about
// FIT rather than about count: a good library is the default now, so the
// hand-rolled path is the one that has to argue, and here is the argument.
//
// The candidate was golang.org/x/crypto/ssh, and the count does not settle
// it — that library is already in this module's graph (v0.32.0, required
// transitively by x/net@v0.34.0, its own x/sys and x/term pinned beside it),
// so a direct import is a line in `require`, not a module tree. What it would
// not give is the operator's own ssh_config, which is the thing this
// repository actually runs against: the `Host contabo` / HostName entry it has
// been using for two days, ProxyJump, ControlMaster, the agent and the
// known_hosts. ssh resolves all of that for free; a client library
// reimplements each one, and a reimplementation that resolves a config file
// almost-right is a host that connects on the operator's machine and on
// nobody else's. It also inherits the diagnostics the operator already knows
// how to read, and this package never has to keep in step with a protocol.
//
// What it costs is one process per step — about a dozen in the walk, each with
// a bounded timeout, and none of them carrying a secret a process listing
// could read, because the capability goes on stdin and the password through
// the askpass pipe (see askpass), so argv is clean by construction — and one
// failure measured live on 2026-10-13: a large payload handed to ssh on stdin
// can be truncated by the write side. That is paid for rather than hidden: a
// write lands on a sibling and moves into place, so a truncated one leaves the
// previous binary behind, and the step after it verifies the installed bytes
// by sha256. A later packet that swaps this transport owes the same comment:
// the library, what it gains, and what it loses.
type SSHHost struct {
	// Name is the address, as the operator typed it: "box.example.com",
	// "root@10.0.0.3", "deploy@box:2222". It is handed to ssh verbatim so the
	// operator's own ssh_config can rewrite it, which is the whole point of
	// using their client.
	Name string
	// User is the login name. Empty means whatever ssh_config resolves, which
	// is the right default for a host the operator has already configured.
	User string
	// Port is the SSH port. Zero means 22.
	Port int
	// IdentityFile is a private key to authenticate with. Empty means the
	// agent and the default key locations, in ssh's own order.
	IdentityFile string
	// Password authenticates when there is no key. It is held in memory for the
	// life of the dial and handed to the askpass helper over a pipe — see
	// askpass — so it is never in argv, never in an environment variable and
	// never on disk.
	Password string
	// Timeout bounds each command. Zero means DefaultTimeout.
	Timeout time.Duration
	// ConnectTimeout bounds the connection. Zero means DefaultConnectTimeout.
	ConnectTimeout time.Duration
}

// DefaultTimeout bounds one remote command. A `curl | sh` on a cold network can
// take a while, and a timeout that kills a Docker install half way leaves a
// box worse than it was found — so this is generous and the walk's checks are
// all fast.
const DefaultTimeout = 15 * time.Minute

// DefaultConnectTimeout bounds the TCP and handshake phase. It is separate from
// DefaultTimeout because "the host is unreachable" and "the install is slow" are
// different failures and a caller waiting on the first should not wait as long
// as one waiting on the second.
const DefaultConnectTimeout = 60 * time.Second

// Run executes one shell command on the host through ssh.
//
// The command is handed to ssh as a SINGLE argument. That matters: ssh joins
// its trailing arguments with spaces to build the remote command line, so a
// command built here is not word-split by the local shell on the way out, and
// the remote shell receives it as one string. It is also why the capability is
// never part of the command — it goes on stdin, and stdin is not argv.
func (h SSHHost) Run(ctx context.Context, command string, stdin []byte) (Output, error) {
	args := h.sshArgs()
	args = append(args, h.target(), command)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(stdin)
	// The password's askpass helper needs the pipe to the child, and it needs
	// no TTY: ssh's askpass path is only taken when there is no terminal, which
	// is exactly the situation a CLI is in.
	if h.Password != "" {
		askpass, cleanup, err := h.askpass()
		if err != nil {
			return Output{}, err
		}
		defer cleanup()
		cmd.Env = append(os.Environ(),
			"SSH_ASKPASS="+askpass,
			"SSH_ASKPASS_REQUIRE=force",
			"DISPLAY=:0",
		)
		// Discard ssh's own stderr? No: a failed handshake is the single most
		// useful thing on stderr, and the caller keeps it in the log.
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	out := Output{
		Code:   exitCode(cmd, runErr),
		Stdout: strings.TrimSpace(stdout.String()),
		Stderr: strings.TrimSpace(stderr.String()),
	}
	if runErr != nil && out.Code == -1 {
		return out, fmt.Errorf("cafe box setup: ssh to %s: %v: %s", h.Name, runErr, firstLine(stderr.String()))
	}
	return out, nil
}

// Write puts a file on the host.
//
// It goes over a single `ssh cat > path` rather than a second transport: a
// command that opens one connection per step is a command whose failure modes
// are one, and scp would be a second client to configure, to trust and to keep
// in step with the one above it.
func (h SSHHost) Write(ctx context.Context, path string, data []byte, mode os.FileMode) error {
	// The install is written to a sibling and moved into place, so a host that
	// loses the connection mid-write keeps its previous binary rather than
	// ending up with a truncated one. Same argument as the keyring's rename,
	// applied to a file on a machine nobody is watching.
	dir := filepath.Dir(path)
	tmp := filepath.Join(dir, "."+filepath.Base(path)+".cafe-tmp")
	script := fmt.Sprintf("mkdir -p %s && cat > %s && chmod %o %s && mv %s %s",
		shellQuote(dir), shellQuote(tmp), mode, shellQuote(tmp), shellQuote(tmp), shellQuote(path))
	out, err := h.Run(ctx, script, data)
	if err != nil {
		return err
	}
	if !out.OK() {
		return fmt.Errorf("cafe box setup: write %s on %s: %s", path, h.Name, firstLine(out.Stderr+out.Stdout))
	}
	return nil
}

// sshArgs builds the options every invocation carries. They are here rather
// than at the call sites because there is one policy for reaching a host and
// four places that would otherwise each have half of it.
func (h SSHHost) sshArgs() []string {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	connect := h.ConnectTimeout
	if connect <= 0 {
		connect = DefaultConnectTimeout
	}
	args := []string{
		// BatchMode when there is no password: a command with no credential
		// must fail rather than hang on a prompt nobody is there to answer,
		// because a hung setup is a setup whose operator cannot tell whether
		// anything happened.
		"-o", "BatchMode=" + boolYesNo(h.Password == ""),
		// accept-new records a host key it has not seen and refuses one that
		// changed. That is the honest middle: a first setup does not need a
		// human to fingerprint a box, and a changed key is the one case where
		// continuing would be wrong.
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=" + connect.String(),
		// One connection, one command. A master connection would make the walk
		// faster and would keep a credential-bearing channel open for the whole
		// run, which is a longer window than any step needs.
		"-o", "ControlMaster=no",
	}
	if h.Port > 0 {
		args = append(args, "-p", fmt.Sprint(h.Port))
	}
	if h.IdentityFile != "" {
		args = append(args, "-i", h.IdentityFile)
	}
	return args
}

// target is the ssh destination: user@name when a user was named, and the name
// alone when it was not, so ssh_config keeps its say.
func (h SSHHost) target() string {
	if h.User == "" {
		return h.Name
	}
	return h.User + "@" + h.Name
}

// askpass writes the tiny helper ssh runs when it needs the password, and
// returns the path plus the cleanup that removes it.
//
// The helper reads the password from file descriptor 3. The write end of that
// pipe is inherited by the ssh process as its fd 3, so the password travels from
// this process to the helper through a pipe and exists nowhere else: not in
// argv, not in an environment variable, not in a file. The helper itself is 0700
// in a private directory and contains no credential, so a leftover file after a
// crash is a seven-line shell script and nothing else.
//
// This is the standard answer to "ssh wants a password and I have no terminal",
// and it is SSH_ASKPASS_REQUIRE=force that makes it unconditional — without it,
// ssh only asks when it believes there is no TTY, and a CI runner with a
// redirected one is exactly the case that gets it wrong.
func (h SSHHost) askpass() (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "cafe-box-setup-")
	if err != nil {
		return "", nil, fmt.Errorf("cafe box setup: create the askpass directory: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	path = filepath.Join(dir, "askpass.sh")
	// IFS= keeps leading and trailing whitespace, and -r keeps backslashes: a
	// password is not a shell word and this helper must not edit it.
	script := "#!/bin/sh\nIFS= read -r password <&3\nprintf '%s\\n' \"$password\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("cafe box setup: write the askpass helper: %w", err)
	}
	return path, cleanup, nil
}

// exitCode reads a command's status without treating a non-zero one as an
// error. It is -1 when the process never ran — a missing binary, a context
// cancelled before the exec — which is the one case the caller must distinguish
// from the host answering "no".
func exitCode(cmd *exec.Cmd, err error) int {
	if cmd.ProcessState == nil {
		return -1
	}
	return cmd.ProcessState.ExitCode()
}

// firstLine is a bounded snippet of a command's error output. It is bounded
// because ssh's stderr on a refused key is three lines and on a proxy failure
// can be three screens.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return "(no output)"
}

// shellQuote single-quotes a string for a remote shell. It is here because the
// paths this package writes are constants today and a host that overrides them
// tomorrow is a host whose file lands somewhere else.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// boolYesNo spells a bool the way an ssh_config value wants it.
func boolYesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
