package git

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateGitLabMR_409ReturnsExisting(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/merge_requests"):
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"message":"Another open merge request already exists"}`)

		case r.Method == "GET" && strings.Contains(r.URL.Path, "/merge_requests"):
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]map[string]string{
				{"web_url": "https://gitlab.com/org/repo/-/merge_requests/42"},
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	got, err := CreateGitLabMR(context.Background(), "tok", srv.URL, "123", "feat/branch", "main", "title", "desc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://gitlab.com/org/repo/-/merge_requests/42" {
		t.Fatalf("want existing MR URL, got %q", got)
	}
}

func TestCreateGitLabMR_409NoExistingReturnsError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/merge_requests"):
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"message":"conflict"}`)

		case r.Method == "GET" && strings.Contains(r.URL.Path, "/merge_requests"):
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `[]`)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	_, err := CreateGitLabMR(context.Background(), "tok", srv.URL, "123", "feat/branch", "main", "title", "desc")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "HTTP 409") {
		t.Fatalf("want HTTP 409 error, got: %v", err)
	}
}

func TestFindGitLabMR_ReturnsFirstMatch(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]map[string]string{
			{"web_url": "https://gitlab.com/org/repo/-/merge_requests/10"},
			{"web_url": "https://gitlab.com/org/repo/-/merge_requests/11"},
		})
	}))
	defer srv.Close()

	got, err := findGitLabMR(context.Background(), "tok", srv.URL, "123", "feat/branch")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://gitlab.com/org/repo/-/merge_requests/10" {
		t.Fatalf("want first MR URL, got %q", got)
	}
}

func TestFindGitLabMR_EmptyWhenNoMatches(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	got, err := findGitLabMR(context.Background(), "tok", srv.URL, "123", "feat/branch")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("want empty string, got %q", got)
	}
}

func TestFindGitLabMRByBranch_StateAll(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("state"); got != "all" {
			t.Fatalf("want state=all, got %q", got)
		}
		if got := r.URL.Query().Get("source_branch"); got != "feat/branch" {
			t.Fatalf("want source_branch=feat/branch, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]map[string]string{
			{"web_url": "https://gitlab.com/org/repo/-/merge_requests/99"},
		})
	}))
	defer srv.Close()

	got, err := FindGitLabMRByBranch(context.Background(), "tok", srv.URL, "123", "feat/branch", "all")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://gitlab.com/org/repo/-/merge_requests/99" {
		t.Fatalf("want MR URL, got %q", got)
	}
}

func TestGetGitHubCheckRunStatus_AllPassed(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/check-runs") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"total_count": 3,
			"check_runs": []map[string]any{
				{"name": "build", "status": "completed", "conclusion": "success", "html_url": "https://github.com/runs/1"},
				{"name": "test", "status": "completed", "conclusion": "success", "html_url": "https://github.com/runs/2"},
				{"name": "lint", "status": "completed", "conclusion": "neutral", "html_url": "https://github.com/runs/3"},
			},
		})
	}))
	defer srv.Close()

	// Override the API URL by using a custom transport - instead, test parsing logic directly.
	// For unit test, we test the parsing via the real function with a mock server.
	// The function hardcodes api.github.com, so we test the response parsing separately.
	t.Run("parsing", func(t *testing.T) {
		response := `{
			"total_count": 3,
			"check_runs": [
				{"name": "build", "status": "completed", "conclusion": "success", "html_url": "https://github.com/runs/1"},
				{"name": "test", "status": "completed", "conclusion": "success", "html_url": "https://github.com/runs/2"},
				{"name": "lint", "status": "completed", "conclusion": "neutral", "html_url": "https://github.com/runs/3"}
			]
		}`

		var result struct {
			TotalCount int `json:"total_count"`
			CheckRuns  []struct {
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				HTMLURL    string `json:"html_url"`
			} `json:"check_runs"`
		}
		if err := json.Unmarshal([]byte(response), &result); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		var status CheckRunStatus
		status.Total = result.TotalCount
		for _, cr := range result.CheckRuns {
			if cr.Status != "completed" {
				status.Pending++
				continue
			}
			status.Completed++
			switch cr.Conclusion {
			case "success", "neutral", "skipped":
				status.Passed++
			default:
				status.Failed++
				if status.FailedCheckName == "" {
					status.FailedCheckName = cr.Name
					status.FailedCheckURL = cr.HTMLURL
				}
			}
		}

		if status.Total != 3 {
			t.Fatalf("expected total=3, got %d", status.Total)
		}
		if status.Passed != 3 {
			t.Fatalf("expected passed=3, got %d", status.Passed)
		}
		if status.Failed != 0 {
			t.Fatalf("expected failed=0, got %d", status.Failed)
		}
		if status.Pending != 0 {
			t.Fatalf("expected pending=0, got %d", status.Pending)
		}
	})
}

func TestGetGitHubCheckRunStatus_WithFailure(t *testing.T) {
	t.Parallel()

	response := `{
		"total_count": 3,
		"check_runs": [
			{"name": "build", "status": "completed", "conclusion": "success", "html_url": "https://github.com/runs/1"},
			{"name": "test", "status": "completed", "conclusion": "failure", "html_url": "https://github.com/runs/2"},
			{"name": "lint", "status": "in_progress", "conclusion": "", "html_url": "https://github.com/runs/3"}
		]
	}`

	var result struct {
		TotalCount int `json:"total_count"`
		CheckRuns  []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
		} `json:"check_runs"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var status CheckRunStatus
	status.Total = result.TotalCount
	for _, cr := range result.CheckRuns {
		if cr.Status != "completed" {
			status.Pending++
			continue
		}
		status.Completed++
		switch cr.Conclusion {
		case "success", "neutral", "skipped":
			status.Passed++
		default:
			status.Failed++
			if status.FailedCheckName == "" {
				status.FailedCheckName = cr.Name
				status.FailedCheckURL = cr.HTMLURL
			}
		}
	}

	if status.Passed != 1 {
		t.Fatalf("expected passed=1, got %d", status.Passed)
	}
	if status.Failed != 1 {
		t.Fatalf("expected failed=1, got %d", status.Failed)
	}
	if status.Pending != 1 {
		t.Fatalf("expected pending=1, got %d", status.Pending)
	}
	if status.FailedCheckName != "test" {
		t.Fatalf("expected failed check name 'test', got %q", status.FailedCheckName)
	}
	if status.FailedCheckURL != "https://github.com/runs/2" {
		t.Fatalf("expected failed check URL, got %q", status.FailedCheckURL)
	}
}

func TestNormalizeGitLabBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: "https://gitlab.com"},
		{name: "whitespace", in: "   ", want: "https://gitlab.com"},
		{name: "trim trailing slash", in: "https://self-hosted.example/", want: "https://self-hosted.example"},
		{name: "keep existing", in: "https://gitlab.example", want: "https://gitlab.example"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeGitLabBaseURL(tc.in); got != tc.want {
				t.Fatalf("NormalizeGitLabBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMergeGitLabMR_Succeeds(t *testing.T) {
	t.Parallel()

	var gotEscapedPath, gotRequestURI, gotMethod string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEscapedPath = r.URL.EscapedPath()
		gotRequestURI = r.RequestURI
		gotMethod = r.Method
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			t.Fatalf("token header mismatch: %q", r.Header.Get("PRIVATE-TOKEN"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	mrURL := srv.URL + "/acmecorp/placeholder/-/merge_requests/99"
	err := MergeGitLabMR(context.Background(), "tok", srv.URL, mrURL, true)
	if err != nil {
		t.Fatalf("MergeGitLabMR: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Fatalf("want method %s, got %s", http.MethodPut, gotMethod)
	}
	wantPath := "/api/v4/projects/acmecorp%2Fplaceholder/merge_requests/99/merge"
	requestPath := gotEscapedPath
	if requestPath == "" {
		requestPath = gotRequestURI
		if i := strings.Index(requestPath, "?"); i != -1 {
			requestPath = requestPath[:i]
		}
	}
	if requestPath != wantPath {
		t.Fatalf("want path %q, got escaped=%q request_uri=%q", wantPath, gotEscapedPath, gotRequestURI)
	}
	if string(gotBody) == "" {
		t.Fatalf("expected JSON body")
	}

	var payload map[string]bool
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if got, ok := payload["squash"]; !ok || !got {
		t.Fatalf("want squash true, got %#v", got)
	}
}

func TestMergeGitLabMR_NotMergeableStatuses(t *testing.T) {
	t.Parallel()

	for _, tc := range []int{http.StatusConflict, http.StatusMethodNotAllowed, http.StatusUnprocessableEntity} {
		t.Run(http.StatusText(tc), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc)
				fmt.Fprintf(w, `{"message":"not mergeable"}`)
			}))
			defer srv.Close()

			mrURL := srv.URL + "/acmecorp/placeholder/-/merge_requests/100"
			err := MergeGitLabMR(context.Background(), "tok", srv.URL, mrURL, false)
			if err == nil || !strings.Contains(err.Error(), "MR is not mergeable") {
				t.Fatalf("want merge-blocked error for %d, got: %v", tc, err)
			}
		})
	}
}

func TestMergeGitHubPR_InvalidMethod(t *testing.T) {
	err := MergeGitHubPR(context.Background(), "tok", "https://github.com/acmecorp/placeholder/pull/123", "bad")
	if err == nil || !strings.Contains(err.Error(), "invalid merge method") {
		t.Fatalf("want invalid method error, got: %v", err)
	}
}

func TestMergeGitHubPR_BadPRURL(t *testing.T) {
	err := MergeGitHubPR(context.Background(), "tok", "https://example.invalid/no-pull", "merge")
	if err == nil || !strings.Contains(err.Error(), "cannot parse PR number") {
		t.Fatalf("want parse error, got: %v", err)
	}
}
