package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"autopr/internal/config"
	"autopr/internal/daemon"
)

const (
	LaunchdLabel          = "io.autopr.daemon"
	launchdThrottleSecs   = 10
	launchdDefaultPathEnv = "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin"
)

type ServiceStatus struct {
	Label     string `json:"label"`
	PlistPath string `json:"plist_path"`
	Installed bool   `json:"installed"`
	Loaded    bool   `json:"loaded"`
	Running   bool   `json:"running"`
	PID       int    `json:"pid"`
}

var (
	runLaunchctlCmd = runLaunchctl
	resolveExePath  = os.Executable
	resolveHomeDir  = os.UserHomeDir
	resolveUID      = currentUID
	resolvePathEnv  = func() string { return os.Getenv("PATH") }
)

var launchdPIDRegex = regexp.MustCompile(`\bpid\s*=\s*([0-9]+)\b`)

func Install(cfg *config.Config, resolvedConfigPath string) error {
	if cfg == nil {
		return fmt.Errorf("missing config")
	}
	if resolvedConfigPath == "" {
		return fmt.Errorf("missing config path")
	}

	exePath, err := resolveExePath()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	exePath, err = filepath.Abs(exePath)
	if err != nil {
		return fmt.Errorf("resolve executable absolute path: %w", err)
	}

	resolvedConfigPath, err = filepath.Abs(resolvedConfigPath)
	if err != nil {
		return fmt.Errorf("resolve config absolute path: %w", err)
	}

	plistPath, err := PlistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("create launchd plist dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	plist, err := renderLaunchdPlist(exePath, resolvedConfigPath, cfg.LogFile, launchdPathEnv(resolvePathEnv()))
	if err != nil {
		return fmt.Errorf("render launchd plist: %w", err)
	}
	if err := writeFileAtomic(plistPath, plist, 0o644); err != nil {
		return fmt.Errorf("write launchd plist: %w", err)
	}

	domain := launchdDomain()
	target := launchdTarget()

	if out, err := runLaunchctlCmd("bootout", target); err != nil && !isLaunchctlNotLoaded(out, err) {
		return fmt.Errorf("launchctl bootout %s: %w: %s", target, err, strings.TrimSpace(out))
	}
	if out, err := runLaunchctlCmd("bootstrap", domain, plistPath); err != nil {
		return fmt.Errorf("launchctl bootstrap %s %s: %w: %s", domain, plistPath, err, strings.TrimSpace(out))
	}
	if out, err := runLaunchctlCmd("enable", target); err != nil {
		return fmt.Errorf("launchctl enable %s: %w: %s", target, err, strings.TrimSpace(out))
	}
	if out, err := runLaunchctlCmd("kickstart", "-k", target); err != nil {
		return fmt.Errorf("launchctl kickstart -k %s: %w: %s", target, err, strings.TrimSpace(out))
	}

	return nil
}

func Uninstall() error {
	plistPath, err := PlistPath()
	if err != nil {
		return err
	}

	target := launchdTarget()
	if out, err := runLaunchctlCmd("bootout", target); err != nil && !isLaunchctlNotLoaded(out, err) {
		return fmt.Errorf("launchctl bootout %s: %w: %s", target, err, strings.TrimSpace(out))
	}

	// Best effort: disabled service may already be gone.
	_, _ = runLaunchctlCmd("disable", target)

	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist %s: %w", plistPath, err)
	}
	return nil
}

func Status(cfg *config.Config) (ServiceStatus, error) {
	plistPath, err := PlistPath()
	if err != nil {
		return ServiceStatus{}, err
	}

	status := ServiceStatus{
		Label:     LaunchdLabel,
		PlistPath: plistPath,
	}

	if _, err := os.Stat(plistPath); err == nil {
		status.Installed = true
	} else if !os.IsNotExist(err) {
		return status, fmt.Errorf("stat plist %s: %w", plistPath, err)
	}

	out, err := runLaunchctlCmd("print", launchdTarget())
	if err != nil {
		if isLaunchctlNotLoaded(out, err) {
			applyPIDFallback(cfg, &status)
			return status, nil
		}
		return status, fmt.Errorf("launchctl print %s: %w: %s", launchdTarget(), err, strings.TrimSpace(out))
	}

	status.Loaded = true
	status.Running, status.PID = parseLaunchctlPrint(out)
	if !status.Running || status.PID == 0 {
		applyPIDFallback(cfg, &status)
	}
	return status, nil
}

func PlistPath() (string, error) {
	home, err := resolveHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", LaunchdLabel+".plist"), nil
}

func launchdDomain() string {
	return fmt.Sprintf("gui/%d", resolveUID())
}

func launchdTarget() string {
	return launchdDomain() + "/" + LaunchdLabel
}

func currentUID() int {
	u, err := user.Current()
	if err != nil {
		return 0
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0
	}
	return uid
}

func launchdPathEnv(current string) string {
	current = strings.TrimSpace(current)
	if current == "" {
		return launchdDefaultPathEnv
	}
	if strings.Contains(current, launchdDefaultPathEnv) {
		return current
	}
	return launchdDefaultPathEnv + ":" + current
}

func renderLaunchdPlist(exePath, configPath, logPath, pathEnv string) ([]byte, error) {
	var buf bytes.Buffer
	write := func(s string) {
		buf.WriteString(s)
	}
	writeEscaped := func(s string) {
		_ = xml.EscapeText(&buf, []byte(s))
	}
	writeStringValue := func(s string) {
		write("<string>")
		writeEscaped(s)
		write("</string>\n")
	}

	write(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	write("<key>Label</key>\n")
	writeStringValue(LaunchdLabel)
	write("<key>RunAtLoad</key>\n<true/>\n")
	write("<key>KeepAlive</key>\n<true/>\n")
	write("<key>ThrottleInterval</key>\n<integer>")
	write(strconv.Itoa(launchdThrottleSecs))
	write("</integer>\n")
	write("<key>ProgramArguments</key>\n<array>\n")
	for _, arg := range []string{exePath, "start", "--foreground", "--config", configPath} {
		writeStringValue(arg)
	}
	write("</array>\n")
	write("<key>StandardOutPath</key>\n")
	writeStringValue(logPath)
	write("<key>StandardErrorPath</key>\n")
	writeStringValue(logPath)
	write("<key>EnvironmentVariables</key>\n<dict>\n")
	write("<key>PATH</key>\n")
	writeStringValue(pathEnv)
	write("<key>AUTOPR_SKIP_UPDATE_NOTICE</key>\n")
	writeStringValue("1")
	write("</dict>\n")
	write("</dict>\n</plist>\n")

	return buf.Bytes(), nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = os.Remove(tmpName)
	}

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

func applyPIDFallback(cfg *config.Config, status *ServiceStatus) {
	if cfg == nil {
		return
	}
	if cfg.Daemon.PIDFile == "" {
		return
	}
	pid, err := daemon.ReadPID(cfg.Daemon.PIDFile)
	if err != nil {
		return
	}
	if daemon.ProcessAlive(pid) {
		status.Running = true
		status.PID = pid
	}
}

func parseLaunchctlPrint(out string) (bool, int) {
	running := strings.Contains(out, "state = running")
	pid := 0
	m := launchdPIDRegex.FindStringSubmatch(out)
	if len(m) > 1 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			pid = n
		}
	}
	if pid > 0 {
		running = true
	}
	return running, pid
}

func isLaunchctlNotLoaded(out string, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(out + "\n" + err.Error()))
	return strings.Contains(text, "no such process") ||
		strings.Contains(text, "could not find service") ||
		strings.Contains(text, "service is not currently loaded")
}

func runLaunchctl(args ...string) (string, error) {
	cmd := exec.Command("launchctl", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
