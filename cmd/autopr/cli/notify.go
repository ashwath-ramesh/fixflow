package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"autopr/internal/config"
	"autopr/internal/notify"

	"github.com/spf13/cobra"
)

var notifyTest bool

var (
	buildNotifySenders = notify.BuildSenders
	sendNotifyAll      = notify.SendAll
)

var notifyCmd = &cobra.Command{
	Use:   "notify",
	Short: "Send notification test events",
	RunE:  runNotify,
}

func init() {
	notifyCmd.Flags().BoolVar(&notifyTest, "test", false, "send a test notification to all configured channels")
	rootCmd.AddCommand(notifyCmd)
}

type notifyTestOutput struct {
	Test    bool                   `json:"test"`
	Success bool                   `json:"success"`
	Results []notify.ChannelResult `json:"results"`
	Error   string                 `json:"error,omitempty"`
}

func runNotify(cmd *cobra.Command, args []string) error {
	if !notifyTest {
		return fmt.Errorf("notify currently supports only --test")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	results, err := runNotifyTest(cmd.Context(), cfg)
	if jsonOut {
		out := notifyTestOutput{Test: true, Success: err == nil, Results: results}
		if err != nil {
			out.Error = err.Error()
		}
		printJSON(out)
		return err
	}

	for _, result := range results {
		if result.Success {
			fmt.Printf("%s: ok\n", result.Channel)
			continue
		}
		if result.Error != "" {
			fmt.Printf("%s: failed (%s)\n", result.Channel, result.Error)
			continue
		}
		fmt.Printf("%s: failed\n", result.Channel)
	}
	if err != nil {
		return err
	}
	fmt.Println("notification test succeeded")
	return nil
}

func runNotifyTest(ctx context.Context, cfg *config.Config) ([]notify.ChannelResult, error) {
	senders := buildNotifySenders(cfg.Notifications, nil)
	if len(senders) == 0 {
		return nil, fmt.Errorf("no notification channels configured")
	}

	payload := notify.TestPayload()
	if len(cfg.Projects) > 0 {
		payload.Project = cfg.Projects[0].Name
	}
	payload.Timestamp = time.Now().UTC().Format(time.RFC3339)

	results := sendNotifyAll(ctx, senders, payload, 4*time.Second)
	successes := 0
	for _, result := range results {
		if result.Success {
			successes++
		}
	}
	if successes == 0 {
		return results, fmt.Errorf("all notification channels failed: %s", summarizeNotifyFailures(results))
	}
	return results, nil
}

func summarizeNotifyFailures(results []notify.ChannelResult) string {
	parts := make([]string, 0, len(results))
	for _, result := range results {
		if result.Success {
			continue
		}
		if result.Error == "" {
			parts = append(parts, result.Channel)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", result.Channel, result.Error))
	}
	return strings.Join(parts, ", ")
}
