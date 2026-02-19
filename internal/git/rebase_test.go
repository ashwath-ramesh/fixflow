package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupStaleRebaseAllowsMissingWorktree(t *testing.T) {
	t.Parallel()
	reposRoot := t.TempDir()
	worktreePath := filepath.Join(reposRoot, "missing-worktree")

	if err := CleanupStaleRebase(worktreePath, reposRoot); err != nil {
		t.Fatalf("cleanup stale rebase: %v", err)
	}
}

func TestCleanupStaleRebaseRequiresReposRoot(t *testing.T) {
	t.Parallel()
	reposRoot := t.TempDir()
	worktreePath := filepath.Join(reposRoot, "present-worktree")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if err := CleanupStaleRebase(worktreePath, ""); err == nil {
		t.Fatalf("expected error with missing repos root")
	}
}
