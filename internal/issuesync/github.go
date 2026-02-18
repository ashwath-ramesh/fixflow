package issuesync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"autopr/internal/config"
	"autopr/internal/db"
	"autopr/internal/httputil"
)

func (s *Syncer) syncGitHub(ctx context.Context, p *config.ProjectConfig) error {
	if s.cfg.Tokens.GitHub == "" {
		slog.Debug("sync: skipping github (no token)", "project", p.Name)
		return nil
	}

	owner := p.GitHub.Owner
	repo := p.GitHub.Repo

	// Get cursor (last updated since timestamp).
	cursor, err := s.store.GetCursor(ctx, p.Name, "github")
	if err != nil {
		return err
	}

	params := url.Values{
		"state":     {"open"},
		"per_page":  {"100"},
		"sort":      {"updated"},
		"direction": {"asc"},
	}
	if cursor != "" {
		params.Set("since", cursor)
	}

	nextURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues?%s", owner, repo, params.Encode())
	token := s.cfg.Tokens.GitHub

	const maxPages = 50
	var latestUpdated string

	for page := range maxPages {
		currentURL := nextURL

		resp, err := httputil.Do(ctx, func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, "GET", currentURL, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Accept", "application/vnd.github+json")
			return req, nil
		}, httputil.DefaultRetryConfig())
		if err != nil {
			return fmt.Errorf("fetch github issues: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			return fmt.Errorf("github API %d: %s", resp.StatusCode, string(body))
		}

		var issues []githubIssue
		if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
			resp.Body.Close()
			return fmt.Errorf("decode github issues: %w", err)
		}

		linkHeader := resp.Header.Get("Link")
		resp.Body.Close()

		slog.Debug("sync: github issues fetched", "project", p.Name, "page", page+1, "count", len(issues))

		if len(issues) == 0 {
			break
		}

		if lu := s.syncGitHubIssues(ctx, p, issues); lu != "" {
			latestUpdated = lu
		}

		nextURL = parseGitHubNextURL(linkHeader)
		if nextURL == "" {
			break
		}
	}

	if latestUpdated != "" {
		if err := s.store.SetCursor(ctx, p.Name, "github", latestUpdated); err != nil {
			slog.Error("sync: set github cursor", "err", err)
		}
	}

	return nil
}

func (s *Syncer) syncGitHubIssues(ctx context.Context, p *config.ProjectConfig, issues []githubIssue) string {
	includeLabels := []string(nil)
	if p.GitHub != nil {
		includeLabels = p.GitHub.IncludeLabels
	}

	var latestUpdated string
	for _, issue := range issues {
		// Skip pull requests (they show up in issues API).
		if issue.PullRequest != nil {
			continue
		}

		// Skip self-created issues.
		if containsMarker(issue.Body) {
			continue
		}

		labels := make([]string, 0, len(issue.Labels))
		for _, l := range issue.Labels {
			labels = append(labels, l.Name)
		}

		eligibility := evaluateGitHubIssueEligibility(includeLabels, labels, time.Now().UTC())
		eligible := eligibility.Eligible

		ffid, err := s.store.UpsertIssue(ctx, db.IssueUpsert{
			ProjectName:   p.Name,
			Source:        "github",
			SourceIssueID: fmt.Sprintf("%d", issue.Number),
			Title:         issue.Title,
			Body:          issue.Body,
			URL:           issue.HTMLURL,
			State:         "open",
			Labels:        labels,
			Eligible:      &eligible,
			SkipReason:    eligibility.SkipReason,
			EvaluatedAt:   eligibility.EvaluatedAt,
			SourceUpdated: issue.UpdatedAt,
		})
		if err != nil {
			slog.Error("sync: upsert github issue", "number", issue.Number, "err", err)
			continue
		}

		if eligibility.Eligible {
			s.createJobIfNeeded(ctx, ffid, p.Name)
		} else {
			slog.Info("sync: github issue skipped by label gate",
				"project", p.Name,
				"number", issue.Number,
				"skip_reason", eligibility.SkipReason)
		}
		latestUpdated = issue.UpdatedAt
	}

	return latestUpdated
}

type githubIssue struct {
	Number      int           `json:"number"`
	Title       string        `json:"title"`
	Body        string        `json:"body"`
	HTMLURL     string        `json:"html_url"`
	State       string        `json:"state"`
	Labels      []githubLabel `json:"labels"`
	UpdatedAt   string        `json:"updated_at"`
	PullRequest *struct{}     `json:"pull_request,omitempty"`
}

type githubLabel struct {
	Name string `json:"name"`
}

var githubNextLinkRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// parseGitHubNextURL extracts the "next" URL from a GitHub Link header.
func parseGitHubNextURL(link string) string {
	m := githubNextLinkRe.FindStringSubmatch(link)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}
