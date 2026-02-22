---
status: pending
priority: p3
issue_id: "008"
tags: [code-review, security, defense-in-depth]
dependencies: []
---

# Success-Path Stderr Not Redacted in `runGitOutputAndErrWithOptions`

## Problem Statement

When git commands succeed, stdout and stderr are returned without redaction. Git sometimes writes informational messages to stderr even on success (e.g., "remote: Enumerating objects..."). If any of these messages contain credential material, it would pass through unredacted.

## Findings

- **Source:** Security Sentinel (LOW severity)
- **Location:** `internal/git/repo.go` line 572
- Practical risk is very low — successful git commands don't echo credentials
- Defense-in-depth improvement

## Proposed Solutions

### Option A: Redact stderr on success path
```go
if err == nil {
    return stdout.String(), redactSensitiveText(stderr.String(), opts.secrets), nil
}
```
- **Effort:** Small
- **Risk:** None

## Work Log

| Date | Action | Learnings |
|------|--------|-----------|
| 2026-02-22 | Identified during security review | Defense-in-depth for success path |
