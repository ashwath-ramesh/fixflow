package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"autopr/internal/cost"
	"autopr/internal/db"

	"github.com/spf13/cobra"
)

var logsFollow bool

var logsCmd = &cobra.Command{
	Use:   "logs <job-id>",
	Short: "Dump full session history for a job",
	Args:  cobra.ExactArgs(1),
	RunE:  runLogs,
}

func init() {
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "stream live LLM output")
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

	issue, issueErr := store.GetIssueByAPID(cmd.Context(), job.AutoPRIssueID)

	tokenSummary, _ := store.AggregateTokensByJob(cmd.Context(), jobID)

	if jsonOut {
		payload := map[string]any{
			"job":       job,
			"sessions":  sessions,
			"artifacts": artifacts,
		}
		if issueErr == nil {
			payload["issue"] = issue
		}
		if tokenSummary.SessionCount > 0 {
			payload["cost_summary"] = map[string]any{
				"sessions":      tokenSummary.SessionCount,
				"input_tokens":  tokenSummary.TotalInputTokens,
				"output_tokens": tokenSummary.TotalOutputTokens,
				"duration_ms":   tokenSummary.TotalDurationMS,
				"estimated_cost": cost.Calculate(tokenSummary.Provider,
					tokenSummary.TotalInputTokens, tokenSummary.TotalOutputTokens),
				"provider": tokenSummary.Provider,
			}
		}
		printJSON(payload)
		return nil
	}

	fmt.Printf("Job: %s  State: %s  Retry: %d/%d\n", job.ID, db.DisplayState(job.State, job.PRMergedAt, job.PRClosedAt), job.Iteration, job.MaxIterations)
	if issueErr == nil && issue.Source != "" && issue.SourceIssueID != "" {
		fmt.Printf("Issue: %s #%s  Project: %s\n",
			strings.ToUpper(issue.Source[:1])+issue.Source[1:], issue.SourceIssueID, job.ProjectName)
		if issue.Title != "" {
			fmt.Printf("Title: %s\n", issue.Title)
		}
		if issue.Eligible {
			fmt.Printf("Eligibility: eligible (evaluated_at: %s)\n", issue.EvaluatedAt)
		} else {
			reason := issue.SkipReason
			if reason == "" {
				reason = "ineligible"
			}
			fmt.Printf("Eligibility: ineligible (reason: %s, evaluated_at: %s)\n", reason, issue.EvaluatedAt)
		}
	} else {
		fmt.Printf("Issue: %s  Project: %s\n", job.AutoPRIssueID, job.ProjectName)
	}
	if job.BranchName != "" {
		fmt.Printf("Branch: %s  Commit: %s\n", job.BranchName, job.CommitSHA)
	}
	if job.ErrorMessage != "" {
		fmt.Printf("Error: %s\n", job.ErrorMessage)
	}
	if job.PRURL != "" {
		fmt.Printf("PR: %s\n", job.PRURL)
	}
	if job.PRMergedAt != "" {
		fmt.Printf("Merged: %s\n", job.PRMergedAt)
	}
	if job.PRClosedAt != "" {
		fmt.Printf("PR Closed: %s\n", job.PRClosedAt)
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
			content := a.Content
			if len(content) > 500 {
				content = content[:500] + "\n... (truncated)"
			}
			fmt.Println(strings.TrimSpace(content))
		}
	}

	// Cost summary.
	if tokenSummary.SessionCount > 0 {
		estCost := cost.Calculate(tokenSummary.Provider, tokenSummary.TotalInputTokens, tokenSummary.TotalOutputTokens)
		durationSec := float64(tokenSummary.TotalDurationMS) / 1000.0
		fmt.Println()
		fmt.Println("=== Cost Summary ===")
		fmt.Printf("Sessions: %d  Input: %d tokens  Output: %d tokens\n",
			tokenSummary.SessionCount, tokenSummary.TotalInputTokens, tokenSummary.TotalOutputTokens)
		fmt.Printf("Estimated cost: %s (%s @ %s)\n",
			cost.FormatUSD(estCost), tokenSummary.Provider, cost.FormatRate(tokenSummary.Provider))
		fmt.Printf("Total duration: %.1fs\n", durationSec)
	}

	// Follow mode.
	if logsFollow {
		return followJob(cmd.Context(), store, jobID, job)
	}

	return nil
}

// isTerminalState returns true if the job state is terminal.
func isTerminalState(state string) bool {
	switch state {
	case "ready", "approved", "rejected", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func followJob(ctx context.Context, store *db.Store, jobID string, job db.Job) error {
	if isTerminalState(job.State) {
		fmt.Println("\nJob already completed.")
		return nil
	}

	fmt.Println("\n--- Following live output (Ctrl+C to stop) ---")

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	for {
		session, err := store.GetRunningSessionForJob(ctx, jobID)
		if err != nil {
			return err
		}

		if session != nil && session.JSONLPath != "" {
			if err := tailJSONL(ctx, store, jobID, session); err != nil {
				if ctx.Err() != nil {
					fmt.Println("\nStopped following.")
					return nil
				}
				return err
			}
		}

		// Check if job is done.
		job, err = store.GetJob(ctx, jobID)
		if err != nil {
			return err
		}
		if isTerminalState(job.State) {
			fmt.Printf("\nJob reached state: %s\n", db.DisplayState(job.State, job.PRMergedAt, job.PRClosedAt))
			return nil
		}

		select {
		case <-ctx.Done():
			fmt.Println("\nStopped following.")
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}

// tailJSONL tails a JSONL file and prints live LLM output until the session
// is no longer running or the file stops growing.
//
// bufio.Scanner cannot resume after EOF, so we track the file offset manually
// and create a fresh scanner on each poll cycle.
func tailJSONL(ctx context.Context, store *db.Store, jobID string, session *db.LLMSession) error {
	f, err := os.Open(session.JSONLPath)
	if err != nil {
		if os.IsNotExist(err) {
			// File not yet created, wait a bit.
			time.Sleep(500 * time.Millisecond)
			return nil
		}
		return err
	}
	defer f.Close()

	// Start tailing from end of file.
	offset, err := f.Seek(0, 2)
	if err != nil {
		return err
	}

	idleCount := 0
	step := db.DisplayStep(session.Step)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Seek to our last known position and create a fresh scanner so
		// new data appended after a previous EOF is picked up.
		if _, err := f.Seek(offset, 0); err != nil {
			return err
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

		scanned := false
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			printJSONLLine(step, line)
			scanned = true
		}

		// Record where we stopped so the next iteration resumes here.
		newOffset, seekErr := f.Seek(0, 1)
		if seekErr == nil {
			offset = newOffset
		}

		if scanned {
			idleCount = 0
			continue // Drain all available lines before sleeping.
		}

		// No new lines — check if session is still running.
		idleCount++
		if idleCount > 10 { // 10 * 500ms = 5s idle
			sess, err := store.GetRunningSessionForJob(ctx, jobID)
			if err != nil {
				return err
			}
			if sess == nil || sess.ID != session.ID {
				return nil // Session ended or changed
			}
			idleCount = 0
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

type jsonlMessage struct {
	Type    string      `json:"type"`
	Message jsonlAssist `json:"message,omitempty"`
	Result  string      `json:"result,omitempty"`
	Item    *jsonlItem  `json:"item,omitempty"`
	Usage   *jsonlUsage `json:"usage,omitempty"`
}

type jsonlAssist struct {
	Content []jsonlBlock `json:"content,omitempty"`
	Usage   jsonlUsage   `json:"usage,omitempty"`
}

type jsonlBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type jsonlItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type jsonlUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func printJSONLLine(step, line string) {
	var msg jsonlMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return
	}

	switch {
	case msg.Type == "assistant" && msg.Message.Content != nil:
		for _, block := range msg.Message.Content {
			if block.Type == "text" && block.Text != "" {
				fmt.Printf("[%s] %s\n", step, block.Text)
			}
		}
	case msg.Type == "result" && msg.Result != "":
		fmt.Printf("[%s] %s\n", step, msg.Result)
	case msg.Type == "item.completed" && msg.Item != nil:
		if msg.Item.Type == "agent_message" && msg.Item.Text != "" {
			fmt.Printf("[%s] %s\n", step, msg.Item.Text)
		}
	case msg.Type == "turn.completed" && msg.Usage != nil:
		fmt.Printf("[%s] tokens: %d in / %d out\n", step, msg.Usage.InputTokens, msg.Usage.OutputTokens)
	}
}
