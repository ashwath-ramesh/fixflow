package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"autopr/internal/update"
)

type mockStartVersionChecker struct {
	readCacheFn    func() (update.VersionCheckCache, error)
	isFreshFn      func(update.VersionCheckCache, time.Duration) bool
	refreshCacheFn func(context.Context) (update.VersionCheckCache, error)
}

func (m mockStartVersionChecker) ReadCache() (update.VersionCheckCache, error) {
	if m.readCacheFn == nil {
		return update.VersionCheckCache{}, os.ErrNotExist
	}
	return m.readCacheFn()
}

func (m mockStartVersionChecker) IsCacheFresh(entry update.VersionCheckCache, ttl time.Duration) bool {
	if m.isFreshFn == nil {
		return false
	}
	return m.isFreshFn(entry, ttl)
}

func (m mockStartVersionChecker) RefreshCache(ctx context.Context) (update.VersionCheckCache, error) {
	if m.refreshCacheFn == nil {
		return update.VersionCheckCache{}, nil
	}
	return m.refreshCacheFn(ctx)
}

func TestMaybePrintUpgradeNoticeShowsCachedUpdate(t *testing.T) {
	var out bytes.Buffer
	checker := mockStartVersionChecker{
		readCacheFn: func() (update.VersionCheckCache, error) {
			return update.VersionCheckCache{CheckedAt: time.Now(), LatestTag: "v0.3.0"}, nil
		},
		isFreshFn: func(update.VersionCheckCache, time.Duration) bool { return true },
	}

	maybePrintUpgradeNotice("v0.2.0", &out, checker)

	got := out.String()
	if !strings.Contains(got, "A newer version of ap is available (v0.3.0, current: v0.2.0).") {
		t.Fatalf("missing update notice: %q", got)
	}
	if !strings.Contains(got, "Run `ap upgrade` to update.") {
		t.Fatalf("missing upgrade hint: %q", got)
	}
}

func TestMaybePrintUpgradeNoticeStaleCacheRefreshesAsync(t *testing.T) {
	prevTimeout := startUpdateRefreshTimeout
	startUpdateRefreshTimeout = 250 * time.Millisecond
	defer func() { startUpdateRefreshTimeout = prevTimeout }()

	refreshCalled := make(chan struct{}, 1)
	var out bytes.Buffer
	checker := mockStartVersionChecker{
		readCacheFn: func() (update.VersionCheckCache, error) {
			return update.VersionCheckCache{}, os.ErrNotExist
		},
		refreshCacheFn: func(context.Context) (update.VersionCheckCache, error) {
			refreshCalled <- struct{}{}
			time.Sleep(120 * time.Millisecond)
			return update.VersionCheckCache{}, nil
		},
	}

	started := time.Now()
	maybePrintUpgradeNotice("v0.2.0", &out, checker)
	if elapsed := time.Since(started); elapsed > 30*time.Millisecond {
		t.Fatalf("expected non-blocking call, took %v", elapsed)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output, got %q", out.String())
	}

	select {
	case <-refreshCalled:
	case <-time.After(1 * time.Second):
		t.Fatal("expected refresh to run asynchronously")
	}
}

func TestMaybePrintUpgradeNoticeRefreshFailureIsSilent(t *testing.T) {
	checker := mockStartVersionChecker{
		readCacheFn: func() (update.VersionCheckCache, error) {
			return update.VersionCheckCache{}, os.ErrNotExist
		},
		refreshCacheFn: func(context.Context) (update.VersionCheckCache, error) {
			return update.VersionCheckCache{}, errors.New("network down")
		},
	}

	var out bytes.Buffer
	maybePrintUpgradeNotice("v0.2.0", &out, checker)
	if out.Len() != 0 {
		t.Fatalf("expected silent failure, got %q", out.String())
	}
}
