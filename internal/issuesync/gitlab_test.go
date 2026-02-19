package issuesync

import (
	"context"
	"testing"

	"autopr/internal/config"
)

func TestSyncGitLabIssuesLabelGateSkipsUnlabeled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitLab: "test-token"},
		Daemon: config.DaemonConfig{MaxIterations: 3},
	}
	project := &config.ProjectConfig{
		Name: "gl-project",
		GitLab: &config.ProjectGitLab{
			BaseURL:       "https://gitlab.com",
			ProjectID:     "99",
			IncludeLabels: []string{"autopr"},
		},
	}
	syncer := NewSyncer(cfg, store, make(chan string, 8))

	// Simulate a page of GitLab issues — none have the "autopr" label.
	issues := []gitlabIssue{
		{
			IID:         1,
			Title:       "unlabeled issue",
			Description: "body",
			WebURL:      "https://gitlab.com/group/repo/-/issues/1",
			Labels:      []string{"bug"},
			UpdatedAt:   "2026-02-17T10:00:00Z",
		},
	}

	syncer.syncGitLabPage(ctx, project, issues)

	// Issue should be stored but ineligible.
	issue := getIssueBySourceID(t, ctx, store, "gl-project", "gitlab", "1")
	if issue.Eligible {
		t.Fatalf("expected issue to be ineligible, got eligible")
	}
	if issue.SkipReason != "missing required labels: autopr" {
		t.Fatalf("unexpected skip reason: %q", issue.SkipReason)
	}

	// No job should be created.
	if countJobs(t, ctx, store) != 0 {
		t.Fatalf("expected no jobs for unlabeled gitlab issue")
	}
}

func TestSyncGitLabIssuesLabelGateSkipsExcluded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitLab: "test-token"},
		Daemon: config.DaemonConfig{MaxIterations: 3},
	}
	project := &config.ProjectConfig{
		Name:         "gl-project",
		ExcludeLabels: []string{"autopr-skip"},
		GitLab: &config.ProjectGitLab{
			BaseURL:       "https://gitlab.com",
			ProjectID:     "99",
			IncludeLabels: []string{"autopr"},
		},
	}
	syncer := NewSyncer(cfg, store, make(chan string, 8))

	issues := []gitlabIssue{
		{
			IID:         2,
			Title:       "excluded issue",
			Description: "body",
			WebURL:      "https://gitlab.com/group/repo/-/issues/2",
			Labels:      []string{"AutoPR", "autopr-skip"},
			UpdatedAt:   "2026-02-17T10:00:00Z",
		},
	}

	syncer.syncGitLabPage(ctx, project, issues)

	issue := getIssueBySourceID(t, ctx, store, "gl-project", "gitlab", "2")
	if issue.Eligible {
		t.Fatalf("expected issue to be excluded, got eligible")
	}
	if issue.SkipReason != "excluded labels: autopr-skip" {
		t.Fatalf("unexpected skip reason: %q", issue.SkipReason)
	}
	if countJobs(t, ctx, store) != 0 {
		t.Fatalf("expected no jobs for excluded gitlab issue")
	}
}

func TestSyncGitLabIssuesLabelGateCreatesJobForLabeled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitLab: "test-token"},
		Daemon: config.DaemonConfig{MaxIterations: 3},
	}
	project := &config.ProjectConfig{
		Name: "gl-project",
		GitLab: &config.ProjectGitLab{
			BaseURL:       "https://gitlab.com",
			ProjectID:     "99",
			IncludeLabels: []string{"autopr"},
		},
	}
	syncer := NewSyncer(cfg, store, make(chan string, 8))

	issues := []gitlabIssue{
		{
			IID:         2,
			Title:       "labeled issue",
			Description: "body",
			WebURL:      "https://gitlab.com/group/repo/-/issues/2",
			Labels:      []string{"AutoPR", "bug"},
			UpdatedAt:   "2026-02-17T10:00:00Z",
		},
	}

	syncer.syncGitLabPage(ctx, project, issues)

	issue := getIssueBySourceID(t, ctx, store, "gl-project", "gitlab", "2")
	if !issue.Eligible {
		t.Fatalf("expected issue to be eligible")
	}

	if countJobs(t, ctx, store) != 1 {
		t.Fatalf("expected one job for labeled gitlab issue")
	}
}

func TestSyncGitLabIssuesNoLabelConfigAllowsAll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitLab: "test-token"},
		Daemon: config.DaemonConfig{MaxIterations: 3},
	}
	project := &config.ProjectConfig{
		Name: "gl-project",
		GitLab: &config.ProjectGitLab{
			BaseURL:   "https://gitlab.com",
			ProjectID: "99",
			// No IncludeLabels — all issues are eligible.
		},
	}
	syncer := NewSyncer(cfg, store, make(chan string, 8))

	issues := []gitlabIssue{
		{
			IID:         3,
			Title:       "any issue",
			Description: "body",
			WebURL:      "https://gitlab.com/group/repo/-/issues/3",
			Labels:      []string{"bug"},
			UpdatedAt:   "2026-02-17T10:00:00Z",
		},
	}

	syncer.syncGitLabPage(ctx, project, issues)

	issue := getIssueBySourceID(t, ctx, store, "gl-project", "gitlab", "3")
	if !issue.Eligible {
		t.Fatalf("expected issue to be eligible when no include_labels configured")
	}

	if countJobs(t, ctx, store) != 1 {
		t.Fatalf("expected one job when no label gate configured")
	}
}
