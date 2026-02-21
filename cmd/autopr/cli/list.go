package cli

import (
	"fmt"
	"strings"

	"autopr/internal/cost"
	"autopr/internal/db"

	"github.com/spf13/cobra"
)

var (
	listProject  string
	listState    string
	listSort     string
	listAsc      bool
	listDesc     bool
	listCost     bool
	listPage     int
	listPageSize int
	listAll      bool
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List jobs with filters",
	RunE:  runList,
}

func init() {
	listCmd.Flags().StringVar(&listProject, "project", "", "filter by project name")
	listCmd.Flags().StringVar(&listState, "state", "all", "filter by state")
	listCmd.Flags().StringVar(&listSort, "sort", "updated_at", "sort by field: updated_at, created_at, state, or project")
	listCmd.Flags().BoolVar(&listAsc, "asc", false, "sort in ascending order")
	listCmd.Flags().BoolVar(&listDesc, "desc", false, "sort in descending order (default)")
	listCmd.Flags().BoolVar(&listCost, "cost", false, "show estimated cost column")
	listCmd.Flags().IntVar(&listPage, "page", 1, "page number (1-based)")
	listCmd.Flags().IntVar(&listPageSize, "page-size", 20, "number of rows per page")
	listCmd.Flags().BoolVar(&listAll, "all", false, "disable pagination and show full output")
	rootCmd.AddCommand(listCmd)
}

func runList(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	sortBy, err := normalizeListSort(listSort)
	if err != nil {
		return err
	}
	state, err := normalizeListState(listState)
	if err != nil {
		return err
	}
	if listAsc && listDesc {
		return fmt.Errorf("--asc and --desc cannot be used together")
	}
	ascending := listAsc

	store, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	paginate := !listAll && (cmd.Flags().Changed("page") || cmd.Flags().Changed("page-size"))
	page := listPage
	pageSize := listPageSize

	if paginate {
		if page < 1 {
			return fmt.Errorf("invalid page value %d; expected >= 1", page)
		}
		if pageSize < 1 {
			return fmt.Errorf("invalid page-size value %d; expected >= 1", pageSize)
		}
	}

	var jobs []db.Job
	total := 0
	if paginate {
		var err error
		jobs, total, err = store.ListJobsPage(cmd.Context(), listProject, state, sortBy, ascending, page, pageSize)
		if err != nil {
			return err
		}
	} else {
		var err error
		jobs, err = store.ListJobs(cmd.Context(), listProject, state, sortBy, ascending)
		if err != nil {
			return err
		}
	}

	if jsonOut {
		if paginate {
			printJSON(struct {
				Jobs     []db.Job `json:"jobs"`
				Page     int      `json:"page"`
				PageSize int      `json:"page_size"`
				Total    int      `json:"total"`
			}{
				Jobs:     jobs,
				Page:     page,
				PageSize: pageSize,
				Total:    total,
			})
			return nil
		}
		printJSON(jobs)
		return nil
	}

	if paginate {
		pages := 0
		if total > 0 {
			pages = (total + pageSize - 1) / pageSize
		}
		fmt.Printf("Page %d/%d, total rows: %d\n", page, pages, total)
	}

	if len(jobs) == 0 && !paginate {
		fmt.Println("No jobs found. Run 'ap start' to begin processing issues.")
		return nil
	}

	// Optionally fetch cost data.
	var costMap map[string]db.TokenSummary
	if listCost && len(jobs) > 0 {
		ids := make([]string, len(jobs))
		for i, j := range jobs {
			ids[i] = j.ID
		}
		costMap, _ = store.AggregateTokensForJobs(cmd.Context(), ids)
	}

	if listCost {
		fmt.Printf("%-10s %-20s %-13s %-13s %-5s %-8s %-45s %s\n", "JOB", "STATE", "PROJECT", "SOURCE", "RETRY", "COST", "ISSUE", "UPDATED")
		fmt.Println(strings.Repeat("-", 136))
	} else {
		fmt.Printf("%-10s %-20s %-13s %-13s %-5s %-55s %s\n", "JOB", "STATE", "PROJECT", "SOURCE", "RETRY", "ISSUE", "UPDATED")
		fmt.Println(strings.Repeat("-", 136))
	}

	total = len(jobs)
	queued, active, failed, merged := 0, 0, 0, 0

	for _, j := range jobs {
		source := ""
		if j.IssueSource != "" && j.SourceIssueID != "" {
			source = fmt.Sprintf("%s #%s", capitalize(j.IssueSource), j.SourceIssueID)
		}

		if listCost {
			costStr := "-"
			if ts, ok := costMap[j.ID]; ok && ts.SessionCount > 0 {
				c := cost.Calculate(ts.Provider, ts.TotalInputTokens, ts.TotalOutputTokens)
				costStr = cost.FormatUSD(c)
			}
			title := truncate(j.IssueTitle, 45)
			fmt.Printf("%-10s %-20s %-13s %-13s %-5s %-8s %-45s %s\n",
				db.ShortID(j.ID), db.DisplayState(j.State, j.PRMergedAt, j.PRClosedAt), truncate(j.ProjectName, 12), source,
				fmt.Sprintf("%d/%d", j.Iteration, j.MaxIterations),
				costStr, title, j.UpdatedAt)
		} else {
			title := truncate(j.IssueTitle, 55)
			fmt.Printf("%-10s %-20s %-13s %-13s %-5s %-55s %s\n",
				db.ShortID(j.ID), db.DisplayState(j.State, j.PRMergedAt, j.PRClosedAt), truncate(j.ProjectName, 12), source,
				fmt.Sprintf("%d/%d", j.Iteration, j.MaxIterations),
				title, j.UpdatedAt)
		}

		if j.State == "queued" {
			queued++
		}
		if isActiveState(j.State) {
			active++
		}
		switch j.State {
		case "failed", "rejected", "cancelled":
			failed++
		}
		if j.State == "approved" && j.PRMergedAt != "" {
			merged++
		}
	}
	fmt.Printf("Total: %d jobs (%d queued, %d active, %d failed, %d merged)\n", total, queued, active, failed, merged)
	return nil
}

func normalizeListSort(sortBy string) (string, error) {
	switch sortBy {
	case "updated_at", "created_at", "state", "project":
		return sortBy, nil
	default:
		return "", fmt.Errorf("invalid --sort %q (expected one of: updated_at, created_at, state, project)", sortBy)
	}
}

func normalizeListState(state string) (string, error) {
	if state == "resolving" {
		return "resolving_conflicts", nil
	}

	switch state {
	case "all", "active", "merged", "queued", "planning", "implementing", "reviewing", "testing", "ready", "rebasing", "resolving_conflicts", "awaiting_checks", "approved", "rejected", "failed", "cancelled":
		return state, nil
	default:
		return "", fmt.Errorf("invalid --state %q (expected one of: all, active, merged, queued, planning, implementing, reviewing, testing, ready, rebasing, resolving, resolving_conflicts, awaiting_checks, approved, rejected, failed, cancelled)", state)
	}
}

func isActiveState(state string) bool {
	switch state {
	case "planning", "implementing", "reviewing", "testing", "rebasing", "resolving_conflicts", "awaiting_checks":
		return true
	default:
		return false
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
