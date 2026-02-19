package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	NotificationEventNeedsPR  = "needs_pr"
	NotificationEventFailed   = "failed"
	NotificationEventPRCreated = "pr_created"
	NotificationEventPRMerged  = "pr_merged"
)

const (
	NotificationStatusPending    = "pending"
	NotificationStatusProcessing = "processing"
	NotificationStatusSent       = "sent"
	NotificationStatusFailed     = "failed"
	NotificationStatusSkipped    = "skipped"
)

const recoveredNotificationEventError = "notification dispatcher restarted while event was processing"

type NotificationEvent struct {
	ID        int64
	JobID     string
	EventType string
	Status    string
	Attempts  int
	LastError string
	CreatedAt string
	UpdatedAt string
}

func (s *Store) EnqueueNotificationEvent(ctx context.Context, jobID, eventType string) (int64, error) {
	if err := validateNotificationEventType(eventType); err != nil {
		return 0, err
	}
	res, err := s.Writer.ExecContext(ctx, `
INSERT INTO notification_events(job_id, event_type, status)
VALUES(?, ?, 'pending')`, jobID, eventType)
	if err != nil {
		return 0, fmt.Errorf("enqueue notification event for job %s: %w", jobID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("enqueue notification event for job %s: %w", jobID, err)
	}
	return id, nil
}

func enqueueNotificationEventTx(ctx context.Context, tx *sql.Tx, jobID, eventType string) error {
	if eventType == "" {
		return nil
	}
	if err := validateNotificationEventType(eventType); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO notification_events(job_id, event_type, status)
VALUES(?, ?, 'pending')`, jobID, eventType); err != nil {
		return fmt.Errorf("enqueue notification event for job %s: %w", jobID, err)
	}
	return nil
}

func (s *Store) ListNotificationEvents(ctx context.Context, status string, limit int) ([]NotificationEvent, error) {
	q := `
SELECT id, job_id, event_type, status, attempts, COALESCE(last_error, ''), created_at, updated_at
FROM notification_events`
	args := make([]any, 0, 2)
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY id ASC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.Reader.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list notification events: %w", err)
	}
	defer rows.Close()

	out := make([]NotificationEvent, 0, max(1, limit))
	for rows.Next() {
		var event NotificationEvent
		if err := rows.Scan(
			&event.ID,
			&event.JobID,
			&event.EventType,
			&event.Status,
			&event.Attempts,
			&event.LastError,
			&event.CreatedAt,
			&event.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan notification event: %w", err)
		}
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list notification events: %w", err)
	}
	return out, nil
}

func (s *Store) ClaimNextNotificationEvent(ctx context.Context, maxAttempts int) (NotificationEvent, bool, error) {
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	const q = `
UPDATE notification_events
SET status = 'processing',
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = (
	SELECT id
	FROM notification_events
	WHERE attempts < ?
	  AND (
		status = 'pending'
		OR (
			status = 'failed'
			AND unixepoch(updated_at) <= unixepoch('now') - CASE
				WHEN attempts <= 1 THEN 5
				WHEN attempts = 2 THEN 15
				WHEN attempts = 3 THEN 60
				ELSE 300
			END
		)
	  )
	ORDER BY created_at ASC
	LIMIT 1
)
RETURNING id, job_id, event_type, status, attempts, COALESCE(last_error, ''), created_at, updated_at`

	var event NotificationEvent
	err := s.Writer.QueryRowContext(ctx, q, maxAttempts).Scan(
		&event.ID,
		&event.JobID,
		&event.EventType,
		&event.Status,
		&event.Attempts,
		&event.LastError,
		&event.CreatedAt,
		&event.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return NotificationEvent{}, false, nil
		}
		return NotificationEvent{}, false, fmt.Errorf("claim notification event: %w", err)
	}
	return event, true, nil
}

func (s *Store) MarkNotificationEventSent(ctx context.Context, id int64) error {
	_, err := s.Writer.ExecContext(ctx, `
UPDATE notification_events
SET status = 'sent',
    last_error = '',
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("mark notification event %d sent: %w", id, err)
	}
	return nil
}

func (s *Store) MarkNotificationEventFailed(ctx context.Context, id int64, lastError string) error {
	_, err := s.Writer.ExecContext(ctx, `
UPDATE notification_events
SET status = 'failed',
    attempts = attempts + 1,
    last_error = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?`, trimNotificationError(lastError), id)
	if err != nil {
		return fmt.Errorf("mark notification event %d failed: %w", id, err)
	}
	return nil
}

func (s *Store) MarkNotificationEventSkipped(ctx context.Context, id int64, reason string) error {
	_, err := s.Writer.ExecContext(ctx, `
UPDATE notification_events
SET status = 'skipped',
    last_error = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?`, trimNotificationError(reason), id)
	if err != nil {
		return fmt.Errorf("mark notification event %d skipped: %w", id, err)
	}
	return nil
}

func (s *Store) RecoverProcessingNotificationEvents(ctx context.Context) (int64, error) {
	res, err := s.Writer.ExecContext(ctx, `
UPDATE notification_events
SET status = 'failed',
    attempts = attempts + 1,
    last_error = CASE
		WHEN last_error = '' THEN ?
		ELSE last_error
	END,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE status = 'processing'`, recoveredNotificationEventError)
	if err != nil {
		return 0, fmt.Errorf("recover processing notification events: %w", err)
	}
	return res.RowsAffected()
}

func (s *Store) SkipExhaustedNotificationEvents(ctx context.Context, maxAttempts int) (int64, error) {
	if maxAttempts <= 0 {
		return 0, nil
	}
	res, err := s.Writer.ExecContext(ctx, `
UPDATE notification_events
SET status = 'skipped',
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    last_error = CASE
		WHEN last_error = '' THEN 'max attempts reached'
		ELSE last_error
	END
WHERE status = 'failed' AND attempts >= ?`, maxAttempts)
	if err != nil {
		return 0, fmt.Errorf("skip exhausted notification events: %w", err)
	}
	return res.RowsAffected()
}

func (s *Store) DeleteOldNotificationEvents(ctx context.Context, olderThan time.Duration) (int64, error) {
	if olderThan <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().Add(-olderThan).Format(time.RFC3339)
	res, err := s.Writer.ExecContext(ctx, `
DELETE FROM notification_events
WHERE status IN ('sent', 'skipped')
  AND updated_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete old notification events: %w", err)
	}
	return res.RowsAffected()
}

func validateNotificationEventType(eventType string) error {
	switch eventType {
	case NotificationEventNeedsPR, NotificationEventFailed, NotificationEventPRCreated, NotificationEventPRMerged:
		return nil
	default:
		return fmt.Errorf("unsupported notification event type %q", eventType)
	}
}

func trimNotificationError(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "unknown error"
	}
	if len(msg) > 512 {
		return msg[:512]
	}
	return msg
}
