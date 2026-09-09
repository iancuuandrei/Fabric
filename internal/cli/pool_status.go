package cli

import (
	"context"
	"errors"
	"io"
	"sort"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/taskpool"
)

func poolStatus(ctx context.Context, configured *config.TaskPool, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if configured == nil {
		return output(out, map[string]string{"status": "NOT_CONFIGURED"})
	}
	snapshot, err := taskpool.InspectContext(ctx, configured.Path)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if snapshot.Limits == nil {
		return output(out, map[string]any{"status": "NOT_BOUND", "limits": configured.Limits})
	}
	want, err := canonical.Hash("harness.pool-status-limits.v1", configured.Limits)
	if err != nil {
		return err
	}
	got, err := canonical.Hash("harness.pool-status-limits.v1", *snapshot.Limits)
	if err != nil {
		return err
	}
	if got != want {
		return errors.New("configured pool limits differ from durable pool")
	}
	attempts := make([]taskpool.Request, 0, len(snapshot.Active))
	for _, request := range snapshot.Active {
		attempts = append(attempts, request)
	}
	sort.Slice(attempts, func(i, j int) bool { return attempts[i].ID < attempts[j].ID })
	return output(out, struct {
		Status  string             `json:"status"`
		Limits  taskpool.Limits    `json:"limits"`
		Active  []taskpool.Request `json:"active_attempts"`
		Settled int                `json:"settled_attempts"`
	}{"BOUND", *snapshot.Limits, attempts, len(snapshot.Settled)})
}
