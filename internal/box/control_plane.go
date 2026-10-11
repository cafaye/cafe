package box

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Installation is what box answers a redemption with: the facts about
// the installation a capability bought. Every one of them is a non-secret —
// they are identifiers and a state word — which is why they can be written to
// box.json and printed anywhere.
type Installation struct {
	BoxID     string `json:"box_id"`
	AccountID string `json:"account_id"`
	Status    string `json:"status"`
	Phase     string `json:"current_phase"`
}

// ControlPlane is the one call this package makes over the network, and it is
// an interface so a test can count it.
//
// Counting it is the whole point. The capability is single-use: a second
// redemption of the same token is refused by box, atomically, and a
// client that redeems twice has not merely wasted a call — it has proven
// nothing about idempotence, because the failure it expects is a failure the
// server was always going to produce. What has to be asserted is the NUMBER of
// redemptions across a retried sequence, and that is only assertable if the
// call is a seam with a counter behind it.
type ControlPlane interface {
	// Redeem presents a capability and the box's public key. The public key is
	// sent so the enrollment binds a key box can later verify a signature
	// from; the private half never leaves the box.
	Redeem(ctx context.Context, token string, publicKey ed25519.PublicKey) (*Installation, error)
}

// The three honest refusals box can answer, mapped to sentinels so a
// caller can branch on the cause rather than on a status code. Each is a
// different instruction to the operator, which is why they are not one error:
// an expired token means mint a new one; an already-redeemed one means a
// previous attempt got it, and where that attempt stopped is the question.
var (
	// ErrCapabilityUnknown is a token box has never heard of: a typo, a
	// truncated paste, or a token from another environment.
	ErrCapabilityUnknown = errors.New("box does not know this capability")
	// ErrCapabilityExpired is a token whose minutes ran out. The TTL is
	// box's and it is minutes by design (box's Capability::TTL_
	// MINUTES), so an expired token is a mint that was never used in time and
	// needs a fresh one.
	ErrCapabilityExpired = errors.New("capability has expired")
	// ErrCapabilityRedeemed is a token that has already bought its enrollment.
	// This is the one that makes idempotence a design question rather than a
	// retry: there is no way to spend it again, so the only workable answer is
	// to have recorded everything the first time.
	ErrCapabilityRedeemed = errors.New("capability has already been redeemed")
)

// HTTPControlPlane is the real ControlPlane: one POST against box's private
// API, outbound from the box, with the capability as the credential in the
// body and nothing else in it.
type HTTPControlPlane struct {
	// BaseURL is box's origin, e.g. https://box.internal:4000.
	// Empty is refused rather than defaulted: a client with no endpoint is a
	// client that would POST to a relative path and get a confusing answer.
	BaseURL string
	// Client is the HTTP client. Nil means one with a timeout, because an
	// enroll that hangs forever on a box nobody is watching is the failure
	// this repository's env-up child described in general terms.
	Client *http.Client
	// Timeout bounds the one call. Default 30s: the box is enrolling, a human
	// is waiting, and the capability lives minutes.
	Timeout time.Duration
}

// DefaultControlPlaneTimeout is what a nil Timeout means. It is generous because
// the thing being waited on is a human's first enrollment, and stingy enough
// that a hung connection still ends inside the capability's TTL.
const DefaultControlPlaneTimeout = 30 * time.Second

type redeemRequest struct {
	Token     string `json:"token"`
	PublicKey string `json:"public_key"`
}

// controlPlaneResponse is what comes back. The installation is the whole answer;
// there is no token in it, and no secret in any field.
type controlPlaneResponse struct {
	Installation struct {
		ID         string `json:"id"`
		BoxID      string `json:"box_id"`
		AccountID  string `json:"account_id"`
		Status     string `json:"status"`
		Phase      string `json:"current_phase"`
		EnrolledAt string `json:"enrolled_at"`
	} `json:"installation"`
}

// Redeem POSTs the capability and maps box's four answers onto the three
// sentinels and success.
//
// The mapping is explicit rather than a switch on strings because box's
// error vocabulary is not this package's, and a fifth code appearing in a
// future release should surface as an unrecognised answer rather than being
// folded into the nearest familiar one. `get` is built by hand rather than
// unmarshalled lazily so a body that is not JSON at all — a proxy's HTML error
// page, a captive portal — is reported as what it is.
func (c HTTPControlPlane) Redeem(ctx context.Context, token string, publicKey ed25519.PublicKey) (*Installation, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, fmt.Errorf("redeem capability: no box URL configured; set it with `cafe box enroll --token … --box <url>` or in %s", identityFile)
	}
	body, err := json.Marshal(redeemRequest{Token: token, PublicKey: publicKeyText(publicKey)})
	if err != nil {
		return nil, fmt.Errorf("encode redemption: %w", err)
	}
	url := strings.TrimSuffix(c.BaseURL, "/") + "/v1/capabilities/redeem"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build redemption request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	res, err := clientFor(c).Do(req)
	if err != nil {
		return nil, fmt.Errorf("redeem capability against %s: %w", url, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read redemption response: %w", err)
	}

	switch res.StatusCode {
	case http.StatusOK:
		var parsed controlPlaneResponse
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("decode redemption response from %s: %w", url, err)
		}
		if parsed.Installation.BoxID == "" {
			return nil, fmt.Errorf("redemption response from %s named no box", url)
		}
		return &Installation{
			BoxID:     parsed.Installation.BoxID,
			AccountID: parsed.Installation.AccountID,
			Status:    parsed.Installation.Status,
			Phase:     parsed.Installation.Phase,
		}, nil
	case http.StatusNotFound:
		return nil, ErrCapabilityUnknown
	case http.StatusGone:
		return nil, ErrCapabilityExpired
	case http.StatusConflict:
		return nil, ErrCapabilityRedeemed
	default:
		return nil, fmt.Errorf("redeem capability against %s: unexpected HTTP %d: %s", url, res.StatusCode, firstLine(raw))
	}
}

func clientFor(c HTTPControlPlane) *http.Client {
	if c.Client != nil {
		return c.Client
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultControlPlaneTimeout
	}
	return &http.Client{Timeout: timeout}
}

// firstLine is a bounded body snippet for an error message. It is bounded
// because an error page can be large and a log is not the place for it.
func firstLine(raw []byte) string {
	const max = 200
	text := strings.TrimSpace(string(raw))
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = strings.TrimSpace(text[:i])
	}
	if len(text) > max {
		text = text[:max] + "…"
	}
	if text == "" {
		text = "(empty body)"
	}
	return text
}
