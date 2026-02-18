package notify

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"autopr/internal/config"
)

var urlPattern = regexp.MustCompile(`https?://[^\s"'` + "`" + `]+`)

func BuildSenders(cfg config.NotificationsConfig, client *http.Client) []Sender {
	senders := make([]Sender, 0, 3)
	if strings.TrimSpace(cfg.WebhookURL) != "" {
		senders = append(senders, NewWebhookSender(cfg.WebhookURL, client))
	}
	if strings.TrimSpace(cfg.SlackWebhook) != "" {
		senders = append(senders, NewSlackSender(cfg.SlackWebhook, client))
	}
	if cfg.Desktop {
		if sender := NewDesktopSender(); sender != nil {
			senders = append(senders, sender)
		}
	}
	return senders
}

func SendAll(ctx context.Context, senders []Sender, payload Payload, timeout time.Duration) []ChannelResult {
	results := make([]ChannelResult, 0, len(senders))
	for _, sender := range senders {
		if sender == nil {
			continue
		}
		sendCtx := ctx
		cancel := func() {}
		if timeout > 0 {
			sendCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		err := sender.Send(sendCtx, payload)
		cancel()
		result := ChannelResult{Channel: sender.Name(), Success: err == nil}
		if err != nil {
			result.Error = sanitizeChannelError(err)
		}
		results = append(results, result)
	}
	return results
}

func summarizeFailures(results []ChannelResult) string {
	parts := make([]string, 0, len(results))
	for _, result := range results {
		if result.Success {
			continue
		}
		if result.Error == "" {
			parts = append(parts, fmt.Sprintf("%s failed", result.Channel))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", result.Channel, result.Error))
	}
	return strings.Join(parts, "; ")
}

func successCount(results []ChannelResult) int {
	count := 0
	for _, result := range results {
		if result.Success {
			count++
		}
	}
	return count
}

func sanitizeChannelError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	msg = redactURLs(msg)
	if len(msg) > 512 {
		msg = msg[:512]
	}
	return msg
}

func redactURLs(msg string) string {
	return urlPattern.ReplaceAllStringFunc(msg, func(match string) string {
		parsed, err := url.Parse(match)
		if err != nil || parsed.Host == "" {
			return "[redacted-url]"
		}
		return parsed.Scheme + "://" + parsed.Host + "/REDACTED"
	})
}
