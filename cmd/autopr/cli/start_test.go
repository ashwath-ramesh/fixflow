package cli

import (
	"errors"
	"io"
	"strings"
	"testing"

	"autopr/internal/config"
)

func TestRunStartWithSkipsNoticeWhenConfigLoadFails(t *testing.T) {
	t.Setenv(skipUpdateNoticeEnv, "")

	expectedErr := errors.New("no config")
	noticeCalled := false
	err := runStartWith(
		func() (*config.Config, error) { return nil, expectedErr },
		func(string) bool {
			t.Fatal("isDaemonRunning should not be called")
			return false
		},
		func(string, io.Writer, startVersionChecker) { noticeCalled = true },
		func(string) startVersionChecker {
			t.Fatal("checkerFactory should not be called")
			return nil
		},
		func(*config.Config) error {
			t.Fatal("runForeground should not be called")
			return nil
		},
		func(*config.Config) error {
			t.Fatal("runBackground should not be called")
			return nil
		},
	)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected config error %v, got %v", expectedErr, err)
	}
	if noticeCalled {
		t.Fatal("expected notice check to be skipped on config error")
	}
}

func TestRunStartWithSkipsNoticeWhenDaemonAlreadyRunning(t *testing.T) {
	t.Setenv(skipUpdateNoticeEnv, "")

	cfg := &config.Config{
		Daemon: config.DaemonConfig{PIDFile: "/tmp/autopr.pid"},
	}
	noticeCalled := false
	err := runStartWith(
		func() (*config.Config, error) { return cfg, nil },
		func(string) bool { return true },
		func(string, io.Writer, startVersionChecker) { noticeCalled = true },
		func(string) startVersionChecker {
			t.Fatal("checkerFactory should not be called")
			return nil
		},
		func(*config.Config) error {
			t.Fatal("runForeground should not be called")
			return nil
		},
		func(*config.Config) error {
			t.Fatal("runBackground should not be called")
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "daemon is already running") {
		t.Fatalf("expected daemon already running error, got %v", err)
	}
	if noticeCalled {
		t.Fatal("expected notice check to be skipped when daemon is already running")
	}
}

func TestRunStartWithRunsNoticeAfterEarlyChecks(t *testing.T) {
	t.Setenv(skipUpdateNoticeEnv, "")

	prevForeground := foreground
	foreground = true
	t.Cleanup(func() { foreground = prevForeground })

	cfg := &config.Config{
		Daemon: config.DaemonConfig{PIDFile: "/tmp/autopr.pid"},
	}
	noticeCalled := false
	fgCalled := false
	err := runStartWith(
		func() (*config.Config, error) { return cfg, nil },
		func(string) bool { return false },
		func(currentVersion string, _ io.Writer, checker startVersionChecker) {
			noticeCalled = true
			if currentVersion != version {
				t.Fatalf("expected current version %q, got %q", version, currentVersion)
			}
			if checker == nil {
				t.Fatal("expected checker")
			}
		},
		func(string) startVersionChecker { return mockStartVersionChecker{} },
		func(*config.Config) error {
			fgCalled = true
			return nil
		},
		func(*config.Config) error {
			t.Fatal("runBackground should not be called")
			return nil
		},
	)
	if err != nil {
		t.Fatalf("runStartWith: %v", err)
	}
	if !noticeCalled {
		t.Fatal("expected notice check to run")
	}
	if !fgCalled {
		t.Fatal("expected foreground runner to be called")
	}
}
