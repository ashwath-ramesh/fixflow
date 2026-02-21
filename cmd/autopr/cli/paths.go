package cli

import (
	"fmt"

	"autopr/internal/config"

	"github.com/spf13/cobra"
)

var pathsCmd = &cobra.Command{
	Use:   "paths",
	Short: "Show where AutoPR stores its files",
	RunE:  runPaths,
}

func init() {
	rootCmd.AddCommand(pathsCmd)
}

func runPaths(cmd *cobra.Command, args []string) error {
	configDir, _ := config.ConfigDir()
	dataDir, _ := config.DataDir()
	stateDir, _ := config.StateDir()

	if jsonOut {
		payload := map[string]string{
			"config": configDir,
			"data":   dataDir,
			"db":     "",
			"repos":  "",
			"log":    "",
		}

		// If a config is loadable, include resolved paths.
		if cfg, err := loadConfig(); err == nil {
			payload["db"] = cfg.DBPath
			payload["repos"] = cfg.ReposRoot
			payload["log"] = cfg.LogFile
		}

		printJSON(payload)
		return nil
	}

	fmt.Printf("Config:  %s\n", configDir)
	fmt.Printf("Data:    %s\n", dataDir)
	fmt.Printf("State:   %s\n", stateDir)
	fmt.Println()

	// If a config is loadable, show resolved paths.
	cfg, err := loadConfig()
	if err != nil {
		return nil // don't error — base dirs are still useful
	}

	fmt.Printf("DB:      %s\n", cfg.DBPath)
	fmt.Printf("Repos:   %s\n", cfg.ReposRoot)
	fmt.Printf("Log:     %s\n", cfg.LogFile)
	fmt.Printf("PID:     %s\n", cfg.Daemon.PIDFile)
	return nil
}
