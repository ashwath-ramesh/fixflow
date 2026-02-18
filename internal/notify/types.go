package notify

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	TriggerAwaitingApproval = "awaiting_approval"
	TriggerFailed           = "failed"
	TriggerPRCreated        = "pr_created"
	TriggerPRMerged         = "pr_merged"
)

var AllTriggers = []string{
	TriggerAwaitingApproval,
	TriggerFailed,
	TriggerPRCreated,
	TriggerPRMerged,
}

type Payload struct {
	Event      string `json:"event"`
	JobID      string `json:"job_id"`
	State      string `json:"state"`
	IssueTitle string `json:"issue_title"`
	PRURL      string `json:"pr_url,omitempty"`
	Project    string `json:"project"`
	Timestamp  string `json:"timestamp"`
}

type Sender interface {
	Name() string
	Send(ctx context.Context, payload Payload) error
}

type ChannelResult struct {
	Channel string `json:"channel"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

func IsValidTrigger(trigger string) bool {
	switch trigger {
	case TriggerAwaitingApproval, TriggerFailed, TriggerPRCreated, TriggerPRMerged:
		return true
	default:
		return false
	}
}

func DefaultTriggers() []string {
	out := make([]string, len(AllTriggers))
	copy(out, AllTriggers)
	return out
}

func TriggerSet(triggers []string) map[string]struct{} {
	if triggers == nil {
		triggers = DefaultTriggers()
	}
	out := make(map[string]struct{}, len(triggers))
	for _, trigger := range triggers {
		normalized := strings.ToLower(strings.TrimSpace(trigger))
		if IsValidTrigger(normalized) {
			out[normalized] = struct{}{}
		}
	}
	return out
}

func EventState(event string) string {
	switch event {
	case TriggerAwaitingApproval:
		return "awaiting approval"
	case TriggerPRCreated:
		return "pr created"
	case TriggerPRMerged:
		return "pr merged"
	default:
		return "failed"
	}
}

func EventLabel(event string) string {
	switch event {
	case TriggerAwaitingApproval:
		return "Awaiting Approval"
	case TriggerPRCreated:
		return "PR Created"
	case TriggerPRMerged:
		return "PR Merged"
	default:
		return "Job Failed"
	}
}

func TestPayload() Payload {
	now := time.Now().UTC().Format(time.RFC3339)
	return Payload{
		Event:      TriggerAwaitingApproval,
		JobID:      "ap-job-test",
		State:      EventState(TriggerAwaitingApproval),
		IssueTitle: "Test notification from AutoPR",
		PRURL:      "https://example.com/pr/123",
		Project:    "autopr",
		Timestamp:  now,
	}
}

func SlackText(payload Payload) string {
	text := fmt.Sprintf("AutoPR: %s\nProject: %s\nJob: %s\nIssue: %s", EventLabel(payload.Event), payload.Project, payload.JobID, payload.IssueTitle)
	if payload.PRURL != "" {
		text += "\nPR: " + payload.PRURL
	}
	return text
}
