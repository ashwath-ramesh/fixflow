package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"autopr/internal/db"
	"autopr/internal/git"

	"github.com/spf13/cobra"
)

var mergeMethod string

var mergeCmd = &cobra.Command{
	Use:   "merge <job-id>",
	Short: "Merge the PR/MR associated with an approved job",
	Args:  cobra.ExactArgs(1),
	RunE:  runMerge,
}

var (
	mergeGitHub = git.MergeGitHubPR
	mergeGitLab = git.MergeGitLabMR
	now         = func() string {
		return time.Now().UTC().Format("2006-01-02T15:04:05Z")
	}
	mergeCleanup = cleanupMergedWorktree
)

func init() {
	mergeCmd.Flags().StringVarP(&mergeMethod, "method", "m", "merge", "merge method: merge, squash, or rebase")
	rootCmd.AddCommand(mergeCmd)
}

func normalizeMergeMethod(method string) (string, error) {
	method = strings.ToLower(strings.TrimSpace(method))
	if method == "" {
		method = "merge"
	}
	switch method {
	case "merge", "squash", "rebase":
		return method, nil
	default:
		return "", fmt.Errorf("invalid merge method %q (must be merge, squash, or rebase)", method)
	}
}

func runMerge(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	store, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	jobID, err := resolveJob(store, args[0])
	if err != nil {
		return err
	}

	job, err := store.GetJob(cmd.Context(), jobID)
	if err != nil {
		return err
	}

	if job.State != "approved" {
		return fmt.Errorf("job %s is in state %q, must be 'approved' to merge", jobID, job.State)
	}
	if strings.TrimSpace(job.PRURL) == "" {
		return fmt.Errorf("job %s has no PR URL", jobID)
	}
	if strings.TrimSpace(job.PRMergedAt) != "" {
		return fmt.Errorf("job %s already merged", jobID)
	}

	method, err := normalizeMergeMethod(mergeMethod)
	if err != nil {
		return err
	}

	proj, ok := cfg.ProjectByName(job.ProjectName)
	if !ok {
		return fmt.Errorf("project %q not found in config", job.ProjectName)
	}

	switch {
	case proj.GitHub != nil:
		if cfg.Tokens.GitHub == "" {
			return fmt.Errorf("GITHUB_TOKEN required to merge PR")
		}
		if err := mergeGitHub(cmd.Context(), cfg.Tokens.GitHub, job.PRURL, method); err != nil {
			return fmt.Errorf("merge PR: %w", err)
		}
	case proj.GitLab != nil:
		if cfg.Tokens.GitLab == "" {
			return fmt.Errorf("GITLAB_TOKEN required to merge MR")
		}
		squash := method == "squash"
		if err := mergeGitLab(cmd.Context(), cfg.Tokens.GitLab, proj.GitLab.BaseURL, job.PRURL, squash); err != nil {
			return fmt.Errorf("merge MR: %w", err)
		}
	default:
		return fmt.Errorf("project %q has no GitHub or GitLab config for merge", proj.Name)
	}

	mergedAt := now()
	if err := store.MarkJobMerged(cmd.Context(), jobID, mergedAt); err != nil {
		return fmt.Errorf("mark job merged: %w", err)
	}

	if err := mergeCleanup(cmd.Context(), store, cfg.ReposRoot, job); err != nil {
		fmt.Fprintf(os.Stderr, "warning: cleanup worktree after merge: %v\n", err)
	}

	if jsonOut {
		printJSON(map[string]any{
			"job_id":    jobID,
			"state":     "merged",
			"pr_url":    job.PRURL,
			"method":    method,
			"merged_at": mergedAt,
		})
		return nil
	}

	fmt.Printf("Job %s merged.\n", jobID)
	fmt.Printf("PR: %s\n", job.PRURL)
	return nil
}

func cleanupMergedWorktree(ctx context.Context, store *db.Store, reposRoot string, job db.Job) error {
	worktreePath := strings.TrimSpace(job.WorktreePath)
	if worktreePath == "" && reposRoot != "" {
		worktreePath = filepath.Join(reposRoot, "worktrees", job.ID)
	}
	if worktreePath == "" {
		return nil
	}

	branchName := strings.TrimSpace(job.BranchName)
	if branchName != "" {
		if err := git.DeleteRemoteBranch(ctx, worktreePath, branchName); err != nil {
			return fmt.Errorf("delete remote branch: %w", err)
		}
	}

	git.RemoveJobDir(worktreePath)
	if err := store.ClearWorktreePath(ctx, job.ID); err != nil {
		return err
	}
	return nil
}
