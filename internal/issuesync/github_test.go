package issuesync

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"autopr/internal/config"
	"autopr/internal/db"
)

func TestEvaluateGitHubIssueEligibility(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 17, 10, 11, 12, 0, time.UTC)

	tests := []struct {
		name          string
		includeLabels []string
		issueLabels   []string
		wantEligible  bool
		wantReason    string
	}{
		{
			name:         "empty include labels keeps all eligible",
			issueLabels:  []string{"bug"},
			wantEligible: true,
		},
		{
			name:          "any matching label is eligible",
			includeLabels: []string{"autopr", "ready"},
			issueLabels:   []string{"Bug", "AUTOpr"},
			wantEligible:  true,
		},
		{
			name:          "missing labels is ineligible",
			includeLabels: []string{"autopr", "ready"},
			issueLabels:   []string{"bug"},
			wantReason:    "missing required labels: autopr, ready",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := evaluateGitHubIssueEligibility(tc.includeLabels, tc.issueLabels, now)
			if got.Eligible != tc.wantEligible {
				t.Fatalf("eligible: want %v got %v", tc.wantEligible, got.Eligible)
			}
			if got.SkipReason != tc.wantReason {
				t.Fatalf("skip_reason: want %q got %q", tc.wantReason, got.SkipReason)
			}
			if got.EvaluatedAt != "2026-02-17T10:11:12Z" {
				t.Fatalf("evaluated_at: %q", got.EvaluatedAt)
			}
		})
	}
}

func TestSyncGitHubIssuesEligibilityTransitions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	cfg := &config.Config{
		Daemon: config.DaemonConfig{MaxIterations: 3},
	}
	project := &config.ProjectConfig{
		Name: "my-project",
		GitHub: &config.ProjectGitHub{
			Owner:         "org",
			Repo:          "repo",
			IncludeLabels: []string{"autopr"},
		},
	}
	syncer := NewSyncer(cfg, store, make(chan string, 8))

	// Initial unlabeled issue: stored, no job.
	syncer.syncGitHubIssues(ctx, project, []githubIssue{
		{
			Number:    7,
			Title:     "needs triage",
			Body:      "body",
			HTMLURL:   "https://github.com/org/repo/issues/7",
			UpdatedAt: "2026-02-17T10:00:00Z",
			Labels:    []githubLabel{{Name: "bug"}},
		},
	})

	issue := getIssueBySourceID(t, ctx, store, "my-project", "github", "7")
	if issue.Eligible {
		t.Fatalf("expected issue to be ineligible")
	}
	if issue.SkipReason != "missing required labels: autopr" {
		t.Fatalf("unexpected skip reason: %q", issue.SkipReason)
	}
	if countJobs(t, ctx, store) != 0 {
		t.Fatalf("expected no jobs for unlabeled issue")
	}

	// Ineligible -> eligible should create exactly one job.
	syncer.syncGitHubIssues(ctx, project, []githubIssue{
		{
			Number:    7,
			Title:     "needs triage",
			Body:      "body",
			HTMLURL:   "https://github.com/org/repo/issues/7",
			UpdatedAt: "2026-02-17T10:05:00Z",
			Labels:    []githubLabel{{Name: "AutoPR"}},
		},
	})
	if countJobs(t, ctx, store) != 1 {
		t.Fatalf("expected one job after label add")
	}
	issue = getIssueBySourceID(t, ctx, store, "my-project", "github", "7")
	if !issue.Eligible {
		t.Fatalf("expected issue to be eligible after label add")
	}
	if issue.SkipReason != "" {
		t.Fatalf("expected empty skip reason for eligible issue, got %q", issue.SkipReason)
	}

	// Move created job to failed so it's retryable (unless eligibility blocks).
	jobID := getOnlyJobID(t, ctx, store)
	claimedID, err := store.ClaimJob(ctx)
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if claimedID != jobID {
		t.Fatalf("expected claimed job %q, got %q", jobID, claimedID)
	}
	if err := store.TransitionState(ctx, jobID, "planning", "failed"); err != nil {
		t.Fatalf("transition to failed: %v", err)
	}

	// Eligible -> ineligible should not create a new job; retry should be blocked.
	syncer.syncGitHubIssues(ctx, project, []githubIssue{
		{
			Number:    7,
			Title:     "needs triage",
			Body:      "body",
			HTMLURL:   "https://github.com/org/repo/issues/7",
			UpdatedAt: "2026-02-17T10:08:00Z",
			Labels:    []githubLabel{{Name: "bug"}},
		},
	})
	if countJobs(t, ctx, store) != 1 {
		t.Fatalf("expected no new jobs after label removal")
	}

	err = store.ResetJobForRetry(ctx, jobID, "retry")
	if err == nil {
		t.Fatalf("expected retry block when issue is ineligible")
	}
	if !strings.Contains(err.Error(), "ineligible") {
		t.Fatalf("expected ineligible retry error, got %v", err)
	}
}

func TestSyncGitHubIssuesIdempotentWhileActiveJobExists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	cfg := &config.Config{
		Daemon: config.DaemonConfig{MaxIterations: 3},
	}
	project := &config.ProjectConfig{
		Name: "my-project",
		GitHub: &config.ProjectGitHub{
			Owner:         "org",
			Repo:          "repo",
			IncludeLabels: []string{"autopr"},
		},
	}
	syncer := NewSyncer(cfg, store, make(chan string, 8))

	payload := []githubIssue{{
		Number:    9,
		Title:     "eligible issue",
		Body:      "body",
		HTMLURL:   "https://github.com/org/repo/issues/9",
		UpdatedAt: "2026-02-17T11:00:00Z",
		Labels:    []githubLabel{{Name: "autopr"}},
	}}
	syncer.syncGitHubIssues(ctx, project, payload)
	if countJobs(t, ctx, store) != 1 {
		t.Fatalf("expected one job after first sync")
	}

	// Same issue remains eligible and active job exists; no duplicate.
	payload[0].UpdatedAt = "2026-02-17T11:05:00Z"
	syncer.syncGitHubIssues(ctx, project, payload)
	if countJobs(t, ctx, store) != 1 {
		t.Fatalf("expected idempotent sync without duplicate active job")
	}
}

func TestSyncGitHubIssuesClosedIssueCancelsActiveJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	cfg := &config.Config{
		Daemon: config.DaemonConfig{MaxIterations: 3},
	}
	project := &config.ProjectConfig{
		Name: "my-project",
		GitHub: &config.ProjectGitHub{
			Owner: "org",
			Repo:  "repo",
		},
	}
	syncer := NewSyncer(cfg, store, make(chan string, 8))

	syncer.syncGitHubIssues(ctx, project, []githubIssue{
		{
			Number:    11,
			State:     "open",
			Title:     "active issue",
			Body:      "body",
			HTMLURL:   "https://github.com/org/repo/issues/11",
			UpdatedAt: "2026-02-17T12:00:00Z",
		},
	})
	if countJobs(t, ctx, store) != 1 {
		t.Fatalf("expected one job after open sync")
	}

	jobID := getOnlyJobID(t, ctx, store)
	if _, err := store.CreateSession(ctx, jobID, "plan", 0, "codex", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}

	syncer.syncGitHubIssues(ctx, project, []githubIssue{
		{
			Number:    11,
			State:     "closed",
			Title:     "active issue",
			Body:      "body",
			HTMLURL:   "https://github.com/org/repo/issues/11",
			UpdatedAt: "2026-02-17T12:05:00Z",
		},
	})

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "cancelled" {
		t.Fatalf("expected cancelled state, got %q", job.State)
	}
	if job.ErrorMessage != db.CancelReasonSourceIssueClosed {
		t.Fatalf("expected error_message %q, got %q", db.CancelReasonSourceIssueClosed, job.ErrorMessage)
	}

	sessions, err := store.ListSessionsByJob(ctx, jobID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Status != "cancelled" {
		t.Fatalf("expected cancelled running session, got %+v", sessions)
	}

	issue := getIssueBySourceID(t, ctx, store, "my-project", "github", "11")
	if issue.State != "closed" {
		t.Fatalf("expected issue state=closed, got %q", issue.State)
	}
}

func TestSyncGitHubIssuesClosedIssueDoesNotTouchNonCancellableStates(t *testing.T) {
	t.Parallel()

	tests := []string{"ready", "approved", "rejected", "failed", "cancelled"}
	for _, state := range tests {
		state := state
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := openTestStore(t)
			defer store.Close()

			cfg := &config.Config{
				Daemon: config.DaemonConfig{MaxIterations: 3},
			}
			project := &config.ProjectConfig{
				Name: "my-project",
				GitHub: &config.ProjectGitHub{
					Owner: "org",
					Repo:  "repo",
				},
			}
			syncer := NewSyncer(cfg, store, make(chan string, 8))

			syncer.syncGitHubIssues(ctx, project, []githubIssue{
				{
					Number:    12,
					State:     "open",
					Title:     "terminal state issue",
					Body:      "body",
					HTMLURL:   "https://github.com/org/repo/issues/12",
					UpdatedAt: "2026-02-17T12:10:00Z",
				},
			})
			jobID := getOnlyJobID(t, ctx, store)
			moveJobToState(t, ctx, store, jobID, state)

			syncer.syncGitHubIssues(ctx, project, []githubIssue{
				{
					Number:    12,
					State:     "closed",
					Title:     "terminal state issue",
					Body:      "body",
					HTMLURL:   "https://github.com/org/repo/issues/12",
					UpdatedAt: "2026-02-17T12:15:00Z",
				},
			})

			job, err := store.GetJob(ctx, jobID)
			if err != nil {
				t.Fatalf("get job: %v", err)
			}
			if job.State != state {
				t.Fatalf("expected state %q to stay unchanged, got %q", state, job.State)
			}
			if job.ErrorMessage == db.CancelReasonSourceIssueClosed {
				t.Fatalf("unexpected cancel reason overwrite for state %q", state)
			}

			issue := getIssueBySourceID(t, ctx, store, "my-project", "github", "12")
			if issue.State != "closed" {
				t.Fatalf("expected issue state=closed, got %q", issue.State)
			}
		})
	}
}

func openTestStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return store
}

func getIssueBySourceID(t *testing.T, ctx context.Context, store *db.Store, project, source, sourceIssueID string) db.Issue {
	t.Helper()
	var issueID string
	if err := store.Reader.QueryRowContext(ctx, `
SELECT autopr_issue_id
FROM issues
WHERE project_name = ? AND source = ? AND source_issue_id = ?`,
		project, source, sourceIssueID,
	).Scan(&issueID); err != nil {
		t.Fatalf("lookup issue: %v", err)
	}
	issue, err := store.GetIssueByAPID(ctx, issueID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	return issue
}

func countJobs(t *testing.T, ctx context.Context, store *db.Store) int {
	t.Helper()
	var n int
	if err := store.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs`).Scan(&n); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	return n
}

func getOnlyJobID(t *testing.T, ctx context.Context, store *db.Store) string {
	t.Helper()
	var jobID string
	if err := store.Reader.QueryRowContext(ctx, `SELECT id FROM jobs LIMIT 1`).Scan(&jobID); err != nil {
		t.Fatalf("get only job id: %v", err)
	}
	return jobID
}

func TestGitHubIssueQueryParams(t *testing.T) {
	t.Parallel()

	noCursor := githubIssueQueryParams("")
	if noCursor.Get("state") != "open" {
		t.Fatalf("expected state=open, got %q", noCursor.Get("state"))
	}
	if noCursor.Get("since") != "" {
		t.Fatalf("expected empty since for initial sync, got %q", noCursor.Get("since"))
	}

	withCursor := githubIssueQueryParams("2026-02-17T12:00:00Z")
	if withCursor.Get("state") != "all" {
		t.Fatalf("expected state=all when cursor exists, got %q", withCursor.Get("state"))
	}
	if withCursor.Get("since") != "2026-02-17T12:00:00Z" {
		t.Fatalf("expected since to be set, got %q", withCursor.Get("since"))
	}
}

func moveJobToState(t *testing.T, ctx context.Context, store *db.Store, jobID, state string) {
	t.Helper()

	switch state {
	case "ready", "approved", "rejected":
		claimedID, err := store.ClaimJob(ctx)
		if err != nil || claimedID != jobID {
			t.Fatalf("claim: id=%q err=%v", claimedID, err)
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
		if state == "approved" {
			if err := store.TransitionState(ctx, jobID, "ready", "approved"); err != nil {
				t.Fatalf("ready->approved: %v", err)
			}
		}
		if state == "rejected" {
			if err := store.TransitionState(ctx, jobID, "ready", "rejected"); err != nil {
				t.Fatalf("ready->rejected: %v", err)
			}
		}
	case "failed":
		claimedID, err := store.ClaimJob(ctx)
		if err != nil || claimedID != jobID {
			t.Fatalf("claim: id=%q err=%v", claimedID, err)
		}
		if err := store.TransitionState(ctx, jobID, "planning", "failed"); err != nil {
			t.Fatalf("planning->failed: %v", err)
		}
	case "cancelled":
		if err := store.CancelJob(ctx, jobID); err != nil {
			t.Fatalf("cancel job: %v", err)
		}
	default:
		t.Fatalf("unsupported target state %q", state)
	}
}

func TestParseGitHubNextURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		link string
		want string
	}{
		{
			name: "has next",
			link: `<https://api.github.com/repos/o/r/issues?page=2>; rel="next", <https://api.github.com/repos/o/r/issues?page=5>; rel="last"`,
			want: "https://api.github.com/repos/o/r/issues?page=2",
		},
		{
			name: "last page no next",
			link: `<https://api.github.com/repos/o/r/issues?page=1>; rel="prev", <https://api.github.com/repos/o/r/issues?page=1>; rel="first"`,
			want: "",
		},
		{
			name: "empty header",
			link: "",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseGitHubNextURL(tc.link)
			if got != tc.want {
				t.Fatalf("parseGitHubNextURL: want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestSyncGitHubPaginates(t *testing.T) {
	t.Parallel()

	page1 := []githubIssue{
		{Number: 1, Title: "issue one", Body: "b1", HTMLURL: "https://github.com/o/r/issues/1", UpdatedAt: "2026-02-17T10:00:00Z"},
		{Number: 2, Title: "issue two", Body: "b2", HTMLURL: "https://github.com/o/r/issues/2", UpdatedAt: "2026-02-17T10:01:00Z"},
	}
	page2 := []githubIssue{
		{Number: 3, Title: "issue three", Body: "b3", HTMLURL: "https://github.com/o/r/issues/3", UpdatedAt: "2026-02-17T10:02:00Z"},
	}

	store := openTestStore(t)
	defer store.Close()

	cfg := &config.Config{
		Tokens: config.TokensConfig{GitHub: "test-token"},
		Daemon: config.DaemonConfig{MaxIterations: 3},
	}
	project := &config.ProjectConfig{
		Name: "paginate-test",
		GitHub: &config.ProjectGitHub{
			Owner: "o",
			Repo:  "r",
		},
	}

	syncer := NewSyncer(cfg, store, make(chan string, 8))
	ctx := context.Background()

	// Simulate two pages being processed via syncGitHubIssues (the per-page
	// handler). The pagination loop in syncGitHub drives page fetching;
	// parseGitHubNextURL is tested separately above.
	syncer.syncGitHubIssues(ctx, project, page1)
	syncer.syncGitHubIssues(ctx, project, page2)

	// Verify all 3 issues were upserted.
	for _, num := range []string{"1", "2", "3"} {
		_ = getIssueBySourceID(t, ctx, store, "paginate-test", "github", num)
	}
}
