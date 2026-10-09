package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Service is tier 1's answer: one service's listing entry. It is the advertised
// half of the catalog — what a service is, free and paid alike — and it
// deliberately carries no repository coordinates. Those are tier 2's, and the
// line between them is the whole of the tiered catalog's design.
type Service struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Language    string `json:"language,omitempty"`
	Visibility  string `json:"visibility,omitempty"`
}

// PantryError is one non-2xx answer from pantry. It carries the problem's own
// code and detail, because the detail is the actionable half: "no official
// cafaye service is registered under that name" is a sentence a reader can act
// on, and a bare 404 is not.
type PantryError struct {
	// Status is the HTTP status.
	Status int
	// Code is the problem's `code`: not_found, unauthorized, unavailable, …
	Code string
	// Detail is the problem's `detail`, specific to this occurrence.
	Detail string
}

func (e *PantryError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("pantry answered %d %s: %s", e.Status, e.Code, e.Detail)
	}
	return fmt.Sprintf("pantry answered %d %s", e.Status, e.Code)
}

// HTTPPantry is the real Pantry: one GET against pantry per method, tier 1
// anonymous and tier 2 behind the caller's caf token.
type HTTPPantry struct {
	// BaseURL is pantry's origin, e.g. https://pantry.cafaye.com. Empty is
	// refused rather than defaulted: a client with no endpoint is a client
	// that would GET a relative path and get a confusing answer.
	BaseURL string
	// Client is the HTTP client. Nil means one with a timeout.
	Client *http.Client
	// Timeout bounds each call. Default 30s: a human is waiting, and the
	// catalog is read-only data.
	Timeout time.Duration
}

// DefaultPantryTimeout is what a nil Timeout means.
const DefaultPantryTimeout = 30 * time.Second

func (c HTTPPantry) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultPantryTimeout
	}
	return &http.Client{Timeout: timeout}
}

// Service is tier 1: one service's listing entry, anonymous.
func (c HTTPPantry) Service(ctx context.Context, name string) (*Service, error) {
	var svc Service
	if err := c.get(ctx, nil, "/v1/services/"+name, &svc); err != nil {
		return nil, err
	}
	return &svc, nil
}

// Coordinates is tier 2: one service's repository coordinates, behind the
// caller's caf token. The token is presented as a bearer and kept for the
// length of exactly this call.
func (c HTTPPantry) Coordinates(ctx context.Context, name, token string) (*Coordinates, error) {
	var coords Coordinates
	if err := c.get(ctx, &token, "/v1/services/"+name+"/coordinates", &coords); err != nil {
		return nil, err
	}
	return &coords, nil
}

// get issues one GET and decodes a 2xx body into out. A non-2xx answer
// becomes a PantryError carrying the problem's own code and detail — pantry
// answers every refusal in the RFC 9457 problem shape (openapi/v1.yaml), and
// a client that paraphrased it would be a client whose failures cannot be
// grepped against the server's log.
func (c HTTPPantry) get(ctx context.Context, token *string, path string, out any) error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return &PantryError{Code: "no_catalog", Detail: "no pantry URL configured"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+path, nil)
	if err != nil {
		return fmt.Errorf("build the pantry request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if token != nil {
		req.Header.Set("Authorization", "Bearer "+*token)
	}
	res, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("call pantry: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read pantry's answer: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return problemError(res.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode pantry's answer: %w", err)
	}
	return nil
}

// problemError turns pantry's problem document into a PantryError. The code
// and detail are read out of the body rather than derived from the status,
// because pantry names its refusals in the body (`code: not_found`) and two
// different refusals can share a status.
//
// An unreadable or non-problem body is still a PantryError, with the status
// and the first line of the body: a proxy in front of pantry that answers
// HTML is a fact the caller is entitled to, and dropping it would report a
// 502 as "pantry said no".
func problemError(status int, raw []byte) error {
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
	return &PantryError{Status: status, Code: problem.Code, Detail: problem.Detail}
}

// firstLine bounds a body so a wall of HTML from a proxy is not pasted into a
// terminal.
func firstLine(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	const max = 240
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
