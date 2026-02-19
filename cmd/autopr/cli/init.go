package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"autopr/internal/config"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Set up AutoPR config and credentials",
	Long:  "Interactive wizard that creates ~/.config/autopr/ with config.toml and credentials.toml.",
	RunE:  runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	// If --config flag was explicitly set, do a project-local init (backward compat).
	if cfgPath != "" {
		return runLocalInit()
	}
	return runGlobalInit()
}

// runGlobalInit is the interactive wizard for ~/.config/autopr/.
func runGlobalInit() error {
	reader := bufio.NewReader(os.Stdin)

	configDir, err := config.ConfigDir()
	if err != nil {
		return err
	}
	cfgFile, err := config.GlobalConfigPath()
	if err != nil {
		return err
	}
	credsFile, err := config.CredentialsPath()
	if err != nil {
		return err
	}

	// 1. Detect existing setup.
	if _, err := os.Stat(credsFile); err == nil {
		fmt.Printf("Existing credentials found at %s\n", credsFile)
		fmt.Print("Re-run setup? [y/N]: ")
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// 2. Create config directory.
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// 3. Prompt for GitHub token (masked input).
	fmt.Print("GitHub token (input is hidden): ")
	tokenBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println() // newline after hidden input
	if err != nil {
		return fmt.Errorf("read token: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))

	// 4. If empty, check env var and offer to save it.
	if token == "" {
		if envToken := os.Getenv("GITHUB_TOKEN"); envToken != "" {
			fmt.Print("GITHUB_TOKEN env var detected. Save it to credentials.toml? [Y/n]: ")
			answer, _ := reader.ReadString('\n')
			answer = strings.TrimSpace(strings.ToLower(answer))
			if answer == "" || answer == "y" || answer == "yes" {
				token = envToken
			}
		}
	}

	// 5. Write credentials.toml (0600), preserving existing fields.
	creds, err := config.LoadCredentials()
	if err != nil {
		creds = &config.Credentials{}
	}
	if token != "" {
		creds.GitHubToken = token
	}
	if err := config.SaveCredentials(creds); err != nil {
		return err
	}
	fmt.Printf("Credentials saved: %s\n", credsFile)

	// 6. Create config.toml with defaults (if not exists).
	if _, err := os.Stat(cfgFile); os.IsNotExist(err) {
		if err := os.WriteFile(cfgFile, []byte(configTemplate), 0o644); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		fmt.Printf("Config created: %s\n", cfgFile)
	} else {
		fmt.Printf("Config already exists: %s\n", cfgFile)
	}

	// 7. Initialize DB via LoadMinimal.
	cfg, err := config.LoadMinimal(cfgFile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return fmt.Errorf("create db directory: %w", err)
	}
	store, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	fmt.Printf("Database initialized: %s\n", cfg.DBPath)
	serviceInstalled, err := maybeInstallServiceFromInit(
		runtime.GOOS,
		reader,
		os.Stdout,
		cfg,
		cfgFile,
		installCurrentService,
	)
	if err != nil {
		return fmt.Errorf("install service: %w", err)
	}

	fmt.Println("\nNext steps:")
	fmt.Printf("  1. Edit your config to add projects: ap config\n")
	if serviceInstalled {
		fmt.Printf("  2. Check service status:             ap service status\n")
		fmt.Printf("  3. Start the TUI:                    ap tui\n")
	} else {
		fmt.Printf("  2. Start the daemon:                 ap start\n")
		fmt.Printf("  3. Start the TUI:                    ap tui\n")
	}
	return nil
}

// runLocalInit creates a project-local config file (backward compat with --config flag).
func runLocalInit() error {
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		if err := os.WriteFile(cfgPath, []byte(configTemplate), 0o644); err != nil {
			return fmt.Errorf("write config template: %w", err)
		}
		fmt.Printf("Created config template: %s\n", cfgPath)
	} else {
		fmt.Printf("Config already exists: %s\n", cfgPath)
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

	fmt.Printf("Database initialized: %s\n", cfg.DBPath)
	fmt.Println("Edit the config file to configure your projects, then run: ap start")
	return nil
}

func maybeInstallServiceFromInit(
	goos string,
	reader *bufio.Reader,
	out io.Writer,
	cfg *config.Config,
	cfgPath string,
	installFn func(*config.Config, string) error,
) (bool, error) {
	if goos != "darwin" {
		return false, nil
	}

	fmt.Fprint(out, "Install as system service (auto-start on login)? [y/N]: ")
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		return false, nil
	}

	if err := installFn(cfg, cfgPath); err != nil {
		return false, err
	}
	fmt.Fprintf(out, "Service installed: %s\n", "io.autopr.daemon")
	return true, nil
}

const configTemplate = `# AutoPR configuration
# See: https://github.com/ashwath-ramesh/autopr
#
# Tokens: store in ~/.config/autopr/credentials.toml or set env vars
# (GITHUB_TOKEN, GITLAB_TOKEN, SENTRY_TOKEN, AUTOPR_WEBHOOK_SECRET)
#
# Data files (DB, repos) default to ~/.local/share/autopr/
# State files (logs, PID) default to ~/.local/state/autopr/
# Override with XDG_DATA_HOME / XDG_STATE_HOME or set paths explicitly below.

log_level = "info"              # debug|info|warn|error

[daemon]
webhook_port = 9847
webhook_secret = ""             # override via AUTOPR_WEBHOOK_SECRET env var
max_workers = 3
max_iterations = 3              # implement<->review loop default
sync_interval = "5m"            # GitHub/Sentry poll interval
auto_pr = false                 # set true to auto-create PRs after tests pass

# [sentry]
# base_url = "https://sentry.io"  # uncomment for self-hosted Sentry

[llm]
provider = "codex"              # codex|claude

[notifications]
# webhook_url = "https://example.com/hook"                     # generic JSON webhook
# slack_webhook = "https://hooks.slack.com/services/..."       # Slack incoming webhook
# desktop = true                                                # macOS desktop notifications
# triggers = ["needs_pr", "failed", "pr_created", "pr_merged"]
# Set triggers = [] to disable all notifications.

# Issue gating: by default, only issues labeled "autopr" (GitHub/GitLab) are
# processed, and issues labeled "autopr-skip" are skipped. Exclusion has precedence.
# Set include_labels = [] and exclude_labels = [] to disable label gating entirely.
# Sentry uses assigned_team = "autopr" and set assigned_team = "" to opt out.

# --- GitHub example ---
[[projects]]
name = "my-project"
repo_url = "git@github.com:org/repo.git"
test_cmd = "go test ./..."
base_branch = "main"

  [projects.github]
  owner = "org"
  repo = "repo"
  # include_labels defaults to ["autopr"] -- label issues "autopr" to process them
  # exclude_labels defaults to ["autopr-skip"] -- issues with this label are skipped
  # include_labels = ["bug"]    # custom: only process issues labeled "bug"
  # exclude_labels = ["blocked"] # custom: skip issues labeled "blocked"
  # include_labels = []          # opt-out: process ALL open issues
  # exclude_labels = []          # opt-out: disable default skip label

  # [projects.sentry]
  # org = "my-org"
  # project = "my-project"
  # assigned_team defaults to "autopr" -- assign issues to #autopr team to process them
  # assigned_team = "my-team"   # custom: only issues assigned to #my-team
  # assigned_team = ""           # opt-out: process ALL unresolved issues

  # Override default LLM prompts with custom markdown files:
  # [projects.prompts]
  # plan = "/path/to/plan.md"
  # implement = "/path/to/implement.md"
  # code_review = "/path/to/code_review.md"

# --- GitLab example ---
# [[projects]]
# name = "my-gitlab-project"
# repo_url = "git@gitlab.com:org/repo.git"
# test_cmd = "make test"
# base_branch = "main"
#
#   [projects.gitlab]
#   base_url = "https://gitlab.com"   # change for self-hosted GitLab
#   project_id = "12345"
#   # include_labels defaults to ["autopr"] -- label issues "autopr" to process them
#   # exclude_labels defaults to ["autopr-skip"] -- issues with this label are skipped
#   # exclude_labels = ["blocked"]    # custom: skip issues labeled "blocked"
#   # include_labels = []             # opt-out: process ALL open issues
#   # exclude_labels = []             # opt-out: disable default skip label
`
