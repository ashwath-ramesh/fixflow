package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompare(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		current        string
		latest         string
		wantCurrent    string
		wantLatest     string
		wantAvailable  bool
		wantComparable bool
	}{
		{
			name:           "newer exists",
			current:        "0.2.0",
			latest:         "v0.3.0",
			wantCurrent:    "v0.2.0",
			wantLatest:     "v0.3.0",
			wantAvailable:  true,
			wantComparable: true,
		},
		{
			name:           "already latest",
			current:        "v0.3.0",
			latest:         "0.3.0",
			wantCurrent:    "v0.3.0",
			wantLatest:     "v0.3.0",
			wantAvailable:  false,
			wantComparable: true,
		},
		{
			name:           "current non-semver",
			current:        "dev",
			latest:         "v0.3.0",
			wantCurrent:    "dev",
			wantLatest:     "v0.3.0",
			wantAvailable:  true,
			wantComparable: false,
		},
		{
			name:           "latest non-semver",
			current:        "v0.3.0",
			latest:         "nightly",
			wantCurrent:    "v0.3.0",
			wantLatest:     "nightly",
			wantAvailable:  false,
			wantComparable: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Compare(tc.current, tc.latest)
			if got.CurrentVersion != tc.wantCurrent {
				t.Fatalf("current version: want %q, got %q", tc.wantCurrent, got.CurrentVersion)
			}
			if got.LatestVersion != tc.wantLatest {
				t.Fatalf("latest version: want %q, got %q", tc.wantLatest, got.LatestVersion)
			}
			if got.UpdateAvailable != tc.wantAvailable {
				t.Fatalf("update available: want %v, got %v", tc.wantAvailable, got.UpdateAvailable)
			}
			if got.Comparable != tc.wantComparable {
				t.Fatalf("comparable: want %v, got %v", tc.wantComparable, got.Comparable)
			}
		})
	}
}

func TestSelectAssetURL(t *testing.T) {
	t.Parallel()

	assets := []githubAsset{
		{Name: "ap_0.4.0_darwin_arm64.tar.gz", URL: "https://example.com/ap_arm64.tar.gz"},
		{Name: "ap_0.4.0_darwin_amd64.tar.gz", URL: "https://example.com/ap_amd64.tar.gz"},
	}

	url, err := selectAssetURL(assets, "v0.4.0", "darwin", "arm64")
	if err != nil {
		t.Fatalf("select asset: %v", err)
	}
	if url != "https://example.com/ap_arm64.tar.gz" {
		t.Fatalf("unexpected URL: %q", url)
	}

	if _, err := selectAssetURL(assets, "v0.4.0", "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "unsupported OS") {
		t.Fatalf("expected unsupported OS error, got %v", err)
	}
	if _, err := selectAssetURL(assets, "v0.4.0", "darwin", "386"); err == nil || !strings.Contains(err.Error(), "unsupported architecture") {
		t.Fatalf("expected unsupported arch error, got %v", err)
	}
}

func TestFetchLatestRelease(t *testing.T) {
	t.Parallel()

	client := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/repos/ashwath-ramesh/autopr/releases/latest" {
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
			if got := r.Header.Get("User-Agent"); got != "autopr/test" {
				t.Fatalf("unexpected user-agent: %q", got)
			}
			return jsonResponse(http.StatusOK, `{"tag_name":"v0.9.1","assets":[{"name":"ap_0.9.1_darwin_arm64.tar.gz","browser_download_url":"https://example.com/asset"}]}`), nil
		}),
	}

	mgr := &Manager{
		Client:     client,
		Now:        time.Now,
		OS:         "darwin",
		Arch:       "arm64",
		ReleaseAPI: "https://api.github.com/repos/ashwath-ramesh/autopr/releases/latest",
		UserAgent:  "autopr/test",
	}

	rel, err := mgr.fetchLatestRelease(context.Background())
	if err != nil {
		t.Fatalf("fetch latest release: %v", err)
	}
	if rel.TagName != "v0.9.1" {
		t.Fatalf("expected tag v0.9.1, got %q", rel.TagName)
	}
	if len(rel.Assets) != 1 {
		t.Fatalf("expected 1 asset, got %d", len(rel.Assets))
	}
}

func TestCacheReadWriteAndFreshness(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	mgr := &Manager{
		Now:       func() time.Time { return now },
		StatePath: filepath.Join(tmp, "version-check.json"),
	}

	if _, err := mgr.ReadCache(); err == nil {
		t.Fatal("expected missing cache error")
	}

	entry := VersionCheckCache{CheckedAt: now.Add(-1 * time.Hour), LatestTag: "v1.2.3"}
	if err := mgr.WriteCache(entry); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	got, err := mgr.ReadCache()
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if got.LatestTag != entry.LatestTag || !got.CheckedAt.Equal(entry.CheckedAt) {
		t.Fatalf("unexpected cache entry: %#v", got)
	}
	if !mgr.IsCacheFresh(got, 24*time.Hour) {
		t.Fatal("expected fresh cache")
	}
	if mgr.IsCacheFresh(VersionCheckCache{CheckedAt: now.Add(-25 * time.Hour), LatestTag: "v1.2.3"}, 24*time.Hour) {
		t.Fatal("expected stale cache")
	}

	if err := os.WriteFile(mgr.StatePath, []byte("{"), 0o644); err != nil {
		t.Fatalf("write corrupt cache: %v", err)
	}
	if _, err := mgr.ReadCache(); err == nil {
		t.Fatal("expected corrupt cache error")
	}
}

func TestUpgradeDownloadsAndReplacesBinary(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	exePath := filepath.Join(tmp, "ap")
	if err := os.WriteFile(exePath, []byte("old-binary"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}

	archive := mustMakeTarGz(t, "ap", []byte("new-binary"), 0o755)

	assetHits := 0
	client := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.String() {
			case "https://api.github.com/repos/ashwath-ramesh/autopr/releases/latest":
				return jsonResponse(http.StatusOK, `{"tag_name":"v0.2.0","assets":[{"name":"ap_0.2.0_darwin_arm64.tar.gz","browser_download_url":"https://example.com/asset/ap_0.2.0_darwin_arm64.tar.gz"}]}`), nil
			case "https://example.com/asset/ap_0.2.0_darwin_arm64.tar.gz":
				assetHits++
				return binaryResponse(http.StatusOK, archive), nil
			default:
				return textResponse(http.StatusNotFound, "not found"), nil
			}
		}),
	}

	mgr := &Manager{
		Client:     client,
		Now:        time.Now,
		OS:         "darwin",
		Arch:       "arm64",
		ReleaseAPI: "https://api.github.com/repos/ashwath-ramesh/autopr/releases/latest",
		UserAgent:  "autopr/test",
		ExecutablePath: func() (string, error) {
			return exePath, nil
		},
	}

	res, err := mgr.Upgrade(context.Background(), "0.1.0")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if !res.UpdateAvailable || !res.Upgraded {
		t.Fatalf("expected upgraded result, got %#v", res)
	}
	if assetHits != 1 {
		t.Fatalf("expected one asset download, got %d", assetHits)
	}
	got, err := os.ReadFile(exePath)
	if err != nil {
		t.Fatalf("read executable: %v", err)
	}
	if string(got) != "new-binary" {
		t.Fatalf("unexpected executable content: %q", string(got))
	}
}

func TestUpgradeSkipsWhenAlreadyLatest(t *testing.T) {
	t.Parallel()

	assetHits := 0
	client := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != "https://api.github.com/repos/ashwath-ramesh/autopr/releases/latest" {
				assetHits++
				return textResponse(http.StatusNotFound, "not found"), nil
			}
			return jsonResponse(http.StatusOK, `{"tag_name":"v0.2.0","assets":[]}`), nil
		}),
	}

	mgr := &Manager{
		Client:     client,
		Now:        time.Now,
		OS:         "darwin",
		Arch:       "arm64",
		ReleaseAPI: "https://api.github.com/repos/ashwath-ramesh/autopr/releases/latest",
		UserAgent:  "autopr/test",
		ExecutablePath: func() (string, error) {
			return "/tmp/ap", nil
		},
	}

	res, err := mgr.Upgrade(context.Background(), "v0.2.0")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if res.UpdateAvailable || res.Upgraded {
		t.Fatalf("expected no upgrade, got %#v", res)
	}
	if assetHits != 0 {
		t.Fatalf("expected no asset downloads, got %d", assetHits)
	}
}

func mustMakeTarGz(t *testing.T, fileName string, content []byte, mode int64) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name:     fileName,
		Mode:     mode,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write tar file content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return buf.Bytes()
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(status int, payload string) *http.Response {
	resp := textResponse(status, payload)
	resp.Header.Set("Content-Type", "application/json")
	return resp
}

func binaryResponse(status int, payload []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:       io.NopCloser(bytes.NewReader(payload)),
		Header:     make(http.Header),
	}
}

func textResponse(status int, payload string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:       io.NopCloser(strings.NewReader(payload)),
		Header:     make(http.Header),
	}
}
