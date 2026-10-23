# cafe

**Current direction (2026-10-19):** Ship is the product. The anywaye stack is internal infrastructure; we are not selling or supporting it as a standalone product as of this date. We continue to develop and improve it, treating the founder as the primary customer and keeping "build once, run everywhere" true, with the expectation that it may be offered again later.

The anywaye platform CLI. One binary, one manifest, one set of contracts: `cafe`
is how a developer creates a project, runs it locally, deploys it, and exposes
it to agents.

**v0 is a skeleton, and the parts that are not a skeleton work.** Every
subcommand exists and validates its arguments. `version`, `doctor`, `cafe
contract`, `dev` and `deploy` do real work; the rest say so, plainly, instead of
half-working:

```
$ cafe deploy --env staging --dry-run identity
identity: deploying config/deploy.yml from ~/src/identity with kamal 2.12.0
  credentials come from .kamal/secrets.staging, by name only
  environment staging, so config/deploy.staging.yml is merged over config/deploy.yml

this is what cafe deploy would run:

  kamal version
      kamal is the deploy engine; this prints its version and fails if it is not installed
  kamal config --destination staging
      reads config/deploy.yml with its ERB evaluated, and refuses a config kamal cannot use
  kamal setup --destination staging
      installs Docker if the host lacks it, boots the accessories, builds, pushes, rolls out, and refuses a release that never goes healthy
  kamal app containers --destination staging
      reports which containers are actually running, which is the only answer to what a partial failure left behind
      the last one runs only if the deploy above fails.

Dry run. The two read-only steps really did run: kamal version and kamal config --destination staging read the
configuration and change nothing. kamal setup --destination staging, which is the only step that changes a
server, was NOT run. No container was started, no image was built or pushed,
no file in /home/you/src/identity was written, and no secret was read.

Deploy it for real with:
  cafe deploy --env staging --yes identity
```

## Install

```sh
go install github.com/anywaye/cafe/cmd/cafe@latest
```

`@latest` resolves to the newest tag. Prebuilt archives, their checksums and the
release notes are on the [Releases page](https://github.com/anywaye/cafe/releases);
see [Release](#release) for how one is cut.

Or build from source:

```sh
bin/prime              # go mod download && go build ./... && go test -v ./... , then say what ran
go build ./cmd/cafe
```

`bin/prime` is the gate, and it is declared rather than guessed: [`gate.yml`](gate.yml)
at the repository root says what it is, what it needs from the machine, and what
its own output must contain before the word "passed" means anything. The format,
the checker and the reasoning are `anywaye/core`'s
(`schemas/gate.schema.json`, `harness/gate_check.py`, `docs/gate.md`). Check it
without running anything:

```sh
../core/harness/bin/gate-check .            # the static half
../core/harness/bin/gate-check --prove .    # and the proving half, which runs bin/prime
tests/gate-declaration-self-test.sh         # and that the declaration can go red
```

Two things about it that are worth knowing before you read the file.
`bin/prime --live` additionally runs the four live tests in
`internal/ports/live_test.go`, `internal/ryuk/live_test.go` and
`internal/deploy/live_test.go`, which need a container runtime and are otherwise
skipped — and it refuses to report success if they did not run. The deploy one
also needs `kamal` on `PATH`, and it really deploys. **Read what it does before
running it on a machine that is not yours**: it starts an SSH daemon, publishes a
port, and uses the global container name `kamal-docker-registry` that Kamal itself
owns. And the declaration's
`live-tier` proof exists so that a run in which they *were* skipped cannot read as
a run in which they passed.

## Release

A release is a tag, and **the tag is the version**. A Go CLI has no version file
to bump: goreleaser reads `{{ .Version }}` off the tag (leading `v` trimmed) and
injects it into the binary's build identity, which is what `cafe version` prints.
So there is nothing to keep in sync — the tag is the whole declaration.

```sh
git switch master && git pull --ff-only
bin/prime                  # green before anything is tagged
git tag -a v0.1.0 -m "cafe v0.1.0"
git push origin v0.1.0
```

The tag push runs [`.github/workflows/release.yml`](.github/workflows/release.yml),
which runs goreleaser `release --clean` and publishes a GitHub Release with, for
each of `darwin`, `linux` and `windows` on `amd64` and `arm64`:

- `cafe_<version>_<os>_<arch>.tar.gz` — a `.zip` on Windows — each containing the
  `cafe` binary, `README.md` and `LICENSE`;
- `cafe_<version>_checksums.txt`, the sha256 of every archive;
- the generated release notes, which are **the changelog of record for the tag**.

`CHANGELOG.md` stays the human log: it is written and grouped by hand, and a
release does not rewrite it. The two are different documents on purpose — the
notes come from the commits that are in the tag and nowhere else, so a stranger
can check them against `git log v0.1.0`, which is not true of a file edited after
the fact.

`go install github.com/anywaye/cafe/cmd/cafe@vX.Y.Z` installs one exact tag from
the module proxy; `@latest` is the newest. There is deliberately **no Homebrew,
Scoop or Docker target yet** — each is a target with an owner, a place to publish
into and a credential to hold, and none of those exists. GitHub Releases plus
`go install` are the install paths until one does.

## Commands

| Command | What it does | v0 |
|---|---|---|
| `cafe init` | write the `anywaye.yml` manifest for an existing project, validated against core's schema | **works** |
| `cafe new <name>` | scaffold a new anywaye service or app from the template catalog | **works** |
| `cafe dev [project]` | run the local development stack | **works** |
| `cafe deploy <service>` | deploy a service or app: `--engine kamal` (default), `--engine argo` or `--engine remote` (the operated platform, pinned off until TG-D1) | **works** |
| `cafe backup <service>` | take one real backup and prove it restores, into a scratch database it then drops | **works** |
| `cafe backup drill <service>` | the same cycle unattended, with the outcome recorded and `--json` for a machine | **works** |
| `cafe agent register --name "Noel"` | ask identity for an unclaimed agent, store its token in the OS keyring, print the claim URL on stderr | **works** |
| `cafe agent login` / `whoami` | make one saved agent session active, and ask identity who this machine is | **works** |
| `cafe agent token show [<name>]` | list the agent's tokens — or the one live token with a name; never the values | **works** |
| `cafe agent token create [<name>]` | mint a fresh named token and store it in the OS keyring | **works** |
| `cafe agent token rotate [<id>]` | replace a token with a fresh one; the session's own when no id is given | **works** |
| `cafe agent token revoke --yes [<id>]` | destroy a token and remove it from the keyring; refused without `--yes` | **works** |
| `cafe provider connect <provider>` | seal a provider credential to the box's public key and post the envelope to box — the agent never sees the value | **works** |
| `cafe provider status --json` | which providers this box is connected to, as box reports it; opens no envelope | **works** |
| `cafe failure check [<observation.json>]` | check a box against the anywaye failure-mode library — the eleven documented conditions a generic monitor cannot see, each answered `holds` / `does not hold` / `unknown`, naming the fact it could not read; the observation carries setting names, never values | **works** |
| `cafe gate floors [--write] [path]` | derive `gate.yml`'s countable floors from the tree — print the delta, or with `--write` set them and re-pin `cafe.lock` | **works** |
| `cafe gen telemetry` | write the OpenTelemetry setup a service needs to honour core's telemetry contract | **works** |
| `cafe gen chart` | write the Helm chart a service is deployed from — Deployment, Service, `/readyz` probe, secrets by reference (the Argo Application is the substrate's) | **works** |
| `cafe lock <path>` | pin this tree's specs, vendored schemas and generated clients in `cafe.lock` | **works** |
| `cafe lock --verify <path>` | recompute every pin and name every file that no longer matches | **works** |
| `cafe contract lint <path>` | validate `anywaye.yml` against the core schema, and the OpenAPI document it names | **works** |
| `cafe contract breaking <a> <b>` | compare two OpenAPI revisions, reporting which tier each change breaks | **works** |
| `cafe contract resolve <c> <v>` | resolve a core version constraint | **works** |
| `cafe cve inventory` | every component on the box and its version, against what the substrate pins | **works** |
| `cafe cve scan` | the box's CVE surface: the component, the advisory, the fixed version, and what applying it touches | **works** |
| `cafe cve schedule` | print the cron line that puts the scan on a schedule | **works** |
| `cafe mcp` | serve the anywaye tools over the Model Context Protocol | **works** |
| `cafe boxd` | run the box daemon: outbound-only mTLS to box, redeeming whitelisted capabilities (invariant 4); its identity comes from the box-owned store, with `$CAF_BOX_IDENTITY` a deprecated fallback | **works** |
| `cafe version` | print the cafe version | **works** |
| `cafe doctor [path]` | report the toolchains, whether this machine can run this project, and what is unreclaimed | **works** |
| `cafe reclaim` | reclaim what a cafe session left behind, containers before volumes | **works** |
| `cafe env up <tier> -- <cmd>` | provision the stack, run a command against it, and reclaim it when the command is done | **works** |
| `cafe docs publish [--dry-run] [--visibility <public\|private>] [--version <v>] [--press <url>] [--account <id-or-slug>] <project>` | ship documentation to press — non-interactive by default: visibility is inferred from the manifest (`public` ships as docs from `src/content/docs/`, `private` as notes from the root pages plus `projects/`), the session is exchanged for a press-scoped token, the plan is printed, and the content tarball is ingested; `--dry-run` sends nothing, and the receipt is one JSON document | **works** |
| `cafe secret inventory [--box <id>]` | every secret the box holds, classified rotatable / conditional / not-rotatable with the reason and the file:line it comes from — one JSON document | **works** |
| `cafe secret rotate [<name>] [--dry-run] [--yes] [--all]` | rotate the rotatable class on the schedule recorded in the state file, and name every entry it refuses to touch — no silent skips | **works** |
| `cafe secret inventory [--state-dir <dir>] [--box <id>]` | every secret this box holds, each classed rotatable / conditional / not-rotatable with the reason and the file:line it comes from, as one JSON document | **works** |
| `cafe secret rotate [--dry-run] [--all] [--yes] [--timeout <d>] [--state-dir <dir>] [--box <id>] [name]` | rotate the rotatable class on its schedule, and refuse the rest by name — a value is never printed, never put in argv, and never read back out of the cluster | **works** |
| `cafe update [path]` | merge the template's updates into this checkout and advance the template pin | **works** |

Every command in the table above works; there are no stubs left in v0.
`cafe help <command>` prints the flags a command will honor.

Global flags: `-h`/`--help`, `-v`/`--version`. `cafe help <command>` is the same
as `<command> --help`.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | the command succeeded, or you asked for help |
| 1 | the command ran and failed |
| 2 | cafe was invoked wrongly: unknown command or flag, wrong argument count |

`cafe doctor` exits 0 unless some row is `fail`, then 1; a `warn` never moves it.
It used to always exit 0, and that is a change — see [`cafe doctor`](#cafe-doctor).
`cafe env up` passes its child's exit code through, so a CI job running a suite
through it cannot read 0 out of a red run. `contract lint` and `contract resolve` print their verdict on stdout and say
nothing on stderr, so a CI log shows the answer once. `cafe dev` does the same for
a stack that came up with something in it that did not: the report is the
message, and it is not repeated on stderr.

### Flag syntax

Flags come from the standard Go `flag` package, so they must precede positional
arguments: `cafe deploy --dry-run identity` works, `cafe deploy identity
--dry-run` does not. Single dash and double dash are equivalent.

## `cafe version`

```
$ cafe version
cafe 0.1.0 (commit a5acc37)
```

The semver and commit are injected at link time (`.goreleaser.yml` does this for
releases). A plain `go build` or `go run` of the source tree reports
`cafe 0.0.0-dev (commit unknown)`.

## `cafe dev`

`cafe dev` reads a project's `anywaye.yml`, validates it against the same rules
`cafe contract lint` applies, renders a compose file from it, **writes that file,
prints it in full**, brings the stack up, waits for it, and reports what came up
and what did not.

```
$ cafe dev --dry-run -registry catalog.json ./hello
project hello-dev: hello (go)
skipped legacy: optional dependency, and the local registry does not know how to run it
wrote /work/hello/hello.compose.yaml

# cafe dev — the local stack for "hello-dev", generated from anywaye.yml.
# Do not edit: every run rewrites this file. The manifest is the
# source of truth; this is what it turned into.
name: hello-dev

services:
  alpha:
    image: nginx:1.27-alpine
    command:
      - nginx
      - -g
      - daemon off;
    ports:
      - "3000:3000"
    environment:
      ALPHA_OTEL_ENDPOINT: http://otel-collector:4317
      DATABASE_URL: postgres://hello:hello@postgres:5432/hello
      GREETING: hello from alpha
      REDIS_URL: redis://redis:6379/0
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_healthy
    healthcheck:
      test:
        - CMD
        - wget
        - -qO-
        - http://localhost:80/
      interval: 2s
      timeout: 3s
      retries: 15
      start_period: 2s

  hello:
    image: nginx:1.27-alpine
    command:
      - nginx
      - -g
      - daemon off;
    ports:
      - "8080:8080"
    environment:
      DATABASE_URL: postgres://hello:hello@postgres:5432/hello
      HELLO_OTEL_ENDPOINT: http://otel-collector:4317
      REDIS_URL: redis://redis:6379/0
    depends_on:
      alpha:
        condition: service_healthy
      postgres:
        condition: service_healthy
      redis:
        condition: service_healthy
    healthcheck:
      test:
        - CMD
        - wget
        - -qO-
        - http://localhost:80/
      interval: 2s
      timeout: 3s
      retries: 15
      start_period: 2s

  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: hello
      POSTGRES_PASSWORD: hello
      POSTGRES_USER: hello
    volumes:
      - postgres-data:/var/lib/postgresql/data
    healthcheck:
      test:
        - CMD
        - pg_isready
        - -U
        - hello
      interval: 2s
      timeout: 3s
      retries: 30
      start_period: 5s

  redis:
    image: redis:7-alpine
    volumes:
      - redis-data:/data
    healthcheck:
      test:
        - CMD
        - redis-cli
        - ping
      interval: 2s
      timeout: 3s
      retries: 30
      start_period: 2s

volumes:
  postgres-data: {}
  redis-data: {}
```

That is real output, from a real run. The document is printed rather than only
written because it is the only copy of what cafe decided: a command that
generates something nobody can inspect is a command nobody can debug.

Then, without `--dry-run`:

```
$ cafe dev -registry catalog.json ./hello
starting 4 services in hello-dev

service   status   origin          notes
postgres  healthy  infrastructure  Up 22 seconds (healthy)
redis     healthy  infrastructure  Up 22 seconds (healthy)
alpha     healthy  registry        Up 12 seconds (healthy)
hello     healthy  project         Up 5 seconds (healthy)

hello is up on http://localhost:8080
```

The rows are in start order, which is why the infrastructure is at the top: the
uptimes say the stack came up in dependency order, `hello` starting only once
`alpha` was healthy. The `origin` column says why each one is there — the
project, the catalog, or cafe.

### What it decides, and refuses

| Situation | What happens |
|---|---|
| No `anywaye.yml` | refused in one sentence, with `cafe init` named as the fix |
| An invalid manifest | refused with `cafe contract lint`'s exact line, so the two cannot disagree |
| A dependency cycle | refused **before anything starts**, with the loop in the message: `dependency cycle: alpha -> beta -> alpha` |
| Two services on one published port | refused, naming both and the port, and pointing at `-port` |
| A required dependency the catalog cannot answer for | refused by name; a service that needs `identity` does not start without it |
| An optional (`required: false`) dependency the catalog cannot answer for | skipped, and the report says which and why |
| A project with no Dockerfile and no catalog entry | refused, naming both ways out |

A cycle is caught here rather than left to the runtime, where it arrives as a
container that never starts and a wall of "depends on itself" lines. Two
services agreeing on a *container* port is not a conflict — every service has
its own address on the stack network — so only published host ports are checked.

### Where the telemetry endpoint comes from

Every anywaye service in the rendered document carries
`<SERVICE>_OTEL_ENDPOINT`, pointed at the collector core's endpoint contract
documents as the default. That variable is the fleet's whole endpoint contract —
core's `otel-endpoint.schema.json` calls it "the ONLY contract" — and a variable
only exists if something writes it. The service that would write it is a binary
that reads it, so `cafe dev` writes it.

A catalog entry's own value outranks that default, which is the self-hoster
escape hatch core describes: one service pointed at their own backend, the rest
left on the collector.

```
"pantry": {
  "name": "pantry",
  "image": "ghcr.io/anywaye/pantry:0.4.1",
  "environment": {"PANTRY_OTEL_ENDPOINT": "https://otlp.example.com:4318"}
}
```

Two things it deliberately does **not** do. There is no `depends_on:
otel-collector` — the collector is not in `internal/dev`'s infrastructure table,
because it needs kit's `otel-collector.yml`, which this repository does not own,
and an edge to a container nothing starts is a stack that never comes up. And
local infrastructure is handed no endpoint: postgres and redis are third-party
images with no anywaye identity and a variable they do not read.

### The service catalog

A manifest says `dependencies: [{name: identity, version: ^0.1.0}]` and cannot
say how `identity` is run: that is a fact about the service and it changes with
it. So the answer comes from a registry, and `-registry` names one. The document
is one JSON object keyed by service name, and it is the same document whether it
is read from a file or served over HTTP — [pantry](https://github.com/anywaye/pantry)
serves the official one:

```json
{
  "identity": {
    "name": "identity",
    "image": "ghcr.io/anywaye/identity:0.4.2",
    "command": ["/app/identity", "serve"],
    "port": 8080,
    "publish": true,
    "environment": {"DATABASE_URL": "postgres://identity:identity@postgres:5432/identity"},
    "volumes": ["identity-data:/var/lib/identity"],
    "healthcheck": {
      "test": ["CMD", "curl", "-fsS", "http://localhost:8080/healthz"],
      "interval": "5s", "timeout": "3s", "retries": 20, "startPeriod": "10s"
    },
    "dependencies": ["postgres", "redis"]
  }
}
```

`dependencies` are service names resolved through the same registry, so the
closure cafe walks is the registry's own graph — and a cycle in it is a cycle in
pantry, reported with its path.

**cafe ships no catalog.** There is no table of images in the binary, because an
image reference guessed by a CLI is a reference that pulls the wrong thing. A
project that declares no dependencies runs today; one that declares some is
told which dependency it needs and how to supply it.

### Idempotence, and what an interrupt does

Running `cafe dev` twice reconciles one stack rather than starting a second
beside it. Two things make that true: the compose project name is
`<service>-dev` and is the same both times, and the document is byte-identical,
so the runtime's config hash does not change and it has nothing to recreate.

```
$ shasum -a 256 hello.compose.yaml
2b3d2d83f022f5e3a9ffcf11c86639c3d7ae34950d7792b1bc3d0b1f51128b8b  hello.compose.yaml
$ cafe dev -registry catalog.json ./hello      # again
$ shasum -a 256 hello.compose.yaml
2b3d2d83f022f5e3a9ffcf11c86639c3d7ae34950d7792b1bc3d0b1f51128b8b  hello.compose.yaml
```

A compose file that reshuffles its services, its environment keys or its
volumes between two runs produces a diff nobody can read, so the byte-for-byte
equality of two runs is a contract and it is pinned by a test that plans the
same manifest twenty times.

An interrupt stops the wait and puts the stack back down, and the volumes stay:

```
$ cafe dev -registry catalog.json ./hello
starting 4 services in hello-dev
^C
stopping hello-dev
hello-dev stopped
```

The teardown checks what is left and repeats itself while something is left. An
interrupt kills the `docker compose up` client but the daemon keeps working, so
containers it had already been told to create arrive after the client is gone —
a single `down` removes the network and misses them. Without the check, an
interrupted bring-up leaves two containers behind every time; with it, nothing.

### Flags

| Flag | What it does |
|---|---|
| `-out <path>` | where to write the document. Relative paths are taken **inside** the project; an absolute path is honoured and named in the output |
| `-dry-run` | render, write and print; start nothing |
| `-no-infra` | leave out the local postgres and redis |
| `-port <n>` | publish the project service on another host port. The container port is a property of the image and is never rewritten |
| `-registry <path>` | the service catalog to resolve dependencies through |
| `-wait <duration>` | how long to wait for the stack, default `3m` |

Everything written is inside the project directory unless `-out` names somewhere
else, and the report always names the file. The local postgres and redis are a
stack default rather than a declaration — a developer developing against a local
database is developing against the wrong one — and `-no-infra` is the opt-out for
a service that genuinely needs neither.

### No TUI

Progress is plain lines, one per step, in the order the steps happen. The
repository had no TUI framework and this packet did not add one: it would be a
dependency nobody asked for and a second rendering of the same state. `cafe
dev --dry-run` is how a plan is inspected before anything starts, which is the
job a TUI would have had here.

## `cafe deploy`

`cafe deploy` runs a real deployment. It drives [Kamal 2](https://kamal-deploy.org),
and it runs **one** command that changes anything:

```sh
cafe deploy <service> --env <env> --yes
```

`kamal setup` — not `kamal deploy` — because `setup` is "install Docker on the
host if it lacks it, boot the accessories, deploy", and the accessories are the
part a first deploy needs and a bare `deploy` skips. A customer following this
path onto a fresh VPS would otherwise get a service with no database, and the
error would be a connection-refused from inside the application.

cafe does not reimplement the deploy. Build, push, proxy boot, the rolling
rollout, the health gate and the rollback are Kamal's; a Go implementation of
them would be a second deploy engine that can disagree with the first about what a
deploy is, and the disagreement would be found in production. What cafe owns is
the part either side: the refusal that should happen before, and the report that
has to be true after.

### What it needs

A project with `config/deploy.yml`, copied from
[`anywaye/kit`](https://github.com/anywaye/kit)'s `templates/kamal/deploy.yml.erb`.
The `KIT_*` variables that template interpolates are **deployment facts, not
secrets**, so they are exported in the shell that runs the deploy and the template
refuses to render a blank one. Credentials are **names** in `.kamal/secrets.<env>`
(or `.kamal/secrets` with no `--env`), resolved by Kamal; no value ever appears in
the config or in cafe's output.

The deploy engine is a Ruby gem, so the machine you deploy *from* needs Ruby. The
**service image does not** — `kamal-proxy` and `kamal-secrets` are standalone
binaries Kamal puts on the *server*. A missing kamal arrives as
`gem install kamal` and that explanation, not as exec's "executable file not
found".

### It refuses one thing, and says why

An accessory that publishes its port puts the database on every interface the host
has. cafe reads Kamal's **own resolved config** — after the ERB is evaluated — and
refuses to deploy a config that does this:

```
$ cafe deploy --env production identity
cafe deploy: refusing to deploy

accessory postgres publishes port 5432 on every interface of the host, and its data is reachable from the network.
  kamal turns `port: 5432` into `docker run --publish 5432:5432`, and Docker binds 0.0.0.0 and :: when the
  published port names no host address. That is measured, not inferred: the boot fails with
  "Bind for 0.0.0.0:5432 failed" on a busy port, which is the same bind.
  Nothing needs it. The accessory is on the `kamal` network as postgres, and app containers reach it by
  that name. The host's loopback is not a container's loopback, so binding 127.0.0.1 does not help
  them either — it only helps a psql run on the box itself.
  Fix it in config/deploy.yml, one of:
    delete the `port:` line, and the database is reachable by name only   <- the right answer
    set it to "127.0.0.1:5432:5432", which publishes to this host only, for a psql on the box
```

Exit 1, nothing deployed, and **stderr stays empty** — the refusal is the report
and a CI log should not see it twice. `DATABASE_URL` then has to name the
accessory's container (`<service>-<accessory>`, e.g. `identity-postgres`) rather
than a host address, which is the one change this costs per service.

Both halves of that are measured, not argued: a boot
with `port: 5432` fails on `Bind for 0.0.0.0:5432`, a loopback publish is
invisible to a container on the `kamal` network, and a deployed app reaches its
database **by name** with the accessory publishing nothing at all.

### `--dry-run` is honest, and three ways

It prints every command that would run — including the one that only runs *after*
a failure — and executes the two that cannot change a server. Those two really do
run, because the config is an ERB template only Kamal can evaluate; printing a
plan cafe had to guess at would be a template with extra steps. And:

- the **filesystem** is walked before and after and compared;
- the **argv** is recorded by a fake, and the changing step is asserted not to
  have been executed;
- the **refusal still fires**. A dry run on a config that publishes a port exits
  nonzero and does not deploy — a dry run more permissive than the real one is
  worse than no dry run.

A dry run also never asks for confirmation: somebody who typed `--dry-run` to see
what would happen gets the plan, not a question, and in a pipeline with no
terminal, asking would hang.

### A partial failure says what to do

```
$ cafe deploy --env staging --yes identity
...
kamal setup --destination staging failed: exit status 1

what is running now (kamal app containers --destination staging):
App Host: 10.0.0.4
CONTAINER ID   IMAGE                    STATUS                     NAMES
9586677b96f3   ghcr.io/anywaye/identity:9f2c  Exited (1) 4 seconds ago   identity-web-9f2c1a8
6f3ce7867e8b   ghcr.io/anywaye/identity:1a4b  Up 43 seconds               identity-web-1a4b7d

what to do:
  the database was not touched. kamal boots accessories separately from the app, so
  this deploy moved application containers only and the database is still up.
  a previous release is usually still serving: kamal leaves it up until a new one is healthy.
  read the previous release's name from the list above, then: kamal rollback <version>
  the reason kamal gave is above.
```

`kamal app containers` is a plan step, not a branch in the command, so a dry run
prints the command a *failure* will run. Asking the server what is running is the
only answer to that question that is not a guess. With `-version` pinned, the
rollback line names the release instead of telling you to read it.

### It has actually been run

A real deployment, to a real Docker daemon reached over real SSH, with a real
`kamal-proxy` gating traffic on a real healthcheck, and a secret from
`.kamal/secrets` arriving in the serving container. It is
`internal/deploy/live_test.go`, it is in the **live** tier rather than the gate
(it needs `kamal` and a container runtime), and it has been run against a real
deployment — including the release that never goes healthy being refused while
the previous one keeps serving. **Read what it does before running it on a
machine that is not yours**: it starts an SSH daemon, publishes a port, and uses
the global container
name `kamal-docker-registry` that Kamal itself owns.

### `--engine remote`, and why it refuses

```sh
$ cafe deploy --engine remote --account acct-1 --yes identity
The remote engine deploys a service into the anywaye-operated platform, where
every client the account serves is an account on one shared deployment rather
than a anywaye deployment of its own. ...
```

That is the third engine, and it deploys **nothing**. The refusal is the packet.

What it would do: ask the operated platform which deployment serves the named
`--account`, and hand it one deploy request — the service, the version and the
commit it was built from. The customer's app is built once and deployed once,
and every client they serve is an account on the shared platform rather than a
anywaye deployment of its own. That is the platform's reason to exist, and it is
what `cafe deploy` could not name before: the other two engines both target
something the operator runs themselves.

Why it does not do that yet: **one deployment serving many accounts is only safe
once one deployment is proven safe for strangers** — `anywaye/notes/tenancy-gate.md`, whose
TG-D1 records the decision. `cafe` cannot make that decision and does not try.
So the gate is a constant in the package that returns the refusal, and not a
flag, an environment variable or a config key: a tenancy decision a CI job could
set with nobody deciding it is not a decision.

The mechanism is built and tested. `planForRemote` is the function the gate calls
once TG-D1 is recorded, and `internal/deploy/remote_test.go` drives it — the two
steps, the request read back out of the plan's own argv (so a dry run shows
exactly what a real deploy asks), the account the request carries, and a report
that states what the deploy did *not* do. Each test in that half opens by
asserting the shipped gate is still closed, so a test written against an enabled
engine would fail rather than pass by accident. Enabling it when the decision
lands is one line.

`--dry-run` is refused too, and ahead of the plan: the first thing a remote
deploy says is why it cannot deploy. A dry run that listed the steps and left
the refusal out would read as a deploy that works.

`--account` goes with `--engine remote` and with nothing else. Passing it to
`kamal` or `argo` is a usage error rather than a silent drop: the account is the
destination, and an operator who typed it said which account, so they are
answered — "the anywaye-operated platform's, and this engine deploys somewhere you
already pointed it at" — rather than told nothing while the flag is discarded.

The argv is the single copy of what a deploy asks, so the two defects the proofs
caught in it are worth naming. `planForRemote` wrote `--account <account>`
unconditionally, which with no `--account` on the command line produced
`--account  --version 0.2.0` — and the parser read the token after the flag as
the value, so the platform would have been asked which deployment serves an
account called `--yes`. And the parser took the last token as the service only
when it was not the engine's own name, so a service called `remote` would have
been deployed as a service called nothing. Both are fixed and both were watched
going red first.

## `cafe backup`

`cafe backup` takes one real backup and then **proves it**, in one command:

```sh
cafe backup <service> --table <table> [--env <env>] [--scratch <db>] --yes
```

A backup that has never been drilled is not a backup. `config/kamal-backup.yml` is a
specification until the accessory that runs it is booted, and a specification has
never restored anything. So this command boots the accessory, takes a real snapshot
through kamal-backup into the service's own restic repository, restores that
snapshot into a scratch database, asserts the tables you name hold rows, and drops
the scratch database — **including when the drill fails**.

The proof is a number, not a word. kamal-backup decides the drill passed by the exit
status of the `--check` command, so the check has to *be* an assertion:

```
$ cafe backup --table users --yes identity
identity: backing up from config/kamal-backup.yml in ~/rehearsal with kamal 2.12.0
  the deployment it belongs to is config/deploy.yml
  credentials come from .kamal/secrets, by name only
  the backup accessory runs it, and the restore is drilled into identity_drill
  its schedule is 1d, which is a scheduled dump and not point-in-time recovery
...
cafe backup: users holds 7 rows in identity_drill after the restore
...
  identity_drill is gone, whether the drill passed or not

identity: the snapshot restored
  the backup accessory is booted and the repository holds a snapshot taken during this run
  kamal-backup restored the latest snapshot into identity_drill and ran the assertion; its exit
  status is the verdict, and the row counts it printed are above. users held rows
  identity_drill has been dropped, so nothing is left to restore into by accident
  what this does NOT prove: that the schedule will take the next one. config/kamal-backup.yml sets
  `backup.schedule`, and a scheduled dump is not point-in-time recovery — there is no WAL
  shipping and no base backup, so a destroyed primary loses up to that interval of
  committed transactions. The window is read from the file, not assumed here.
  its logs: kamal accessory logs backup
```

The last paragraph is the point of the last paragraph. A drill is evidence about **one
snapshot**; the data-loss window is the schedule in the file, and the command reads it
rather than assuming one.

### It checks the two files agree, before anything is booted

`config/kamal-backup.yml` and `config/deploy.yml` are one contract. Every clause of it
is a claim about what happens at deploy time, and a pair that is broken in a way that
only fails then is cheapest to catch while nothing has started. cafe reads Kamal's
**own resolved config** — after the ERB is evaluated — and refuses four things:

| reason | what is wrong |
|---|---|
| `cafe-backup/accessory-not-declared` | the backup configuration names an accessory the deploy configuration does not have |
| `cafe-backup/config-not-mounted` | the accessory does not mount that backup configuration read-only |
| `cafe-backup/secret-not-declared` | the backup configuration names a secret the accessory's `env.secret` does not declare |
| `cafe-backup/app-name-mismatch` | `app:` disagrees with the service, so every snapshot is unfindable |

```
$ cafe backup --table users --yes identity
cafe backup: refusing to back up

cafe-backup/secret-not-declared
  the backup configuration names the secret RESTIC_REPOSITORY, and the backup accessory does not declare it.
  kamal-backup builds that accessory's environment from that accessory's `env.secret` list and from nothing else, so this pair deploys and then fails validation with a message about RESTIC_REPOSITORY — measured on kamal-backup 0.5.2, and what the operator will actually be shown.
  The accessory declares: DATABASE_URL, DATABASE_PASSWORD, RESTIC_PASSWORD, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY
  Fix it in .../config/deploy.yml, by adding
    RESTIC_REPOSITORY
  to the backup accessory's env.secret list. Declaring a secret the backup configuration does not use is NOT this failure: restic reads AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY from the environment directly, and the accessory has to declare them for the repository to be reachable.
```

Exit 1, nothing booted, and **stderr stays empty** — the refusal is the report. The
reasons are `cafe-backup/…` so a script can match one instead of grepping prose.

### One refusal is deliberately NOT cafe's

A scratch database whose name *looks* production-shaped is refused by
**kamal-backup** (`Config#production_like_target?`), and cafe hands it the name and
reports what the gem said:

```
refusing production-looking restore target identity-postgres/caf_prod_drill; choose a scratch target that does not look like production
```

cafe keeps **no copy** of that rule. A second copy of a rule about which database gets
dropped is a second opinion about the one thing that must not be in doubt — and in
operator form the same rule already exists in
[anywaye/kit](https://github.com/anywaye/kit)'s `templates/kamal/drill.sh`, which is
also where this command's `env.secret` connection preamble and its "a check must be
an assertion, not a count" rule come from. What cafe refuses itself is narrower: a name
it cannot write down unquoted through two shells (`plainIdentifier`).

### One race is measured, and the retry is conditioned on the cause

Booting a backup accessory starts its scheduler, and the scheduler's **first cycle
takes the restic repository lock**. So the snapshot this command wants can collide
with a cycle the boot itself caused, and restic answers exit 11 — which kamal-backup
then reports as a failure to `restic init`, so the last line an operator reads says
`config file already exists` and the line above it says a lock was held.

cafe waits for the repository to be free, and if the snapshot still collides it retries
**only while the evidence says it was a lock** — the failing command's own transcript
first, then a fresh reading. A wrong repository password (restic's exit 12) or a
missing repository (10) fails in one attempt instead of being retried until the
budget, and the gem's own cause reaches the report unchanged.

### `--dry-run` is at least as strict as the real run

Same two read-only steps really run, same contract check against the same resolved
document, same refusal — and every step that would change something is printed and
not executed. `TestADryRunRefusesExactlyWhatTheRealRunRefuses` runs both paths over
five documents and compares the reason, so a dry run more permissive than the real one
is a red test rather than a surprise.

### It has actually been run

A real `pg_dump` of a real Postgres, streamed into a real restic repository by the
real `ghcr.io/crmne/kamal-backup:0.5.2`, restored into a scratch database with seven
rows asserted — plus the two refusals, one of them the gem's. It is
`internal/backup/live_test.go`, it is in the **live** tier rather than the gate (it
needs `kamal` and a container runtime), and it has been run — a real `pg_dump` into a
real restic repository, restored into a scratch database with seven rows asserted.
**Read what it does before running it on a machine that is not yours**: it starts an SSH
daemon, publishes a port, boots two containers against the host's Docker socket, and
creates and drops a database.

### `--engine cnpg` — the launch substrate's engine

The fleet runs Kamal today, so `kamal` is the default. The launch substrate has no
Kamal, so there is a second engine, selected by `--engine`:

```sh
cafe backup <service> --engine cnpg --table <table> [--scratch <cluster>] --yes
```

`kubectl cnpg` is the tool, and the engine owns its argv. The backup is a CloudNativePG
`Backup` object (`kubectl cnpg backup <service>`), and the restore is **declarative**:
`kubectl cnpg` has no `restore` subcommand, so the restore renders a recovery `Cluster`
whose `spec.bootstrap.recovery.backup.name` points at the `Backup`, and applies it with
`kubectl apply`. The drill waits with blocking `kubectl wait` commands on the resource
phases, asserts with `kubectl cnpg psql` (one query that divides by zero if any named
table is empty), and drops the scratch cluster on every path.

The CNPG engine reads no `config/kamal-backup.yml` and checks no two-file contract —
the cluster is a fact about the Kubernetes namespace, not a pair of rendered documents.
So the two-file contract refusal is **gone** on this engine, deliberately: there is no
pair of files to disagree. The refusals that survive are the ones about the drill rather
than about Kamal: a restore with no `--table` to assert, and a scratch cluster name that
is not a plain identifier. `--env` is refused, because it is a Kamal destination and the
CNPG engine has no destination.

`--dry-run --engine cnpg` prints every `kubectl cnpg` command it would run, the recovery
manifest the restore would apply, and executes nothing — not even the preflight. That is
the proof this machine can produce, and it needs no cluster.

Kamal and `kit/templates/kamal` retire at parity, not before — the Kamal path is the
default and is unchanged.

### `cafe backup drill` — the outcome is recorded, not just printed

Everything above is a verdict printed to a terminal the process then closes, which is
the difference between a drill that happened and a drill anybody can point at. So the
drill has an unattended form that **records** it:

```sh
cafe backup drill <service> --table <table> [flags] [--json]
```

It is the same cycle with the confirmation taken away — a drill on a schedule has nobody
to answer a prompt, and a command that hung waiting for one would be a schedule that never
ran. Every run appends one JSON file per service under the state directory:
`CAF_DRILL_DIR`, else `$XDG_STATE_HOME/cafe/drills`, else `~/.local/state/cafe/drills`.
That is `internal/ledger`'s `DefaultDir` convention, and it is state rather than config —
these are records cafe wrote about work it did, not decisions a person made, and an
editable record is not evidence. The last twelve drills are kept, which is a year of the
monthly cadence.

`--json` writes **one** document to stdout and moves cafe's own report to stderr, so a
script pipes stdout into a parser without skipping a line of prose:

```
$ cafe backup drill --table users --json identity    # the report goes to stderr
{
  "service": "identity",
  "engine": "kamal",
  "scratch": "identity_drill",
  "tables": ["users"],
  "outcome": "failed",
  "reason": "cafe-backup/drill-not-run",
  "detail": "cafe backup: kamal could not read the deployment, so there is nothing to back up: exit status 1",
  "started_at": "2026-10-10T09:00:00Z",
  "finished_at": "2026-10-10T09:00:02Z",
  "duration_ms": 1383
}
```

The monthly report and the box agent read that document. Nothing secret is in it and
nothing can be: cafe never reads a credential's value, so it has none to record.

A refused drill is a third outcome and not a boolean, because "the restore worked" and
"cafe would not touch the service" are different facts with different fixes — one is a log
to read and one is a file to edit. A refusal's record names the **clause**, not only the
class, so a script learns which line to change rather than only that the config is wrong.
A fourth fact is kept apart from all of those: `cafe-backup/drill-not-run` means the cycle
never started — the engine is not installed, or the deployment cannot be read — and
recording it as a failed *restore* would send a reader to fix the wrong thing.

### What a failed drill does

Exit 1, a `cafe-backup/drill-…` reason a script can match, and one line on stderr naming
the service and where the record went:

```
cafe backup drill: cafe-backup/drill-failed: identity did not prove a restore (failed)
  kamal-backup drill production --database identity_drill failed: exit status 1
  recorded in ~/.local/state/cafe/drills
```

cafe has no notification transport — the tier copy states that nobody is paged — so the
exit code and the named reason are the whole convention, and a monitor that wants more
reads `--json`. A record that could not be written is itself a failure: a verdict that
was printed and forgotten is the gap this closes, and a drill whose record did not land
has been forgotten exactly as thoroughly.

**The schedule is not here.** `cafe backup drill` is what a schedule RUNS; it is not a
scheduler. The cadence stays where the fleet already keeps it — cron, a systemd timer, or
the box's own — and the record's `finished_at` is what tells you whether it has been
running.

## `cafe doctor`

`cafe doctor` answers three questions. The first is unchanged; the second is why
the command is worth running at all; the third is the one that makes cleanup
something the tool reminds you about rather than something you remember.

**Which toolchains does this machine have?** The tool table, unchanged: binaries
resolved on `PATH`, the path of the one that answered, nothing executed.

```
$ cafe doctor
tool            status  found at
git             ok      /opt/homebrew/bin/git
docker          ok      /usr/local/bin/docker
docker compose  ok      /usr/local/bin/docker-compose
tilt            ok      /Users/kaka/.local/share/mise/shims/tilt
go              ok      /Users/kaka/.local/share/mise/shims/go
ruby            ok      /Users/kaka/.local/share/mise/shims/ruby
elixir          ok      /Users/kaka/.local/share/mise/shims/elixir
python          ok      /opt/homebrew/bin/python3
bun             ok      /Users/kaka/.bun/bin/bun
rust            ok      /Users/kaka/.cargo/bin/rustc
checked 10 tools, 10 ok, 0 missing
```

**Can this machine run this project?** And **what has it not reclaimed?**

```
$ cafe doctor -registry catalog.json ./hello
check      state  detail                  fix
toolchain  ok     10 of 10 tools present  -
1 state check(s): 1 ok, 0 warn, 0 fail

project /work/hello
check    state  detail                        fix
runtime  ok     server 29.4.0                 -
machine  ok     16 GiB and 8 CPUs             -
ports    warn   free, but published outside cafe's block 15000-15999: 8080; a parallel worker cannot see these and will not avoid them   cafe env up go -- <command>   # reserves from 15000-15999 and holds them for the session
plan     ok     4 services in hello-dev       -
4 state check(s): 2 ok, 1 warn, 0 fail

reclamation
check        state  detail                                       fix
reclaimable  warn   1 entr(ies) in /Users/k/.local/state/cafe/ledger nothing has reclaimed: 1 stack(s), 1 port reservation(s), oldest 2 days ago   cafe reclaim          # a dry run; add -yes to remove
port block   ok     nothing is holding a port in 15000-15999     -
2 state check(s): 1 ok, 1 warn, 0 fail

this machine can run this project, with the warnings above; none of them stop it
```

### ok, warn, fail

Every check is one of three words, and the middle one is the load-bearing one.
A boolean check forces a choice between failing on warnings — noisy, and the
first thing a team disables — and ignoring them, which makes the report a
decoration. So there are three states, and `warn` moves nothing:

| state | meaning | exit code |
|---|---|---|
| `ok` | the machine is fine | 0 |
| `warn` | it works, and here is the thing worth knowing | 0 |
| `fail` | it cannot do what was asked, and here is the command | 1 |

**This is a change from cafe 0.x, where `doctor` always exited 0.** It is the one
behaviour change in this section and `cafe help doctor` says so too.

Severity is reasoned per *fact*, not per check, because one check can fail in
ways of different importance: a ledger with one unreclaimed stack is worth
mentioning, and a ledger that cannot be read means nothing can be reclaimed at
all. `tilt` missing is a `warn` — most people do not use it — and the container
runtime missing is a `fail`, because nothing can start. Every fact a check can
fail on is in one table in `internal/cli/doctor_check.go`, and a test asserts
that table is complete: a fact that is not in it takes a default nobody reasoned
about.

Every row that is not `ok` carries the exact command that fixes it.

### What the project section can see that the tool table cannot

- **A container runtime that is installed and not answering.** `docker --version`
  succeeds either way, so the tool table says `ok` while nothing can be started.
  The `runtime` check asks the *server*, and tells the two failures apart, because
  they have different fixes.
- **A machine with too little of something**, and a machine that would not say.
  4 GiB and 4 CPUs are the floor for a database, a cache and a service at once. A
  machine that will not report its memory is a `warn`, not a `fail`: reporting
  zero as "too little" is a false alarm about a machine that is probably fine,
  and an alarm people learn to ignore is worse than no alarm.
- **A port the stack needs and something else already holds.** The ports come from
  the same plan `cafe dev` would build, not from a list written here, so the two
  cannot drift into a check that passes while the stack cannot start. A port
  published *outside* 15000-15999 is a `warn` with a reason: it works, and no
  sibling worker on the machine can see it.
- **A `spec` repository** — core, and the contract fixtures — ships no process, so
  there is no stack and no port. It is reported as such rather than asked about.

### What the reclamation section is for

- **Is there anything unreclaimed?** Every stack `cafe dev` brought up is written
  to a ledger before the resource is created, so a cafe killed mid-run leaves a row
  naming something that still exists. This check says so, with the age, and the
  fix is `cafe reclaim`.
- **Is something in cafe's port block that is not ours?** cafe publishes from
  15000-15999 so that every worker on a machine can see which ports are ours. A
  stranger in that range is not an error — the stranger may be entitled to it —
  but it is exactly what produces the silent collision, and the check names the
  command that says who.

`-tools-only` prints the tool table alone, for anything that was already reading
it.

### The meta-test

`internal/cli/doctor_severity_test.go` walks the check registry and, for every
registered check, requires a row in a table naming the machine state that drives
it to each of the three severities — then runs that table against a real machine
through the real report and asserts the state and the exit code. A check added
without its rows fails by name.

That is a gate over the tests, and it exists because of a measured failure: a
hand-written `doctor` in this fleet had a red test for seven of its ten checks,
three shipped with only a green one, and nothing structurally prevented it. The
rule this enforces is that a check which cannot be driven red in a hermetic test
does not ship.

## `cafe reclaim`

`cafe reclaim` is the command that makes cleanup something the tool does rather
than something a person remembers. It reads the ledger and removes what the
ledger accounts for.

```
$ cafe reclaim
ledger /Users/k/.local/state/cafe/ledger: 1 entr(ies)
resource                         kind       outcome   detail  command
identity-worker-1-g1-postgres-1  container  :dropped  -       docker rm -f identity-worker-1-g1-postgres-1
identity-worker-1-g1_pgdata      volume     :dropped  -       docker volume rm identity-worker-1-g1_pgdata

dry run: 2 resource(s) in 1 ledger entr(ies), 1 container(s), 1 volume(s); nothing was removed and no entry was released

nothing was removed. Re-run with -yes to do it.
```

**It is a dry run unless you pass `-yes`.** The plan is printed in full, one row
per resource, with the exact command that would remove it.

**Containers go before volumes, always.** Docker will not remove a volume a
container still references, so a single leaked *stopped* container pins its named
volume forever. That is the measured mechanism behind the volume leak in this
fleet — not a broken pruner, but an orphan holding a volume nothing could
reclaim. A sweep that prunes volumes first reclaims nothing and says it did, so
the sweep removes the containers, re-lists the volumes, and only then removes
them.

**Each row ends in one of three words, and they are different answers:**

| word | meaning | entry |
|---|---|---|
| `:dropped` | it was there and is gone | released |
| `:missing` | it was not there | released |
| `:failed` | it was there and could not be removed | **kept** |

Only `:failed` keeps the ledger entry, because the entry is the only handle to a
resource that still exists. A sweep that cannot see the daemon is a failure, not
an empty report — "reclaimed 0 things" because nothing was listening is how a
sweeper becomes a thing people stop running. The command exits 1 when something
was `:failed`, so a script can tell a clean sweep from a broken one.

**It never runs a blanket prune.** Every call is scoped by the compose project
label, which carries the ledger's generation, and a resource whose name does not
carry that generation is *refused and reported* rather than removed. `searxng-*`,
the kamal buildkit volume, and any volume with no anywaye worker name are not
reachable from here at all.

A reservation whose holder is gone is reclaimed; one held by a live session is
left completely alone and counted, so that "released nothing" is not read as
"found nothing".

Flags: `-yes`, `-ledger <dir>`, `-generation <n>`.

## `cafe update`

```
cafe update [path]           # merge the template branch into this checkout, move the pin
cafe update --check [path]   # preview only: the source, the tip, what it carries
```

`cafe update` reads `template.yml` — the file that records which template
this checkout was seeded from, which branch it tracks, the full-sha `commit`
pin it has taken work up to, and the ordered candidate `remotes` — and merges
the tracked branch into this checkout with a real three-way `git merge
--no-ff`. Both histories stay reviewable; conflicts are reported with the
files and left in progress rather than resolved by a global rule. There is no
`-X ours`/`-X theirs` anywhere in it, nothing is pushed, and no PR is opened.

The guards, in the order they fire: the pin must be a full 40-character sha
(a branch name or short sha is refused); the checkout must be on a branch
that is not `master` or `main`; the recorded pin must already be an ancestor
of `HEAD`; a relative local candidate with no checkout behind it is skipped
while any other unreadable source is an error; candidates whose tips disagree
without one containing the other are refused rather than chosen between; and
the selected tip must descend from the pin. The pin moves — rewritten
in place so every comment in `template.yml` survives, in the same commit as
the merge — only after the merge commits. A conflict leaves `MERGE_HEAD` and
the old pin with the recovery spelled out (`git merge --abort`, or resolve,
commit, and move the pin by hand). `--check` fetches the candidates and
reports, changing no tracked file and no branch. The `added`/`diverged`/
`removed` lists are hints for the reader about where collisions will be; git
decides the merge.

## `cafe env up`

```
cafe env up [flags] <tier> -- <command> [arguments]
```

`cafe env up` is the only provisioning verb. It writes the stack to the ledger
before creating it, reserves the host ports it will publish from 15000-15999 and
**holds** them for the life of the session, brings the stack up, runs `<command>`
as its **child**, and takes the stack down and releases the entry when the child
is gone.

```
$ cafe env up go -- go test ./...
starting 3 services in identity-dev
tier         go
session      d45866fe7141f6eaa31f8e83142f5de5
generation   1
worktree     /work/identity
project      identity-dev
ports        15020
databases    identity-dev_pgdata,identity-dev_redisdata
ledger       /Users/k/.local/state/cafe/ledger
tier policy  unimplemented: MD12 owns the tier policy; this command records the tier and enforces nothing
...
$ echo $?
0
```

Four things this is and is not:

- **It is the parent, not a sibling.** A sibling that exits leaves the stack up
  and the cleanup to memory. A parent that is interrupted has the kernel take the
  whole group down. That is what makes cleanup guaranteed rather than remembered,
  and it is why there is no separate "stop" verb to remember.
- **The child's exit code is the command's exit code.** A CI job that read 0 out
  of a red suite would be a green badge for a red run, which is the specific thing
  this command exists to make impossible.
- **The tier is recorded, not enforced.** MD12 owns the tier policy and it is owed
  as its own decision. A tier policy this command could apply on its own would be a
  weak gate, and a pass-able gate is worse than no gate — so the receipt says which
  tier ran and says plainly that the policy is not here.
- **The receipt is the seam a gate will read.** A machine-readable statement of the
  session, generation, worktree, project, ports and named volumes, on one
  `CAF_RECEIPT=` line. It never carries a value from the stack's own environment:
  a receipt is read by CI, pasted into a bug, and printed to a log.

`-port` inside the block is honoured — cafe holds the port you typed rather than
replacing it. Outside the block it is refused, by name, because a port nobody
arbitrates is how 21101 and 55432 happened.

## `cafe contract lint`

```
$ cafe contract lint .
OK services/courier/anywaye.yml
INVALID services/billing/anywaye.yml: name: "Billing_Service" does not match "^[a-z][a-z0-9]*(-[a-z0-9]+)*$"
INVALID services/legacy/anywaye.yml: invalid YAML: [10:1] found character '	' that cannot start any token
```

`<path>` is a file or a directory. A directory is walked in lexical order and
every `anywaye.yml` in it is checked; `.git`, `node_modules`, `deps`, `_build`
and `target` are skipped, because they hold other projects' manifests. One line
per manifest — `OK <path>` or `INVALID <path>: <the first error>` — and nothing
else, so the output is greppable and diffable.

Exit code 1 if any manifest is invalid, **or if the path holds no manifest at
all**: a tree nothing was validated in is not a pass.

Every manifest is checked against two things:

1. **The core JSON Schema**, vendored at
   [`internal/contract/schemas/manifest-0.2.json`](internal/contract/schemas/manifest-0.2.json)
   — a byte-identical copy of `anywaye/core`'s schema, pinned by sha256. `cafe`
   never fetches it: see [schemas/README.md](internal/contract/schemas/README.md)
   for the provenance, what each spec bump changes, and the refresh procedure.
2. **The three cross-field rules** from core's
   `docs/manifest-conventions.md` that a JSON Schema cannot state, because it
   validates one field at a time and cannot compare two:

   | Rule | What it rejects |
   |---|---|
   | `convention.event-prefix` | a published event type that does not start with the publisher's own name |
   | `convention.no-self-consume` | a service that subscribes to an event it publishes |
   | `convention.declares-surface` | an `exposes` that names neither an OpenAPI document nor an event |

core documents six such rules. The other three are not implemented, each for a
stated reason: "serves or receives traffic" is not expressible from a manifest
field, `dependencies` are already pinned to anywaye service names by the
schema, and "every consumed type exists in the core catalog" needs a catalog
that core ships as prose rather than as data.

The schema is checked first and the cross-field rules only run on a document
that passed it, because every one of them compares two fields and a document
with a field of the wrong type has nothing to compare.

### The OpenAPI document a manifest names

When a manifest declares `exposes.api`, `cafe contract lint` reads that document
too and reports it as **its own line**, because a document is not a manifest and a
line naming the manifest would send the reader to the wrong file for a rule about
an integer. A manifest that names a document which is not there is reported rather
than skipped: `cafe gen`, the contract tests and pantry all resolve the same path,
and one that resolves to nothing is a contract surface that is declared and not
there.

The numeric rules are quoted from the Kubernetes API conventions rather than
paraphrased, because a paraphrase is how a lint turns into a preference:

| Rule | What it rejects |
|---|---|
| `openapi.int64-must-be-js-safe` | an `int64` whose declared bound leaves `-(2^53) < x < (2^53)` |
| `openapi.no-unsigned-integer` | `uint32`, `uint64` and friends |
| `openapi.no-floating-point` | `type: number`, or a `float` / `double` format |
| `openapi.no-numeric-enum` | an `enum` holding a number |

These are the hazard class that bites one language in the fleet and not the next:
a 64-bit integer is exact in Go and Ruby and a `float64` in TypeScript, and a
float cannot be round-tripped through any of them.

One bound is deliberately **not** enforced: an `int64` with no bound at all is
accepted. Kubernetes asks for `int64` fields to be bounds-checked, and enforcing
the existence of a bound would be red on every unbounded integer in the fleet on
the day it landed, including identity's own `expires_in`. What is enforced is that
a bound somebody *did* write is inside the range a JSON reader can hold.

`cafe contract lint` also derives each document's **stability** from the version in
its path — `openapi/v1.yaml` is stable, `openapi/v1alpha1.yaml` is alpha — and
refuses a stable service that depends on a contract less stable than itself. It is
decidable only when the dependency's own manifest is in the same run, and says so
rather than passing silently on a dependency it cannot see.

## `cafe contract breaking`

```
$ cafe contract breaking openapi/v1.yaml openapi/v1.1.yaml
property-no-delete [SOURCE+JSON+WIRE] #/components/schemas/User/name: Previously present property "name" on schema "User" was deleted without reserving the name "name".
$ echo $?
1
```

Compares two revisions of one OpenAPI document and reports every breaking change,
each tagged with the tiers it breaks:

| Tier | Means |
|---|---|
| `SOURCE` | generated source code stops compiling |
| `JSON` | a serialized payload stops round-tripping |
| `WIRE` | the binary encoding changes |

The tiers are buf's categories, and the row that shows why there are three rather
than one boolean is a **renamed property**: it breaks `SOURCE` and `JSON` and not
`WIRE`, because a binary encoding keys on a field's position and never carried the
name. A single boolean has to answer that as both breaking and not, and whichever
answer it picks is wrong for half of a six-language fleet. Every rule's tiers were
read out of buf's own rule table and the derivation is asserted row by row in
[`internal/contract/tier_test.go`](internal/contract/tier_test.go).

`--tiers source,json` is the default and `WIRE` is opt-in, because no service in
the fleet ships a binary encoding derived from its OpenAPI document yet. Pass
`--tiers wire` for one that will, or `--tiers all`.

### Leaving a mark where a property used to be

OpenAPI has no `reserved` statement — buf's is a protobuf keyword and this format
has no equivalent — so anywaye vendors an extension:

```yaml
components:
  schemas:
    User:
      x-anywaye-reserved-properties: [old_name]
      properties:
        display_name: {type: string}
```

A property removed **with** its name in that list is sanctioned, and a property
removed without it is breaking at every tier. The mark is also what distinguishes
a **rename** from a deletion: a rename is a tombstone plus the new name, and it
lands in `SOURCE+JSON` rather than at `WIRE`.

Two projects arrived at this independently, which is the argument for the design
rather than for the spelling: buf's `FIELD_NO_DELETE_UNLESS_NAME_RESERVED` and
Kubernetes' `// +k8s:deprecated=width,protobuf=3`. The cost of getting it wrong is
a name an old consumer still sends being reused for something else; the cost of a
tombstone is one line naming where it used to live.

## `cafe contract resolve`

```
$ cafe contract resolve '^0.2.0' 0.2.3
yes  ^0.2.0 allows 0.2.3: 0.2.3 is in [0.2.0, 0.3.0)
$ cafe contract resolve '^0.2.0' 0.3.0
no   ^0.2.0 allows 0.3.0: 0.3.0 is not in [0.2.0, 0.3.0)
```

The constraint is `MAJOR.MINOR.PATCH`, optionally prefixed:

| Form | Meaning |
|---|---|
| `1.2.3` | exactly |
| `^1.2.3` | `>=1.2.3 <2.0.0`; on a 0.x service `^0.2.0` is `>=0.2.0 <0.3.0` |
| `~1.2.3` | `>=1.2.3 <1.3.0`, the minor pinned |
| `>=1.2.3` | any version at or above it |

The grammar is deliberately tiny (core's decision D5): no ranges, no `||`, no
`x`-ranges, no prereleases. Leading zeros are rejected — `01.2.3` is two
spellings of one number, and a range that accepts both is a range nobody can
reason about.

Exit code 0 when the version is inside the constraint, 1 when it is not, and 2
when cafe cannot read what you typed.

## Layout

```
cmd/cafe/main.go     thin entrypoint: process in, exit code out
internal/cli/       the router, the registry, one file per subcommand
internal/contract/  manifest loading, schema validation, version resolution
internal/deploy/    the deploy: the pure plan, the exposure refusal, the one seam
internal/dev/       the local stack: planning, rendering, the two seams
internal/gen/       cafe gen: the pure generator, and the drift gate over it
internal/telemetry/ GENERATED: cafe's own copy of `cafe gen telemetry`'s output
internal/emitter/    the OTel wiring: one root span per invocation, and the no-op
internal/mcp/       the MCP server: transports, the served table, the seam
internal/boxd/      the box daemon: the whitelisted route table, the outbound mTLS channel
internal/setup/     cafe box setup: the host bootstrap plan, its walk, and the SSH seam
internal/ledger/    the reclamation ledger: what cafe created, so cleanup is not memory
internal/ports/     the port block, the reservation that holds a port, the prober
internal/resolve/   the tiered catalog's consumer: resolve a manifest's deps, name failures
internal/reclaim/   the sweep's container-runtime seam, scoped to one project
internal/ryuk/      the reaper lease client, for testcontainers' Ryuk
internal/publish/   the write half of the same client: register a project, publish a version, declare an edge
```

`internal/dev` is the only place that knows what a local stack is. `Plan` is a
pure function from a manifest and a registry to a rendered compose document and
a start order — it reads no file, opens no socket and runs no command, which is
why the interesting behaviour of `cafe dev` is covered by tests that start
nothing at all. Everything that does touch the world sits behind one of two
seams there: `Registry` (how a service is run locally, which pantry owns),
`Runtime` (the container runtime, three calls wide) and `PortAllocator` (which host
port a publishing service gets, which only `cafe env up` fills in).

The split follows the pipeline-stage pattern: the router knows nothing about
any subcommand, and each subcommand owns its flags, help and run function, so
one can be read, changed and tested without touching the others. Tests sit next
to what they test as `*_test.go`.

`internal/contract` is where the contract lives, so `cafe init`, `cafe gen` and
`cafe deploy` validate against the same schema with the same messages instead of
each growing its own copy. It has two dependencies, both for jobs the standard
library cannot do:

- `github.com/goccy/go-yaml` — YAML to JSON, with parse errors that carry a
  line and a column, which is what a lint line needs to say.
- `github.com/santhosh-tekuri/jsonschema/v6` — draft 2020-12 validation, the
  draft core's schema declares.

There is no CLI framework: routing is hand-rolled on the standard `flag`
package.

`internal/gen` is `cafe gen`, and it is a pure function like `internal/dev`'s
`Plan`: a manifest and the vendored core telemetry contract go in, a list of
`{path, content}` comes out, and nothing in between touches a file. `cafe gen
telemetry` writes them; `cafe gen chart` writes the Helm chart the service is
deployed from — Chart.yaml, values.yaml, and a templates/ directory carrying
the Deployment, the Service and the Ingress. The ArgoCD Application that
deploys the chart is the substrate's, not the chart's: one static Application
per service, OCI-sourced, applied by install.sh, and a service repository
commits none (anywaye/notes/deployment.md, "Who owns the Application") — a generated
one would be a second Application of the same name in the same namespace,
and the later `kubectl apply` wins. The chart's contract is the fleet's: the
readiness probe is `/readyz`
(baked into the template, not a value — a readiness path that can be edited is
one that can be pointed at `/up`), secrets come by reference (a named Secret,
never an inlined value), the image tag is a value and is never `latest`, and
resources are set.

Every fact in what it writes is **read out of six schemas vendored from
[anywaye/core](https://github.com/anywaye/core)** rather than typed into cafe — the
three per-signal attribute allowlists and their value vocabularies, the fourteen
names core refuses as a measurement attribute, the ten resource attributes, the
thirteen `error.type` values, the span-name grammar, and the `const`s that pin the
no-op path. So a core release that changes an allowlist changes what `cafe gen`
emits, with nobody editing cafe, and `internal/gen/drift_test.go` compares the
regenerated bytes against the committed copy to say so.

Two things about that are worth reading before changing it. **`internal/telemetry/`
is committed generated Go**, so `go build`, `go vet`, `gofmt -l .` and `go test`
all reach it — cafe runs its own generator on its own `anywaye.yml` so the output has
a first consumer whose compiler and test runner are this repository's gate.

That last clause is a claim about the DRIFT GATE as much as about the files, and
it was false until this packet measured it. The gate generated from a test
fixture — courier's manifest — and compared the result against files in a
repository whose own manifest says `name: cafe`, so it was green because it was
measuring courier against courier and `internal/telemetry` shipped declaring
`ServiceName = "courier"` and reading `COURIER_OTEL_ENDPOINT` in the anywaye
platform CLI. The gate now reads this repository's own `anywaye.yml`, which is
what makes it a check on what cafe ships rather than a check that the generator is
deterministic — the thing `internal/gen/telemetry_test.go` already asserted. And the
generated `telemetry_test.go` beside it is not decoration: it is twelve top-level
tests written from the same contract tables as the setup they test, and it is the
standing check that keeps holding after nobody has run `cafe gen` for a year. It
found four real defects in the generated setup while this was being written; the
report says which.

`internal/telemetry` is not cafe's telemetry. It is the CONTRACT — the allowlists,
the span-name bound, the endpoint variable, the no-op path — transcribed out of
core's schemas, with no OTel SDK import and nothing wired to it. It is what a
service compiles against; it is not a client.

**`internal/emitter` is the other half, and it is where cafe's own telemetry
lives.** The generated file deliberately stops at the contract, so something has
to wire the SDK — and this is it. One root span per invocation, kind `internal`,
named `cafe.<command>` per core's span-name grammar, identity on the RESOURCE only,
`error.type` from core's closed vocabulary on failure. It reads the same switches
the generated file names (`CAFE_OTEL_ENDPOINT`, `OTEL_SDK_DISABLED`,
`OTEL_TRACES|METRICS|LOGS_EXPORTER`) and re-expresses the two the contract needs
over an injected lookup — see `contract.go` for why that is a seam and not a
second definition of "disabled".

Two properties are load-bearing and both are measured, not asserted:

- **The precedence covers the SCHEME, and that took a measurement to find.**
  `CAFE_OTEL_ENDPOINT` is documented as winning over the standard
  `OTEL_EXPORTER_OTLP_ENDPOINT`, and courier's worker measured the standard one
  winning instead. Measured here it was half true: the ADDRESS followed the
  precedence, because `newGRPCExporter` passes it explicitly and a caller's
  options are applied after the SDK's own environment read — but the SCHEME did
  not, because the exporter appended a scheme option only when it wanted
  plaintext and so left an `https` endpoint to whatever the standard variable had
  said. With `CAFE_OTEL_ENDPOINT=https://…` beside
  `OTEL_EXPORTER_OTLP_ENDPOINT=http://…`, the first bytes on the connection were
  `50 52 49 20 2a 20 48 54 54 50 2f 32 2e` — the HTTP/2 client preface. The
  standard variable lost the address and won the scheme, and the converse
  direction was already correct, which is what kept it invisible: an asymmetry,
  not a rule. The exporter now states the scheme in both directions, and
  `internal/emitter/precedence_test.go` measures all five cases over the wire on
  two loopback sockets, reading the first bytes of the connection. It sets the
  variables with `t.Setenv` rather than an injected lookup because the SDK reads
  `os.Getenv` itself — which is exactly why the first version of that test passed
  against the broken exporter.
- **Unset is free.** With a switch off, `Install` returns before it constructs an
  exporter, so nothing dials and nothing allocates. `cafe version` on the disabled
  path: **84ns**, against **200ms** on the exporting path with an unresolvable
  collector. The no-op is asserted through a factory that counts its calls, not by
  reading the code, because the property is about what did not happen.
- **A CLI must not be slowed by telemetry.** The flush budget is 200ms because the
  first value tried was 2s and `cafe version` took **2.001s** with no stack running
  — paid by every command a developer runs before `cafe dev` once. core's endpoint
  contract anticipates this and says to lower `timeoutMs`. A deadline on the flush
  is reported as success, because a collector that is absent is telemetry being
  off, which core calls a supported deployment.

The gRPC exporter deliberately omits `WithDialOption(WithBlock())`: the dial is
asynchronous, so constructing an exporter costs nothing, and pointing cafe at an
address nothing listens on is **silent** (measured) rather than a warning on every
command on every machine without a local stack. The SDK's own error handler, which
logs one line per failed export to stderr, is replaced for the same reason —
core pins `noOp.warnings` to `none`.

**What is deliberately not emitted:** metrics and logs (a CLI counter exists once
and is aggregated by whoever runs cafe; a CLI log line is the terminal the user is
already reading), attributes on the invocation span (core's trace allowlist is
`additionalProperties: false` and has nothing for a command name, an argument, a
path or an exit code — all unbounded or caller text), and child spans (a root span
containing no children is what a span is when the work is one opaque call into a
package).

`internal/deploy` is `internal/dev`'s shape with a different payload. `PlanFor` is
a pure function from a project and some options to an ordered list of steps, each
with its exact argv and the reason it is there; everything that touches the world
is behind `Runner`, which is **one method** — run one Kamal subcommand — because
every step of a deploy is a Kamal command and a seam exists to be the smallest
thing that cannot be faked. It runs exactly one command that changes a server,
`kamal setup`, and owns the refusal before it and the report after it. The one
thing worth reading before changing it is
`internal/deploy/exposure.go`'s comment on the *shape* of `kamal config`'s output:
the top level is symbol keys and the accessory blocks are plain strings, and a
check written against the wrong one reports every database in the fleet as safely
unpublished.

The four packages above `internal/dev` are the reclamation half, and they are
separate for one reason: the decisions — what was created, which port belongs to
whom, what a sweep may touch — must be testable on a machine with a live
container runtime and no ability to start one. So `internal/ledger` owns the
record and the `flock`, `internal/ports` owns the block and the prober,
`internal/reclaim` is the only file that knows what a `docker` command line looks
like, and `internal/ryuk` is fifteen lines of client for a reaper nobody in this
repository runs. None of them has a dependency, and each is skipped or faked in
the two places that touch Docker for real.

## `cafe mcp`

`cafe mcp` serves six tools to an agent over the Model Context Protocol. Every
one of them is a read over work cafe already does — `cafe doctor` for the machine,
the contract linter for a manifest, the planner for a stack — rather than a
second implementation of any of them.

| Tool | What it answers | Writes? |
|---|---|---|
| `caf_doctor` | which toolchains this machine has, and whether it can run this project | no |
| `caf_manifest` | what a `anywaye.yml` declares, and its API document's title and version | no |
| `caf_registry` | what the service catalog says, and what the plan leaves out and why | no |
| `caf_dev_plan` | the stack `cafe dev` would build, and where the compose document went | writes the document |
| `caf_dev_up` | bring the local stack up and report what came up | starts containers |
| `caf_dev_down` | take the local stack down and report what is left | stops containers |

Most agent hosts spawn it as a child process and speak stdio, which is the
default. A host configuration looks like this:

```json
{
  "mcpServers": {
    "cafe": { "command": "cafe", "args": ["mcp", "-registry", "../pantry/registry.json"] }
  }
}
```

`-registry` is the service catalog the tools resolve dependencies through, the
same one `cafe dev -registry` takes.

### Three things it does that a stub cannot

**A failing tool is a tool error.** The protocol has two error channels: a
JSON-RPC error, meaning the call did not happen, and a tool result with
`isError`, meaning it happened and did not succeed. An agent reads the second
and carries on; the first means its host has to restart the server. Every
failure inside a tool is the second — including a panic, which is caught and
turned into a tool error rather than taking the process with it.

**An unknown method is answered.** `tools/definitelyNotAMethod` comes back as
JSON-RPC `-32601`. A server that hangs on a method it does not implement is a
server an agent cannot reason about.

**Stdout is the protocol.** On the stdio transport stdout *is* the wire, so a
single stray print is a corrupt stream: the client loses its place and every
later call fails. Everything cafe would have printed — `cafe doctor`'s two tables,
`cafe dev`'s compose document, a container list — goes to stderr instead.

### What the tools do not return

Environment values, anywhere. A registry entry's `environment` is that service's
own configuration, and configuration is where credentials live, so
`caf_registry` reports the variable *names* and `caf_dev_plan` reports the names
of what the compose document sets. The document itself is written to disk and
its path is returned; the `DATABASE_URL` in it carries the project name as its
password and is not in the answer.

The tools are annotated for what they do to the machine: the four reads carry
`readOnlyHint: true`, the two that start or stop containers do not, and both are
`idempotentHint: true` because calling `caf_dev_up` twice reconciles the same
stack rather than starting a second one.

### HTTP, on loopback only

`cafe mcp -transport http` serves the streamable HTTP transport. The listener is
loopback-only and anything else is refused, checked both at the flag and again
where the listener is made. This is not a conservative default: two of the tools
start and stop containers, so a listener reachable from the network would be an
unauthenticated remote shell over the machine's container runtime.

### The library

`internal/mcp` is a thin wrapper over
[github.com/modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk),
the official Go SDK. MCP's wire format has moved several times and a hand-rolled
JSON-RPC framing layer is a month of work and a permanent maintenance burden.
Above that wrapper nothing imports the SDK, so every tool stays an ordinary Go
function with an ordinary Go signature.

## `cafe boxd`

`cafe boxd` is the root daemon that runs on the customer's box, in the same
binary. It is the only thing on the box that can act on a box
capability: it dials **out** to box over mTLS, waits for capabilities,
and redeems them. **It opens no listener.** There is no `-listen` flag — an
undeclared flag is a usage error — and no inbound path exists in the package.
The only connection a box has is the one it makes.

This is AGENTS-FIRST invariant 4 as engineering: *the enforcement lives on
the box, not in the agent's instructions.* An agent steering a deployment can
ask the box for what the box can do; it cannot ask the box to run a command or
read a credential, because neither route exists to be asked for.

| Route | What it does | Mutates? |
|---|---|---|
| `box.status` | the box's enrolled identity, the daemon's version, where its control plane is | no |
| `observe.report` | the observe stream: health, deployed version, certificate expiry, disk, the last backup drill per service and across the box, and which releases are pending against the box's version range — as one machine-readable JSON document. It also carries the per-service settings NAMES and the names sealed for the control plane, which is what lets a box answer which secrets it holds without ever being able to answer what they are worth | no |
| `stack.status` | the substrate's phase, and the phase the install plan reached | no |
| `stack.apply` | apply one phase of the recorded install plan, idempotently | yes |
| `provider.connect` | install a provider credential sealed to this box as a Kubernetes Secret | yes |

### The route table is the security boundary

A capability names a route and carries its parameters. The daemon looks the
route up in a table compiled into the binary and dispatches — or refuses by
name. There is no fallback, no prefix match and no wildcard, and nothing that
arrives over the wire can add a route.

The second half is what a handler can touch. Every route's second parameter is
a `World` interface with one method per capability the box has: no method runs
a process, and no method returns a `Secret`. A route that would need either
cannot be written, because the interface it is handed has no way to ask.

`internal/boxd`'s load-bearing test renders every route in the table through a
recording world and walks each answer's type graph. A Secret-shaped value
reachable from **any** route's answer is a red suite naming the route and the
path. The guard has a negative control that has been watched going red: a route
carrying a `Credential`, a `Secret` behind a pointer, a map of `PrivateKey` and
a `BearerToken` is caught by the same walk. A test nobody has seen fire is not
on offer. And the daemon's private key is loaded per connection and dropped
with it — a daemon whose core dump is a keyring would defeat the point.

`cafe boxd -once` redeems at most one capability and returns, which is one
round trip under an operator's eye rather than a signal they cannot see. The
so and names that command.

The daemon reads its identity and its client certificate from the box-owned
**store** (`/var/lib/cafe/box`, or `$CAF_BOX_DIR`) — the same store every
`cafe box` verb reads and the migration owns. `$CAF_BOX_IDENTITY` is still
honored for a box that has not been migrated to the store yet, with one
deprecation sentence naming the store; when both exist, the **store wins** and
the daemon says so. This is what keeps a re-enroll from leaving the daemon
presenting a certificate the store has already replaced (measured on Contabo
2026-10-15: `x509: unknown certificate authority`). `-identity <path>` still
overrides both, for the operator who means one file.

## Roadmap

Phase 0 (v0, now):

- [x] CLI skeleton: every subcommand wired, flagged, tested
- [x] `cafe version`, `cafe doctor`
- [x] `cafe contract lint`, `cafe contract resolve`
- [x] `anywaye.yml` manifest draft (the schema is owned by
      [anywaye/core](https://github.com/anywaye/core))
- [x] `cafe init` — write the manifest from the core schema
- [x] `cafe new` — scaffold from the template catalog
- [x] `cafe dev` — the local stack: compose from the manifest, health-gated, idempotent
- [ ] `cafe dev` — tilt, and a TUI (see [AGENTS.md](AGENTS.md); both are a later packet)
- [x] `cafe gen telemetry` — the OpenTelemetry setup, its suite, and the endpoint
  declaration, every value read out of core's vendored telemetry schemas
- [x] `cafe gen chart` — the Helm chart: Deployment, Service, `/readyz` probe,
  secrets by reference, rendered and dry-run applied (the Argo Application is
  the substrate's)
- [ ] `cafe gen` — typed SDK clients from an API document (a manifest field does
  not record one yet, so there is nothing to generate them from)
- [ ] `cafe contract fetch` — pull a contract from a registry
- [x] `cafe deploy` — a real Kamal 2 deploy, health-gated, with a refusal for a
      published accessory port and an honest `--dry-run`
- [x] `cafe backup` — a real kamal-backup snapshot, restored into a scratch database
      with the rows asserted and the scratch dropped, four contract refusals before
      any boot, and an honest `--dry-run`
- [x] `cafe mcp` — serve the anywaye tools to agents
- [x] `cafe reclaim` — the reclamation ledger, containers before volumes, tri-state
- [x] `cafe env up` — the only provisioning verb: reserve, run as the parent, reclaim
- [ ] `cafe gate` — the tier policy (MD12), reading `env up`'s receipt. **Not
      written here on purpose:** a gate that can be passed is worse than no gate,
      because it turns "not built yet" into a green badge.
- [ ] Homebrew and Scoop install targets (GitHub Releases + `go install` are the
      install paths until they exist; see [Release](#release))
- [ ] shell completions

See [AGENTS.md](AGENTS.md) for the conventions this repository follows.

## License

MIT. See [LICENSE](LICENSE).

cafe is meant to be consumed as a dependency of a anywaye service, and the whole
point of the service-registry model is that adding a platform dependency does
not change a consumer's own licensing situation. That is why this is MIT and not
a copyleft licence, and it is the reason for it fleet-wide rather than a
per-repository decision.
