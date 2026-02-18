package notify

import (
	"errors"
	"strings"
	"testing"
)

func TestSanitizeChannelErrorRedactsURLs(t *testing.T) {
	t.Parallel()
	err := errors.New(`Post "https://hooks.slack.com/services/T000/B000/SECRET": context deadline exceeded`)
	msg := sanitizeChannelError(err)
	if strings.Contains(msg, "SECRET") {
		t.Fatalf("expected webhook URL secret to be redacted, got %q", msg)
	}
	if !strings.Contains(msg, "https://hooks.slack.com/REDACTED") {
		t.Fatalf("expected redacted host marker, got %q", msg)
	}
}
