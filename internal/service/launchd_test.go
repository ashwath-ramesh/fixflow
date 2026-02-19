package service

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"autopr/internal/config"
)

func TestRenderLaunchdPlistIncludesRequiredFields(t *testing.T) {
	t.Parallel()

	out, err := renderLaunchdPlist(
		"/usr/local/bin/ap",
		"/tmp/config.toml",
		"/tmp/autopr.log",
		"/usr/bin:/bin",
	)
	if err != nil {
		t.Fatalf("render launchd plist: %v", err)
	}
	plist := string(out)

	for _, want := range []string{
		"<key>Label</key>",
		"<string>io.autopr.daemon</string>",
		"<key>RunAtLoad</key>",
		"<true/>",
		"<key>KeepAlive</key>",
		"<key>ThrottleInterval</key>",
		"<integer>10</integer>",
		"<key>ProgramArguments</key>",
		"<string>/usr/local/bin/ap</string>",
		"<string>start</string>",
		"<string>--foreground</string>",
		"<string>--config</string>",
		"<string>/tmp/config.toml</string>",
		"<key>StandardOutPath</key>",
		"<key>StandardErrorPath</key>",
		"<string>/tmp/autopr.log</string>",
		"<key>PATH</key>",
		"<string>/usr/bin:/bin</string>",
		"<key>AUTOPR_SKIP_UPDATE_NOTICE</key>",
		"<string>1</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("missing %q in plist:\n%s", want, plist)
		}
	}
}

func TestInstallWritesPlistAndRunsLaunchctlSequence(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	logPath := filepath.Join(tmp, "state", "autopr.log")

	prevRun := runLaunchctlCmd
	prevExe := resolveExePath
	prevHome := resolveHomeDir
	prevUID := resolveUID
	prevPATH := resolvePathEnv
	t.Cleanup(func() {
		runLaunchctlCmd = prevRun
		resolveExePath = prevExe
		resolveHomeDir = prevHome
		resolveUID = prevUID
		resolvePathEnv = prevPATH
	})

	resolveExePath = func() (string, error) { return filepath.Join(tmp, "bin", "ap"), nil }
	resolveHomeDir = func() (string, error) { return home, nil }
	resolveUID = func() int { return 501 }
	resolvePathEnv = func() string { return "/custom/bin" }

	var calls [][]string
	runLaunchctlCmd = func(args ...string) (string, error) {
		call := append([]string(nil), args...)
		calls = append(calls, call)
		if len(args) > 0 && args[0] == "bootout" {
			return "Boot-out failed: 3: No such process", errors.New("exit status 3")
		}
		return "", nil
	}

	cfg := &config.Config{LogFile: logPath}
	if err := Install(cfg, "relative/config.toml"); err != nil {
		t.Fatalf("install launchd service: %v", err)
	}

	plistPath, err := PlistPath()
	if err != nil {
		t.Fatalf("plist path: %v", err)
	}
	data, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	plist := string(data)
	if !strings.Contains(plist, "<string>"+filepath.Join(tmp, "bin", "ap")+"</string>") {
		t.Fatalf("plist missing executable path: %s", plist)
	}
	absCfgPath, err := filepath.Abs("relative/config.toml")
	if err != nil {
		t.Fatalf("resolve abs config path: %v", err)
	}
	if !strings.Contains(plist, "<string>"+absCfgPath+"</string>") {
		t.Fatalf("plist missing absolute config path: %s", plist)
	}
	if !strings.Contains(plist, "<string>"+logPath+"</string>") {
		t.Fatalf("plist missing log path: %s", plist)
	}

	expected := [][]string{
		{"bootout", "gui/501/io.autopr.daemon"},
		{"bootstrap", "gui/501", filepath.Join(home, "Library", "LaunchAgents", "io.autopr.daemon.plist")},
		{"enable", "gui/501/io.autopr.daemon"},
		{"kickstart", "-k", "gui/501/io.autopr.daemon"},
	}
	if !reflect.DeepEqual(expected, calls) {
		t.Fatalf("unexpected launchctl calls:\nwant: %#v\ngot:  %#v", expected, calls)
	}
}

func TestUninstallRemovesPlist(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")

	prevRun := runLaunchctlCmd
	prevHome := resolveHomeDir
	prevUID := resolveUID
	t.Cleanup(func() {
		runLaunchctlCmd = prevRun
		resolveHomeDir = prevHome
		resolveUID = prevUID
	})

	resolveHomeDir = func() (string, error) { return home, nil }
	resolveUID = func() int { return 777 }

	plistPath := filepath.Join(home, "Library", "LaunchAgents", "io.autopr.daemon.plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatalf("mkdir plist dir: %v", err)
	}
	if err := os.WriteFile(plistPath, []byte("plist"), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}

	var calls [][]string
	runLaunchctlCmd = func(args ...string) (string, error) {
		call := append([]string(nil), args...)
		calls = append(calls, call)
		if len(args) > 0 && args[0] == "bootout" {
			return "Boot-out failed: 3: No such process", errors.New("exit status 3")
		}
		return "", nil
	}

	if err := Uninstall(); err != nil {
		t.Fatalf("uninstall launchd service: %v", err)
	}

	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Fatalf("expected plist removed, err=%v", err)
	}

	expected := [][]string{
		{"bootout", "gui/777/io.autopr.daemon"},
		{"disable", "gui/777/io.autopr.daemon"},
	}
	if !reflect.DeepEqual(expected, calls) {
		t.Fatalf("unexpected launchctl calls:\nwant: %#v\ngot:  %#v", expected, calls)
	}
}

func TestStatusParsesLaunchctlPrint(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")

	prevRun := runLaunchctlCmd
	prevHome := resolveHomeDir
	prevUID := resolveUID
	t.Cleanup(func() {
		runLaunchctlCmd = prevRun
		resolveHomeDir = prevHome
		resolveUID = prevUID
	})

	resolveHomeDir = func() (string, error) { return home, nil }
	resolveUID = func() int { return 88 }

	plistPath := filepath.Join(home, "Library", "LaunchAgents", "io.autopr.daemon.plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatalf("mkdir plist dir: %v", err)
	}
	if err := os.WriteFile(plistPath, []byte("plist"), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}

	runLaunchctlCmd = func(args ...string) (string, error) {
		if len(args) > 0 && args[0] == "print" {
			return "state = running\npid = 1234\n", nil
		}
		return "", nil
	}

	status, err := Status(&config.Config{})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Installed || !status.Loaded || !status.Running {
		t.Fatalf("unexpected status: %#v", status)
	}
	if status.PID != 1234 {
		t.Fatalf("expected pid 1234, got %d", status.PID)
	}
}

func TestStatusFallsBackToPIDFile(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	pidPath := filepath.Join(tmp, "autopr.pid")

	prevRun := runLaunchctlCmd
	prevHome := resolveHomeDir
	prevUID := resolveUID
	t.Cleanup(func() {
		runLaunchctlCmd = prevRun
		resolveHomeDir = prevHome
		resolveUID = prevUID
	})

	resolveHomeDir = func() (string, error) { return home, nil }
	resolveUID = func() int { return 99 }

	plistPath := filepath.Join(home, "Library", "LaunchAgents", "io.autopr.daemon.plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatalf("mkdir plist dir: %v", err)
	}
	if err := os.WriteFile(plistPath, []byte("plist"), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	runLaunchctlCmd = func(args ...string) (string, error) {
		if len(args) > 0 && args[0] == "print" {
			return "state = waiting\n", nil
		}
		return "", nil
	}

	status, err := Status(&config.Config{
		Daemon: config.DaemonConfig{PIDFile: pidPath},
	})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Installed || !status.Loaded || !status.Running {
		t.Fatalf("unexpected status: %#v", status)
	}
	if status.PID != os.Getpid() {
		t.Fatalf("expected pid %d, got %d", os.Getpid(), status.PID)
	}
}

func TestStatusNotLoadedIsNotError(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")

	prevRun := runLaunchctlCmd
	prevHome := resolveHomeDir
	prevUID := resolveUID
	t.Cleanup(func() {
		runLaunchctlCmd = prevRun
		resolveHomeDir = prevHome
		resolveUID = prevUID
	})

	resolveHomeDir = func() (string, error) { return home, nil }
	resolveUID = func() int { return 104 }

	plistPath := filepath.Join(home, "Library", "LaunchAgents", "io.autopr.daemon.plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatalf("mkdir plist dir: %v", err)
	}
	if err := os.WriteFile(plistPath, []byte("plist"), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}

	runLaunchctlCmd = func(args ...string) (string, error) {
		return "Could not find service", errors.New("exit status 113")
	}

	status, err := Status(&config.Config{})
	if err != nil {
		t.Fatalf("status should not fail: %v", err)
	}
	if !status.Installed {
		t.Fatalf("expected installed=true, got %#v", status)
	}
	if status.Loaded || status.Running || status.PID != 0 {
		t.Fatalf("expected unloaded status, got %#v", status)
	}
}
