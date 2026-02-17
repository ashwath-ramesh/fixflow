package cli

import (
	"fmt"
	"os"

	"autopr/internal/db"
	"autopr/internal/git"

	"github.com/spf13/cobra"
)

var cleanupDryRun bool

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Remove worktree directories for completed, rejected, failed, and cancelled jobs",
	RunE:  runCleanup,
}

func init() {
	cleanupCmd.Flags().BoolVar(&cleanupDryRun, "dry-run", false, "show what would be removed without deleting")
	rootCmd.AddCommand(cleanupCmd)
}

func runCleanup(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	store, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	jobs, err := store.ListCleanableJobs(cmd.Context())
	if err != nil {
		return err
	}

	if len(jobs) == 0 {
		fmt.Println("No worktrees to clean up.")
		return nil
	}

	var cleaned, skipped int
	for _, job := range jobs {
		shortID := db.ShortID(job.ID)

		// Check if directory actually exists.
		if _, err := os.Stat(job.WorktreePath); os.IsNotExist(err) {
			// Directory already gone — just clear the DB field.
			if !cleanupDryRun {
				_ = store.ClearWorktreePath(cmd.Context(), job.ID)
			}
			skipped++
			continue
		}

		if cleanupDryRun {
			fmt.Printf("  [dry-run] would remove %s  (%s, %s)\n", job.WorktreePath, shortID, job.State)
			cleaned++
			continue
		}

		git.RemoveJobDir(job.WorktreePath)
		if err := store.ClearWorktreePath(cmd.Context(), job.ID); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: removed dir but failed to update DB for %s: %v\n", shortID, err)
			continue
		}

		fmt.Printf("  removed %s  (%s, %s)\n", job.WorktreePath, shortID, job.State)
		cleaned++
	}

	if cleanupDryRun {
		fmt.Printf("\n%d worktrees would be removed (%d already missing).\n", cleaned, skipped)
	} else {
		fmt.Printf("\n%d worktrees removed (%d already missing).\n", cleaned, skipped)
	}
	return nil
}
