package cli

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"autopr/internal/daemon"

	"github.com/spf13/cobra"
)

var foreground bool

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
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	// Configure logging.
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

	// Check if already running.
	if daemon.IsRunning(cfg.Daemon.PIDFile) {
		return fmt.Errorf("daemon is already running (see %s)", cfg.Daemon.PIDFile)
	}

	fmt.Println("Starting autopr daemon...")
	return daemon.Run(cfg, foreground)
}
