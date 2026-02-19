package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadParsesProjectsAndDefaults(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
db_path = "autopr.db"

[[projects]]
name = "myproject"
repo_url = "https://gitlab.com/org/repo.git"
test_cmd = "go test ./..."

  [projects.gitlab]
  base_url = "https://gitlab.com"
  project_id = "123"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// Check defaults.
	if cfg.Daemon.WebhookPort != 9847 {
		t.Fatalf("expected default webhook port 9847, got %d", cfg.Daemon.WebhookPort)
	}
	if cfg.Daemon.MaxWorkers != 3 {
		t.Fatalf("expected default max workers 3, got %d", cfg.Daemon.MaxWorkers)
	}
	if cfg.LLM.Provider != "codex" {
		t.Fatalf("expected default provider codex, got %s", cfg.LLM.Provider)
	}

	// Check project.
	p, ok := cfg.ProjectByName("myproject")
	if !ok {
		t.Fatalf("expected myproject")
	}
	if p.GitLab == nil || p.GitLab.ProjectID != "123" {
		t.Fatalf("expected gitlab project_id 123, got %+v", p.GitLab)
	}
	if p.BaseBranch != "main" {
		t.Fatalf("expected default base_branch main, got %s", p.BaseBranch)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"

  [projects.github]
  owner = "org"
  repo = "repo"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("AUTOPR_WEBHOOK_SECRET", "mysecret")
	t.Setenv("GITLAB_TOKEN", "gltoken")

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Daemon.WebhookSecret != "mysecret" {
		t.Fatalf("expected webhook secret from env, got %q", cfg.Daemon.WebhookSecret)
	}
	if cfg.Tokens.GitLab != "gltoken" {
		t.Fatalf("expected gitlab token from env, got %q", cfg.Tokens.GitLab)
	}
}

func TestLoadFailsForNoProjects(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	if err := os.WriteFile(cfgPath, []byte(`log_level = "info"`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDefaultPathsAreAbsoluteXDG(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	// Config with no path fields set — defaults should kick in.
	content := `
[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"

  [projects.github]
  owner = "org"
  repo = "repo"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// All default paths must be absolute.
	if !filepath.IsAbs(cfg.DBPath) {
		t.Fatalf("expected absolute DBPath, got %q", cfg.DBPath)
	}
	if !filepath.IsAbs(cfg.ReposRoot) {
		t.Fatalf("expected absolute ReposRoot, got %q", cfg.ReposRoot)
	}
	if !filepath.IsAbs(cfg.LogFile) {
		t.Fatalf("expected absolute LogFile, got %q", cfg.LogFile)
	}
	if !filepath.IsAbs(cfg.Daemon.PIDFile) {
		t.Fatalf("expected absolute PIDFile, got %q", cfg.Daemon.PIDFile)
	}

	// DBPath and ReposRoot should be under the data directory.
	if !strings.Contains(cfg.DBPath, filepath.Join(".local", "share", "autopr")) {
		t.Fatalf("expected DBPath under XDG data dir, got %q", cfg.DBPath)
	}
	if !strings.Contains(cfg.ReposRoot, filepath.Join(".local", "share", "autopr")) {
		t.Fatalf("expected ReposRoot under XDG data dir, got %q", cfg.ReposRoot)
	}

	// LogFile and PIDFile should be under the state directory.
	if !strings.Contains(cfg.LogFile, filepath.Join(".local", "state", "autopr")) {
		t.Fatalf("expected LogFile under XDG state dir, got %q", cfg.LogFile)
	}
	if !strings.Contains(cfg.Daemon.PIDFile, filepath.Join(".local", "state", "autopr")) {
		t.Fatalf("expected PIDFile under XDG state dir, got %q", cfg.Daemon.PIDFile)
	}
}

func TestExplicitRelativePathsResolveToConfigDir(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
db_path = "my.db"
repos_root = ".repos"
log_file = "my.log"

[daemon]
pid_file = "my.pid"

[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"

  [projects.github]
  owner = "org"
  repo = "repo"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// Explicit relative paths should resolve relative to config dir (tmp).
	if cfg.DBPath != filepath.Join(tmp, "my.db") {
		t.Fatalf("expected DBPath %q, got %q", filepath.Join(tmp, "my.db"), cfg.DBPath)
	}
	if cfg.ReposRoot != filepath.Join(tmp, ".repos") {
		t.Fatalf("expected ReposRoot %q, got %q", filepath.Join(tmp, ".repos"), cfg.ReposRoot)
	}
	if cfg.LogFile != filepath.Join(tmp, "my.log") {
		t.Fatalf("expected LogFile %q, got %q", filepath.Join(tmp, "my.log"), cfg.LogFile)
	}
	if cfg.Daemon.PIDFile != filepath.Join(tmp, "my.pid") {
		t.Fatalf("expected PIDFile %q, got %q", filepath.Join(tmp, "my.pid"), cfg.Daemon.PIDFile)
	}
}

func TestXDGEnvOverridesDefaults(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"

  [projects.github]
  owner = "org"
  repo = "repo"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	customData := filepath.Join(tmp, "custom-data")
	customState := filepath.Join(tmp, "custom-state")
	t.Setenv("XDG_DATA_HOME", customData)
	t.Setenv("XDG_STATE_HOME", customState)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.DBPath != filepath.Join(customData, "autopr", "autopr.db") {
		t.Fatalf("expected DBPath under custom XDG_DATA_HOME, got %q", cfg.DBPath)
	}
	if cfg.ReposRoot != filepath.Join(customData, "autopr", "repos") {
		t.Fatalf("expected ReposRoot under custom XDG_DATA_HOME, got %q", cfg.ReposRoot)
	}
	if cfg.LogFile != filepath.Join(customState, "autopr", "autopr.log") {
		t.Fatalf("expected LogFile under custom XDG_STATE_HOME, got %q", cfg.LogFile)
	}
	if cfg.Daemon.PIDFile != filepath.Join(customState, "autopr", "autopr.pid") {
		t.Fatalf("expected PIDFile under custom XDG_STATE_HOME, got %q", cfg.Daemon.PIDFile)
	}
}

func TestLoadFailsForInvalidProvider(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[llm]
provider = "openai"

[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"

  [projects.github]
  owner = "org"
  repo = "repo"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("expected error for unsupported provider")
	}
	if !strings.Contains(err.Error(), "unsupported llm.provider") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadNormalizesGitHubIncludeLabels(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"

  [projects.github]
  owner = "org"
  repo = "repo"
  include_labels = [" AutoPR ", "BUG", "autopr", "bug"]
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	p, ok := cfg.ProjectByName("test")
	if !ok || p.GitHub == nil {
		t.Fatalf("expected github project")
	}
	got := p.GitHub.IncludeLabels
	want := []string{"autopr", "bug"}
	if len(got) != len(want) {
		t.Fatalf("expected %d labels, got %d: %#v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("label[%d]: want %q got %q", i, want[i], got[i])
		}
	}
}

func TestLoadNormalizesExcludeLabels(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"
exclude_labels = [" AUTOPR-SKIP ", "autopr-skip", "Bug", "bug"]

  [projects.github]
  owner = "org"
  repo = "repo"
  include_labels = ["autopr"]
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	p, ok := cfg.ProjectByName("test")
	if !ok || p == nil {
		t.Fatalf("expected github project")
	}
	got := p.ExcludeLabels
	want := []string{"autopr-skip", "bug"}
	if len(got) != len(want) {
		t.Fatalf("expected %d labels, got %d: %#v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("label[%d]: want %q got %q", i, want[i], got[i])
		}
	}
}

func TestLoadFailsForEmptyGitHubIncludeLabel(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"

  [projects.github]
  owner = "org"
  repo = "repo"
  include_labels = ["autopr", "   "]
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "include_labels") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadFailsForEmptyGitHubExcludeLabel(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"
exclude_labels = ["autopr-skip", "   "]

  [projects.github]
  owner = "org"
  repo = "repo"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "exclude_labels") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadDefaultsNotificationTriggers(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"

  [projects.github]
  owner = "org"
  repo = "repo"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	want := []string{
		TriggerNeedsPR,
		TriggerFailed,
		TriggerPRCreated,
		TriggerPRMerged,
	}
	if !reflect.DeepEqual(cfg.Notifications.Triggers, want) {
		t.Fatalf("expected default triggers %v, got %v", want, cfg.Notifications.Triggers)
	}
}

func TestLoadAllowsExplicitEmptyNotificationTriggers(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[notifications]
triggers = []

[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"

  [projects.github]
  owner = "org"
  repo = "repo"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Notifications.Triggers) != 0 {
		t.Fatalf("expected explicit empty trigger list to remain empty, got %v", cfg.Notifications.Triggers)
	}
}

func TestLoadFailsForInvalidNotificationTrigger(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[notifications]
triggers = ["needs_pr", "oops"]

[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"

  [projects.github]
  owner = "org"
  repo = "repo"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "notifications.triggers") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadDefaultsIncludeLabelsForGitHub(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"

  [projects.github]
  owner = "org"
  repo = "repo"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	p, ok := cfg.ProjectByName("test")
	if !ok || p.GitHub == nil {
		t.Fatalf("expected github project")
	}
	want := []string{DefaultIncludeLabel}
	if !reflect.DeepEqual(p.GitHub.IncludeLabels, want) {
		t.Fatalf("expected default include_labels %v, got %v", want, p.GitHub.IncludeLabels)
	}
}

func TestLoadDefaultsIncludeLabelsForGitLab(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[[projects]]
name = "test"
repo_url = "https://gitlab.com/org/repo.git"
test_cmd = "make test"

  [projects.gitlab]
  base_url = "https://gitlab.com"
  project_id = "123"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	p, ok := cfg.ProjectByName("test")
	if !ok || p.GitLab == nil {
		t.Fatalf("expected gitlab project")
	}
	want := []string{DefaultIncludeLabel}
	if !reflect.DeepEqual(p.GitLab.IncludeLabels, want) {
		t.Fatalf("expected default include_labels %v, got %v", want, p.GitLab.IncludeLabels)
	}
}

func TestLoadDefaultsExcludeLabelsForGitHub(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"

  [projects.github]
  owner = "org"
  repo = "repo"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	p, ok := cfg.ProjectByName("test")
	if !ok {
		t.Fatalf("expected github project")
	}
	want := []string{DefaultExcludeLabel}
	if !reflect.DeepEqual(p.ExcludeLabels, want) {
		t.Fatalf("expected default exclude_labels %v, got %v", want, p.ExcludeLabels)
	}
}

func TestLoadDefaultsExcludeLabelsForGitLab(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[[projects]]
name = "test"
repo_url = "https://gitlab.com/org/repo.git"
test_cmd = "make test"

  [projects.gitlab]
  base_url = "https://gitlab.com"
  project_id = "123"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	p, ok := cfg.ProjectByName("test")
	if !ok {
		t.Fatalf("expected gitlab project")
	}
	want := []string{DefaultExcludeLabel}
	if !reflect.DeepEqual(p.ExcludeLabels, want) {
		t.Fatalf("expected default exclude_labels %v, got %v", want, p.ExcludeLabels)
	}
}

func TestLoadDefaultsAssignedTeamForSentry(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"

  [projects.github]
  owner = "org"
  repo = "repo"

  [projects.sentry]
  org = "myorg"
  project = "myproject"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	p, ok := cfg.ProjectByName("test")
	if !ok || p.Sentry == nil {
		t.Fatalf("expected sentry project")
	}
	if p.Sentry.AssignedTeam == nil || *p.Sentry.AssignedTeam != DefaultAssignedTeam {
		t.Fatalf("expected default assigned_team %q, got %v", DefaultAssignedTeam, p.Sentry.AssignedTeam)
	}
}

func TestLoadExplicitEmptyIncludeLabelsDisablesGate(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"

  [projects.github]
  owner = "org"
  repo = "repo"
  include_labels = []
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	p, ok := cfg.ProjectByName("test")
	if !ok || p.GitHub == nil {
		t.Fatalf("expected github project")
	}
	// Explicit empty list disables label gating (normalizeLabels converts [] to nil).
	if p.GitHub.IncludeLabels != nil {
		t.Fatalf("expected nil include_labels for explicit empty, got %v", p.GitHub.IncludeLabels)
	}
}

func TestLoadExplicitEmptyExcludeLabelsDisablesGate(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"
exclude_labels = []

  [projects.github]
  owner = "org"
  repo = "repo"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	p, ok := cfg.ProjectByName("test")
	if !ok {
		t.Fatalf("expected github project")
	}
	if p.ExcludeLabels != nil {
		t.Fatalf("expected nil exclude_labels for explicit empty, got %v", p.ExcludeLabels)
	}
}

func TestLoadExplicitEmptyAssignedTeamDisablesGate(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"

  [projects.github]
  owner = "org"
  repo = "repo"

  [projects.sentry]
  org = "myorg"
  project = "myproject"
  assigned_team = ""
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	p, ok := cfg.ProjectByName("test")
	if !ok || p.Sentry == nil {
		t.Fatalf("expected sentry project")
	}
	// Explicit empty string disables team gating.
	if p.Sentry.AssignedTeam == nil || *p.Sentry.AssignedTeam != "" {
		t.Fatalf("expected empty assigned_team for explicit opt-out, got %v", p.Sentry.AssignedTeam)
	}
}

func TestLoadFailsForInvalidNotificationURL(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "autopr.toml")

	content := `
[notifications]
webhook_url = "ftp://example.com/hook"

[[projects]]
name = "test"
repo_url = "https://github.com/org/repo.git"
test_cmd = "make test"

  [projects.github]
  owner = "org"
  repo = "repo"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "notifications.webhook_url") {
		t.Fatalf("unexpected error: %v", err)
	}
}
