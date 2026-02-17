# FixFlow

Autonomous issue-to-code daemon. FixFlow watches your GitLab, GitHub, and Sentry issues,
then uses an LLM (Claude or Codex CLI) to plan, implement, test, and push fixes — ready
for human approval.

## How It Works

```
                              ┌─────────────┐
  GitLab webhook ────────────>│             │
  GitHub/Sentry sync loop ──>│  ff daemon   │──> clone repo, create branch
                              │             │    plan → implement → review → test
                              └──────┬──────┘
                                     │
                              ┌──────▼──────┐
                              │   SQLite    │  jobs, sessions, artifacts
                              └──────┬──────┘
                                     │
                              ┌──────▼──────┐
                              │  LLM CLI    │  claude --print / codex --full-auto
                              └─────────────┘
```

**Pipeline per issue:**

1. **Plan** — LLM analyzes the issue and produces an implementation plan.
2. **Implement** — LLM writes code in a git worktree following the plan.
3. **Code Review** — LLM reviews its own changes. If not approved, loops back to implement (up to `max_iterations`).
4. **Test** — Runs the project's test command. On pass, pushes the branch.
5. **Ready** — Waits for human `approve` / `reject` via CLI or TUI.

## Prerequisites

### Build

- Go 1.23+
- Git
- SQLite (via `modernc.org/sqlite`, no CGO required)

### LLM Provider (pick one)

**OpenAI Codex CLI:**

```bash
npm install -g @openai/codex
```

Requires an OpenAI API key with access to Codex models. Set `OPENAI_API_KEY` in your environment.
An OpenAI Plus or Pro subscription, or API credits, is needed.

**Anthropic Claude CLI:**

```bash
npm install -g @anthropic-ai/claude-code
```

Requires an Anthropic API key. Set `ANTHROPIC_API_KEY` in your environment.
A Claude Max subscription or API credits is needed.

Configure which provider to use in `fixflow.toml`:

```toml
[llm]
provider = "codex"   # or "claude"
```

### Source Tokens

- **GitHub:** Fine-grained PAT with `Contents: Read and write` + `Issues: Read-only`
- **GitLab:** Project access token with `api` scope
- **Sentry:** Auth token with `event:read` + `project:read`

## Quick Start

```bash
# Build
go build -o ff ./cmd/fixflow

# Initialize config + database
./ff init

# Edit the config
./ff config   # opens fixflow.toml in $EDITOR

# Set tokens via env vars (never commit these)
export GITLAB_TOKEN="glpat-..."
export GITHUB_TOKEN="ghp_..."
export SENTRY_TOKEN="sntrys_..."
export FIXFLOW_WEBHOOK_SECRET="your-secret"

# Start the daemon
./ff start

# Or run in foreground for debugging
./ff start -f
```

## Configuration

FixFlow uses a single `fixflow.toml` file. Running `ff init` creates a starter template.

```toml
db_path = "fixflow.db"
repos_root = ".repos"
log_level = "info"         # debug, info, warn, error
# log_file = "fixflow.log" # uncomment to log to file

[daemon]
webhook_port = 8080
max_workers = 3
max_iterations = 3         # implement<->review retries
sync_interval = "5m"       # GitHub/Sentry polling interval
pid_file = "fixflow.pid"

[tokens]
# Prefer env vars: GITLAB_TOKEN, GITHUB_TOKEN, SENTRY_TOKEN

[sentry]
base_url = "https://sentry.io"

[llm]
provider = "claude"        # claude or codex

[[projects]]
name = "my-project"
repo_url = "git@gitlab.com:org/repo.git"
test_cmd = "make test"
base_branch = "main"

  [projects.gitlab]
  base_url = "https://gitlab.com"
  project_id = "12345"

  # [projects.github]
  # owner = "org"
  # repo = "repo"

  # [projects.sentry]
  # org = "my-org"
  # project = "my-project"

  # [projects.prompts]
  # plan = "prompts/plan.md"
  # implement = "prompts/implement.md"
  # code_review = "prompts/code_review.md"
```

### Environment Variable Overrides

| Env Var | Overrides |
|---------|-----------|
| `GITLAB_TOKEN` | `[tokens] gitlab` |
| `GITHUB_TOKEN` | `[tokens] github` |
| `SENTRY_TOKEN` | `[tokens] sentry` |
| `FIXFLOW_WEBHOOK_SECRET` | `[daemon] webhook_secret` |

> **Note:** `GITHUB_TOKEN` requires a fine-grained PAT with `Contents: Read and write` + `Issues: Read-only`
> scoped to the target repo. With read-only contents access, the daemon will work end-to-end but
> branch push will fail — you'll need to push branches manually after approving jobs.

## Setting Up a Project

### GitLab (webhook-driven)

1. Add a `[[projects]]` block with `[projects.gitlab]` containing your `project_id`.
2. In GitLab, go to **Settings > Webhooks** and add:
   - **URL:** `http://<your-host>:8080/webhook`
   - **Secret token:** same value as `FIXFLOW_WEBHOOK_SECRET`
   - **Trigger:** Issue events
3. When an issue is opened or reopened, FixFlow creates a job automatically.

### GitHub (polling)

1. Add `[projects.github]` with `owner` and `repo`.
2. FixFlow polls for open issues every `sync_interval`.
3. New issues are picked up and processed automatically.

### Sentry (polling)

1. Add `[projects.sentry]` with `org` and `project`.
2. FixFlow polls for unresolved issues every `sync_interval`.

## CLI Commands

| Command | Description |
|---------|-------------|
| `ff init` | Create config template and initialize database |
| `ff start [-f]` | Start the daemon (`-f` for foreground) |
| `ff stop` | Gracefully stop the daemon |
| `ff status` | Show daemon status and job counts by state |
| `ff list [--project X] [--state Y]` | List jobs with optional filters |
| `ff logs <job-id>` | Show full session history, artifacts, and tokens |
| `ff approve <job-id>` | Approve a job in `ready` state |
| `ff reject <job-id> [-r reason]` | Reject a job in `ready` state |
| `ff retry <job-id> [-n notes]` | Re-queue a `failed` or `rejected` job |
| `ff config` | Open `fixflow.toml` in `$EDITOR` |
| `ff tui` | Interactive terminal dashboard (see below) |

All commands accept `--json` for machine-readable output and `-v` for debug logging.

### Job ID Prefix Matching

`ff list` shows an 8-character short job ID (e.g. `2dad8b6b`). All action commands
accept a prefix of any length — just enough to be unambiguous:

```bash
ff logs 2dad          # matches ff-job-2dad8b6b...
ff approve 2d         # works if only one job starts with "2d"
ff reject 2dad8b6b    # full short ID also works
```

For automation, use `ff list --json` which returns full job IDs.

## TUI Dashboard

`ff tui` launches an interactive terminal UI with keyboard navigation.

**Level 1 — Job List:** Dashboard header showing daemon status, sync interval,
worker count, and job state counters. Job table shows short job ID, state, project,
issue source (e.g. GitHub #1), iteration progress, and truncated issue title.

**Level 2 — Job Detail:** Full job metadata plus a pipeline session table showing each step
(plan, implement, code_review) with status, token usage, and duration. Press `d` to view the
git diff of changes.

**Level 3 — Session Detail:** Full LLM output rendered as styled markdown with syntax-highlighted
code blocks (via glamour). Press `tab` to toggle between the input prompt and output response.

| Key | Action |
|-----|--------|
| `j/k` | Navigate up/down |
| `enter` | Drill into selected item |
| `esc` | Go back one level |
| `tab` | Toggle input/output (session view) |
| `d` | View git diff (job detail) |
| `u/d` | Half-page scroll (session/diff view) |
| `r` | Refresh data |
| `q` | Quit |

## Job States

```
queued → planning → implementing → reviewing → testing → ready
                        ↑              │
                        └──────────────┘  (review requested changes)

ready → approved    (human approves)
ready → rejected    (human rejects)
any   → failed      (error)
failed/rejected → queued  (ff retry)
```

## Custom Prompts

Override default prompts per project in `fixflow.toml`:

```toml
[projects.prompts]
plan = "prompts/plan.md"
implement = "prompts/implement.md"
code_review = "prompts/code_review.md"
```

Prompt templates support these placeholders:

| Placeholder | Value |
|-------------|-------|
| `{{title}}` | Issue title |
| `{{body}}` | Issue body (sanitized) |
| `{{plan}}` | Plan artifact content |
| `{{review_feedback}}` | Previous review + test output |

## Health Check

The daemon exposes a health endpoint on the webhook port:

```bash
curl http://localhost:8088/health
```

Returns JSON with `status`, `uptime_seconds`, and `job_queue_depth`.

## Architecture

```
cmd/fixflow/           CLI (cobra)
internal/
  config/              TOML config loader with env overrides
  daemon/              Daemon lifecycle, PID file, signal handling
  db/                  SQLite store (WAL mode, reader/writer pools)
  git/                 Clone, branch, worktree, push operations
  issuesync/           GitHub + Sentry polling sync loop
  llm/                 CLI provider interface (claude, codex)
  pipeline/            Plan → implement → review → test orchestration
  tui/                 Bubbletea interactive dashboard
  webhook/             GitLab webhook handler
  worker/              Concurrent job processing pool
```

## Development

```bash
go build ./...
go vet ./...
go test ./...
```

## Resetting the Database

```bash
rm -f fixflow.db
./ff init   # re-creates schema
```
