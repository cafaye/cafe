# Test fixtures

Manifests copied verbatim from
[anywaye/core](https://github.com/anywaye/core) at commit
`3340e0f3171e09750f43e9f2974a3efb00f0ed82` (spec v0.2), so `cafe`'s validator
and core's own `tests/test_specs.py` answer the same question about the same
bytes.

| Fixture | Core source | Why it is here |
| --- | --- | --- |
| `valid/identity.anywaye.yml` | `examples/valid/go-api.anywaye.yml` | API + 12 events, every one prefixed `identity.`. The full shape. |
| `valid/worker.anywaye.yml` | `examples/valid/worker.anywaye.yml` | Publishes events, serves no HTTP. The case that "exposes means api" gets wrong. |
| `valid/worker-only.anywaye.yml` | `examples/valid/worker-only.anywaye.yml` | No `exposes` at all, consumes two events. The library/worker shape. |
| `valid/spec.anywaye.yml` | `anywaye.yml` | Core's own manifest: `language: spec`, no contract surface, and the reserved name `core`. |
| `invalid/manifest.anywaye.invalid.yml` | `examples/invalid/manifest.anywaye.invalid.yml` | Eight schema violations at once, documented one by one in core's `examples/invalid/README.md`. Note `user.Created` is now wrong twice over: upper case *and* two segments. |

The fixtures under `invalid/` that core does **not** ship
(`self-consume`, `foreign-long-event`, `empty-surface`, `broken`) are written
for cafe. Each one is schema-valid — except `broken`, which is not even YAML —
so the only thing that can reject them is a cafe rule. A fixture that the JSON
Schema already rejects would pass a linter with the cross-field rules deleted.

`foreign-long-event` is the one to keep an eye on across core bumps. Under
v0.1 it existed because the schema accepted a two-segment type; under v0.2 it
exists for a different reason — both of its event types are three segments and
lowercase, so the pattern is satisfied and only the comparison against `name`
finds it. A future core bump that makes the prefix a schema constraint would
make this fixture worthless, and `go test` would pass while the rule silently
did nothing.

## Refreshing

Re-copy the five core files after a core bump and run `go test ./...`. The
provenance and the refresh procedure for the vendored schema are in
[`../schemas/README.md`](../schemas/README.md).
