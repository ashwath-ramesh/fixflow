package notify

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"autopr/internal/db"
)

const (
	defaultSendTimeout    = 4 * time.Second
	defaultPollInterval   = 2 * time.Second
	defaultCleanupEvery   = 6 * time.Hour
	defaultRetention      = 7 * 24 * time.Hour
	defaultMaxSendAttempt = 5
)

type Dispatcher struct {
	store        *db.Store
	senders      []Sender
	triggers     map[string]struct{}
	sendTimeout  time.Duration
	pollEvery    time.Duration
	cleanupEvery time.Duration
	retention    time.Duration
	maxAttempts  int
}

func NewDispatcher(store *db.Store, senders []Sender, triggers []string) *Dispatcher {
	return &Dispatcher{
		store:        store,
		senders:      senders,
		triggers:     TriggerSet(triggers),
		sendTimeout:  defaultSendTimeout,
		pollEvery:    defaultPollInterval,
		cleanupEvery: defaultCleanupEvery,
		retention:    defaultRetention,
		maxAttempts:  defaultMaxSendAttempt,
	}
}

func (d *Dispatcher) Run(ctx context.Context) {
	if d.store == nil {
		return
	}

	if recovered, err := d.store.RecoverProcessingNotificationEvents(ctx); err != nil {
		slog.Warn("notify: recover processing events failed", "err", err)
	} else if recovered > 0 {
		slog.Info("notify: recovered processing events", "count", recovered)
	}
	d.cleanup(ctx)

	pollTicker := time.NewTicker(d.pollEvery)
	defer pollTicker.Stop()
	cleanupTicker := time.NewTicker(d.cleanupEvery)
	defer cleanupTicker.Stop()

	for {
		processed, err := d.runOnce(ctx)
		if err != nil {
			slog.Warn("notify: dispatch failed", "err", err)
		}
		if processed {
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-pollTicker.C:
		case <-cleanupTicker.C:
			d.cleanup(ctx)
		}
	}
}

func (d *Dispatcher) runOnce(ctx context.Context) (bool, error) {
	event, ok, err := d.store.ClaimNextNotificationEvent(ctx, d.maxAttempts)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if err := d.processEvent(ctx, event); err != nil {
		return true, err
	}
	return true, nil
}

func (d *Dispatcher) processEvent(ctx context.Context, event db.NotificationEvent) error {
	if len(d.senders) == 0 {
		if err := d.store.MarkNotificationEventSkipped(ctx, event.ID, "no notification channels configured"); err != nil {
			return fmt.Errorf("skip event %d: %w", event.ID, err)
		}
		return nil
	}
	if _, ok := d.triggers[event.EventType]; !ok {
		if err := d.store.MarkNotificationEventSkipped(ctx, event.ID, "trigger disabled"); err != nil {
			return fmt.Errorf("skip disabled event %d: %w", event.ID, err)
		}
		return nil
	}

	payload, err := d.buildPayload(ctx, event)
	if err != nil {
		markErr := d.store.MarkNotificationEventFailed(ctx, event.ID, err.Error())
		if markErr != nil {
			return fmt.Errorf("build payload failed: %v (mark failed: %w)", err, markErr)
		}
		return fmt.Errorf("build payload for event %d: %w", event.ID, err)
	}

	results := SendAll(ctx, d.senders, payload, d.sendTimeout)
	if successCount(results) > 0 {
		if err := d.store.MarkNotificationEventSent(ctx, event.ID); err != nil {
			return fmt.Errorf("mark event %d sent: %w", event.ID, err)
		}
		for _, result := range results {
			if !result.Success {
				slog.Warn("notify: channel send failed", "channel", result.Channel, "job", db.ShortID(event.JobID), "event", event.EventType, "err", result.Error)
			}
		}
		return nil
	}

	summary := summarizeFailures(results)
	if summary == "" {
		summary = "all channels failed"
	}
	if err := d.store.MarkNotificationEventFailed(ctx, event.ID, summary); err != nil {
		return fmt.Errorf("mark event %d failed: %w", event.ID, err)
	}
	return fmt.Errorf("send event %d failed: %s", event.ID, summary)
}

func (d *Dispatcher) buildPayload(ctx context.Context, event db.NotificationEvent) (Payload, error) {
	job, err := d.store.GetJob(ctx, event.JobID)
	if err != nil {
		return Payload{}, fmt.Errorf("load job %s: %w", event.JobID, err)
	}

	issueTitle := strings.TrimSpace(job.IssueTitle)
	if issueTitle == "" {
		issue, issueErr := d.store.GetIssueByAPID(ctx, job.AutoPRIssueID)
		if issueErr != nil {
			slog.Warn("notify: issue lookup failed", "job", db.ShortID(job.ID), "autopr_issue_id", job.AutoPRIssueID, "err", issueErr)
			issueTitle = job.AutoPRIssueID
		} else {
			issueTitle = issue.Title
		}
	}

	return Payload{
		Event:      event.EventType,
		JobID:      job.ID,
		State:      EventState(event.EventType),
		IssueTitle: issueTitle,
		PRURL:      strings.TrimSpace(job.PRURL),
		Project:    job.ProjectName,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func (d *Dispatcher) cleanup(ctx context.Context) {
	skipped, err := d.store.SkipExhaustedNotificationEvents(ctx, d.maxAttempts)
	if err != nil {
		slog.Warn("notify: skip exhausted events failed", "err", err)
	} else if skipped > 0 {
		slog.Info("notify: skipped exhausted events", "count", skipped)
	}

	if d.retention <= 0 {
		return
	}
	deleted, err := d.store.DeleteOldNotificationEvents(ctx, d.retention)
	if err != nil {
		slog.Warn("notify: cleanup failed", "err", err)
	} else if deleted > 0 {
		slog.Debug("notify: cleaned old events", "count", deleted)
	}
}
