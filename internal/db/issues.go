package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Issue struct {
	AutoPRIssueID  string
	ProjectName    string
	Source         string
	SourceIssueID  string
	Title          string
	Body           string
	URL            string
	State          string
	LabelsJSON     string
	SourceMetaJSON string
	Eligible       bool
	SkipReason     string
	EvaluatedAt    string
	SourceUpdated  string
	SyncedAt       string
}

type IssueUpsert struct {
	ProjectName   string
	Source        string
	SourceIssueID string
	Title         string
	Body          string
	URL           string
	State         string
	Labels        []string
	SourceMeta    map[string]any
	Eligible      *bool
	SkipReason    string
	EvaluatedAt   string
	SourceUpdated string
}

func (s *Store) UpsertIssue(ctx context.Context, in IssueUpsert) (string, error) {
	newID, err := newAutoPRIssueID()
	if err != nil {
		return "", err
	}
	now := nowRFC3339()
	if in.SourceUpdated == "" {
		in.SourceUpdated = now
	}
	labelsJSON := "[]"
	if len(in.Labels) > 0 {
		b, _ := json.Marshal(in.Labels)
		labelsJSON = string(b)
	}
	metaJSON := "{}"
	if len(in.SourceMeta) > 0 {
		b, _ := json.Marshal(in.SourceMeta)
		metaJSON = string(b)
	}
	eligible := true
	if in.Eligible != nil {
		eligible = *in.Eligible
	}
	evaluatedAt := in.EvaluatedAt
	if evaluatedAt == "" {
		evaluatedAt = now
	}
	skipReason := in.SkipReason
	if eligible {
		skipReason = ""
	}
	const q = `
INSERT INTO issues(
  autopr_issue_id, project_name, source, source_issue_id, title, body, url, state,
  labels_json, source_meta_json, eligible, skip_reason, evaluated_at, source_updated_at, synced_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(project_name, source, source_issue_id) DO UPDATE SET
  title=excluded.title,
  body=excluded.body,
  url=excluded.url,
  state=excluded.state,
  labels_json=excluded.labels_json,
  source_meta_json=excluded.source_meta_json,
  eligible=excluded.eligible,
  skip_reason=excluded.skip_reason,
  evaluated_at=excluded.evaluated_at,
  source_updated_at=excluded.source_updated_at,
  synced_at=excluded.synced_at
RETURNING autopr_issue_id`
	var actualID string
	err = s.Writer.QueryRowContext(ctx, q,
		newID, in.ProjectName, in.Source, in.SourceIssueID, in.Title, in.Body, in.URL, in.State,
		labelsJSON, metaJSON, boolToInt(eligible), skipReason, evaluatedAt, in.SourceUpdated, now,
	).Scan(&actualID)
	if err != nil {
		return "", fmt.Errorf("upsert issue %s/%s/%s: %w", in.ProjectName, in.Source, in.SourceIssueID, err)
	}
	return actualID, nil
}

func (s *Store) GetIssueByAPID(ctx context.Context, autoprID string) (Issue, error) {
	const q = `
SELECT autopr_issue_id, project_name, source, source_issue_id, title, body, url, state,
       labels_json, source_meta_json, eligible, skip_reason, evaluated_at, source_updated_at, synced_at
FROM issues WHERE autopr_issue_id = ?`
	var it Issue
	var eligible int
	err := s.Reader.QueryRowContext(ctx, q, autoprID).Scan(
		&it.AutoPRIssueID, &it.ProjectName, &it.Source, &it.SourceIssueID,
		&it.Title, &it.Body, &it.URL, &it.State,
		&it.LabelsJSON, &it.SourceMetaJSON, &eligible, &it.SkipReason, &it.EvaluatedAt, &it.SourceUpdated, &it.SyncedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return Issue{}, fmt.Errorf("issue %s not found", autoprID)
		}
		return Issue{}, fmt.Errorf("get issue %s: %w", autoprID, err)
	}
	it.Eligible = eligible == 1
	return it, nil
}

// Cursor operations.

func (s *Store) GetCursor(ctx context.Context, project, source string) (string, error) {
	const q = `SELECT cursor_value FROM sync_cursors WHERE project_name = ? AND source = ?`
	var v sql.NullString
	err := s.Reader.QueryRowContext(ctx, q, project, source).Scan(&v)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("get cursor %s/%s: %w", project, source, err)
	}
	return v.String, nil
}

func (s *Store) SetCursor(ctx context.Context, project, source, cursor string) error {
	const q = `
INSERT INTO sync_cursors(project_name, source, cursor_value, last_synced_at)
VALUES(?,?,?,?)
ON CONFLICT(project_name, source) DO UPDATE SET
  cursor_value=excluded.cursor_value,
  last_synced_at=excluded.last_synced_at`
	_, err := s.Writer.ExecContext(ctx, q, project, source, cursor, nowRFC3339())
	if err != nil {
		return fmt.Errorf("set cursor %s/%s: %w", project, source, err)
	}
	return nil
}

// Helpers.

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func newAutoPRIssueID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate autopr_issue_id: %w", err)
	}
	return "ap-" + strings.ToLower(hex.EncodeToString(buf)), nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
