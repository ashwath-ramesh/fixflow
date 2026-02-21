package git

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestDiffFilesAgainstBaseIncludesTrackedAndUntracked(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	remote := createRemoteWithMainBranch(t, tmp)
	worktree := filepath.Join(tmp, "worktree")
	if err := CloneForJob(ctx, remote, "", worktree, "autopr/job-1", "main"); err != nil {
		t.Fatalf("clone for job: %v", err)
	}

	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("hello changed\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "new.txt"), []byte("new file\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	filesText, err := DiffFilesAgainstBase(ctx, worktree, "main")
	if err != nil {
		t.Fatalf("diff files against base: %v", err)
	}

	files := strings.Split(strings.TrimSpace(filesText), "\n")
	sort.Strings(files)
	got := make(map[string]struct{}, len(files))
	for _, file := range files {
		if file != "" {
			got[file] = struct{}{}
		}
	}

	expected := map[string]struct{}{
		"README.md": {},
		"new.txt":   {},
	}
	if len(got) != len(expected) {
		t.Fatalf("unexpected file count: got=%d want=%d from %q", len(got), len(expected), filesText)
	}
	for file := range expected {
		if _, ok := got[file]; !ok {
			t.Fatalf("missing expected file %q in %q", file, filesText)
		}
	}
}

func TestDiffFilesAgainstBaseNoChanges(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	remote := createRemoteWithMainBranch(t, tmp)
	worktree := filepath.Join(tmp, "worktree")
	if err := CloneForJob(ctx, remote, "", worktree, "autopr/job-2", "main"); err != nil {
		t.Fatalf("clone for job: %v", err)
	}

	filesText, err := DiffFilesAgainstBase(ctx, worktree, "main")
	if err != nil {
		t.Fatalf("diff files against base: %v", err)
	}
	if strings.TrimSpace(filesText) != "" {
		t.Fatalf("expected no file changes, got %q", filesText)
	}
}
