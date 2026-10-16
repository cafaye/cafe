package docs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

// DefaultPublishTimeout is what a nil Timeout on HTTPPublisher means.
const DefaultPublishTimeout = 30 * time.Second

// Ingest is what one publish sends: the project and version press files it
// under, the inferred kind, and the content tarball. The token is a
// parameter of the call, for the reason the pantry publisher takes it: the
// credential is presented for the length of exactly one write and held by
// nothing. A Publisher that stored the token would be a Publisher that
// outlives the run the token was presented for.
type Ingest struct {
	Project string
	Version string
	Kind    string
	Content []byte
}

// Receipt is what press answered: the ingest's id, kind, digest, size and
// state. It is a small struct because the 201 body is press's own document
// and the command needs these five fields off it — and because the receipt
// is the whole stdout contract: one JSON document, these fields, nothing
// about the credential that sent it.
type Receipt struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	State  string `json:"state"`
}

// Publisher is press's ingest surface: one multipart POST per publish,
// caller-authenticated. It is a seam so a test can drive the whole decision
// table — dry run, refusal, 401, success — against a fake that records
// calls and answers without a network, the same shape the pantry
// publisher's seam holds.
type Publisher interface {
	Publish(ctx context.Context, token string, ingest Ingest) (Receipt, error)
}

// PressError is one non-201 answer from press. It carries the problem's own
// code and detail, because the detail is the actionable half and a client
// that paraphrased it would be a client whose failures cannot be grepped
// against the server's log — the same envelope rule the pantry read path
// holds.
type PressError struct {
	// Status is the HTTP status.
	Status int
	// Code is the problem's `code`: unauthorized, conflict, unprocessable, …
	Code string
	// Detail is the problem's `detail`, specific to this occurrence.
	Detail string
}

func (e *PressError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("press answered %d %s: %s", e.Status, e.Code, e.Detail)
	}
	return fmt.Sprintf("press answered %d %s", e.Status, e.Code)
}

// HTTPPublisher is the real Publisher: one multipart POST against press's
// ingest route, caller-authenticated through the caller's cafe token. The
// fields are press's wire shape — `kind`, `content`, and `project`/`version`
// — and a client that renamed one would be a second copy of the document,
// and the second copy is the one that drifts.
type HTTPPublisher struct {
	// BaseURL is press's origin, e.g. https://press.cafaye.com. Empty is
	// refused rather than defaulted, for the reason HTTPPublisher's pantry
	// sibling refuses it: a client with no endpoint is a client that would
	// POST a relative path.
	BaseURL string
	// Client is the HTTP client. Nil means one with a timeout.
	Client *http.Client
	// Timeout bounds each call. The writes are one and the caller is waiting.
	Timeout time.Duration
}

func (c HTTPPublisher) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultPublishTimeout
	}
	return &http.Client{Timeout: timeout}
}

// Publish is POST /v1/ingest: the kind, the content tarball, and the
// project and version press files it under, as multipart form data. The
// content part carries a filename — the kind with a .tar.gz suffix —
// because a multipart part with no filename is a file nobody named, and
// press logs the name it was given.
func (c HTTPPublisher) Publish(ctx context.Context, token string, ingest Ingest) (Receipt, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return Receipt{}, &PressError{Code: "no_press", Detail: "no press URL configured"}
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("kind", ingest.Kind); err != nil {
		return Receipt{}, fmt.Errorf("build the ingest form: %w", err)
	}
	// project and version belong to a PANTRY ingest only, and press refuses
	// them on any other kind outright — a docs ingest is tenant-scoped, not
	// project-scoped, and sending a project it would ignore is a contract
	// the client invented. Kept for pantry, absent everywhere else.
	if ingest.Kind == "pantry" {
		if err := form.WriteField("project", ingest.Project); err != nil {
			return Receipt{}, fmt.Errorf("build the ingest form: %w", err)
		}
		if err := form.WriteField("version", ingest.Version); err != nil {
			return Receipt{}, fmt.Errorf("build the ingest form: %w", err)
		}
	}
	part, err := form.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": []string{fmt.Sprintf(`form-data; name="content"; filename="%s.tar.gz"`, ingest.Kind)},
		"Content-Type":        []string{"application/gzip"},
	})
	if err != nil {
		return Receipt{}, fmt.Errorf("build the ingest form: %w", err)
	}
	if _, err := part.Write(ingest.Content); err != nil {
		return Receipt{}, fmt.Errorf("build the ingest form: %w", err)
	}
	if err := form.Close(); err != nil {
		return Receipt{}, fmt.Errorf("build the ingest form: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/v1/ingest", &body)
	if err != nil {
		return Receipt{}, fmt.Errorf("build the press request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", form.FormDataContentType())
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.client().Do(req)
	if err != nil {
		return Receipt{}, fmt.Errorf("call press: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Receipt{}, fmt.Errorf("read press's answer: %w", err)
	}
	// 201 is the verdict: the ingest landed. Any other status — even
	// another 2xx — is a refusal carrying the server's own message,
	// because a receipt for bytes press did not store would be a
	// document that says "shipped" about nothing.
	if res.StatusCode != http.StatusCreated {
		return Receipt{}, problemError(res.StatusCode, raw)
	}
	var receipt Receipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return Receipt{}, fmt.Errorf("decode press's answer: %w", err)
	}
	return receipt, nil
}

// problemError turns press's problem document into a PressError. The code
// and detail are read out of the body rather than derived from the status,
// because press names its refusals in the body and two different refusals
// can share a status. An unreadable body is still a PressError, with the
// status and the first line of the body: a proxy in front of press that
// answers HTML is a fact the caller is entitled to.
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
	return &PressError{Status: status, Code: problem.Code, Detail: problem.Detail}
}

// firstLine bounds a body so a wall of HTML from a proxy is not pasted into
// a terminal.
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
