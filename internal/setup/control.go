package setup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// The one connection a multi-chunk Write shares, and why it exists.
//
// MEASURED, on the Contabo link, 2026-10-13, Mac to box and back at ~180 ms RTT:
// 28 x 1 MiB chunks, each on its own `ssh` child, took 199.33 s. The same 28
// chunks over ONE control master took 92.8 s, and both landed byte-identical
// with a matching host sha256. The difference is 28 handshakes — an `ssh
// contabo true` measures 2.2 s on that link — and a cold TCP window per chunk.
// That is 2.15x, and it is the difference between a write an operator waits
// through and one they trust.
//
// The other two candidates were measured too, and both lost on the evidence:
//
//   - A bigger chunk. The bound is not a byte ceiling. A single unbounded
//     `ssh cat >` carrying 28 MiB was refused as 17,625,600 bytes of a
//     29,250,507-byte binary on the day this code was written, with a zero
//     exit status; the same single write, measured for this packet, landed
//     whole in 96 s. The number moved by 7 MB between two runs on the same
//     link, so there is no ceiling to raise by measurement — only a bound that
//     failed once silently and can again. Throughput does not argue for it
//     either: one stream at 96 s against 28 shared chunks at 92.8 s is a 3%
//     difference for the entire risk.
//   - A tar stream in one stream. Same problem, better dressed: the payload
//     crosses as one unbounded pipe write, which is the exact shape that
//     truncated.
//
// So the chunking stays at 1 MiB, the sibling and the hash gate stay exactly
// as they were, and what changes is how many times the transport says hello.
// The safety is not traded for the speed: the safety turned out to cost nothing
// once the handshake was shared.

// controlMaster is the connection one multi-chunk write shares. It is an
// interface rather than a shape over SSHHost because the claim it carries —
// "the chunks share ONE connection, and it is taken down once" — is a claim
// about counts, and a test must be able to count them against an interpreter
// that performs the commands exactly as the transfer tests already do.
type controlMaster interface {
	// command runs one remote command over the shared connection.
	command(ctx context.Context, command string, stdin []byte) (Output, error)
	// close takes the connection down. Best effort by contract: the bytes are
	// already verified (or refused) by the time this runs, so nothing about
	// the delivery may depend on it.
	close(ctx context.Context)
}

// writeOver is the delivery over one shared connection, and it is the whole of
// the change on the deliver side: the transfer is run with the shared
// connection's runner, and the connection is closed on every path out, in the
// order that matters — the delivery's answer first, the take-down second.
func writeOver(ctx context.Context, m controlMaster, name string, progress func(WriteEvent, int, int), path string, data []byte, mode os.FileMode) error {
	err := transfer{name: name, run: m.command, progress: progress}.write(ctx, path, data, mode)
	m.close(ctx)
	return err
}

// needsControl is the payload size at which a shared connection pays for
// itself. One chunk is one command — a unit file, an identity document — and a
// master would be two extra processes to save nothing, so those go out exactly
// as they did before.
func needsControl(total int) bool {
	return chunkCount(total) > 1
}

// controlArgs are the three options that turn a per-command ssh into one
// channel. They are a function so the difference between the walk's policy and
// the write's is readable in one place and assertable as data.
func controlArgs(sock string) []string {
	return []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + sock,
		"-o", "ControlPersist=yes",
	}
}

// controlSocketDir is the private directory the control socket lives in for
// the life of one write. Private because a control socket is a credential-
// bearing channel: anyone who can connect to it can run commands on the box as
// this operator, so 0700 in a directory nobody else can name is the whole of
// its access control. The prefix is short for the same reason — a unix socket
// path is bounded by the platform (104 bytes on macOS), and a control master
// that cannot be created is a write that fails at its first chunk.
func controlSocketDir() (string, error) {
	dir, err := os.MkdirTemp("", "cafe-ctl-")
	if err != nil {
		return "", err
	}
	return dir, nil
}

// sshMaster is the real controlMaster: the operator's own `ssh`, with
// ControlMaster=auto so the first chunk command opens a master and every
// command after it rides the same channel.
type sshMaster struct {
	host SSHHost
	sock string
}

func (m sshMaster) command(ctx context.Context, command string, stdin []byte) (Output, error) {
	return m.host.run(ctx, command, stdin, m.sock)
}

func (m sshMaster) close(ctx context.Context) {
	m.host.stopControl(ctx, m.sock)
}

// stopControl takes the master down. It returns nothing on purpose: by the time
// it runs the delivery has already answered, and a stop that failed must not
// overwrite a refusal that named both hashes. What it leaves behind is a socket
// in a directory the caller removes, and nothing that holds a connection open.
func (h SSHHost) stopControl(ctx context.Context, sock string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := append(h.baseArgs(),
		"-o", "ControlPath="+sock,
		"-O", "stop", h.target(),
	)
	_ = exec.CommandContext(ctx, "ssh", args...).Run()
}

// controlPath is where the socket goes for one write. It is derived from a
// private directory rather than from the host name so two concurrent writes to
// two hosts, or two writes to one, cannot share a channel by accident.
func (h SSHHost) controlPath() (string, func(), error) {
	dir, err := controlSocketDir()
	if err != nil {
		return "", nil, err
	}
	sock := filepath.Join(dir, "s")
	cleanup := func() { _ = os.RemoveAll(dir) }
	return sock, cleanup, nil
}
