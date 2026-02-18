//go:build darwin

package notify

import "testing"

func TestNewDesktopSenderDarwin(t *testing.T) {
	t.Parallel()
	if sender := NewDesktopSender(); sender == nil {
		t.Fatal("expected desktop sender on darwin")
	}
}
