package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"autopr/internal/config"
	"autopr/internal/db"
)

func TestHealth_OK(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := db.Open(filepath.Join(t.TempDir(), "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	// Seed one non-queued job and two queued jobs.
	failedJobID := seedQueuedJob(t, ctx, store, "failed")
	claimedID, err := store.ClaimJob(ctx)
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if claimedID != failedJobID {
		t.Fatalf("expected claimed job %q, got %q", failedJobID, claimedID)
	}
	if err := store.TransitionState(ctx, failedJobID, "planning", "failed"); err != nil {
		t.Fatalf("transition job to failed: %v", err)
	}
	_ = seedQueuedJob(t, ctx, store, "queued-1")
	_ = seedQueuedJob(t, ctx, store, "queued-2")

	srv := NewServer(&config.Config{}, store, make(chan string, 1))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("unexpected content type %q", rec.Header().Get("Content-Type"))
	}

	var got struct {
		Status        string `json:"status"`
		UptimeSeconds int    `json:"uptime_seconds"`
		JobQueueDepth int    `json:"job_queue_depth"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Status != "running" {
		t.Fatalf("expected status running, got %q", got.Status)
	}
	if got.JobQueueDepth != 2 {
		t.Fatalf("expected job_queue_depth=2, got %d", got.JobQueueDepth)
	}
	if got.UptimeSeconds < 0 {
		t.Fatalf("expected non-negative uptime, got %d", got.UptimeSeconds)
	}
}

func TestHealth_DBError(t *testing.T) {
	t.Parallel()

	store, err := db.Open(filepath.Join(t.TempDir(), "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Writer.Close()

	srv := NewServer(&config.Config{}, store, make(chan string, 1))
	if err := store.Reader.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("unexpected content type %q", rec.Header().Get("Content-Type"))
	}

	var got map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["error"] != "internal error" {
		t.Fatalf("expected internal error payload, got %+v", got)
	}
}

func seedQueuedJob(t *testing.T, ctx context.Context, store *db.Store, sourceIssueID string) string {
	t.Helper()

	issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
		ProjectName:   "test-project",
		Source:        "gitlab",
		SourceIssueID: sourceIssueID,
		Title:         "issue " + sourceIssueID,
		URL:           "https://gitlab.local/" + sourceIssueID,
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue %q: %v", sourceIssueID, err)
	}

	jobID, err := store.CreateJob(ctx, issueID, "test-project", 3)
	if err != nil {
		t.Fatalf("create job %q: %v", sourceIssueID, err)
	}
	return jobID
}

func TestIssueHookCloseCancelsActiveJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store, err := db.Open(filepath.Join(t.TempDir(), "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	cfg := &config.Config{
		Daemon: config.DaemonConfig{MaxIterations: 3},
		Projects: []config.ProjectConfig{
			{
				Name:   "test-project",
				GitLab: &config.ProjectGitLab{ProjectID: "123"},
			},
		},
	}
	srv := NewServer(cfg, store, make(chan string, 1))

	issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
		ProjectName:   "test-project",
		Source:        "gitlab",
		SourceIssueID: "42",
		Title:         "existing issue",
		URL:           "https://gitlab.local/group/repo/-/issues/42",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, "test-project", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.CreateSession(ctx, jobID, "plan", 0, "codex", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(issueHookPayload(t, 123, 42, "close", "closed")))
	req.Header.Set("X-Gitlab-Event", "Issue Hook")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "cancelled" {
		t.Fatalf("expected cancelled job, got %q", job.State)
	}
	if job.ErrorMessage != db.CancelReasonSourceIssueClosed {
		t.Fatalf("expected error_message %q, got %q", db.CancelReasonSourceIssueClosed, job.ErrorMessage)
	}

	sessions, err := store.ListSessionsByJob(ctx, jobID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Status != "cancelled" {
		t.Fatalf("expected cancelled session, got %+v", sessions)
	}

	issue, err := store.GetIssueByAPID(ctx, issueID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if issue.State != "closed" {
		t.Fatalf("expected closed issue state, got %q", issue.State)
	}
}

func TestIssueHookCloseLeavesNonCancellableJobUnchanged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store, err := db.Open(filepath.Join(t.TempDir(), "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	cfg := &config.Config{
		Daemon: config.DaemonConfig{MaxIterations: 3},
		Projects: []config.ProjectConfig{
			{
				Name:   "test-project",
				GitLab: &config.ProjectGitLab{ProjectID: "123"},
			},
		},
	}
	srv := NewServer(cfg, store, make(chan string, 1))

	issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
		ProjectName:   "test-project",
		Source:        "gitlab",
		SourceIssueID: "43",
		Title:         "ready issue",
		URL:           "https://gitlab.local/group/repo/-/issues/43",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, "test-project", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

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

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(issueHookPayload(t, 123, 43, "close", "closed")))
	req.Header.Set("X-Gitlab-Event", "Issue Hook")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "ready" {
		t.Fatalf("expected ready to remain unchanged, got %q", job.State)
	}
	if job.ErrorMessage == db.CancelReasonSourceIssueClosed {
		t.Fatalf("unexpected cancel reason overwrite on ready job")
	}

	issue, err := store.GetIssueByAPID(ctx, issueID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if issue.State != "closed" {
		t.Fatalf("expected closed issue state, got %q", issue.State)
	}
}

func issueHookPayload(t *testing.T, projectID, iid int, action, state string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"object_kind": "issue",
		"object_attributes": map[string]any{
			"iid":         iid,
			"title":       "issue title",
			"description": "issue description",
			"url":         fmt.Sprintf("https://gitlab.local/group/repo/-/issues/%d", iid),
			"action":      action,
			"state":       state,
		},
		"project": map[string]any{
			"id": projectID,
		},
		"labels": []map[string]any{{"title": "bug"}},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return body
}
