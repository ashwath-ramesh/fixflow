package cli

import (
	"errors"
	"path/filepath"
	"testing"

	"autopr/internal/config"
	launchdservice "autopr/internal/service"
)

func TestResolveStopPIDFallsBackToServiceOnDarwin(t *testing.T) {
	prevPlatform := stopPlatform
	prevStatus := stopServiceStatus
	t.Cleanup(func() {
		stopPlatform = prevPlatform
		stopServiceStatus = prevStatus
	})

	stopPlatform = "darwin"
	stopServiceStatus = func(*config.Config) (launchdservice.ServiceStatus, error) {
		return launchdservice.ServiceStatus{Running: true, PID: 4321}, nil
	}

	cfg := &config.Config{
		Daemon: config.DaemonConfig{PIDFile: filepath.Join(t.TempDir(), "missing.pid")},
	}
	pid, err := resolveStopPID(cfg)
	if err != nil {
		t.Fatalf("resolveStopPID: %v", err)
	}
	if pid != 4321 {
		t.Fatalf("expected pid 4321, got %d", pid)
	}
}

func TestResolveStopPIDNonDarwinReturnsPIDError(t *testing.T) {
	prevPlatform := stopPlatform
	prevStatus := stopServiceStatus
	t.Cleanup(func() {
		stopPlatform = prevPlatform
		stopServiceStatus = prevStatus
	})

	stopPlatform = "linux"
	stopServiceStatus = func(*config.Config) (launchdservice.ServiceStatus, error) {
		t.Fatal("service status should not be called")
		return launchdservice.ServiceStatus{}, nil
	}

	cfg := &config.Config{
		Daemon: config.DaemonConfig{PIDFile: filepath.Join(t.TempDir(), "missing.pid")},
	}
	if _, err := resolveStopPID(cfg); err == nil {
		t.Fatal("expected error")
	}
}

func TestResolveStopPIDServiceErrorReturnsPIDError(t *testing.T) {
	prevPlatform := stopPlatform
	prevStatus := stopServiceStatus
	t.Cleanup(func() {
		stopPlatform = prevPlatform
		stopServiceStatus = prevStatus
	})

	stopPlatform = "darwin"
	stopServiceStatus = func(*config.Config) (launchdservice.ServiceStatus, error) {
		return launchdservice.ServiceStatus{}, errors.New("launchctl failed")
	}

	cfg := &config.Config{
		Daemon: config.DaemonConfig{PIDFile: filepath.Join(t.TempDir(), "missing.pid")},
	}
	if _, err := resolveStopPID(cfg); err == nil {
		t.Fatal("expected error")
	}
}
