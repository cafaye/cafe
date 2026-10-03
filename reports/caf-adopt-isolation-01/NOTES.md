# caf-adopt-isolation-01 — working notes

## Claim under test
Packet fleet table: `caf | Go | holds rows | yes | <-- THIS PACKET`.

## What was read (not guessed)
| probe | command | result |
|---|---|---|
| `account_id` in Go source | `grep -rn account_id --include=*.go .` | 4 files, ALL telemetry attribute allowlists |
| `account_id` anywhere | `grep -rln account_id .` | 8 files, all telemetry/schema |
| database driver | `grep -rniE 'pgx\|lib/pq\|sqlx\|sqlite\|mysql\|mongo' go.mod go.sum *.go` | 0 in go.mod; 0 non-test imports |
| `database/sql` | `grep -rn '"database/sql"' --include=*.go .` | 0 |
| migrations | `find . -name '*.sql'` | 0 |
| goose dir | `ls migrations/` | does not exist |
| tenancy.yml | `find . -name tenancy.yml` | does not exist |
| go.mod deps | direct: goccy/go-yaml, modelcontextprotocol/go-sdk, santhosh-tekuri/jsonschema, uritemplate | no driver |

## Conclusion so far
caf holds NO rows. It is a CLI/gate binary. `cafaye.yml` says so itself:
"caf is a binary, not a service: it exposes no HTTP surface and publishes no events".

Fleet corroboration: only `courier` and `identity` carry `tenancy.yml` with
`accountScoped: true` — exactly the two services the harness has proven.

## Still to do
- Empirically run the Go adapter against caf and capture the receipt.
- Write `mutations.sh` as a withdrawal probe (TypeScript packet's discipline).
