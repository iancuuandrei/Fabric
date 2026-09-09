package cli

import (
	"context"
	"errors"
	"io"
	"sync"

	"harness.local/engorch/internal/taskscheduler"
)

type scheduleWorkerResult struct {
	Decision taskscheduler.Decision `json:"decision"`
	Failed   bool                   `json:"failed"`
}

func scheduleBatch(ctx context.Context, path string, workers int, adapter taskscheduler.Adapter, out io.Writer) error {
	results := make([]scheduleWorkerResult, workers)
	failures := make([]error, workers)
	var group sync.WaitGroup
	for i := range workers {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			decision, err := taskscheduler.Tick(ctx, path, adapter)
			results[index] = scheduleWorkerResult{Decision: decision, Failed: err != nil}
			failures[index] = err
		}(i)
	}
	group.Wait()
	// Report all attempted decisions even if one failed; the durable scheduler
	// and controller journals remain the authority for uncertain outcomes.
	return errors.Join(output(out, results), errors.Join(failures...))
}
