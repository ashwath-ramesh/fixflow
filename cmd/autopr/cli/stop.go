package cli

import (
	"fmt"
	"os"
	"syscall"

	"autopr/internal/daemon"

	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the autopr daemon",
	RunE:  runStop,
}

func init() {
	rootCmd.AddCommand(stopCmd)
}

func runStop(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	pid, err := daemon.ReadPID(cfg.Daemon.PIDFile)
	if err != nil {
		return fmt.Errorf("daemon not running (no PID file)")
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}

	// Send SIGTERM for graceful shutdown.
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		// Process might already be dead.
		daemon.RemovePID(cfg.Daemon.PIDFile)
		return fmt.Errorf("signal process %d: %w", pid, err)
	}

	fmt.Printf("Stopping daemon (pid %d)...\n", pid)
	return nil
}
