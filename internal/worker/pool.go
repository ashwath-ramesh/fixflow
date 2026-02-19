package worker

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"autopr/internal/db"
	"autopr/internal/pipeline"
)

// Pool manages N worker goroutines that process jobs.
type Pool struct {
	n        int
	store    *db.Store
	pipeline *pipeline.Runner
	jobCh    <-chan string
	wg       sync.WaitGroup
	cancel   context.CancelFunc
}

func NewPool(n int, store *db.Store, pipeline *pipeline.Runner, jobCh <-chan string) *Pool {
	return &Pool{
		n:        n,
		store:    store,
		pipeline: pipeline,
		jobCh:    jobCh,
	}
}

func (p *Pool) Start(ctx context.Context) {
	ctx, p.cancel = context.WithCancel(ctx)
	for i := 0; i < p.n; i++ {
		p.wg.Add(1)
		go p.worker(ctx, i)
	}
}

func (p *Pool) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
}

func (p *Pool) worker(ctx context.Context, id int) {
	defer p.wg.Done()
	slog.Debug("worker started", "id", id)

	poll := time.NewTicker(5 * time.Second)
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Debug("worker stopping", "id", id)
			return
		case notifiedJobID, ok := <-p.jobCh:
			if !ok {
				return
			}
			p.processJob(ctx, id, notifiedJobID)
		case <-poll.C:
			p.processJob(ctx, id, "")
		}
	}
}

func (p *Pool) processJob(ctx context.Context, workerID int, notifiedJobID string) {
	// Panic recovery.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("worker panic", "worker", workerID, "job", notifiedJobID, "panic", r, "stack", string(debug.Stack()))
			// Try to mark job as failed.
			job, err := p.store.GetJob(ctx, notifiedJobID)
			if err == nil && job.State != "failed" {
				_ = p.store.TransitionState(ctx, notifiedJobID, job.State, "failed")
				_ = p.store.UpdateJobField(ctx, notifiedJobID, "error_message", "worker panic")
			}
		}
	}()

	// Claim job atomically (the notified ID is a hint; we claim from DB).
	jobID, err := p.store.ClaimJob(ctx)
	if err != nil {
		slog.Error("claim job failed", "err", err)
		return
	}
	if jobID == "" {
		// No queued job available (another worker may have claimed it).
		return
	}

	slog.Info("worker processing job", "worker", workerID, "job", jobID)

	if err := p.pipeline.Run(ctx, jobID); err != nil {
		slog.Error("pipeline failed", "job", jobID, "err", err)
	}
}
