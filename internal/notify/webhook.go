package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxErrorBodyBytes = 1024

type WebhookSender struct {
	url    string
	client *http.Client
}

func NewWebhookSender(webhookURL string, client *http.Client) *WebhookSender {
	if client == nil {
		client = http.DefaultClient
	}
	return &WebhookSender{
		url:    strings.TrimSpace(webhookURL),
		client: client,
	}
}

func (s *WebhookSender) Name() string {
	return "webhook"
}

func (s *WebhookSender) Send(ctx context.Context, payload Payload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}
	return postJSON(ctx, s.client, s.url, body, s.Name())
}

func postJSON(ctx context.Context, client *http.Client, endpoint string, body []byte, channel string) error {
	if strings.TrimSpace(endpoint) == "" {
		return fmt.Errorf("%s endpoint is empty", channel)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build %s request: %w", channel, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send %s request: %w", channel, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		msg := strings.TrimSpace(string(respBody))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("%s request failed with status %d: %s", channel, resp.StatusCode, msg)
	}
	return nil
}
