package stack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestAServiceChangeGoesThroughTheHostNamesOnTheBox is the packet's
// verification item, and it is hermetic on purpose: the box's edge is a real
// loopback HTTP server that routes on the Host header, two real host names
// are deployed through it, and a third deploy that fails its gate is watched
// putting the previous one back.
//
// It runs in the gate rather than in the live tier because the property being
// proved is the pointer and the history, not Docker: the edge below serves the
// file the `current` symlink resolves to, which is exactly what the box's
// edge does with the release directory, so switching the pointer is observed
// as a change in what the host answers. A test that needed a container
// runtime to prove that would be a test nobody runs.
func TestAServiceChangeGoesThroughTheHostNamesOnTheBox(t *testing.T) {
	// ONE BOX, SEVERAL SERVICES: a stack root with a stack per service, and an
	// edge in front of both. The edge is the only thing that knows both, which
	// is why a rollback of one service does not touch the other.
	root := t.TempDir()
	apiStack, authStack := openStackIn(t, root, "api"), openStackIn(t, root, "auth")
	stacks := map[string]*Store{"api.anywaye.test": apiStack, "auth.anywaye.test": authStack}

	// The edge: one listener on loopback, two host names, and its upstream is
	// the directory the pointer names. This is the whole of kamal's proxy in
	// ten lines, and it is why a rollback is instant rather than a rebuild.
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel, err := stacks[r.Host].Current()
		if err != nil || rel.ID == "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Host != rel.Host {
			// A host this box serves nothing on is a 503, not a wrong
			// service's answer.
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		http.ServeFile(w, r, stacks[r.Host].releaseDir(rel.ID)+"/index.html")
	}))
	defer edge.Close()

	b := newBox(t, map[string]*Store{"api": apiStack, "auth": authStack}, "api.anywaye.test", "auth.anywaye.test")

	ctx := context.Background()
	apiDigest, api2Digest, authDigest := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	// What each build serves once it is on the box. Keyed by digest, because
	// the box writes the bytes while the release is being started.
	b.content = map[string]string{
		"sha256:" + apiDigest:  "api 1.4.0",
		"sha256:" + api2Digest: "api 1.5.0",
		"sha256:" + authDigest: "auth 2.1.0",
	}

	// 1. api ships. Through the Host header, on the real name.
	api, err := Deploy(ctx, apiStack, b, Request{
		Service: "api", Host: "api.anywaye.test", Secrets: "api/production",
		Build: Build{Registry: "ghcr.io", Name: "anywaye/api", Tag: "1.4.0", Digest: "sha256:" + apiDigest},
	})
	if err != nil {
		t.Fatalf("api deploy: %v", err)
	}
	if got := get(t, edge.URL, "api.anywaye.test"); got != "api 1.4.0" {
		t.Fatalf("api.anywaye.test answered %q, want the release it was just given", got)
	}

	// 2. auth ships on its OWN host name, and api is untouched.
	if _, err := Deploy(ctx, authStack, b, Request{
		Service: "auth",
		Host:    "auth.anywaye.test",
		Secrets: "auth/production",
		Build:   Build{Registry: "ghcr.io", Name: "anywaye/auth", Tag: "2.1.0", Digest: "sha256:" + authDigest},
	}); err != nil {
		t.Fatalf("auth deploy: %v", err)
	}

	// 3. The service change: api ships a second build, and the name answers
	//    with it — while auth still answers with its own.
	api2, err := Deploy(ctx, apiStack, b, Request{
		Service: "api", Host: "api.anywaye.test", Secrets: "api/production",
		Build: Build{Registry: "ghcr.io", Name: "anywaye/api", Tag: "1.5.0", Digest: "sha256:" + api2Digest},
	})
	if err != nil {
		t.Fatalf("the api service change: %v", err)
	}
	if api2.Release.ID == api.Release.ID {
		t.Fatal("the service change did not stage a second release")
	}
	if got := get(t, edge.URL, "api.anywaye.test"); got != "api 1.5.0" {
		t.Fatalf("api.anywaye.test still answers %q after the service change", got)
	}

	// 4. The broken deploy: it fails its gate, and the box puts the previous
	//    release back — the host answers the good release again, on purpose.
	// The broken deploy is broken by ARTIFACT: its build is the one the box
	// cannot bring up, which is what a real failure looks like — a bad image,
	// not a bad clock.
	b.failReady = "sha256:" + strings.Repeat("d", 64)
	rep, err := Deploy(ctx, apiStack, b, Request{
		Service: "api", Host: "api.anywaye.test", Secrets: "api/production",
		Build: Build{Registry: "ghcr.io", Name: "anywaye/api", Tag: "1.6.0", Digest: "sha256:" + strings.Repeat("d", 64)},
		Now:   time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("the broken deploy returned no error, so a script would have believed it worked")
	}
	if !rep.RolledBack {
		t.Fatalf("the report does not say the box rolled back: %+v", rep)
	}
	if got := get(t, edge.URL, "api.anywaye.test"); got != "api 1.5.0" {
		t.Fatalf("api.anywaye.test answers %q after the broken deploy, want the release the box rolled back to", got)
	}
	// ...and the rollback left auth alone: one stack, several services, and a
	// rollback of one is not a rollback of the box.
	if got := get(t, edge.URL, "auth.anywaye.test"); got != "auth 2.1.0" {
		t.Fatalf("auth.anywaye.test answers %q, and a rollback of api must not touch it", got)
	}
}

// get is a request the way a client makes one: to the edge, carrying the host
// name. Nothing here consults the stack, so the answer is what the box is
// really serving.
func get(t *testing.T, url, host string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	// The host name is the whole point: the edge routes on it, so the request
	// has to carry it rather than the edge's own address.
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s answered %d, and the test needs a serving release to read", host, resp.StatusCode)
	}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	return strings.TrimSpace(string(buf[:n]))
}

// TestTheEdgeSeesThePointerSwitchAsNoDowntime is the claim the atomic switch
// exists for: a reader polling the edge never sees a missing answer because
// the pointer was being written. It is a race by construction — a goroutine
// reads while deploys run — and what it asserts is that no read ever fails
// with anything but the states the stack declares.
//
// This test caught a real defect rather than a hypothetical one. When the
// pointer was a `current` SYMLINK switched by rename, this went red on macOS:
// readlink returned EINVAL inside the rename window, so "the box has nothing
// to serve" was a state a client could observe. The pointer became a file
// because of this test, and the test stays so that changing it back is a
// failure rather than a preference.
func TestTheEdgeSeesThePointerSwitchAsNoDowntime(t *testing.T) {
	s := openStack(t)
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The edge reads the pointer with every request, exactly as the real
		// one does. This is the assertion the APFL symlink defect was caught
		// by, and it stays: a pointer read that fails while deploys run is a
		// window in which the box has nothing to serve.
		tgt, err := s.SwitchTarget()
		if err != nil || tgt == "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if _, err := os.Stat(s.ReleasePath(tgt)); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer edge.Close()

	b := newBox(t, map[string]*Store{"api": s}, "api.anywaye.test")
	first := Request{
		Service: "api", Host: "api.anywaye.test", Secrets: "api/production",
		Build: Build{Registry: "ghcr.io", Name: "anywaye/api", Digest: "sha256:" + strings.Repeat("a", 64)},
		Now:   time.Now().UTC(),
	}
	if _, err := Deploy(context.Background(), s, b, first); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	// The reader starts once there IS something to serve: a read before the
	// first deploy is a declared no-current, not a switch window.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, edge.URL, nil)
			if err != nil {
				// A listener that is shutting down is not a pointer failure.
				continue
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				// A listener that is shutting down is not a pointer failure.
				continue
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("the edge answered %d, and the pointer switch had a window in which it had nothing to serve", resp.StatusCode)
				return
			}
		}
	}()
	for _, d := range []string{strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)} {
		req := Request{
			Service: "api", Host: "api.anywaye.test", Secrets: "api/production",
			Build: Build{Registry: "ghcr.io", Name: "anywaye/api", Digest: "sha256:" + d},
			Now:   time.Now().UTC(),
		}
		if _, err := Deploy(context.Background(), s, b, req); err != nil {
			t.Fatalf("deploy: %v", err)
		}
	}
	close(stop)
	<-done
}
