package taskscheduler

import (
	"context"
	"errors"
	"sync"
	"time"
)

// PumpOptions bounds concurrent Tick calls and the interval between decisions.
// Workers include waiting parent runtimes; nested work needs an available worker
// and remains subject to the controller's independent TaskPool limits.
type PumpOptions struct {
	Workers      int
	PollInterval time.Duration
}

// Pump keeps workers available for tasks appended while other tasks execute.
// It runs until cancellation or a Tick error, even when the queue is empty.
// It delegates every claim to Tick and never redispatches an uncertain claim.
// Adapters must honor cancellation; Pump waits for all owned calls to return.
func Pump(ctx context.Context, path string, adapter Adapter, options PumpOptions) error {
	if ctx == nil || adapter == nil || options.Workers < 1 || options.Workers > 64 {
		return errors.New("scheduler pump requires context, adapter and 1..64 workers")
	}
	interval := options.PollInterval
	if interval == 0 {
		interval = 100 * time.Millisecond
	}
	if interval < 10*time.Millisecond || interval > time.Second {
		return errors.New("scheduler pump poll interval must be 10ms..1s")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	failures := make([]error, options.Workers)
	var workers sync.WaitGroup
	for i := range options.Workers {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			for {
				if workerCtx.Err() != nil {
					return
				}
				if _, err := Tick(workerCtx, path, adapter); err != nil {
					failures[index] = err
					cancel()
					return
				}
				timer := time.NewTimer(interval)
				select {
				case <-workerCtx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}(i)
	}
	workers.Wait()
	return errors.Join(append(failures, ctx.Err())...)
}
