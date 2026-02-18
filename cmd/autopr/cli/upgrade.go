package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"autopr/internal/update"

	"github.com/spf13/cobra"
)

var upgradeCheckOnly bool

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade ap to the latest release",
	RunE:  runUpgrade,
}

type upgradeService interface {
	Check(context.Context, string) (update.CheckResult, error)
	Upgrade(context.Context, string) (update.UpgradeResult, error)
}

func init() {
	upgradeCmd.Flags().BoolVar(&upgradeCheckOnly, "check", false, "Check for updates without installing")
	rootCmd.AddCommand(upgradeCmd)
}

func runUpgrade(cmd *cobra.Command, args []string) error {
	return runUpgradeWith(cmd.Context(), os.Stdout, update.NewManager(version), version, upgradeCheckOnly)
}

func runUpgradeWith(ctx context.Context, out io.Writer, svc upgradeService, currentVersion string, checkOnly bool) error {
	if checkOnly {
		res, err := svc.Check(ctx, currentVersion)
		if err != nil {
			return err
		}
		if res.UpdateAvailable {
			fmt.Fprintf(out, "update available: %s (current: %s)\n", res.LatestVersion, res.CurrentVersion)
			return nil
		}
		fmt.Fprintf(out, "already up to date (%s)\n", nonEmptyVersion(res.CurrentVersion, currentVersion))
		return nil
	}

	res, err := svc.Upgrade(ctx, currentVersion)
	if err != nil {
		return err
	}
	if !res.UpdateAvailable {
		fmt.Fprintf(out, "already up to date (%s)\n", nonEmptyVersion(res.CurrentVersion, currentVersion))
		return nil
	}
	if res.Upgraded {
		fmt.Fprintf(out, "upgraded ap to %s\n", res.LatestVersion)
		return nil
	}
	fmt.Fprintf(out, "already up to date (%s)\n", nonEmptyVersion(res.CurrentVersion, currentVersion))
	return nil
}

func nonEmptyVersion(preferred, fallback string) string {
	if preferred != "" {
		return preferred
	}
	if fallback != "" {
		return fallback
	}
	return "unknown"
}
