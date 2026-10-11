package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// # Transport authentication, and the scopes that gate the tools
//
// This file is the packet `internal/mcp/doc.go` called "a later packet" for
// two releases. Until it landed the HTTP transport was loopback-only and
// *unauthenticated* — which was defensible only because everything it served
// ran containers on the machine it ran on, so the listener being reachable was
// the whole attack. That reasoning does not survive the remote tools: a
// caller on another continent reading this box's health is the point, and an
// unauthenticated read is not a lesser thing than an unauthenticated command.
//
// Two mechanisms, and they are deliberately not the same one:
//
//   - The TRANSPORT authenticates. Over HTTP every request must present a
//     bearer token, because a transport is the only place a caller exists
//     before any tool runs. An unauthenticated caller is refused with a 401
//     and a body that names what to do.
//
//   - The TOOLS are gated on the caller's scopes. A read-only agent may read
//     health, logs, rollout state and status; it may not propose or apply.
//     The gate is per tool call and reads the principal the transport
//     resolved, so it cannot be talked out of it by a handler.
//
// # The credential is the agent token, and invariant 2 applies to it
//
// `cafe agent token create` (W1-P4) mints a scoped, rotatable token and the
// claim screen (W2-P1) picks its scopes. `Authority` is the seam that turns
// one into a Principal; the real implementation forwards the bearer to
// identity's introspection, which is the same thing identity does for every
// other service in the fleet. Nothing in this package knows what that costs.
//
// The token's TTL is enforced here rather than trusted: a Principal with no
// expiry is refused, because invariant 2 says everything handed to an agent is
// short-TTL and a verifier that reported an undated principal would be a
// verifier handing out a credential that never lapses. `AllowMissingExpiration`
// is not passed, so the SDK's own check refuses one too — two independent
// guards over the one property.

// The scope vocabulary. These are the strings a claim screen grants and a
// token carries, and they are written down here rather than transcribed into
// the handlers because a scope a tool needs is a decision about the tool.
// Identity owns the set; cafe's table says which of them answers which tool.
const (
	// ScopeRead is health, logs, rollout state and status: the facts an agent
	// reads to decide what to do. It is the default scope a claimed agent is
	// granted, and the one the landing page's read-only promise rests on.
	ScopeRead = "read"
	// ScopePropose is proposing a release: it asks a human to approve something
	// and mints nothing. Invariant 5 still applies — only a human's click
	// approves — so this scope is the right to *ask*.
	ScopePropose = "deploy.propose"
	// ScopeApply is applying one, which is the scope that changes a box.
	ScopeApply = "deploy.apply"
)

// RequiredScope is the one place a tool's scope is written down, keyed by tool
// name. The gate reads it; a test walks the served table and reads it. A tool
// missing from this table needs no scope, which is what the six local tools do
// — they are the machine's own tools, and the person who could run them on a
// terminal can run them over stdio.
//
// A table keyed by name rather than a field on Tool because a name is stable
// across a type parameter and a field is not: Tool[In, Out] is generic and a
// map keyed on the *tools* is one place a reader can see the whole policy.
var RequiredScope = map[string]string{
	"caf_health":  ScopeRead,
	"caf_logs":    ScopeRead,
	"caf_rollout": ScopeRead,
	"caf_status":  ScopeRead,
	"caf_propose": ScopePropose,
	"caf_apply":   ScopeApply,
}

// Principal is who a caller is and what it may do. Every field is a non-secret
// and it is derivable from a bearer without holding one, which is what lets it
// travel in a request's context and into a log line.
type Principal struct {
	// Subject is who it is: the agent's username, or a box's ref. It is what an
	// audit event names, so a refusal is attributable to somebody.
	Subject string
	// Scopes are the capabilities the token carries. Empty is a principal that
	// may do nothing scoped, which is a valid thing for a freshly registered
	// agent to be.
	Scopes []string
	// Expires is when this principal stops being one. A zero value is refused:
	// see the note at the head of this file about invariant 2.
	Expires time.Time
}

// Allows reports whether this principal holds one scope. It is a method rather
// than a lookup so a caller cannot ask the question and get a slice back.
func (p *Principal) Allows(scope string) bool {
	if p == nil || scope == "" {
		return false
	}
	return slices.Contains(p.Scopes, scope)
}

// expired reports whether the principal's TTL has passed, tolerating the clock
// skew a caller passes. An expired principal is not a principal.
func (p *Principal) expired(skew time.Duration) bool {
	if p == nil || p.Expires.IsZero() {
		return true
	}
	return p.Expires.Add(skew).Before(time.Now())
}

// ErrNoAuthority is a scoped tool served by a server with nothing to
// authenticate against. It is fail-closed on purpose: a tool whose scope is
// enforced only when an authority happens to be configured is a tool whose
// scope is not enforced.
var ErrNoAuthority = errors.New("no authority to authenticate against")

// ErrDenied is the sentinel every refusal in this file wraps, so a caller can
// ask "was this a scope problem" the way it asks "was this a status problem"
// of internal/agent's StatusError.
var ErrDenied = errors.New("denied")

// Authority resolves a raw bearer token into a Principal.
//
// It is an interface rather than a function because the real one is a network
// call to identity and the tests answer without a network, and because a
// caller that holds it cannot read a token out of it — the token goes in and a
// non-secret comes out, which is the same one-way door
// internal/boxd's World keeps.
type Authority interface {
	Authenticate(ctx context.Context, token string) (*Principal, error)
}

// tokenInfo is what the SDK needs and what this package works with, in one
// shape. Principal's fields are carried in Extra because TokenInfo has no
// place for a subject: the SDK's own TokenInfo is about *OAuth*, and this
// packet is not OAuth.
func (p *Principal) tokenInfo() *auth.TokenInfo {
	return &auth.TokenInfo{
		UserID:     p.Subject,
		Scopes:     slices.Clone(p.Scopes),
		Expiration: p.Expires,
		Extra: map[string]any{
			"subject": p.Subject,
			"scopes":  slices.Clone(p.Scopes),
			"expires": p.Expires.Format(time.RFC3339),
		},
	}
}

// bearerVerifier adapts an Authority to the SDK's TokenVerifier signature.
//
// The error message is load-bearing rather than incidental: `RequireBearerToken`
// writes the verifier's error into the 401's body verbatim, so this is where an
// unauthenticated remote caller is told what to do. A 401 whose body is
// `invalid token` sends the reader to a server log; one that names the command
// mints a token ends the interaction in one round trip.
func bearerVerifier(a Authority, skew time.Duration) auth.TokenVerifier {
	return func(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		if strings.TrimSpace(token) == "" {
			return nil, fmt.Errorf("%w: no bearer token; pass `Authorization: Bearer <token>` "+
				"with a token `cafe agent token create` minted", auth.ErrInvalidToken)
		}
		principal, err := a.Authenticate(ctx, token)
		if err != nil {
			// Wrapped rather than passed through, and for two reasons. The
			// remedy belongs here so that EVERY authority's refusal is
			// actionable — a verifier that returned `invalid token` verbatim
			// would be one whose message a reader could act on only if that
			// authority happened to have written a good one. And the authority's
			// prose is redacted before it is used, because an error that echoed
			// a credential would put it in a response body, and this repository
			// already holds that a token never appears in one.
			return nil, fmt.Errorf("%w: the token was not accepted (%s). Pass `Authorization: Bearer <token>` "+
				"with a token `cafe agent token create` minted, or `cafe agent token rotate` if this one was "+
				"rotated or revoked", auth.ErrInvalidToken, redactToken(err.Error(), token))
		}
		if principal == nil {
			return nil, fmt.Errorf("%w: the authority accepted the token and named no principal, "+
				"so nothing may be served", auth.ErrInvalidToken)
		}
		if principal.Subject == "" {
			return nil, fmt.Errorf("%w: the principal carries no subject, so a refusal could not be "+
				"attributed to anyone", auth.ErrInvalidToken)
		}
		// Fail closed on a missing or elapsed TTL. The SDK checks this too
		// (AllowMissingExpiration is not passed), and this package checks it
		// again because "two processes disagree about one property" is cheaper
		// than "one of them forgot".
		if principal.Expires.IsZero() {
			return nil, fmt.Errorf("%w: the principal expires at no time; invariant 2 requires every "+
				"credential handed to an agent to be short-TTL, so this one is refused", auth.ErrInvalidToken)
		}
		if principal.expired(skew) {
			return nil, fmt.Errorf("%w: the token expired at %s; run `cafe agent token rotate`",
				auth.ErrInvalidToken, principal.Expires.Format(time.RFC3339))
		}
		return principal.tokenInfo(), nil
	}
}

// authenticate wraps a handler in the bearer check. A server with no Authority
// is returned unauthenticated, and every scoped tool it serves is then refused
// by the gate — an unauthenticated server is not a server that lets scoped
// tools through, it is one that lets nothing scoped through.
//
// TWO LAYERS, and the split is not arbitrary. This one owns the caller who
// presented NOTHING — the SDK's own middleware answers `no bearer token`
// without ever calling the verifier, and that sentence sends an agent to a
// server log that says nothing. The SDK's layer owns the caller who presented
// something UNVERIFIABLE, and there the body is the verifier's error message,
// which is where the remedy belongs. Both are 401; both name the command.
func authenticate(a Authority, next http.Handler, skew time.Duration) http.Handler {
	if a == nil {
		return next
	}
	sdkLayer := auth.RequireBearerToken(bearerVerifier(a, skew), &auth.RequireBearerTokenOptions{
		// Every scope decision is cafe's, per tool. A blanket list here would
		// refuse a read-only agent before it could call the one tool it is
		// allowed to call, and the refusal would name the wrong scope.
		Scopes: nil,
		// ResourceMetadataURL is OAuth's discovery document. cafe's caller
		// already holds a token and already knows how to mint one, so the
		// header would advertise a flow nobody is going to run.
		ResourceMetadataURL: "",
	})(next)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get("Authorization")) == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="cafe mcp"`)
			w.WriteHeader(http.StatusUnauthorized)
			// The body is the actionable half. An agent that asked for a
			// remote tool with no credential already has the command that
			// fixes it, so naming it ends the interaction in one round trip
			// rather than after a trip to a log.
			_, _ = w.Write([]byte("cafe mcp: no bearer token. Pass `Authorization: Bearer <token>` " +
				"with a token `cafe agent token create` minted; an unauthenticated caller is refused " +
				"because this transport answers reads about a box that is running something.\n"))
			return
		}
		sdkLayer.ServeHTTP(w, r)
	})
}

// principalFor is who one call is. The request's own token info wins, always:
// over HTTP it is the caller the transport authenticated, and falling back to
// the standing principal would let any authenticated caller act as whoever
// started the process. Over stdio there is no request-level bearer — the
// caller is the machine's own session — so the standing principal is used.
func principalFor(req *sdk.CallToolRequest, standing *Principal) *auth.TokenInfo {
	if req != nil && req.Extra != nil && req.Extra.TokenInfo != nil {
		return req.Extra.TokenInfo
	}
	if standing == nil {
		return nil
	}
	return standing.tokenInfo()
}

// principalOf reads a Principal back out of a resolved *auth.TokenInfo. The
// subject and the scopes are carried in Extra because TokenInfo has nowhere
// else to put a cafe principal subject, and the round trip is asserted rather
// than assumed — the one place a nil map would be read as "no scopes".
func principalOf(info *auth.TokenInfo) *Principal {
	if info == nil {
		return nil
	}
	p := &Principal{
		Subject: info.UserID,
		Scopes:  slices.Clone(info.Scopes),
		Expires: info.Expiration,
	}
	if p.Subject == "" {
		if s, ok := info.Extra["subject"].(string); ok {
			p.Subject = s
		}
	}
	return p
}

// checkScope is the per-call gate. It answers nil when the call may proceed
// and an actionable error when it may not, and the error is a tool error by the
// first rule in this package's doc: a refusal is something an agent reads and
// recovers from, not something its host restarts the server over.
//
// It takes the Server rather than the two values it would read off it, because
// the question "is there an authority" is the question a caller would otherwise
// have to be handed, and a gate that took a nil authority and a nil standing
// principal separately is a gate with two ways to say the same thing.
//
// The message names the tool, the scope, who the caller is, and the command
// that mints a token carrying it — the same "403 agent_unclaimed is an
// instruction, not a status code" habit internal/agent's StatusError.Hint
// established.
func (s *Server) checkScope(toolName string, req *sdk.CallToolRequest) error {
	scope, gated := RequiredScope[toolName]
	if !gated {
		return nil
	}

	info := principalFor(req, s.standing)
	switch {
	case s.authority == nil && info == nil:
		return fmt.Errorf("%w: cafe mcp %s needs the %q scope and this server was started with no "+
			"authority to authenticate a caller with; point it at an identity origin (CAF_IDENTITY_URL) "+
			"so the transport can verify a bearer", ErrDenied, toolName, scope)
	case info == nil:
		return fmt.Errorf("%w: cafe mcp %s needs the %q scope and this caller presented no token; "+
			"run `cafe agent token create` on the agent that holds the credential", ErrDenied, toolName, scope)
	}

	principal := principalOf(info)
	if principal.expired(s.skew) {
		return fmt.Errorf("%w: cafe mcp %s: %s's token expired at %s; run `cafe agent token rotate`",
			ErrDenied, toolName, principal.Subject, principal.Expires.Format(time.RFC3339))
	}
	if !principal.Allows(scope) {
		return fmt.Errorf("%w: cafe mcp %s needs the %q scope and %s holds %s; a human grants it on the "+
			"claim screen (`cafe agent register` prints the URL), or run `cafe agent token create` for a "+
			"token carrying it",
			ErrDenied, toolName, scope, principal.Subject, scopeWords(principal.Scopes))
	}
	return nil
}

// scopeWords renders a principal's scopes for a message. "none" rather than
// `[]` because the thing a reader needs to know is that the token carries
// nothing, not a slice's JSON spelling.
func scopeWords(scopes []string) string {
	if len(scopes) == 0 {
		return "no scopes"
	}
	return strings.Join(slices.Sorted(slices.Values(scopes)), ", ")
}

// redactToken removes a credential's own bytes from a message.
//
// This is invariant 1 applied to an error path: an authority that echoed the
// token it was handed would put it in the body of a 401, and a 401 body is a
// thing a log collects and an agent reads. Redacting is cheap and the failure it
// prevents is exactly the one this repository's whole security story is about.
func redactToken(message, token string) string {
	if len(token) < 4 {
		// Too short to be a credential and too common a substring to be safely
		// replaced: a two-character token would redact half of every sentence.
		return message
	}
	return strings.ReplaceAll(message, token, "[redacted]")
}
