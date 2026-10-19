# Cafaye Agent Skill

Practical guide for building, deploying, and running projects on Cafaye.

## What Cafaye is

Cafaye is the agentic stack: solve hard problems once, choose the right tools for the work, and share reliable solutions across projects. The `cafe` CLI is your hands — it manages projects, dev boxes, pipelines, deploys, and secrets. The agent is the primary interface; the human steers.

## Defaults to assume

- Base URL for ship service: `https://ship.cafaye.com` (or `SHIP_URL` env var)
- Base URL for identity: `https://auth.cafaye.com` (or `IDENTITY_URL` env var)
- Dev boxes run on instabox (ephemeral, auto-suspend, pay-when-in-use)
- Prod runs on the user's Cafaye box (Contabo or self-hosted)
- All backends share one Postgres cluster
- The manifest is `cafaye.yml` (credential-free, committable)

## Getting started (agent flow)

### 1. Register yourself

```bash
cafe agent register --name "Your Name"
```

This prints a claim URL. The human opens it and clicks Approve (one click). This is also where we get their email. You are now a claimed agent principal.

### 2. Create a project

```bash
cafe project create --name my-saas --owner <owner-id>
```

Or via ship API:
```bash
curl -s -X POST $SHIP_URL/v1/projects/ -H "Content-Type: application/json" \
  -d '{"name":"my-saas","owner_id":"<owner-id>"}'
```

### 3. Provision a dev box

```bash
cafe devbox create --name my-saas
# Wraps: instabox create --name my-saas --auto-suspend 30m
```

The dev box is an instabox Linux VM with the universal template (git, Node 22, Python, Go, Rust, Docker, Claude Code, Codex). It auto-suspends after 30 idle minutes.

### 4. Connect your repo

```bash
# Move your local repo to the dev box (with uncommitted work + history)
# Follow the instabox-move-repo skill

# Or clone from GitHub/Forgejo
instabox ssh my-saas -- 'git clone https://github.com/you/my-saas.git /data/workspace/my-saas'
```

### 5. Add cafaye services

```bash
# Hosted (cafaye operates on Contabo)
cafe project add-service <project-id> identity
cafe project add-service <project-id> courier

# Or via API
curl -s -X POST $SHIP_URL/v1/projects/<id>/services -d '{"service":"identity"}'
```

### 6. Set up secrets

```bash
# Set secrets on the dev box (via instabox Vault)
instabox vault set ANTHROPIC_API_KEY  # value from stdin
instabox vault set DATABASE_URL

# Or via cafe secret (local dev)
cafe secret set DATABASE_URL
```

### 7. Develop on the dev box

```bash
# SSH into the dev box
instabox ssh my-saas

# Inside the box:
cd /data/workspace/my-saas
npm install  # or whatever your stack uses
npm test     # run tests
```

Your agent (Claude Code, Codex, etc.) can SSH into the box and do the same:
```bash
instabox exec my-saas -- 'cd /data/workspace/my-saas && npm test'
```

### 8. Deploy to prod

```bash
# Agent asks: "Deploy to prod? (yes/no)"
# Human says yes in their editor.
cafe deploy  # commits values change → Argo syncs on your box
# Argo waits for /readyz before completing rollout
```

Deploy is reversible: `git revert <commit>` + Argo sync.

### 9. Monitor and fix

```bash
# Agent observes via cafe mcp (read-only)
cafe mcp  # serves read-only tools: Argo status, pod logs, Loki queries

# On error, agent diagnoses, opens fix commit, human approves, deploy
```

### 10. Add a second project

Repeat from step 2. Each project gets its own dev box, services, and deploy target.

## CLI command reference

### Project management
- `cafe project create --name <name> --owner <id>` — Create a project
- `cafe project list --owner <id>` — List projects
- `cafe project get <id>` — Get project details

### Dev boxes
- `cafe devbox create --name <name>` — Create a dev box (wraps instabox)
- `instabox ssh <name>` — SSH into a dev box
- `instabox exec <name> -- <cmd>` — Run a command on a dev box
- `instabox ls` — List dev boxes
- `instabox rm <name>` — Delete a dev box

### Pipeline
- `cafe pipeline get <project-id>` — Show the project's pipeline
- `cafe pipeline update <project-id> --recipe <name>` — Update pipeline (install a preset)
- `cafe pipeline advance <project-id>` — Advance to the next stage
- `cafe pipeline recipes` — List available pipeline recipes

Recipes: `software` (Backlog → In Progress → Review → Done), `deploy` (Build → Test → Deploy → Verify), `contribution` (Backlog (open) → Implement → Verify → Review → Done), `default` (single Work stage)

### Gates
- `cafe gate decide <project-id> <stage> --status passed|failed --by <id> --note <text>` — Decide a gate

### Tasks
- `cafe task create <project-id> --title <title> --kind <kind>` — Create a task
- `cafe task list <project-id> [--kind] [--status]` — List tasks
- `cafe task get <project-id> <task-id>` — Get task details
- `cafe task status <project-id> <task-id> <status>` — Update status
- `cafe task stage <project-id> <task-id> <stage>` — Move to stage
- `cafe task approve <project-id> <task-id> [--by] [--note]` — Approve a task
- `cafe task reject <project-id> <task-id> [--by] [--note]` — Reject a task
- `cafe task merge <project-id> <task-id>` — Merge a task

### Landings (contributions)
- `cafe landing create <project-id> --kind <kind> --title <title>` — Submit a landing
- `cafe landing list <project-id> [--kind] [--status]` — List landings
- `cafe landing get <project-id> <landing-id>` — Get landing details
- `cafe landing approve <project-id> <landing-id> [--by] [--note]` — Approve
- `cafe landing reject <project-id> <landing-id> [--by] [--note]` — Reject
- `cafe landing merge <project-id> <landing-id>` — Merge

### Board
- `cafe board view <project-id>` — Record that you viewed the board
- `cafe board attention <project-id>` — Check if attention is needed

### Deploy
- `cafe deploy` — Deploy to prod (commits values change → Argo sync)
- `cafe backup` — Backup the box

### Secrets
- `cafe secret set <name>` — Set a secret (value from stdin)
- `cafe secret get <name>` — Get a secret
- `cafe secret list` — List secrets

### Observability
- `cafe mcp` — Serve read-only MCP tools (Argo status, pod logs, Loki queries)

### Skills
- `cafe skills install` — Install/update the cafaye agent skill file
- `cafe skills install --root <path>` — Install to a specific root

## Common patterns

### Auto-advance on green
The agent can implement "auto-advance when tests pass" by:
1. Running tests on the dev box
2. If tests pass, calling `cafe pipeline advance <project-id>`
3. If tests fail, opening a fix commit

### Feature flags
```bash
# Create a flag
curl -s -X POST $SHIP_URL/v1/projects/<id>/flags -d '{"name":"dark-mode","enabled":true,"rollout_percent":50}'

# Evaluate for a user
curl -s -X POST $SHIP_URL/v1/projects/<id>/flags/dark-mode/evaluate -d '{"user_id":"user123"}'
```

### Dev databases
```bash
# Provision a dev database
curl -s -X POST $SHIP_URL/v1/projects/<id>/devdb
# Returns: connection_string, host, port, username, password
```

### Landings (contributions)
```bash
# Submit a landing (replaces PR)
cafe landing create <project-id> --kind change --title "add dark mode"

# Approve (human or agent)
cafe landing approve <project-id> <landing-id> --by <id> --note "looks good"

# The landing creates a linked task automatically
```

## Key principles

1. **No local clone required** — Everything happens on dev boxes (instabox) or prod boxes.
2. **Agents do the editing** — Humans speak to agents; agents SSH into dev boxes and edit code.
3. **Reversible prod** — Every deploy is a git commit. Revert = `git revert` + Argo sync.
4. **Auto-suspend dev boxes** — Pay only when active. Near-zero cost when idle.
5. **Forgejo canonical** — All code lives on self-hosted Forgejo. GitHub is an optional mirror.
6. **In-chat approval** — Agent asks "deploy? yes/no" in your editor. You say yes. Done.
7. **Shared Postgres** — One cluster, per-service databases and roles.
8. **Sane defaults** — Single generic "Work" stage. Install a recipe when you want structure.
9. **Evidence-backed** — Every gate transition carries proof (test results, build output, etc.).
10. **Attention model** — "Finished while you were away" — the board shows what changed since you last looked.
## Private repos and packages

Cafaye repos and packages are private. To build on instabox or deploy on Contabo:

### Instabox (dev boxes)
1. Set up GitHub auth on your laptop: `gh auth login`
2. Put your GitHub token into the instabox Vault:
   ```
   instabox-basic-setup  # scans your machine, lets you pick tokens/files
   ```
   This puts `GH_TOKEN` into every box (current and future).
3. Boxes can then clone private repos: `gh repo clone cafaye/<repo>`
4. Boxes can pull private images: `docker login ghcr.io -u <user> -p $GH_TOKEN`

### Contabo (prod)
- Pull secrets (`ghcr-pull-secret`) exist in every namespace
- They use a GitHub token with `write:packages` scope (implies `read:packages`)
- ArgoCD pulls charts from `oci://ghcr.io/cafaye/charts` using `ghcr-charts-repo` secret
- No changes needed — already configured for private packages

### Forgejo (internal git host)
- Forgejo runs on Contabo at `git.cafaye.com`
- All cafaye code is pushed here as the canonical source
- GitHub is an optional mirror
- Instabox boxes can reach Forgejo (public URL)
- Boxes need a Forgejo token to push: set via instabox Vault or `cafe secret`
