---
status: pending
priority: p3
issue_id: "007"
tags: [code-review, security, defense-in-depth]
dependencies: []
---

# Expand Known Token Redaction Patterns

## Problem Statement

The `knownTokenPatterns` regex list covers GitHub PATs, GitLab PATs, Slack tokens, and oauth2 URLs, but misses some GitLab token variants. While the primary redaction layer (explicit secret replacement) handles exact values, the regex layer is defense-in-depth for unexpected appearances.

## Findings

- **Source:** Security Sentinel (LOW severity)
- **Location:** `internal/git/repo.go` lines 26-32
- Missing patterns: GitLab deploy tokens (`gldt-`), CI job tokens (`glcbt-`), pipeline trigger tokens (`glptt-`)

## Proposed Solutions

### Option A: Add missing patterns
```go
regexp.MustCompile(`gldt-[A-Za-z0-9_-]{20,}`),   // GitLab deploy tokens
regexp.MustCompile(`glcbt-[A-Za-z0-9_-]{20,}`),  // GitLab CI job tokens
regexp.MustCompile(`glptt-[A-Za-z0-9_-]{20,}`),  // GitLab pipeline trigger tokens
```
- **Effort:** Small
- **Risk:** None

## Acceptance Criteria

- [ ] New patterns added
- [ ] Tests verify new patterns redact correctly

## Work Log

| Date | Action | Learnings |
|------|--------|-----------|
| 2026-02-22 | Identified during security review | Defense-in-depth gap for GitLab token variants |
