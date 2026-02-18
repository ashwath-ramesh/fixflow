package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var retryNotes string

var retryCmd = &cobra.Command{
	Use:   "retry <job-id>",
	Short: "Retry a failed, rejected, or cancelled job",
	Args:  cobra.ExactArgs(1),
	RunE:  runRetry,
}

func init() {
	retryCmd.Flags().StringVarP(&retryNotes, "notes", "n", "", "Notes or guidance for the retry")
	rootCmd.AddCommand(retryCmd)
}

func runRetry(cmd *cobra.Command, args []string) error {
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

	if job.State != "failed" && job.State != "rejected" && job.State != "cancelled" {
		return fmt.Errorf("job %s is in state %q, must be 'failed', 'rejected', or 'cancelled' to retry", jobID, job.State)
	}

	// Proactive check: give a clear error if another active job already exists for this issue.
	activeID, err := store.GetActiveJobForIssue(cmd.Context(), job.AutoPRIssueID)
	if err != nil {
		return err
	}
	if activeID != "" {
		return fmt.Errorf("cannot retry: another active job (%s) already exists for this issue", activeID)
	}

	if err := store.ResetJobForRetry(cmd.Context(), jobID, retryNotes); err != nil {
		return err
	}

	if jsonOut {
		printJSON(map[string]string{"job_id": jobID, "state": "queued", "notes": retryNotes})
		return nil
	}
	fmt.Printf("Job %s reset to queued.\n", jobID)
	return nil
}
