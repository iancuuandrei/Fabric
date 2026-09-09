package taskscheduler

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// BenchmarkPump measures durable scheduling of 16 independent tasks with a
// fixed 2ms simulated runtime. It is not provider latency or model throughput.
func BenchmarkPump(b *testing.B) {
	for _, workers := range []int{1, 2, 4} {
		b.Run(fmt.Sprintf("workers_%d", workers), func(b *testing.B) {
			const taskCount = 16
			b.ReportAllocs()
			for iteration := 0; iteration < b.N; iteration++ {
				b.StopTimer()
				directory := b.TempDir()
				tasks := make([]TaskSpec, taskCount)
				adapter := &fakeAdapter{evidence: map[string]Evidence{}, dispatches: map[string]int{}}
				for i := range tasks {
					tasks[i] = TaskSpec{ID: fmt.Sprintf("task-%02d", i), RunID: digest('a'), ControllerPath: filepath.Join(directory, "run"), Operation: OperationPlanner, InvocationID: fmt.Sprintf("%064x", i+1)}
					adapter.set(tasks[i].ID, readyEvidence(tasks[i], 'b'))
				}
				path := filepath.Join(directory, "schedule")
				if _, err := Bind(path, Definition{Version: 1, Nonce: "pump-benchmark", Tasks: tasks}); err != nil {
					b.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				var completed atomic.Int32
				adapter.dispatch = func(claim Claim) (Evidence, error) {
					time.Sleep(2 * time.Millisecond)
					evidence := admittedEvidence(claim.Task, StatusSucceeded, 'e')
					adapter.set(claim.Task.ID, evidence)
					if completed.Add(1) == taskCount {
						cancel()
					}
					return evidence, nil
				}
				b.StartTimer()
				err := Pump(ctx, path, adapter, PumpOptions{Workers: workers, PollInterval: 10 * time.Millisecond})
				b.StopTimer()
				cancel()
				if !errors.Is(err, context.Canceled) || completed.Load() != taskCount {
					b.Fatal("incomplete scheduling batch", completed.Load(), err)
				}
				snapshot, err := Inspect(path)
				if err != nil {
					b.Fatal(err)
				}
				for _, task := range tasks {
					if snapshot.Tasks[task.ID].Status != StatusSucceeded || adapter.count(task.ID) != 1 {
						b.Fatal("batch did not complete exactly once", task.ID)
					}
				}
			}
			b.ReportMetric(taskCount, "tasks/op")
		})
	}
}
