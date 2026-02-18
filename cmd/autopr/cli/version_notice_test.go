package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"autopr/internal/update"
)

type mockStartVersionChecker struct {
	readCacheFn    func() (update.VersionCheckCache, error)
	isFreshFn      func(update.VersionCheckCache, time.Duration) bool
	refreshCacheFn func(context.Context) (update.VersionCheckCache, error)
	markAttemptFn  func(string) error
}

func setNoticeRefreshSync(t *testing.T) {
	t.Helper()
	prev := runStartUpdateRefresh
	runStartUpdateRefresh = func(fn func()) { fn() }
	t.Cleanup(func() { runStartUpdateRefresh = prev })
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

func (m mockStartVersionChecker) MarkCheckAttempt(latestTag string) error {
	if m.markAttemptFn == nil {
		return nil
	}
	return m.markAttemptFn(latestTag)
}

func TestMaybePrintUpgradeNoticeShowsCachedUpdate(t *testing.T) {
	setNoticeRefreshSync(t)

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

func TestMaybePrintUpgradeNoticeStaleCachePrintsRefreshedUpdate(t *testing.T) {
	setNoticeRefreshSync(t)

	prevTimeout := startUpdateRefreshTimeout
	startUpdateRefreshTimeout = 250 * time.Millisecond
	defer func() { startUpdateRefreshTimeout = prevTimeout }()

	var refreshCalls atomic.Int32
	var out bytes.Buffer
	checker := mockStartVersionChecker{
		readCacheFn: func() (update.VersionCheckCache, error) {
			return update.VersionCheckCache{}, os.ErrNotExist
		},
		refreshCacheFn: func(context.Context) (update.VersionCheckCache, error) {
			refreshCalls.Add(1)
			return update.VersionCheckCache{CheckedAt: time.Now(), LatestTag: "v0.3.0"}, nil
		},
	}

	maybePrintUpgradeNotice("v0.2.0", &out, checker)
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("expected one refresh call, got %d", got)
	}

	got := out.String()
	if !strings.Contains(got, "A newer version of ap is available (v0.3.0, current: v0.2.0).") {
		t.Fatalf("missing update notice: %q", got)
	}
	if !strings.Contains(got, "Run `ap upgrade` to update.") {
		t.Fatalf("missing upgrade hint: %q", got)
	}
}

func TestMaybePrintUpgradeNoticeRefreshFailureMarksAttemptAndIsSilent(t *testing.T) {
	setNoticeRefreshSync(t)

	prevTimeout := startUpdateRefreshTimeout
	startUpdateRefreshTimeout = 250 * time.Millisecond
	defer func() { startUpdateRefreshTimeout = prevTimeout }()

	markCalled := make(chan string, 1)
	checker := mockStartVersionChecker{
		readCacheFn: func() (update.VersionCheckCache, error) {
			return update.VersionCheckCache{}, os.ErrNotExist
		},
		refreshCacheFn: func(context.Context) (update.VersionCheckCache, error) {
			return update.VersionCheckCache{}, errors.New("network down")
		},
		markAttemptFn: func(tag string) error {
			markCalled <- tag
			return nil
		},
	}

	var out bytes.Buffer
	maybePrintUpgradeNotice("v0.2.0", &out, checker)
	select {
	case got := <-markCalled:
		if got != "v0.2.0" {
			t.Fatalf("expected fallback tag v0.2.0, got %q", got)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("expected failed refresh to mark check attempt")
	}
	if out.Len() != 0 {
		t.Fatalf("expected silent failure, got %q", out.String())
	}
}

func TestMaybePrintUpgradeNoticeFreshCacheSkipsRefresh(t *testing.T) {
	setNoticeRefreshSync(t)

	var refreshCalls atomic.Int32
	checker := mockStartVersionChecker{
		readCacheFn: func() (update.VersionCheckCache, error) {
			return update.VersionCheckCache{CheckedAt: time.Now(), LatestTag: "v0.2.0"}, nil
		},
		isFreshFn: func(update.VersionCheckCache, time.Duration) bool { return true },
		refreshCacheFn: func(context.Context) (update.VersionCheckCache, error) {
			refreshCalls.Add(1)
			return update.VersionCheckCache{}, nil
		},
	}

	maybePrintUpgradeNotice("v0.2.0", &bytes.Buffer{}, checker)
	if refreshCalls.Load() != 0 {
		t.Fatalf("expected no refresh for fresh cache, got %d", refreshCalls.Load())
	}
}

func TestMaybePrintUpgradeNoticeFailedRefreshThrottlesForTTL(t *testing.T) {
	setNoticeRefreshSync(t)

	prevTimeout := startUpdateRefreshTimeout
	startUpdateRefreshTimeout = 250 * time.Millisecond
	defer func() { startUpdateRefreshTimeout = prevTimeout }()

	var (
		mu    sync.Mutex
		cache *update.VersionCheckCache
	)
	var refreshCalls atomic.Int32
	markCalled := make(chan struct{}, 1)
	checker := mockStartVersionChecker{
		readCacheFn: func() (update.VersionCheckCache, error) {
			mu.Lock()
			defer mu.Unlock()
			if cache == nil {
				return update.VersionCheckCache{}, os.ErrNotExist
			}
			return *cache, nil
		},
		isFreshFn: func(entry update.VersionCheckCache, ttl time.Duration) bool {
			return time.Since(entry.CheckedAt) <= ttl
		},
		refreshCacheFn: func(context.Context) (update.VersionCheckCache, error) {
			refreshCalls.Add(1)
			return update.VersionCheckCache{}, errors.New("offline")
		},
		markAttemptFn: func(tag string) error {
			mu.Lock()
			cache = &update.VersionCheckCache{CheckedAt: time.Now(), LatestTag: tag}
			mu.Unlock()
			markCalled <- struct{}{}
			return nil
		},
	}

	maybePrintUpgradeNotice("v0.2.0", &bytes.Buffer{}, checker)
	select {
	case <-markCalled:
	case <-time.After(1 * time.Second):
		t.Fatal("expected first failed refresh to mark cache")
	}
	maybePrintUpgradeNotice("v0.2.0", &bytes.Buffer{}, checker)

	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("expected one refresh attempt within TTL, got %d", got)
	}
}

func TestMaybePrintUpgradeNoticeRefreshRunsAsync(t *testing.T) {
	prevTimeout := startUpdateRefreshTimeout
	startUpdateRefreshTimeout = 120 * time.Millisecond
	defer func() { startUpdateRefreshTimeout = prevTimeout }()

	markCalled := make(chan struct{}, 1)
	checker := mockStartVersionChecker{
		readCacheFn: func() (update.VersionCheckCache, error) {
			return update.VersionCheckCache{}, os.ErrNotExist
		},
		refreshCacheFn: func(ctx context.Context) (update.VersionCheckCache, error) {
			<-ctx.Done()
			return update.VersionCheckCache{}, ctx.Err()
		},
		markAttemptFn: func(string) error {
			markCalled <- struct{}{}
			return nil
		},
	}

	started := time.Now()
	maybePrintUpgradeNotice("v0.2.0", &bytes.Buffer{}, checker)
	elapsed := time.Since(started)
	if elapsed > 50*time.Millisecond {
		t.Fatalf("expected immediate return, took %v", elapsed)
	}

	select {
	case <-markCalled:
	case <-time.After(1 * time.Second):
		t.Fatal("expected async refresh path to mark check attempt")
	}
}
