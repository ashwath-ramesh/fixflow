---
status: pending
priority: p2
issue_id: "001"
tags: [code-review, quality, dead-code]
dependencies: []
---

# Dead `runGitCaptured*` Functions and `captured` Parameter

## Problem Statement

After the refactor, all `runGitCaptured*` functions are exact aliases of their `runGit*` counterparts. The behavioral distinction (captured vs streaming output) was eliminated when `runGit` switched from `cmd.Stdout = os.Stdout` to `cmd.CombinedOutput()`. The `captured` boolean parameter in `pushBranchToRemote` is now dead — both branches execute identical code.

This is actively misleading: readers will assume "Captured" means something different from non-captured variants, but it does not.

## Findings

- **Source:** Pattern Recognition + Code Simplicity reviewers (confirmed by all agents)
- `runGitCaptured` (line 577) → delegates to `runGitWithOptions` → identical to `runGit`
- `runGitCapturedWithOptions` (line 581) → delegates to `runGitWithOptions` → identical to `runGitWithOptions`
- `runGitCapturedWithConfig` (line 585) → zero callers
- `runGitCapturedWithConfigAndOptions` (line 589) → only called by above
- `pushBranchToRemote` `captured` parameter (line 191) → both branches call same function
- **~25 LOC of dead code**

## Proposed Solutions

### Option A: Delete all `runGitCaptured*` functions (Recommended)
- Replace 2 actual call sites with `runGitWithOptions` directly
- Remove `captured` parameter from `pushBranchToRemote`
- **Pros:** Eliminates confusion, reduces API surface
- **Cons:** None
- **Effort:** Small
- **Risk:** Low

## Acceptance Criteria

- [ ] All `runGitCaptured*` functions removed
- [ ] `captured` parameter removed from `pushBranchToRemote`
- [ ] All callers updated
- [ ] Tests pass

## Technical Details

**Affected files:**
- `internal/git/repo.go` (lines 577-601, 191-226)
- `internal/git/rebase.go` (line 29 — uses `runGitCapturedWithOptions`)

## Work Log

| Date | Action | Learnings |
|------|--------|-----------|
| 2026-02-22 | Identified during code review | Behavioral distinction lost during CombinedOutput refactor |
