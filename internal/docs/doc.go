// Package docs is `cafe docs publish`: the one command an agent (or a
// human) runs to ship documentation to press, the documentation service.
//
// It is the client half of press's ingest API (`POST /v1/ingest`), and its
// whole shape follows `cafe publish` (the pantry one): non-interactive by
// default since the 2026-10-11 ruling, the proposal printed before anything
// is sent, `--dry-run` sending nothing, and the receipt as one JSON document
// on stdout.
//
// It imports the standard library plus this repository's own
// `internal/contract`, for the ONE type it must not have an opinion about —
// `contract.Manifest`, whose `repository.visibility` field the kind is
// inferred from. Everything else is stdlib: the tarball is archive/tar, the
// form is mime/multipart, the two clients are net/http and encoding/json, so a
// publisher that needed a third-party dependency would be a publisher that
// could not be rebuilt from a release tarball with nothing but the toolchain.
//
// # The session is exchanged before press is called
//
// press verifies a short-lived, account-scoped JWT off identity's JWKS and
// refuses any audience but its own (`press`). The token cafe holds is
// identity's SESSION credential, which is user-scoped and is not a JWS — press
// refuses it by design. So the flow is: resolve the account
// (GET /v1/accounts), mint a press-scoped token for it
// (POST /v1/session/tokens), and present THAT to press. The mint is per-publish
// and never stored.
//
// # What pantry turned out to verify, and why it is deliberately not here
//
// The same question was asked of the pantry publish path (cafe publish), and
// the answer is that pantry is a DIFFERENT contract, not a second instance of
// this one: pantry's write surface (`cafaye/pantry`, internal/publisher/
// principal.go and internal/httpapi/write.go) verifies a GATEWAY principal
// assertion crossing in the `x-cafaye-principal` header — RS256 against
// gateway's public key, with `aud` equal to `PANTRY_AUDIENCE`, which is
// pantry's own ORIGIN (an http(s) URL), not an audience NAME. Identity's
// exchange mints an audience matching `^[a-z][a-z0-9-]{0,63}$` and signs with
// identity's JWKS key, so a token from it could never satisfy pantry's
// verifier, and pantry's own `mountPublisherPath` is nil-tolerant (unset is
// the shipped state: the three write routes are simply not mounted). There is
// therefore no identity exchange to share with `cafe publish`, and inventing
// one would be a second credential contract nobody serves. This note is the
// evidence, so the next packet starts from it rather than re-reading pantry.
package docs
