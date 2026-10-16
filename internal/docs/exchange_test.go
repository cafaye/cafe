package docs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The identity client over a real loopback listener: the two calls a publish
// makes before press are wired exactly as the wire expects — GET /v1/accounts
// and POST /v1/session/tokens, both session-authenticated, the mint body
// carrying the named account and the receiver's audience.
func TestHTTPIdentityListsAccountsAndExchanges(t *testing.T) {
	var gotAuth string
	var gotPath, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		if r.URL.Path == "/v1/accounts" {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `[{"id":"acct_1","name":"Noel","slug":"noel","personal":true,"role":"owner"}]`)
			return
		}
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"token":"the-jwt","expires_at":"2026-10-16T12:15:00Z"}`)
	}))
	defer server.Close()

	client := HTTPIdentity{BaseURL: server.URL}
	accounts, err := client.Accounts(context.Background(), "the-session")
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].ID != "acct_1" || !accounts[0].Personal {
		t.Fatalf("accounts = %+v, want one personal account acct_1", accounts)
	}
	if gotAuth != "Bearer the-session" {
		t.Errorf("Authorization = %q, want the session presented as a bearer", gotAuth)
	}
	if gotPath != "/v1/accounts" {
		t.Errorf("path = %q, want /v1/accounts", gotPath)
	}

	exchange, err := client.ExchangeToken(context.Background(), "the-session", "acct_1", PressAudience)
	if err != nil {
		t.Fatalf("ExchangeToken: %v", err)
	}
	if exchange.Token != "the-jwt" {
		t.Errorf("token = %q, want the minted JWT", exchange.Token)
	}
	if exchange.ExpiresAt.IsZero() {
		t.Error("expires_at was not decoded")
	}
	if !strings.Contains(gotBody, `"account_id":"acct_1"`) || !strings.Contains(gotBody, `"audience":"press"`) {
		t.Errorf("mint body = %s, want the account id and the audience", gotBody)
	}
	if !strings.Contains(gotPath, "/v1/session/tokens") {
		t.Errorf("path = %q, want /v1/session/tokens", gotPath)
	}
}

// A non-2xx answer is an IdentityError carrying the server's own code and
// detail — never a paraphrase, because an agent reading the failure must be
// able to grep identity's log with it. An unreadable body is still an
// IdentityError, because a proxy that answers HTML is a fact the caller is
// entitled to.
func TestHTTPIdentityNamesRefusalsWithTheServersWords(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		code   string
		detail string
	}{
		{name: "unauthorized", status: 401, body: `{"code":"unauthorized","detail":"the session is not valid"}`, code: "unauthorized", detail: "the session is not valid"},
		{name: "not found", status: 404, body: `{"code":"not_found","detail":"no such account"}`, code: "not_found", detail: "no such account"},
		{name: "a proxy's HTML is still an IdentityError", status: 502, body: `<html>bad gateway</html>`, code: "Bad Gateway", detail: "<html>bad gateway</html>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = fmt.Fprint(w, tt.body)
			}))
			defer server.Close()

			_, err := HTTPIdentity{BaseURL: server.URL}.Accounts(context.Background(), "session")
			if err == nil {
				t.Fatal("a refusal was read as a list")
			}
			var identityErr *IdentityError
			if !errors.As(err, &identityErr) {
				t.Fatalf("err = %T (%v), want an *IdentityError", err, err)
			}
			if identityErr.Status != tt.status {
				t.Errorf("status = %d, want %d", identityErr.Status, tt.status)
			}
			if !strings.Contains(identityErr.Code, tt.code) || !strings.Contains(identityErr.Detail, tt.detail) {
				t.Errorf("the error does not carry the server's code and detail: %v", identityErr)
			}
		})
	}
}

// A client with no endpoint is refused rather than defaulted: it would GET a
// relative path and get a confusing answer — the same rule the press and
// pantry clients hold.
func TestHTTPIdentityRefusesNoEndpoint(t *testing.T) {
	if _, err := (HTTPIdentity{}).Accounts(context.Background(), "session"); err == nil {
		t.Error("an identity client with no endpoint sent without complaint")
	}
	if _, err := (HTTPIdentity{}).ExchangeToken(context.Background(), "session", "acct", PressAudience); err == nil {
		t.Error("an identity client with no endpoint minted without complaint")
	}
}

// The account decision, as a table: one account is used, several are a
// refusal that RENDERS the list, a flag naming a listed id or slug resolves
// to that id, and a flag naming anything else is passed through verbatim so
// identity's own mint is the authority on membership.
func TestChooseAccount(t *testing.T) {
	many := []Account{
		{ID: "acct_aaaa", Name: "Noel", Slug: "noel", Personal: true, Role: "owner"},
		{ID: "acct_bbbb", Name: "Acme", Slug: "acme", Role: "admin"},
	}
	tests := []struct {
		name     string
		accounts []Account
		flag     string
		want     string
		wantErr  string
	}{
		{name: "one account is used", accounts: docsOneAccountForTest(), want: "acct_0001"},
		{name: "several with no flag is a refusal", accounts: many, wantErr: "--account"},
		{name: "a slug resolves to its id", accounts: many, flag: "acme", want: "acct_bbbb"},
		{name: "an id is accepted", accounts: many, flag: "acct_aaaa", want: "acct_aaaa"},
		{name: "a foreign flag is passed through verbatim", accounts: many, flag: "acct_9999", want: "acct_9999"},
		{name: "no accounts is a refusal", accounts: nil, wantErr: "cafe login"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chooseAccount(tt.accounts, tt.flag)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("chooseAccount = %q, want a refusal naming %q", got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("the refusal does not name %q:\n%s", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("chooseAccount: %v", err)
			}
			if got != tt.want {
				t.Errorf("chooseAccount = %q, want %q", got, tt.want)
			}
		})
	}
}

// docsOneAccountForTest is the single-account case for the table above.
func docsOneAccountForTest() []Account {
	return []Account{{ID: "acct_0001", Name: "Noel", Slug: "noel", Personal: true, Role: "owner"}}
}

// recordingIdentity is the smallest Identity a direct ExchangeSession test
// needs: it answers the accounts list and the mint, and records what it was
// asked.
type recordingIdentity struct {
	accounts    []Account
	accountsErr error
	exchangeErr error
	gotAccount  string
	gotAudience string
}

func (r *recordingIdentity) Accounts(context.Context, string) ([]Account, error) {
	if r.accountsErr != nil {
		return nil, r.accountsErr
	}
	return r.accounts, nil
}

func (r *recordingIdentity) ExchangeToken(_ context.Context, _, accountID, audience string) (Exchange, error) {
	r.gotAccount = accountID
	r.gotAudience = audience
	if r.exchangeErr != nil {
		return Exchange{}, r.exchangeErr
	}
	return Exchange{Token: "minted"}, nil
}

// ExchangeSession walks the accounts list then the mint, in order, with the
// account resolved by the caller rather than guessed, and never converts a
// failed exchange into a token.
func TestExchangeSessionWalksAccountsThenTheMint(t *testing.T) {
	id := &recordingIdentity{accounts: docsOneAccountForTest()}
	exchanged, err := ExchangeSession(context.Background(), id, "session", "")
	if err != nil {
		t.Fatalf("ExchangeSession: %v", err)
	}
	if exchanged.Token != "minted" {
		t.Errorf("token = %q, want the minted one", exchanged.Token)
	}
	if id.gotAccount != "acct_0001" {
		t.Errorf("minted for %q, want the session's one account", id.gotAccount)
	}
	if id.gotAudience != PressAudience {
		t.Errorf("audience = %q, want %q", id.gotAudience, PressAudience)
	}
}

// A failed mint is surfaced, and its 404 is turned into a refusal that names
// the accounts the session DOES own — a render of the list, not a paraphrase.
func TestExchangeSessionRendersTheListOnANonMemberRefusal(t *testing.T) {
	id := &recordingIdentity{
		accounts:    docsOneAccountForTest(),
		exchangeErr: &IdentityError{Status: 404, Code: "not_found", Detail: "no such account"},
	}
	_, err := ExchangeSession(context.Background(), id, "session", "acct_foreign")
	if err == nil {
		t.Fatal("a refused membership minted a token")
	}
	if !strings.Contains(err.Error(), "that account is not one of yours") {
		t.Errorf("the refusal does not say the account is not the session's:\n%s", err)
	}
	if !strings.Contains(err.Error(), "noel") {
		t.Errorf("the refusal does not render the accounts the session owns:\n%s", err)
	}
}

// A non-404 exchange failure is surfaced with its own words and no list: a
// dead session is not a membership question.
func TestExchangeSessionSurfacesANonMembershipFailure(t *testing.T) {
	id := &recordingIdentity{
		accounts:    docsOneAccountForTest(),
		exchangeErr: &IdentityError{Status: 401, Code: "unauthorized", Detail: "the session is no longer valid"},
	}
	_, err := ExchangeSession(context.Background(), id, "session", "")
	if err == nil {
		t.Fatal("a dead session minted a token")
	}
	if !strings.Contains(err.Error(), "the session is no longer valid") {
		t.Errorf("the failure does not carry identity's own detail:\n%s", err)
	}
}
