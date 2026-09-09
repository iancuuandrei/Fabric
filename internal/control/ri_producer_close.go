package control

import (
	"context"
	"errors"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/worktree"
	"strings"
)

// RIProducerClosure preserves uncertainty while recording explicit workload quiescence.
// Operator evidence is not an inferred process result or artifact admission.
type RIProducerClosure struct {
	IntentID         string             `json:"intent_id"`
	Actor            string             `json:"actor"`
	Evidence         string             `json:"evidence"`
	WorkloadsStopped bool               `json:"workloads_stopped"`
	Candidate        worktree.Candidate `json:"candidate"`
}

func replayRIProducerClosure(s *Snapshot, event journal.Event) error {
	if s.RIProducer == nil || s.RIProducer.Outcome != "UNKNOWN" || s.RIProducer.Closure != nil {
		return errors.New("no unresolved producer to close")
	}
	var closure RIProducerClosure
	if err := canonical.Decode(event.Payload, &closure); err != nil {
		return err
	}
	id, err := s.RIProducer.Intent.Intent.ID()
	if err != nil {
		return err
	}
	if closure.IntentID != id || strings.TrimSpace(closure.Actor) == "" || len(closure.Actor) > 256 || strings.TrimSpace(closure.Evidence) == "" || len(closure.Evidence) > 2048 || !closure.WorkloadsStopped {
		return errors.New("exact producer and explicit quiescence evidence required")
	}
	if s.Candidate == nil || closure.Candidate != *s.Candidate {
		return errors.New("producer closure cannot admit source changes")
	}
	s.RIProducer.Closure = &closure
	return nil
}

// CloseRIProducer records caller-attested quiescence after pristine source validation.
// It neither reruns nor kills processes and never promotes unknown output to success.
func CloseRIProducer(ctx context.Context, path, intentID, actor, evidence string, stopped bool) (snapshot Snapshot, err error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if s.RIProducer == nil || s.RIProducer.Outcome != "UNKNOWN" || s.RIProducer.Closure != nil || s.Workspace == nil {
		return s, errors.New("no unresolved producer to close")
	}
	lease, err := worktree.Acquire(s.Workspace.Request)
	if err != nil {
		return s, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	candidate, err := worktree.Pristine(ctx, *s.Workspace)
	if err != nil {
		return s, err
	}
	if err := Append(path, "ri.producer-closed", RIProducerClosure{intentID, actor, evidence, stopped, candidate}); err != nil {
		return s, err
	}
	return Inspect(path)
}
