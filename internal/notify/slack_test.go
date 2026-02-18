package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSlackSenderFormatsTextPayload(t *testing.T) {
	t.Parallel()

	var body map[string]string
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			defer r.Body.Close()
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("ok")),
				Header:     make(http.Header),
			}, nil
		}),
	}

	sender := NewSlackSender("https://hooks.slack.com/services/T000/B000/XXX", client)
	payload := TestPayload()
	payload.Project = "my-project"
	payload.JobID = "ap-job-abc"
	payload.IssueTitle = "Fix login"
	payload.PRURL = "https://example.com/pr/99"

	if err := sender.Send(context.Background(), payload); err != nil {
		t.Fatalf("send slack: %v", err)
	}

	text := body["text"]
	if !strings.Contains(text, "AutoPR") {
		t.Fatalf("expected AutoPR in text, got %q", text)
	}
	if !strings.Contains(text, payload.IssueTitle) {
		t.Fatalf("expected issue title in text, got %q", text)
	}
	if !strings.Contains(text, payload.PRURL) {
		t.Fatalf("expected pr url in text, got %q", text)
	}
}

func TestSlackTextUsesEventLabel(t *testing.T) {
	t.Parallel()
	payload := Payload{Event: TriggerPRMerged, Project: "proj", JobID: "id", IssueTitle: "title"}
	text := SlackText(payload)
	if !strings.Contains(text, "PR Merged") {
		t.Fatalf("expected PR Merged label, got %q", text)
	}
}
