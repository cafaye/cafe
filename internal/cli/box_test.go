package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cafaye/cafe/internal/box"
)

// The seams a box test needs, in one place: a keyring the test owns and a
// control plane that counts. The count is the assertion the whole file turns on.
type boxTestRing struct{ entries map[string]string }

func newBoxTestRing() *boxTestRing { return &boxTestRing{entries: map[string]string{}} }

func (r *boxTestRing) Get(service, account string) (string, error) {
	if v, ok := r.entries[service+"/"+account]; ok {
		return v, nil
	}
	return "", box.ErrNotFound
}

func (r *boxTestRing) Set(service, account, value string) error {
	r.entries[service+"/"+account] = value
	return nil
}

func (r *boxTestRing) Delete(service, account string) error {
	delete(r.entries, service+"/"+account)
	return nil
}

// countingBoxControlPlane counts redemptions. Box's single-use means a
// second redemption is ALWAYS refused server-side, so a test that asserted
// "the second enroll fails" would pass against a client that redeemed twice.
// The count is what distinguishes idempotence from luck.
type countingBoxControlPlane struct {
	calls    int
	tokens   []string
	answer   *box.Installation
	err      error
	tripwire bool
}

func (c *countingBoxControlPlane) Redeem(_ context.Context, token string, _ ed25519.PublicKey) (*box.Installation, error) {
	c.calls++
	c.tokens = append(c.tokens, token)
	if c.tripwire {
		panic("unreachable: an enrollment that recovered from its journal must not present the capability again")
	}
	if c.err != nil {
		return nil, c.err
	}
	return c.answer, nil
}

func fakeBoxInstallation() *box.Installation {
	return &box.Installation{
		BoxID:     "box-11111111-2222-3333-4444-555555555555",
		AccountID: "acct-aaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Status:    "active",
		Phase:     "enrolled",
	}
}

// boxTestEnv is the deps every case below wires a command tree to: a directory
// the test owns, a keyring in memory, and a control plane that answers with a
// planted installation. Nothing here touches the machine's config directory,
// its keychain or the network, which is the only reason these tests can run on
// a bare CI runner.
func boxTestEnv(t *testing.T) (deps boxDeps, cp *countingBoxControlPlane, ring *boxTestRing) {
	t.Helper()
	cp = &countingBoxControlPlane{answer: fakeBoxInstallation()}
	ring = newBoxTestRing()
	deps = boxDeps{dir: t.TempDir(), ring: ring, controlPlane: cp, url: "https://box.test"}
	return deps, cp, ring
}

// boxTestToken is a capability in box's own shape. It is not a real
// one and nothing accepts it but the fake.
var boxTestToken = "cap_" + strings.Repeat("b", 64)

// runBoxCLI drives ONE cafe box command tree against the injected deps and hands
// back the exit code plus both streams — exactly what main() does with the real
// process, minus the process. It goes through the parent's dispatcher rather
// than at a verb directly, so the verb lookup and its usage errors are what is
// being tested and not something beside them.
func runBoxCLI(t *testing.T, deps boxDeps, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion, Context: context.Background()}
	err := newBoxCommand(deps).Execute(env, args)
	return codeFor(*env, err), out.String(), errOut.String()
}

// runBoxCLIStdin is runBoxCLI with a stream the test can hand a value on. It is
// a second helper rather than a parameter on the first because the commands that
// read a stream are the exception, and a reader every invocation had to fill in
// would end up empty in most of them — and empty is a refusal.
func runBoxCLIStdin(t *testing.T, deps boxDeps, stdin io.Reader, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &errOut, Stdin: stdin, Version: testVersion, Context: context.Background()}
	err := newBoxCommand(deps).Execute(env, args)
	return codeFor(*env, err), out.String(), errOut.String()
}

// explodingReader fails the test if it is read. It is the guard that a command
// given what it needs another way never opens the stream.
type explodingReader struct{ t *testing.T }

func (r *explodingReader) Read([]byte) (int, error) {
	r.t.Error("the command read stdin when it was given what it needed another way")
	return 0, io.EOF
}

// enrollResult decodes the document an enroll wrote, so an assertion is made
// about the numbers the command reported rather than about a string match
// against them.
func enrollResult(t *testing.T, stdout string) box.EnrollResult {
	t.Helper()
	var res box.EnrollResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("enroll stdout is not the enroll document: %v\n%s", err, stdout)
	}
	return res
}

// fakeBoxKey is a real keypair, so the pending keyring entry and the journal
// this case plants are the same bytes a cafe run would have written.
type fakeBoxKey struct {
	text   string
	public string
}

func newFakeBoxKey(t *testing.T) fakeBoxKey {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return fakeBoxKey{
		text:   base64.RawStdEncoding.EncodeToString(private),
		public: base64.RawStdEncoding.EncodeToString(private.Public().(ed25519.PublicKey)),
	}
}

// THE enrollment test: the whole sequence, run twice, through the real
// dispatcher.
//
// Two invocations rather than one, and two separate command trees sharing one
// directory, because the state that has to survive a crash is the state on disk
// and not the state in a process. The control plane is injected so the count is the
// network's count; the filesystem is real, which is the part a mock would
// otherwise make worthless.
func TestBoxEnrollIsIdempotentThroughTheCommandTree(t *testing.T) {
	deps, cp, ring := boxTestEnv(t)

	code, stdout, stderr := runBoxCLI(t, deps, "enroll", "--token", boxTestToken)
	if code != 0 {
		t.Fatalf("first enroll exited %d (stderr: %s)", code, stderr)
	}
	first := enrollResult(t, stdout)
	if first.Action != box.ActionEnrolled {
		t.Errorf("first action = %q, want %q", first.Action, box.ActionEnrolled)
	}
	if first.Redemptions != 1 {
		t.Errorf("first redemptions = %d, want 1", first.Redemptions)
	}
	if first.Identity == nil || first.Identity.BoxID != fakeBoxInstallation().BoxID {
		t.Errorf("first identity = %+v, want it to name the enrolled box", first.Identity)
	}
	if strings.Count(stderr, "\n") != 1 || !strings.HasPrefix(stderr, "cafe box: ") {
		t.Errorf("first stderr is not the one-sentence verdict:\n%s", stderr)
	}
	if _, err := deps.store().Identity(); err != nil {
		t.Fatalf("no identity after a successful enroll: %v", err)
	}
	private := ring.entries["cafe/box/"+fakeBoxInstallation().BoxID]
	if private == "" {
		t.Fatal("no box key in the keyring")
	}

	code, stdout, stderr = runBoxCLI(t, deps, "enroll", "--token", boxTestToken)
	if code != 0 {
		t.Fatalf("second enroll exited %d (stderr: %s), want it to be a success", code, stderr)
	}
	second := enrollResult(t, stdout)
	if second.Action != box.ActionAlreadyEnrolled {
		t.Errorf("second action = %q, want %q", second.Action, box.ActionAlreadyEnrolled)
	}
	if second.Redemptions != 0 {
		t.Errorf("second attempt redeemed %d times, want 0", second.Redemptions)
	}
	if cp.calls != 1 {
		t.Fatalf("control plane was called %d times across two enrolls, want exactly 1", cp.calls)
	}
	if len(cp.tokens) != 1 || cp.tokens[0] != boxTestToken {
		t.Errorf("control plane was presented %v, want the one token once", cp.tokens)
	}
	if strings.Contains(stdout, private) || strings.Contains(stderr, private) {
		t.Error("an enroll printed the box private key")
	}
}

// The convergence case the brief is about, at the command level: an enrollment
// that got as far as presenting the capability and no further is FINISHED by a
// retry without presenting it again. The control plane here is a tripwire — any
// attempt to redeem panics the test — which is the only way to prove the retry
// did not go back to the network.
func TestBoxEnrollRecoversFromAnInterruptedSequence(t *testing.T) {
	deps, _, _ := boxTestEnv(t)

	// Interrupt the sequence the way a process that died between the redemption
	// and the write would have: the keypair is filed under the pending account
	// and the journal records the installation facts. Both are written as the
	// bytes cafe itself writes, because the state under test is the file.
	store := deps.store()
	key := newFakeBoxKey(t)
	if err := store.Ring.Set(box.KeyringService, box.KeyringAccountPending, key.text); err != nil {
		t.Fatal(err)
	}
	inst := fakeBoxInstallation()
	journal := map[string]any{
		"state":       "redeemed",
		"public_key":  key.public,
		"box_id":      inst.BoxID,
		"account_id":  inst.AccountID,
		"status":      inst.Status,
		"phase":       inst.Phase,
		"box_url":     "https://box.test",
		"started_at":  "2026-10-09T11:59:00Z",
		"redeemed_at": "2026-10-09T12:00:00Z",
	}
	if err := writeBoxJournal(t, store.Dir, journal); err != nil {
		t.Fatal(err)
	}

	// The control plane of THIS tree is the tripwire: it refuses to answer, so a
	// retry that presented the capability fails the test rather than passing it.
	code, stdout, stderr := runBoxCLI(t, deps, "enroll", "--token", "cap_spent")
	if code != 0 {
		t.Fatalf("recovering enroll exited %d (stderr: %s)", code, stderr)
	}
	got := enrollResult(t, stdout)
	if !got.Recovered {
		t.Errorf("recovered = false, want the retry to report that it recovered")
	}
	if got.Redemptions != 0 {
		t.Errorf("recovered redemptions = %d, want 0", got.Redemptions)
	}
	id, err := store.Identity()
	if err != nil {
		t.Fatalf("the recovered run wrote no identity: %v", err)
	}
	if id.BoxID != inst.BoxID {
		t.Errorf("identity names %q, want %q", id.BoxID, inst.BoxID)
	}
	// The pending keypair moved to its permanent account, so a later crash does
	// not strand a key.
	if _, err := store.Ring.Get(box.KeyringService, "box/"+inst.BoxID); err != nil {
		t.Errorf("no permanent key after a recovered enroll: %v", err)
	}
	if _, err := store.Ring.Get(box.KeyringService, box.KeyringAccountPending); !errors.Is(err, box.ErrNotFound) {
		t.Error("the pending keypair was not cleared, so a later enrollment would reuse it")
	}
}

// ---------------------------------------------------------------------------
// the capability on a pipe
// ---------------------------------------------------------------------------

// `cafe box enroll --token -` reads the capability off the command's stdin.
//
// This is the command `cafe box setup` runs ON THE HOST, over SSH: the plan's
// enroll step is `/usr/local/bin/cafe box enroll --token - --box <url>` with the
// capability on the pipe and nowhere else. Measured on the Contabo box, the
// host's enroll answered box's redeem lookup for sha256("-") — the literal dash
// went out AS the capability, box answered 404, and setup failed at
// box-enrolled. b67f561's commit message said the stdin read was there; its diff
// did not contain it, which is the whole reason this test exists.
func TestBoxEnrollReadsTheCapabilityFromStdinAtDash(t *testing.T) {
	deps, cp, _ := boxTestEnv(t)

	// A pipe delivers whatever the transport left on the end of it, so the
	// capability here carries surrounding whitespace the way a real one does.
	code, stdout, stderr := runBoxCLIStdin(t, deps,
		strings.NewReader("\n  "+boxTestToken+"\t\n"),
		"enroll", "--token", "-", "--box", "https://box.test")
	if code != 0 {
		t.Fatalf("enroll --token - exited %d (stderr: %s)", code, stderr)
	}
	if cp.calls != 1 {
		t.Fatalf("the control plane was called %d times, want 1", cp.calls)
	}
	if len(cp.tokens) != 1 {
		t.Fatalf("the control plane was presented %v, want one capability", cp.tokens)
	}
	if cp.tokens[0] != boxTestToken {
		t.Fatalf("the capability presented = %q, want the token from stdin (%q)", cp.tokens[0], boxTestToken)
	}
	// The document is still the enroll document, on stdout, with the private key
	// nowhere in either stream.
	if got := enrollResult(t, stdout); got.Identity == nil || got.Identity.BoxID != fakeBoxInstallation().BoxID {
		t.Errorf("the enroll document's identity = %+v, want the enrolled box", got.Identity)
	}
}

// A dash whose pipe is empty is a USAGE mistake rather than a failure: the
// command was handed a stream with nothing on it, so nothing was attempted, and
// exit 2 says so — which is also what tells this apart from the store's own
// refusal of a blank token, which would be exit 1.
func TestBoxEnrollWithAnEmptyPipeIsAUsageError(t *testing.T) {
	for name, stdin := range map[string]string{"empty": "", "blank lines": "  \n\t\n"} {
		t.Run(name, func(t *testing.T) {
			// A fresh directory per case: the first case must not leave an
			// enrolled box behind that would let the second answer
			// "already enrolled" instead of reporting the pipe.
			deps, cp, _ := boxTestEnv(t)
			code, _, stderr := runBoxCLIStdin(t, deps, strings.NewReader(stdin),
				"enroll", "--token", "-", "--box", "https://box.test")
			if code != exitUsage {
				t.Errorf("enroll --token - with %s exited %d, want %d (stderr: %s)", name, code, exitUsage, stderr)
			}
			if !strings.Contains(stderr, "--token") {
				t.Errorf("stderr = %q, want it to name --token", stderr)
			}
			if cp.calls != 0 {
				t.Errorf("the control plane was called %d times with no capability", cp.calls)
			}
		})
	}
}

// A token named by the flag is read from the flag, and stdin is not touched: a
// command that opens a stream nobody asked it to read blocks forever on a
// terminal, which is the failure this reader turns into a test failure.
func TestBoxEnrollWithAFlagTokenDoesNotReadStdin(t *testing.T) {
	deps, cp, _ := boxTestEnv(t)
	code, _, stderr := runBoxCLIStdin(t, deps, &explodingReader{t: t},
		"enroll", "--token", boxTestToken, "--box", "https://box.test")
	if code != 0 {
		t.Fatalf("enroll --token <value> exited %d (stderr: %s)", code, stderr)
	}
	if len(cp.tokens) != 1 || cp.tokens[0] != boxTestToken {
		t.Errorf("the control plane was presented %v, want the flag's token", cp.tokens)
	}
}

// The private key is never printed: every command's stdout AND stderr are
// searched for the key's bytes. This is invariant 1, and the control half is
// that the public key IS printed, because a build that redacted everything
// would pass this test by leaking nothing at all.
func TestNoBoxCommandEverPrintsThePrivateKey(t *testing.T) {
	deps, _, ring := boxTestEnv(t)
	if code, _, stderr := runBoxCLI(t, deps, "enroll", "--token", boxTestToken); code != 0 {
		t.Fatalf("enroll exited %d (stderr: %s)", code, stderr)
	}
	store := deps.store()
	id, err := store.Identity()
	if err != nil {
		t.Fatal(err)
	}
	private := ring.entries["cafe/box/"+id.BoxID]
	if private == "" || private == id.PublicKey {
		t.Fatal("the fixture holds no private key distinct from the public one")
	}

	var sawPublicKey bool
	for _, args := range [][]string{
		{"status"}, {"status", "--json"}, {"plan"}, {"plan", "--json"},
		{"logs"}, {"logs", "--json"}, {"verify"}, {"verify", "--json"},
		{"apply", "--phase", "substrate"}, {"apply", "--phase", "substrate", "--json"},
		{"enroll", "--token", boxTestToken},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, stdout, stderr := runBoxCLI(t, deps, args...)
			for stream, got := range map[string]string{"stdout": stdout, "stderr": stderr} {
				if strings.Contains(got, private) {
					t.Errorf("%s carries the box private key:\n%s", stream, got)
				}
			}
			if strings.Contains(stdout+stderr, id.PublicKey) {
				sawPublicKey = true
			}
			// --json means JSON, and the default means not: a flag that does
			// nothing is a flag nobody will pass.
			if args[len(args)-1] == "--json" && !json.Valid([]byte(stdout)) {
				t.Errorf("%s stdout is not JSON: %s", strings.Join(args, " "), stdout)
			}
			if args[len(args)-1] == "substrate" && json.Valid([]byte(stdout)) {
				t.Errorf("the human document of %s is JSON: %s", strings.Join(args, " "), stdout)
			}
			if code != 0 && code != 1 {
				t.Fatalf("%s exited %d (stderr: %s)", strings.Join(args, " "), code, stderr)
			}
		})
	}
	if !sawPublicKey {
		t.Error("no command printed the public key, so the control half of this test proved nothing")
	}
}

// The stderr/stdout split is a contract, not a habit: stdout is the document,
// stderr is exactly one sentence, and `--json` is what selects the machine
// document.
func TestBoxCommandsSplitProseFromTheDocument(t *testing.T) {
	deps, _, _ := boxTestEnv(t)
	if code, _, stderr := runBoxCLI(t, deps, "enroll", "--token", boxTestToken); code != 0 {
		t.Fatalf("enroll exited %d (stderr: %s)", code, stderr)
	}
	for _, args := range [][]string{{"status"}, {"status", "--json"}} {
		code, stdout, stderr := runBoxCLI(t, deps, args...)
		if code != 0 {
			t.Fatalf("%s exited %d (stderr: %s)", args, code, stderr)
		}
		lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
		if len(lines) != 1 {
			t.Errorf("%s stderr is not one sentence:\n%s", args, stderr)
		}
		valid := json.Valid([]byte(stdout))
		if args[len(args)-1] == "--json" && !valid {
			t.Errorf("%s stdout is not JSON: %s", args, stdout)
		}
		if args[len(args)-1] != "--json" && valid {
			t.Errorf("%s stdout is JSON, want the human document: %s", args, stdout)
		}
	}
}

// The lifecycle verbs on a box that has never enrolled. The ones that need an
// identity refuse with the sentence that says what to run; `status` and
// `verify` are reports rather than refusals, because "did anything happen
// here" is the question an agent asks a fresh box.
func TestBoxLifecycleOnAnUnenrolledBox(t *testing.T) {
	deps, _, _ := boxTestEnv(t)
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "plan", args: []string{"plan"}, want: box.ErrNotEnrolled.Error(), wantErr: true},
		{name: "apply", args: []string{"apply", "--phase", "substrate"}, want: box.ErrNotEnrolled.Error(), wantErr: true},
		{name: "status", args: []string{"status"}, want: "not enrolled"},
		{name: "verify", args: []string{"verify"}, want: "not enrolled", wantErr: true},
		{name: "logs", args: []string{"logs"}, want: "(no events)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := runBoxCLI(t, deps, tt.args...)
			if !strings.Contains(stdout+stderr, tt.want) {
				t.Errorf("%s output does not name %q:\n%s\n%s", tt.args, tt.want, stdout, stderr)
			}
			if tt.wantErr && code != exitFailure {
				t.Errorf("%s exited %d, want %d", tt.args, code, exitFailure)
			}
			if !tt.wantErr && code != 0 {
				t.Errorf("%s exited %d, want 0 (stderr: %s)", tt.args, code, stderr)
			}
		})
	}
}

// A spent capability is refused honestly: exit 1, a sentence naming the cause,
// and no identity written. The journal is left where the NEXT capability can
// converge from — the alternative is a client that cleared it and stranded the
// keypair it had already filed.
func TestBoxEnrollRefusesASpentCapabilityHonestly(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "redeemed", err: box.ErrCapabilityRedeemed, want: "already been redeemed"},
		{name: "expired", err: box.ErrCapabilityExpired, want: "expired"},
		{name: "unknown", err: box.ErrCapabilityUnknown, want: "does not know"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, cp, _ := boxTestEnv(t)
			cp.err = tt.err
			code, _, stderr := runBoxCLI(t, deps, "enroll", "--token", boxTestToken)
			if code != exitFailure {
				t.Errorf("exit = %d, want %d (stderr: %s)", code, exitFailure, stderr)
			}
			if !strings.Contains(stderr, tt.want) {
				t.Errorf("stderr = %q, want it to name %q", stderr, tt.want)
			}
			if !strings.Contains(stderr, "mint a fresh token") {
				t.Errorf("stderr = %q, want the remedy", stderr)
			}
			store := deps.store()
			if _, err := store.Identity(); !errors.Is(err, box.ErrNotEnrolled) {
				t.Errorf("identity err = %v, want ErrNotEnrolled", err)
			}
			st, err := store.Status()
			if err != nil {
				t.Fatal(err)
			}
			if st.Journal != box.JournalPrepared {
				t.Errorf("journal state = %q, want %q", st.Journal, box.JournalPrepared)
			}
		})
	}
}

// The usage errors. A box command invoked wrongly exits 2, not 1, and its
// message names the command the person typed.
func TestBoxUsageErrors(t *testing.T) {
	deps, _, _ := boxTestEnv(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no token", args: []string{"enroll"}, want: "--token <capability>"},
		{name: "unknown verb", args: []string{"frobnicate"}, want: "cafe box \"frobnicate\" is not a command"},
		{name: "phase twice", args: []string{"apply", "--phase", "data", "data"}, want: "got the phase twice"},
		{name: "no phase", args: []string{"apply"}, want: "wants --phase"},
		{name: "too many args", args: []string{"status", "extra"}, want: "wants 0 arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, stderr := runBoxCLI(t, deps, tt.args...)
			if code != exitUsage {
				t.Errorf("%s exited %d, want %d (stderr: %s)", tt.args, code, exitUsage, stderr)
			}
			if !strings.Contains(stderr, tt.want) {
				t.Errorf("stderr = %q, want %q", stderr, tt.want)
			}
		})
	}
}

// `apply --phase N` takes an index as well as a name, an index outside the
// table is refused rather than clamped, and skipping a phase is refused with a
// sentence naming the phase that IS next.
func TestBoxApplyTakesAnIndexOrAName(t *testing.T) {
	deps, _, _ := boxTestEnv(t)
	if code, _, stderr := runBoxCLI(t, deps, "enroll", "--token", boxTestToken); code != 0 {
		t.Fatalf("enroll exited %d (stderr: %s)", code, stderr)
	}
	if code, _, stderr := runBoxCLI(t, deps, "apply", "--phase", "1"); code != 0 {
		t.Fatalf("apply --phase 1 exited %d (stderr: %s)", code, stderr)
	}
	if code, _, stderr := runBoxCLI(t, deps, "apply", "--phase", "99"); code != exitUsage {
		t.Errorf("apply --phase 99 exited %d, want %d (stderr: %s)", code, exitUsage, stderr)
	}
	if _, _, stderr := runBoxCLI(t, deps, "apply", "--phase", "99"); !strings.Contains(stderr, "no such phase") {
		t.Errorf("stderr = %q, want it to name the phases", stderr)
	}
	// `data` is the phase after `substrate`, so it is allowed; `stack` is two
	// along, so it is refused, and the refusal names `data` as the one that is
	// next — which is the sentence that keeps a box honest about where it is.
	if code, _, _ := runBoxCLI(t, deps, "apply", "--phase", "data"); code != 0 {
		t.Errorf("applying the next phase exited %d, want 0", code)
	}
	if code, _, _ := runBoxCLI(t, deps, "apply", "--phase", "stack"); code != 0 {
		t.Errorf("applying the last phases exited %d, want 0", code)
	}
}

// The install is sequential, and refusing to skip is the refusal that keeps a
// box honest about where it is. It is a fresh store because the question is
// about the FIRST step being jumped, not about a later one.
func TestBoxApplyRefusesToSkipAPhase(t *testing.T) {
	deps, _, _ := boxTestEnv(t)
	if code, _, stderr := runBoxCLI(t, deps, "enroll", "--token", boxTestToken); code != 0 {
		t.Fatalf("enroll exited %d (stderr: %s)", code, stderr)
	}
	code, _, stderr := runBoxCLI(t, deps, "apply", "--phase", "data")
	if code != exitFailure {
		t.Fatalf("skipping substrate exited %d, want %d (stderr: %s)", code, exitFailure, stderr)
	}
	if !strings.Contains(stderr, "substrate") {
		t.Errorf("stderr = %q, want it to name the phase that IS next", stderr)
	}
	// And the refusal left the box where it was.
	st, err := deps.store().Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Identity.Phase != box.PhaseNames()[0] {
		t.Errorf("phase = %q, want %q", st.Identity.Phase, box.PhaseNames()[0])
	}
}

// Bare `cafe box` is a question, not a mistake: it answers with its help and
// exits 0. It must also not touch the machine — the zero deps resolve to the
// config directory only when a verb runs.
func TestBareBoxIsAQuestionNotAMistake(t *testing.T) {
	var out, errOut bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion, Context: context.Background()}
	if err := newBoxCommand(boxDeps{}).Execute(env, nil); err != nil {
		t.Fatalf("bare `cafe box` failed: %v", err)
	}
	if !strings.Contains(out.String(), "cafe box <enroll|status|plan|apply|setup|wipe|deploy|rollback|logs|verify|publish|edge>") {
		t.Errorf("help does not carry its usage line:\n%s", out.String())
	}
	for _, verb := range []string{"enroll", "status", "plan", "apply", "setup", "wipe", "deploy", "rollback", "logs", "verify", "publish", "edge"} {
		if !strings.Contains(out.String(), verb) {
			t.Errorf("help does not list %q:\n%s", verb, out.String())
		}
	}
}

// The journal is written by the box package, so this case writes the same bytes
// it writes and no others. It is here rather than in internal/box because the
// file's NAME is the command layer's contract with the config directory.
func writeBoxJournal(t *testing.T, dir string, journal map[string]any) error {
	t.Helper()
	raw, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	return os.WriteFile(filepath.Join(dir, box.JournalFileName), raw, 0o600)
}
