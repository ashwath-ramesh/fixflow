package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var (
	issuesProject    string
	issuesEligible   bool
	issuesIneligible bool
)

var issuesCmd = &cobra.Command{
	Use:   "issues",
	Short: "List synced issues and eligibility",
	RunE:  runIssues,
}

func init() {
	issuesCmd.Flags().StringVar(&issuesProject, "project", "", "filter by project name")
	issuesCmd.Flags().BoolVar(&issuesEligible, "eligible", false, "show only eligible issues")
	issuesCmd.Flags().BoolVar(&issuesIneligible, "ineligible", false, "show only ineligible issues")
	rootCmd.AddCommand(issuesCmd)
}

func runIssues(cmd *cobra.Command, args []string) error {
	if issuesEligible && issuesIneligible {
		return fmt.Errorf("--eligible and --ineligible are mutually exclusive")
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

	var eligibleFilter *bool
	if issuesEligible {
		v := true
		eligibleFilter = &v
	} else if issuesIneligible {
		v := false
		eligibleFilter = &v
	}

	issues, err := store.ListIssues(cmd.Context(), issuesProject, eligibleFilter)
	if err != nil {
		return err
	}

	if jsonOut {
		printJSON(issues)
		return nil
	}

	if len(issues) == 0 {
		fmt.Println("No synced issues found.")
		return nil
	}

	fmt.Printf("%-8s %-13s %-10s %-7s %-8s %-40s %s\n", "ISSUE", "PROJECT", "SOURCE", "STATE", "ELIGIBLE", "SKIP REASON", "SYNCED")
	fmt.Println(strings.Repeat("-", 136))
	for _, issue := range issues {
		issueID := issue.SourceIssueID
		if issueID != "" && !strings.HasPrefix(issueID, "#") {
			issueID = "#" + issueID
		}
		if issueID == "" {
			issueID = "-"
		}

		eligibleText := "no"
		if issue.Eligible {
			eligibleText = "yes"
		}

		skipReason := strings.TrimSpace(issue.SkipReason)
		if skipReason == "" {
			skipReason = "-"
		}

		fmt.Printf("%-8s %-13s %-10s %-7s %-8s %-40s %s\n",
			issueID,
			truncate(issue.ProjectName, 12),
			capitalize(issue.Source),
			issue.State,
			eligibleText,
			truncate(skipReason, 40),
			issue.SyncedAt,
		)
	}
	return nil
}
