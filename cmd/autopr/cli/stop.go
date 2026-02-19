package cli

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"

	"autopr/internal/config"
	"autopr/internal/daemon"
	launchdservice "autopr/internal/service"

	"github.com/spf13/cobra"
)

var (
	stopPlatform      = runtime.GOOS
	stopServiceStatus = launchdservice.Status
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

	pid, err := resolveStopPID(cfg)
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
			printStopServiceKeepAliveNote(cfg)
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Force kill.
	_ = proc.Signal(syscall.SIGKILL)
	daemon.RemovePID(cfg.Daemon.PIDFile)
	fmt.Println("Daemon killed (did not stop gracefully).")
	printStopServiceKeepAliveNote(cfg)
	return nil
}

func resolveStopPID(cfg *config.Config) (int, error) {
	pid, err := daemon.ReadPID(cfg.Daemon.PIDFile)
	if err == nil {
		return pid, nil
	}

	if stopPlatform != "darwin" {
		return 0, err
	}
	status, statusErr := stopServiceStatus(cfg)
	if statusErr != nil {
		return 0, err
	}
	if status.Running && status.PID > 0 {
		return status.PID, nil
	}
	return 0, err
}

func printStopServiceKeepAliveNote(cfg *config.Config) {
	if stopPlatform != "darwin" {
		return
	}
	status, err := stopServiceStatus(cfg)
	if err != nil {
		return
	}
	if status.Installed {
		fmt.Println("Note: launchd KeepAlive may restart the daemon. Run `ap service uninstall` to disable auto-restart.")
	}
}
