package issuesync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"autopr/internal/config"
	"autopr/internal/db"
	"autopr/internal/httputil"
)

func (s *Syncer) syncGitLab(ctx context.Context, p *config.ProjectConfig) error {
	if s.cfg.Tokens.GitLab == "" {
		slog.Debug("sync: skipping gitlab (no token)", "project", p.Name)
		return nil
	}

	baseURL := p.GitLab.BaseURL
	if baseURL == "" {
		baseURL = "https://gitlab.com"
	}
	projectID := p.GitLab.ProjectID

	// Get cursor (last updated_after timestamp).
	cursor, err := s.store.GetCursor(ctx, p.Name, "gitlab")
	if err != nil {
		return err
	}

	params := url.Values{
		"state":    {"opened"},
		"per_page": {"100"},
		"order_by": {"updated_at"},
		"sort":     {"asc"},
	}
	if cursor != "" {
		params.Set("updated_after", cursor)
	}

	baseAPIURL := fmt.Sprintf("%s/api/v4/projects/%s/issues?%s", baseURL, projectID, params.Encode())
	token := s.cfg.Tokens.GitLab

	const maxPages = 50
	var latestUpdated string
	nextPage := "1"

	for page := range maxPages {
		currentURL := baseAPIURL + "&page=" + nextPage

		resp, err := httputil.Do(ctx, func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, "GET", currentURL, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("PRIVATE-TOKEN", token)
			return req, nil
		}, httputil.DefaultRetryConfig())
		if err != nil {
			return fmt.Errorf("fetch gitlab issues: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			return fmt.Errorf("gitlab API %d: %s", resp.StatusCode, string(body))
		}

		var issues []gitlabIssue
		if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
			resp.Body.Close()
			return fmt.Errorf("decode gitlab issues: %w", err)
		}

		xNextPage := resp.Header.Get("X-Next-Page")
		resp.Body.Close()

		slog.Debug("sync: gitlab issues fetched", "project", p.Name, "page", page+1, "count", len(issues))

		if len(issues) == 0 {
			break
		}

		if lu := s.syncGitLabPage(ctx, p, issues); lu != "" {
			latestUpdated = lu
		}

		nextPage = strings.TrimSpace(xNextPage)
		if nextPage == "" {
			break
		}
	}

	// Update cursor.
	if latestUpdated != "" {
		if err := s.store.SetCursor(ctx, p.Name, "gitlab", latestUpdated); err != nil {
			slog.Error("sync: set gitlab cursor", "err", err)
		}
	}

	return nil
}

func (s *Syncer) syncGitLabPage(ctx context.Context, p *config.ProjectConfig, issues []gitlabIssue) string {
	var includeLabels []string
	if p.GitLab != nil {
		includeLabels = p.GitLab.IncludeLabels
	}
	excludeLabels := p.ExcludeLabels

	var latestUpdated string
	for _, issue := range issues {
		// Skip issues created by autopr (contain our marker).
		if containsMarker(issue.Description) {
			continue
		}

		labels := make([]string, 0, len(issue.Labels))
		labels = append(labels, issue.Labels...)

		eligibility := evaluateIssueEligibility(includeLabels, excludeLabels, labels, time.Now().UTC())
		eligible := eligibility.Eligible

		ffid, err := s.store.UpsertIssue(ctx, db.IssueUpsert{
			ProjectName:   p.Name,
			Source:        "gitlab",
			SourceIssueID: fmt.Sprintf("%d", issue.IID),
			Title:         issue.Title,
			Body:          issue.Description,
			URL:           issue.WebURL,
			State:         "open",
			Labels:        labels,
			Eligible:      &eligible,
			SkipReason:    eligibility.SkipReason,
			EvaluatedAt:   eligibility.EvaluatedAt,
			SourceUpdated: issue.UpdatedAt,
		})
		if err != nil {
			slog.Error("sync: upsert gitlab issue", "iid", issue.IID, "err", err)
			continue
		}

		if eligibility.Eligible {
			s.createJobIfNeeded(ctx, ffid, p.Name)
		} else {
			slog.Info("sync: gitlab issue skipped by label gate",
				"project", p.Name,
				"iid", issue.IID,
				"skip_reason", eligibility.SkipReason)
		}
		latestUpdated = issue.UpdatedAt
	}

	return latestUpdated
}

type gitlabIssue struct {
	IID         int      `json:"iid"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	WebURL      string   `json:"web_url"`
	State       string   `json:"state"`
	Labels      []string `json:"labels"`
	UpdatedAt   string   `json:"updated_at"`
	CreatedAt   string   `json:"created_at"`
}

func containsMarker(s string) bool {
	return strings.Contains(s, "ap-id:") || strings.Contains(s, "ap-sentry-issue:")
}
