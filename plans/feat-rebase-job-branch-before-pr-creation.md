# feat: Rebase Job Branch on Latest Base Branch Before PR Creation

> GitHub Issue: #79
> Branch: `feat/rebase-before-pr`
> Type: enhancement
> Risk: Medium (state machine changes, new LLM step, schema migrations)

---

## Overview

After the LLM pipeline completes (plan → implement → review → test → ready) but before `git push` + PR creation, fetch and rebase the job branch onto the latest base branch. Handle conflicts using a 3-tier approach:

- **Tier 1 (Clean rebase):** No conflicts → re-run `test_cmd` → push PR (~80% of cases)
- **Tier 2 (LLM-resolved):** Conflicts totaling < 20 lines → LLM resolves → re-test → push PR (~15%)
- **Tier 3 (Human needed):** Conflicts > 20 lines → reject with detailed error (~5%)

This delivers the core thesis: turn on the server at night, come back to ready PRs in the morning — even when `main` has moved.

---

## Problem Statement

Currently, `maybeAutoPR()` (`pipeline.go:319-353`) pushes and creates a PR without checking if the base branch has diverged since clone time. If `main` received commits while the job ran, the PR may have merge conflicts that block auto-merge. Worse, the user has to manually rebase and re-test — defeating the purpose of an autonomous daemon.

---

## Proposed Solution

Insert a rebase sub-pipeline inside `maybeAutoPR()` at `pipeline.go:329`, **before** `PushBranch()`. Add two new explicit states (`rebasing`, `resolving_conflicts`) to the job state machine for visibility and crash recovery.

```
Current:  ... → test → ready → [push + PR] → approved

Proposed: ... → test → ready → [fetch + rebase] → ...
                                     |
                          ┌──────────┼──────────────┐
                          │          │              │
                     No-op rebase  Clean rebase  Conflicts
                     (up to date)  (no conflicts)    │
                          │          │          ┌────┼────┐
                          │        re-test    ≤20 lines  >20 lines
                          │          │          │         │
                          │        pass?    LLM resolve  reject
                          │       ╱    ╲        │
                          │     yes     no    re-test
                          │      │      │    pass? fail?
                          │      │   reject     │    │
                          ▼      ▼              ▼    ▼
                        push + PR           push  reject
```

---

## Technical Approach

### Architecture

#### State Machine Changes

```
# Current transitions (db/jobs.go:21-31)
ready → approved, rejected

# New transitions
testing     → rebasing                           (rebasing is new entry from runTestingAndReadiness)
rebasing    → resolving_conflicts, ready, failed, cancelled  (ready = rebase done, tests pass)
resolving_conflicts → ready, failed, cancelled               (ready = LLM resolved, tests pass)
```

Note: `ready` retains its existing `→ approved, rejected` paths for the manual approval flow. The rebase flow transitions `testing → rebasing → ready → approved`.

#### New Files

| File | Purpose |
|------|---------|
| `internal/git/rebase.go` | Git rebase operations: fetch, rebase, abort, conflict detection |
| `internal/pipeline/rebase.go` | Rebase pipeline step: orchestrates the 3-tier logic |
| `internal/pipeline/conflict.go` | Conflict marker parser and complexity estimator |
| `templates/prompts/conflict_resolve.md` | Default LLM prompt for conflict resolution |
| `internal/git/rebase_test.go` | Unit tests for git rebase operations |
| `internal/pipeline/rebase_test.go` | Unit tests for rebase pipeline step |
| `internal/pipeline/conflict_test.go` | Unit tests for conflict parser |

#### Modified Files

| File | Changes |
|------|---------|
| `internal/db/schema.go` | 3 migrations: jobs state CHECK, llm_sessions step CHECK, artifacts kind CHECK |
| `internal/db/jobs.go` | ValidTransitions, IsCancellableState, StepForState, DisplayState, RecoverInFlightJobs, resolveJobOrderExpression |
| `internal/pipeline/pipeline.go` | `maybeAutoPR()` calls new rebase step before push |
| `internal/tui/model.go` | stateStyle map, filterStateCycle, DisplayStep |

---

### Implementation Phases

#### Phase 1: Git Operations (`internal/git/rebase.go`)

New exported functions following the existing `runGit`/`runGitOutput`/`runGitCaptured` pattern in `repo.go:93-132`:

```go
// internal/git/rebase.go

// FetchBranch fetches a specific branch from origin.
func FetchBranch(ctx context.Context, dir, branch string) error

// RebaseOnto attempts to rebase HEAD onto origin/<branch>.
// Returns (conflicted=true, nil) if rebase halted due to conflicts.
// Returns (false, nil) on clean rebase or no-op.
// Returns (false, error) on unexpected failure.
func RebaseOnto(ctx context.Context, dir, branch string) (conflicted bool, err error)

// RebaseAbort aborts an in-progress rebase, restoring the branch to its pre-rebase state.
func RebaseAbort(ctx context.Context, dir string) error

// RebaseContinue continues a rebase after conflicts have been resolved.
// Returns (moreConflicts=true, nil) if additional commits produce new conflicts.
func RebaseContinue(ctx context.Context, dir string) (moreConflicts bool, err error)

// ConflictedFiles returns the list of files with unmerged conflicts.
func ConflictedFiles(ctx context.Context, dir string) ([]string, error)

// IsRebaseInProgress checks if the worktree is in the middle of a rebase.
func IsRebaseInProgress(dir string) bool

// StageFile marks a file as resolved during a rebase.
func StageFile(ctx context.Context, dir, file string) error

// ConfigureDiff3 sets merge.conflictStyle=diff3 for richer conflict markers.
func ConfigureDiff3(ctx context.Context, dir string) error
```

**Implementation details:**

- `RebaseOnto` needs a **new git runner** that captures stdout and stderr separately (the existing `runGitCaptured` merges them). Add `runGitSeparated(ctx, dir, args) (stdout, stderr string, exitCode int, err error)` to `repo.go`. Conflict detection relies on stderr containing `"CONFLICT"`.
- `RebaseContinue` must set `GIT_EDITOR=true` env var to skip the commit message editor.
- `FetchBranch` uses the existing `runGitCaptured` pattern.
- No-op detection: Compare HEAD SHA before and after `git rebase`. If identical, it's a no-op.

**Acceptance criteria:**
- [ ] `FetchBranch` fetches `origin/<branch>` successfully
- [ ] `RebaseOnto` returns `(false, nil)` for clean rebase
- [ ] `RebaseOnto` returns `(true, nil)` when conflicts exist
- [ ] `RebaseOnto` returns `(false, error)` for unexpected failures (corrupt repo, invalid ref)
- [ ] `RebaseAbort` restores the branch to pre-rebase HEAD
- [ ] `ConflictedFiles` returns correct list of unmerged files
- [ ] `IsRebaseInProgress` correctly detects `.git/rebase-merge` or `.git/rebase-apply`
- [ ] `ConfigureDiff3` enables diff3 conflict style in the worktree
- [ ] All functions respect `context.Context` cancellation
- [ ] New `runGitSeparated` helper captures stdout/stderr separately

---

#### Phase 2: Conflict Parser (`internal/pipeline/conflict.go`)

Pure functions for parsing git conflict markers and estimating complexity.

```go
// internal/pipeline/conflict.go

// ConflictRegion represents a single conflict hunk within a file.
type ConflictRegion struct {
    FilePath  string
    StartLine int
    EndLine   int
    Ours      string // content between <<<<<<< and =======
    Base      string // content between ||||||| and ======= (diff3 only)
    Theirs    string // content between ======= and >>>>>>>
}

// ParseConflicts reads a file with conflict markers and returns all conflict regions.
func ParseConflicts(filePath string, content []byte) []ConflictRegion

// CountConflictLines returns the total number of lines within all conflict
// regions (between and including <<<<<<< and >>>>>>>). This is the metric
// used for the 20-line tier threshold.
func CountConflictLines(regions []ConflictRegion) int

// HasConflictMarkers returns true if the content contains any unresolved markers.
func HasConflictMarkers(content []byte) bool
```

**Threshold definition:** "Conflict lines" = total lines between (and including) `<<<<<<<` and `>>>>>>>` markers across ALL conflicted files in the rebase. Boundary: exactly 20 lines is Tier 3 (strictly `< 20` for Tier 2).

**Acceptance criteria:**
- [ ] `ParseConflicts` extracts ours/theirs/base sections from diff3 format
- [ ] `ParseConflicts` handles multiple conflict hunks in a single file
- [ ] `CountConflictLines` counts correctly: 19 lines = Tier 2, 20 lines = Tier 3
- [ ] `HasConflictMarkers` catches all three markers
- [ ] Parser handles edge cases: empty ours, empty theirs, nested markers (malformed)
- [ ] Pure functions with no side effects — fully unit-testable

---

#### Phase 3: Schema Migrations (`internal/db/schema.go`)

Three table-rebuild migrations following the exact pattern from `migrateJobsForCancelledState()` (`schema.go:178-258`):

**Migration 1: `migrateJobsForRebasingState()`**
- Check: `if strings.Contains(sqlText, "'rebasing'") { return nil }`
- Add `'rebasing','resolving_conflicts'` to `jobs.state` CHECK constraint
- Recreate all indexes including the partial unique index `idx_jobs_one_active_per_issue`
- Update the partial unique index WHERE clause: `WHERE state NOT IN ('approved', 'rejected', 'failed', 'cancelled')` — no change needed since `rebasing` and `resolving_conflicts` are active states

**Migration 2: `migrateSessionsForRebaseStep()`**
- Add `'conflict_resolution'` to `llm_sessions.step` CHECK constraint (`schema.go:67`)
- Follow pattern from `migrateSessionsForCancelledStatus()` (`schema.go:260+`)

**Migration 3: `migrateArtifactsForRebaseKind()`**
- Add `'rebase_conflict'` to `artifacts.kind` CHECK constraint (`schema.go:90`)
- Same table-rebuild pattern

**Also update `schemaSQL` constant** (`schema.go:4-122`) to include the new values for fresh database creation.

**Acceptance criteria:**
- [ ] Migrations are idempotent (safe to run multiple times)
- [ ] Existing data is preserved during table rebuild
- [ ] Fresh databases include new CHECK values
- [ ] Migrations are called from `createSchema()` in the correct order

---

#### Phase 4: State Machine Updates (`internal/db/jobs.go`)

**ValidTransitions** (`jobs.go:21-31`):
```go
var ValidTransitions = map[string][]string{
    "queued":                {"planning", "cancelled"},
    "planning":              {"implementing", "failed", "cancelled"},
    "implementing":          {"reviewing", "failed", "cancelled"},
    "reviewing":             {"implementing", "testing", "failed", "cancelled"},
    "testing":               {"ready", "implementing", "rebasing", "failed", "cancelled"},
    "ready":                 {"approved", "rejected"},
    "rebasing":              {"resolving_conflicts", "ready", "failed", "cancelled"}, // NEW
    "resolving_conflicts":   {"ready", "failed", "cancelled"},                 // NEW
    "failed":                {"queued"},
    "rejected":              {"queued"},
    "cancelled":             {"queued"},
}
```

**IsCancellableState** (`jobs.go:34-41`): Add `"rebasing"` and `"resolving_conflicts"`.

**StepForState** (`jobs.go:44-57`):
```go
case "rebasing":
    return ""
case "resolving_conflicts":
    return "conflict_resolution"
```

**DisplayState** (`jobs.go:60-75`):
```go
case "rebasing":
    return "rebasing"
case "resolving_conflicts":
    return "resolving"
```

**RecoverInFlightJobs** (`schema.go:~390`): Add `'rebasing','resolving_conflicts'` to the WHERE clause. Recovery resets to `'ready'` (not `'queued'`) since the code changes are intact — we just need to re-attempt the rebase.

**resolveJobOrderExpression** (`jobs.go:339-366`): Add CASE entries for new states with appropriate sort ordering (between `testing` and `ready`).

**CancelAllCancellableJobs**: Add new states to the IN clause.

**Acceptance criteria:**
- [ ] All transition paths validated: `testing → rebasing`, `rebasing → resolving_conflicts`, etc.
- [ ] New states are cancellable
- [ ] Crash recovery resets rebasing/resolving_conflicts jobs to `ready`
- [ ] Sort order places rebasing between testing and ready
- [ ] Display labels are user-friendly

---

#### Phase 5: Rebase Pipeline Step (`internal/pipeline/rebase.go`)

The core orchestration logic, called from `maybeAutoPR()`:

```go
// internal/pipeline/rebase.go

// runRebaseBeforeReady performs the rebase-before-ready step.
// It transitions through rebasing/resolving_conflicts states and
// returns nil on success (job transitions back to ready for push).
// Returns an error if the job should be failed/rejected.
func (r *Runner) runRebaseBeforeReady(
    ctx context.Context,
    jobID string,
    issue db.Issue,
    projectCfg *config.ProjectConfig,
    workDir string,
) error
```

**Flow inside `runRebaseBeforeReady`:**

1. **Transition** `testing → rebasing`
2. **Configure diff3** via `git.ConfigureDiff3(ctx, workDir)`
3. **Fetch** via `git.FetchBranch(ctx, workDir, projectCfg.BaseBranch)`
   - On error: abort, `failJob` from `rebasing`
4. **Record HEAD SHA** before rebase (for no-op detection)
5. **Rebase** via `git.RebaseOnto(ctx, workDir, projectCfg.BaseBranch)`
6. **Tier routing:**
   - **No-op** (HEAD unchanged): transition `rebasing → ready`, return nil (skip re-test)
   - **Clean rebase** (no conflicts): re-run `test_cmd`, store artifact, on pass transition `rebasing → ready`; on fail `failJob` from `rebasing`
   - **Conflicts detected:**
     a. Read conflicted files via `git.ConflictedFiles()`
     b. Parse all conflicts via `ParseConflicts()` and `CountConflictLines()`
     c. **Tier 3** (>= 20 lines): `git.RebaseAbort()`, `failJob` from `rebasing` with descriptive error listing files and line counts
     d. **Tier 2** (< 20 lines): transition `rebasing → resolving_conflicts`, invoke LLM:
        - Build prompt from `conflict_resolve.md` template with full file contents + conflict markers + issue context
        - Call `r.invokeProvider(ctx, jobID, "conflict_resolution", 0, workDir, prompt)`
        - The LLM writes resolved files directly to the worktree
        - Verify no conflict markers remain via `HasConflictMarkers()`
        - `git.StageFile()` for each resolved file
        - `git.RebaseContinue()`
        - If `--continue` produces more conflicts: `git.RebaseAbort()`, `failJob` from `resolving_conflicts` (treat as Tier 3)
        - If clean: re-run `test_cmd`, store artifact
        - On test pass: transition `resolving_conflicts → ready`
        - On test fail: `failJob` from `resolving_conflicts`
7. **All failure paths** call `git.RebaseAbort()` if `git.IsRebaseInProgress()` before failing
8. **Safety commit** after successful conflict resolution: `git.CommitAll(ctx, workDir, "autopr: resolve rebase conflicts")`
9. **Context cancellation**: If `ctx.Err() != nil`, abort rebase with a fresh 5-second context

**Artifact storage:**
- Store conflict details as `rebase_conflict` artifact (files, line counts, LLM response)
- Re-run test output stored as new `test_output` artifact (latest wins via `GetLatestArtifact`)

**Loop prevention:** No retry logic. Max 1 rebase attempt. If `RebaseContinue` produces more conflicts, abort immediately.

**Acceptance criteria:**
- [ ] Tier 1 clean rebase: tests re-run, job proceeds to PR
- [ ] Tier 1 no-op: tests skipped, job proceeds to PR
- [ ] Tier 1 test failure after clean rebase: job fails with test output
- [ ] Tier 2 LLM resolution succeeds: tests pass, job proceeds to PR
- [ ] Tier 2 LLM resolution fails: rebase aborted, job fails
- [ ] Tier 2 rebase --continue produces more conflicts: aborted, job fails
- [ ] Tier 2 test failure after LLM resolution: job fails with test output
- [ ] Tier 3 conflicts too large: rebase aborted, job fails with conflict details
- [ ] All failure paths call `RebaseAbort` if rebase is in progress
- [ ] Context cancellation aborts rebase cleanly
- [ ] Artifact stored for conflict resolution attempts
- [ ] LLM session created with `conflict_resolution` step
- [ ] `force-with-lease` push used since rebase rewrites history

---

#### Phase 6: Integration into `maybeAutoPR()` (`internal/pipeline/pipeline.go`)

Modify `maybeAutoPR()` at `pipeline.go:319-353`:

```go
func (r *Runner) maybeAutoPR(ctx context.Context, jobID string, issue db.Issue, projectCfg *config.ProjectConfig) error {
    job, err := r.store.GetJob(ctx, jobID)
    if err != nil {
        return err
    }
    if job.State != "ready" {
        return nil
    }

    // NEW: Rebase before push.
    if err := r.runRebaseBeforeReady(ctx, jobID, issue, projectCfg, job.WorktreePath); err != nil {
        return err // Job already transitioned to failed inside runRebaseBeforeReady
    }

    // Re-fetch job to get potentially updated state after rebase.
    job, err = r.store.GetJob(ctx, jobID)
    if err != nil {
        return err
    }

    // Push branch to remote before creating PR.
    // Use force-with-lease since rebase rewrites commit history.
    if err := git.PushBranchForceWithLease(ctx, job.WorktreePath, job.BranchName); err != nil {
        return fmt.Errorf("push branch for auto-PR: %w", err)
    }

    // ... rest of existing PR creation logic unchanged ...
}
```

Also add `PushBranchForceWithLease` to `git/repo.go`:
```go
func PushBranchForceWithLease(ctx context.Context, dir, branchName string) error {
    return runGit(ctx, dir, "push", "--force-with-lease", "origin", branchName)
}
```

**Note:** `--force-with-lease` is required because `git rebase` rewrites commit history. Plain `git push` would fail with "non-fast-forward" after a rebase. `--force-with-lease` is safe because it refuses to push if the remote has unexpected changes.

**Acceptance criteria:**
- [ ] `maybeAutoPR` calls rebase before push
- [ ] Push uses `--force-with-lease` after rebase
- [ ] Job re-fetched after rebase to get current state
- [ ] Existing non-rebase auto-PR path still works when base branch hasn't changed

---

#### Phase 7: TUI Updates (`internal/tui/model.go`)

**stateStyle map** (`model.go:37-50`):
```go
"rebasing":            lipgloss.NewStyle().Foreground(lipgloss.Color("135")),  // purple
"resolving_conflicts": lipgloss.NewStyle().Foreground(lipgloss.Color("202")),  // orange-red
"resolving":           lipgloss.NewStyle().Foreground(lipgloss.Color("202")),  // display name
```

**filterStateCycle** (`model.go:70-80`): Add `"rebasing"` between `"active"` and `"ready"`.

**ListJobs "active" filter** (`jobs.go:~295`): Add `'rebasing','resolving_conflicts'` to the active state set.

**Acceptance criteria:**
- [ ] New states display with distinct colors in TUI
- [ ] `rebasing` appears in filter cycle
- [ ] `ap list --state active` includes rebasing and resolving_conflicts jobs

---

#### Phase 8: Conflict Resolution Prompt (`templates/prompts/conflict_resolve.md`)

```markdown
You are an expert software engineer. Resolve the merge conflicts in the files below.

<issue>
Title: {{title}}
{{body}}
</issue>

<conflict_details>
The job branch was rebased onto the latest base branch ({{base_branch}}).
The following files have merge conflicts:

{{conflict_files}}
</conflict_details>

Instructions:
- Read each conflicted file in the working directory
- Understand the intent of BOTH sides of each conflict
- Resolve each conflict by combining both changes correctly
- Write the resolved file content (without any conflict markers)
- Do NOT add, remove, or modify code outside the conflict regions
- Do NOT add explanatory comments
- Preserve all imports, formatting, and whitespace conventions
```

Allow override via `ProjectPrompts.ConflictResolve` in config.

**Acceptance criteria:**
- [ ] Prompt includes issue context (title, body)
- [ ] Prompt lists conflicted files
- [ ] Prompt instructs LLM to write resolved files directly
- [ ] Prompt is overridable per-project via config

---

#### Phase 9: PR Body Enhancement (`internal/pipeline/pipeline.go`)

Update `BuildPRContent()` (`pipeline.go:382+`) to mention if rebase and/or conflict resolution occurred:

```go
if rebaseArtifact, err := store.GetLatestArtifact(ctx, job.ID, "rebase_conflict"); err == nil {
    body.WriteString("\n---\n**Note:** This PR was rebased onto the latest base branch.")
    body.WriteString(" Merge conflicts were automatically resolved by LLM.\n")
    body.WriteString("<details><summary>Conflict resolution details</summary>\n\n")
    body.WriteString(rebaseArtifact.Content)
    body.WriteString("\n</details>\n")
}
```

**Acceptance criteria:**
- [ ] PR body notes when rebase occurred
- [ ] PR body includes conflict resolution details in a collapsible section
- [ ] No change to PR body when no rebase was needed

---

#### Phase 10: Tests

**Git operations tests** (`internal/git/rebase_test.go`):
Follow existing pattern from `repo_test.go` using `t.TempDir()`, `t.Parallel()`, real git repos.

| Test | Scenario |
|------|----------|
| `TestFetchBranch` | Fetches new commits from bare remote |
| `TestRebaseOnto_Clean` | Clean rebase with no conflicts |
| `TestRebaseOnto_NoOp` | Branch already up to date |
| `TestRebaseOnto_Conflict` | Returns `(true, nil)` on conflicts |
| `TestRebaseOnto_InvalidRef` | Returns error for non-existent branch |
| `TestRebaseAbort_RestoresState` | HEAD matches pre-rebase SHA after abort |
| `TestRebaseContinue_Success` | Completes after resolving all conflicts |
| `TestRebaseContinue_MoreConflicts` | Returns `(true, nil)` for multi-commit conflicts |
| `TestConflictedFiles` | Returns correct list of unmerged files |
| `TestIsRebaseInProgress` | Detects `.git/rebase-merge` |
| `TestStageFile` | `git add` marks file as resolved |
| `TestConfigureDiff3` | Sets conflict style to diff3 |

**Conflict parser tests** (`internal/pipeline/conflict_test.go`):

| Test | Scenario |
|------|----------|
| `TestParseConflicts_SingleHunk` | One conflict region |
| `TestParseConflicts_MultipleHunks` | Three conflicts in one file |
| `TestParseConflicts_Diff3Format` | Extracts base section |
| `TestParseConflicts_EmptyOurs` | Ours side is empty (deletion) |
| `TestCountConflictLines_Threshold` | 19 lines = Tier 2, 20 lines = Tier 3 |
| `TestHasConflictMarkers` | Detects all three marker types |

**Pipeline rebase tests** (`internal/pipeline/rebase_test.go`):
Follow `pipeline_clone_test.go` pattern with stub providers and real git repos.

| Test | Scenario |
|------|----------|
| `TestRunRebaseBeforePR_CleanRebase` | Tier 1: clean rebase, tests pass |
| `TestRunRebaseBeforePR_NoOp` | No-op: skip re-test |
| `TestRunRebaseBeforePR_CleanRebase_TestFail` | Tier 1: clean rebase, tests fail → job failed |
| `TestRunRebaseBeforePR_LLMResolve` | Tier 2: LLM resolves conflicts, tests pass |
| `TestRunRebaseBeforePR_LLMFails` | Tier 2: LLM errors → rebase aborted, job failed |
| `TestRunRebaseBeforePR_Tier3Reject` | Tier 3: >20 lines → rebase aborted, job failed |
| `TestRunRebaseBeforePR_FetchFails` | Network error on fetch → job failed |
| `TestRunRebaseBeforePR_CancelDuringRebase` | Context cancelled → rebase aborted cleanly |

**Schema migration tests**: Existing pattern — test that migration is idempotent, existing data preserved.

**Acceptance criteria:**
- [ ] All new git operations have unit tests
- [ ] Conflict parser has comprehensive edge case tests
- [ ] Pipeline rebase step has tests for all 3 tiers + failure modes
- [ ] Tests use `t.Parallel()` and `t.TempDir()` for isolation
- [ ] No testify — standard library `testing` only

---

## Design Decisions Made

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Scope | Full tiered approach | Delivers the "come back to PRs" thesis |
| Re-validation depth | Re-test only | Tests are ~free; LLM re-review costs $1-5+ and risks producing different code |
| State persistence | Explicit DB states | Enables TUI visibility, crash recovery, cancellation |
| Timing | Before push in `maybeAutoPR` | Minimal pipeline changes, job stays in worktree as long as possible |
| Threshold metric | Lines between `<<<<<<<` and `>>>>>>>` | Unambiguous, includes both sides of conflict |
| Threshold boundary | `< 20` = Tier 2, `>= 20` = Tier 3 | Strictly less-than for safety margin |
| Push after rebase | `--force-with-lease` | Required because rebase rewrites history; safe against unexpected remote changes |
| Crash recovery target | Reset to `ready` | Code changes intact, just re-attempt rebase |
| Loop prevention | Max 1 rebase attempt | Hard cap prevents infinite loops when base branch is volatile |
| `rebase --continue` conflicts | Abort immediately | Treat as Tier 3 regardless of new conflict size |
| No-op detection | Compare HEAD SHA before/after | More reliable than parsing git output |

---

## Risk Analysis & Mitigation

| Risk | Impact | Mitigation |
|------|--------|------------|
| LLM produces incorrect resolution | Subtle bugs in PR | Post-resolution test gate catches breakage; PR body notes LLM resolution occurred |
| Token expired between clone and rebase | Fetch fails | `failJob` with descriptive error; token refresh is out of scope (existing issue) |
| Concurrent `ap approve` and auto-PR race | Double state transition | `TransitionState` uses optimistic concurrency (`WHERE state = ?`) — second caller silently fails |
| Schema migration on large DB | Lock contention | Migrations wrapped in transactions; idempotent checks prevent redundant work |
| Binary file conflicts | LLM cannot resolve | `ConflictedFiles` + file type check → auto-Tier 3 for binary files |

---

## Dependencies & Prerequisites

- No new Go dependencies required
- Existing `git` CLI must be available (already a requirement)
- Existing LLM CLI provider (claude/codex) must support file writing (already does)
- Schema migration runs automatically on daemon start

---

## References

### Internal References
- `internal/pipeline/pipeline.go:319-353` — `maybeAutoPR()` integration point
- `internal/pipeline/steps.go:255-274` — `runTestCommand()` for re-test
- `internal/git/repo.go:93-132` — git runner helpers pattern
- `internal/db/jobs.go:21-31` — `ValidTransitions` map
- `internal/db/schema.go:41` — jobs state CHECK constraint
- `internal/db/schema.go:67` — llm_sessions step CHECK constraint
- `internal/db/schema.go:90` — artifacts kind CHECK constraint
- `internal/db/schema.go:178-258` — table rebuild migration pattern
- `internal/tui/model.go:37-50` — stateStyle color map

### External References
- [sketch.dev/blog/merde](https://sketch.dev/blog/merde) — LLM conflict resolution: context is everything
- [ConGra benchmark (NeurIPS 2024)](https://arxiv.org/abs/2409.14121) — LLMs better at complex conflicts than trivial ones
- [Harmony AI (source.dev)](https://www.source.dev/journal/harmony-preview) — 88-90% auto-resolution with fine-tuned SLMs
- [LLMinus (Linux kernel)](https://lkml.org/lkml/2026/1/11/553) — embedding-based similarity search for historical resolutions
- [git rebase --force-with-lease](https://git-scm.com/docs/git-push#Documentation/git-push.txt---force-with-leaseltrefnamegt) — safe force push after rebase

### Related Work
- Issue #79: This implementation plan
- Issue #80: CI polling after PR creation (out of scope, future work)
- Issue #56: Original rebase sub-item (superseded by #79)
