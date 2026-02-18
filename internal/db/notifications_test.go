package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestNotificationEventsEnqueuedOnStateChanges(t *testing.T) {
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
		SourceIssueID: "900",
		Title:         "notify me",
		URL:           "https://github.com/org/repo/issues/900",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	if _, err := store.ClaimJob(ctx); err != nil {
		t.Fatalf("claim job: %v", err)
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

	if err := store.UpdateJobField(ctx, jobID, "pr_url", "https://github.com/org/repo/pull/900"); err != nil {
		t.Fatalf("set pr_url: %v", err)
	}
	if err := store.TransitionState(ctx, jobID, "ready", "approved"); err != nil {
		t.Fatalf("ready->approved: %v", err)
	}
	if err := store.MarkJobMerged(ctx, jobID, "2026-02-18T00:00:00Z"); err != nil {
		t.Fatalf("mark merged: %v", err)
	}
	if err := store.MarkJobMerged(ctx, jobID, "2026-02-18T00:00:01Z"); err != nil {
		t.Fatalf("mark merged idempotent: %v", err)
	}

	issueID2, err := store.UpsertIssue(ctx, IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "901",
		Title:         "failed event",
		URL:           "https://github.com/org/repo/issues/901",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue2: %v", err)
	}
	jobID2, err := store.CreateJob(ctx, issueID2, "myproject", 3)
	if err != nil {
		t.Fatalf("create job2: %v", err)
	}
	if _, err := store.ClaimJob(ctx); err != nil {
		t.Fatalf("claim job2: %v", err)
	}
	if err := store.TransitionState(ctx, jobID2, "planning", "failed"); err != nil {
		t.Fatalf("planning->failed: %v", err)
	}

	events, err := store.ListNotificationEvents(ctx, "", 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	counts := map[string]int{}
	for _, event := range events {
		counts[event.EventType]++
	}
	if counts[NotificationEventAwaitingApproval] != 1 {
		t.Fatalf("expected 1 awaiting_approval event, got %d", counts[NotificationEventAwaitingApproval])
	}
	if counts[NotificationEventPRCreated] != 1 {
		t.Fatalf("expected 1 pr_created event, got %d", counts[NotificationEventPRCreated])
	}
	if counts[NotificationEventPRMerged] != 1 {
		t.Fatalf("expected 1 pr_merged event, got %d", counts[NotificationEventPRMerged])
	}
	if counts[NotificationEventFailed] != 1 {
		t.Fatalf("expected 1 failed event, got %d", counts[NotificationEventFailed])
	}
}

func TestEnsureJobApprovedEnqueuesPRCreatedOnlyWhenPRExists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	withPR := createTestJobWithState(t, ctx, store, "910", "ready", "autopr/910", "https://github.com/org/repo/pull/910", "", "")
	if err := store.EnsureJobApproved(ctx, withPR); err != nil {
		t.Fatalf("ensure approved with pr: %v", err)
	}
	if err := store.EnsureJobApproved(ctx, withPR); err != nil {
		t.Fatalf("ensure approved idempotent: %v", err)
	}

	withoutPR := createTestJobWithState(t, ctx, store, "911", "ready", "autopr/911", "", "", "")
	if err := store.EnsureJobApproved(ctx, withoutPR); err != nil {
		t.Fatalf("ensure approved without pr: %v", err)
	}

	events, err := store.ListNotificationEvents(ctx, "", 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 event, got %d", len(events))
	}
	if events[0].EventType != NotificationEventPRCreated {
		t.Fatalf("expected pr_created event, got %q", events[0].EventType)
	}
}

func TestNotificationEventLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	jobID := createTestJobWithState(t, ctx, store, "920", "failed", "", "", "", "")
	eventID, err := store.EnqueueNotificationEvent(ctx, jobID, NotificationEventFailed)
	if err != nil {
		t.Fatalf("enqueue event: %v", err)
	}

	event, ok, err := store.ClaimNextNotificationEvent(ctx, 3)
	if err != nil {
		t.Fatalf("claim event: %v", err)
	}
	if !ok {
		t.Fatal("expected event claim")
	}
	if event.ID != eventID {
		t.Fatalf("expected claimed id %d, got %d", eventID, event.ID)
	}
	if event.Status != NotificationStatusProcessing {
		t.Fatalf("expected processing status, got %q", event.Status)
	}

	if err := store.MarkNotificationEventFailed(ctx, eventID, "timeout"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	_, ok, err = store.ClaimNextNotificationEvent(ctx, 3)
	if err != nil {
		t.Fatalf("claim immediately after fail: %v", err)
	}
	if ok {
		t.Fatal("expected retry backoff to prevent immediate reclaim")
	}

	if _, err := store.Writer.ExecContext(ctx, `
UPDATE notification_events
SET updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-30 seconds')
WHERE id = ?`, eventID); err != nil {
		t.Fatalf("age failed event: %v", err)
	}

	event, ok, err = store.ClaimNextNotificationEvent(ctx, 3)
	if err != nil {
		t.Fatalf("claim retried event: %v", err)
	}
	if !ok {
		t.Fatal("expected retried event claim")
	}
	if err := store.MarkNotificationEventSent(ctx, event.ID); err != nil {
		t.Fatalf("mark sent: %v", err)
	}

	events, err := store.ListNotificationEvents(ctx, NotificationStatusSent, 0)
	if err != nil {
		t.Fatalf("list sent events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 sent event, got %d", len(events))
	}
	if events[0].Attempts != 1 {
		t.Fatalf("expected attempts=1 after one failure, got %d", events[0].Attempts)
	}

	jobID2 := createTestJobWithState(t, ctx, store, "921", "failed", "", "", "", "")
	exhaustedID, err := store.EnqueueNotificationEvent(ctx, jobID2, NotificationEventFailed)
	if err != nil {
		t.Fatalf("enqueue exhausted event: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := store.MarkNotificationEventFailed(ctx, exhaustedID, "boom"); err != nil {
			t.Fatalf("mark failed %d: %v", i, err)
		}
	}
	skipped, err := store.SkipExhaustedNotificationEvents(ctx, 3)
	if err != nil {
		t.Fatalf("skip exhausted: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("expected 1 skipped exhausted event, got %d", skipped)
	}

	jobID3 := createTestJobWithState(t, ctx, store, "922", "failed", "", "", "", "")
	processingID, err := store.EnqueueNotificationEvent(ctx, jobID3, NotificationEventFailed)
	if err != nil {
		t.Fatalf("enqueue processing event: %v", err)
	}
	if _, err := store.Writer.ExecContext(ctx, `UPDATE notification_events SET status = 'processing' WHERE id = ?`, processingID); err != nil {
		t.Fatalf("set processing status: %v", err)
	}
	recovered, err := store.RecoverProcessingNotificationEvents(ctx)
	if err != nil {
		t.Fatalf("recover processing events: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("expected 1 recovered processing event, got %d", recovered)
	}
}
