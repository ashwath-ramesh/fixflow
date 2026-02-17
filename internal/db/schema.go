package db

import (
	"context"
	"fmt"
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
        CHECK(state IN ('queued','planning','implementing','reviewing','testing','ready','approved','rejected','failed')),
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
    WHERE state NOT IN ('approved', 'rejected', 'failed');

CREATE TABLE IF NOT EXISTS llm_sessions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id        TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    step          TEXT NOT NULL CHECK(step IN ('plan','plan_review','implement','code_review','tests')),
    iteration     INTEGER NOT NULL DEFAULT 0,
    llm_provider  TEXT NOT NULL CHECK(llm_provider IN ('codex', 'claude')),
    prompt_hash   TEXT,
    response_text TEXT,
    input_tokens  INTEGER,
    output_tokens INTEGER,
    duration_ms   INTEGER,
    jsonl_path    TEXT,
    commit_sha    TEXT,
    status        TEXT NOT NULL DEFAULT 'running' CHECK(status IN ('running','completed','failed')),
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
