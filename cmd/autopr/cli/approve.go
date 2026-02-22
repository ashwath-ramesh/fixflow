package cli

import (
	"fmt"

	"autopr/internal/git"
	"autopr/internal/pipeline"

	"github.com/spf13/cobra"
)

var approveDraft bool

var approveCmd = &cobra.Command{
	Use:   "approve <job-id>",
	Short: "Approve a job and create a pull request",
	Args:  cobra.ExactArgs(1),
	RunE:  runApprove,
}

func init() {
	approveCmd.Flags().BoolVar(&approveDraft, "draft", false, "create PR as draft")
	rootCmd.AddCommand(approveCmd)
}

func runApprove(cmd *cobra.Command, args []string) error {
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

	if job.State != "ready" {
		return fmt.Errorf("job %s is in state %q, must be 'ready' to approve", jobID, job.State)
	}

	// Look up project config and issue for PR creation.
	proj, ok := cfg.ProjectByName(job.ProjectName)
	if !ok {
		return fmt.Errorf("project %q not found in config", job.ProjectName)
	}

	issue, err := store.GetIssueByAPID(cmd.Context(), job.AutoPRIssueID)
	if err != nil {
		return fmt.Errorf("load issue: %w", err)
	}

	// Rebase onto latest base branch before pushing.
	if err := pipeline.RebaseBeforePush(cmd.Context(), store, job.ID, job.AutoPRIssueID, proj.BaseBranch, job.WorktreePath, job.Iteration, cfg.GitTokenForProject(proj)); err != nil {
		return fmt.Errorf("rebase before push: %w", err)
	}

	pushRemote := "origin"
	pushHead := job.BranchName
	if proj.GitHub != nil {
		var err error
		pushRemote, pushHead, err = pipeline.ResolveGitHubPushTarget(cmd.Context(), proj, job.BranchName, job.WorktreePath, cfg.Tokens.GitHub)
		if err != nil {
			return fmt.Errorf("resolve push target: %w", err)
		}
	}

	// Push branch to remote before creating PR.
	if err := git.PushBranchWithLeaseToRemoteWithToken(cmd.Context(), job.WorktreePath, pushRemote, job.BranchName, cfg.GitTokenForProject(proj)); err != nil {
		return fmt.Errorf("push branch: %w", err)
	}

	prURL := job.PRURL
	if prURL != "" {
		// PR already created (e.g. by auto_pr), skip creation.
		fmt.Printf("PR already exists: %s\n", prURL)
	} else {
		prTitle, prBody := pipeline.BuildPRContent(cmd.Context(), store, job, issue)

		// Create PR/MR depending on source.
		prURL, err = pipeline.CreatePRForProject(cmd.Context(), cfg, proj, job, pushHead, prTitle, prBody, approveDraft)
		if err != nil {
			return fmt.Errorf("create PR: %w", err)
		}

		// Store PR URL.
		if prURL != "" {
			if err := store.UpdateJobField(cmd.Context(), jobID, "pr_url", prURL); err != nil {
				return fmt.Errorf("store PR URL: %w", err)
			}
		}
	}

	// Transition to approved.
	if err := store.TransitionState(cmd.Context(), jobID, "ready", "approved"); err != nil {
		return err
	}

	if jsonOut {
		out := map[string]string{"job_id": jobID, "state": "approved"}
		if prURL != "" {
			out["pr_url"] = prURL
		}
		printJSON(out)
		return nil
	}
	fmt.Printf("Job %s approved.\n", jobID)
	if prURL != "" {
		fmt.Printf("PR: %s\n", prURL)
	}
	return nil
}
