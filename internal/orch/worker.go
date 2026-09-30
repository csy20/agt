package orch

import (
	"context"
	"errors"
	"time"

	"github.com/csy20/agt/internal/id"
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
		err = w.Run(ctx, job.RunID)
		if err != nil {
			_ = w.Store.ReleaseJob(context.Background(), job.ID)
			if ctx.Err() != nil {
				return
			}
			continue
		}
		_ = w.Store.FinishJob(context.Background(), job.ID)
	}
}
