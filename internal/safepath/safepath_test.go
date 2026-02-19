package safepath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveNoSymlinkPath(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	worktree := filepath.Join(tmp, "worktree")
	root, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("eval temp root: %v", err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	filePath := filepath.Join(worktree, "ok.txt")
	worktreeResolved, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatalf("eval worktree: %v", err)
	}
	filePathResolved := filepath.Join(worktreeResolved, "ok.txt")
	if err := os.WriteFile(filePath, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	expected := filepath.Clean(filePathResolved)

	got, err := ResolveNoSymlinkPath(root, filePathResolved)
	if err != nil {
		t.Fatalf("resolve allowed path: %v", err)
	}
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestResolveNoSymlinkPathRejectsTraversal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("eval root: %v", err)
	}
	_, err = ResolveNoSymlinkPath(root, filepath.Join("..", "etc", "passwd"))
	if err == nil {
		t.Fatalf("expected traversal path rejection")
	}
}

func TestResolveNoSymlinkPathAllowsNonExistentLeaf(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("eval root: %v", err)
	}
	missing := filepath.Join(root, "missing.txt")

	got, err := ResolveNoSymlinkPath(root, missing)
	if err != nil {
		t.Fatalf("resolve missing file path: %v", err)
	}
	if got != filepath.Clean(missing) {
		t.Fatalf("expected resolved path %q, got %q", filepath.Clean(missing), got)
	}
}
