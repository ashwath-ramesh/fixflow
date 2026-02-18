package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"autopr/internal/config"
	"autopr/internal/db"
	"autopr/internal/issuesync"
	"autopr/internal/llm"
	"autopr/internal/notify"
	"autopr/internal/pipeline"
	"autopr/internal/webhook"
	"autopr/internal/worker"
)

// Run starts the daemon: webhook server + worker pool + sync loop.
// Blocks until SIGINT/SIGTERM is received.
func Run(cfg *config.Config, foreground bool) error {
	// Write PID file.
	if err := os.MkdirAll(filepath.Dir(cfg.Daemon.PIDFile), 0o755); err != nil {
		return fmt.Errorf("create pid dir: %w", err)
	}
	if err := WritePID(cfg.Daemon.PIDFile); err != nil {
		return err
	}
	defer RemovePID(cfg.Daemon.PIDFile)

	// Open DB.
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return fmt.Errorf("create db dir: %w", err)
	}
	store, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	// Crash recovery: reset in-flight jobs.
	recovered, err := store.RecoverInFlightJobs(context.Background())
	if err != nil {
		return fmt.Errorf("crash recovery: %w", err)
	}
	if recovered > 0 {
		slog.Info("recovered in-flight jobs", "count", recovered)
	}
	recoveredSessions, err := store.RecoverRunningSessions(context.Background())
	if err != nil {
		return fmt.Errorf("recover running sessions: %w", err)
	}
	if recoveredSessions > 0 {
		slog.Info("recovered stale llm sessions", "count", recoveredSessions)
	}

	// Signal context.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Create LLM provider.
	provider := llm.NewCLIProvider(cfg.LLM.Provider)

	// Create pipeline runner.
	pipelineRunner := pipeline.New(store, provider, cfg)

	// Create job channel (notification-only, SQLite is authoritative).
	jobCh := make(chan string, 100)

	// Re-enqueue any existing queued jobs from DB.
	queuedJobs, err := store.ListJobs(ctx, "", "queued")
	if err != nil {
		return fmt.Errorf("list queued jobs: %w", err)
	}
	for _, j := range queuedJobs {
		select {
		case jobCh <- j.ID:
		default:
		}
	}

	// Start worker pool.
	pool := worker.NewPool(cfg.Daemon.MaxWorkers, store, pipelineRunner, jobCh)
	pool.Start(ctx)

	// Start webhook server.
	whSrv := webhook.NewServer(cfg, store, jobCh)
	httpSrv := &http.Server{
		Addr:         fmt.Sprintf("127.0.0.1:%d", cfg.Daemon.WebhookPort),
		Handler:      whSrv,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	var wg sync.WaitGroup

	// Webhook server goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		slog.Info("webhook server starting", "addr", httpSrv.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("webhook server error", "err", err)
		}
	}()

	// Sync loop goroutine.
	syncInterval, _ := time.ParseDuration(cfg.Daemon.SyncInterval)
	if syncInterval > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			syncer := issuesync.NewSyncer(cfg, store, jobCh)
			syncer.RunLoop(ctx, syncInterval)
		}()
	}

	// Notification dispatcher goroutine.
	notificationDispatcher := notify.NewDispatcher(
		store,
		notify.BuildSenders(cfg.Notifications, nil),
		cfg.Notifications.Triggers,
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		notificationDispatcher.Run(ctx)
	}()

	slog.Info("daemon started", "workers", cfg.Daemon.MaxWorkers, "webhook_port", cfg.Daemon.WebhookPort)

	// Wait for shutdown signal.
	<-ctx.Done()
	slog.Info("shutdown signal received, stopping...")

	// Force-exit on second signal.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		slog.Error("second signal received, forcing exit")
		os.Exit(1)
	}()

	// Graceful shutdown with hard deadline.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = httpSrv.Shutdown(shutdownCtx)
		pool.Stop()
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("daemon stopped")
	case <-shutdownCtx.Done():
		slog.Error("shutdown timed out after 10s, forcing exit")
		os.Exit(1)
	}

	return nil
}
