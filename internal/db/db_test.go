package db

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpsertIssueAssignsAndPreservesAutoPRIssueID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	firstID, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "sentry",
		SourceIssueID: "95751702",
		Title:         "boom",
		URL:           "https://sentry.local/issues/95751702",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert first: %v", err)
	}
	if firstID == "" || !strings.HasPrefix(firstID, "ap-") {
		t.Fatalf("expected ap- prefixed id, got %q", firstID)
	}

	secondID, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "sentry",
		SourceIssueID: "95751702",
		Title:         "boom updated",
		URL:           "https://sentry.local/issues/95751702",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert second: %v", err)
	}
	if secondID != firstID {
		t.Fatalf("expected stable autopr id, first=%s second=%s", firstID, secondID)
	}

	it, err := store.GetIssueByAPID(ctx, firstID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if it.Title != "boom updated" {
		t.Fatalf("expected updated title, got %s", it.Title)
	}
}

func TestGetIssueByAPIDMissingReturnsError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	_, err = store.GetIssueByAPID(ctx, "missing")
	if err == nil {
		t.Fatalf("expected error for missing issue")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestJobStateTransitions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	// Create issue first.
	ffid, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "gitlab",
		SourceIssueID: "1",
		Title:         "test issue",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}

	// Create job.
	jobID, err := store.CreateJob(ctx, ffid, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if !strings.HasPrefix(jobID, "ap-job-") {
		t.Fatalf("expected ap-job- prefix, got %q", jobID)
	}

	// Claim job (queued -> planning).
	claimedID, err := store.ClaimJob(ctx)
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if claimedID != jobID {
		t.Fatalf("expected claimed job %s, got %s", jobID, claimedID)
	}

	// Verify state.
	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "planning" {
		t.Fatalf("expected planning state, got %s", job.State)
	}

	// Valid transition: planning -> implementing.
	if err := store.TransitionState(ctx, jobID, "planning", "implementing"); err != nil {
		t.Fatalf("transition planning->implementing: %v", err)
	}

	// Invalid transition: implementing -> ready (should fail).
	if err := store.TransitionState(ctx, jobID, "implementing", "ready"); err == nil {
		t.Fatalf("expected error for invalid transition")
	}

	// Valid transition: implementing -> reviewing.
	if err := store.TransitionState(ctx, jobID, "implementing", "reviewing"); err != nil {
		t.Fatalf("transition implementing->reviewing: %v", err)
	}
}

func TestHasActiveJobForIssue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	ffid, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "gitlab",
		SourceIssueID: "2",
		Title:         "test",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// No active job.
	active, err := store.HasActiveJobForIssue(ctx, ffid)
	if err != nil {
		t.Fatalf("check active: %v", err)
	}
	if active {
		t.Fatalf("expected no active job")
	}

	// Create job.
	_, err = store.CreateJob(ctx, ffid, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	// Now active.
	active, err = store.HasActiveJobForIssue(ctx, ffid)
	if err != nil {
		t.Fatalf("check active: %v", err)
	}
	if !active {
		t.Fatalf("expected active job")
	}
}

func TestRecoverInFlightJobs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	ffid, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "gitlab",
		SourceIssueID: "3",
		Title:         "crash test",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	jobID, err := store.CreateJob(ctx, ffid, "myproject", 3)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Simulate in-flight: claim the job (queued -> planning).
	_, err = store.ClaimJob(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Recover.
	n, err := store.RecoverInFlightJobs(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 recovered, got %d", n)
	}

	// Job should be back to queued.
	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if job.State != "queued" {
		t.Fatalf("expected queued, got %s", job.State)
	}
}

func TestRecoverRunningSessionsMarksFailed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	ffid, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "gitlab",
		SourceIssueID: "4",
		Title:         "session recovery test",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	jobID, err := store.CreateJob(ctx, ffid, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	runningID, err := store.CreateSession(ctx, jobID, "plan", 0, "codex", "")
	if err != nil {
		t.Fatalf("create running session: %v", err)
	}

	completedID, err := store.CreateSession(ctx, jobID, "implement", 0, "codex", "")
	if err != nil {
		t.Fatalf("create completed session: %v", err)
	}
	if err := store.CompleteSession(ctx, completedID, "completed", "ok", "prompt", "", "", "", "", 5, 7, 12); err != nil {
		t.Fatalf("complete completed session: %v", err)
	}

	n, err := store.RecoverRunningSessions(ctx)
	if err != nil {
		t.Fatalf("recover running sessions: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 recovered session, got %d", n)
	}

	recovered, err := store.GetFullSession(ctx, int(runningID))
	if err != nil {
		t.Fatalf("get recovered session: %v", err)
	}
	if recovered.Status != "failed" {
		t.Fatalf("expected recovered status failed, got %q", recovered.Status)
	}
	if recovered.ErrorMessage != recoveredSessionErrorMessage {
		t.Fatalf("unexpected recovered error message: %q", recovered.ErrorMessage)
	}
	if recovered.CompletedAt == "" {
		t.Fatalf("expected recovered completed_at to be set")
	}
	if recovered.InputTokens != 0 || recovered.OutputTokens != 0 || recovered.DurationMS != 0 {
		t.Fatalf("expected recovered metrics to default to 0, got %d/%d/%d", recovered.InputTokens, recovered.OutputTokens, recovered.DurationMS)
	}

	completed, err := store.GetFullSession(ctx, int(completedID))
	if err != nil {
		t.Fatalf("get completed session: %v", err)
	}
	if completed.Status != "completed" {
		t.Fatalf("expected completed status unchanged, got %q", completed.Status)
	}
	if completed.ErrorMessage != "" {
		t.Fatalf("expected completed error unchanged, got %q", completed.ErrorMessage)
	}
}

func TestIssueEligibilityRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	eligible := false
	issueID, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "44",
		Title:         "label-gated issue",
		URL:           "https://github.com/org/repo/issues/44",
		State:         "open",
		Eligible:      &eligible,
		SkipReason:    "missing required labels: autopr",
		EvaluatedAt:   "2026-02-17T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}

	issue, err := store.GetIssueByAPID(ctx, issueID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if issue.Eligible {
		t.Fatalf("expected ineligible issue")
	}
	if issue.SkipReason != "missing required labels: autopr" {
		t.Fatalf("unexpected skip reason: %q", issue.SkipReason)
	}
	if issue.EvaluatedAt != "2026-02-17T00:00:00Z" {
		t.Fatalf("unexpected evaluated_at: %q", issue.EvaluatedAt)
	}
}

func TestClaimJobSkipsIneligibleIssues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	ineligible := false
	ineligibleIssueID, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "100",
		Title:         "ineligible issue",
		URL:           "https://github.com/org/repo/issues/100",
		State:         "open",
		Eligible:      &ineligible,
		SkipReason:    "missing required labels: autopr",
	})
	if err != nil {
		t.Fatalf("upsert ineligible issue: %v", err)
	}
	if _, err := store.CreateJob(ctx, ineligibleIssueID, "myproject", 3); err != nil {
		t.Fatalf("create ineligible job: %v", err)
	}

	eligibleIssueID, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "101",
		Title:         "eligible issue",
		URL:           "https://github.com/org/repo/issues/101",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert eligible issue: %v", err)
	}
	eligibleJobID, err := store.CreateJob(ctx, eligibleIssueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create eligible job: %v", err)
	}

	claimedID, err := store.ClaimJob(ctx)
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if claimedID != eligibleJobID {
		t.Fatalf("expected eligible job %q, got %q", eligibleJobID, claimedID)
	}

	job, err := store.GetJob(ctx, eligibleJobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "planning" {
		t.Fatalf("expected claimed job in planning, got %q", job.State)
	}
}

func TestResetJobForRetryBlockedWhenIssueIneligible(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	issueID, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "200",
		Title:         "retry gate issue",
		URL:           "https://github.com/org/repo/issues/200",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}

	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

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

	ineligible := false
	if _, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "200",
		Title:         "retry gate issue",
		URL:           "https://github.com/org/repo/issues/200",
		State:         "open",
		Eligible:      &ineligible,
		SkipReason:    "missing required labels: autopr",
	}); err != nil {
		t.Fatalf("update issue eligibility: %v", err)
	}

	err = store.ResetJobForRetry(ctx, jobID, "retry")
	if err == nil {
		t.Fatalf("expected retry to be blocked")
	}
	if !strings.Contains(err.Error(), "ineligible") {
		t.Fatalf("expected ineligible error, got %v", err)
	}
	if !strings.Contains(err.Error(), "missing required labels: autopr") {
		t.Fatalf("expected skip reason in error, got %v", err)
	}

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "failed" {
		t.Fatalf("expected failed state after blocked retry, got %q", job.State)
	}
}

func TestListIssuesFilters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	ineligible := false
	id1, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "project-a",
		Source:        "github",
		SourceIssueID: "1",
		Title:         "eligible-a",
		URL:           "https://github.com/org/repo/issues/1",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue 1: %v", err)
	}
	id2, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "project-a",
		Source:        "github",
		SourceIssueID: "2",
		Title:         "ineligible-a",
		URL:           "https://github.com/org/repo/issues/2",
		State:         "open",
		Eligible:      &ineligible,
		SkipReason:    "missing required labels: autopr",
	})
	if err != nil {
		t.Fatalf("upsert issue 2: %v", err)
	}
	id3, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "project-b",
		Source:        "gitlab",
		SourceIssueID: "3",
		Title:         "eligible-b",
		URL:           "https://gitlab.com/org/repo/-/issues/3",
		State:         "closed",
	})
	if err != nil {
		t.Fatalf("upsert issue 3: %v", err)
	}

	_, err = store.Writer.ExecContext(ctx, `UPDATE issues SET synced_at = ? WHERE autopr_issue_id = ?`, "2026-02-18T00:00:01Z", id1)
	if err != nil {
		t.Fatalf("set synced_at issue 1: %v", err)
	}
	_, err = store.Writer.ExecContext(ctx, `UPDATE issues SET synced_at = ? WHERE autopr_issue_id = ?`, "2026-02-18T00:00:02Z", id2)
	if err != nil {
		t.Fatalf("set synced_at issue 2: %v", err)
	}
	_, err = store.Writer.ExecContext(ctx, `UPDATE issues SET synced_at = ? WHERE autopr_issue_id = ?`, "2026-02-18T00:00:03Z", id3)
	if err != nil {
		t.Fatalf("set synced_at issue 3: %v", err)
	}

	all, err := store.ListIssues(ctx, "", nil)
	if err != nil {
		t.Fatalf("list all issues: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 issues, got %d", len(all))
	}
	if all[0].SourceIssueID != "3" || all[1].SourceIssueID != "2" || all[2].SourceIssueID != "1" {
		t.Fatalf("expected synced_at desc order, got [%s, %s, %s]", all[0].SourceIssueID, all[1].SourceIssueID, all[2].SourceIssueID)
	}

	projectOnly, err := store.ListIssues(ctx, "project-a", nil)
	if err != nil {
		t.Fatalf("list project issues: %v", err)
	}
	if len(projectOnly) != 2 {
		t.Fatalf("expected 2 project-a issues, got %d", len(projectOnly))
	}
	for _, it := range projectOnly {
		if it.ProjectName != "project-a" {
			t.Fatalf("expected project-a issue, got %q", it.ProjectName)
		}
	}

	eligible := true
	eligibleOnly, err := store.ListIssues(ctx, "", &eligible)
	if err != nil {
		t.Fatalf("list eligible issues: %v", err)
	}
	if len(eligibleOnly) != 2 {
		t.Fatalf("expected 2 eligible issues, got %d", len(eligibleOnly))
	}
	for _, it := range eligibleOnly {
		if !it.Eligible {
			t.Fatalf("expected all eligible issues")
		}
	}

	ineligibleOnly, err := store.ListIssues(ctx, "", &ineligible)
	if err != nil {
		t.Fatalf("list ineligible issues: %v", err)
	}
	if len(ineligibleOnly) != 1 {
		t.Fatalf("expected 1 ineligible issue, got %d", len(ineligibleOnly))
	}
	if ineligibleOnly[0].Eligible {
		t.Fatalf("expected ineligible issue")
	}
	if ineligibleOnly[0].SkipReason != "missing required labels: autopr" {
		t.Fatalf("unexpected skip reason: %q", ineligibleOnly[0].SkipReason)
	}
}

func TestGetIssueSyncSummary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	ineligible := false
	if _, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "project-a",
		Source:        "github",
		SourceIssueID: "1",
		Title:         "eligible-a",
		URL:           "https://github.com/org/repo/issues/1",
		State:         "open",
	}); err != nil {
		t.Fatalf("upsert issue 1: %v", err)
	}
	if _, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "project-a",
		Source:        "github",
		SourceIssueID: "2",
		Title:         "ineligible-a",
		URL:           "https://github.com/org/repo/issues/2",
		State:         "open",
		Eligible:      &ineligible,
		SkipReason:    "missing required labels: autopr",
	}); err != nil {
		t.Fatalf("upsert issue 2: %v", err)
	}
	if _, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "project-b",
		Source:        "gitlab",
		SourceIssueID: "3",
		Title:         "eligible-b",
		URL:           "https://gitlab.com/org/repo/-/issues/3",
		State:         "closed",
	}); err != nil {
		t.Fatalf("upsert issue 3: %v", err)
	}

	summary, err := store.GetIssueSyncSummary(ctx, "")
	if err != nil {
		t.Fatalf("summary all: %v", err)
	}
	if summary.Synced != 3 || summary.Eligible != 2 || summary.Skipped != 1 {
		t.Fatalf("unexpected summary all: %+v", summary)
	}

	projectSummary, err := store.GetIssueSyncSummary(ctx, "project-a")
	if err != nil {
		t.Fatalf("summary project-a: %v", err)
	}
	if projectSummary.Synced != 2 || projectSummary.Eligible != 1 || projectSummary.Skipped != 1 {
		t.Fatalf("unexpected summary project-a: %+v", projectSummary)
	}
}

func TestGetIssueSyncSummaryNoRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	summary, err := store.GetIssueSyncSummary(ctx, "")
	if err != nil {
		t.Fatalf("summary no rows: %v", err)
	}
	if summary.Synced != 0 || summary.Eligible != 0 || summary.Skipped != 0 {
		t.Fatalf("unexpected empty summary: %+v", summary)
	}

	projectSummary, err := store.GetIssueSyncSummary(ctx, "missing-project")
	if err != nil {
		t.Fatalf("summary missing project: %v", err)
	}
	if projectSummary.Synced != 0 || projectSummary.Eligible != 0 || projectSummary.Skipped != 0 {
		t.Fatalf("unexpected missing-project summary: %+v", projectSummary)
	}
}

func TestTransitionToCancelledFromCancellableStates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	tests := []struct {
		name       string
		targetFrom string
		setup      func(t *testing.T, store *Store, jobID string)
	}{
		{name: "queued", targetFrom: "queued"},
		{
			name:       "planning",
			targetFrom: "planning",
			setup: func(t *testing.T, store *Store, jobID string) {
				t.Helper()
				claimed, err := store.ClaimJob(ctx)
				if err != nil || claimed != jobID {
					t.Fatalf("claim: id=%q err=%v", claimed, err)
				}
			},
		},
		{
			name:       "implementing",
			targetFrom: "implementing",
			setup: func(t *testing.T, store *Store, jobID string) {
				t.Helper()
				claimed, err := store.ClaimJob(ctx)
				if err != nil || claimed != jobID {
					t.Fatalf("claim: id=%q err=%v", claimed, err)
				}
				if err := store.TransitionState(ctx, jobID, "planning", "implementing"); err != nil {
					t.Fatalf("planning->implementing: %v", err)
				}
			},
		},
		{
			name:       "reviewing",
			targetFrom: "reviewing",
			setup: func(t *testing.T, store *Store, jobID string) {
				t.Helper()
				claimed, err := store.ClaimJob(ctx)
				if err != nil || claimed != jobID {
					t.Fatalf("claim: id=%q err=%v", claimed, err)
				}
				if err := store.TransitionState(ctx, jobID, "planning", "implementing"); err != nil {
					t.Fatalf("planning->implementing: %v", err)
				}
				if err := store.TransitionState(ctx, jobID, "implementing", "reviewing"); err != nil {
					t.Fatalf("implementing->reviewing: %v", err)
				}
			},
		},
		{
			name:       "testing",
			targetFrom: "testing",
			setup: func(t *testing.T, store *Store, jobID string) {
				t.Helper()
				claimed, err := store.ClaimJob(ctx)
				if err != nil || claimed != jobID {
					t.Fatalf("claim: id=%q err=%v", claimed, err)
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
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issueID, err := store.UpsertIssue(ctx, IssueUpsert{
				ProjectName:   "myproject",
				Source:        "github",
				SourceIssueID: "cancel-" + tc.name,
				Title:         "cancel state " + tc.name,
				URL:           "https://github.com/org/repo/issues/" + tc.name,
				State:         "open",
			})
			if err != nil {
				t.Fatalf("upsert issue: %v", err)
			}
			jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
			if err != nil {
				t.Fatalf("create job: %v", err)
			}
			if tc.setup != nil {
				tc.setup(t, store, jobID)
			}
			if err := store.TransitionState(ctx, jobID, tc.targetFrom, "cancelled"); err != nil {
				t.Fatalf("%s->cancelled: %v", tc.targetFrom, err)
			}
		})
	}
}

func TestCancelJobRejectsTerminalState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	issueID, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "cancel-terminal",
		Title:         "cancel terminal",
		URL:           "https://github.com/org/repo/issues/300",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
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

	err = store.CancelJob(ctx, jobID)
	if err == nil {
		t.Fatalf("expected cancel error for ready job")
	}
	if !strings.Contains(err.Error(), "cannot be cancelled") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResetJobForRetryFromCancelled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	issueID, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "retry-cancelled",
		Title:         "retry cancelled",
		URL:           "https://github.com/org/repo/issues/301",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := store.CancelJob(ctx, jobID); err != nil {
		t.Fatalf("cancel job: %v", err)
	}

	if err := store.ResetJobForRetry(ctx, jobID, "retry after cancel"); err != nil {
		t.Fatalf("reset for retry: %v", err)
	}
	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "queued" {
		t.Fatalf("expected queued, got %q", job.State)
	}
}

func TestHasActiveJobForIssueIgnoresCancelled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	issueID, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "active-cancelled",
		Title:         "active cancelled",
		URL:           "https://github.com/org/repo/issues/302",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	active, err := store.HasActiveJobForIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("check active before cancel: %v", err)
	}
	if !active {
		t.Fatalf("expected active before cancel")
	}

	if err := store.CancelJob(ctx, jobID); err != nil {
		t.Fatalf("cancel job: %v", err)
	}
	active, err = store.HasActiveJobForIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("check active after cancel: %v", err)
	}
	if active {
		t.Fatalf("expected inactive after cancel")
	}
}

func TestListCleanableJobsIncludesCancelled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	issueID, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "clean-cancelled",
		Title:         "clean cancelled",
		URL:           "https://github.com/org/repo/issues/303",
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
	if err := store.CancelJob(ctx, jobID); err != nil {
		t.Fatalf("cancel job: %v", err)
	}

	jobs, err := store.ListCleanableJobs(ctx)
	if err != nil {
		t.Fatalf("list cleanable jobs: %v", err)
	}
	found := false
	for _, j := range jobs {
		if j.ID == jobID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected cancelled job in cleanable list")
	}
}

func TestCancelAllCancellableJobs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	makeJob := func(sourceID string) string {
		t.Helper()
		issueID, err := store.UpsertIssue(ctx, IssueUpsert{
			ProjectName:   "myproject",
			Source:        "github",
			SourceIssueID: sourceID,
			Title:         "bulk cancel " + sourceID,
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

	queuedID := makeJob("401")
	planningID := makeJob("402")
	readyID := makeJob("403")

	claimedID, err := store.ClaimJob(ctx)
	if err != nil {
		t.Fatalf("claim #1: %v", err)
	}
	if claimedID != queuedID {
		t.Fatalf("expected first claimed %q, got %q", queuedID, claimedID)
	}
	claimedID, err = store.ClaimJob(ctx)
	if err != nil {
		t.Fatalf("claim #2: %v", err)
	}
	if claimedID != planningID {
		t.Fatalf("expected second claimed %q, got %q", planningID, claimedID)
	}
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

	cancelledIDs, err := store.CancelAllCancellableJobs(ctx)
	if err != nil {
		t.Fatalf("cancel all: %v", err)
	}
	if len(cancelledIDs) != 2 {
		t.Fatalf("expected 2 cancelled jobs, got %d", len(cancelledIDs))
	}

	for _, id := range []string{queuedID, planningID} {
		job, err := store.GetJob(ctx, id)
		if err != nil {
			t.Fatalf("get cancelled job: %v", err)
		}
		if job.State != "cancelled" {
			t.Fatalf("expected cancelled state for %s, got %q", id, job.State)
		}
	}
	readyJob, err := store.GetJob(ctx, readyID)
	if err != nil {
		t.Fatalf("get ready job: %v", err)
	}
	if readyJob.State != "ready" {
		t.Fatalf("expected ready to remain ready, got %q", readyJob.State)
	}
}

func TestCancelCancellableJobsForIssue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	issueA, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "cancel-by-issue-a",
		Title:         "cancel by issue A",
		URL:           "https://github.com/org/repo/issues/cancel-by-issue-a",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue A: %v", err)
	}
	issueB, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "cancel-by-issue-b",
		Title:         "cancel by issue B",
		URL:           "https://github.com/org/repo/issues/cancel-by-issue-b",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue B: %v", err)
	}

	jobA, err := store.CreateJob(ctx, issueA, "myproject", 3)
	if err != nil {
		t.Fatalf("create job A: %v", err)
	}
	jobB, err := store.CreateJob(ctx, issueB, "myproject", 3)
	if err != nil {
		t.Fatalf("create job B: %v", err)
	}

	cancelledIDs, err := store.CancelCancellableJobsForIssue(ctx, issueA, CancelReasonSourceIssueClosed)
	if err != nil {
		t.Fatalf("cancel by issue: %v", err)
	}
	if len(cancelledIDs) != 1 || cancelledIDs[0] != jobA {
		t.Fatalf("unexpected cancelled IDs: %+v", cancelledIDs)
	}

	cancelledJob, err := store.GetJob(ctx, jobA)
	if err != nil {
		t.Fatalf("get cancelled job: %v", err)
	}
	if cancelledJob.State != "cancelled" {
		t.Fatalf("expected cancelled state, got %q", cancelledJob.State)
	}
	if cancelledJob.ErrorMessage != CancelReasonSourceIssueClosed {
		t.Fatalf("expected cancel reason %q, got %q", CancelReasonSourceIssueClosed, cancelledJob.ErrorMessage)
	}

	otherJob, err := store.GetJob(ctx, jobB)
	if err != nil {
		t.Fatalf("get unaffected job: %v", err)
	}
	if otherJob.State != "queued" {
		t.Fatalf("expected other job to stay queued, got %q", otherJob.State)
	}
	if otherJob.ErrorMessage != "" {
		t.Fatalf("expected other job error_message to stay empty, got %q", otherJob.ErrorMessage)
	}
}

func TestCancelCancellableJobsForIssueDoesNotTouchNonCancellableStates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state string
		setup func(t *testing.T, ctx context.Context, store *Store, jobID string)
	}{
		{
			name:  "ready",
			state: "ready",
			setup: func(t *testing.T, ctx context.Context, store *Store, jobID string) {
				t.Helper()
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
			},
		},
		{
			name:  "approved",
			state: "approved",
			setup: func(t *testing.T, ctx context.Context, store *Store, jobID string) {
				t.Helper()
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
				if err := store.TransitionState(ctx, jobID, "ready", "approved"); err != nil {
					t.Fatalf("ready->approved: %v", err)
				}
			},
		},
		{
			name:  "rejected",
			state: "rejected",
			setup: func(t *testing.T, ctx context.Context, store *Store, jobID string) {
				t.Helper()
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
				if err := store.TransitionState(ctx, jobID, "ready", "rejected"); err != nil {
					t.Fatalf("ready->rejected: %v", err)
				}
			},
		},
		{
			name:  "failed",
			state: "failed",
			setup: func(t *testing.T, ctx context.Context, store *Store, jobID string) {
				t.Helper()
				claimedID, err := store.ClaimJob(ctx)
				if err != nil || claimedID != jobID {
					t.Fatalf("claim: id=%q err=%v", claimedID, err)
				}
				if err := store.TransitionState(ctx, jobID, "planning", "failed"); err != nil {
					t.Fatalf("planning->failed: %v", err)
				}
			},
		},
		{
			name:  "cancelled",
			state: "cancelled",
			setup: func(t *testing.T, ctx context.Context, store *Store, jobID string) {
				t.Helper()
				if err := store.CancelJob(ctx, jobID); err != nil {
					t.Fatalf("cancel job: %v", err)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			tmp := t.TempDir()

			store, err := Open(filepath.Join(tmp, "autopr.db"))
			if err != nil {
				t.Fatalf("open db: %v", err)
			}
			defer store.Close()

			issueID, err := store.UpsertIssue(ctx, IssueUpsert{
				ProjectName:   "myproject",
				Source:        "github",
				SourceIssueID: "cancel-by-issue-terminal-" + tc.name,
				Title:         "cancel by issue terminal " + tc.name,
				URL:           "https://github.com/org/repo/issues/cancel-by-issue-terminal-" + tc.name,
				State:         "open",
			})
			if err != nil {
				t.Fatalf("upsert issue: %v", err)
			}
			jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
			if err != nil {
				t.Fatalf("create job: %v", err)
			}

			tc.setup(t, ctx, store, jobID)

			cancelledIDs, err := store.CancelCancellableJobsForIssue(ctx, issueID, CancelReasonSourceIssueClosed)
			if err != nil {
				t.Fatalf("cancel by issue: %v", err)
			}
			if len(cancelledIDs) != 0 {
				t.Fatalf("expected no cancelled jobs, got %+v", cancelledIDs)
			}

			job, err := store.GetJob(ctx, jobID)
			if err != nil {
				t.Fatalf("get job: %v", err)
			}
			if job.State != tc.state {
				t.Fatalf("expected state %q, got %q", tc.state, job.State)
			}
			if job.ErrorMessage == CancelReasonSourceIssueClosed {
				t.Fatalf("unexpected cancel reason overwrite on %s", tc.state)
			}
		})
	}
}

func TestMarkRunningSessionsCancelledAndCompleteSessionRace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	issueID, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "session-cancel",
		Title:         "session cancel",
		URL:           "https://github.com/org/repo/issues/500",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	sessionID, err := store.CreateSession(ctx, jobID, "plan", 0, "codex", "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := store.MarkRunningSessionsCancelled(ctx, jobID); err != nil {
		t.Fatalf("mark running sessions cancelled: %v", err)
	}

	if err := store.CompleteSession(ctx, sessionID, "failed", "", "", "", "", "", "should not overwrite", 0, 0, 1); err != nil {
		t.Fatalf("complete session after cancel should no-op: %v", err)
	}

	sessions, err := store.ListSessionsByJob(ctx, jobID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].Status != "cancelled" {
		t.Fatalf("expected cancelled session status, got %q", sessions[0].Status)
	}
}

func TestCreateJobDuplicateActiveReturnsErrDuplicate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	ffid, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "dup-1",
		Title:         "duplicate test",
		URL:           "https://github.com/org/repo/issues/dup-1",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}

	// First job should succeed.
	_, err = store.CreateJob(ctx, ffid, "myproject", 3)
	if err != nil {
		t.Fatalf("first create job: %v", err)
	}

	// Second job for the same issue should return sentinel error.
	_, err = store.CreateJob(ctx, ffid, "myproject", 3)
	if err == nil {
		t.Fatalf("expected error on duplicate create")
	}
	if !errors.Is(err, ErrDuplicateActiveJob) {
		t.Fatalf("expected ErrDuplicateActiveJob, got: %v", err)
	}
}

func TestResetJobForRetryBlockedByActiveSibling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	ffid, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "sibling-1",
		Title:         "sibling test",
		URL:           "https://github.com/org/repo/issues/sibling-1",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}

	// Create job A (will be failed).
	jobA, err := store.CreateJob(ctx, ffid, "myproject", 3)
	if err != nil {
		t.Fatalf("create job A: %v", err)
	}

	// Transition job A to failed.
	claimedID, err := store.ClaimJob(ctx)
	if err != nil || claimedID != jobA {
		t.Fatalf("claim job A: id=%q err=%v", claimedID, err)
	}
	if err := store.TransitionState(ctx, jobA, "planning", "failed"); err != nil {
		t.Fatalf("transition A to failed: %v", err)
	}

	// Create job B (active — queued).
	jobB, err := store.CreateJob(ctx, ffid, "myproject", 3)
	if err != nil {
		t.Fatalf("create job B: %v", err)
	}

	// Retry job A should fail due to active sibling B.
	err = store.ResetJobForRetry(ctx, jobA, "retry")
	if err == nil {
		t.Fatalf("expected retry to be blocked by active sibling")
	}
	if !strings.Contains(err.Error(), "another active job") {
		t.Fatalf("expected active sibling error, got: %v", err)
	}
	if !strings.Contains(err.Error(), jobB) {
		t.Fatalf("expected error to contain sibling job ID %s, got: %v", jobB, err)
	}
}

func TestRetrySucceedsWhenNoActiveSibling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	ffid, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "no-sibling-1",
		Title:         "no sibling test",
		URL:           "https://github.com/org/repo/issues/no-sibling-1",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}

	// Create job A, transition to failed.
	jobA, err := store.CreateJob(ctx, ffid, "myproject", 3)
	if err != nil {
		t.Fatalf("create job A: %v", err)
	}
	claimedID, err := store.ClaimJob(ctx)
	if err != nil || claimedID != jobA {
		t.Fatalf("claim job A: id=%q err=%v", claimedID, err)
	}
	if err := store.TransitionState(ctx, jobA, "planning", "failed"); err != nil {
		t.Fatalf("transition A to failed: %v", err)
	}

	// Create job B, also transition to failed (terminal — not active).
	jobB, err := store.CreateJob(ctx, ffid, "myproject", 3)
	if err != nil {
		t.Fatalf("create job B: %v", err)
	}
	claimedID, err = store.ClaimJob(ctx)
	if err != nil || claimedID != jobB {
		t.Fatalf("claim job B: id=%q err=%v", claimedID, err)
	}
	if err := store.TransitionState(ctx, jobB, "planning", "failed"); err != nil {
		t.Fatalf("transition B to failed: %v", err)
	}

	// Retry job A should succeed since sibling B is also in terminal state.
	if err := store.ResetJobForRetry(ctx, jobA, "retry with no active sibling"); err != nil {
		t.Fatalf("reset for retry should succeed: %v", err)
	}

	job, err := store.GetJob(ctx, jobA)
	if err != nil {
		t.Fatalf("get job A: %v", err)
	}
	if job.State != "queued" {
		t.Fatalf("expected queued, got %q", job.State)
	}
}

func TestAggregateTokensByJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	issueID, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "agg-1",
		Title:         "aggregate test",
		URL:           "https://github.com/org/repo/issues/agg-1",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	// No sessions yet — should return zero summary.
	ts, err := store.AggregateTokensByJob(ctx, jobID)
	if err != nil {
		t.Fatalf("aggregate empty: %v", err)
	}
	if ts.SessionCount != 0 {
		t.Fatalf("expected 0 sessions, got %d", ts.SessionCount)
	}

	// Create and complete two sessions.
	s1, err := store.CreateSession(ctx, jobID, "plan", 0, "claude", "/tmp/s1.jsonl")
	if err != nil {
		t.Fatalf("create session 1: %v", err)
	}
	if err := store.CompleteSession(ctx, s1, "completed", "ok", "prompt", "", "/tmp/s1.jsonl", "", "", 100, 50, 1000); err != nil {
		t.Fatalf("complete session 1: %v", err)
	}

	s2, err := store.CreateSession(ctx, jobID, "implement", 0, "claude", "/tmp/s2.jsonl")
	if err != nil {
		t.Fatalf("create session 2: %v", err)
	}
	if err := store.CompleteSession(ctx, s2, "completed", "ok", "prompt", "", "/tmp/s2.jsonl", "", "", 200, 100, 2000); err != nil {
		t.Fatalf("complete session 2: %v", err)
	}

	ts, err = store.AggregateTokensByJob(ctx, jobID)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if ts.SessionCount != 2 {
		t.Fatalf("expected 2 sessions, got %d", ts.SessionCount)
	}
	if ts.TotalInputTokens != 300 {
		t.Fatalf("expected 300 input tokens, got %d", ts.TotalInputTokens)
	}
	if ts.TotalOutputTokens != 150 {
		t.Fatalf("expected 150 output tokens, got %d", ts.TotalOutputTokens)
	}
	if ts.TotalDurationMS != 3000 {
		t.Fatalf("expected 3000ms duration, got %d", ts.TotalDurationMS)
	}
	if ts.Provider != "claude" {
		t.Fatalf("expected claude provider, got %q", ts.Provider)
	}
}

func TestAggregateTokensForJobs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	issueID, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "batch-1",
		Title:         "batch test",
		URL:           "https://github.com/org/repo/issues/batch-1",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	s1, err := store.CreateSession(ctx, jobID, "plan", 0, "codex", "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.CompleteSession(ctx, s1, "completed", "", "", "", "", "", "", 500, 200, 5000); err != nil {
		t.Fatalf("complete session: %v", err)
	}

	result, err := store.AggregateTokensForJobs(ctx, []string{jobID, "nonexistent"})
	if err != nil {
		t.Fatalf("aggregate for jobs: %v", err)
	}
	ts, ok := result[jobID]
	if !ok {
		t.Fatalf("expected entry for %s", jobID)
	}
	if ts.TotalInputTokens != 500 || ts.TotalOutputTokens != 200 {
		t.Fatalf("unexpected tokens: %d/%d", ts.TotalInputTokens, ts.TotalOutputTokens)
	}
	if _, ok := result["nonexistent"]; ok {
		t.Fatalf("did not expect entry for nonexistent job")
	}
}

func TestAggregateTokensForJobsMixedProviders(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	issueID, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "mixed-1",
		Title:         "mixed provider test",
		URL:           "https://github.com/org/repo/issues/mixed-1",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	// 2 claude sessions, 1 codex session → should pick "claude"
	for i, prov := range []string{"claude", "claude", "codex"} {
		sid, err := store.CreateSession(ctx, jobID, "plan", i, prov, "")
		if err != nil {
			t.Fatalf("create session %d: %v", i, err)
		}
		if err := store.CompleteSession(ctx, sid, "completed", "", "", "", "", "", "", 100, 50, 1000); err != nil {
			t.Fatalf("complete session %d: %v", i, err)
		}
	}

	result, err := store.AggregateTokensForJobs(ctx, []string{jobID})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	ts, ok := result[jobID]
	if !ok {
		t.Fatalf("expected entry for %s", jobID)
	}
	if ts.Provider != "claude" {
		t.Fatalf("expected provider 'claude' (most frequent), got %q", ts.Provider)
	}
	if ts.SessionCount != 3 {
		t.Fatalf("expected 3 sessions, got %d", ts.SessionCount)
	}
	if ts.TotalInputTokens != 300 || ts.TotalOutputTokens != 150 {
		t.Fatalf("unexpected tokens: %d/%d", ts.TotalInputTokens, ts.TotalOutputTokens)
	}
}

func TestGetRunningSessionForJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	issueID, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "running-1",
		Title:         "running session test",
		URL:           "https://github.com/org/repo/issues/running-1",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	// No running session.
	sess, err := store.GetRunningSessionForJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get running session (none): %v", err)
	}
	if sess != nil {
		t.Fatalf("expected nil session, got %+v", sess)
	}

	// Create a running session with JSONL path.
	s1, err := store.CreateSession(ctx, jobID, "plan", 0, "claude", "/tmp/live.jsonl")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	sess, err = store.GetRunningSessionForJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get running session: %v", err)
	}
	if sess == nil {
		t.Fatalf("expected running session")
	}
	if sess.ID != int(s1) {
		t.Fatalf("expected session ID %d, got %d", s1, sess.ID)
	}
	if sess.JSONLPath != "/tmp/live.jsonl" {
		t.Fatalf("expected jsonl path /tmp/live.jsonl, got %q", sess.JSONLPath)
	}

	// Complete the session — should no longer appear.
	if err := store.CompleteSession(ctx, s1, "completed", "ok", "", "", "", "", "", 0, 0, 0); err != nil {
		t.Fatalf("complete session: %v", err)
	}
	sess, err = store.GetRunningSessionForJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get running session after complete: %v", err)
	}
	if sess != nil {
		t.Fatalf("expected nil after completion, got %+v", sess)
	}
}

func TestCreateSessionStoresJSONLPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	issueID, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "jsonl-1",
		Title:         "jsonl path test",
		URL:           "https://github.com/org/repo/issues/jsonl-1",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	jsonlPath := "/data/sessions/session-12345.jsonl"
	sessionID, err := store.CreateSession(ctx, jobID, "plan", 0, "claude", jsonlPath)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	sess, err := store.GetFullSession(ctx, int(sessionID))
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.JSONLPath != jsonlPath {
		t.Fatalf("expected jsonl path %q, got %q", jsonlPath, sess.JSONLPath)
	}
}

func TestListReadyOrApprovedJobsWithBranchNoPR(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	keepReady := createTestJobWithState(t, ctx, store, "ready-branch", "ready", "autopr/ready-branch", "", "", "")
	keepApproved := createTestJobWithState(t, ctx, store, "approved-branch", "approved", "autopr/approved-branch", "", "", "")
	_ = createTestJobWithState(t, ctx, store, "ready-no-branch", "ready", "", "", "", "")
	_ = createTestJobWithState(t, ctx, store, "ready-has-pr", "ready", "autopr/ready-has-pr", "https://github.com/org/repo/pull/1", "", "")
	_ = createTestJobWithState(t, ctx, store, "approved-merged", "approved", "autopr/approved-merged", "", "2026-02-18T00:00:00Z", "")
	_ = createTestJobWithState(t, ctx, store, "approved-closed", "approved", "autopr/approved-closed", "", "", "2026-02-18T00:00:00Z")
	_ = createTestJobWithState(t, ctx, store, "failed-branch", "failed", "autopr/failed-branch", "", "", "")

	got, err := store.ListReadyOrApprovedJobsWithBranchNoPR(ctx)
	if err != nil {
		t.Fatalf("list fallback jobs: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 fallback jobs, got %d", len(got))
	}
	ids := map[string]bool{
		got[0].ID: true,
		got[1].ID: true,
	}
	if !ids[keepReady] || !ids[keepApproved] {
		t.Fatalf("unexpected fallback jobs: %+v", []string{got[0].ID, got[1].ID})
	}
}

func TestEnsureJobApproved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	readyJob := createTestJobWithState(t, ctx, store, "ensure-ready", "ready", "autopr/ensure-ready", "", "", "")
	if err := store.EnsureJobApproved(ctx, readyJob); err != nil {
		t.Fatalf("ensure approved from ready: %v", err)
	}
	job, err := store.GetJob(ctx, readyJob)
	if err != nil {
		t.Fatalf("get ready job: %v", err)
	}
	if job.State != "approved" {
		t.Fatalf("expected ready job to become approved, got %q", job.State)
	}

	// Idempotent on already approved jobs.
	if err := store.EnsureJobApproved(ctx, readyJob); err != nil {
		t.Fatalf("ensure approved idempotent: %v", err)
	}

	failedJob := createTestJobWithState(t, ctx, store, "ensure-failed", "failed", "autopr/ensure-failed", "", "", "")
	if err := store.EnsureJobApproved(ctx, failedJob); err != nil {
		t.Fatalf("ensure approved failed job: %v", err)
	}
	job, err = store.GetJob(ctx, failedJob)
	if err != nil {
		t.Fatalf("get failed job: %v", err)
	}
	if job.State != "failed" {
		t.Fatalf("expected failed job unchanged, got %q", job.State)
	}
}

func TestListJobsDefaultSortUpdatedAtDesc(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	jobOld := createTestJobWithOrderFields(t, ctx, store,
		"sort-old", "myproject", "queued", "2025-02-01T10:00:00Z", "2025-02-01T11:00:00Z", "")
	jobNew := createTestJobWithOrderFields(t, ctx, store,
		"sort-new", "myproject", "queued", "2025-02-01T10:30:00Z", "2025-03-01T11:00:00Z", "")

	jobs, err := store.ListJobs(ctx, "", "all", "updated_at", false)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if got, want := len(jobs), 2; got != want {
		t.Fatalf("expected %d jobs, got %d", want, got)
	}
	if jobs[0].ID != jobNew || jobs[1].ID != jobOld {
		t.Fatalf("expected updated_at desc order, got %v, %v", jobs[0].ID, jobs[1].ID)
	}
}

func TestListJobsSortByStateLogicalOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	_ = createTestJobWithOrderFields(t, ctx, store, "state-queued", "myproject", "queued", "2025-02-01T10:01:00Z", "2025-02-01T10:01:00Z", "")
	_ = createTestJobWithOrderFields(t, ctx, store, "state-planning", "myproject", "planning", "2025-02-01T10:02:00Z", "2025-02-01T10:02:00Z", "")
	_ = createTestJobWithOrderFields(t, ctx, store, "state-implementing", "myproject", "implementing", "2025-02-01T10:03:00Z", "2025-02-01T10:03:00Z", "")
	_ = createTestJobWithOrderFields(t, ctx, store, "state-reviewing", "myproject", "reviewing", "2025-02-01T10:04:00Z", "2025-02-01T10:04:00Z", "")
	_ = createTestJobWithOrderFields(t, ctx, store, "state-testing", "myproject", "testing", "2025-02-01T10:05:00Z", "2025-02-01T10:05:00Z", "")
	_ = createTestJobWithOrderFields(t, ctx, store, "state-ready", "myproject", "ready", "2025-02-01T10:06:00Z", "2025-02-01T10:06:00Z", "")
	_ = createTestJobWithOrderFields(t, ctx, store, "state-approved", "myproject", "approved", "2025-02-01T10:07:00Z", "2025-02-01T10:07:00Z", "")
	_ = createTestJobWithOrderFields(t, ctx, store, "state-merged", "myproject", "approved", "2025-02-01T10:08:00Z", "2025-02-01T10:08:00Z", "2025-02-01T10:09:00Z")
	_ = createTestJobWithOrderFields(t, ctx, store, "state-rejected", "myproject", "rejected", "2025-02-01T10:10:00Z", "2025-02-01T10:10:00Z", "")
	_ = createTestJobWithOrderFields(t, ctx, store, "state-failed", "myproject", "failed", "2025-02-01T10:11:00Z", "2025-02-01T10:11:00Z", "")
	_ = createTestJobWithOrderFields(t, ctx, store, "state-cancelled", "myproject", "cancelled", "2025-02-01T10:12:00Z", "2025-02-01T10:12:00Z", "")

	jobs, err := store.ListJobs(ctx, "", "all", "state", true)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}

	if got, want := len(jobs), 11; got != want {
		t.Fatalf("expected %d jobs, got %d", want, got)
	}
	if jobs[0].State != "queued" || jobs[1].State != "planning" || jobs[2].State != "implementing" ||
		jobs[3].State != "reviewing" || jobs[4].State != "testing" || jobs[5].State != "ready" {
		t.Fatalf("expected queue->ready pipeline states first, got %v", []string{jobs[0].State, jobs[1].State, jobs[2].State, jobs[3].State, jobs[4].State, jobs[5].State})
	}

	approvedIdx := -1
	mergedIdx := -1
	rejectedIdx := -1
	failedIdx := -1
	cancelledIdx := -1

	for i, job := range jobs {
		if job.State == "approved" {
			if job.PRMergedAt == "" && approvedIdx == -1 {
				approvedIdx = i
			}
			if job.PRMergedAt != "" && mergedIdx == -1 {
				mergedIdx = i
			}
		}
		switch job.State {
		case "rejected":
			if rejectedIdx == -1 {
				rejectedIdx = i
			}
		case "failed":
			if failedIdx == -1 {
				failedIdx = i
			}
		case "cancelled":
			if cancelledIdx == -1 {
				cancelledIdx = i
			}
		}
	}

	if approvedIdx < 0 {
		t.Fatalf("expected non-merged approved job in state sort order")
	}
	if mergedIdx < 0 {
		t.Fatalf("expected merged job in state sort order")
	}
	if approvedIdx > mergedIdx {
		t.Fatalf("expected non-merged approved before merged job, got indices %d and %d", approvedIdx, mergedIdx)
	}
	if rejectedIdx < mergedIdx || failedIdx < mergedIdx || cancelledIdx < mergedIdx {
		t.Fatalf("expected rejected/failed/cancelled after merged job, got indices: approved=%d merged=%d rejected=%d failed=%d cancelled=%d", approvedIdx, mergedIdx, rejectedIdx, failedIdx, cancelledIdx)
	}
}

func TestListJobsSortByProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	projectA := createTestJobWithOrderFields(t, ctx, store, "project-a", "zulu", "queued", "2025-02-01T10:00:00Z", "2025-02-01T10:00:00Z", "")
	projectB := createTestJobWithOrderFields(t, ctx, store, "project-b", "alpha", "queued", "2025-02-01T10:00:00Z", "2025-02-01T10:00:00Z", "")
	projectC := createTestJobWithOrderFields(t, ctx, store, "project-c", "bravo", "queued", "2025-02-01T10:00:00Z", "2025-02-01T10:00:00Z", "")

	jobs, err := store.ListJobs(ctx, "", "all", "project", true)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if got, want := len(jobs), 3; got != want {
		t.Fatalf("expected %d jobs, got %d", want, got)
	}
	if jobs[0].ID != projectB || jobs[1].ID != projectC || jobs[2].ID != projectA {
		t.Fatalf("expected project asc order alpha, bravo, zulu, got %v", []string{jobs[0].ID, jobs[1].ID, jobs[2].ID})
	}
}

func TestListJobsSortByCreatedAtAscDesc(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	earliestID := createTestJobWithOrderFields(t, ctx, store, "created-old", "myproject", "queued", "2025-02-01T08:00:00Z", "2025-02-01T08:00:00Z", "")
	middleID := createTestJobWithOrderFields(t, ctx, store, "created-mid", "myproject", "queued", "2025-02-01T09:00:00Z", "2025-02-01T09:00:00Z", "")
	latestID := createTestJobWithOrderFields(t, ctx, store, "created-new", "myproject", "queued", "2025-02-01T10:00:00Z", "2025-02-01T10:00:00Z", "")

	desc, err := store.ListJobs(ctx, "", "all", "created_at", false)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(desc) != 3 {
		t.Fatalf("expected 3 jobs desc, got %d", len(desc))
	}
	if desc[0].ID != latestID || desc[1].ID != middleID || desc[2].ID != earliestID {
		t.Fatalf("expected created_at desc order latest->oldest, got %v", []string{desc[0].ID, desc[1].ID, desc[2].ID})
	}

	asc, err := store.ListJobs(ctx, "", "all", "created_at", true)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if asc[0].ID != earliestID || asc[1].ID != middleID || asc[2].ID != latestID {
		t.Fatalf("expected created_at asc order oldest->latest, got %v", []string{asc[0].ID, asc[1].ID, asc[2].ID})
	}
}

func createTestJobWithState(t *testing.T, ctx context.Context, store *Store, sourceIssueID, state, branch, prURL, mergedAt, closedAt string) string {
	t.Helper()
	issueID, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: sourceIssueID,
		Title:         sourceIssueID,
		URL:           "https://github.com/org/repo/issues/" + sourceIssueID,
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue %s: %v", sourceIssueID, err)
	}
	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job %s: %v", sourceIssueID, err)
	}
	if _, err := store.Writer.ExecContext(ctx, `
UPDATE jobs
SET state = ?, branch_name = ?, pr_url = ?, pr_merged_at = ?, pr_closed_at = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?`, state, branch, prURL, mergedAt, closedAt, jobID); err != nil {
		t.Fatalf("configure job %s: %v", sourceIssueID, err)
	}
	return jobID
}

func createTestJobWithOrderFields(t *testing.T, ctx context.Context, store *Store, sourceIssueID, project, state, createdAt, updatedAt, prMergedAt string) string {
	t.Helper()
	issueID, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   project,
		Source:        "github",
		SourceIssueID: sourceIssueID,
		Title:         sourceIssueID,
		URL:           "https://github.com/org/repo/issues/" + sourceIssueID,
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue %s: %v", sourceIssueID, err)
	}
	jobID, err := store.CreateJob(ctx, issueID, project, 3)
	if err != nil {
		t.Fatalf("create job %s: %v", sourceIssueID, err)
	}
	_, err = store.Writer.ExecContext(ctx, `
UPDATE jobs
SET state = ?, created_at = ?, updated_at = ?, pr_merged_at = ?
WHERE id = ?`, state, createdAt, updatedAt, prMergedAt, jobID)
	if err != nil {
		t.Fatalf("set order fields for job %s: %v", sourceIssueID, err)
	}
	return jobID
}
