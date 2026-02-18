package httputil

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoSuccessFirstAttempt(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	resp, err := Do(context.Background(), func() (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	}, DefaultRetryConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("want body %q, got %q", "ok", string(body))
	}
}

func TestDoRetriesOn503(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "recovered")
	}))
	defer srv.Close()

	cfg := RetryConfig{
		MaxAttempts:  4,
		BaseDelay:    10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
		JitterFactor: 0,
	}
	resp, err := Do(context.Background(), func() (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	}, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if got := attempts.Load(); got != 3 {
		t.Fatalf("want 3 attempts, got %d", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "recovered" {
		t.Fatalf("want body %q, got %q", "recovered", string(body))
	}
}

func TestDoRetriesOn429WithRetryAfter(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	cfg := RetryConfig{
		MaxAttempts:  3,
		BaseDelay:    10 * time.Millisecond,
		MaxDelay:     5 * time.Second,
		JitterFactor: 0,
	}

	start := time.Now()
	resp, err := Do(context.Background(), func() (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	}, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	if elapsed < 900*time.Millisecond {
		t.Fatalf("expected ~1s delay from Retry-After, got %v", elapsed)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("want 2 attempts, got %d", got)
	}
}

func TestDoFailFastOn422(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"message":"validation failed"}`)
	}))
	defer srv.Close()

	cfg := RetryConfig{
		MaxAttempts:  4,
		BaseDelay:    10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
		JitterFactor: 0,
	}
	resp, err := Do(context.Background(), func() (*http.Request, error) {
		return http.NewRequest("POST", srv.URL, nil)
	}, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if got := attempts.Load(); got != 1 {
		t.Fatalf("want 1 attempt (fail fast), got %d", got)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "validation failed") {
		t.Fatalf("expected body intact, got %q", string(body))
	}
}

func TestDoMaxAttemptsExhausted(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	cfg := RetryConfig{
		MaxAttempts:  3,
		BaseDelay:    10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
		JitterFactor: 0,
	}
	_, err := Do(context.Background(), func() (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	}, cfg)
	if err == nil {
		t.Fatal("expected error after max attempts exhausted")
	}
	if !strings.Contains(err.Error(), "all 3 attempts exhausted") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("want 3 attempts, got %d", got)
	}
}

func TestDoContextCancellation(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())

	cfg := RetryConfig{
		MaxAttempts:  10,
		BaseDelay:    5 * time.Second, // long delay so we can cancel during it
		MaxDelay:     30 * time.Second,
		JitterFactor: 0,
	}

	done := make(chan error, 1)
	go func() {
		_, err := Do(ctx, func() (*http.Request, error) {
			return http.NewRequest("GET", srv.URL, nil)
		}, cfg)
		done <- err
	}()

	// Wait for first attempt to complete, then cancel.
	time.Sleep(100 * time.Millisecond)
	cancel()

	err := <-done
	if err == nil {
		t.Fatal("expected context error")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("expected context canceled, got: %v", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		val  string
		want time.Duration
	}{
		{"seconds", "120", 120 * time.Second},
		{"one second", "1", 1 * time.Second},
		{"empty", "", 0},
		{"invalid", "abc", 0},
		{"zero", "0", 0},
		{"negative", "-5", 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseRetryAfter(tc.val)
			if got != tc.want {
				t.Fatalf("parseRetryAfter(%q) = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	t.Parallel()

	// Use a date in the future.
	future := time.Now().Add(5 * time.Second).UTC().Format(time.RFC1123)
	d := parseRetryAfter(future)
	if d < 3*time.Second || d > 6*time.Second {
		t.Fatalf("expected ~5s from HTTP-date, got %v", d)
	}
}
