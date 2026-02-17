package config

import (
	"os"
	"path/filepath"
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
