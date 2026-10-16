// Package docs is `cafe docs publish`: the one command an agent (or a
// human) runs to ship documentation to press, the documentation service.
//
// It is the client half of press's ingest API (`POST /v1/ingest`), and its
// whole shape follows `cafe publish` (the pantry one): non-interactive by
// default since the 2026-10-11 ruling, the proposal printed before anything
// is sent, `--dry-run` sending nothing, and the receipt as one JSON document
// on stdout.
//
// It imports nothing beyond the standard library, for the reason
// internal/publish holds to its own narrow surface: the tarball is
// archive/tar and the form is mime/multipart, so a publisher that needed a
// dependency would be a publisher that could not be rebuilt from a release
// tarball with nothing but the toolchain.
package docs
