package resolve

import (
	"context"
	"net/http"
	"net/http/httptest"
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
		_, _ = w.Write([]byte(`{"name":"identity","description":"Authentication","language":"go","visibility":"private"}`))
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
	if svc.Visibility != "private" {
		t.Errorf("visibility = %q", svc.Visibility)
	}
}

// A 404 is a PantryError carrying the problem's own code and detail, so the
// resolver can surface them verbatim.
func TestHTTPPantryTier1NotFoundIsNamed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"type":"https://errors.cafaye.com/not_found","title":"Not found","status":404,"detail":"no official cafaye service is named \"nope\"","instance":"/v1/services/nope","code":"not_found","trace_id":"0af7651916cd43dd8448eb211c80319c"}`))
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
	if pe.Detail == "" {
		t.Errorf("detail is empty, want the problem's detail")
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
// PantryError with pantry's own detail, which is the same for both refusals
// by design.
func TestHTTPPantryTier2UnauthorizedIsNamed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"https://errors.cafaye.com/unauthorized","title":"Unauthorized","status":401,"detail":"this operation requires a caf token and none was presented","instance":"/v1/services/identity/coordinates","code":"unauthorized","trace_id":"0af7651916cd43dd8448eb211c80319c"}`))
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
