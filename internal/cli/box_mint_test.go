package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cafaye/cafe/internal/box"
	"github.com/google/uuid"
)

var mintTestToken = "caf_human_" + strings.Repeat("m", 40)
var mintCapabilityToken = "cap_" + strings.Repeat("c", 40)

// mintTestRing is the in-memory ring for mint tests, with ListHumanTokens.
type mintTestRing struct {
	entries map[string]string
}

func newMintTestRing() *mintTestRing { return &mintTestRing{entries: map[string]string{}} }

func (r *mintTestRing) Get(service, account string) (string, error) {
	if v, ok := r.entries[service+"/"+account]; ok {
		return v, nil
	}
	return "", box.ErrNotFound
}
func (r *mintTestRing) Set(service, account, value string) error {
	r.entries[service+"/"+account] = value
	return nil
}
func (r *mintTestRing) Delete(service, account string) error {
	delete(r.entries, service+"/"+account)
	return nil
}
func (r *mintTestRing) ListHumanTokens() ([]string, []string, error) {
	var tokens []string
	var accounts []string
	for k, v := range r.entries {
		if strings.HasPrefix(k, "cafe/human/") && strings.TrimSpace(v) != "" {
			tokens = append(tokens, v)
			accounts = append(accounts, k)
		}
	}
	return tokens, accounts, nil
}

var _ box.Keyring = (*mintTestRing)(nil)

func runBoxMintCLI(t *testing.T, deps mintDeps, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion, Context: context.Background()}
	err := newBoxMintCommand(deps).Execute(env, args)
	return codeFor(*env, err), out.String(), errOut.String()
}

func mintTestServer(t *testing.T, accountID, boxID string, wantBearer string) (*httptest.Server, *[]string) {
	t.Helper()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path+" bearer="+r.Header.Get("Authorization"))
		// Check bearer
		if wantBearer != "" && r.Header.Get("Authorization") != "Bearer "+wantBearer {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/accounts" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `[{"id":%q}]`, accountID)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/accounts/"+accountID+"/capabilities" {
			body, _ := io.ReadAll(r.Body)
			var req mintRequest
			_ = json.Unmarshal(body, &req)
			if req.BoxID != boxID {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = fmt.Fprintf(w, `{"error":"box_id mismatch want %s got %s"}`, boxID, req.BoxID)
				return
			}
			if req.Purpose != "box.enroll" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"error":"purpose mismatch"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{
				"capability": map[string]string{
					"token":      mintCapabilityToken,
					"purpose":    "box.enroll",
					"box_id":     boxID,
					"expires_at": "2026-10-15T13:00:00Z",
				},
				"installation": map[string]string{"id": "inst_123"},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestBoxMintHappyPathAssertsRequestAndDocument(t *testing.T) {
	accountID := "11111111-1111-4111-8111-111111111111"
	boxID := uuid.NewString()
	ring := newMintTestRing()
	_ = ring.Set("cafe", "human/noel@cafaye.test", mintTestToken)
	srv, calls := mintTestServer(t, accountID, boxID, mintTestToken)
	deps := mintDeps{ring: ring, boxURL: srv.URL, client: srv.Client()}

	code, stdout, stderr := runBoxMintCLI(t, deps, "--box", srv.URL, "--account", accountID, "--box-id", boxID)
	if code != 0 {
		t.Fatalf("mint exited %d (stderr: %s stdout: %s)", code, stderr, stdout)
	}
	if !json.Valid([]byte(stdout)) {
		t.Fatalf("stdout not JSON: %s", stdout)
	}
	var doc mintDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout not mintDoc: %v %s", err, stdout)
	}
	if doc.Token != mintCapabilityToken {
		t.Errorf("token = %q want %q", doc.Token, mintCapabilityToken)
	}
	if doc.BoxID != boxID {
		t.Errorf("box_id = %q want %q", doc.BoxID, boxID)
	}
	if doc.AccountID != accountID {
		t.Errorf("account_id = %q want %q", doc.AccountID, accountID)
	}
	if doc.ExpiresAt != "2026-10-15T13:00:00Z" {
		t.Errorf("expires_at = %q", doc.ExpiresAt)
	}
	if !strings.Contains(doc.EnrollCommand, "cafe box enroll --token - --box") {
		t.Errorf("enroll_command = %q", doc.EnrollCommand)
	}
	if !strings.Contains(doc.EnrollCommand, srv.URL) {
		t.Errorf("enroll_command missing origin: %q", doc.EnrollCommand)
	}
	if strings.Contains(stderr, mintCapabilityToken) {
		t.Error("stderr carried the capability token; it must be token-free")
	}
	if !strings.Contains(stderr, "single-use") || !strings.Contains(stderr, "minutes-lived") {
		t.Errorf("stderr missing single-use/minutes-lived: %s", stderr)
	}
	if !strings.Contains(doc.Message, "single-use") {
		t.Errorf("doc message missing single-use: %q", doc.Message)
	}
	// Request assertions via server calls: the POST was made with correct body
	found := false
	for _, c := range *calls {
		if strings.Contains(c, "POST /v1/accounts/"+accountID+"/capabilities") {
			found = true
		}
	}
	if !found {
		t.Errorf("mint did not POST the capability: calls %v", *calls)
	}
	// Bearer was checked server-side via wantBearer
}

func TestBoxMintNoSessionIsNamed(t *testing.T) {
	ring := newMintTestRing() // empty
	deps := mintDeps{ring: ring, boxURL: "https://box.test", client: &http.Client{}}
	code, _, stderr := runBoxMintCLI(t, deps, "--box", "https://box.test", "--account", "11111111-1111-4111-8111-111111111111", "--box-id", uuid.NewString())
	if code != exitFailure {
		t.Errorf("exit %d want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "cafe login") {
		t.Errorf("stderr missing cafe login: %s", stderr)
	}
}

func TestBoxMintNoOriginIsRefusal(t *testing.T) {
	ring := newMintTestRing()
	_ = ring.Set("cafe", "human/a@b.test", mintTestToken)
	deps := mintDeps{ring: ring, boxURL: "", client: &http.Client{}}
	// Ensure env not set
	t.Setenv(boxURLEnv, "")
	code, _, stderr := runBoxMintCLI(t, deps, "--account", "11111111-1111-4111-8111-111111111111", "--box-id", uuid.NewString())
	if code != exitFailure {
		t.Errorf("exit %d want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, boxURLEnv) {
		t.Errorf("stderr missing env var: %s", stderr)
	}
	if !strings.Contains(stderr, "--box") {
		t.Errorf("stderr missing --box: %s", stderr)
	}
}

func TestBoxMintAccountAmbiguityIsNamed(t *testing.T) {
	ring := newMintTestRing()
	_ = ring.Set("cafe", "human/noel@cafaye.test", mintTestToken)
	// Server returns two accounts
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/accounts" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"11111111-1111-4111-8111-111111111111"},{"id":"22222222-2222-4222-8222-222222222222"}]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	deps := mintDeps{ring: ring, boxURL: srv.URL, client: srv.Client()}
	code, _, stderr := runBoxMintCLI(t, deps, "--box", srv.URL, "--box-id", uuid.NewString())
	if code != exitFailure {
		t.Errorf("exit %d want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "--account") {
		t.Errorf("stderr missing --account: %s", stderr)
	}
}

func TestBoxMintBearerIsTheStoredSession(t *testing.T) {
	accountID := "11111111-1111-4111-8111-111111111111"
	boxID := uuid.NewString()
	ring := newMintTestRing()
	_ = ring.Set("cafe", "human/noel@cafaye.test", mintTestToken)
	var gotBearer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBearer = r.Header.Get("Authorization")
		if r.URL.Path == "/v1/accounts" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `[{"id":%q}]`, accountID)
			return
		}
		if r.URL.Path == "/v1/accounts/"+accountID+"/capabilities" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"capability":{"token":%q,"purpose":"box.enroll","box_id":%q,"expires_at":"2026-10-15T13:00:00Z"}}`, mintCapabilityToken, boxID)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	deps := mintDeps{ring: ring, boxURL: srv.URL, client: srv.Client()}
	code, _, stderr := runBoxMintCLI(t, deps, "--box", srv.URL, "--box-id", boxID)
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr)
	}
	if gotBearer != "Bearer "+mintTestToken {
		t.Errorf("bearer = %q want Bearer %q", gotBearer, mintTestToken)
	}
}

func TestBoxMintDefaultBoxIDIsFreshUUID(t *testing.T) {
	accountID := "11111111-1111-4111-8111-111111111111"
	ring := newMintTestRing()
	_ = ring.Set("cafe", "human/a@b.test", mintTestToken)
	var seenBoxID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/accounts" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `[{"id":%q}]`, accountID)
			return
		}
		if r.URL.Path == "/v1/accounts/"+accountID+"/capabilities" {
			body, _ := io.ReadAll(r.Body)
			var req mintRequest
			_ = json.Unmarshal(body, &req)
			seenBoxID = req.BoxID
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"capability":{"token":%q,"purpose":"box.enroll","box_id":%q,"expires_at":"2026-10-15T13:00:00Z"}}`, mintCapabilityToken, req.BoxID)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	deps := mintDeps{ring: ring, boxURL: srv.URL, client: srv.Client()}
	code, stdout, stderr := runBoxMintCLI(t, deps, "--box", srv.URL, "--account", accountID)
	if code != 0 {
		t.Fatalf("exit %d %s", code, stderr)
	}
	if _, err := uuid.Parse(seenBoxID); err != nil {
		t.Errorf("generated box_id %q not UUID: %v", seenBoxID, err)
	}
	var doc mintDoc
	_ = json.Unmarshal([]byte(stdout), &doc)
	if doc.BoxID != seenBoxID {
		t.Errorf("doc box_id %q != requested %q", doc.BoxID, seenBoxID)
	}
	// Second mint should generate different UUID
	seenBoxID = ""
	code, stdout, _ = runBoxMintCLI(t, deps, "--box", srv.URL, "--account", accountID)
	if code != 0 {
		t.Fatalf("second exit %d", code)
	}
	var doc2 mintDoc
	_ = json.Unmarshal([]byte(stdout), &doc2)
	if doc2.BoxID == doc.BoxID {
		t.Error("second mint reused the same box_id; fresh UUID expected")
	}
}

func TestBoxMintAPIErrorSurfaced(t *testing.T) {
	ring := newMintTestRing()
	_ = ring.Set("cafe", "human/a@b.test", mintTestToken)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/accounts" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"11111111-1111-4111-8111-111111111111"}]`))
			return
		}
		if r.URL.Path == "/v1/accounts/11111111-1111-4111-8111-111111111111/capabilities" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":"box_id already taken"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	deps := mintDeps{ring: ring, boxURL: srv.URL, client: srv.Client()}
	code, _, stderr := runBoxMintCLI(t, deps, "--box", srv.URL)
	if code != exitFailure {
		t.Errorf("exit %d want failure", code)
	}
	if !strings.Contains(stderr, "422") || !strings.Contains(stderr, "box_id already taken") {
		t.Errorf("stderr missing 422/message: %s", stderr)
	}
}

func TestBoxMintViaBoxCommandTree(t *testing.T) {
	accountID := "11111111-1111-4111-8111-111111111111"
	boxID := uuid.NewString()
	dir := t.TempDir()
	ringDir := filepath.Join(dir, "keyring")
	_ = os.MkdirAll(ringDir, 0o700)
	_ = os.WriteFile(filepath.Join(ringDir, "cafe-human_noel@cafaye.test"), []byte(mintTestToken), 0o600)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/accounts" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `[{"id":%q}]`, accountID)
			return
		}
		if r.URL.Path == "/v1/accounts/"+accountID+"/capabilities" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"capability":{"token":%q,"purpose":"box.enroll","box_id":%q,"expires_at":"2026-10-15T13:00:00Z"}}`, mintCapabilityToken, boxID)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	deps := mintDeps{dir: dir, ring: box.FileRing{Dir: ringDir}, boxURL: srv.URL, client: srv.Client()}
	code, stdout, stderr := runBoxMintCLI(t, deps, "--box", srv.URL, "--box-id", boxID)
	if code != 0 {
		t.Fatalf("exit %d %s %s", code, stderr, stdout)
	}
	var doc mintDoc
	_ = json.Unmarshal([]byte(stdout), &doc)
	if doc.Token != mintCapabilityToken {
		t.Errorf("token %q", doc.Token)
	}
}

func TestBoxMintViaRouterWithInjectedHumanRing(t *testing.T) {
	accountID := "11111111-1111-4111-8111-111111111111"
	boxID := uuid.NewString()
	ring := newMintTestRing()
	_ = ring.Set("cafe", "human/noel@cafaye.test", mintTestToken)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+mintTestToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		if r.URL.Path == "/v1/accounts" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `[{"id":%q}]`, accountID)
			return
		}
		if r.URL.Path == "/v1/accounts/"+accountID+"/capabilities" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"capability":{"token":%q,"purpose":"box.enroll","box_id":%q,"expires_at":"2026-10-15T13:00:00Z"}}`, mintCapabilityToken, boxID)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	deps := boxDeps{
		dir:          t.TempDir(),
		ring:         newBoxTestRing(),
		controlPlane: &countingBoxControlPlane{answer: fakeBoxInstallation()},
		url:          srv.URL,
		mint:         mintDeps{ring: ring, boxURL: srv.URL, client: srv.Client()},
	}
	var out, errOut bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion, Context: context.Background()}
	err := newBoxCommand(deps).Execute(env, []string{"mint", "--box-id", boxID})
	if code := codeFor(*env, err); code != 0 {
		t.Fatalf("router mint exit %d stderr %s", code, errOut.String())
	}
	var doc mintDoc
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("not json: %v %s", err, out.String())
	}
	if doc.Token != mintCapabilityToken {
		t.Errorf("token %q", doc.Token)
	}
	if strings.Contains(errOut.String(), mintCapabilityToken) {
		t.Error("stderr leaked token")
	}
	if !strings.Contains(out.String(), boxID) {
		t.Errorf("stdout missing box_id %s: %s", boxID, out.String())
	}
}

func TestBoxMintHelpSaysMinutes(t *testing.T) {
	var out bytes.Buffer
	printHelp(&out, newBoxMintCommand(mintDeps{}))
	help := out.String()
	if !strings.Contains(help, "minutes") {
		t.Errorf("help missing minutes: %s", help)
	}
	if !strings.Contains(help, "cafe box enroll") {
		t.Errorf("help missing enroll command: %s", help)
	}
}

func TestBoxMintRegisteredInBoxVerbs(t *testing.T) {
	verbs := boxVerbs(boxDeps{})
	found := false
	for _, v := range verbs {
		if v.name == "mint" {
			found = true
			break
		}
	}
	if !found {
		t.Error("boxVerbs does not contain mint")
	}
}
