---
status: pending
priority: p3
issue_id: "011"
tags: [code-review, quality, cleanup]
dependencies: []
---

# Minor Cleanup: Dead Assignment and Double URL Parse

## Problem Statement

Two minor code quality issues:
1. `_ = out` dead assignment at line 116 of `repo.go` (should use `_, err :=` on the original call)
2. `prepareGitRemoteAuth` re-parses the same URL that `prepareRemoteURL` already parsed, to extract username/password for the fallback path

## Findings

- **Source:** Code Simplicity reviewer
- Dead assignment is noise — trivial fix
- Double URL parse is correctness-neutral but adds complexity; could be eliminated by returning structured data from `prepareRemoteURL`

## Proposed Solutions

### Fix dead assignment
Change `out, err :=` to `_, err :=` at line 116

### Optional: Return struct from `prepareRemoteURL`
```go
type parsedRemote struct {
    url      string
    username string
    password string
    secrets  []string
}
```
- **Effort:** Small (dead assignment) / Medium (struct refactor)
- **Risk:** None

## Work Log

| Date | Action | Learnings |
|------|--------|-----------|
| 2026-02-22 | Identified during code review | Minor cleanup opportunities |
