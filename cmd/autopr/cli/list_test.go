package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"autopr/internal/db"

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

	jobs := decodeListJobs(t, out)
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

	out := runListWithTestConfigWithOptions(t, cfg, false, "", "all", "updated_at", false, false)
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

	out := runListWithTestConfigWithOptions(t, cfg, false, "", "active", "updated_at", false, false)
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

	out := runListWithTestConfig(t, cfg, true)
	got := strings.TrimSpace(out)
	if strings.Contains(got, "Total:") {
		t.Fatalf("unexpected summary line in JSON output: %q", got)
	}

	jobs := decodeListJobs(t, out)
	if len(jobs) != 1 {
		t.Fatalf("expected one job in JSON output, got %d", len(jobs))
	}
}

func TestNormalizeListSort(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{in: "updated_at", want: "updated_at"},
		{in: "created_at", want: "created_at"},
		{in: "state", want: "state"},
		{in: "project", want: "project"},
	} {
		got, err := normalizeListSort(tc.in)
		if err != nil {
			t.Fatalf("normalizeListSort(%q): unexpected error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("normalizeListSort(%q): expected %q, got %q", tc.in, tc.want, got)
		}
	}
}

func TestNormalizeListSortRejectsInvalidInput(t *testing.T) {
	_, err := normalizeListSort("bad")
	if err == nil {
		t.Fatalf("expected error for invalid sort value")
	}
	if !strings.Contains(err.Error(), "expected one of") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalizeListState(t *testing.T) {
	for _, state := range []string{"all", "active", "merged", "queued", "planning", "implementing", "reviewing", "testing", "ready", "rebasing", "resolving", "resolving_conflicts", "awaiting_checks", "approved", "rejected", "failed", "cancelled"} {
		got, err := normalizeListState(state)
		if err != nil {
			t.Fatalf("normalizeListState(%q): unexpected error: %v", state, err)
		}
		if state == "resolving" && got != "resolving_conflicts" {
			t.Fatalf("normalizeListState(%q): expected resolving_conflicts, got %q", state, got)
		}
		if state != "resolving" && got != state {
			t.Fatalf("normalizeListState(%q): expected same value, got %q", state, got)
		}
	}
}

func TestNormalizeListStateRejectsInvalidInput(t *testing.T) {
	_, err := normalizeListState("bad")
	if err == nil {
		t.Fatalf("expected error for invalid state value")
	}
	if !strings.Contains(err.Error(), "expected one of") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunListSortByCreatedAtHonorsDirection(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeStatusConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	ids := createListJobsForTest(t, dbPath, []listJobSeed{
		{state: "queued", createdAt: "2025-01-01T00:00:00Z", updatedAt: "2025-01-01T00:00:00Z"},
		{state: "queued", createdAt: "2025-01-03T00:00:00Z", updatedAt: "2025-01-03T00:00:00Z"},
		{state: "queued", createdAt: "2025-01-02T00:00:00Z", updatedAt: "2025-01-02T00:00:00Z"},
	})

	out := runListWithTestConfigWithOptions(t, cfg, true, "", "all", "created_at", false, false)
	desc := decodeListJobs(t, out)
	if got, want := jobIDs(desc), []string{ids[1], ids[2], ids[0]}; !slicesEqual(got, want) {
		t.Fatalf("created_at desc: expected %v, got %v", want, got)
	}

	out = runListWithTestConfigWithOptions(t, cfg, true, "", "all", "created_at", true, false)
	asc := decodeListJobs(t, out)
	if got, want := jobIDs(asc), []string{ids[0], ids[2], ids[1]}; !slicesEqual(got, want) {
		t.Fatalf("created_at asc: expected %v, got %v", want, got)
	}
}

func TestRunListSortByState(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeStatusConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	ids := createListJobsForTest(t, dbPath, []listJobSeed{
		{state: "failed", createdAt: "2025-01-01T00:00:00Z", updatedAt: "2025-01-01T00:00:00Z"},
		{state: "planning", createdAt: "2025-01-01T00:00:00Z", updatedAt: "2025-01-01T00:00:00Z"},
		{state: "queued", createdAt: "2025-01-01T00:00:00Z", updatedAt: "2025-01-01T00:00:00Z"},
	})

	out := runListWithTestConfigWithOptions(t, cfg, true, "", "all", "state", true, false)
	jobs := decodeListJobs(t, out)
	if got, want := jobIDs(jobs), []string{ids[2], ids[1], ids[0]}; !slicesEqual(got, want) {
		t.Fatalf("state sort asc: expected %v, got %v", want, got)
	}
}

func TestRunListSortByProject(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeStatusConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	ids := createListJobsForTest(t, dbPath, []listJobSeed{
		{state: "queued", project: "zulu", createdAt: "2025-01-01T00:00:00Z", updatedAt: "2025-01-01T00:00:00Z"},
		{state: "queued", project: "alpha", createdAt: "2025-01-01T00:00:00Z", updatedAt: "2025-01-01T00:00:00Z"},
		{state: "queued", project: "bravo", createdAt: "2025-01-01T00:00:00Z", updatedAt: "2025-01-01T00:00:00Z"},
	})

	out := runListWithTestConfigWithOptions(t, cfg, true, "", "all", "project", true, false)
	jobs := decodeListJobs(t, out)
	if got, want := jobIDs(jobs), []string{ids[1], ids[2], ids[0]}; !slicesEqual(got, want) {
		t.Fatalf("project sort asc: expected %v, got %v", want, got)
	}
}

func TestRunListStateFiltersSpecialValues(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeStatusConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	ids := createListJobsForTest(t, dbPath, []listJobSeed{
		{state: "planning", updatedAt: "2025-01-03T00:00:00Z"},
		{state: "queued", updatedAt: "2025-01-04T00:00:00Z"},
		{state: "approved", updatedAt: "2025-01-05T00:00:00Z", mergedAt: "2025-01-05T00:00:00Z"},
		{state: "rebasing", updatedAt: "2025-01-02T00:00:00Z"},
		{state: "resolving_conflicts", updatedAt: "2025-01-01T00:00:00Z"},
	})

	tests := []struct {
		name   string
		state  string
		wantID []string
	}{
		{name: "merged", state: "merged", wantID: []string{ids[2]}},
		{name: "active", state: "active", wantID: []string{ids[0], ids[3], ids[4]}},
		{name: "rebasing", state: "rebasing", wantID: []string{ids[3]}},
		{name: "resolving", state: "resolving", wantID: []string{ids[4]}},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			out := runListWithTestConfigWithOptions(t, cfg, true, "", tc.state, "updated_at", false, false)
			jobs := decodeListJobs(t, out)
			gotIDs := jobIDs(jobs)
			if !slicesEqual(gotIDs, tc.wantID) {
				t.Fatalf("state=%q: expected job IDs %v, got %v", tc.state, tc.wantID, gotIDs)
			}
		})
	}
}

func TestRunListInvalidSortReturnsError(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeStatusConfig(t, tmp)

	_, err := runListWithTestConfigWithOptionsResult(t, cfg, true, "", "all", "bad", false, false)
	if err == nil {
		t.Fatalf("expected invalid sort error")
	}
	if !strings.Contains(err.Error(), "invalid --sort") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunListInvalidStateReturnsError(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeStatusConfig(t, tmp)

	_, err := runListWithTestConfigWithOptionsResult(t, cfg, true, "", "bad", "updated_at", false, false)
	if err == nil {
		t.Fatalf("expected invalid state error")
	}
	if !strings.Contains(err.Error(), "invalid --state") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunListRejectsConflictingDirectionFlags(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeStatusConfig(t, tmp)

	_, err := runListWithTestConfigWithOptionsResult(t, cfg, true, "", "all", "updated_at", true, true)
	if err == nil {
		t.Fatalf("expected conflicting direction flags error")
	}
	if !strings.Contains(err.Error(), "--asc") || !strings.Contains(err.Error(), "--desc") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Pagination tests

func TestRunListPaginationFirstPage(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeStatusConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	ordered := seedPaginationJobs(t, dbPath)

	out := runListWithTestConfigPagination(t, cfg, false, "--page", "1", "--page-size", "2")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 {
		t.Fatalf("unexpected short output: %q", out)
	}
	if !strings.Contains(out, "Page 1/3, total rows: 5") {
		t.Fatalf("expected metadata line, got %q", out)
	}

	gotIDs := extractListOutputIDs(out)
	want := []string{db.ShortID(ordered[4]), db.ShortID(ordered[3])}
	if !reflect.DeepEqual(gotIDs, want) {
		t.Fatalf("unexpected first-page IDs: got %v want %v", gotIDs, want)
	}
}

func TestRunListPaginationLastPage(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeStatusConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	ordered := seedPaginationJobs(t, dbPath)

	out := runListWithTestConfigPagination(t, cfg, false, "--page", "3", "--page-size", "2")
	if !strings.Contains(out, "Page 3/3, total rows: 5") {
		t.Fatalf("expected metadata line, got %q", out)
	}

	gotIDs := extractListOutputIDs(out)
	want := []string{db.ShortID(ordered[0])}
	if !reflect.DeepEqual(gotIDs, want) {
		t.Fatalf("unexpected last-page IDs: got %v want %v", gotIDs, want)
	}
}

func TestRunListPaginationOutOfRangePage(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeStatusConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	seedPaginationJobs(t, dbPath)

	out := runListWithTestConfigPagination(t, cfg, false, "--page", "4", "--page-size", "2")
	if !strings.Contains(out, "Page 4/3, total rows: 5") {
		t.Fatalf("expected metadata line, got %q", out)
	}
	if strings.Contains(out, "No jobs found.") {
		t.Fatalf("unexpected no-jobs message for paged output: %q", out)
	}
	if !strings.Contains(strings.TrimSpace(out), "Total: 0 jobs (0 queued, 0 active, 0 failed, 0 merged)") {
		t.Fatalf("expected empty summary for out-of-range page, got %q", out)
	}
}

func TestRunListPaginationInvalidPageSize(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeStatusConfig(t, tmp)

	if _, err := runListWithTestConfigPaginationError(t, cfg, false, "--page", "0", "--page-size", "2"); err == nil {
		t.Fatalf("expected error for invalid page")
	}
	if _, err := runListWithTestConfigPaginationError(t, cfg, false, "--page", "1", "--page-size", "0"); err == nil {
		t.Fatalf("expected error for page-size 0")
	}
	if _, err := runListWithTestConfigPaginationError(t, cfg, false, "--page", "1", "--page-size", "-2"); err == nil {
		t.Fatalf("expected error for negative page-size")
	}
}

func TestRunListAllOverridesPaginationFlags(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeStatusConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	ordered := seedPaginationJobs(t, dbPath)

	out := runListWithTestConfigPagination(t, cfg, false, "--all", "--page", "1", "--page-size", "2")
	if strings.Contains(out, "Page") {
		t.Fatalf("unexpected pagination metadata with --all: %q", out)
	}

	gotIDs := extractListOutputIDs(out)
	if len(gotIDs) != len(ordered) {
		t.Fatalf("expected full output with --all, got %d rows", len(gotIDs))
	}
}

func TestRunListPaginationJSONPayload(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeStatusConfig(t, tmp)
	dbPath := filepath.Join(tmp, "autopr.db")
	seedPaginationJobs(t, dbPath)

	out := runListWithTestConfigPagination(t, cfg, true, "--page", "2", "--page-size", "2")
	got := strings.TrimSpace(out)
	var payload struct {
		Jobs     []map[string]any `json:"jobs"`
		Page     int              `json:"page"`
		PageSize int              `json:"page_size"`
		Total    int              `json:"total"`
	}
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("decode paged JSON: %v", err)
	}
	if payload.Page != 2 {
		t.Fatalf("expected page=2, got %d", payload.Page)
	}
	if payload.PageSize != 2 {
		t.Fatalf("expected page_size=2, got %d", payload.PageSize)
	}
	if payload.Total != 5 {
		t.Fatalf("expected total=5, got %d", payload.Total)
	}
	if len(payload.Jobs) != 2 {
		t.Fatalf("expected 2 jobs on page, got %d", len(payload.Jobs))
	}
}

// Test helpers

func runListWithTestConfig(t *testing.T, configPath string, asJSON bool, extraArgs ...string) string {
	if len(extraArgs) > 0 {
		out, err := runListWithTestConfigPaginationError(t, configPath, asJSON, extraArgs...)
		if err != nil {
			t.Fatalf("run list: %v", err)
		}
		return out
	}
	out, err := runListWithTestConfigWithOptionsResult(t, configPath, asJSON, "", "all", "updated_at", false, false)
	if err != nil {
		t.Fatalf("run list: %v", err)
	}
	return out
}

func runListWithTestConfigWithOptions(t *testing.T, configPath string, asJSON bool, project, state, sort string, asc, desc bool) string {
	out, err := runListWithTestConfigWithOptionsResult(t, configPath, asJSON, project, state, sort, asc, desc)
	if err != nil {
		t.Fatalf("run list: %v", err)
	}
	return out
}

func runListWithTestConfigWithOptionsResult(t *testing.T, configPath string, asJSON bool, project, state, sort string, asc, desc bool) (string, error) {
	t.Helper()
	prevCfgPath := cfgPath
	prevJSON := jsonOut
	prevProject := listProject
	prevState := listState
	prevSort := listSort
	prevAsc := listAsc
	prevDesc := listDesc
	prevCost := listCost
	prevPage := listPage
	prevPageSize := listPageSize
	prevAll := listAll
	cfgPath = configPath
	jsonOut = asJSON
	listProject = project
	listState = state
	listSort = sort
	listAsc = asc
	listDesc = desc
	listCost = false
	listPage = 1
	listPageSize = 20
	listAll = false
	t.Cleanup(func() {
		cfgPath = prevCfgPath
		jsonOut = prevJSON
		listProject = prevProject
		listState = prevState
		listSort = prevSort
		listAsc = prevAsc
		listDesc = prevDesc
		listCost = prevCost
		listPage = prevPage
		listPageSize = prevPageSize
		listAll = prevAll
	})

	cmd := &cobra.Command{}
	cmd.Flags().StringVar(&listProject, "project", "", "filter by project name")
	cmd.Flags().StringVar(&listState, "state", "all", "filter by state")
	cmd.Flags().StringVar(&listSort, "sort", "updated_at", "sort by field")
	cmd.Flags().BoolVar(&listAsc, "asc", false, "ascending")
	cmd.Flags().BoolVar(&listDesc, "desc", false, "descending")
	cmd.Flags().BoolVar(&listCost, "cost", false, "show estimated cost column")
	cmd.Flags().IntVar(&listPage, "page", 1, "page number (1-based)")
	cmd.Flags().IntVar(&listPageSize, "page-size", 20, "number of rows per page")
	cmd.Flags().BoolVar(&listAll, "all", false, "disable pagination and show full output")
	cmd.SetContext(context.Background())

	return captureStdoutWithError(t, func() error {
		return runList(cmd, nil)
	})
}

func runListWithTestConfigPagination(t *testing.T, configPath string, asJSON bool, args ...string) string {
	out, err := runListWithTestConfigPaginationError(t, configPath, asJSON, args...)
	if err != nil {
		t.Fatalf("run list: %v", err)
	}
	return out
}

func runListWithTestConfigPaginationError(t *testing.T, configPath string, asJSON bool, args ...string) (string, error) {
	t.Helper()
	prevCfgPath := cfgPath
	prevJSON := jsonOut
	prevProject := listProject
	prevState := listState
	prevSort := listSort
	prevAsc := listAsc
	prevDesc := listDesc
	prevCost := listCost
	prevPage := listPage
	prevPageSize := listPageSize
	prevAll := listAll

	cfgPath = configPath
	jsonOut = asJSON
	listProject = ""
	listState = "all"
	listSort = "updated_at"
	listAsc = false
	listDesc = false
	listCost = false
	listPage = 1
	listPageSize = 20
	listAll = false
	t.Cleanup(func() {
		cfgPath = prevCfgPath
		jsonOut = prevJSON
		listProject = prevProject
		listState = prevState
		listSort = prevSort
		listAsc = prevAsc
		listDesc = prevDesc
		listCost = prevCost
		listPage = prevPage
		listPageSize = prevPageSize
		listAll = prevAll
	})

	cmd := &cobra.Command{}
	cmd.Flags().StringVar(&listProject, "project", "", "filter by project name")
	cmd.Flags().StringVar(&listState, "state", "all", "filter by state")
	cmd.Flags().StringVar(&listSort, "sort", "updated_at", "sort by field")
	cmd.Flags().BoolVar(&listAsc, "asc", false, "ascending")
	cmd.Flags().BoolVar(&listDesc, "desc", false, "descending")
	cmd.Flags().BoolVar(&listCost, "cost", false, "show estimated cost column")
	cmd.Flags().IntVar(&listPage, "page", 1, "page number (1-based)")
	cmd.Flags().IntVar(&listPageSize, "page-size", 20, "number of rows per page")
	cmd.Flags().BoolVar(&listAll, "all", false, "disable pagination and show full output")
	cmd.SetArgs(args)
	if err := cmd.ParseFlags(args); err != nil {
		return "", err
	}
	cmd.SetContext(context.Background())

	prevStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stdout = w

	runErr := runList(cmd, nil)

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
	return string(out), runErr
}

func extractListOutputIDs(output string) []string {
	var ids []string
	re := regexp.MustCompile(`^[0-9a-f]{8}$`)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "JOB" || fields[0] == "Total:" || fields[0] == "Page" {
			continue
		}
		if re.MatchString(fields[0]) {
			ids = append(ids, fields[0])
		}
	}
	return ids
}

func seedPaginationJobs(t *testing.T, dbPath string) []string {
	t.Helper()
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	var ordered []string
	updates := []struct {
		source string
		time   string
	}{
		{"job-1", "2026-02-01T10:00:00Z"},
		{"job-2", "2026-02-02T10:00:00Z"},
		{"job-3", "2026-02-03T10:00:00Z"},
		{"job-4", "2026-02-04T10:00:00Z"},
		{"job-5", "2026-02-05T10:00:00Z"},
	}

	for _, entry := range updates {
		issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
			ProjectName:   "project",
			Source:        "github",
			SourceIssueID: entry.source,
			Title:         entry.source,
			URL:           "https://example.com/" + entry.source,
			State:         "open",
		})
		if err != nil {
			t.Fatalf("upsert issue %q: %v", entry.source, err)
		}
		jobID, err := store.CreateJob(ctx, issueID, "project", 3)
		if err != nil {
			t.Fatalf("create job %q: %v", entry.source, err)
		}
		if _, err := store.Writer.ExecContext(ctx, `
UPDATE jobs
SET state = 'queued', updated_at = ?, created_at = ?
WHERE id = ?`, entry.time, entry.time, jobID); err != nil {
			t.Fatalf("set times for %q: %v", entry.source, err)
		}
		ordered = append(ordered, jobID)
	}
	return ordered
}

type listJobSeed struct {
	state     string
	project   string
	createdAt string
	updatedAt string
	mergedAt  string
}

func createListJobsForTest(t *testing.T, dbPath string, seeds []listJobSeed) []string {
	t.Helper()
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	var ids []string
	for i, seed := range seeds {
		project := seed.project
		if project == "" {
			project = "project"
		}
		createdAt := seed.createdAt
		if createdAt == "" {
			createdAt = fmt.Sprintf("2025-01-0%dT00:00:00Z", i+1)
		}
		updatedAt := seed.updatedAt
		if updatedAt == "" {
			updatedAt = createdAt
		}

		issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
			ProjectName:   project,
			Source:        "github",
			SourceIssueID: fmt.Sprintf("issue-%d", i+1),
			Title:         fmt.Sprintf("issue-%d", i+1),
			URL:           fmt.Sprintf("https://example.com/%d", i+1),
			State:         "open",
		})
		if err != nil {
			t.Fatalf("upsert issue %d: %v", i+1, err)
		}
		jobID, err := store.CreateJob(ctx, issueID, project, 3)
		if err != nil {
			t.Fatalf("create job %d: %v", i+1, err)
		}
		if _, err := store.Writer.ExecContext(ctx, `UPDATE jobs SET state = ?, project_name = ?, created_at = ?, updated_at = ?, pr_merged_at = ? WHERE id = ?`, seed.state, project, createdAt, updatedAt, seed.mergedAt, jobID); err != nil {
			t.Fatalf("seed job %d: %v", i+1, err)
		}
		ids = append(ids, jobID)
	}
	return ids
}

func decodeListJobs(t *testing.T, out string) []db.Job {
	t.Helper()
	var jobs []db.Job
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &jobs); err != nil {
		t.Fatalf("decode JSON jobs: %v", err)
	}
	return jobs
}

func jobIDs(jobs []db.Job) []string {
	ids := make([]string, len(jobs))
	for i, job := range jobs {
		ids[i] = job.ID
	}
	return ids
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func captureStdoutWithError(t *testing.T, fn func() error) (string, error) {
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
	return strings.TrimSpace(string(out)), runErr
}
