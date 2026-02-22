---
status: pending
priority: p2
issue_id: "003"
tags: [code-review, quality, dead-code]
dependencies: []
---

# Dead `runGitWithConfig*` Functions

## Problem Statement

`runGitWithConfig` and `runGitWithConfigAndOptions` have zero callers after the refactor removed the `git -c remote.*.pushurl=authURL` approach. These 16 lines of dead code should be removed.

## Findings

- **Source:** Code Simplicity reviewer
- **Location:** `internal/git/repo.go` lines 530-545
- Previously used by `pushBranchToRemote` for temporary pushurl config
- Replaced by askpass-based approach, no remaining callers

## Proposed Solutions

### Option A: Delete both functions (Recommended)
- **Effort:** Small
- **Risk:** None

## Acceptance Criteria

- [ ] `runGitWithConfig` removed
- [ ] `runGitWithConfigAndOptions` removed
- [ ] No compilation errors

## Technical Details

**Affected files:**
- `internal/git/repo.go` (lines 530-545)

## Work Log

| Date | Action | Learnings |
|------|--------|-----------|
| 2026-02-22 | Identified during code review | Dead code from pushurl config approach removal |
