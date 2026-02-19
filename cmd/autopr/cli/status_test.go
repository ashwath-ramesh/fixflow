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

	out := runStatusWithTestConfig(t, cfgPath, true)

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

	out := runStatusWithTestConfig(t, cfgPath, false)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected daemon + jobs lines, got %d lines: %q", len(lines), out)
	}
	if lines[0] != "Daemon: stopped" {
		t.Fatalf("unexpected daemon line: %q", lines[0])
	}
	if lines[1] != "Jobs: queued=1 active=1 planning=1 implementing=0 reviewing=0 testing=0 rebasing=0 resolving=0 needs_pr=1 failed=0 cancelled=0 pr_created=1 merged=0 rejected=0" {
		t.Fatalf("unexpected jobs line: %q", lines[1])
	}
}

func TestRunStatusJSONNoPidFile(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeStatusConfig(t, tmp)

	out := runStatusWithTestConfig(t, cfgPath, true)

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

	out := runStatusWithTestConfig(t, cfgPath, true)

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
	out := runStatusWithTestConfig(t, cfgPath, true)

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

func runStatusWithTestConfig(t *testing.T, configPath string, asJSON bool) string {
	t.Helper()
	prevCfgPath := cfgPath
	prevJSON := jsonOut
	cfgPath = configPath
	jsonOut = asJSON
	t.Cleanup(func() {
		cfgPath = prevCfgPath
		jsonOut = prevJSON
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
