package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"autopr/internal/config"
	"autopr/internal/db"
	"autopr/internal/git"
	"autopr/internal/safepath"
)

const (
	rebaseConflictArtifactKind = "rebase_conflict"
	rebaseResultArtifactKind   = "rebase_result"
	maxRebaseConflictFileBytes = 1_000_000
)

type rebaseConflictReport struct {
	conflicts       map[string][]ConflictRegion
	filePaths       []string
	resolvedPaths   map[string]string
	conflictLines   int
	summary         string
	artifactSummary string
}

// RebaseBeforePush rebases the job branch onto the latest base branch right
// before pushing for PR creation. It is called from all three approval paths
// (TUI, CLI, daemon auto_pr). Unlike the pipeline rebase step, conflicts here
// are not auto-resolved — any conflict is treated as a hard error so the user
// can re-run the pipeline.
func RebaseBeforePush(ctx context.Context, store *db.Store, jobID, issueAPID, baseBranch, workDir string, iteration int, token string) error {
	if err := git.FetchBranch(ctx, workDir, baseBranch, token); err != nil {
		return fmt.Errorf("fetch base branch: %w", err)
	}

	beforeSHA, err := git.LatestCommit(ctx, workDir)
	if err != nil {
		return fmt.Errorf("read HEAD before rebase: %w", err)
	}

	hasConflicts, err := git.RebaseOntoBase(ctx, workDir, baseBranch)
	if err != nil {
		abortRebaseIfNeeded(ctx, workDir)
		return fmt.Errorf("rebase onto %s: %w", baseBranch, err)
	}

	if hasConflicts {
		conflictFiles, _ := git.ConflictedFiles(ctx, workDir)
		abortRebaseIfNeeded(ctx, workDir)
		return fmt.Errorf("rebase onto %s has conflicts (resolve manually or re-run pipeline): %s",
			baseBranch, strings.Join(conflictFiles, ", "))
	}

	afterSHA, err := git.LatestCommit(ctx, workDir)
	if err != nil {
		return fmt.Errorf("read HEAD after rebase: %w", err)
	}

	var content string
	if beforeSHA == afterSHA {
		content = fmt.Sprintf("No-op: branch already up to date with %s", baseBranch)
	} else {
		content = fmt.Sprintf("Clean rebase onto %s before push\nBefore: %s\nAfter: %s", baseBranch, beforeSHA, afterSHA)
	}
	if _, err := store.CreateArtifact(ctx, jobID, issueAPID, rebaseResultArtifactKind, content, iteration, afterSHA); err != nil {
		slog.Warn("failed to store approval-time rebase_result artifact", "job", jobID, "err", err)
	}
	return nil
}

// abortRebaseIfNeeded is the package-level variant used by RebaseBeforePush.
func abortRebaseIfNeeded(ctx context.Context, workDir string) {
	if !git.IsRebaseInProgress(workDir) {
		return
	}
	abortCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := git.RebaseAbort(abortCtx, workDir); err != nil {
		slog.Warn("failed to abort rebase", "workdir", workDir, "err", err)
	}
}

func (r *Runner) runRebaseBeforeReady(ctx context.Context, jobID string, issue db.Issue, projectCfg *config.ProjectConfig, workDir string) error {
	iteration := 0
	if job, err := r.store.GetJob(ctx, jobID); err == nil {
		iteration = job.Iteration
	} else {
		slog.Warn("failed to load job for iteration", "job", jobID, "err", err)
	}

	if err := r.store.TransitionState(ctx, jobID, "testing", "rebasing"); err != nil {
		if r.isJobCancelledError(ctx, jobID, err) {
			return errJobCancelled
		}
		return err
	}

	if err := git.ConfigureDiff3(ctx, workDir); err != nil {
		return r.failJob(ctx, jobID, "rebasing", "configure git diff3 markers: "+err.Error())
	}
	if err := git.FetchBranch(ctx, workDir, projectCfg.BaseBranch, r.cfg.GitTokenForProject(projectCfg)); err != nil {
		return r.failJob(ctx, jobID, "rebasing", "fetch base branch: "+err.Error())
	}

	beforeSHA, err := git.LatestCommit(ctx, workDir)
	if err != nil {
		return r.failJob(ctx, jobID, "rebasing", "read head before rebase: "+err.Error())
	}

	hasConflicts, err := git.RebaseOntoBase(ctx, workDir, projectCfg.BaseBranch)
	if err != nil {
		r.abortRebaseIfNeeded(ctx, workDir)
		return r.failJob(ctx, jobID, "rebasing", "rebase onto base: "+err.Error())
	}

	afterSHA, err := git.LatestCommit(ctx, workDir)
	if err != nil {
		r.abortRebaseIfNeeded(ctx, workDir)
		return r.failJob(ctx, jobID, "rebasing", "read head after rebase: "+err.Error())
	}

	if !hasConflicts {
		if beforeSHA == afterSHA {
			// No-op; branch already contains the latest base commit.
			noopContent := fmt.Sprintf("No-op: branch already up to date with %s", projectCfg.BaseBranch)
			if _, err := r.store.CreateArtifact(ctx, jobID, issue.AutoPRIssueID, rebaseResultArtifactKind, noopContent, iteration, afterSHA); err != nil {
				slog.Warn("failed to store rebase_result artifact", "job", jobID, "err", err)
			}
			if err := r.store.TransitionState(ctx, jobID, "rebasing", "ready"); err != nil {
				if r.isJobCancelledError(ctx, jobID, err) {
					return errJobCancelled
				}
				return err
			}
			return nil
		}
		cleanContent := fmt.Sprintf("Clean rebase onto %s (no conflicts)\nBefore: %s\nAfter: %s", projectCfg.BaseBranch, beforeSHA, afterSHA)
		if _, err := r.store.CreateArtifact(ctx, jobID, issue.AutoPRIssueID, rebaseResultArtifactKind, cleanContent, iteration, afterSHA); err != nil {
			slog.Warn("failed to store rebase_result artifact", "job", jobID, "err", err)
		}
		return r.rerunTestsAndMarkReady(ctx, jobID, issue, projectCfg, workDir, "rebasing")
	}

	conflicts, err := r.collectRebaseConflicts(ctx, workDir)
	if err != nil {
		r.abortRebaseIfNeeded(ctx, workDir)
		return r.failJob(ctx, jobID, "rebasing", "collect rebase conflicts: "+err.Error())
	}
	if len(conflicts.filePaths) == 0 || len(conflicts.conflicts) == 0 {
		r.abortRebaseIfNeeded(ctx, workDir)
		return r.failJob(ctx, jobID, "rebasing", "no conflict files or parseable conflict regions found")
	}

	artifactText := fmt.Sprintf("Conflicts after rebasing onto %s\n\n%s", projectCfg.BaseBranch, conflicts.summary)
	if _, err := r.store.CreateArtifact(ctx, jobID, issue.AutoPRIssueID, rebaseConflictArtifactKind, artifactText, iteration, ""); err != nil {
		slog.Warn("failed to store rebase conflict artifact", "job", jobID, "err", err)
	}

	maxAutoResolvableConflictLines := config.DefaultMaxAutoResolvableConflictLines
	if projectCfg != nil {
		maxAutoResolvableConflictLines = projectCfg.MaxAutoResolvableConflictLines
	}
	if maxAutoResolvableConflictLines <= 0 {
		maxAutoResolvableConflictLines = config.DefaultMaxAutoResolvableConflictLines
	}
	if conflicts.conflictLines >= maxAutoResolvableConflictLines {
		r.abortRebaseIfNeeded(ctx, workDir)
		return r.failJob(ctx, jobID, "rebasing",
			fmt.Sprintf("rebase conflict line count %d reached limit %d (%s)",
				conflicts.conflictLines, maxAutoResolvableConflictLines, strings.Join(conflicts.filePaths, ", ")))
	}

	if err := r.store.TransitionState(ctx, jobID, "rebasing", "resolving_conflicts"); err != nil {
		r.abortRebaseIfNeeded(ctx, workDir)
		if r.isJobCancelledError(ctx, jobID, err) {
			return errJobCancelled
		}
		return err
	}

	if err := r.resolveRebaseConflictsWithLLM(ctx, jobID, issue, projectCfg, workDir, iteration, conflicts); err != nil {
		r.abortRebaseIfNeeded(ctx, workDir)
		if r.isJobCancelledError(ctx, jobID, err) {
			return errJobCancelled
		}
		return r.failJob(ctx, jobID, "resolving_conflicts", err.Error())
	}

	return r.rerunTestsAndMarkReady(ctx, jobID, issue, projectCfg, workDir, "resolving_conflicts")
}

func (r *Runner) resolveRebaseConflictsWithLLM(ctx context.Context, jobID string, issue db.Issue, projectCfg *config.ProjectConfig, workDir string, iteration int, conflicts rebaseConflictReport) error {
	template := defaultConflictResolvePrompt
	if projectCfg.Prompts != nil && projectCfg.Prompts.ConflictResolve != "" {
		if custom := LoadTemplate(projectCfg.Prompts.ConflictResolve); custom != "" {
			template = custom
		}
	}

	prompt := BuildPrompt(template, map[string]string{
		"base_branch":      projectCfg.BaseBranch,
		"conflict_files":   sanitizeConflictFilePaths(conflicts.filePaths),
		"conflict_details": SanitizeIssueContent(conflicts.summary),
	})

	resp, err := r.invokeProvider(ctx, jobID, "conflict_resolution", iteration, workDir, prompt)
	if err != nil {
		return fmt.Errorf("conflict resolution failed: %w", err)
	}

	summaryArtifact := conflicts.artifactSummary + fmt.Sprintf("\n\nResolved by LLM:\n%s", strings.TrimSpace(resp.Text))
	if _, err := r.store.CreateArtifact(ctx, jobID, issue.AutoPRIssueID, rebaseConflictArtifactKind, summaryArtifact, iteration, ""); err != nil {
		slog.Warn("failed to update rebase conflict artifact", "job", jobID, "err", err)
	}

	if err := r.stageAndVerifyResolvedConflicts(ctx, workDir, conflicts.conflicts, conflicts.resolvedPaths); err != nil {
		return fmt.Errorf("verify resolved conflicts: %w", err)
	}

	hasMoreConflicts, err := git.RebaseContinue(ctx, workDir)
	if err != nil {
		return fmt.Errorf("rebase continue: %w", err)
	}
	if hasMoreConflicts {
		return errors.New("multiple rebase conflicts remain after LLM resolution")
	}

	return nil
}

func (r *Runner) rerunTestsAndMarkReady(ctx context.Context, jobID string, issue db.Issue, projectCfg *config.ProjectConfig, workDir, fromState string) error {
	err := r.runTests(ctx, jobID, issue, projectCfg, workDir)
	if err != nil {
		if errors.Is(err, errJobCancelled) || errors.Is(err, context.Canceled) {
			return errJobCancelled
		}
		if errors.Is(err, errTestsFailed) {
			return r.failJob(ctx, jobID, fromState, "tests failed after rebase: "+err.Error())
		}
		return r.failJob(ctx, jobID, fromState, "rebase test run failed: "+err.Error())
	}
	if transErr := r.store.TransitionState(ctx, jobID, fromState, "ready"); transErr != nil {
		if r.isJobCancelledError(ctx, jobID, transErr) {
			return errJobCancelled
		}
		return transErr
	}
	return nil
}

func (r *Runner) collectRebaseConflicts(ctx context.Context, workDir string) (rebaseConflictReport, error) {
	conflictedFiles, err := git.ConflictedFiles(ctx, workDir)
	if err != nil {
		return rebaseConflictReport{}, err
	}
	sort.Strings(conflictedFiles)

	result := rebaseConflictReport{
		conflicts:     map[string][]ConflictRegion{},
		resolvedPaths: map[string]string{},
	}
	totalLines := 0
	details := []string{}

	for _, file := range conflictedFiles {
		path := filepath.Clean(file)
		absPath, err := rebaseConflictFilePath(workDir, path)
		if err != nil {
			return rebaseConflictReport{}, fmt.Errorf("resolve conflict file path %s: %w", path, err)
		}
		result.resolvedPaths[path] = absPath

		data, err := readRebaseConflictFile(absPath)
		if err != nil {
			return rebaseConflictReport{}, fmt.Errorf("read %s: %w", path, err)
		}
		regions := ParseConflicts(path, data)
		if len(regions) == 0 {
			continue
		}
		result.filePaths = append(result.filePaths, path)
		result.conflicts[path] = regions

		fileLineCount := CountConflictLines(regions)
		totalLines += fileLineCount
		details = append(details, renderConflictSummary(path, regions, fileLineCount))
	}

	summary := strings.Join(details, "\n")
	artifactSummary := fmt.Sprintf("conflict files: %s\ntotal conflict lines: %d\n\n", strings.Join(result.filePaths, ", "), totalLines)
	if summary == "" {
		summary = "(no conflict regions parsed)"
	}
	artifactSummary += summary

	result.summary = summary
	result.artifactSummary = artifactSummary
	result.conflictLines = totalLines
	return result, nil
}

func (r *Runner) stageAndVerifyResolvedConflicts(ctx context.Context, workDir string, conflicts map[string][]ConflictRegion, resolvedPaths map[string]string) error {
	filePaths := make([]string, 0, len(conflicts))
	for filePath := range conflicts {
		filePaths = append(filePaths, filePath)
	}
	sort.Strings(filePaths)
	stagePaths := make([]string, 0, len(filePaths))

	for _, filePath := range filePaths {
		absPath, ok := resolvedPaths[filePath]
		if !ok {
			var err error
			absPath, err = rebaseConflictFilePath(workDir, filePath)
			if err != nil {
				return fmt.Errorf("resolve conflict file path %s: %w", filePath, err)
			}
		}
		data, err := readRebaseConflictFile(absPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", filePath, err)
		}
		if HasConflictMarkers(data) {
			return fmt.Errorf("%s still has conflict markers after LLM response", filePath)
		}
		stagePaths = append(stagePaths, filePath)
	}

	if err := git.StageFiles(ctx, workDir, stagePaths...); err != nil {
		return fmt.Errorf("stage conflict files: %w", err)
	}

	return nil
}

func rebaseConflictFilePath(workDir, file string) (string, error) {
	if file == "" {
		return "", fmt.Errorf("empty conflict file path")
	}
	cleanFile := filepath.Clean(file)
	if filepath.IsAbs(cleanFile) {
		return "", fmt.Errorf("conflict file path must be relative: %s", file)
	}

	worktreeAbs, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	worktreeReal, err := filepath.EvalSymlinks(worktreeAbs)
	if err != nil {
		return "", err
	}
	absPath := filepath.Clean(filepath.Join(worktreeReal, cleanFile))
	resolvedAbsPath, err := safepath.ResolveNoSymlinkPath(worktreeReal, absPath)
	if err != nil {
		return "", fmt.Errorf("conflict file path is not safe: %w", err)
	}
	return resolvedAbsPath, nil
}

func readRebaseConflictFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("conflict file is not regular: %s", path)
	}
	if info.Size() > maxRebaseConflictFileBytes {
		return nil, fmt.Errorf("file too large for conflict analysis: %d bytes", info.Size())
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (r *Runner) abortRebaseIfNeeded(ctx context.Context, workDir string) {
	abortRebaseIfNeeded(ctx, workDir)
}

func sanitizeConflictFilePaths(filePaths []string) string {
	lines := make([]string, 0, len(filePaths))
	for _, filePath := range filePaths {
		lines = append(lines, "- "+filePath)
	}
	return SanitizeIssueContent(strings.Join(lines, "\n"))
}

func renderConflictSummary(filePath string, regions []ConflictRegion, fileTotal int) string {
	lines := []string{fmt.Sprintf("%s (%d lines)", filePath, fileTotal)}
	for _, region := range regions {
		lines = append(lines, fmt.Sprintf("  - %d-%d", region.StartLine, region.EndLine))
	}
	return strings.Join(lines, "\n")
}

const defaultConflictResolvePrompt = `You are an expert software engineer. Resolve the merge conflicts in the files below.

<base_branch>
{{base_branch}}
</base_branch>

<conflict_details>
The job branch was rebased onto {{base_branch}}.
The following files have conflicts:

{{conflict_files}}

{{conflict_details}}
</conflict_details>

Instructions:
- Open each conflicted file in the working directory.
- Resolve only conflict regions.
- Preserve both sides of intended changes where possible.
- Remove all conflict markers.
- Do not modify unrelated code.
- Do not add comments.
`
