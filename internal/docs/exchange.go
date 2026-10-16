package docs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// PressAudience is the audience a token minted for press carries, and the one
// press's verifier admits: `NewJWKSVerifierFor(jwksURL, issuer, "press")` —
// press's own comment names it "the name identity's exchange pattern allows
// and the one a caller names when exchanging a session for a token to publish
// with". Identity's pattern is `^[a-z][a-z0-9-]{0,63}$`, and `press` fits it.
//
// The audience is a fact about the RECEIVER, not a preference of this
// command: a token minted for billing does not authorize a call to press, and
// the exchange mints one audience per token precisely so the receiver has
// something to check. Billing pins the same kind of constant
// (`BILLING_IDENTITY_AUDIENCE`) for the same reason.
const PressAudience = "press"

// Account is one entry of GET /v1/accounts: the accounts the caller's session
// owns or belongs to. The shape is identity's accountListItem — a navigation
// aid, carrying the minimum a client needs to render a row and open it. A
// fresh signup gets exactly one, and it is personal.
type Account struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Personal bool   `json:"personal"`
	Role     string `json:"role"`
}

// Exchange is what POST /v1/session/tokens answers: a short-lived token
// scoped to ONE account and ONE audience, and its expiry. The token is a JWS
// press verifies off identity's JWKS; it is never stored, because it is
// fifteen minutes long and minted per publish — a cached one is a stale
// credential wearing the last publish's name.
type Exchange struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Identity is identity's half of the docs publish flow: the two calls made
// before press is called. It is a seam for the same reason Publisher is — a
// test can drive the whole decision table (one account, many accounts, a
// foreign id, a dead session) against a fake that records calls and answers
// without a network.
//
// Both methods take the SESSION token as a parameter and hold it for the
// length of exactly one call: the session is user-scoped and the token is
// account-scoped, and the account is named by the caller, never picked here.
type Identity interface {
	// Accounts is GET /v1/accounts with the session: the accounts the caller
	// owns or belongs to, in identity's own order.
	Accounts(ctx context.Context, sessionToken string) ([]Account, error)
	// ExchangeToken is POST /v1/session/tokens with the session, the
	// account to act in and the receiving service's audience. The
	// membership is checked at mint time, so a session that names an
	// account it is not a member of is refused here and not downstream.
	ExchangeToken(ctx context.Context, sessionToken, accountID, audience string) (Exchange, error)
}

// IdentityError is one non-2xx answer from identity's exchange surface. It
// carries the problem's own code and detail for the reason PressError does:
// identity names its refusals in the body, and a client that paraphrased them
// would be a client whose failures cannot be grepped against the server's
// log.
type IdentityError struct {
	Status int
	Code   string
	Detail string
}

func (e *IdentityError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("identity answered %d %s: %s", e.Status, e.Code, e.Detail)
	}
	return fmt.Sprintf("identity answered %d %s", e.Status, e.Code)
}

// DefaultExchangeTimeout is what a nil Timeout on HTTPIdentity means.
const DefaultExchangeTimeout = 30 * time.Second

// HTTPIdentity is the real Identity: one GET and one POST against identity's
// account surface, session-authenticated, for the length of the call. It is
// the first half of the same client HTTPPublisher is the second half of —
// same bearer discipline, same problem envelope — because a publish that had
// two ideas about identity's wire shape would eventually have two shapes.
type HTTPIdentity struct {
	// BaseURL is identity's origin, e.g. https://identity.cafaye.com. Empty
	// is refused rather than defaulted: a client with no endpoint is a client
	// that would GET a relative path. The origin comes from CAF_IDENTITY_URL
	// or --base-url, the rule the agent verbs and `cafe login` hold.
	BaseURL string
	// Client is the HTTP client. Nil means one with a timeout.
	Client *http.Client
	// Timeout bounds each call. The calls are two and the caller is waiting.
	Timeout time.Duration
}

func (c HTTPIdentity) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultExchangeTimeout
	}
	return &http.Client{Timeout: timeout}
}

// Accounts is GET /v1/accounts.
func (c HTTPIdentity) Accounts(ctx context.Context, sessionToken string) ([]Account, error) {
	var out []Account
	if err := c.get(ctx, sessionToken, "/v1/accounts", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ExchangeToken is POST /v1/session/tokens.
func (c HTTPIdentity) ExchangeToken(ctx context.Context, sessionToken, accountID, audience string) (Exchange, error) {
	var out Exchange
	body := struct {
		AccountID string `json:"account_id"`
		Audience  string `json:"audience"`
	}{AccountID: accountID, Audience: audience}
	if err := c.post(ctx, sessionToken, "/v1/session/tokens", body, &out); err != nil {
		return Exchange{}, err
	}
	return out, nil
}

// get issues one GET and decodes a 2xx body into out. A non-2xx answer
// becomes the identity error carrying the problem's own code and detail — the
// same envelope rule the pantry and press clients hold.
func (c HTTPIdentity) get(ctx context.Context, sessionToken, path string, out any) error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return &IdentityError{Code: "no_identity", Detail: "no identity origin configured"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+path, nil)
	if err != nil {
		return fmt.Errorf("build the identity request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(sessionToken) != "" {
		req.Header.Set("Authorization", "Bearer "+sessionToken)
	}
	return c.do(req, out)
}

// post issues one POST and decodes a 2xx body into out, under the same
// envelope rule.
func (c HTTPIdentity) post(ctx context.Context, sessionToken, path string, body, out any) error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return &IdentityError{Code: "no_identity", Detail: "no identity origin configured"}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode the request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("build the identity request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(sessionToken) != "" {
		req.Header.Set("Authorization", "Bearer "+sessionToken)
	}
	return c.do(req, out)
}

// do performs one call and decodes the answer, or turns a non-2xx answer into
// an IdentityError. The problem's code and detail are read out of the body
// rather than derived from the status, because two different refusals can
// share a status — a 422 for a field and a 422 for the session are different
// sentences, and the caller is entitled to both.
func (c HTTPIdentity) do(req *http.Request, out any) error {
	res, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("call identity: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read identity's answer: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return identityProblemError(res.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode identity's answer: %w", err)
	}
	return nil
}

// identityProblemError turns identity's problem document into an IdentityError,
// in the shape PressError reads press's. The name is distinct from the press
// client's problemError because both live in this package and only one of them
// may own the bare name; a shared decoder would have to know which envelope it
// was decoding. An unreadable body is still an IdentityError with the status
// and the first line of the body: a proxy in front of identity that answers
// HTML is a fact the caller is entitled to.
func identityProblemError(status int, raw []byte) error {
	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(raw, &problem)
	if problem.Code == "" {
		problem.Code = http.StatusText(status)
	}
	if problem.Detail == "" {
		problem.Detail = firstLine(raw)
	}
	return &IdentityError{Status: status, Code: problem.Code, Detail: problem.Detail}
}

// ExchangeSession resolves the account the publish acts in and exchanges the
// session for a press-scoped token, in identity's own order: the accounts
// first (GET /v1/accounts), then the mint (POST /v1/session/tokens).
//
// The account choice is the caller's, never this function's guess:
//
//   - no flag and exactly one account — that account, which for a fresh
//     signup is the personal one;
//   - no flag and several — a refusal that RENDERS the list, because the
//     choice between two accounts is ambiguous and never guessed;
//   - a flag naming a listed id or slug — that account;
//   - a flag naming anything else — sent to identity verbatim, whose mint
//     checks membership and answers 404 for a non-member, indistinguishable
//     from non-existent by design. The refusal names the list so the next
//     invocation is one flag away.
//
// The session is presented to both calls and held by nothing; the minted
// token is returned to the caller for the length of exactly one publish.
func ExchangeSession(ctx context.Context, id Identity, session, accountFlag string) (Exchange, error) {
	accounts, err := id.Accounts(ctx, session)
	if err != nil {
		return Exchange{}, err
	}

	accountID, err := chooseAccount(accounts, accountFlag)
	if err != nil {
		return Exchange{}, err
	}

	exchange, err := id.ExchangeToken(ctx, session, accountID, PressAudience)
	if err != nil {
		var ierr *IdentityError
		if errors.As(err, &ierr) && ierr.Status == http.StatusNotFound {
			return Exchange{}, fmt.Errorf("that account is not one of yours (%s)\npass --account with one of:\n%s",
				ierr.Detail, renderAccounts(accounts))
		}
		return Exchange{}, fmt.Errorf("exchange the session for a press token: %w", err)
	}
	return exchange, nil
}

// chooseAccount is the pure half of the account decision: given the list and
// the flag, the account id to mint for, or a refusal. A slug resolves to its
// account's id because identity's mint parses `account_id` as an id and a
// slug it has never heard of is a 422 rather than a publish. A flag that
// matches nothing is returned verbatim: refusing locally would be a second
// membership check that disagrees with identity's, and the mint's 404 is the
// answer that cannot leak whether the account exists.
func chooseAccount(accounts []Account, flag string) (string, error) {
	if strings.TrimSpace(flag) == "" {
		switch len(accounts) {
		case 0:
			return "", errors.New("identity lists no accounts for this session: a signup always carries a " +
				"personal account, so this session cannot act anywhere — run `cafe login` again")
		case 1:
			return accounts[0].ID, nil
		default:
			return "", fmt.Errorf("this session belongs to %d accounts, and choosing one for you is a guess "+
				"about where these pages ship — pass --account with one of:\n%s",
				len(accounts), renderAccounts(accounts))
		}
	}
	for _, a := range accounts {
		if flag == a.ID || flag == a.Slug {
			return a.ID, nil
		}
	}
	return flag, nil
}

// renderAccounts is the list a refusal hands back: one line per account, the
// slug and id a --account flag accepts, and the name and role a human
// recognizes. It is the remedy half of every refusal above — a refusal that
// says "choose" without saying WHAT to choose from is a refusal that costs
// the reader a second command to answer.
func renderAccounts(accounts []Account) string {
	var b strings.Builder
	for _, a := range accounts {
		who := a.Name
		if who == "" {
			who = a.Slug
		}
		_, _ = fmt.Fprintf(&b, "  --account %s  %s (%s, %s)\n", a.Slug, a.ID, who, a.Role)
	}
	return b.String()
}
