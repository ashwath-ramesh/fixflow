package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"autopr/internal/safepath"
)

// FetchBranch fetches the latest commits for the given base branch.
func FetchBranch(ctx context.Context, dir, baseBranch string) error {
	return runGit(ctx, dir, "fetch", "origin", "--", baseBranch)
}

// ConfigureDiff3 enables diff3 conflict markers for clearer rebase conflict output.
func ConfigureDiff3(ctx context.Context, dir string) error {
	return runGit(ctx, dir, "config", "merge.conflictStyle", "diff3")
}

// RebaseOntoBase rebases the current branch onto origin/<baseBranch>.
// Returns true when conflicts are detected.
func RebaseOntoBase(ctx context.Context, dir, baseBranch string) (bool, error) {
	stdout, stderr, err := runGitOutputAndErr(ctx, dir, "rebase", "origin/"+baseBranch)
	if err == nil {
		return false, nil
	}
	if isGitRebaseError(stderr) || isGitRebaseError(stdout) {
		return true, nil
	}
	if stdout == "" && stderr == "" {
		return false, fmt.Errorf("git rebase origin/%s: %w", baseBranch, err)
	}
	return false, fmt.Errorf("git rebase origin/%s: %w: %s %s", baseBranch, err, strings.TrimSpace(stdout), strings.TrimSpace(stderr))
}

// RebaseContinue resumes a rebase after conflict resolution.
// Returns true when more conflicts remain.
func RebaseContinue(ctx context.Context, dir string) (bool, error) {
	stdout, stderr, err := runGitOutputAndErrWithNoEditor(ctx, dir, "rebase", "--continue")
	if err == nil {
		return false, nil
	}
	if isGitRebaseError(stderr) || isGitRebaseError(stdout) {
		return true, nil
	}
	return false, fmt.Errorf("git rebase --continue: %w: %s %s", err, strings.TrimSpace(stdout), strings.TrimSpace(stderr))
}

// RebaseAbort aborts the current rebase.
func RebaseAbort(ctx context.Context, dir string) error {
	if !IsRebaseInProgress(dir) {
		return nil
	}
	return runGit(ctx, dir, "rebase", "--abort")
}

// CleanupStaleRebase removes rebase metadata directories for a worktree when
// recovering from interrupted rebase operations.
func CleanupStaleRebase(worktreePath, reposRoot string) error {
	worktreePath, err := normalizeRebaseWorktreePath(worktreePath, reposRoot)
	if err != nil {
		return err
	}
	if worktreePath == "" {
		return nil
	}

	for _, dir := range []string{
		filepath.Join(worktreePath, ".git", "rebase-merge"),
		filepath.Join(worktreePath, ".git", "rebase-apply"),
	} {
		if err := removeRebaseMetadataDir(dir); err != nil {
			return err
		}
	}
	return nil
}

func normalizeRebaseWorktreePath(worktreePath, reposRoot string) (string, error) {
	worktreePath = filepath.Clean(worktreePath)
	if worktreePath == "" || worktreePath == "." {
		return "", nil
	}
	absWorktree, err := filepath.Abs(worktreePath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absWorktree)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("worktree path is not a directory: %s", worktreePath)
	}
	reposRoot, err = filepath.EvalSymlinks(reposRoot)
	if err != nil {
		return "", err
	}
	worktreePath, err = filepath.EvalSymlinks(absWorktree)
	if err != nil {
		return "", err
	}
	normalizedWorktree, err := safepath.ResolveNoSymlinkPath(reposRoot, worktreePath)
	if err != nil {
		return "", err
	}
	return normalizedWorktree, nil
}

func removeRebaseMetadataDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to remove symlinked rebase metadata path: %s", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("rebase metadata path is not a directory: %s", path)
	}
	return os.RemoveAll(path)
}

// IsRebaseInProgress reports whether a rebase is currently running.
func IsRebaseInProgress(dir string) bool {
	paths := []string{filepath.Join(dir, ".git", "rebase-merge"), filepath.Join(dir, ".git", "rebase-apply")}
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

// ConflictedFiles returns files with unresolved conflicts.
func ConflictedFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := runGitOutput(ctx, dir, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, fmt.Errorf("find conflicted files: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		return []string{}, nil
	}
	return lines, nil
}

// StageFiles stages multiple files in a single git add call.
func StageFiles(ctx context.Context, dir string, files ...string) error {
	if len(files) == 0 {
		return nil
	}
	args := make([]string, 0, len(files)+2)
	args = append(args, "add", "--")
	args = append(args, files...)
	return runGit(ctx, dir, args...)
}

func isGitRebaseError(msg string) bool {
	return strings.Contains(msg, "CONFLICT") ||
		strings.Contains(msg, "fix conflicts and then run") ||
		strings.Contains(msg, "Could not apply") ||
		strings.Contains(msg, "need to resolve your current index first")
}
