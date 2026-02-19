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

	"autopr/internal/config"
	"autopr/internal/db"
	"autopr/internal/httputil"
)

func (s *Syncer) syncSentry(ctx context.Context, p *config.ProjectConfig) error {
	if s.cfg.Tokens.Sentry == "" {
		slog.Debug("sync: skipping sentry (no token)", "project", p.Name)
		return nil
	}

	org := p.Sentry.Org
	project := p.Sentry.Project
	baseURL := s.cfg.Sentry.BaseURL

	var assignedTeam string
	if p.Sentry.AssignedTeam != nil {
		assignedTeam = *p.Sentry.AssignedTeam
	}
	query := sentryIssueQuery(assignedTeam)
	baseAPIURL := fmt.Sprintf("%s/api/0/projects/%s/%s/issues/?query=%s&sort=date", baseURL, org, project, url.QueryEscape(query))

	// Get cursor for pagination.
	cursor, err := s.store.GetCursor(ctx, p.Name, "sentry")
	if err != nil {
		return err
	}

	token := s.cfg.Tokens.Sentry

	const maxPages = 50
	var lastCursor string
	nextURL := baseAPIURL
	if cursor != "" {
		nextURL += "&cursor=" + cursor
	}

	for page := range maxPages {
		currentURL := nextURL

		resp, err := httputil.Do(ctx, func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, "GET", currentURL, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+token)
			return req, nil
		}, httputil.DefaultRetryConfig())
		if err != nil {
			return fmt.Errorf("fetch sentry issues: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			return fmt.Errorf("sentry API %d: %s", resp.StatusCode, string(body))
		}

		var issues []sentryIssue
		if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
			resp.Body.Close()
			return fmt.Errorf("decode sentry issues: %w", err)
		}

		linkHeader := resp.Header.Get("Link")
		resp.Body.Close()

		slog.Debug("sync: sentry issues fetched", "project", p.Name, "page", page+1, "count", len(issues))

		if len(issues) == 0 {
			break
		}

		for _, issue := range issues {
			body := fmt.Sprintf("Sentry Issue: %s\n\nCulprit: %s\nCount: %d\nFirst Seen: %s\nLast Seen: %s\n\nPermalink: %s",
				issue.Title, issue.Culprit, issue.Count, issue.FirstSeen, issue.LastSeen, issue.Permalink)

			ffid, err := s.store.UpsertIssue(ctx, db.IssueUpsert{
				ProjectName:   p.Name,
				Source:        "sentry",
				SourceIssueID: issue.ID,
				Title:         issue.Title,
				Body:          body,
				URL:           issue.Permalink,
				State:         "open",
				SourceUpdated: issue.LastSeen,
			})
			if err != nil {
				slog.Error("sync: upsert sentry issue", "id", issue.ID, "err", err)
				continue
			}

			s.createJobIfNeeded(ctx, ffid, p.Name)
		}

		nextCursor := parseSentryNextCursor(linkHeader)
		if nextCursor == "" {
			break
		}
		lastCursor = nextCursor
		nextURL = baseAPIURL + "&cursor=" + nextCursor
	}

	// Save final cursor after full loop completes.
	if lastCursor != "" {
		if err := s.store.SetCursor(ctx, p.Name, "sentry", lastCursor); err != nil {
			slog.Error("sync: set sentry cursor", "err", err)
		}
	}

	return nil
}

// sentryIssueQuery builds the Sentry search query. When assignedTeam is set,
// only issues assigned to that team are returned.
func sentryIssueQuery(assignedTeam string) string {
	query := "is:unresolved"
	if team := strings.TrimSpace(assignedTeam); team != "" {
		query += " assigned:#" + team
	}
	return query
}

type sentryIssue struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Culprit   string `json:"culprit"`
	Permalink string `json:"permalink"`
	Count     int    `json:"count,string"`
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
}

// parseSentryNextCursor extracts the next cursor from Sentry's Link header.
func parseSentryNextCursor(link string) string {
	// Sentry Link header format:
	// <url>; rel="previous"; results="false"; cursor="...", <url>; rel="next"; results="true"; cursor="..."
	for _, part := range splitLink(link) {
		if strings.Contains(part, `rel="next"`) && strings.Contains(part, `results="true"`) {
			return extractCursor(part)
		}
	}
	return ""
}

func splitLink(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '>' && i+1 < len(s) && s[i+1] == ',' {
			parts = append(parts, s[start:i+1])
			start = i + 2
			for start < len(s) && s[start] == ' ' {
				start++
			}
			i = start - 1
		}
	}
	if start < len(s) {
		parts = append(parts, s[start:])
	}
	return parts
}

func extractCursor(s string) string {
	_, after, ok := strings.Cut(s, `cursor="`)
	if !ok {
		return ""
	}
	before, _, ok := strings.Cut(after, `"`)
	if !ok {
		return after
	}
	return before
}
