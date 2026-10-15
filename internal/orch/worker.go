package orch

import (
	"context"
	"errors"
	"time"

	"github.com/csy20/agt/internal/id"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
)

type Worker struct {
	Store *store.Store
	Run   func(context.Context, string) error
	ID    string
}

func (w *Worker) Loop(ctx context.Context) {
	if w.ID == "" {
		w.ID = id.New("wrk_")
	}
	for {
		if ctx.Err() != nil {
			return
		}
		job, err := w.Store.ClaimJob(ctx, w.ID)
		if errors.Is(err, store.ErrNotFound) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(150 * time.Millisecond):
			}
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(200 * time.Millisecond)
			continue
		}
		w.drive(ctx, job)
	}
}

// drive owns one claimed job. A pause finishes the job. A lost lease does not
// release it, because another worker may already hold the new fence. An
// interrupt or a process signal requeues only while this fence is still ours.
func (w *Worker) drive(parent context.Context, job store.Job) {
	for attempt := 0; attempt < 8; attempt++ {
		if parent.Err() != nil {
			_ = w.Store.ReleaseJob(context.Background(), job.ID, w.ID, job.Fence)
			return
		}
		runCtx, cancel := context.WithCancelCause(parent)
		done := make(chan struct{})
		go w.watch(runCtx, cancel, job, done)
		err := w.Run(runCtx, job.RunID)
		cancel(context.Cause(runCtx))
		<-done
		if errors.Is(err, ErrLeaseLost) || errors.Is(context.Cause(runCtx), ErrLeaseLost) {
			return
		}
		paused := errors.Is(err, ErrPaused) || errors.Is(context.Cause(runCtx), ErrPaused)
		if err != nil && !paused {
			_ = w.Store.ReleaseJob(context.Background(), job.ID, w.ID, job.Fence)
			return
		}
		finished, ferr := w.Store.FinishIfQuiescent(context.Background(), job.ID, w.ID, job.Fence)
		if errors.Is(ferr, store.ErrConflict) || errors.Is(ferr, store.ErrNotFound) {
			return
		}
		if ferr != nil {
			_ = w.Store.ReleaseJob(context.Background(), job.ID, w.ID, job.Fence)
			return
		}
		if finished {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = w.Store.ReleaseJob(context.Background(), job.ID, w.ID, job.Fence)
}

func (w *Worker) watch(ctx context.Context, cancel context.CancelCauseFunc, job store.Job, done chan struct{}) {
	defer close(done)
	renew := time.NewTicker(10 * time.Second)
	defer renew.Stop()
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-renew.C:
			if err := w.Store.RenewLease(context.Background(), job.ID, w.ID, job.Fence); err != nil {
				cancel(ErrLeaseLost)
				return
			}
		case <-poll.C:
			current, err := w.Store.GetRun(context.Background(), job.RunID)
			if err != nil {
				continue
			}
			switch current.State {
			case run.Paused, run.PausedBudget, run.Cancelled:
				cancel(ErrPaused)
				return
			}
		}
	}
}
