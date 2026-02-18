package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"autopr/internal/config"
	"autopr/internal/db"
	"autopr/internal/git"
)

// Default prompt templates.
const (
	defaultPlanPrompt = `You are an expert software engineer. Analyze the following issue and create a detailed implementation plan.

<issue>
Title: {{title}}

{{body}}
</issue>

{{human_notes}}

Create a step-by-step implementation plan that includes:
1. Which files need to be modified or created
2. The specific changes needed in each file
3. Any potential risks or edge cases
4. Testing strategy

Output your plan in a clear, structured format.`

	defaultImplementPrompt = `You are an expert software engineer. Implement the changes described in the following plan.

<issue>
Title: {{title}}

{{body}}
</issue>

<plan>
{{plan}}
</plan>

{{review_feedback}}

Instructions:
- Implement all changes described in the plan
- Write clean, idiomatic code following the project's conventions
- Add or update tests as needed
- Make small, focused commits with clear messages`

	defaultCodeReviewPrompt = `You are an expert code reviewer. Review the changes made in this working directory.

<issue>
Title: {{title}}

{{body}}
</issue>

<plan>
{{plan}}
</plan>

Review the code changes for:
1. Correctness - does the code solve the issue?
2. Code quality - is it clean, readable, maintainable?
3. Testing - are there adequate tests?
4. Security - are there any vulnerabilities?
5. Performance - any obvious performance issues?

If the code is acceptable, respond with: APPROVED
If changes are needed, list the specific issues that must be fixed.`
)

func (r *Runner) runPlan(ctx context.Context, jobID string, issue db.Issue, projectCfg *config.ProjectConfig, workDir string) error {
	job, err := r.store.GetJob(ctx, jobID)
	if err != nil {
		return err
	}

	template := defaultPlanPrompt
	if projectCfg.Prompts != nil && projectCfg.Prompts.Plan != "" {
		if custom := LoadTemplate(projectCfg.Prompts.Plan); custom != "" {
			template = custom
		}
	}

	humanNotes := ""
	if job.HumanNotes != "" {
		humanNotes = fmt.Sprintf("<human_notes>\n%s\n</human_notes>", job.HumanNotes)
	}

	prompt := BuildPrompt(template, map[string]string{
		"title":       issue.Title,
		"body":        SanitizeIssueContent(issue.Body),
		"human_notes": humanNotes,
	})

	resp, err := r.invokeProvider(ctx, jobID, "plan", job.Iteration, workDir, prompt)
	if err != nil {
		return fmt.Errorf("plan step: %w", err)
	}

	// Store the plan as an artifact.
	_, err = r.store.CreateArtifact(ctx, jobID, issue.AutoPRIssueID, "plan", resp.Text, job.Iteration, "")
	if err != nil {
		return fmt.Errorf("store plan artifact: %w", err)
	}

	slog.Info("plan step completed", "job", jobID)
	return nil
}

func (r *Runner) runImplement(ctx context.Context, jobID string, issue db.Issue, projectCfg *config.ProjectConfig, workDir string) error {
	job, err := r.store.GetJob(ctx, jobID)
	if err != nil {
		return err
	}

	// Get the plan artifact.
	planArtifact, err := r.store.GetLatestArtifact(ctx, jobID, "plan")
	if err != nil {
		return fmt.Errorf("get plan: %w", err)
	}

	// Get previous review feedback if this is a re-implementation.
	reviewFeedback := ""
	if job.Iteration > 0 {
		if reviewArtifact, err := r.store.GetLatestArtifact(ctx, jobID, "code_review"); err == nil {
			reviewFeedback = fmt.Sprintf("<previous_review_feedback>\n%s\n</previous_review_feedback>", reviewArtifact.Content)
		}
		// Also include test output if available.
		if testArtifact, err := r.store.GetLatestArtifact(ctx, jobID, "test_output"); err == nil {
			reviewFeedback += fmt.Sprintf("\n\n<previous_test_output>\n%s\n</previous_test_output>", testArtifact.Content)
		}
	}

	template := defaultImplementPrompt
	if projectCfg.Prompts != nil && projectCfg.Prompts.Implement != "" {
		if custom := LoadTemplate(projectCfg.Prompts.Implement); custom != "" {
			template = custom
		}
	}

	prompt := BuildPrompt(template, map[string]string{
		"title":           issue.Title,
		"body":            SanitizeIssueContent(issue.Body),
		"plan":            planArtifact.Content,
		"review_feedback": reviewFeedback,
	})

	_, err = r.invokeProvider(ctx, jobID, "implement", job.Iteration, workDir, prompt)
	if err != nil {
		return fmt.Errorf("implement step: %w", err)
	}

	// Safety-net commit: some LLM providers leave changes uncommitted.
	sha, commitErr := git.CommitAll(ctx, workDir, "autopr: implement changes for "+issue.Title)
	if commitErr != nil {
		slog.Debug("safety-net commit skipped (nothing to commit)", "job", jobID)
	} else {
		slog.Info("safety-net commit created", "job", jobID, "sha", sha)
		_ = r.store.UpdateJobField(ctx, jobID, "commit_sha", sha)
	}

	slog.Info("implement step completed", "job", jobID)
	return nil
}

func (r *Runner) runCodeReview(ctx context.Context, jobID string, issue db.Issue, projectCfg *config.ProjectConfig, workDir string) error {
	job, err := r.store.GetJob(ctx, jobID)
	if err != nil {
		return err
	}

	planArtifact, err := r.store.GetLatestArtifact(ctx, jobID, "plan")
	if err != nil {
		return fmt.Errorf("get plan for review: %w", err)
	}

	template := defaultCodeReviewPrompt
	if projectCfg.Prompts != nil && projectCfg.Prompts.CodeReview != "" {
		if custom := LoadTemplate(projectCfg.Prompts.CodeReview); custom != "" {
			template = custom
		}
	}

	prompt := BuildPrompt(template, map[string]string{
		"title": issue.Title,
		"body":  SanitizeIssueContent(issue.Body),
		"plan":  planArtifact.Content,
	})

	resp, err := r.invokeProvider(ctx, jobID, "code_review", job.Iteration, workDir, prompt)
	if err != nil {
		return fmt.Errorf("code review step: %w", err)
	}

	// Store the review as an artifact.
	_, err = r.store.CreateArtifact(ctx, jobID, issue.AutoPRIssueID, "code_review", resp.Text, job.Iteration, "")
	if err != nil {
		return fmt.Errorf("store review artifact: %w", err)
	}

	// Check if review approved or needs changes.
	if !isApproved(resp.Text) {
		slog.Info("code review requested changes", "job", jobID, "iteration", job.Iteration)
		return errReviewChangesRequested
	}

	slog.Info("code review approved", "job", jobID)
	return nil
}

func (r *Runner) runTests(ctx context.Context, jobID string, issue db.Issue, projectCfg *config.ProjectConfig, workDir string) error {
	job, err := r.store.GetJob(ctx, jobID)
	if err != nil {
		return err
	}

	// Run the project's test command.
	testOutput, testErr := runTestCommand(ctx, workDir, projectCfg.TestCmd)

	// Store test output as artifact.
	_, err = r.store.CreateArtifact(ctx, jobID, issue.AutoPRIssueID, "test_output", testOutput, job.Iteration, "")
	if err != nil {
		slog.Warn("failed to store test artifact", "err", err)
	}

	if testErr != nil {
		if errors.Is(testErr, context.Canceled) || ctx.Err() != nil {
			return context.Canceled
		}
		slog.Info("tests failed", "job", jobID, "err", testErr)
		return errTestsFailed
	}

	slog.Info("test step completed", "job", jobID)
	return nil
}

func isApproved(text string) bool {
	upper := strings.ToUpper(text)
	// Reject if it explicitly says NOT APPROVED.
	if strings.Contains(upper, "NOT APPROVED") || strings.Contains(upper, "NOT YET APPROVED") {
		return false
	}
	return strings.Contains(upper, "APPROVED")
}

func runTestCommand(ctx context.Context, dir, testCmd string) (string, error) {
	if testCmd == "" {
		return "no test command configured", nil
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", testCmd)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	output := string(out)

	// Truncate output to prevent huge artifacts.
	if len(output) > 100000 {
		output = output[:100000] + "\n... (truncated)"
	}

	if err != nil && ctx.Err() != nil {
		return output, context.Canceled
	}
	return output, err
}
