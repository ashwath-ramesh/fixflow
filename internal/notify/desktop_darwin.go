//go:build darwin

package notify

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type desktopSender struct{}

func NewDesktopSender() Sender {
	return &desktopSender{}
}

func (s *desktopSender) Name() string {
	return "desktop"
}

func (s *desktopSender) Send(ctx context.Context, payload Payload) error {
	title := escapeAppleScriptString("AutoPR: " + EventLabel(payload.Event))
	message := escapeAppleScriptString(fmt.Sprintf("%s - %s", payload.Project, payload.IssueTitle))
	if payload.PRURL != "" {
		message = escapeAppleScriptString(fmt.Sprintf("%s - %s (%s)", payload.Project, payload.IssueTitle, payload.PRURL))
	}
	script := fmt.Sprintf(`display notification "%s" with title "%s"`, message, title)
	if err := exec.CommandContext(ctx, "osascript", "-e", script).Run(); err != nil {
		return fmt.Errorf("desktop notification failed: %w", err)
	}
	return nil
}

func escapeAppleScriptString(v string) string {
	v = strings.ReplaceAll(v, `\\`, `\\\\`)
	v = strings.ReplaceAll(v, `"`, `\\"`)
	return v
}
