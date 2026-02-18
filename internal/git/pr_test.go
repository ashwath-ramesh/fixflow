package git

import (
	"context"
	"encoding/json"
	"fmt"
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
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeGitLabBaseURL(tc.in); got != tc.want {
				t.Fatalf("normalizeGitLabBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
