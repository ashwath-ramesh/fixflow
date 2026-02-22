---
status: pending
priority: p2
issue_id: "002"
tags: [code-review, security, redaction]
dependencies: []
---

# `runGitOutput()` Does Not Accept Secrets for Redaction

## Problem Statement

`runGitOutput()` passes `nil` as the secrets list to `redactSensitiveText()`. While known-token-pattern regexes still fire, caller-specific secrets won't be redacted. Additionally, `cmd.Output()` lets stderr go to the parent process's stderr, meaning credential-containing diagnostics could leak to the terminal. The `*exec.ExitError` can contain stderr that is wrapped in the error without redaction.

## Findings

- **Source:** Security Sentinel (MEDIUM severity)
- **Location:** `internal/git/repo.go` lines 603-614
- Current callers (`LatestCommit`, `CommitAll`, `ConflictedFiles`, `DiffAgainstBase`, etc.) don't pass credentials in args, so practical risk is low
- But the function is a generic facility that could be reused in credential-sensitive contexts

## Proposed Solutions

### Option A: Add `runGitOutputWithOptions` variant (Recommended)
- Accept `gitRunOptions` like other `*WithOptions` functions
- Use `formatGitCommandError` for consistent redaction
- **Pros:** Consistent with rest of codebase, future-proof
- **Cons:** Another function variant (but follows established pattern)
- **Effort:** Small
- **Risk:** Low

### Option B: Wrap `exec.ExitError` stderr through `redactSensitiveText`
- Minimal change, just redact the error's stderr bytes
- **Pros:** Smaller change
- **Cons:** Doesn't address stdout redaction or env propagation
- **Effort:** Small
- **Risk:** Low

## Acceptance Criteria

- [ ] `runGitOutput` error messages are redacted
- [ ] stderr from `exec.ExitError` is redacted before inclusion in error
- [ ] Tests verify redaction in error output

## Technical Details

**Affected files:**
- `internal/git/repo.go` (lines 603-614)

## Work Log

| Date | Action | Learnings |
|------|--------|-----------|
| 2026-02-22 | Identified during security review | Gap in redaction coverage for output-returning git functions |
