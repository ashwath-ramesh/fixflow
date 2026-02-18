package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestWebhookSenderSendsPayload(t *testing.T) {
	t.Parallel()

	var received Payload
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			defer r.Body.Close()
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Fatalf("expected content-type application/json, got %q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("ok")),
				Header:     make(http.Header),
			}, nil
		}),
	}

	sender := NewWebhookSender("https://example.com/hook", client)
	payload := Payload{
		Event:      TriggerPRCreated,
		JobID:      "ap-job-123",
		State:      "pr created",
		IssueTitle: "Fix bug",
		PRURL:      "https://example.com/pr/1",
		Project:    "proj",
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}
	if err := sender.Send(context.Background(), payload); err != nil {
		t.Fatalf("send webhook: %v", err)
	}

	if received.Event != payload.Event || received.JobID != payload.JobID {
		t.Fatalf("unexpected payload: %#v", received)
	}
	if received.PRURL != payload.PRURL {
		t.Fatalf("expected pr_url %q, got %q", payload.PRURL, received.PRURL)
	}
}

func TestWebhookSenderHonorsContextTimeout(t *testing.T) {
	t.Parallel()

	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}),
	}

	sender := NewWebhookSender("https://example.com/hook", client)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := sender.Send(ctx, TestPayload())
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if !strings.Contains(err.Error(), "send webhook request") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
