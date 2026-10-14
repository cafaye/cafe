package stack

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeBox is the seam, and it counts. Every claim this packet makes about
// what a deploy does is an assertion about one of those counts: a rollback
// that pulled a container would show up as a serve, and a deploy that staged
// a release for a host the edge does not route would show up as a release
// directory on disk.
type fakeBox struct {
	// dirs holds one stack per service, for a box that runs several. The box
	// installs the secret set INTO the release directory of the service the
	// release belongs to, which is the whole point: the value lives where the
	// release reads it and nowhere else.
	dirs        map[string]*Store
	hosts       map[string]Host
	served      int
	ready       int
	failReady   string
	failServe   string
	secretValue string
	// content is what each release serves, keyed by the DIGEST of its image,
	// because Serve writes the bytes while the deploy runs and a test can only
	// know the release id afterwards. It is how a test makes a "service
	// change": the second release serves different bytes, and the edge answers
	// with whatever `current` resolves to.
	content map[string]string
}

func (f *fakeBox) Host(name string) (Host, bool) {
	h, ok := f.hosts[name]
	return h, ok
}

func (f *fakeBox) Serve(_ context.Context, rel Release) error {
	f.served++
	if f.failServe != "" && f.failServe == digestOf(rel.Image) {
		return errors.New("the container would not start")
	}
	dir := f.dirs[rel.Service].releaseDir(rel.ID)
	if err := os.WriteFile(filepath.Join(dir, "env"), []byte(f.secretValue), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "index.html"), []byte(f.content[digestOf(rel.Image)]), 0o644)
}

// digestOf is the digest half of a pinned reference, which is what a build is
// identified by — so the content a release serves is keyed by the artifact and
// not by the release, which a test only learns after the deploy has run.
func digestOf(image string) string {
	if at := strings.Index(image, "@"); at >= 0 {
		return image[at+1:]
	}
	return image
}

func (f *fakeBox) Ready(_ context.Context, rel Release) error {
	f.ready++
	// The gate fails on the ARTIFACT, not on the release id: a release id
	// carries the clock, and a test that computed the expected id would be
	// asserting a fact about the second the deploy happened to run in.
	if f.failReady != "" && f.failReady == digestOf(rel.Image) {
		return errors.New("readiness probe timed out")
	}
	return nil
}

// newBox is the seam over one stack. The store is passed in rather than a
// directory because the box installs the secret set into the release
// directory, and a fake that wrote it anywhere else would not be testing the
// thing that matters.
// routed is the edge's answer for a host with a certificate that is valid.
func routed(name string) Host {
	return Host{Name: name, Routed: true, CertSubject: name, CertNotAfter: time.Now().Add(90 * 24 * time.Hour)}
}

// newBox is a box serving SEVERAL stacks — the real shape, where one box runs
// gateway and identity and pantry and each has its own releases. The stack a
// release is staged into is chosen by its service, so a test can deploy two
// services through one seam.
func newBox(t *testing.T, byService map[string]*Store, hosts ...string) *fakeBox {
	t.Helper()
	b := &fakeBox{dirs: byService, hosts: map[string]Host{}, secretValue: "s3cret-value-never-recorded"}
	for _, h := range hosts {
		b.hosts[h] = routed(h)
	}
	return b
}

var (
	digestA = "sha256:" + strings.Repeat("a", 64)
	digestB = "sha256:" + strings.Repeat("b", 64)
	digestC = "sha256:" + strings.Repeat("c", 64)
)

func openStack(t *testing.T) *Store {
	t.Helper()
	return openStackIn(t, t.TempDir(), "api")
}

// openStackIn is one service's stack under a shared box root, which is the
// shape a real box has: several services, each with its own pointer, and none
// of them able to move another's traffic.
func openStackIn(t *testing.T, root, service string) *Store {
	t.Helper()
	s, err := Open(ServiceDir(root, service))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func request(host string, digest string) Request {
	return Request{
		Service: "api",
		Host:    host,
		Build: Build{
			Registry: "ghcr.io", Name: "cafaye/api", Tag: "1.4.0",
			Digest: digest,
		},
		Secrets: "api/production",
		Now:     time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC),
	}
}

// TestADeployStagesAServesBAndSwitchesC is the whole happy path, and it is
// the first item on the Phase B sign-off list: a service change reaches the
// box and is served on the host name it answers on.
func TestADeployStagesAServesBAndSwitchesC(t *testing.T) {
	s := openStack(t)
	b := newBox(t, map[string]*Store{"api": s}, "api.cafaye.test")

	if _, err := Deploy(context.Background(), s, b, request("api.cafaye.test", digestA)); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	if b.served != 1 {
		t.Fatalf("the box served %d releases, want 1", b.served)
	}
	cur, err := s.Current()
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	// The pointer is a FILE naming the release, and the release directory it
	// names is really there.
	tgt, err := s.SwitchTarget()
	if err != nil {
		t.Fatalf("read the pointer: %v", err)
	}
	if tgt != cur.ID {
		t.Fatalf("the pointer names %q and the current release is %q", tgt, cur.ID)
	}
	if _, err := os.Stat(s.ReleasePath(tgt)); err != nil {
		t.Fatalf("the pointer does not resolve to a release on disk: %v", err)
	}
	if cur.Outcome != Live {
		t.Fatalf("the current release is %q, want %q", cur.Outcome, Live)
	}
	if b.ready != 1 {
		t.Fatalf("the gate ran %d times, want 1", b.ready)
	}
	h, err := s.History()
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if h.LastGood != cur.ID {
		t.Fatalf("last good is %q and current is %q, and they should agree", h.LastGood, cur.ID)
	}
}

// TestAReleaseThatFailsItsGateFlipsBack is the gate's `release history` item:
// a second deploy that exits non-zero puts the first release back, on
// purpose, and the history says so afterwards.
func TestAReleaseThatFailsItsGateFlipsBack(t *testing.T) {
	s := openStack(t)
	b := newBox(t, map[string]*Store{"api": s}, "api.cafaye.test")

	first, err := Deploy(context.Background(), s, b, request("api.cafaye.test", digestA))
	if err != nil {
		t.Fatalf("first deploy: %v", err)
	}

	req := request("api.cafaye.test", digestB)
	req.Now = req.Now.Add(time.Minute)
	b.failReady = digestB

	rep, err := Deploy(context.Background(), s, b, req)
	if err == nil {
		t.Fatal("a release that failed its health gate returned no error, so a script would have believed the deploy worked")
	}
	if !errors.Is(err, ErrGateFailed) {
		t.Fatalf("the error is %v, and it should be ErrGateFailed so the CLI exits non-zero on purpose", err)
	}
	if !rep.RolledBack {
		t.Fatal("the report does not say the box rolled back")
	}
	// The box is serving the FIRST release again, not the one that failed.
	if rep.Release.ID != first.Release.ID {
		t.Fatalf("the box is serving %q, want the first release %q", rep.Release.ID, first.Release.ID)
	}
	if rep.Failed == nil || rep.Failed.Outcome != Failed {
		t.Fatalf("the failed release is not recorded as failed: %+v", rep.Failed)
	}
	cur, err := s.Current()
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if cur.ID != first.Release.ID {
		t.Fatalf("the pointer names %q, want %q", cur.ID, first.Release.ID)
	}
	// The history names the failed release AND keeps it on disk, because the
	// operator's first question is "what broke".
	h, err := s.History()
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if h.LastGood != first.Release.ID {
		t.Fatalf("last good is %q, want %q — a rollback after the failed deploy must still go back to the good one", h.LastGood, first.Release.ID)
	}
	var failed, good int
	for _, r := range h.Releases {
		switch r.Outcome {
		case Failed:
			failed++
			if _, err := os.Stat(s.ReleasePath(r.ID)); err != nil {
				t.Fatalf("the failed release is not on disk: %v", err)
			}
		case Live:
			good++
		default:
			// Other outcomes are not on disk in this history.
		}
	}
	if failed != 1 || good != 1 {
		t.Fatalf("the history holds %d failed and %d live releases, want 1 of each", failed, good)
	}
	// And the history file is readable by a person on the box, which is the
	// product: `cat history.json` is the release history the gate names.
	raw, err := os.ReadFile(filepath.Join(s.Root, historyFile))
	if err != nil {
		t.Fatalf("read the history: %v", err)
	}
	if !strings.Contains(string(raw), first.Release.ID) || !strings.Contains(string(raw), rep.Failed.ID) {
		t.Fatalf("the history does not name both releases:\n%s", raw)
	}
}

// TestRollbackFlipsThePointerAndNothingElse is the claim that rollback is
// instant: it pulls nothing, starts nothing and rebuilds nothing. The serve
// count is the whole proof.
func TestRollbackFlipsThePointerAndNothingElse(t *testing.T) {
	s := openStack(t)
	b := newBox(t, map[string]*Store{"api": s}, "api.cafaye.test")

	one, err := Deploy(context.Background(), s, b, request("api.cafaye.test", digestA))
	if err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	two := request("api.cafaye.test", digestB)
	two.Now = two.Now.Add(time.Minute)
	second, err := Deploy(context.Background(), s, b, two)
	if err != nil {
		t.Fatalf("second deploy: %v", err)
	}
	if b.served != 2 {
		t.Fatalf("the box served %d releases, want 2", b.served)
	}

	before, err := s.SwitchTarget()
	if err != nil {
		t.Fatalf("switch target: %v", err)
	}
	back, rep, err := s.Rollback("", time.Now())
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if b.served != 2 {
		t.Fatalf("the rollback served %d releases, want 2 — a rollback that starts a container is not instant", b.served)
	}
	if back.ID != one.Release.ID {
		t.Fatalf("rollback went back to %q, want %q", back.ID, one.Release.ID)
	}
	if after, _ := s.SwitchTarget(); after == before {
		t.Fatalf("the pointer still names %q after the rollback", after)
	}
	if !rep.RolledBack {
		t.Fatal("the report does not say it was a rollback")
	}
	h, err := s.History()
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	// LastGood is "the last release that passed a gate", which after two good
	// deploys is the SECOND one: it is the pointer a failed deploy flips back
	// to, not the one an operator's undo goes to. Two pointers, two questions.
	if h.LastGood != second.Release.ID {
		t.Fatalf("last good is %q, want %q", h.LastGood, second.Release.ID)
	}
}

// TestARollbackToANamedReleaseGoesBackTwo is the case the last-good pointer
// cannot express: going back two releases, by id.
func TestARollbackToANamedReleaseGoesBackTwo(t *testing.T) {
	s := openStack(t)
	b := newBox(t, map[string]*Store{"api": s}, "api.cafaye.test")

	one := request("api.cafaye.test", digestA)
	if _, err := Deploy(context.Background(), s, b, one); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	first, err := s.Current()
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	for _, d := range []string{digestB, digestC} {
		req := request("api.cafaye.test", d)
		req.Now = req.Now.Add(time.Minute)
		if _, err := Deploy(context.Background(), s, b, req); err != nil {
			t.Fatalf("deploy: %v", err)
		}
	}
	back, _, err := s.Rollback(first.ID, time.Now())
	if err != nil {
		t.Fatalf("rollback to a named release: %v", err)
	}
	if back.ID != first.ID {
		t.Fatalf("rollback went to %q, want %q", back.ID, first.ID)
	}
}

// TestARefusalLeavesNothingStaged is the ordering claim: the edge check runs
// before the stage, so a deploy pointed at a host this box does not route
// costs nothing and says which host.
func TestARefusalLeavesNothingStaged(t *testing.T) {
	s := openStack(t)
	b := newBox(t, map[string]*Store{"api": s}, "api.cafaye.test")

	for name, tc := range map[string]struct {
		req  Request
		want string
	}{
		"a host the edge does not route": {request("auth.cafaye.test", digestA), ErrNoRoute.Error()},
		"a build that is not pinned": {
			request("api.cafaye.test", "latest"),
			"not pinned to a digest",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Deploy(context.Background(), s, b, tc.req)
			if err == nil {
				t.Fatal("the deploy succeeded, and it should have been refused")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the refusal is %q, and it should name %q", err.Error(), tc.want)
			}
			if b.served != 0 {
				t.Fatalf("the box served %d releases on a refused deploy, want 0", b.served)
			}
			entries, err := os.ReadDir(filepath.Join(s.Root, releasesDir))
			if err != nil && !os.IsNotExist(err) {
				t.Fatalf("read the releases directory: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("a refused deploy staged %d releases, want none", len(entries))
			}
			if cur, err := s.Current(); err != nil || cur.ID != "" {
				t.Fatalf("a refused deploy left a current release: %+v %v", cur, err)
			}
		})
	}
}

// TestAnExpiredCertificateIsARefusal is the second edge check. A host routed
// by the edge but holding an expired certificate is a deploy that would prove
// rollback by breaking the site a second time.
func TestAnExpiredCertificateIsARefusal(t *testing.T) {
	s := openStack(t)
	b := newBox(t, map[string]*Store{"api": s}, "api.cafaye.test")
	// The clock the deploy runs against is the one its request carries, so an
	// expiry in the FUTURE of the wall clock can still be in the past of the
	// deploy — and a test that forgot that would prove nothing.
	past := time.Date(2026, 10, 11, 11, 0, 0, 0, time.UTC)
	b.hosts["api.cafaye.test"] = Host{Name: "api.cafaye.test", Routed: true, CertNotAfter: past}

	_, err := Deploy(context.Background(), s, b, request("api.cafaye.test", digestA))
	if !errors.Is(err, ErrNoCert) {
		t.Fatalf("the error is %v, want ErrNoCert", err)
	}
	if b.served != 0 {
		t.Fatalf("the box served %d releases, want 0", b.served)
	}
}

// TestAReleaseIsNeverOverwrittenInPlace is the rule the whole design rests
// on. Staging the same id twice is refused by name, and the first release's
// record is byte for byte what it was.
func TestAReleaseIsNeverOverwrittenInPlace(t *testing.T) {
	s := openStack(t)
	rel := Release{
		ID: "20261011T120000Z-" + shortDigest(digestA), Service: "api",
		Image: "ghcr.io/cafaye/api@" + digestA, Host: "api.cafaye.test",
		StartedAt: time.Now().UTC(), Outcome: Staged,
	}
	if _, err := s.Stage(rel); err != nil {
		t.Fatalf("stage: %v", err)
	}
	clobber := rel
	clobber.Image = "ghcr.io/cafaye/api@" + digestB
	if _, err := s.Stage(clobber); !errors.Is(err, ErrExists) {
		t.Fatalf("the second stage returned %v, want ErrExists", err)
	}
	raw, err := os.ReadFile(s.ReleasePath(rel.ID))
	if err != nil {
		t.Fatalf("read the record: %v", err)
	}
	if !strings.Contains(string(raw), digestA) {
		t.Fatalf("the record was overwritten:\n%s", raw)
	}
	if strings.Contains(string(raw), digestB) {
		t.Fatalf("the record now names the second digest:\n%s", raw)
	}
}

// TestKeepNReleasesAndNeverThePointer is the retention rule: five releases
// stay, the current one and the last-good one are never the ones dropped.
func TestKeepNReleasesAndNeverThePointer(t *testing.T) {
	s := openStack(t)
	s.Keep = 3
	b := newBox(t, map[string]*Store{"api": s}, "api.cafaye.test")

	first, err := Deploy(context.Background(), s, b, request("api.cafaye.test", digestA))
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	for _, d := range []string{digestB, digestC} {
		req := request("api.cafaye.test", d)
		req.Now = req.Now.Add(time.Minute)
		if _, err := Deploy(context.Background(), s, b, req); err != nil {
			t.Fatalf("deploy: %v", err)
		}
	}
	h, err := s.History()
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(h.Releases) > 3 {
		names := make([]string, 0, len(h.Releases))
		for _, r := range h.Releases {
			names = append(names, r.ID)
		}
		t.Fatalf("the history holds %d releases (%s), want at most 3", len(h.Releases), strings.Join(names, ", "))
	}
	for _, protected := range []string{h.Current, h.LastGood} {
		if _, err := os.Stat(s.ReleasePath(protected)); err != nil {
			t.Fatalf("release %s was pruned while the pointer still needed it: %v", protected, err)
		}
	}
	// With three releases kept and three deployed, the first is the one
	// pruning should have taken — and it must NOT be the last-good one.
	if h.LastGood == first.Release.ID {
		t.Skip("nothing was pruned with three releases and keep=3, so this proves nothing")
	}
	if _, err := os.Stat(s.ReleasePath(h.LastGood)); err != nil {
		t.Fatalf("the last-good release %s was pruned: %v", h.LastGood, err)
	}
	if _, _, err := s.Rollback("", time.Now()); err != nil {
		t.Fatalf("after pruning, rollback: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(s.Root, releasesDir))
	if err != nil {
		t.Fatalf("read the releases directory: %v", err)
	}
	if len(entries) != len(h.Releases) {
		t.Fatalf("the disk holds %d release directories and the history %d releases", len(entries), len(h.Releases))
	}
}

// TestTheReleaseRecordCarriesNoSecretValue is invariant 3 in this package:
// the box installs the secret set INTO the release at deploy time — that is
// the whole "secrets at deploy, pushed to the box, never baked into the
// image" requirement — and the RECORDS the stack writes above it carry the
// set's name and nothing else.
//
// The control half matters as much as the assertion: the secret must actually
// be on disk in the release directory, or a stack that never installed
// anything would pass by leaking nothing at all.
func TestTheReleaseRecordCarriesNoSecretValue(t *testing.T) {
	s := openStack(t)
	b := newBox(t, map[string]*Store{"api": s}, "api.cafaye.test")

	rep, err := Deploy(context.Background(), s, b, request("api.cafaye.test", digestA))
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if rep.Release.Secrets != "api/production" {
		t.Fatalf("the release record names secret set %q, want %q", rep.Release.Secrets, "api/production")
	}
	written, err := os.ReadFile(filepath.Join(s.releaseDir(rep.Release.ID), "env"))
	if err != nil {
		t.Fatalf("the box did not install the secret set into the release: %v", err)
	}
	if string(written) != b.secretValue {
		t.Fatalf("the release holds %q, want the secret value the box installed", written)
	}

	// Every file the STACK wrote: the per-release records, the history, and
	// the rendered report.
	records := []string{s.ReleasePath(rep.Release.ID), filepath.Join(s.Root, historyFile)}
	for _, f := range records {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.Contains(string(raw), b.secretValue) {
			t.Fatalf("%s carries the secret value the box installed:\n%s", f, raw)
		}
		// ...and it DOES name the set, so the check above is not passing on a
		// record that says nothing at all.
		if !strings.Contains(string(raw), "api/production") {
			t.Fatalf("%s does not name the secret set, so the check above proves nothing:\n%s", f, raw)
		}
	}
	if out, err := json.Marshal(rep); err != nil {
		t.Fatalf("render the report: %v", err)
	} else if strings.Contains(string(out), b.secretValue) {
		t.Fatalf("the report carries the secret value:\n%s", out)
	}
}

// TestAFailedFirstDeployServesNothing is the edge case the rollback has to
// answer honestly: there is no previous release, so the box serves nothing
// rather than serving the release that just failed.
func TestAFailedFirstDeployServesNothing(t *testing.T) {
	s := openStack(t)
	b := newBox(t, map[string]*Store{"api": s}, "api.cafaye.test")
	b.failReady = digestA

	rep, err := Deploy(context.Background(), s, b, request("api.cafaye.test", digestA))
	if !errors.Is(err, ErrGateFailed) {
		t.Fatalf("the error is %v, want ErrGateFailed", err)
	}
	if rep.Outcome != OutcomeNoCurrent {
		t.Fatalf("the report says %q, want %q", rep.Outcome, OutcomeNoCurrent)
	}
	if tgt, err := s.SwitchTarget(); err != nil || tgt != "" {
		t.Fatalf("the pointer names %q after a failed first deploy, want none: %v", tgt, err)
	}
	if _, _, err := s.Rollback("", time.Now()); err == nil {
		t.Fatal("a rollback with no last-good release succeeded, and it should say so")
	}
}

// TestAFailedServeIsRecordedAndNothingServes covers the release that could
// not even be brought up: never in front of traffic, still on the record.
func TestAFailedServeIsRecordedAndNothingServes(t *testing.T) {
	s := openStack(t)
	b := newBox(t, map[string]*Store{"api": s}, "api.cafaye.test")
	b.failServe = digestA

	_, err := Deploy(context.Background(), s, b, request("api.cafaye.test", digestA))
	if !errors.Is(err, ErrGateFailed) {
		t.Fatalf("the error is %v, want ErrGateFailed", err)
	}
	h, err := s.History()
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(h.Releases) != 1 || h.Releases[0].Outcome != Failed {
		t.Fatalf("the history holds %+v, want one failed release", h.Releases)
	}
	if tgt, err := s.SwitchTarget(); err != nil || tgt != "" {
		t.Fatalf("the pointer names %q, want none: %v", tgt, err)
	}
}

// TestThePointerAndTheHistoryAgree is the self-check over the one state the
// stack owns. A history naming one release while the symlink names another is
// the disagreement that would serve traffic nobody asked for.
func TestThePointerAndTheHistoryAgree(t *testing.T) {
	s := openStack(t)
	b := newBox(t, map[string]*Store{"api": s}, "api.cafaye.test")
	for _, d := range []string{digestA, digestB} {
		req := request("api.cafaye.test", d)
		req.Now = req.Now.Add(time.Minute)
		if _, err := Deploy(context.Background(), s, b, req); err != nil {
			t.Fatalf("deploy: %v", err)
		}
		tgt, err := s.SwitchTarget()
		if err != nil {
			t.Fatalf("switch target: %v", err)
		}
		h, err := s.History()
		if err != nil {
			t.Fatalf("history: %v", err)
		}
		if h.Current != tgt {
			t.Fatalf("the history says %q and the pointer says %q", h.Current, tgt)
		}
	}
}

// TestTheHistoryIsAWholeDocumentIs the shape claim: the file a person reads
// with `cat` has a version, a current, a last_good and the releases.
func TestTheHistoryIsAWholeDocument(t *testing.T) {
	s := openStack(t)
	b := newBox(t, map[string]*Store{"api": s}, "api.cafaye.test")
	if _, err := Deploy(context.Background(), s, b, request("api.cafaye.test", digestA)); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	var doc map[string]any
	raw, err := os.ReadFile(filepath.Join(s.Root, historyFile))
	if err != nil {
		t.Fatalf("read the history: %v", err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the history is not JSON: %v\n%s", err, raw)
	}
	for _, key := range []string{"version", "current", "last_good", "releases"} {
		if _, ok := doc[key]; !ok {
			t.Fatalf("the history has no %q key:\n%s", key, raw)
		}
	}
}

// TestLogPathNamesOneReleasesLog is the `logs --release` half: each release
// has its own log file, named by the release, so following one release is a
// path and not a grep.
func TestLogPathNamesOneReleasesLog(t *testing.T) {
	s := openStack(t)
	one := filepath.Join(s.Root, logsDir, "20261011T120000Z-aaaaaaaaaaaa.log")
	if s.LogPath("20261011T120000Z-aaaaaaaaaaaa") != one {
		t.Fatalf("the log path is %q, want %q", s.LogPath("20261011T120000Z-aaaaaaaaaaaa"), one)
	}
	if err := os.WriteFile(one, []byte("serving\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(s.LogPath("20261011T120000Z-aaaaaaaaaaaa"))
	if err != nil {
		t.Fatalf("read the release's log: %v", err)
	}
	if string(raw) != "serving\n" {
		t.Fatalf("the log is %q, want the one release's own output", raw)
	}
}

// TestTheKeepCountSurvivesAReopen is the detail that keeps a box from
// silently changing its retention: the keep count is written into the
// history, so a second `cafe box deploy` on the same box keeps N.
func TestTheKeepCountSurvivesAReopen(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s.Keep = 7
	b := newBox(t, map[string]*Store{"api": s}, "api.cafaye.test")
	if _, err := Deploy(context.Background(), s, b, request("api.cafaye.test", digestA)); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	again, err := Open(root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if again.Keep != 7 {
		t.Fatalf("the reopened stack keeps %d releases, want 7", again.Keep)
	}
}

// TestAListOfServiceNamesIsAPointerToThem is the guard over NewID: an id that
// does not sort in deploy order would make the history lie about which
// release came first.
func TestNewIDSortsInDeployOrder(t *testing.T) {
	base := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	one, err := NewID(base, digestA)
	if err != nil {
		t.Fatalf("id: %v", err)
	}
	two, err := NewID(base.Add(time.Second), digestB)
	if err != nil {
		t.Fatalf("id: %v", err)
	}
	if one >= two {
		t.Fatalf("%q is not before %q, so the history's order is not deploy order", one, two)
	}
	if _, err := NewID(base, "latest"); err == nil {
		t.Fatal("an id was made for an unpinned build, and rollback to it would be a guess")
	}
}
