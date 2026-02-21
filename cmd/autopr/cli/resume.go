package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var resumeCmd = &cobra.Command{
	Use:   "resume <job-id>",
	Short: "Resume a failed or cancelled job",
	Args:  cobra.ExactArgs(1),
	RunE:  runResume,
}

func init() {
	rootCmd.AddCommand(resumeCmd)
}

func runResume(cmd *cobra.Command, args []string) error {
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

	if job.State != "failed" && job.State != "cancelled" {
		return fmt.Errorf("job %s is in state %q, must be 'failed' or 'cancelled' to resume", jobID, job.State)
	}

	activeID, err := store.GetActiveJobForIssue(cmd.Context(), job.AutoPRIssueID)
	if err != nil {
		return err
	}
	if activeID != "" {
		return fmt.Errorf("cannot resume: another active job (%s) already exists for this issue", activeID)
	}

	if err := store.ResetJobForResume(cmd.Context(), jobID); err != nil {
		return err
	}

	if jsonOut {
		printJSON(map[string]string{"job_id": jobID, "state": "queued"})
		return nil
	}
	fmt.Printf("Job %s reset to queued.\n", jobID)
	return nil
}

