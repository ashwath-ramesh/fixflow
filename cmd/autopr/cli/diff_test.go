package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"autopr/internal/db"

	"github.com/spf13/cobra"
)

func TestRunDiffFilesOutputsOnePathPerLine(t *testing.T) {
	tmp := t.TempDir()
	configPath := writeMergeConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	jobID := createMergeJobForTest(t, dbPath, "project", "8101", "implementing", "", "")

	worktreePath := setupDiffWorktree(t, tmp)
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	if err := store.UpdateJobField(context.Background(), jobID, "worktree_path", worktreePath); err != nil {
		t.Fatalf("set worktree path: %v", err)
	}

	if err := os.WriteFile(filepath.Join(worktreePath, "README.md"), []byte("updated\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "changed.go"), []byte("package changed\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	prevCfgPath := cfgPath
	prevDiffFiles := diffFiles
	prevDiffStat := diffStat
	prevJSON := jsonOut
	cfgPath = configPath
	diffFiles = true
	diffStat = false
	jsonOut = false
	defer func() {
		cfgPath = prevCfgPath
		diffFiles = prevDiffFiles
		diffStat = prevDiffStat
		jsonOut = prevJSON
	}()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	out := captureStdout(t, func() error {
		return runDiff(cmd, []string{jobID})
	})

	got := strings.Split(strings.TrimSpace(out), "\n")
	sort.Strings(got)
	expected := []string{"README.md", "changed.go"}
	sort.Strings(expected)
	if len(got) != len(expected) {
		t.Fatalf("unexpected file count: got=%d want=%d output=%q", len(got), len(expected), out)
	}
	for i, file := range expected {
		if got[i] != file {
			t.Fatalf("file %d: expected %q, got %q", i, file, got[i])
		}
	}
}

func TestRunDiffFilesMutuallyExclusiveWithStat(t *testing.T) {
	tmp := t.TempDir()
	configPath := writeMergeConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	jobID := createMergeJobForTest(t, dbPath, "project", "8102", "implementing", "", "")

	worktreePath := setupDiffWorktree(t, tmp)
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	if err := store.UpdateJobField(context.Background(), jobID, "worktree_path", worktreePath); err != nil {
		t.Fatalf("set worktree path: %v", err)
	}

	prevCfgPath := cfgPath
	prevDiffFiles := diffFiles
	prevDiffStat := diffStat
	prevJSON := jsonOut
	cfgPath = configPath
	diffFiles = true
	diffStat = true
	jsonOut = false
	defer func() {
		cfgPath = prevCfgPath
		diffFiles = prevDiffFiles
		diffStat = prevDiffStat
		jsonOut = prevJSON
	}()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err = runDiff(cmd, []string{jobID})
	if err == nil {
		t.Fatalf("expected --files and --stat conflict error")
	}
	if !strings.Contains(err.Error(), "--files cannot be combined with --stat") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunDiffFilesJSONOutput(t *testing.T) {
	tmp := t.TempDir()
	configPath := writeMergeConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	jobID := createMergeJobForTest(t, dbPath, "project", "8103", "implementing", "", "")

	worktreePath := setupDiffWorktree(t, tmp)
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	if err := store.UpdateJobField(context.Background(), jobID, "worktree_path", worktreePath); err != nil {
		t.Fatalf("set worktree path: %v", err)
	}

	if err := os.WriteFile(filepath.Join(worktreePath, "README.md"), []byte("updated\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}

	prevCfgPath := cfgPath
	prevDiffFiles := diffFiles
	prevDiffStat := diffStat
	prevJSON := jsonOut
	cfgPath = configPath
	diffFiles = true
	diffStat = false
	jsonOut = true
	defer func() {
		cfgPath = prevCfgPath
		diffFiles = prevDiffFiles
		diffStat = prevDiffStat
		jsonOut = prevJSON
	}()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	out := captureStdout(t, func() error {
		return runDiff(cmd, []string{jobID})
	})

	var decoded struct {
		JobID string   `json:"job_id"`
		Files []string `json:"files"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &decoded); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if decoded.JobID != jobID {
		t.Fatalf("expected job_id %q, got %q", jobID, decoded.JobID)
	}
	if len(decoded.Files) != 1 || decoded.Files[0] != "README.md" {
		t.Fatalf("unexpected files: %#v", decoded.Files)
	}
}

func TestRunDiffFilesNoChanges(t *testing.T) {
	tmp := t.TempDir()
	configPath := writeMergeConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	jobID := createMergeJobForTest(t, dbPath, "project", "8104", "implementing", "", "")

	worktreePath := setupDiffWorktree(t, tmp)
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	if err := store.UpdateJobField(context.Background(), jobID, "worktree_path", worktreePath); err != nil {
		t.Fatalf("set worktree path: %v", err)
	}

	prevCfgPath := cfgPath
	prevDiffFiles := diffFiles
	prevDiffStat := diffStat
	prevJSON := jsonOut
	cfgPath = configPath
	diffFiles = true
	diffStat = false
	jsonOut = false
	defer func() {
		cfgPath = prevCfgPath
		diffFiles = prevDiffFiles
		diffStat = prevDiffStat
		jsonOut = prevJSON
	}()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	out := captureStdout(t, func() error {
		return runDiff(cmd, []string{jobID})
	})

	if strings.TrimSpace(out) != "(no changes)" {
		t.Fatalf("unexpected output: %q", out)
	}
}

func setupDiffWorktree(t *testing.T, tmp string) string {
	t.Helper()
	seedDir := filepath.Join(tmp, "seed")
	remoteDir := filepath.Join(tmp, "remote.git")
	worktreePath := filepath.Join(tmp, "worktree")

	runGitCmd(t, "", "init", "--bare", remoteDir)
	runGitCmd(t, "", "init", seedDir)
	runGitCmd(t, seedDir, "config", "user.email", "test@example.com")
	runGitCmd(t, seedDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	runGitCmd(t, seedDir, "add", "README.md")
	runGitCmd(t, seedDir, "commit", "-m", "init")
	runGitCmd(t, seedDir, "branch", "-M", "main")
	runGitCmd(t, seedDir, "remote", "add", "origin", remoteDir)
	runGitCmd(t, seedDir, "push", "-u", "origin", "main")
	runGitCmd(t, "", "clone", remoteDir, worktreePath)

	return worktreePath
}

func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}
