package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status and queue depth",
	RunE:  runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	// Check daemon running.
	running := false
	pidStr := ""
	pidBytes, err := os.ReadFile(cfg.Daemon.PIDFile)
	if err == nil {
		pidStr = strings.TrimSpace(string(pidBytes))
		pid, err := strconv.Atoi(pidStr)
		if err == nil {
			if proc, err := os.FindProcess(pid); err == nil {
				if err := proc.Signal(syscall.Signal(0)); err == nil {
					running = true
				}
			}
		}
	}

	store, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	// Count jobs by state.
	type stateCount struct {
		State string
		Count int
	}
	rows, err := store.Reader.QueryContext(cmd.Context(), `SELECT state, COUNT(*) FROM jobs GROUP BY state`)
	if err != nil {
		return fmt.Errorf("count jobs: %w", err)
	}
	defer rows.Close()

	counts := map[string]int{
		"queued":       0,
		"planning":     0,
		"implementing": 0,
		"reviewing":    0,
		"testing":      0,
		"ready":        0,
		"approved":     0,
		"failed":       0,
		"cancelled":    0,
		"rejected":     0,
	}
	for rows.Next() {
		var sc stateCount
		if err := rows.Scan(&sc.State, &sc.Count); err != nil {
			return err
		}
		counts[sc.State] = sc.Count
	}

	// Count merged separately (approved jobs with pr_merged_at set).
	var merged int
	_ = store.Reader.QueryRowContext(cmd.Context(),
		`SELECT COUNT(*) FROM jobs WHERE state = 'approved' AND pr_merged_at IS NOT NULL AND pr_merged_at != ''`).Scan(&merged)
	prCreated := counts["approved"] - merged
	if prCreated < 0 {
		prCreated = 0
	}
	jobCounts := map[string]int{
		"queued":       counts["queued"],
		"planning":     counts["planning"],
		"implementing": counts["implementing"],
		"reviewing":    counts["reviewing"],
		"testing":      counts["testing"],
		"needs_pr":     counts["ready"],
		"failed":       counts["failed"],
		"cancelled":    counts["cancelled"],
		"rejected":     counts["rejected"],
		"pr_created":   prCreated,
		"merged":       merged,
	}

	if jsonOut {
		printJSON(map[string]any{
			"running":    running,
			"pid":        pidStr,
			"job_counts": jobCounts,
		})
		return nil
	}

	if running {
		fmt.Printf("Daemon: running (PID %s)\n", pidStr)
	} else {
		fmt.Println("Daemon: stopped")
	}
	fmt.Printf("Jobs: queued=%d planning=%d implementing=%d reviewing=%d testing=%d needs_pr=%d failed=%d cancelled=%d pr_created=%d merged=%d rejected=%d\n",
		counts["queued"], counts["planning"], counts["implementing"], counts["reviewing"],
		counts["testing"], counts["ready"], counts["failed"], counts["cancelled"], prCreated, merged, counts["rejected"])
	return nil
}
