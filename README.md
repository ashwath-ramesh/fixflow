# AutoPR

Autonomous issue-to-PR daemon. AutoPR watches your GitHub, GitLab, and Sentry issues,
then uses an LLM (Claude or Codex CLI) to plan, implement, test, and push fixes — ready
for human approval.

## Table of Contents

1. [Install](#1-install)
2. [Quick Start](#2-quick-start)
3. [Prerequisites](#3-prerequisites)
4. [Configuration](#4-configuration)
5. [Setting Up a Project](#5-setting-up-a-project)
6. [CLI Commands](#6-cli-commands)
7. [TUI Dashboard](#7-tui-dashboard)
8. [Job States](#8-job-states)
9. [Custom Prompts](#9-custom-prompts)
10. [Health Check](#10-health-check)
11. [Architecture](#11-architecture)
12. [Development](#12-development)
13. [Resetting](#13-resetting)

## 1. Install

**macOS:**

```bash
curl -fsSL https://raw.githubusercontent.com/ashwath-ramesh/autopr/master/scripts/install.sh | bash
```

Upgrade later from CLI:

```bash
ap upgrade
ap upgrade --check
```

**From source (any platform with Go 1.26+):**

```bash
go build -o ap ./cmd/autopr && mv ap /usr/local/bin/
```

## 2. Quick Start

### 2.1 Install an LLM CLI

AutoPR shells out to an LLM CLI tool. Pick one and install it:

```bash
# Option A: OpenAI Codex CLI (needs OPENAI_API_KEY)
npm install -g @openai/codex

# Option B: Anthropic Claude Code CLI (needs ANTHROPIC_API_KEY)
npm install -g @anthropic-ai/claude-code
```

### 2.2 Set up AutoPR

```bash
ap init                    # creates ~/.config/autopr/ with config + credentials
ap config                  # opens config in $EDITOR — add your projects
```

### 2.3 Connect your issues

AutoPR needs a source of issues to work on. Configure at least one in `config.toml`:

- **GitHub** — add `[projects.github]` with `owner` and `repo`. AutoPR polls for open issues and uses **labels** for gating. By default, only issues labeled `autopr` are processed, and `autopr-skip` skips processing.
- **GitLab** — add `[projects.gitlab]` with `project_id`. AutoPR polls for open issues (and accepts webhooks) and uses **labels** for gating. By default, only issues labeled `autopr` are processed, and `autopr-skip` skips processing.
- **Sentry** — add `[projects.sentry]` with `org` and `project`. AutoPR polls for unresolved issues and uses **team assignment** for gating. By default, only issues assigned to the `#autopr` team are processed.

> **Safe defaults:** AutoPR will not process any issues until you label them `autopr` (GitHub/GitLab) or assign them to the `#autopr` team (Sentry). This prevents accidentally flooding the job queue on first start. Set `include_labels = []` in the relevant source block and `exclude_labels = []` in `[[projects]]`, or `assigned_team = ""`, to opt out and process all issues.

See [Section 5](#5-setting-up-a-project) for full setup details.

### 2.4 Start the daemon

```bash
# macOS (persists across reboots):
ap service install
ap service status

# manual mode:
ap start                   # background daemon
ap start -f                # foreground (for debugging)
```

> **Note:** On a MacBook, the daemon suspends when the machine sleeps — it does not
> process issues overnight with the lid closed. It resumes instantly on wake.
> For true 24/7 operation, run `ap` on an always-on server (Linux VM, cloud instance, etc.).

### 2.5 Watch it work

```bash
ap tui                     # interactive dashboard
ap list                    # list all jobs
ap issues                  # list synced issues + eligibility
ap logs <job-id>           # view LLM output for a job
ap approve <job-id>        # approve and create PR
```

### What happens

1. AutoPR polls GitHub/Sentry (or receives GitLab webhooks) for new issues
2. For each issue: **Plan** → **Implement** → **Code Review** → **Test** → **Ready**
3. You review the result with `ap tui` or `ap logs`, then `ap approve` or `ap reject`
4. On approve, a PR is created. Set `auto_pr = true` to skip manual approval.

## 3. Prerequisites

### 3.1 LLM CLI Tool

AutoPR does not call LLM APIs directly. It shells out to a CLI in
non-interactive mode and parses the output. You need one installed and authenticated:

| Provider | Install | Auth |
|----------|---------|------|
| OpenAI Codex | `npm install -g @openai/codex` | `OPENAI_API_KEY` env var |
| Anthropic Claude | `npm install -g @anthropic-ai/claude-code` | `ANTHROPIC_API_KEY` env var |

Configure in `config.toml`:

```toml
[llm]
provider = "codex"   # or "claude"
```

### 3.2 Source Tokens

| Source | Token type | Scopes |
|--------|-----------|--------|
| GitHub | Fine-grained PAT | `Contents: Read and write`, `Issues: Read-only` |
| GitLab | Project access token | `api` |
| Sentry | Auth token | `event:read`, `project:read` |

Set via `ap init` or env vars (`GITHUB_TOKEN`, `GITLAB_TOKEN`, `SENTRY_TOKEN`).

## 4. Configuration

AutoPR uses `~/.config/autopr/config.toml`. Running `ap init` creates it interactively.

```toml
log_level = "info"         # debug, info, warn, error

[daemon]
webhook_port = 9847
max_workers = 3
max_iterations = 3         # implement<->review retries
sync_interval = "5m"       # GitHub/Sentry polling interval
# auto_pr = false          # set true to auto-create PRs after tests pass

[llm]
provider = "codex"         # codex or claude

[notifications]
# webhook_url = "https://example.com/hook"               # generic JSON webhook
# slack_webhook = "https://hooks.slack.com/services/..." # Slack incoming webhook
# desktop = true                                          # macOS desktop notifications
# triggers = ["needs_pr", "failed", "pr_created", "pr_merged"]
# triggers = [] disables all notifications

[[projects]]
name = "my-project"
repo_url = "git@github.com:org/repo.git"
test_cmd = "go test ./..."
base_branch = "main"
  # exclude_labels = ["autopr-skip"] # optional: issues with these labels are ignored
  # exclude_labels = [] # optional: disable default skip label

  [projects.github]
  owner = "org"
  repo = "repo"
  # include_labels = ["autopr"] # optional: ANY match; empty means no include gate
```

### 4.1 File Locations

AutoPR follows the [XDG Base Directory Specification](https://specifications.freedesktop.org/basedir-spec/latest/):

| Directory | Default | Contents |
|-----------|---------|----------|
| Config | `~/.config/autopr/` | `config.toml`, `credentials.toml` |
| Data | `~/.local/share/autopr/` | `autopr.db`, `repos/` |
| State | `~/.local/state/autopr/` | `autopr.log`, `autopr.pid`, `version-check.json` |

Override with `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, or `XDG_STATE_HOME`. Run `ap paths` to see resolved locations.

You can also set explicit paths in config:

```toml
db_path = "/custom/path/autopr.db"
repos_root = "/custom/path/repos"
log_file = "/custom/path/autopr.log"
```

### 4.2 Environment Variable Overrides

| Env Var | Overrides |
|---------|-----------|
| `GITLAB_TOKEN` | `[tokens] gitlab` |
| `GITHUB_TOKEN` | `[tokens] github` |
| `SENTRY_TOKEN` | `[tokens] sentry` |
| `AUTOPR_WEBHOOK_SECRET` | `[daemon] webhook_secret` |

> **Note:** `GITHUB_TOKEN` requires a fine-grained PAT with `Contents: Read and write` + `Issues: Read-only`
> scoped to the target repo. With read-only contents access, the daemon will work end-to-end but
> branch push will fail — you'll need to push branches manually after approving jobs.

### 4.3 Notifications

AutoPR emits notifications from a durable DB outbox when jobs hit key states:

- `needs_pr` (job reached `ready`)
- `failed`
- `pr_created`
- `pr_merged`

Channels:

- `notifications.webhook_url`: sends JSON payload (`event`, `job_id`, `state`, `issue_title`, `pr_url`, `project`, `timestamp`)
- `notifications.slack_webhook`: sends Slack incoming webhook message
- `notifications.desktop = true`: sends native macOS desktop notification (`osascript`)

Test your setup:

```bash
ap notify --test
ap notify --test --json
```

## 5. Setting Up a Project

### 5.1 GitHub (polling, label-gated)

1. Add `[projects.github]` with `owner` and `repo`.
2. **Default:** only issues with the `autopr` label are processed (`include_labels` defaults to `["autopr"]` in `[projects.github]`) and issues with `autopr-skip` are excluded (`exclude_labels` defaults to `["autopr-skip"]` in `[[projects]]`).
3. Exclusion has precedence: if an issue matches both include and exclude labels, it is skipped.
4. Add the `autopr` label to any GitHub issue you want AutoPR to work on.
5. Matching is case-insensitive and uses ANY configured label.
6. To use a different include label: set `include_labels = ["my-label"]`.
7. To use different skip labels: set `exclude_labels = ["on-hold"]`.
8. To process ALL open issues (opt-out): set `include_labels = []` in `[projects.github]` and `exclude_labels = []` in `[[projects]]`.
9. AutoPR polls for open issues every `sync_interval`.

### 5.2 GitLab (polling + webhook, label-gated)

1. Add a `[[projects]]` block with `[projects.gitlab]` containing your `project_id`.
2. **Default:** only issues with the `autopr` label are processed (`include_labels` defaults to `["autopr"]` in `[projects.gitlab]`) and issues with `autopr-skip` are excluded (`exclude_labels` defaults to `["autopr-skip"]` in `[[projects]]`).
3. Exclusion has precedence: if an issue matches both include and exclude labels, it is skipped.
4. Add the `autopr` label to any GitLab issue you want AutoPR to work on.
5. To use a different include label: set `include_labels = ["my-label"]`.
6. To use different skip labels: set `exclude_labels = ["on-hold"]`.
7. To process ALL open issues (opt-out): set `include_labels = []` and `exclude_labels = []` (`include_labels` in `[projects.gitlab]`, `exclude_labels` in `[[projects]]`).
8. GitLab webhooks still call the same gate rules used by the poller.
9. Optionally add a webhook in GitLab (**Settings > Webhooks**) for instant processing:
   - **URL:** `http://<your-host>:9847/webhook`
   - **Secret token:** same value as `AUTOPR_WEBHOOK_SECRET`
   - **Trigger:** Issue events

### 5.3 Sentry (polling, team-gated)

1. Add `[projects.sentry]` with `org` and `project`.
2. **Default:** only issues assigned to the `#autopr` team are processed (`assigned_team` defaults to `"autopr"`).
3. Set up the team in Sentry:
   - Create a team (e.g. "autopr") under **Settings > Teams**.
   - Grant the team access to the relevant projects.
   - Assign individual issues or user feedback to `#autopr` via the assignee dropdown.
4. To use a different team: set `assigned_team = "my-team"`.
5. To process ALL unresolved issues (opt-out): set `assigned_team = ""`.

## 6. CLI Commands

| Command | Description |
|---------|-------------|
| `ap init` | Interactive setup wizard |
| `ap start [-f]` | Start the daemon (`-f` for foreground) |
| `ap service install` | Install + enable macOS launchd auto-start service |
| `ap service uninstall` | Disable + remove macOS launchd service |
| `ap service status` | Show macOS launchd service install/load/run state |
| `ap upgrade [--check]` | Check for and install the latest `ap` release |
| `ap stop` | Gracefully stop the daemon |
| `ap status` | Show daemon status and job counts |
| `ap list [--project X] [--state Y]` | List jobs with optional filters |
| `ap issues [--project X] [--eligible|--ineligible]` | List synced issues and eligibility |
| `ap logs <job-id>` | Show LLM output, artifacts, and tokens |
| `ap approve <job-id>` | Approve a job and create PR |
| `ap reject <job-id> [-r reason]` | Reject a job |
| `ap cancel <job-id> \| --all` | Cancel a queued/running job (or all) |
| `ap retry <job-id> [-n notes]` | Re-queue a failed/rejected/cancelled job |
| `ap config` | Open config in `$EDITOR` |
| `ap paths` | Show where files are stored |
| `ap notify --test` | Send a test notification to configured channels |
| `ap tui` | Interactive terminal dashboard |

All commands accept `--json` for machine-readable output and `-v` for debug logging.
`ap start` checks for new releases at most once every 24h and prints a non-blocking upgrade notice when available.
On macOS with `ap service install`, `ap stop` sends `SIGTERM` but launchd `KeepAlive` may restart it; run `ap service uninstall` to fully disable auto-start/restart.

### 6.1 Job ID Prefix Matching

`ap list` shows an 8-character short job ID (e.g. `2dad8b6b`). All action commands
accept a prefix of any length — just enough to be unambiguous:

```bash
ap logs 2dad          # matches ap-job-2dad8b6b...
ap approve 2d         # works if only one job starts with "2d"
ap reject 2dad8b6b    # full short ID also works
```

For automation, use `ap list --json` which returns full job IDs.

## 7. TUI Dashboard

`ap tui` launches an interactive terminal UI with keyboard navigation.

**Level 1 — Job List:** Dashboard header showing daemon status, sync interval,
worker count, job state counters, and synced issue summary (`Issues: X synced, Y eligible, Z skipped`).
Job table shows short job ID, state, project, issue source (e.g. GitHub #1), iteration progress,
and truncated issue title.

**Level 2 — Job Detail:** Full job metadata plus a pipeline session table showing each step
(plan, implement, code_review) with status, token usage, and duration. Press `d` to view the
git diff of changes.

**Level 3 — Session Detail:** Full LLM output rendered as styled markdown with syntax-highlighted
code blocks (via glamour). Press `tab` to toggle between the input prompt and output response.

Auto-refresh runs every 5 seconds in job list and job detail views. Auto-refresh pauses in
session detail and diff views to avoid content jumping.

| Key | Action |
|-----|--------|
| `j/k` | Navigate up/down |
| `enter` | Drill into selected item |
| `esc` | Go back one level |
| `tab` | Toggle input/output (session view) |
| `d` | View git diff (job detail) |
| `c` | Cancel selected/current job (list/detail) |
| `u/d` | Half-page scroll (session/diff view) |
| `r` | Refresh immediately |
| `q` | Quit |

## 8. Job States

See the **[interactive job state diagram](https://ashwath-ramesh.github.io/autopr/job_state.html)** — hover, click, and filter by actor (daemon / user / LLM / config).

- **Actors:** `daemon` (automatic orchestration), `llm` (AI review decision), `user` (CLI action), `config` (auto_pr).
- **Terminal states:** `approved` is final; `failed`, `rejected`, and `cancelled` are retryable via `ap retry`.

## 9. Custom Prompts

Override default LLM prompts per project with custom markdown files:

```toml
[projects.prompts]
plan = "/path/to/plan.md"
implement = "/path/to/implement.md"
code_review = "/path/to/code_review.md"
```

Prompt templates support these placeholders:

| Placeholder | Value |
|-------------|-------|
| `{{title}}` | Issue title |
| `{{body}}` | Issue body (sanitized) |
| `{{plan}}` | Plan artifact content |
| `{{review_feedback}}` | Previous review + test output |
| `{{human_notes}}` | Human guidance from `ap retry -n` (plan step only) |

## 10. Health Check

The daemon exposes a health endpoint on the webhook port:

```bash
curl http://localhost:9847/health
```

Returns JSON with `status`, `uptime_seconds`, and `job_queue_depth`.

## 11. Architecture

```
cmd/autopr/            CLI (cobra)
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

## 12. Development

```bash
go build ./...
go vet ./...
go test ./...
```

## 13. Resetting

```bash
# Reset database only
rm -f ~/.local/share/autopr/autopr.db
ap init

# Full clean slate
rm -rf ~/.config/autopr ~/.local/share/autopr ~/.local/state/autopr
ap init
```
