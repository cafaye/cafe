# REPORT: OPS-P12 — the cafaye failure-mode library, seeded and checked

**Packet.** OPS-P12, `moon/CAFAYE-OPS.md:903` — the failure-mode library as a
machine-readable rule set the box is checked against. Seed table:
`CAFAYE-OPS.md:731-748`. Attended tier's core: `CAFAYE-OPS.md:816`.

**Branch.** `ops/w2-failure-library`, commits `29589f2` → `0bc803b`.
**Worktree.** `/private/tmp/w2-failure-library-finish` — see §7 for why this is
not the path in the brief.

---

## 1. What exists

`internal/failure/` — five files, stdlib plus `internal/boxd` and nothing else:

| file | what it is |
|---|---|
| `doc.go` | the package's argument: what a rule is, why the condition is data, why an unanswerable rule is `Unknown` and not a pass, and what the package deliberately does not do |
| `rule.go` | `Rule` and `Library()` — **the ten rows of the doc's table**, in the doc's order |
| `condition.go` | the twelve-predicate vocabulary, its evaluator, and the `describe` rendering |
| `observe.go` | `Observation` (the fact surface), the `Observer` seam, `BoxObserver` |
| `check.go` | `Evaluate`, `Report`, `Verdict` (`holds` / `does-not-hold` / `unknown`), the rendering |

`internal/cli/failure.go` — `cafe failure check [<observation.json>]`, the verb
over it. Registered in `Commands()` (`internal/cli/command.go:78`), row in the
README command table (`README.md:99`), Layout paragraph in
`AGENTS.md:364-386`.

## 2. The rule set is the ten documented entries, and each cites its line

`Library()` returns ten. Each carries `Failure`, `Sees` (what a generic monitor
sees — the doc's second column), `Known` (the doc's third column, with the
`CAFAYE-OPS.md` line), `Remedy` (derived — see §3), `Contract` (the core
contract cited, or empty when the rule needs none) and `Condition`.

`library_test.go` asserts the set is ten, every field non-empty, no duplicate
id, every `Known` carrying `CAFAYE-OPS.md:`, and every condition's predicates
reading facts the `Observation` actually carries — the last one driven through
the real evaluator against a fully populated observation rather than through a
list of names kept beside it, because a second list is a second thing that can
drift.

**No invented entries.** The doc's table is ten rows and the library is ten.
An eleventh would be a guess, and a guess in a file a customer's box is checked
against is the kind that gets copied.

Two rows cite beyond the doc, because the doc's own sentence is corroborated by
the fleet's source and the citation makes it checkable:

- `identity-mfa-routes-absent` cites `identity/cmd/identity/main.go:646` and
  `:1185` — the MFA management routes are not mounted when
  `MFA_ENCRYPTION_KEY` is unconfigured, and the message is a *warning* rather
  than an error. The doc's "no error is ever printed" (`CAFAYE-OPS.md:741`) is
  the same fact read from the other side.
- `courier-password-reset-503` cites `courier/config/runtime.exs:235-241` for
  the setting name `COURIER_MAIL_ADAPTER` — the doc says "mailer
  unconfigured" without naming the variable, and the doc does not name the
  mailer variable, so I read it rather than invent one.

## 3. The remedy column, and where it is derived rather than documented

The doc's table has three columns and no remedy, and the brief asks for one.
Remedies are derived from the third column's fact: where the doc states an
action (run one replica, `CAFAYE-OPS.md:746`) the remedy is that sentence;
where the doc only rules something out (the probe timeout is the bug, not the
service, `:805`) the remedy says what is ruled out rather than prescribing a
change the doc did not decide. `doc.go` states the rule so the next person
adding an entry knows what the field is.

## 4. The condition is data, and that is the extensibility

A condition is `[]Predicate` (a twelve-kind vocabulary, each with `Service`,
`Name`, `Value`) under `All` and `Any`. There are no closures, so adding an
entry is a table row in `rule.go` plus a fixture row in the test — no code, no
registry. The price is a fixed vocabulary, and the price buys three things: the
rule set is data a test can walk, `Evaluate` takes `[]Rule` as a parameter so
a caller can check a subset or a superset with the same function, and a
predicate the checker does not know evaluates to *unknown* rather than false
(`condition.go:190-194`), which is what keeps a misspelling from silently
disarming a rule.

Predicates whose subject the doc does not name take an empty `Service` meaning
"any service" — `outbox-rows-over`, `publisher-absent`,
`database-declared-twice`. The doc's rows are about the stack, not about a
named service, so naming one would be inventing it.

## 5. The checker answers three verdicts, and the third is the load-bearing one

`Evaluate(rules, obs) Report`. `unknown` carries the name of the observation
field the rule needed and the box did not supply. The report renders holds
(the rule, what a monitor sees, the condition, the predicates observed, what
we know, the remedy, the contract) and unknowns (the rule and the missing
fact), then the count.

**The box daemon today answers nothing and says so.** `BoxObserver` adapts
`boxd.World` and supplies exactly one fact — the service names — because that
is all `World.Stack` carries (`internal/boxd/identity.go:130-135`; and a route answer
deliberately never carries a service's configuration either,
`internal/boxd/identity.go:131-134`). `TestTheBoxDaemonsObservationAnswersNothingAndSaysSo`
drives the whole seed through that adapter and asserts **0 holds, 0
does-not-hold, 10 unknown**, with the seven distinct facts named. A false
"does not hold" is a green light over a blind spot — the same report
`internal/lock`'s verifier refuses to produce.

That test is also the specification of what the substrate packet owes this
library, which is why it exists as code rather than as a paragraph.

## 6. Core contracts are cited, never forked

Where a rule needs a core contract, `Contract` names the file and the fact
taken from it:

- probes — `core/schemas/telemetry/probes.schema.json`: `/healthz` and
  `/readyz` are the two pinned `const` paths, 503 is readiness's contract
  answer, `timeoutMs` is bounded at 10s.
- the OTLP endpoint — `core/schemas/telemetry/otel-endpoint.schema.json`:
  `<SERVICE>_OTEL_ENDPOINT` is the only variable, the shipped collector is its
  default value, `OTEL_SDK_DISABLED=true` is the no-op.
- the event envelope — `core/schemas/event-envelope.schema.json`, with payload
  shapes owned by `schemas/events/<service>/<entity>/<action>.schema.json`.
- the MFA event pair — `schemas/events/identity/mfa/enabled|disabled`.

No path, variable or event name is transcribed into this tree as a second copy.
Four rows have an empty `Contract` because the failure mode is cafe's own and
core says nothing about it; an invented citation would be worse than an empty
field.

## 7. The gate, and the exit codes actually observed

Every command run from the worktree root, keyed to
`/tmp/gate-w2p12-final` and `/tmp/gate-w2-failure-library-finish2`:

| command | exit | what it said |
|---|---|---|
| `go build ./...` | **0** | — |
| `go vet ./...` | **0** | — |
| `gofmt -l .` | **0** | no output |
| `go run ./cmd/cafe lock --verify .` | **0** | `caf.lock matches 12 pinned file(s)` |
| `bin/prime` (16:31, pre-fix) | **1** | `24 packages, 1069 … 1 failures, 8 skips` — `TestRegistryContainsEveryCommand`, the registry table missing the new command |
| `bin/prime` (final, `/tmp/gate-w2p12-final`) | **0** | `24 packages, 1069 top-level passes, 1159 subtest passes, 0 failures, 8 skips` |

The gate floors moved in the same commits as the tests: `packages` 23 → 24,
`suite` 1051 → 1067 → **1069** (the ratchet's own instruction, from the 1077
the tree declares and `expectedSkips = 8`), and `caf.lock` re-pinned in the
same commit because `gate.yml` is its one `rule-bundle`.

## 8. What I would flag to the manager

1. **The worktree in the brief was deleted twice while I worked in it.**
   `/Users/kaka/Code/any/ops-wt/w2-failure-library` disappeared between two
   commands at ~16:04 and again after I recreated it; the manager had committed
   my in-flight work as `26e5b50` and moved the branch on. I continued in
   `/private/tmp/w2-failure-library-finish`, which the manager had created on
   the same branch. **Three commits I did not write landed on the branch while
   I was editing** (`3d1e5c0`, `4238faa`, `a398271`) — they raised the floors,
   wrote a report and documented the Layout, the same work I was doing. The
   result was a duplicated README command row, now fixed. A fleet where two
   workers share one branch will keep producing this.
2. **A duplicate worker was dispatched against this brief at 15:46**
   (`dispatch-retry.sh`, PID 49660) while I was already running it. Its own
   report is `REPORT-w2p12-failure-library-DUPLICATE.md`, committed on this
   branch; it correctly wrote no code. This is the packet's first hard rule
   ("one brief = one worker") broken by the dispatcher, not by either worker.
3. **A tension in the doc worth a decision, not a fix.** The doc says
   courier's password reset answers 503 because the mailer is unconfigured
   (`CAFAYE-OPS.md:747`), but courier's own boot *raises* on an unset
   `COURIER_MAIL_ADAPTER` (`courier/config/runtime.exs:235-241`) — so on a box
   where it is unset the failure is a boot one and there is no process to
   answer 503. Either the doc's row is about a different deployment shape, or
   courier's prod boot is not what a customer box runs. I encoded the row as
   the setting's absence and did not decide this.
4. **The remedy for `courier-readyz-slow-when-db-gone` says "raise the probe
   timeout" but core bounds `timeoutMs` at 10s**
   (`probes.schema.json`). 4.4s under a 10s ceiling means the timeout is not
   obviously wrong on core's terms; the doc's claim is that the orchestrator's
   *probe* timeout is the bug, which is a substrate setting this tree does not
   own. The rule identifies the condition and the remedy names the direction;
   the number is the substrate packet's.

## 9. What is left

- **The observation producer.** `BoxObserver` supplies one of eight facts. The
  substrate packet's `cafe box observe` (or a `World` method) is what turns the
  ten `unknown`s into answers; the test in §5 names the seven fields.
- **The schedule.** `CAFAYE-OPS.md:772` frames every ops item as a schedule;
  nothing here runs on one. OPS-P13's monthly report consumes the report.
- **`cafe failure list`** — the library as a table without an observation, which
  is what a customer's runbook cites. Not built.
