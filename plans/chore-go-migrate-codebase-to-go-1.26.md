# chore(go): Migrate Codebase to Go 1.26

> GitHub Issue: #55
> Branch: `chore/migrate-go-1.26`
> Type: chore
> Risk: Low
> Deepened: 2026-02-19

## Enhancement Summary

**Research agents used:** performance-oracle, security-sentinel, deployment-verification-agent, code-simplicity-reviewer, architecture-strategist, pattern-recognition-specialist, best-practices-researcher (CGo cross-compilation)

### Key Improvements from Research
1. Precise `go fix` targets identified: 9 code locations across 7 files (no guessing)
2. CGo cross-compilation confirmed working on arm64 macOS runners — risk downgraded
3. Race detector (`-race`) added to CI workflow per architecture review
4. GoReleaser snapshot verification moved earlier (after go.mod bump, before go fix)
5. `CGO_ENABLED=1` should be added to `.goreleaser.yaml` explicitly
6. Post-quantum TLS (ML-KEM) escape hatch documented for corporate proxy compatibility
7. GODEBUG TLS deprecation timeline for Go 1.27 captured as advance notice

### Simplification Applied
- Removed speculative modernizer list — replaced with exact findings from codebase scan
- Corrected `url.Parse` audit scope from 4 files to 2 (gitlab.go and pr.go don't call `url.Parse`)
- Merged GoReleaser verification into Phase 2 as a sub-step
- Collapsed plan from 6 phases to 4 phases

---

## Overview

Migrate autopr from Go 1.24.0 to Go 1.26.0 (stable, released February 2026) with staged verification. The migration is low-risk due to Go's backward-compatibility promise, but requires careful attention to CGo (go-sqlite3), CI infrastructure gaps, and GODEBUG behavior changes.

## Problem Statement / Motivation

- Go 1.24.0 is two minor versions behind the latest stable release.
- Go 1.26 brings performance improvements directly relevant to autopr: 30% faster CGo calls (benefits SQLite — the highest-impact improvement given ~50 SQL call sites crossing the CGo boundary), Green Tea GC (benefits the long-running daemon), and 2x faster `io.ReadAll`.
- The rewritten `go fix` tool with modernizers can clean up code idioms automatically.
- Staying current reduces the delta for future upgrades and keeps the project within the Go team's supported version window.

## Current State

| Item | Current | Target |
|------|---------|--------|
| `go.mod` go directive | `go 1.24.0` | `go 1.26.0` |
| `toolchain` directive | absent | `toolchain go1.26.0` |
| README version requirement | `Go 1.23+` (line 38, already inconsistent) | `Go 1.26+` |
| Test CI workflow | **does not exist** | New `test.yml` running on PR/push |
| Release workflow Go setup | `actions/setup-go@v5` | `actions/setup-go@v5` (no change needed) |
| `.goreleaser.yaml` CGO_ENABLED | implicit | explicit `CGO_ENABLED=1` in env block |

---

## Technical Approach

### Phase 1: Create Test CI Workflow (Foundation)

**Why first:** There is no CI workflow that runs tests today. Creating this on the current Go 1.24.0 baseline gives us a green reference before any migration changes.

**File to create:** `.github/workflows/test.yml`

```yaml
name: Test

on:
  push:
    branches: [master]
  pull_request:
    branches: [master]

env:
  CGO_ENABLED: "1"

jobs:
  test:
    runs-on: macos-14
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: go build ./...
      - run: go vet ./...
      - run: go test -race ./...
```

**Key decisions:**
- `macos-14` pinned (not `macos-latest`): reproducible builds, prevents unexpected runner image changes. CGo + go-sqlite3 needs a C compiler, and `//go:build darwin` tests in `internal/notify/` must run on macOS.
- `CGO_ENABLED: "1"` explicit: documents the requirement rather than relying on implicit defaults
- `-race` flag added: the daemon runs concurrent goroutines (webhook server, sync loop, notification dispatcher, worker pool) sharing `db.Store`. Green Tea GC changes scheduling, which could expose latent data races. Race detector catches these.
- Single OS, no matrix: project only targets darwin, no need for ubuntu/windows

**Files affected:**
- `.github/workflows/test.yml` (new)

> **Commit this phase separately** so the CI baseline is established independently of the version bump.

---

### Phase 2: Bump go.mod, Verify Build, and Validate Release Pipeline

**Strict execution order:**

1. Update `go.mod` directive:
   ```bash
   go mod edit -go=1.26.0
   ```
2. Tidy the module graph:
   ```bash
   go mod tidy
   ```
3. **Verify lipgloss pin unchanged:**
   ```bash
   grep lipgloss go.mod
   # MUST still show: v1.1.1-0.20250404203927-76690c660834
   ```
4. Verify compilation:
   ```bash
   go build ./...
   ```
5. Run vet:
   ```bash
   go vet ./...
   ```
6. Run tests:
   ```bash
   go test -race ./...
   ```
7. **Validate GoReleaser snapshot** (moved here from original Phase 6 — catch CGo cross-compilation issues early):
   ```bash
   goreleaser build --snapshot --clean
   ```
8. Verify both binaries:
   ```bash
   ./dist/ap_darwin_arm64_v8.0/ap version
   ./dist/ap_darwin_amd64_v1/ap version
   ```

**Files affected:**
- `go.mod` (line 3: `go 1.24.0` -> `go 1.26.0`)
- `go.sum` (regenerated by `go mod tidy`)

**Also add `CGO_ENABLED=1` to `.goreleaser.yaml`:**

```yaml
builds:
  - main: ./cmd/autopr
    binary: ap
    env:
      - CGO_ENABLED=1
    ldflags:
      # ... existing ldflags ...
```

This makes the CGo requirement explicit and documented rather than relying on implicit defaults. Per research: darwin/amd64 cross-compilation from arm64 macOS works natively via Apple Clang's `-arch` flag — no custom CC or Rosetta needed.

> **Research Insight (CGo Cross-Compilation):** Since Go 1.16+, the Go toolchain automatically passes `-arch x86_64` to Apple's Clang when building `GOARCH=amd64` on an arm64 host. The GoReleaser cross-compile risk is **lower than originally assessed**. Apple's Clang is a universal cross-compiler. No additional toolchains needed for darwin-to-darwin builds.

> **Commit this phase separately** for granular revert capability.

---

### Phase 3: Apply `go fix` Modernizers

1. Preview changes:
   ```bash
   go fix -diff ./...
   ```
2. Apply changes:
   ```bash
   go fix ./...
   ```
3. Re-run tests:
   ```bash
   go test -race ./...
   ```

**Exact modernizer targets identified by codebase scan (9 locations, 7 files):**

| Category | File | Line | What Changes |
|----------|------|------|-------------|
| `rangeint` | `internal/worker/pool.go` | 35 | `for i := 0; i < p.n; i++` -> `for i := range p.n` |
| `rangeint` | `internal/db/notifications_test.go` | 222 | `for i := 0; i < 3; i++` -> `for i := range 3` |
| `stringscut` | `internal/issuesync/sentry.go` | 163 | `strings.Index` + manual offset -> `strings.Cut` |
| `stringscut` | `internal/git/pr.go` | 317 | `strings.Index` for prefix split -> `strings.Cut` |
| `minmax` | `internal/tui/model.go` | 1635 | Custom `minInt()` helper -> builtin `min()` (also remove function definition at line 1635-1640, update call at line 1094) |
| `forvar` | `internal/git/worktree_test.go` | 80 | Remove redundant `tc := tc` (dead since Go 1.22) |
| `forvar` | `internal/git/pr_test.go` | 151 | Remove redundant `tc := tc` (dead since Go 1.22) |

**Not candidates (confirmed by scan):**
- `any`: 0 instances of `interface{}` — codebase already uses `any` throughout
- `sentry.go:145` 3-clause loop: mutates `i` mid-loop body, NOT suitable for range-over-int

**Also clean up manually (not auto-fixable):**
- `internal/db/issues.go:234` — remove dead `rand.Read` error check branch
- `internal/db/jobs.go:1108` — remove dead `rand.Read` error check branch

These `crypto/rand.Read` error checks are dead code since Go 1.24 (rand.Read never returns error on supported platforms). Simplify both ID generation functions to drop the error return:
```go
// Before:
func newJobID() (string, error) {
    buf := make([]byte, 8)
    if _, err := rand.Read(buf); err != nil {
        return "", fmt.Errorf("generate job id: %w", err)
    }
    return "ap-job-" + strings.ToLower(hex.EncodeToString(buf)), nil
}

// After:
func newJobID() string {
    buf := make([]byte, 8)
    rand.Read(buf)
    return "ap-job-" + strings.ToLower(hex.EncodeToString(buf))
}
```

Update callers accordingly: `CreateJob` at `jobs.go:131` and `UpsertIssue` at `issues.go:56`.

> **Commit this phase separately** so `go fix` changes can be reverted independently.

---

### Phase 4: Update Documentation and README

**README.md** line 38:
- Change: `Go 1.23+` -> `Go 1.26+`

**PR description** should cover:
- Go version bumped from 1.24.0 to 1.26.0
- Green Tea GC now default (opt out: `GOEXPERIMENT=nogreenteagc`)
- CGo calls ~30% faster (benefits SQLite operations)
- `go fix` modernizer changes applied (list the specific changes)
- New test CI workflow added with race detector
- `CGO_ENABLED=1` made explicit in `.goreleaser.yaml`

---

## Acceptance Criteria

### Functional Requirements
- [ ] `go.mod` updated to `go 1.26.0`
- [ ] `go build ./...` succeeds on Go 1.26
- [ ] `go vet ./...` passes on Go 1.26
- [ ] `go test -race ./...` passes on Go 1.26 (all 13 test packages)
- [ ] `go fix` modernizers applied and reviewed (9 locations across 7 files)
- [ ] Dead `rand.Read` error branches removed (2 files, callers updated)
- [ ] New `.github/workflows/test.yml` created with race detector
- [ ] README updated: `Go 1.23+` -> `Go 1.26+`
- [ ] `goreleaser build --snapshot --clean` produces valid darwin/amd64 and darwin/arm64 binaries
- [ ] `CGO_ENABLED=1` added to `.goreleaser.yaml` env block

### Non-Functional Requirements
- [ ] No regressions in CLI behavior: `ap version`, `ap status`, `ap sync`
- [ ] No new `go vet` warnings
- [ ] `go.sum` cleanly regenerated (no stale entries)
- [ ] Pre-release lipgloss pin unchanged after `go mod tidy`

---

## Dependencies & Risks

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| CGo / go-sqlite3 breaks on 1.26 | Low | High | Build first, test early. Fallback: `GOTOOLCHAIN=go1.24.0` |
| Pre-release lipgloss upgraded by `go mod tidy` | Low | Medium | Verify with `grep lipgloss go.mod` before and after tidy |
| GoReleaser cross-compile fails (arm64->amd64 CGo) | **Low** (downgraded) | High | Apple Clang handles cross-arch natively since Go 1.16+. Test with `goreleaser build --snapshot --clean` in Phase 2 |
| Post-quantum TLS (ML-KEM) breaks corporate proxies | Low | Medium | Escape hatch: `GODEBUG=tlsmlkem=0`. Document in PR for self-hosted GitLab users |
| `net/url.Parse` rejects a URL in tests/configs | Low | Low | Only 2 actual call sites (`senders.go:94`, `config.go:383`); both handle errors gracefully already |

> **Research Insight (url.Parse):** The original plan listed 4 files to audit. Codebase scan confirmed only 2 files actually call `url.Parse`. `internal/issuesync/gitlab.go` uses `fmt.Sprintf` for URL construction and `internal/git/pr.go` uses `url.QueryEscape` — neither passes through `url.Parse`.

---

## Rollback Plan

If issues are found after merging:

1. **Revert the PR** — each phase is a separate commit, so partial reverts are possible (e.g., revert only `go fix` changes while keeping the version bump).
2. **GODEBUG escape hatch** for specific behavior regressions: add `godebug default=go1.25` to go.mod. This should be time-limited (30-day tracking issue to resolve the underlying issue).
3. **Release rollback**: `ap upgrade` cannot downgrade (per `compareSemver` in `internal/update/update.go`). If a broken release ships: (a) delete or mark the release as pre-release on GitHub, (b) tag a patch release that reverts, (c) users who already upgraded will get the fix via `ap upgrade`. Users who cannot self-update must re-run `install.sh`.

---

## Go/No-Go Checklist (Pre-Merge)

| # | Criterion | Pass? |
|---|-----------|-------|
| 1 | All 13 test packages pass on Go 1.26 (`go test -race ./...`) | [ ] |
| 2 | `go vet ./...` zero warnings | [ ] |
| 3 | `go build ./...` zero errors | [ ] |
| 4 | Lipgloss pin unchanged (`v1.1.1-0.20250404203927-76690c660834`) | [ ] |
| 5 | `go-sqlite3 v1.14.24` unchanged | [ ] |
| 6 | GoReleaser snapshot produces both darwin binaries | [ ] |
| 7 | Both snapshot binaries execute `ap version` | [ ] |
| 8 | `test.yml` CI passed on GitHub Actions | [ ] |
| 9 | `go fix` changes reviewed (9 locations) | [ ] |
| 10 | README updated to `Go 1.26+` | [ ] |

**If ANY criterion is false: NO-GO.**

---

## Files Changed (Summary)

| File | Action | Description |
|------|--------|-------------|
| `go.mod` | Edit | Bump `go 1.24.0` -> `go 1.26.0` |
| `go.sum` | Regenerate | Via `go mod tidy` |
| `.github/workflows/test.yml` | Create | New test CI workflow with race detector |
| `.goreleaser.yaml` | Edit | Add `CGO_ENABLED=1` to env block |
| `README.md` | Edit | Update Go version requirement (line 38) |
| `internal/worker/pool.go` | Edit | `go fix`: range-over-int (line 35) |
| `internal/db/notifications_test.go` | Edit | `go fix`: range-over-int (line 222) |
| `internal/issuesync/sentry.go` | Edit | `go fix`: strings.Cut (line 163) |
| `internal/git/pr.go` | Edit | `go fix`: strings.Cut (line 317) |
| `internal/tui/model.go` | Edit | `go fix`: remove `minInt` helper, use builtin `min()` (lines 1094, 1635-1640) |
| `internal/git/worktree_test.go` | Edit | `go fix`: remove `tc := tc` (line 80) |
| `internal/git/pr_test.go` | Edit | `go fix`: remove `tc := tc` (line 151) |
| `internal/db/issues.go` | Edit | Remove dead `rand.Read` error check, simplify `newAutoPRIssueID` signature (lines 232-237), update caller at line 56 |
| `internal/db/jobs.go` | Edit | Remove dead `rand.Read` error check, simplify `newJobID` signature (lines 1106-1111), update caller at line 131 |

---

## Advance Notice: Go 1.27 Considerations

These GODEBUG settings will be **permanently removed** in Go 1.27. Document now for future migration:

| Setting | What It Does | Action Before Go 1.27 |
|---------|-------------|----------------------|
| `tls3des` | 3DES cipher suites | Verify no endpoints require 3DES |
| `tls10server` | TLS 1.0/1.1 server support | TLS 1.2 becomes minimum |
| `tlsrsakex` | RSA-only key exchanges | Verify no endpoints require RSA-only |
| `tlsunsafeekm` | EKM without TLS 1.3 | Requires TLS 1.3+ |

Users with self-hosted GitLab instances using legacy TLS should upgrade their TLS configuration before the project moves to Go 1.27.

---

## References

### Internal
- `go.mod:3` — current Go version directive
- `.github/workflows/release.yml:19-21` — current CI Go setup
- `.goreleaser.yaml` — release build configuration (needs `CGO_ENABLED=1`)
- `README.md:38` — user-facing Go version requirement
- `internal/db/store.go:7` — go-sqlite3 CGo import
- `internal/db/issues.go:232-237`, `internal/db/jobs.go:1106-1111` — `rand.Read` dead error checks
- `internal/notify/senders.go:94` — `url.Parse` usage (handles errors gracefully)
- `internal/config/config.go:383` — `url.Parse` usage (validates user webhook URLs)
- `internal/update/update.go:117-123` — self-update, no downgrade support
- `internal/tui/model.go:1635-1640` — `minInt` helper to remove
- `internal/worker/pool.go:35` — range-over-int candidate
- `internal/git/worktree_test.go:80`, `internal/git/pr_test.go:151` — dead `tc := tc`

### External
- [Go 1.26 Release Notes](https://go.dev/doc/go1.26)
- [Go Toolchain Documentation](https://go.dev/doc/toolchain)
- [GODEBUG Compatibility](https://go.dev/doc/godebug)
- [go fix Modernizers Blog](https://go.dev/blog/gofix)
- [actions/setup-go](https://github.com/actions/setup-go)
- [GoReleaser CGo Limitations](https://goreleaser.com/limitations/cgo/)
- [golang/go#43476 — CGo cross-compilation on Apple Silicon](https://github.com/golang/go/issues/43476)

### Related
- Issue: #55
