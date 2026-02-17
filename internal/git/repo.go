package git

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// EnsureClone clones the repo if it doesn't exist, otherwise fetches.
func EnsureClone(ctx context.Context, repoURL, localPath, token string) error {
	if _, err := os.Stat(localPath); err == nil {
		return Fetch(ctx, localPath)
	}
	authURL := injectToken(repoURL, token)
	slog.Info("cloning repository", "url", repoURL, "path", localPath)
	if err := os.MkdirAll(localPath, 0o755); err != nil {
		return fmt.Errorf("create repo dir: %w", err)
	}
	// Init as bare repo with origin configured so origin/* refs work with worktrees.
	if err := runGit(ctx, localPath, "init", "--bare"); err != nil {
		return err
	}
	if err := runGit(ctx, localPath, "remote", "add", "origin", authURL); err != nil {
		return err
	}
	return Fetch(ctx, localPath)
}

// Fetch fetches all refs in the bare repo.
func Fetch(ctx context.Context, localPath string) error {
	return runGit(ctx, localPath, "fetch", "--all", "--prune")
}

// CreateBranch creates a new branch from baseBranch in the bare repo.
func CreateBranch(ctx context.Context, repoPath, branchName, baseBranch string) error {
	return runGit(ctx, repoPath, "branch", branchName, "origin/"+baseBranch)
}

// LatestCommit returns the HEAD commit SHA in the given directory.
func LatestCommit(ctx context.Context, dir string) (string, error) {
	out, err := runGitOutput(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CommitAll stages all tracked changes and commits with the given message.
// Uses scoped add (not -A) to avoid accidental inclusion of untracked files.
func CommitAll(ctx context.Context, dir, message string) (string, error) {
	// Stage modified and deleted tracked files.
	if err := runGit(ctx, dir, "add", "-u"); err != nil {
		return "", fmt.Errorf("git add: %w", err)
	}

	// Check if there's anything to commit.
	out, err := runGitOutput(ctx, dir, "status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("git status: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("nothing to commit")
	}

	if err := runGit(ctx, dir, "commit", "-m", message); err != nil {
		return "", fmt.Errorf("git commit: %w", err)
	}

	return LatestCommit(ctx, dir)
}

// PushBranch pushes a branch to origin.
// NOTE: This requires Contents: Read and write on the GitHub fine-grained PAT.
// With read-only access, this call will fail with a permission error.
func PushBranch(ctx context.Context, dir, branchName string) error {
	return runGit(ctx, dir, "push", "origin", branchName)
}

func injectToken(repoURL, token string) string {
	if token == "" {
		return repoURL
	}
	// For HTTPS URLs, inject token as oauth2 credential.
	if strings.HasPrefix(repoURL, "https://") {
		return strings.Replace(repoURL, "https://", "https://oauth2:"+token+"@", 1)
	}
	return repoURL
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func runGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}
