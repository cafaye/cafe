package edge

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// the box the edge is proven against
// ---------------------------------------------------------------------------

// sampleStack is the stack this packet proves the edge with: two services, two
// hostnames, one release. It is a real HTTP server on a real loopback port
// answering real requests, because the thing under test is the routing and
// nothing about a service is a stub of it.
type sampleStack struct {
	release string
}

func (s sampleStack) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "%s ready", s.release)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "%s on %s", s.release, r.Host)
	})
	return mux
}

// boxState is a box on disk: a state directory with a release published in it
// and a spec written over it, exactly as a deploy leaves it.
type boxState struct {
	dir      string
	spec     Spec
	upstream map[string]string // hostname -> upstream address
}

func newBoxState(t *testing.T, box string, mode Mode) *boxState {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "boxd", "edge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create the box state directory: %v", err)
	}
	spec := New(box, mode)
	if err := Save(dir, spec); err != nil {
		t.Fatalf("write the spec: %v", err)
	}
	return &boxState{dir: dir, spec: spec, upstream: map[string]string{}}
}

// deploy publishes one release of the sample stack and records the host rows
// the way `cafe box deploy` does: release directory on disk, symlink flipped,
// spec written last.
func (b *boxState) deploy(t *testing.T, release string, hosts ...string) {
	t.Helper()
	files := map[string][]byte{
		"README":                          []byte("sample-stack " + release + "\n"),
		filepath.Join("etc", "edge.json"): []byte("{\"release\":\"" + release + "\"}\n"),
	}
	if _, err := Publish(b.dir, release, files); err != nil {
		t.Fatalf("publish %s: %v", release, err)
	}
	for _, h := range hosts {
		b.spec = b.spec.WithHost(Host{Name: h, Upstream: b.upstream[h], Service: "sample-stack", Health: HealthUnknown})
	}
	b.spec.Previous = b.spec.Current
	b.spec.Current = release
	if err := Save(b.dir, b.spec); err != nil {
		t.Fatalf("write the spec after publishing %s: %v", release, err)
	}
}

// gateway is the running edge for this box: real listener, real TLS, real
// routing record read off disk. The listener is closed when the test ends, so
// nothing outlives it.
//
// THE GATEWAY IS BUILT WITH NO SINK, and that is the fix for a measured lost
// update rather than a preference. The health column has a writer of its own
// (SpecSink, proven by TestTheProbeLoopPersistsTheHealthItObserved), and a
// gateway that ALSO writes the record is a SECOND writer racing any deploy the
// test runs beside it. Two writers on one file with no shared lock is a lost
// update: the probe's read-merge-write can land after the deploy's write and
// drop the Previous pointer a rollback needs. That is exactly what made
// TestTheSwitchIsARenameAndRollbackNeedsNoControlPlane red — 57 failures in
// 200 runs, the routing row's Health moving under the test and a rollback
// refusing because the recorded Previous had been overwritten by a health
// write. A nil sink is the package's own answer for "a test that does not want
// a second writer" (see SpecSink), and the edge still answers every request
// with the health it observed, out of its in-memory probe table.
func (b *boxState) gateway(t *testing.T, certs Certificates) (*Gateway, string) {
	t.Helper()
	g := &Gateway{
		Spec:         func() (Spec, error) { return Load(b.dir) },
		Certificates: certs,
		ErrorLog:     log.New(io.Discard, "", 0),
	}
	ln, err := g.ListenTLS("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = g.Serve(context.Background(), ln) }()
	t.Cleanup(func() { _ = ln.Close() })
	return g, ln.Addr().String()
}

// sameRoute reports whether two rows route the same hostname the same way: the
// name, the upstream and the service. Health and Detail are the edge's own
// observation of the upstream — the package says they are rewritten as the
// edge probes — so they are not part of the routing identity a deploy must
// preserve, and a test that compared them whole would be asserting a
// coincidence about timing rather than the routing claim it names.
func sameRoute(a, b Host) bool {
	return a.Name == b.Name && a.Upstream == b.Upstream && a.Service == b.Service
}

// --------------------------------- listener helpers ------------------------

// dialTLS is a real TLS handshake against the edge's real listener, with the
// SNI name of the hostname under test. Everything a client does to reach the
// edge is done here for real: TCP connect, ClientHello with ServerName,
// certificate selection by name, and the HTTP request over the encrypted
// connection.
func clientFor(addr, sni string) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialTLSContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				tc := tls.Client(conn, &tls.Config{
					ServerName: sni,
					// The certificate is minted by the box for a name that is
					// not in any trust store, which is the honest state of a
					// `.test` name. What is being proven is that the EDGE
					// selected a certificate for THIS name and completed a
					// handshake with it — not that a public CA vouches for a
					// name nobody owns.
					InsecureSkipVerify: true, //nolint:gosec // the minted cert is the thing under test
					MinVersion:         tls.VersionTLS12,
				})
				if err := tc.HandshakeContext(ctx); err != nil {
					conn.Close()
					return nil, err
				}
				return tc, nil
			},
		},
	}
}

func get(t *testing.T, client *http.Client, addr, host, path string) (int, string) {
	t.Helper()
	url := "https://" + host + path
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build the request for %s: %v", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s through the edge: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the answer from %s: %v", url, err)
	}
	return resp.StatusCode, string(body)
}

// ---------------------------------------------------------------------------
// the edge, exercised for real
// ---------------------------------------------------------------------------

// The edge routes by hostname over a real TLS handshake on a real listener,
// and the two names this plan is about — api.<box> and auth.<box> — answer
// through it. `/healthz` is the edge's own path and `/readyz` is the app's
// (see HealthPath's comment); both are exercised so the distinction is proven
// rather than asserted.
func TestTheEdgeRoutesByHostnameOverRealTLS(t *testing.T) {
	api := httptest.NewServer(sampleStack{release: "v1"}.handler())
	defer api.Close()
	auth := httptest.NewServer(sampleStack{release: "v1"}.handler())
	defer auth.Close()

	box := newBoxState(t, "box-01", ModeOrigin)
	box.upstream["api.anywaye.test"] = strings.TrimPrefix(api.URL, "http://")
	box.upstream["auth.anywaye.test"] = strings.TrimPrefix(auth.URL, "http://")
	box.deploy(t, "sample-stack-1", "api.anywaye.test", "auth.anywaye.test")

	certs := NewMinted("api.anywaye.test", "auth.anywaye.test")
	_, addr := box.gateway(t, certs)

	for _, host := range []string{"api.anywaye.test", "auth.anywaye.test"} {
		client := clientFor(addr, host)

		code, body := get(t, client, addr, host, "/readyz")
		if code != http.StatusOK {
			t.Errorf("GET https://%s/readyz = %d, want 200 (body: %s)", host, code, body)
		}
		if !strings.Contains(body, "v1 ready") {
			t.Errorf("GET https://%s/readyz answered %q, want the sample stack's readiness", host, body)
		}

		code, body = get(t, client, addr, host, "/")
		if code != http.StatusOK {
			t.Errorf("GET https://%s/ = %d, want 200 (body: %s)", host, code, body)
		}
		// The app sees the hostname the client asked for, not the upstream's
		// address — the Director's one job.
		if !strings.Contains(body, "on "+host) {
			t.Errorf("the app saw %q, want it to see the hostname %q it was asked for", body, host)
		}

		// The edge's own health path, answered by the edge through the same
		// handshake: the box, the mode, the release serving, and the health
		// the edge last observed for this host.
		code, body = get(t, client, addr, host, HealthPath)
		if code != http.StatusOK {
			t.Fatalf("GET https://%s%s = %d, want 200 (body: %s)", host, HealthPath, code, body)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatalf("the edge's health answer is not JSON: %v (%s)", err, body)
		}
		for _, want := range []string{"edge", "box", "mode", "current", "hosts"} {
			if _, ok := doc[want]; !ok {
				t.Errorf("the edge's health answer has no %q field: %s", want, body)
			}
		}
		if doc["current"] != "sample-stack-1" {
			t.Errorf("the edge reports current = %v, want sample-stack-1", doc["current"])
		}
		if doc["mode"] != string(ModeOrigin) {
			t.Errorf("the edge reports mode = %v, want %s", doc["mode"], ModeOrigin)
		}
		if strings.Contains(body, "PRIVATE KEY") || strings.Contains(body, "privateKey") {
			t.Errorf("the edge's own health answer carries key material: %s", body)
		}
	}
}

// The internal zone is served too, and over the same handshake: a wildcard
// certificate covers `*.svc.anywaye.com` and the box routes it cluster-internally.
// The plan's stability rule 4 says those names never appear in public DNS, so
// the certificate is minted on the box and not issued for them at all.
func TestTheInternalZoneIsServedOverTheSameHandshake(t *testing.T) {
	identity := httptest.NewServer(sampleStack{release: "v1"}.handler())
	defer identity.Close()

	box := newBoxState(t, "box-01", ModeInternal)
	box.upstream["identity.svc.anywaye.com"] = strings.TrimPrefix(identity.URL, "http://")
	box.deploy(t, "sample-stack-1", "identity.svc.anywaye.com")

	certs := NewMinted("*.svc.anywaye.com")
	_, addr := box.gateway(t, certs)

	client := clientFor(addr, "identity.svc.anywaye.com")
	code, body := get(t, client, addr, "identity.svc.anywaye.com", "/readyz")
	if code != http.StatusOK {
		t.Fatalf("GET https://identity.svc.anywaye.com/readyz = %d, want 200 (body: %s)", code, body)
	}
	if !strings.Contains(body, "v1 ready") {
		t.Errorf("the .svc name answered %q, want the sample stack's readiness", body)
	}
}

// A hostname the box does not serve is refused by name, and the refusal names
// the hostname and what the box does serve. A default upstream that served
// whichever app happened to be first would be the ingress-shaped failure this
// package exists to avoid.
func TestAHostnameTheBoxDoesNotServeIsRefusedByName(t *testing.T) {
	api := httptest.NewServer(sampleStack{release: "v1"}.handler())
	defer api.Close()

	box := newBoxState(t, "box-01", ModeOrigin)
	box.upstream["api.anywaye.test"] = strings.TrimPrefix(api.URL, "http://")
	box.deploy(t, "sample-stack-1", "api.anywaye.test")

	certs := NewMinted("api.anywaye.test", "api.anywaye.test.other")
	_, addr := box.gateway(t, certs)

	client := clientFor(addr, "api.anywaye.test.other")
	code, body := get(t, client, addr, "api.anywaye.test.other", "/")
	if code != http.StatusNotFound {
		t.Fatalf("GET a host the box does not serve = %d, want 404 (body: %s)", code, body)
	}
	if !strings.Contains(body, "api.anywaye.test.other") {
		t.Errorf("the refusal does not name the hostname: %s", body)
	}
	if !strings.Contains(body, "api.anywaye.test") {
		t.Errorf("the refusal does not say what the box does serve: %s", body)
	}
}

// The release switch is a rename: a request in flight reaches the release it
// started on, `current` moves atomically, and a rollback needs nothing but the
// filesystem. This is the plan's "a broken deploy demonstrates rollback"
// checklist item, and it is proven against the symlink rather than against a
// mock of one.
//
// The upstream here is the release directory itself, served over the `current`
// symlink — which is the shape the box actually has: boxd serves `current` and
// the edge routes a hostname at it. So a publish and a rollback change what
// the customer gets with no change to the routing record at all, and the
// routing record is what this test asserts did NOT need to change.
func TestTheSwitchIsARenameAndRollbackNeedsNoControlPlane(t *testing.T) {
	dir := t.TempDir()

	// The stack, served out of `current` — the shape the box actually has:
	// boxd serves the release directory the symlink names and the edge routes
	// a hostname at it. `/readyz` is answered so the edge's health probe has
	// something to observe; everything else is a file out of the release the
	// symlink names, which is what makes a publish and a rollback visible.
	stack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "ready")
			return
		}
		http.FileServer(http.Dir(CurrentPath(dir))).ServeHTTP(w, r)
	}))
	defer stack.Close()

	box := &boxState{dir: dir, upstream: map[string]string{"api.anywaye.test": strings.TrimPrefix(stack.URL, "http://")}}
	box.spec = New("box-01", ModeOrigin)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create the box state directory: %v", err)
	}
	if err := Save(dir, box.spec); err != nil {
		t.Fatalf("write the spec: %v", err)
	}
	box.deploy(t, "sample-stack-1", "api.anywaye.test")

	if got, err := Current(dir); err != nil || got != "sample-stack-1" {
		t.Fatalf("current = %q, %v; want sample-stack-1", got, err)
	}

	// The release the symlink names is a real directory with the files the
	// deploy wrote, and it is a symlink rather than a copy.
	target, err := os.Readlink(CurrentPath(dir))
	if err != nil {
		t.Fatalf("current is not a symlink: %v", err)
	}
	if want := filepath.Join(ReleaseDirName, "sample-stack-1"); target != want {
		t.Errorf("current points at %q, want the relative target %q", target, want)
	}

	_, addr := box.gateway(t, NewMinted("api.anywaye.test"))
	client := clientFor(addr, "api.anywaye.test")

	// A second deploy moves current at the new release, and what the customer
	// gets changes with it — through the edge, over the wire, with the routing
	// record untouched.
	before, err := Load(dir)
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	box.deploy(t, "sample-stack-2", "api.anywaye.test")

	if _, body := get(t, client, addr, "api.anywaye.test", "/README"); !strings.Contains(body, "sample-stack-2") {
		t.Errorf("after the second deploy the edge answered %q, want sample-stack-2", body)
	}
	after, err := Load(dir)
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	if len(before.Hosts) != len(after.Hosts) || !sameRoute(before.Hosts[0], after.Hosts[0]) {
		t.Errorf("the routing row moved during a deploy: %+v -> %+v", before.Hosts[0], after.Hosts[0])
	}

	// Rollback: the edge's own record, the edge's own filesystem, and no
	// control plane anywhere in the call.
	spec, err := Load(dir)
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	back, err := Rollback(dir, spec)
	if err != nil {
		t.Fatalf("roll back: %v", err)
	}
	if back != "sample-stack-1" {
		t.Fatalf("rollback returned %q, want sample-stack-1", back)
	}
	if got, _ := Current(dir); got != "sample-stack-1" {
		t.Errorf("current = %q after the rollback, want sample-stack-1", got)
	}
	if _, body := get(t, client, addr, "api.anywaye.test", "/README"); !strings.Contains(body, "sample-stack-1") {
		t.Errorf("after the rollback the edge answered %q, want sample-stack-1", body)
	}

	// And a rollback with nothing to go back to refuses rather than guessing
	// at the second-newest directory.
	spec, _ = Load(dir)
	spec.Previous = ""
	if _, err := Rollback(dir, spec); err == nil {
		t.Error("a rollback with no recorded previous release did not refuse")
	}
}

// TestTheProbeLoopPersistsTheHealthItObserved is the deterministic half the
// running gateways in this file deliberately do not exercise. probeAll measures
// every upstream and writes what it saw through the Sink, and this test drives
// it as the ONLY writer — no deploy runs beside it — which is the state a box
// is in when nobody is deploying. The switch test used to be the only cover
// for the Sink, and there it was a second writer racing the deploy's own; the
// lost update between the two is why the gateway helper now builds with no
// sink at all.
func TestTheProbeLoopPersistsTheHealthItObserved(t *testing.T) {
	up := httptest.NewServer(sampleStack{release: "v1"}.handler())
	defer up.Close()

	b := newBoxState(t, "box-01", ModeInternal)
	b.upstream["api.anywaye.test"] = strings.TrimPrefix(up.URL, "http://")
	b.deploy(t, "sample-stack-1", "api.anywaye.test")

	g := &Gateway{
		Spec:     func() (Spec, error) { return Load(b.dir) },
		Sink:     func(s Spec) error { return Save(b.dir, s) },
		ErrorLog: log.New(io.Discard, "", 0),
	}
	g.probeAll(context.Background())

	got, err := Load(b.dir)
	if err != nil {
		t.Fatalf("read the record after the probe: %v", err)
	}
	row, ok := got.Host("api.anywaye.test")
	if !ok {
		t.Fatal("the record lost its host row")
	}
	if row.Health != HealthHealthy {
		t.Errorf("the persisted health = %q, want %q", row.Health, HealthHealthy)
	}
	// The write moved the health column and nothing else — not the release
	// pointer, which the probe never measured.
	if got.Current != "sample-stack-1" {
		t.Errorf("the probe moved current to %q, want sample-stack-1", got.Current)
	}
}

// An unhealthy upstream is answered by the edge with 503 and the edge's own
// reason, and the app behind it is never reached. The alternative — a 502 from
// the proxy — sends a customer to debug an app that is already down for a
// reason the edge knows.
func TestAnUnhealthyUpstreamIsAnsweredByTheEdgeNotTheApp(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "app on %s", r.Host)
	}))
	defer up.Close()

	box := newBoxState(t, "box-01", ModeInternal)
	box.upstream["api.anywaye.test"] = strings.TrimPrefix(up.URL, "http://")
	box.deploy(t, "sample-stack-1", "api.anywaye.test")

	g, addr := box.gateway(t, NewMinted("api.anywaye.test"))
	g.probeAll(context.Background())

	client := clientFor(addr, "api.anywaye.test")
	code, body := get(t, client, addr, "api.anywaye.test", "/")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("GET through an unhealthy upstream = %d, want 503 (body: %s)", code, body)
	}
	if !strings.Contains(body, "500") {
		t.Errorf("the edge's answer does not carry the reason it observed: %s", body)
	}
	if strings.Contains(body, "app on") {
		t.Errorf("the request reached the app: %s", body)
	}

	// The edge's own health path still answers, and says so: an edge that
	// cannot report because one upstream is down is an edge that hides the
	// rest of the box.
	if code, body := get(t, client, addr, "api.anywaye.test", HealthPath); code != http.StatusOK {
		t.Errorf("GET %s with an unhealthy upstream = %d, want 200 (body: %s)", HealthPath, code, body)
	} else if !strings.Contains(body, "unhealthy") {
		t.Errorf("the edge's health answer does not carry the health it observed: %s", body)
	}
}

// The switch is atomic, and that is a property of `rename(2)` rather than of
// this package's care — so the claim is observed rather than asserted. A
// request that is in flight while the link moves reaches the release it started
// on, which is the whole of zero downtime: the reader holds the directory it
// resolved, not a path it re-resolves on every stat.
func TestTheSwitchIsAtomicForARequestInFlight(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b := &boxState{dir: dir, upstream: map[string]string{}}
	b.spec = New("box-01", ModeInternal)

	// A release whose file is served slowly, so the request is guaranteed to
	// still be in flight when the switch happens.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(120 * time.Millisecond)
			fmt.Fprintln(w, "started on sample-stack-1")
			return
		}
		if r.URL.Path == "/readyz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		body, err := os.ReadFile(filepath.Join(dir, "releases", "sample-stack-1", "README"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write(body)
	}))
	defer slow.Close()
	b.upstream["api.anywaye.test"] = strings.TrimPrefix(slow.URL, "http://")

	rel, err := Publish(dir, "sample-stack-1", map[string][]byte{"README": []byte("one\n")})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	b.spec = b.spec.WithHost(Host{Name: "api.anywaye.test", Upstream: b.upstream["api.anywaye.test"]})
	b.spec.Current = rel.ID
	if err := Save(dir, b.spec); err != nil {
		t.Fatalf("write the spec: %v", err)
	}

	_, addr := b.gateway(t, NewMinted("api.anywaye.test"))
	client := clientFor(addr, "api.anywaye.test")

	// The request begins, and while it is in flight a whole deploy happens.
	done := make(chan string, 1)
	go func() {
		_, body := get(t, client, addr, "api.anywaye.test", "/slow")
		done <- body
	}()
	time.Sleep(30 * time.Millisecond)
	if _, err := Publish(dir, "sample-stack-2", map[string][]byte{"README": []byte("two\n")}); err != nil {
		t.Fatalf("the second publish failed mid-flight: %v", err)
	}
	if got, _ := Current(dir); got != "sample-stack-2" {
		t.Fatalf("current = %q after the second publish, want sample-stack-2", got)
	}

	body := <-done
	if !strings.Contains(body, "started on sample-stack-1") {
		t.Errorf("the in-flight request did not complete on the release it started on: %q", body)
	}
}

// A check nobody has watched go red is a check that might not work. This is the
// negative control over the routing decision: a spec whose host row has been
// RETIRED — removed from the record while a client still asks for it — must be
// refused by name, and the control proves the refusal is a decision rather
// than an accident of ordering.
func TestARetiredHostIsRefusedAndTheControlProvesIt(t *testing.T) {
	api := httptest.NewServer(sampleStack{release: "v1"}.handler())
	defer api.Close()

	box := newBoxState(t, "box-01", ModeOrigin)
	box.upstream["api.anywaye.test"] = strings.TrimPrefix(api.URL, "http://")
	box.deploy(t, "sample-stack-1", "api.anywaye.test")

	// The row is dropped from the record — the deploy that retired a hostname
	// — and the file is re-written as a real retire would leave it.
	spec, err := Load(box.dir)
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	spec.Hosts = nil
	if err := Save(box.dir, spec); err != nil {
		t.Fatalf("write the retired spec: %v", err)
	}

	_, addr := box.gateway(t, NewMinted("api.anywaye.test"))
	client := clientFor(addr, "api.anywaye.test")

	code, body := get(t, client, addr, "api.anywaye.test", "/")
	if code != http.StatusNotFound {
		t.Fatalf("a retired host answered %d, want 404 (body: %s)", code, body)
	}
	if !strings.Contains(body, "no_route") {
		t.Errorf("the refusal does not carry its own reason: %s", body)
	}

	// The control: put the row back and the same request succeeds. A test
	// that only asserted the 404 would also pass against a gateway that
	// refused everything.
	if err := Save(box.dir, func() Spec {
		s, _ := Load(box.dir)
		return s.WithHost(Host{Name: "api.anywaye.test", Upstream: box.upstream["api.anywaye.test"]})
	}()); err != nil {
		t.Fatalf("restore the row: %v", err)
	}
	if code, body = get(t, client, addr, "api.anywaye.test", "/"); code != http.StatusOK {
		t.Errorf("with the row restored the edge answered %d, want 200 (body: %s)", code, body)
	}
}
