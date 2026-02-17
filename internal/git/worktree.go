package git

import (
	"context"
	"fmt"
	"os"
)

// CloneForJob clones the remote repo into destPath and creates a job branch
// from the base branch. Uses a regular clone (not a worktree) because LLM
// tools (e.g. codex) may run `git init` in the working directory, which
// destroys worktree .git link files but is a no-op on a .git directory.
func CloneForJob(ctx context.Context, repoURL, token, destPath, branchName, baseBranch string) error {
	authURL := injectToken(repoURL, token)

	// Clone from the remote. For repos that already have a local bare cache,
	// git will use hard links for shared objects automatically when on the
	// same filesystem.
	if err := runGit(ctx, "", "clone", "--branch", baseBranch, authURL, destPath); err != nil {
		return fmt.Errorf("clone for job: %w", err)
	}

	// Create and checkout the job branch.
	if err := runGit(ctx, destPath, "checkout", "-b", branchName); err != nil {
		return fmt.Errorf("create job branch: %w", err)
	}

	return nil
}

// RemoveJobDir removes a job's cloned working directory.
func RemoveJobDir(worktreePath string) {
	_ = os.RemoveAll(worktreePath)
}
