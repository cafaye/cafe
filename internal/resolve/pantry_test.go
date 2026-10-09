package resolve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHTTPPantryTier1ServesTheListingEntry drives the real HTTP client against
// a server that answers the documented shape, so the decoding — not a fake —
// is what is under test.
func TestHTTPPantryTier1ServesTheListingEntry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/services/identity" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("tier 1 is anonymous but a bearer was presented")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"identity","description":"Authentication","language":"go","repository":{"visibility":"private"},"owner":{"team":"identity"}}`))
	}))
	defer srv.Close()

	p := HTTPPantry{BaseURL: srv.URL}
	svc, err := p.Service(context.Background(), "identity")
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	if svc.Name != "identity" {
		t.Errorf("name = %q", svc.Name)
	}
	if svc.Repository.Visibility != "private" {
		t.Errorf("visibility = %q, want it under `repository` where pantry sends it", svc.Repository.Visibility)
	}
}

// The 404 pantry actually answers for an unknown name, taken from
// internal/httpapi/httpapi.go's `catalog.ErrNoSuchService` branch. It is
// written out here rather than asserted loosely because the sentence does NOT
// name the requested service — a detail that echoed the name would make the
// 404 an existence oracle, which is the property pantry's own tests pin.
func TestHTTPPantryTier1NotFoundIsNamed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"type":"https://errors.cafaye.com/not_found","title":"Not found","status":404,"detail":"no official cafaye service is registered under that name","instance":"/v1/services/nope","code":"not_found","trace_id":"0af7651916cd43dd8448eb211c80319c"}`))
	}))
	defer srv.Close()

	p := HTTPPantry{BaseURL: srv.URL}
	_, err := p.Service(context.Background(), "nope")
	if err == nil {
		t.Fatalf("a 404 resolved without an error")
	}
	pe, ok := err.(*PantryError)
	if !ok {
		t.Fatalf("error = %T, want *PantryError", err)
	}
	if pe.Code != "not_found" {
		t.Errorf("code = %q, want not_found", pe.Code)
	}
	if pe.Status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", pe.Status)
	}
	if pe.Detail != "no official cafaye service is registered under that name" {
		t.Errorf("detail = %q, want the sentence pantry sends (and it must not echo the name)", pe.Detail)
	}
	// The trace id is the field pantry's own support starts from, and dropping
	// it would be the failure the code-and-detail decoding already avoided,
	// one field further on.
	if pe.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("trace_id = %q, want the problem's own", pe.TraceID)
	}
}

// Tier 2 presents the caller's caf token as the bearer and decodes the
// coordinates.
func TestHTTPPantryTier2ServesCoordinatesWithTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/services/identity/coordinates" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cafaye_x" {
			t.Errorf("bearer = %q, want the caller's token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"identity","repository":{"url":"git@github.com:cafaye/identity.git","defaultBranch":"master"}}`))
	}))
	defer srv.Close()

	p := HTTPPantry{BaseURL: srv.URL}
	coords, err := p.Coordinates(context.Background(), "identity", "cafaye_x")
	if err != nil {
		t.Fatalf("coordinates: %v", err)
	}
	if coords.Repository.URL != "git@github.com:cafaye/identity.git" {
		t.Errorf("url = %q", coords.Repository.URL)
	}
	if coords.Repository.DefaultBranch != "master" {
		t.Errorf("defaultBranch = %q", coords.Repository.DefaultBranch)
	}
}

// A 401 from tier 2 — no token, or a token identity calls inactive — is a
// PantryError with pantry's own detail, which is the same long sentence for
// both refusals BY DESIGN (a 401 that said which one it was would be a probe
// for whether a stolen value was live). The client reads the code and the
// detail; it never switches on the detail to tell the two apart, and this test
// is where that rule is pinned.
func TestHTTPPantryTier2UnauthorizedIsNamed(t *testing.T) {
	const noEntitlementYet = "this operation requires a caf token and none was presented. A service's " +
		"repository coordinates are tier 2: `GET /v1/services` and `GET /v1/services/{name}` publish what a " +
		"service is, and this operation alone publishes where its code lives. Present `Authorization: Bearer " +
		"cafaye_…` with a live caf token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"https://errors.cafaye.com/unauthorized","title":"Unauthorized","status":401,"detail":"` + noEntitlementYet + `","instance":"/v1/services/identity/coordinates","code":"unauthorized","trace_id":"0af7651916cd43dd8448eb211c80319c"}`))
	}))
	defer srv.Close()

	p := HTTPPantry{BaseURL: srv.URL}
	_, err := p.Coordinates(context.Background(), "identity", "")
	if err == nil {
		t.Fatalf("a 401 resolved without an error")
	}
	pe, ok := err.(*PantryError)
	if !ok {
		t.Fatalf("error = %T, want *PantryError", err)
	}
	if pe.Code != "unauthorized" {
		t.Errorf("code = %q, want unauthorized", pe.Code)
	}
	if pe.Status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", pe.Status)
	}
	if pe.Detail != noEntitlementYet {
		t.Errorf("detail = %q, want pantry's sentence verbatim", pe.Detail)
	}
}

// Identity unreachable is pantry's 503, never a 401 and never the coordinates:
// a gate that fails open when its verifier is down is not a gate, and an
// outage reported as 401 would send every caller with a good token to go and
// get another one. The client surfaces it as the tier-2 failure it is, with
// the detail that says it is a deployment problem rather than a token one.
func TestHTTPPantryTier2IdentityUnreachableIsAnUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"type":"https://errors.cafaye.com/unavailable","title":"Service unavailable","status":503,"detail":"the presented caf token could not be verified: identity could not be reached, so this operation was refused rather than answered. This is a deployment problem rather than a problem with your token","instance":"/v1/services/identity/coordinates","code":"unavailable","trace_id":"0af7651916cd43dd8448eb211c80319c"}`))
	}))
	defer srv.Close()

	p := HTTPPantry{BaseURL: srv.URL}
	_, err := p.Coordinates(context.Background(), "identity", "cafaye_x")
	if err == nil {
		t.Fatalf("a 503 resolved without an error")
	}
	pe, ok := err.(*PantryError)
	if !ok {
		t.Fatalf("error = %T, want *PantryError", err)
	}
	if pe.Code != "unavailable" {
		t.Errorf("code = %q, want unavailable", pe.Code)
	}
	if !strings.Contains(pe.Detail, "identity could not be reached") {
		t.Errorf("detail = %q, want it to name the deployment problem", pe.Detail)
	}
}

// A client with no endpoint refuses rather than GETting a relative path.
func TestHTTPPantryWithoutBaseURLRefuses(t *testing.T) {
	p := HTTPPantry{}
	if _, err := p.Service(context.Background(), "identity"); err == nil {
		t.Fatalf("a client with no endpoint resolved without an error")
	} else if _, ok := err.(*PantryError); !ok {
		t.Errorf("error = %T, want *PantryError", err)
	}
}
