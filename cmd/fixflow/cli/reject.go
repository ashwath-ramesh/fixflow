package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var rejectReason string

var rejectCmd = &cobra.Command{
	Use:   "reject <job-id>",
	Short: "Reject a job that is ready for human review",
	Args:  cobra.ExactArgs(1),
	RunE:  runReject,
}

func init() {
	rejectCmd.Flags().StringVarP(&rejectReason, "reason", "r", "", "Reason for rejection")
	rootCmd.AddCommand(rejectCmd)
}

func runReject(cmd *cobra.Command, args []string) error {
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

	if job.State != "ready" {
		return fmt.Errorf("job %s is in state %q, must be 'ready' to reject", jobID, job.State)
	}

	if err := store.TransitionState(cmd.Context(), jobID, "ready", "rejected"); err != nil {
		return err
	}

	if rejectReason != "" {
		_ = store.UpdateJobField(cmd.Context(), jobID, "reject_reason", rejectReason)
	}

	if jsonOut {
		printJSON(map[string]string{"job_id": jobID, "state": "rejected", "reason": rejectReason})
		return nil
	}
	fmt.Printf("Job %s rejected.\n", jobID)
	return nil
}
