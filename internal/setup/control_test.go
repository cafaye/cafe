package setup

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// The shared connection, as the gate sees it.
//
// Everything below drives the real transfer over a connection whose runner and
// whose take-down are counted. That is the point: "the chunks share one
// connection" is a claim about counts, and the interpreter underneath performs
// the commands for real — mkdir, cat with a redirect, chmod, sha256sum, wc, mv —
// so the delivery being watched is the delivery that runs on a box.

// countingMaster is the shared connection: it forwards to the interpreter and
// remembers what was asked of it. The socket is held as a value so a row can
// assert the connection had one.
type countingMaster struct {
	host     *shellHost
	sock     string
	commands []masterCall
	closed   int
}

type masterCall struct {
	command string
	stdin   int
}

func (m *countingMaster) command(ctx context.Context, command string, stdin []byte) (Output, error) {
	m.commands = append(m.commands, masterCall{command: command, stdin: len(stdin)})
	return m.host.Run(ctx, command, stdin)
}

func (m *countingMaster) close(context.Context) {
	m.closed++
}

func (m *countingMaster) catCalls() int {
	n := 0
	for _, c := range m.commands {
		if strings.HasPrefix(c.command, "cat ") {
			n++
		}
	}
	return n
}

// The chunks ride one connection, the bytes land, and the connection is taken
// down exactly once — whatever the delivery answered. The stdin lengths are
// asserted because the shared channel must not become an excuse for one big
// write: every chunk still crosses as a bounded 1 MiB pipe write.
func TestTheChunksShareOneConnectionAndItIsTakenDown(t *testing.T) {
	host := newShellHost(t)
	master := &countingMaster{host: host, sock: "/tmp/cafe-ctl-0000/s"}
	payload := make([]byte, 3*WriteChunkSize-1)
	for i := range payload {
		payload[i] = byte(i * 13)
	}

	if err := writeOver(context.Background(), master, "box.test", nil, "/usr/local/bin/cafe", payload, 0o755); err != nil {
		t.Fatalf("write over the shared connection: %v", err)
	}
	if master.closed != 1 {
		t.Errorf("the connection was taken down %d times, want exactly 1", master.closed)
	}
	if got := master.catCalls(); got != 3 {
		t.Errorf("%d chunk commands crossed the shared connection, want 3", got)
	}
	for _, c := range master.commands {
		if !strings.HasPrefix(c.command, "cat ") {
			continue
		}
		if c.stdin > WriteChunkSize {
			t.Errorf("a chunk carried %d bytes on the shared connection, want at most %d", c.stdin, WriteChunkSize)
		}
	}
	got, ok := host.read("/usr/local/bin/cafe")
	if !ok || string(got) != string(payload) {
		t.Error("the bytes on the host are not the bytes sent")
	}
	if host.mode("/usr/local/bin/cafe") != 0o755 {
		t.Errorf("the file's mode is %o, want 755", host.mode("/usr/local/bin/cafe"))
	}
	if host.exists("/usr/local/bin/.cafe.cafe-tmp") {
		t.Error("the sibling was left behind")
	}
}

// The connection is taken down on the REFUSAL path too. That is the half a
// test that only watched the happy path would miss: a write that failed and
// left a channel open would be a channel open on a box whose operator believes
// the run is over.
func TestTheSharedConnectionIsTakenDownAfterARefusal(t *testing.T) {
	for _, cut := range []int{1, 2} {
		host := newShellHost(t)
		host.dieOnCat = cut
		master := &countingMaster{host: host, sock: "/tmp/cafe-ctl-0000/s"}

		err := writeOver(context.Background(), master, "box.test", nil, "/usr/local/bin/cafe", make([]byte, 4*WriteChunkSize), 0o755)
		if err == nil {
			t.Fatalf("cut %d: a dead transport was reported as success", cut)
		}
		if master.closed != 1 {
			t.Errorf("cut %d: the connection was taken down %d times, want 1", cut, master.closed)
		}
		if host.exists("/usr/local/bin/.cafe.cafe-tmp") {
			t.Errorf("cut %d: the partial sibling was left behind", cut)
		}
	}
}

// The measured truncation, reproduced through the SHARED connection: the
// interpreter caps total stdin at 17,625,600 bytes and answers success to every
// chunk. The write is still refused, the refusal still names both hashes and
// both byte counts, and the destination still holds nothing. This is the row
// that says a faster transport has not loosened the gate — a change that made
// the shared connection swallow the delivery would land here rather than on a
// box.
func TestTheSharedConnectionStillRefusesATruncatedArrival(t *testing.T) {
	host := newShellHost(t)
	host.pipeCap = measuredRemoteBytes
	master := &countingMaster{host: host, sock: "/tmp/cafe-ctl-0000/s"}

	err := writeOver(context.Background(), master, "box.test", nil, "/usr/local/bin/cafe", measuredPayload(t), 0o755)
	if err == nil {
		t.Fatal("a truncated arrival was reported as success through the shared connection")
	}
	var mismatch writeMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("the refusal is a %T, want a writeMismatch: %v", err, err)
	}
	if mismatch.localBytes != measuredLocalBytes || mismatch.remoteBytes != measuredRemoteBytes {
		t.Errorf("the refusal names %d/%d bytes, want %d/%d",
			mismatch.localBytes, mismatch.remoteBytes, measuredLocalBytes, measuredRemoteBytes)
	}
	if mismatch.localHash == mismatch.remoteHash {
		t.Error("the two hashes are equal, so the refusal cannot name a disagreement")
	}
	if host.exists("/usr/local/bin/cafe") {
		t.Error("the destination holds a file after a refused write")
	}
	if master.closed != 1 {
		t.Errorf("the connection was taken down %d times, want 1", master.closed)
	}
}

// The write's policy and the walk's differ in ONE line, and the whole of the
// difference is the control master. Everything else — batch mode, the host-key
// rule, the timeouts, the port, the identity file — is shared through baseArgs,
// which is what keeps a transport change from quietly dropping the policy a
// check depends on.
func TestTheWritesPolicyDiffersFromTheWalksInOneLine(t *testing.T) {
	host := SSHHost{Name: "contabo", Port: 2222, IdentityFile: "/keys/id"}
	// Read the SAME construction the transport hands to ssh, in both modes. The
	// first version of this test rebuilt the write's list itself — baseArgs plus
	// controlArgs — while `run` built sshArgs plus controlArgs, and ssh honors
	// the FIRST -o, so ControlMaster=no won and no master ever opened. A test
	// that produces its own args cannot see the transport's. argsFor is the one
	// function both now read.
	walk, write := host.argsFor(""), host.argsFor("/tmp/cafe-ctl-0000/s")

	dropControl := func(args []string) []string {
		out := make([]string, 0, len(args))
		for i := 0; i < len(args); i++ {
			if args[i] != "-o" {
				out = append(out, args[i])
				continue
			}
			if i+1 < len(args) && (strings.HasPrefix(args[i+1], "ControlMaster=") ||
				strings.HasPrefix(args[i+1], "ControlPath=") ||
				strings.HasPrefix(args[i+1], "ControlPersist=")) {
				i++
				continue
			}
			out = append(out, args[i])
		}
		return out
	}
	if !reflect.DeepEqual(dropControl(walk), dropControl(write)) {
		t.Errorf("the two policies differ beyond the control master:\nwalk  %v\nwrite %v", walk, write)
	}
	if !containsPair(walk, "ControlMaster=no") {
		t.Errorf("the walk lost its one-connection-per-command rule: %v", walk)
	}
	for _, want := range []string{"ControlMaster=auto", "ControlPath=/tmp/cafe-ctl-0000/s", "ControlPersist=yes"} {
		if !containsPair(write, want) {
			t.Errorf("the write policy is missing %q: %v", want, write)
		}
	}
	// The FIRST value wins in ssh_config, so a change that appended the master
	// options to the walk's own list would leave the walk's `no` in force and
	// the master would never open — a silent no-op that the timing alone would
	// catch. The write list must carry one and only one ControlMaster.
	if containsPair(write, "ControlMaster=no") {
		t.Errorf("the write policy stacked both ControlMaster values, and ssh honors the first: %v", write)
	}
}

func containsPair(args []string, want string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-o" && args[i+1] == want {
			return true
		}
	}
	return false
}

// One chunk is one command, and it goes out with no master to open or close:
// a unit file and an identity document are hundreds of bytes, and two extra
// processes would buy nothing.
func TestOneChunkNeverOpensASharedConnection(t *testing.T) {
	for _, total := range []int{0, 1, 1 << 10, WriteChunkSize} {
		if needsControl(total) {
			t.Errorf("a payload of %d bytes wants a control master", total)
		}
	}
	for _, total := range []int{WriteChunkSize + 1, 2 * WriteChunkSize, measuredLocalBytes} {
		if !needsControl(total) {
			t.Errorf("a payload of %d bytes does not want a control master", total)
		}
	}
}

// The socket lives in a private directory of its own, and the directory is
// short enough to be a unix socket path at all. A control socket is a
// credential-bearing channel, so its access control is its directory.
func TestTheControlSocketIsPrivateAndShort(t *testing.T) {
	dir, err := controlSocketDir()
	if err != nil {
		t.Fatalf("controlSocketDir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("the socket directory is %o, want 700", info.Mode().Perm())
	}
	if len(dir)+len("/s") > 100 {
		t.Errorf("the socket path %q is %d bytes, which some platforms refuse", dir, len(dir)+2)
	}
}
