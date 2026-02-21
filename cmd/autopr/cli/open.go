package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"autopr/internal/config"
	"autopr/internal/db"

	"github.com/spf13/cobra"
)

var (
	openTargetEditor bool
	openTargetIssue  bool
	openTargetPR     bool
)

type openOutput struct {
	Action string `json:"action"`
	JobID  string `json:"job_id"`
	Target string `json:"target"`
	Path   string `json:"path,omitempty"`
	URL    string `json:"url,omitempty"`
}

var openCmd = &cobra.Command{
	Use:   "open <job-id>",
	Short: "Open job context (editor, issue, PR)",
	Args:  cobra.ExactArgs(1),
	RunE:  runOpen,
}

func init() {
	openCmd.Flags().BoolVar(&openTargetEditor, "editor", false, "open job worktree in editor")
	openCmd.Flags().BoolVar(&openTargetIssue, "issue", false, "open job issue URL in browser")
	openCmd.Flags().BoolVar(&openTargetPR, "pr", false, "open job PR/MR URL in browser")
	rootCmd.AddCommand(openCmd)
}

func runOpen(cmd *cobra.Command, args []string) error {
	target, err := resolveOpenTarget()
	if err != nil {
		return err
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

	jobID, err := resolveJob(store, args[0])
	if err != nil {
		return err
	}
	job, err := store.GetJob(cmd.Context(), jobID)
	if err != nil {
		return err
	}

	out := openOutput{
		Action: "open",
		JobID:  jobID,
		Target: target,
	}

	switch target {
	case "editor":
		worktreePath, err := resolveOpenWorktree(cfg, job)
		if err != nil {
			return err
		}
		if err := runOpenInEditor(worktreePath); err != nil {
			return fmt.Errorf("open editor: %w", err)
		}
		out.Path = worktreePath
	case "issue":
		issueURL, err := resolveOpenIssueURL(cmd.Context(), store, job.ID, job.AutoPRIssueID)
		if err != nil {
			return err
		}
		if err := runOpenInBrowser(issueURL); err != nil {
			return fmt.Errorf("open issue URL: %w", err)
		}
		out.URL = issueURL
	case "pr":
		prURL := strings.TrimSpace(job.PRURL)
		if prURL == "" {
			return fmt.Errorf("job %s has no PR URL", jobID)
		}
		if err := runOpenInBrowser(prURL); err != nil {
			return fmt.Errorf("open PR URL: %w", err)
		}
		out.URL = prURL
	default:
		return fmt.Errorf("unknown open target %q", target)
	}

	if jsonOut {
		printJSON(out)
	}
	return nil
}

func resolveOpenIssueURL(ctx context.Context, store *db.Store, jobID, issueID string) (string, error) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return "", fmt.Errorf("job %s has no issue URL", jobID)
	}

	issue, err := store.GetIssueByAPID(ctx, issueID)
	if err != nil {
		return "", fmt.Errorf("load issue for job %s: %w", jobID, err)
	}

	issueURL := strings.TrimSpace(issue.URL)
	if issueURL == "" {
		return "", fmt.Errorf("job %s has no issue URL", jobID)
	}

	return issueURL, nil
}

func resolveOpenTarget() (string, error) {
	selected := 0
	target := "editor"
	if openTargetEditor {
		selected++
	}
	if openTargetIssue {
		selected++
		target = "issue"
	}
	if openTargetPR {
		selected++
		target = "pr"
	}

	if selected > 1 {
		return "", fmt.Errorf("at most one of --editor, --issue, --pr may be specified")
	}

	return target, nil
}

func resolveOpenWorktree(cfg *config.Config, job db.Job) (string, error) {
	worktreePath := strings.TrimSpace(job.WorktreePath)
	if worktreePath == "" && strings.TrimSpace(cfg.ReposRoot) != "" {
		worktreePath = filepath.Join(strings.TrimSpace(cfg.ReposRoot), "worktrees", job.ID)
	}
	if worktreePath == "" {
		return "", fmt.Errorf("job %s has no worktree", job.ID)
	}

	info, err := os.Stat(worktreePath)
	if err != nil || !info.IsDir() {
		if err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("job %s has no worktree", job.ID)
			}
			return "", fmt.Errorf("job %s has no worktree: %w", job.ID, err)
		}
		return "", fmt.Errorf("job %s has no worktree", job.ID)
	}
	return worktreePath, nil
}

func openInEditor(path string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		if _, err := exec.LookPath("code"); err == nil {
			editor = "code"
		} else {
			editor = "vim"
		}
	}

	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Start()
}

func openInBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Start()
}

var runOpenInEditor = openInEditor
var runOpenInBrowser = openInBrowser
