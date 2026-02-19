package webhook

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"autopr/internal/config"
	"autopr/internal/db"
	"autopr/internal/issuesync"
)

const maxBodySize = 1 << 20 // 1MB

// Server handles GitLab webhook events.
type Server struct {
	cfg       *config.Config
	store     *db.Store
	jobCh     chan<- string
	mux       *http.ServeMux
	startedAt time.Time

	// Simple rate limiter: per-IP request count per window.
	mu         sync.Mutex
	rates      map[string]int
	rateWindow int64
}

func NewServer(cfg *config.Config, store *db.Store, jobCh chan<- string) *Server {
	s := &Server{
		cfg:       cfg,
		store:     store,
		jobCh:     jobCh,
		startedAt: time.Now(),
		rates:     make(map[string]int),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", s.handleWebhook)
	mux.HandleFunc("GET /health", s.handleHealth)
	s.mux = mux
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	jobQueueDepth, err := s.queuedJobDepth(r.Context())
	if err != nil {
		slog.Error("health: queued jobs count", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	uptimeSeconds := max(int(time.Since(s.startedAt).Seconds()), 0)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "running",
		"uptime_seconds":  uptimeSeconds,
		"job_queue_depth": jobQueueDepth,
	})
}

func (s *Server) queuedJobDepth(ctx context.Context) (int, error) {
	const q = `SELECT COUNT(*) FROM jobs WHERE state = 'queued'`
	var count int
	if err := s.store.Reader.QueryRowContext(ctx, q).Scan(&count); err != nil {
		return 0, fmt.Errorf("count queued jobs: %w", err)
	}
	return count, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// Rate limit (extract IP without port).
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr
	}
	s.mu.Lock()
	now := time.Now().Unix()
	if s.rateWindow != now {
		clear(s.rates)
		s.rateWindow = now
	}
	s.rates[ip]++
	count := s.rates[ip]
	s.mu.Unlock()
	if count > 10 {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}

	// Validate token.
	if s.cfg.Daemon.WebhookSecret != "" {
		token := r.Header.Get("X-Gitlab-Token")
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.Daemon.WebhookSecret)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	// Read body with size limit.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	// Parse event type.
	eventType := r.Header.Get("X-Gitlab-Event")
	if eventType != "Issue Hook" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Parse issue event.
	var event gitlabIssueEvent
	if err := json.Unmarshal(body, &event); err != nil {
		slog.Warn("parse webhook body", "err", err)
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	action := event.ObjectAttributes.Action
	if action != "open" && action != "reopen" && action != "close" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Find project.
	projectCfg := s.findProject(event)
	if projectCfg == nil {
		slog.Debug("webhook: no matching project", "project_id", event.Project.ID)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Skip self-created mirror issues.
	if containsAPMarker(event.ObjectAttributes.Description) {
		slog.Debug("webhook: skipping self-created mirror issue", "iid", event.ObjectAttributes.IID)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	includeLabels := []string(nil)
	excludeLabels := projectCfg.ExcludeLabels
	if projectCfg.GitLab != nil {
		includeLabels = projectCfg.GitLab.IncludeLabels
	}

	labels := event.Labels()
	eligibility := issuesync.EvaluateIssueEligibility(includeLabels, excludeLabels, labels, time.Now().UTC())

	// Upsert issue.
	ctx := r.Context()
	sourceIssueID := fmt.Sprintf("%d", event.ObjectAttributes.IID)
	state := "open"
	if action == "close" {
		state = "closed"
	}
	eligible := eligibility.Eligible
	ffid, err := s.store.UpsertIssue(ctx, db.IssueUpsert{
		ProjectName:   projectCfg.Name,
		Source:        "gitlab",
		SourceIssueID: sourceIssueID,
		Title:         event.ObjectAttributes.Title,
		Body:          event.ObjectAttributes.Description,
		URL:           event.ObjectAttributes.URL,
		State:         state,
		Labels:        labels,
		Eligible:      &eligible,
		SkipReason:    eligibility.SkipReason,
		EvaluatedAt:   eligibility.EvaluatedAt,
	})
	if err != nil {
		slog.Error("webhook: upsert issue", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if action == "close" {
		cancelledIDs, err := s.store.CancelCancellableJobsForIssue(ctx, ffid, db.CancelReasonSourceIssueClosed)
		if err != nil {
			slog.Error("webhook: cancel jobs for closed issue", "project", projectCfg.Name, "issue", sourceIssueID, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, jobID := range cancelledIDs {
			if err := s.store.MarkRunningSessionsCancelled(ctx, jobID); err != nil {
				slog.Warn("webhook: mark running sessions cancelled", "job_id", jobID, "err", err)
			}
			slog.Info("webhook: cancelled job for closed issue",
				"project", projectCfg.Name,
				"source", "gitlab",
				"issue", sourceIssueID,
				"job_id", jobID,
				"reason", db.CancelReasonSourceIssueClosed)
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Skip ineligible issues.
	if !eligibility.Eligible {
		slog.Info("webhook: issue skipped by label gate",
			"project", projectCfg.Name,
			"iid", event.ObjectAttributes.IID,
			"skip_reason", eligibility.SkipReason)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Check for existing active job.
	active, err := s.store.HasActiveJobForIssue(ctx, ffid)
	if err != nil {
		slog.Error("webhook: check active job", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if active {
		slog.Debug("webhook: active job exists, skipping", "ffid", ffid)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Create job.
	jobID, err := s.store.CreateJob(ctx, ffid, projectCfg.Name, s.cfg.Daemon.MaxIterations)
	if err != nil {
		if errors.Is(err, db.ErrDuplicateActiveJob) {
			slog.Debug("webhook: active job already exists, skipping", "ffid", ffid)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		slog.Error("webhook: create job", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Notify worker pool.
	select {
	case s.jobCh <- jobID:
	default:
		slog.Warn("webhook: job channel full, job will be picked up on next poll", "job_id", jobID)
	}

	slog.Info("webhook: job created", "job_id", jobID, "ffid", ffid, "project", projectCfg.Name)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) findProject(event gitlabIssueEvent) *config.ProjectConfig {
	projectID := fmt.Sprintf("%d", event.Project.ID)
	for i := range s.cfg.Projects {
		p := &s.cfg.Projects[i]
		if p.GitLab != nil && p.GitLab.ProjectID == projectID {
			return p
		}
	}
	return nil
}

func containsAPMarker(desc string) bool {
	return strings.Contains(desc, "ap-id:") || strings.Contains(desc, "ap-sentry-issue:")
}

type gitlabIssueEvent struct {
	ObjectKind       string           `json:"object_kind"`
	ObjectAttributes gitlabIssueAttrs `json:"object_attributes"`
	Project          gitlabProject    `json:"project"`
	LabelsRaw        []gitlabLabel    `json:"labels"`
}

func (e gitlabIssueEvent) Labels() []string {
	out := make([]string, 0, len(e.LabelsRaw))
	for _, l := range e.LabelsRaw {
		out = append(out, l.Title)
	}
	return out
}

type gitlabIssueAttrs struct {
	IID         int    `json:"iid"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Action      string `json:"action"`
	State       string `json:"state"`
}

type gitlabProject struct {
	ID int `json:"id"`
}

type gitlabLabel struct {
	Title string `json:"title"`
}
