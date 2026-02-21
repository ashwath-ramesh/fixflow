package db

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"autopr/internal/git"
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
        CHECK(state IN ('queued','planning','implementing','reviewing','testing','ready','rebasing','resolving_conflicts','awaiting_checks','approved','rejected','failed','cancelled')),
    iteration        INTEGER NOT NULL DEFAULT 0 CHECK(iteration >= 0),
    max_iterations   INTEGER NOT NULL DEFAULT 3 CHECK(max_iterations > 0),
    worktree_path    TEXT,
    branch_name      TEXT,
    commit_sha       TEXT,
    human_notes      TEXT,
    error_message    TEXT,
    pr_url           TEXT,
    pr_merged_at     TEXT,
    pr_closed_at     TEXT,
    reject_reason    TEXT,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    started_at       TEXT,
    completed_at     TEXT,
    ci_started_at    TEXT,
    ci_completed_at  TEXT,
    ci_status_summary TEXT
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
    step          TEXT NOT NULL CHECK(step IN ('plan','plan_review','implement','code_review','tests','conflict_resolution')),
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
CREATE INDEX IF NOT EXISTS idx_sessions_job_iteration_step_status
    ON llm_sessions(job_id, iteration, step, status);

CREATE TABLE IF NOT EXISTS artifacts (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id           TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    autopr_issue_id  TEXT NOT NULL,
    kind             TEXT NOT NULL CHECK(kind IN ('plan','plan_review','code_review','test_output','rebase_conflict','rebase_result')),
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
    event_type TEXT NOT NULL CHECK(event_type IN ('needs_pr','failed','pr_created','pr_merged')),
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
	_, _ = s.Writer.Exec("ALTER TABLE jobs ADD COLUMN ci_started_at TEXT")
	_, _ = s.Writer.Exec("ALTER TABLE jobs ADD COLUMN ci_completed_at TEXT")
	_, _ = s.Writer.Exec("ALTER TABLE jobs ADD COLUMN ci_status_summary TEXT")
	_, _ = s.Writer.Exec("ALTER TABLE issues ADD COLUMN eligible INTEGER NOT NULL DEFAULT 1 CHECK(eligible IN (0,1))")
	_, _ = s.Writer.Exec("ALTER TABLE issues ADD COLUMN skip_reason TEXT NOT NULL DEFAULT ''")
	_, _ = s.Writer.Exec("ALTER TABLE issues ADD COLUMN evaluated_at TEXT NOT NULL DEFAULT ''")
	if err := s.migrateJobsForCancelledState(); err != nil {
		return err
	}
	if err := s.migrateJobsForRebasingState(); err != nil {
		return err
	}
	if err := s.migrateSessionsForCancelledStatus(); err != nil {
		return err
	}
	if err := s.migrateSessionsForConflictResolutionStep(); err != nil {
		return err
	}
	if err := s.migrateArtifactsForRebaseKind(); err != nil {
		return err
	}
	if err := s.migrateArtifactsForRebaseResultKind(); err != nil {
		return err
	}
	if err := s.migrateJobsForAwaitingChecksState(); err != nil {
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

	if err := s.migrateNotificationEventsNeedsPR(); err != nil {
		return err
	}

	// Ensure CI metadata columns exist even if an older migration recreated jobs.
	_, _ = s.Writer.Exec("ALTER TABLE jobs ADD COLUMN ci_started_at TEXT")
	_, _ = s.Writer.Exec("ALTER TABLE jobs ADD COLUMN ci_completed_at TEXT")
	_, _ = s.Writer.Exec("ALTER TABLE jobs ADD COLUMN ci_status_summary TEXT")

	return nil
}

func (s *Store) tableSQL(table string) (string, error) {
	var sqlText string
	if err := s.Writer.QueryRow(`SELECT COALESCE(sql,'') FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&sqlText); err != nil {
		return "", fmt.Errorf("load %s table SQL: %w", table, err)
	}
	return strings.ToLower(sqlText), nil
}

func normalizeForeignKeysPragma(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "1", "ON":
		return "ON"
	case "0", "OFF":
		return "OFF"
	default:
		return "OFF"
	}
}

func (s *Store) withForeignKeysOff(fn func() error) error {
	var fnErr error
	var original string
	if err := s.Writer.QueryRow("PRAGMA foreign_keys").Scan(&original); err != nil {
		return fmt.Errorf("read foreign_keys pragma: %w", err)
	}

	original = normalizeForeignKeysPragma(original)
	if _, err := s.Writer.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		return fmt.Errorf("disable foreign_keys pragma: %w", err)
	}

	restore := func() error {
		_, err := s.Writer.Exec("PRAGMA foreign_keys = " + original)
		if err != nil {
			return fmt.Errorf("restore foreign_keys pragma: %w", err)
		}
		return nil
	}

	defer func() {
		restoreErr := restore()
		if restoreErr == nil {
			return
		}
		if fnErr == nil {
			fnErr = restoreErr
			return
		}
		fnErr = fmt.Errorf("%w; %v", fnErr, restoreErr)
	}()

	fnErr = fn()

	if fnErr != nil {
		return fnErr
	}
	return nil
}

func (s *Store) migrateJobsForCancelledState() error {
	sqlText, err := s.tableSQL("jobs")
	if err != nil {
		return err
	}
	if strings.Contains(sqlText, "'cancelled'") {
		return nil
	}

	return s.withForeignKeysOff(func() error {
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
	})
}

func (s *Store) migrateJobsForRebasingState() error {
	sqlText, err := s.tableSQL("jobs")
	if err != nil {
		return err
	}
	if strings.Contains(sqlText, "'rebasing'") && strings.Contains(sqlText, "'resolving_conflicts'") {
		return nil
	}

	return s.withForeignKeysOff(func() error {
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
        CHECK(state IN ('queued','planning','implementing','reviewing','testing','ready','rebasing','resolving_conflicts','approved','rejected','failed','cancelled')),
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
			return fmt.Errorf("create jobs_new for rebasing migration: %w", err)
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
			return fmt.Errorf("copy jobs rows for rebasing migration: %w", err)
		}

		if _, err := tx.Exec(`DROP TABLE jobs`); err != nil {
			return fmt.Errorf("drop jobs for rebasing migration: %w", err)
		}
		if _, err := tx.Exec(`ALTER TABLE jobs_new RENAME TO jobs`); err != nil {
			return fmt.Errorf("rename jobs_new for rebasing migration: %w", err)
		}
		if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state)`); err != nil {
			return fmt.Errorf("create idx_jobs_state for rebasing migration: %w", err)
		}
		if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_jobs_issue ON jobs(autopr_issue_id)`); err != nil {
			return fmt.Errorf("create idx_jobs_issue for rebasing migration: %w", err)
		}
		if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_jobs_state_project ON jobs(state, project_name)`); err != nil {
			return fmt.Errorf("create idx_jobs_state_project for rebasing migration: %w", err)
		}
		if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_one_active_per_issue
    ON jobs(autopr_issue_id)
    WHERE state NOT IN ('approved', 'rejected', 'failed', 'cancelled')`); err != nil {
			return fmt.Errorf("create active-job index for rebasing migration: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit jobs rebasing migration: %w", err)
		}
		return nil
	})
}

func (s *Store) migrateJobsForAwaitingChecksState() error {
	sqlText, err := s.tableSQL("jobs")
	if err != nil {
		return err
	}
	if strings.Contains(sqlText, "'awaiting_checks'") {
		return nil
	}

	return s.withForeignKeysOff(func() error {
		tx, err := s.Writer.Begin()
		if err != nil {
			return fmt.Errorf("begin jobs awaiting_checks migration: %w", err)
		}
		defer tx.Rollback()

		if _, err := tx.Exec(`
CREATE TABLE jobs_new (
    id              TEXT PRIMARY KEY,
    autopr_issue_id TEXT NOT NULL REFERENCES issues(autopr_issue_id) ON DELETE RESTRICT,
    project_name     TEXT NOT NULL,
    state            TEXT NOT NULL DEFAULT 'queued'
        CHECK(state IN ('queued','planning','implementing','reviewing','testing','ready','rebasing','resolving_conflicts','awaiting_checks','approved','rejected','failed','cancelled')),
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
			return fmt.Errorf("create jobs_new for awaiting_checks migration: %w", err)
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
			return fmt.Errorf("copy jobs rows for awaiting_checks migration: %w", err)
		}

		if _, err := tx.Exec(`DROP TABLE jobs`); err != nil {
			return fmt.Errorf("drop jobs for awaiting_checks migration: %w", err)
		}
		if _, err := tx.Exec(`ALTER TABLE jobs_new RENAME TO jobs`); err != nil {
			return fmt.Errorf("rename jobs_new for awaiting_checks migration: %w", err)
		}
		if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state)`); err != nil {
			return fmt.Errorf("create idx_jobs_state for awaiting_checks migration: %w", err)
		}
		if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_jobs_issue ON jobs(autopr_issue_id)`); err != nil {
			return fmt.Errorf("create idx_jobs_issue for awaiting_checks migration: %w", err)
		}
		if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_jobs_state_project ON jobs(state, project_name)`); err != nil {
			return fmt.Errorf("create idx_jobs_state_project for awaiting_checks migration: %w", err)
		}
		if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_one_active_per_issue
    ON jobs(autopr_issue_id)
    WHERE state NOT IN ('approved', 'rejected', 'failed', 'cancelled')`); err != nil {
			return fmt.Errorf("create active-job index for awaiting_checks migration: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit jobs awaiting_checks migration: %w", err)
		}
		return nil
	})
}

func (s *Store) migrateSessionsForCancelledStatus() error {
	sqlText, err := s.tableSQL("llm_sessions")
	if err != nil {
		return err
	}
	if strings.Contains(sqlText, "'cancelled'") {
		return nil
	}

	return s.withForeignKeysOff(func() error {
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
	})
}

func (s *Store) migrateSessionsForConflictResolutionStep() error {
	sqlText, err := s.tableSQL("llm_sessions")
	if err != nil {
		return err
	}
	if strings.Contains(sqlText, "'conflict_resolution'") && !strings.Contains(sqlText, "'rebase'") {
		return nil
	}

	return s.withForeignKeysOff(func() error {
		tx, err := s.Writer.Begin()
		if err != nil {
			return fmt.Errorf("begin llm_sessions conflict migration: %w", err)
		}
		defer tx.Rollback()

		if _, err := tx.Exec(`
CREATE TABLE llm_sessions_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id        TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    step          TEXT NOT NULL CHECK(step IN ('plan','plan_review','implement','code_review','tests','conflict_resolution')),
    iteration     INTEGER NOT NULL DEFAULT 0,
    llm_provider  TEXT NOT NULL CHECK(llm_provider IN ('codex', 'claude')),
    prompt_hash   TEXT,
    response_text TEXT,
    prompt_text    TEXT,
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
			return fmt.Errorf("create llm_sessions_new for conflict migration: %w", err)
		}

		if _, err := tx.Exec(`
INSERT INTO llm_sessions_new (
    id, job_id, step, iteration, llm_provider, prompt_hash, response_text, prompt_text,
    input_tokens, output_tokens, duration_ms, jsonl_path, commit_sha, status,
    error_message, created_at, completed_at
)
SELECT
    id, job_id, CASE WHEN step = 'rebase' THEN 'conflict_resolution' ELSE step END, iteration,
    llm_provider, prompt_hash, response_text, prompt_text,
    input_tokens, output_tokens, duration_ms, jsonl_path, commit_sha, status,
    error_message, created_at, completed_at
FROM llm_sessions`); err != nil {
			return fmt.Errorf("copy llm_sessions rows for conflict migration: %w", err)
		}

		if _, err := tx.Exec(`DROP TABLE llm_sessions`); err != nil {
			return fmt.Errorf("drop llm_sessions for conflict migration: %w", err)
		}
		if _, err := tx.Exec(`ALTER TABLE llm_sessions_new RENAME TO llm_sessions`); err != nil {
			return fmt.Errorf("rename llm_sessions_new for conflict migration: %w", err)
		}
		if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_job ON llm_sessions(job_id)`); err != nil {
			return fmt.Errorf("create idx_sessions_job for conflict migration: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit llm_sessions conflict migration: %w", err)
		}
		return nil
	})
}

func (s *Store) migrateArtifactsForRebaseKind() error {
	sqlText, err := s.tableSQL("artifacts")
	if err != nil {
		return err
	}
	if strings.Contains(sqlText, "'rebase_conflict'") {
		return nil
	}

	return s.withForeignKeysOff(func() error {
		tx, err := s.Writer.Begin()
		if err != nil {
			return fmt.Errorf("begin artifacts migration: %w", err)
		}
		defer tx.Rollback()

		if _, err := tx.Exec(`
CREATE TABLE artifacts_new (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id           TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    autopr_issue_id  TEXT NOT NULL,
    kind             TEXT NOT NULL CHECK(kind IN ('plan','plan_review','code_review','test_output','rebase_conflict')),
    content          TEXT NOT NULL,
    iteration        INTEGER NOT NULL DEFAULT 0,
    commit_sha       TEXT,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
)`); err != nil {
			return fmt.Errorf("create artifacts_new for rebase conflict migration: %w", err)
		}

		if _, err := tx.Exec(`
INSERT INTO artifacts_new (
    id, job_id, autopr_issue_id, kind, content, iteration, commit_sha, created_at
)
SELECT
    id, job_id, autopr_issue_id, kind, content, iteration, commit_sha, created_at
FROM artifacts`); err != nil {
			return fmt.Errorf("copy artifacts rows for rebase conflict migration: %w", err)
		}

		if _, err := tx.Exec(`DROP TABLE artifacts`); err != nil {
			return fmt.Errorf("drop artifacts for rebase conflict migration: %w", err)
		}
		if _, err := tx.Exec(`ALTER TABLE artifacts_new RENAME TO artifacts`); err != nil {
			return fmt.Errorf("rename artifacts_new for rebase conflict migration: %w", err)
		}
		if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_artifacts_job ON artifacts(job_id)`); err != nil {
			return fmt.Errorf("create idx_artifacts_job for rebase conflict migration: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit artifacts migration: %w", err)
		}
		return nil
	})
}

func (s *Store) migrateArtifactsForRebaseResultKind() error {
	sqlText, err := s.tableSQL("artifacts")
	if err != nil {
		return err
	}
	if strings.Contains(sqlText, "'rebase_result'") {
		return nil
	}

	return s.withForeignKeysOff(func() error {
		tx, err := s.Writer.Begin()
		if err != nil {
			return fmt.Errorf("begin artifacts rebase_result migration: %w", err)
		}
		defer tx.Rollback()

		if _, err := tx.Exec(`
CREATE TABLE artifacts_new (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id           TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    autopr_issue_id  TEXT NOT NULL,
    kind             TEXT NOT NULL CHECK(kind IN ('plan','plan_review','code_review','test_output','rebase_conflict','rebase_result')),
    content          TEXT NOT NULL,
    iteration        INTEGER NOT NULL DEFAULT 0,
    commit_sha       TEXT,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
)`); err != nil {
			return fmt.Errorf("create artifacts_new for rebase_result migration: %w", err)
		}

		if _, err := tx.Exec(`
INSERT INTO artifacts_new (
    id, job_id, autopr_issue_id, kind, content, iteration, commit_sha, created_at
)
SELECT
    id, job_id, autopr_issue_id, kind, content, iteration, commit_sha, created_at
FROM artifacts`); err != nil {
			return fmt.Errorf("copy artifacts rows for rebase_result migration: %w", err)
		}

		if _, err := tx.Exec(`DROP TABLE artifacts`); err != nil {
			return fmt.Errorf("drop artifacts for rebase_result migration: %w", err)
		}
		if _, err := tx.Exec(`ALTER TABLE artifacts_new RENAME TO artifacts`); err != nil {
			return fmt.Errorf("rename artifacts_new for rebase_result migration: %w", err)
		}
		if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_artifacts_job ON artifacts(job_id)`); err != nil {
			return fmt.Errorf("create idx_artifacts_job for rebase_result migration: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit artifacts rebase_result migration: %w", err)
		}
		return nil
	})
}

// migrateNotificationEventsNeedsPR renames event_type 'awaiting_approval' → 'needs_pr'
// and recreates the table with an updated CHECK constraint.
func (s *Store) migrateNotificationEventsNeedsPR() error {
	sqlText, err := s.tableSQL("notification_events")
	if err != nil {
		return err
	}
	if !strings.Contains(sqlText, "'awaiting_approval'") {
		return nil
	}

	return s.withForeignKeysOff(func() error {
		tx, err := s.Writer.Begin()
		if err != nil {
			return fmt.Errorf("begin notification_events migration: %w", err)
		}
		defer tx.Rollback()

		if _, err := tx.Exec(`
CREATE TABLE notification_events_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id     TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL CHECK(event_type IN ('needs_pr','failed','pr_created','pr_merged')),
    status     TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','processing','sent','failed','skipped')),
    attempts   INTEGER NOT NULL DEFAULT 0 CHECK(attempts >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
)`); err != nil {
			return fmt.Errorf("create notification_events_new: %w", err)
		}

		if _, err := tx.Exec(`
INSERT INTO notification_events_new (id, job_id, event_type, status, attempts, last_error, created_at, updated_at)
SELECT id, job_id,
       CASE WHEN event_type = 'awaiting_approval' THEN 'needs_pr' ELSE event_type END,
       status, attempts, last_error, created_at, updated_at
FROM notification_events`); err != nil {
			return fmt.Errorf("copy notification_events rows: %w", err)
		}

		if _, err := tx.Exec(`DROP TABLE notification_events`); err != nil {
			return fmt.Errorf("drop notification_events: %w", err)
		}
		if _, err := tx.Exec(`ALTER TABLE notification_events_new RENAME TO notification_events`); err != nil {
			return fmt.Errorf("rename notification_events_new: %w", err)
		}
		if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_notification_events_status_created ON notification_events(status, created_at)`); err != nil {
			return fmt.Errorf("create idx_notification_events_status_created: %w", err)
		}
		if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_notification_events_job ON notification_events(job_id)`); err != nil {
			return fmt.Errorf("create idx_notification_events_job: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit notification_events migration: %w", err)
		}
		return nil
	})
}

// RecoverInFlightJobs resets any jobs stuck in active states back to queued,
// except rebasing/resolving_conflicts which return to ready to continue readiness checks.
// Called on daemon startup after a crash.
//
// reposRoot is used as a safety boundary so cleanup logic only removes metadata
// under the configured repository root.
func (s *Store) RecoverInFlightJobs(ctx context.Context, reposRoot string) (int64, error) {
	inFlightQuery := `
SELECT id, state, COALESCE(worktree_path, '')
FROM jobs
WHERE state IN ('planning', 'implementing', 'reviewing', 'testing', 'rebasing', 'resolving_conflicts')`
	rows, err := s.Reader.QueryContext(ctx, inFlightQuery)
	if err != nil {
		return 0, fmt.Errorf("recover in-flight jobs: query in-flight jobs: %w", err)
	}
	defer rows.Close()
	type inflightJob struct {
		ID       string
		State    string
		Worktree string
	}
	recoveredJobs := make([]inflightJob, 0, 32)
	for rows.Next() {
		var job inflightJob
		if err := rows.Scan(&job.ID, &job.State, &job.Worktree); err != nil {
			return 0, fmt.Errorf("recover in-flight jobs: scan in-flight job: %w", err)
		}
		recoveredJobs = append(recoveredJobs, job)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("recover in-flight jobs: in-flight rows: %w", err)
	}

	for _, job := range recoveredJobs {
		if job.State != "rebasing" && job.State != "resolving_conflicts" {
			continue
		}
		if err := git.CleanupStaleRebase(job.Worktree, reposRoot); err != nil {
			slog.Warn("failed to cleanup rebase metadata", "job", job.ID, "worktree", job.Worktree, "err", err)
		}
	}

	res, err := s.Writer.ExecContext(ctx,
		`UPDATE jobs
	SET state = CASE
		WHEN state IN ('rebasing', 'resolving_conflicts') THEN 'ready'
		ELSE 'queued'
	END,
	updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
	WHERE state IN ('planning', 'implementing', 'reviewing', 'testing', 'rebasing', 'resolving_conflicts')`)
	if err != nil {
		return 0, fmt.Errorf("recover in-flight jobs: %w", err)
	}

	return res.RowsAffected()
}
