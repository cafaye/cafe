package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cafaye/cafe/internal/edge"
)

// aBoxEdgeState is a box state directory with one release published in it, the
// shape `cafe box publish` leaves behind. It is on a real filesystem because
// the verb's whole claim is about a real one: a rollback is a rename over a
// symlink, and a stub of that would be a stub of the claim.
type aBoxEdgeState struct {
	dir   string
	spec  edge.Spec
	files map[string][]byte
}

func newBoxEdgeState(t *testing.T) *aBoxEdgeState {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "boxd")
	if err := edge.Save(filepath.Join(dir, "edge"), edge.New("box-01", edge.ModeACME)); err != nil {
		t.Fatalf("seed the edge record: %v", err)
	}
	return &aBoxEdgeState{
		dir:   dir,
		spec:  edge.New("box-01", edge.ModeACME),
		files: map[string][]byte{"README": []byte("release\n")},
	}
}

// src is a directory the publish verb reads, written the way a build leaves one.
func (b *aBoxEdgeState) src(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "build")
	for name, body := range b.files {
		path := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return src
}

func (b *aBoxEdgeState) publish(t *testing.T, id string) {
	t.Helper()
	src := b.src(t)
	if code, _, stderr := runBoxVerb(t, b.dir, "publish", id, src); code != exitSuccess {
		t.Fatalf("publish %s exited %d (stderr: %s)", id, code, stderr)
	}
	b.spec.Previous = b.spec.Current
	b.spec.Current = id
}

// runBoxVerb runs one `cafe box` verb against an injected state directory, so
// the test never reads /var/lib or the real config directory — and so the
// three verbs are driven exactly as a box runs them, with no seam in between.
func runBoxVerb(t *testing.T, stateDir, verb string, args ...string) (int, string, string) {
	t.Helper()
	deps := boxDeps{dir: filepath.Join(stateDir, "config"), stateDir: stateDir}
	c, found := lookupBoxVerb(boxEdgeVerbs(deps), verb)
	if !found {
		t.Fatalf("cafe box has no %q verb", verb)
	}
	env := newTestEnv()
	err := c.cmd.Execute(env, args)
	code := exitSuccess
	if err != nil {
		code = exitFailure
	}
	return code, written(t, env.Stdout), written(t, env.Stderr)
}

// `cafe box publish` writes the release, flips the link and records it — in
// that order, which is the zero-downtime claim's order. The verb is proven
// against the real filesystem: after it, the `current` symlink names the
// release and the file the deploy wrote is reachable through it.
func TestBoxPublishWritesTheReleaseAndFlipsTheLink(t *testing.T) {
	b := newBoxEdgeState(t)

	code, stdout, _ := runBoxVerb(t, b.dir, "publish", "sample-stack-1", b.src(t))
	if code != exitSuccess {
		t.Fatalf("cafe box publish exited %d", code)
	}

	// The document says what is serving, and the symlink agrees with it.
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("cafe box publish's answer is not JSON: %v (%s)", err, stdout)
	}
	if doc["current"] != "sample-stack-1" {
		t.Errorf("the record says current = %v, want sample-stack-1", doc["current"])
	}
	if doc["link"] != "sample-stack-1" {
		t.Errorf("the link says current = %v, want sample-stack-1", doc["link"])
	}
	// The release is really on disk, and the file the deploy wrote is really
	// in it.
	body, err := os.ReadFile(filepath.Join(b.dir, "edge", "releases", "sample-stack-1", "README"))
	if err != nil {
		t.Fatalf("read the published release: %v", err)
	}
	if string(body) != "release\n" {
		t.Errorf("the published file is %q, want what was deployed", string(body))
	}
	if got, _ := edge.Current(filepath.Join(b.dir, "edge")); got != "sample-stack-1" {
		t.Errorf("current = %q, want sample-stack-1", got)
	}
}

// A release id that is already deployed is refused rather than overwritten:
// two deploys of one id are either the same deploy twice or two different
// things with the same name, and neither is safe to merge into one directory.
func TestBoxPublishRefusesAnIdThatIsAlreadyDeployed(t *testing.T) {
	b := newBoxEdgeState(t)
	src := b.src(t)

	if code, _, _ := runBoxVerb(t, b.dir, "publish", "sample-stack-1", src); code != exitSuccess {
		t.Fatal("the first publish failed")
	}
	code, stdout, stderr := runBoxVerb(t, b.dir, "publish", "sample-stack-1", src)
	if code == exitSuccess {
		t.Fatalf("a second publish of the same id succeeded (stdout: %s)", stdout)
	}
	if !strings.Contains(stderr, "already deployed") {
		t.Errorf("the refusal does not say what is in the way: %s", stderr)
	}
	// And the release that was there is untouched.
	body, _ := os.ReadFile(filepath.Join(b.dir, "edge", "releases", "sample-stack-1", "README"))
	if string(body) != "release\n" {
		t.Errorf("the refused publish modified the release that was already there: %q", string(body))
	}
}

// `cafe box rollback` flips the link back and needs nothing but the disk. This
// is the plan's "a broken deploy demonstrates rollback" checklist item proved
// through the real command rather than through the package underneath it.
func TestBoxRollbackFlipsTheLinkBackAndNeedsNoControlPlane(t *testing.T) {
	b := newBoxEdgeState(t)
	b.publish(t, "sample-stack-1")
	b.publish(t, "sample-stack-2")
	b.publish(t, "sample-stack-3")

	code, stdout, _ := runBoxVerb(t, b.dir, "rollback")
	if code != exitSuccess {
		t.Fatalf("cafe box rollback exited %d", code)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("the rollback's answer is not JSON: %v (%s)", err, stdout)
	}
	if doc["current"] != "sample-stack-2" {
		t.Fatalf("the rollback recorded current = %v, want sample-stack-2", doc["current"])
	}
	if doc["link"] != "sample-stack-2" {
		t.Errorf("the rollback flipped the link to %v, want sample-stack-2", doc["link"])
	}
	// The release that was serving before it is still on disk and is now the
	// one serving — a rollback did not delete anything on its way past.
	if _, err := os.Stat(filepath.Join(b.dir, "edge", "releases", "sample-stack-2")); err != nil {
		t.Errorf("the release rolled back to is not on disk: %v", err)
	}
}

// A rollback with nothing to go back to refuses and names what it is refusing
// to do. A command that guessed at the second-newest directory would sometimes
// serve a release nobody deployed last, which is the failure this verb exists
// to avoid rather than to commit.
func TestBoxRollbackWithNothingToReturnToRefuses(t *testing.T) {
	b := newBoxEdgeState(t)
	b.publish(t, "sample-stack-1")

	code, stdout, stderr := runBoxVerb(t, b.dir, "rollback")
	if code == exitSuccess {
		t.Fatalf("the rollback succeeded with nothing to go back to (stdout: %s)", stdout)
	}
	if !strings.Contains(stderr, "no earlier release") {
		t.Errorf("the refusal does not name what is missing: %s", stderr)
	}
	if got, _ := edge.Current(filepath.Join(b.dir, "edge")); got != "sample-stack-1" {
		t.Errorf("the refused rollback moved current to %q", got)
	}
}

// `cafe box edge` reports what the box serves, and it reports the one
// disagreement worth seeing: a `current` link that does not match the record
// is a deploy that did not finish, and a verb that resolved it silently in
// either direction would be a verb that hid it.
func TestBoxEdgeReportsTheRecordAndNamesAMismatch(t *testing.T) {
	b := newBoxEdgeState(t)
	b.publish(t, "sample-stack-1")

	code, stdout, _ := runBoxVerb(t, b.dir, "edge")
	if code != exitSuccess {
		t.Fatalf("cafe box edge exited %d", code)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("cafe box edge's answer is not JSON: %v (%s)", err, stdout)
	}
	if doc["box"] != "box-01" || doc["mode"] != string(edge.ModeACME) {
		t.Errorf("the answer does not carry the box and its TLS mode: %s", stdout)
	}

	// Move the link without moving the record — a deploy that died between
	// the two writes — and the verb says so on stderr.
	if err := edge.Switch(filepath.Join(b.dir, "edge"), "sample-stack-1"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	b.publish(t, "sample-stack-2")
	if err := edge.Switch(filepath.Join(b.dir, "edge"), "sample-stack-1"); err != nil {
		t.Fatalf("switch back: %v", err)
	}
	code, _, stderr := runBoxVerb(t, b.dir, "edge")
	if code != exitSuccess {
		t.Fatalf("cafe box edge exited %d with a mismatched link", code)
	}
	if !strings.Contains(stderr, "did not finish") {
		t.Errorf("a half-finished deploy was not reported: %s", stderr)
	}
}
