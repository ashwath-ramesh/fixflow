package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autopr/internal/db"

	"github.com/spf13/cobra"
)

func TestRunOpenEditorTargetUsesResolvedWorktree(t *testing.T) {
	tmp := t.TempDir()
	configPath := writeMergeConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	jobID := createMergeJobForTest(t, dbPath, "project", "8601", "implementing", "", "")

	repoWorktree := filepath.Join(tmp, "repos", "worktrees", jobID)
	if err := os.MkdirAll(repoWorktree, 0o755); err != nil {
		t.Fatalf("create fallback worktree: %v", err)
	}

	restoreGlobals := setOpenCommandState(configPath, false, false, false, false)
	prevRunEditor := runOpenInEditor
	var openedPath string
	runOpenInEditor = func(path string) error {
		openedPath = path
		return nil
	}
	defer func() {
		restoreGlobals()
		runOpenInEditor = prevRunEditor
	}()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := runOpen(cmd, []string{db.ShortID(jobID)}); err != nil {
		t.Fatalf("runOpen: %v", err)
	}
	if openedPath != repoWorktree {
		t.Fatalf("expected opened path %q, got %q", repoWorktree, openedPath)
	}
}

func TestRunOpenIssueTargetLaunchesIssueURL(t *testing.T) {
	tmp := t.TempDir()
	configPath := writeMergeConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	jobID := createMergeJobForTest(t, dbPath, "project", "8602", "implementing", "", "")
	issueURL := "https://example.com/issues/8602"

	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	job, err := store.GetJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if _, err := store.Writer.ExecContext(context.Background(),
		`UPDATE issues SET url = ? WHERE autopr_issue_id = ?`, issueURL, job.AutoPRIssueID); err != nil {
		t.Fatalf("set issue URL: %v", err)
	}

	restoreGlobals := setOpenCommandState(configPath, false, false, true, false)
	prevRunBrowser := runOpenInBrowser
	var openedURL string
	runOpenInBrowser = func(url string) error {
		openedURL = url
		return nil
	}
	defer func() {
		restoreGlobals()
		runOpenInBrowser = prevRunBrowser
	}()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := runOpen(cmd, []string{jobID}); err != nil {
		t.Fatalf("runOpen: %v", err)
	}
	if openedURL != issueURL {
		t.Fatalf("expected opened URL %q, got %q", issueURL, openedURL)
	}
}

func TestRunOpenPRTargetLaunchesPRURL(t *testing.T) {
	tmp := t.TempDir()
	configPath := writeMergeConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	prURL := "https://github.com/acmecorp/placeholder/pull/8603"
	jobID := createMergeJobForTest(t, dbPath, "project", "8603", "implementing", prURL, "")

	restoreGlobals := setOpenCommandState(configPath, false, false, false, true)
	prevRunBrowser := runOpenInBrowser
	var openedURL string
	runOpenInBrowser = func(url string) error {
		openedURL = url
		return nil
	}
	defer func() {
		restoreGlobals()
		runOpenInBrowser = prevRunBrowser
	}()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := runOpen(cmd, []string{jobID}); err != nil {
		t.Fatalf("runOpen: %v", err)
	}
	if openedURL != prURL {
		t.Fatalf("expected opened URL %q, got %q", prURL, openedURL)
	}
}

func TestRunOpenJSONPayloadIncludesActionAndTarget(t *testing.T) {
	tmp := t.TempDir()
	configPath := writeMergeConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	jobID := createMergeJobForTest(t, dbPath, "project", "8604", "implementing", "", "")

	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	worktreePath := filepath.Join(tmp, "worktree")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if err := store.UpdateJobField(context.Background(), jobID, "worktree_path", worktreePath); err != nil {
		t.Fatalf("set worktree path: %v", err)
	}

	restoreGlobals := setOpenCommandState(configPath, true, false, false, false)
	prevRunEditor := runOpenInEditor
	var openedPath string
	runOpenInEditor = func(path string) error {
		openedPath = path
		return nil
	}
	defer func() {
		restoreGlobals()
		runOpenInEditor = prevRunEditor
	}()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	out := captureStdout(t, func() error {
		return runOpen(cmd, []string{db.ShortID(jobID)})
	})

	if openedPath != worktreePath {
		t.Fatalf("expected opened path %q, got %q", worktreePath, openedPath)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if got["action"] != "open" {
		t.Fatalf("unexpected action: %#v", got["action"])
	}
	if got["job_id"] != jobID {
		t.Fatalf("unexpected job_id: %#v", got["job_id"])
	}
	if got["target"] != "editor" {
		t.Fatalf("unexpected target: %#v", got["target"])
	}
	if got["path"] != worktreePath {
		t.Fatalf("unexpected path: %#v", got["path"])
	}
}

func TestRunOpenErrorWhenNoEditorWorktree(t *testing.T) {
	tmp := t.TempDir()
	configPath := writeMergeConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	jobID := createMergeJobForTest(t, dbPath, "project", "8605", "implementing", "", "")

	restoreGlobals := setOpenCommandState(configPath, false, false, false, false)
	defer func() {
		restoreGlobals()
	}()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := runOpen(cmd, []string{jobID})
	if err == nil {
		t.Fatalf("expected missing-worktree error")
	}
	if !strings.Contains(err.Error(), "has no worktree") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunOpenIssueTargetErrorWhenURLMissing(t *testing.T) {
	tmp := t.TempDir()
	configPath := writeMergeConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	jobID := createMergeJobForTest(t, dbPath, "project", "8606", "implementing", "", "")

	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	if _, err := store.Writer.ExecContext(context.Background(),
		`UPDATE issues SET url = '' WHERE autopr_issue_id = (SELECT autopr_issue_id FROM jobs WHERE id = ?)`, jobID); err != nil {
		t.Fatalf("clear issue URL: %v", err)
	}

	restoreGlobals := setOpenCommandState(configPath, false, false, true, false)
	defer func() {
		restoreGlobals()
	}()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err = runOpen(cmd, []string{jobID})
	if err == nil {
		t.Fatalf("expected missing issue URL error")
	}
	if !strings.Contains(err.Error(), "has no issue URL") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunOpenPRTargetErrorWhenURLMissing(t *testing.T) {
	tmp := t.TempDir()
	configPath := writeMergeConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	jobID := createMergeJobForTest(t, dbPath, "project", "8607", "implementing", "", "")

	restoreGlobals := setOpenCommandState(configPath, false, false, false, true)
	defer func() {
		restoreGlobals()
	}()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := runOpen(cmd, []string{jobID})
	if err == nil {
		t.Fatalf("expected missing PR URL error")
	}
	if !strings.Contains(err.Error(), "has no PR URL") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunOpenTargetFlagsAreMutuallyExclusive(t *testing.T) {
	tmp := t.TempDir()
	configPath := writeMergeConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	jobID := createMergeJobForTest(t, dbPath, "project", "8608", "implementing", "https://github.com/acmecorp/placeholder/pull/8608", "")

	restoreGlobals := setOpenCommandState(configPath, false, true, true, false)
	defer func() {
		restoreGlobals()
	}()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := runOpen(cmd, []string{jobID})
	if err == nil {
		t.Fatalf("expected flag conflict error")
	}
	if !strings.Contains(err.Error(), "at most one of --editor, --issue, --pr may be specified") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func setOpenCommandState(configPath string, asJSON, editorTarget, issueTarget, prTarget bool) func() {
	prevCfgPath := cfgPath
	prevJSON := jsonOut
	prevEditor := openTargetEditor
	prevIssue := openTargetIssue
	prevPR := openTargetPR

	cfgPath = configPath
	jsonOut = asJSON
	openTargetEditor = editorTarget
	openTargetIssue = issueTarget
	openTargetPR = prTarget

	return func() {
		cfgPath = prevCfgPath
		jsonOut = prevJSON
		openTargetEditor = prevEditor
		openTargetIssue = prevIssue
		openTargetPR = prevPR
	}
}
