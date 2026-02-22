---
status: pending
priority: p2
issue_id: "006"
tags: [code-review, quality, duplication]
dependencies: []
---

# Triplicated PR Creation Logic

## Problem Statement

Three nearly identical PR creation functions exist across three files. They all follow the same structure: check branch name, switch on GitHub/GitLab, check token, call `CreateGitHubPR` or `CreateGitLabMR`. The only difference is the `draft` parameter. Risk of divergence as changes are made to one copy but not others.

## Findings

- **Source:** Pattern Recognition specialist
- `cmd/autopr/cli/approve.go:126-166` — `createPR` (uses `approveDraft` CLI flag)
- `internal/pipeline/pipeline.go:460-483` — `createPRForProject` (hardcodes `draft=false`)
- `internal/tui/model.go:576-596` — `createTUIPR` (passes `draft` argument)
- **Pre-existing issue**, not introduced by this branch, but worth noting

## Proposed Solutions

### Option A: Extract shared `CreatePR` function (Recommended)
```go
func CreatePR(ctx context.Context, cfg *config.Config, proj *config.ProjectConfig,
    job db.Job, head, title, body string, draft bool) (string, error)
```
- **Effort:** Medium
- **Risk:** Low

## Acceptance Criteria

- [ ] Single PR creation function used by all three paths
- [ ] `draft` parameter passed from call sites
- [ ] Tests pass

## Work Log

| Date | Action | Learnings |
|------|--------|-----------|
| 2026-02-22 | Identified during code review | Pre-existing duplication, not introduced by this branch |
