package resolve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHTTPConductorMintsAndRedeems drives the real client through the whole of
// tier 3: the entitlement check posts the triple, the mint answers with a
// capability, and the redemption returns the fetch grant.
func TestHTTPConductorMintsAndRedeems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/accounts/11111111-1111-4111-8111-111111111111/artifact_fetches":
			if r.Method != http.MethodPost {
				t.Errorf("mint method = %q", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cafaye_caller" {
				t.Errorf("mint bearer = %q, want the caller's token", got)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"capability":{"token":"cap_abc","purpose":"artifact.fetch","account_id":"11111111-1111-4111-8111-111111111111","artifact":"identity","artifact_version_range":"^0.1.0","expires_at":"2026-10-09T16:00:00Z"}}`))
		case "/v1/capabilities/redeem":
			if r.Method != http.MethodPost {
				t.Errorf("redeem method = %q", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cap_abc" {
				t.Errorf("redeem bearer = %q, want the capability token", got)
			}
			_, _ = w.Write([]byte(`{"capability":{"purpose":"artifact.fetch","account_id":"11111111-1111-4111-8111-111111111111","artifact":"identity","artifact_version_range":"^0.1.0","expires_at":"2026-10-09T16:00:00Z"}}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := HTTPConductor{BaseURL: srv.URL}
	grant, err := c.ArtifactFetch(context.Background(), "11111111-1111-4111-8111-111111111111", "identity", "^0.1.0", "cafaye_caller")
	if err != nil {
		t.Fatalf("artifact fetch: %v", err)
	}
	if grant.Purpose != "artifact.fetch" {
		t.Errorf("purpose = %q", grant.Purpose)
	}
	if grant.Artifact != "identity" {
		t.Errorf("artifact = %q", grant.Artifact)
	}
	if grant.VersionRange != "^0.1.0" {
		t.Errorf("version range = %q", grant.VersionRange)
	}
}

// The two named 403s are distinguished by the code in the body, and the client
// reads them out of the body rather than the status.
func TestHTTPConductorEntitlementRequiredIsNamed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"entitlement_required","message":"product ` + "`identity`" + ` needs an entitlement — start one at https://cafaye.com"}}`))
	}))
	defer srv.Close()

	c := HTTPConductor{BaseURL: srv.URL}
	_, err := c.ArtifactFetch(context.Background(), "11111111-1111-4111-8111-111111111111", "identity", "^0.1.0", "cafaye_caller")
	if err == nil {
		t.Fatalf("a 403 resolved without an error")
	}
	ce, ok := err.(*ConductorError)
	if !ok {
		t.Fatalf("error = %T, want *ConductorError", err)
	}
	if ce.Code != "entitlement_required" {
		t.Errorf("code = %q, want entitlement_required", ce.Code)
	}
}

func TestHTTPConductorVersionNotGrantedIsNamed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"version_not_granted","message":"your entitlement covers ` + "`^0.2.0`" + `, this project asks for ` + "`^0.1.0`" + `"}}`))
	}))
	defer srv.Close()

	c := HTTPConductor{BaseURL: srv.URL}
	_, err := c.ArtifactFetch(context.Background(), "11111111-1111-4111-8111-111111111111", "identity", "^0.1.0", "cafaye_caller")
	if err == nil {
		t.Fatalf("a 403 resolved without an error")
	}
	ce, ok := err.(*ConductorError)
	if !ok {
		t.Fatalf("error = %T, want *ConductorError", err)
	}
	if ce.Code != "version_not_granted" {
		t.Errorf("code = %q, want version_not_granted", ce.Code)
	}
}

// A 404 — an agent acting alone, or an unknown account, which conductor
// answers alike on purpose — is a named ConductorError.
func TestHTTPConductorNotFoundIsNamed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"not found"}}`))
	}))
	defer srv.Close()

	c := HTTPConductor{BaseURL: srv.URL}
	_, err := c.ArtifactFetch(context.Background(), "11111111-1111-4111-8111-111111111111", "identity", "^0.1.0", "cafaye_caller")
	if err == nil {
		t.Fatalf("a 404 resolved without an error")
	}
	ce, ok := err.(*ConductorError)
	if !ok {
		t.Fatalf("error = %T, want *ConductorError", err)
	}
	if ce.Code != "not_found" {
		t.Errorf("code = %q, want not_found", ce.Code)
	}
}

// A redemption refusal — the capability already spent — is named, so a
// double-redeem is a sentence rather than a silent error.
func TestHTTPConductorRedeemConflictIsNamed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/capabilities/redeem" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"capability_redeemed","message":"capability has already been redeemed"}}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"capability":{"token":"cap_abc","purpose":"artifact.fetch"}}`))
	}))
	defer srv.Close()

	c := HTTPConductor{BaseURL: srv.URL}
	_, err := c.ArtifactFetch(context.Background(), "11111111-1111-4111-8111-111111111111", "identity", "^0.1.0", "cafaye_caller")
	if err == nil {
		t.Fatalf("a redeemed capability resolved without an error")
	}
	ce, ok := err.(*ConductorError)
	if !ok {
		t.Fatalf("error = %T, want *ConductorError", err)
	}
	if ce.Code != "capability_redeemed" {
		t.Errorf("code = %q, want capability_redeemed", ce.Code)
	}
}

// A client with no endpoint refuses rather than POSTing to a relative path.
func TestHTTPConductorWithoutBaseURLRefuses(t *testing.T) {
	c := HTTPConductor{}
	_, err := c.ArtifactFetch(context.Background(), "11111111-1111-4111-8111-111111111111", "identity", "^0.1.0", "cafaye_caller")
	if err == nil {
		t.Fatalf("a client with no endpoint resolved without an error")
	} else if _, ok := err.(*ConductorError); !ok {
		t.Errorf("error = %T, want *ConductorError", err)
	}
}
