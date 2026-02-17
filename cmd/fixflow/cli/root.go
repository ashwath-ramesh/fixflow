package cli

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"fixflow/internal/config"
	"fixflow/internal/db"

	"github.com/spf13/cobra"
)

var (
	cfgPath string
	verbose bool
	jsonOut bool
)

var rootCmd = &cobra.Command{
	Use:   "ff",
	Short: "FixFlow — autonomous issue-to-code daemon",
	Long:  "FixFlow processes GitLab/GitHub issues through an LLM pipeline: plan → review → implement → test → human approval.",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		level := slog.LevelInfo
		if verbose {
			level = slog.LevelDebug
		}
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgPath, "config", "c", "fixflow.toml", "config file path")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable debug logging")
	rootCmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "output JSON")
}

func Execute() error {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return err
	}
	return nil
}

func loadConfig() (*config.Config, error) {
	return config.Load(cfgPath)
}

func openStore(cfg *config.Config) (*db.Store, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}
	// Clean up orphaned WAL sidecar files if the main DB was deleted.
	if _, err := os.Stat(cfg.DBPath); os.IsNotExist(err) {
		_ = os.Remove(cfg.DBPath + "-shm")
		_ = os.Remove(cfg.DBPath + "-wal")
	}
	return db.Open(cfg.DBPath)
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
