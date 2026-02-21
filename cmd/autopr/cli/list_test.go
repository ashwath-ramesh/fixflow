package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRunListNoJobsShowsStartHint(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeStatusConfig(t, tmp)

	out := runListWithTestConfig(t, cfg, false)
	got := strings.TrimSpace(out)
	want := "No jobs found. Run 'ap start' to begin processing issues."
	if got != want {
		t.Fatalf("unexpected output: got %q, want %q", got, want)
	}
}

func TestRunListNoJobsJSONStillWorks(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeStatusConfig(t, tmp)

	out := runListWithTestConfig(t, cfg, true)
	got := strings.TrimSpace(out)
	if strings.Contains(got, "No jobs found.") {
		t.Fatalf("unexpected human-readable message in JSON output: %q", got)
	}

	var jobs []map[string]any
	if err := json.Unmarshal([]byte(got), &jobs); err != nil {
		t.Fatalf("decode JSON jobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected no jobs in JSON output, got %d", len(jobs))
	}
}

func TestRunListSummaryLineShowsBuckets(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeStatusConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	seedStatusJobs(t, dbPath, []statusSeed{
		{state: "queued", count: 3},
		{state: "planning", count: 2},
		{state: "failed", count: 1},
		{state: "rejected", count: 2},
		{state: "cancelled", count: 1},
		{state: "approved", count: 3, merged: 3},
	})

	prevState := listState
	prevCost := listCost
	prevProject := listProject
	listState = "all"
	listCost = false
	listProject = ""
	t.Cleanup(func() {
		listState = prevState
		listCost = prevCost
		listProject = prevProject
	})

	out := runListWithTestConfig(t, cfg, false)
	got := strings.TrimSpace(out)
	want := "Total: 12 jobs (3 queued, 2 active, 4 failed, 3 merged)"
	if !strings.Contains(got, want) {
		t.Fatalf("expected summary line %q in output: %q", want, got)
	}
}

func TestRunListSummaryRespectsStateFilter(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeStatusConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	seedStatusJobs(t, dbPath, []statusSeed{
		{state: "queued", count: 3},
		{state: "planning", count: 2},
		{state: "failed", count: 1},
		{state: "approved", count: 1, merged: 0},
	})

	prevState := listState
	prevCost := listCost
	prevProject := listProject
	listState = "active"
	listCost = false
	listProject = ""
	t.Cleanup(func() {
		listState = prevState
		listCost = prevCost
		listProject = prevProject
	})

	out := runListWithTestConfig(t, cfg, false)
	got := strings.TrimSpace(out)
	want := "Total: 2 jobs (0 queued, 2 active, 0 failed, 0 merged)"
	if !strings.Contains(got, want) {
		t.Fatalf("expected summary line %q in output: %q", want, got)
	}
}

func TestRunListSummaryNotPrintedInJSON(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeStatusConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	seedStatusJobs(t, dbPath, []statusSeed{
		{state: "queued", count: 1},
	})

	prevState := listState
	prevCost := listCost
	prevProject := listProject
	listState = "all"
	listCost = false
	listProject = ""
	t.Cleanup(func() {
		listState = prevState
		listCost = prevCost
		listProject = prevProject
	})

	out := runListWithTestConfig(t, cfg, true)
	got := strings.TrimSpace(out)
	if strings.Contains(got, "Total:") {
		t.Fatalf("unexpected summary line in JSON output: %q", got)
	}

	var jobs []map[string]any
	if err := json.Unmarshal([]byte(got), &jobs); err != nil {
		t.Fatalf("decode JSON jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected one job in JSON output, got %d", len(jobs))
	}
}

func runListWithTestConfig(t *testing.T, configPath string, asJSON bool) string {
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
		return runList(cmd, nil)
	})
}
