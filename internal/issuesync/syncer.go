package issuesync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"autopr/internal/config"
	"autopr/internal/db"
	"autopr/internal/git"
)

// Syncer periodically pulls issues from configured sources.
type Syncer struct {
	cfg   *config.Config
	store *db.Store
	jobCh chan<- string

	findGitHubPRByBranch    func(ctx context.Context, token, owner, repo, head, state string) (string, error)
	findGitLabMRByBranch    func(ctx context.Context, token, baseURL, projectID, sourceBranch, state string) (string, error)
	checkGitHubPRStatus     func(ctx context.Context, token, prURL string) (git.PRMergeStatus, error)
	checkGitLabMRStatus     func(ctx context.Context, token, baseURL, mrURL string) (git.PRMergeStatus, error)
	deleteRemoteBranch      func(ctx context.Context, dir, branchName, token string) error
	getGitHubCheckRunStatus func(ctx context.Context, token, owner, repo, ref string) (git.CheckRunStatus, error)
}

func NewSyncer(cfg *config.Config, store *db.Store, jobCh chan<- string) *Syncer {
	return &Syncer{
		cfg:                     cfg,
		store:                   store,
		jobCh:                   jobCh,
		findGitHubPRByBranch:    git.FindGitHubPRByBranch,
		findGitLabMRByBranch:    git.FindGitLabMRByBranch,
		checkGitHubPRStatus:     git.CheckGitHubPRStatus,
		checkGitLabMRStatus:     git.CheckGitLabMRStatus,
		deleteRemoteBranch:      git.DeleteRemoteBranchWithToken,
		getGitHubCheckRunStatus: git.GetGitHubCheckRunStatus,
	}
}

// RunLoop polls all configured sources at the given interval.
func (s *Syncer) RunLoop(ctx context.Context, interval time.Duration) {
	slog.Info("sync loop starting", "interval", interval)

	// Run immediately on start.
	s.syncAll(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Debug("sync loop stopping")
			return
		case <-ticker.C:
			s.syncAll(ctx)
		}
	}
}

func (s *Syncer) syncAll(ctx context.Context) {
	for i := range s.cfg.Projects {
		p := &s.cfg.Projects[i]
		if err := s.syncProject(ctx, p); err != nil {
			slog.Error("sync project failed", "project", p.Name, "err", err)
		}
	}

	// Check if any job PRs have been merged or closed.
	s.checkPRStatus(ctx)
}

func (s *Syncer) syncProject(ctx context.Context, p *config.ProjectConfig) error {
	if p.GitLab != nil {
		if err := s.syncGitLab(ctx, p); err != nil {
			return fmt.Errorf("gitlab sync: %w", err)
		}
	}
	if p.GitHub != nil {
		if err := s.syncGitHub(ctx, p); err != nil {
			return fmt.Errorf("github sync: %w", err)
		}
	}
	if p.Sentry != nil {
		if err := s.syncSentry(ctx, p); err != nil {
			return fmt.Errorf("sentry sync: %w", err)
		}
	}
	return nil
}

// createJobIfNeeded creates a job for an issue if there isn't already a non-merged one.
func (s *Syncer) createJobIfNeeded(ctx context.Context, ffid, projectName string) {
	exists, err := s.store.HasAnyNonMergedJobForIssue(ctx, ffid)
	if err != nil {
		slog.Error("sync: check existing job", "err", err)
		return
	}
	if exists {
		return
	}

	jobID, err := s.store.CreateJob(ctx, ffid, projectName, s.cfg.Daemon.MaxIterations)
	if err != nil {
		if errors.Is(err, db.ErrDuplicateActiveJob) {
			slog.Debug("sync: active job already exists, skipping", "ffid", ffid)
			return
		}
		slog.Error("sync: create job", "err", err)
		return
	}

	select {
	case s.jobCh <- jobID:
	default:
		slog.Warn("sync: job channel full", "job_id", jobID)
	}

	slog.Info("sync: created job", "job_id", jobID, "ffid", ffid)
}

// checkPRStatus polls GitHub/GitLab for jobs whose PR may have been merged or closed.
func (s *Syncer) checkPRStatus(ctx context.Context) {
	knownPRJobs, err := s.store.ListApprovedJobsWithPR(ctx)
	if err != nil {
		slog.Error("check PR status: list approved jobs", "err", err)
		return
	}

	fallbackJobs, err := s.store.ListReadyOrApprovedJobsWithBranchNoPR(ctx)
	if err != nil {
		slog.Error("check PR status: list fallback jobs", "err", err)
		return
	}

	if len(knownPRJobs) == 0 && len(fallbackJobs) == 0 {
		return
	}

	slog.Debug("checking PR status", "known_pr_count", len(knownPRJobs), "branch_fallback_count", len(fallbackJobs))

	for _, job := range knownPRJobs {
		proj, ok := s.cfg.ProjectByName(job.ProjectName)
		if !ok {
			continue
		}
		s.checkAndApplyPRStatus(ctx, job, proj)
	}

	for _, job := range fallbackJobs {
		proj, ok := s.cfg.ProjectByName(job.ProjectName)
		if !ok {
			continue
		}

		var (
			prURL        string
			lookupErr    error
			branchName   = strings.TrimSpace(job.BranchName)
			forkHeadName = branchName
		)
		switch {
		case proj.GitHub != nil:
			if s.cfg.Tokens.GitHub == "" || branchName == "" {
				continue
			}
			if strings.TrimSpace(proj.GitHub.ForkOwner) != "" {
				forkHeadName = proj.GitHub.GitHubForkHead(branchName)
			}
			prURL, lookupErr = s.findGitHubPRByBranch(ctx, s.cfg.Tokens.GitHub, proj.GitHub.Owner, proj.GitHub.Repo, forkHeadName, "all")
		case proj.GitLab != nil:
			if s.cfg.Tokens.GitLab == "" || branchName == "" {
				continue
			}
			prURL, lookupErr = s.findGitLabMRByBranch(
				ctx,
				s.cfg.Tokens.GitLab,
				git.NormalizeGitLabBaseURL(proj.GitLab.BaseURL),
				proj.GitLab.ProjectID,
				branchName,
				"all",
			)
		default:
			continue
		}
		if lookupErr != nil {
			slog.Warn("branch PR lookup failed", "job", job.ID, "branch", branchName, "err", lookupErr)
			continue
		}
		if prURL == "" {
			continue
		}

		if err := s.store.UpdateJobField(ctx, job.ID, "pr_url", prURL); err != nil {
			slog.Error("check PR status: persist discovered PR URL", "job", job.ID, "err", err)
			continue
		}
		if err := s.store.EnsureJobApproved(ctx, job.ID); err != nil {
			slog.Error("check PR status: ensure approved", "job", job.ID, "err", err)
			continue
		}
		job.PRURL = prURL
		job.State = "approved"
		s.checkAndApplyPRStatus(ctx, job, proj)
	}
}

func (s *Syncer) checkAndApplyPRStatus(ctx context.Context, job db.Job, proj *config.ProjectConfig) {
	if s.applyTerminalPRStatus(ctx, job, proj) {
		return
	}
}

func (s *Syncer) applyTerminalPRStatus(ctx context.Context, job db.Job, proj *config.ProjectConfig) bool {
	var (
		status   git.PRMergeStatus
		checkErr error
	)

	switch {
	case proj.GitHub != nil && strings.Contains(job.PRURL, "/pull/"):
		if s.cfg.Tokens.GitHub == "" {
			return false
		}
		status, checkErr = s.checkGitHubPRStatus(ctx, s.cfg.Tokens.GitHub, job.PRURL)
	case proj.GitLab != nil && strings.Contains(job.PRURL, "/merge_requests/"):
		if s.cfg.Tokens.GitLab == "" {
			return false
		}
		status, checkErr = s.checkGitLabMRStatus(
			ctx,
			s.cfg.Tokens.GitLab,
			git.NormalizeGitLabBaseURL(proj.GitLab.BaseURL),
			job.PRURL,
		)
	default:
		return false
	}

	if checkErr != nil {
		slog.Warn("check PR status failed", "job", job.ID, "err", checkErr)
		return false
	}

	if status.Merged {
		mergedAt := status.MergedAt
		if mergedAt == "" {
			mergedAt = time.Now().UTC().Format("2006-01-02T15:04:05Z")
		}
		if err := s.store.MarkJobMerged(ctx, job.ID, mergedAt); err != nil {
			slog.Error("mark job merged", "job", job.ID, "err", err)
			return false
		}
		slog.Info("PR merged", "job", db.ShortID(job.ID), "pr_url", job.PRURL)
		s.cleanupWorktree(ctx, job)
		return true
	}

	if status.Closed {
		closedAt := status.ClosedAt
		if closedAt == "" {
			closedAt = time.Now().UTC().Format("2006-01-02T15:04:05Z")
		}
		if err := s.store.MarkJobPRClosed(ctx, job.ID, closedAt); err != nil {
			slog.Error("mark job PR closed", "job", job.ID, "err", err)
			return false
		}
		slog.Info("PR closed", "job", db.ShortID(job.ID), "pr_url", job.PRURL)
		s.cleanupWorktree(ctx, job)
		return true
	}

	return false
}

// cleanupWorktree removes the job's worktree directory and clears the DB field.
func (s *Syncer) cleanupWorktree(ctx context.Context, job db.Job) {
	branchName := strings.TrimSpace(job.BranchName)
	if branchName != "" && job.WorktreePath != "" {
		token := ""
		if proj, ok := s.cfg.ProjectByName(job.ProjectName); ok {
			token = s.cfg.GitTokenForProject(proj)
		}
		if err := s.deleteRemoteBranch(ctx, job.WorktreePath, branchName, token); err != nil {
			slog.Warn("cleanup worktree: delete remote branch", "job", db.ShortID(job.ID), "branch", branchName, "err", err)
		}
	}
	if job.WorktreePath == "" {
		return
	}
	git.RemoveJobDir(job.WorktreePath)
	if err := s.store.ClearWorktreePath(ctx, job.ID); err != nil {
		slog.Error("cleanup worktree: clear DB path", "job", db.ShortID(job.ID), "err", err)
		return
	}
	slog.Info("worktree cleaned up", "job", db.ShortID(job.ID), "path", job.WorktreePath)
}

// CheckCIStatus polls GitHub check-runs for all awaiting_checks jobs and
// transitions them to approved (all passed) or rejected (any failed / timeout).
func (s *Syncer) CheckCIStatus(ctx context.Context) {
	ciTimeout, _ := time.ParseDuration(s.cfg.Daemon.CICheckTimeout)
	if ciTimeout <= 0 {
		ciTimeout = 30 * time.Minute
	}

	jobs, err := s.store.ListAwaitingChecksJobs(ctx)
	if err != nil {
		slog.Error("check CI status: list awaiting_checks jobs", "err", err)
		return
	}
	if len(jobs) == 0 {
		return
	}

	slog.Debug("checking CI status", "job_count", len(jobs))

	for _, job := range jobs {
		proj, ok := s.cfg.ProjectByName(job.ProjectName)
		if !ok {
			continue
		}

		// Non-GitHub projects: auto-approve (CI polling not supported).
		if proj.GitHub == nil {
			if err := s.store.UpdateJobCIStatusSummary(ctx, job.ID, "CI polling skipped: non-GitHub project"); err != nil {
				slog.Warn("check CI: persist summary", "job", job.ID, "err", err)
			}
			if err := s.store.TransitionState(ctx, job.ID, "awaiting_checks", "approved"); err != nil {
				slog.Error("check CI: auto-approve non-GitHub job", "job", job.ID, "err", err)
			}
			continue
		}

		// Handle PR close/merge before CI evaluation and timeout.
		if s.applyTerminalPRStatus(ctx, job, proj) {
			continue
		}

		// Timeout check.
		timeoutBase := strings.TrimSpace(job.CIStartedAt)
		if timeoutBase == "" {
			timeoutBase = job.UpdatedAt
		}
		updatedAt, ok := parseTimestamp(timeoutBase)
		if ok && time.Since(updatedAt) > ciTimeout {
			reason := fmt.Sprintf("CI check timeout: no result after %s", ciTimeout)
			if err := s.store.UpdateJobCIStatusSummary(ctx, job.ID, reason); err != nil {
				slog.Warn("check CI: persist timeout summary", "job", job.ID, "err", err)
			}
			if err := s.store.RejectJob(ctx, job.ID, "awaiting_checks", reason); err != nil {
				slog.Error("check CI: reject timed-out job", "job", job.ID, "err", err)
			} else {
				slog.Info("CI checks timed out", "job", db.ShortID(job.ID))
			}
			continue
		}

		// Prefer commit SHA for accuracy; fall back to branch name.
		ref := strings.TrimSpace(job.CommitSHA)
		if ref == "" {
			ref = strings.TrimSpace(job.BranchName)
		}
		if s.cfg.Tokens.GitHub == "" || ref == "" {
			continue
		}

		status, err := s.getGitHubCheckRunStatus(ctx, s.cfg.Tokens.GitHub, proj.GitHub.Owner, proj.GitHub.Repo, ref)
		if err != nil {
			slog.Warn("check CI: get check-run status", "job", job.ID, "err", err)
			continue
		}
		if err := s.store.UpdateJobCIStatusSummary(ctx, job.ID, formatCISummary(status)); err != nil {
			slog.Warn("check CI: persist summary", "job", job.ID, "err", err)
		}

		// No checks registered yet — wait for next poll.
		if status.Total == 0 {
			continue
		}

		// Any failed check → reject.
		if status.Failed > 0 {
			reason := fmt.Sprintf("CI check failed: %s", status.FailedCheckName)
			if status.FailedCheckURL != "" {
				reason += " (" + status.FailedCheckURL + ")"
			}
			if err := s.store.UpdateJobCIStatusSummary(ctx, job.ID, reason); err != nil {
				slog.Warn("check CI: persist failed summary", "job", job.ID, "err", err)
			}
			if err := s.store.RejectJob(ctx, job.ID, "awaiting_checks", reason); err != nil {
				slog.Error("check CI: reject failed job", "job", job.ID, "err", err)
			} else {
				slog.Info("CI check failed", "job", db.ShortID(job.ID), "check", status.FailedCheckName)
			}
			continue
		}

		// All completed and passed → approve.
		if status.Pending == 0 && status.Passed > 0 {
			if err := s.store.UpdateJobCIStatusSummary(ctx, job.ID, fmt.Sprintf("CI checks passed: %d/%d completed", status.Passed, status.Total)); err != nil {
				slog.Warn("check CI: persist passed summary", "job", job.ID, "err", err)
			}
			if err := s.store.TransitionState(ctx, job.ID, "awaiting_checks", "approved"); err != nil {
				slog.Error("check CI: approve job", "job", job.ID, "err", err)
			} else {
				slog.Info("CI checks passed", "job", db.ShortID(job.ID), "passed", status.Passed)
			}
			continue
		}

		// Still pending — wait for next poll cycle.
	}
}

func formatCISummary(status git.CheckRunStatus) string {
	if status.Total == 0 {
		return "CI checks pending: no check-runs registered yet"
	}
	summary := fmt.Sprintf(
		"CI checks: total=%d completed=%d passed=%d failed=%d pending=%d",
		status.Total,
		status.Completed,
		status.Passed,
		status.Failed,
		status.Pending,
	)
	if status.FailedCheckName != "" {
		summary += fmt.Sprintf(" (first failed: %s)", status.FailedCheckName)
		if status.FailedCheckURL != "" {
			summary += " " + status.FailedCheckURL
		}
	}
	return summary
}

// parseTimestamp parses an RFC3339 timestamp string.
func parseTimestamp(ts string) (time.Time, bool) {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return time.Time{}, false
		}
	}
	return t, true
}

func (s *Syncer) cancelJobsForClosedIssue(ctx context.Context, projectName, source, sourceIssueID, autoprIssueID string) {
	cancelledIDs, err := s.store.CancelCancellableJobsForIssue(ctx, autoprIssueID, db.CancelReasonSourceIssueClosed)
	if err != nil {
		slog.Error("sync: cancel jobs for closed issue",
			"project", projectName,
			"source", source,
			"issue", sourceIssueID,
			"err", err)
		return
	}
	for _, jobID := range cancelledIDs {
		if err := s.store.MarkRunningSessionsCancelled(ctx, jobID); err != nil {
			slog.Warn("sync: mark running sessions cancelled",
				"project", projectName,
				"source", source,
				"issue", sourceIssueID,
				"job", jobID,
				"err", err)
		}
		slog.Info("sync: cancelled job for closed issue",
			"project", projectName,
			"source", source,
			"issue", sourceIssueID,
			"job", jobID,
			"reason", db.CancelReasonSourceIssueClosed)
	}
}
