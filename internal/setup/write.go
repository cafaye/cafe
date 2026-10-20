package setup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// WriteChunkSize bounds what ONE pipe write carries, and why it is not the
// whole payload is a measured defect rather than a preference.
//
// A 29 MB cafe binary shipped to a Contabo box arrived TRUNCATED: 17,625,600
// bytes on the host against 29,250,507 held locally, and the write reported
// success — the next step then failed with "cannot execute binary file: Exec
// format error", a symptom that names the binary and not the transport. The
// cause was a single `ssh "cat > path"` child carrying the whole payload on
// its stdin: the sandbox's SSH proxy caps that pipe somewhere between 11 and
// 17 MB, silently, while still answering zero. A pipe write nobody bounded is
// a transfer whose failure mode is a corrupt binary that passed its check.
//
// Kamal does not have this bug, and its answer is the shape borrowed here:
// sshkit ships files with `ssh.scp.upload!` — net-scp, a STREAMING protocol
// over the same connection, never one giant pipe write. This package carries
// no dependency for a transport the operator's own `ssh` already speaks, so
// the streaming is done with the same command tree: N bounded appends, one
// hash gate, one rename. `reference/sshkit` is the reference.
//
// 1 MiB is the bound because it is small enough that a cap anywhere between
// 11 and 17 MB cannot swallow a chunk, and large enough that a 29 MB binary
// is 28 processes rather than 290.
const WriteChunkSize = 1 << 20

// The two bounds that keep a write from becoming a silent stall, and the
// measurement behind each one.
//
// A `cafe box setup` hung 10 m 50 s at "8.0 MiB of 19.9 MiB written" — the
// process alive in state S, holding nothing but pipes, the host healthy and
// carrying no partial file — while the capability it was about to spend lives
// 15 minutes by design. The retry sent the whole 19.9 MiB in 139 s, so the
// stall was intermittent: nothing about the box had changed. An unbounded
// foreground chunk is a stall nobody can see into, and this fleet's rule is
// that such a command must become an error with a number in it.
//
// DefaultChunkTimeout bounds ONE chunk. A 1 MiB chunk crossed a healthy
// Contabo link in seconds (19.9 MiB in 139 s is ~7 s per chunk including the
// handshake), so two minutes is an order of magnitude of headroom: a chunk
// that made no progress for that long is stuck, not slow. The slow-but-steady
// control — a reader that acks every chunk just under the bound — must NOT
// trip it, and a bound that kills a working transfer would be worse than the
// hang, so the bound is per-chunk silence rather than per-chunk speed.
//
// DefaultTotalTimeout bounds the WHOLE transfer. Fifteen minutes is the
// capability's own TTL (box's Capability::TTL_MINUTES = 15): a transfer that
// outlives it has already spent the token it was setting up to use, so going
// on is spending minutes the run does not have. Both bounds fail loudly —
// naming the chunk, the bytes acknowledged and the elapsed time — and both
// leave the transfer restarting from zero, for the reason restartNote names.
const (
	DefaultChunkTimeout = 2 * time.Minute
	DefaultTotalTimeout = 15 * time.Minute
)

// restartNote is the tail of every write refusal that cost bytes on the wire.
// The write is deliberately NOT resumable: a failed transfer removes the
// sibling, and the hash gate proves the whole file or nothing — resuming
// would need verified offsets the host has no protocol for. So a retry
// re-sends the whole file from zero, and the operator's cost of a stall is
// the whole payload, stated rather than discovered on the second run.
const restartNote = "The transfer is not resumable: a retry restarts from zero and re-sends the whole file."

// WriteEvent is what progress reports. Two events, not one, because "how much
// crossed" and "the bytes that arrived were proven equal to the bytes sent"
// are the two things an operator watches during a slow transfer, and a line
// that reported only the first would leave the final match as silent as the
// write used to be.
type WriteEvent int

const (
	// WriteChunk reports one chunk appended, with the bytes crossed so far.
	WriteChunk WriteEvent = iota
	// WriteVerified reports that the host's own sha256 of what landed matched
	// the local hash — the only fact that makes the rename safe.
	WriteVerified
)

// chunkCount is how many chunks a payload of n bytes goes out in. It is a
// pure function of the size because the transfer has to be plannable: the
// operator's progress line and the test's assertion read the same count, and
// a transfer whose chunking depended on a connection's mood could not be
// checked by either.
//
// Zero bytes is one chunk, not none: the file has to come into existence, and
// an empty `cat > tmp` is how a host spells "this file is empty" rather than
// leaving no file at all.
func chunkCount(n int) int {
	if n <= 0 {
		return 1
	}
	return (n + WriteChunkSize - 1) / WriteChunkSize
}

// chunkBounds is the [lo, hi) slice of the payload that chunk i carries.
// Bounded arithmetic on the caller's own size, so no chunk is zero-length
// except the deliberate empty payload, which chunkCount calls one chunk.
func chunkBounds(i, total int) (int, int) {
	lo := i * WriteChunkSize
	hi := lo + WriteChunkSize
	if hi > total {
		hi = total
	}
	return lo, hi
}

// Write puts a file on the host. The work is in transfer, because the transfer
// has to be drivable without an SSH server — see that type.
//
// A payload big enough to go out in more than one chunk goes out over ONE
// connection, and a payload of one chunk goes out exactly as it did before: see
// control.go for the measurement that decided it and needsControl for the
// boundary. What does NOT change either way is the delivery itself — the
// bounded chunks, the sibling, the host's own sha256 and byte count, and the
// rename that only happens once they match.
func (h SSHHost) Write(ctx context.Context, path string, data []byte, mode os.FileMode) error {
	if !needsControl(len(data)) {
		return transfer{
			name:     h.Name,
			run:      h.Run,
			progress: h.Progress,
		}.write(ctx, path, data, mode)
	}
	sock, cleanup, err := h.controlPath()
	if err != nil {
		return fmt.Errorf("cafe box setup: write %s on %s: open the connection the chunks share: %w", path, h.Name, err)
	}
	defer cleanup()
	return writeOver(ctx, sshMaster{host: h, sock: sock}, h.Name, h.Progress, path, data, mode)
}

// transfer is the chunked delivery with the two things it reaches for passed
// in: a runner for the child, and a sink for progress.
//
// It is a struct rather than a method on SSHHost for one reason: the whole
// point of the packet is a failure mode a test must be able to reproduce —
// a pipe that cuts after 17,625,600 bytes and still answers success — and the
// only way to reproduce that deterministically is to drive the real transfer
// against a host that performs the commands for real. Holding the loop in a
// type with a runner seam makes that an ordinary test rather than a live one
// that needs a Contabo box and a proxy with a cap.
type transfer struct {
	// name is the host's address as the operator typed it, which every
	// refusal carries so a log line says which box it was about.
	name string
	// run is one remote command. It is Host.Run's own signature, so a fake
	// host and the real SSH transport are the same value to this type.
	run func(ctx context.Context, command string, stdin []byte) (Output, error)
	// progress is called per chunk and once when the hash matched. Nil is
	// silent, which is the right default for a library: the CLI sets it, and
	// a package that printed from inside a transfer would put operator prose
	// into a stream some caller is parsing.
	progress func(WriteEvent, int, int)
	// chunkTimeout bounds one chunk's silence; totalTimeout bounds the whole
	// transfer. Zero means the Default above, and a test sets them to seconds
	// so the suite proves the bound without waiting for it. Negative disables
	// the bound, which is how the red control — the write as it stands on
	// master, hanging forever against a stalled reader — is driven: the same
	// transfer, the same fixture, no deadline.
	chunkTimeout time.Duration
	totalTimeout time.Duration
}

// chunkBound is the per-chunk deadline in force for this transfer.
func (t transfer) chunkBound() time.Duration {
	if t.chunkTimeout < 0 {
		return 0
	}
	if t.chunkTimeout == 0 {
		return DefaultChunkTimeout
	}
	return t.chunkTimeout
}

// totalBound is the whole-transfer deadline in force for this transfer.
func (t transfer) totalBound() time.Duration {
	if t.totalTimeout < 0 {
		return 0
	}
	if t.totalTimeout == 0 {
		return DefaultTotalTimeout
	}
	return t.totalTimeout
}

// write is the delivery, and the six steps are the whole argument.
//
// It is N bounded chunks instead of one command for the whole file, and that
// is the entire packet: a 29 MB payload on a single `cat >` was cut at
// 17,625,600 bytes by a pipe cap nobody could see, and it reported success.
// Now:
//
//  1. the sibling is cleared and the directory exists;
//  2. N bounded chunks are appended, the first with `>` and the rest with `>>`;
//  3. the host hashes what it HOLDS, and only a hash equal to the local one
//     lets the rename happen.
//
// The sibling-tmp + rename is kept, because it was already right: the
// destination is never open for writing, so the host's previous binary
// survives every failure this method can have. What was missing was the step
// that makes the rename worth something — a hash — and the bound that makes
// the delivery.
//
// No chunk goes near a command line. Each is bytes on a child's stdin and the
// redirect target is the only part of the path in the command, so a payload
// containing a quote, a newline or a redirect cannot be read as a shell word.
// Same argument the capability gets, applied to a binary.
func (t transfer) write(ctx context.Context, path string, data []byte, mode os.FileMode) error {
	local := sha256Hex(data)
	dir := filepath.Dir(path)
	tmp := filepath.Join(dir, "."+filepath.Base(path)+".cafe-tmp")
	total := len(data)
	chunks := chunkCount(total)
	start := time.Now()

	// The whole transfer runs under one deadline, because a transfer that
	// outlives the capability's TTL has already spent the token it was
	// setting up to use. A non-positive bound disables it — the red
	// control's configuration, never production's.
	totalCtx := ctx
	totalCancel := context.CancelFunc(func() {})
	if bound := t.totalBound(); bound > 0 {
		totalCtx, totalCancel = context.WithTimeout(ctx, bound)
	}
	defer totalCancel()

	if out, err := t.run(totalCtx, "mkdir -p "+shellQuote(dir)+" && rm -f "+shellQuote(tmp), nil); err != nil {
		if t.totalBound() > 0 && totalCtx.Err() == context.DeadlineExceeded {
			return t.stalled(path, tmp, total, local, 0, chunks, 0, start, true, false)
		}
		return t.transportLost(path, tmp, total, local, 0, chunks, 0, err, false)
	} else if !out.OK() {
		return fmt.Errorf("cafe box setup: write %s on %s: %s", path, t.name, firstLine(out.Stderr+out.Stdout))
	}

	crossed := 0
	for i := 0; i < chunks; i++ {
		lo, hi := chunkBounds(i, total)
		// The first chunk truncates and the rest append. `>` on the first is
		// what makes a sibling a dead attempt left behind irrelevant even if
		// the rm above were racing something.
		redirect := ">>"
		if i == 0 {
			redirect = ">"
		}
		// One deadline per chunk: a chunk that made no progress for the
		// bound is stuck, not slow, and the run must say so with the chunk's
		// number in it rather than stop moving its progress line.
		chunkCtx := totalCtx
		chunkCancel := context.CancelFunc(func() {})
		if bound := t.chunkBound(); bound > 0 {
			chunkCtx, chunkCancel = context.WithTimeout(totalCtx, bound)
		}
		out, err := t.run(chunkCtx, "cat "+redirect+" "+shellQuote(tmp), data[lo:hi])
		chunkCancel()
		// A deadline is not a death: it is the stall this packet was
		// measured on, and it answers as one — naming the chunk, the
		// bytes acknowledged and the elapsed time — rather than as a
		// transport that died. The bound being enabled is part of the
		// condition: with no bound in force a canceled caller is just a
		// dead transport, which is the red control's whole point.
		chunkStalled := t.chunkBound() > 0 && chunkCtx.Err() == context.DeadlineExceeded && totalCtx.Err() != context.DeadlineExceeded
		totalStalled := t.totalBound() > 0 && totalCtx.Err() == context.DeadlineExceeded
		if err != nil {
			// The transport died mid-stream. The destination was never opened
			// for writing, so this is the one failure that cannot truncate
			// anything: the host keeps its previous file, or nothing.
			if chunkStalled || totalStalled {
				cleaned := t.removeSibling(context.Background(), tmp)
				return t.stalled(path, tmp, total, local, crossed, chunks, i, start, totalStalled, cleaned)
			}
			cleaned := t.removeSibling(ctx, tmp)
			return t.transportLost(path, tmp, total, local, crossed, chunks, i, err, cleaned)
		}
		if totalStalled {
			cleaned := t.removeSibling(context.Background(), tmp)
			return t.stalled(path, tmp, total, local, crossed, chunks, i, start, true, cleaned)
		}
		if !out.OK() {
			cleaned := t.removeSibling(ctx, tmp)
			return fmt.Errorf("cafe box setup: write %s on %s: chunk %d of %d: %s%s",
				path, t.name, i+1, chunks, firstLine(out.Stderr+out.Stdout), siblingNote(tmp, cleaned))
		}
		crossed = hi
		if t.progress != nil {
			t.progress(WriteChunk, crossed, total)
		}
	}

	// The gate. The host hashes what it actually holds, and its size, before
	// anything moves: a hash taken after the rename would be a hash of a
	// thing nobody can fix any more.
	remote, remoteBytes, out, err := t.verifyOnHost(totalCtx, tmp, mode)
	if err != nil {
		if t.totalBound() > 0 && totalCtx.Err() == context.DeadlineExceeded {
			cleaned := t.removeSibling(context.Background(), tmp)
			return t.stalled(path, tmp, total, local, crossed, chunks, chunks, start, true, cleaned)
		}
		cleaned := t.removeSibling(ctx, tmp)
		return t.transportLost(path, tmp, total, local, crossed, chunks, chunks, err, cleaned)
	}
	if !out.OK() {
		cleaned := t.removeSibling(ctx, tmp)
		return fmt.Errorf("cafe box setup: verify %s on %s: %s%s",
			path, t.name, firstLine(out.Stderr+out.Stdout), siblingNote(tmp, cleaned))
	}
	if remote != local {
		// The refusal is the product. It names what was held locally, what
		// arrived, and both byte counts, because "the write failed" sends a
		// reader to a log that says nothing about a pipe that cut at 17 MB.
		cleaned := t.removeSibling(ctx, tmp)
		return writeMismatch{
			name: t.name, path: path, sibling: tmp, cleaned: cleaned,
			localHash: local, remoteHash: remote, localBytes: total, remoteBytes: remoteBytes,
		}
	}
	if t.progress != nil {
		t.progress(WriteVerified, total, total)
	}

	if out, err := t.run(totalCtx, "mv "+shellQuote(tmp)+" "+shellQuote(path), nil); err != nil {
		return fmt.Errorf("cafe box setup: write %s on %s: the bytes are on the host and match, but the move into place died: %v. "+
			"The verified file is at %s; move it by hand and the host is whole", path, t.name, err, tmp)
	} else if !out.OK() {
		return fmt.Errorf("cafe box setup: write %s on %s: the bytes are on the host and match, but the move into place failed: %s. "+
			"The verified file is at %s; move it by hand and the host is whole",
			path, t.name, firstLine(out.Stderr+out.Stdout), tmp)
	}
	return nil
}

// verifyOnHost asks the host for the hash and the size of what it holds. Two
// facts in one command because the interesting half of a failure is both of
// them: a hash that differs with an EQUAL byte count is corruption in flight,
// and a hash that differs with a SMALLER one is a pipe that cut. The caller
// separates those two in its refusal, which is the difference between "your
// network is bad" and "your transport truncated a binary".
//
// The mode is set here, before the hash, rather than after the rename: the
// file becomes visible at its destination already carrying the permissions
// it needs, and a chmod after a rename would leave a window in which
// /usr/local/bin/cafe was executable by nobody.
func (t transfer) verifyOnHost(ctx context.Context, tmp string, mode os.FileMode) (hash string, bytes int, out Output, err error) {
	script := fmt.Sprintf("chmod %o %s && sha256sum %s && wc -c < %s",
		mode.Perm(), shellQuote(tmp), shellQuote(tmp), shellQuote(tmp))
	out, err = t.run(ctx, script, nil)
	if err != nil || !out.OK() {
		return "", 0, out, err
	}
	lines := strings.SplitN(out.Stdout, "\n", 2)
	fields := strings.Fields(strings.TrimSpace(lines[0]))
	if len(fields) < 2 || len(fields[0]) != 64 {
		// A host that answered something other than "<64 hex>  <path>" is a
		// host whose sha256sum is not the one this was written against.
		// Refuse rather than parse a guess.
		return "", 0, Output{Code: 1, Stderr: "unexpected sha256sum output: " + firstLine(out.Stdout)}, nil
	}
	if len(lines) < 2 {
		return "", 0, Output{Code: 1, Stderr: "the host did not answer the byte count of " + tmp}, nil
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(lines[1]))
	if convErr != nil {
		return "", 0, Output{Code: 1, Stderr: "the host's byte count was not a number: " + firstLine(lines[1])}, nil
	}
	return fields[0], n, out, nil
}

// removeSibling takes the partial file off the host, and reports whether it
// is gone. It is best-effort by design: the paths that reach it include a
// dead transport, and a cleanup that failed must not replace the error that
// named the real failure. What it guarantees either way is that the
// DESTINATION was never touched — the sibling is a dot-file nothing points at.
func (t transfer) removeSibling(ctx context.Context, tmp string) bool {
	out, err := t.run(ctx, "rm -f "+shellQuote(tmp), nil)
	return err == nil && out.OK()
}

// siblingNote is the tail of a refusal that says where the partial file went.
// Named rather than assumed: a leftover sibling is litter a person can
// remove, but only if the message says it is there.
func siblingNote(tmp string, cleaned bool) string {
	if cleaned {
		return "\nThe partial file was removed; the previous file is untouched."
	}
	return "\nThe partial file at " + tmp + " could not be removed; remove it by hand — nothing points at it, and the destination is untouched."
}

// transportLost is what a Write answers when the transport itself died, which
// is a different failure from a host saying no and reads differently: the
// local hash is named, the byte counts are named, and the sentence says no
// remote hash could be established rather than inventing one.
func (t transfer) transportLost(path, tmp string, total int, localHash string, crossed, chunks, at int, err error, cleaned bool) error {
	return fmt.Errorf("cafe box setup: write %s on %s REFUSED: the transport died after %d of %d bytes, in chunk %d of %d: %v. "+
		"Local sha256 is %s; no remote hash could be taken, so the bytes that arrived are unproven. %s %s",
		path, t.name, crossed, total, at+1, chunks, err, localHash, siblingNote(tmp, cleaned), restartNote)
}

// stalled builds the refusal the hang becomes: a chunk (or the whole
// transfer) that made no progress inside its deadline. It names the chunk
// index, the bytes acknowledged so far, the host and the elapsed time,
// because the measured failure's last line — a progress line that stopped
// moving — distinguished none of "slow" from "stuck".
func (t transfer) stalled(path, tmp string, total int, localHash string, crossed, chunks, at int, start time.Time, isTotal, cleaned bool) error {
	elapsed := time.Since(start).Round(time.Second)
	what := fmt.Sprintf("chunk %d of %d made no progress for %s", at+1, chunks, t.chunkBound())
	if isTotal {
		what = fmt.Sprintf("the transfer made no progress inside the whole-transfer bound of %s", t.totalBound())
	}
	return writeStall{
		name: t.name, path: path, sibling: tmp, cleaned: cleaned,
		crossed: crossed, total: total, chunks: chunks, at: at,
		elapsed: elapsed, what: what, isTotal: isTotal,
	}
}

// writeStall is the hang as an error: which chunk stopped, how many bytes had
// been acknowledged, and how long the run had been going. It is a type rather
// than a format string so a test asserts on the fields — the chunk, the bytes,
// the elapsed time — rather than on English.
type writeStall struct {
	name, path, sibling string
	cleaned             bool
	crossed, total      int
	chunks, at          int
	elapsed             time.Duration
	what                string
	isTotal             bool
}

func (s writeStall) Error() string {
	return fmt.Sprintf("cafe box setup: write %s on %s STALLED: %s after %d of %d bytes acknowledged (elapsed %s). "+
		"This is a stall, not slowness: kill it and retry rather than wait. %s %s",
		s.path, s.name, s.what, s.crossed, s.total, s.elapsed, siblingNote(s.sibling, s.cleaned), restartNote)
}

// writeMismatch is the refusal the whole packet exists for: the host holds a
// file, it hashes to something that is not what was sent, and the destination
// has therefore NOT been touched. It is a type rather than a format string so
// a test asserts on the fields — both hashes, both byte counts — rather than
// on English.
type writeMismatch struct {
	name, path, sibling     string
	cleaned                 bool
	localHash, remoteHash   string
	localBytes, remoteBytes int
}

func (m writeMismatch) Error() string {
	return fmt.Sprintf("cafe box setup: write %s on %s REFUSED: the bytes that arrived are not the bytes held locally — "+
		"local %d bytes sha256 %s, remote %d bytes sha256 %s. %s %s",
		m.path, m.name, m.localBytes, m.localHash, m.remoteBytes, m.remoteHash, siblingNote(m.sibling, m.cleaned), restartNote)
}
