package cli

import (
	"fmt"
	"strings"

	"autopr/internal/db"

	"github.com/spf13/cobra"
)

var (
	listProject string
	listState   string
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List jobs with filters",
	RunE:  runList,
}

func init() {
	listCmd.Flags().StringVar(&listProject, "project", "", "filter by project name")
	listCmd.Flags().StringVar(&listState, "state", "all", "filter by state")
	rootCmd.AddCommand(listCmd)
}

func runList(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	store, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	jobs, err := store.ListJobs(cmd.Context(), listProject, listState)
	if err != nil {
		return err
	}

	if jsonOut {
		printJSON(jobs)
		return nil
	}

	if len(jobs) == 0 {
		fmt.Println("No jobs found.")
		return nil
	}

	fmt.Printf("%-10s %-20s %-13s %-13s %-5s %-55s %s\n", "JOB", "STATE", "PROJECT", "SOURCE", "RETRY", "ISSUE", "UPDATED")
	fmt.Println(strings.Repeat("-", 136))
	for _, j := range jobs {
		source := ""
		if j.IssueSource != "" && j.SourceIssueID != "" {
			source = fmt.Sprintf("%s #%s", capitalize(j.IssueSource), j.SourceIssueID)
		}
		title := truncate(j.IssueTitle, 55)

		fmt.Printf("%-10s %-20s %-13s %-13s %-5s %-55s %s\n",
			db.ShortID(j.ID), db.DisplayState(j.State, j.PRMergedAt, j.PRClosedAt), truncate(j.ProjectName, 12), source,
			fmt.Sprintf("%d/%d", j.Iteration, j.MaxIterations),
			title, j.UpdatedAt)
	}
	return nil
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
