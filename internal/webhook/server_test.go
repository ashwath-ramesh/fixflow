package webhook

import (
	"context"
	"encoding/json"
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
