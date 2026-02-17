package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autopr/internal/db"
)

func TestCancelJobByIDHappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := db.Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "700",
		Title:         "cancel me",
		URL:           "https://github.com/org/repo/issues/700",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := store.UpdateJobField(ctx, jobID, "worktree_path", filepath.Join(tmp, "wt")); err != nil {
		t.Fatalf("set worktree path: %v", err)
	}
	if _, err := store.CreateSession(ctx, jobID, "plan", 0, "codex"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	warnings, err := cancelJobByID(ctx, store, jobID, filepath.Join(tmp, "repos"))
	if err != nil {
		t.Fatalf("cancel job: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "cancelled" {
		t.Fatalf("expected cancelled, got %q", job.State)
	}
	if job.WorktreePath != "" {
		t.Fatalf("expected cleared worktree path, got %q", job.WorktreePath)
	}

	sessions, err := store.ListSessionsByJob(ctx, jobID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Status != "cancelled" {
		t.Fatalf("expected cancelled session, got %#v", sessions)
	}
}

func TestCancelJobByIDTerminalStateError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := db.Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "701",
		Title:         "ready job",
		URL:           "https://github.com/org/repo/issues/701",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := store.TransitionState(ctx, jobID, "queued", "planning"); err != nil {
		t.Fatalf("queued->planning: %v", err)
	}
	if err := store.TransitionState(ctx, jobID, "planning", "implementing"); err != nil {
		t.Fatalf("planning->implementing: %v", err)
	}
	if err := store.TransitionState(ctx, jobID, "implementing", "reviewing"); err != nil {
		t.Fatalf("implementing->reviewing: %v", err)
	}
	if err := store.TransitionState(ctx, jobID, "reviewing", "testing"); err != nil {
		t.Fatalf("reviewing->testing: %v", err)
	}
	if err := store.TransitionState(ctx, jobID, "testing", "ready"); err != nil {
		t.Fatalf("testing->ready: %v", err)
	}

	_, err = cancelJobByID(ctx, store, jobID, filepath.Join(tmp, "repos"))
	if err == nil {
		t.Fatalf("expected terminal-state cancel error")
	}
	if !strings.Contains(err.Error(), "cannot be cancelled") || !strings.Contains(err.Error(), "ap reject") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCancelAllJobsCancelsOnlyEligibleStates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := db.Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	makeJob := func(sourceID string) string {
		t.Helper()
		issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
			ProjectName:   "myproject",
			Source:        "github",
			SourceIssueID: sourceID,
			Title:         "job " + sourceID,
			URL:           "https://github.com/org/repo/issues/" + sourceID,
			State:         "open",
		})
		if err != nil {
			t.Fatalf("upsert issue: %v", err)
		}
		jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
		if err != nil {
			t.Fatalf("create job: %v", err)
		}
		return jobID
	}

	planningID := makeJob("710")
	if err := store.TransitionState(ctx, planningID, "queued", "planning"); err != nil {
		t.Fatalf("planning prep: %v", err)
	}
	queuedID := makeJob("711")
	readyID := makeJob("712")
	if err := store.TransitionState(ctx, readyID, "queued", "planning"); err != nil {
		t.Fatalf("ready prep queued->planning: %v", err)
	}
	if err := store.TransitionState(ctx, readyID, "planning", "implementing"); err != nil {
		t.Fatalf("ready prep planning->implementing: %v", err)
	}
	if err := store.TransitionState(ctx, readyID, "implementing", "reviewing"); err != nil {
		t.Fatalf("ready prep implementing->reviewing: %v", err)
	}
	if err := store.TransitionState(ctx, readyID, "reviewing", "testing"); err != nil {
		t.Fatalf("ready prep reviewing->testing: %v", err)
	}
	if err := store.TransitionState(ctx, readyID, "testing", "ready"); err != nil {
		t.Fatalf("ready prep testing->ready: %v", err)
	}

	cancelledIDs, warnings, err := cancelAllJobs(ctx, store, filepath.Join(tmp, "repos"))
	if err != nil {
		t.Fatalf("cancel all: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	if len(cancelledIDs) != 2 {
		t.Fatalf("expected 2 cancelled IDs, got %d", len(cancelledIDs))
	}

	for _, id := range []string{planningID, queuedID} {
		job, err := store.GetJob(ctx, id)
		if err != nil {
			t.Fatalf("get cancelled job: %v", err)
		}
		if job.State != "cancelled" {
			t.Fatalf("expected cancelled for %s, got %q", id, job.State)
		}
	}

	readyJob, err := store.GetJob(ctx, readyID)
	if err != nil {
		t.Fatalf("get ready job: %v", err)
	}
	if readyJob.State != "ready" {
		t.Fatalf("expected ready job to remain ready, got %q", readyJob.State)
	}
}

func TestCancelJobByIDUsesFallbackWorktreePath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()
	reposRoot := filepath.Join(tmp, "repos")

	store, err := db.Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "713",
		Title:         "cancel fallback path",
		URL:           "https://github.com/org/repo/issues/713",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	fallbackPath := filepath.Join(reposRoot, "worktrees", jobID)
	if err := os.MkdirAll(fallbackPath, 0o755); err != nil {
		t.Fatalf("create fallback worktree: %v", err)
	}

	warnings, err := cancelJobByID(ctx, store, jobID, reposRoot)
	if err != nil {
		t.Fatalf("cancel job: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}

	if _, err := os.Stat(fallbackPath); !os.IsNotExist(err) {
		t.Fatalf("expected fallback worktree removed, stat err=%v", err)
	}
}
