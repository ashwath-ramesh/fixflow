package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"fixflow/internal/config"
	"fixflow/internal/db"
	"fixflow/internal/git"
	"fixflow/internal/llm"
)

// errReviewChangesRequested signals that code review requested changes.
var errReviewChangesRequested = errors.New("code review requested changes")

// Runner orchestrates the full pipeline for a job.
type Runner struct {
	store    *db.Store
	provider llm.Provider
	cfg      *config.Config
}

func New(store *db.Store, provider llm.Provider, cfg *config.Config) *Runner {
	return &Runner{store: store, provider: provider, cfg: cfg}
}

// Run processes a job through the pipeline: plan -> implement <-> review -> tests -> ready.
func (r *Runner) Run(ctx context.Context, jobID string) error {
	job, err := r.store.GetJob(ctx, jobID)
	if err != nil {
		return err
	}

	issue, err := r.store.GetIssueByFFID(ctx, job.FixFlowIssueID)
	if err != nil {
		return fmt.Errorf("get issue for job %s: %w", jobID, err)
	}

	projectCfg, ok := r.cfg.ProjectByName(job.ProjectName)
	if !ok {
		return r.failJob(ctx, jobID, job.State, "project not found: "+job.ProjectName)
	}

	// Determine token for git operations.
	token := r.tokenForProject(projectCfg)

	// Clone repo directly for this job (regular clone, not a worktree).
	branchName := fmt.Sprintf("fixflow/%s", jobID)
	worktreePath := filepath.Join(r.cfg.ReposRoot, "worktrees", jobID)

	if job.WorktreePath == "" {
		if err := git.CloneForJob(ctx, projectCfg.RepoURL, token, worktreePath, branchName, projectCfg.BaseBranch); err != nil {
			return r.failJob(ctx, jobID, job.State, "clone for job: "+err.Error())
		}
		_ = r.store.UpdateJobField(ctx, jobID, "worktree_path", worktreePath)
		_ = r.store.UpdateJobField(ctx, jobID, "branch_name", branchName)
	} else {
		worktreePath = job.WorktreePath
		branchName = job.BranchName
	}

	// Run pipeline steps based on current state.
	return r.runSteps(ctx, jobID, job.State, issue, projectCfg, worktreePath)
}

func (r *Runner) runSteps(ctx context.Context, jobID, currentState string, issue db.Issue, projectCfg *config.ProjectConfig, workDir string) error {
	steps := []struct {
		state string
		next  string
		run   func(ctx context.Context, jobID string, issue db.Issue, projectCfg *config.ProjectConfig, workDir string) error
	}{
		{"planning", "implementing", r.runPlan},
		{"implementing", "reviewing", r.runImplement},
		{"reviewing", "testing", r.runCodeReview},
		{"testing", "ready", r.runTests},
	}

	for _, step := range steps {
		if currentState != step.state {
			continue
		}

		slog.Info("running step", "job", jobID, "step", db.StepForState(step.state))

		if err := step.run(ctx, jobID, issue, projectCfg, workDir); err != nil {
			// Code review requested changes — loop back to implementing.
			if errors.Is(err, errReviewChangesRequested) {
				if err := r.store.TransitionState(ctx, jobID, "reviewing", "implementing"); err != nil {
					return err
				}
				return r.handleReviewLoop(ctx, jobID, issue, projectCfg, workDir)
			}
			return r.failJob(ctx, jobID, step.state, err.Error())
		}

		// Transition to next state.
		if err := r.store.TransitionState(ctx, jobID, step.state, step.next); err != nil {
			return err
		}
		currentState = step.next
	}

	return nil
}

func (r *Runner) handleReviewLoop(ctx context.Context, jobID string, issue db.Issue, projectCfg *config.ProjectConfig, workDir string) error {
	job, err := r.store.GetJob(ctx, jobID)
	if err != nil {
		return err
	}

	if job.Iteration >= job.MaxIterations {
		slog.Warn("max iterations reached", "job", jobID, "iterations", job.Iteration)
		return r.failJob(ctx, jobID, job.State, "max iterations reached")
	}

	if err := r.store.IncrementIteration(ctx, jobID); err != nil {
		return err
	}

	// Re-run from implementing.
	return r.runSteps(ctx, jobID, "implementing", issue, projectCfg, workDir)
}

func (r *Runner) failJob(ctx context.Context, jobID, fromState, errMsg string) error {
	slog.Error("job failed", "job", jobID, "state", fromState, "error", errMsg)
	_ = r.store.TransitionState(ctx, jobID, fromState, "failed")
	_ = r.store.UpdateJobField(ctx, jobID, "error_message", errMsg)
	return fmt.Errorf("job %s failed in %s: %s", jobID, fromState, errMsg)
}

func (r *Runner) invokeProvider(ctx context.Context, jobID, step string, iteration int, workDir, prompt string) (llm.Response, error) {
	sessionID, err := r.store.CreateSession(ctx, jobID, step, iteration, r.provider.Name())
	if err != nil {
		return llm.Response{}, fmt.Errorf("create session: %w", err)
	}

	resp, runErr := r.provider.Run(ctx, workDir, prompt)

	status := "completed"
	errMsg := ""
	if runErr != nil {
		status = "failed"
		errMsg = runErr.Error()
	}

	_ = r.store.CompleteSession(ctx, sessionID, status, resp.Text, "", resp.JSONLPath, resp.CommitSHA, errMsg, resp.InputTokens, resp.OutputTokens, resp.DurationMS)

	if runErr != nil {
		return resp, runErr
	}
	return resp, nil
}

func (r *Runner) tokenForProject(p *config.ProjectConfig) string {
	if p.GitLab != nil {
		return r.cfg.Tokens.GitLab
	}
	if p.GitHub != nil {
		return r.cfg.Tokens.GitHub
	}
	return ""
}
