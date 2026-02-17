package db

import (
	"context"
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
