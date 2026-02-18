package cli

import (
	"strings"
	"testing"
)

func TestShouldCheckForUpdatesHonorsSkipEnv(t *testing.T) {
	t.Setenv(skipUpdateNoticeEnv, "")
	if !shouldCheckForUpdates() {
		t.Fatal("expected update check when env is unset")
	}

	t.Setenv(skipUpdateNoticeEnv, "1")
	if shouldCheckForUpdates() {
		t.Fatal("expected update check to be skipped when env is set")
	}
}

func TestChildEnvWithSkippedUpdateNoticeSetsFlag(t *testing.T) {
	t.Setenv("EXISTING_ENV", "ok")

	env := childEnvWithSkippedUpdateNotice()
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "EXISTING_ENV=ok") {
		t.Fatal("expected child env to preserve existing vars")
	}
	if !strings.Contains(joined, skipUpdateNoticeEnv+"=1") {
		t.Fatal("expected child env to include update-notice skip flag")
	}
}
