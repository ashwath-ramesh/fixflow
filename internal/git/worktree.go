package git

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// CloneForJob clones the remote repo into destPath and creates a job branch
// from the base branch. Uses a regular clone (not a worktree) because LLM
// tools (e.g. codex) may run `git init` in the working directory, which
// destroys worktree .git link files but is a no-op on a .git directory.
func CloneForJob(ctx context.Context, repoURL, token, destPath, branchName, baseBranch string) error {
	destPath, err := prepareCloneDestination(destPath)
	if err != nil {
		return fmt.Errorf("prepare clone destination: %w", err)
	}

	authURL, auth, err := prepareGitRemoteAuth(repoURL, token)
	if err != nil {
		return err
	}
	defer closeGitAuth(auth)

	// Clone from the remote. For repos that already have a local bare cache,
	// git will use hard links for shared objects automatically when on the
	// same filesystem.
	slog.Info("cloning job repository", "url", redactSensitiveText(authURL, nil), "path", destPath, "base_branch", baseBranch)
	if err := runGitWithOptions(ctx, "", optionsFromAuth(auth), "clone", "--branch", baseBranch, authURL, destPath); err != nil {
		return fmt.Errorf("clone for job: %w", err)
	}

	if err := ensureRemoteSanitized(ctx, destPath, "origin", repoURL, authURL, auth); err != nil {
		return fmt.Errorf("sanitize origin remote: %w", err)
	}

	// Create and checkout the job branch.
	if err := runGit(ctx, destPath, "checkout", "-b", branchName); err != nil {
		return fmt.Errorf("create job branch: %w", err)
	}

	return nil
}

func prepareCloneDestination(destPath string) (string, error) {
	if strings.TrimSpace(destPath) == "" {
		return "", fmt.Errorf("destination path is empty")
	}

	cleanPath := filepath.Clean(destPath)
	if cleanPath == "." || cleanPath == ".." || isFilesystemRoot(cleanPath) {
		return "", fmt.Errorf("destination path %q is unsafe", destPath)
	}

	if _, err := os.Stat(cleanPath); err == nil {
		if err := os.RemoveAll(cleanPath); err != nil {
			return "", fmt.Errorf("remove stale worktree %q: %w", cleanPath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat worktree destination %q: %w", cleanPath, err)
	}

	parent := filepath.Dir(cleanPath)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("create worktree parent %q: %w", parent, err)
	}

	return cleanPath, nil
}

func isFilesystemRoot(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	volume := filepath.VolumeName(path)
	root := volume + string(os.PathSeparator)
	return path == root
}

// RemoveJobDir removes a job's cloned working directory.
func RemoveJobDir(worktreePath string) {
	_ = os.RemoveAll(worktreePath)
}
