package resolve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ControlPlaneError is one non-2xx answer from box. Like PantryError, it
// carries the server's own code and message, because box's refusals are
// named and actionable (`entitlement_required`, `version_not_granted`) and a
// client that paraphrased them would be a client whose failures cannot be
// grepped against the server's log.
type ControlPlaneError struct {
	// Status is the HTTP status.
	Status int
	// Code is the error's `code`: entitlement_required, version_not_granted,
	// not_found, unauthenticated, unprocessable, unavailable,
	// capability_expired, capability_redeemed, capability_wrong_account.
	Code string
	// Message is the error's `message`.
	Message string
}

func (e *ControlPlaneError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("box answered %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("box answered %d %s", e.Status, e.Code)
}

// HTTPControlPlane is the real ControlPlane: the entitlement check, the mint of the
// artifact.fetch capability, and its redemption — the whole of tier 3 in one
// call, because the resolver's tier-3 question is one question and the
// capability is not a thing to hold, it is a thing to spend.
type HTTPControlPlane struct {
	// BaseURL is box's origin. Empty is refused rather than defaulted.
	BaseURL string
	// Client is the HTTP client. Nil means one with a timeout.
	Client *http.Client
	// Timeout bounds each call. Default 30s.
	Timeout time.Duration
}

// DefaultControlPlaneTimeout is what a nil Timeout means.
const DefaultControlPlaneTimeout = 30 * time.Second

func (c HTTPControlPlane) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultControlPlaneTimeout
	}
	return &http.Client{Timeout: timeout}
}

// ArtifactFetch is tier 3: ask "does this account hold this product at this
// range?", mint the artifact.fetch capability, and redeem it — returning the
// fetch grant, which is the proof the chain closed.
//
// The capability token crosses from the mint response to the redemption
// request and is dropped on the floor here. It never reaches the caller: the
// grant is the deliverable, and the token is a bearer credential that is spent
// by the redemption, so handing it back would be handing back something
// already used.
//
// The caller's token (the agent's cafe credential) is presented as the bearer
// for the mint and kept for the length of exactly this call. Whether it is
// sufficient — whether the caller is a member of the account — is box's
// decision, and the refusal is named: 401 unauthenticated, 404 not_found (an
// agent acting alone is not a member, and box answers that with the
// same 404 as an unknown account), 503 unavailable when identity cannot be
// reached.
func (c HTTPControlPlane) ArtifactFetch(ctx context.Context, accountID, artifact, versionRange, token string) (*FetchGrant, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, &ControlPlaneError{Code: "no_control_plane", Message: "no box URL configured"}
	}

	// The mint: POST /v1/accounts/{id}/artifact_fetches {artifact, version_range}.
	capToken, err := c.mint(ctx, accountID, artifact, versionRange, token)
	if err != nil {
		return nil, err
	}

	// The redemption: POST /v1/capabilities/redeem {token, account_id}. The
	// account_id must match the capability's own account — a capability for
	// account A cannot fetch for account B — and box refuses a mismatch
	// without spending the capability.
	grant, err := c.redeem(ctx, capToken, accountID)
	if err != nil {
		return nil, err
	}
	return grant, nil
}

// mint posts the entitlement check and returns the capability token.
func (c HTTPControlPlane) mint(ctx context.Context, accountID, artifact, versionRange, token string) (string, error) {
	body, err := json.Marshal(map[string]string{
		"artifact":      artifact,
		"version_range": versionRange,
	})
	if err != nil {
		return "", fmt.Errorf("encode the entitlement check: %w", err)
	}
	path := "/v1/accounts/" + accountID + "/artifact_fetches"
	status, raw, err := c.do(ctx, token, http.MethodPost, path, body)
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated {
		// "the entitlement check" is the `where`: a 404 here is about a caller
		// who is not a member of the account, not about a capability.
		if status == http.StatusNotFound {
			return "", &ControlPlaneError{Status: status, Code: "not_found", Message: notFoundHint()}
		}
		return "", errorFrom(status, raw, "the entitlement check")
	}
	var payload struct {
		Capability struct {
			Token string `json:"token"`
		} `json:"capability"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("decode the mint answer: %w", err)
	}
	if strings.TrimSpace(payload.Capability.Token) == "" {
		return "", &ControlPlaneError{Code: "no_capability", Message: "the entitlement check passed but minted no capability token"}
	}
	return payload.Capability.Token, nil
}

// redeem redeems the capability and returns the fetch grant. The grant is the
// capability's scope, read back from box — the proof that the capability
// was real and has been spent.
func (c HTTPControlPlane) redeem(ctx context.Context, capToken, accountID string) (*FetchGrant, error) {
	body, err := json.Marshal(map[string]string{
		"token":      capToken,
		"account_id": accountID,
	})
	if err != nil {
		return nil, fmt.Errorf("encode the redemption: %w", err)
	}
	status, raw, err := c.do(ctx, capToken, http.MethodPost, "/v1/capabilities/redeem", body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		// "the redemption" is the `where`: a 404 here is about a capability
		// that is gone, not about a caller who is not a member.
		return nil, errorFrom(status, raw, "the redemption")
	}
	var payload struct {
		Capability struct {
			Purpose              string `json:"purpose"`
			AccountID            string `json:"account_id"`
			Artifact             string `json:"artifact"`
			ArtifactVersionRange string `json:"artifact_version_range"`
			ExpiresAt            string `json:"expires_at"`
		} `json:"capability"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decode the redemption answer: %w", err)
	}
	return &FetchGrant{
		Purpose:      payload.Capability.Purpose,
		AccountID:    payload.Capability.AccountID,
		Artifact:     payload.Capability.Artifact,
		VersionRange: payload.Capability.ArtifactVersionRange,
		ExpiresAt:    payload.Capability.ExpiresAt,
	}, nil
}

// do issues one authenticated POST and returns the status, the body and any
// transport error. The token is presented as the bearer; an empty token is
// refused here rather than sent as an empty bearer, which a server answers 401
// for a token that never existed.
//
// It returns the STATUS rather than the response: the body is read to its end
// and closed here, so there is no body left to leak — a caller that received
// the response would hold a body nobody closes, which is the leak bodyclose
// exists to refuse.
func (c HTTPControlPlane) do(ctx context.Context, token, method, path string, body []byte) (int, []byte, error) {
	if strings.TrimSpace(token) == "" {
		return 0, nil, &ControlPlaneError{Code: "unauthenticated", Message: "this call needs a cafe token and none was presented"}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("build the box request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := c.client().Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("call box: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return res.StatusCode, nil, fmt.Errorf("read box's answer: %w", err)
	}
	return res.StatusCode, raw, nil
}

// errorFrom turns box's {error:{code,message}} body into a ControlPlaneError.
// The code is read out of the body because box names its refusals there,
// and the two 403s — entitlement_required and version_not_granted — are
// distinguished by the code and nothing else.
//
// `where` says WHICH of the two tier-3 calls was refused, and it is the one
// fact the two share no other way. The mint asks "may this account hold this
// product"; the redemption asks "is this capability live, and is it mine". Both
// answer 404 for a reason the reader must act on differently — a 404 from the
// mint is a 404 about a caller acting alone, which the sentence names; a 404
// from the redemption is about a capability that has already been spent. The
// two refusals otherwise produce an identical sentence, and a sentence that
// cannot say which of two calls failed sends the reader to a log that cannot
// either.
func errorFrom(status int, raw []byte, where string) error {
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &payload)
	code := payload.Error.Code
	if code == "" {
		code = http.StatusText(status)
	}
	message := payload.Error.Message
	if message == "" {
		message = firstLine(raw)
	}
	if where != "" {
		message = where + ": " + message
	}
	return &ControlPlaneError{Status: status, Code: code, Message: message}
}

// notFoundHint is what box's 404 does NOT say, and it has to be said
// somewhere because the 404 is one answer to two questions.
//
// Box looks a capability up by token and an account by id, and it answers
// the same 404 for "unknown" and "not yours" — a capability for account A is
// not a capability for account B, and saying which would be an oracle for
// whether a guessed id is real. That is right on box's side and wrong on
// this one: a 404 about a fetch is almost always an agent acting alone, and the
// remedy is a call the reader can actually make.
func notFoundHint() string {
	return "the entitlement check was refused. An agent acting alone is not a " +
		"member of the account: a human must claim the agent, and the account " +
		"that holds the entitlement must be named with --account"
}
