package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"autopr/internal/db"

	"github.com/spf13/cobra"
)

func TestResolveLogsSessionByIndex(t *testing.T) {
	sessions := []db.LLMSession{
		{ID: 101, JobID: "job-1"},
		{ID: 202, JobID: "job-1"},
		{ID: 303, JobID: "job-1"},
	}

	session, err := resolveLogsSession(sessions, "2", "job-1")
	if err != nil {
		t.Fatalf("resolveLogsSession(index): unexpected error: %v", err)
	}
	if got, want := session.ID, 202; got != want {
		t.Fatalf("resolveLogsSession(index): expected %d, got %d", want, got)
	}
}

func TestResolveLogsSessionByID(t *testing.T) {
	sessions := []db.LLMSession{
		{ID: 101, JobID: "job-1"},
		{ID: 1203, JobID: "job-1"},
	}

	session, err := resolveLogsSession(sessions, "1203", "job-1")
	if err != nil {
		t.Fatalf("resolveLogsSession(id): unexpected error: %v", err)
	}
	if got, want := session.ID, 1203; got != want {
		t.Fatalf("resolveLogsSession(id): expected %d, got %d", want, got)
	}
}

func TestResolveLogsSessionIndexFirstOverID(t *testing.T) {
	sessions := []db.LLMSession{
		{ID: 101, JobID: "job-1"},
		{ID: 1, JobID: "job-1"},
	}

	session, err := resolveLogsSession(sessions, "1", "job-1")
	if err != nil {
		t.Fatalf("resolveLogsSession(index-first): unexpected error: %v", err)
	}
	if got, want := session.ID, 101; got != want {
		t.Fatalf("resolveLogsSession(index-first): expected %d, got %d", want, got)
	}
}

func TestResolveLogsSessionInvalidSelectors(t *testing.T) {
	sessions := []db.LLMSession{
		{ID: 11, JobID: "job-1"},
		{ID: 22, JobID: "job-1"},
	}

	tests := []struct {
		name  string
		value string
	}{
		{name: "zero index", value: "0"},
		{name: "negative index", value: "-1"},
		{name: "non-numeric", value: "abc"},
		{name: "out-of-range unknown id", value: "33"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if _, err := resolveLogsSession(sessions, tc.value, "job-1"); err == nil {
				t.Fatalf("expected error for selector=%q", tc.value)
			}
		})
	}
}

func TestResolveLogsOutputMode(t *testing.T) {
	tests := []struct {
		name       string
		showInput  bool
		showOutput bool
		want       logsOutputMode
	}{
		{name: "default output", showInput: false, showOutput: false, want: logsOutputModeOutput},
		{name: "show-output", showInput: false, showOutput: true, want: logsOutputModeOutput},
		{name: "show-input", showInput: true, showOutput: false, want: logsOutputModeInput},
		{name: "both flags output wins", showInput: true, showOutput: true, want: logsOutputModeOutput},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := resolveLogsOutputMode(tc.showInput, tc.showOutput)
			if got != tc.want {
				t.Fatalf("resolveLogsOutputMode(%v, %v): expected %s, got %s", tc.showInput, tc.showOutput, tc.want, got)
			}
		})
	}
}

func TestRunLogsSessionIDOutOfIndexRange(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeLogsConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")

	jobID, sessions := seedLogsJobForTestWithSessionIDs(t, dbPath, 101, 1203)
	if err := completeLogSessionFixtures(t, dbPath, sessions[0], sessions[1]); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}

	out := runLogsForTest(t, cfg, jobID, logsRunOptions{session: "1203"})
	if !strings.Contains(out, "Session ID: 1203") {
		t.Fatalf("expected session id 1203, got %q", out)
	}
	if !strings.Contains(out, "Response Text:") {
		t.Fatalf("expected output mode label: %q", out)
	}
	if !strings.Contains(out, "Response for session 2") {
		t.Fatalf("expected selected session text for id 1203, got: %q", out)
	}
	if strings.Contains(out, "Session ID: 101") {
		t.Fatalf("unexpected aggregate session leaked into session mode: %q", out)
	}
}

func TestRunLogsSessionIndexDefaultsToOutput(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeLogsConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")

	jobID, s1, s2 := seedLogsJobForTest(t, dbPath)
	if err := completeLogSessionFixtures(t, dbPath, s1, s2); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}

	out := runLogsForTest(t, cfg, jobID, logsRunOptions{session: "1"})
	if !strings.Contains(out, "Session ID: "+strconv.FormatInt(s1, 10)) {
		t.Fatalf("expected output session id %d, got %q", s1, out)
	}
	if !strings.Contains(out, "Response Text:") {
		t.Fatalf("expected output mode label: %q", out)
	}
	if strings.Contains(out, "Prompt for session 1") {
		t.Fatalf("expected output-only text path, got prompt text present")
	}
}

func TestRunLogsSessionIDShowsInput(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeLogsConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")

	jobID, s1, s2 := seedLogsJobForTest(t, dbPath)
	if err := completeLogSessionFixtures(t, dbPath, s1, s2); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}

	out := runLogsForTest(t, cfg, jobID, logsRunOptions{session: strconv.FormatInt(s2, 10), showInput: true})
	if !strings.Contains(out, "Session ID: "+strconv.FormatInt(s2, 10)) {
		t.Fatalf("expected session id %d, got %q", s2, out)
	}
	if !strings.Contains(out, "Prompt Text:") {
		t.Fatalf("expected input mode label: %q", out)
	}
	if strings.Contains(out, "Response for session 2") {
		t.Fatalf("expected input-only text path, got response text present")
	}
}

func TestRunLogsSessionIDPrecedenceOutputWins(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeLogsConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")

	jobID, _, s2 := seedLogsJobForTest(t, dbPath)
	if err := completeLogSessionFixtures(t, dbPath, 0, s2); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}

	out := runLogsForTest(t, cfg, jobID, logsRunOptions{session: strconv.FormatInt(s2, 10), showInput: true, showOutput: true})
	if !strings.Contains(out, "Response Text:") {
		t.Fatalf("expected output mode precedence, got: %q", out)
	}
	if strings.Contains(out, "Prompt for session 2") {
		t.Fatalf("expected output to win over input mode")
	}
}

func TestRunLogsWithoutSessionShowsAggregateOutput(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeLogsConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")

	jobID, s1, s2 := seedLogsJobForTest(t, dbPath)
	if err := completeLogSessionFixtures(t, dbPath, s1, s2); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}

	out := runLogsForTest(t, cfg, jobID, logsRunOptions{})
	if !strings.Contains(out, "=== LLM Sessions ===") {
		t.Fatalf("expected aggregate session header: %q", out)
	}
	if !strings.Contains(out, "--- plan (iter 1)") {
		t.Fatalf("expected plan step in aggregate output: %q", out)
	}
	if strings.Contains(out, "Response Text:") {
		t.Fatalf("unexpected session mode label in aggregate output")
	}
}

func writeLogsConfig(t *testing.T, dir string) string {
	t.Helper()
	cfgPath := filepath.Join(dir, "autopr.toml")
	dbPath := filepath.Join(dir, "autopr.db")
	cfg := fmt.Sprintf(`db_path = %q

[[projects]]
name = "project"
repo_url = "https://github.com/autopr/placeholder"
test_cmd = "echo ok"

[projects.github]
owner = "autopr"
repo = "placeholder"
`, dbPath)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func seedLogsJobForTest(t *testing.T, dbPath string) (jobID string, sessionOne int64, sessionTwo int64) {
	t.Helper()
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
		ProjectName:   "project",
		Source:        "github",
		SourceIssueID: "1010",
		Title:         "logs test",
		URL:           "https://github.com/autopr/logs-test/issues/1010",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err = store.CreateJob(ctx, issueID, "project", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	sessionOne, err = store.CreateSession(ctx, jobID, "plan", 1, "codex", "")
	if err != nil {
		t.Fatalf("create session one: %v", err)
	}
	sessionTwo, err = store.CreateSession(ctx, jobID, "implement", 2, "codex", "")
	if err != nil {
		t.Fatalf("create session two: %v", err)
	}

	return jobID, sessionOne, sessionTwo
}

func seedLogsJobForTestWithSessionIDs(t *testing.T, dbPath string, sessionIDs ...int64) (jobID string, sessions []int64) {
	t.Helper()
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
		ProjectName:   "project",
		Source:        "github",
		SourceIssueID: "1010",
		Title:         "logs test",
		URL:           "https://github.com/autopr/logs-test/issues/1010",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err = store.CreateJob(ctx, issueID, "project", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	for idx, sessionID := range sessionIDs {
		if _, err := store.Writer.ExecContext(ctx, `INSERT INTO llm_sessions(id, job_id, step, iteration, llm_provider, status) VALUES(?, ?, ?, ?, ?, 'running')`, sessionID, jobID, "plan", idx+1, "codex"); err != nil {
			t.Fatalf("insert session %d: %v", sessionID, err)
		}
		sessions = append(sessions, sessionID)
	}

	return jobID, sessions
}

func completeLogSessionFixtures(t *testing.T, dbPath string, sessionOne int64, sessionTwo int64) error {
	t.Helper()
	store, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	ctx := context.Background()
	if sessionOne != 0 {
		if err := store.CompleteSession(ctx, sessionOne, "completed", "Response for session 1", "Prompt for session 1", "", "", "", "", 11, 21, 100); err != nil {
			return err
		}
	}
	if sessionTwo != 0 {
		if err := store.CompleteSession(ctx, sessionTwo, "completed", "Response for session 2", "Prompt for session 2", "", "", "", "", 31, 41, 200); err != nil {
			return err
		}
	}
	return nil
}

type logsRunOptions struct {
	session    string
	showInput  bool
	showOutput bool
	jsonOut    bool
}

func runLogsForTestResult(t *testing.T, configPath string, jobID string, opts logsRunOptions) (string, error) {
	t.Helper()
	prevCfgPath := cfgPath
	prevJSON := jsonOut
	prevSession := logsSession
	prevShowInput := logsShowInput
	prevShowOutput := logsShowOutput
	prevFollow := logsFollow

	cfgPath = configPath
	jsonOut = opts.jsonOut
	logsSession = opts.session
	logsShowInput = opts.showInput
	logsShowOutput = opts.showOutput
	logsFollow = false

	t.Cleanup(func() {
		cfgPath = prevCfgPath
		jsonOut = prevJSON
		logsSession = prevSession
		logsShowInput = prevShowInput
		logsShowOutput = prevShowOutput
		logsFollow = prevFollow
	})

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return captureStdoutWithError(t, func() error {
		return runLogs(cmd, []string{jobID})
	})
}

func runLogsForTest(t *testing.T, configPath string, jobID string, opts logsRunOptions) string {
	t.Helper()
	out, err := runLogsForTestResult(t, configPath, jobID, opts)
	if err != nil {
		t.Fatalf("run logs: %v", err)
	}
	return out
}
