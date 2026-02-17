package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var retryNotes string

var retryCmd = &cobra.Command{
	Use:   "retry <job-id>",
	Short: "Retry a failed or rejected job",
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

	if job.State != "failed" && job.State != "rejected" {
		return fmt.Errorf("job %s is in state %q, must be 'failed' or 'rejected' to retry", jobID, job.State)
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
