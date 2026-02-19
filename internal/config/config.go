package config

import (
	"bytes"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const Version = "0.1.0"

// Credentials holds tokens loaded from credentials.toml.
type Credentials struct {
	GitHubToken   string `toml:"github_token"`
	GitLabToken   string `toml:"gitlab_token"`
	SentryToken   string `toml:"sentry_token"`
	WebhookSecret string `toml:"webhook_secret"`
}

// LoadCredentials reads credentials.toml. Returns an empty Credentials if
// the file does not exist. Warns if the file has insecure permissions.
func LoadCredentials() (*Credentials, error) {
	path, err := CredentialsPath()
	if err != nil {
		return &Credentials{}, nil
	}

	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return &Credentials{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat credentials: %w", err)
	}

	// Warn on insecure permissions (anything beyond owner read/write).
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		slog.Warn("credentials file has insecure permissions",
			"path", path, "mode", fmt.Sprintf("%04o", perm))
	}

	creds := &Credentials{}
	if _, err := toml.DecodeFile(path, creds); err != nil {
		return nil, fmt.Errorf("decode credentials %s: %w", path, err)
	}
	return creds, nil
}

// SaveCredentials writes credentials.toml with 0600 permissions.
func SaveCredentials(creds *Credentials) error {
	path, err := CredentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(creds); err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), fs.FileMode(0o600)); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	return nil
}

type Config struct {
	DBPath    string `toml:"db_path"`
	ReposRoot string `toml:"repos_root"`
	LogLevel  string `toml:"log_level"`
	LogFile   string `toml:"log_file"`

	Daemon        DaemonConfig        `toml:"daemon"`
	Tokens        TokensConfig        `toml:"tokens"`
	Sentry        SentryConfig        `toml:"sentry"`
	LLM           LLMConfig           `toml:"llm"`
	Notifications NotificationsConfig `toml:"notifications"`

	Projects []ProjectConfig `toml:"projects"`

	// Resolved at runtime (not in TOML).
	BaseDir string `toml:"-"`
}

type DaemonConfig struct {
	WebhookPort   int    `toml:"webhook_port"`
	WebhookSecret string `toml:"webhook_secret"`
	MaxWorkers    int    `toml:"max_workers"`
	MaxIterations int    `toml:"max_iterations"`
	SyncInterval  string `toml:"sync_interval"`
	PIDFile       string `toml:"pid_file"`
	AutoPR        bool   `toml:"auto_pr"`
}

type TokensConfig struct {
	GitLab string `toml:"gitlab"`
	GitHub string `toml:"github"`
	Sentry string `toml:"sentry"`
}

type SentryConfig struct {
	BaseURL string `toml:"base_url"`
}

type LLMConfig struct {
	Provider string `toml:"provider"`
}

type NotificationsConfig struct {
	WebhookURL   string   `toml:"webhook_url"`
	SlackWebhook string   `toml:"slack_webhook"`
	Desktop      bool     `toml:"desktop"`
	Triggers     []string `toml:"triggers"`
}

const (
	TriggerNeedsPR  = "needs_pr"
	TriggerFailed   = "failed"
	TriggerPRCreated = "pr_created"
	TriggerPRMerged  = "pr_merged"
)

var defaultNotificationTriggers = []string{
	TriggerNeedsPR,
	TriggerFailed,
	TriggerPRCreated,
	TriggerPRMerged,
}

type ProjectConfig struct {
	Name       string          `toml:"name"`
	RepoURL    string          `toml:"repo_url"`
	TestCmd    string          `toml:"test_cmd"`
	BaseBranch string          `toml:"base_branch"`
	GitLab     *ProjectGitLab  `toml:"gitlab"`
	GitHub     *ProjectGitHub  `toml:"github"`
	Sentry     *ProjectSentry  `toml:"sentry"`
	Prompts    *ProjectPrompts `toml:"prompts"`
}

type ProjectGitLab struct {
	BaseURL       string   `toml:"base_url"`
	ProjectID     string   `toml:"project_id"`
	IncludeLabels []string `toml:"include_labels"`
}

type ProjectGitHub struct {
	Owner         string   `toml:"owner"`
	Repo          string   `toml:"repo"`
	IncludeLabels []string `toml:"include_labels"`
}

type ProjectSentry struct {
	Org          string  `toml:"org"`
	Project      string  `toml:"project"`
	AssignedTeam *string `toml:"assigned_team"`
}

// DefaultLabel is the default label gate applied to GitHub and GitLab
// issue sources when include_labels is not configured. Set include_labels = []
// in config to explicitly disable label gating.
const DefaultLabel = "autopr"

// DefaultAssignedTeam is the default Sentry team gate applied when
// assigned_team is not configured. Set assigned_team = "" in config to
// explicitly disable team gating.
const DefaultAssignedTeam = "autopr"

type ProjectPrompts struct {
	Plan       string `toml:"plan"`
	PlanReview string `toml:"plan_review"`
	Implement  string `toml:"implement"`
	CodeReview string `toml:"code_review"`
}

func Load(path string) (*Config, error) {
	cfg := &Config{}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	cfg.BaseDir = filepath.Dir(path)
	// Snapshot tokens from config file before credentials/env are merged in.
	fileTokens := cfg.Tokens
	applyDefaults(cfg)
	applyCredentialsAndEnv(cfg)
	warnTokensInFile(fileTokens)
	if err := validate(cfg); err != nil {
		return nil, err
	}
	resolvePaths(cfg)
	return cfg, nil
}

// LoadMinimal loads config without running validate(). Used by `ap init`
// where projects may not be configured yet.
func LoadMinimal(path string) (*Config, error) {
	cfg := &Config{}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	cfg.BaseDir = filepath.Dir(path)
	applyDefaults(cfg)
	applyCredentialsAndEnv(cfg)
	resolvePaths(cfg)
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.DBPath == "" {
		if d, err := DataDir(); err == nil {
			cfg.DBPath = filepath.Join(d, "autopr.db")
		} else {
			cfg.DBPath = "autopr.db"
		}
	}
	if cfg.ReposRoot == "" {
		if d, err := DataDir(); err == nil {
			cfg.ReposRoot = filepath.Join(d, "repos")
		} else {
			cfg.ReposRoot = ".repos"
		}
	}
	if cfg.LogFile == "" {
		if d, err := StateDir(); err == nil {
			cfg.LogFile = filepath.Join(d, "autopr.log")
		} else {
			cfg.LogFile = "autopr.log"
		}
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	if cfg.Daemon.WebhookPort == 0 {
		cfg.Daemon.WebhookPort = 9847
	}
	if cfg.Daemon.MaxWorkers == 0 {
		cfg.Daemon.MaxWorkers = 3
	}
	if cfg.Daemon.MaxIterations == 0 {
		cfg.Daemon.MaxIterations = 3
	}
	if cfg.Daemon.SyncInterval == "" {
		cfg.Daemon.SyncInterval = "5m"
	}
	if cfg.Daemon.PIDFile == "" {
		if d, err := StateDir(); err == nil {
			cfg.Daemon.PIDFile = filepath.Join(d, "autopr.pid")
		} else {
			cfg.Daemon.PIDFile = "autopr.pid"
		}
	}
	if cfg.Sentry.BaseURL == "" {
		cfg.Sentry.BaseURL = "https://sentry.io"
	}
	if cfg.LLM.Provider == "" {
		cfg.LLM.Provider = "codex"
	}
	if cfg.Notifications.Triggers == nil {
		cfg.Notifications.Triggers = slices.Clone(defaultNotificationTriggers)
	}
	for i := range cfg.Projects {
		if cfg.Projects[i].BaseBranch == "" {
			cfg.Projects[i].BaseBranch = "main"
		}
		// Safe defaults: require "autopr" label/team unless explicitly overridden.
		if cfg.Projects[i].GitHub != nil && cfg.Projects[i].GitHub.IncludeLabels == nil {
			cfg.Projects[i].GitHub.IncludeLabels = []string{DefaultLabel}
		}
		if cfg.Projects[i].GitLab != nil && cfg.Projects[i].GitLab.IncludeLabels == nil {
			cfg.Projects[i].GitLab.IncludeLabels = []string{DefaultLabel}
		}
		if cfg.Projects[i].Sentry != nil && cfg.Projects[i].Sentry.AssignedTeam == nil {
			defaultTeam := DefaultAssignedTeam
			cfg.Projects[i].Sentry.AssignedTeam = &defaultTeam
		}
	}
}

// applyCredentialsAndEnv merges token values from credentials.toml and then
// from environment variables. Priority (highest → lowest): env > credentials.toml > config file.
func applyCredentialsAndEnv(cfg *Config) {
	// Layer credentials.toml on top of config file values.
	creds, err := LoadCredentials()
	if err != nil {
		slog.Warn("failed to load credentials", "error", err)
	}
	if creds != nil {
		if creds.GitHubToken != "" {
			cfg.Tokens.GitHub = creds.GitHubToken
		}
		if creds.GitLabToken != "" {
			cfg.Tokens.GitLab = creds.GitLabToken
		}
		if creds.SentryToken != "" {
			cfg.Tokens.Sentry = creds.SentryToken
		}
		if creds.WebhookSecret != "" {
			cfg.Daemon.WebhookSecret = creds.WebhookSecret
		}
	}

	// Env vars win over everything.
	if v := os.Getenv("AUTOPR_WEBHOOK_SECRET"); v != "" {
		cfg.Daemon.WebhookSecret = v
	}
	if v := os.Getenv("GITLAB_TOKEN"); v != "" {
		cfg.Tokens.GitLab = v
	}
	if v := os.Getenv("GITHUB_TOKEN"); v != "" {
		cfg.Tokens.GitHub = v
	}
	if v := os.Getenv("SENTRY_TOKEN"); v != "" {
		cfg.Tokens.Sentry = v
	}
}

// warnTokensInFile warns only when a token was literally written in config.toml.
// It receives the token values as they were before credentials.toml and env vars
// were merged, so tokens from credentials.toml don't trigger a false positive.
func warnTokensInFile(fileTokens TokensConfig) {
	if fileTokens.GitLab != "" {
		slog.Warn("gitlab token found in config file; prefer credentials.toml or GITLAB_TOKEN env var")
	}
	if fileTokens.GitHub != "" {
		slog.Warn("github token found in config file; prefer credentials.toml or GITHUB_TOKEN env var")
	}
	if fileTokens.Sentry != "" {
		slog.Warn("sentry token found in config file; prefer credentials.toml or SENTRY_TOKEN env var")
	}
}

func validate(cfg *Config) error {
	switch cfg.LLM.Provider {
	case "claude", "codex":
	default:
		return fmt.Errorf("unsupported llm.provider: %q (must be claude or codex)", cfg.LLM.Provider)
	}
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("unsupported log_level: %q", cfg.LogLevel)
	}
	if _, err := time.ParseDuration(cfg.Daemon.SyncInterval); err != nil {
		return fmt.Errorf("invalid daemon.sync_interval %q: %w", cfg.Daemon.SyncInterval, err)
	}
	normalizedTriggers, err := validateNotificationsConfig(cfg.Notifications)
	if err != nil {
		return err
	}
	cfg.Notifications.Triggers = normalizedTriggers
	if len(cfg.Projects) == 0 {
		return fmt.Errorf("at least one [[projects]] entry is required")
	}
	for i, p := range cfg.Projects {
		if p.Name == "" {
			return fmt.Errorf("projects[%d]: name is required", i)
		}
		if p.RepoURL == "" {
			return fmt.Errorf("project %q: repo_url is required", p.Name)
		}
		if p.TestCmd == "" {
			return fmt.Errorf("project %q: test_cmd is required", p.Name)
		}
		if p.GitLab == nil && p.GitHub == nil && p.Sentry == nil {
			return fmt.Errorf("project %q: at least one source (gitlab/github/sentry) is required", p.Name)
		}
		if p.GitHub != nil {
			normalized, err := normalizeLabels(p.GitHub.IncludeLabels)
			if err != nil {
				return fmt.Errorf("project %q github.include_labels: %w", p.Name, err)
			}
			cfg.Projects[i].GitHub.IncludeLabels = normalized
		}
		if p.GitLab != nil {
			normalized, err := normalizeLabels(p.GitLab.IncludeLabels)
			if err != nil {
				return fmt.Errorf("project %q gitlab.include_labels: %w", p.Name, err)
			}
			cfg.Projects[i].GitLab.IncludeLabels = normalized
		}
	}
	return nil
}

func validateNotificationsConfig(cfg NotificationsConfig) ([]string, error) {
	if cfg.WebhookURL != "" {
		if err := validateWebhookURL(cfg.WebhookURL); err != nil {
			return nil, fmt.Errorf("invalid notifications.webhook_url: %w", err)
		}
	}
	if cfg.SlackWebhook != "" {
		if err := validateWebhookURL(cfg.SlackWebhook); err != nil {
			return nil, fmt.Errorf("invalid notifications.slack_webhook: %w", err)
		}
	}
	normalized, err := normalizeTriggers(cfg.Triggers)
	if err != nil {
		return nil, fmt.Errorf("invalid notifications.triggers: %w", err)
	}
	return normalized, nil
}

func validateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("must use http or https")
	}
	if u.Host == "" {
		return fmt.Errorf("host is required")
	}
	return nil
}

func normalizeTriggers(triggers []string) ([]string, error) {
	out := make([]string, 0, len(triggers))
	seen := make(map[string]struct{}, len(triggers))
	for i, trigger := range triggers {
		normalized := strings.ToLower(strings.TrimSpace(trigger))
		if normalized == "" {
			return nil, fmt.Errorf("trigger at index %d is empty", i)
		}
		if !isValidTrigger(normalized) {
			return nil, fmt.Errorf("unsupported trigger %q", normalized)
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}

func isValidTrigger(trigger string) bool {
	switch trigger {
	case TriggerNeedsPR, TriggerFailed, TriggerPRCreated, TriggerPRMerged:
		return true
	default:
		return false
	}
}

func normalizeLabels(labels []string) ([]string, error) {
	if len(labels) == 0 {
		return nil, nil
	}

	out := make([]string, 0, len(labels))
	seen := make(map[string]struct{}, len(labels))
	for i, label := range labels {
		normalized := strings.ToLower(strings.TrimSpace(label))
		if normalized == "" {
			return nil, fmt.Errorf("label at index %d is empty", i)
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}

func resolvePaths(cfg *Config) {
	cfg.DBPath = absPath(cfg.BaseDir, cfg.DBPath)
	cfg.ReposRoot = absPath(cfg.BaseDir, cfg.ReposRoot)
	cfg.Daemon.PIDFile = absPath(cfg.BaseDir, cfg.Daemon.PIDFile)
	if cfg.LogFile != "" {
		cfg.LogFile = absPath(cfg.BaseDir, cfg.LogFile)
	}
	for i := range cfg.Projects {
		p := &cfg.Projects[i]
		if p.Prompts != nil {
			if p.Prompts.Plan != "" {
				p.Prompts.Plan = absPath(cfg.BaseDir, p.Prompts.Plan)
			}
			if p.Prompts.PlanReview != "" {
				p.Prompts.PlanReview = absPath(cfg.BaseDir, p.Prompts.PlanReview)
			}
			if p.Prompts.Implement != "" {
				p.Prompts.Implement = absPath(cfg.BaseDir, p.Prompts.Implement)
			}
			if p.Prompts.CodeReview != "" {
				p.Prompts.CodeReview = absPath(cfg.BaseDir, p.Prompts.CodeReview)
			}
		}
	}
}

func absPath(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

func (cfg *Config) ProjectByName(name string) (*ProjectConfig, bool) {
	for i := range cfg.Projects {
		if cfg.Projects[i].Name == name {
			return &cfg.Projects[i], true
		}
	}
	return nil, false
}

func (cfg *Config) SlogLevel() slog.Level {
	switch cfg.LogLevel {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// LocalRepoPath returns the local clone path for a project.
func (cfg *Config) LocalRepoPath(projectName string) string {
	return filepath.Join(cfg.ReposRoot, sanitize(projectName))
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "default"
	}
	return out
}
