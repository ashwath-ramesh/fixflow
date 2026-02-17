package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var approveCmd = &cobra.Command{
	Use:   "approve <job-id>",
	Short: "Approve a job that is ready for human review",
	Args:  cobra.ExactArgs(1),
	RunE:  runApprove,
}

func init() {
	rootCmd.AddCommand(approveCmd)
}

func runApprove(cmd *cobra.Command, args []string) error {
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
		return fmt.Errorf("job %s is in state %q, must be 'ready' to approve", jobID, job.State)
	}

	if err := store.TransitionState(cmd.Context(), jobID, "ready", "approved"); err != nil {
		return err
	}

	if jsonOut {
		printJSON(map[string]string{"job_id": jobID, "state": "approved"})
		return nil
	}
	fmt.Printf("Job %s approved.\n", jobID)
	return nil
}
