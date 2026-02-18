package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"autopr/internal/update"
)

var startUpdateRefreshTimeout = 3 * time.Second

type startVersionChecker interface {
	ReadCache() (update.VersionCheckCache, error)
	IsCacheFresh(update.VersionCheckCache, time.Duration) bool
	RefreshCache(context.Context) (update.VersionCheckCache, error)
}

func maybePrintUpgradeNotice(currentVersion string, out io.Writer, checker startVersionChecker) {
	cache, err := checker.ReadCache()
	hasCache := err == nil
	if hasCache {
		res := update.Compare(currentVersion, cache.LatestTag)
		if res.UpdateAvailable && res.Comparable {
			fmt.Fprintf(out, "A newer version of ap is available (%s, current: %s).\n", res.LatestVersion, res.CurrentVersion)
			fmt.Fprintln(out, "Run `ap upgrade` to update.")
		}
	}

	if hasCache && checker.IsCacheFresh(cache, update.DefaultCheckTTL) {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), startUpdateRefreshTimeout)
		defer cancel()
		_, _ = checker.RefreshCache(ctx)
	}()
}
