package issuesync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"autopr/internal/config"
)

func TestCleanupWorktreeDeletesRemoteBranchBeforeLocalCleanup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	jobID := createSyncTestJob(t, ctx, store, "project-gh", "cleanup-before-local", "approved", " autopr/cleanup-before-local ", "https://github.com/acme/repo/pull/88")

	worktreePath := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "artifact.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := store.UpdateJobField(ctx, jobID, "worktree_path", worktreePath); err != nil {
		t.Fatalf("set worktree path: %v", err)
	}

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	remoteDeleted := false
	s := NewSyncer(&config.Config{}, store, make(chan string, 1))
	s.deleteRemoteBranch = func(ctx context.Context, dir, branchName string) error {
		remoteDeleted = true
		if branchName != "autopr/cleanup-before-local" {
			t.Fatalf("unexpected branch: %q", branchName)
		}
		if _, err := os.Stat(dir); err != nil {
			return err
		}
		return errors.New("forced remote delete failure")
	}

	s.cleanupWorktree(ctx, job)

	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree to be removed even after remote delete failure")
	}
	updatedJob, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job after cleanup: %v", err)
	}
	if updatedJob.WorktreePath != "" {
		t.Fatalf("expected worktree path cleared, got %q", updatedJob.WorktreePath)
	}
	if !remoteDeleted {
		t.Fatalf("expected remote delete attempt")
	}
}

func TestCleanupWorktreeSkipsRemoteDeleteWithEmptyBranch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	jobID := createSyncTestJob(t, ctx, store, "project-gh", "cleanup-empty-branch", "approved", "", "https://github.com/acme/repo/pull/89")

	worktreePath := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if err := store.UpdateJobField(ctx, jobID, "worktree_path", worktreePath); err != nil {
		t.Fatalf("set worktree path: %v", err)
	}

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	s := NewSyncer(&config.Config{}, store, make(chan string, 1))
	s.deleteRemoteBranch = func(ctx context.Context, dir, branchName string) error {
		t.Fatalf("unexpected remote delete call for empty branch: branch=%q", branchName)
		return nil
	}

	s.cleanupWorktree(ctx, job)

	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree to be removed")
	}
	updatedJob, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job after cleanup: %v", err)
	}
	if updatedJob.WorktreePath != "" {
		t.Fatalf("expected worktree path cleared, got %q", updatedJob.WorktreePath)
	}
}
