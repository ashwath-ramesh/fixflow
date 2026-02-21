package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"autopr/internal/update"
)

type mockUpgradeService struct {
	checkFn   func(context.Context, string) (update.CheckResult, error)
	upgradeFn func(context.Context, string) (update.UpgradeResult, error)
}

func (m mockUpgradeService) Check(ctx context.Context, current string) (update.CheckResult, error) {
	if m.checkFn == nil {
		return update.CheckResult{}, nil
	}
	return m.checkFn(ctx, current)
}

func (m mockUpgradeService) Upgrade(ctx context.Context, current string) (update.UpgradeResult, error) {
	if m.upgradeFn == nil {
		return update.UpgradeResult{}, nil
	}
	return m.upgradeFn(ctx, current)
}

func TestRunUpgradeCheckShowsAvailableUpdate(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	err := runUpgradeWith(context.Background(), &out, mockUpgradeService{
		checkFn: func(context.Context, string) (update.CheckResult, error) {
			return update.CheckResult{CurrentVersion: "v0.2.0", LatestVersion: "v0.3.0", UpdateAvailable: true}, nil
		},
	}, "v0.2.0", true)
	if err != nil {
		t.Fatalf("runUpgradeWith: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "update available: v0.3.0 (current: v0.2.0)") {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestRunUpgradeCheckShowsUpToDate(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	err := runUpgradeWith(context.Background(), &out, mockUpgradeService{
		checkFn: func(context.Context, string) (update.CheckResult, error) {
			return update.CheckResult{CurrentVersion: "v0.2.0", LatestVersion: "v0.2.0", UpdateAvailable: false}, nil
		},
	}, "v0.2.0", true)
	if err != nil {
		t.Fatalf("runUpgradeWith: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "already up to date (v0.2.0)") {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestRunUpgradeInstallsOnlyWhenNewer(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	calls := 0
	err := runUpgradeWith(context.Background(), &out, mockUpgradeService{
		upgradeFn: func(context.Context, string) (update.UpgradeResult, error) {
			calls++
			return update.UpgradeResult{CheckResult: update.CheckResult{CurrentVersion: "v0.2.0", LatestVersion: "v0.3.0", UpdateAvailable: true}, Upgraded: true}, nil
		},
	}, "v0.2.0", false)
	if err != nil {
		t.Fatalf("runUpgradeWith: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected one upgrade call, got %d", calls)
	}
	got := out.String()
	if !strings.Contains(got, "upgraded ap to v0.3.0") {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestRunUpgradeSurfacesInstallErrors(t *testing.T) {
	t.Parallel()

	expectedErr := errors.New("install failed")
	err := runUpgradeWith(context.Background(), &bytes.Buffer{}, mockUpgradeService{
		upgradeFn: func(context.Context, string) (update.UpgradeResult, error) {
			return update.UpgradeResult{}, expectedErr
		},
	}, "v0.2.0", false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "install failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}
