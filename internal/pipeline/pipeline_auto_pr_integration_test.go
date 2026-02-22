package pipeline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"autopr/internal/config"
	"autopr/internal/db"
	"autopr/internal/git"
)

func TestMaybeAutoPR_WithForkOwnerPushesToForkRemote(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := db.Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	upstreamRemote := createBareRemoteWithMain(t, tmp)
	forkRemote := filepath.Join(tmp, "fork.git")
	runGitCmdLocal(t, "", "init", "--bare", forkRemote)

	cfg := &config.Config{
		ReposRoot: filepath.Join(tmp, "repos"),
		LLM:       config.LLMConfig{Provider: "codex"},
		Tokens:    config.TokensConfig{GitHub: "token"},
		Projects: []config.ProjectConfig{{
			Name:       "myproject",
			RepoURL:    upstreamRemote,
			BaseBranch: "main",
			TestCmd:    "true",
			GitHub: &config.ProjectGitHub{
				Owner:     "acme",
				Repo:      "repo",
				ForkOwner: "my-fork",
			},
		}},
	}

	issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "123",
		Title:         "forked PR auto PR",
		URL:           "https://github.com/acme/repo/issues/123",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}

	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	worktree := filepath.Join(cfg.ReposRoot, "worktrees", jobID)
	runGitCmdLocal(t, "", "clone", upstreamRemote, worktree)

	branchName := "autopr/fork-integration"
	runGitCmdLocal(t, worktree, "checkout", "-b", branchName)
	if err := appendCommitForBranch(t, worktree); err != nil {
		t.Fatalf("create commit: %v", err)
	}

	if _, err := store.Writer.ExecContext(ctx, `
		UPDATE jobs
		SET state = ?, branch_name = ?, worktree_path = ?
		WHERE id = ?`, "ready", branchName, worktree, jobID); err != nil {
		t.Fatalf("setup ready job: %v", err)
	}

	runner := New(store, nil, cfg)
	runner.prepareGitHubPushTarget = func(ctx context.Context, projectCfg *config.ProjectConfig, branchName, worktreePath, token string) (string, string, error) {
		if err := git.EnsureRemote(ctx, worktreePath, "fork", forkRemote); err != nil {
			return "", "", err
		}
		return "fork", projectCfg.GitHub.GitHubForkHead(branchName), nil
	}

	pushedRemote := ""
	runner.pushBranchWithLeaseToRemote = func(ctx context.Context, dir, remote, branchName, token string) error {
		pushedRemote = remote
		return git.PushBranchWithLeaseToRemoteWithToken(ctx, dir, remote, branchName, token)
	}

	var createdHead string
	runner.createPRForProjectFn = func(ctx context.Context, cfg *config.Config, proj *config.ProjectConfig, job db.Job, head, title, body string, draft bool) (string, error) {
		createdHead = head
		return "https://github.com/acme/repo/pull/123", nil
	}

	projectCfg := &cfg.Projects[0]
	if err := runner.maybeAutoPR(ctx, jobID, db.Issue{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "123",
		Title:         "forked PR auto PR",
		URL:           "https://github.com/acme/repo/issues/123",
		State:         "open",
	}, projectCfg); err != nil {
		t.Fatalf("auto PR: %v", err)
	}

	if pushedRemote != "fork" {
		t.Fatalf("expected fork push remote, got %q", pushedRemote)
	}
	if createdHead != "my-fork:"+branchName {
		t.Fatalf("expected fork head, got %q", createdHead)
	}

	out, err := runGitCommandOutput(t, "", "ls-remote", "--heads", forkRemote, branchName)
	if err != nil {
		t.Fatalf("verify fork remote push: %v", err)
	}
	if !strings.Contains(out, "refs/heads/"+branchName) {
		t.Fatalf("expected branch %q on fork remote, got: %q", branchName, out)
	}

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "awaiting_checks" {
		t.Fatalf("expected job state awaiting_checks, got %q", job.State)
	}
}

func appendCommitForBranch(t *testing.T, dir string) error {
	t.Helper()
	file := filepath.Join(dir, "AUTOPR-FORK-TEST.txt")
	if err := os.WriteFile(file, []byte("fork branch\n"), 0o644); err != nil {
		return err
	}
	runGitCmdLocal(t, dir, "config", "user.email", "test@example.com")
	runGitCmdLocal(t, dir, "config", "user.name", "AutoPR Test")
	runGitCmdLocal(t, dir, "add", filepath.Base(file))
	runGitCmdLocal(t, dir, "commit", "-m", "create fork test file")
	return nil
}

func runGitCommandOutput(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func TestMaybeAutoPR_ForkOwner_UnreachableRemoteValidationError(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	store, err := db.Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	upstreamRemote := createBareRemoteWithMain(t, tmp)
	unreachableFork := filepath.Join(tmp, "missing", "fork.git")

	cfg := &config.Config{
		ReposRoot: filepath.Join(tmp, "repos"),
		LLM:       config.LLMConfig{Provider: "codex"},
		Tokens:    config.TokensConfig{GitHub: "token"},
		Projects: []config.ProjectConfig{{
			Name:       "myproject",
			RepoURL:    upstreamRemote,
			BaseBranch: "main",
			TestCmd:    "true",
			GitHub: &config.ProjectGitHub{
				Owner:     "acme",
				Repo:      "repo",
				ForkOwner: "my-fork",
			},
		}},
	}

	issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "456",
		Title:         "unreachable fork",
		URL:           "https://github.com/acme/repo/issues/456",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	worktree := filepath.Join(cfg.ReposRoot, "worktrees", jobID)
	runGitCmdLocal(t, "", "clone", upstreamRemote, worktree)
	runGitCmdLocal(t, worktree, "checkout", "-b", "autopr/unreachable")
	if err := appendCommitForBranch(t, worktree); err != nil {
		t.Fatalf("create commit: %v", err)
	}
	if _, err := store.Writer.ExecContext(ctx, `
		UPDATE jobs
		SET state = ?, branch_name = ?, worktree_path = ?
		WHERE id = ?`, "ready", "autopr/unreachable", worktree, jobID); err != nil {
		t.Fatalf("setup ready job: %v", err)
	}

	runner := New(store, nil, cfg)
	runner.prepareGitHubPushTarget = func(ctx context.Context, projectCfg *config.ProjectConfig, branchName, worktreePath, token string) (string, string, error) {
		if err := git.EnsureRemote(ctx, worktreePath, "fork", unreachableFork); err != nil {
			return "", "", err
		}
		if err := git.CheckGitRemoteReachable(ctx, unreachableFork, token); err != nil {
			return "", "", fmt.Errorf("fork remote unreachable: %w", err)
		}
		return "fork", projectCfg.GitHub.GitHubForkHead(branchName), nil
	}

	projectCfg := &cfg.Projects[0]
	err = runner.maybeAutoPR(ctx, jobID, db.Issue{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "456",
		Title:         "unreachable fork",
		URL:           "https://github.com/acme/repo/issues/456",
		State:         "open",
	}, projectCfg)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "resolve auto-PR push target") {
		t.Fatalf("expected resolve auto-PR push target error, got: %v", err)
	}

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "ready" {
		t.Fatalf("expected job state ready after failure, got %q", job.State)
	}
}
