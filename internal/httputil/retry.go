package httputil

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryConfig controls the retry behavior.
type RetryConfig struct {
	MaxAttempts  int
	BaseDelay    time.Duration
	MaxDelay     time.Duration
	JitterFactor float64 // fraction of delay to randomize (0..1)
}

// DefaultRetryConfig returns sensible defaults for API calls.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:  4,
		BaseDelay:    1 * time.Second,
		MaxDelay:     30 * time.Second,
		JitterFactor: 0.25,
	}
}

// Do executes an HTTP request with retry/backoff. buildReq is called per
// attempt because request bodies are consumed on read and must be recreated.
//
// Retries on: network errors, HTTP 429, HTTP 5xx.
// Fails fast on 4xx (non-429) — the response is returned with body intact.
func Do(ctx context.Context, buildReq func() (*http.Request, error), cfg RetryConfig) (*http.Response, error) {
	var lastErr error

	for attempt := range cfg.MaxAttempts {
		req, err := buildReq()
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			if attempt < cfg.MaxAttempts-1 {
				slog.Warn("httputil: retrying after network error",
					"attempt", attempt+1,
					"max", cfg.MaxAttempts,
					"err", err,
				)
				if sleepErr := sleepWithContext(ctx, backoff(cfg, attempt, nil)); sleepErr != nil {
					return nil, sleepErr
				}
			}
			continue
		}

		// Success — no retry needed.
		if resp.StatusCode < 400 {
			return resp, nil
		}

		// 429 Too Many Requests — retry with Retry-After if present.
		if resp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			// Must drain body before retrying.
			resp.Body.Close()
			if attempt < cfg.MaxAttempts-1 {
				delay := backoff(cfg, attempt, resp)
				slog.Warn("httputil: retrying after 429",
					"attempt", attempt+1,
					"max", cfg.MaxAttempts,
					"delay", delay,
				)
				if sleepErr := sleepWithContext(ctx, delay); sleepErr != nil {
					return nil, sleepErr
				}
			}
			continue
		}

		// 5xx — retry.
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			resp.Body.Close()
			if attempt < cfg.MaxAttempts-1 {
				delay := backoff(cfg, attempt, resp)
				slog.Warn("httputil: retrying after server error",
					"attempt", attempt+1,
					"max", cfg.MaxAttempts,
					"status", resp.StatusCode,
					"delay", delay,
				)
				if sleepErr := sleepWithContext(ctx, delay); sleepErr != nil {
					return nil, sleepErr
				}
			}
			continue
		}

		// 4xx (non-429) — fail fast, return response with body intact.
		return resp, nil
	}

	return nil, fmt.Errorf("all %d attempts exhausted: %w", cfg.MaxAttempts, lastErr)
}

// backoff computes the sleep duration for the given attempt. If the response
// contains a Retry-After header, that value takes precedence.
func backoff(cfg RetryConfig, attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if ra := parseRetryAfter(resp.Header.Get("Retry-After")); ra > 0 {
			return ra
		}
	}

	delay := float64(cfg.BaseDelay) * math.Pow(2, float64(attempt))
	if delay > float64(cfg.MaxDelay) {
		delay = float64(cfg.MaxDelay)
	}

	jitter := delay * cfg.JitterFactor * (rand.Float64()*2 - 1) // ±jitter
	delay += jitter
	if delay < 0 {
		delay = float64(cfg.BaseDelay)
	}

	return time.Duration(delay)
}

// parseRetryAfter parses the Retry-After header value. It supports:
//   - seconds (e.g. "120")
//   - HTTP-date (e.g. "Thu, 01 Dec 2024 16:00:00 GMT")
//
// Returns 0 if the header is empty or unparseable.
func parseRetryAfter(val string) time.Duration {
	val = strings.TrimSpace(val)
	if val == "" {
		return 0
	}

	// Try seconds first.
	if secs, err := strconv.Atoi(val); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}

	// Try HTTP-date.
	if t, err := time.Parse(time.RFC1123, val); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}

	return 0
}

// sleepWithContext sleeps for d but returns immediately if ctx is cancelled.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
