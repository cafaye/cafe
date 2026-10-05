# HANDOFF-otel-02 — caf's dev registry carries the endpoint, and the precedence is true

Branch `worker/caf-otlreg-01`, four commits on top of `worker/caf-otel-01`. This
file supersedes the parts of `HANDOFF-otel-01.md` that are now wrong; the rest of
that file still stands.

## Done and verified

Packet: two defects named by wave-1 workers.

### 1. `caf dev` rendered no telemetry endpoint

`internal/dev` mentioned neither `<SERVICE>_OTEL_ENDPOINT>` nor 4317/4318, so
every service in a rendered compose document was left to whatever default its own
binary carried. pantry's `HANDOFF-otel-01.md` names why that is not closeable
inside pantry: it holds no `docker-compose.yml` by design, so the compose half of
the fleet's endpoint convention is only writable in the generator.

`wire()` now hands every cafaye service the variable, with core's documented
collector address read from `contract.Endpoint.Default`. Rendered, for a
kebab-case project depending on pantry:

```
  my-service:
    environment:
      DATABASE_URL: postgres://my-service:my-service@postgres:5432/my-service
      MYSERVICE_OTEL_ENDPOINT: http://otel-collector:4317
      REDIS_URL: redis://redis:6379/0

  pantry:
    environment:
      PANTRY_DATABASE_URL: postgres://my-service:my-service@postgres:5432/my-service
      PANTRY_OTEL_ENDPOINT: http://otel-collector:4317
      REDIS_URL: redis://redis:6379/0
```

### 2. The precedence held for the address and not for the scheme

`CAF_OTEL_ENDPOINT` is documented as winning over
`OTEL_EXPORTER_OTLP_ENDPOINT`. courier's worker measured the standard variable
winning. Measured here, it was half true.

An OTel exporter resolves its configuration as: built-in defaults, then
`ApplyGRPCEnvConfigs` — which reads `OTEL_EXPORTER_OTLP_ENDPOINT` from the
**process** environment — then the caller's options. `newGRPCExporter` passed the
address explicitly, so its option was applied last and the address was right. It
appended a scheme option only when it wanted plaintext, so with an `https` cafaye
value nothing overrode whatever the standard variable said:

```
CAF_OTEL_ENDPOINT=https://127.0.0.1:PORT
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:PORT
-> 50 52 49 20 2a 20 48 54 54 50 2f 32 2e   "PRI * HTTP/2.0", plaintext
```

**The converse direction was already correct.** That is what made this invisible:
an asymmetry rather than a rule, and a table test that only exercised the address
would have stayed green. After the fix, `16 03 01` — a TLS record.

`otlptracegrpc` has no `WithSecure`, and its config resolves `GRPCCredentials`
before `Insecure`, so the secure case now names
`credentials.NewTLS(nil)` — the host root CA pool, which is what the secure
default branch built anyway. What changes is which input decides.

## Verification

```
$ go mod download && go test ./internal/dev/... ./internal/telemetry/... \
      ./internal/emitter/... ./internal/gen/... -count=1
ok  	github.com/cafaye/caf/internal/dev	0.590s
ok  	github.com/cafaye/caf/internal/telemetry	0.666s
ok  	github.com/cafaye/caf/internal/emitter	5.211s
ok  	github.com/cafaye/caf/internal/gen	0.922s

$ bin/prime
caf: 16 packages, 771 top-level passes, 965 subtest passes, 0 failures, 8 skips

$ gofmt -l .          # prints nothing
$ go vet ./...        # clean
$ ../core/harness/bin/gate-check .
OK ...: 0 failure(s), 3 warning(s) — warnings do not move the exit code
```

The three warnings are external requirements the checker did not run: the Go
toolchain, the module proxy and a container runtime. Unchanged by this packet.

`gate.yml`: suite floor 759 → 765 → 771. `caf.lock` re-pinned in every commit
that moved `gate.yml`.

## Mutation checks run

| Mutation | Caught by |
|---|---|
| `newGRPCExporter` back to appending an option only for plaintext | the scheme row of `TestTheEndpointPrecedenceHoldsOnTheWire`, failing with the h2 preface printed |
| `telemetryEnv` removed from `wire` | all six cases in `internal/dev/telemetry_test.go` |
| the emitter precedence table driven through `Options.Lookup` instead of `t.Setenv` | it would pass against a broken exporter — which is how the first version of it passed, and the reason the test says so in its own comment |

## What I deliberately did not do, and why

- **Did not add `depends_on: otel-collector`, and did not add the collector to
  `internal/dev`'s `infra` table.** The collector needs kit's
  `templates/compose/otel-collector.yml`, which this repository does not own, and
  an edge to a container nothing starts is a stack that never comes up. The
  endpoint variable naming an address nothing serves is the documented state —
  core's schema means "not required" by exactly that.
- **Did not edit core.** `core/schemas/telemetry/otel-endpoint.schema.json` does
  **not** contradict the documented precedence: it never mentions
  `OTEL_EXPORTER_OTLP_ENDPOINT` at all, and it describes the endpoint variable as
  "the ONLY contract". Nothing to report there, and nothing to fix.
- **Did not edit kit's snippets, which have the same shape caf just fixed.**
  `kit/templates/otel/go/otelhttp.go.snippet` resolves the same precedence and
  passes the result to `otlptrace.New(ctx, otlptrace.WithEndpointURL(endpoint))`.
  `WithEndpointURL` takes the scheme, so that snippet is **not** affected by the
  leak found here — which is worth saying, because the difference between the two
  exporters (`WithEndpoint` takes a host:port and has no scheme argument;
  `WithEndpointURL` takes the whole URL) is the whole reason one service loses the
  scheme and another does not. A report, not a fix.
- **Did not touch `internal/contracts`' vendored schema pins.** Nothing in core
  moved.
- **Did not run the live tier.** `bin/prime --live` needs a container runtime and
  takes minutes; the declared gate skips all seven and says so in a line
  `gate.yml` matches.
- **Did not add the other three from `HANDOFF-otel-01.md`'s successor list.**
  `commandWords` flag arity and a per-command `shutdownTimeout` are a separate
  packet about `internal/cli`'s router, and nothing in this packet's diff reaches
  them.

## Open, and not mine

- **`caf gen telemetry` still refuses `gateway/cafaye.yml`** because that manifest
  says `language: typescript` and the generator is Go-only. Unchanged from
  `HANDOFF-otel-01.md`, and still a core/kit decision rather than a caf one.
- **The port in the generated default is `4317`** (gRPC), from core's
  `examples/valid/telemetry/otel-endpoint.json`. kit's Go snippet and courier both
  default to `4318`. Both are plaintext on the compose network and both work; it is
  a fleet inconsistency in the *documented default value*, owned by core, and
  nothing here depends on which is right.

## The successor's first move

1. Read `internal/emitter/precedence_test.go`'s header. It explains why the table
   is driven through `t.Setenv` rather than an injected lookup, and the reason is
   the defect this packet fixed: an injected lookup never reaches the SDK.
2. `go test ./internal/emitter/ -run TestTheEndpointPrecedenceHoldsOnTheWire -v`,
   then make `newGRPCExporter` append its scheme option only for plaintext again
   and confirm the scheme row fails. That round trip is the gate.
3. If you touch `internal/dev`'s `wire`, re-run
   `go test ./internal/dev/ -run 'Telemetry|Endpoint'` — six cases, and the
   count in `internal/dev/plan_test.go`'s `TestAServiceThatDeclaresNothingStill-
   GetsTheConventionalName` moves with it.
