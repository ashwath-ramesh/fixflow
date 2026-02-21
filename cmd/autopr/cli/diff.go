package cli

import (
	"fmt"
	"os"
	"strings"

	"autopr/internal/git"

	"github.com/spf13/cobra"
)

var (
	diffStat    bool
	diffFiles   bool
	diffNoColor bool
)

var diffCmd = &cobra.Command{
	Use:   "diff <job-id>",
	Short: "Show the git diff for a job's changes against the base branch",
	Args:  cobra.ExactArgs(1),
	RunE:  runDiff,
}

func init() {
	diffCmd.Flags().BoolVar(&diffStat, "stat", false, "show diffstat summary only")
	diffCmd.Flags().BoolVar(&diffFiles, "files", false, "list changed file paths only")
	diffCmd.Flags().BoolVar(&diffNoColor, "no-color", false, "disable ANSI colors")
	rootCmd.AddCommand(diffCmd)
}

func runDiff(cmd *cobra.Command, args []string) error {
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

	if job.State == "queued" {
		return fmt.Errorf("job has not started yet")
	}
	if job.WorktreePath == "" {
		return fmt.Errorf("no worktree available (job may have been cleaned up)")
	}
	if _, err := os.Stat(job.WorktreePath); os.IsNotExist(err) {
		return fmt.Errorf("worktree directory not found (run `ap cleanup` removed it?)")
	}

	baseBranch := "main"
	if p, ok := cfg.ProjectByName(job.ProjectName); ok && p.BaseBranch != "" {
		baseBranch = p.BaseBranch
	}

	if diffFiles && diffStat {
		return fmt.Errorf("--files cannot be combined with --stat")
	}

	if diffFiles {
		return runDiffFiles(cmd, job.WorktreePath, baseBranch, jobID)
	}

	if diffStat {
		return runDiffStat(cmd, job.WorktreePath, baseBranch, jobID)
	}

	diffText, err := git.DiffAgainstBase(cmd.Context(), job.WorktreePath, baseBranch)
	if err != nil {
		return err
	}

	if diffText == "" {
		fmt.Println("(no changes)")
		return nil
	}

	if jsonOut {
		printJSON(map[string]any{
			"job_id": jobID,
			"diff":   diffText,
		})
		return nil
	}

	if diffNoColor {
		fmt.Print(diffText)
		return nil
	}

	// Print with ANSI colors.
	for line := range strings.SplitSeq(diffText, "\n") {
		fmt.Println(colorDiffLine(line))
	}
	return nil
}

func runDiffFiles(cmd *cobra.Command, worktreePath, baseBranch, jobID string) error {
	filesText, err := git.DiffFilesAgainstBase(cmd.Context(), worktreePath, baseBranch)
	if err != nil {
		return err
	}

	if filesText == "" {
		fmt.Println("(no changes)")
		return nil
	}

	if jsonOut {
		lines := strings.Split(strings.TrimSuffix(filesText, "\n"), "\n")
		printJSON(map[string]any{
			"job_id": jobID,
			"files":  lines,
		})
		return nil
	}

	fmt.Print(filesText)
	return nil
}

func runDiffStat(cmd *cobra.Command, worktreePath, baseBranch, jobID string) error {
	stat, err := git.DiffStatAgainstBase(cmd.Context(), worktreePath, baseBranch)
	if err != nil {
		return err
	}

	if stat == "" {
		fmt.Println("(no changes)")
		return nil
	}

	if jsonOut {
		printJSON(map[string]any{
			"job_id": jobID,
			"stat":   stat,
		})
		return nil
	}

	fmt.Print(stat)
	return nil
}

// colorDiffLine applies ANSI color codes to a diff line for CLI output.
func colorDiffLine(line string) string {
	const (
		reset = "\033[0m"
		red   = "\033[31m"
		green = "\033[32m"
		cyan  = "\033[36m"
		bold  = "\033[1m"
	)
	switch {
	case strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- "):
		return bold + line + reset
	case strings.HasPrefix(line, "+"):
		return green + line + reset
	case strings.HasPrefix(line, "-"):
		return red + line + reset
	case strings.HasPrefix(line, "@@"):
		return cyan + line + reset
	case strings.HasPrefix(line, "diff --git"):
		return bold + line + reset
	default:
		return line
	}
}
