package git

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestPushBranchWithLeaseToRemoteWithTokenIncludesStderrInError(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	remote := filepath.Join(tmp, "remote.git")
	runGitCmd(t, "", "init", "--bare", remote)

	repo := filepath.Join(tmp, "repo")
	runGitCmd(t, "", "init", repo)
	runGitCmd(t, repo, "remote", "add", "origin", remote)

	err := PushBranchWithLeaseToRemoteWithToken(ctx, repo, "origin", "missing-branch", "")
	if err == nil {
		t.Fatal("expected push failure")
	}
	msg := err.Error()
	if !strings.Contains(msg, "src refspec") || !strings.Contains(msg, "missing-branch") {
		t.Fatalf("expected stderr details in error, got: %v", err)
	}
}

func TestPushBranchWithLeaseToRemoteWithTokenWritesToNamedRemote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	remote := filepath.Join(tmp, "remote.git")
	runGitCmd(t, "", "init", "--bare", remote)

	repo := filepath.Join(tmp, "repo")
	runGitCmd(t, "", "init", repo)
	runGitCmd(t, repo, "config", "user.email", "test@example.com")
	runGitCmd(t, repo, "config", "user.name", "Test User")
	runGitCmd(t, repo, "remote", "add", "upstream", remote)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGitCmd(t, repo, "add", "README.md")
	runGitCmd(t, repo, "commit", "-m", "init")
	runGitCmd(t, repo, "branch", "-M", "main")

	destination := filepath.Join(tmp, "fork.git")
	runGitCmd(t, "", "init", "--bare", destination)

	if err := PushBranchWithLeaseToRemoteWithToken(ctx, repo, "origin", "main", ""); err == nil {
		t.Fatal("expected push to missing origin to fail")
	}

	if err := PushBranchWithLeaseToRemoteWithToken(ctx, repo, "upstream", "main", ""); err != nil {
		t.Fatalf("push to upstream: %v", err)
	}

	if err := PushBranchWithLeaseToRemoteWithToken(ctx, repo, "destination", "main", ""); err == nil {
		t.Fatal("expected push to destination to fail before remote exists")
	}
	runGitCmd(t, repo, "remote", "add", "destination", destination)
	if err := PushBranchWithLeaseToRemoteWithToken(ctx, repo, "destination", "main", ""); err != nil {
		t.Fatalf("push to destination: %v", err)
	}

	out := runGitCmdOutput(t, "", "ls-remote", "--heads", destination, "main")
	if !strings.Contains(out, "refs/heads/main") {
		t.Fatalf("expected branch pushed to destination, got %q", out)
	}
}

func TestEnsureRemoteAddsAndMatchesExistingURL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	remoteURL := filepath.Join(tmp, "origin.git")
	repo := filepath.Join(tmp, "repo")
	runGitCmd(t, "", "init", "--bare", remoteURL)
	runGitCmd(t, "", "init", repo)

	if err := EnsureRemote(ctx, repo, "origin", remoteURL); err != nil {
		t.Fatalf("add origin remote: %v", err)
	}
	if err := EnsureRemote(ctx, repo, "origin", remoteURL); err != nil {
		t.Fatalf("ensure matching origin remote: %v", err)
	}
}

func TestEnsureRemoteRejectsDifferentURL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()
	remoteA := filepath.Join(tmp, "a.git")
	remoteB := filepath.Join(tmp, "b.git")
	repo := filepath.Join(tmp, "repo")
	runGitCmd(t, "", "init", "--bare", remoteA)
	runGitCmd(t, "", "init", "--bare", remoteB)
	runGitCmd(t, "", "init", repo)
	if err := runGitCmdOutputErr(t, repo, "remote", "add", "origin", remoteA); err != nil {
		t.Fatalf("add remote: %v", err)
	}
	if err := EnsureRemote(ctx, repo, "origin", remoteB); err == nil {
		t.Fatalf("expected different URL error")
	}
}

func TestCheckGitRemoteReachable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	emptyRemote := filepath.Join(tmp, "empty.git")
	runGitCmd(t, "", "init", "--bare", emptyRemote)

	if err := CheckGitRemoteReachable(ctx, emptyRemote, ""); err != nil {
		t.Fatalf("expected empty bare remote to be reachable: %v", err)
	}

	seed := filepath.Join(tmp, "seed")
	runGitCmd(t, "", "init", seed)
	runGitCmd(t, seed, "config", "user.email", "test@example.com")
	runGitCmd(t, seed, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	runGitCmd(t, seed, "add", "README.md")
	runGitCmd(t, seed, "commit", "-m", "init")
	runGitCmd(t, seed, "branch", "-M", "main")

	remote := filepath.Join(tmp, "remote.git")
	runGitCmd(t, "", "init", "--bare", remote)
	runGitCmd(t, seed, "remote", "add", "origin", remote)
	runGitCmd(t, seed, "push", "origin", "main")

	if err := CheckGitRemoteReachable(ctx, remote, ""); err != nil {
		t.Fatalf("expected reachable local remote: %v", err)
	}
	if err := CheckGitRemoteReachable(ctx, filepath.Join(tmp, "missing.git"), ""); err == nil {
		t.Fatal("expected unreachable remote error")
	}
}

func TestDeleteRemoteBranchSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	remote := filepath.Join(tmp, "remote.git")
	runGitCmd(t, "", "init", "--bare", remote)

	repo := filepath.Join(tmp, "repo")
	runGitCmd(t, "", "init", repo)
	runGitCmd(t, repo, "config", "user.email", "test@example.com")
	runGitCmd(t, repo, "config", "user.name", "Test User")
	runGitCmd(t, repo, "remote", "add", "origin", remote)
	runGitCmd(t, repo, "checkout", "-B", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGitCmd(t, repo, "add", "README.md")
	runGitCmd(t, repo, "commit", "-m", "init")
	runGitCmd(t, repo, "push", "origin", "main")
	runGitCmd(t, repo, "checkout", "-b", "autopr/test-delete")
	runGitCmd(t, repo, "push", "origin", "autopr/test-delete")

	if err := DeleteRemoteBranchWithToken(ctx, repo, "autopr/test-delete", ""); err != nil {
		t.Fatalf("delete remote branch: %v", err)
	}

	cmd := exec.Command("git", "ls-remote", "--heads", remote, "autopr/test-delete")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ls-remote remote: %v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("expected branch to be deleted on remote, got: %s", strings.TrimSpace(string(out)))
	}
}

func TestDeleteRemoteBranchFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	remote := filepath.Join(tmp, "remote.git")
	runGitCmd(t, "", "init", "--bare", remote)

	repo := filepath.Join(tmp, "repo")
	runGitCmd(t, "", "init", repo)
	runGitCmd(t, repo, "config", "user.email", "test@example.com")
	runGitCmd(t, repo, "config", "user.name", "Test User")
	runGitCmd(t, repo, "remote", "add", "origin", remote)
	runGitCmd(t, repo, "checkout", "-B", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGitCmd(t, repo, "add", "README.md")
	runGitCmd(t, repo, "commit", "-m", "init")
	runGitCmd(t, repo, "push", "origin", "main")

	if err := DeleteRemoteBranchWithToken(ctx, repo, "autopr/does-not-exist", ""); err == nil {
		t.Fatalf("expected delete remote branch failure")
	}
}

func TestPrepareRemoteURLEdgeCases(t *testing.T) {
	t.Parallel()

	sshInfo, err := prepareRemoteURL("git@github.com:acme/repo.git")
	if err != nil {
		t.Fatalf("prepare ssh url: %v", err)
	}
	if sshInfo.SanitizedURL != "git@github.com:acme/repo.git" {
		t.Fatalf("unexpected ssh URL: %q", sshInfo.SanitizedURL)
	}

	usernameOnly, err := prepareRemoteURL("https://token@example.com/acme/repo.git")
	if err != nil {
		t.Fatalf("prepare username-only url: %v", err)
	}
	if usernameOnly.SanitizedURL != "https://example.com/acme/repo.git" {
		t.Fatalf("unexpected sanitized url: %q", usernameOnly.SanitizedURL)
	}
	if usernameOnly.Username != "token" || usernameOnly.Password != "" {
		t.Fatalf("unexpected credentials: user=%q pass=%q", usernameOnly.Username, usernameOnly.Password)
	}

	specialPass, err := prepareRemoteURL("https://oauth2:pa%3Ass%40word@example.com/acme/repo.git")
	if err != nil {
		t.Fatalf("prepare url with encoded password: %v", err)
	}
	if specialPass.Password != "pa:ss@word" {
		t.Fatalf("expected decoded password, got %q", specialPass.Password)
	}
}

func TestPrepareGitRemoteAuthHTTPSUsesAskPass(t *testing.T) {
	t.Parallel()

	token := "ghp_abcdefghijklmnopqrstuvwxyz123456"
	authURL, auth, err := prepareGitRemoteAuth("https://github.com/acme/repo.git", token)
	if err != nil {
		t.Fatalf("prepare auth: %v", err)
	}
	if authURL != "https://github.com/acme/repo.git" {
		t.Fatalf("unexpected auth URL: %q", authURL)
	}
	if auth == nil || len(auth.env) == 0 {
		t.Fatalf("expected askpass environment to be configured")
	}
	defer closeGitAuth(auth)

	joined := strings.Join(auth.env, "\n")
	if !strings.Contains(joined, "GIT_ASKPASS=") {
		t.Fatalf("expected GIT_ASKPASS in env: %q", joined)
	}
	if !strings.Contains(joined, askPassPasswordEnv+"="+token) {
		t.Fatalf("expected token in askpass env")
	}
}

func TestPrepareGitRemoteAuthGitLabUsesAskPass(t *testing.T) {
	t.Parallel()

	token := "glpat-abcdefghijklmnopqrstuvwxyz1234"
	authURL, auth, err := prepareGitRemoteAuth("https://gitlab.com/acme/repo.git", token)
	if err != nil {
		t.Fatalf("prepare auth: %v", err)
	}
	if authURL != "https://gitlab.com/acme/repo.git" {
		t.Fatalf("unexpected auth URL: %q", authURL)
	}
	if auth == nil || len(auth.env) == 0 {
		t.Fatalf("expected askpass environment to be configured")
	}
	defer closeGitAuth(auth)

	joined := strings.Join(auth.env, "\n")
	if !strings.Contains(joined, "GIT_ASKPASS=") {
		t.Fatalf("expected GIT_ASKPASS in env: %q", joined)
	}
	if !strings.Contains(joined, askPassPasswordEnv+"="+token) {
		t.Fatalf("expected token in askpass env")
	}
}

func TestPrepareGitRemoteAuthCredentialURLFallback(t *testing.T) {
	t.Parallel()

	authURL, auth, err := prepareGitRemoteAuth("https://oauth2:legacy-token@example.com/acme/repo.git", "")
	if err != nil {
		t.Fatalf("prepare auth: %v", err)
	}
	if authURL != "https://example.com/acme/repo.git" {
		t.Fatalf("unexpected sanitized auth URL: %q", authURL)
	}
	if auth == nil || len(auth.env) == 0 {
		t.Fatalf("expected askpass from credential fallback")
	}
	defer closeGitAuth(auth)

	joined := strings.Join(auth.env, "\n")
	if !strings.Contains(joined, askPassPasswordEnv+"=legacy-token") {
		t.Fatalf("expected legacy password token in askpass env")
	}
}

func TestPrepareGitRemoteAuthNonHTTPSNoAskPass(t *testing.T) {
	t.Parallel()

	authURL, auth, err := prepareGitRemoteAuth("git@github.com:acme/repo.git", "ghp_abcdefghijklmnopqrstuvwxyz123456")
	if err != nil {
		t.Fatalf("prepare auth: %v", err)
	}
	if authURL != "git@github.com:acme/repo.git" {
		t.Fatalf("unexpected auth URL: %q", authURL)
	}
	if auth == nil {
		t.Fatalf("expected auth session object")
	}
	if len(auth.env) != 0 {
		t.Fatalf("expected no askpass env for non-HTTPS URL, got: %v", auth.env)
	}
}

func TestRedactSensitiveText(t *testing.T) {
	t.Parallel()

	token := "ghp_abcdefghijklmnopqrstuvwxyz123456"
	in := "clone https://oauth2:my-secret@example.com/acme/repo.git using " + token +
		" and glpat-abcdefghijklmnopqrstuv and gldt-abcdefghijklmnopqrstuv and glcbt-abcdefghijklmnopqrstuv and glptt-abcdefghijklmnopqrstuv"
	out := redactSensitiveText(in, []string{"my-secret", token})
	if strings.Contains(out, "my-secret") {
		t.Fatalf("expected embedded secret to be redacted, got: %q", out)
	}
	if strings.Contains(out, token) {
		t.Fatalf("expected token to be redacted, got: %q", out)
	}
	for _, leaked := range []string{"glpat-abcdefghijklmnopqrstuv", "gldt-abcdefghijklmnopqrstuv", "glcbt-abcdefghijklmnopqrstuv", "glptt-abcdefghijklmnopqrstuv"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("expected known token pattern redaction for %q, got: %q", leaked, out)
		}
	}
	if strings.Contains(out, "oauth2:") {
		t.Fatalf("expected URL userinfo to be stripped, got: %q", out)
	}
}

func TestRedactURLUserInfoEdgeCases(t *testing.T) {
	t.Parallel()

	in := "x https://user:pass@example.com:8443/a/b?x=1#frag y https://a:b@host.local/path"
	out := redactURLUserInfo(in)
	if strings.Contains(out, "user:pass") || strings.Contains(out, "a:b") {
		t.Fatalf("expected userinfo stripped, got: %q", out)
	}
	if !strings.Contains(out, "https://example.com:8443/a/b?x=1#frag") {
		t.Fatalf("expected URL with port/query/fragment preserved, got: %q", out)
	}
}

func TestFormatGitCommandErrorRedactsSecrets(t *testing.T) {
	t.Parallel()

	token := "ghp_abcdefghijklmnopqrstuvwxyz123456"
	err := formatGitCommandError(
		[]string{"push", "https://oauth2:my-secret@example.com/acme/repo.git"},
		[]byte("fatal: failed auth for my-secret and "+token),
		errors.New("exit status 128"),
		[]string{"my-secret", token},
	)

	msg := err.Error()
	if strings.Contains(msg, "my-secret") {
		t.Fatalf("expected embedded secret to be redacted, got: %q", msg)
	}
	if strings.Contains(msg, token) {
		t.Fatalf("expected token to be redacted, got: %q", msg)
	}
	if strings.Contains(msg, "oauth2:") {
		t.Fatalf("expected URL userinfo to be stripped, got: %q", msg)
	}
}

func TestRunGitOutputWithOptionsRedactsExitError(t *testing.T) {
	t.Parallel()

	_, err := runGitOutputWithOptions(context.Background(), "", gitRunOptions{secrets: []string{"my-secret"}}, "my-secret")
	if err == nil {
		t.Fatalf("expected error")
	}
	if strings.Contains(err.Error(), "my-secret") {
		t.Fatalf("expected redacted error, got: %v", err)
	}
}

func TestWarnCredentialURLIdempotent(t *testing.T) {
	credentialURLWarnings = sync.Map{}
	defer func() { credentialURLWarnings = sync.Map{} }()

	prev := slog.Default()
	defer slog.SetDefault(prev)

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	warnCredentialURL("https://oauth2:secret@example.com/acme/repo.git")
	warnCredentialURL("https://oauth2:secret@example.com/acme/repo.git")

	if got := strings.Count(buf.String(), "embedded credentials"); got != 1 {
		t.Fatalf("expected one warning, got %d logs: %s", got, buf.String())
	}
}

func TestDedupeNonEmpty(t *testing.T) {
	t.Parallel()

	if got := dedupeNonEmpty(); got != nil {
		t.Fatalf("expected nil for empty input, got %v", got)
	}
	if got := dedupeNonEmpty("  ", "a", "a", " b ", ""); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("unexpected dedupe result: %v", got)
	}
}

func TestEnsureRemoteStripsCredentialUserInfo(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	runGitCmd(t, "", "init", repo)

	credentialURL := "https://oauth2:my-secret@example.com/acme/repo.git"
	if err := EnsureRemote(ctx, repo, "origin", credentialURL); err != nil {
		t.Fatalf("ensure remote: %v", err)
	}
	got := strings.TrimSpace(runGitCmdOutput(t, repo, "remote", "get-url", "origin"))
	if got != "https://example.com/acme/repo.git" {
		t.Fatalf("expected sanitized origin URL, got %q", got)
	}

	if err := EnsureRemote(ctx, repo, "origin", credentialURL); err != nil {
		t.Fatalf("ensure remote idempotent: %v", err)
	}
}

func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

func runGitCmdOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s failed: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

func runGitCmdOutputErr(t *testing.T, dir string, args ...string) error {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	_, err := cmd.Output()
	return err
}
