---
status: pending
priority: p3
issue_id: "009"
tags: [code-review, security, consistency]
dependencies: []
---

# `DeleteRemoteBranch` Lacks Auth Session

## Problem Statement

`DeleteRemoteBranch` calls `runGit` without an auth session — no token parameter, no askpass env. If the remote requires authentication (HTTPS with no credential helper), this operation would fail. If the remote URL still has embedded credentials, error output won't be redacted through the auth secrets path.

## Findings

- **Source:** Security Sentinel (LOW severity)
- **Location:** `internal/git/repo.go` lines 238-246
- Currently relies on git credential helpers or previously-sanitized remotes
- Known token patterns in `formatGitCommandError` still provide some redaction

## Proposed Solutions

### Option A: Add `token` parameter and auth session
- Consistent with `Fetch`, `FetchBranch`, `pushBranchToRemote`
- **Effort:** Small
- **Risk:** Low (requires updating callers)

### Option B: Document as intentionally unauthenticated
- Add comment explaining the function relies on remote URL being pre-sanitized
- **Effort:** Small
- **Risk:** None

## Work Log

| Date | Action | Learnings |
|------|--------|-----------|
| 2026-02-22 | Identified during security review | Inconsistency with other remote-interacting functions |
