// Package resolve is the consumer of the tiered catalog (W4-P1) and the
// artifact.fetch capability (W4-P2): it answers, for one project's cafaye.yml
// `dependencies`, "does this resolve, and what is the named failure if not?"
//
// THE THREE TIERS, AND WHAT EACH ONE IS FOR. A dependency is resolved through
// the tiers in order, and the order is the semantics:
//
//	Tier 1  the listing	pantry  GET /v1/projects/{name}	         anonymous
//	Tier 2  the metadata	pantry  GET /v1/projects/{name}/coordinates  cafe token
//	Tier 3  the artifact	box POST …/artifact_fetches → redeem	entitlement
//
// Tier 1 says the project exists and what it is (name, description, language,
// `repository.visibility`) — advertising, free and paid alike (D5). Tier 2
// says where its code lives (repository coordinates), to any caller
// presenting a live cafe token. Tier 3 says whether the caller may fetch the
// artifact at all: the entitlement check mints a single-use, minutes-lived
// artifact.fetch capability bound to exactly (account, artifact,
// version-range), and the redemption is the fetch grant. A free service never
// reaches tier 3 — it is not a product, and box's check refuses it with
// 422.
//
// TWO VOCABULARIES, and the difference is deliberate rather than sloppy. Tier
// 1 serves pantry's `Project` — W3 renamed pantry's catalog schema from
// `Service` — and tier 3 asks box about its PRODUCTS (`kit`,
// `platform`, `identity`, `courier`), which are still service names because
// box's contract did not move. A sentence about the catalog says
// "project"; a sentence about what is paid says "product"; the runtime word
// "service" survives in cafe's own vocabulary.
//
// THE ONE INVARIANT, stated once because everything else here is downstream of
// it: **the caller's credential is presented, never stored.** The resolver
// takes the caller's cafe token as a parameter, hands it to the pantry and
// control-plane clients for the length of one call, and keeps no reference to
// it. It is never printed, never logged, never put in a document. The token is
// the agent's, read from the keyring by the command that calls this package;
// this package is a courier, exactly as internal/agent is.
//
// FAILURES ARE NAMED, NEVER SILENT. Every non-2xx answer from either service
// becomes a Failure carrying the server's own code and detail —
// `entitlement_required` with the product and where to start one,
// `version_not_granted` with both ranges spelled out, `not_found`,
// `unauthorized`, `unavailable`. A resolver that reported "resolution failed"
// for a 403 would be sending the reader to a server log that says nothing
// useful, which is the habit the 403 agent_unclaimed hint established.
//
// required: false DEGRADES. The manifest schema's `required` field marks a
// soft dependency ("the service runs without it, degraded"), and the resolver
// honors it: a failed optional dependency is a warning, not a block. The
// decision is the command's — the resolver reports every failure with its
// dependency's Required flag, and the caller decides warn-vs-block. Box
// never sees `required`; it is cafe's side of the contract.
//
// WHAT THIS PACKAGE DOES NOT DO, and deliberately: it does not fetch artifact
// bytes. There is no artifact-serving endpoint yet — tier 3's fetch grant is
// the proof of entitlement, and the registry that honors it is a later
// packet's row. It does not decide which services are paid products; it
// carries box's PRODUCTS list (see Products) for the reason
// internal/provider carries box's PROVIDERS: the client has to refuse
// what the server would refuse, and there is no endpoint to ask.
package resolve
