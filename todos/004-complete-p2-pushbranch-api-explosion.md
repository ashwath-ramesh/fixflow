---
status: pending
priority: p2
issue_id: "004"
tags: [code-review, quality, architecture]
dependencies: ["001"]
---

# PushBranch Public API Explosion — 12 Functions, 3 Used

## Problem Statement

There are 12 exported `PushBranch*` functions formed from combinations of WithLease/Captured/ToRemote/WithToken. Only 3 distinct call patterns exist outside the git package. The "Captured" dimension is now a no-op (see TODO 001), making several variants fully redundant.

## Findings

- **Source:** Pattern Recognition + Code Simplicity reviewers
- **Actual callers outside git package:**
  1. `pipeline.go:47` — `PushBranchWithLeaseToRemoteWithToken`
  2. `model.go:422` — `PushBranchWithLeaseCapturedToRemoteWithToken` (identical to #1)
  3. `approve.go:80` — `PushBranchWithLeaseToRemoteWithToken`
  4. `pipeline_auto_pr_integration_test.go:92` — `PushBranchWithLeaseToRemote` (= `...WithToken(""))`)
- **Functions with zero external callers:** `PushBranch`, `PushBranchWithLease`, `PushBranchToRemoteWithToken`, `PushBranchWithLeaseCaptured`, `PushBranchCapturedToRemote`, `PushBranchCapturedToRemoteWithToken`, `PushBranchWithLeaseCapturedToRemote`
- **~60 LOC removable**

## Proposed Solutions

### Option A: Collapse to 2 exported functions (Recommended)
- Keep `PushBranchWithLeaseToRemoteWithToken` (used by all real callers)
- Keep a simple `PushBranch(ctx, dir, branchName)` convenience wrapper
- Remove all other variants
- **Effort:** Medium
- **Risk:** Low (update 1 test, 1 TUI caller)

### Option B: Options struct approach
```go
type PushOptions struct {
    Remote         string
    ForceWithLease bool
    Token          string
}
func PushBranch(ctx context.Context, dir, branchName string, opts PushOptions) error
```
- **Effort:** Medium
- **Risk:** Low

## Acceptance Criteria

- [ ] Only actively-used PushBranch variants remain
- [ ] All callers updated
- [ ] Tests pass

## Technical Details

**Affected files:**
- `internal/git/repo.go` (lines 125-189)
- `internal/pipeline/pipeline.go`, `cmd/autopr/cli/approve.go`, `internal/tui/model.go` (callers)
- `internal/git/repo_test.go`, `internal/pipeline/pipeline_auto_pr_integration_test.go` (tests)

## Work Log

| Date | Action | Learnings |
|------|--------|-----------|
| 2026-02-22 | Identified during code review | Combinatorial API growth over 5 days of rapid development |
