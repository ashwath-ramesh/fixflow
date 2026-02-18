package issuesync

import (
	"context"
	"testing"

	"autopr/internal/config"
	"autopr/internal/db"
	"autopr/internal/git"
)

func TestCheckPRStatus_BranchFallbackMergedReadyJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	jobID := createSyncTestJob(t, ctx, store, "project-gh", "fallback-merged", "ready", "autopr/branch-merged", "")

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitHub: "token"},
		Projects: []config.ProjectConfig{
			{
				Name:   "project-gh",
				GitHub: &config.ProjectGitHub{Owner: "acme", Repo: "repo"},
			},
		},
	}
	s := NewSyncer(cfg, store, make(chan string, 1))

	findCalls := 0
	statusCalls := 0
	s.findGitHubPRByBranch = func(ctx context.Context, token, owner, repo, head, state string) (string, error) {
		findCalls++
		if state != "all" {
			t.Fatalf("expected state=all, got %q", state)
		}
		if head != "autopr/branch-merged" {
			t.Fatalf("unexpected branch lookup: %q", head)
		}
		return "https://github.com/acme/repo/pull/46", nil
	}
	s.checkGitHubPRStatus = func(ctx context.Context, token, prURL string) (git.PRMergeStatus, error) {
		statusCalls++
		if prURL != "https://github.com/acme/repo/pull/46" {
			t.Fatalf("unexpected PR URL: %q", prURL)
		}
		return git.PRMergeStatus{Merged: true, MergedAt: "2026-02-18T01:02:03Z"}, nil
	}

	s.checkPRStatus(ctx)

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.PRURL != "https://github.com/acme/repo/pull/46" {
		t.Fatalf("expected discovered PR URL, got %q", job.PRURL)
	}
	if job.PRMergedAt != "2026-02-18T01:02:03Z" {
		t.Fatalf("expected merged timestamp, got %q", job.PRMergedAt)
	}
	if job.State != "approved" {
		t.Fatalf("expected job state approved after normalization, got %q", job.State)
	}
	if findCalls != 1 {
		t.Fatalf("expected one branch lookup, got %d", findCalls)
	}
	if statusCalls != 1 {
		t.Fatalf("expected one status check, got %d", statusCalls)
	}
}

func TestCheckPRStatus_BranchFallbackOpenPRTransitionsApproved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	jobID := createSyncTestJob(t, ctx, store, "project-gh", "fallback-open", "ready", "autopr/branch-open", "")

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitHub: "token"},
		Projects: []config.ProjectConfig{
			{
				Name:   "project-gh",
				GitHub: &config.ProjectGitHub{Owner: "acme", Repo: "repo"},
			},
		},
	}
	s := NewSyncer(cfg, store, make(chan string, 1))
	s.findGitHubPRByBranch = func(ctx context.Context, token, owner, repo, head, state string) (string, error) {
		return "https://github.com/acme/repo/pull/47", nil
	}
	s.checkGitHubPRStatus = func(ctx context.Context, token, prURL string) (git.PRMergeStatus, error) {
		return git.PRMergeStatus{}, nil
	}

	s.checkPRStatus(ctx)

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "approved" {
		t.Fatalf("expected ready job to become approved, got %q", job.State)
	}
	if job.PRURL != "https://github.com/acme/repo/pull/47" {
		t.Fatalf("expected discovered PR URL, got %q", job.PRURL)
	}
	if job.PRMergedAt != "" || job.PRClosedAt != "" {
		t.Fatalf("expected no terminal PR timestamps, got merged=%q closed=%q", job.PRMergedAt, job.PRClosedAt)
	}
}

func TestCheckPRStatus_BranchFallbackNoRemotePRLeavesJobUnchanged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	jobID := createSyncTestJob(t, ctx, store, "project-gh", "fallback-none", "ready", "autopr/branch-none", "")

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitHub: "token"},
		Projects: []config.ProjectConfig{
			{
				Name:   "project-gh",
				GitHub: &config.ProjectGitHub{Owner: "acme", Repo: "repo"},
			},
		},
	}
	s := NewSyncer(cfg, store, make(chan string, 1))
	s.findGitHubPRByBranch = func(ctx context.Context, token, owner, repo, head, state string) (string, error) {
		return "", nil
	}
	s.checkGitHubPRStatus = func(ctx context.Context, token, prURL string) (git.PRMergeStatus, error) {
		t.Fatalf("status check should not run when no PR is found")
		return git.PRMergeStatus{}, nil
	}

	s.checkPRStatus(ctx)

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "ready" {
		t.Fatalf("expected job to remain ready, got %q", job.State)
	}
	if job.PRURL != "" {
		t.Fatalf("expected PR URL to stay empty, got %q", job.PRURL)
	}
}

func TestCheckPRStatus_GitLabBranchFallbackMergedDefaultsBaseURL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	jobID := createSyncTestJob(t, ctx, store, "project-gl", "gitlab-fallback-merged", "ready", "autopr/gl-merged", "")

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitLab: "token"},
		Projects: []config.ProjectConfig{
			{
				Name:   "project-gl",
				GitLab: &config.ProjectGitLab{ProjectID: "group%2Frepo"},
			},
		},
	}
	s := NewSyncer(cfg, store, make(chan string, 1))
	s.findGitLabMRByBranch = func(ctx context.Context, token, baseURL, projectID, sourceBranch, state string) (string, error) {
		if baseURL != "https://gitlab.com" {
			t.Fatalf("expected default gitlab base URL, got %q", baseURL)
		}
		if state != "all" {
			t.Fatalf("expected state=all, got %q", state)
		}
		if sourceBranch != "autopr/gl-merged" {
			t.Fatalf("unexpected branch lookup: %q", sourceBranch)
		}
		return "https://gitlab.com/group/repo/-/merge_requests/46", nil
	}
	s.checkGitLabMRStatus = func(ctx context.Context, token, baseURL, mrURL string) (git.PRMergeStatus, error) {
		if baseURL != "https://gitlab.com" {
			t.Fatalf("expected default gitlab base URL for status check, got %q", baseURL)
		}
		if mrURL != "https://gitlab.com/group/repo/-/merge_requests/46" {
			t.Fatalf("unexpected MR URL: %q", mrURL)
		}
		return git.PRMergeStatus{Merged: true, MergedAt: "2026-02-18T06:07:08Z"}, nil
	}

	s.checkPRStatus(ctx)

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.PRURL != "https://gitlab.com/group/repo/-/merge_requests/46" {
		t.Fatalf("expected discovered MR URL, got %q", job.PRURL)
	}
	if job.PRMergedAt != "2026-02-18T06:07:08Z" {
		t.Fatalf("expected merged timestamp, got %q", job.PRMergedAt)
	}
	if job.State != "approved" {
		t.Fatalf("expected job state approved after normalization, got %q", job.State)
	}
}

func TestCheckPRStatus_GitLabBranchFallbackOpenMRTransitionsApproved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	jobID := createSyncTestJob(t, ctx, store, "project-gl", "gitlab-fallback-open", "ready", "autopr/gl-open", "")

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitLab: "token"},
		Projects: []config.ProjectConfig{
			{
				Name:   "project-gl",
				GitLab: &config.ProjectGitLab{ProjectID: "group%2Frepo"},
			},
		},
	}
	s := NewSyncer(cfg, store, make(chan string, 1))
	s.findGitLabMRByBranch = func(ctx context.Context, token, baseURL, projectID, sourceBranch, state string) (string, error) {
		if baseURL != "https://gitlab.com" {
			t.Fatalf("expected default gitlab base URL, got %q", baseURL)
		}
		return "https://gitlab.com/group/repo/-/merge_requests/47", nil
	}
	s.checkGitLabMRStatus = func(ctx context.Context, token, baseURL, mrURL string) (git.PRMergeStatus, error) {
		if baseURL != "https://gitlab.com" {
			t.Fatalf("expected default gitlab base URL for status check, got %q", baseURL)
		}
		return git.PRMergeStatus{}, nil
	}

	s.checkPRStatus(ctx)

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "approved" {
		t.Fatalf("expected ready job to become approved, got %q", job.State)
	}
	if job.PRURL != "https://gitlab.com/group/repo/-/merge_requests/47" {
		t.Fatalf("expected discovered MR URL, got %q", job.PRURL)
	}
	if job.PRMergedAt != "" || job.PRClosedAt != "" {
		t.Fatalf("expected no terminal PR timestamps, got merged=%q closed=%q", job.PRMergedAt, job.PRClosedAt)
	}
}

func TestCheckPRStatus_ApprovedKnownPRStillPolled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	jobID := createSyncTestJob(t, ctx, store, "project-gh", "known-pr", "approved", "autopr/known", "https://github.com/acme/repo/pull/88")

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitHub: "token"},
		Projects: []config.ProjectConfig{
			{
				Name:   "project-gh",
				GitHub: &config.ProjectGitHub{Owner: "acme", Repo: "repo"},
			},
		},
	}
	s := NewSyncer(cfg, store, make(chan string, 1))
	s.findGitHubPRByBranch = func(ctx context.Context, token, owner, repo, head, state string) (string, error) {
		t.Fatalf("branch lookup should not run for known PR jobs")
		return "", nil
	}
	s.checkGitHubPRStatus = func(ctx context.Context, token, prURL string) (git.PRMergeStatus, error) {
		if prURL != "https://github.com/acme/repo/pull/88" {
			t.Fatalf("unexpected PR URL: %q", prURL)
		}
		return git.PRMergeStatus{Merged: true, MergedAt: "2026-02-18T04:05:06Z"}, nil
	}

	s.checkPRStatus(ctx)

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.PRMergedAt != "2026-02-18T04:05:06Z" {
		t.Fatalf("expected merged timestamp to be updated, got %q", job.PRMergedAt)
	}
}

func createSyncTestJob(t *testing.T, ctx context.Context, store *db.Store, projectName, sourceIssueID, state, branch, prURL string) string {
	t.Helper()
	issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
		ProjectName:   projectName,
		Source:        "github",
		SourceIssueID: sourceIssueID,
		Title:         sourceIssueID,
		URL:           "https://github.com/acme/repo/issues/" + sourceIssueID,
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, projectName, 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.Writer.ExecContext(ctx, `
UPDATE jobs
SET state = ?, branch_name = ?, pr_url = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?`, state, branch, prURL, jobID); err != nil {
		t.Fatalf("configure job: %v", err)
	}
	return jobID
}
