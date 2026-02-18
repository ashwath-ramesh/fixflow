package git

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"autopr/internal/httputil"
)

// CreateGitHubPR creates a pull request on GitHub and returns its HTML URL.
func CreateGitHubPR(ctx context.Context, token, owner, repo, head, base, title, body string, draft bool) (string, error) {
	payload := map[string]any{
		"title": title,
		"head":  head,
		"base":  base,
		"body":  body,
		"draft": draft,
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal PR payload: %w", err)
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls", owner, repo)

	resp, err := httputil.Do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	}, httputil.DefaultRetryConfig())
	if err != nil {
		return "", fmt.Errorf("github create PR: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusUnprocessableEntity {
		// PR may already exist for this branch — try to find it.
		if existingURL, err := findGitHubPR(ctx, token, owner, repo, head); err == nil && existingURL != "" {
			return existingURL, nil
		}
		msg := string(respBody)
		if len(msg) > 4096 {
			msg = msg[:4096]
		}
		return "", fmt.Errorf("github create PR: HTTP %d: %s", resp.StatusCode, msg)
	}

	if resp.StatusCode != http.StatusCreated {
		msg := string(respBody)
		if len(msg) > 4096 {
			msg = msg[:4096]
		}
		return "", fmt.Errorf("github create PR: HTTP %d: %s", resp.StatusCode, msg)
	}

	var result struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("decode PR response: %w", err)
	}
	return result.HTMLURL, nil
}

// findGitHubPR looks up an existing open PR for the given head branch.
func findGitHubPR(ctx context.Context, token, owner, repo, head string) (string, error) {
	return FindGitHubPRByBranch(ctx, token, owner, repo, head, "open")
}

// FindGitHubPRByBranch looks up an existing PR for the given head branch.
// state should be "open" or "all"; defaults to "open".
func FindGitHubPRByBranch(ctx context.Context, token, owner, repo, head, state string) (string, error) {
	if state == "" {
		state = "open"
	}
	headRef := fmt.Sprintf("%s:%s", owner, head)
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls?head=%s&state=%s",
		owner, repo, url.QueryEscape(headRef), url.QueryEscape(state))

	resp, err := httputil.Do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		return req, nil
	}, httputil.DefaultRetryConfig())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("list PRs: HTTP %d", resp.StatusCode)
	}

	var prs []struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(body, &prs); err != nil {
		return "", err
	}
	if len(prs) > 0 {
		return prs[0].HTMLURL, nil
	}
	return "", nil
}

// CreateGitLabMR creates a merge request on GitLab and returns its web URL.
func CreateGitLabMR(ctx context.Context, token, baseURL, projectID, sourceBranch, targetBranch, title, description string) (string, error) {
	baseURL = normalizeGitLabBaseURL(baseURL)

	payload := map[string]any{
		"source_branch": sourceBranch,
		"target_branch": targetBranch,
		"title":         title,
		"description":   description,
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal MR payload: %w", err)
	}

	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests", baseURL, projectID)

	resp, err := httputil.Do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		req.Header.Set("PRIVATE-TOKEN", token)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	}, httputil.DefaultRetryConfig())
	if err != nil {
		return "", fmt.Errorf("gitlab create MR: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// 409 Conflict — MR may already exist for this source branch.
	if resp.StatusCode == http.StatusConflict {
		if existing, err := findGitLabMR(ctx, token, baseURL, projectID, sourceBranch); err == nil && existing != "" {
			return existing, nil
		}
		msg := string(respBody)
		if len(msg) > 4096 {
			msg = msg[:4096]
		}
		return "", fmt.Errorf("gitlab create MR: HTTP %d: %s", resp.StatusCode, msg)
	}

	if resp.StatusCode != http.StatusCreated {
		msg := string(respBody)
		if len(msg) > 4096 {
			msg = msg[:4096]
		}
		return "", fmt.Errorf("gitlab create MR: HTTP %d: %s", resp.StatusCode, msg)
	}

	var result struct {
		WebURL string `json:"web_url"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("decode MR response: %w", err)
	}
	return result.WebURL, nil
}

// findGitLabMR looks up an existing open MR for the given source branch.
func findGitLabMR(ctx context.Context, token, baseURL, projectID, sourceBranch string) (string, error) {
	return FindGitLabMRByBranch(ctx, token, baseURL, projectID, sourceBranch, "opened")
}

// FindGitLabMRByBranch looks up an existing MR for the given source branch.
// state should be "opened" (or "open") or "all"; defaults to "opened".
func FindGitLabMRByBranch(ctx context.Context, token, baseURL, projectID, sourceBranch, state string) (string, error) {
	baseURL = normalizeGitLabBaseURL(baseURL)
	if state == "" {
		state = "opened"
	}
	if state == "open" {
		state = "opened"
	}
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests?source_branch=%s&state=%s",
		baseURL, projectID, url.QueryEscape(sourceBranch), url.QueryEscape(state))

	resp, err := httputil.Do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("PRIVATE-TOKEN", token)
		return req, nil
	}, httputil.DefaultRetryConfig())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("list MRs: HTTP %d", resp.StatusCode)
	}

	var mrs []struct {
		WebURL string `json:"web_url"`
	}
	if err := json.Unmarshal(body, &mrs); err != nil {
		return "", err
	}
	if len(mrs) > 0 {
		return mrs[0].WebURL, nil
	}
	return "", nil
}

// PRMergeStatus holds the result of a PR/MR status check.
type PRMergeStatus struct {
	Merged   bool
	MergedAt string // ISO 8601 timestamp, empty if not merged
	Closed   bool
	ClosedAt string // ISO 8601 timestamp, empty if not closed
}

var githubPRNumberRe = regexp.MustCompile(`/pull/(\d+)`)

// CheckGitHubPRStatus checks whether a GitHub PR has been merged or closed.
// prURL should be like "https://github.com/owner/repo/pull/123".
func CheckGitHubPRStatus(ctx context.Context, token, prURL string) (PRMergeStatus, error) {
	matches := githubPRNumberRe.FindStringSubmatch(prURL)
	if len(matches) < 2 {
		return PRMergeStatus{}, fmt.Errorf("cannot parse PR number from URL: %s", prURL)
	}
	prNumber := matches[1]

	// Extract owner/repo from URL.
	// URL format: https://github.com/{owner}/{repo}/pull/{number}
	parts := strings.Split(strings.TrimPrefix(prURL, "https://github.com/"), "/")
	if len(parts) < 3 {
		return PRMergeStatus{}, fmt.Errorf("cannot parse owner/repo from URL: %s", prURL)
	}
	owner, repo := parts[0], parts[1]

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%s", owner, repo, prNumber)

	resp, err := httputil.Do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		return req, nil
	}, httputil.DefaultRetryConfig())
	if err != nil {
		return PRMergeStatus{}, fmt.Errorf("check PR status: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return PRMergeStatus{}, fmt.Errorf("check PR status: HTTP %d", resp.StatusCode)
	}

	var pr struct {
		State    string `json:"state"`
		Merged   bool   `json:"merged"`
		MergedAt string `json:"merged_at"`
		ClosedAt string `json:"closed_at"`
	}
	if err := json.Unmarshal(body, &pr); err != nil {
		return PRMergeStatus{}, fmt.Errorf("decode PR status: %w", err)
	}
	status := PRMergeStatus{Merged: pr.Merged, MergedAt: pr.MergedAt}
	// GitHub: state "closed" + merged false = closed without merge.
	if pr.State == "closed" && !pr.Merged {
		status.Closed = true
		status.ClosedAt = pr.ClosedAt
	}
	return status, nil
}

var gitlabMRNumberRe = regexp.MustCompile(`/merge_requests/(\d+)`)

// CheckGitLabMRStatus checks whether a GitLab MR has been merged or closed.
// mrURL should be like "https://gitlab.com/org/repo/-/merge_requests/123".
func CheckGitLabMRStatus(ctx context.Context, token, baseURL, mrURL string) (PRMergeStatus, error) {
	baseURL = normalizeGitLabBaseURL(baseURL)

	matches := gitlabMRNumberRe.FindStringSubmatch(mrURL)
	if len(matches) < 2 {
		return PRMergeStatus{}, fmt.Errorf("cannot parse MR number from URL: %s", mrURL)
	}
	mrNumber := matches[1]

	// Extract project path from URL.
	// URL format: https://gitlab.com/{group}/{project}/-/merge_requests/{number}
	trimmed := strings.TrimPrefix(mrURL, baseURL+"/")
	mrIdx := strings.Index(trimmed, "/-/merge_requests/")
	if mrIdx < 0 {
		return PRMergeStatus{}, fmt.Errorf("cannot parse project path from URL: %s", mrURL)
	}
	projectPath := strings.ReplaceAll(trimmed[:mrIdx], "/", "%2F")

	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s", baseURL, projectPath, mrNumber)

	resp, err := httputil.Do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("PRIVATE-TOKEN", token)
		return req, nil
	}, httputil.DefaultRetryConfig())
	if err != nil {
		return PRMergeStatus{}, fmt.Errorf("check MR status: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return PRMergeStatus{}, fmt.Errorf("check MR status: HTTP %d", resp.StatusCode)
	}

	var mr struct {
		State    string `json:"state"`
		MergedAt string `json:"merged_at"`
		ClosedAt string `json:"closed_at"`
	}
	if err := json.Unmarshal(body, &mr); err != nil {
		return PRMergeStatus{}, fmt.Errorf("decode MR status: %w", err)
	}
	status := PRMergeStatus{Merged: mr.State == "merged", MergedAt: mr.MergedAt}
	// GitLab: "closed" is distinct from "merged".
	if mr.State == "closed" {
		status.Closed = true
		status.ClosedAt = mr.ClosedAt
	}
	return status, nil
}

func normalizeGitLabBaseURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "https://gitlab.com"
	}
	return strings.TrimRight(baseURL, "/")
}
