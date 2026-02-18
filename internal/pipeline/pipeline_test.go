package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"autopr/internal/db"
	"autopr/internal/llm"
)

type stubProvider struct {
	run func(ctx context.Context, workDir, prompt string) (llm.Response, error)
}

func (p stubProvider) Name() string { return "codex" }

func (p stubProvider) Run(ctx context.Context, workDir, prompt, jsonlPath string) (llm.Response, error) {
	return p.run(ctx, workDir, prompt)
}

func setupInvokeProviderTest(t *testing.T, provider llm.Provider) (*Runner, *db.Store, string) {
	t.Helper()

	ctx := context.Background()
	store, err := db.Open(filepath.Join(t.TempDir(), "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
		ProjectName:   "myproject",
		Source:        "gitlab",
		SourceIssueID: "1",
		Title:         "pipeline test issue",
		URL:           "https://example.com/issues/1",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}

	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	return &Runner{store: store, provider: provider}, store, jobID
}

func TestInvokeProviderMarksSessionFailedOnProviderError(t *testing.T) {
	provider := stubProvider{
		run: func(ctx context.Context, workDir, prompt string) (llm.Response, error) {
			return llm.Response{InputTokens: 10, OutputTokens: 2, DurationMS: 25}, fmt.Errorf("provider failed")
		},
	}
	runner, store, jobID := setupInvokeProviderTest(t, provider)

	_, err := runner.invokeProvider(context.Background(), jobID, "plan", 0, t.TempDir(), "prompt")
	if err == nil || !strings.Contains(err.Error(), "provider failed") {
		t.Fatalf("expected provider error, got %v", err)
	}

	sessions, err := store.ListSessionsByJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	sess := sessions[0]
	if sess.Status != "failed" {
		t.Fatalf("expected failed status, got %q", sess.Status)
	}
	if sess.ErrorMessage != "provider failed" {
		t.Fatalf("expected provider error message, got %q", sess.ErrorMessage)
	}
	if sess.CompletedAt == "" {
		t.Fatalf("expected completed_at to be set")
	}
}

func TestInvokeProviderCompletesSessionWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	provider := stubProvider{
		run: func(runCtx context.Context, workDir, prompt string) (llm.Response, error) {
			cancel()
			<-runCtx.Done()
			return llm.Response{}, fmt.Errorf("provider canceled: %w", runCtx.Err())
		},
	}
	runner, store, jobID := setupInvokeProviderTest(t, provider)

	_, err := runner.invokeProvider(ctx, jobID, "plan", 0, t.TempDir(), "prompt")
	if !strings.Contains(fmt.Sprint(err), "context canceled") {
		t.Fatalf("expected context canceled error, got %v", err)
	}

	sessions, err := store.ListSessionsByJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	sess := sessions[0]
	if sess.Status != "failed" {
		t.Fatalf("expected failed status, got %q", sess.Status)
	}
	if sess.ErrorMessage != "session interrupted: context canceled" {
		t.Fatalf("unexpected error message: %q", sess.ErrorMessage)
	}
	if sess.CompletedAt == "" {
		t.Fatalf("expected completed_at to be set")
	}
}

func TestInvokeProviderMarksSessionFailedOnPanic(t *testing.T) {
	provider := stubProvider{
		run: func(ctx context.Context, workDir, prompt string) (llm.Response, error) {
			panic("boom")
		},
	}
	runner, store, jobID := setupInvokeProviderTest(t, provider)

	var panicVal any
	func() {
		defer func() {
			panicVal = recover()
		}()
		_, _ = runner.invokeProvider(context.Background(), jobID, "plan", 0, t.TempDir(), "prompt")
	}()
	if panicVal == nil {
		t.Fatalf("expected panic to propagate")
	}

	sessions, err := store.ListSessionsByJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	sess := sessions[0]
	if sess.Status != "failed" {
		t.Fatalf("expected failed status, got %q", sess.Status)
	}
	if !strings.Contains(sess.ErrorMessage, "session interrupted: panic: boom") {
		t.Fatalf("unexpected panic error message: %q", sess.ErrorMessage)
	}
	if sess.CompletedAt == "" {
		t.Fatalf("expected completed_at to be set")
	}
}
