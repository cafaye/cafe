# HANDOFF-otel-01 — caf emits real telemetry

Branch `worker/caf-otel-01`. Three gate-green commits. Read this file before
changing anything under `internal/emitter/`.

## Done and verified

**1. The drift gate was measuring a fixture.** `internal/telemetry/telemetry.go`
declared `ServiceName = "courier"` and read `COURIER_OTEL_ENDPOINT` in the cafaye
platform CLI. The gate generated from `manifestFor` — a courier fixture — and
compared the result against files in a repository whose own `cafaye.yml` says
`name: caf`, so it was green because it was measuring courier against courier.
The generated `TestTheServiceIdentityMatchesTheManifest` that would have noticed
was emitted by the same run, so it also said courier. Fixed: the gate now reads
this repository's own manifest (`internal/gen/drift_test.go:ownManifest`),
regenerated, `caf.lock` re-pinned.

**2. caf emits one root span per invocation.** `internal/emitter`, wired from
`cli.Run`. Kind `internal`; named `caf.<command>` per core's span-name grammar;
identity on the RESOURCE only; `error.type` from core's closed vocabulary on
failure; no-op free when a switch is off.

**3. It actually reaches a collector.** Verified over real OTLP/gRPC against an
in-process receiver, with the real exporter:

```
RESOURCE service.name = caf          SPAN name="caf.doctor"      kind=INTERNAL
RESOURCE service.namespace = cafaye        status=UNSET   otel.status_code=OK
RESOURCE service.version = 0.0.0-dev   SPAN name="caf.nonsense"    kind=INTERNAL
RESOURCE telemetry.sdk.name = opentelemetry  status=ERROR   error.type=invalid_request
RESOURCE telemetry.sdk.language = go   SPAN name="caf.contract.lint" kind=INTERNAL
RESOURCE telemetry.sdk.version = 1.34.0    status=ERROR   error.type=internal_error
```

`caf gen telemetry` → `caf.gen.telemetry`. `caf lock --verify` (missing arg) →
`caf.lock` + `invalid_request`. `caf --help` → `caf.cli.run`. An inbound
traceparent's trace id and parent span id both survive the wire; a malformed one
starts a new valid trace and still exports.

**4. The generator smoke test — read-only, rendered to a temp dir.** Both
findings below.

## Two findings about the generator other packets call

**`caf gen telemetry` works against `pantry/cafaye.yml`.** Three files written to
a temp dir; `ServiceName = "pantry"`, `PANTRY_OTEL_ENDPOINT`; the committed pair
build, vet and gofmt clean in a scratch module, and 26 tests pass — including all
13 of kit's traceparent conformance tests. Nothing was written into `pantry/`.

**`caf gen telemetry` REFUSES `gateway/cafaye.yml`**, because that manifest says
`language: typescript` and the generator is Go-only:

```
caf gen telemetry: unsupported language: this manifest is written in typescript.
caf generates telemetry for go only; the other languages' OpenTelemetry templates
are in kit at templates/otel/, and emitting a second copy of them here is how two
generators of one file start disagreeing
```

Nothing was written, and the refusal is the designed behaviour — but see
"the user's decision" below, because gateway is being rewritten in Go and that
manifest cannot currently describe either implementation (its own note 7).

## The bug that would have shipped

`otlptracegrpc` defaults to TLS. core's shipped default endpoint is
`http://otel-collector:4317`, plaintext. So the handshake never completed, the
error was dropped by the error handler installed for `noOp.warnings`, and caf
exported **nothing** while appearing to work. Confirmed before the fix: `lsof`
against a real receiver on the configured port showed the listener and nothing
else, on every command.

Every test in `internal/emitter` **passes** with that wrong, because they inject
an exporter. That is why four wire tests exist. Verified they go red when the fix
is reverted: all four fail with "no OTLP request arrived within 5s".

If you touch `internal/emitter/exporter.go`, run the wire tests with the change
reverted once to confirm they still bite.

## What is deliberately not done, and why

- **Metrics and logs.** `internal/emitter` emits traces only. A CLI counter
  exists once and is aggregated by whoever runs caf; a CLI log line is the
  terminal the user is already reading.
- **Attributes on the invocation span.** core's trace allowlist is
  `additionalProperties: false` and has nothing for a command name, an argument,
  a path or an exit code — all unbounded or caller text. The command lives in the
  span NAME, where core's 15-character-per-segment bound makes it low cardinality
  by construction.
- **Child spans.** Instrumenting `dev`, `deploy` and `backup` individually is a
  per-command decision each should make when it has something worth showing.
- **`service.instance.id` on the resource.** Every invocation is a new id, so it
  counts invocations rather than processes.
- **`DEPLOYMENT_ENVIRONMENT` / `TENANT_ID` are read but undeclared.** There is no
  `.env.example` in caf and inventing one is new structure. They are documented
  in `internal/cli/emitter_test.go`'s table with core's justification each.
- **compose/dev env passthrough (DONE MEANS item 5) needs nothing here.** The
  fleet convention is the service's own `.env` (`pantry/.env.example` sets
  `PANTRY_OTEL_ENDPOINT`), and a CLI has no container. caf's variable is already
  declared in `telemetry/otel-endpoint.json` as `CAF_OTEL_ENDPOINT`, and
  `internal/emitter/contract.go` reads the name from there rather than writing it
  down. Whether `caf dev` should ALSO inject `<SVC>_OTEL_ENDPOINT` into each
  service's container is a fleet decision, not caf's — see below.

## Known rough edges, stated rather than left

1. **The flag heuristic is wrong for boolean flags with a following word.**
   `commandWords` skips the argument after a flag, so `caf dev -no-infra up`
   produces `caf.dev` when the command was `caf dev up`. Every legal name is
   still produced, and the derivation is documented as a heuristic at
   `span.go:commandWords`, but the honest fix is threading each command's real
   name through the router — which `internal/cli` does not currently expose.
2. **The flush budget is 200ms and a developer with no stack pays it on every
   command** (measured: `caf version` 2.001s at the original 2s budget, 0.2s now,
   84ns disabled). `caf dev` is the one long-running command and can afford a
   longer wait; `shutdownTimeout` is a constant with no per-command override.
3. **`coreKinds` in `internal/emitter/span_test.go` is hand-transcribed** from
   core's `span-naming.schema.json`, because the generated package does not carry
   span kinds. Five values; the row that uses it is what keeps it honest.

## The successor's first move

1. Read `internal/emitter/doc.go` and `internal/emitter/exporter.go`'s comment on
   `WithDialOption` and `WithInsecure` — both explain measured failures.
2. Run `go test ./internal/emitter/ -run 'Wire|OverTheWire' -v` and confirm four
   tests pass. Then break `if !secure` and confirm four fail. That round trip is
   the gate; anything else is a test that cannot fail.
3. Tighten `commandWords`: add each command's flag arities to the registry so the
   derivation stops guessing, and add a row per command to
   `TestTheSpanNameDerivation` for any flag that takes a value.
4. Make `shutdownTimeout` a per-command value with a 200ms default, so `caf dev`
   can wait longer for its flush.

## A decision that is genuinely the user's

**Should `caf gen telemetry` support TypeScript?** It refuses today, pointing at
kit's templates, which is the designed behaviour and I did not change it. But
`gateway` is mid-rewrite into Go (`cmd/gateway`, `internal/`) while its manifest
still says `language: typescript`, so gateway cannot generate its own telemetry
setup through caf today and neither can any future service whose first
implementation is not Go. Widening the generator's language set, or adding a
`language`/`implementation` field to core's manifest schema so one manifest can
describe two implementations, is a core/kit decision. I did not guess.

## Commands and their current output

```sh
bin/prime            # 16 packages, 759 top-level passes, 958 subtest passes,
                     # 0 failures, 8 skips
go vet ./...         # clean
gofmt -l .           # prints nothing
../core/harness/bin/gate-check .    # OK, 0 failures, 3 warnings (external
                                    # requirements the checker did not run)
```

`gate.yml` floors raised in the same commits as the tests: packages 15 → 16,
suite 727 → 753 → 759. `caf.lock` re-pinned in every commit that moved
`gate.yml`.
