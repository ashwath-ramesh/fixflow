package db

import (
	"context"
	"fmt"
	"strings"
)

const schemaVersion = 1

const schemaSQL = `
CREATE TABLE IF NOT EXISTS schema_version (
    version    INTEGER NOT NULL,
    applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE TABLE IF NOT EXISTS issues (
    autopr_issue_id   TEXT PRIMARY KEY,
    project_name      TEXT NOT NULL,
    source            TEXT NOT NULL CHECK(source IN ('gitlab', 'github', 'sentry')),
    source_issue_id   TEXT NOT NULL,
    title             TEXT NOT NULL,
    body              TEXT NOT NULL DEFAULT '',
    url               TEXT NOT NULL,
    state             TEXT NOT NULL CHECK(state IN ('open', 'closed')),
    labels_json       TEXT NOT NULL DEFAULT '[]',
    source_meta_json  TEXT NOT NULL DEFAULT '{}',
    eligible          INTEGER NOT NULL DEFAULT 1 CHECK(eligible IN (0,1)),
    skip_reason       TEXT NOT NULL DEFAULT '',
    evaluated_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    source_updated_at TEXT NOT NULL,
    synced_at         TEXT NOT NULL,
    UNIQUE(project_name, source, source_issue_id)
);

CREATE TABLE IF NOT EXISTS jobs (
    id              TEXT PRIMARY KEY,
    autopr_issue_id TEXT NOT NULL REFERENCES issues(autopr_issue_id) ON DELETE RESTRICT,
    project_name     TEXT NOT NULL,
    state            TEXT NOT NULL DEFAULT 'queued'
        CHECK(state IN ('queued','planning','implementing','reviewing','testing','ready','approved','rejected','failed','cancelled')),
    iteration        INTEGER NOT NULL DEFAULT 0 CHECK(iteration >= 0),
    max_iterations   INTEGER NOT NULL DEFAULT 3 CHECK(max_iterations > 0),
    worktree_path    TEXT,
    branch_name      TEXT,
    commit_sha       TEXT,
    human_notes      TEXT,
    error_message    TEXT,
    pr_url           TEXT,
    reject_reason    TEXT,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    started_at       TEXT,
    completed_at     TEXT
);

CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state);
CREATE INDEX IF NOT EXISTS idx_jobs_issue ON jobs(autopr_issue_id);
CREATE INDEX IF NOT EXISTS idx_jobs_state_project ON jobs(state, project_name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_one_active_per_issue
    ON jobs(autopr_issue_id)
    WHERE state NOT IN ('approved', 'rejected', 'failed', 'cancelled');

CREATE TABLE IF NOT EXISTS llm_sessions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id        TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    step          TEXT NOT NULL CHECK(step IN ('plan','plan_review','implement','code_review','tests')),
    iteration     INTEGER NOT NULL DEFAULT 0,
    llm_provider  TEXT NOT NULL CHECK(llm_provider IN ('codex', 'claude')),
    prompt_hash   TEXT,
    response_text TEXT,
    prompt_text   TEXT,
    input_tokens  INTEGER,
    output_tokens INTEGER,
    duration_ms   INTEGER,
    jsonl_path    TEXT,
    commit_sha    TEXT,
    status        TEXT NOT NULL DEFAULT 'running' CHECK(status IN ('running','completed','failed','cancelled')),
    error_message TEXT,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    completed_at  TEXT
);

CREATE INDEX IF NOT EXISTS idx_sessions_job ON llm_sessions(job_id);

CREATE TABLE IF NOT EXISTS artifacts (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id           TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    autopr_issue_id  TEXT NOT NULL,
    kind             TEXT NOT NULL CHECK(kind IN ('plan','plan_review','code_review','test_output')),
    content          TEXT NOT NULL,
    iteration        INTEGER NOT NULL DEFAULT 0,
    commit_sha       TEXT,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_artifacts_job ON artifacts(job_id);

CREATE TABLE IF NOT EXISTS sync_cursors (
    project_name   TEXT NOT NULL,
    source         TEXT NOT NULL CHECK(source IN ('gitlab', 'github', 'sentry')),
    cursor_value   TEXT NOT NULL DEFAULT '',
    last_synced_at TEXT NOT NULL,
    PRIMARY KEY(project_name, source)
);

CREATE TABLE IF NOT EXISTS notification_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id     TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL CHECK(event_type IN ('awaiting_approval','failed','pr_created','pr_merged')),
    status     TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','processing','sent','failed','skipped')),
    attempts   INTEGER NOT NULL DEFAULT 0 CHECK(attempts >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_notification_events_status_created
    ON notification_events(status, created_at);
CREATE INDEX IF NOT EXISTS idx_notification_events_job
    ON notification_events(job_id);
`

func (s *Store) createSchema() error {
	if _, err := s.Writer.Exec(schemaSQL); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	// Insert schema version if not present.
	var count int
	if err := s.Writer.QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&count); err != nil {
		return fmt.Errorf("check schema version: %w", err)
	}
	if count == 0 {
		if _, err := s.Writer.Exec("INSERT INTO schema_version (version) VALUES (?)", schemaVersion); err != nil {
			return fmt.Errorf("insert schema version: %w", err)
		}
	}

	// Migrations: add columns that may not exist in older schemas.
	_, _ = s.Writer.Exec("ALTER TABLE llm_sessions ADD COLUMN prompt_text TEXT")
	_, _ = s.Writer.Exec("ALTER TABLE jobs RENAME COLUMN mr_url TO pr_url")
	_, _ = s.Writer.Exec("ALTER TABLE jobs ADD COLUMN pr_merged_at TEXT")
	_, _ = s.Writer.Exec("ALTER TABLE jobs ADD COLUMN pr_closed_at TEXT")
	_, _ = s.Writer.Exec("ALTER TABLE issues ADD COLUMN eligible INTEGER NOT NULL DEFAULT 1 CHECK(eligible IN (0,1))")
	_, _ = s.Writer.Exec("ALTER TABLE issues ADD COLUMN skip_reason TEXT NOT NULL DEFAULT ''")
	_, _ = s.Writer.Exec("ALTER TABLE issues ADD COLUMN evaluated_at TEXT NOT NULL DEFAULT ''")
	if err := s.migrateJobsForCancelledState(); err != nil {
		return err
	}
	if err := s.migrateSessionsForCancelledStatus(); err != nil {
		return err
	}
	// Recreate to ensure predicate includes cancelled for existing DBs.
	if _, err := s.Writer.Exec("DROP INDEX IF EXISTS idx_jobs_one_active_per_issue"); err != nil {
		return fmt.Errorf("drop active-job index: %w", err)
	}
	if _, err := s.Writer.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_one_active_per_issue
		ON jobs(autopr_issue_id)
		WHERE state NOT IN ('approved', 'rejected', 'failed', 'cancelled')`); err != nil {
		return fmt.Errorf("create active-job index: %w", err)
	}

	return nil
}

func (s *Store) tableSQL(table string) (string, error) {
	var sqlText string
	if err := s.Writer.QueryRow(`SELECT COALESCE(sql,'') FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&sqlText); err != nil {
		return "", fmt.Errorf("load %s table SQL: %w", table, err)
	}
	return strings.ToLower(sqlText), nil
}

func (s *Store) migrateJobsForCancelledState() error {
	sqlText, err := s.tableSQL("jobs")
	if err != nil {
		return err
	}
	if strings.Contains(sqlText, "'cancelled'") {
		return nil
	}

	_, _ = s.Writer.Exec("PRAGMA foreign_keys=OFF")
	defer func() { _, _ = s.Writer.Exec("PRAGMA foreign_keys=ON") }()

	tx, err := s.Writer.Begin()
	if err != nil {
		return fmt.Errorf("begin jobs migration: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
CREATE TABLE jobs_new (
    id              TEXT PRIMARY KEY,
    autopr_issue_id TEXT NOT NULL REFERENCES issues(autopr_issue_id) ON DELETE RESTRICT,
    project_name     TEXT NOT NULL,
    state            TEXT NOT NULL DEFAULT 'queued'
        CHECK(state IN ('queued','planning','implementing','reviewing','testing','ready','approved','rejected','failed','cancelled')),
    iteration        INTEGER NOT NULL DEFAULT 0 CHECK(iteration >= 0),
    max_iterations   INTEGER NOT NULL DEFAULT 3 CHECK(max_iterations > 0),
    worktree_path    TEXT,
    branch_name      TEXT,
    commit_sha       TEXT,
    human_notes      TEXT,
    error_message    TEXT,
    pr_url           TEXT,
    reject_reason    TEXT,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    started_at       TEXT,
    completed_at     TEXT,
    pr_merged_at     TEXT,
    pr_closed_at     TEXT
)`); err != nil {
		return fmt.Errorf("create jobs_new: %w", err)
	}

	if _, err := tx.Exec(`
INSERT INTO jobs_new (
    id, autopr_issue_id, project_name, state, iteration, max_iterations,
    worktree_path, branch_name, commit_sha, human_notes, error_message,
    pr_url, reject_reason, created_at, updated_at, started_at, completed_at,
    pr_merged_at, pr_closed_at
)
SELECT
    id, autopr_issue_id, project_name, state, iteration, max_iterations,
    worktree_path, branch_name, commit_sha, human_notes, error_message,
    pr_url, reject_reason, created_at, updated_at, started_at, completed_at,
    pr_merged_at, pr_closed_at
FROM jobs`); err != nil {
		return fmt.Errorf("copy jobs rows: %w", err)
	}

	if _, err := tx.Exec(`DROP TABLE jobs`); err != nil {
		return fmt.Errorf("drop jobs: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE jobs_new RENAME TO jobs`); err != nil {
		return fmt.Errorf("rename jobs_new: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state)`); err != nil {
		return fmt.Errorf("create idx_jobs_state: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_jobs_issue ON jobs(autopr_issue_id)`); err != nil {
		return fmt.Errorf("create idx_jobs_issue: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_jobs_state_project ON jobs(state, project_name)`); err != nil {
		return fmt.Errorf("create idx_jobs_state_project: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit jobs migration: %w", err)
	}
	return nil
}

func (s *Store) migrateSessionsForCancelledStatus() error {
	sqlText, err := s.tableSQL("llm_sessions")
	if err != nil {
		return err
	}
	if strings.Contains(sqlText, "'cancelled'") {
		return nil
	}

	tx, err := s.Writer.Begin()
	if err != nil {
		return fmt.Errorf("begin llm_sessions migration: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
CREATE TABLE llm_sessions_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id        TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    step          TEXT NOT NULL CHECK(step IN ('plan','plan_review','implement','code_review','tests')),
    iteration     INTEGER NOT NULL DEFAULT 0,
    llm_provider  TEXT NOT NULL CHECK(llm_provider IN ('codex', 'claude')),
    prompt_hash   TEXT,
    response_text TEXT,
    prompt_text   TEXT,
    input_tokens  INTEGER,
    output_tokens INTEGER,
    duration_ms   INTEGER,
    jsonl_path    TEXT,
    commit_sha    TEXT,
    status        TEXT NOT NULL DEFAULT 'running' CHECK(status IN ('running','completed','failed','cancelled')),
    error_message TEXT,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    completed_at  TEXT
)`); err != nil {
		return fmt.Errorf("create llm_sessions_new: %w", err)
	}

	if _, err := tx.Exec(`
INSERT INTO llm_sessions_new (
    id, job_id, step, iteration, llm_provider, prompt_hash, response_text, prompt_text,
    input_tokens, output_tokens, duration_ms, jsonl_path, commit_sha, status,
    error_message, created_at, completed_at
)
SELECT
    id, job_id, step, iteration, llm_provider, prompt_hash, response_text, prompt_text,
    input_tokens, output_tokens, duration_ms, jsonl_path, commit_sha, status,
    error_message, created_at, completed_at
FROM llm_sessions`); err != nil {
		return fmt.Errorf("copy llm_sessions rows: %w", err)
	}

	if _, err := tx.Exec(`DROP TABLE llm_sessions`); err != nil {
		return fmt.Errorf("drop llm_sessions: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE llm_sessions_new RENAME TO llm_sessions`); err != nil {
		return fmt.Errorf("rename llm_sessions_new: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_job ON llm_sessions(job_id)`); err != nil {
		return fmt.Errorf("create idx_sessions_job: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit llm_sessions migration: %w", err)
	}
	return nil
}

// RecoverInFlightJobs resets any jobs stuck in active states back to queued.
// Called on daemon startup after a crash.
func (s *Store) RecoverInFlightJobs(ctx context.Context) (int64, error) {
	res, err := s.Writer.ExecContext(ctx,
		`UPDATE jobs SET state = 'queued', updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
		 WHERE state IN ('planning', 'implementing', 'reviewing', 'testing')`)
	if err != nil {
		return 0, fmt.Errorf("recover in-flight jobs: %w", err)
	}
	return res.RowsAffected()
}
