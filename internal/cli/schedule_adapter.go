package cli

import (
	"context"

	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/taskscheduler"
)

// repositoryScheduleAdapter rechecks every selected task, including tasks added
// concurrently after the CLI inspected the schedule's initial definition.
type repositoryScheduleAdapter struct {
	root        string
	journalPath string
}

func (a repositoryScheduleAdapter) validate(task taskscheduler.TaskSpec) error {
	return validateScheduleRepository(a.root, taskscheduler.Definition{Tasks: []taskscheduler.TaskSpec{task}})
}

// Probe validates repository identity before reading controller evidence.
func (a repositoryScheduleAdapter) Probe(ctx context.Context, request taskscheduler.ProbeRequest) (taskscheduler.Evidence, error) {
	if err := a.validate(request.Task); err != nil {
		return taskscheduler.Evidence{}, err
	}
	return (control.ScheduledDispatchAdapter{JournalPath: a.journalPath}).Probe(ctx, request)
}

// Dispatch confines the exact claim to a run in the selected repository.
func (a repositoryScheduleAdapter) Dispatch(ctx context.Context, claim taskscheduler.Claim) (taskscheduler.Evidence, error) {
	if err := a.validate(claim.Task); err != nil {
		return taskscheduler.Evidence{}, err
	}
	return (control.ScheduledDispatchAdapter{JournalPath: a.journalPath}).Dispatch(ctx, claim)
}

// Reconcile validates repository identity before offline receipt recovery.
func (a repositoryScheduleAdapter) Reconcile(ctx context.Context, claim taskscheduler.Claim) (taskscheduler.Evidence, error) {
	if err := a.validate(claim.Task); err != nil {
		return taskscheduler.Evidence{}, err
	}
	return (control.ScheduledDispatchAdapter{JournalPath: a.journalPath}).Reconcile(ctx, claim)
}
