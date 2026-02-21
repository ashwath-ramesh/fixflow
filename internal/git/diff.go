package git

import (
	"context"
	"fmt"
	"os/exec"
)

// DiffAgainstBase returns the raw diff of a worktree against origin/<baseBranch>.
// It runs `git add -N .` first so untracked files appear in the diff output.
func DiffAgainstBase(ctx context.Context, worktreePath, baseBranch string) (string, error) {
	// Mark untracked files as intent-to-add so they appear in diff output.
	// Use CombinedOutput to capture/discard any diagnostics from git add -N
	// and prevent them from leaking to the TUI terminal.
	addN := exec.CommandContext(ctx, "git", "add", "-N", ".")
	addN.Dir = worktreePath
	_, _ = addN.CombinedOutput()

	out, err := runGitOutput(ctx, worktreePath, "diff", fmt.Sprintf("origin/%s", baseBranch))
	if err != nil {
		return "", fmt.Errorf("diff against origin/%s: %w", baseBranch, err)
	}
	return out, nil
}

// DiffFilesAgainstBase returns changed file paths against origin/<baseBranch>.
// It runs `git add -N .` first so untracked files appear in the diff output.
func DiffFilesAgainstBase(ctx context.Context, worktreePath, baseBranch string) (string, error) {
	// Mark untracked files as intent-to-add so they appear in diff output.
	addN := exec.CommandContext(ctx, "git", "add", "-N", ".")
	addN.Dir = worktreePath
	_, _ = addN.CombinedOutput()

	out, err := runGitOutput(ctx, worktreePath, "diff", "--name-only", fmt.Sprintf("origin/%s", baseBranch))
	if err != nil {
		return "", fmt.Errorf("diff files against origin/%s: %w", baseBranch, err)
	}
	return out, nil
}

// DiffStatAgainstBase returns the --stat summary of a worktree against origin/<baseBranch>.
func DiffStatAgainstBase(ctx context.Context, worktreePath, baseBranch string) (string, error) {
	// Mark untracked files as intent-to-add so they appear in diff output.
	addN := exec.CommandContext(ctx, "git", "add", "-N", ".")
	addN.Dir = worktreePath
	_, _ = addN.CombinedOutput()

	out, err := runGitOutput(ctx, worktreePath, "diff", "--stat", fmt.Sprintf("origin/%s", baseBranch))
	if err != nil {
		return "", fmt.Errorf("diff stat against origin/%s: %w", baseBranch, err)
	}
	return out, nil
}
