package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"autopr/internal/db"
	"autopr/internal/git"

	"github.com/spf13/cobra"
)

var cancelAll bool

var cancelCmd = &cobra.Command{
	Use:   "cancel <job-id> | --all",
	Short: "Cancel a running job",
	Args:  cobra.ArbitraryArgs,
	RunE:  runCancel,
}

func init() {
	cancelCmd.Flags().BoolVar(&cancelAll, "all", false, "cancel all running/queued jobs")
	rootCmd.AddCommand(cancelCmd)
}

func runCancel(cmd *cobra.Command, args []string) error {
	if cancelAll && len(args) > 0 {
		return fmt.Errorf("cannot use <job-id> with --all")
	}
	if !cancelAll && len(args) != 1 {
		return fmt.Errorf("expected exactly one <job-id> or use --all")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	store, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	if cancelAll {
		ids, warnings, err := cancelAllJobs(cmd.Context(), store, cfg.ReposRoot)
		if err != nil {
			return err
		}
		if jsonOut {
			printJSON(map[string]any{
				"cancelled": ids,
				"count":     len(ids),
				"warnings":  warnings,
			})
			return nil
		}
		if len(ids) == 0 {
			fmt.Println("No cancellable jobs found.")
			return nil
		}

		short := make([]string, 0, len(ids))
		for _, id := range ids {
			short = append(short, db.ShortID(id))
		}
		sort.Strings(short)
		fmt.Printf("Cancelled %d jobs: %s\n", len(ids), strings.Join(short, ", "))
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
		return nil
	}

	jobID, err := resolveJob(store, args[0])
	if err != nil {
		return err
	}
	warnings, err := cancelJobByID(cmd.Context(), store, jobID, cfg.ReposRoot)
	if err != nil {
		return err
	}

	if jsonOut {
		printJSON(map[string]any{
			"job_id":   jobID,
			"state":    "cancelled",
			"warnings": warnings,
		})
		return nil
	}

	fmt.Printf("Job %s cancelled.\n", jobID)
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	return nil
}

func cancelJobByID(ctx context.Context, store *db.Store, jobID, reposRoot string) ([]string, error) {
	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if !db.IsCancellableState(job.State) {
		if job.State == "ready" {
			return nil, fmt.Errorf("job %s is in state %q and cannot be cancelled (use 'ap reject <job-id>')", jobID, job.State)
		}
		return nil, fmt.Errorf("job %s is in state %q and cannot be cancelled", jobID, job.State)
	}
	if err := store.CancelJob(ctx, jobID); err != nil {
		return nil, err
	}

	var warnings []string
	if err := store.MarkRunningSessionsCancelled(ctx, jobID); err != nil {
		warnings = append(warnings, fmt.Sprintf("%s: mark sessions cancelled: %v", db.ShortID(jobID), err))
	}
	if err := cleanupCancelledWorktree(ctx, store, job, reposRoot); err != nil {
		warnings = append(warnings, fmt.Sprintf("%s: cleanup worktree: %v", db.ShortID(jobID), err))
	}
	return warnings, nil
}

func cancelAllJobs(ctx context.Context, store *db.Store, reposRoot string) ([]string, []string, error) {
	ids, err := store.CancelAllCancellableJobs(ctx)
	if err != nil {
		return nil, nil, err
	}

	var warnings []string
	for _, id := range ids {
		if err := store.MarkRunningSessionsCancelled(ctx, id); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: mark sessions cancelled: %v", db.ShortID(id), err))
		}
		job, err := store.GetJob(ctx, id)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: load job for cleanup: %v", db.ShortID(id), err))
			continue
		}
		if err := cleanupCancelledWorktree(ctx, store, job, reposRoot); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: cleanup worktree: %v", db.ShortID(id), err))
		}
	}
	return ids, warnings, nil
}

func cleanupCancelledWorktree(ctx context.Context, store *db.Store, job db.Job, reposRoot string) error {
	worktreePath := job.WorktreePath
	if worktreePath == "" && reposRoot != "" {
		worktreePath = filepath.Join(reposRoot, "worktrees", job.ID)
	}
	if worktreePath == "" {
		return nil
	}

	git.RemoveJobDir(worktreePath)
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		if err == nil {
			return fmt.Errorf("worktree path still exists: %s", worktreePath)
		}
		return err
	}

	if job.WorktreePath != "" {
		if err := store.ClearWorktreePath(ctx, job.ID); err != nil {
			return err
		}
	}
	return nil
}
