package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"autopr/internal/db"
	"autopr/internal/config"
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

func setupRunStepsJob(t *testing.T, provider llm.Provider, initialState string) (*Runner, *db.Store, db.Issue, string) {
	t.Helper()

	ctx := context.Background()
	runner, store, jobID := setupInvokeProviderTest(t, provider)

	if initialState != "queued" {
		claimedID, err := store.ClaimJob(ctx)
		if err != nil {
			t.Fatalf("claim job: %v", err)
		}
		if claimedID != jobID {
			t.Fatalf("expected claimed job %q, got %q", jobID, claimedID)
		}
		switch initialState {
		case "planning":
			// Nothing to do; job is now planning.
		case "implementing":
			if err := store.TransitionState(ctx, "planning", "implementing"); err != nil {
				t.Fatalf("planning->implementing: %v", err)
			}
		case "reviewing":
			if err := store.TransitionState(ctx, "planning", "implementing"); err != nil {
				t.Fatalf("planning->implementing: %v", err)
			}
			if err := store.TransitionState(ctx, "implementing", "reviewing"); err != nil {
				t.Fatalf("implementing->reviewing: %v", err)
			}
		case "testing":
			if err := store.TransitionState(ctx, "planning", "implementing"); err != nil {
				t.Fatalf("planning->implementing: %v", err)
			}
			if err := store.TransitionState(ctx, "implementing", "reviewing"); err != nil {
				t.Fatalf("implementing->reviewing: %v", err)
			}
			if err := store.TransitionState(ctx, "reviewing", "testing"); err != nil {
				t.Fatalf("reviewing->testing: %v", err)
			}
		default:
			t.Fatalf("unsupported initial state: %q", initialState)
		}
	}

	issue, err := store.GetIssueByAPID(ctx, mustJobAutoPRIssueID(t, store, jobID))
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}

	return runner, store, issue, jobID
}

func mustJobAutoPRIssueID(t *testing.T, store *db.Store, jobID string) string {
	t.Helper()
	job, err := store.GetJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("get job %s: %v", jobID, err)
	}
	return job.AutoPRIssueID
}

func seedCompletedSessionForStep(t *testing.T, ctx context.Context, store *db.Store, jobID, step string, iteration int) {
	t.Helper()
	sessionID, err := store.CreateSession(ctx, jobID, step, iteration, "codex", "")
	if err != nil {
		t.Fatalf("create %s session: %v", step, err)
	}
	if err := store.CompleteSession(ctx, sessionID, "completed", "done", "prompt", "hash", "", "", "", 1, 2, 3); err != nil {
		t.Fatalf("complete %s session: %v", step, err)
	}
}

func sessionCountForStep(t *testing.T, store *db.Store, ctx context.Context, jobID, step string) int {
	t.Helper()
	sessions, err := store.ListSessionsByJob(ctx, jobID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	n := 0
	for _, session := range sessions {
		if session.Step == step {
			n++
		}
	}
	return n
}

func testProjectConfigWithoutRebase() *config.ProjectConfig {
	return &config.ProjectConfig{
		Name:       "project",
		RepoURL:    "https://example.com/org/repo.git",
		BaseBranch: "main",
		TestCmd:    "",
	}
}

func setupArtifactPrefix(t *testing.T, store *db.Store, jobID, issueID string) {
	t.Helper()
	if _, err := store.CreateArtifact(context.Background(), jobID, issueID, "plan", "plan body", 0, ""); err != nil {
		t.Fatalf("seed plan artifact: %v", err)
	}
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

func TestRunStepsSkipsCompletedPlanAndStartsFromImplementing(t *testing.T) {
	t.Parallel()
	provider := stubProvider{
		run: func(ctx context.Context, workDir, prompt string) (llm.Response, error) {
			return llm.Response{
				InputTokens:  1,
				OutputTokens: 1,
				DurationMS:   1,
				Text:         "approved",
			}, nil
		},
	}

	runner, store, issue, jobID := setupRunStepsJob(t, provider, "planning")
	workDir := t.TempDir()
	ctx := context.Background()

	seedCompletedSessionForStep(t, ctx, store, jobID, "plan", 0)
	setupArtifactPrefix(t, store, jobID, issue.AutoPRIssueID)

	planBefore := sessionCountForStep(t, store, ctx, jobID, "plan")
	if planBefore != 1 {
		t.Fatalf("expected seeded plan session, got %d", planBefore)
	}

	err := runner.runSteps(ctx, jobID, "planning", issue, testProjectConfigWithoutRebase(), workDir)
	if err == nil {
		t.Fatalf("expected runSteps error (testing stage requires git rebase context)")
	}

	if got := sessionCountForStep(t, store, ctx, jobID, "plan"); got != 1 {
		t.Fatalf("plan step should not rerun, got %d sessions", got)
	}
	if got := sessionCountForStep(t, store, ctx, jobID, "implement"); got == 0 {
		t.Fatalf("expected implement step to run after completed plan, got %d", got)
	}
}

func TestRunStepsSkipsCompletedPlanImplementAndReviewAndStartsFromTesting(t *testing.T) {
	t.Parallel()
	provider := stubProvider{
		run: func(ctx context.Context, workDir, prompt string) (llm.Response, error) {
			return llm.Response{
				InputTokens:  1,
				OutputTokens: 1,
				DurationMS:   1,
				Text:         "approved",
			}, nil
		},
	}

	runner, store, issue, jobID := setupRunStepsJob(t, provider, "reviewing")
	ctx := context.Background()
	workDir := t.TempDir()

	seedCompletedSessionForStep(t, ctx, store, jobID, "plan", 0)
	seedCompletedSessionForStep(t, ctx, store, jobID, "implement", 0)
	seedCompletedSessionForStep(t, ctx, store, jobID, "code_review", 0)
	setupArtifactPrefix(t, store, jobID, issue.AutoPRIssueID)

	planBefore := sessionCountForStep(t, store, ctx, jobID, "plan")
	implementBefore := sessionCountForStep(t, store, ctx, jobID, "implement")
	reviewBefore := sessionCountForStep(t, store, ctx, jobID, "code_review")

	err := runner.runSteps(ctx, jobID, "reviewing", issue, testProjectConfigWithoutRebase(), workDir)
	if err == nil {
		t.Fatalf("expected testing-stage failure")
	}

	if got := sessionCountForStep(t, store, ctx, jobID, "plan"); got != planBefore {
		t.Fatalf("expected no new plan sessions, got %d (before %d)", got, planBefore)
	}
	if got := sessionCountForStep(t, store, ctx, jobID, "implement"); got != implementBefore {
		t.Fatalf("expected no new implement sessions, got %d (before %d)", got, implementBefore)
	}
	if got := sessionCountForStep(t, store, ctx, jobID, "code_review"); got != reviewBefore {
		t.Fatalf("expected no new code_review sessions, got %d (before %d)", got, reviewBefore)
	}
}

func TestRunStepsSkipsCompletedPrefixAndStartsFromFirstIncompleteStep(t *testing.T) {
	t.Parallel()
	provider := stubProvider{
		run: func(ctx context.Context, workDir, prompt string) (llm.Response, error) {
			return llm.Response{
				InputTokens:  1,
				OutputTokens: 1,
				DurationMS:   1,
				Text:         "approved",
			}, nil
		},
	}

	runner, store, issue, jobID := setupRunStepsJob(t, provider, "planning")
	ctx := context.Background()
	workDir := t.TempDir()

	seedCompletedSessionForStep(t, ctx, store, jobID, "plan", 0)
	seedCompletedSessionForStep(t, ctx, store, jobID, "implement", 0)
	setupArtifactPrefix(t, store, jobID, issue.AutoPRIssueID)

	planBefore := sessionCountForStep(t, store, ctx, jobID, "plan")
	implementBefore := sessionCountForStep(t, store, ctx, jobID, "implement")
	reviewBefore := sessionCountForStep(t, store, ctx, jobID, "code_review")

	err := runner.runSteps(ctx, jobID, "planning", issue, testProjectConfigWithoutRebase(), workDir)
	if err == nil {
		t.Fatalf("expected testing-stage failure")
	}

	if got := sessionCountForStep(t, store, ctx, jobID, "plan"); got != planBefore {
		t.Fatalf("expected no new plan sessions, got %d (before %d)", got, planBefore)
	}
	if got := sessionCountForStep(t, store, ctx, jobID, "implement"); got != implementBefore {
		t.Fatalf("expected no new implement sessions, got %d (before %d)", got, implementBefore)
	}
	if got := sessionCountForStep(t, store, ctx, jobID, "code_review"); got != reviewBefore+1 {
		t.Fatalf("expected code_review to run once, got %d (before %d)", got, reviewBefore)
	}
}
