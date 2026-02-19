package pipeline

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRebaseConflictFilePath(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	safeFile := filepath.Join(workDir, "safe.txt")
	if err := os.WriteFile(safeFile, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write safe file: %v", err)
	}
	expected, err := filepath.EvalSymlinks(safeFile)
	if err != nil {
		t.Fatalf("eval safe file symlink: %v", err)
	}

	got, err := rebaseConflictFilePath(workDir, "safe.txt")
	if err != nil {
		t.Fatalf("expected safe file accepted: %v", err)
	}
	if got != expected {
		t.Fatalf("expected resolved path %q, got %q", expected, got)
	}
}

func TestRebaseConflictFilePathRejectsEmptyPath(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	if _, err := rebaseConflictFilePath(workDir, ""); err == nil {
		t.Fatal("expected empty path rejection")
	}
}

func TestRebaseConflictFilePathRejectsAbsolutePath(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	absPath := filepath.Join(workDir, "outside.txt")
	if _, err := rebaseConflictFilePath(workDir, absPath); err == nil {
		t.Fatal("expected absolute path rejection")
	}
}

func TestRebaseConflictFilePathRejectsTraversal(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	_, err := rebaseConflictFilePath(workDir, "../outside.txt")
	if err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}

func TestRebaseConflictFilePathRejectsSymlinkTraversal(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("no"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workDir, "outside")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	if _, err := rebaseConflictFilePath(workDir, filepath.Join("outside", "secret.txt")); err == nil {
		t.Fatal("expected symlinked path to be rejected")
	}
}

func TestReadRebaseConflictFileRejectsLargeFile(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	bigFile := filepath.Join(workDir, "huge.txt")
	blob := bytes.Repeat([]byte("a"), maxRebaseConflictFileBytes+1)
	if err := os.WriteFile(bigFile, blob, 0o600); err != nil {
		t.Fatalf("write huge file: %v", err)
	}
	if _, err := readRebaseConflictFile(bigFile); err == nil {
		t.Fatal("expected large file rejection")
	}
}

func TestReadRebaseConflictFileRejectsNonRegular(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	nonRegular := filepath.Join(workDir, "dir")
	if err := os.Mkdir(nonRegular, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := readRebaseConflictFile(nonRegular); err == nil {
		t.Fatal("expected directory rejection")
	}
}
