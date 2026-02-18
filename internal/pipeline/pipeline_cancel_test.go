package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"autopr/internal/config"
	"autopr/internal/db"
	"autopr/internal/llm"
)

type blockingProvider struct {
	once    sync.Once
	started chan struct{}
}

func (p *blockingProvider) Name() string { return "codex" }

func (p *blockingProvider) Run(ctx context.Context, workDir, prompt, jsonlPath string) (llm.Response, error) {
	p.once.Do(func() { close(p.started) })
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

type neverCalledProvider struct{}

func (p *neverCalledProvider) Name() string { return "codex" }

func (p *neverCalledProvider) Run(ctx context.Context, workDir, prompt, jsonlPath string) (llm.Response, error) {
	return llm.Response{}, errors.New("provider should not be called")
}

func TestRunCancelledJobStopsWithoutFailing(t *testing.T) {
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
		SourceIssueID: "88",
		Title:         "cancel pipeline",
		URL:           "https://github.com/org/repo/issues/88",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}

	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	claimed, err := store.ClaimJob(ctx)
	if err != nil || claimed != jobID {
		t.Fatalf("claim job: claimed=%q err=%v", claimed, err)
	}

	workDir := filepath.Join(tmp, "worktree")
	if err := store.UpdateJobField(ctx, jobID, "worktree_path", workDir); err != nil {
		t.Fatalf("set worktree path: %v", err)
	}
	if err := store.UpdateJobField(ctx, jobID, "branch_name", "autopr/test"); err != nil {
		t.Fatalf("set branch name: %v", err)
	}

	provider := &blockingProvider{started: make(chan struct{})}
	cfg := &config.Config{
		ReposRoot: filepath.Join(tmp, "repos"),
		LLM:       config.LLMConfig{Provider: "codex"},
		Projects: []config.ProjectConfig{{
			Name:       "myproject",
			RepoURL:    "https://github.com/org/repo.git",
			BaseBranch: "main",
			TestCmd:    "echo ok",
			GitHub:     &config.ProjectGitHub{Owner: "org", Repo: "repo"},
		}},
	}
	runner := New(store, provider, cfg)

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- runner.Run(ctx, jobID)
	}()

	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}

	if err := store.CancelJob(ctx, jobID); err != nil {
		t.Fatalf("cancel job: %v", err)
	}
	if err := store.MarkRunningSessionsCancelled(ctx, jobID); err != nil {
		t.Fatalf("mark sessions cancelled: %v", err)
	}

	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("runner returned error after cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not stop after cancellation")
	}

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "cancelled" {
		t.Fatalf("expected cancelled job state, got %q", job.State)
	}

	sessions, err := store.ListSessionsByJob(ctx, jobID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatalf("expected at least one session")
	}
	if sessions[0].Status != "cancelled" {
		t.Fatalf("expected cancelled session status, got %q", sessions[0].Status)
	}
}

func TestRunCancelledDuringCloneKeepsWorktreeTrackable(t *testing.T) {
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
		SourceIssueID: "89",
		Title:         "cancel during clone",
		URL:           "https://github.com/org/repo/issues/89",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}

	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	claimed, err := store.ClaimJob(ctx)
	if err != nil || claimed != jobID {
		t.Fatalf("claim job: claimed=%q err=%v", claimed, err)
	}

	cfg := &config.Config{
		ReposRoot: filepath.Join(tmp, "repos"),
		LLM:       config.LLMConfig{Provider: "codex"},
		Projects: []config.ProjectConfig{{
			Name:       "myproject",
			RepoURL:    "https://github.com/org/repo.git",
			BaseBranch: "main",
			TestCmd:    "echo ok",
			GitHub:     &config.ProjectGitHub{Owner: "org", Repo: "repo"},
		}},
	}
	runner := New(store, &neverCalledProvider{}, cfg)

	cloneStarted := make(chan struct{})
	runner.cloneForJob = func(ctx context.Context, repoURL, token, destPath, branchName, baseBranch string) error {
		if err := os.MkdirAll(destPath, 0o755); err != nil {
			return err
		}
		close(cloneStarted)
		<-ctx.Done()
		return ctx.Err()
	}

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- runner.Run(ctx, jobID)
	}()

	select {
	case <-cloneStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("clone did not start")
	}

	if err := store.CancelJob(ctx, jobID); err != nil {
		t.Fatalf("cancel job: %v", err)
	}

	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("runner returned error after cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not stop after cancellation")
	}

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "cancelled" {
		t.Fatalf("expected cancelled job state, got %q", job.State)
	}

	expectedWorktreePath := filepath.Join(cfg.ReposRoot, "worktrees", jobID)
	if job.WorktreePath != expectedWorktreePath {
		t.Fatalf("expected worktree path %q, got %q", expectedWorktreePath, job.WorktreePath)
	}

	cleanable, err := store.ListCleanableJobs(ctx)
	if err != nil {
		t.Fatalf("list cleanable jobs: %v", err)
	}
	found := false
	for _, j := range cleanable {
		if j.ID == jobID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected cancelled job to be cleanable")
	}
}
