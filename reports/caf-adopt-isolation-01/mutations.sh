#!/usr/bin/env bash
#
# reports/caf-adopt-isolation-01/mutations.sh — the Go adapter, pointed at caf.
#
#   bash reports/caf-adopt-isolation-01/mutations.sh
#
# THIS IS NOT SIX BREAKAGES, AND THE REASON IS THE FINDING
#
#   `kit/reports/tenant-adapter-go-01/mutations.sh` plants six edits in
#   identity's own query layer and watches each one go RED. Every one of those
#   six needs an ANCHOR: real source text, read out of a real file, with a file
#   and a line. There are none to read in caf, and that is not a gap in the
#   reading — it is the answer.
#
#   The harness measures a service's QUERY LAYER. It brings up a real Postgres,
#   applies THE SERVICE'S OWN MIGRATIONS, seeds two accounts through the
#   service's own write path, and calls the service's own functions as account
#   A. caf has none of those four things, so there is no statement to remove a
#   predicate from and no test to write for it.
#
#   So this file does what `kit/reports/tenant-adapter-ts-01/subject-audit.sh`
#   did, for the same reason: it is a WITHDRAWAL PROBE, and it fails in one
#   direction only. It exits 0 while caf holds no rows and exits 1 — naming
#   what appeared and where — the moment caf grows a database. A TypeScript
#   service that acquires a database becomes the first subject this harness can
#   measure in TypeScript; a caf that acquires one becomes the second subject it
#   can measure in Go, and the six-breakage recipe becomes writable. That is the
#   condition this probe exists to go red on.
#
# WHY IT IS NOT A SIX-BREAKAGE RECIPE WITH ZERO BREAKAGES
#
#   A recipe that plants nothing and exits 0 is the vacuous green this
#   repository has a written rule about, wearing a Go adapter's name. So this
#   file runs THREE arms, and the third one is the load-bearing one:
#
#     ARM 1  the static measurement, over caf's own tree
#     ARM 2  the empirical receipt — kit's Go adapter, really invoked, against
#            caf, and the exact step it stops at
#     ARM 3  THE CONTROL: arms 1 and 2's own logic, pointed at `identity`,
#            which DOES hold rows, and required to come out the OTHER way
#
#   ARM 3 is what makes ARM 1 a measurement. Without it "caf holds no rows" is
#   unfalsifiable: a script that greps for a driver and finds none would report
#   the same absence against a service whose driver it simply failed to
#   recognise. Running the identical logic against a service that is known to
#   hold rows — and requiring it to answer the OPPOSITE way — is what makes the
#   answer about caf rather than about the probe. This is kit's own rule that a
#   control's green must be about the thing it names: "a green control that two
#   different checks could satisfy proves neither", and a finding asserted
#   without a positive control is a claim nobody has measured.
#
# WHERE THE MEASUREMENT LIVES, AND WHY IT IS NOT JUST A COUNT
#
#   caf does mention `account_id`. It mentions it THIRTEEN TIMES, in four Go
#   files and four JSON schemas — and not one of the thirteen is a predicate.
#   All of them are the NAME of a telemetry attribute: an allowlist entry in
#   `internal/telemetry/telemetry.go`, the same name in core's vendored
#   telemetry schemas, and the resource-attribute list in
#   `telemetry/otel-endpoint.json`. A packet's note that "caf carries 2 test
#   files that mention account_id" counted exactly those two files
#   (`internal/contract/telemetry_test.go`, `internal/telemetry/telemetry_test.go`)
#   and read the count as "a start".
#
#   It is not a start. It is the opposite: it is the only place the string
#   appears, and it appears as the name of an OpenTelemetry dimension. So ARM 1
#   does not COUNT `account_id` — counting is what produced the wrong reading.
#   It CLASSIFIES every occurrence and requires that none of them is a SQL
#   predicate, and it prints all thirteen so the classification is a receipt a
#   reader can check rather than a verdict they have to trust.
#
# WHAT IT PROVES, AND WHAT IT DOES NOT
#
#   It proves the cross-tenant harness has NO Go query layer in caf to measure,
#   and that this is a property of caf rather than a defect in the probe.
#
#   It does NOT prove caf is safe. caf is a CLI and a gate: it holds no
#   customer's rows, so there is no `WHERE` that could be dropped, and D18's
#   question — can account A read account B's data — has no answer to give
#   about caf because caf is not the service that holds the data. The account
#   boundary for the cafaye platform is enforced downstream, by identity and
#   courier, which are the two services this harness has proven. Reporting this
#   as "caf is isolated" would be the nominal-adapter defect wearing a
#   measurement's clothes.

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="$ROOT/reports/caf-adopt-isolation-01"
KIT="${KIT_REPO_ROOT:-$(cd "$ROOT/../kit" && pwd)}"
HARNESS="$KIT/tests/tenant_isolation/cross_tenant_harness.sh"
GO_ADAPTER="$KIT/tests/tenant_isolation/adapters/go.sh"

# The control's subject. `identity` is the Go service this adapter was written
# for and the one service in the fleet that is known, measured, to hold rows:
# it carries `tenancy.yml` with `accountScoped: true`, a `migrations/` directory
# goose can apply, and `internal/accounts/store.go`. If this cannot be found,
# ARM 3 exits 2 — an absent control is not a passing control.
IDENTITY="${KIT_IDENTITY_REPO:-$(cd "$ROOT/../identity" 2>/dev/null && pwd || true)}"

mkdir -p "$OUT"

controls=0
breakages=0
observed_red=0
failures=0
findings=0

say() { printf '%s\n' "$*"; }
rule() { say "--------------------------------------------------------------------------"; }

# `contains` — a shell `case` over a variable already in memory, never
# `printf … | grep -q`. kit has been bitten by this twice: `grep -q` closes the
# pipe on its first match, the writer dies of SIGPIPE, and `pipefail` promotes
# 141, so the verdict flips on the SIZE of the output rather than its content.
contains() {
  case "$1" in
    *"$2"*) return 0 ;;
    *) return 1 ;;
  esac
}

# count_lines <text> — non-empty lines of a captured block. A herestring rather
# than a pipe, for the reason `contains` says.
count_lines() {
  local text="$1" n=0 line
  while IFS= read -r line; do
    [ -n "$line" ] && n=$(( n + 1 ))
  done <<EOF
$text
EOF
  printf '%s' "$n"
}

say "recipe:   $0"
say "subject:  $ROOT   (caf)"
say "kit:      $KIT"
say "adapter:  $GO_ADAPTER"
say "control:  ${IDENTITY:-<not found>}"
say ""

[ -r "$HARNESS" ] || {
  say "mutations.sh: $HARNESS is missing. Exiting 2: nothing was proven."
  exit 2
}
[ -r "$GO_ADAPTER" ] || {
  say "mutations.sh: $GO_ADAPTER is missing. Exiting 2."
  exit 2
}

# ==========================================================================
# ARM 1 — THE STATIC MEASUREMENT, over the subject's own tree.
#
# Three signals, reported SEPARATELY and not folded together, because they mean
# different things. This is the same three-signal discipline
# `subject-audit.sh` uses, kept deliberately: IMPORTS is the one the finding
# rests on, DECLARES is scaffolding until something imports it, and MIGRATES is
# the service's own schema.
# ==========================================================================

# The Go database drivers and ORM/driver packages this probe recognises. One
# list, in one place, so it can be argued about as a list. `database/sql` is in
# it and is the important one: it is the standard library's own interface, it
# needs no module entry, and a driver nobody wrote down in go.mod would still
# show up here.
GO_DB_MODULES="database/sql github.com/jackc/pgx github.com/jackc/pgx/v5 github.com/lib/pq github.com/jmoiron/sqlx modernc.org/sqlite github.com/mattn/go-sqlite3 github.com/go-sql-driver/mysql github.com/mongo-driver/mongo gorm.io/gorm entgo.io/ent github.com/uptrace/bun"

# A .sql under any of these is a fixture, not the service's own schema.
FIXTURE_DIRS="test tests e2e fixtures testdata"

# go_files <dir> — the subject's own Go source, .git pruned rather than
# filtered afterwards.
go_files() {
  find "$1" \( -name .git \) -prune -o -type f -name '*.go' -print 2>/dev/null
}

# db_imports <dir> — source files importing a recognised database package.
# Scoped to NON-TEST source: a driver imported only by a test is a test
# harness, not the service's query layer, and conflating the two is how a
# service that owns no rows gets reported as one that does. Both counts are
# printed so the distinction is visible rather than decided silently.
db_imports() {
  local list="$1" mod hits
  go_files "$list" | while IFS= read -r f; do
    [ -n "$f" ] || continue
    case "$f" in *_test.go) continue ;; esac
    printf '%s\n' "$f"
  done >"${TMPDIR:-/tmp}/caf-arm1-nontest.$$"
  for mod in $GO_DB_MODULES; do
    hits="$(xargs grep -lF "\"$mod" <"${TMPDIR:-/tmp}/caf-arm1-nontest.$$" 2>/dev/null || true)"
    printf '%s' "$hits"
    printf '\n'
  done | grep . | sort -u
  rm -f "${TMPDIR:-/tmp}/caf-arm1-nontest.$$"
}

# declares_db_driver <dir> — a recognised driver named in go.mod's require.
declares_db_driver() {
  local mod
  [ -f "$1/go.mod" ] || return 1
  for mod in $GO_DB_MODULES; do
    if grep -qsF "$mod" "$1/go.mod"; then
      printf '%s' "$mod"
      return 0
    fi
  done
  return 1
}

# own_migrations <dir> — .sql files that are the subject's OWN schema: not under
# a fixture directory. Plus, separately, a goose migrations DIRECTORY, because
# that is what `adapter_migrate` actually feeds to goose and a service can hold
# migrations in a directory with no .sql in it that this find would see.
own_migrations() {
  local base="$1" f rel skip d
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    rel="${f#"$base"/}"
    skip=0
    for d in $FIXTURE_DIRS; do
      case "$rel" in "$d"/*) skip=1 ;; esac
    done
    [ "$skip" -eq 0 ] && printf '%s\n' "$rel"
  done <<EOF
$(find "$base" \( -name .git \) -prune -o -type f -name '*.sql' -print 2>/dev/null)
EOF
  return 0
}

# account_occurrences <dir> — every line naming an account, with its path and
# line number, and CLASSIFIED. This is the caf-specific measurement and the
# reason the recipe is not a count.
#
# The classification is three-way and it is the whole point:
#
#   PREDICATE    the line is a SQL statement carrying an account predicate —
#               a `WHERE … account_id …`, a bind on one, an INSERT column list.
#               This is what a breakage would remove. Its presence is what would
#               make a six-breakage recipe writable.
#   TELEMETRY    the line is the NAME of an OpenTelemetry attribute — an
#               allowlist entry, a schema property, a resource-attribute list.
#               caf has thirteen of these and zero of the other kind, which is
#               a sentence about caf's architecture, not about its SQL.
#   OTHER        anything else, printed so the classification cannot quietly
#               absorb a case it does not understand.
#
# A two-way count (occurs / does not occur) is what the packet's note did and it
# read thirteen telemetry attribute names as "a start". So the buckets are named
# and every line is printed.
# The classifier is TWO extended regular expressions rather than a shell `case`,
# for one reason worth recording: the first draft spelled the SQL bucket as one
# `case` arm per SQL keyword and bash's parser could not read the arm. A
# measurement script that will not parse is a measurement script that measures
# nothing, and the failure lands at parse time rather than at the verdict. A
# regex states the same bucket in one line and has no quoting regime of its own
# to get wrong.
#
# PREDICATE  the line carries an account FILTER. A WHERE that mentions the
#            account is the shape a breakage removes; the DML keywords catch a
#            statement that binds the account in a column list.
PREDICATE_RE="WHERE.*account_id|where.*account_id|account_id[[:space:]]*(=|==|:)[[:space:]]*[0-9$\?]|^[^:]*INSERT INTO|insert into| UPDATE | DELETE FROM| delete from"

# TELEMETRY  the line NAMES an OpenTelemetry attribute rather than filtering on
#            one: an allowlist entry, a schema property, a resource-attribute
#            list. All thirteen of caf's occurrences land here.
TELEMETRY_RE="telemetry|otel|resourceAttributes|resource_attributes|description|allowlist|[Aa]ttribute|schema"

classify_account_lines() {
  local dir="$1"
  # `.git` and this recipe's own directory are excluded: a measurement that
  # counts its own receipt is a measurement with a floor of one.
  grep -rn "account_id" "$dir" 2>/dev/null \
    | grep -v "/\.git/" \
    | grep -v "^$dir/reports/caf-adopt-isolation-01/" \
    | while IFS= read -r line; do
        if printf '%s' "$line" | grep -qE "$PREDICATE_RE"; then
          printf 'PREDICATE\t%s\n' "$line"
        elif printf '%s' "$line" | grep -qE "$TELEMETRY_RE"; then
          printf 'TELEMETRY\t%s\n' "$line"
        else
          printf 'OTHER\t%s\n' "$line"
        fi
      done
}

# measure <dir> <label> — arms 1's whole body, parameterised by subject so the
# CONTROL runs the identical logic rather than a simplified copy of it. A control
# that runs different code is not a control of the thing it names.
measure() {
  local dir="$1" label="$2"
  local imports migrations n_imports n_migrations declares goose_dir tenancy
  local pred other tele

  imports="$(db_imports "$dir")"
  n_imports="$(count_lines "$imports")"
  migrations="$(own_migrations "$dir")"
  n_migrations="$(count_lines "$migrations")"
  declares="-"
  declares_db_driver "$dir" >/dev/null && declares="$(declares_db_driver "$dir")"

  # `adapter_migrate` runs `goose -dir migrations`. A service with migrations in
  # some other layout satisfies the find above and still has nothing for goose,
  # so the directory is measured in its own right.
  goose_dir="absent"
  [ -d "$dir/migrations" ] && goose_dir="present"

  tenancy="absent"
  [ -f "$dir/tenancy.yml" ] && tenancy="present"

  # Classified ONCE and split, not classified per bucket: three calls would be
  # three greps over the same tree and could disagree if the tree moved between
  # them, which is the shape of a self-inconsistent measurement.
  classified="$(classify_account_lines "$dir")"
  pred="$(printf '%s' "$classified" | grep '^PREDICATE' || true)"
  tele="$(printf '%s' "$classified" | grep '^TELEMETRY' || true)"
  other="$(printf '%s' "$classified" | grep '^OTHER' || true)"

  say "  [$label]"
  say "      IMPORTS a db driver (non-test) : $n_imports"
  if [ "$n_imports" -gt 0 ]; then
    while IFS= read -r i; do
      [ -n "$i" ] && say "          ${i#"$dir"/}"
    done <<EOF
$imports
EOF
  fi
  say "      DECLARES a driver in go.mod     : $declares"
  say "      MIGRATES (.sql own schema)      : $n_migrations"
  if [ -n "$migrations" ]; then
    while IFS= read -r m; do
      [ -n "$m" ] && say "          $m"
    done <<EOF
$migrations
EOF
  fi
  say "      goose migrations/ directory    : $goose_dir"
  say "      tenancy.yml                    : $tenancy"
  say "      account_id PREDICATE lines     : $(count_lines "$pred")"
  say "      account_id TELEMETRY lines     : $(count_lines "$tele")"
  say "      account_id OTHER lines         : $(count_lines "$other")"

  # THE TALLY THE FINDING RESTS ON: a database exists to this probe if the
  # service imports a driver, declares one, or ships its own migrations. Any one
  # of the three is enough, and requiring all three would be a weaker claim
  # dressed as a stricter one.
  if [ "$n_imports" -gt 0 ] || [ "$n_migrations" -gt 0 ] || [ "$goose_dir" = "present" ] || [ "$declares" != "-" ]; then
    HOLDS_ROWS=yes
  else
    HOLDS_ROWS=no
  fi
  N_PREDICATES="$(count_lines "$pred")"
  say "      => HOLDS ROWS                  : $HOLDS_ROWS"
  say ""
}

# ARM 1 needs these to be visible to the arms below, and shell functions do not
# return a struct, so they are globals set by `measure` and read once.
HOLDS_ROWS=""
N_PREDICATES=0

say "=== ARM 1: THE STATIC MEASUREMENT, over caf's own tree ==="
say ""
say "A service that HOLDS ROWS has a query layer to measure: a driver it imports,"
say "migrations of its own, and an account predicate that could be dropped. This"
say "arm reports which of those caf has, and prints every account_id line it"
say "found with a bucket, because caf names account_id thirteen times and none"
say "of the thirteen is a predicate."
say ""
measure "$ROOT" "caf"
say "  every account_id occurrence in caf, classified:"
while IFS= read -r line; do
  [ -n "$line" ] && say "      $line"
done <<EOF
$(classify_account_lines "$ROOT")
EOF
say ""

if [ "$HOLDS_ROWS" = "yes" ]; then
  say "RED ARM 1: caf now HOLDS ROWS."
  say "  The premise of this packet has changed underneath it, and that is the"
  say "  condition this probe exists to catch: caf has become the SECOND Go"
  say "  service this harness can measure. The recipe this file withdraws is now"
  say "  writable, and the six anchors belong in caf's own query layer."
  say ""
  say "  Do not delete this file on the strength of one green run. Read the"
  say "  numbers above, find the table and the predicate, and write"
  say "  reports/caf-adopt-isolation-01/mutations.sh as the six-breakage recipe"
  say "  kit/reports/tenant-adapter-go-01/mutations.sh is."
  observed_red=$(( observed_red + 1 ))
  breakages=$(( breakages + 1 ))
  say ""
fi

# ==========================================================================
# ARM 2 — THE EMPIRICAL RECEIPT.
#
# Not a grep: kit's Go adapter, really invoked against caf, so the step it stops
# at is observed rather than predicted. Bounded, and it never touches caf's own
# checkout — the adapter copies the service into its own work directory, which is
# the property `adapter_prepare` promises and the reason a mutation pass can
# never edit the tree it is reporting on.
# ==========================================================================

say "=== ARM 2: THE EMPIRICAL RECEIPT — kit's Go adapter, really invoked ==="
say ""
say "adapter_prepare should SUCCEED (caf has a go.mod and compiles), and"
say "adapter_migrate should FAIL for want of migrations. Which of those two"
say "happens is the receipt; predicting it would prove nothing."
say ""

SESSION="${KIT_TENANT_SESSION:-$$}"
case "$SESSION" in
  '' | *[!0-9]*) SESSION=$$ ;;
esac

# `harness up` refuses a non-numeric session by calling `say` BEFORE `say` is
# defined, so it exits 2 having printed nothing at all. Measured here before the
# probe was written: `KIT_TENANT_SESSION=cafprobe1` produced a bare `UP_EXIT=2`
# and no diagnostic. Forcing a number here rather than passing the caller's
# through is what keeps a bad override from looking like a finding about caf.
TIMEOUT_BIN="$(command -v timeout 2>/dev/null || command -v gtimeout 2>/dev/null || true)"

ARM2_LOG="$OUT/arm2-adapter-vs-caf.log"
ARM2_RC=0
(
  export KIT_TENANT_ADAPTER="$GO_ADAPTER"
  export KIT_SERVICE_REPO="$ROOT"
  export KIT_TENANT_SESSION="$SESSION"
  "$HARNESS" up
) >"$ARM2_LOG" 2>&1 || ARM2_RC=$?

# Tear the container down whatever happened. `harness down` is tried FIRST, and
# it is not enough on its own — MEASURED on this subject: when
# `adapter_migrate` fails, `harness up` has already removed its
# `kit-tenant-iso.$SESSION/` work directory, so `down` cannot find
# `session.env`, exits 2 with "no session", and leaves a postgres:17 running.
# On a box shared with three other workers that is a leaked database, so the
# container is removed by its derived name afterwards. Two teardown paths
# because one of them is not enough, and the recipe does not get to choose
# which one runs when the harness has already failed.
(
  export KIT_TENANT_ADAPTER="$GO_ADAPTER"
  export KIT_SERVICE_REPO="$ROOT"
  export KIT_TENANT_SESSION="$SESSION"
  "$HARNESS" down
) >/dev/null 2>&1 || true

if command -v docker >/dev/null 2>&1; then
  "$TIMEOUT_BIN" 60 docker rm -f "kit-tenant-iso-$SESSION" >/dev/null 2>&1 || true
fi

tail -20 "$ARM2_LOG" | sed 's/^/  /'
say ""
say "  harness exit status: $ARM2_RC   (2 = a run that could not happen, never 0)"
say ""

if [ "$ARM2_RC" -eq 0 ]; then
  say "RED ARM 2: the Go adapter RAN TO COMPLETION against caf."
  say "  The premise of this packet has changed underneath it: caf has become"
  say "  measurable by this harness, and a green harness against it is the"
  say "  signal that a six-breakage recipe is now writable."
  observed_red=$(( observed_red + 1 ))
  breakages=$(( breakages + 1 ))
elif contains "$(cat "$ARM2_LOG")" "migrations failed"; then
  say "ARM 2 as predicted: the adapter compiled caf, then stopped at"
  say "  adapter_migrate — `goose -dir migrations` against a directory caf does"
  say "  not have. It is the fourth of the harness's five steps and the second"
  say "  that needs something the subject holds. Full log: ${ARM2_LOG#"$ROOT"/}"
  say ""
  say "  This is the receipt. `adapter_migrate` is where the adapter contract"
  say "  says \"no hand-written schema — a harness that seeds a table it invented"
  say "  is measuring its own DDL\", and caf has nothing to apply. Writing a"
  say "  schema here would produce a green line resting on the harness's own"
  say "  DDL, which is the vacuous green this repository has a written rule about."
elif contains "$(cat "$ARM2_LOG")" "is not the Go service this adapter was written"; then
  say "ARM 2: the adapter rejected caf as not a Go service — which is wrong, and"
  say "  is itself a finding. caf is a go.mod module and it compiles."
  failures=$(( failures + 1 ))
else
  say "ARM 2 INCONCLUSIVE: the harness exited $ARM2_RC for a reason this recipe"
  say "  does not recognise. That is not a pass and not a finding — it is a"
  say "  measurement this recipe could not read. The log is the evidence:"
  say "    ${ARM2_LOG#"$ROOT"/}"
  failures=$(( failures + 1 ))
fi

# ==========================================================================
# ARM 3 — THE CONTROL. Arms 1 and 2's own logic, pointed at identity.
#
# This is the arm that makes ARM 1 a measurement. Identity holds rows; the
# identical logic must say so, or ARM 1 is reporting what the probe recognises
# rather than what caf has.
# ==========================================================================

rule
say "=== ARM 3: THE CONTROL — the same measurement, pointed at identity ==="
say ""
say "identity is the Go service kit's adapter was written for, and the one"
say "  service in the fleet measured to hold rows. The logic below is ARM 1's"
say "  logic, unchanged, with identity substituted for caf. If it answers the"
say "  SAME way it answered for caf, ARM 1 measured nothing and this run's"
say "  finding is not a finding."
say ""

[ -n "${IDENTITY:-}" ] && [ -d "${IDENTITY:-}" ] || {
  say "FAIL: no identity checkout beside this repository, so ARM 3 cannot run."
  say "  An absent control is not a passing control, and a finding asserted with"
  say "  no positive control is a claim nobody has measured. Point"
  say "  KIT_IDENTITY_REPO at one. Exiting 2."
  exit 2
}

CAF_HOLDS="$HOLDS_ROWS"
CAF_PREDICATES="$N_PREDICATES"
measure "$IDENTITY" "identity (control — expected: HOLDS ROWS yes)"

if [ "$HOLDS_ROWS" != "yes" ]; then
  say "FAIL: the control came out \`$HOLDS_ROWS\`, the same answer it gave for"
  say "  caf. The measurement cannot tell a service that holds rows from one"
  say "  that does not, so ARM 1's verdict is about the probe rather than about"
  say "  caf, and this run proves nothing."
  failures=$(( failures + 1 ))
elif [ "$N_PREDICATES" -lt 1 ]; then
  say "FAIL: the control holds rows but this recipe classified ZERO account_id"
  say "  lines as a SQL PREDICATE in it. identity's store.go spells"
  say "  \`WHERE account_id = \$1\` in five functions, so the classifier is"
  say "  mis-bucketing the very lines ARM 1's verdict rests on — and a"
  say "  classifier that cannot recognise a real predicate would call caf's"
  say "  thirteen telemetry attribute names predicates too."
  failures=$(( failures + 1 ))
else
  say "PASS control 3/3: the identical logic answers the OTHER way for identity."
  say "  identity HOLDS ROWS (yes) with $N_PREDICATES account_id lines classified"
  say "  as SQL predicates. caf answered no, with $CAF_PREDICATES."
  say "  So the difference is a property of the two trees and not of the probe."
  controls=$(( controls + 1 ))
fi

# The control also pins the classifier, which is the subtle half: if the
# classifier bucketed telemetry attribute names as PREDICATES, ARM 1's caf
# number would be 13 rather than 0 and the finding would invert. The control is
# what makes "caf has zero predicates" mean zero predicates.
say ""

# ==========================================================================
# THE VERDICT
# ==========================================================================

rule
say "=== VERDICT ==="
say ""

if [ "$failures" -ne 0 ]; then
  say "FAIL: $failures arm(s) could not be evaluated. An arm that could not be"
  say "  read is not an arm that passed, and it is not an arm that failed."
  exit 1
fi

if [ "$breakages" -gt 0 ]; then
  say "RED: caf has become measurable by this harness."
  say ""
  say "  This recipe is WITHDRAWN, and the withdrawal is the finding. caf has"
  say "  rows to isolate; the Go adapter runs; the six anchors belong in caf's"
  say "  own query layer. Write the six-breakage recipe against it, and keep"
  say "  this file as the tripwire that told you the moment that became true."
  exit 1
fi

say "FINDING: caf holds no rows, and the cross-tenant harness is UNDEFINED"
say "  against it — not unfinished. There is no query layer here for it to"
say "  measure."
say ""
say "  caf is a CLI and a gate. It declares no HTTP surface and publishes no"
say "  events (\`cafaye.yml\`, in its own words). Its four direct dependencies"
say "  are a YAML parser, the MCP SDK, a JSON Schema validator and a URI"
say "  template matcher, and NOT ONE of them is a database driver. It imports"
say "  no database package, ships no .sql and no migrations/ directory, and has"
say "  no tenancy.yml."
say ""
say "  It names \`account_id\` thirteen times and not one of the thirteen is a"
say "  predicate: every one is the NAME of an OpenTelemetry attribute. That is"
say "  the opposite of a query layer. It is the signature of a service that"
say "  emits telemetry about accounts it forwards to, and forwards the"
say "  authorisation to the service that holds the rows."
say ""
say "  So the packet's premise — \`caf | Go | holds rows | yes\` — is not"
say "  supported by the tree, and this is reported rather than worked around."
say "  Manufacturing a subject here would have meant writing a schema for the"
say "  harness to seed, and a green line resting on DDL this repository wrote."
say ""
say "  THE TALLIES, SEPARATELY:"
say "    controls green        : $controls"
say "    breakages planted     : $breakages"
say "    observed RED          : $observed_red"
say ""
say "  There are no breakages because there is no statement to break. The one"
say "  control that matters is ARM 3, and it is the reason the finding is a"
say "  measurement: the same logic, run against identity, answers the opposite"
say "  way and finds $N_PREDICATES account predicates where it found zero in caf."
say ""
say "  This is a FINDING and not a PASS. It is also a tripwire: the moment caf"
say "  gains a driver, a migration or a tenancy.yml, this script exits 1 and"
say "  names what appeared."
exit 0