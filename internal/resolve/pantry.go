package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Project is tier 1's answer: one project's listing entry. It is the
// advertised half of the catalog — what a project is, free and paid alike —
// and it deliberately carries no repository coordinates. Those are tier 2's,
// and the line between them is the whole of the tiered catalog's design.
//
// The shape is pantry's wire shape, and pantry publishes exactly FOUR
// advertising facts: name, description, language and `repository.visibility`.
// `visibility` is NESTED under `repository`, and it is the field that lets a
// caller tell a public project from a private one before it asks for
// coordinates it may not get — it stays on tier 1 because whether a repository
// is public is itself an advertising fact. A client that spelled it as a
// top-level `visibility` would be reading a key pantry never sends, which is
// the hole a hand-written struct gives you when the document is the contract.
//
// W3 renamed pantry's catalog schema from `Service` to `Project`, and its
// routes from the `services` family to the `projects` family; this type is
// the client half of that rename. The fields are otherwise UNCHANGED — `kind`,
// `core`, `basePath`, `exposes`, `consumes`, `dependencies` and `owner` are in
// pantry's schema and are not decoded here, because caf asks tier 1 for one
// thing only (does this name exist, and is it public) and a struct that
// carried the whole schema would be a second copy of it.
type Project struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Language    string     `json:"language,omitempty"`
	Repository  PublicRepo `json:"repository"`
}

// PublicRepo is what tier 1 says about a project's repository — one field and
// not three. `url` and `defaultBranch` are the coordinates, they are tier 2's,
// and they are not in this object for anyone else.
type PublicRepo struct {
	Visibility string `json:"visibility,omitempty"`
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
	// TraceID is the problem's `trace_id`, which is also the `X-Trace-Id`
	// response header pantry sends on EVERY answer. It is carried because it
	// is the one field that makes this failure greppable against pantry's own
	// log — the difference between "a named failure" and "a named failure
	// somebody can act on". Dropping it is the failure the code-and-detail
	// decoding already avoided, one field further on.
	TraceID string
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

// Project is tier 1: one project's listing entry, anonymous.
func (c HTTPPantry) Project(ctx context.Context, name string) (*Project, error) {
	var proj Project
	if err := c.get(ctx, nil, "/v1/projects/"+name, &proj); err != nil {
		return nil, err
	}
	return &proj, nil
}

// Coordinates is tier 2: one project's repository coordinates, behind the
// caller's caf token. The token is presented as a bearer and kept for the
// length of exactly this call.
func (c HTTPPantry) Coordinates(ctx context.Context, name, token string) (*Coordinates, error) {
	var coords Coordinates
	if err := c.get(ctx, &token, "/v1/projects/"+name+"/coordinates", &coords); err != nil {
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
		return ProblemError(res.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode pantry's answer: %w", err)
	}
	return nil
}

// ProblemError turns pantry's problem document into a PantryError. The code
// and detail are read out of the body rather than derived from the status,
// because pantry names its refusals in the body (`code: not_found`) and two
// different refusals can share a status.
//
// The trace id is read out of the body too, and it is dropped from the ERROR
// STRING rather than carried in it: an error string with a trace id in it is a
// string that gets pasted into a ticket where the id is the only part nobody
// wanted. It is on the field, where a caller that wants it asks for it, and
// the field is what a log line is built from.
//
// An unreadable or non-problem body is still a PantryError, with the status
// and the first line of the body: a proxy in front of pantry that answers
// HTML is a fact the caller is entitled to, and dropping it would report a
// 502 as "pantry said no".
//
// It is exported because the problem envelope is PANTRY'S, not the read path's:
// the publish client (internal/publish) posts to the write routes and decodes
// the same refusals, and a second copy of the decoder is a second thing to
// drift. The read path's own non-2xx answers go through it unchanged.
func ProblemError(status int, raw []byte) error {
	var problem struct {
		Code    string `json:"code"`
		Detail  string `json:"detail"`
		TraceID string `json:"trace_id"`
	}
	_ = json.Unmarshal(raw, &problem)
	if problem.Code == "" {
		problem.Code = http.StatusText(status)
	}
	if problem.Detail == "" {
		problem.Detail = firstLine(raw)
	}
	return &PantryError{Status: status, Code: problem.Code, Detail: problem.Detail, TraceID: problem.TraceID}
}

// firstLine bounds a body so a wall of HTML from a proxy is not pasted into a
// terminal. The bound cuts on a rune boundary rather than a byte boundary,
// because a detail carrying a non-ASCII character — a service description, an
// em dash — would otherwise be truncated in the middle of its UTF-8 encoding
// and produce invalid bytes on a stderr line.
func firstLine(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	const max = 240
	if len(s) > max {
		cut := s[:max]
		for len(cut) > 0 && !utf8.ValidString(cut) {
			// Walk back to the last rune boundary at or before the bound.
			cut = cut[:len(cut)-1]
		}
		return cut + "..."
	}
	return s
}
