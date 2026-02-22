package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autopr/internal/db"

	"github.com/spf13/cobra"
)

func TestMergeApprovedJobSetsMergedAt(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	dbPath := filepath.Join(tmp, "autopr.db")
	mergeCfgPath := writeMergeConfig(t, tmp)

	jobID := createMergeJobForTest(t, dbPath, "project", "7001", "approved", "https://github.com/acmecorp/placeholder/pull/123", "")

	prevCfgPath := cfgPath
	prevGitHub := mergeGitHub
	prevNow := now
	prevCleanup := mergeCleanup
	prevMergeMethod := mergeMethod
	prevJSON := jsonOut
	defer func() {
		cfgPath = prevCfgPath
		mergeGitHub = prevGitHub
		now = prevNow
		mergeCleanup = prevCleanup
		mergeMethod = prevMergeMethod
		jsonOut = prevJSON
	}()

	mergedAt := "2026-02-20T10:00:00Z"
	mergeCalled := false
	mergeGitHub = func(context.Context, string, string, string) error {
		mergeCalled = true
		return nil
	}
	now = func() string { return mergedAt }
	mergeCleanup = func(context.Context, *db.Store, string, db.Job, string) error { return nil }
	jsonOut = false
	cfgPath = mergeCfgPath
	mergeMethod = "merge"

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := runMerge(cmd, []string{jobID}); err != nil {
		t.Fatalf("runMerge: %v", err)
	}
	if !mergeCalled {
		t.Fatalf("expected merge helper call")
	}

	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	got, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.PRMergedAt != mergedAt {
		t.Fatalf("want pr_merged_at %q, got %q", mergedAt, got.PRMergedAt)
	}
}

func TestMergeJobMustBeApproved(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	dbPath := filepath.Join(tmp, "autopr.db")
	mergeCfgPath := writeMergeConfig(t, tmp)
	jobID := createMergeJobForTest(t, dbPath, "project", "7002", "ready", "https://github.com/acmecorp/placeholder/pull/124", "")

	prevCfgPath := cfgPath
	prevGitHub := mergeGitHub
	prevMergeMethod := mergeMethod
	defer func() {
		cfgPath = prevCfgPath
		mergeGitHub = prevGitHub
		mergeMethod = prevMergeMethod
	}()
	mergeGitHub = func(context.Context, string, string, string) error {
		t.Fatalf("merge helper should not be called")
		return nil
	}
	mergeMethod = "merge"
	cfgPath = mergeCfgPath

	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	err := runMerge(cmd, []string{jobID})
	if err == nil {
		t.Fatalf("expected error for non-approved job")
	}
	if !strings.Contains(err.Error(), `must be 'approved'`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMergeRequiresPRURL(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	dbPath := filepath.Join(tmp, "autopr.db")
	mergeCfgPath := writeMergeConfig(t, tmp)
	jobID := createMergeJobForTest(t, dbPath, "project", "7003", "approved", "", "")

	prevCfgPath := cfgPath
	prevGitHub := mergeGitHub
	prevMergeMethod := mergeMethod
	defer func() {
		cfgPath = prevCfgPath
		mergeGitHub = prevGitHub
		mergeMethod = prevMergeMethod
	}()
	mergeGitHub = func(context.Context, string, string, string) error {
		t.Fatalf("merge helper should not be called")
		return nil
	}
	mergeMethod = "merge"
	cfgPath = mergeCfgPath

	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	err := runMerge(cmd, []string{jobID})
	if err == nil {
		t.Fatalf("expected error for missing PR URL")
	}
	if !strings.Contains(err.Error(), "has no PR URL") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMergeRejectsAlreadyMerged(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	dbPath := filepath.Join(tmp, "autopr.db")
	mergeCfgPath := writeMergeConfig(t, tmp)
	jobID := createMergeJobForTest(t, dbPath, "project", "7004", "approved", "https://github.com/acmecorp/placeholder/pull/125", "2026-01-01T00:00:00Z")

	prevCfgPath := cfgPath
	prevGitHub := mergeGitHub
	prevMergeMethod := mergeMethod
	defer func() {
		cfgPath = prevCfgPath
		mergeGitHub = prevGitHub
		mergeMethod = prevMergeMethod
	}()
	mergeGitHub = func(context.Context, string, string, string) error {
		t.Fatalf("merge helper should not be called")
		return nil
	}
	mergeMethod = "merge"
	cfgPath = mergeCfgPath

	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	err := runMerge(cmd, []string{jobID})
	if err == nil {
		t.Fatalf("expected error for already merged job")
	}
	if !strings.Contains(err.Error(), "already merged") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMergeRejectsInvalidMethod(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "autopr.db")
	mergeCfgPath := writeMergeConfig(t, tmp)
	jobID := createMergeJobForTest(t, dbPath, "project", "7005", "approved", "https://github.com/acmecorp/placeholder/pull/126", "")

	prevCfgPath := cfgPath
	prevGitHub := mergeGitHub
	prevMethod := mergeMethod
	defer func() {
		cfgPath = prevCfgPath
		mergeGitHub = prevGitHub
		mergeMethod = prevMethod
	}()
	mergeGitHub = func(context.Context, string, string, string) error {
		t.Fatalf("merge helper should not be called for invalid method")
		return nil
	}
	mergeMethod = "bad"
	cfgPath = mergeCfgPath

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := runMerge(cmd, []string{jobID})
	if err == nil {
		t.Fatalf("expected invalid method error")
	}
	if !strings.Contains(err.Error(), "invalid merge method") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMergeJSONOutput(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "autopr.db")
	mergeCfgPath := writeMergeConfig(t, tmp)
	jobID := createMergeJobForTest(t, dbPath, "project", "7006", "approved", "https://github.com/acmecorp/placeholder/pull/127", "")

	prevCfgPath := cfgPath
	prevGitHub := mergeGitHub
	prevNow := now
	prevMergeMethod := mergeMethod
	prevJSON := jsonOut
	prevCleanup := mergeCleanup
	defer func() {
		cfgPath = prevCfgPath
		mergeGitHub = prevGitHub
		now = prevNow
		mergeMethod = prevMergeMethod
		jsonOut = prevJSON
		mergeCleanup = prevCleanup
	}()

	cfgPath = mergeCfgPath
	now = func() string { return "2026-02-20T11:00:00Z" }
	mergeGitHub = func(context.Context, string, string, string) error { return nil }
	mergeCleanup = func(context.Context, *db.Store, string, db.Job, string) error { return nil }
	mergeMethod = "merge"
	jsonOut = true

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	out := captureStdout(t, func() error {
		return runMerge(cmd, []string{jobID})
	})

	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &decoded); err != nil {
		t.Fatalf("parse json output: %v", err)
	}
	if got, ok := decoded["job_id"]; !ok || got != jobID {
		t.Fatalf("expected job_id=%q in output, got %#v", jobID, decoded["job_id"])
	}
	if got, ok := decoded["state"]; !ok || got != "merged" {
		t.Fatalf("expected state=merged in output, got %#v", decoded["state"])
	}
	if _, ok := decoded["merged_at"]; !ok {
		t.Fatalf("expected merged_at in output: %#v", decoded)
	}
}

func TestMergeCommandRegistered(t *testing.T) {
	var found bool
	for _, c := range rootCmd.Commands() {
		if c.Name() == "merge" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("merge command not registered on root")
	}
}

func createMergeJobForTest(t *testing.T, dbPath, project, issueIDSuffix, state, prURL, prMergedAt string) string {
	t.Helper()
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
		ProjectName:   project,
		Source:        "github",
		SourceIssueID: issueIDSuffix,
		Title:         fmt.Sprintf("merge test %s", issueIDSuffix),
		URL:           fmt.Sprintf("https://github.com/acmecorp/placeholder/issues/%s", issueIDSuffix),
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, project, 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.Writer.ExecContext(ctx, `
UPDATE jobs
SET state = ?, pr_url = ?, pr_merged_at = ?
WHERE id = ?`, state, prURL, prMergedAt, jobID); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	return jobID
}

func writeMergeConfig(t *testing.T, dir string) string {
	t.Helper()
	cfgPath := filepath.Join(dir, "autopr.toml")
	dbPath := filepath.Join(dir, "autopr.db")
	reposRoot := filepath.Join(dir, "repos")
	cfg := fmt.Sprintf(`db_path = %q
repos_root = %q

[[projects]]
name = "project"
repo_url = "https://github.com/acmecorp/placeholder"
test_cmd = "echo ok"

[projects.github]
owner = "acmecorp"
repo = "placeholder"

[tokens]
github = "gh_token"
`, dbPath, reposRoot)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}
