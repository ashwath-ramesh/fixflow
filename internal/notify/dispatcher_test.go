package notify

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"autopr/internal/db"
)

type stubSender struct {
	name     string
	err      error
	payloads []Payload
}

func (s *stubSender) Name() string { return s.name }

func (s *stubSender) Send(_ context.Context, payload Payload) error {
	s.payloads = append(s.payloads, payload)
	return s.err
}

func TestDispatcherMarksEventSent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openNotifyTestStore(t)
	defer store.Close()

	jobID := createNotifyTestJob(t, ctx, store, "1000", "Fix notifications")
	if _, err := store.EnqueueNotificationEvent(ctx, jobID, TriggerNeedsPR); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	sender := &stubSender{name: "stub"}
	dispatcher := NewDispatcher(store, []Sender{sender}, []string{TriggerNeedsPR})
	processed, err := dispatcher.runOnce(ctx)
	if err != nil {
		t.Fatalf("run once: %v", err)
	}
	if !processed {
		t.Fatal("expected event to be processed")
	}

	events, err := store.ListNotificationEvents(ctx, db.NotificationStatusSent, 0)
	if err != nil {
		t.Fatalf("list sent events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 sent event, got %d", len(events))
	}
	if len(sender.payloads) != 1 {
		t.Fatalf("expected 1 payload sent, got %d", len(sender.payloads))
	}
	if sender.payloads[0].IssueTitle != "Fix notifications" {
		t.Fatalf("expected issue title in payload, got %q", sender.payloads[0].IssueTitle)
	}
}

func TestDispatcherMarksDisabledTriggerSkipped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openNotifyTestStore(t)
	defer store.Close()

	jobID := createNotifyTestJob(t, ctx, store, "1001", "Disabled trigger")
	if _, err := store.EnqueueNotificationEvent(ctx, jobID, TriggerFailed); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	dispatcher := NewDispatcher(store, []Sender{&stubSender{name: "stub"}}, []string{TriggerNeedsPR})
	processed, err := dispatcher.runOnce(ctx)
	if err != nil {
		t.Fatalf("run once: %v", err)
	}
	if !processed {
		t.Fatal("expected event to be processed")
	}

	events, err := store.ListNotificationEvents(ctx, db.NotificationStatusSkipped, 0)
	if err != nil {
		t.Fatalf("list skipped events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 skipped event, got %d", len(events))
	}
}

func TestDispatcherMarksFailuresAndSkipsExhausted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openNotifyTestStore(t)
	defer store.Close()

	jobID := createNotifyTestJob(t, ctx, store, "1002", "Failing sender")
	if _, err := store.EnqueueNotificationEvent(ctx, jobID, TriggerFailed); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	dispatcher := NewDispatcher(store, []Sender{&stubSender{name: "stub", err: errors.New("boom")}}, []string{TriggerFailed})
	dispatcher.maxAttempts = 1
	processed, err := dispatcher.runOnce(ctx)
	if !processed {
		t.Fatal("expected event to be processed")
	}
	if err == nil {
		t.Fatal("expected send failure")
	}

	events, err := store.ListNotificationEvents(ctx, db.NotificationStatusFailed, 0)
	if err != nil {
		t.Fatalf("list failed events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 failed event, got %d", len(events))
	}
	if events[0].Attempts != 1 {
		t.Fatalf("expected attempts=1, got %d", events[0].Attempts)
	}

	dispatcher.cleanup(ctx)
	skipped, err := store.ListNotificationEvents(ctx, db.NotificationStatusSkipped, 0)
	if err != nil {
		t.Fatalf("list skipped events: %v", err)
	}
	if len(skipped) != 1 {
		t.Fatalf("expected exhausted event to be skipped, got %d", len(skipped))
	}
}

func openNotifyTestStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return store
}

func createNotifyTestJob(t *testing.T, ctx context.Context, store *db.Store, sourceIssueID, title string) string {
	t.Helper()
	issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: sourceIssueID,
		Title:         title,
		URL:           "https://github.com/org/repo/issues/" + sourceIssueID,
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
