package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autopr/internal/config"
	"autopr/internal/db"

	tea "github.com/charmbracelet/bubbletea"
)

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
	jobs, err := store.ListJobs(ctx, "", "all")
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
	jobs, err := store.ListJobs(ctx, "", "all")
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	m.jobs = jobs
	m.cursor = 0
	return m, store, jobID
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

func TestJobsMsgClampsCursorWhenListShrinks(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	m, store, _ := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()

	m.cursor = 5
	modelAny, _ := m.Update(jobsMsg([]db.Job{{ID: "ap-job-one"}}))
	m = modelAny.(Model)
	if m.cursor != 0 {
		t.Fatalf("expected cursor to clamp to 0, got %d", m.cursor)
	}

	m.cursor = 2
	modelAny, _ = m.Update(jobsMsg(nil))
	m = modelAny.(Model)
	if m.cursor != 0 {
		t.Fatalf("expected cursor to reset to 0 for empty jobs list, got %d", m.cursor)
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
	jobs, err := store.ListJobs(ctx, "", "all")
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	modelAny, _ := m.Update(jobsMsg(jobs))
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
	modelAny, _ = m.Update(jobsMsg(updated))
	m = modelAny.(Model)
	if m.selected == nil || m.selected.State != "approved" {
		t.Fatalf("expected selected state to update to approved after jobs refresh")
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

func TestCancelOnReviewingStateJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmp := t.TempDir()

	m, store, jobID := newTestModelWithQueuedJob(t, tmp)
	defer store.Close()

	// Move job to "reviewing" via valid state machine path.
	transitionToReviewing(t, store, jobID)

	jobs, err := store.ListJobs(ctx, "", "all")
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
