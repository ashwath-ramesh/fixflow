package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"autopr/internal/config"
	"autopr/internal/db"
	"autopr/internal/git"
	"autopr/internal/llm"
)

// errReviewChangesRequested signals that code review requested changes.
var errReviewChangesRequested = errors.New("code review requested changes")

// errTestsFailed signals that tests failed and the job should retry from implementing.
var errTestsFailed = errors.New("tests failed")

// errJobCancelled signals that a job was explicitly cancelled by the user.
var errJobCancelled = errors.New("job cancelled")

// Runner orchestrates the full pipeline for a job.
type Runner struct {
	store                       *db.Store
	provider                    llm.Provider
	cfg                         *config.Config
	cloneForJob                 func(ctx context.Context, repoURL, token, destPath, branchName, baseBranch string) error
	prepareGitHubPushTarget     func(ctx context.Context, projectCfg *config.ProjectConfig, branchName, worktreePath, token string) (string, string, error)
	pushBranchWithLeaseToRemote func(ctx context.Context, dir, remoteName, branchName, token string) error
	createPRForProjectFn        func(ctx context.Context, cfg *config.Config, proj *config.ProjectConfig, job db.Job, head, title, body string, draft bool) (string, error)
}

func New(store *db.Store, provider llm.Provider, cfg *config.Config) *Runner {
	return &Runner{
		store:                   store,
		provider:                provider,
		cfg:                     cfg,
		cloneForJob:             git.CloneForJob,
		prepareGitHubPushTarget: ResolveGitHubPushTarget,
		pushBranchWithLeaseToRemote: func(ctx context.Context, dir, remoteName, branchName, token string) error {
			return git.PushBranchWithLeaseToRemoteWithToken(ctx, dir, remoteName, branchName, token)
		},
		createPRForProjectFn: CreatePRForProject,
	}
}

// Run processes a job through the pipeline: plan -> implement <-> review -> tests -> ready.
func (r *Runner) Run(ctx context.Context, jobID string) error {
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	go r.watchForJobCancellation(runCtx, jobID, cancelRun)

	job, err := r.store.GetJob(ctx, jobID)
	if err != nil {
		return err
	}
	if job.State == "cancelled" {
		return nil
	}

	issue, err := r.store.GetIssueByAPID(ctx, job.AutoPRIssueID)
	if err != nil {
		return fmt.Errorf("get issue for job %s: %w", jobID, err)
	}

	projectCfg, ok := r.cfg.ProjectByName(job.ProjectName)
	if !ok {
		return r.failJob(ctx, jobID, job.State, "project not found: "+job.ProjectName)
	}

	// Determine token for git operations.
	token := r.cfg.GitTokenForProject(projectCfg)

	// Clone repo directly for this job (regular clone, not a worktree).
	branchName := buildBranchName(issue, jobID)
	worktreePath := filepath.Join(r.cfg.ReposRoot, "worktrees", jobID)

	if job.WorktreePath == "" {
		if err := r.store.UpdateJobField(ctx, jobID, "worktree_path", worktreePath); err != nil {
			if r.jobCancelled(jobID) {
				return r.onJobCancelled(jobID)
			}
			return r.failJob(ctx, jobID, job.State, "set worktree path: "+err.Error())
		}
		if err := r.store.UpdateJobField(ctx, jobID, "branch_name", branchName); err != nil {
			if r.jobCancelled(jobID) {
				return r.onJobCancelled(jobID)
			}
			return r.failJob(ctx, jobID, job.State, "set branch name: "+err.Error())
		}

		if err := r.cloneForJob(runCtx, projectCfg.RepoURL, token, worktreePath, branchName, projectCfg.BaseBranch); err != nil {
			if r.isJobCancelledError(runCtx, jobID, err) {
				return r.onJobCancelled(jobID)
			}
			return r.failJob(ctx, jobID, job.State, "clone for job: "+err.Error())
		}
	} else {
		worktreePath = job.WorktreePath
		branchName = job.BranchName
	}

	// Run pipeline steps based on current state.
	if err := r.runSteps(runCtx, jobID, job.State, issue, projectCfg, worktreePath); err != nil {
		if errors.Is(err, errJobCancelled) {
			return r.onJobCancelled(jobID)
		}
		return err
	}

	// Auto-create PR if configured.
	if r.cfg.Daemon.AutoPR {
		return r.maybeAutoPR(runCtx, jobID, issue, projectCfg)
	}

	return nil
}

func (r *Runner) runSteps(ctx context.Context, jobID, currentState string, issue db.Issue, projectCfg *config.ProjectConfig, workDir string) error {
	type pipelineStep struct {
		state string
		next  string
		run   func(ctx context.Context, jobID string, issue db.Issue, projectCfg *config.ProjectConfig, workDir string) error
		// true for steps that own their own failure transitions.
		// Caller should not call failJob() automatically.
		skipDefaultFailure bool
	}
	job, err := r.store.GetJob(ctx, jobID)
	if err != nil {
		return err
	}
	iteration := job.Iteration

	steps := []pipelineStep{
		{state: "planning", next: "implementing", run: r.runPlan},
		{state: "implementing", next: "reviewing", run: r.runImplement},
		{state: "reviewing", next: "testing", run: r.runCodeReview},
		{state: "testing", next: "", run: r.runTestingAndReadiness, skipDefaultFailure: true},
	}

	for _, step := range steps {
		if currentState != step.state {
			continue
		}
		if r.jobCancelled(jobID) {
			return errJobCancelled
		}
		stepName := db.StepForState(step.state)
		if stepName != "" {
			completed, err := r.store.HasCompletedSessionForStep(ctx, jobID, iteration, stepName)
			if err != nil {
				return err
			}
			if completed {
				slog.Info("skipping completed step", "job", jobID, "step", stepName)
				if r.jobCancelled(jobID) {
					return errJobCancelled
				}
				if step.next == "" {
					continue
				}
				if err := r.store.TransitionState(ctx, jobID, step.state, step.next); err != nil {
					if r.jobCancelled(jobID) {
						return errJobCancelled
					}
					return err
				}
				currentState = step.next
				continue
			}
		}

		slog.Info("running step", "job", jobID, "step", db.StepForState(step.state))

		if err := step.run(ctx, jobID, issue, projectCfg, workDir); err != nil {
			if r.isJobCancelledError(ctx, jobID, err) {
				return errJobCancelled
			}
			// Code review requested changes — loop back to implementing.
			if errors.Is(err, errReviewChangesRequested) {
				if err := r.store.TransitionState(ctx, jobID, "reviewing", "implementing"); err != nil {
					if r.jobCancelled(jobID) {
						return errJobCancelled
					}
					return err
				}
				return r.handleRetryLoop(ctx, jobID, issue, projectCfg, workDir)
			}
			// Tests failed — loop back to implementing so LLM can fix.
			if errors.Is(err, errTestsFailed) {
				slog.Info("tests failed, looping back to implement", "job", jobID)
				if err := r.store.TransitionState(ctx, jobID, "testing", "implementing"); err != nil {
					if r.jobCancelled(jobID) {
						return errJobCancelled
					}
					return err
				}
				return r.handleRetryLoop(ctx, jobID, issue, projectCfg, workDir)
			}
			if step.skipDefaultFailure {
				return err
			}
			return r.failJob(ctx, jobID, step.state, err.Error())
		}
		if step.next == "" {
			continue
		}
		if r.jobCancelled(jobID) {
			return errJobCancelled
		}

		// Transition to next state.
		if err := r.store.TransitionState(ctx, jobID, step.state, step.next); err != nil {
			if r.jobCancelled(jobID) {
				return errJobCancelled
			}
			return err
		}
		currentState = step.next
	}

	return nil
}

func (r *Runner) handleRetryLoop(ctx context.Context, jobID string, issue db.Issue, projectCfg *config.ProjectConfig, workDir string) error {
	if r.jobCancelled(jobID) {
		return errJobCancelled
	}
	job, err := r.store.GetJob(ctx, jobID)
	if err != nil {
		return err
	}

	if job.Iteration >= job.MaxIterations {
		slog.Info("max iterations reached, moving to ready for human review", "job", jobID, "iterations", job.Iteration)
		if err := r.store.TransitionState(ctx, jobID, job.State, "ready"); err != nil && !r.jobCancelled(jobID) {
			return err
		}
		return nil
	}

	if err := r.store.IncrementIteration(ctx, jobID); err != nil {
		if r.jobCancelled(jobID) {
			return errJobCancelled
		}
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
	// Generate JSONL path before session creation so it's stored in the DB
	// and discoverable by `ap logs --follow`.
	jsonlDir := filepath.Join(filepath.Dir(workDir), "sessions")
	_ = os.MkdirAll(jsonlDir, 0o755)
	jsonlPath := filepath.Join(jsonlDir, fmt.Sprintf("session-%d.jsonl", time.Now().UnixNano()))

	sessionID, err := r.store.CreateSession(ctx, jobID, step, iteration, r.provider.Name(), jsonlPath)
	if err != nil {
		return llm.Response{}, fmt.Errorf("create session: %w", err)
	}

	var resp llm.Response
	defer func() {
		status := "completed"
		errMsg := ""
		panicVal := recover()

		if panicVal != nil {
			status = "failed"
			errMsg = fmt.Sprintf("session interrupted: panic: %v", panicVal)
		} else if err != nil {
			if r.isJobCancelledError(ctx, jobID, err) {
				status = "cancelled"
				errMsg = "cancelled"
			} else {
				status = "failed"
				errMsg = sessionErrorMessage(err)
			}
		}

		completeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if completeErr := r.store.CompleteSession(completeCtx, sessionID, status, resp.Text, prompt, "", resp.JSONLPath, resp.CommitSHA, errMsg, resp.InputTokens, resp.OutputTokens, resp.DurationMS); completeErr != nil {
			slog.Warn("failed to complete llm session", "job", jobID, "session_id", sessionID, "status", status, "err", completeErr)
		}

		if panicVal != nil {
			panic(panicVal)
		}
	}()

	resp, err = r.provider.Run(ctx, workDir, prompt, jsonlPath)
	return resp, err
}

func sessionErrorMessage(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "session interrupted: context canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "session interrupted: context deadline exceeded"
	default:
		return err.Error()
	}
}

func (r *Runner) watchForJobCancellation(ctx context.Context, jobID string, cancel context.CancelFunc) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if r.jobCancelled(jobID) {
				cancel()
				return
			}
		}
	}
}

func (r *Runner) jobCancelled(jobID string) bool {
	job, err := r.store.GetJob(context.Background(), jobID)
	if err != nil {
		return false
	}
	return job.State == "cancelled"
}

func (r *Runner) isJobCancelledError(ctx context.Context, jobID string, err error) bool {
	if !r.jobCancelled(jobID) {
		return false
	}
	if errors.Is(err, errJobCancelled) || errors.Is(err, context.Canceled) {
		return true
	}
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context canceled") || strings.Contains(msg, "killed")
}

func (r *Runner) onJobCancelled(jobID string) error {
	_ = r.store.MarkRunningSessionsCancelled(context.Background(), jobID)
	return nil
}

// ResolveGitHubPushTarget chooses the push remote and PR head for a GitHub project.
//
// If github.fork_owner is set, pushes go to the "fork" remote and PR head uses
// "fork_owner:branch". Otherwise pushes go to origin and PR head is branch.
func ResolveGitHubPushTarget(ctx context.Context, projectCfg *config.ProjectConfig, branchName, worktreePath, token string) (string, string, error) {
	branchName = strings.TrimSpace(branchName)
	if projectCfg == nil || projectCfg.GitHub == nil || strings.TrimSpace(projectCfg.GitHub.ForkOwner) == "" {
		return "origin", branchName, nil
	}

	forkOwner := strings.TrimSpace(projectCfg.GitHub.ForkOwner)
	if forkOwner == "" {
		return "", "", fmt.Errorf("project %q github.fork_owner cannot be blank", projectCfg.Name)
	}
	if strings.TrimSpace(token) == "" {
		return "", "", fmt.Errorf("GITHUB_TOKEN required when github.fork_owner is set")
	}

	forkRemote := projectCfg.GitHub.GitHubForkRemote()
	if forkRemote == "" {
		return "", "", fmt.Errorf("project %q github.fork_owner could not resolve fork remote", projectCfg.Name)
	}

	if err := git.EnsureRemote(ctx, worktreePath, "fork", forkRemote); err != nil {
		return "", "", fmt.Errorf("ensure fork remote: %w", err)
	}
	if err := git.CheckGitRemoteReachable(ctx, forkRemote, token); err != nil {
		return "", "", fmt.Errorf("fork remote unreachable: %w", err)
	}

	return "fork", projectCfg.GitHub.GitHubForkHead(branchName), nil
}

func (r *Runner) maybeAutoPR(ctx context.Context, jobID string, issue db.Issue, projectCfg *config.ProjectConfig) error {
	job, err := r.store.GetJob(ctx, jobID)
	if err != nil {
		return err
	}
	if job.State != "ready" {
		return nil
	}

	// Rebase onto latest base branch before pushing.
	if err := RebaseBeforePush(ctx, r.store, job.ID, issue.AutoPRIssueID, projectCfg.BaseBranch, job.WorktreePath, job.Iteration, r.cfg.GitTokenForProject(projectCfg)); err != nil {
		return fmt.Errorf("rebase before auto-PR push: %w", err)
	}

	remoteName := "origin"
	head := job.BranchName
	if projectCfg.GitHub != nil {
		var err error
		remoteName, head, err = r.prepareGitHubPushTarget(ctx, projectCfg, job.BranchName, job.WorktreePath, r.cfg.GitTokenForProject(projectCfg))
		if err != nil {
			return fmt.Errorf("resolve auto-PR push target: %w", err)
		}
	}

	// Push branch to remote before creating PR.
	if err := r.pushBranchWithLeaseToRemote(ctx, job.WorktreePath, remoteName, job.BranchName, r.cfg.GitTokenForProject(projectCfg)); err != nil {
		return fmt.Errorf("push branch for auto-PR: %w", err)
	}

	slog.Info("auto_pr enabled, creating PR", "job", jobID)

	prTitle, prBody := BuildPRContent(ctx, r.store, job, issue)

	prURL, err := r.createPRForProjectFn(ctx, r.cfg, projectCfg, job, head, prTitle, prBody, false)
	if err != nil {
		slog.Error("auto-PR creation failed", "job", jobID, "err", err)
		return fmt.Errorf("auto-create PR: %w", err)
	}

	if prURL != "" {
		_ = r.store.UpdateJobField(ctx, jobID, "pr_url", prURL)
	}

	// GitHub projects with CI: transition to awaiting_checks so the daemon
	// polls check-runs before approving. GitLab projects approve immediately
	// (CI polling not yet supported).
	nextState := "approved"
	if projectCfg.GitHub != nil {
		nextState = "awaiting_checks"
	}

	if err := r.store.TransitionState(ctx, jobID, "ready", nextState); err != nil {
		return err
	}

	slog.Info("auto-PR created", "job", jobID, "pr_url", prURL, "next_state", nextState)
	return nil
}

// CreatePRForProject creates a GitHub PR or GitLab MR based on project config.
func CreatePRForProject(ctx context.Context, cfg *config.Config, proj *config.ProjectConfig, job db.Job, head, title, body string, draft bool) (string, error) {
	if job.BranchName == "" {
		return "", fmt.Errorf("job has no branch name — was the branch pushed?")
	}

	switch {
	case proj.GitHub != nil:
		if cfg.Tokens.GitHub == "" {
			return "", fmt.Errorf("GITHUB_TOKEN required to create PR")
		}
		return git.CreateGitHubPR(ctx, cfg.Tokens.GitHub, proj.GitHub.Owner, proj.GitHub.Repo,
			head, proj.BaseBranch, title, body, draft)

	case proj.GitLab != nil:
		if cfg.Tokens.GitLab == "" {
			return "", fmt.Errorf("GITLAB_TOKEN required to create MR")
		}
		return git.CreateGitLabMR(ctx, cfg.Tokens.GitLab, proj.GitLab.BaseURL, proj.GitLab.ProjectID,
			job.BranchName, proj.BaseBranch, title, body)

	default:
		return "", fmt.Errorf("project %q has no GitHub or GitLab config for PR creation", proj.Name)
	}
}

// BuildPRContent assembles the PR title and body from job data and artifacts.
func BuildPRContent(ctx context.Context, store *db.Store, job db.Job, issue db.Issue) (string, string) {
	title := fmt.Sprintf("[AutoPR] %s", issue.Title)

	var body strings.Builder
	body.WriteString(fmt.Sprintf("Closes %s\n\n", issue.URL))
	body.WriteString(fmt.Sprintf("**Issue:** %s\n\n", issue.Title))

	if plan, err := store.GetLatestArtifact(ctx, job.ID, "plan"); err == nil {
		content := plan.Content
		if len(content) > 2000 {
			content = content[:2000] + "\n\n_(truncated)_"
		}
		body.WriteString("<details>\n<summary>Plan</summary>\n\n")
		body.WriteString(content)
		body.WriteString("\n</details>\n\n")
	}

	if conflictArtifact, err := store.GetLatestArtifact(ctx, job.ID, "rebase_conflict"); err == nil {
		content := conflictArtifact.Content
		if len(content) > 2000 {
			content = content[:2000] + "\n\n_(truncated)_"
		}
		if strings.TrimSpace(content) != "" {
			body.WriteString("<details>\n<summary>Rebase Conflict Resolution</summary>\n\n")
			body.WriteString(content)
			body.WriteString("\n</details>\n\n")
		}
	}

	body.WriteString(fmt.Sprintf("_Generated by [AutoPR](https://github.com/ashwath-ramesh/autopr) from job `%s`_\n", db.ShortID(job.ID)))

	return title, body.String()
}

// buildBranchName creates a descriptive branch name from the issue.
// Includes a job-unique suffix to avoid collisions when repeated jobs target the same issue.
// Example: autopr/github-42-fix-login-timeout-8aeda806
func buildBranchName(issue db.Issue, jobID string) string {
	suffix := db.ShortID(jobID)

	// Start with source and issue number if available.
	prefix := "autopr/"
	if issue.Source != "" && issue.SourceIssueID != "" {
		prefix += issue.Source + "-" + issue.SourceIssueID + "-"
	}

	// Slugify the issue title.
	slug := slugify(issue.Title)
	if slug == "" {
		return "autopr/" + suffix
	}

	// Keep branch name reasonable length (reserve room for -suffix).
	name := prefix + slug
	maxLen := 60 - len(suffix) - 1 // -1 for the hyphen
	if len(name) > maxLen {
		name = name[:maxLen]
		name = strings.TrimRight(name, "-")
	}
	return name + "-" + suffix
}

// slugify converts a string to a git-branch-safe slug.
func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_' || r == '/' || r == '.':
			b.WriteRune('-')
		}
		// skip everything else
	}
	// Collapse consecutive hyphens.
	result := b.String()
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	return strings.Trim(result, "-")
}
