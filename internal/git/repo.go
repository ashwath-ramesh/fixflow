package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

const (
	redactedValue      = "[REDACTED]"
	askPassUsernameEnv = "AUTOPR_GIT_ASKPASS_USERNAME"
	askPassPasswordEnv = "AUTOPR_GIT_ASKPASS_PASSWORD"
)

var (
	credentialURLWarnings sync.Map
	urlPattern            = regexp.MustCompile(`https?://[^\s"'` + "`" + `]+`)
	knownTokenPatterns    = []*regexp.Regexp{
		regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`glpat-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`gldt-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`glcbt-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`glptt-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`oauth2:[^@/\s]+@`),
	}
)

type gitRunOptions struct {
	env     []string
	secrets []string
}

type gitAuthSession struct {
	env     []string
	secrets []string
	cleanup func()
}

type remoteURLInfo struct {
	SanitizedURL string
	Username     string
	Password     string
	Secrets      []string
}

// EnsureClone clones the repo if it doesn't exist, otherwise fetches.
func EnsureClone(ctx context.Context, repoURL, localPath, token string) error {
	if _, err := os.Stat(localPath); err == nil {
		return fetchAll(ctx, localPath, repoURL, token)
	}

	remoteInfo, err := prepareRemoteURL(repoURL)
	if err != nil {
		return err
	}
	slog.Info("cloning repository", "url", redactSensitiveText(remoteInfo.SanitizedURL, nil), "path", localPath)
	if err := os.MkdirAll(localPath, 0o755); err != nil {
		return fmt.Errorf("create repo dir: %w", err)
	}
	// Init as bare repo with origin configured so origin/* refs work with worktrees.
	if err := runGit(ctx, localPath, "init", "--bare"); err != nil {
		return err
	}
	if err := runGit(ctx, localPath, "remote", "add", "origin", remoteInfo.SanitizedURL); err != nil {
		return err
	}
	return fetchAll(ctx, localPath, remoteInfo.SanitizedURL, token)
}

// Fetch fetches all refs in the bare repo.
func Fetch(ctx context.Context, localPath, token string) error {
	remoteURL, err := getRemoteURL(ctx, localPath, "origin")
	if err != nil {
		return runGit(ctx, localPath, "fetch", "--all", "--prune")
	}
	return fetchAll(ctx, localPath, remoteURL, token)
}

func fetchAll(ctx context.Context, localPath, remoteURL, token string) error {
	authURL, auth, err := prepareGitRemoteAuth(remoteURL, token)
	if err != nil {
		return err
	}
	defer closeGitAuth(auth)

	if err := ensureRemoteSanitized(ctx, localPath, "origin", remoteURL, authURL, auth); err != nil {
		return err
	}

	slog.Info("fetching repository", "remote", redactSensitiveText(authURL, nil), "path", localPath)
	return runGitWithOptions(ctx, localPath, optionsFromAuth(auth), "fetch", "--all", "--prune")
}

// LatestCommit returns the HEAD commit SHA in the given directory.
func LatestCommit(ctx context.Context, dir string) (string, error) {
	out, err := runGitOutput(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CommitAll stages all changes (including new files) and commits with the given message.
func CommitAll(ctx context.Context, dir, message string) (string, error) {
	// Stage everything — LLM tools create new files that need to be included.
	if err := runGit(ctx, dir, "add", "-A"); err != nil {
		return "", fmt.Errorf("git add: %w", err)
	}

	// Check if there's anything to commit.
	_, err := runGitOutput(ctx, dir, "diff", "--cached", "--quiet")
	if err == nil {
		// No diff means nothing staged.
		return "", fmt.Errorf("nothing to commit")
	}

	if err := runGit(ctx, dir, "commit", "-m", message); err != nil {
		return "", fmt.Errorf("git commit: %w", err)
	}

	return LatestCommit(ctx, dir)
}

// PushBranch pushes a branch to origin.
func PushBranch(ctx context.Context, dir, branchName string) error {
	return pushBranchToRemote(ctx, dir, "origin", branchName, false, "")
}

// PushBranchWithLeaseToRemoteWithToken pushes a branch with --force-with-lease.
func PushBranchWithLeaseToRemoteWithToken(ctx context.Context, dir, remoteName, branchName, token string) error {
	return pushBranchToRemote(ctx, dir, remoteName, branchName, true, token)
}

func pushBranchToRemote(ctx context.Context, dir, remoteName, branchName string, forceWithLease bool, token string) error {
	remoteName = strings.TrimSpace(remoteName)
	branchName = strings.TrimSpace(branchName)
	if remoteName == "" {
		return fmt.Errorf("remote name is empty")
	}
	if branchName == "" {
		return fmt.Errorf("branch name is empty")
	}

	args := []string{"push", remoteName}
	if forceWithLease {
		args = append(args, "--force-with-lease")
	}
	args = append(args, branchName)

	remoteURL, err := getRemoteURL(ctx, dir, remoteName)
	if err != nil {
		return err
	}
	authURL, auth, err := prepareGitRemoteAuth(remoteURL, token)
	if err != nil {
		return err
	}
	defer closeGitAuth(auth)

	if err := ensureRemoteSanitized(ctx, dir, remoteName, remoteURL, authURL, auth); err != nil {
		return err
	}

	return runGitWithOptions(ctx, dir, optionsFromAuth(auth), args...)
}

// DeleteRemoteBranch deletes a branch from origin in the given repository.
// Callers decide whether a failure should be fatal.
func DeleteRemoteBranch(ctx context.Context, dir, branchName string) error {
	return DeleteRemoteBranchWithToken(ctx, dir, branchName, "")
}

// DeleteRemoteBranchWithToken deletes a branch from origin using optional token auth.
func DeleteRemoteBranchWithToken(ctx context.Context, dir, branchName, token string) error {
	branchName = strings.TrimSpace(branchName)
	if branchName == "" {
		return fmt.Errorf("branch name is empty")
	}

	remoteURL, err := getRemoteURL(ctx, dir, "origin")
	if err != nil {
		return err
	}
	authURL, auth, err := prepareGitRemoteAuth(remoteURL, token)
	if err != nil {
		return err
	}
	defer closeGitAuth(auth)

	if err := ensureRemoteSanitized(ctx, dir, "origin", remoteURL, authURL, auth); err != nil {
		return err
	}

	return runGitWithOptions(ctx, dir, optionsFromAuth(auth), "push", "origin", "--delete", branchName)
}

func ensureRemoteSanitized(ctx context.Context, dir, remoteName, currentURL, targetURL string, auth *gitAuthSession) error {
	if strings.TrimSpace(currentURL) == "" || strings.TrimSpace(targetURL) == "" {
		return nil
	}
	if currentURL == targetURL {
		return nil
	}
	return runGitWithOptions(ctx, dir, optionsFromAuth(auth), "remote", "set-url", remoteName, targetURL)
}

// EnsureRemote configures a named remote URL.
//
// If the remote already exists, the existing URL must match exactly.
func EnsureRemote(ctx context.Context, dir, remoteName, remoteURL string) error {
	remoteName = strings.TrimSpace(remoteName)
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteName == "" {
		return fmt.Errorf("remote name is empty")
	}
	if remoteURL == "" {
		return fmt.Errorf("remote URL is empty")
	}

	targetInfo, err := prepareRemoteURL(remoteURL)
	if err != nil {
		return err
	}

	existingRaw, errOut, err := runGitOutputAndErr(ctx, dir, "remote", "get-url", remoteName)
	if err == nil {
		existingInfo, prepErr := prepareRemoteURL(strings.TrimSpace(existingRaw))
		if prepErr != nil {
			return prepErr
		}
		if existingInfo.SanitizedURL == targetInfo.SanitizedURL {
			if strings.TrimSpace(existingRaw) != existingInfo.SanitizedURL {
				return runGit(ctx, dir, "remote", "set-url", remoteName, existingInfo.SanitizedURL)
			}
			return nil
		}
		return fmt.Errorf("remote %q exists with different URL %q", remoteName, existingInfo.SanitizedURL)
	}

	errText := strings.ToLower(strings.TrimSpace(errOut))
	if errText == "" {
		errText = strings.ToLower(err.Error())
	}
	if !isMissingGitRemoteError(errText) {
		return fmt.Errorf("get remote %q url: %w: %s", remoteName, err, errText)
	}
	return runGit(ctx, dir, "remote", "add", remoteName, targetInfo.SanitizedURL)
}

func isMissingGitRemoteError(errText string) bool {
	return strings.Contains(errText, "no such remote") ||
		strings.Contains(errText, "did not resolve to a git repository") ||
		strings.Contains(errText, "does not appear to be a git repository")
}

// CheckGitRemoteReachable checks that the given remote URL responds to ls-remote.
func CheckGitRemoteReachable(ctx context.Context, remoteURL, token string) error {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return fmt.Errorf("remote URL is empty")
	}

	authURL, auth, err := prepareGitRemoteAuth(remoteURL, token)
	if err != nil {
		return err
	}
	defer closeGitAuth(auth)

	if _, _, err := runGitOutputAndErrWithOptions(ctx, "", false, optionsFromAuth(auth), "ls-remote", authURL); err != nil {
		return fmt.Errorf("check remote reachability: %w", err)
	}
	return nil
}

func getRemoteURL(ctx context.Context, dir, remoteName string) (string, error) {
	remoteName = strings.TrimSpace(remoteName)
	if remoteName == "" {
		return "", fmt.Errorf("remote name is empty")
	}

	existingRaw, _, err := runGitOutputAndErr(ctx, dir, "remote", "get-url", remoteName)
	if err != nil {
		return "", err
	}
	existingURL := strings.TrimSpace(existingRaw)
	if existingURL == "" {
		return "", fmt.Errorf("remote %q has empty URL", remoteName)
	}
	return existingURL, nil
}

func prepareRemoteURL(remoteURL string) (remoteURLInfo, error) {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return remoteURLInfo{}, fmt.Errorf("remote URL is empty")
	}
	if !isHTTPRemoteURL(remoteURL) {
		return remoteURLInfo{SanitizedURL: remoteURL}, nil
	}

	parsed, err := url.Parse(remoteURL)
	if err != nil {
		return remoteURLInfo{}, fmt.Errorf("parse remote URL: %w", err)
	}

	info := remoteURLInfo{}
	if parsed.User != nil {
		warnCredentialURL(remoteURL)
		info.Username = parsed.User.Username()
		if pass, ok := parsed.User.Password(); ok {
			info.Password = pass
		}
		info.Secrets = dedupeNonEmpty(info.Username, info.Password)
		parsed.User = nil
	}
	info.SanitizedURL = parsed.String()
	return info, nil
}

func prepareGitRemoteAuth(remoteURL, token string) (string, *gitAuthSession, error) {
	remoteInfo, err := prepareRemoteURL(remoteURL)
	if err != nil {
		return "", nil, err
	}

	if !isHTTPRemoteURL(remoteInfo.SanitizedURL) {
		return remoteInfo.SanitizedURL, &gitAuthSession{secrets: dedupeNonEmpty(append(remoteInfo.Secrets, token)...)}, nil
	}

	username := "oauth2"
	password := strings.TrimSpace(token)
	if password == "" {
		if remoteInfo.Password != "" {
			password = remoteInfo.Password
			if remoteInfo.Username != "" {
				username = remoteInfo.Username
			}
		} else if remoteInfo.Username != "" {
			// Compatibility fallback for URLs like https://TOKEN@host/repo.git
			password = remoteInfo.Username
		}
	}

	allSecrets := dedupeNonEmpty(append(remoteInfo.Secrets, token, password)...)
	if password == "" {
		return remoteInfo.SanitizedURL, &gitAuthSession{secrets: allSecrets}, nil
	}

	scriptPath, err := writeAskPassScript()
	if err != nil {
		return "", nil, fmt.Errorf("create askpass script: %w", err)
	}
	auth := &gitAuthSession{
		env: []string{
			"GIT_TERMINAL_PROMPT=0",
			"GIT_ASKPASS=" + scriptPath,
			askPassUsernameEnv + "=" + username,
			askPassPasswordEnv + "=" + password,
		},
		secrets: allSecrets,
		cleanup: func() {
			_ = os.Remove(scriptPath)
		},
	}
	return remoteInfo.SanitizedURL, auth, nil
}

func optionsFromAuth(auth *gitAuthSession) gitRunOptions {
	if auth == nil {
		return gitRunOptions{}
	}
	return gitRunOptions{env: auth.env, secrets: auth.secrets}
}

func closeGitAuth(auth *gitAuthSession) {
	if auth != nil && auth.cleanup != nil {
		auth.cleanup()
	}
}

func writeAskPassScript() (string, error) {
	if runtime.GOOS == "windows" {
		return writeAskPassScriptWindows()
	}
	return writeAskPassScriptPOSIX()
}

func writeAskPassScriptPOSIX() (string, error) {
	f, err := os.CreateTemp("", "autopr-git-askpass-*.sh")
	if err != nil {
		return "", err
	}
	path := f.Name()
	script := "#!/bin/sh\n" +
		"prompt=\"$1\"\n" +
		"case \"$prompt\" in\n" +
		"  *Username*|*username*) printf '%s\\n' \"$" + askPassUsernameEnv + "\" ;;\n" +
		"  *) printf '%s\\n' \"$" + askPassPasswordEnv + "\" ;;\n" +
		"esac\n"
	if _, err := f.WriteString(script); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func writeAskPassScriptWindows() (string, error) {
	f, err := os.CreateTemp("", "autopr-git-askpass-*.cmd")
	if err != nil {
		return "", err
	}
	path := f.Name()
	script := "@echo off\r\n" +
		"set prompt=%~1\r\n" +
		"echo %prompt%| findstr /I \"username\" >nul\r\n" +
		"if %errorlevel%==0 (\r\n" +
		"  echo %" + askPassUsernameEnv + "%\r\n" +
		") else (\r\n" +
		"  echo %" + askPassPasswordEnv + "%\r\n" +
		")\r\n"
	if _, err := f.WriteString(script); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func warnCredentialURL(remoteURL string) {
	trimmed := strings.TrimSpace(remoteURL)
	if trimmed == "" || !isHTTPRemoteURL(trimmed) {
		return
	}

	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || parsed.User == nil {
		return
	}
	parsed.User = nil
	key := parsed.String()
	if _, loaded := credentialURLWarnings.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	slog.Warn("git remote URL contains embedded credentials; use credentials.toml or env token instead", "url", redactSensitiveText(trimmed, nil))
}

func isHTTPRemoteURL(raw string) bool {
	return strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://")
}

func runGit(ctx context.Context, dir string, args ...string) error {
	return runGitWithOptions(ctx, dir, gitRunOptions{}, args...)
}

func runGitWithOptions(ctx context.Context, dir string, opts gitRunOptions, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(opts.env) > 0 {
		cmd.Env = append(cmd.Environ(), opts.env...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return formatGitCommandError(args, out, err, opts.secrets)
	}
	return nil
}

func runGitOutputAndErr(ctx context.Context, dir string, args ...string) (string, string, error) {
	return runGitOutputAndErrWithOptions(ctx, dir, false, gitRunOptions{}, args...)
}

func runGitOutputAndErrWithNoEditor(ctx context.Context, dir string, args ...string) (string, string, error) {
	return runGitOutputAndErrWithOptions(ctx, dir, true, gitRunOptions{}, args...)
}

func runGitOutputAndErrWithOptions(ctx context.Context, dir string, noEditor bool, opts gitRunOptions, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if noEditor {
		opts.env = append(opts.env, "GIT_EDITOR=true")
	}
	if len(opts.env) > 0 {
		cmd.Env = append(cmd.Environ(), opts.env...)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return redactSensitiveText(stdout.String(), opts.secrets), redactSensitiveText(stderr.String(), opts.secrets), nil
	}
	return redactSensitiveText(stdout.String(), opts.secrets), redactSensitiveText(stderr.String(), opts.secrets), err
}

func runGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	return runGitOutputWithOptions(ctx, dir, gitRunOptions{}, args...)
}

func runGitOutputWithOptions(ctx context.Context, dir string, opts gitRunOptions, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(opts.env) > 0 {
		cmd.Env = append(cmd.Environ(), opts.env...)
	}
	out, err := cmd.Output()
	if err != nil {
		msg := out
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			if len(msg) > 0 && msg[len(msg)-1] != '\n' {
				msg = append(msg, '\n')
			}
			msg = append(msg, exitErr.Stderr...)
		}
		return "", formatGitCommandError(args, msg, err, opts.secrets)
	}
	return redactSensitiveText(string(out), opts.secrets), nil
}

func formatGitCommandError(args []string, out []byte, err error, secrets []string) error {
	cmdText := redactSensitiveText(strings.Join(args, " "), secrets)
	msg := strings.TrimSpace(redactSensitiveText(string(out), secrets))
	if msg != "" {
		return fmt.Errorf("git %s: %w: %s", cmdText, err, msg)
	}
	return fmt.Errorf("git %s: %w", cmdText, err)
}

func redactSensitiveText(msg string, secrets []string) string {
	if msg == "" {
		return msg
	}
	redacted := msg
	for _, secret := range dedupeNonEmpty(secrets...) {
		redacted = strings.ReplaceAll(redacted, secret, redactedValue)
	}
	redacted = redactURLUserInfo(redacted)
	for _, pattern := range knownTokenPatterns {
		redacted = pattern.ReplaceAllString(redacted, redactedValue)
	}
	return redacted
}

func redactURLUserInfo(msg string) string {
	return urlPattern.ReplaceAllStringFunc(msg, func(match string) string {
		parsed, err := url.Parse(match)
		if err != nil {
			return match
		}
		if parsed.User == nil {
			return match
		}
		parsed.User = nil
		return parsed.String()
	})
}

func dedupeNonEmpty(values ...string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
