package setup

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// the fake host: a small shell that actually performs what a Write sends
// ---------------------------------------------------------------------------

// shellHost is a host whose Run PERFORMS the commands a transfer sends. It is
// not a mirror of the command strings — it is an interpreter: a file map plus
// mkdir, rm, cat with a redirect, chmod, sha256sum, wc and mv. That matters,
// because a fake that answered by recognising whole strings would go green the
// day the emitter's spelling changed and the interpreter's did not, which is
// the exact failure this packet is about: two sides that agreed about
// everything except the bytes.
type shellHost struct {
	root     string
	fs       map[string][]byte
	modes    map[string]os.FileMode
	commands []string
	// cats counts the `cat` invocations, which is the number of chunks the
	// transport sent. A test reads it to assert the chunk count was derived
	// from the size.
	cats int

	// pipeCap is the measured Contabo defect, reintroduced on purpose: the
	// host accepts a chunk's stdin and keeps only what fits under this many
	// bytes of TOTAL stdin, silently, and answers success. That is what 29 MB
	// on one pipe write did — 17,625,600 bytes arrived and the write reported
	// OK. Zero means unlimited.
	pipeCap int

	// dieOnCat kills the transport at the nth `cat` (1-based): Run answers
	// with an error rather than an Output, the way a dead ssh child does.
	dieOnCat int
}

func newShellHost(t *testing.T) *shellHost {
	t.Helper()
	return &shellHost{
		root:    t.TempDir(),
		fs:      map[string][]byte{},
		modes:   map[string]os.FileMode{},
		pipeCap: -1,
	}
}

func (h *shellHost) read(path string) ([]byte, bool) {
	data, ok := h.fs[h.root+path]
	return data, ok
}

func (h *shellHost) exists(path string) bool {
	_, ok := h.fs[h.root+path]
	return ok
}

func (h *shellHost) mode(path string) os.FileMode { return h.modes[h.root+path] }

func (h *shellHost) catCount() int { return h.cats }

func (h *shellHost) Run(_ context.Context, command string, stdin []byte) (Output, error) {
	h.commands = append(h.commands, command)
	if strings.HasPrefix(command, "cat ") {
		h.cats++
		if h.dieOnCat == h.cats {
			return Output{}, errors.New("ssh: connect to host " + h.root + ": connection closed by remote host")
		}
	}
	var stdout, stderr strings.Builder
	code := 0
	for _, op := range splitOps(command) {
		c, out, errOut := h.execOne(op, stdin)
		// Every operation ends its own line, the way a shell's stdout works;
		// the transfer parses the answer line by line.
		stdout.WriteString(out)
		stdout.WriteByte('\n')
		if c != 0 {
			code = c
			stderr.WriteString(errOut)
			break
		}
	}
	return Output{Code: code, Stdout: strings.TrimSpace(stdout.String()), Stderr: stderr.String()}, nil
}

// execOne runs one `&&`-separated operation against the host's file map.
func (h *shellHost) execOne(op string, stdin []byte) (int, string, string) {
	f := lexFields(op)
	if len(f) == 0 {
		return 0, "", ""
	}
	switch f[0] {
	case "mkdir":
		if err := os.MkdirAll(h.root+f[len(f)-1], 0o755); err != nil {
			return 1, "", err.Error()
		}
		return 0, "", ""
	case "rm":
		if len(f) > 1 && f[1] == "-f" {
			delete(h.fs, h.root+f[2])
		}
		return 0, "", ""
	case "cat":
		// cat > 'path'  |  cat >> 'path'
		path := h.root + f[2]
		data := stdin
		if h.pipeCap >= 0 {
			take := len(stdin)
			if take > h.pipeCap {
				take = h.pipeCap
			}
			data = stdin[:take]
			h.pipeCap -= take
		}
		if f[1] == ">" {
			h.fs[path] = append([]byte(nil), data...)
		} else {
			h.fs[path] = append(h.fs[path], data...)
		}
		return 0, "", ""
	case "chmod":
		m, err := strconv.ParseUint(f[1], 8, 32)
		if err != nil {
			return 1, "", "chmod: invalid mode: " + f[1]
		}
		h.modes[h.root+f[2]] = os.FileMode(m)
		return 0, "", ""
	case "sha256sum":
		path := h.root + f[1]
		data, ok := h.fs[path]
		if !ok {
			return 1, "", "sha256sum: " + f[1] + ": No such file or directory"
		}
		return 0, sha256Hex(data) + "  " + f[1], ""
	case "wc":
		// wc -c < 'path'
		i := 0
		for ; i < len(f) && f[i] != "<"; i++ {
		}
		if i+1 >= len(f) {
			return 1, "", "wc: missing stdin redirect"
		}
		path := h.root + f[i+1]
		data, ok := h.fs[path]
		if !ok {
			return 1, "", "wc: " + f[i+1] + ": No such file or directory"
		}
		return 0, strconv.Itoa(len(data)), ""
	case "mv":
		src, dst := h.root+f[1], h.root+f[2]
		data, ok := h.fs[src]
		if !ok {
			return 1, "", "mv: cannot stat '" + f[1] + "': No such file or directory"
		}
		h.fs[dst] = data
		if m, ok := h.modes[src]; ok {
			h.modes[dst] = m
		}
		delete(h.fs, src)
		delete(h.modes, src)
		return 0, "", ""
	}
	return 127, "", "sh: 1: " + f[0] + ": not found"
}

// splitOps splits a command on `&&`, respecting single quotes.
func splitOps(s string) []string {
	var ops []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			inQuote = !inQuote
			cur.WriteByte(c)
		case c == '&' && !inQuote && i+1 < len(s) && s[i+1] == '&':
			ops = append(ops, cur.String())
			cur.Reset()
			i++
		default:
			cur.WriteByte(c)
		}
	}
	return append(ops, cur.String())
}

// lexFields splits one operation into tokens, unquoting single-quoted runs and
// honouring the `'\”` escape shellQuote produces.
func lexFields(op string) []string {
	var fields []string
	var cur strings.Builder
	inQuote, started := false, false
	for i := 0; i < len(op); i++ {
		c := op[i]
		switch {
		case c == '\'' && inQuote && i+3 < len(op) && op[i+1] == '\\' && op[i+2] == '\'' && op[i+3] == '\'':
			cur.WriteByte('\'')
			i += 3
		case c == '\'':
			inQuote = !inQuote
			started = true
		case (c == ' ' || c == '\t') && !inQuote:
			if started {
				fields = append(fields, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	if started {
		fields = append(fields, cur.String())
	}
	return fields
}

// transferOver drives the real transfer against the fake host. Progress is
// gathered into a slice so the reporting contract is assertable without a TTY.
func transferOver(host *shellHost) (*transfer, *[]progressEvent) {
	var events []progressEvent
	t := transfer{
		name: "box.test",
		run:  host.Run,
		progress: func(e WriteEvent, wrote, total int) {
			events = append(events, progressEvent{event: e, wrote: wrote, total: total})
		},
	}
	return &t, &events
}

type progressEvent struct {
	event        WriteEvent
	wrote, total int
}

// measuredPayload is the file the Contabo defect was measured with: 29,250,507
// bytes of cafe binary. It is generated rather than read because the byte
// VALUES do not matter to the transport — only their count and their hash do
// — and a test that needed a real 29 MB binary would be a test that could not
// run in a gate.
func measuredPayload(t *testing.T) []byte {
	t.Helper()
	data := make([]byte, 29_250_507)
	rng := rand.New(rand.NewSource(1))
	if _, err := rng.Read(data); err != nil {
		t.Fatal(err)
	}
	return data
}

// The measured defect, as numbers: local size against host size.
const (
	measuredLocalBytes  = 29_250_507
	measuredRemoteBytes = 17_625_600
)

// ---------------------------------------------------------------------------
// the chunk count is derived from the size
// ---------------------------------------------------------------------------

func TestTheChunkCountIsDerivedFromTheSize(t *testing.T) {
	for _, tc := range []struct {
		bytes int
		want  int
	}{
		{0, 1},                      // an empty file is still a file
		{1, 1},                      //
		{WriteChunkSize - 1, 1},     //
		{WriteChunkSize, 1},         // exactly at the boundary: one chunk
		{WriteChunkSize + 1, 2},     // one byte over: two
		{3 * WriteChunkSize, 3},     //
		{measuredLocalBytes, 28},    // the 29 MB binary: 28 chunks
		{28*WriteChunkSize + 1, 29}, //
	} {
		if got := chunkCount(tc.bytes); got != tc.want {
			t.Errorf("chunkCount(%d) = %d, want %d", tc.bytes, got, tc.want)
		}
	}
}

// A payload at the chunk boundary is one chunk, not two, and it lands whole.
// The off-by-one this watches for is a first chunk that carries two chunks'
// worth, or a second chunk that carries nothing.
func TestAChunkBoundaryAtExactlyOneMebibyteIsOneChunk(t *testing.T) {
	host := newShellHost(t)
	tr, _ := transferOver(host)

	exact := make([]byte, WriteChunkSize)
	for i := range exact {
		exact[i] = byte(i)
	}
	if err := tr.write(context.Background(), "/usr/local/bin/cafe", exact, 0o755); err != nil {
		t.Fatalf("write a 1 MiB payload: %v", err)
	}
	if host.catCount() != 1 {
		t.Errorf("a payload of exactly 1 MiB was sent in %d cats, want 1", host.catCount())
	}
	if got, _ := host.read("/usr/local/bin/cafe"); string(got) != string(exact) {
		t.Error("the payload that landed is not the payload that was sent")
	}

	// And one byte over the boundary is two chunks with the second carrying
	// exactly one byte, which is where a `hi := lo + chunk` without the
	// clamp would read past the payload.
	over := append(append([]byte(nil), exact...), 0x42)
	host2 := newShellHost(t)
	tr2, _ := transferOver(host2)
	if err := tr2.write(context.Background(), "/usr/local/bin/cafe", over, 0o755); err != nil {
		t.Fatalf("write a 1 MiB + 1 payload: %v", err)
	}
	if host2.catCount() != 2 {
		t.Errorf("a payload of 1 MiB + 1 was sent in %d cats, want 2", host2.catCount())
	}
	if got, _ := host2.read("/usr/local/bin/cafe"); string(got) != string(over) {
		t.Error("the payload that landed is not the payload that was sent")
	}
}

// ---------------------------------------------------------------------------
// the happy path
// ---------------------------------------------------------------------------

func TestAMultiChunkWriteLandsWholeAndTheHostKeepsNoSibling(t *testing.T) {
	host := newShellHost(t)
	tr, _ := transferOver(host)

	payload := make([]byte, 2*WriteChunkSize+513)
	copy(payload, "#!/bin/sh\nexec cafe-real \"$@\"\n")
	for i := len("#!/bin/sh\nexec cafe-real \"$@\"\n"); i < len(payload); i++ {
		payload[i] = byte(i * 7)
	}
	tr, events := transferOver(host)
	if err := tr.write(context.Background(), "/usr/local/bin/cafe", payload, 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if host.catCount() != 3 {
		t.Errorf("the payload crossed in %d cats, want 3", host.catCount())
	}
	got, ok := host.read("/usr/local/bin/cafe")
	if !ok {
		t.Fatal("the file is not on the host")
	}
	if string(got) != string(payload) {
		t.Error("the bytes on the host are not the bytes sent")
	}
	if host.mode("/usr/local/bin/cafe") != 0o755 {
		t.Errorf("the file's mode on the host is %o, want 755", host.mode("/usr/local/bin/cafe"))
	}
	if host.exists("/usr/local/bin/.cafe.cafe-tmp") {
		t.Error("the sibling was left behind after a successful write")
	}
	if first := (*events)[0]; first.event != WriteChunk || first.wrote != WriteChunkSize || first.total != len(payload) {
		t.Errorf("the first progress event is %v", first)
	}
	last := (*events)[len(*events)-1]
	if last.event != WriteVerified || last.wrote != len(payload) || last.total != len(payload) {
		t.Errorf("the last progress event is %v, want the verified event over the whole payload", last)
	}
}

// The payload is bytes on a pipe, never a word in a command: a binary that
// contains a quote, a newline, a backtick or a `$` must land byte for byte.
// The interpreter unquotes what the emitter quoted, so a payload that could be
// read as shell would land changed and this would catch it.
func TestAPayloadThatLooksLikeShellLandsByteForByte(t *testing.T) {
	host := newShellHost(t)
	tr, _ := transferOver(host)
	payload := []byte("#!/bin/sh\necho '$(rm -rf /) `id` \"$HOME\"\n&& mv x y\n")
	if err := tr.write(context.Background(), "/usr/local/bin/cafe", payload, 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok := host.read("/usr/local/bin/cafe")
	if !ok || string(got) != string(payload) {
		t.Errorf("the bytes on the host are not the bytes sent:\n got %q\nwant %q", got, payload)
	}
}

// An empty payload still creates the file: a host that ended up with no file
// where it expected a zero-length one is a host whose first step then failed
// on a missing path.
func TestAnEmptyPayloadStillCreatesTheFile(t *testing.T) {
	host := newShellHost(t)
	tr, _ := transferOver(host)
	if err := tr.write(context.Background(), "/etc/cafe/box-identity.json", nil, 0o644); err != nil {
		t.Fatalf("write an empty payload: %v", err)
	}
	got, ok := host.read("/etc/cafe/box-identity.json")
	if !ok || len(got) != 0 {
		t.Errorf("the host holds %d bytes, want an empty file", len(got))
	}
	if host.catCount() != 1 {
		t.Errorf("an empty payload was sent in %d cats, want 1", host.catCount())
	}
}

// The destination is never opened for writing: every `cat` targets the
// sibling, and the only command that names the destination is the rename that
// happens after the hash matched. This is the structural half of "never a
// truncated one" — the hash gate is the other half, and a test that asserted
// only the gate would pass against a transfer that opened the destination and
// truncated it with a matching prefix.
func TestOnlyTheSiblingIsEverOpenedForWriting(t *testing.T) {
	host := newShellHost(t)
	tr, _ := transferOver(host)
	if err := tr.write(context.Background(), "/usr/local/bin/cafe", make([]byte, 3*WriteChunkSize), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	sawRename := false
	for _, command := range host.commands {
		switch {
		case strings.HasPrefix(command, "cat "):
			fields := lexFields(command)
			if len(fields) < 3 || fields[2] != "/usr/local/bin/.cafe.cafe-tmp" {
				t.Fatalf("a cat wrote to %q, which is not the sibling", command)
			}
		case strings.HasPrefix(command, "mv "):
			fields := lexFields(command)
			if len(fields) != 3 || fields[2] != "/usr/local/bin/cafe" {
				t.Fatalf("the rename moved %q, which is not the sibling onto the destination", command)
			}
			sawRename = true
		default:
			if strings.Contains(command, "cat > /usr/local/bin/cafe") || strings.Contains(command, "cat >> /usr/local/bin/cafe") {
				t.Fatalf("the destination was opened for writing: %q", command)
			}
		}
	}
	if !sawRename {
		t.Error("no rename ran, so the file was never moved into place")
	}
}

// A sibling a dead attempt left behind cannot contaminate the next one: the
// transfer clears it and the first chunk truncates it, so a 29 MB leftover
// never becomes a prefix of the next binary.
func TestALeftoverSiblingCannotContaminateTheNextWrite(t *testing.T) {
	host := newShellHost(t)
	host.fs[host.root+"/usr/local/bin/.cafe.cafe-tmp"] = make([]byte, WriteChunkSize+7)
	tr, _ := transferOver(host)
	payload := []byte("#!/bin/sh\nexec cafe \"$@\"\n")
	if err := tr.write(context.Background(), "/usr/local/bin/cafe", payload, 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, _ := host.read("/usr/local/bin/cafe")
	if string(got) != string(payload) {
		t.Errorf("the leftover sibling leaked into the new file: %d bytes on the host, want %d", len(got), len(payload))
	}
}

// ---------------------------------------------------------------------------
// the stall: a chunk that stops being acknowledged must become an error,
// not a line that stops moving
// ---------------------------------------------------------------------------

// stallHost is a host whose reader stops acknowledging: on the nth `cat` it
// waits for the caller's context to die and answers with its error, the way a
// wedged ssh child behaves — the pipe is open, nothing arrives, the process
// sleeps. A transfer with no deadline waits here forever, which is the
// measured 10 m 50 s hang at "8.0 MiB of 19.9 MiB written".
type stallHost struct {
	*shellHost
	stallOnCat int
}

func (s *stallHost) Run(ctx context.Context, command string, stdin []byte) (Output, error) {
	if strings.HasPrefix(command, "cat ") && s.cats+1 == s.stallOnCat {
		s.cats++
		s.commands = append(s.commands, command)
		<-ctx.Done()
		return Output{}, ctx.Err()
	}
	return s.shellHost.Run(ctx, command, stdin)
}

// slowHost is the control over the bound: it acks every chunk, but slowly —
// just under the deadline. A bound that killed this transfer would be worse
// than the hang, because it would turn every slow link into a refusal.
type slowHost struct {
	*shellHost
	perCat time.Duration
}

func (s *slowHost) Run(ctx context.Context, command string, stdin []byte) (Output, error) {
	if strings.HasPrefix(command, "cat ") {
		select {
		case <-ctx.Done():
			return Output{}, ctx.Err()
		case <-time.After(s.perCat):
		}
	}
	return s.shellHost.Run(ctx, command, stdin)
}

// A stalled chunk times out at the bound, naming the chunk, the bytes
// acknowledged and the elapsed time — the three numbers the measured hang's
// last line did not carry. The payload is three chunks stalling on the
// second, the shape of "8.0 MiB of 19.9 MiB written" stopping mid-sentence.
func TestAStalledChunkTimesOutInsteadOfHanging(t *testing.T) {
	base := newShellHost(t)
	host := &stallHost{shellHost: base, stallOnCat: 2}
	var events []progressEvent
	tr := transfer{
		name: "box.test", run: host.Run,
		progress:     func(e WriteEvent, wrote, total int) { events = append(events, progressEvent{event: e, wrote: wrote, total: total}) },
		chunkTimeout: 150 * time.Millisecond,
		totalTimeout: 10 * time.Second,
	}

	payload := make([]byte, 2*WriteChunkSize+513)
	start := time.Now()
	err := tr.write(context.Background(), "/usr/local/bin/cafe", payload, 0o755)
	took := time.Since(start)
	if err == nil {
		t.Fatal("a stalled chunk was reported as success — the hang is back")
	}
	var stall writeStall
	if !errors.As(err, &stall) {
		t.Fatalf("the refusal is a %T, want a writeStall: %v", err, err)
	}
	if stall.at != 1 {
		t.Errorf("the stall names chunk index %d, want 1 (the second chunk)", stall.at)
	}
	if stall.chunks != 3 {
		t.Errorf("the stall names %d chunks, want 3", stall.chunks)
	}
	if stall.crossed != WriteChunkSize {
		t.Errorf("the stall names %d bytes acknowledged, want the first chunk's %d", stall.crossed, WriteChunkSize)
	}
	if stall.total != len(payload) {
		t.Errorf("the stall names %d total bytes, want %d", stall.total, len(payload))
	}
	for _, want := range []string{"STALLED", "chunk 2 of 3", strconv.Itoa(WriteChunkSize), "elapsed", "restarts from zero"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the stall does not carry %q: %v", want, err)
		}
	}
	// The bound discriminates by time: the error arrived at the bound, not at
	// the patience of whoever watched it. Ten seconds would be a hang with a
	// nicer message; the assertion is that it answered in about the bound.
	if took > 5*time.Second {
		t.Errorf("the stall took %s to answer, want about the 150 ms bound", took)
	}
	// One chunk crossed before the stall, so one progress event ran — the
	// operator saw the line move and then the refusal, not silence.
	if len(events) != 1 || events[0].wrote != WriteChunkSize {
		t.Errorf("progress before the stall = %v, want the first chunk's event", events)
	}
	if host.exists("/usr/local/bin/cafe") {
		t.Error("the destination holds a file after a stalled write")
	}
	if host.exists("/usr/local/bin/.cafe.cafe-tmp") {
		t.Error("the partial sibling was left behind after a stalled write")
	}
}

// A slow-but-steady reader must NOT trip the deadline: every chunk acked just
// under the bound still lands whole. This is the control that keeps the bound
// honest — a deadline that refused a working transfer would be worse than the
// hang it was written for.
func TestASlowButSteadyTransferDoesNotTripTheDeadline(t *testing.T) {
	base := newShellHost(t)
	host := &slowHost{shellHost: base, perCat: 80 * time.Millisecond}
	tr := transfer{
		name: "box.test", run: host.Run,
		chunkTimeout: 2 * time.Second,
		totalTimeout: 30 * time.Second,
	}

	payload := make([]byte, 3*WriteChunkSize)
	for i := range payload {
		payload[i] = byte(i * 11)
	}
	if err := tr.write(context.Background(), "/usr/local/bin/cafe", payload, 0o755); err != nil {
		t.Fatalf("a slow-but-working transfer was refused: %v", err)
	}
	if got, _ := base.read("/usr/local/bin/cafe"); string(got) != string(payload) {
		t.Error("the bytes on the host are not the bytes sent")
	}
}

// The whole-transfer bound fires too: a reader that acks every chunk but
// takes longer than the transfer allows is refused with the bytes so far and
// the elapsed time, because a transfer that outlives the capability's TTL has
// already spent the token it was setting up to use.
func TestTheWholeTransferBoundFires(t *testing.T) {
	base := newShellHost(t)
	host := &slowHost{shellHost: base, perCat: 120 * time.Millisecond}
	tr := transfer{
		name: "box.test", run: host.Run,
		chunkTimeout: 5 * time.Second,
		totalTimeout: 200 * time.Millisecond,
	}

	err := tr.write(context.Background(), "/usr/local/bin/cafe", make([]byte, 5*WriteChunkSize), 0o755)
	if err == nil {
		t.Fatal("a transfer past its whole-transfer bound was reported as success")
	}
	var stall writeStall
	if !errors.As(err, &stall) {
		t.Fatalf("the refusal is a %T, want a writeStall: %v", err, err)
	}
	if !strings.Contains(err.Error(), "whole-transfer bound") {
		t.Errorf("the refusal does not say which bound fired: %v", err)
	}
	if !strings.Contains(err.Error(), "elapsed") || !strings.Contains(err.Error(), "restarts from zero") {
		t.Errorf("the refusal does not carry the elapsed time and the retry cost: %v", err)
	}
}

// The red control, in-gate: the SAME stalled reader against a transfer with
// both bounds disabled — the write as it stands on master — never answers on
// its own. It returns only when the CALLER gives up, and what it answers then
// is a dead transport, not a stall naming the chunk. A test that asserted a
// stall here would hang until `go test -timeout` killed it; the assertion
// below is that the bound is what discriminates, because without it there is
// no stall error at all.
func TestTheUnboundedWriteNeverAnswersAStallOnItsOwn(t *testing.T) {
	base := newShellHost(t)
	host := &stallHost{shellHost: base, stallOnCat: 2}
	tr := transfer{
		name: "box.test", run: host.Run,
		chunkTimeout: -1,
		totalTimeout: -1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := tr.write(ctx, "/usr/local/bin/cafe", make([]byte, 2*WriteChunkSize+513), 0o755)
	if err == nil {
		t.Fatal("a stalled write with no bound reported success")
	}
	var stall writeStall
	if errors.As(err, &stall) {
		t.Fatalf("the unbounded write answered as a stall — the bound cannot be what discriminates: %v", err)
	}
	if !strings.Contains(err.Error(), "the transport died") {
		t.Errorf("the unbounded write answered %v, want the caller's own timeout as a dead transport", err)
	}
	// And the control half: the same fixture with the bound DOES name the
	// chunk. Without this half a transfer that always failed would pass.
	base2 := newShellHost(t)
	host2 := &stallHost{shellHost: base2, stallOnCat: 2}
	tr2 := transfer{
		name: "box.test", run: host2.Run,
		chunkTimeout: 100 * time.Millisecond,
		totalTimeout: 10 * time.Second,
	}
	err2 := tr2.write(context.Background(), "/usr/local/bin/cafe", make([]byte, 2*WriteChunkSize+513), 0o755)
	var stall2 writeStall
	if !errors.As(err2, &stall2) {
		t.Fatalf("the bounded write against the same fixture is a %T, want a writeStall: %v", err2, err2)
	}
}

// ---------------------------------------------------------------------------
// the failure mode the gate must hold
// ---------------------------------------------------------------------------

// The measured defect, reproduced exactly: a 29,250,507-byte payload against a
// transport that caps total stdin at 17,625,600 bytes and answers success to
// every chunk. The write is refused, the refusal names BOTH hashes and BOTH
// byte counts, and nothing is left at the destination.
func TestTheMeasuredTruncationIsRefusedAndNamesBothHashes(t *testing.T) {
	host := newShellHost(t)
	host.pipeCap = measuredRemoteBytes
	tr, _ := transferOver(host)

	payload := measuredPayload(t)
	err := tr.write(context.Background(), "/usr/local/bin/cafe", payload, 0o755)
	if err == nil {
		t.Fatal("a truncated arrival was reported as success — the defect is back")
	}
	var mismatch writeMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("the refusal is a %T, want a writeMismatch: %v", err, err)
	}
	if mismatch.localBytes != measuredLocalBytes {
		t.Errorf("the refusal names %d local bytes, want %d", mismatch.localBytes, measuredLocalBytes)
	}
	if mismatch.remoteBytes != measuredRemoteBytes {
		t.Errorf("the refusal names %d remote bytes, want %d", mismatch.remoteBytes, measuredRemoteBytes)
	}
	if mismatch.localHash == mismatch.remoteHash {
		t.Error("the two hashes are equal, so the refusal cannot name a disagreement")
	}
	if !strings.Contains(err.Error(), mismatch.localHash) || !strings.Contains(err.Error(), mismatch.remoteHash) {
		t.Errorf("the refusal does not carry both hashes: %v", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(measuredLocalBytes)) ||
		!strings.Contains(err.Error(), strconv.Itoa(measuredRemoteBytes)) {
		t.Errorf("the refusal does not carry both byte counts: %v", err)
	}
	// The numbers the Contabo defect was measured with, asserted as such.
	if got, _ := host.read("/usr/local/bin/cafe"); got != nil {
		t.Errorf("the destination holds %d bytes after a refused write, want nothing", len(got))
	}
	if host.exists("/usr/local/bin/.cafe.cafe-tmp") {
		t.Error("the partial sibling was left behind after a refused write")
	}
	if !mismatch.cleaned {
		t.Error("the refusal says the partial file could not be removed, but the interpreter removed it")
	}
}

// The same defect on a box that already HAS a binary: the upgrade case, and
// the one that produced "Exec format error". The host keeps its previous
// binary, byte for byte, and the refusal names the hashes.
func TestATruncatedWriteLeavesThePreviousBinaryAlone(t *testing.T) {
	host := newShellHost(t)
	host.pipeCap = measuredRemoteBytes
	previous := []byte("#!/bin/sh\nexit 0\n")
	host.fs[host.root+"/usr/local/bin/cafe"] = append([]byte(nil), previous...)
	host.modes[host.root+"/usr/local/bin/cafe"] = 0o755
	tr, _ := transferOver(host)

	err := tr.write(context.Background(), "/usr/local/bin/cafe", measuredPayload(t), 0o755)
	if err == nil {
		t.Fatal("a truncated arrival was reported as success")
	}
	var mismatch writeMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("the refusal is a %T, want a writeMismatch: %v", err, err)
	}
	got, ok := host.read("/usr/local/bin/cafe")
	if !ok {
		t.Fatal("the previous binary is gone from the host")
	}
	if string(got) != string(previous) {
		t.Errorf("the previous binary was changed: %d bytes on the host, want the original %d", len(got), len(previous))
	}
	if host.exists("/usr/local/bin/.cafe.cafe-tmp") {
		t.Error("the partial sibling was left behind")
	}
	// The control half: without the cap, the same write upgrades the binary.
	// A test that only asserted the refusal would also pass against a
	// transfer that always failed.
	whole := newShellHost(t)
	whole.fs[whole.root+"/usr/local/bin/cafe"] = append([]byte(nil), previous...)
	trWhole, _ := transferOver(whole)
	if err := trWhole.write(context.Background(), "/usr/local/bin/cafe", []byte("#!/bin/sh\nexec cafe-real \"$@\"\n"), 0o755); err != nil {
		t.Fatalf("the same write without the cap failed: %v", err)
	}
	if got, _ := whole.read("/usr/local/bin/cafe"); string(got) != "#!/bin/sh\nexec cafe-real \"$@\"\n" {
		t.Error("without the cap the new binary did not land")
	}
}

// The transport dies mid-stream — the child is killed after k chunks. The
// destination keeps its previous binary or nothing, and the error names the
// LOCAL hash and the byte counts rather than inventing a remote one.
func TestATransportCutMidStreamKeepsThePreviousBinary(t *testing.T) {
	for _, cut := range []int{1, 3, 5} {
		host := newShellHost(t)
		host.dieOnCat = cut
		previous := []byte("#!/bin/sh\nexit 0\n")
		host.fs[host.root+"/usr/local/bin/cafe"] = append([]byte(nil), previous...)
		tr, _ := transferOver(host)

		payload := make([]byte, 5*WriteChunkSize) // five chunks, cut in the first, third and last
		err := tr.write(context.Background(), "/usr/local/bin/cafe", payload, 0o755)
		if err == nil {
			t.Fatalf("cut after chunk %d: a dead transport was reported as success", cut)
		}
		var mismatch writeMismatch
		if errors.As(err, &mismatch) {
			t.Fatalf("cut after chunk %d: the refusal is a hash mismatch, but no hash could have been taken", cut)
		}
		if !strings.Contains(err.Error(), sha256Hex(payload)) {
			t.Errorf("cut after chunk %d: the error does not name the local hash: %v", cut, err)
		}
		if !strings.Contains(err.Error(), "no remote hash could be taken") {
			t.Errorf("cut after chunk %d: the error does not say the remote hash is unproven: %v", cut, err)
		}
		got, ok := host.read("/usr/local/bin/cafe")
		if !ok || string(got) != string(previous) {
			t.Errorf("cut after chunk %d: the destination holds %d bytes, want the previous %d", cut, len(got), len(previous))
		}
	}
}

// A transport that dies on a box with NO previous binary leaves nothing at
// all: the sibling is the only thing on disk and it is removed. "Nothing" is
// the other half of the brief's "either its previous binary or nothing".
func TestATransportCutOnABareHostLeavesNothing(t *testing.T) {
	host := newShellHost(t)
	host.dieOnCat = 4
	tr, _ := transferOver(host)
	if err := tr.write(context.Background(), "/usr/local/bin/cafe", make([]byte, 9*WriteChunkSize), 0o755); err == nil {
		t.Fatal("a dead transport was reported as success")
	}
	if host.exists("/usr/local/bin/cafe") {
		t.Error("the destination exists on a host that had no binary")
	}
	if host.exists("/usr/local/bin/.cafe.cafe-tmp") {
		t.Error("the partial sibling was left behind")
	}
}

// A host whose sha256sum answers something unexpected is refused, not parsed:
// a verifier that guessed would be a verifier that could pass a truncated
// write on a host with a busybox spelling.
func TestAHostThatAnswersSomethingElseIsRefused(t *testing.T) {
	host := newShellHost(t)
	weird := &weirdHashHost{shellHost: host}
	tr := transfer{name: "box.test", run: weird.Run}
	err := tr.write(context.Background(), "/usr/local/bin/cafe", []byte("binary"), 0o755)
	if err == nil {
		t.Fatal("a host that printed something unusable was reported as success")
	}
	if !strings.Contains(err.Error(), "unexpected sha256sum output") {
		t.Errorf("the refusal does not name what the host answered: %v", err)
	}
	if !weird.askedForHash {
		t.Error("the host was never asked to hash the file")
	}
}

// weirdHashHost is a host that answers sha256sum with prose instead of a
// digest — the shape a busybox, an alias or a shell function produces.
type weirdHashHost struct {
	*shellHost
	askedForHash bool
}

func (w *weirdHashHost) Run(ctx context.Context, command string, stdin []byte) (Output, error) {
	out, err := w.shellHost.Run(ctx, command, stdin)
	if err != nil {
		return out, err
	}
	if strings.Contains(command, "sha256sum") {
		// Exit zero, wrong English: the parser, not the status, has to catch
		// this one, because a host that prints a digest in a foreign spelling
		// and succeeds would otherwise be believed.
		w.askedForHash = true
		out.Stdout = "sha256sum: with -b"
		out.Stderr = ""
	}
	return out, nil
}

// Progress is reported per chunk and once at the match, so an operator
// watching a slow box sees the bytes cross and then the sentence that says the
// bytes that crossed are the bytes that were sent.
func TestProgressReportsTheBytesThatCrossedAndTheFinalMatch(t *testing.T) {
	host := newShellHost(t)
	tr, events := transferOver(host)
	payload := make([]byte, 2*WriteChunkSize+1)
	if err := tr.write(context.Background(), "/usr/local/bin/cafe", payload, 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := []progressEvent{
		{WriteChunk, WriteChunkSize, len(payload)},
		{WriteChunk, 2 * WriteChunkSize, len(payload)},
		{WriteChunk, len(payload), len(payload)},
		{WriteVerified, len(payload), len(payload)},
	}
	if len(*events) != len(want) {
		t.Fatalf("%d progress events, want %d: %v", len(*events), len(want), *events)
	}
	for i, w := range want {
		got := (*events)[i]
		if got != w {
			t.Errorf("event %d is %+v, want %+v", i, got, w)
		}
	}
}

// A write with no progress sink is silent and still correct: the library's
// default must not print, because some callers are parsing stdout.
func TestANilProgressSinkIsSilent(t *testing.T) {
	host := newShellHost(t)
	tr := transfer{name: "box.test", run: host.Run}
	if err := tr.write(context.Background(), "/usr/local/bin/cafe", []byte("binary"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got, _ := host.read("/usr/local/bin/cafe"); string(got) != "binary" {
		t.Error("the file did not land")
	}
}

// The paths the walk writes are not only /usr/local/bin: the daemon's identity
// document and the unit file go through the same transfer. The same test over
// a path with several directories is the difference between a transfer that
// works on a flat path and one that works on a host.
func TestAWriteIntoANewDirectoryTreeMakesTheDirectories(t *testing.T) {
	host := newShellHost(t)
	tr, _ := transferOver(host)
	if err := tr.write(context.Background(), "/etc/cafe/box-identity.json", []byte(`{"box_id":"box_1"}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok := host.read("/etc/cafe/box-identity.json")
	if !ok || string(got) != `{"box_id":"box_1"}` {
		t.Error("the identity document did not land in a fresh directory tree")
	}
	if host.mode("/etc/cafe/box-identity.json") != 0o644 {
		t.Errorf("the document's mode is %o, want 644", host.mode("/etc/cafe/box-identity.json"))
	}
	if _, err := os.Stat(filepath.Join(host.root, "etc", "cafe")); err != nil {
		t.Errorf("the interpreter's own directory was not made: %v", err)
	}
}
