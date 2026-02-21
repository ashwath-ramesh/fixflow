package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"autopr/internal/db"

	"github.com/spf13/cobra"
)

func TestRunResumeResetsFailedJobAndKeepsIteration(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	configPath := writeMergeConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")

	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	jobID := createMergeJobForTest(t, dbPath, "project", "resume-1", "failed", "", "")
	if _, err := store.Writer.ExecContext(context.Background(), `UPDATE jobs SET iteration = 4 WHERE id = ?`, jobID); err != nil {
		t.Fatalf("seed iteration: %v", err)
	}

	prevCfgPath := cfgPath
	prevJSON := jsonOut
	cfgPath = configPath
	jsonOut = false
	defer func() {
		cfgPath = prevCfgPath
		jsonOut = prevJSON
	}()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := runResume(cmd, []string{db.ShortID(jobID)}); err != nil {
		t.Fatalf("runResume: %v", err)
	}

	job, err := store.GetJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "queued" {
		t.Fatalf("expected queued, got %q", job.State)
	}
	if job.Iteration != 4 {
		t.Fatalf("expected iteration 4, got %d", job.Iteration)
	}
}

func TestRunResumeRejectsBlockedStates(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	configPath := writeMergeConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")

	prevCfgPath := cfgPath
	cfgPath = configPath
	defer func() {
		cfgPath = prevCfgPath
	}()

	states := []string{"queued", "planning", "implementing", "ready"}
	for _, state := range states {
		t.Run(state, func(t *testing.T) {
			store, err := db.Open(dbPath)
			if err != nil {
				t.Fatalf("open db: %v", err)
			}
			defer store.Close()

			jobID := createMergeJobForTest(t, dbPath, "project", "resume-blocked-"+state, state, "", "")
			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())
			err = runResume(cmd, []string{db.ShortID(jobID)})
			if err == nil {
				t.Fatalf("expected blocked state %s to fail", state)
			}
			if !strings.Contains(err.Error(), "must be 'failed'") || !strings.Contains(err.Error(), "cancelled'") {
				t.Fatalf("unexpected blocked-state error: %v", err)
			}
		})
	}
}

func TestRunResumeJSONOutput(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	configPath := writeMergeConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")

	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	jobID := createMergeJobForTest(t, dbPath, "project", "resume-json", "cancelled", "", "")

	prevCfgPath := cfgPath
	prevJSON := jsonOut
	cfgPath = configPath
	jsonOut = true
	defer func() {
		cfgPath = prevCfgPath
		jsonOut = prevJSON
	}()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	out := captureStdout(t, func() error {
		return runResume(cmd, []string{jobID})
	})

	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("decode json output: %v", err)
	}
	if got["job_id"] != jobID {
		t.Fatalf("expected job_id=%q, got %#v", jobID, got["job_id"])
	}
	if got["state"] != "queued" {
		t.Fatalf("expected state queued, got %#v", got["state"])
	}
}

func TestRunResumeActiveSiblingGuard(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	configPath := writeMergeConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")

	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	jobA := createMergeJobForTest(t, dbPath, "project", "resume-active-a", "failed", "", "")
	jobB := createMergeJobForTest(t, dbPath, "project", "resume-active-b", "queued", "", "")

	prevCfgPath := cfgPath
	cfgPath = configPath
	defer func() {
		cfgPath = prevCfgPath
	}()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err = runResume(cmd, []string{db.ShortID(jobA)})
	if err == nil {
		t.Fatalf("expected active sibling error")
	}
	if !strings.Contains(err.Error(), "another active job") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), jobB) {
		t.Fatalf("expected sibling job id %s in error, got %v", jobB, err)
	}
}

func TestResumeCommandRegistered(t *testing.T) {
	found := false
	for _, c := range rootCmd.Commands() {
		if c.Name() == "resume" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("resume command not registered on root")
	}
}
