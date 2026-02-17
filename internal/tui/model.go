package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"autopr/internal/config"
	"autopr/internal/db"
	"autopr/internal/git"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// ── Styles ──────────────────────────────────────────────────────────────────

const pad = 2 // horizontal padding on each side

var (
	frameStyle    = lipgloss.NewStyle().Padding(1, pad)
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("46"))
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("37"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("46"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("242"))
	labelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	dotRunning    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("46")).Render("●")
	dotStopped    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196")).Render("●")
	stateStyle    = map[string]lipgloss.Style{
		"queued":       lipgloss.NewStyle().Foreground(lipgloss.Color("246")),
		"planning":     lipgloss.NewStyle().Foreground(lipgloss.Color("33")),
		"implementing": lipgloss.NewStyle().Foreground(lipgloss.Color("33")),
		"reviewing":    lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
		"testing":      lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
		"ready":        lipgloss.NewStyle().Foreground(lipgloss.Color("46")),
		"approved":     lipgloss.NewStyle().Foreground(lipgloss.Color("40")),
		"merged":       lipgloss.NewStyle().Foreground(lipgloss.Color("141")),
		"pr closed":   lipgloss.NewStyle().Foreground(lipgloss.Color("208")),
		"rejected":     lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
		"failed":       lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
	}
	sessStatusStyle = map[string]lipgloss.Style{
		"running":   lipgloss.NewStyle().Foreground(lipgloss.Color("33")),
		"completed": lipgloss.NewStyle().Foreground(lipgloss.Color("46")),
		"failed":    lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
	}
	diffAddStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("46"))
	diffDelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	diffHunkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("37"))
	diffMetaStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	activeTab     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("46")).Underline(true)
	inactiveTab   = dimStyle
)

// ── Model ───────────────────────────────────────────────────────────────────

// Model is the BubbleTea model for the AutoPR TUI.
//
// Navigation depth:
//
//	selected == nil                          → Level 1 (job list)
//	selected != nil && !showDiff && selectedSession == nil → Level 2 (job detail + sessions)
//	showDiff                                 → Level 2d (diff view)
//	selectedSession != nil                   → Level 3 (session detail)
type Model struct {
	store *db.Store
	cfg   *config.Config

	// Level 1: job list
	jobs   []db.Job
	cursor int

	// Level 2: job detail + session list
	selected     *db.Job
	sessions     []db.LLMSessionSummary
	testArtifact *db.Artifact // test_output artifact (nil if tests haven't run)
	sessCursor   int

	// Level 2: confirmation prompt and action feedback
	confirmAction string // "approve", "reject", "retry", or "" (none)
	actionErr     error  // non-fatal error from last action (shown inline)

	// Level 2d: diff view
	showDiff   bool
	diffLines  []string
	diffOffset int

	// Level 3: session detail with scrollable output
	selectedSession *db.LLMSession
	showInput       bool     // tab toggles input/output
	scrollOffset    int
	lines           []string // pre-split content lines

	err    error
	width  int
	height int
}

func NewModel(store *db.Store, cfg *config.Config) Model {
	return Model{store: store, cfg: cfg}
}

// ── Messages ────────────────────────────────────────────────────────────────

type jobsMsg []db.Job
type sessionsMsg struct {
	jobID        string
	sessions     []db.LLMSessionSummary
	testArtifact *db.Artifact
}
type sessionMsg struct {
	jobID   string
	session db.LLMSession
}
type diffMsg struct {
	jobID string
	lines []string
}
type actionResultMsg struct {
	action string
	err    error
	prURL  string
}
type errMsg error

// ── Init / Commands ─────────────────────────────────────────────────────────

func (m Model) Init() tea.Cmd { return m.fetchJobs }

func (m Model) fetchJobs() tea.Msg {
	jobs, err := m.store.ListJobs(context.Background(), "", "all")
	if err != nil {
		return errMsg(err)
	}
	return jobsMsg(jobs)
}

func (m Model) fetchSessions() tea.Msg {
	jobID := m.selected.ID
	sessions, err := m.store.ListSessionSummariesByJob(context.Background(), jobID)
	if err != nil {
		return errMsg(err)
	}
	msg := sessionsMsg{jobID: jobID, sessions: sessions}
	if art, err := m.store.GetLatestArtifact(context.Background(), jobID, "test_output"); err == nil {
		msg.testArtifact = &art
	}
	return msg
}

func (m Model) fetchFullSession() tea.Msg {
	jobID := m.selected.ID
	sess, err := m.store.GetFullSession(context.Background(), m.sessions[m.sessCursor].ID)
	if err != nil {
		return errMsg(err)
	}
	return sessionMsg{jobID: jobID, session: sess}
}

func (m Model) fetchDiff() tea.Msg {
	job := m.selected
	if job == nil || job.WorktreePath == "" {
		return diffMsg{jobID: "", lines: []string{"(no worktree available)"}}
	}

	baseBranch := "master"
	if p, ok := m.cfg.ProjectByName(job.ProjectName); ok && p.BaseBranch != "" {
		baseBranch = p.BaseBranch
	}

	// Mark untracked files as intent-to-add so they appear in diff output.
	addN := exec.CommandContext(context.Background(), "git", "add", "-N", ".")
	addN.Dir = job.WorktreePath
	_ = addN.Run()

	// Diff against origin base branch to show all job changes:
	// committed, staged, unstaged, and newly created files.
	cmd := exec.CommandContext(context.Background(),
		"git", "diff", fmt.Sprintf("origin/%s", baseBranch))
	cmd.Dir = job.WorktreePath
	out, err := cmd.Output()
	if err != nil {
		return diffMsg{jobID: job.ID, lines: []string{fmt.Sprintf("(git diff error: %v)", err)}}
	}
	if len(out) == 0 {
		return diffMsg{jobID: job.ID, lines: []string{"(no changes)"}}
	}
	return diffMsg{jobID: job.ID, lines: strings.Split(string(out), "\n")}
}

// openInEditor opens the worktree directory in the user's preferred editor.
// Tries $EDITOR, then falls back to "code", then "vim".
func (m Model) openInEditor() tea.Msg {
	dir := m.selected.WorktreePath
	editor := os.Getenv("EDITOR")
	if editor == "" {
		// Prefer VS Code if available, fall back to vim.
		if _, err := exec.LookPath("code"); err == nil {
			editor = "code"
		} else {
			editor = "vim"
		}
	}
	cmd := exec.Command(editor, dir)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Start()
	return nil
}

// openInBrowser opens the PR URL in the default browser.
func (m Model) openInBrowser() tea.Msg {
	openURL(m.selected.PRURL)
	return nil
}

// openIssue opens the original issue URL in the default browser.
func (m Model) openIssue() tea.Msg {
	openURL(m.selected.IssueURL)
	return nil
}

// openURL opens a URL in the default browser across platforms.
func openURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default: // linux, freebsd, etc.
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// ── Job Actions ─────────────────────────────────────────────────────────────

func (m Model) executeApprove() tea.Msg {
	ctx := context.Background()
	job := m.selected

	issue, err := m.store.GetIssueByAPID(ctx, job.AutoPRIssueID)
	if err != nil {
		return actionResultMsg{action: "approve", err: fmt.Errorf("load issue: %w", err)}
	}

	prURL := job.PRURL
	if prURL == "" {
		proj, ok := m.cfg.ProjectByName(job.ProjectName)
		if !ok {
			return actionResultMsg{action: "approve", err: fmt.Errorf("project %q not found", job.ProjectName)}
		}

		prTitle, prBody := buildTUIPRContent(job, issue)
		var prErr error
		prURL, prErr = createTUIPR(ctx, m.cfg, proj, *job, prTitle, prBody)
		if prErr != nil {
			return actionResultMsg{action: "approve", err: fmt.Errorf("create PR: %w", prErr)}
		}

		if prURL != "" {
			_ = m.store.UpdateJobField(ctx, job.ID, "pr_url", prURL)
		}
	}

	if err := m.store.TransitionState(ctx, job.ID, "ready", "approved"); err != nil {
		return actionResultMsg{action: "approve", err: err}
	}
	return actionResultMsg{action: "approve", prURL: prURL}
}

func (m Model) executeReject() tea.Msg {
	ctx := context.Background()
	if err := m.store.TransitionState(ctx, m.selected.ID, "ready", "rejected"); err != nil {
		return actionResultMsg{action: "reject", err: err}
	}
	return actionResultMsg{action: "reject"}
}

func (m Model) executeRetry() tea.Msg {
	ctx := context.Background()
	if err := m.store.ResetJobForRetry(ctx, m.selected.ID, ""); err != nil {
		return actionResultMsg{action: "retry", err: err}
	}
	return actionResultMsg{action: "retry"}
}

// buildTUIPRContent assembles PR title and body (mirrors pipeline.BuildPRContent).
func buildTUIPRContent(job *db.Job, issue db.Issue) (string, string) {
	title := fmt.Sprintf("[AutoPR] %s", issue.Title)
	body := fmt.Sprintf("Closes %s\n\n**Issue:** %s\n\n_Generated by [AutoPR](https://github.com/ashwath-ramesh/autopr) from job `%s`_\n",
		issue.URL, issue.Title, db.ShortID(job.ID))
	return title, body
}

// createTUIPR creates a GitHub PR or GitLab MR based on project config.
func createTUIPR(ctx context.Context, cfg *config.Config, proj *config.ProjectConfig, job db.Job, title, body string) (string, error) {
	if job.BranchName == "" {
		return "", fmt.Errorf("job has no branch name — was the branch pushed?")
	}
	switch {
	case proj.GitHub != nil:
		if cfg.Tokens.GitHub == "" {
			return "", fmt.Errorf("GITHUB_TOKEN required to create PR")
		}
		return git.CreateGitHubPR(ctx, cfg.Tokens.GitHub, proj.GitHub.Owner, proj.GitHub.Repo,
			job.BranchName, proj.BaseBranch, title, body, false)
	case proj.GitLab != nil:
		if cfg.Tokens.GitLab == "" {
			return "", fmt.Errorf("GITLAB_TOKEN required to create MR")
		}
		return git.CreateGitLabMR(ctx, cfg.Tokens.GitLab, proj.GitLab.BaseURL, proj.GitLab.ProjectID,
			job.BranchName, proj.BaseBranch, title, body)
	default:
		return "", fmt.Errorf("project %q has no GitHub or GitLab config for PR creation", proj.Name)
	}
}

// ── Update ──────────────────────────────────────────────────────────────────

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case jobsMsg:
		m.jobs = msg
		m.err = nil
	case sessionsMsg:
		// Discard stale response if user navigated away.
		if m.selected == nil || m.selected.ID != msg.jobID {
			break
		}
		m.sessions = msg.sessions
		m.testArtifact = msg.testArtifact
		m.sessCursor = 0
		m.err = nil
	case sessionMsg:
		if m.selected == nil || m.selected.ID != msg.jobID {
			break
		}
		sess := msg.session
		m.selectedSession = &sess
		m.showInput = false
		m.scrollOffset = 0
		m.lines = splitContent(sess.ResponseText, sess.Status, m.cw())
	case diffMsg:
		if m.selected == nil || m.selected.ID != msg.jobID {
			break
		}
		m.diffLines = msg.lines
		m.showDiff = true
		m.diffOffset = 0
	case actionResultMsg:
		m.confirmAction = ""
		if msg.err != nil {
			// Non-fatal: show error inline on the detail view.
			m.actionErr = msg.err
		} else {
			// Action succeeded — refresh job list and go back to Level 1.
			m.actionErr = nil
			m.selected = nil
			m.sessions = nil
			m.testArtifact = nil
			m.sessCursor = 0
			return m, m.fetchJobs
		}
	case errMsg:
		m.err = msg
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func splitContent(text, status string, width int) []string {
	if text == "" {
		if status == "running" {
			return []string{"(in progress)"}
		}
		return []string{"(no output)"}
	}
	return renderMarkdown(text, width)
}

// renderMarkdown renders text as terminal-styled markdown via glamour.
// Falls back to plain text splitting on error.
func renderMarkdown(text string, width int) []string {
	if width < 40 {
		width = 76
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return strings.Split(text, "\n")
	}
	rendered, err := r.Render(text)
	if err != nil {
		return strings.Split(text, "\n")
	}
	// Trim trailing newlines that glamour adds.
	rendered = strings.TrimRight(rendered, "\n")
	return strings.Split(rendered, "\n")
}

// ── Key Handling ────────────────────────────────────────────────────────────

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	}

	// Confirmation prompt active — handle y/n.
	if m.confirmAction != "" {
		switch key {
		case "y":
			action := m.confirmAction
			switch action {
			case "approve":
				return m, m.executeApprove
			case "reject":
				return m, m.executeReject
			case "retry":
				return m, m.executeRetry
			}
		case "n", "esc":
			m.confirmAction = ""
		}
		return m, nil
	}

	if m.showDiff {
		return m.handleKeyDiff(key)
	}
	if m.selectedSession != nil {
		return m.handleKeyLevel3(key)
	}
	if m.selected != nil {
		return m.handleKeyLevel2(key)
	}
	return m.handleKeyLevel1(key)
}

func (m Model) handleKeyLevel1(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.jobs)-1 {
			m.cursor++
		}
	case "enter":
		if m.cursor < len(m.jobs) {
			m.selected = &m.jobs[m.cursor]
			return m, m.fetchSessions
		}
	case "r":
		return m, m.fetchJobs
	}
	return m, nil
}

func (m Model) handleKeyLevel2(key string) (tea.Model, tea.Cmd) {
	maxCursor := len(m.sessions) - 1
	if m.testArtifact != nil {
		maxCursor++
	}
	if m.selected != nil && m.selected.PRURL != "" {
		maxCursor++
	}
	if m.selected != nil && m.selected.PRMergedAt != "" {
		maxCursor++
	}
	if m.selected != nil && m.selected.PRClosedAt != "" {
		maxCursor++
	}
	switch key {
	case "up", "k":
		if m.sessCursor > 0 {
			m.sessCursor--
		}
	case "down", "j":
		if m.sessCursor < maxCursor {
			m.sessCursor++
		}
	case "enter":
		if m.sessCursor < len(m.sessions) {
			return m, m.fetchFullSession
		}
		testRowIdx := len(m.sessions)
		prRowIdx := testRowIdx
		if m.testArtifact != nil {
			prRowIdx++
		}
		mergedRowIdx := prRowIdx
		if m.selected != nil && m.selected.PRURL != "" {
			mergedRowIdx++
		}
		closedRowIdx := mergedRowIdx
		if m.selected != nil && m.selected.PRMergedAt != "" {
			closedRowIdx++
		}
		if m.testArtifact != nil && m.sessCursor == testRowIdx {
			m = m.enterTestView()
			return m, nil
		}
		if m.selected != nil && m.selected.PRURL != "" && m.sessCursor == prRowIdx {
			m = m.enterPRView()
			return m, nil
		}
		if m.selected != nil && m.selected.PRMergedAt != "" && m.sessCursor == mergedRowIdx {
			m = m.enterMergedView()
			return m, nil
		}
		if m.selected != nil && m.selected.PRClosedAt != "" && m.sessCursor == closedRowIdx {
			m = m.enterPRClosedView()
			return m, nil
		}
	case "d":
		if m.selected != nil && m.selected.WorktreePath != "" {
			return m, m.fetchDiff
		}
	case "o":
		if m.selected != nil && m.selected.WorktreePath != "" {
			return m, m.openInEditor
		}
	case "b":
		if m.selected != nil && m.selected.PRURL != "" {
			return m, m.openInBrowser
		}
	case "i":
		if m.selected != nil && m.selected.IssueURL != "" {
			return m, m.openIssue
		}
	case "a":
		if m.selected != nil && m.selected.State == "ready" {
			m.confirmAction = "approve"
			m.actionErr = nil
		}
	case "x":
		if m.selected != nil && m.selected.State == "ready" {
			m.confirmAction = "reject"
			m.actionErr = nil
		}
	case "R":
		if m.selected != nil && (m.selected.State == "failed" || m.selected.State == "rejected") {
			m.confirmAction = "retry"
			m.actionErr = nil
		}
	case "esc":
		m.selected = nil
		m.sessions = nil
		m.testArtifact = nil
		m.sessCursor = 0
		m.confirmAction = ""
		m.actionErr = nil
	case "r":
		return m, m.fetchSessions
	}
	return m, nil
}

func (m Model) handleKeyLevel3(key string) (tea.Model, tea.Cmd) {
	avail := m.scrollHeight()
	switch key {
	case "up", "k":
		if m.scrollOffset > 0 {
			m.scrollOffset--
		}
	case "down", "j":
		if m.scrollOffset < maxOffset(m.lines, avail) {
			m.scrollOffset++
		}
	case "u":
		m.scrollOffset -= avail / 2
		if m.scrollOffset < 0 {
			m.scrollOffset = 0
		}
	case "d":
		m.scrollOffset += avail / 2
		if m.scrollOffset > maxOffset(m.lines, avail) {
			m.scrollOffset = maxOffset(m.lines, avail)
		}
	case "tab":
		m.showInput = !m.showInput
		m.scrollOffset = 0
		if m.showInput {
			if m.selectedSession.PromptText != "" {
				m.lines = renderMarkdown(m.selectedSession.PromptText, m.cw())
			} else {
				m.lines = []string{"(no input recorded)"}
			}
		} else {
			m.lines = splitContent(m.selectedSession.ResponseText, m.selectedSession.Status, m.cw())
		}
	case "esc":
		m.selectedSession = nil
		m.lines = nil
		m.scrollOffset = 0
		m.showInput = false
	}
	return m, nil
}

func (m Model) handleKeyDiff(key string) (tea.Model, tea.Cmd) {
	avail := m.scrollHeight()
	switch key {
	case "up", "k":
		if m.diffOffset > 0 {
			m.diffOffset--
		}
	case "down", "j":
		if m.diffOffset < maxOffset(m.diffLines, avail) {
			m.diffOffset++
		}
	case "u":
		m.diffOffset -= avail / 2
		if m.diffOffset < 0 {
			m.diffOffset = 0
		}
	case "d":
		m.diffOffset += avail / 2
		if m.diffOffset > maxOffset(m.diffLines, avail) {
			m.diffOffset = maxOffset(m.diffLines, avail)
		}
	case "esc":
		m.showDiff = false
		m.diffLines = nil
		m.diffOffset = 0
	}
	return m, nil
}

// testStatus derives the test step status from the current job state.
func (m Model) testStatus() string {
	if m.selected == nil {
		return "completed"
	}
	switch m.selected.State {
	case "ready", "approved", "rejected":
		return "completed"
	case "testing":
		return "running"
	default:
		return "failed"
	}
}

// enterTestView enters Level 3 to display the test artifact output.
func (m Model) enterTestView() Model {
	testCmd := "(no test command configured)"
	if p, ok := m.cfg.ProjectByName(m.selected.ProjectName); ok && p.TestCmd != "" {
		testCmd = fmt.Sprintf("$ %s", p.TestCmd)
	}
	m.selectedSession = &db.LLMSession{
		Step:         "tests",
		Iteration:    m.testArtifact.Iteration,
		LLMProvider:  "shell",
		Status:       m.testStatus(),
		ResponseText: m.testArtifact.Content,
		PromptText:   testCmd,
	}
	m.showInput = false
	m.scrollOffset = 0
	m.lines = splitContent(m.selectedSession.ResponseText, m.selectedSession.Status, m.cw())
	return m
}

// enterMergedView enters Level 3 to display the PR merge details.
func (m Model) enterMergedView() Model {
	content := fmt.Sprintf("Pull request was merged.\n\n**Merged at:** %s\n\n**PR:** %s", m.selected.PRMergedAt, m.selected.PRURL)
	m.selectedSession = &db.LLMSession{
		Step:         "merged",
		LLMProvider:  "-",
		Status:       "completed",
		ResponseText: content,
		PromptText:   "(detected by sync loop)",
	}
	m.showInput = false
	m.scrollOffset = 0
	m.lines = renderMarkdown(content, m.cw())
	return m
}

// enterPRView enters Level 3 to display the PR creation details.
func (m Model) enterPRView() Model {
	prURL := m.selected.PRURL
	content := fmt.Sprintf("Pull request created successfully.\n\n**URL:** %s", prURL)
	m.selectedSession = &db.LLMSession{
		Step:         "approved",
		LLMProvider:  "-",
		Status:       "completed",
		ResponseText: content,
		PromptText:   fmt.Sprintf("ap approve %s", db.ShortID(m.selected.ID)),
	}
	m.showInput = false
	m.scrollOffset = 0
	m.lines = renderMarkdown(content, m.cw())
	return m
}

// enterPRClosedView enters Level 3 to display the PR closed details.
func (m Model) enterPRClosedView() Model {
	content := fmt.Sprintf("Pull request was closed without merging.\n\n**Closed at:** %s\n\n**PR:** %s", m.selected.PRClosedAt, m.selected.PRURL)
	m.selectedSession = &db.LLMSession{
		Step:         "pr closed",
		LLMProvider:  "-",
		Status:       "completed",
		ResponseText: content,
		PromptText:   "(detected by sync loop)",
	}
	m.showInput = false
	m.scrollOffset = 0
	m.lines = renderMarkdown(content, m.cw())
	return m
}

func maxOffset(lines []string, avail int) int {
	n := len(lines) - avail
	if n < 0 {
		return 0
	}
	return n
}

// ── Views ───────────────────────────────────────────────────────────────────

func (m Model) View() string {
	var content string
	if m.err != nil {
		content = fmt.Sprintf("Error: %v\n\nPress q to quit.", m.err)
	} else if m.showDiff {
		content = m.diffView()
	} else if m.selectedSession != nil {
		content = m.sessionView()
	} else if m.selected != nil {
		content = m.detailView()
	} else {
		content = m.listView()
	}
	return frameStyle.Render(content)
}

// ── Level 1: Job List with Dashboard Header ─────────────────────────────────

func (m Model) listView() string {
	var b strings.Builder
	w := m.cw()

	// ── Title bar ──
	b.WriteString(titleStyle.Render("AUTOPR"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(strings.Repeat("─", w)))
	b.WriteString("\n\n")

	// ── Dashboard status — one row per stat ──
	daemonDot := dotStopped
	daemonLabel := "stopped"
	if isDaemonRunning(m.cfg.Daemon.PIDFile) {
		daemonDot = dotRunning
		daemonLabel = "running"
	}

	dashKV := func(k, v string) {
		b.WriteString(fmt.Sprintf("  %s  %s\n", labelStyle.Render(padRight(k, 9)), v))
	}
	dashKV("daemon", daemonDot+" "+daemonLabel)
	dashKV("sync", m.cfg.Daemon.SyncInterval)
	dashKV("workers", fmt.Sprintf("%d", m.cfg.Daemon.MaxWorkers))
	b.WriteString("\n")

	// Job state counters.
	counts := m.jobCounts()
	active := counts["planning"] + counts["implementing"] + counts["reviewing"] + counts["testing"]
	b.WriteString(fmt.Sprintf("  %s %d   %s %d   %s %d   %s %d   %s %d\n",
		labelStyle.Render("queued"), counts["queued"],
		labelStyle.Render("active"), active,
		stateStyle["ready"].Render("ready"), counts["ready"],
		stateStyle["failed"].Render("failed"), counts["failed"],
		labelStyle.Render("done"), counts["approved"]+counts["rejected"],
	))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(strings.Repeat("─", w)))
	b.WriteString("\n")

	// ── Job table ──
	const (
		colJob     = 10
		colState   = 20
		colProject = 13
		colSource  = 13
		colRetry   = 8
		colIssue   = 55
	)

	if len(m.jobs) == 0 {
		b.WriteString(dimStyle.Render("No jobs found. Waiting for issues..."))
		b.WriteString("\n")
	} else {
		header := "  " +
			headerStyle.Render(padRight("JOB", colJob)) +
			headerStyle.Render(padRight("STATE", colState)) +
			headerStyle.Render(padRight("PROJECT", colProject)) +
			headerStyle.Render(padRight("SOURCE", colSource)) +
			headerStyle.Render(padRight("RETRY", colRetry)) +
			headerStyle.Render(padRight("ISSUE", colIssue)) +
			headerStyle.Render("UPDATED")
		b.WriteString(header)
		b.WriteString("\n")

		for i, job := range m.jobs {
			cursor := "  "
			if i == m.cursor {
				cursor = "> "
			}

			displayState := db.DisplayState(job.State, job.PRMergedAt, job.PRClosedAt)
			st, ok := stateStyle[displayState]
			if !ok {
				st, ok = stateStyle[job.State]
				if !ok {
					st = dimStyle
				}
			}

			source := ""
			if job.IssueSource != "" && job.SourceIssueID != "" {
				source = fmt.Sprintf("%s #%s", capitalize(job.IssueSource), job.SourceIssueID)
			}

			title := truncate(job.IssueTitle, colIssue-2)

			updated := job.UpdatedAt
			if len(updated) > 11 {
				updated = updated[11:]
			}

			line := cursor +
				padRight(db.ShortID(job.ID), colJob) +
				st.Render(padRight(displayState, colState)) +
				padRight(truncate(job.ProjectName, colProject-1), colProject) +
				padRight(source, colSource) +
				padRight(fmt.Sprintf("%d/%d", job.Iteration, job.MaxIterations), colRetry) +
				padRight(title, colIssue) +
				dimStyle.Render(updated)

			if i == m.cursor {
				line = selectedStyle.Render(line)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	b.WriteString(dimStyle.Render(strings.Repeat("─", w)))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("j/k navigate  enter details  r refresh  q quit"))
	return b.String()
}

// ── Level 2: Job Detail + Session List ──────────────────────────────────────

func (m Model) detailView() string {
	var b strings.Builder
	w := m.cw()
	job := m.selected

	displayState := db.DisplayState(job.State, job.PRMergedAt, job.PRClosedAt)
	st, ok := stateStyle[displayState]
	if !ok {
		st, ok = stateStyle[job.State]
		if !ok {
			st = dimStyle
		}
	}

	b.WriteString(titleStyle.Render("JOB"))
	b.WriteString(dimStyle.Render("  " + job.ID))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(strings.Repeat("─", w)))
	b.WriteString("\n")

	kv := func(k, v string) {
		b.WriteString(fmt.Sprintf("%s %s\n", headerStyle.Render(fmt.Sprintf("%-11s", k)), v))
	}
	kv("State", st.Render(displayState))
	kv("Project", job.ProjectName)
	if job.IssueSource != "" && job.SourceIssueID != "" {
		kv("Issue", fmt.Sprintf("%s #%s", capitalize(job.IssueSource), job.SourceIssueID))
	} else {
		kv("Issue", job.AutoPRIssueID)
	}
	if job.IssueTitle != "" {
		kv("Title", job.IssueTitle)
	}
	kv("Retry", fmt.Sprintf("%d/%d", job.Iteration, job.MaxIterations))
	if job.BranchName != "" {
		kv("Branch", job.BranchName)
	}
	if job.CommitSHA != "" {
		kv("Commit", job.CommitSHA[:minInt(12, len(job.CommitSHA))])
	}
	if job.PRMergedAt != "" {
		kv("Merged", stateStyle["merged"].Render(job.PRMergedAt))
	}
	if job.PRClosedAt != "" {
		kv("PR Closed", stateStyle["pr closed"].Render(job.PRClosedAt))
	}
	if job.ErrorMessage != "" {
		kv("Error", lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(job.ErrorMessage))
	}
	if job.RejectReason != "" {
		kv("Rejected", job.RejectReason)
	}
	if m.actionErr != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(fmt.Sprintf("Action failed: %v", m.actionErr)))
		b.WriteString("\n")
	}

	// Session pipeline table.
	b.WriteString("\n")
	b.WriteString(titleStyle.Render("PIPELINE"))
	stepCount := len(m.sessions)
	if m.testArtifact != nil {
		stepCount++
	}
	if job.PRURL != "" {
		stepCount++
	}
	if job.PRMergedAt != "" {
		stepCount++
	}
	if job.PRClosedAt != "" {
		stepCount++
	}
	b.WriteString(dimStyle.Render(fmt.Sprintf("  %d steps", stepCount)))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(strings.Repeat("─", w)))
	b.WriteString("\n")

	// Column widths for sessions table.
	const (
		sColNum      = 4
		sColStep     = 15
		sColStatus   = 12
		sColProvider = 10
		sColTokens   = 16
	)

	if stepCount == 0 {
		b.WriteString(dimStyle.Render("(no steps yet)"))
		b.WriteString("\n")
	} else {
		header := "  " +
			headerStyle.Render(padRight("#", sColNum)) +
			headerStyle.Render(padRight("STATE", sColStep)) +
			headerStyle.Render(padRight("STATUS", sColStatus)) +
			headerStyle.Render(padRight("PROVIDER", sColProvider)) +
			headerStyle.Render(padRight("TOKENS", sColTokens)) +
			headerStyle.Render("TIME")
		b.WriteString(header)
		b.WriteString("\n")

		for i, s := range m.sessions {
			cursor := "  "
			if i == m.sessCursor {
				cursor = "> "
			}

			sst, ok := sessStatusStyle[s.Status]
			if !ok {
				sst = dimStyle
			}

			stepDisplay := db.DisplayStep(s.Step)
			tokens := fmt.Sprintf("%d/%d", s.InputTokens, s.OutputTokens)
			dur := fmt.Sprintf("%ds", s.DurationMS/1000)

			line := cursor +
				padRight(fmt.Sprintf("%d", i+1), sColNum) +
				padRight(stepDisplay, sColStep) +
				sst.Render(padRight(s.Status, sColStatus)) +
				padRight(s.LLMProvider, sColProvider) +
				padRight(tokens, sColTokens) +
				dimStyle.Render(dur)

			if i == m.sessCursor {
				line = selectedStyle.Render(line)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}

		// Test row (shell step, not an LLM session).
		if m.testArtifact != nil {
			testIdx := len(m.sessions)
			cursor := "  "
			if testIdx == m.sessCursor {
				cursor = "> "
			}

			status := m.testStatus()
			sst, ok := sessStatusStyle[status]
			if !ok {
				sst = dimStyle
			}

			line := cursor +
				padRight(fmt.Sprintf("%d", testIdx+1), sColNum) +
				padRight("testing", sColStep) +
				sst.Render(padRight(status, sColStatus)) +
				padRight("-", sColProvider) +
				padRight("-", sColTokens) +
				dimStyle.Render("-")

			if testIdx == m.sessCursor {
				line = selectedStyle.Render(line)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}

		// PR row (shows when a PR/MR was created).
		if job.PRURL != "" {
			prIdx := len(m.sessions)
			if m.testArtifact != nil {
				prIdx++
			}
			cursor := "  "
			if prIdx == m.sessCursor {
				cursor = "> "
			}

			line := cursor +
				padRight(fmt.Sprintf("%d", prIdx+1), sColNum) +
				padRight("pr created", sColStep) +
				sessStatusStyle["completed"].Render(padRight("completed", sColStatus)) +
				padRight("-", sColProvider) +
				padRight("-", sColTokens) +
				dimStyle.Render("-")

			if prIdx == m.sessCursor {
				line = selectedStyle.Render(line)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}

		// Merged row (shows when the PR was merged remotely).
		if job.PRMergedAt != "" {
			mergedIdx := len(m.sessions)
			if m.testArtifact != nil {
				mergedIdx++
			}
			if job.PRURL != "" {
				mergedIdx++
			}
			cursor := "  "
			if mergedIdx == m.sessCursor {
				cursor = "> "
			}

			line := cursor +
				padRight(fmt.Sprintf("%d", mergedIdx+1), sColNum) +
				padRight("merged", sColStep) +
				stateStyle["merged"].Render(padRight("completed", sColStatus)) +
				padRight("-", sColProvider) +
				padRight("-", sColTokens) +
				dimStyle.Render("-")

			if mergedIdx == m.sessCursor {
				line = selectedStyle.Render(line)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}

		// PR closed row (shows when the PR was closed without merging).
		if job.PRClosedAt != "" {
			closedIdx := len(m.sessions)
			if m.testArtifact != nil {
				closedIdx++
			}
			if job.PRURL != "" {
				closedIdx++
			}
			if job.PRMergedAt != "" {
				closedIdx++
			}
			cursor := "  "
			if closedIdx == m.sessCursor {
				cursor = "> "
			}

			line := cursor +
				padRight(fmt.Sprintf("%d", closedIdx+1), sColNum) +
				padRight("pr closed", sColStep) +
				stateStyle["pr closed"].Render(padRight("closed", sColStatus)) +
				padRight("-", sColProvider) +
				padRight("-", sColTokens) +
				dimStyle.Render("-")

			if closedIdx == m.sessCursor {
				line = selectedStyle.Render(line)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	b.WriteString(dimStyle.Render(strings.Repeat("─", w)))
	b.WriteString("\n")

	// Confirmation prompt overrides normal hint bar.
	if m.confirmAction != "" {
		label := map[string]string{
			"approve": "Approve job " + db.ShortID(job.ID) + " and create PR?",
			"reject":  "Reject job " + db.ShortID(job.ID) + "?",
			"retry":   "Retry job " + db.ShortID(job.ID) + "?",
		}
		prompt := label[m.confirmAction]
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214")).Render(prompt))
		b.WriteString(dimStyle.Render("  y confirm  n cancel"))
		return b.String()
	}

	var hintParts []string
	hintParts = append(hintParts, "j/k navigate", "enter view step")
	if job.WorktreePath != "" {
		hintParts = append(hintParts, "d diff", "o editor")
	}
	if job.IssueURL != "" {
		hintParts = append(hintParts, "i issue")
	}
	if job.PRURL != "" {
		hintParts = append(hintParts, "b open PR")
	}
	if job.State == "ready" {
		hintParts = append(hintParts, "a approve", "x reject")
	}
	if job.State == "failed" || job.State == "rejected" {
		hintParts = append(hintParts, "R retry")
	}
	hintParts = append(hintParts, "esc back", "r refresh", "q quit")
	hints := strings.Join(hintParts, "  ")
	b.WriteString(dimStyle.Render(hints))
	return b.String()
}

// ── Level 3: Session Detail ─────────────────────────────────────────────────

func (m Model) sessionView() string {
	var b strings.Builder
	w := m.cw()
	sess := m.selectedSession

	// Find session number from sessions list.
	sessNum := 0
	for i, s := range m.sessions {
		if s.ID == sess.ID {
			sessNum = i + 1
			break
		}
	}
	if sessNum == 0 && sess.Step == "tests" {
		sessNum = len(m.sessions) + 1
	}
	if sessNum == 0 && sess.Step == "approved" {
		sessNum = len(m.sessions) + 1
		if m.testArtifact != nil {
			sessNum++
		}
	}
	if sessNum == 0 && sess.Step == "merged" {
		sessNum = len(m.sessions) + 1
		if m.testArtifact != nil {
			sessNum++
		}
		if m.selected != nil && m.selected.PRURL != "" {
			sessNum++
		}
	}
	if sessNum == 0 && sess.Step == "pr closed" {
		sessNum = len(m.sessions) + 1
		if m.testArtifact != nil {
			sessNum++
		}
		if m.selected != nil && m.selected.PRURL != "" {
			sessNum++
		}
		if m.selected != nil && m.selected.PRMergedAt != "" {
			sessNum++
		}
	}

	b.WriteString(titleStyle.Render(fmt.Sprintf("SESSION #%d", sessNum)))
	b.WriteString(dimStyle.Render(fmt.Sprintf("  %s (iter %d)", db.DisplayStep(sess.Step), sess.Iteration)))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(strings.Repeat("─", w)))
	b.WriteString("\n")

	sst, ok := sessStatusStyle[sess.Status]
	if !ok {
		sst = dimStyle
	}
	kv := func(k, v string) {
		b.WriteString(fmt.Sprintf("%s %s\n", headerStyle.Render(fmt.Sprintf("%-11s", k)), v))
	}
	kv("Status", sst.Render(sess.Status))
	kv("Provider", sess.LLMProvider)
	kv("Tokens", fmt.Sprintf("%d in / %d out", sess.InputTokens, sess.OutputTokens))
	kv("Duration", fmt.Sprintf("%ds", sess.DurationMS/1000))
	if sess.ErrorMessage != "" {
		kv("Error", lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(sess.ErrorMessage))
	}

	// Tab bar.
	b.WriteString("\n")
	inputTab := inactiveTab.Render(" INPUT ")
	outputTab := inactiveTab.Render(" OUTPUT ")
	if m.showInput {
		inputTab = activeTab.Render(" INPUT ")
	} else {
		outputTab = activeTab.Render(" OUTPUT ")
	}
	b.WriteString(inputTab)
	b.WriteString(dimStyle.Render(" │ "))
	b.WriteString(outputTab)
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(strings.Repeat("─", w)))
	b.WriteString("\n")

	// Scrollable body.
	avail := m.scrollHeight()
	start, end := scrollWindow(m.lines, m.scrollOffset, avail)
	for _, line := range m.lines[start:end] {
		b.WriteString(line)
		b.WriteString("\n")
	}

	b.WriteString(dimStyle.Render(strings.Repeat("─", w)))
	b.WriteString("\n")
	pct := scrollPercent(m.lines, m.scrollOffset, avail)
	b.WriteString(dimStyle.Render(fmt.Sprintf("j/k scroll  d/u half-page  tab toggle  esc back  q quit%s", pct)))
	return b.String()
}

// ── Diff View ───────────────────────────────────────────────────────────────

func (m Model) diffView() string {
	var b strings.Builder
	w := m.cw()

	b.WriteString(titleStyle.Render("DIFF"))
	if m.selected != nil {
		b.WriteString(dimStyle.Render("  " + m.selected.ID))
	}
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(strings.Repeat("─", w)))
	b.WriteString("\n")

	avail := m.scrollHeight()
	start, end := scrollWindow(m.diffLines, m.diffOffset, avail)
	for _, line := range m.diffLines[start:end] {
		b.WriteString(colorDiffLine(line))
		b.WriteString("\n")
	}

	b.WriteString(dimStyle.Render(strings.Repeat("─", w)))
	b.WriteString("\n")
	pct := scrollPercent(m.diffLines, m.diffOffset, avail)
	b.WriteString(dimStyle.Render(fmt.Sprintf("j/k scroll  d/u half-page  esc back  q quit%s", pct)))
	return b.String()
}

func colorDiffLine(line string) string {
	switch {
	case strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- "):
		return diffMetaStyle.Render(line)
	case strings.HasPrefix(line, "+"):
		return diffAddStyle.Render(line)
	case strings.HasPrefix(line, "-"):
		return diffDelStyle.Render(line)
	case strings.HasPrefix(line, "@@"):
		return diffHunkStyle.Render(line)
	case strings.HasPrefix(line, "diff --git"):
		return diffMetaStyle.Render(line)
	default:
		return line
	}
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// cw returns content width (terminal width minus frame padding).
func (m Model) cw() int {
	w := m.width - pad*2
	if w < 40 {
		w = 76 // sensible default before first WindowSizeMsg
	}
	return w
}

func (m Model) scrollHeight() int {
	// Reserve lines for chrome: frame padding(2) + title(1) + separator(1) + metadata(~6) + tabs(2) + footer(2).
	h := m.height - 16
	if h < 1 {
		h = 1
	}
	return h
}

func (m Model) jobCounts() map[string]int {
	counts := make(map[string]int)
	for _, j := range m.jobs {
		counts[j.State]++
	}
	return counts
}

func isDaemonRunning(pidFile string) bool {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func scrollWindow(lines []string, offset, avail int) (int, int) {
	if avail < 1 {
		avail = 1
	}
	start := offset
	if start > len(lines) {
		start = len(lines)
	}
	end := start + avail
	if end > len(lines) {
		end = len(lines)
	}
	return start, end
}

func scrollPercent(lines []string, offset, avail int) string {
	if len(lines) <= avail {
		return ""
	}
	mx := len(lines) - avail
	if mx <= 0 {
		return ""
	}
	return fmt.Sprintf("  [%d%%]", offset*100/mx)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// padRight pads a plain string to n characters with spaces.
func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}
