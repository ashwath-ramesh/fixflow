package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"autopr/internal/update"
)

var startUpdateRefreshTimeout = 400 * time.Millisecond

type startVersionChecker interface {
	ReadCache() (update.VersionCheckCache, error)
	IsCacheFresh(update.VersionCheckCache, time.Duration) bool
	RefreshCache(context.Context) (update.VersionCheckCache, error)
	MarkCheckAttempt(string) error
}

func maybePrintUpgradeNotice(currentVersion string, out io.Writer, checker startVersionChecker) {
	cache, err := checker.ReadCache()
	hasCache := err == nil
	printed := false
	fallbackTag := update.Compare(currentVersion, currentVersion).CurrentVersion
	if hasCache {
		fallbackTag = cache.LatestTag
		printed = printUpgradeNotice(currentVersion, cache.LatestTag, out)
	}

	if hasCache && checker.IsCacheFresh(cache, update.DefaultCheckTTL) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), startUpdateRefreshTimeout)
	defer cancel()
	refreshed, err := checker.RefreshCache(ctx)
	if err != nil {
		_ = checker.MarkCheckAttempt(fallbackTag)
		return
	}
	if !printed {
		_ = printUpgradeNotice(currentVersion, refreshed.LatestTag, out)
	}
}

func printUpgradeNotice(currentVersion, latestTag string, out io.Writer) bool {
	res := update.Compare(currentVersion, latestTag)
	if !res.UpdateAvailable || !res.Comparable {
		return false
	}
	fmt.Fprintf(out, "A newer version of ap is available (%s, current: %s).\n", res.LatestVersion, res.CurrentVersion)
	fmt.Fprintln(out, "Run `ap upgrade` to update.")
	return true
}
