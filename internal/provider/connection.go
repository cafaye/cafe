package provider

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

// Providers is the vocabulary box accepts (EmailProviderConnection::
// PROVIDERS). It is repeated here because the client has to refuse a provider
// box would refuse, and refusing here — with a sentence naming the two —
// is a better error than a 422 from a server a person is staring at.
var Providers = []string{"smtp", "cloudflare"}

// Connection is one email provider a box's account uses, as box records
// it. Every field but SealedCredential is a non-secret: they say WHICH provider
// and WHICH account, and those facts are what ` + "`cafe provider status`" + `
// prints. SealedCredential is the envelope — ciphertext box cannot open
// and the box can — and it is the one field no status report ever echoes.
type Connection struct {
	Provider         string            `json:"provider"`
	Account          string            `json:"account"`
	BoxID            string            `json:"box_id"`
	Config           map[string]string `json:"config"`
	SealedCredential string            `json:"sealed_credential"`
}

// ConnectRequest is what ` + "`cafe provider connect`" + ` seals and posts. The
// credential is in plaintext here and ONLY here: it is sealed by Connect and
// the envelope is what reaches box. The request is a parameter rather
// than three arguments because the day a fourth field lands, a caller that
// passes three positional values breaks at compile time in the caller and not
// at runtime in the server.
type ConnectRequest struct {
	Provider   string
	Account    string
	BoxID      string
	Config     map[string]string
	Credential string
	// BoxPublic is the box's ed25519 public key, read from box.json. It is
	// what the credential is sealed TO: the envelope is openable by the box
	// and by nobody else, including box.
	BoxPublic []byte
}

// ControlPlane is the one call this package makes over the network, and it is an
// interface so a test can drive a connect without a network — the same seam
// internal/box uses for the capability redemption, for the same reason: a
// test that needs a server to prove the client is a test that cannot run on a
// bare CI runner.
//
// Connect and Status are separate methods because they are separate questions
// with separate failure modes, and a caller that only reports status must not
// have to hold a credential to do it.
type ControlPlane interface {
	// Connect posts a sealed connection and returns what box recorded.
	Connect(ctx context.Context, conn Connection) (*Connection, error)
	// Status lists the connections box holds for one enrolled machine.
	Status(ctx context.Context, boxID string) ([]Connection, error)
}

// The two honest refusals a connect can meet before it reaches the network.
var (
	// ErrUnknownProvider is a provider box does not accept. It is a
	// usage error in the CLI's sense: the person typed a word cafe does not
	// have, and the answer is the list of the ones it does.
	ErrUnknownProvider = fmt.Errorf("unknown provider (the providers are %s)", strings.Join(Providers, ", "))
	// ErrEmptyCredential is a connect whose credential is blank. Refusing it
	// here rather than sealing an empty envelope is the honest answer: an
	// empty credential is a mistake, not a connection.
	ErrEmptyCredential = errors.New("the credential is empty")
)

// Connect seals the credential to the box's public key and posts the envelope
// to box. It is the whole of the client half of boundary 6 in one call:
// the credential goes into the envelope and the envelope goes to box,
// and nothing in between ever sees the plaintext but this function.
//
// The returned Connection carries box's record of what was stored — the
// provider, the account, the box, the config and the envelope — and never the
// credential. A caller that logs the return value logs no secret.
func Connect(ctx context.Context, c ControlPlane, req ConnectRequest) (*Connection, error) {
	if !knownProvider(req.Provider) {
		return nil, ErrUnknownProvider
	}
	if strings.TrimSpace(req.Credential) == "" {
		return nil, ErrEmptyCredential
	}
	if strings.TrimSpace(req.BoxID) == "" {
		return nil, errors.New("no box: run `cafe box enroll --token …` first")
	}

	envelope, err := Seal(req.Credential, req.BoxPublic)
	if err != nil {
		return nil, fmt.Errorf("seal the credential to the box: %w", err)
	}

	conn := Connection{
		Provider:         req.Provider,
		Account:          req.Account,
		BoxID:            req.BoxID,
		Config:           req.Config,
		SealedCredential: envelope,
	}
	// The credential is dropped on the floor here, deliberately and finally.
	// Nothing after this line holds it.
	return c.Connect(ctx, conn)
}

// Status reports the connections box holds for a machine. It carries no
// credential and opens none: the envelopes come back as opaque JSON and are
// passed through untouched, because a status report that opened the envelopes
// would be a status report that prints secrets.
func Status(ctx context.Context, c ControlPlane, boxID string) ([]Connection, error) {
	return c.Status(ctx, boxID)
}

// knownProvider reports whether provider is one box accepts.
func knownProvider(provider string) bool {
	for _, p := range Providers {
		if p == provider {
			return true
		}
	}
	return false
}

// DefaultTimeout bounds the one call. It is short because the thing being
// waited on is a person's first connection, and a hung connect is worse than
// a refused one — the credential is sitting in the envelope unsealed either
// way, and a timeout means the box is not left half-connected.
const DefaultTimeout = 30 * time.Second

// HTTPControlPlane is the real ControlPlane: one POST and one GET against
// box's private API, outbound from the box.
//
// The routes are the ones the model's name implies — POST and GET
// /v1/email-provider-connections — built against the documented shape because
// box merged the model, the migration and the sealing primitive (W3-P2)
// but no route yet. When the route lands, this client needs no change.
type HTTPControlPlane struct {
	// BaseURL is box's origin, e.g. https://box.internal:4000.
	// Empty is refused rather than defaulted, for the reason internal/box's
	// HTTPControlPlane refuses it: a client with no endpoint is a client that
	// would POST to a relative path.
	BaseURL string
	// Client is the HTTP client. Nil means one with a timeout.
	Client *http.Client
	// Timeout bounds each call. Default 30s.
	Timeout time.Duration
}

// connectPath is the resource the connection lives at. It is named once so
// the POST and the GET cannot drift onto different paths.
const connectPath = "/v1/email-provider-connections"

func (c HTTPControlPlane) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &http.Client{Timeout: timeout}
}

// Connect POSTs the sealed connection. The body is the Connection document
// verbatim: provider, account, box_id, config and the envelope. There is no
// credential in it — the envelope is the only thing the credential became.
func (c HTTPControlPlane) Connect(ctx context.Context, conn Connection) (*Connection, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, errors.New("no box URL configured; set it with `cafe provider connect --box <url>` or in the box identity")
	}
	body, err := json.Marshal(conn)
	if err != nil {
		return nil, fmt.Errorf("encode the connection: %w", err)
	}
	url := strings.TrimSuffix(c.BaseURL, "/") + connectPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build the connect request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	res, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect against %s: %w", url, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read the connect response: %w", err)
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("connect against %s: unexpected HTTP %d: %s", url, res.StatusCode, firstLine(raw))
	}

	var recorded Connection
	if err := json.Unmarshal(raw, &recorded); err != nil {
		return nil, fmt.Errorf("decode the connect response from %s: %w", url, err)
	}
	if recorded.SealedCredential == "" {
		return nil, fmt.Errorf("the connect response from %s named no envelope; nothing was stored", url)
	}
	return &recorded, nil
}

// Status GETs the connections box holds for one machine. A 404 is reported
// as no connections rather than an error, because "box has never heard
// of this machine" and "this machine has no providers" are the same answer to
// the question `cafe provider status` asks.
func (c HTTPControlPlane) Status(ctx context.Context, boxID string) ([]Connection, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, errors.New("no box URL configured; set it with `cafe provider status --box <url>` or in the box identity")
	}
	url := strings.TrimSuffix(c.BaseURL, "/") + connectPath + "?box_id=" + boxID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build the status request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	res, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("status against %s: %w", url, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read the status response: %w", err)
	}
	switch res.StatusCode {
	case http.StatusOK:
		var conns []Connection
		if err := json.Unmarshal(raw, &conns); err != nil {
			// A body that is not a list is a server answering a different
			// question, and folding it into an empty list would report
			// "not connected" for a box that is.
			return nil, fmt.Errorf("decode the status response from %s: %w", url, err)
		}
		return conns, nil
	case http.StatusNotFound:
		return nil, nil
	default:
		return nil, fmt.Errorf("status against %s: unexpected HTTP %d: %s", url, res.StatusCode, firstLine(raw))
	}
}

// firstLine is a bounded body snippet for an error message, for the reason
// internal/box's firstLine is: an error page can be large and a log is not the
// place for it.
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
