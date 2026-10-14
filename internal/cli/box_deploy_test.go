package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cafaye/cafe/internal/stack"
)

// deployTestBox is the seam the deploy verbs run against: an edge whose hosts
// and certificates are a file the test wrote, a container runtime that
// records its argv, and a health gate that answers from a real loopback HTTP
// server carrying the host name.
//
// It exists because the verbs have to be provable on a bare CI runner: no
// Docker, no DNS, no TLS. The argv and the Host header are what the box will
// really send, and the gate is answered by a real socket.
type deployTestBox struct {
	root string
	// argv is what the container runtime was asked to do, which is the only
	// part of a deploy that can be asserted without a container runtime.
	argv [][]string
	// edge is the loopback address the health gate is dialed through.
	edge string
	// failOn is the digest prefix whose release fails its health gate: the
	// broken deploy the gate's `release history` item names.
	failOn string
}

func (b *deployTestBox) run(_ context.Context, argv []string) error {
	b.argv = append(b.argv, append([]string(nil), argv...))
	return nil
}

var expiry = time.Now().Add(90 * 24 * time.Hour).UTC().Format(time.RFC3339)

func (b *deployTestBox) writeEdge(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(b.root, "edge.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newDeployEnv wires the verbs to one stack in a temporary directory and
// one seam the test owns. The control-plane halves of boxDeps are left nil,
// because a deploy never speaks to the control plane — that is the claim the
// rollback test below makes by running with nothing injected at all.
func newDeployEnv(t *testing.T) (deps boxDeps, box *deployTestBox) {
	t.Helper()
	root := t.TempDir()
	box = &deployTestBox{root: root}
	// A real HTTP server the gate is dialed through, with the Host header the
	// public name would carry. It serves whatever the release's edge state
	// says, so a deploy that switched the pointer serves the new release.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "broken.cafaye.test" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	box.edge = strings.TrimPrefix(srv.URL, "http://")
	deps = boxDeps{
		dir: t.TempDir(),
		stack: boxStackDeps{
			root: root,
			run:  box.run,
			// The gate is a real request through a real socket, with the host
			// name the public traffic would carry. A release whose digest is
			// the one the test named answers 503: that is the broken deploy,
			// and it has to reach the gate rather than be refused at the edge
			// or there would be nothing to roll back from.
			ready: func(ctx context.Context, rel stack.Release) error {
				if box.failOn != "" && strings.Contains(rel.Image, strings.Repeat(box.failOn, 64)) {
					return errors.New("the release answered 503 through the edge")
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+box.edge+"/", nil)
				if err != nil {
					return err
				}
				req.Host = rel.Host
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					return errors.New("the edge answered " + resp.Status)
				}
				return nil
			},
		},
	}
	return deps, box
}

func deployDoc(t *testing.T, stdout string) stack.Report {
	t.Helper()
	var rep stack.Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("stdout is not the deploy document: %v\n%s", err, stdout)
	}
	return rep
}

// goodEdge is the live state the box's edge publishes, written by the test
// with an expiry computed from the clock — a fixture with a fixed date would
// be a fixture that starts failing in six months, which is a gate that rots
// rather than one that holds.
const goodEdge = `{"listen":"127.0.0.1:8443","hosts":{"api.cafaye.test":{"routed":true,"cert_subject":"api.cafaye.test","cert_not_after":%q},"auth.cafaye.test":{"routed":true,"cert_subject":"auth.cafaye.test","cert_not_after":%q}}}`

// TestBoxDeployShipsAPantryBuildEndToEnd is the first Phase B sign-off item as
// a command: a build pinned by digest reaches the box, is pulled by digest,
// is staged, and traffic points at it.
func TestBoxDeployShipsAPantryBuildEndToEnd(t *testing.T) {
	deps, box := newDeployEnv(t)
	box.writeEdge(t, fmt.Sprintf(goodEdge, expiry, expiry))

	code, stdout, stderr := runBoxCLI(t, deps, "deploy",
		"--build", "ghcr.io/cafaye/api@sha256:"+strings.Repeat("a", 64),
		"--tag", "1.4.0", "--host", "api.cafaye.test", "--secrets", "api/production")
	if code != 0 {
		t.Fatalf("cafe box deploy exited %d: %s", code, stderr)
	}
	rep := deployDoc(t, stdout)
	if rep.Outcome != stack.OutcomeDeployed {
		t.Fatalf("the report says %q, want %q (%s)", rep.Outcome, stack.OutcomeDeployed, rep.Detail)
	}
	if rep.Release.Host != "api.cafaye.test" {
		t.Fatalf("the release answers on %q, want api.cafaye.test", rep.Release.Host)
	}
	// The pull is BY DIGEST. A pull by tag is how a box ends up running
	// something it never intended.
	var pulled string
	for _, argv := range box.argv {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "image pull") {
			pulled = joined
		}
	}
	if !strings.Contains(pulled, "@sha256:"+strings.Repeat("a", 64)) {
		t.Fatalf("the box pulled %q, and the pull is not pinned by digest", pulled)
	}
	// The secret set traveled as a FILE reference, never in argv.
	for _, argv := range box.argv {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "api/production") && !strings.Contains(joined, "--env-file") {
			t.Fatalf("a secret set name reached argv: %s", joined)
		}
		if strings.Contains(joined, "run") && !strings.Contains(joined, "--env-file") {
			t.Fatalf("the release was run without an env-file: %s", joined)
		}
	}
}

// TestBoxDeployARollbackFlipsBackOnPurpose is the gate's `release history`
// item: the second deploy exits non-zero, and the box is serving the FIRST
// release again afterwards.
func TestBoxDeployARollbackFlipsBackOnPurpose(t *testing.T) {
	deps, box := newDeployEnv(t)
	box.writeEdge(t, fmt.Sprintf(goodEdge, expiry, expiry))

	first := []string{"deploy",
		"--build", "ghcr.io/cafaye/api@sha256:" + strings.Repeat("a", 64),
		"--host", "api.cafaye.test"}
	if code, _, stderr := runBoxCLI(t, deps, first...); code != 0 {
		t.Fatalf("the first deploy exited %d: %s", code, stderr)
	}
	// The second deploy is BROKEN: its release answers 503 through the edge,
	// so it reaches the health gate and fails it — which is the only case in
	// which a rollback is the right answer.
	box.failOn = "b"
	broken := []string{"deploy",
		"--build", "ghcr.io/cafaye/api@sha256:" + strings.Repeat("b", 64),
		"--host", "api.cafaye.test", "--secrets", "api/production"}
	code, stdout, stderr := runBoxCLI(t, deps, broken...)
	if code == 0 {
		t.Fatal("a deploy whose release failed its health gate exited zero, so a script would have believed it worked")
	}
	rep := deployDoc(t, stdout)
	if !rep.RolledBack {
		t.Fatalf("the report does not say the box rolled back: %+v", rep)
	}
	if rep.Failed == nil {
		t.Fatal("the report names no failed release")
	}
	if !strings.Contains(stderr, "again") {
		t.Fatalf("stderr does not say the box is serving the previous release again:\n%s", stderr)
	}
	// And the box really is: the pointer names the first release.
	store, err := stack.Open(filepath.Join(deps.stack.root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	cur, err := store.Current()
	if err != nil {
		t.Fatal(err)
	}
	rels, err := store.Releases()
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) < 2 {
		t.Fatalf("the history holds %d releases, want both", len(rels))
	}
	if cur.ID == rep.Failed.ID {
		t.Fatalf("the pointer names the failed release %s", cur.ID)
	}
	h, err := store.History()
	if err != nil {
		t.Fatal(err)
	}
	if h.LastGood != cur.ID {
		t.Fatalf("last good is %s and current is %s, and a rollback after a broken deploy must go back to the good one", h.LastGood, cur.ID)
	}
}

// TestBoxRollbackIsOneSymlinkAndNoPull is the claim that a rollback is
// instant: nothing is pulled, nothing is run, and the pointer moves.
func TestBoxRollbackIsOneSymlinkAndNoPull(t *testing.T) {
	deps, box := newDeployEnv(t)
	box.writeEdge(t, fmt.Sprintf(goodEdge, expiry, expiry))
	for _, d := range []string{"a", "b"} {
		if code, _, stderr := runBoxCLI(t, deps, "deploy",
			"--build", "ghcr.io/cafaye/api@sha256:"+strings.Repeat(d, 64),
			"--host", "api.cafaye.test"); code != 0 {
			t.Fatalf("deploy: %d %s", code, stderr)
		}
	}
	before := len(box.argv)

	code, stdout, stderr := runBoxCLI(t, deps, "rollback", "--service", "api")
	if code != 0 {
		t.Fatalf("cafe box rollback exited %d: %s", code, stderr)
	}
	if len(box.argv) != before {
		t.Fatalf("the rollback ran %d argv, want none — a rollback that starts a container is not instant", len(box.argv)-before)
	}
	var rep stack.Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("stdout is not the rollback document: %v\n%s", err, stdout)
	}
	if !rep.RolledBack {
		t.Fatalf("the report does not say it was a rollback: %+v", rep)
	}
	store, err := stack.Open(filepath.Join(deps.stack.root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	if cur, err := store.Current(); err != nil || cur.ID != rep.Release.ID {
		t.Fatalf("the pointer names %+v and the report says %s", cur, rep.Release.ID)
	}
}

// TestBoxRollbackToANamedRelease goes back two, by id, which the
// previous-release default alone cannot express.
func TestBoxRollbackToANamedRelease(t *testing.T) {
	deps, box := newDeployEnv(t)
	box.writeEdge(t, fmt.Sprintf(goodEdge, expiry, expiry))
	var firstID string
	for _, d := range []string{"a", "b", "c"} {
		code, stdout, stderr := runBoxCLI(t, deps, "deploy",
			"--build", "ghcr.io/cafaye/api@sha256:"+strings.Repeat(d, 64),
			"--host", "api.cafaye.test")
		if code != 0 {
			t.Fatalf("deploy: %d %s", code, stderr)
		}
		if firstID == "" {
			firstID = deployDoc(t, stdout).Release.ID
		}
	}
	code, stdout, _ := runBoxCLI(t, deps, "rollback", firstID)
	if code != 0 {
		t.Fatalf("rollback to a named release exited %d", code)
	}
	if got := deployDoc(t, stdout).Release.ID; got != firstID {
		t.Fatalf("the box is serving %q, want %q", got, firstID)
	}
}

// ---------------------------------------------------------------------------
// the flags may come after the release id — the same shape the host verbs take
// ---------------------------------------------------------------------------

// `rollback` takes an optional positional release id, so the flags may sit on
// either side of it: `cafe box rollback <id> --service api` is the same
// command as `cafe box rollback --service api <id>`. Each form names its own
// first release, so the assertion is that each lands on the id IT named.
func TestRollbackTakesFlagsOnEitherSideOfTheReleaseID(t *testing.T) {
	run := func(t *testing.T, order func(id string) []string) {
		t.Helper()
		deps, box := newDeployEnv(t)
		box.writeEdge(t, fmt.Sprintf(goodEdge, expiry, expiry))
		var firstID string
		for _, d := range []string{"a", "b", "c"} {
			code, stdout, stderr := runBoxCLI(t, deps, "deploy",
				"--build", "ghcr.io/cafaye/api@sha256:"+strings.Repeat(d, 64),
				"--host", "api.cafaye.test")
			if code != 0 {
				t.Fatalf("deploy: %d %s", code, stderr)
			}
			if firstID == "" {
				firstID = deployDoc(t, stdout).Release.ID
			}
		}
		code, stdout, stderr := runBoxCLI(t, deps, append([]string{"rollback"}, order(firstID)...)...)
		if code != 0 {
			t.Fatalf("rollback %v exited %d: %s", order(firstID), code, stderr)
		}
		if got := deployDoc(t, stdout).Release.ID; got != firstID {
			t.Errorf("rollback %v landed on %q, want the named %q", order(firstID), got, firstID)
		}
	}

	run(t, func(id string) []string { return []string{"--service", "api", id} })
	run(t, func(id string) []string { return []string{id, "--service", "api"} })
}

// A misspelled flag after the release id is refused as a flag.
func TestARollbackFlagTypoAfterTheReleaseIDIsRefusedAsAFlag(t *testing.T) {
	deps, box := newDeployEnv(t)
	box.writeEdge(t, fmt.Sprintf(goodEdge, expiry, expiry))

	code, _, stderr := runBoxCLI(t, deps, "rollback", "sample-stack-1", "--servicee", "api")
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitUsage, stderr)
	}
	if !strings.Contains(stderr, "servicee") {
		t.Errorf("the refusal does not name the flag it did not recognize: %s", stderr)
	}
}

// TestBoxDeployRefusalsAreUsageErrors is the distinction every script that
// calls this verb depends on: a request cafe cannot even make is exit 2, and
// a request the box refused is exit 1 with the document still on stdout.
func TestBoxDeployRefusalsAreUsageErrors(t *testing.T) {
	deps, box := newDeployEnv(t)
	box.writeEdge(t, fmt.Sprintf(goodEdge, expiry, expiry))
	tests := []struct {
		name string
		args []string
		want string
		code int
	}{
		{
			name: "a build that is not pinned",
			args: []string{"deploy", "--build", "ghcr.io/cafaye/api:latest", "--host", "api.cafaye.test"},
			want: "name@sha256", code: 2,
		},
		{
			name: "no build at all",
			args: []string{"deploy", "--host", "api.cafaye.test"},
			want: "--build", code: 2,
		},
		{
			name: "no host",
			args: []string{"deploy", "--build", "ghcr.io/cafaye/api@sha256:" + strings.Repeat("a", 64)},
			want: "--host", code: 2,
		},
		{
			name: "a host the edge does not route",
			args: []string{"deploy", "--build", "ghcr.io/cafaye/api@sha256:" + strings.Repeat("a", 64), "--host", "nope.cafaye.test"},
			want: "does not route", code: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runBoxCLI(t, deps, tc.args...)
			if code != tc.code {
				t.Fatalf("exit %d, want %d: %s", code, tc.code, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("stderr is %q, and it should name %q", stderr, tc.want)
			}
		})
	}
}

// TestBoxLogsReleaseFollowsOneRelease is the third deliverable: the log of
// ONE release, named, on the box, readable with the verb rather than a grep.
func TestBoxLogsReleaseFollowsOneRelease(t *testing.T) {
	deps, box := newDeployEnv(t)
	box.writeEdge(t, fmt.Sprintf(goodEdge, expiry, expiry))
	code, stdout, stderr := runBoxCLI(t, deps, "deploy",
		"--build", "ghcr.io/cafaye/api@sha256:"+strings.Repeat("a", 64),
		"--host", "api.cafaye.test")
	if code != 0 {
		t.Fatalf("deploy: %d %s", code, stderr)
	}
	id := deployDoc(t, stdout).Release.ID
	store, err := stack.Open(filepath.Join(deps.stack.root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.LogPath(id), []byte("serving on api.cafaye.test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, stderr := runBoxCLI(t, deps, "logs", "--release", id)
	if code != 0 {
		t.Fatalf("cafe box logs --release exited %d: %s", code, stderr)
	}
	if !strings.Contains(out, "serving on api.cafaye.test") {
		t.Fatalf("stdout is %q, want the release's own log", out)
	}
	// An unknown release is a usage error that names what is on the box, not
	// a crash.
	code, _, stderr = runBoxCLI(t, deps, "logs", "--release", "19990101T000000Z-000000000000")
	if code != 2 {
		t.Fatalf("an unknown release exited %d, want 2: %s", code, stderr)
	}
	if !strings.Contains(stderr, "19990101T000000Z-000000000000") {
		t.Fatalf("the refusal does not name the release it was given: %s", stderr)
	}
}

// TestBoxDeployUsageNamesEveryVerb is the guard over the parent's usage line:
// the verbs exist in the dispatcher, and the help a person reads lists them.
func TestBoxDeployUsageNamesEveryVerb(t *testing.T) {
	deps, _ := newDeployEnv(t)
	_, stdout, _ := runBoxCLI(t, deps)
	for _, verb := range []string{"deploy", "rollback"} {
		if !strings.Contains(stdout, verb) {
			t.Fatalf("`cafe box` help does not name %q:\n%s", verb, stdout)
		}
	}
	var out bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &bytes.Buffer{}, Version: testVersion, Context: context.Background()}
	if err := newBoxCommand(deps).Execute(env, []string{"deploy", "--nope"}); err == nil {
		t.Fatal("an unknown flag on deploy was accepted")
	}
}
