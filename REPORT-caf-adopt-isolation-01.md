# REPORT — cafe-adopt-isolation-01

**Branch** `worker/cafe-adopt-isolation-01` · **Worktree** `wt-m39-cafe-adopt-isolation-01`
**Recipe** `reports/cafe-adopt-isolation-01/mutations.sh`
**Run** `reports/cafe-adopt-isolation-01/RUN.txt` (exit 0)
**Tripwire proof** `reports/cafe-adopt-isolation-01/FALSIFY-RED.txt` (exit 1)

---

## The finding, first

**cafe holds no rows. The cross-tenant harness is UNDEFINED against it — not
unfinished.** There is no account-scoped query layer in cafe to measure, so the
packet's premise (`cafe | Go | holds rows | yes`) is not supported by the tree.

There are no six breakages, because there is no statement to remove a predicate
from. That is reported as a result with a measurement behind it, which is the
same discipline `kit/reports/tenant-adapter-ts-01/subject-audit.sh` used when it
withdrew the TypeScript adapter rather than shipping a nominal one.

**cafe is not broken.** Nothing here is a cafe defect and nothing was fixed in caf.
The absence of a query layer is cafe's architecture, stated correctly by cafe's own
manifest: *"cafe is a binary, not a service: it exposes no HTTP surface and
publishes no events."*

---

## 1. The anchors the packet asked for, and why there are none

The brief asked for anchors "with the file and line for each", the way
`kit/reports/tenant-adapter-go-01/mutations.sh` has six read out of identity's
`internal/accounts/store.go`. **There are none to read, and the recipe says so in
its own header rather than substituting plausible-looking text.**

A guessed anchor is how a recipe reports "stayed green" for a breakage it never
planted. A borrowed anchor — identity's `WHERE account_id = $1`, applied to a
service with no table — would be the same defect wearing cafe's name, and it would
have produced a green line resting on a file that does not exist in this
repository.

What exists instead is **a classification of all 14 `account_id` occurrences in
cafe**, every one printed with its path and line number, and a `PREDICATE` count
of **0**.

### The 14 occurrences, and what each one is

| # | file:line | what it is | bucket |
|---|---|---|---|
| 1 | `internal/contract/telemetry_test.go:251` | prohibited-measurement-attribute list | TELEMETRY |
| 2 | `internal/contract/telemetry_test.go:263` | same, iterated | TELEMETRY |
| 3 | `internal/contract/telemetry_test.go:295` | same, required set | TELEMETRY |
| 4 | `internal/contract/schemas/telemetry/traces.schema.json:203` | schema property | TELEMETRY |
| 5 | `internal/contract/schemas/telemetry/metrics.schema.json:5` | schema `description` prose | TELEMETRY |
| 6 | `internal/contract/schemas/telemetry/metrics.schema.json:34` | schema `description` prose | TELEMETRY |
| 7 | `internal/contract/schemas/telemetry/metrics.schema.json:100` | `{ "required": ["account_id"] }` | TELEMETRY |
| 8 | `internal/contract/schemas/telemetry/metrics.schema.json:129` | schema property | TELEMETRY |
| 9 | `internal/contract/schemas/telemetry/logs.schema.json:102` | schema property | TELEMETRY |
| 10 | `internal/gen/render.go:1114` | generator iterating resource-attribute names | OTHER |
| 11 | `internal/telemetry/telemetry_test.go:134` | allowlist assertion | TELEMETRY |
| 12 | `internal/telemetry/telemetry.go:574` | generated allowlist entry | TELEMETRY |
| 13 | `internal/telemetry/telemetry.go:607` | generated allowlist entry | TELEMETRY |
| 14 | `telemetry/otel-endpoint.json:36` | resource-attribute list | TELEMETRY |

**13 TELEMETRY, 1 OTHER, 0 PREDICATE.** Every occurrence is the *name of an
OpenTelemetry attribute*. Not one filters on an account.

### The packet's "2 test files" note was this, read the wrong way

The brief records that cafe "currently carries **2** test files that mention
`account_id` — a start, not the five-arm suite." Those two files are rows 1–3 and
row 11 above: `internal/contract/telemetry_test.go` and
`internal/telemetry/telemetry_test.go`. They are telemetry allowlist tests. They
are not a partial suite; **they are the complete extent of the string's presence
in Go test code**, and they point away from a query layer rather than toward one.

---

## 2. The measurement, and the control that makes it one

A grep that finds no `WHERE account_id` proves nothing on its own — a probe that
recognises nothing looks identical to a probe that is right about a tree
containing nothing. So `mutations.sh` runs three arms, and the third is the one
that carries the finding.

### ARM 1 — static, over cafe's own tree

```
IMPORTS a db driver (non-test) : 0
DECLARES a driver in go.mod     : -
MIGRATES (.sql own schema)      : 0
goose migrations/ directory    : absent
tenancy.yml                    : absent
account_id PREDICATE lines     : 0
account_id TELEMETRY lines     : 13
=> HOLDS ROWS                  : no
```

Corroborating, read by hand before the recipe was written:
`go.mod`'s four direct dependencies are `goccy/go-yaml`,
`modelcontextprotocol/go-sdk`, `santhosh-tekuri/jsonschema/v6` and
`yosida95/uritemplate/v3` — a YAML parser, the MCP SDK, a schema validator and a
URI matcher. `grep -rn '"database/sql"' --include='*.go'` returns **0**.
`find . -name '*.sql'` returns **0**. `ls migrations/` does not exist.

Across the whole fleet, only **`courier`** and **`identity`** carry a
`tenancy.yml`, and both say `accountScoped: true`. Those are exactly the two
services the harness has proven. cafe declares nothing, because cafe has nothing to
declare.

### ARM 2 — the empirical receipt

Not a prediction. kit's Go adapter, really invoked against cafe:

```
harness: up — kit-tenant-iso-97198 on host port 15898 (postgres:17)
harness: preparing a throwaway copy of wt-m39-cafe-adopt-isolation-01
  identity's own migrations failed; tail of the log:
  2026/10/03 18:40:52 goose run: migrations directory does not exist
cross_tenant_harness: the service's own migrations did not apply to a real
database. Exiting 2 — a harness that seeds into a schema nobody migrated proves
nothing.
harness exit status: 2
```

`adapter_prepare` **succeeded** — cafe has a `go.mod` and `go build ./...` passes
over the whole module. `adapter_migrate` then failed at `goose -dir migrations`.
That is step two of five, and the first that needs something the subject holds.

Both halves matter. cafe **is** a Go service the adapter can build; what it lacks
is not a toolchain and not a language adapter. It is a database.

Full log: `reports/cafe-adopt-isolation-01/arm2-adapter-vs-caf.log`.

### ARM 3 — the control, and it changed the recipe twice

The same `measure` function, unchanged, pointed at `identity`:

```
IMPORTS a db driver (non-test) : 18
DECLARES a driver in go.mod     : github.com/jackc/pgx
MIGRATES (.sql own schema)      : 17 (+ internal/tenancy/isolation.sql)
goose migrations/ directory    : present
tenancy.yml                    : present
account_id PREDICATE lines     : 69
=> HOLDS ROWS                  : yes
```

Same logic, opposite answer, 69 predicates against 0. So the difference is a
property of the two trees and not of the probe.

---

## 3. Two defects this control caught in my own work

Reported because the recipe's value is that it could be wrong in a way I could
see.

**(a) The classifier reported cafe with 2 predicates; the truth is 0.** Both were
English prose in `metrics.schema.json` — one matched only because the word
"update" appears in a sentence about telemetry cards. The control's original
assertion was "identity has at least one predicate", which a classifier that
buckets *everything* as a predicate also satisfies. The assertion was too weak for
the classifier it was policing. The rule is now **structural** — a predicate lives
in a `.go`/`.ex`/`.rb`/`.py`/`.ts`/`.rs`/`.sql` file and carries a statement, so
a `.json` schema description fails the first condition by construction — and the
assertion is "cafe's predicate count is exactly 0", which matches the rule rather
than a proxy for it.

**(b) `SOURCE_EXT` was tested against the whole `grep -rn` line**, which is
`path:lineno:content`, so `.go$` matched **never** — on every line, including
identity's own `WHERE account_id = $1`. The classifier recognised nothing
anywhere. ARM 1's subject happens to contain nothing, so this would have shipped
**green having measured nothing at all**. Only ARM 3 could see it.

A third, smaller one: ARM 2's narration contained raw backticks, so bash executed
`adapter_migrate` as a command and swallowed 60 lines of `goose` usage as its own
evidence. Now escaped, and the receipt is extracted by matching the meaningful
lines rather than by `tail`.

**(c) The probe measured its own report.** Caught by the final reproducibility
check, and the worst of the three because it was silent. Writing
`REPORT-cafe-adopt-isolation-01.md` at the repository root added ten mentions of
`account_id` in a document *about this measurement*, and cafe's tally moved from
13 TELEMETRY / 1 OTHER to 14 / 11. Every number still looked plausible and nothing
went red. The first draft excluded `reports/cafe-adopt-isolation-01/` and forgot
the report sitting beside it.

That is kit's own rule about the staleness reporter arriving from the other
direction -- "the reporter never commits its output, because a committed report is
the generated output this rule forbids and the kind of file that rots." A probe
that reads its own report has a floor that moves every time somebody writes the
finding down, which is to say exactly when it is most used. Both artifacts are now
excluded, and the reproducibility check below is what notices if either is ever
dropped from that list.

---

## 4. The tallies, separately from the passes

```
controls green    : 1
breakages planted : 0
observed RED      : 0
```

Per the standing rule, these are separate numbers because "6 passed" could be six
reds or six controls.

**There are zero breakages and that is the result, not a gap in the run.** The
packet asked for one breakage per way scoping can be dropped. cafe has no scoping
to drop. The control is the one that matters, and it is why the zero is a
measurement rather than an absence of measurement.

## 5. The tripwire, exercised

A withdrawal probe that has never been watched red is a probe that might not
work. `FALSIFY-RED.txt` is that claim exercised: the identical script run against
a throwaway subject built from identity's database surface (`go.mod`,
`migrations/`, `tenancy.yml`, `internal/accounts/store.go`).

**Result: exit 1, `RED ARM 1`**, naming exactly what appeared —

```
IMPORTS a db driver (non-test) : 1
DECLARES a driver in go.mod     : github.com/jackc/pgx
MIGRATES (.sql own schema)      : 17
goose migrations/ directory    : present
tenancy.yml                    : present
account_id PREDICATE lines     : 14
=> HOLDS ROWS                  : yes
```

Both directions are now measured: green on cafe (the finding), red on a subject
holding rows (the tripwire).

**Reproducibility:** the committed recipe was run three times after the last edit.
Two consecutive runs are byte-identical apart from the `goose` log timestamp, and
the committed `RUN.txt` matches a fresh run. cafe reads 0 PREDICATE / 13 TELEMETRY /
1 OTHER and identity reads 69 / 14 / 520 on every run, and a self-reference count
of the report inside its own measurement is 0. cafe's own gate is unaffected:
`gofmt -l .` empty, `go vet ./...` clean, `bin/prime` green at 714 top-level and
876 subtest passes, 0 failures, 8 skips.

**Not exercised:** ARM 2's red branch (`harness up` exiting 0 against cafe). It
needs a cafe that holds rows, which does not exist. Its green branch — the
"adapter ran to completion" reading — is the same two-line comparison.

---

## 6. Findings for kit — reported, not edited

I may read `kit` and may not merge into it, so these are for the manager.

1. **`adapters/go.sh` hardcodes `identity` in a diagnostic.** On the migrate
   failure it prints *"identity's own migrations failed"* — measured verbatim in
   `arm2-adapter-vs-caf.log`, against a subject that is not identity. A message
   naming the wrong service is the same class as a self-test needle that is not a
   thing the check emits: the reader is told which service is at fault, and it is
   not this one. `adapters/go.sh:222`.

2. **`harness up` leaks its container when `adapter_migrate` fails.** Measured: on
   migrate failure the work directory `kit-tenant-iso.$SESSION/` has already been
   removed, so a following `harness down` exits 2 with `no session …` and leaves a
   `postgres:17` running. On a box shared with other workers that is a leaked
   database. `mutations.sh` works around it with a second teardown path by
   container name; the harness should own it.

3. **`cross_tenant_harness.sh` calls `say` before `say` is defined.** A
   non-numeric `KIT_TENANT_SESSION` hits the guard at line 86, which calls `say`
   defined at line 95 — so it exits 2 having printed **nothing**. Measured: a bare
   `UP_EXIT=2` and no diagnostic, which reads as a harness problem rather than an
   invocation one. `mutations.sh` forces a numeric session rather than passing the
   override through, so a bad override cannot masquerade as a finding about caf.

4. **`adapters/README.md`'s Go subject is not the only Go service that could be
   one.** It says Go is proven "against `identity`". Correct as written. Worth
   noting for whoever updates the language list: cafe is Go, is in MVP scope, and
   was on a packet's table as holding rows. It does not. The tripwire in this
   packet is the thing that keeps that table honest from cafe's side.

---

## 7. What is still unproven across the MVP's in-scope services

After this packet, every account-scoped service in MVP scope that holds rows is
proven by the harness, and the set of unproven languages is unchanged at four —
Ruby (`cafaye-rb`), TypeScript (`cafaye-ts`, `guard`, `parlor`, `site`), Python
(`cafaye-py`) and Rust (`darkroom`) — because nothing here moved a language. What
this packet changed is the *fleet table* rather than the language list: cafe was
recorded as the last in-scope Go service holding rows and needing proof, and the
measurement says cafe holds none, so the "exactly one left" premise this packet was
dispatched on was never true and the MVP's in-scope set of row-holding services
is now **proven in full**, and by the two the fleet has already named: **courier**
(Elixir, 19 cross-tenant negative tests) and **identity** (Go, six breakages in
`kit/reports/tenant-adapter-go-01/mutations.sh`, plus seven in identity's own
`internal/tenancy` audit). What remains unproven is
unchanged and is not about cafe: four languages with no adapter, of which
TypeScript is the strongest case, because no TypeScript service in the fleet holds
a database at all, so the harness has nothing to measure there; Ruby, Python and
Rust each hold rows in a language kit has no adapter for, and those are real gaps
rather than undefined instruments. The honest one-line claim is therefore
unchanged from `kit/reports/tenant-adapter-go-01` — *cross-tenant isolation is
proven for Elixir and Go, on services that scope correctly; four languages remain
unproven* — and this packet's contribution is that cafe has been removed from the
list of services someone might mistake for a fifth gap.

---

## 8. Decisions taken, and what was rejected

**Decided: withdraw rather than manufacture a subject.** The adapter contract
forbids hand-written schema in as many words, so a six-breakage recipe here would
have needed a table cafe does not have. Rejected.

**Decided: classification, not counting.** A count of `account_id` occurrences
produced the packet's "a start" reading, and reproducing that reading is the
defect. Rejected: reporting the count as the measurement.

**Decided: build the falsification rather than assert it.** A tripwire claimed
unexercised is a claim, and this packet exists because claims were not measured.
Rejected: shipping the red direction as prose.

**Decided: report the two self-caught defects rather than quietly fixing them.**
Both are recorded in §3 and in the recipe's comments, because a classifier that
was wrong once and was quietly corrected is a classifier whose next reader cannot
tell which number to trust.

**Decided: no fix in caf.** Nothing in cafe is broken, and a cafe change would have
been scope this packet does not have.

**Rejected: a forwarding-shaped probe for caf.** cafe holds state keyed by
account-adjacent things — port reservations, ledger entries, compose projects —
and one could write a "can a caller reach an account it does not hold" probe. That
is the instrument `adapters/README.md` names for the TypeScript layer, it needs a
different instrument entirely, and it is a different packet. Not started.