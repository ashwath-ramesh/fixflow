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

	findGitHubPRByBranch func(ctx context.Context, token, owner, repo, head, state string) (string, error)
	findGitLabMRByBranch func(ctx context.Context, token, baseURL, projectID, sourceBranch, state string) (string, error)
	checkGitHubPRStatus  func(ctx context.Context, token, prURL string) (git.PRMergeStatus, error)
	checkGitLabMRStatus  func(ctx context.Context, token, baseURL, mrURL string) (git.PRMergeStatus, error)
}

func NewSyncer(cfg *config.Config, store *db.Store, jobCh chan<- string) *Syncer {
	return &Syncer{
		cfg:                  cfg,
		store:                store,
		jobCh:                jobCh,
		findGitHubPRByBranch: git.FindGitHubPRByBranch,
		findGitLabMRByBranch: git.FindGitLabMRByBranch,
		checkGitHubPRStatus:  git.CheckGitHubPRStatus,
		checkGitLabMRStatus:  git.CheckGitLabMRStatus,
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

// createJobIfNeeded creates a job for an issue if there isn't already an active one.
func (s *Syncer) createJobIfNeeded(ctx context.Context, ffid, projectName string) {
	active, err := s.store.HasActiveJobForIssue(ctx, ffid)
	if err != nil {
		slog.Error("sync: check active job", "err", err)
		return
	}
	if active {
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
			prURL      string
			lookupErr  error
			branchName = strings.TrimSpace(job.BranchName)
		)
		switch {
		case proj.GitHub != nil:
			if s.cfg.Tokens.GitHub == "" || branchName == "" {
				continue
			}
			prURL, lookupErr = s.findGitHubPRByBranch(ctx, s.cfg.Tokens.GitHub, proj.GitHub.Owner, proj.GitHub.Repo, branchName, "all")
		case proj.GitLab != nil:
			if s.cfg.Tokens.GitLab == "" || branchName == "" {
				continue
			}
			prURL, lookupErr = s.findGitLabMRByBranch(
				ctx,
				s.cfg.Tokens.GitLab,
				normalizeGitLabBaseURL(proj.GitLab.BaseURL),
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
	var (
		status   git.PRMergeStatus
		checkErr error
	)

	switch {
	case proj.GitHub != nil && strings.Contains(job.PRURL, "/pull/"):
		if s.cfg.Tokens.GitHub == "" {
			return
		}
		status, checkErr = s.checkGitHubPRStatus(ctx, s.cfg.Tokens.GitHub, job.PRURL)
	case proj.GitLab != nil && strings.Contains(job.PRURL, "/merge_requests/"):
		if s.cfg.Tokens.GitLab == "" {
			return
		}
		status, checkErr = s.checkGitLabMRStatus(
			ctx,
			s.cfg.Tokens.GitLab,
			normalizeGitLabBaseURL(proj.GitLab.BaseURL),
			job.PRURL,
		)
	default:
		return
	}

	if checkErr != nil {
		slog.Warn("check PR status failed", "job", job.ID, "err", checkErr)
		return
	}

	if status.Merged {
		mergedAt := status.MergedAt
		if mergedAt == "" {
			mergedAt = time.Now().UTC().Format("2006-01-02T15:04:05Z")
		}
		if err := s.store.MarkJobMerged(ctx, job.ID, mergedAt); err != nil {
			slog.Error("mark job merged", "job", job.ID, "err", err)
			return
		}
		slog.Info("PR merged", "job", db.ShortID(job.ID), "pr_url", job.PRURL)
		s.cleanupWorktree(ctx, job)
		return
	}

	if status.Closed {
		closedAt := status.ClosedAt
		if closedAt == "" {
			closedAt = time.Now().UTC().Format("2006-01-02T15:04:05Z")
		}
		if err := s.store.MarkJobPRClosed(ctx, job.ID, closedAt); err != nil {
			slog.Error("mark job PR closed", "job", job.ID, "err", err)
			return
		}
		slog.Info("PR closed", "job", db.ShortID(job.ID), "pr_url", job.PRURL)
		s.cleanupWorktree(ctx, job)
	}
}

// cleanupWorktree removes the job's worktree directory and clears the DB field.
func (s *Syncer) cleanupWorktree(ctx context.Context, job db.Job) {
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

func normalizeGitLabBaseURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "https://gitlab.com"
	}
	return strings.TrimRight(baseURL, "/")
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
