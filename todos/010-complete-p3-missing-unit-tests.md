---
status: pending
priority: p3
issue_id: "010"
tags: [code-review, testing]
dependencies: []
---

# Missing Unit Tests for New Auth Functions

## Problem Statement

Several new functions lack direct unit test coverage, even though they are exercised indirectly through higher-level tests.

## Findings

- **Source:** Pattern Recognition specialist
- Missing tests:
  1. `prepareRemoteURL` edge cases: SSH URL, username-only URL, special chars in password
  2. `redactURLUserInfo` edge cases: URLs with ports, query strings, fragments
  3. `dedupeNonEmpty`: direct tests for empty input, single item, whitespace items
  4. `prepareGitRemoteAuth` with credential URL and no explicit token (the fallback path at lines 375-388)
  5. `warnCredentialURL` idempotency (warning emitted only once)

## Proposed Solutions

### Option A: Add targeted unit tests for each function
- **Effort:** Medium
- **Risk:** None

## Acceptance Criteria

- [ ] `prepareRemoteURL` edge cases covered
- [ ] `dedupeNonEmpty` directly tested
- [ ] Credential URL fallback path tested
- [ ] `redactURLUserInfo` edge cases covered

## Work Log

| Date | Action | Learnings |
|------|--------|-----------|
| 2026-02-22 | Identified during code review | Functions exercised indirectly but lack direct coverage |
