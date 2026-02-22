---
status: pending
priority: p2
issue_id: "005"
tags: [code-review, performance, ux]
dependencies: []
---

# UX Regression: Lost Real-Time Git Progress Output

## Problem Statement

The switch from `cmd.Stdout = os.Stdout` / `cmd.Stderr = os.Stderr` to `cmd.CombinedOutput()` suppresses all real-time git progress output. Users will no longer see fetch/clone progress bars during long operations. For a CLI tool on slow networks, this could appear as a "hang".

## Findings

- **Source:** Performance Oracle
- Old `runGit` streamed output directly to terminal — users saw progress
- New `runGitWithOptions` buffers all output, only shows it on error
- `EnsureClone` does log `slog.Info("cloning repository", ...)` before cloning
- Daemon and TUI modes were already using "Captured" variants (no terminal output), so impact is primarily on CLI paths

## Proposed Solutions

### Option A: Add slog progress messages for long operations (Recommended)
- Add `slog.Info("fetching from remote...")` before fetch operations
- Minimal change, consistent with existing clone logging
- **Effort:** Small
- **Risk:** None

### Option B: Streaming redactor for fetch/clone
- Pipe git output through a redacting writer that forwards to terminal
- **Effort:** Medium
- **Risk:** Medium (complexity of streaming redaction)

### Option C: Accept the change
- The daemon and TUI already suppressed output; CLI users rarely watch fetches
- **Effort:** None
- **Risk:** Minor UX degradation

## Acceptance Criteria

- [ ] Users get feedback during long-running git operations
- [ ] No credentials leak through progress output

## Work Log

| Date | Action | Learnings |
|------|--------|-----------|
| 2026-02-22 | Identified during performance review | Behavioral change from CombinedOutput switch |
