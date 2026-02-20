package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"autopr/internal/config"
	"autopr/internal/daemon"
	"autopr/internal/update"

	"github.com/spf13/cobra"
)

var foreground bool

const skipUpdateNoticeEnv = "AUTOPR_SKIP_UPDATE_NOTICE"

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the autopr daemon",
	RunE:  runStart,
}

func init() {
	startCmd.Flags().BoolVarP(&foreground, "foreground", "f", false, "Run in foreground (don't daemonize)")
	rootCmd.AddCommand(startCmd)
}

func runStart(cmd *cobra.Command, args []string) error {
	return runStartWith(
		loadConfig,
		daemon.IsRunning,
		maybePrintUpgradeNotice,
		func(currentVersion string) startVersionChecker {
			return update.NewManager(currentVersion)
		},
		runForeground,
		runBackground,
	)
}

type noticePrinterFunc func(string, io.Writer, startVersionChecker)
type checkerFactoryFunc func(string) startVersionChecker
type daemonRunningFunc func(string) bool
type startRunnerFunc func(*config.Config) error

func runStartWith(
	loadConfigFn func() (*config.Config, error),
	isDaemonRunning daemonRunningFunc,
	noticePrinter noticePrinterFunc,
	checkerFactory checkerFactoryFunc,
	runForegroundFn startRunnerFunc,
	runBackgroundFn startRunnerFunc,
) error {
	cfg, err := loadConfigFn()
	if err != nil {
		return err
	}

	// Check if already running.
	if isDaemonRunning(cfg.Daemon.PIDFile) {
		return fmt.Errorf("daemon is already running (see %s)", cfg.Daemon.PIDFile)
	}

	if shouldCheckForUpdates() {
		noticePrinter(version, os.Stdout, checkerFactory(version))
	}

	if foreground {
		return runForegroundFn(cfg)
	}
	return runBackgroundFn(cfg)
}

// runForeground configures logging and runs the daemon in the current process.
func runForeground(cfg *config.Config) error {
	level := cfg.SlogLevel()
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFile != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0o755); err != nil {
			return fmt.Errorf("create log dir: %w", err)
		}
		f, err := os.OpenFile(cfg.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("open log file: %w", err)
		}
		defer f.Close()
		slog.SetDefault(slog.New(slog.NewJSONHandler(f, opts)))
	} else {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, opts)))
	}

	fmt.Println("Starting autopr daemon in foreground...")
	return daemon.Run(cfg, foreground)
}

// runBackground re-execs the current binary with --foreground as a detached
// child process, then waits briefly to verify it started successfully.
func runBackground(cfg *config.Config) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}

	// Build child args: start --foreground, plus --config if the user passed one.
	childArgs := []string{"start", "--foreground"}
	if cfgPath != "" {
		childArgs = append(childArgs, "--config", cfgPath)
	}

	// Ensure log directory exists and open the log file for the child.
	logPath := cfg.LogFile
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer logFile.Close()

	child := exec.Command(exe, childArgs...)
	child.Stdout = logFile
	child.Stderr = logFile
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	child.Env = childEnvWithSkippedUpdateNotice()

	if err := child.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}

	// Race child.Wait() against a grace period timer. We must reap the child
	// to detect early exits — a zombie still passes kill(pid, 0).
	waitCh := make(chan error, 1)
	go func() { waitCh <- child.Wait() }()

	select {
	case err := <-waitCh:
		// Child exited during the grace period — it crashed.
		if err != nil {
			return fmt.Errorf("daemon exited immediately (%v); check logs at %s", err, logPath)
		}
		return fmt.Errorf("daemon exited immediately; check logs at %s", logPath)
	case <-time.After(500 * time.Millisecond):
		// Still running after 500ms — detach and let it go.
	}

	fmt.Printf("Daemon started. Log: %s\n", logPath)
	return nil
}

func shouldCheckForUpdates() bool {
	return os.Getenv(skipUpdateNoticeEnv) != "1"
}

func childEnvWithSkippedUpdateNotice() []string {
	return append(os.Environ(), skipUpdateNoticeEnv+"=1")
}
