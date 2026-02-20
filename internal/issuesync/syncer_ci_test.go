package issuesync

import (
	"context"
	"strings"
	"testing"

	"autopr/internal/config"
	"autopr/internal/git"
)

func TestCheckCIStatus_AllChecksPassed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	jobID := createSyncTestJob(t, ctx, store, "project-gh", "ci-passed", "awaiting_checks", "autopr/ci-pass", "https://github.com/acme/repo/pull/100")

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitHub: "token"},
		Daemon: config.DaemonConfig{CICheckTimeout: "30m"},
		Projects: []config.ProjectConfig{
			{
				Name:   "project-gh",
				GitHub: &config.ProjectGitHub{Owner: "acme", Repo: "repo"},
			},
		},
	}
	s := NewSyncer(cfg, store, make(chan string, 1))
	s.getGitHubCheckRunStatus = func(ctx context.Context, token, owner, repo, ref string) (git.CheckRunStatus, error) {
		if ref != "autopr/ci-pass" {
			t.Fatalf("unexpected ref: %q", ref)
		}
		return git.CheckRunStatus{
			Total:     3,
			Completed: 3,
			Passed:    3,
		}, nil
	}

	s.CheckCIStatus(ctx)

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "approved" {
		t.Fatalf("expected job state approved, got %q", job.State)
	}
	if job.CIStatusSummary == "" || !strings.Contains(job.CIStatusSummary, "passed") {
		t.Fatalf("expected CI summary to include pass details, got %q", job.CIStatusSummary)
	}
}

func TestCheckCIStatus_CheckFailed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	jobID := createSyncTestJob(t, ctx, store, "project-gh", "ci-failed", "awaiting_checks", "autopr/ci-fail", "https://github.com/acme/repo/pull/101")

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitHub: "token"},
		Daemon: config.DaemonConfig{CICheckTimeout: "30m"},
		Projects: []config.ProjectConfig{
			{
				Name:   "project-gh",
				GitHub: &config.ProjectGitHub{Owner: "acme", Repo: "repo"},
			},
		},
	}
	s := NewSyncer(cfg, store, make(chan string, 1))
	s.getGitHubCheckRunStatus = func(ctx context.Context, token, owner, repo, ref string) (git.CheckRunStatus, error) {
		return git.CheckRunStatus{
			Total:           2,
			Completed:       2,
			Passed:          1,
			Failed:          1,
			FailedCheckName: "lint",
			FailedCheckURL:  "https://github.com/acme/repo/runs/999",
		}, nil
	}

	s.CheckCIStatus(ctx)

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "rejected" {
		t.Fatalf("expected job state rejected, got %q", job.State)
	}
	if job.RejectReason == "" {
		t.Fatalf("expected reject_reason to be set")
	}
	if job.CIStatusSummary == "" || !strings.Contains(job.CIStatusSummary, "lint") {
		t.Fatalf("expected CI summary to include failed check details, got %q", job.CIStatusSummary)
	}
}

func TestCheckCIStatus_Pending(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	jobID := createSyncTestJob(t, ctx, store, "project-gh", "ci-pending", "awaiting_checks", "autopr/ci-pend", "https://github.com/acme/repo/pull/102")

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitHub: "token"},
		Daemon: config.DaemonConfig{CICheckTimeout: "30m"},
		Projects: []config.ProjectConfig{
			{
				Name:   "project-gh",
				GitHub: &config.ProjectGitHub{Owner: "acme", Repo: "repo"},
			},
		},
	}
	s := NewSyncer(cfg, store, make(chan string, 1))
	s.getGitHubCheckRunStatus = func(ctx context.Context, token, owner, repo, ref string) (git.CheckRunStatus, error) {
		return git.CheckRunStatus{
			Total:     3,
			Completed: 1,
			Passed:    1,
			Pending:   2,
		}, nil
	}

	s.CheckCIStatus(ctx)

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "awaiting_checks" {
		t.Fatalf("expected job to remain awaiting_checks, got %q", job.State)
	}
	if job.CIStatusSummary == "" || !strings.Contains(job.CIStatusSummary, "pending=2") {
		t.Fatalf("expected CI summary to include pending count, got %q", job.CIStatusSummary)
	}
}

func TestCheckCIStatus_NoChecksYet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	jobID := createSyncTestJob(t, ctx, store, "project-gh", "ci-nochecks", "awaiting_checks", "autopr/ci-none", "https://github.com/acme/repo/pull/103")

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitHub: "token"},
		Daemon: config.DaemonConfig{CICheckTimeout: "30m"},
		Projects: []config.ProjectConfig{
			{
				Name:   "project-gh",
				GitHub: &config.ProjectGitHub{Owner: "acme", Repo: "repo"},
			},
		},
	}
	s := NewSyncer(cfg, store, make(chan string, 1))
	s.getGitHubCheckRunStatus = func(ctx context.Context, token, owner, repo, ref string) (git.CheckRunStatus, error) {
		return git.CheckRunStatus{Total: 0}, nil
	}

	s.CheckCIStatus(ctx)

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "awaiting_checks" {
		t.Fatalf("expected job to remain awaiting_checks, got %q", job.State)
	}
	if job.CIStatusSummary == "" || !strings.Contains(job.CIStatusSummary, "no check-runs") {
		t.Fatalf("expected CI summary to mention no checks, got %q", job.CIStatusSummary)
	}
}

func TestCheckCIStatus_AwaitingChecksPRMergedBeforeTimeout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	jobID := createSyncTestJob(t, ctx, store, "project-gh", "ci-merged", "awaiting_checks", "autopr/ci-merged", "https://github.com/acme/repo/pull/105")

	if _, err := store.Writer.ExecContext(ctx, `
UPDATE jobs SET updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-2 hours')
	WHERE id = ?`, jobID); err != nil {
		t.Fatalf("set old updated_at: %v", err)
	}

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitHub: "token"},
		Daemon: config.DaemonConfig{CICheckTimeout: "1m"},
		Projects: []config.ProjectConfig{
			{
				Name:   "project-gh",
				GitHub: &config.ProjectGitHub{Owner: "acme", Repo: "repo"},
			},
		},
	}
	s := NewSyncer(cfg, store, make(chan string, 1))
	s.checkGitHubPRStatus = func(ctx context.Context, token, prURL string) (git.PRMergeStatus, error) {
		if token != "token" {
			t.Fatalf("unexpected token: %q", token)
		}
		if prURL != "https://github.com/acme/repo/pull/105" {
			t.Fatalf("unexpected PR URL: %q", prURL)
		}
		return git.PRMergeStatus{Merged: true, MergedAt: "2026-02-18T12:00:00Z"}, nil
	}
	s.getGitHubCheckRunStatus = func(ctx context.Context, token, owner, repo, ref string) (git.CheckRunStatus, error) {
		t.Fatalf("CI check should not run after merged PR is detected")
		return git.CheckRunStatus{}, nil
	}

	s.CheckCIStatus(ctx)

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.PRMergedAt != "2026-02-18T12:00:00Z" {
		t.Fatalf("expected merged timestamp, got %q", job.PRMergedAt)
	}
	if job.State != "awaiting_checks" {
		t.Fatalf("expected job to remain awaiting_checks, got %q", job.State)
	}
	if job.RejectReason != "" {
		t.Fatalf("expected no reject reason for merged job, got %q", job.RejectReason)
	}
}

func TestCheckCIStatus_AwaitingChecksPRClosedBeforeTimeout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	jobID := createSyncTestJob(t, ctx, store, "project-gh", "ci-closed", "awaiting_checks", "autopr/ci-closed", "https://github.com/acme/repo/pull/106")

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitHub: "token"},
		Daemon: config.DaemonConfig{CICheckTimeout: "1m"},
		Projects: []config.ProjectConfig{
			{
				Name:   "project-gh",
				GitHub: &config.ProjectGitHub{Owner: "acme", Repo: "repo"},
			},
		},
	}
	s := NewSyncer(cfg, store, make(chan string, 1))
	s.checkGitHubPRStatus = func(ctx context.Context, token, prURL string) (git.PRMergeStatus, error) {
		if token != "token" {
			t.Fatalf("unexpected token: %q", token)
		}
		if prURL != "https://github.com/acme/repo/pull/106" {
			t.Fatalf("unexpected PR URL: %q", prURL)
		}
		return git.PRMergeStatus{Closed: true, ClosedAt: "2026-02-18T12:01:00Z"}, nil
	}
	s.getGitHubCheckRunStatus = func(ctx context.Context, token, owner, repo, ref string) (git.CheckRunStatus, error) {
		t.Fatalf("CI check should not run after closed PR is detected")
		return git.CheckRunStatus{}, nil
	}

	s.CheckCIStatus(ctx)

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.PRClosedAt != "2026-02-18T12:01:00Z" {
		t.Fatalf("expected closed timestamp, got %q", job.PRClosedAt)
	}
	if job.State != "awaiting_checks" {
		t.Fatalf("expected job to remain awaiting_checks, got %q", job.State)
	}
	if job.RejectReason != "" {
		t.Fatalf("expected no reject reason for closed job, got %q", job.RejectReason)
	}
}

func TestCheckCIStatus_Timeout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	jobID := createSyncTestJob(t, ctx, store, "project-gh", "ci-timeout", "awaiting_checks", "autopr/ci-timeout", "https://github.com/acme/repo/pull/104")

	// Set updated_at to 2 hours ago to trigger timeout.
	if _, err := store.Writer.ExecContext(ctx, `
UPDATE jobs SET updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-2 hours')
WHERE id = ?`, jobID); err != nil {
		t.Fatalf("set old updated_at: %v", err)
	}

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitHub: "token"},
		Daemon: config.DaemonConfig{CICheckTimeout: "1m"}, // 1 minute timeout
		Projects: []config.ProjectConfig{
			{
				Name:   "project-gh",
				GitHub: &config.ProjectGitHub{Owner: "acme", Repo: "repo"},
			},
		},
	}
	s := NewSyncer(cfg, store, make(chan string, 1))
	s.getGitHubCheckRunStatus = func(ctx context.Context, token, owner, repo, ref string) (git.CheckRunStatus, error) {
		t.Fatalf("check-run status should not be called for timed-out job")
		return git.CheckRunStatus{}, nil
	}

	s.CheckCIStatus(ctx)

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "rejected" {
		t.Fatalf("expected job state rejected after timeout, got %q", job.State)
	}
	if job.RejectReason == "" {
		t.Fatalf("expected reject_reason for timeout")
	}
}

func TestCheckCIStatus_NonGitHub(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	jobID := createSyncTestJob(t, ctx, store, "project-gl", "ci-nongithub", "awaiting_checks", "autopr/ci-gl", "https://gitlab.com/org/repo/-/merge_requests/50")

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitLab: "token"},
		Daemon: config.DaemonConfig{CICheckTimeout: "30m"},
		Projects: []config.ProjectConfig{
			{
				Name:   "project-gl",
				GitLab: &config.ProjectGitLab{ProjectID: "123"},
			},
		},
	}
	s := NewSyncer(cfg, store, make(chan string, 1))
	s.getGitHubCheckRunStatus = func(ctx context.Context, token, owner, repo, ref string) (git.CheckRunStatus, error) {
		t.Fatalf("GitHub check-run status should not be called for GitLab project")
		return git.CheckRunStatus{}, nil
	}

	s.CheckCIStatus(ctx)

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	// Non-GitHub projects should be auto-approved.
	if job.State != "approved" {
		t.Fatalf("expected non-GitHub job to be auto-approved, got %q", job.State)
	}
}
