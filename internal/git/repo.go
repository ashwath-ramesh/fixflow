package git

import (
	"bytes"
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

// LatestCommit returns the HEAD commit SHA in the given directory.
func LatestCommit(ctx context.Context, dir string) (string, error) {
	out, err := runGitOutput(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CommitAll stages all changes (including new files) and commits with the given message.
func CommitAll(ctx context.Context, dir, message string) (string, error) {
	// Stage everything — LLM tools create new files that need to be included.
	if err := runGit(ctx, dir, "add", "-A"); err != nil {
		return "", fmt.Errorf("git add: %w", err)
	}

	// Check if there's anything to commit.
	out, err := runGitOutput(ctx, dir, "diff", "--cached", "--quiet")
	if err == nil {
		// No diff means nothing staged.
		return "", fmt.Errorf("nothing to commit")
	}
	// err != nil means there are staged changes (diff --cached returns exit 1).
	_ = out

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

// PushBranchWithLease pushes a branch with --force-with-lease.
func PushBranchWithLease(ctx context.Context, dir, branchName string) error {
	return runGit(ctx, dir, "push", "origin", "--force-with-lease", branchName)
}

// PushBranchCaptured pushes a branch to origin without writing output to the
// process stdout/stderr. Any git output is captured and included in errors.
func PushBranchCaptured(ctx context.Context, dir, branchName string) error {
	return runGitCaptured(ctx, dir, "push", "origin", branchName)
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

func runGitOutputAndErr(ctx context.Context, dir string, args ...string) (string, string, error) {
	return runGitOutputAndErrWithNoEditorSetting(ctx, dir, false, args...)
}

func runGitOutputAndErrWithNoEditor(ctx context.Context, dir string, args ...string) (string, string, error) {
	return runGitOutputAndErrWithNoEditorSetting(ctx, dir, true, args...)
}

func runGitOutputAndErrWithNoEditorSetting(ctx context.Context, dir string, noEditor bool, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if noEditor {
		cmd.Env = append(cmd.Environ(), "GIT_EDITOR=true")
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), stderr.String(), nil
	}
	return stdout.String(), stderr.String(), err
}

func runGitCaptured(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
		}
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
