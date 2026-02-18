package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type SlackSender struct {
	url    string
	client *http.Client
}

func NewSlackSender(webhookURL string, client *http.Client) *SlackSender {
	if client == nil {
		client = http.DefaultClient
	}
	return &SlackSender{
		url:    strings.TrimSpace(webhookURL),
		client: client,
	}
}

func (s *SlackSender) Name() string {
	return "slack"
}

func (s *SlackSender) Send(ctx context.Context, payload Payload) error {
	body := map[string]string{"text": SlackText(payload)}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal slack payload: %w", err)
	}
	return postJSON(ctx, s.client, s.url, encoded, s.Name())
}
