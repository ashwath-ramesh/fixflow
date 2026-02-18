package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"autopr/internal/config"
	"autopr/internal/notify"

	"github.com/spf13/cobra"
)

type notifyStubSender struct {
	name string
}

func (s notifyStubSender) Name() string { return s.name }

func (s notifyStubSender) Send(context.Context, notify.Payload) error { return nil }

func TestRunNotifyTestSuccess(t *testing.T) {
	origBuild := buildNotifySenders
	origSend := sendNotifyAll
	t.Cleanup(func() {
		buildNotifySenders = origBuild
		sendNotifyAll = origSend
	})

	buildNotifySenders = func(config.NotificationsConfig, *http.Client) []notify.Sender {
		return []notify.Sender{notifyStubSender{name: "slack"}}
	}
	sendNotifyAll = func(context.Context, []notify.Sender, notify.Payload, time.Duration) []notify.ChannelResult {
		return []notify.ChannelResult{{Channel: "slack", Success: true}}
	}

	cfg := &config.Config{Projects: []config.ProjectConfig{{Name: "proj"}}}
	results, err := runNotifyTest(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run notify test: %v", err)
	}
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("unexpected results: %#v", results)
	}
}

func TestRunNotifyTestNoChannels(t *testing.T) {
	origBuild := buildNotifySenders
	t.Cleanup(func() { buildNotifySenders = origBuild })

	buildNotifySenders = func(config.NotificationsConfig, *http.Client) []notify.Sender {
		return nil
	}
	_, err := runNotifyTest(context.Background(), &config.Config{})
	if err == nil {
		t.Fatalf("expected no-channel error")
	}
	if !strings.Contains(err.Error(), "no notification channels configured") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunNotifyTestAllFailures(t *testing.T) {
	origBuild := buildNotifySenders
	origSend := sendNotifyAll
	t.Cleanup(func() {
		buildNotifySenders = origBuild
		sendNotifyAll = origSend
	})

	buildNotifySenders = func(config.NotificationsConfig, *http.Client) []notify.Sender {
		return []notify.Sender{notifyStubSender{name: "webhook"}}
	}
	sendNotifyAll = func(context.Context, []notify.Sender, notify.Payload, time.Duration) []notify.ChannelResult {
		return []notify.ChannelResult{{Channel: "webhook", Success: false, Error: "timeout"}}
	}

	results, err := runNotifyTest(context.Background(), &config.Config{})
	if err == nil {
		t.Fatalf("expected all-failed error")
	}
	if len(results) != 1 || results[0].Success {
		t.Fatalf("unexpected results: %#v", results)
	}
	if !strings.Contains(err.Error(), "all notification channels failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunNotifyReturnsConfigError(t *testing.T) {
	prevNotifyTest := notifyTest
	prevCfgPath := cfgPath
	notifyTest = true
	cfgPath = t.TempDir() + "/missing.toml"
	t.Cleanup(func() {
		notifyTest = prevNotifyTest
		cfgPath = prevCfgPath
	})

	err := runNotify(&cobra.Command{}, nil)
	if err == nil {
		t.Fatalf("expected config error")
	}
	if !strings.Contains(err.Error(), "decode config") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNotifyJSONOutputSchema(t *testing.T) {
	t.Parallel()
	out := notifyTestOutput{
		Test:    true,
		Success: false,
		Results: []notify.ChannelResult{{Channel: "slack", Success: false, Error: "timeout"}},
		Error:   "all failed",
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"test", "success", "results", "error"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing key %q in output: %s", key, string(encoded))
		}
	}
}
