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

func keyRunes(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}
