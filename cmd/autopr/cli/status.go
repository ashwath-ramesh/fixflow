package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

type statusJobCounts struct {
	Queued       int `json:"queued"`
	Planning     int `json:"planning"`
	Implementing int `json:"implementing"`
	Reviewing    int `json:"reviewing"`
	Testing      int `json:"testing"`
	NeedsPR      int `json:"needs_pr"`
	Failed       int `json:"failed"`
	Cancelled    int `json:"cancelled"`
	Rejected     int `json:"rejected"`
	PRCreated    int `json:"pr_created"`
	Merged       int `json:"merged"`
}

type statusOutput struct {
	Running   bool            `json:"running"`
	PID       string          `json:"pid"`
	JobCounts statusJobCounts `json:"job_counts"`
}

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
	jobCounts := statusJobCounts{
		Queued:       counts["queued"],
		Planning:     counts["planning"],
		Implementing: counts["implementing"],
		Reviewing:    counts["reviewing"],
		Testing:      counts["testing"],
		NeedsPR:      counts["ready"],
		Failed:       counts["failed"],
		Cancelled:    counts["cancelled"],
		Rejected:     counts["rejected"],
		PRCreated:    prCreated,
		Merged:       merged,
	}

	if jsonOut {
		printJSON(statusOutput{
			Running:   running,
			PID:       pidStr,
			JobCounts: jobCounts,
		})
		return nil
	}

	if running {
		fmt.Printf("Daemon: running (PID %s)\n", pidStr)
	} else {
		fmt.Println("Daemon: stopped")
	}
	active := counts["planning"] + counts["implementing"] + counts["reviewing"] + counts["testing"] + counts["rebasing"] + counts["resolving_conflicts"]

	fmt.Printf("Jobs: queued=%d active=%d planning=%d implementing=%d reviewing=%d testing=%d rebasing=%d resolving=%d needs_pr=%d failed=%d cancelled=%d pr_created=%d merged=%d rejected=%d\n",
		counts["queued"], active, counts["planning"], counts["implementing"], counts["reviewing"],
		counts["testing"], counts["rebasing"], counts["resolving_conflicts"],
		counts["ready"], counts["failed"], counts["cancelled"], prCreated, merged, counts["rejected"])
	return nil
}
