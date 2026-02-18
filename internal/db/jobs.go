package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/mattn/go-sqlite3"
)

// ErrDuplicateActiveJob is returned when attempting to create a job for an issue
// that already has an active job (caught by the partial unique index).
var ErrDuplicateActiveJob = errors.New("an active job already exists for this issue")

// ValidTransitions defines the allowed state machine transitions.
var ValidTransitions = map[string][]string{
	"queued":       {"planning", "cancelled"},
	"planning":     {"implementing", "failed", "cancelled"},
	"implementing": {"reviewing", "failed", "cancelled"},
	"reviewing":    {"implementing", "testing", "failed", "cancelled"},
	"testing":      {"ready", "implementing", "failed", "cancelled"},
	"ready":        {"approved", "rejected"},
	"failed":       {"queued"},
	"rejected":     {"queued"},
	"cancelled":    {"queued"},
}

// IsCancellableState reports whether a job can be cancelled.
func IsCancellableState(state string) bool {
	switch state {
	case "queued", "planning", "implementing", "reviewing", "testing":
		return true
	default:
		return false
	}
}

// StepForState derives the pipeline step name from job state.
func StepForState(state string) string {
	switch state {
	case "planning":
		return "plan"
	case "implementing":
		return "implement"
	case "reviewing":
		return "code_review"
	case "testing":
		return "tests"
	default:
		return ""
	}
}

// DisplayState returns a display-friendly label for a job state.
func DisplayState(state, prMergedAt, prClosedAt string) string {
	if prMergedAt != "" {
		return "merged"
	}
	if prClosedAt != "" {
		return "pr closed"
	}
	switch state {
	case "ready":
		return "awaiting approval"
	case "approved":
		return "pr created"
	default:
		return state
	}
}

// DisplayStep returns a display-friendly name for an LLM session step,
// aligned with the job state names for consistency across the UI.
func DisplayStep(step string) string {
	switch step {
	case "plan":
		return "planning"
	case "plan_review":
		return "reviewing plan"
	case "implement":
		return "implementing"
	case "code_review":
		return "reviewing"
	case "tests":
		return "testing"
	case "approved":
		return "approved"
	case "merged":
		return "merged"
	case "pr closed":
		return "pr closed"
	default:
		return step
	}
}

type Job struct {
	ID            string
	AutoPRIssueID string
	ProjectName   string
	State         string
	Iteration     int
	MaxIterations int
	WorktreePath  string
	BranchName    string
	CommitSHA     string
	HumanNotes    string
	ErrorMessage  string
	PRURL         string
	RejectReason  string
	PRMergedAt    string
	PRClosedAt    string
	CreatedAt     string
	UpdatedAt     string
	StartedAt     string
	CompletedAt   string

	// Joined from issues table (populated by ListJobs).
	IssueSource   string
	SourceIssueID string
	IssueTitle    string
	IssueURL      string
}

func (s *Store) CreateJob(ctx context.Context, autoprIssueID, projectName string, maxIterations int) (string, error) {
	id, err := newJobID()
	if err != nil {
		return "", err
	}
	const q = `INSERT INTO jobs(id, autopr_issue_id, project_name, state, max_iterations) VALUES(?,?,?,'queued',?)`
	_, err = s.Writer.ExecContext(ctx, q, id, autoprIssueID, projectName, maxIterations)
	if err != nil {
		var sqliteErr sqlite3.Error
		if errors.As(err, &sqliteErr) && sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique {
			return "", ErrDuplicateActiveJob
		}
		return "", fmt.Errorf("create job: %w", err)
	}
	return id, nil
}

// ClaimJob atomically claims the next queued job. Returns empty string if none available.
func (s *Store) ClaimJob(ctx context.Context) (string, error) {
	const q = `
UPDATE jobs SET state = 'planning', started_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
               updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = (
	SELECT j.id
	FROM jobs j
	JOIN issues i ON i.autopr_issue_id = j.autopr_issue_id
	WHERE j.state = 'queued' AND i.eligible = 1
	ORDER BY j.created_at ASC
	LIMIT 1
)
RETURNING id`
	var id string
	err := s.Writer.QueryRowContext(ctx, q).Scan(&id)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("claim job: %w", err)
	}
	return id, nil
}

// TransitionState validates and performs a state transition on a job.
func (s *Store) TransitionState(ctx context.Context, jobID, from, to string) error {
	allowed := ValidTransitions[from]
	valid := false
	for _, s := range allowed {
		if s == to {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid transition: %s -> %s", from, to)
	}
	extra := ""
	if to == "approved" || to == "rejected" || to == "ready" || to == "failed" || to == "cancelled" {
		extra = ", completed_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')"
	}
	q := fmt.Sprintf(`UPDATE jobs SET state = ?, updated_at = strftime('%%Y-%%m-%%dT%%H:%%M:%%SZ', 'now')%s WHERE id = ? AND state = ?`, extra)
	res, err := s.Writer.ExecContext(ctx, q, to, jobID, from)
	if err != nil {
		return fmt.Errorf("transition job %s %s->%s: %w", jobID, from, to, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("job %s not in state %s (concurrent modification?)", jobID, from)
	}
	return nil
}

func (s *Store) GetJob(ctx context.Context, jobID string) (Job, error) {
	const q = `
SELECT id, autopr_issue_id, project_name, state, iteration, max_iterations,
       COALESCE(worktree_path,''), COALESCE(branch_name,''), COALESCE(commit_sha,''),
       COALESCE(human_notes,''), COALESCE(error_message,''), COALESCE(pr_url,''),
       COALESCE(reject_reason,''), COALESCE(pr_merged_at,''), COALESCE(pr_closed_at,''),
       created_at, updated_at, COALESCE(started_at,''), COALESCE(completed_at,'')
FROM jobs WHERE id = ?`
	var j Job
	err := s.Reader.QueryRowContext(ctx, q, jobID).Scan(
		&j.ID, &j.AutoPRIssueID, &j.ProjectName, &j.State, &j.Iteration, &j.MaxIterations,
		&j.WorktreePath, &j.BranchName, &j.CommitSHA,
		&j.HumanNotes, &j.ErrorMessage, &j.PRURL,
		&j.RejectReason, &j.PRMergedAt, &j.PRClosedAt,
		&j.CreatedAt, &j.UpdatedAt, &j.StartedAt, &j.CompletedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return Job{}, fmt.Errorf("job %s not found", jobID)
		}
		return Job{}, fmt.Errorf("get job %s: %w", jobID, err)
	}
	return j, nil
}

func (s *Store) ListJobs(ctx context.Context, project, state string) ([]Job, error) {
	q := `
SELECT j.id, j.autopr_issue_id, j.project_name, j.state, j.iteration, j.max_iterations,
       COALESCE(j.worktree_path,''), COALESCE(j.branch_name,''), COALESCE(j.commit_sha,''),
       COALESCE(j.human_notes,''), COALESCE(j.error_message,''), COALESCE(j.pr_url,''),
       COALESCE(j.reject_reason,''), COALESCE(j.pr_merged_at,''), COALESCE(j.pr_closed_at,''),
       j.created_at, j.updated_at, COALESCE(j.started_at,''), COALESCE(j.completed_at,''),
       COALESCE(i.source,''), COALESCE(i.source_issue_id,''), COALESCE(i.title,''), COALESCE(i.url,'')
FROM jobs j
LEFT JOIN issues i ON j.autopr_issue_id = i.autopr_issue_id
WHERE 1=1`
	var args []any
	if project != "" {
		q += ` AND j.project_name = ?`
		args = append(args, project)
	}
	if state != "" && state != "all" {
		q += ` AND j.state = ?`
		args = append(args, state)
	}
	q += ` ORDER BY j.updated_at DESC`

	rows, err := s.Reader.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(
			&j.ID, &j.AutoPRIssueID, &j.ProjectName, &j.State, &j.Iteration, &j.MaxIterations,
			&j.WorktreePath, &j.BranchName, &j.CommitSHA,
			&j.HumanNotes, &j.ErrorMessage, &j.PRURL,
			&j.RejectReason, &j.PRMergedAt, &j.PRClosedAt,
			&j.CreatedAt, &j.UpdatedAt, &j.StartedAt, &j.CompletedAt,
			&j.IssueSource, &j.SourceIssueID, &j.IssueTitle, &j.IssueURL,
		); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// UpdateJobField updates a single field on a job.
func (s *Store) UpdateJobField(ctx context.Context, jobID, field, value string) error {
	allowed := map[string]bool{
		"worktree_path": true, "branch_name": true, "commit_sha": true,
		"human_notes": true, "error_message": true, "pr_url": true,
		"reject_reason": true, "pr_merged_at": true, "pr_closed_at": true,
	}
	if !allowed[field] {
		return fmt.Errorf("cannot update field %q", field)
	}
	q := fmt.Sprintf(`UPDATE jobs SET %s = ?, updated_at = strftime('%%Y-%%m-%%dT%%H:%%M:%%SZ', 'now') WHERE id = ?`, field)
	_, err := s.Writer.ExecContext(ctx, q, value, jobID)
	if err != nil {
		return fmt.Errorf("update job %s.%s: %w", jobID, field, err)
	}
	return nil
}

// IncrementIteration bumps the iteration counter.
func (s *Store) IncrementIteration(ctx context.Context, jobID string) error {
	_, err := s.Writer.ExecContext(ctx,
		`UPDATE jobs SET iteration = iteration + 1, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?`, jobID)
	if err != nil {
		return fmt.Errorf("increment iteration %s: %w", jobID, err)
	}
	return nil
}

// ResetJobForRetry resets a failed/rejected/cancelled job to queued with fresh state.
func (s *Store) ResetJobForRetry(ctx context.Context, jobID, notes string) error {
	res, err := s.Writer.ExecContext(ctx, `
UPDATE jobs SET state = 'queued', iteration = 0, worktree_path = NULL, branch_name = NULL,
               commit_sha = NULL, error_message = NULL, human_notes = ?,
               started_at = NULL, completed_at = NULL,
               updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? AND state IN ('failed', 'rejected', 'cancelled')
  AND EXISTS (
    SELECT 1 FROM issues i
    WHERE i.autopr_issue_id = jobs.autopr_issue_id AND i.eligible = 1
  )
  AND NOT EXISTS (
    SELECT 1 FROM jobs AS sibling
    WHERE sibling.autopr_issue_id = jobs.autopr_issue_id
      AND sibling.id != jobs.id
      AND (
        sibling.state NOT IN ('approved', 'rejected', 'failed', 'cancelled')
        OR (sibling.state = 'approved' AND sibling.pr_url != ''
            AND (sibling.pr_merged_at IS NULL OR sibling.pr_merged_at = '')
            AND (sibling.pr_closed_at IS NULL OR sibling.pr_closed_at = ''))
      )
  )`, notes, jobID)
	if err != nil {
		return fmt.Errorf("reset job %s: %w", jobID, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		var state string
		var eligible int
		var skipReason string
		var siblingID string
		rowErr := s.Reader.QueryRowContext(ctx, `
SELECT j.state, COALESCE(i.eligible, 1), COALESCE(i.skip_reason, ''),
       COALESCE((
         SELECT s.id FROM jobs s
         WHERE s.autopr_issue_id = j.autopr_issue_id AND s.id != j.id
           AND (
             s.state NOT IN ('approved', 'rejected', 'failed', 'cancelled')
             OR (s.state = 'approved' AND s.pr_url != ''
                 AND (s.pr_merged_at IS NULL OR s.pr_merged_at = '')
                 AND (s.pr_closed_at IS NULL OR s.pr_closed_at = ''))
           )
         LIMIT 1
       ), '')
FROM jobs j
LEFT JOIN issues i ON i.autopr_issue_id = j.autopr_issue_id
WHERE j.id = ?`, jobID).Scan(&state, &eligible, &skipReason, &siblingID)
		if rowErr == nil && siblingID != "" {
			return fmt.Errorf("cannot retry: another active job (%s) already exists for this issue", siblingID)
		}
		if rowErr == nil && eligible == 0 {
			if skipReason != "" {
				return fmt.Errorf("job %s cannot be retried: issue ineligible (%s)", jobID, skipReason)
			}
			return fmt.Errorf("job %s cannot be retried: issue ineligible", jobID)
		}
		return fmt.Errorf("job %s cannot be retried from current state", jobID)
	}
	return nil
}

// CancelJob transitions a single job to cancelled when currently cancellable.
func (s *Store) CancelJob(ctx context.Context, jobID string) error {
	res, err := s.Writer.ExecContext(ctx, `
UPDATE jobs
SET state = 'cancelled',
    completed_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? AND state IN ('queued', 'planning', 'implementing', 'reviewing', 'testing')`, jobID)
	if err != nil {
		return fmt.Errorf("cancel job %s: %w", jobID, err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		return nil
	}

	var state string
	err = s.Reader.QueryRowContext(ctx, `SELECT state FROM jobs WHERE id = ?`, jobID).Scan(&state)
	if err == sql.ErrNoRows {
		return fmt.Errorf("job %s not found", jobID)
	}
	if err != nil {
		return fmt.Errorf("load job %s state: %w", jobID, err)
	}
	return fmt.Errorf("job %s is in state %q and cannot be cancelled", jobID, state)
}

// CancelAllCancellableJobs cancels all jobs currently in cancellable states.
func (s *Store) CancelAllCancellableJobs(ctx context.Context) ([]string, error) {
	rows, err := s.Writer.QueryContext(ctx, `
UPDATE jobs
SET state = 'cancelled',
    completed_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE state IN ('queued', 'planning', 'implementing', 'reviewing', 'testing')
RETURNING id`)
	if err != nil {
		return nil, fmt.Errorf("cancel all jobs: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan cancelled job id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("collect cancelled job ids: %w", err)
	}
	return ids, nil
}

// MarkRunningSessionsCancelled marks any running LLM sessions for a job as cancelled.
func (s *Store) MarkRunningSessionsCancelled(ctx context.Context, jobID string) error {
	_, err := s.Writer.ExecContext(ctx, `
UPDATE llm_sessions
SET status = 'cancelled',
    error_message = CASE WHEN error_message IS NULL OR error_message = '' THEN 'cancelled' ELSE error_message END,
    completed_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE job_id = ? AND status = 'running'`, jobID)
	if err != nil {
		return fmt.Errorf("mark running sessions cancelled for %s: %w", jobID, err)
	}
	return nil
}

// MarkJobMerged sets pr_merged_at on a job.
func (s *Store) MarkJobMerged(ctx context.Context, jobID, mergedAt string) error {
	_, err := s.Writer.ExecContext(ctx,
		`UPDATE jobs SET pr_merged_at = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?`,
		mergedAt, jobID)
	if err != nil {
		return fmt.Errorf("mark job merged %s: %w", jobID, err)
	}
	return nil
}

// MarkJobPRClosed sets pr_closed_at on a job (PR closed without merging).
func (s *Store) MarkJobPRClosed(ctx context.Context, jobID, closedAt string) error {
	_, err := s.Writer.ExecContext(ctx,
		`UPDATE jobs SET pr_closed_at = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?`,
		closedAt, jobID)
	if err != nil {
		return fmt.Errorf("mark job PR closed %s: %w", jobID, err)
	}
	return nil
}

// ListApprovedJobsWithPR returns approved jobs that have a PR URL but haven't been marked as merged or closed.
func (s *Store) ListApprovedJobsWithPR(ctx context.Context) ([]Job, error) {
	const q = `
SELECT j.id, j.autopr_issue_id, j.project_name, j.state, j.iteration, j.max_iterations,
       COALESCE(j.worktree_path,''), COALESCE(j.branch_name,''), COALESCE(j.commit_sha,''),
       COALESCE(j.human_notes,''), COALESCE(j.error_message,''), COALESCE(j.pr_url,''),
       COALESCE(j.reject_reason,''), COALESCE(j.pr_merged_at,''), COALESCE(j.pr_closed_at,''),
       j.created_at, j.updated_at, COALESCE(j.started_at,''), COALESCE(j.completed_at,''),
       COALESCE(i.source,''), COALESCE(i.source_issue_id,''), COALESCE(i.title,''), COALESCE(i.url,'')
FROM jobs j
LEFT JOIN issues i ON j.autopr_issue_id = i.autopr_issue_id
WHERE j.state = 'approved' AND j.pr_url != ''
  AND (j.pr_merged_at IS NULL OR j.pr_merged_at = '')
  AND (j.pr_closed_at IS NULL OR j.pr_closed_at = '')
ORDER BY j.updated_at DESC`
	rows, err := s.Reader.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list approved jobs with PR: %w", err)
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(
			&j.ID, &j.AutoPRIssueID, &j.ProjectName, &j.State, &j.Iteration, &j.MaxIterations,
			&j.WorktreePath, &j.BranchName, &j.CommitSHA,
			&j.HumanNotes, &j.ErrorMessage, &j.PRURL,
			&j.RejectReason, &j.PRMergedAt, &j.PRClosedAt,
			&j.CreatedAt, &j.UpdatedAt, &j.StartedAt, &j.CompletedAt,
			&j.IssueSource, &j.SourceIssueID, &j.IssueTitle, &j.IssueURL,
		); err != nil {
			return nil, fmt.Errorf("scan approved job: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ListCleanableJobs returns jobs whose worktrees can be safely removed:
// rejected/failed/cancelled jobs, and approved jobs where the PR has been merged or closed.
func (s *Store) ListCleanableJobs(ctx context.Context) ([]Job, error) {
	const q = `
SELECT id, autopr_issue_id, project_name, state, iteration, max_iterations,
       COALESCE(worktree_path,''), COALESCE(branch_name,''), COALESCE(commit_sha,''),
       COALESCE(human_notes,''), COALESCE(error_message,''), COALESCE(pr_url,''),
       COALESCE(reject_reason,''), COALESCE(pr_merged_at,''), COALESCE(pr_closed_at,''),
       created_at, updated_at, COALESCE(started_at,''), COALESCE(completed_at,'')
FROM jobs
WHERE worktree_path IS NOT NULL AND worktree_path != ''
  AND (
    state IN ('rejected', 'failed', 'cancelled')
    OR (state = 'approved' AND pr_merged_at IS NOT NULL AND pr_merged_at != '')
    OR (state = 'approved' AND pr_closed_at IS NOT NULL AND pr_closed_at != '')
  )
ORDER BY updated_at DESC`
	rows, err := s.Reader.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list cleanable jobs: %w", err)
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(
			&j.ID, &j.AutoPRIssueID, &j.ProjectName, &j.State, &j.Iteration, &j.MaxIterations,
			&j.WorktreePath, &j.BranchName, &j.CommitSHA,
			&j.HumanNotes, &j.ErrorMessage, &j.PRURL,
			&j.RejectReason, &j.PRMergedAt, &j.PRClosedAt,
			&j.CreatedAt, &j.UpdatedAt, &j.StartedAt, &j.CompletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan cleanable job: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ClearWorktreePath sets worktree_path to NULL for a job after cleanup.
func (s *Store) ClearWorktreePath(ctx context.Context, jobID string) error {
	_, err := s.Writer.ExecContext(ctx,
		`UPDATE jobs SET worktree_path = NULL, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?`,
		jobID)
	if err != nil {
		return fmt.Errorf("clear worktree path %s: %w", jobID, err)
	}
	return nil
}

// HasActiveJobForIssue checks if there's already an active or open-PR job for an issue.
// Returns true if there's a job in progress OR an approved job whose PR hasn't been merged/closed.
func (s *Store) HasActiveJobForIssue(ctx context.Context, autoprIssueID string) (bool, error) {
	const q = `SELECT COUNT(*) FROM jobs WHERE autopr_issue_id = ? AND (
		state NOT IN ('approved', 'rejected', 'failed', 'cancelled')
		OR (state = 'approved' AND pr_url != '' AND (pr_merged_at IS NULL OR pr_merged_at = '') AND (pr_closed_at IS NULL OR pr_closed_at = ''))
	)`
	var count int
	err := s.Reader.QueryRowContext(ctx, q, autoprIssueID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check active job: %w", err)
	}
	return count > 0, nil
}

// GetActiveJobForIssue returns the ID of an active job for the given issue, or empty string if none.
func (s *Store) GetActiveJobForIssue(ctx context.Context, autoprIssueID string) (string, error) {
	const q = `SELECT id FROM jobs WHERE autopr_issue_id = ? AND (
		state NOT IN ('approved', 'rejected', 'failed', 'cancelled')
		OR (state = 'approved' AND pr_url != '' AND (pr_merged_at IS NULL OR pr_merged_at = '') AND (pr_closed_at IS NULL OR pr_closed_at = ''))
	) LIMIT 1`
	var id string
	err := s.Reader.QueryRowContext(ctx, q, autoprIssueID).Scan(&id)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("get active job for issue: %w", err)
	}
	return id, nil
}

// LLM Session operations.

type LLMSession struct {
	ID           int
	JobID        string
	Step         string
	Iteration    int
	LLMProvider  string
	PromptHash   string
	ResponseText string
	PromptText   string
	InputTokens  int
	OutputTokens int
	DurationMS   int
	JSONLPath    string
	CommitSHA    string
	Status       string
	ErrorMessage string
	CreatedAt    string
	CompletedAt  string
}

const recoveredSessionErrorMessage = "session recovered on daemon startup: previous run interrupted"

func (s *Store) CreateSession(ctx context.Context, jobID, step string, iteration int, provider, jsonlPath string) (int64, error) {
	const q = `INSERT INTO llm_sessions(job_id, step, iteration, llm_provider, jsonl_path) VALUES(?,?,?,?,?)`
	res, err := s.Writer.ExecContext(ctx, q, jobID, step, iteration, provider, jsonlPath)
	if err != nil {
		return 0, fmt.Errorf("create session: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) CompleteSession(ctx context.Context, sessionID int64, status, responseText, promptText, promptHash, jsonlPath, commitSHA, errMsg string, inputTokens, outputTokens, durationMS int) error {
	res, err := s.Writer.ExecContext(ctx, `
UPDATE llm_sessions SET status = ?, response_text = ?, prompt_text = ?, prompt_hash = ?, jsonl_path = ?,
                       commit_sha = ?, error_message = ?, input_tokens = ?, output_tokens = ?,
                       duration_ms = ?, completed_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? AND status = 'running'`,
		status, responseText, promptText, promptHash, jsonlPath, commitSHA, errMsg, inputTokens, outputTokens, durationMS, sessionID)
	if err != nil {
		return fmt.Errorf("complete session %d: %w", sessionID, err)
	}
	// Session may already be terminal (e.g. cancelled by user). Treat as no-op.
	_, _ = res.RowsAffected()
	return nil
}

// RecoverRunningSessions marks any stale running LLM sessions as failed.
// Called on daemon startup after a crash/interruption.
func (s *Store) RecoverRunningSessions(ctx context.Context) (int64, error) {
	res, err := s.Writer.ExecContext(ctx, `
UPDATE llm_sessions
SET status = 'failed',
    error_message = COALESCE(NULLIF(error_message, ''), ?),
    input_tokens = COALESCE(input_tokens, 0),
    output_tokens = COALESCE(output_tokens, 0),
    duration_ms = COALESCE(duration_ms, 0),
    completed_at = COALESCE(completed_at, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
WHERE status = 'running'`,
		recoveredSessionErrorMessage,
	)
	if err != nil {
		return 0, fmt.Errorf("recover running sessions: %w", err)
	}
	return res.RowsAffected()
}

func (s *Store) ListSessionsByJob(ctx context.Context, jobID string) ([]LLMSession, error) {
	const q = `
SELECT id, job_id, step, iteration, llm_provider,
       COALESCE(prompt_hash,''), COALESCE(response_text,''),
       COALESCE(input_tokens,0), COALESCE(output_tokens,0), COALESCE(duration_ms,0),
       COALESCE(jsonl_path,''), COALESCE(commit_sha,''), status,
       COALESCE(error_message,''), created_at, COALESCE(completed_at,'')
FROM llm_sessions WHERE job_id = ? ORDER BY id ASC`
	rows, err := s.Reader.QueryContext(ctx, q, jobID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var out []LLMSession
	for rows.Next() {
		var sess LLMSession
		if err := rows.Scan(
			&sess.ID, &sess.JobID, &sess.Step, &sess.Iteration, &sess.LLMProvider,
			&sess.PromptHash, &sess.ResponseText,
			&sess.InputTokens, &sess.OutputTokens, &sess.DurationMS,
			&sess.JSONLPath, &sess.CommitSHA, &sess.Status,
			&sess.ErrorMessage, &sess.CreatedAt, &sess.CompletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// LLMSessionSummary contains only metadata columns (no response_text) for list displays.
type LLMSessionSummary struct {
	ID           int
	JobID        string
	Step         string
	Iteration    int
	LLMProvider  string
	InputTokens  int
	OutputTokens int
	DurationMS   int
	Status       string
	ErrorMessage string
	CreatedAt    string
	CompletedAt  string
}

func (s *Store) ListSessionSummariesByJob(ctx context.Context, jobID string) ([]LLMSessionSummary, error) {
	const q = `
SELECT id, job_id, step, iteration, llm_provider,
       COALESCE(input_tokens,0), COALESCE(output_tokens,0), COALESCE(duration_ms,0),
       status, COALESCE(error_message,''), created_at, COALESCE(completed_at,'')
FROM llm_sessions WHERE job_id = ? ORDER BY id ASC`
	rows, err := s.Reader.QueryContext(ctx, q, jobID)
	if err != nil {
		return nil, fmt.Errorf("list session summaries: %w", err)
	}
	defer rows.Close()

	var out []LLMSessionSummary
	for rows.Next() {
		var sess LLMSessionSummary
		if err := rows.Scan(
			&sess.ID, &sess.JobID, &sess.Step, &sess.Iteration, &sess.LLMProvider,
			&sess.InputTokens, &sess.OutputTokens, &sess.DurationMS,
			&sess.Status, &sess.ErrorMessage, &sess.CreatedAt, &sess.CompletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan session summary: %w", err)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (s *Store) GetFullSession(ctx context.Context, sessionID int) (LLMSession, error) {
	const q = `
SELECT id, job_id, step, iteration, llm_provider,
       COALESCE(prompt_hash,''), COALESCE(response_text,''), COALESCE(prompt_text,''),
       COALESCE(input_tokens,0), COALESCE(output_tokens,0), COALESCE(duration_ms,0),
       COALESCE(jsonl_path,''), COALESCE(commit_sha,''), status,
       COALESCE(error_message,''), created_at, COALESCE(completed_at,'')
FROM llm_sessions WHERE id = ?`
	var sess LLMSession
	err := s.Reader.QueryRowContext(ctx, q, sessionID).Scan(
		&sess.ID, &sess.JobID, &sess.Step, &sess.Iteration, &sess.LLMProvider,
		&sess.PromptHash, &sess.ResponseText, &sess.PromptText,
		&sess.InputTokens, &sess.OutputTokens, &sess.DurationMS,
		&sess.JSONLPath, &sess.CommitSHA, &sess.Status,
		&sess.ErrorMessage, &sess.CreatedAt, &sess.CompletedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return LLMSession{}, fmt.Errorf("session %d not found", sessionID)
		}
		return LLMSession{}, fmt.Errorf("get session %d: %w", sessionID, err)
	}
	return sess, nil
}

// Artifact operations.

type Artifact struct {
	ID            int
	JobID         string
	AutoPRIssueID string
	Kind          string
	Content       string
	Iteration     int
	CommitSHA     string
	CreatedAt     string
}

func (s *Store) CreateArtifact(ctx context.Context, jobID, autoprIssueID, kind, content string, iteration int, commitSHA string) (int64, error) {
	const q = `INSERT INTO artifacts(job_id, autopr_issue_id, kind, content, iteration, commit_sha) VALUES(?,?,?,?,?,?)`
	res, err := s.Writer.ExecContext(ctx, q, jobID, autoprIssueID, kind, content, iteration, commitSHA)
	if err != nil {
		return 0, fmt.Errorf("create artifact: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) GetLatestArtifact(ctx context.Context, jobID, kind string) (Artifact, error) {
	const q = `
SELECT id, job_id, autopr_issue_id, kind, content, iteration, COALESCE(commit_sha,''), created_at
FROM artifacts WHERE job_id = ? AND kind = ? ORDER BY id DESC LIMIT 1`
	var a Artifact
	err := s.Reader.QueryRowContext(ctx, q, jobID, kind).Scan(
		&a.ID, &a.JobID, &a.AutoPRIssueID, &a.Kind, &a.Content, &a.Iteration, &a.CommitSHA, &a.CreatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return Artifact{}, fmt.Errorf("no %s artifact for job %s", kind, jobID)
		}
		return Artifact{}, fmt.Errorf("get artifact: %w", err)
	}
	return a, nil
}

func (s *Store) ListArtifactsByJob(ctx context.Context, jobID string) ([]Artifact, error) {
	const q = `
SELECT id, job_id, autopr_issue_id, kind, content, iteration, COALESCE(commit_sha,''), created_at
FROM artifacts WHERE job_id = ? ORDER BY id ASC`
	rows, err := s.Reader.QueryContext(ctx, q, jobID)
	if err != nil {
		return nil, fmt.Errorf("list artifacts: %w", err)
	}
	defer rows.Close()

	var out []Artifact
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.JobID, &a.AutoPRIssueID, &a.Kind, &a.Content, &a.Iteration, &a.CommitSHA, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan artifact: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ResolveJobID resolves a full or partial job ID prefix to a single job ID.
// Accepts full IDs (ap-job-2dad8b6b5f96e0df), short prefixes (2dad), or
// prefixed short forms (ap-job-2dad). Returns an error if zero or multiple matches.
func (s *Store) ResolveJobID(ctx context.Context, prefix string) (string, error) {
	// Try exact match first.
	var id string
	err := s.Reader.QueryRowContext(ctx, `SELECT id FROM jobs WHERE id = ?`, prefix).Scan(&id)
	if err == nil {
		return id, nil
	}

	// Prefix match: try with and without ap-job- prefix.
	like := prefix + "%"
	if !strings.HasPrefix(prefix, "ap-job-") {
		like = "ap-job-%" + prefix + "%"
	}

	rows, err := s.Reader.QueryContext(ctx, `SELECT id FROM jobs WHERE id LIKE ? ORDER BY updated_at DESC LIMIT 2`, like)
	if err != nil {
		return "", fmt.Errorf("resolve job ID %q: %w", prefix, err)
	}
	defer rows.Close()

	var matches []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return "", fmt.Errorf("scan job ID: %w", err)
		}
		matches = append(matches, m)
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no job matching %q", prefix)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous job prefix %q — matches %s and others", prefix, matches[0])
	}
}

// ShortID returns a human-friendly short form of a job ID (last 8 hex chars).
func ShortID(id string) string {
	// ap-job-2dad8b6b5f96e0df → 2dad8b6b
	if strings.HasPrefix(id, "ap-job-") {
		hex := id[7:]
		if len(hex) >= 8 {
			return hex[:8]
		}
		return hex
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// TokenSummary holds aggregated token/cost data for a job's sessions.
type TokenSummary struct {
	TotalInputTokens  int
	TotalOutputTokens int
	TotalDurationMS   int
	SessionCount      int
	Provider          string // Most-used provider (for cost calculation).
}

// AggregateTokensByJob returns aggregated token counts for a single job.
func (s *Store) AggregateTokensByJob(ctx context.Context, jobID string) (TokenSummary, error) {
	const q = `
SELECT COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
       COALESCE(SUM(duration_ms),0), COUNT(*),
       COALESCE((SELECT llm_provider FROM llm_sessions WHERE job_id = ? AND status IN ('completed','failed')
                 GROUP BY llm_provider ORDER BY COUNT(*) DESC LIMIT 1), '')
FROM llm_sessions WHERE job_id = ? AND status IN ('completed','failed')`
	var ts TokenSummary
	err := s.Reader.QueryRowContext(ctx, q, jobID, jobID).Scan(
		&ts.TotalInputTokens, &ts.TotalOutputTokens,
		&ts.TotalDurationMS, &ts.SessionCount, &ts.Provider,
	)
	if err != nil {
		return TokenSummary{}, fmt.Errorf("aggregate tokens for job %s: %w", jobID, err)
	}
	return ts, nil
}

// AggregateTokensForJobs returns aggregated token counts for multiple jobs.
func (s *Store) AggregateTokensForJobs(ctx context.Context, jobIDs []string) (map[string]TokenSummary, error) {
	if len(jobIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(jobIDs))
	args := make([]any, len(jobIDs))
	for i, id := range jobIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	ph := strings.Join(placeholders, ",")

	q := fmt.Sprintf(`
SELECT job_id, COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
       COALESCE(SUM(duration_ms),0), COUNT(*),
       COALESCE((SELECT s2.llm_provider FROM llm_sessions s2
                 WHERE s2.job_id = llm_sessions.job_id AND s2.status IN ('completed','failed')
                 GROUP BY s2.llm_provider ORDER BY COUNT(*) DESC LIMIT 1), '')
FROM llm_sessions
WHERE job_id IN (%s) AND status IN ('completed','failed')
GROUP BY job_id`, ph)

	rows, err := s.Reader.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("aggregate tokens for jobs: %w", err)
	}
	defer rows.Close()

	out := make(map[string]TokenSummary, len(jobIDs))
	for rows.Next() {
		var ts TokenSummary
		var jobID string
		if err := rows.Scan(&jobID, &ts.TotalInputTokens, &ts.TotalOutputTokens,
			&ts.TotalDurationMS, &ts.SessionCount, &ts.Provider); err != nil {
			return nil, fmt.Errorf("scan token summary: %w", err)
		}
		out[jobID] = ts
	}
	return out, rows.Err()
}

// GetRunningSessionForJob returns the most recent running session for a job, or nil if none.
func (s *Store) GetRunningSessionForJob(ctx context.Context, jobID string) (*LLMSession, error) {
	const q = `
SELECT id, job_id, step, iteration, llm_provider,
       COALESCE(prompt_hash,''), COALESCE(response_text,''),
       COALESCE(input_tokens,0), COALESCE(output_tokens,0), COALESCE(duration_ms,0),
       COALESCE(jsonl_path,''), COALESCE(commit_sha,''), status,
       COALESCE(error_message,''), created_at, COALESCE(completed_at,'')
FROM llm_sessions WHERE job_id = ? AND status = 'running' ORDER BY id DESC LIMIT 1`
	var sess LLMSession
	err := s.Reader.QueryRowContext(ctx, q, jobID).Scan(
		&sess.ID, &sess.JobID, &sess.Step, &sess.Iteration, &sess.LLMProvider,
		&sess.PromptHash, &sess.ResponseText,
		&sess.InputTokens, &sess.OutputTokens, &sess.DurationMS,
		&sess.JSONLPath, &sess.CommitSHA, &sess.Status,
		&sess.ErrorMessage, &sess.CreatedAt, &sess.CompletedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get running session for job %s: %w", jobID, err)
	}
	return &sess, nil
}

// Helpers.

func newJobID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate job id: %w", err)
	}
	return "ap-job-" + strings.ToLower(hex.EncodeToString(buf)), nil
}
