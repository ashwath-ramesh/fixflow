package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autopr/internal/db"

	"github.com/spf13/cobra"
)

type statusSeed struct {
	state  string
	count  int
	merged int
}

type statusJSONOutput struct {
	Running   bool           `json:"running"`
	PID       string         `json:"pid"`
	JobCounts map[string]int `json:"job_counts"`
}

func TestRunStatusJSONOutputsNormalizedJobCounts(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeStatusConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")

	seedStatusJobs(t, dbPath, []statusSeed{
		{state: "queued", count: 1},
		{state: "planning", count: 2},
		{state: "implementing", count: 1},
		{state: "reviewing", count: 1},
		{state: "testing", count: 1},
		{state: "ready", count: 1},
		{state: "failed", count: 1},
		{state: "cancelled", count: 1},
		{state: "rejected", count: 1},
		{state: "approved", count: 3, merged: 1},
	})

	out := runStatusWithTestConfig(t, cfgPath, true, false)

	var decoded statusJSONOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &decoded); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if decoded.Running {
		t.Fatal("expected running=false")
	}
	if decoded.PID != "" {
		t.Fatalf("expected empty PID, got %q", decoded.PID)
	}
	if _, ok := decoded.JobCounts["queued"]; !ok {
		t.Fatal("missing queued in job_counts")
	}
	expected := map[string]int{
		"queued":       1,
		"planning":     2,
		"implementing": 1,
		"reviewing":    1,
		"testing":      1,
		"needs_pr":     1,
		"failed":       1,
		"cancelled":    1,
		"pr_created":   2,
		"merged":       1,
		"rejected":     1,
	}
	for key, value := range expected {
		got, ok := decoded.JobCounts[key]
		if !ok {
			t.Fatalf("missing %q in job_counts", key)
		}
		if got != value {
			t.Fatalf("job_counts[%q]: expected %d, got %d", key, value, got)
		}
	}
}

func TestRunStatusTableOutputUnchanged(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeStatusConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")

	seedStatusJobs(t, dbPath, []statusSeed{
		{state: "queued", count: 1},
		{state: "planning", count: 1},
		{state: "ready", count: 1},
		{state: "approved", count: 1, merged: 0},
	})

	out := runStatusWithTestConfig(t, cfgPath, false, false)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	expected := []string{
		"Daemon: stopped",
		"",
		"Pipeline:  1 queued · 1 active",
		"Active:    1 planning · 0 implementing · 0 reviewing · 0 testing",
		"Output:    1 needs_pr · 0 merged · 1 pr_created",
	}
	if len(lines) != len(expected) {
		t.Fatalf("unexpected output lines (%d): %q", len(lines), out)
	}
	for i, line := range expected {
		if lines[i] != line {
			t.Fatalf("line %d mismatch: expected %q, got %q", i, line, lines[i])
		}
	}
}

func TestRunStatusShortOutputStopped(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeStatusConfig(t, tmp)

	out := runStatusWithTestConfig(t, cfgPath, false, true)
	if got := strings.TrimSpace(out); got != "stopped | 0 queued, 0 active" {
		t.Fatalf("unexpected short output: %q", got)
	}
}

func TestRunStatusShortOutputRunning(t *testing.T) {
	tmp := t.TempDir()
	pidPath := filepath.Join(tmp, "autopr.pid")
	cfgPath := writeStatusConfigWithPID(t, tmp, pidPath)
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	out := runStatusWithTestConfig(t, cfgPath, false, true)
	if got := strings.TrimSpace(out); got != "running | 0 queued, 0 active" {
		t.Fatalf("unexpected short output: %q", got)
	}
}

func TestRunStatusShortOutputActiveCountIncludesRebasingAndResolvingConflicts(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeStatusConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")

	seedStatusJobs(t, dbPath, []statusSeed{
		{state: "queued", count: 2},
		{state: "planning", count: 1},
		{state: "implementing", count: 2},
		{state: "reviewing", count: 3},
		{state: "testing", count: 4},
		{state: "rebasing", count: 1},
		{state: "resolving_conflicts", count: 5},
	})

	out := runStatusWithTestConfig(t, cfgPath, false, true)
	if got := strings.TrimSpace(out); got != "stopped | 2 queued, 16 active" {
		t.Fatalf("unexpected short output: %q", got)
	}
}

func TestRunStatusJSONHasPriorityOverShort(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeStatusConfig(t, tmp)

	out := runStatusWithTestConfig(t, cfgPath, true, true)
	out = strings.TrimSpace(out)
	if !strings.HasPrefix(out, "{") {
		t.Fatalf("expected JSON output, got %q", out)
	}
}

func TestRunStatusTableOutputNoJobSectionsForZeroCounts(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeStatusConfig(t, tmp)

	out := runStatusWithTestConfig(t, cfgPath, false, false)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected daemon-only output, got %d lines: %q", len(lines), out)
	}
	if lines[0] != "Daemon: stopped" {
		t.Fatalf("unexpected daemon line: %q", lines[0])
	}
}

func TestRunStatusTableOutputSkipsZeroSections(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeStatusConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")

	seedStatusJobs(t, dbPath, []statusSeed{
		{state: "planning", count: 2},
		{state: "testing", count: 1},
		{state: "failed", count: 3},
	})

	out := runStatusWithTestConfig(t, cfgPath, false, false)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	expected := []string{
		"Daemon: stopped",
		"",
		"Pipeline:  0 queued · 3 active",
		"Active:    2 planning · 0 implementing · 0 reviewing · 1 testing",
		"Problems:  3 failed · 0 rejected · 0 cancelled",
	}
	if len(lines) != len(expected) {
		t.Fatalf("unexpected output lines (%d): %q", len(lines), out)
	}
	for i, line := range expected {
		if lines[i] != line {
			t.Fatalf("line %d mismatch: expected %q, got %q", i, line, lines[i])
		}
	}
}

func TestRunStatusTableOutputIncludesAllSections(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeStatusConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")

	seedStatusJobs(t, dbPath, []statusSeed{
		{state: "queued", count: 2},
		{state: "planning", count: 1},
		{state: "implementing", count: 1},
		{state: "reviewing", count: 2},
		{state: "testing", count: 3},
		{state: "ready", count: 4},
		{state: "failed", count: 1},
		{state: "rejected", count: 2},
		{state: "cancelled", count: 3},
		{state: "approved", count: 5, merged: 2},
	})

	out := runStatusWithTestConfig(t, cfgPath, false, false)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	expected := []string{
		"Daemon: stopped",
		"",
		"Pipeline:  2 queued · 7 active",
		"Active:    1 planning · 1 implementing · 2 reviewing · 3 testing",
		"Output:    4 needs_pr · 2 merged · 3 pr_created",
		"Problems:  1 failed · 2 rejected · 3 cancelled",
	}
	if len(lines) != len(expected) {
		t.Fatalf("unexpected output lines (%d): %q", len(lines), out)
	}
	for i, line := range expected {
		if lines[i] != line {
			t.Fatalf("line %d mismatch: expected %q, got %q", i, line, lines[i])
		}
	}
}

func TestRunStatusJSONNoPidFile(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeStatusConfig(t, tmp)

	out := runStatusWithTestConfig(t, cfgPath, true, false)

	var decoded statusJSONOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &decoded); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if decoded.Running {
		t.Fatalf("expected running=false")
	}
	if decoded.PID != "" {
		t.Fatalf("expected empty PID, got %q", decoded.PID)
	}
}

func TestRunStatusJSONBadPidFile(t *testing.T) {
	tmp := t.TempDir()
	pidPath := filepath.Join(tmp, "autopr.pid")
	cfgPath := writeStatusConfigWithPID(t, tmp, pidPath)
	if err := os.WriteFile(pidPath, []byte("not-a-number"), 0o644); err != nil {
		t.Fatalf("write bad pid file: %v", err)
	}

	out := runStatusWithTestConfig(t, cfgPath, true, false)

	var decoded statusJSONOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &decoded); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if decoded.Running {
		t.Fatalf("expected running=false")
	}
	if decoded.PID != "not-a-number" {
		t.Fatalf("expected PID to be preserved as %q, got %q", "not-a-number", decoded.PID)
	}
}

func TestRunStatusJSONEmptyDBIncludesZeroCounts(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeStatusConfig(t, tmp)
	out := runStatusWithTestConfig(t, cfgPath, true, false)

	var decoded statusJSONOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &decoded); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	expectedKeys := []string{
		"queued",
		"planning",
		"implementing",
		"reviewing",
		"testing",
		"needs_pr",
		"failed",
		"cancelled",
		"pr_created",
		"merged",
		"rejected",
	}
	for _, key := range expectedKeys {
		got, ok := decoded.JobCounts[key]
		if !ok {
			t.Fatalf("missing key %q in job_counts", key)
		}
		if got != 0 {
			t.Fatalf("expected %q == 0, got %d", key, got)
		}
	}
}

func writeStatusConfig(t *testing.T, dir string) string {
	t.Helper()
	return writeStatusConfigWithPID(t, dir, filepath.Join(dir, "autopr.pid"))
}

func writeStatusConfigWithPID(t *testing.T, dir, pidPath string) string {
	t.Helper()
	cfgPath := filepath.Join(dir, "autopr.toml")
	dbPath := filepath.Join(dir, "autopr.db")
	cfg := fmt.Sprintf(`db_path = %q

[daemon]
pid_file = %q

[[projects]]
name = "project"
repo_url = "https://github.com/autopr/placeholder"
test_cmd = "echo ok"

[projects.github]
owner = "autopr"
repo = "placeholder"
`, dbPath, pidPath)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func seedStatusJobs(t *testing.T, dbPath string, seeds []statusSeed) {
	t.Helper()
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	jobID := 0
	for _, seed := range seeds {
		for i := 0; i < seed.count; i++ {
			jobID++
			issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
				ProjectName:   "project",
				Source:        "github",
				SourceIssueID: fmt.Sprintf("issue-%d", jobID),
				Title:         fmt.Sprintf("issue %d", jobID),
				URL:           fmt.Sprintf("https://example.com/%d", jobID),
				State:         "open",
			})
			if err != nil {
				t.Fatalf("upsert issue: %v", err)
			}
			job, err := store.CreateJob(ctx, issueID, "project", 3)
			if err != nil {
				t.Fatalf("create job: %v", err)
			}
			if seed.state != "queued" {
				if _, err := store.Writer.ExecContext(ctx, `UPDATE jobs SET state = ? WHERE id = ?`, seed.state, job); err != nil {
					t.Fatalf("update job state: %v", err)
				}
			}
			if seed.state == "approved" && i < seed.merged {
				if _, err := store.Writer.ExecContext(ctx, `UPDATE jobs SET pr_merged_at = '2024-01-01T00:00:00Z' WHERE id = ?`, job); err != nil {
					t.Fatalf("mark merged job: %v", err)
				}
			}
		}
	}
}

func runStatusWithTestConfig(t *testing.T, configPath string, asJSON bool, asShort bool) string {
	t.Helper()
	prevCfgPath := cfgPath
	prevJSON := jsonOut
	prevShort := statusShort
	cfgPath = configPath
	jsonOut = asJSON
	statusShort = asShort
	t.Cleanup(func() {
		cfgPath = prevCfgPath
		jsonOut = prevJSON
		statusShort = prevShort
	})

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return captureStdout(t, func() error {
		return runStatus(cmd, nil)
	})
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	prevStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stdout = w
	runErr := fn()
	if err := w.Close(); err != nil {
		t.Fatalf("close write pipe: %v", err)
	}
	os.Stdout = prevStdout

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close read pipe: %v", err)
	}
	if runErr != nil {
		t.Fatalf("run status: %v", runErr)
	}
	return string(out)
}
