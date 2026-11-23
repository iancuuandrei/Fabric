package cli

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/iancuuandrei/fabric/internal/taskscheduler"
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
	var correctionErrs []error
	seen := map[string]bool{}
	for _, result := range results {
		decision := result.Decision
		if result.Failed || decision.Status != taskscheduler.StatusFailed || seen[decision.TaskID] {
			continue
		}
		seen[decision.TaskID] = true
		if err := admitSettledScheduledCorrection(ctx, path, decision); err != nil {
			correctionErrs = append(correctionErrs, err)
		}
	}
	// Report all attempted decisions even if one failed; the durable scheduler
	// and controller journals remain the authority for uncertain outcomes.
	return errors.Join(output(out, results), errors.Join(failures...), errors.Join(correctionErrs...))
}
