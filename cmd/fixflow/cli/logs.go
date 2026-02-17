package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var logsCmd = &cobra.Command{
	Use:   "logs <job-id>",
	Short: "Dump full session history for a job",
	Args:  cobra.ExactArgs(1),
	RunE:  runLogs,
}

func init() {
	rootCmd.AddCommand(logsCmd)
}

func runLogs(cmd *cobra.Command, args []string) error {
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

	sessions, err := store.ListSessionsByJob(cmd.Context(), jobID)
	if err != nil {
		return err
	}

	artifacts, err := store.ListArtifactsByJob(cmd.Context(), jobID)
	if err != nil {
		return err
	}

	if jsonOut {
		printJSON(map[string]any{
			"job":       job,
			"sessions":  sessions,
			"artifacts": artifacts,
		})
		return nil
	}

	// Fetch linked issue for source info.
	issue, issueErr := store.GetIssueByFFID(cmd.Context(), job.FixFlowIssueID)

	fmt.Printf("Job: %s  State: %s  Iteration: %d/%d\n", job.ID, job.State, job.Iteration, job.MaxIterations)
	if issueErr == nil && issue.Source != "" && issue.SourceIssueID != "" {
		fmt.Printf("Issue: %s #%s  Project: %s\n",
			strings.ToUpper(issue.Source[:1])+issue.Source[1:], issue.SourceIssueID, job.ProjectName)
		if issue.Title != "" {
			fmt.Printf("Title: %s\n", issue.Title)
		}
	} else {
		fmt.Printf("Issue: %s  Project: %s\n", job.FixFlowIssueID, job.ProjectName)
	}
	if job.BranchName != "" {
		fmt.Printf("Branch: %s  Commit: %s\n", job.BranchName, job.CommitSHA)
	}
	if job.ErrorMessage != "" {
		fmt.Printf("Error: %s\n", job.ErrorMessage)
	}
	if job.MRURL != "" {
		fmt.Printf("MR: %s\n", job.MRURL)
	}
	fmt.Println()

	if len(sessions) > 0 {
		fmt.Println("=== LLM Sessions ===")
		for _, s := range sessions {
			fmt.Printf("\n--- %s (iter %d) [%s] %s ---\n", s.Step, s.Iteration, s.LLMProvider, s.Status)
			fmt.Printf("Tokens: %d in / %d out  Duration: %dms\n", s.InputTokens, s.OutputTokens, s.DurationMS)
			if s.JSONLPath != "" {
				fmt.Printf("JSONL: %s\n", s.JSONLPath)
			}
			if s.CommitSHA != "" {
				fmt.Printf("Commit: %s\n", s.CommitSHA)
			}
			if s.ErrorMessage != "" {
				fmt.Printf("Error: %s\n", s.ErrorMessage)
			}
		}
	}

	if len(artifacts) > 0 {
		fmt.Println("\n=== Artifacts ===")
		for _, a := range artifacts {
			fmt.Printf("\n--- %s (iter %d) ---\n", a.Kind, a.Iteration)
			// Show first 500 chars of content.
			content := a.Content
			if len(content) > 500 {
				content = content[:500] + "\n... (truncated)"
			}
			fmt.Println(strings.TrimSpace(content))
		}
	}

	return nil
}
