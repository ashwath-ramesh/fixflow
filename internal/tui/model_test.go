package tui

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"autopr/internal/config"
	"autopr/internal/db"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

var ansiRegexp = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestFilterGhostSessions(t *testing.T) {
	sessions := []db.LLMSessionSummary{
		{ID: 1, Step: "plan", Status: "running", InputTokens: 0, OutputTokens: 0, DurationMS: 0},
		{ID: 2, Step: "plan", Status: "completed", InputTokens: 12, OutputTokens: 8, DurationMS: 1200},
		{ID: 3, Step: "implement", Status: "running", InputTokens: 0, OutputTokens: 0, DurationMS: 0},
		{ID: 4, Step: "code_review", Status: "running", InputTokens: 5, OutputTokens: 2, DurationMS: 800},
	}

	filtered := filterGhostSessions(sessions, "implement")
	if len(filtered) != 3 {
		t.Fatalf("expected 3 sessions after filtering, got %d", len(filtered))
	}
	if filtered[0].ID != 2 {
		t.Fatalf("expected completed session id=2 first, got id=%d", filtered[0].ID)
	}
	if filtered[1].ID != 3 {
		t.Fatalf("expected active running session id=3 to be kept, got id=%d", filtered[1].ID)
	}
	if filtered[2].ID != 4 {
		t.Fatalf("expected non-ghost running session id=4 to be kept, got id=%d", filtered[2].ID)
	}
}

func TestSelectedStyleRendersBackgroundInANSI256(t *testing.T) {
	renderer := lipgloss.NewRenderer(io.Discard)
	renderer.SetColorProfile(termenv.ANSI256)
	rendered := selectedStyle.Renderer(renderer).Render("selected row")
	if !strings.Contains(rendered, "48;5;236") {
		t.Fatalf("expected selectedStyle to render ANSI256 background 236, got: %q", rendered)
	}
}

func TestListViewSelectedRowKeepsBackgroundAcrossStyledCells(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	job := db.Job{
		ID:            "ap-job-1234",
		State:         "ready",
		ProjectName:   "proj",
		IssueSource:   "github",
		SourceIssueID: "42",
		IssueTitle:    "selected row background",
		UpdatedAt:     "2025-02-19T14:04:05Z",
	}
	m := Model{
		cfg: &config.Config{
			Daemon: config.DaemonConfig{
				SyncInterval: "5m",
				MaxWorkers:   1,
			},
		},
		jobs:   []db.Job{job},
		cursor: 0,
	}

	line := findLineContainingText(t, m.listView(), db.ShortID(job.ID))
	if !strings.Contains(stripANSI(line), "> ") {
		t.Fatalf("expected selected cursor marker in line, got: %q", stripANSI(line))
	}
	if got := strings.Count(line, "48;5;236"); got < 3 {
		t.Fatalf("expected selected row background to be reapplied across styled cells, got %d: %q", got, line)
	}
}

func TestDetailViewSelectedPipelineRowKeepsBackgroundAcrossStyledCells(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	job := db.Job{
		ID:          "ap-job-1234",
		State:       "implementing",
		ProjectName: "proj",
	}
	m := Model{
		selected: &job,
		sessions: []db.LLMSessionSummary{
			{
				ID:           1,
				Step:         "plan",
				Status:       "completed",
				LLMProvider:  "codex",
				InputTokens:  1,
				OutputTokens: 2,
				DurationMS:   3000,
				CreatedAt:    "2025-02-19T14:01:02Z",
			},
		},
		sessCursor: 0,
	}

	line := findLineContainingText(t, m.detailView(), "codex")
	if !strings.Contains(stripANSI(line), "> ") {
		t.Fatalf("expected selected cursor marker in line, got: %q", stripANSI(line))
	}
	if got := strings.Count(line, "48;5;236"); got < 3 {
		t.Fatalf("expected selected pipeline row background to be reapplied across styled cells, got %d: %q", got, line)
	}
}

func stripANSI(s string) string {
	return ansiRegexp.ReplaceAllString(s, "")
}

func findLineContainingText(t *testing.T, view, want string) string {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(stripANSI(line), want) {
			return line
		}
	}
	t.Fatalf("could not find line containing %q in:\n%s", want, view)
	return ""
}

func findLineContainingAll(t *testing.T, view string, wants ...string) string {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		plain := stripANSI(line)
		ok := true
		for _, want := range wants {
			if !strings.Contains(plain, want) {
				ok = false
				break
			}
		}
		if ok {
			return plain
		}
	}
	t.Fatalf("could not find line containing all %v in:\n%s", wants, view)
	return ""
}

func TestListViewShowsFilterIndicatorAndFooterHints(t *testing.T) {
	t.Parallel()
	jobs := []db.Job{
		{ID: "ap-job-1", State: "queued", ProjectName: "autopr"},
		{ID: "ap-job-2", State: "ready", ProjectName: "other"},
	}
	m := Model{
		cfg: &config.Config{
			Daemon: config.DaemonConfig{
				SyncInterval: "5m",
				MaxWorkers:   1,
			},
		},
		jobs:          jobs,
		allJobsCounts: jobs,
		filterState:   "ready",
		filterProject: "autopr",
		filterMode:    false,
		cursor:        0,
	}

	view := m.listView()
	if !strings.Contains(view, "Filter: state=ready  project=autopr") {
		t.Fatalf("expected filter indicator, got:\n%s", view)
	}
	if !strings.Contains(view, "f filter") {
		t.Fatalf("expected filter hint, got:\n%s", view)
	}
	if !strings.Contains(view, "F clear filters") {
		t.Fatalf("expected clear filters hint, got:\n%s", view)
	}

	m.filterMode = true
	filterModeView := m.listView()
	for _, want := range []string{"s state", "p project", "F clear all", "esc done"} {
		if !strings.Contains(filterModeView, want) {
			t.Fatalf("expected filter-mode hint %q, got:\n%s", want, filterModeView)
		}
	}

	m = newTestModelForFilterCycle(jobs)
	modelAny, _ := m.handleKey(keyRunes('f'))
	m = modelAny.(Model)
	m.cursor = 3
	modelAny, _ = m.handleKey(keyRunes('s'))
	m = modelAny.(Model)
	if m.cursor != 0 {
		t.Fatalf("expected cursor reset to 0 after filter state change")
	}

	m.cursor = 3
	modelAny, _ = m.handleKey(keyRunes('p'))
	m = modelAny.(Model)
	if m.cursor != 0 {
		t.Fatalf("expected cursor reset to 0 after filter project change")
	}

	m = newTestModelForFilterCycle(jobs)
	m.filterState = "ready"
	m.filterProject = "other"
	modelAny, _ = m.handleKey(keyRunes('F'))
	m = modelAny.(Model)
	if m.filterState != filterAllState || m.filterProject != filterAllProject {
		t.Fatalf("expected F to clear filters, got state=%q project=%q", m.filterState, m.filterProject)
	}
}

func TestHandleKeyFEntersFilterMode(t *testing.T) {
	t.Parallel()
	m := newTestModelForFilterCycle([]db.Job{
		{ID: "ap-job-1", ProjectName: "proj-a", State: "ready"},
		{ID: "ap-job-2", ProjectName: "proj-b", State: "queued"},
	})
	m.filterState = "ready"
	m.filterProject = "proj-a"
	m.cursor = 1

	modelAny, _ := m.handleKey(keyRunes('f'))
	m = modelAny.(Model)

	if !m.filterMode {
		t.Fatalf("expected filter mode after pressing f")
	}
	if m.filterStateDraft != "ready" {
		t.Fatalf("expected draft state to match current state, got %q", m.filterStateDraft)
	}
	if m.filterProjectDraft != "proj-a" {
		t.Fatalf("expected draft project to match current project, got %q", m.filterProjectDraft)
	}
	if m.filterStateBefore != "ready" {
		t.Fatalf("expected draft state backup to match current state, got %q", m.filterStateBefore)
	}
	if m.filterProjectBefore != "proj-a" {
		t.Fatalf("expected draft project backup to match current project, got %q", m.filterProjectBefore)
	}
	if m.filterCursorBefore != 1 {
		t.Fatalf("expected cursor backup to match prior cursor, got %d", m.filterCursorBefore)
	}
}

func TestShouldHideGhostSession(t *testing.T) {
	ghost := db.LLMSessionSummary{Step: "plan", Status: "running", InputTokens: 0, OutputTokens: 0, DurationMS: 0}
	if !shouldHideGhostSession(ghost, "") {
		t.Fatalf("expected ghost session to be hidden when no active step")
	}
	if shouldHideGhostSession(ghost, "plan") {
		t.Fatalf("expected active running session to be visible")
	}

	nonGhost := db.LLMSessionSummary{Step: "plan", Status: "running", InputTokens: 1, OutputTokens: 0, DurationMS: 0}
	if shouldHideGhostSession(nonGhost, "") {
		t.Fatalf("did not expect non-ghost running session to be hidden")
	}
}

func TestListViewCancelPromptAndFooter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	m, store, jobID := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()

	view := m.listView()
	if !strings.Contains(view, "c cancel") {
		t.Fatalf("expected list footer to include cancel hint, got:\n%s", view)
	}

	modelAny, _ := m.handleKey(keyRunes('c'))
	m = modelAny.(Model)
	if m.confirmAction != "cancel" {
		t.Fatalf("expected confirmAction=cancel, got %q", m.confirmAction)
	}
	if m.confirmJobID != jobID {
		t.Fatalf("expected confirmJobID=%q, got %q", jobID, m.confirmJobID)
	}
	if !strings.Contains(m.listView(), "Cancel job "+db.ShortID(jobID)+"? (y/n)") {
		t.Fatalf("expected cancel confirmation prompt in list view")
	}

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "queued" {
		t.Fatalf("expected queued before confirmation, got %q", job.State)
	}
}

func TestListViewSortKeysCycleAndRefresh(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	m, store, _ := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()

	modelAny, cmd := m.handleKey(keyRunes('s'))
	m = modelAny.(Model)
	if m.sortColumn != "state" {
		t.Fatalf("expected sortColumn=state after first s, got %q", m.sortColumn)
	}
	if m.cursor != 0 {
		t.Fatalf("expected cursor reset to 0 on sort change")
	}
	if cmd == nil {
		t.Fatalf("expected fetchJobs command for sort change")
	}

	modelAny, cmd = m.handleKey(keyRunes('s'))
	m = modelAny.(Model)
	if m.sortColumn != "project" {
		t.Fatalf("expected sortColumn=project after second s, got %q", m.sortColumn)
	}
	if cmd == nil {
		t.Fatalf("expected fetchJobs command for sort change")
	}

	modelAny, cmd = m.handleKey(keyRunes('s'))
	m = modelAny.(Model)
	if m.sortColumn != "created_at" {
		t.Fatalf("expected sortColumn=created_at after third s, got %q", m.sortColumn)
	}
	if cmd == nil {
		t.Fatalf("expected fetchJobs command for sort change")
	}

	modelAny, cmd = m.handleKey(keyRunes('s'))
	m = modelAny.(Model)
	if m.sortColumn != "updated_at" {
		t.Fatalf("expected sortColumn=updated_at after fourth s, got %q", m.sortColumn)
	}
	if cmd == nil {
		t.Fatalf("expected fetchJobs command for sort change")
	}
}

func TestListViewToggleSortDirection(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	m, store, _ := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()
	m.cursor = 3

	modelAny, cmd := m.handleKey(keyRunes('S'))
	m = modelAny.(Model)
	if !m.sortAsc {
		t.Fatalf("expected sortAsc=true after toggle")
	}
	if m.cursor != 0 {
		t.Fatalf("expected cursor reset to 0 on sort direction toggle")
	}
	if cmd == nil {
		t.Fatalf("expected fetchJobs command for sort direction toggle")
	}
}

func TestListViewSortIndicatorInHeader(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	m, store, _ := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()

	view := m.listView()
	if !strings.Contains(view, "UPDATED ▼") {
		t.Fatalf("expected default sort indicator on UPDATED header, got:\n%s", view)
	}

	m.sortColumn = "project"
	m.sortAsc = true
	view = m.listView()
	if !strings.Contains(view, "PROJECT ▲") {
		t.Fatalf("expected active sort indicator on PROJECT header, got:\n%s", view)
	}

	m.sortColumn = "created_at"
	m.sortAsc = false
	view = m.listView()
	if !strings.Contains(view, "CREATED ▼") {
		t.Fatalf("expected active sort indicator on CREATED header for created_at sort, got:\n%s", view)
	}
}

func TestListViewSortFooterHints(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	m, store, _ := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()

	view := m.listView()
	if !strings.Contains(view, "s sort") {
		t.Fatalf("expected footer hint for sort key, got:\n%s", view)
	}
	if !strings.Contains(view, "S sort dir") {
		t.Fatalf("expected footer hint for sort direction key, got:\n%s", view)
	}
}

func TestDetailViewCancelPromptAndConfirmNo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	m, store, jobID := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()
	m.selected = &m.jobs[0]

	if !strings.Contains(m.detailView(), "c cancel") {
		t.Fatalf("expected detail footer to include cancel hint")
	}

	modelAny, _ := m.handleKey(keyRunes('c'))
	m = modelAny.(Model)
	if m.confirmAction != "cancel" {
		t.Fatalf("expected confirmAction=cancel, got %q", m.confirmAction)
	}
	if !strings.Contains(m.detailView(), "Cancel job "+db.ShortID(jobID)+"? (y/n)") {
		t.Fatalf("expected cancel prompt in detail view")
	}

	modelAny, _ = m.handleKey(keyRunes('n'))
	m = modelAny.(Model)
	if m.confirmAction != "" {
		t.Fatalf("expected cancel confirmation cleared, got %q", m.confirmAction)
	}

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "queued" {
		t.Fatalf("expected queued after cancel abort, got %q", job.State)
	}
}

func TestDetailViewMergeHintVisibility(t *testing.T) {
	t.Parallel()

	job := db.Job{
		ID:         "ap-job-merge-1",
		State:      "approved",
		PRURL:      "https://github.com/org/repo/pull/1",
		PRClosedAt: "",
	}
	m := Model{selected: &job}
	if !strings.Contains(m.detailView(), "m merge") {
		t.Fatalf("expected merge hint for approved job with open PR")
	}

	job.State = "ready"
	m.selected = &job
	if strings.Contains(m.detailView(), "m merge") {
		t.Fatalf("did not expect merge hint when job not approved")
	}

	job.State = "approved"
	job.PRURL = ""
	m.selected = &job
	if strings.Contains(m.detailView(), "m merge") {
		t.Fatalf("did not expect merge hint when PR URL missing")
	}

	job.PRURL = "https://github.com/org/repo/pull/1"
	job.PRMergedAt = "2025-02-19T14:04:05Z"
	m.selected = &job
	if strings.Contains(m.detailView(), "m merge") {
		t.Fatalf("did not expect merge hint when PR already merged")
	}

	job.PRMergedAt = ""
	job.PRClosedAt = "2025-02-19T14:05:06Z"
	m.selected = &job
	if strings.Contains(m.detailView(), "m merge") {
		t.Fatalf("did not expect merge hint when PR already closed")
	}
}

func TestHandleKeyMergeStartsConfirmationWhenEligible(t *testing.T) {
	t.Parallel()

	job := db.Job{
		ID:    "ap-job-merge-2",
		State: "approved",
		PRURL: "https://github.com/org/repo/pull/2",
	}
	m := Model{selected: &job}

	modelAny, _ := m.handleKey(keyRunes('m'))
	m = modelAny.(Model)
	if m.confirmAction != "merge" {
		t.Fatalf("expected confirmAction=merge, got %q", m.confirmAction)
	}
	if m.confirmJobID != job.ID {
		t.Fatalf("expected confirmJobID=%q, got %q", job.ID, m.confirmJobID)
	}
}

func TestHandleKeyMergeNoopWhenIneligible(t *testing.T) {
	t.Parallel()

	cases := []db.Job{
		{ID: "ap-job-merge-3", State: "ready", PRURL: "https://github.com/org/repo/pull/3"},
		{ID: "ap-job-merge-4", State: "approved", PRURL: ""},
		{ID: "ap-job-merge-5", State: "approved", PRURL: "https://github.com/org/repo/pull/5", PRMergedAt: "2025-02-19T14:04:05Z"},
		{ID: "ap-job-merge-6", State: "approved", PRURL: "https://github.com/org/repo/pull/6", PRClosedAt: "2025-02-19T14:05:06Z"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.ID, func(t *testing.T) {
			t.Parallel()
			m := Model{selected: &tc}
			modelAny, _ := m.handleKey(keyRunes('m'))
			m = modelAny.(Model)
			if m.confirmAction != "" || m.confirmJobID != "" {
				t.Fatalf("expected merge key to be no-op for ineligible job")
			}
		})
	}
}

func TestConfirmPromptMerge(t *testing.T) {
	t.Parallel()

	m := Model{
		confirmAction: "merge",
		confirmJobID:  "ap-job-merge-7",
	}
	want := "Merge PR for job " + db.ShortID(m.confirmJobID) + "?"
	if got := m.confirmPrompt(); got != want {
		t.Fatalf("confirmPrompt() = %q, want %q", got, want)
	}
}

func TestMergeConfirmYesReturnsExecuteCmd(t *testing.T) {
	t.Parallel()

	job := db.Job{
		ID:    "ap-job-merge-8",
		State: "approved",
		PRURL: "https://github.com/org/repo/pull/8",
	}
	m := Model{selected: &job}

	modelAny, _ := m.handleKey(keyRunes('m'))
	m = modelAny.(Model)
	modelAny, cmd := m.handleKey(keyRunes('y'))
	m = modelAny.(Model)
	if m.confirmAction != "merge" {
		t.Fatalf("expected merge confirmation to remain pending until action result")
	}
	if cmd == nil {
		t.Fatalf("expected execute merge command")
	}
}

func TestCancelConfirmYesCancelsJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	m, store, jobID := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()

	modelAny, _ := m.handleKey(keyRunes('c'))
	m = modelAny.(Model)
	modelAny, cmd := m.handleKey(keyRunes('y'))
	m = modelAny.(Model)
	if cmd == nil {
		t.Fatalf("expected execute cancel command")
	}

	msg := cmd()
	modelAny, refreshCmd := m.Update(msg)
	m = modelAny.(Model)
	if refreshCmd != nil {
		msg = refreshCmd()
		modelAny, _ = m.Update(msg)
		m = modelAny.(Model)
	}

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "cancelled" {
		t.Fatalf("expected cancelled, got %q", job.State)
	}
}

func TestCancelWithCleanupWarningStillSucceeds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	m, store, jobID := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()

	blockingFile := filepath.Join(tmp, "blocking-file")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}
	badPath := filepath.Join(blockingFile, "child")
	if err := store.UpdateJobField(ctx, jobID, "worktree_path", badPath); err != nil {
		t.Fatalf("set invalid worktree path: %v", err)
	}
	jobs, err := store.ListJobs(ctx, "", "all", "updated_at", false)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	m.jobs = jobs

	modelAny, _ := m.handleKey(keyRunes('c'))
	m = modelAny.(Model)
	modelAny, cmd := m.handleKey(keyRunes('y'))
	m = modelAny.(Model)
	if cmd == nil {
		t.Fatalf("expected execute cancel command")
	}

	msg := cmd()
	modelAny, refreshCmd := m.Update(msg)
	m = modelAny.(Model)
	if refreshCmd != nil {
		msg = refreshCmd()
		modelAny, _ = m.Update(msg)
		m = modelAny.(Model)
	}

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "cancelled" {
		t.Fatalf("expected cancelled, got %q", job.State)
	}
	if m.actionErr != nil {
		t.Fatalf("expected non-fatal warning only, got error: %v", m.actionErr)
	}
	if m.actionWarn == "" {
		t.Fatalf("expected warning after cleanup failure")
	}
	if !strings.Contains(m.listView(), "Warning: ") {
		t.Fatalf("expected warning in list view")
	}
}

func newTestModelWithQueuedJob(t *testing.T, tmp string) (Model, *db.Store, string) {
	t.Helper()
	ctx := context.Background()

	store, err := db.Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
		ProjectName:   "myproject",
		Source:        "github",
		SourceIssueID: "900",
		Title:         "tui cancel",
		URL:           "https://github.com/org/repo/issues/900",
		State:         "open",
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	jobID, err := store.CreateJob(ctx, issueID, "myproject", 3)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := store.UpdateJobField(ctx, jobID, "worktree_path", filepath.Join(tmp, "wt")); err != nil {
		t.Fatalf("set worktree path: %v", err)
	}

	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			PIDFile:      filepath.Join(tmp, "autopr.pid"),
			SyncInterval: "5m",
			MaxWorkers:   1,
		},
	}
	m := NewModel(store, cfg)
	jobs, err := store.ListJobs(ctx, "", "all", "updated_at", false)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	m.jobs = jobs
	m.cursor = 0
	return m, store, jobID
}

func newMergeExecutionModel(t *testing.T, tmp, prURL string) (Model, *db.Store, string) {
	t.Helper()
	ctx := context.Background()

	m, store, jobID := newTestModelWithQueuedJob(t, tmp)
	if _, err := store.Writer.ExecContext(ctx, `
		UPDATE jobs
		SET state = 'approved', pr_url = ?, pr_merged_at = '', pr_closed_at = ''
		WHERE id = ?`, prURL, jobID); err != nil {
		store.Close()
		t.Fatalf("seed mergeable job: %v", err)
	}

	jobs, err := store.ListJobs(ctx, "", "all", "updated_at", false)
	if err != nil {
		store.Close()
		t.Fatalf("list jobs: %v", err)
	}
	m.jobs = jobs
	m.selected = &m.jobs[0]
	m.confirmJobID = jobID
	return m, store, jobID
}

type jobSeed struct {
	state   string
	project string
}

func newTestModelWithJobs(t *testing.T, tmp string, seeds []jobSeed) (Model, *db.Store) {
	t.Helper()
	ctx := context.Background()

	store, err := db.Open(filepath.Join(tmp, "autopr.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	for i, seed := range seeds {
		issueID, err := store.UpsertIssue(ctx, db.IssueUpsert{
			ProjectName:   seed.project,
			Source:        "github",
			SourceIssueID: fmt.Sprintf("issue-%d", i+1),
			Title:         fmt.Sprintf("issue %d", i+1),
			URL:           fmt.Sprintf("https://github.com/org/repo/issues/%d", i+1),
			State:         "open",
		})
		if err != nil {
			t.Fatalf("upsert issue %d: %v", i+1, err)
		}
		jobID, err := store.CreateJob(ctx, issueID, seed.project, 3)
		if err != nil {
			t.Fatalf("create job %d: %v", i+1, err)
		}
		if _, err := store.Writer.ExecContext(ctx, `
			UPDATE jobs
			SET state = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
			WHERE id = ?`, seed.state, jobID); err != nil {
			t.Fatalf("configure job %d state %q: %v", i+1, seed.state, err)
		}
	}

	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			PIDFile:      filepath.Join(tmp, "autopr.pid"),
			SyncInterval: "5m",
			MaxWorkers:   1,
		},
	}
	m := NewModel(store, cfg)
	jobs, err := store.ListJobs(ctx, "", "all", "updated_at", false)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	m.jobs = jobs
	return m, store
}

func newTestModelForFilterCycle(jobs []db.Job) Model {
	return Model{
		cfg: &config.Config{
			Daemon: config.DaemonConfig{
				SyncInterval: "5m",
				MaxWorkers:   1,
			},
		},
		jobs:          jobs,
		allJobsCounts: jobs,
		filterState:   filterAllState,
		filterProject: filterAllProject,
	}
}

func TestFilterModeCycleStateAndProject(t *testing.T) {
	t.Parallel()
	m := newTestModelForFilterCycle([]db.Job{
		{ID: "ap-job-1", ProjectName: "proj-b", State: "queued"},
		{ID: "ap-job-2", ProjectName: "proj-a", State: "ready"},
		{ID: "ap-job-3", ProjectName: "proj-c", State: "active"},
	})

	modelAny, _ := m.handleKey(keyRunes('f'))
	m = modelAny.(Model)

	expectedStates := []string{"queued", "active", "awaiting_checks", "rebasing", "resolving_conflicts", "ready", "failed", "merged", "rejected", "cancelled", "all"}
	for _, state := range expectedStates {
		modelAny, _ = m.handleKey(keyRunes('s'))
		m = modelAny.(Model)
		if m.filterState != state {
			t.Fatalf("expected filter state %q, got %q", state, m.filterState)
		}
	}

	expectedProjects := []string{"proj-a", "proj-b", "proj-c", "all"}
	for _, project := range expectedProjects {
		modelAny, _ = m.handleKey(keyRunes('p'))
		m = modelAny.(Model)
		if m.filterProject != project {
			t.Fatalf("expected project filter %q, got %q", project, m.filterProject)
		}
	}
	if !m.filterMode {
		t.Fatalf("expected filter mode to remain active while cycling")
	}
}

func TestFilterModeEscCancelsPendingChanges(t *testing.T) {
	t.Parallel()
	m := newTestModelForFilterCycle([]db.Job{
		{ID: "ap-job-1", ProjectName: "proj-b", State: "queued"},
		{ID: "ap-job-2", ProjectName: "proj-a", State: "ready"},
	})
	m.filterState = "ready"
	m.filterProject = "proj-a"
	m.cursor = 1

	modelAny, _ := m.handleKey(keyRunes('f'))
	m = modelAny.(Model)
	modelAny, _ = m.handleKey(keyRunes('s'))
	m = modelAny.(Model)
	if m.filterState == "ready" {
		t.Fatalf("expected filter state change")
	}
	modelAny, _ = m.handleKey(keyRunes('p'))
	m = modelAny.(Model)
	if m.filterProject == "proj-a" {
		t.Fatalf("expected project filter change")
	}

	modelAny, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = modelAny.(Model)
	if m.filterMode {
		t.Fatalf("expected filter mode to exit on esc")
	}
	if m.filterState != "ready" || m.filterProject != "proj-a" {
		t.Fatalf("expected filters to rollback to ready/proj-a, got state=%q project=%q", m.filterState, m.filterProject)
	}
	if m.cursor != 1 {
		t.Fatalf("expected cursor to restore to prior position, got %d", m.cursor)
	}
}

func TestFilterModeEscRestoresCursor(t *testing.T) {
	t.Parallel()
	m := newTestModelForFilterCycle([]db.Job{
		{ID: "ap-job-1", ProjectName: "proj-b", State: "queued"},
		{ID: "ap-job-2", ProjectName: "proj-a", State: "ready"},
	})
	m.filterState = "ready"
	m.filterProject = "proj-a"
	m.cursor = 1

	modelAny, _ := m.handleKey(keyRunes('f'))
	m = modelAny.(Model)
	modelAny, _ = m.handleKey(keyRunes('s'))
	m = modelAny.(Model)
	modelAny, _ = m.handleKey(keyRunes('p'))
	m = modelAny.(Model)
	if m.filterState == "ready" || m.filterProject == "proj-a" {
		t.Fatalf("expected draft changes after s/p cycling")
	}

	modelAny, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = modelAny.(Model)
	if m.filterMode {
		t.Fatalf("expected filter mode to exit on esc")
	}
	if m.filterState != "ready" || m.filterProject != "proj-a" {
		t.Fatalf("expected pending changes to rollback to ready/proj-a, got state=%q project=%q", m.filterState, m.filterProject)
	}
	if m.cursor != 1 {
		t.Fatalf("expected cursor restore to prior position, got %d", m.cursor)
	}

}

func TestFilterModeClearShortcut(t *testing.T) {
	t.Parallel()
	m := newTestModelForFilterCycle([]db.Job{
		{ID: "ap-job-1", ProjectName: "proj-b", State: "queued"},
		{ID: "ap-job-2", ProjectName: "proj-a", State: "ready"},
	})
	m.filterState = "ready"
	m.filterProject = "proj-a"
	m.cursor = 3

	modelAny, _ := m.handleKey(keyRunes('F'))
	m = modelAny.(Model)
	if m.filterState != filterAllState || m.filterProject != filterAllProject {
		t.Fatalf("expected F to clear all filters, got state=%q project=%q", m.filterState, m.filterProject)
	}
	if m.cursor != 0 {
		t.Fatalf("expected cursor reset on clear")
	}
}

func TestFilterModeEscWithoutApplying(t *testing.T) {
	t.Parallel()
	m := newTestModelForFilterCycle([]db.Job{
		{ID: "ap-job-1", ProjectName: "proj-b", State: "queued"},
		{ID: "ap-job-2", ProjectName: "proj-a", State: "ready"},
	})
	m.filterState = "ready"
	m.filterProject = "proj-a"
	m.cursor = 1
	modelAny, _ := m.handleKey(keyRunes('f'))
	m = modelAny.(Model)
	modelAny, _ = m.handleKey(keyRunes('s'))
	m = modelAny.(Model)
	if m.filterState == "ready" {
		t.Fatalf("expected draft filter change to happen before esc")
	}
	modelAny, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = modelAny.(Model)
	if m.filterMode {
		t.Fatalf("expected filter mode to exit on esc")
	}
	if m.filterState != "ready" || m.filterProject != "proj-a" {
		t.Fatalf("expected pending changes not applied after esc, got state=%q project=%q", m.filterState, m.filterProject)
	}
	if m.cursor != 1 {
		t.Fatalf("expected cursor restore after filter cancel, got %d", m.cursor)
	}
}

// transitionToReviewing moves a job from queued → planning → implementing → reviewing.
func transitionToReviewing(t *testing.T, store *db.Store, jobID string) {
	t.Helper()
	ctx := context.Background()
	for _, tr := range [][2]string{{"queued", "planning"}, {"planning", "implementing"}, {"implementing", "reviewing"}} {
		if err := store.TransitionState(ctx, jobID, tr[0], tr[1]); err != nil {
			t.Fatalf("transition %s→%s: %v", tr[0], tr[1], err)
		}
	}
}

func keyRunes(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func keyType(t tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: t}
}

func makeTestJobs(count int) []db.Job {
	jobs := make([]db.Job, count)
	for i := 0; i < count; i++ {
		jobs[i] = db.Job{
			ID:            fmt.Sprintf("ap-job-%03d", i),
			State:         "queued",
			ProjectName:   "proj",
			IssueSource:   "github",
			SourceIssueID: fmt.Sprintf("%03d", i+1),
			IssueTitle:    "issue " + fmt.Sprint(i),
		}
	}
	return jobs
}

func batchCmdCount(t *testing.T, cmd tea.Cmd) int {
	t.Helper()
	if cmd == nil {
		return 0
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return 1
	}
	return len(batch)
}

func batchHasMessageType(t *testing.T, cmd tea.Cmd, want string) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return batchMsgType(msg) == want
	}
	for _, c := range batch {
		if c == nil {
			continue
		}
		if batchMsgType(c()) == want {
			return true
		}
	}
	return false
}

func batchMsgType(msg tea.Msg) string {
	switch msg.(type) {
	case jobsMsg:
		return "jobs"
	case sessionsMsg:
		return "sessions"
	case issueSummaryMsg:
		return "summary"
	default:
		return ""
	}
}

func TestTickMsgInListViewSchedulesRefresh(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	m, store, _ := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()

	_, cmd := m.Update(tickMsg{})
	if got, want := batchCmdCount(t, cmd), 3; got != want {
		t.Fatalf("expected %d tick batch commands in list view, got %d", want, got)
	}
}

func TestTickMsgInDetailViewSchedulesJobsSessionsRefresh(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	m, store, _ := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()
	m.selected = &m.jobs[0]

	_, cmd := m.Update(tickMsg{})
	if got, want := batchCmdCount(t, cmd), 4; got != want {
		t.Fatalf("expected %d tick batch commands in detail view, got %d", want, got)
	}
}

func TestTickMsgInSessionDetailPausesAutoRefresh(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	m, store, _ := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()
	m.selected = &m.jobs[0]
	m.selectedSession = &db.LLMSession{}

	_, cmd := m.Update(tickMsg{})
	if got, want := batchCmdCount(t, cmd), 1; got != want {
		t.Fatalf("expected %d tick batch command while paused in session detail, got %d", want, got)
	}
}

func TestTickMsgInDiffViewPausesAutoRefresh(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	m, store, _ := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()
	m.selected = &m.jobs[0]
	m.showDiff = true

	_, cmd := m.Update(tickMsg{})
	if got, want := batchCmdCount(t, cmd), 1; got != want {
		t.Fatalf("expected %d tick batch command while paused in diff view, got %d", want, got)
	}
}

func TestManualRefreshInListViewStillWorks(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	m, store, _ := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()

	_, cmd := m.handleKey(keyRunes('r'))
	if got, want := batchCmdCount(t, cmd), 2; got != want {
		t.Fatalf("expected %d refresh commands in list view, got %d", want, got)
	}
}

func TestManualRefreshHonorsActiveFilters(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	m, store := newTestModelWithJobs(t, tmp, []jobSeed{
		{state: "ready", project: "alpha"},
		{state: "queued", project: "alpha"},
		{state: "ready", project: "beta"},
		{state: "queued", project: "beta"},
		{state: "failed", project: "beta"},
	})
	defer store.Close()

	m.filterState = "ready"
	m.filterProject = "alpha"

	_, cmd := m.handleKey(keyRunes('r'))
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected batch from manual refresh, got %T", msg)
	}

	var filtered []db.Job
	for _, c := range batch {
		if c == nil {
			continue
		}
		if msg := c(); msg != nil {
			if v, ok := msg.(jobsMsg); ok {
				filtered = append(filtered, v.filtered...)
			}
		}
	}
	if len(filtered) != 1 {
		t.Fatalf("expected 1 filtered job, got %d", len(filtered))
	}
	if filtered[0].State != "ready" || filtered[0].ProjectName != "alpha" {
		t.Fatalf("expected filtered job to be ready/alpha, got %s/%s", filtered[0].State, filtered[0].ProjectName)
	}
}

func TestManualRefreshInDetailViewStillWorks(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	m, store, _ := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()
	m.selected = &m.jobs[0]

	_, cmd := m.handleKey(keyRunes('r'))
	if got, want := batchCmdCount(t, cmd), 3; got != want {
		t.Fatalf("expected %d refresh commands in detail view, got %d", want, got)
	}
}

func TestTickRefreshHonorsActiveFilters(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	m, store := newTestModelWithJobs(t, tmp, []jobSeed{
		{state: "ready", project: "alpha"},
		{state: "queued", project: "alpha"},
		{state: "ready", project: "beta"},
		{state: "queued", project: "beta"},
		{state: "failed", project: "beta"},
	})
	defer store.Close()

	m.filterState = "ready"
	m.filterProject = "alpha"

	_, cmd := m.Update(tickMsg{})
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected batch from tick refresh, got %T", msg)
	}

	var filtered, unfiltered []db.Job
	for _, c := range batch {
		if c == nil {
			continue
		}
		switch v := c().(type) {
		case jobsMsg:
			filtered = append(filtered, v.filtered...)
			unfiltered = append(unfiltered, v.unfiltered...)
		}
	}
	if len(filtered) != 1 {
		t.Fatalf("expected 1 filtered job, got %d", len(filtered))
	}
	if filtered[0].State != "ready" || filtered[0].ProjectName != "alpha" {
		t.Fatalf("expected filtered job to be ready/alpha, got %s/%s", filtered[0].State, filtered[0].ProjectName)
	}
	if len(unfiltered) != 5 {
		t.Fatalf("expected 5 unfiltered jobs, got %d", len(unfiltered))
	}
}

func TestFilteredRefreshPreservesUnfilteredCounters(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	m, store := newTestModelWithJobs(t, tmp, []jobSeed{
		{state: "ready", project: "alpha"},
		{state: "queued", project: "alpha"},
		{state: "ready", project: "beta"},
		{state: "queued", project: "beta"},
		{state: "failed", project: "beta"},
	})
	defer store.Close()

	m.filterState = "ready"
	m.filterProject = "alpha"

	_, cmd := m.Update(tickMsg{})
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected batch from tick refresh, got %T", msg)
	}

	var jobsPayload jobsMsg
	found := false
	for _, c := range batch {
		if c == nil {
			continue
		}
		if msg := c(); msg != nil {
			if v, ok := msg.(jobsMsg); ok {
				jobsPayload = v
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected jobs message in tick batch")
	}
	modelAny, _ := m.Update(jobsPayload)
	m = modelAny.(Model)
	counts := m.jobCounts()
	if counts["ready"] != 2 {
		t.Fatalf("expected unfiltered ready count 2, got %d", counts["ready"])
	}
	if counts["queued"] != 2 {
		t.Fatalf("expected unfiltered queued count 2, got %d", counts["queued"])
	}
}

func TestJobsMsgClampsCursorWhenListShrinks(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	m, store, _ := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()

	m.pageSize = 10
	m.page = 2
	m.cursor = 25
	modelAny, _ := m.Update(jobsMsg{filtered: makeTestJobs(31), unfiltered: makeTestJobs(31)})
	m = modelAny.(Model)
	if m.page != 2 {
		t.Fatalf("expected page to stay 2, got %d", m.page)
	}
	if m.cursor != 25 {
		t.Fatalf("expected cursor to stay 25, got %d", m.cursor)
	}

	modelAny, _ = m.Update(jobsMsg{filtered: makeTestJobs(12), unfiltered: makeTestJobs(12)})
	m = modelAny.(Model)
	if m.page != 1 {
		t.Fatalf("expected page to clamp to 1, got %d", m.page)
	}
	if m.cursor != 11 {
		t.Fatalf("expected cursor to clamp within last page, got %d", m.cursor)
	}

	m.page = 0
	m.cursor = 2
	modelAny, _ = m.Update(jobsMsg{filtered: nil})
	m = modelAny.(Model)
	if m.page != 0 {
		t.Fatalf("expected page to reset to 0 for empty jobs list, got %d", m.page)
	}
	if m.cursor != 0 {
		t.Fatalf("expected cursor to reset to 0 for empty jobs list, got %d", m.cursor)
	}
}

func TestWindowSizeMsgSetsPageSize(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Daemon: config.DaemonConfig{
		SyncInterval: "5m",
		MaxWorkers:   1,
	}}

	m := NewModel(nil, cfg)
	mAny, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = mAny.(Model)
	if m.pageSize != 26 {
		t.Fatalf("expected pageSize 26, got %d", m.pageSize)
	}
	if m.page != 0 {
		t.Fatalf("expected page to stay 0, got %d", m.page)
	}
	if m.cursor != 0 {
		t.Fatalf("expected cursor to stay 0, got %d", m.cursor)
	}
}

func TestHandleKeyLevel1PaginationControls(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Daemon: config.DaemonConfig{
		SyncInterval: "5m",
		MaxWorkers:   1,
	}}
	m := Model{
		cfg:      cfg,
		jobs:     makeTestJobs(25),
		pageSize: 10,
	}

	modelAny, _ := m.handleKey(keyRunes('n'))
	m = modelAny.(Model)
	if m.page != 1 || m.cursor != 10 {
		t.Fatalf("expected page 1, cursor 10 after n; got page %d cursor %d", m.page, m.cursor)
	}

	modelAny, _ = m.handleKey(keyRunes('p'))
	m = modelAny.(Model)
	if m.page != 0 || m.cursor != 0 {
		t.Fatalf("expected page 0, cursor 0 after p; got page %d cursor %d", m.page, m.cursor)
	}

	modelAny, _ = m.handleKey(keyRunes('n'))
	m = modelAny.(Model)
	if m.page != 1 || m.cursor != 10 {
		t.Fatalf("expected page 1, cursor 10 after n; got page %d cursor %d", m.page, m.cursor)
	}

	modelAny, _ = m.handleKey(keyType(tea.KeyPgDown))
	m = modelAny.(Model)
	if m.page != 2 || m.cursor != 20 {
		t.Fatalf("expected page clamp at 2 after extra pgdown; got page %d cursor %d", m.page, m.cursor)
	}

	modelAny, _ = m.handleKey(keyType(tea.KeyPgUp))
	m = modelAny.(Model)
	if m.page != 1 || m.cursor != 10 {
		t.Fatalf("expected page 1, cursor 10 after pgup; got page %d cursor %d", m.page, m.cursor)
	}

	modelAny, _ = m.handleKey(keyRunes('p'))
	m = modelAny.(Model)
	if m.page != 0 || m.cursor != 0 {
		t.Fatalf("expected page 0, cursor 0 after p; got page %d cursor %d", m.page, m.cursor)
	}

	modelAny, _ = m.handleKey(keyType(tea.KeyPgUp))
	m = modelAny.(Model)
	if m.page != 0 || m.cursor != 0 {
		t.Fatalf("expected page 0, cursor 0 after pgup; got page %d cursor %d", m.page, m.cursor)
	}

	modelAny, _ = m.handleKey(keyRunes('G'))
	m = modelAny.(Model)
	if m.page != 2 || m.cursor != 20 {
		t.Fatalf("expected page 2, cursor 20 after G; got page %d cursor %d", m.page, m.cursor)
	}

	modelAny, _ = m.handleKey(keyRunes('k'))
	m = modelAny.(Model)
	if m.cursor != 24 {
		t.Fatalf("expected cursor to wrap to end of page on k; got %d", m.cursor)
	}

	modelAny, _ = m.handleKey(keyRunes('j'))
	m = modelAny.(Model)
	if m.cursor != 20 {
		t.Fatalf("expected cursor to wrap to start of page on j; got %d", m.cursor)
	}

	modelAny, _ = m.handleKey(keyRunes('g'))
	m = modelAny.(Model)
	if m.page != 0 || m.cursor != 0 {
		t.Fatalf("expected page 0, cursor 0 after g; got page %d cursor %d", m.page, m.cursor)
	}
}

func TestListViewShowsPaginationInfo(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Daemon: config.DaemonConfig{
		SyncInterval: "5m",
		MaxWorkers:   1,
	}}
	m := Model{
		cfg:      cfg,
		jobs:     makeTestJobs(25),
		pageSize: 10,
		page:     1,
		cursor:   10,
	}

	view := m.listView()
	if !strings.Contains(view, "Page 2/3 (25 jobs)") {
		t.Fatalf("expected page indicator in list footer, got:\n%s", view)
	}
	if !strings.Contains(view, "n/pgdown next page") {
		t.Fatalf("expected next-page hint in pagination footer, got:\n%s", view)
	}
	if !strings.Contains(view, "p/pgup prev page") {
		t.Fatalf("expected prev-page hint in pagination footer, got:\n%s", view)
	}
	if !strings.Contains(view, "010") {
		t.Fatalf("expected current page to render page-1 first row, got:\n%s", view)
	}
	if strings.Contains(view, "000") {
		t.Fatalf("expected previous page rows to be omitted, got:\n%s", view)
	}
}

func TestListViewHandlesZeroAndOneJob(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Daemon: config.DaemonConfig{
		SyncInterval: "5m",
		MaxWorkers:   1,
	}}
	m := Model{cfg: cfg, pageSize: 10}

	view := m.listView()
	if !strings.Contains(view, "No jobs found. Waiting for issues...") {
		t.Fatalf("expected no jobs message, got:\n%s", view)
	}
	if !strings.Contains(view, "Page 0/0 (0 jobs)") {
		t.Fatalf("expected empty pagination footer, got:\n%s", view)
	}

	m.jobs = makeTestJobs(1)
	view = m.listView()
	if !strings.Contains(view, "Page 1/1 (1 jobs)") {
		t.Fatalf("expected one-job pagination footer, got:\n%s", view)
	}
	if strings.Contains(view, "n/pgdown next page") {
		t.Fatalf("did not expect next-page hint on single page, got:\n%s", view)
	}
	if strings.Contains(view, "p/pgup prev page") {
		t.Fatalf("did not expect prev-page hint on single page, got:\n%s", view)
	}
	if !strings.Contains(view, "000") {
		t.Fatalf("expected single job row, got:\n%s", view)
	}
}

func TestLevel1EnterAndCancelUseCurrentPageSelection(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Daemon: config.DaemonConfig{
		SyncInterval: "5m",
		MaxWorkers:   1,
	}}
	m := Model{
		cfg:      cfg,
		jobs:     makeTestJobs(15),
		pageSize: 10,
		page:     1,
		cursor:   12,
	}
	wantID := m.jobs[12].ID

	modelAny, cmd := m.handleKey(keyType(tea.KeyEnter))
	m = modelAny.(Model)
	if m.selected == nil || m.selected.ID != wantID {
		t.Fatalf("expected selected job %s, got %#v", wantID, m.selected)
	}
	if cmd == nil {
		t.Fatalf("expected command from enter key on list view")
	}

	modelAny, _ = m.handleKey(keyRunes('c'))
	m = modelAny.(Model)
	if m.confirmAction != "cancel" {
		t.Fatalf("expected cancel confirmation action, got %q", m.confirmAction)
	}
	if m.confirmJobID != wantID {
		t.Fatalf("expected confirm job %s, got %q", wantID, m.confirmJobID)
	}
}

func TestSelectedSyncsAfterJobsMsg(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	m, store, jobID := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()

	// Select the job (simulates user navigating into detail view).
	m.selected = &m.jobs[0]
	if m.selected.State != "queued" {
		t.Fatalf("expected queued, got %q", m.selected.State)
	}

	// Transition job to "reviewing" via valid state machine path.
	transitionToReviewing(t, store, jobID)

	// Simulate a jobsMsg arriving (as auto-refresh would deliver).
	jobs, err := store.ListJobs(ctx, "", "all", "updated_at", false)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	modelAny, _ := m.Update(jobsMsg{filtered: jobs, unfiltered: jobs})
	m = modelAny.(Model)

	if m.selected == nil {
		t.Fatalf("expected selected to remain set")
	}
	if m.selected.State != "reviewing" {
		t.Fatalf("expected selected state to be reviewing, got %q", m.selected.State)
	}
}

func TestApproveSuccessKeepsDetailViewAndRefreshesJobsSessionsSummary(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	m, store, _ := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()
	m.selected = &m.jobs[0]
	m.confirmAction = "approve"
	m.confirmJobID = m.selected.ID

	modelAny, cmd := m.Update(actionResultMsg{action: "approve"})
	m = modelAny.(Model)

	if m.confirmAction != "" || m.confirmJobID != "" {
		t.Fatalf("expected confirmation state cleared after approve success")
	}
	if m.selected == nil {
		t.Fatalf("expected selected job to stay open on approve success")
	}
	if got, want := batchCmdCount(t, cmd), 3; got != want {
		t.Fatalf("expected %d refresh commands for approve success, got %d", want, got)
	}
	if !batchHasMessageType(t, cmd, "jobs") {
		t.Fatalf("expected approve success refresh to include jobs fetch")
	}
	if !batchHasMessageType(t, cmd, "sessions") {
		t.Fatalf("expected approve success refresh to include sessions fetch")
	}
	if !batchHasMessageType(t, cmd, "summary") {
		t.Fatalf("expected approve success refresh to include issue summary fetch")
	}

	updated := make([]db.Job, len(m.jobs))
	copy(updated, m.jobs)
	updated[0].State = "approved"
	modelAny, _ = m.Update(jobsMsg{filtered: updated, unfiltered: updated})
	m = modelAny.(Model)
	if m.selected == nil || m.selected.State != "approved" {
		t.Fatalf("expected selected state to update to approved after jobs refresh")
	}
}

func TestMergeSuccessKeepsDetailViewAndRefreshesJobsSessionsSummary(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	m, store, _ := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()
	m.selected = &m.jobs[0]
	m.confirmAction = "merge"
	m.confirmJobID = m.selected.ID

	modelAny, cmd := m.Update(actionResultMsg{action: "merge"})
	m = modelAny.(Model)

	if m.confirmAction != "" || m.confirmJobID != "" {
		t.Fatalf("expected confirmation state cleared after merge success")
	}
	if m.selected == nil {
		t.Fatalf("expected selected job to stay open on merge success")
	}
	if got, want := batchCmdCount(t, cmd), 3; got != want {
		t.Fatalf("expected %d refresh commands for merge success, got %d", want, got)
	}
	if !batchHasMessageType(t, cmd, "jobs") {
		t.Fatalf("expected merge success refresh to include jobs fetch")
	}
	if !batchHasMessageType(t, cmd, "sessions") {
		t.Fatalf("expected merge success refresh to include sessions fetch")
	}
	if !batchHasMessageType(t, cmd, "summary") {
		t.Fatalf("expected merge success refresh to include issue summary fetch")
	}
}

func TestNonApproveSuccessKeepsExistingNavigationReset(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	m, store, _ := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()
	m.selected = &m.jobs[0]
	m.confirmAction = "cancel"
	m.confirmJobID = m.selected.ID

	modelAny, cmd := m.Update(actionResultMsg{action: "cancel"})
	m = modelAny.(Model)

	if m.confirmAction != "" || m.confirmJobID != "" {
		t.Fatalf("expected confirmation state cleared after cancel success")
	}
	if m.selected != nil {
		t.Fatalf("expected selected to reset for non-approve success")
	}
	if got, want := batchCmdCount(t, cmd), 2; got != want {
		t.Fatalf("expected %d refresh commands for non-approve success, got %d", want, got)
	}
	if batchHasMessageType(t, cmd, "sessions") {
		t.Fatalf("did not expect sessions refresh for non-approve success")
	}
}

func TestExecuteMergeGitLabSuccessMarksJobMerged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	var gotEscapedPath, gotRequestURI string
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("tcp listener unavailable in sandbox: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEscapedPath = r.URL.EscapedPath()
		gotRequestURI = r.RequestURI
		if r.Method != http.MethodPut {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if got := r.Header.Get("PRIVATE-TOKEN"); got != "gl-token" {
			t.Fatalf("unexpected token header: %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"state":"merged"}`))
	}))
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	m, store, jobID := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()

	prURL := srv.URL + "/group/repo/-/merge_requests/123"
	if _, err := store.Writer.ExecContext(ctx, `
		UPDATE jobs
		SET state = 'approved', pr_url = ?, pr_merged_at = '', pr_closed_at = ''
		WHERE id = ?`, prURL, jobID); err != nil {
		t.Fatalf("seed approved PR job: %v", err)
	}

	jobs, err := store.ListJobs(ctx, "", "all", "updated_at", false)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	m.jobs = jobs
	m.selected = &m.jobs[0]
	m.confirmJobID = jobID
	m.cfg = &config.Config{
		Tokens: config.TokensConfig{GitLab: "gl-token"},
		Projects: []config.ProjectConfig{
			{
				Name: "myproject",
				GitLab: &config.ProjectGitLab{
					BaseURL:   srv.URL,
					ProjectID: "group/repo",
				},
			},
		},
	}

	msg := m.executeMerge()
	res, ok := msg.(actionResultMsg)
	if !ok {
		t.Fatalf("expected actionResultMsg, got %T", msg)
	}
	if res.err != nil {
		t.Fatalf("executeMerge error: %v", res.err)
	}
	if res.action != "merge" {
		t.Fatalf("expected action merge, got %q", res.action)
	}
	wantPath := "/api/v4/projects/group%2Frepo/merge_requests/123/merge"
	requestPath := gotEscapedPath
	if requestPath == "" {
		requestPath = gotRequestURI
		if i := strings.Index(requestPath, "?"); i != -1 {
			requestPath = requestPath[:i]
		}
	}
	if requestPath != wantPath {
		t.Fatalf("unexpected gitlab merge path: want=%q escaped=%q request_uri=%q", wantPath, gotEscapedPath, gotRequestURI)
	}

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.PRMergedAt == "" {
		t.Fatalf("expected PRMergedAt to be set")
	}
}

func TestExecuteMergeErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("missing token", func(t *testing.T) {
		t.Parallel()
		m, _, _ := newMergeExecutionModel(t, t.TempDir(), "https://gitlab.com/group/repo/-/merge_requests/1")
		m.cfg = &config.Config{
			Projects: []config.ProjectConfig{
				{
					Name: "myproject",
					GitLab: &config.ProjectGitLab{
						BaseURL:   "https://gitlab.com",
						ProjectID: "group/repo",
					},
				},
			},
		}

		msg := m.executeMerge()
		res := msg.(actionResultMsg)
		if res.err == nil || !strings.Contains(res.err.Error(), "GITLAB_TOKEN required") {
			t.Fatalf("expected missing gitlab token error, got %v", res.err)
		}
	})

	t.Run("unsupported provider", func(t *testing.T) {
		t.Parallel()
		m, _, _ := newMergeExecutionModel(t, t.TempDir(), "https://example.com/pr/1")
		m.cfg = &config.Config{
			Projects: []config.ProjectConfig{
				{Name: "myproject"},
			},
		}

		msg := m.executeMerge()
		res := msg.(actionResultMsg)
		if res.err == nil || !strings.Contains(res.err.Error(), "no GitHub or GitLab config") {
			t.Fatalf("expected unsupported provider error, got %v", res.err)
		}
	})

	t.Run("github bad URL", func(t *testing.T) {
		t.Parallel()
		m, _, _ := newMergeExecutionModel(t, t.TempDir(), "not-a-pr-url")
		m.cfg = &config.Config{
			Tokens: config.TokensConfig{GitHub: "gh-token"},
			Projects: []config.ProjectConfig{
				{
					Name: "myproject",
					GitHub: &config.ProjectGitHub{
						Owner: "org",
						Repo:  "repo",
					},
				},
			},
		}

		msg := m.executeMerge()
		res := msg.(actionResultMsg)
		if res.err == nil || !strings.Contains(res.err.Error(), "cannot parse PR number") {
			t.Fatalf("expected parse error for bad PR URL, got %v", res.err)
		}
	})
}

func TestMergeFailureSetsActionErrInDetailView(t *testing.T) {
	t.Parallel()

	m, _, _ := newMergeExecutionModel(t, t.TempDir(), "https://gitlab.com/group/repo/-/merge_requests/1")
	m.cfg = &config.Config{
		Projects: []config.ProjectConfig{
			{
				Name: "myproject",
				GitLab: &config.ProjectGitLab{
					BaseURL:   "https://gitlab.com",
					ProjectID: "group/repo",
				},
			},
		},
	}
	m.confirmAction = "merge"

	msg := m.executeMerge()
	modelAny, _ := m.Update(msg)
	m = modelAny.(Model)

	if m.actionErr == nil {
		t.Fatalf("expected actionErr to be set after merge failure")
	}
	if m.selected == nil {
		t.Fatalf("expected detail view to remain open after merge failure")
	}
}

func TestCancelOnReviewingStateJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	m, store, jobID := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()

	// Move job to "reviewing" via valid state machine path.
	transitionToReviewing(t, store, jobID)

	jobs, err := store.ListJobs(ctx, "", "all", "updated_at", false)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	m.jobs = jobs
	m.selected = &m.jobs[0]

	// Verify cancel hint shows in detail view.
	view := m.detailView()
	if !strings.Contains(view, "c cancel") {
		t.Fatalf("expected cancel hint for reviewing job, got:\n%s", view)
	}

	// Press c then y to cancel.
	modelAny, _ := m.handleKey(keyRunes('c'))
	m = modelAny.(Model)
	if m.confirmAction != "cancel" {
		t.Fatalf("expected confirmAction=cancel, got %q", m.confirmAction)
	}
	modelAny, cmd := m.handleKey(keyRunes('y'))
	m = modelAny.(Model)
	if cmd == nil {
		t.Fatalf("expected execute cancel command")
	}

	msg := cmd()
	modelAny, refreshCmd := m.Update(msg)
	m = modelAny.(Model)
	if refreshCmd != nil {
		msg = refreshCmd()
		modelAny, _ = m.Update(msg)
		m = modelAny.(Model)
	}

	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.State != "cancelled" {
		t.Fatalf("expected cancelled, got %q", job.State)
	}
}

func TestStartTimeFormattingHelpers(t *testing.T) {
	t.Parallel()

	const ts = "2025-02-19T14:32:05Z"
	if got, want := formatTimestamp(ts), "2025-02-19 14:32:05"; got != want {
		t.Fatalf("formatTimestamp() = %q, want %q", got, want)
	}
	if got, want := formatTimestamp(""), "-"; got != want {
		t.Fatalf("formatTimestamp(empty) = %q, want %q", got, want)
	}
	if got, want := formatTimestamp("bad-time"), "-"; got != want {
		t.Fatalf("formatTimestamp(bad) = %q, want %q", got, want)
	}
	if got, want := formatDuration(9100), "9s"; got != want {
		t.Fatalf("formatDuration() = %q, want %q", got, want)
	}
}

func TestFormatTimestampLocal(t *testing.T) {
	t.Parallel()

	const (
		ts      = "2025-02-19T14:04:05Z"
		tsNanos = "2025-02-19T14:04:05.999999999Z"
	)

	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t.Fatalf("parse %q: %v", ts, err)
	}
	if got, want := formatTimestampLocal(ts, "15:04:05"), parsed.In(time.Local).Format("15:04:05"); got != want {
		t.Fatalf("formatTimestampLocal(%q, HH:MM:SS) = %q, want %q", ts, got, want)
	}
	if got, want := formatTimestampLocal(ts, "2006-01-02 15:04:05"), parsed.In(time.Local).Format("2006-01-02 15:04:05"); got != want {
		t.Fatalf("formatTimestampLocal(%q, datetime) = %q, want %q", ts, got, want)
	}
	parsedNanos, err := time.Parse(time.RFC3339Nano, tsNanos)
	if err != nil {
		t.Fatalf("parse %q: %v", tsNanos, err)
	}
	if got, want := formatTimestampLocal(tsNanos, "15:04:05"), parsedNanos.In(time.Local).Format("15:04:05"); got != want {
		t.Fatalf("formatTimestampLocal(%q, HH:MM:SS) = %q, want %q", tsNanos, got, want)
	}
	if got, want := formatTimestampLocal("", "15:04:05"), ""; got != want {
		t.Fatalf("formatTimestampLocal(empty) = %q, want %q", got, want)
	}
	if got, want := formatTimestampLocal("bad-time", "15:04:05"), "bad-time"; got != want {
		t.Fatalf("formatTimestampLocal(bad) = %q, want %q", got, want)
	}
}

func TestListViewUpdatedTimestampUsesYYYYMMDDHHMMSS(t *testing.T) {
	t.Parallel()

	job := db.Job{
		ID:        "ap-job-1234",
		State:     "implementing",
		UpdatedAt: "2025-02-19T14:04:05Z",
	}
	m := Model{
		cfg: &config.Config{
			Daemon: config.DaemonConfig{
				SyncInterval: "5m",
				MaxWorkers:   1,
			},
		},
		jobs: []db.Job{job},
	}

	view := m.listView()
	expected := formatTimestampLocal("2025-02-19T14:04:05Z", "2006-01-02 15:04:05")
	if !strings.Contains(view, expected) {
		t.Fatalf("expected formatted updated timestamp in list view (%q), got:\n%s", expected, view)
	}
}

func TestDetailViewPipelineHeaderIncludesStartAndDuration(t *testing.T) {
	t.Parallel()

	job := db.Job{
		ID:          "ap-job-1234",
		State:       "implementing",
		ProjectName: "proj",
	}
	m := Model{
		selected: &job,
		sessions: []db.LLMSessionSummary{
			{
				ID:           1,
				Step:         "plan",
				Status:       "completed",
				LLMProvider:  "codex",
				InputTokens:  1,
				OutputTokens: 2,
				DurationMS:   3000,
				CreatedAt:    "2025-02-19T14:32:05Z",
			},
		},
	}

	view := m.detailView()
	var headerLine string
	for line := range strings.SplitSeq(view, "\n") {
		if strings.Contains(line, "PROVIDER") && strings.Contains(line, "TOKENS") {
			headerLine = line
			break
		}
	}
	if headerLine == "" {
		t.Fatalf("pipeline header line not found:\n%s", view)
	}
	for _, token := range []string{"#", "STATE", "STATUS", "PROVIDER", "TOKENS", "START", "DURATION"} {
		if !strings.Contains(headerLine, token) {
			t.Fatalf("expected pipeline header to contain %q, got:\n%s", token, headerLine)
		}
	}
	if strings.Contains(headerLine, "TIME") {
		t.Fatalf("did not expect TIME header, got:\n%s", headerLine)
	}
	if strings.Index(headerLine, "START") > strings.Index(headerLine, "DURATION") {
		t.Fatalf("expected START before DURATION, got:\n%s", headerLine)
	}
}

func TestDetailViewPipelineShowsStartTimesForRows(t *testing.T) {
	t.Parallel()

	job := db.Job{
		ID:          "ap-job-1234",
		State:       "approved",
		ProjectName: "proj",
		PRURL:       "https://example.com/pr/1",
		CompletedAt: "2025-02-19T14:03:04Z",
		PRMergedAt:  "2025-02-19T14:04:05Z",
		PRClosedAt:  "2025-02-19T14:05:06Z",
	}
	m := Model{
		selected: &job,
		sessions: []db.LLMSessionSummary{
			{
				ID:           1,
				Step:         "plan",
				Status:       "completed",
				LLMProvider:  "codex",
				InputTokens:  1,
				OutputTokens: 2,
				DurationMS:   3000,
				CreatedAt:    "2025-02-19T14:01:02Z",
			},
		},
		testArtifact: &db.Artifact{CreatedAt: "2025-02-19T14:02:03Z"},
	}

	view := m.detailView()
	for _, want := range []string{"2025-02-19 14:01:02", "2025-02-19 14:02:03", "2025-02-19 14:03:04", "2025-02-19 14:04:05", "2025-02-19 14:05:06"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected pipeline view to contain start time %q:\n%s", want, view)
		}
	}
}

func TestDetailViewPipelineShowsCheckingCIRow(t *testing.T) {
	t.Parallel()

	job := db.Job{
		ID:              "ap-job-ci-1",
		State:           "awaiting_checks",
		ProjectName:     "proj",
		PRURL:           "https://example.com/pr/1",
		CIStartedAt:     "2025-02-19T14:03:04Z",
		CIStatusSummary: "CI checks: total=3 completed=1 passed=1 failed=0 pending=2",
	}
	m := Model{
		selected: &job,
		sessions: []db.LLMSessionSummary{
			{ID: 1, Step: "plan", Status: "completed", LLMProvider: "codex", CreatedAt: "2025-02-19T14:01:02Z"},
		},
	}

	line := findLineContainingAll(t, m.detailView(), "checking ci", "github")
	if !strings.Contains(line, "running") {
		t.Fatalf("expected checking ci row to be running, got %q", line)
	}
}

func TestDetailViewPipelineCheckingCIStatusByState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		state  string
		want   string
		reason string
	}{
		{name: "awaiting", state: "awaiting_checks", want: "running"},
		{name: "approved", state: "approved", want: "completed"},
		{name: "rejected", state: "rejected", want: "failed", reason: "CI check failed: lint"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			job := db.Job{
				ID:              "ap-job-ci-status-" + tc.name,
				State:           tc.state,
				ProjectName:     "proj",
				PRURL:           "https://example.com/pr/2",
				CIStartedAt:     "2025-02-19T14:03:04Z",
				CICompletedAt:   "2025-02-19T14:05:04Z",
				RejectReason:    tc.reason,
				CIStatusSummary: "summary",
				CompletedAt:     "2025-02-19T14:06:04Z",
			}
			m := Model{selected: &job}
			line := findLineContainingAll(t, m.detailView(), "checking ci", "github")
			if !strings.Contains(line, tc.want) {
				t.Fatalf("expected checking ci row status %q, got %q", tc.want, line)
			}
		})
	}
}

func TestDetailViewPipelineCheckingCIRowOrderBeforePRCreated(t *testing.T) {
	t.Parallel()

	job := db.Job{
		ID:              "ap-job-ci-order",
		State:           "approved",
		ProjectName:     "proj",
		PRURL:           "https://example.com/pr/3",
		CIStartedAt:     "2025-02-19T14:03:04Z",
		CICompletedAt:   "2025-02-19T14:04:04Z",
		CIStatusSummary: "summary",
		CompletedAt:     "2025-02-19T14:05:04Z",
	}
	view := stripANSI((Model{selected: &job}).detailView())
	ciLine := findLineContainingAll(t, view, "checking ci", "github")
	prLine := findLineContainingAll(t, view, "pr created", "completed")
	ciIdx := strings.Index(view, ciLine)
	prIdx := strings.Index(view, prLine)
	if ciIdx == -1 || prIdx == -1 || ciIdx > prIdx {
		t.Fatalf("expected checking ci before pr created:\n%s", view)
	}
}

func TestDetailViewPipelineHidesCheckingCIWithoutCIMetadata(t *testing.T) {
	t.Parallel()

	job := db.Job{
		ID:          "ap-job-ci-hide",
		State:       "approved",
		ProjectName: "proj",
		PRURL:       "https://example.com/pr/4",
		CompletedAt: "2025-02-19T14:05:04Z",
	}
	view := stripANSI((Model{selected: &job}).detailView())
	if strings.Contains(view, "checking ci") {
		t.Fatalf("did not expect checking ci row without ci metadata/state:\n%s", view)
	}
}

func TestHandleKeyEnterOnCheckingCIRowOpensCIDetailView(t *testing.T) {
	t.Parallel()

	job := db.Job{
		ID:              "ap-job-ci-enter",
		State:           "approved",
		ProjectName:     "proj",
		PRURL:           "https://example.com/pr/5",
		CIStartedAt:     "2025-02-19T14:03:04Z",
		CICompletedAt:   "2025-02-19T14:04:04Z",
		CIStatusSummary: "CI checks: total=3 completed=3 passed=3 failed=0 pending=0",
		CompletedAt:     "2025-02-19T14:05:04Z",
	}
	m := Model{
		selected: &job,
		sessions: []db.LLMSessionSummary{
			{ID: 1, Step: "plan", Status: "completed", LLMProvider: "codex"},
		},
		sessCursor: 1, // first synthetic row = checking ci
	}

	modelAny, cmd := m.handleKey(keyType(tea.KeyEnter))
	m = modelAny.(Model)
	if cmd != nil {
		t.Fatalf("expected no async command for synthetic checking ci row")
	}
	if m.selectedSession == nil || m.selectedSession.Step != "awaiting_checks" {
		t.Fatalf("expected checking ci detail view, got %+v", m.selectedSession)
	}
}

func TestSessionViewNumberingIncludesCheckingCIRow(t *testing.T) {
	t.Parallel()

	job := db.Job{
		ID:              "ap-job-ci-num",
		State:           "approved",
		ProjectName:     "proj",
		PRURL:           "https://example.com/pr/6",
		CIStartedAt:     "2025-02-19T14:03:04Z",
		CICompletedAt:   "2025-02-19T14:04:04Z",
		CIStatusSummary: "summary",
		CompletedAt:     "2025-02-19T14:05:04Z",
	}
	m := Model{
		selected: &job,
		sessions: []db.LLMSessionSummary{
			{ID: 1, Step: "plan", Status: "completed"},
		},
	}
	m = m.enterCheckingCIView()
	view := stripANSI(m.sessionView())
	if !strings.Contains(view, "SESSION #2") {
		t.Fatalf("expected checking ci synthetic step to be numbered after sessions, got:\n%s", view)
	}
}

func TestSessionViewShowsStartTimeAboveDuration(t *testing.T) {
	t.Parallel()

	m := Model{
		selectedSession: &db.LLMSession{
			ID:           1,
			Step:         "plan",
			Iteration:    1,
			Status:       "completed",
			LLMProvider:  "codex",
			InputTokens:  3,
			OutputTokens: 5,
			DurationMS:   11000,
			CreatedAt:    "2025-02-19T14:32:05Z",
		},
		lines: []string{"ok"},
	}

	view := m.sessionView()
	if !strings.Contains(view, "Start Time") || !strings.Contains(view, "2025-02-19 14:32:05") {
		t.Fatalf("expected session metadata to include formatted Start Time:\n%s", view)
	}
	startIdx := strings.Index(view, "Start Time")
	durIdx := strings.Index(view, "Duration")
	if startIdx == -1 || durIdx == -1 || startIdx > durIdx {
		t.Fatalf("expected Start Time to appear above Duration:\n%s", view)
	}
}

func TestSyntheticSessionViewsCarryStartTimes(t *testing.T) {
	t.Parallel()

	base := Model{
		cfg: &config.Config{},
		selected: &db.Job{
			ID:              "ap-job-1234",
			ProjectName:     "proj",
			PRURL:           "https://example.com/pr/1",
			CIStartedAt:     "2025-02-19T14:02:30Z",
			CICompletedAt:   "2025-02-19T14:02:59Z",
			CIStatusSummary: "CI checks: total=3 completed=3 passed=3 failed=0 pending=0",
			CompletedAt:     "2025-02-19T14:03:04Z",
			PRMergedAt:      "2025-02-19T14:04:05Z",
			PRClosedAt:      "2025-02-19T14:05:06Z",
		},
		testArtifact: &db.Artifact{
			CreatedAt: "2025-02-19T14:02:03Z",
			Iteration: 1,
		},
	}

	testView := base.enterTestView()
	if got, want := testView.selectedSession.CreatedAt, "2025-02-19T14:02:03Z"; got != want {
		t.Fatalf("test view created_at = %q, want %q", got, want)
	}

	prView := base.enterPRView()
	if got, want := prView.selectedSession.CreatedAt, "2025-02-19T14:03:04Z"; got != want {
		t.Fatalf("pr view created_at = %q, want %q", got, want)
	}

	ciView := base.enterCheckingCIView()
	if got, want := ciView.selectedSession.CreatedAt, "2025-02-19T14:02:30Z"; got != want {
		t.Fatalf("checking ci view created_at = %q, want %q", got, want)
	}
	if !strings.Contains(ciView.selectedSession.ResponseText, "CI checks: total=3") {
		t.Fatalf("expected checking ci details in synthetic session response, got:\n%s", ciView.selectedSession.ResponseText)
	}

	mergedView := base.enterMergedView()
	if got, want := mergedView.selectedSession.CreatedAt, "2025-02-19T14:04:05Z"; got != want {
		t.Fatalf("merged view created_at = %q, want %q", got, want)
	}
	if !strings.Contains(mergedView.selectedSession.ResponseText, formatTimestampLocal("2025-02-19T14:04:05Z", "2006-01-02 15:04:05")) {
		t.Fatalf("expected merged markdown to format PR merged timestamp, got:\n%s", mergedView.selectedSession.ResponseText)
	}

	closedView := base.enterPRClosedView()
	if got, want := closedView.selectedSession.CreatedAt, "2025-02-19T14:05:06Z"; got != want {
		t.Fatalf("pr closed view created_at = %q, want %q", got, want)
	}
	if !strings.Contains(closedView.selectedSession.ResponseText, formatTimestampLocal("2025-02-19T14:05:06Z", "2006-01-02 15:04:05")) {
		t.Fatalf("expected pr-closed markdown to format PR closed timestamp, got:\n%s", closedView.selectedSession.ResponseText)
	}
}
