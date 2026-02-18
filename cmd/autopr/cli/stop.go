package cli

import (
	"fmt"
	"os"
	"syscall"
	"time"

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

	// Wait for process to exit.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !daemon.ProcessAlive(pid) {
			fmt.Println("Daemon stopped.")
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Force kill.
	_ = proc.Signal(syscall.SIGKILL)
	daemon.RemovePID(cfg.Daemon.PIDFile)
	fmt.Println("Daemon killed (did not stop gracefully).")
	return nil
}
