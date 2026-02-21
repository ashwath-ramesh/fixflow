package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autopr/internal/config"
)

type runPathsJSON struct {
	Config string `json:"config"`
	Data   string `json:"data"`
	DB     string `json:"db"`
	Repos  string `json:"repos"`
	Log    string `json:"log"`
}

func TestRunPathsJSONOutput(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "custom-autopr.db")
	reposPath := filepath.Join(tmp, "custom-repos")
	logPath := filepath.Join(tmp, "custom.log")
	pidPath := filepath.Join(tmp, "autopr.pid")
	cfgPath := writePathsConfig(t, tmp, dbPath, reposPath, logPath, pidPath)

	out := runPathsWithTestConfig(t, cfgPath, true)
	var got runPathsJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("decode JSON output: %v", err)
	}
	var gotRaw map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &gotRaw); err != nil {
		t.Fatalf("decode JSON output for raw keys: %v", err)
	}

	expectedConfig, err := config.ConfigDir()
	if err != nil {
		t.Fatalf("resolve config dir: %v", err)
	}
	expectedData, err := config.DataDir()
	if err != nil {
		t.Fatalf("resolve data dir: %v", err)
	}

	if got.Config != expectedConfig {
		t.Fatalf("config: expected %q, got %q", expectedConfig, got.Config)
	}
	if got.Data != expectedData {
		t.Fatalf("data: expected %q, got %q", expectedData, got.Data)
	}
	if got.DB != dbPath {
		t.Fatalf("db: expected %q, got %q", dbPath, got.DB)
	}
	if got.Repos != reposPath {
		t.Fatalf("repos: expected %q, got %q", reposPath, got.Repos)
	}
	if got.Log != logPath {
		t.Fatalf("log: expected %q, got %q", logPath, got.Log)
	}
	if got.DB == "" {
		t.Fatalf("expected db path in JSON output")
	}
	if got.Repos == "" {
		t.Fatalf("expected repos path in JSON output")
	}
	if got.Log == "" {
		t.Fatalf("expected log path in JSON output")
	}
	if _, ok := gotRaw["state"]; ok {
		t.Fatalf("JSON output should not include \"state\"")
	}
	if _, ok := gotRaw["pid"]; ok {
		t.Fatalf("JSON output should not include \"pid\"")
	}
}

func TestRunPathsJSONWithoutConfig(t *testing.T) {
	missingCfgPath := filepath.Join(t.TempDir(), "missing.toml")

	out := runPathsWithTestConfig(t, missingCfgPath, true)
	got := map[string]string{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("decode JSON output: %v", err)
	}

	expectedConfig, err := config.ConfigDir()
	if err != nil {
		t.Fatalf("resolve config dir: %v", err)
	}
	expectedData, err := config.DataDir()
	if err != nil {
		t.Fatalf("resolve data dir: %v", err)
	}

	if got["config"] != expectedConfig {
		t.Fatalf("config: expected %q, got %q", expectedConfig, got["config"])
	}
	if got["data"] != expectedData {
		t.Fatalf("data: expected %q, got %q", expectedData, got["data"])
	}
	if got["db"] != "" || got["repos"] != "" || got["log"] != "" {
		t.Fatalf("expected empty db, repos, and log when config is unavailable")
	}

	if _, ok := got["state"]; ok {
		t.Fatalf("JSON output should not include \"state\"")
	}
}

func TestRunPathsHumanOutput(t *testing.T) {
	missingCfgPath := filepath.Join(t.TempDir(), "missing.toml")

	out := runPathsWithTestConfig(t, missingCfgPath, false)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	expectedConfig, err := config.ConfigDir()
	if err != nil {
		t.Fatalf("resolve config dir: %v", err)
	}
	expectedData, err := config.DataDir()
	if err != nil {
		t.Fatalf("resolve data dir: %v", err)
	}
	expectedState, err := config.StateDir()
	if err != nil {
		t.Fatalf("resolve state dir: %v", err)
	}

	prefixes := []string{
		"Config:  " + expectedConfig,
		"Data:    " + expectedData,
		"State:   " + expectedState,
	}
	if len(lines) < len(prefixes) {
		t.Fatalf("expected at least %d lines, got %d: %q", len(prefixes), len(lines), out)
	}
	for _, prefix := range prefixes {
		if !strings.Contains(out, prefix) {
			t.Fatalf("expected output to contain %q, got %q", prefix, out)
		}
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("expected non-JSON output, got %q", strings.TrimSpace(out))
	}
}

func runPathsWithTestConfig(t *testing.T, path string, asJSON bool) string {
	t.Helper()

	prevCfgPath := cfgPath
	prevJSON := jsonOut
	cfgPath = path
	jsonOut = asJSON
	t.Cleanup(func() {
		cfgPath = prevCfgPath
		jsonOut = prevJSON
	})

	return captureStdout(t, func() error {
		return runPaths(nil, nil)
	})
}

func writePathsConfig(t *testing.T, dir, dbPath, reposPath, logPath, pidPath string) string {
	t.Helper()
	cfgPath := filepath.Join(dir, "autopr.toml")
	cfg := fmt.Sprintf(`db_path = %q
repos_root = %q
log_file = %q

[daemon]
pid_file = %q

[[projects]]
name = "project"
repo_url = "https://github.com/acmecorp/placeholder"
test_cmd = "echo ok"

[projects.github]
owner = "acmecorp"
repo = "placeholder"
`, dbPath, reposPath, logPath, pidPath)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}
