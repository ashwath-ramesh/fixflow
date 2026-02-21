package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"autopr/internal/config"
)

const (
	latestReleaseURL = "https://api.github.com/repos/ashwath-ramesh/autopr/releases/latest"
	binaryName       = "ap"

	defaultReleaseRequestTimeout = 4 * time.Second
	defaultAssetDownloadTimeout  = 2 * time.Minute

	DefaultCheckTTL = 24 * time.Hour
)

type VersionCheckCache struct {
	CheckedAt time.Time `json:"checked_at"`
	LatestTag string    `json:"latest_tag"`
}

type CheckResult struct {
	CurrentVersion  string
	LatestVersion   string
	UpdateAvailable bool
	Comparable      bool
}

type UpgradeResult struct {
	CheckResult
	Upgraded bool
}

type Manager struct {
	Client         *http.Client
	Now            func() time.Time
	OS             string
	Arch           string
	ReleaseAPI     string
	UserAgent      string
	ExecutablePath func() (string, error)
	StatePath      string
}

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type semVersion struct {
	major int
	minor int
	patch int
}

func NewManager(currentVersion string) *Manager {
	statePath, err := config.VersionCheckPath()
	if err != nil {
		statePath = ""
	}
	return &Manager{
		Client: &http.Client{},
		Now:    time.Now,
		OS:     runtime.GOOS,
		Arch:   runtime.GOARCH,

		ReleaseAPI: latestReleaseURL,
		UserAgent:  fmt.Sprintf("autopr/%s", currentVersion),
		StatePath:  statePath,

		ExecutablePath: os.Executable,
	}
}

func Compare(currentVersion, latestVersion string) CheckResult {
	current := canonicalVersion(currentVersion)
	latest := canonicalVersion(latestVersion)

	currSem, currOK := parseSemver(currentVersion)
	latestSem, latestOK := parseSemver(latestVersion)
	if !latestOK {
		return CheckResult{
			CurrentVersion: current,
			LatestVersion:  latest,
			Comparable:     false,
		}
	}
	if !currOK {
		return CheckResult{
			CurrentVersion:  current,
			LatestVersion:   latest,
			UpdateAvailable: true,
			Comparable:      false,
		}
	}

	cmp := compareSemver(currSem, latestSem)
	return CheckResult{
		CurrentVersion:  current,
		LatestVersion:   latest,
		UpdateAvailable: cmp < 0,
		Comparable:      true,
	}
}

func (m *Manager) Check(ctx context.Context, currentVersion string) (CheckResult, error) {
	release, err := m.fetchLatestRelease(ctx)
	if err != nil {
		return CheckResult{}, err
	}
	return Compare(currentVersion, release.TagName), nil
}

func (m *Manager) Upgrade(ctx context.Context, currentVersion string) (UpgradeResult, error) {
	release, err := m.fetchLatestRelease(ctx)
	if err != nil {
		return UpgradeResult{}, err
	}

	comparison := Compare(currentVersion, release.TagName)
	result := UpgradeResult{CheckResult: comparison}
	if !comparison.UpdateAvailable {
		return result, nil
	}

	asset, err := selectAsset(release.Assets, release.TagName, m.OS, m.Arch)
	if err != nil {
		return UpgradeResult{}, err
	}
	checksumURL, err := selectChecksumsURL(release.Assets)
	if err != nil {
		return UpgradeResult{}, err
	}
	expectedChecksum, err := m.fetchExpectedChecksum(ctx, checksumURL, asset.Name)
	if err != nil {
		return UpgradeResult{}, err
	}

	payload, mode, err := m.downloadAndExtractBinary(ctx, asset.URL, expectedChecksum)
	if err != nil {
		return UpgradeResult{}, err
	}
	if err := m.replaceCurrentBinary(payload, mode); err != nil {
		return UpgradeResult{}, err
	}

	result.Upgraded = true
	return result, nil
}

func (m *Manager) RefreshCache(ctx context.Context) (VersionCheckCache, error) {
	release, err := m.fetchLatestRelease(ctx)
	if err != nil {
		return VersionCheckCache{}, err
	}
	entry := VersionCheckCache{CheckedAt: m.Now(), LatestTag: release.TagName}
	if err := m.WriteCache(entry); err != nil {
		return VersionCheckCache{}, err
	}
	return entry, nil
}

// MarkCheckAttempt records that a version check was attempted, even if the
// network call failed. This throttles future checks by the regular cache TTL.
func (m *Manager) MarkCheckAttempt(latestTag string) error {
	latestTag = canonicalVersion(latestTag)
	if latestTag == "" {
		latestTag = "unknown"
	}
	return m.WriteCache(VersionCheckCache{
		CheckedAt: m.Now(),
		LatestTag: latestTag,
	})
}

func (m *Manager) ReadCache() (VersionCheckCache, error) {
	if m.StatePath == "" {
		return VersionCheckCache{}, errors.New("version-check cache path is not configured")
	}
	buf, err := os.ReadFile(m.StatePath)
	if err != nil {
		return VersionCheckCache{}, err
	}
	var entry VersionCheckCache
	if err := json.Unmarshal(buf, &entry); err != nil {
		return VersionCheckCache{}, fmt.Errorf("decode version-check cache: %w", err)
	}
	if entry.CheckedAt.IsZero() || strings.TrimSpace(entry.LatestTag) == "" {
		return VersionCheckCache{}, errors.New("version-check cache is invalid")
	}
	return entry, nil
}

func (m *Manager) WriteCache(entry VersionCheckCache) error {
	if m.StatePath == "" {
		return errors.New("version-check cache path is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(m.StatePath), 0o755); err != nil {
		return fmt.Errorf("create version-check state dir: %w", err)
	}

	buf, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode version-check cache: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.StatePath), ".version-check-*")
	if err != nil {
		return fmt.Errorf("create version-check temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(buf); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write version-check cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close version-check cache: %w", err)
	}
	if err := os.Rename(tmpPath, m.StatePath); err != nil {
		return fmt.Errorf("replace version-check cache: %w", err)
	}
	cleanup = false
	return nil
}

func (m *Manager) IsCacheFresh(entry VersionCheckCache, ttl time.Duration) bool {
	if entry.CheckedAt.IsZero() || ttl <= 0 {
		return false
	}
	return m.Now().Sub(entry.CheckedAt) <= ttl
}

func (m *Manager) fetchLatestRelease(ctx context.Context) (githubRelease, error) {
	ctx, cancel := withTimeout(ctx, defaultReleaseRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.ReleaseAPI, nil)
	if err != nil {
		return githubRelease{}, fmt.Errorf("build release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if m.UserAgent != "" {
		req.Header.Set("User-Agent", m.UserAgent)
	}
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := m.httpClient().Do(req)
	if err != nil {
		return githubRelease{}, fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = "unknown response"
		}
		return githubRelease{}, fmt.Errorf("fetch latest release: status %d: %s", resp.StatusCode, msg)
	}

	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return githubRelease{}, fmt.Errorf("decode latest release response: %w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return githubRelease{}, errors.New("latest release missing tag_name")
	}
	return rel, nil
}

func selectAsset(assets []githubAsset, tag, osName, arch string) (githubAsset, error) {
	if osName != "darwin" && osName != "linux" {
		return githubAsset{}, fmt.Errorf("unsupported OS %q (supported: darwin, linux)", osName)
	}
	if arch != "amd64" && arch != "arm64" {
		return githubAsset{}, fmt.Errorf("unsupported architecture %q (supported: amd64, arm64)", arch)
	}

	expected := expectedAssetNames(tag, osName, arch)
	for _, name := range expected {
		for _, asset := range assets {
			if asset.Name == name && asset.URL != "" {
				return asset, nil
			}
		}
	}

	suffix := fmt.Sprintf("_%s_%s.tar.gz", osName, arch)
	for _, asset := range assets {
		if strings.HasPrefix(asset.Name, "ap_") && strings.HasSuffix(asset.Name, suffix) && asset.URL != "" {
			return asset, nil
		}
	}
	return githubAsset{}, fmt.Errorf("no release asset found for %s/%s", osName, arch)
}

func selectChecksumsURL(assets []githubAsset) (string, error) {
	for _, asset := range assets {
		if asset.Name == "checksums.txt" && asset.URL != "" {
			return asset.URL, nil
		}
	}
	return "", errors.New("release is missing checksums.txt asset")
}

func (m *Manager) fetchExpectedChecksum(ctx context.Context, checksumURL, assetName string) (string, error) {
	ctx, cancel := withTimeout(ctx, defaultReleaseRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumURL, nil)
	if err != nil {
		return "", fmt.Errorf("build checksum request: %w", err)
	}
	if m.UserAgent != "" {
		req.Header.Set("User-Agent", m.UserAgent)
	}
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := m.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("download checksums: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download checksums: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read checksums: %w", err)
	}

	return checksumForAsset(string(body), assetName)
}

func checksumForAsset(text, assetName string) (string, error) {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		sum := strings.ToLower(fields[0])
		file := strings.TrimPrefix(fields[1], "*")
		if filepath.Base(file) != assetName {
			continue
		}
		if len(sum) != sha256.Size*2 {
			return "", fmt.Errorf("invalid checksum for %q", assetName)
		}
		return sum, nil
	}
	return "", fmt.Errorf("checksum for %q not found in checksums.txt", assetName)
}

func expectedAssetNames(tag, osName, arch string) []string {
	tag = strings.TrimSpace(tag)
	withoutV := strings.TrimPrefix(tag, "v")
	withV := tag
	if withV == "" {
		withV = "v" + withoutV
	}
	if !strings.HasPrefix(withV, "v") {
		withV = "v" + withV
	}

	seen := map[string]struct{}{}
	var names []string
	for _, candidate := range []string{
		fmt.Sprintf("ap_%s_%s_%s.tar.gz", tag, osName, arch),
		fmt.Sprintf("ap_%s_%s_%s.tar.gz", withoutV, osName, arch),
		fmt.Sprintf("ap_%s_%s_%s.tar.gz", withV, osName, arch),
	} {
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		names = append(names, candidate)
	}
	return names
}

func (m *Manager) downloadAndExtractBinary(ctx context.Context, assetURL, expectedChecksum string) ([]byte, os.FileMode, error) {
	ctx, cancel := withTimeout(ctx, defaultAssetDownloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build download request: %w", err)
	}
	if m.UserAgent != "" {
		req.Header.Set("User-Agent", m.UserAgent)
	}

	resp, err := m.httpClient().Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("download release asset: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("download release asset: status %d", resp.StatusCode)
	}

	archive, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("read release asset: %w", err)
	}
	if expectedChecksum != "" {
		actual := sha256.Sum256(archive)
		actualHex := fmt.Sprintf("%x", actual)
		if !strings.EqualFold(actualHex, expectedChecksum) {
			return nil, 0, errors.New("release checksum verification failed")
		}
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, 0, fmt.Errorf("open release archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("read release archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		if filepath.Base(hdr.Name) != binaryName {
			continue
		}
		payload, err := io.ReadAll(tr)
		if err != nil {
			return nil, 0, fmt.Errorf("extract %s from archive: %w", binaryName, err)
		}
		mode := os.FileMode(hdr.Mode) & 0o777
		if mode&0o111 == 0 {
			mode = 0o755
		}
		return payload, mode, nil
	}
	return nil, 0, fmt.Errorf("release archive does not contain %q", binaryName)
}

func (m *Manager) replaceCurrentBinary(payload []byte, defaultMode os.FileMode) error {
	exe, err := m.ExecutablePath()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	mode := defaultMode
	if info, err := os.Stat(exe); err == nil {
		mode = info.Mode().Perm()
	}
	if mode&0o111 == 0 {
		mode = 0o755
	}

	tmp, err := os.CreateTemp(filepath.Dir(exe), ".ap-upgrade-*")
	if err != nil {
		return fmt.Errorf("create replacement binary: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write replacement binary: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod replacement binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close replacement binary: %w", err)
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	cleanup = false
	return nil
}

func canonicalVersion(v string) string {
	if sem, ok := parseSemver(v); ok {
		return fmt.Sprintf("v%d.%d.%d", sem.major, sem.minor, sem.patch)
	}
	return strings.TrimSpace(v)
}

func parseSemver(v string) (semVersion, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return semVersion{}, false
	}
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return semVersion{}, false
	}

	core := v
	if idx := strings.IndexAny(core, "+-"); idx >= 0 {
		core = core[:idx]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semVersion{}, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return semVersion{}, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return semVersion{}, false
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return semVersion{}, false
	}
	return semVersion{major: major, minor: minor, patch: patch}, true
}

func compareSemver(a, b semVersion) int {
	if a.major != b.major {
		if a.major < b.major {
			return -1
		}
		return 1
	}
	if a.minor != b.minor {
		if a.minor < b.minor {
			return -1
		}
		return 1
	}
	if a.patch != b.patch {
		if a.patch < b.patch {
			return -1
		}
		return 1
	}
	return 0
}

func (m *Manager) httpClient() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return &http.Client{}
}

func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	if deadline, ok := ctx.Deadline(); ok {
		if time.Until(deadline) <= timeout {
			return ctx, func() {}
		}
	}
	return context.WithTimeout(ctx, timeout)
}
