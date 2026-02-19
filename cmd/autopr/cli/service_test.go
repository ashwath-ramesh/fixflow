package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"autopr/internal/config"
	launchdservice "autopr/internal/service"
)

type stubServiceManager struct {
	installCfg     *config.Config
	installCfgPath string
	installErr     error
	uninstallErr   error
	statusResp     launchdservice.ServiceStatus
	statusErr      error
	plistPath      string
	plistPathErr   error
	installCalls   int
	uninstallCalls int
	statusCalls    int
	plistPathCalls int
}

func (s *stubServiceManager) Install(cfg *config.Config, resolvedConfigPath string) error {
	s.installCalls++
	s.installCfg = cfg
	s.installCfgPath = resolvedConfigPath
	return s.installErr
}

func (s *stubServiceManager) Uninstall() error {
	s.uninstallCalls++
	return s.uninstallErr
}

func (s *stubServiceManager) Status(cfg *config.Config) (launchdservice.ServiceStatus, error) {
	s.statusCalls++
	return s.statusResp, s.statusErr
}

func (s *stubServiceManager) PlistPath() (string, error) {
	s.plistPathCalls++
	return s.plistPath, s.plistPathErr
}

func TestRunServiceInstallUnsupportedOS(t *testing.T) {
	t.Parallel()

	err := runServiceInstallWith(
		"linux",
		func() (*config.Config, error) { return &config.Config{}, nil },
		func() (string, error) { return "config.toml", nil },
		&stubServiceManager{},
		&bytes.Buffer{},
	)
	if err == nil {
		t.Fatal("expected unsupported error")
	}
	if !strings.Contains(err.Error(), "supported only on macOS") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunServiceInstallSuccess(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	stub := &stubServiceManager{
		plistPath: "/Users/test/Library/LaunchAgents/io.autopr.daemon.plist",
	}

	var out bytes.Buffer
	err := runServiceInstallWith(
		"darwin",
		func() (*config.Config, error) { return cfg, nil },
		func() (string, error) { return "configs/dev.toml", nil },
		stub,
		&out,
	)
	if err != nil {
		t.Fatalf("runServiceInstallWith: %v", err)
	}
	if stub.installCalls != 1 {
		t.Fatalf("expected one install call, got %d", stub.installCalls)
	}
	abs, err := filepath.Abs("configs/dev.toml")
	if err != nil {
		t.Fatalf("resolve abs path: %v", err)
	}
	if stub.installCfgPath != abs {
		t.Fatalf("expected abs config path %q, got %q", abs, stub.installCfgPath)
	}
	if stub.installCfg != cfg {
		t.Fatalf("expected same config pointer")
	}
	got := out.String()
	if !strings.Contains(got, "Service installed: io.autopr.daemon") {
		t.Fatalf("unexpected output: %q", got)
	}
	if !strings.Contains(got, stub.plistPath) {
		t.Fatalf("missing plist path in output: %q", got)
	}
}

func TestRunServiceUninstallAndError(t *testing.T) {
	t.Parallel()

	stub := &stubServiceManager{}
	var out bytes.Buffer
	if err := runServiceUninstallWith("darwin", stub, &out); err != nil {
		t.Fatalf("uninstall success path: %v", err)
	}
	if stub.uninstallCalls != 1 {
		t.Fatalf("expected one uninstall call, got %d", stub.uninstallCalls)
	}
	if !strings.Contains(out.String(), "Service uninstalled.") {
		t.Fatalf("unexpected output: %q", out.String())
	}

	stub.uninstallErr = errors.New("boom")
	if err := runServiceUninstallWith("darwin", stub, &bytes.Buffer{}); err == nil {
		t.Fatal("expected uninstall error")
	}
}

func TestRunServiceUninstallUnsupportedOS(t *testing.T) {
	t.Parallel()

	err := runServiceUninstallWith("linux", &stubServiceManager{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected unsupported error")
	}
	if !strings.Contains(err.Error(), "supported only on macOS") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunServiceStatusJSONShape(t *testing.T) {
	t.Parallel()

	stub := &stubServiceManager{
		statusResp: launchdservice.ServiceStatus{
			Label:     launchdservice.LaunchdLabel,
			PlistPath: "/tmp/io.autopr.daemon.plist",
			Installed: true,
			Loaded:    true,
			Running:   true,
			PID:       42,
		},
	}

	var out bytes.Buffer
	err := runServiceStatusWith(
		"darwin",
		func() (*config.Config, error) { return &config.Config{}, nil },
		stub,
		&out,
		true,
	)
	if err != nil {
		t.Fatalf("runServiceStatusWith: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	for _, key := range []string{"label", "plist_path", "installed", "loaded", "running", "pid"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing key %q in output: %s", key, out.String())
		}
	}
}

func TestRunServiceStatusUnsupportedOS(t *testing.T) {
	t.Parallel()

	err := runServiceStatusWith(
		"linux",
		func() (*config.Config, error) { return &config.Config{}, nil },
		&stubServiceManager{},
		&bytes.Buffer{},
		false,
	)
	if err == nil {
		t.Fatal("expected unsupported error")
	}
	if !strings.Contains(err.Error(), "supported only on macOS") {
		t.Fatalf("unexpected error: %v", err)
	}
}
