package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"autopr/internal/config"
	launchdservice "autopr/internal/service"

	"github.com/spf13/cobra"
)

type serviceManager interface {
	Install(cfg *config.Config, resolvedConfigPath string) error
	Uninstall() error
	Status(cfg *config.Config) (launchdservice.ServiceStatus, error)
	PlistPath() (string, error)
}

type launchdManager struct{}

func (launchdManager) Install(cfg *config.Config, resolvedConfigPath string) error {
	return launchdservice.Install(cfg, resolvedConfigPath)
}

func (launchdManager) Uninstall() error {
	return launchdservice.Uninstall()
}

func (launchdManager) Status(cfg *config.Config) (launchdservice.ServiceStatus, error) {
	return launchdservice.Status(cfg)
}

func (launchdManager) PlistPath() (string, error) {
	return launchdservice.PlistPath()
}

var (
	servicePlatform                         = runtime.GOOS
	serviceConfigLoader                     = loadConfig
	serviceConfigPathResolve                = resolveConfigPath
	serviceMgr               serviceManager = launchdManager{}
)

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage daemon persistence service (macOS launchd)",
}

var serviceInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install and start launchd service",
	RunE:  runServiceInstall,
}

var serviceUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Uninstall launchd service",
	RunE:  runServiceUninstall,
}

var serviceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show launchd service status",
	RunE:  runServiceStatus,
}

func init() {
	serviceCmd.AddCommand(serviceInstallCmd)
	serviceCmd.AddCommand(serviceUninstallCmd)
	serviceCmd.AddCommand(serviceStatusCmd)
	rootCmd.AddCommand(serviceCmd)
}

func runServiceInstall(cmd *cobra.Command, args []string) error {
	return runServiceInstallWith(servicePlatform, serviceConfigLoader, serviceConfigPathResolve, serviceMgr, os.Stdout)
}

func runServiceInstallWith(
	goos string,
	loadCfg func() (*config.Config, error),
	resolveCfgPath func() (string, error),
	manager serviceManager,
	out io.Writer,
) error {
	if goos != "darwin" {
		return fmt.Errorf("service commands are currently supported only on macOS")
	}

	cfg, err := loadCfg()
	if err != nil {
		return err
	}

	cfgPath, err := resolveCfgPath()
	if err != nil {
		return err
	}

	if err := installServiceForConfig(goos, manager, cfg, cfgPath); err != nil {
		return err
	}

	plistPath, err := manager.PlistPath()
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Service installed: %s\n", launchdservice.LaunchdLabel)
	fmt.Fprintf(out, "Plist: %s\n", plistPath)
	return nil
}

func installCurrentService(cfg *config.Config, cfgPath string) error {
	return installServiceForConfig(servicePlatform, serviceMgr, cfg, cfgPath)
}

func installServiceForConfig(goos string, manager serviceManager, cfg *config.Config, cfgPath string) error {
	if goos != "darwin" {
		return fmt.Errorf("service commands are currently supported only on macOS")
	}
	absCfgPath, err := filepath.Abs(cfgPath)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	if err := manager.Install(cfg, absCfgPath); err != nil {
		return err
	}
	return nil
}

func runServiceUninstall(cmd *cobra.Command, args []string) error {
	return runServiceUninstallWith(servicePlatform, serviceMgr, os.Stdout)
}

func runServiceUninstallWith(goos string, manager serviceManager, out io.Writer) error {
	if goos != "darwin" {
		return fmt.Errorf("service commands are currently supported only on macOS")
	}
	if err := manager.Uninstall(); err != nil {
		return err
	}
	fmt.Fprintln(out, "Service uninstalled.")
	return nil
}

func runServiceStatus(cmd *cobra.Command, args []string) error {
	return runServiceStatusWith(servicePlatform, serviceConfigLoader, serviceMgr, os.Stdout, jsonOut)
}

func runServiceStatusWith(
	goos string,
	loadCfg func() (*config.Config, error),
	manager serviceManager,
	out io.Writer,
	asJSON bool,
) error {
	if goos != "darwin" {
		return fmt.Errorf("service commands are currently supported only on macOS")
	}

	cfg, err := loadCfg()
	if err != nil {
		return err
	}

	status, err := manager.Status(cfg)
	if err != nil {
		return err
	}
	if status.Label == "" {
		status.Label = launchdservice.LaunchdLabel
	}

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(status)
	}

	fmt.Fprintf(out, "Label: %s\n", status.Label)
	fmt.Fprintf(out, "Plist: %s\n", status.PlistPath)
	fmt.Fprintf(out, "Installed: %t\n", status.Installed)
	fmt.Fprintf(out, "Loaded: %t\n", status.Loaded)
	fmt.Fprintf(out, "Running: %t\n", status.Running)
	if status.PID > 0 {
		fmt.Fprintf(out, "PID: %d\n", status.PID)
	}
	return nil
}
