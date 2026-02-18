//go:build !darwin

package notify

import "testing"

func TestNewDesktopSenderNonDarwin(t *testing.T) {
	t.Parallel()
	if sender := NewDesktopSender(); sender != nil {
		t.Fatalf("expected nil sender on non-darwin, got %#v", sender)
	}
}
