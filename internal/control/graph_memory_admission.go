package control

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/memoryadmission"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
)

const (
	graphMemoryAdmissionObservationLimit = 300
	graphMemoryAdmissionResampleInterval = time.Second
)

// GraphMemoryAdmissionRecord durably records one memory sample and its
// decision about a future isolated writer claim. It is scheduling evidence,
// not a measurement of worker memory use.
type GraphMemoryAdmissionRecord struct {
	Version                     int                         `json:"version"`
	RunID                       string                      `json:"run_id"`
	GraphDigest                 string                      `json:"graph_digest"`
	Revision                    int                         `json:"revision"`
	CandidateID                 string                      `json:"candidate_id"`
	PreparationID               string                      `json:"preparation_id"`
	TaskID                      string                      `json:"task_id"`
	GraphTaskID                 string                      `json:"graph_task_id,omitempty"`
	CorrectionAttempt           int                         `json:"correction_attempt,omitempty"`
	PriorClaimID                string                      `json:"prior_claim_id,omitempty"`
	PriorScheduleID             string                      `json:"prior_schedule_id,omitempty"`
	PriorScheduleDefinitionHash string                      `json:"prior_schedule_definition_hash,omitempty"`
	InvocationID                string                      `json:"invocation_id"`
	Sequence                    int                         `json:"sequence"`
	PreviousID                  string                      `json:"previous_id,omitempty"`
	SampledAtUTC                string                      `json:"sampled_at_utc"`
	Observation                 memoryadmission.Observation `json:"observation"`
	Decision                    memoryadmission.Decision    `json:"decision"`
	ActiveTaskIDs               []string                    `json:"active_task_ids"`
	Granted                     bool                        `json:"granted"`
	AdmissionID                 string                      `json:"admission_id"`
}

// GraphMemoryAdmissionRelease records a grant that never reached a durable
// scheduler claim. No task or provider effect can have started in that case.
type GraphMemoryAdmissionRelease struct {
	Version                     int    `json:"version"`
	TaskID                      string `json:"task_id"`
	AdmissionID                 string `json:"admission_id"`
	Reason                      string `json:"reason"`
	ScopeRequestID              string `json:"scope_request_id,omitempty"`
	ScopeMemberTaskID           string `json:"scope_member_task_id,omitempty"`
	ScopeClaimID                string `json:"scope_claim_id,omitempty"`
	ScopeResultTaskID           string `json:"scope_result_task_id,omitempty"`
	CorrectionTaskID            string `json:"correction_task_id,omitempty"`
	PriorClaimID                string `json:"prior_claim_id,omitempty"`
	PriorScheduleID             string `json:"prior_schedule_id,omitempty"`
	PriorScheduleDefinitionHash string `json:"prior_schedule_definition_hash,omitempty"`
	InvocationID                string `json:"invocation_id,omitempty"`
	ReceiptHead                 string `json:"receipt_head,omitempty"`
	ResultHash                  string `json:"result_hash,omitempty"`
}

// GraphMemoryAdmissionState is replayed memory-only scheduling state.
type GraphMemoryAdmissionState struct {
	Sequence      int                                   `json:"sequence"`
	LastAdmission string                                `json:"last_admission_id"`
	Decision      memoryadmission.Decision              `json:"decision"`
	ActiveTaskIDs map[string]GraphMemoryAdmissionActive `json:"active_task_ids"`
}

// GraphMemoryAdmissionActive binds an unreconciled grant to its invocation.
type GraphMemoryAdmissionActive struct {
	AdmissionID  string `json:"admission_id"`
	InvocationID string `json:"invocation_id"`
}

type graphMemoryAdmissionGate struct {
	controllerPath  string
	observe         func() memoryadmission.Observation
	mu              sync.Mutex
	changed         chan struct{}
	nextObservation time.Time
}

type memoryGatedScheduledDispatchAdapter struct {
	ScheduledDispatchAdapter
	gate *graphMemoryAdmissionGate
}

// BeforeClaim gates a future claim while preserving existing reservations.
func (a memoryGatedScheduledDispatchAdapter) BeforeClaim(ctx context.Context, task taskscheduler.TaskSpec) (func(taskscheduler.AdmissionClaimOutcome) (func(), error), error) {
	return a.gate.BeforeClaim(ctx, task)
}

func newGraphMemoryAdmissionGate(controllerPath string, observe func() memoryadmission.Observation) *graphMemoryAdmissionGate {
	if observe == nil {
		observe = memoryadmission.Observe
	}
	return &graphMemoryAdmissionGate{controllerPath: controllerPath, observe: observe, changed: make(chan struct{})}
}

// BeforeClaim is called after scheduler Probe has established READY and before
// task.claimed is appended. It never cancels or lowers the limit of an active
// claim; a newly lower ceiling only blocks future claims.
func (g *graphMemoryAdmissionGate) BeforeClaim(ctx context.Context, task taskscheduler.TaskSpec) (func(taskscheduler.AdmissionClaimOutcome) (func(), error), error) {
	if ctx == nil || g == nil || !filepath.IsAbs(g.controllerPath) || filepath.Clean(g.controllerPath) != g.controllerPath {
		return nil, errors.New("invalid graph memory admission gate")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		g.mu.Lock()
		snapshot, err := Inspect(g.controllerPath)
		if err != nil {
			g.mu.Unlock()
			return nil, err
		}
		if snapshot.GraphMemoryAdmission != nil && len(snapshot.GraphMemoryAdmission.ActiveTaskIDs) >= snapshot.GraphMemoryAdmission.Decision.EffectiveWorkers {
			changed := g.changed
			g.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-changed:
			}
			continue
		}
		alreadyGranted := false
		if snapshot.GraphMemoryAdmission != nil {
			_, alreadyGranted = snapshot.GraphMemoryAdmission.ActiveTaskIDs[task.ID]
		}
		if alreadyGranted {
			// Duplicate READY observation for an already granted task is
			// stale when spare effective capacity remains (the full
			// ceiling above already waited). Report it stale so the
			// scheduler re-evaluates another READY sibling on the next
			// Tick; never release, mutate, re-observe, or resend here.
			// The original grant, including UNKNOWN, is preserved.
			g.mu.Unlock()
			return nil, nil
		}
		if _, alreadyProposed := snapshot.GraphWriterResults[task.ID]; alreadyProposed {
			g.mu.Unlock()
			return nil, nil
		}
		if snapshot.GraphMemoryAdmission != nil && snapshot.GraphMemoryAdmission.Sequence >= graphMemoryAdmissionObservationLimit {
			changed := g.changed
			g.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-changed:
			}
			continue
		}
		if wait := time.Until(g.nextObservation); wait > 0 {
			changed := g.changed
			g.mu.Unlock()
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-changed:
				timer.Stop()
			case <-timer.C:
			}
			continue
		}
		g.nextObservation = time.Now().Add(graphMemoryAdmissionResampleInterval)
		observation := g.observe()
		record, err := expectedGraphMemoryAdmission(snapshot, task, observation, time.Now().UTC())
		if err != nil {
			g.mu.Unlock()
			return nil, err
		}
		if err := Append(g.controllerPath, "graph.writer.memory-admitted", record); err != nil {
			appendErr := err
			if record.Granted {
				latest, inspectErr := Inspect(g.controllerPath)
				if inspectErr == nil && latest.GraphMemoryAdmission != nil {
					active, exists := latest.GraphMemoryAdmission.ActiveTaskIDs[task.ID]
					if exists && active.AdmissionID == record.AdmissionID && active.InvocationID == record.InvocationID {
						releaseErr := Append(g.controllerPath, "graph.writer.memory-released", GraphMemoryAdmissionRelease{Version: 1, TaskID: task.ID, AdmissionID: record.AdmissionID, Reason: "claim_not_recorded"})
						released := releaseErr == nil
						if !released {
							afterRelease, checkErr := Inspect(g.controllerPath)
							releaseErr = errors.Join(releaseErr, checkErr)
							if checkErr == nil && afterRelease.GraphMemoryAdmission != nil {
								stillActive, stillExists := afterRelease.GraphMemoryAdmission.ActiveTaskIDs[task.ID]
								released = !stillExists || stillActive.AdmissionID != record.AdmissionID
							} else if checkErr == nil {
								released = true
							}
						}
						if released {
							g.signalLocked()
						}
						g.mu.Unlock()
						return nil, errors.Join(appendErr, releaseErr)
					}
				}
				appendErr = errors.Join(appendErr, inspectErr)
			}
			g.mu.Unlock()
			return nil, appendErr
		}
		if !record.Granted {
			changed := g.changed
			nextObservation := g.nextObservation
			g.mu.Unlock()
			wait := time.Until(nextObservation)
			if wait < 0 {
				wait = 0
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-changed:
				timer.Stop()
			case <-timer.C:
			}
			continue
		}
		var once sync.Once
		return func(outcome taskscheduler.AdmissionClaimOutcome) (func(), error) {
			var releaseErr error
			var complete func()
			once.Do(func() {
				switch outcome {
				case taskscheduler.AdmissionClaimNotRecorded:
					releaseErr = Append(g.controllerPath, "graph.writer.memory-released", GraphMemoryAdmissionRelease{Version: 1, TaskID: task.ID, AdmissionID: record.AdmissionID, Reason: "claim_not_recorded"})
					g.signalLocked()
				case taskscheduler.AdmissionClaimRecorded:
					complete = g.signal
				case taskscheduler.AdmissionClaimUnknown:
					// Keep the durable reservation active while claim persistence is
					// unresolved; recovery cannot create or refund this admission.
					complete = g.signal
				default:
					releaseErr = errors.New("invalid admission claim outcome; reservation retained")
					complete = g.signal
				}
				g.mu.Unlock()
			})
			return complete, releaseErr
		}, nil
	}
}

func expectedGraphMemoryAdmission(s Snapshot, task taskscheduler.TaskSpec, observation memoryadmission.Observation, sampledAt time.Time) (GraphMemoryAdmissionRecord, error) {
	if !isolatedImplementationEnabled(s) || s.State != "IMPLEMENTING" || s.Graph == nil || s.Candidate == nil || s.GraphIsolationPreparation == nil || s.GraphWriterBatch != nil {
		return GraphMemoryAdmissionRecord{}, errors.New("isolated initial writer memory admission is unavailable")
	}
	if task.RunID != s.RunID || task.ControllerPath == "" || filepath.Clean(task.ControllerPath) != task.ControllerPath || task.Operation != taskscheduler.OperationWriter || task.Input != "" || len(task.DependsOn) != 0 {
		return GraphMemoryAdmissionRecord{}, errors.New("memory admission task binding differs")
	}
	graphTaskID := task.ID
	var correction *RoleSemanticCorrection
	if candidate, ok := scheduledCorrectionForTask(s, task.ID); ok {
		if candidate.TaskID == "" || candidate.Invocation.Profile.Role == "reviewer" || candidate.ScheduledTaskID != task.ID {
			return GraphMemoryAdmissionRecord{}, errors.New("memory admission correction is not an isolated writer successor")
		}
		graphTaskID = candidate.TaskID
		correction = &candidate
	}
	if !containsGraphIsolationTask(s.GraphIsolationPreparation.SelectedTaskIDs, graphTaskID) {
		return GraphMemoryAdmissionRecord{}, errors.New("memory admission task is outside frozen cohort")
	}
	_, _, err := expectedGraphIsolationIntent(s, graphTaskID)
	if err != nil {
		return GraphMemoryAdmissionRecord{}, err
	}
	invocation, err := writerInvocationForTask(s, graphTaskID)
	var correctionAttempt int
	var priorClaimID, priorScheduleID, priorScheduleDefinitionHash string
	if correction != nil {
		if correction.PriorClaim == nil || correction.PriorEvidence == nil || correction.CandidateID == "" {
			return GraphMemoryAdmissionRecord{}, errors.New("memory admission correction lacks its settled predecessor")
		}
		claimID, claimErr := correction.PriorClaim.ID()
		if claimErr != nil || correction.PriorEvidence.Status != taskscheduler.StatusFailed || correction.PriorClaim.Task.Operation != taskscheduler.OperationWriter {
			return GraphMemoryAdmissionRecord{}, errors.Join(errors.New("memory admission correction predecessor is not a terminal writer rejection"), claimErr)
		}
		binding := scheduledRoleCorrectionBinding{SchedulePath: correction.PriorSchedulePath, ScheduleID: correction.PriorScheduleID, ScheduleDefinitionHash: correction.PriorScheduleDefinitionHash, PriorTaskID: correction.PriorTaskID, Claim: *correction.PriorClaim, Evidence: *correction.PriorEvidence}
		if err := validateScheduledRoleCorrectionBinding(s, graphTaskID, correction.Original, binding); err != nil {
			return GraphMemoryAdmissionRecord{}, err
		}
		schedule, inspectErr := taskscheduler.Inspect(binding.SchedulePath)
		if inspectErr != nil || schedule.Definition == nil || !settledStaticRoleCohort(schedule, task.ControllerPath, taskscheduler.OperationWriter) {
			return GraphMemoryAdmissionRecord{}, errors.Join(errors.New("memory correction requires a terminal original writer cohort"), inspectErr)
		}
		candidateID, candidateErr := s.Candidate.ID()
		if candidateErr != nil || candidateID != correction.CandidateID || correction.BaseInvocationID != invocation.ID {
			return GraphMemoryAdmissionRecord{}, errors.Join(errors.New("memory correction candidate or base invocation changed"), candidateErr)
		}
		invocation, err = scheduledTurnInvocation(correction.Invocation, taskscheduler.OperationWriter, correction.ScheduledTaskID)
		if err != nil {
			return GraphMemoryAdmissionRecord{}, err
		}
		correctionAttempt, priorClaimID = correction.Attempt, claimID
		priorScheduleID, priorScheduleDefinitionHash = correction.PriorScheduleID, correction.PriorScheduleDefinitionHash
		if priorClaimID == "" {
			return GraphMemoryAdmissionRecord{}, errors.New("memory correction predecessor claim ID is empty")
		}
	}
	if err != nil || invocation.ID != task.InvocationID {
		return GraphMemoryAdmissionRecord{}, errors.Join(errors.New("memory admission invocation differs"), err)
	}
	candidateID, err := s.Candidate.ID()
	if err != nil || candidateID != s.GraphIsolationPreparation.BaseCandidateID {
		return GraphMemoryAdmissionRecord{}, errors.Join(errors.New("memory admission candidate differs"), err)
	}
	// Decision.ConfiguredMaxWorkers stays stable for the whole run so replay
	// never observes a policy change across waves. The per-wave bound is
	// enforced separately on the grant, not as a new configured maximum.
	maxWorkers := isolatedMemoryStableMaxWorkers(s)
	waveBound := isolatedMemoryMaxWorkers(s, graphTaskID)
	if maxWorkers < 1 || waveBound < 1 {
		return GraphMemoryAdmissionRecord{}, errors.New("memory admission has no configured workers")
	}
	var previous *memoryadmission.Decision
	sequence, previousID := 1, ""
	active := map[string]GraphMemoryAdmissionActive{}
	if s.GraphMemoryAdmission != nil {
		prior := s.GraphMemoryAdmission
		previous = &prior.Decision
		sequence, previousID = prior.Sequence+1, prior.LastAdmission
		for id, value := range prior.ActiveTaskIDs {
			active[id] = value
		}
	}
	decision, err := memoryadmission.Next(previous, observation, maxWorkers, s.Creation.Execution.IsolationEstimate.MemoryMiB, memoryadmission.DefaultReserveMiB, memoryadmission.DefaultHysteresisMiB)
	if err != nil {
		return GraphMemoryAdmissionRecord{}, err
	}
	activeIDs := make([]string, 0, len(active))
	for id := range active {
		activeIDs = append(activeIDs, id)
	}
	sort.Strings(activeIDs)
	if _, alreadyActive := active[task.ID]; alreadyActive {
		return GraphMemoryAdmissionRecord{}, errors.New("memory admission task already has an unresolved claim")
	}
	if _, alreadyProposed := s.GraphWriterResults[graphTaskID]; alreadyProposed {
		return GraphMemoryAdmissionRecord{}, errors.New("memory admission task already has a writer result")
	}
	if sampledAt.Location() != time.UTC {
		return GraphMemoryAdmissionRecord{}, errors.New("memory observation timestamp must be UTC")
	}
	record := GraphMemoryAdmissionRecord{
		Version: 1, RunID: s.RunID, GraphDigest: s.Graph.Digest, Revision: s.Graph.Revision,
		CandidateID: candidateID, PreparationID: s.GraphIsolationPreparation.PreparationID,
		TaskID: task.ID, InvocationID: invocation.ID, Sequence: sequence, PreviousID: previousID,
		SampledAtUTC: sampledAt.Format(time.RFC3339Nano), Observation: observation,
		Decision: decision, ActiveTaskIDs: activeIDs,
		Granted: len(activeIDs) < decision.EffectiveWorkers && len(activeIDs) < waveBound,
	}
	if correction != nil {
		record.GraphTaskID = graphTaskID
		record.CorrectionAttempt = correctionAttempt
		record.PriorClaimID = priorClaimID
		record.PriorScheduleID = priorScheduleID
		record.PriorScheduleDefinitionHash = priorScheduleDefinitionHash
	}
	if err := setGraphMemoryAdmissionID(&record); err != nil {
		return GraphMemoryAdmissionRecord{}, err
	}
	return record, nil
}

func setGraphMemoryAdmissionID(record *GraphMemoryAdmissionRecord) error {
	copy := *record
	copy.AdmissionID = ""
	id, err := canonical.Hash("harness.graph-memory-admission.v1", copy)
	if err != nil {
		return err
	}
	record.AdmissionID = id
	return nil
}

func replayGraphMemoryAdmission(s *Snapshot, event journal.Event) error {
	switch event.Kind {
	case "graph.writer.memory-admitted":
		var record GraphMemoryAdmissionRecord
		if err := canonical.Decode(event.Payload, &record); err != nil {
			return err
		}
		if err := validateGraphMemoryAdmission(*s, record); err != nil {
			return err
		}
		active := map[string]GraphMemoryAdmissionActive{}
		if s.GraphMemoryAdmission != nil {
			for id, value := range s.GraphMemoryAdmission.ActiveTaskIDs {
				active[id] = value
			}
		}
		if record.Granted {
			active[record.TaskID] = GraphMemoryAdmissionActive{AdmissionID: record.AdmissionID, InvocationID: record.InvocationID}
		}
		s.GraphMemoryAdmission = &GraphMemoryAdmissionState{Sequence: record.Sequence, LastAdmission: record.AdmissionID, Decision: record.Decision, ActiveTaskIDs: active}
	case "graph.writer.memory-released":
		var release GraphMemoryAdmissionRelease
		if err := canonical.Decode(event.Payload, &release); err != nil {
			return err
		}
		if release.Version != 1 || s.GraphMemoryAdmission == nil {
			return errors.New("invalid memory admission release")
		}
		active, ok := s.GraphMemoryAdmission.ActiveTaskIDs[release.TaskID]
		if !ok || active.AdmissionID != release.AdmissionID {
			return errors.New("memory admission release does not match active grant")
		}
		switch release.Reason {
		case "claim_not_recorded":
			if release.ScopeRequestID != "" || release.ScopeMemberTaskID != "" || release.ScopeClaimID != "" || release.ScopeResultTaskID != "" || release.CorrectionTaskID != "" || release.PriorClaimID != "" || release.PriorScheduleID != "" || release.PriorScheduleDefinitionHash != "" || release.InvocationID != "" || release.ReceiptHead != "" || release.ResultHash != "" {
				return errors.New("claim release contains correction evidence")
			}
		case "semantic_correction":
			if release.ScopeRequestID != "" || release.ScopeMemberTaskID != "" || release.ScopeClaimID != "" || release.ScopeResultTaskID != "" {
				return errors.New("semantic correction release contains scope evidence")
			}
			if err := validateGraphMemoryCorrectionRelease(*s, release, active); err != nil {
				return err
			}
		case "scope_replan":
			if release.CorrectionTaskID != "" || release.PriorClaimID != "" || release.PriorScheduleID != "" || release.PriorScheduleDefinitionHash != "" {
				return errors.New("scope replan release contains semantic correction evidence")
			}
			if err := validateGraphMemoryScopeReplanRelease(*s, release, active); err != nil {
				return err
			}
		default:
			return errors.New("invalid memory admission release reason")
		}
		delete(s.GraphMemoryAdmission.ActiveTaskIDs, release.TaskID)
	default:
		return errors.New("unknown graph memory admission event")
	}
	return nil
}

func validateGraphMemoryAdmission(s Snapshot, record GraphMemoryAdmissionRecord) error {
	if !isolatedImplementationEnabled(s) || s.State != "IMPLEMENTING" || s.Graph == nil || s.Candidate == nil || s.GraphIsolationPreparation == nil || s.GraphWriterBatch != nil {
		return errors.New("isolated graph memory admission not enabled")
	}
	if record.Version != 1 || record.RunID != s.RunID || record.GraphDigest != s.Graph.Digest || record.Revision != s.Graph.Revision || record.PreparationID != s.GraphIsolationPreparation.PreparationID {
		return errors.New("graph memory admission identity differs")
	}
	candidateID, err := s.Candidate.ID()
	if err != nil || record.CandidateID != candidateID || candidateID != s.GraphIsolationPreparation.BaseCandidateID {
		return errors.Join(errors.New("graph memory admission candidate differs"), err)
	}
	graphTaskID := record.TaskID
	var invocation runtime.Invocation
	correction, isCorrection := scheduledCorrectionForTask(s, record.TaskID)
	if record.CorrectionAttempt == 0 && record.GraphTaskID == "" && record.PriorClaimID == "" && record.PriorScheduleID == "" && record.PriorScheduleDefinitionHash == "" {
		if isCorrection || !containsGraphIsolationTask(s.GraphIsolationPreparation.SelectedTaskIDs, record.TaskID) {
			return errors.New("graph memory admission task is outside frozen cohort")
		}
		invocation, err = writerInvocationForTask(s, record.TaskID)
	} else {
		if !isCorrection || correction.TaskID == "" || correction.Invocation.Profile.Role == "reviewer" || correction.ScheduledTaskID != record.TaskID || record.GraphTaskID != correction.TaskID || record.CorrectionAttempt != correction.Attempt || !containsGraphIsolationTask(s.GraphIsolationPreparation.SelectedTaskIDs, correction.TaskID) {
			return errors.New("graph memory correction admission identity differs")
		}
		graphTaskID = correction.TaskID
		baseInvocation, baseErr := writerInvocationForTask(s, graphTaskID)
		if correction.PriorClaim == nil || correction.PriorEvidence == nil {
			return errors.New("graph memory correction predecessor is absent")
		}
		claimID, claimErr := correction.PriorClaim.ID()
		if baseErr != nil || correction.BaseInvocationID != baseInvocation.ID || correction.CandidateID != candidateID || claimErr != nil || correction.PriorEvidence.Status != taskscheduler.StatusFailed || correction.PriorClaim.Task.Operation != taskscheduler.OperationWriter || claimID != record.PriorClaimID || correction.PriorScheduleID != record.PriorScheduleID || correction.PriorScheduleDefinitionHash != record.PriorScheduleDefinitionHash {
			return errors.Join(errors.New("graph memory correction predecessor differs"), baseErr, claimErr)
		}
		binding := scheduledRoleCorrectionBinding{SchedulePath: correction.PriorSchedulePath, ScheduleID: correction.PriorScheduleID, ScheduleDefinitionHash: correction.PriorScheduleDefinitionHash, PriorTaskID: correction.PriorTaskID, Claim: *correction.PriorClaim, Evidence: *correction.PriorEvidence}
		if err := validateScheduledRoleCorrectionBinding(s, graphTaskID, correction.Original, binding); err != nil {
			return err
		}
		invocation, err = scheduledTurnInvocation(correction.Invocation, taskscheduler.OperationWriter, correction.ScheduledTaskID)
	}
	if err != nil || invocation.ID != record.InvocationID {
		return errors.Join(errors.New("graph memory admission invocation differs"), err)
	}
	if _, alreadyProposed := s.GraphWriterResults[graphTaskID]; alreadyProposed {
		return errors.New("graph memory admission task already has a writer result")
	}
	sampledAt, err := time.Parse(time.RFC3339Nano, record.SampledAtUTC)
	if err != nil || sampledAt.Location() != time.UTC || sampledAt.Format(time.RFC3339Nano) != record.SampledAtUTC {
		return errors.Join(errors.New("invalid memory observation time"), err)
	}
	maxWorkers := isolatedMemoryStableMaxWorkers(s)
	waveBound := isolatedMemoryMaxWorkers(s, graphTaskID)
	if maxWorkers < 1 || waveBound < 1 {
		return errors.New("memory admission has no configured workers")
	}
	var previous *memoryadmission.Decision
	sequence, previousID := 1, ""
	activeIDs := make([]string, 0)
	if s.GraphMemoryAdmission != nil {
		previous = &s.GraphMemoryAdmission.Decision
		sequence, previousID = s.GraphMemoryAdmission.Sequence+1, s.GraphMemoryAdmission.LastAdmission
		for id := range s.GraphMemoryAdmission.ActiveTaskIDs {
			activeIDs = append(activeIDs, id)
		}
	}
	sort.Strings(activeIDs)
	if record.Sequence != sequence || record.PreviousID != previousID || strings.Join(record.ActiveTaskIDs, "\x00") != strings.Join(activeIDs, "\x00") {
		return errors.New("graph memory admission sequence or active set differs")
	}
	decision, err := memoryadmission.Next(previous, record.Observation, maxWorkers, s.Creation.Execution.IsolationEstimate.MemoryMiB, memoryadmission.DefaultReserveMiB, memoryadmission.DefaultHysteresisMiB)
	if err != nil || decision != record.Decision {
		return errors.Join(errors.New("graph memory admission decision differs"), err)
	}
	if record.Granted != (len(activeIDs) < decision.EffectiveWorkers && len(activeIDs) < waveBound) {
		return errors.New("graph memory admission grant differs from effective capacity")
	}
	want := record
	if err := setGraphMemoryAdmissionID(&want); err != nil || want.AdmissionID != record.AdmissionID {
		return errors.Join(errors.New("graph memory admission identity hash differs"), err)
	}
	return nil
}

func settleGraphMemoryAdmissionProposal(s *Snapshot, record GraphWriterRecord) error {
	if s.GraphMemoryAdmission == nil {
		return nil
	}
	invocationID := graphWriterRecordInvocation(record).ID
	if active, ok := s.GraphMemoryAdmission.ActiveTaskIDs[record.TaskID]; ok && invocationID == active.InvocationID {
		delete(s.GraphMemoryAdmission.ActiveTaskIDs, record.TaskID)
		return nil
	}
	matched := ""
	for _, correction := range s.RoleCorrections {
		if correction.TaskID != record.TaskID || correction.ScheduledTaskID == "" || correction.Invocation.Profile.Role != "writer" {
			continue
		}
		invocation, err := scheduledTurnInvocation(correction.Invocation, taskscheduler.OperationWriter, correction.ScheduledTaskID)
		if err != nil || invocation.ID != invocationID {
			continue
		}
		active, ok := s.GraphMemoryAdmission.ActiveTaskIDs[correction.ScheduledTaskID]
		if !ok || active.InvocationID != invocationID || matched != "" {
			return errors.New("graph writer correction proposal lacks its unique exact memory admission")
		}
		matched = correction.ScheduledTaskID
	}
	if matched == "" {
		return errors.New("graph writer proposal lacks its exact memory admission")
	}
	delete(s.GraphMemoryAdmission.ActiveTaskIDs, matched)
	return nil
}

func releaseGraphMemoryAdmissionForCorrection(controllerPath string, correction RoleSemanticCorrection) error {
	if correction.ScheduledTaskID == "" || correction.Invocation.Profile.Role != "writer" || correction.PriorClaim == nil || correction.PriorEvidence == nil {
		return nil
	}
	s, err := Inspect(controllerPath)
	if err != nil {
		return err
	}
	if s.GraphMemoryAdmission == nil {
		return nil
	}
	active, ok := s.GraphMemoryAdmission.ActiveTaskIDs[correction.PriorTaskID]
	if !ok {
		// The predecessor may have had no active reservation, or a prior call
		// may already have recorded this exact release.
		return nil
	}
	claimID, err := correction.PriorClaim.ID()
	if err != nil || correction.PriorClaim.Task.ID != correction.PriorTaskID || correction.PriorClaim.Task.InvocationID != active.InvocationID || correction.PriorEvidence.Status != taskscheduler.StatusFailed || correction.ReceiptHead == "" {
		return errors.Join(errors.New("semantic correction does not prove its predecessor memory reservation settled"), err)
	}
	domain, err := runtimeResultDomain(correction.Invocation.Profile.Role)
	if err != nil {
		return err
	}
	resultHash, err := canonical.Hash(domain, correction.Predecessor)
	if err != nil {
		return err
	}
	release := GraphMemoryAdmissionRelease{
		Version: 1, TaskID: correction.PriorTaskID, AdmissionID: active.AdmissionID, Reason: "semantic_correction",
		CorrectionTaskID: correction.ScheduledTaskID, PriorClaimID: claimID,
		PriorScheduleID: correction.PriorScheduleID, PriorScheduleDefinitionHash: correction.PriorScheduleDefinitionHash,
		InvocationID: correction.PriorClaim.Task.InvocationID, ReceiptHead: correction.ReceiptHead, ResultHash: resultHash,
	}
	if err := Append(controllerPath, "graph.writer.memory-released", release); err == nil {
		return nil
	} else {
		appendErr := err
		latest, inspectErr := Inspect(controllerPath)
		if inspectErr != nil {
			return errors.Join(appendErr, inspectErr)
		}
		if latest.GraphMemoryAdmission == nil {
			return appendErr
		}
		current, stillActive := latest.GraphMemoryAdmission.ActiveTaskIDs[correction.PriorTaskID]
		if stillActive && current.AdmissionID == active.AdmissionID {
			return appendErr
		}
		events, journalErr := journal.Read(controllerPath)
		if journalErr != nil {
			return errors.Join(appendErr, journalErr)
		}
		for _, event := range events {
			if event.Kind != "graph.writer.memory-released" {
				continue
			}
			var observed GraphMemoryAdmissionRelease
			if canonical.Decode(event.Payload, &observed) == nil && sameCanonical(observed, release) {
				return nil
			}
		}
		return appendErr
	}
}

// releaseGraphMemoryAdmissionForScopeRequest releases only settled reservations
// for scope-violating members already captured by a durable scope request. A
// missing active grant is an idempotent no-op (for example, a correction may
// already have released its predecessor); any mismatched grant is an error.
func releaseGraphMemoryAdmissionForScopeRequest(controllerPath, requestID string) error {
	if requestID == "" {
		return errors.New("scope memory release requires a request identity")
	}
	s, err := Inspect(controllerPath)
	if err != nil {
		return err
	}
	var request *ScopeReplanRequest
	for i := range s.ScopeReplanRequests {
		candidate := &s.ScopeReplanRequests[i]
		if candidate.RequestID != requestID {
			continue
		}
		if request != nil {
			return errors.New("scope memory release request is ambiguous")
		}
		request = candidate
	}
	if request == nil {
		return errors.New("scope memory release request is not durable")
	}
	if s.GraphMemoryAdmission == nil {
		return nil
	}
	for _, member := range request.Cohort.Members {
		if !member.ScopeViolation {
			continue
		}
		release, active, err := scopeReplanMemoryRelease(*request, member, s.GraphMemoryAdmission.ActiveTaskIDs)
		if err != nil {
			return err
		}
		if !active {
			continue
		}
		if err := appendGraphMemoryScopeRelease(controllerPath, release); err != nil {
			return err
		}
		s, err = Inspect(controllerPath)
		if err != nil {
			return err
		}
		if s.GraphMemoryAdmission == nil {
			return errors.New("scope memory release removed admission state")
		}
	}
	return nil
}

func scopeReplanMemoryRelease(request ScopeReplanRequest, member SettledGraphWriterMember, active map[string]GraphMemoryAdmissionActive) (GraphMemoryAdmissionRelease, bool, error) {
	var empty GraphMemoryAdmissionRelease
	if request.RequestID == "" || request.Version != 1 || member.TaskID == "" || !member.ScopeViolation || member.RejectedResult == nil || member.Evidence.Status != taskscheduler.StatusSucceeded && member.Evidence.Status != taskscheduler.StatusFailed {
		return empty, false, errors.New("scope memory release lacks a durable settled scope rejection")
	}
	claimID, err := member.Claim.ID()
	if err != nil || claimID != member.ClaimID || member.Claim.Task.ID != member.TaskID || member.Claim.Task.InvocationID != member.Invocation.ID {
		return empty, false, errors.Join(errors.New("scope memory release claim binding differs"), err)
	}
	resultInvocation := member.Invocation
	resultEvidence := member.Evidence
	resultTaskID := member.TaskID
	if len(member.Corrections) != 0 {
		if _, stale := active[member.TaskID]; stale {
			return empty, false, errors.New("scope memory release found a stale static reservation after correction")
		}
		last := member.Corrections[len(member.Corrections)-1]
		resultInvocation = last.Invocation
		resultEvidence = last.Evidence
		resultTaskID = last.Dynamic.Task.ID
		if member.ResultInvocation == nil || *member.ResultInvocation != resultInvocation || member.ResultEvidence == nil || !sameCanonical(*member.ResultEvidence, resultEvidence) || resultTaskID == "" {
			return empty, false, errors.New("scope memory release correction result binding differs")
		}
	}
	if resultEvidence.Status != taskscheduler.StatusFailed || member.RuntimeReceipt.InvocationID != resultInvocation.ID || member.RuntimeReceipt.JournalHead == "" || member.RuntimeReceipt.ResultHash != member.ResultHash || member.ResultHash == "" {
		return empty, false, errors.New("scope memory release lacks the exact failed runtime result")
	}
	grant, ok := active[resultTaskID]
	if !ok {
		return empty, false, nil
	}
	if grant.AdmissionID == "" || grant.InvocationID != resultInvocation.ID {
		return empty, false, errors.New("scope memory release active grant does not match the settled result")
	}
	return GraphMemoryAdmissionRelease{
		Version: 1, TaskID: resultTaskID, AdmissionID: grant.AdmissionID, Reason: "scope_replan",
		ScopeRequestID: request.RequestID, ScopeMemberTaskID: member.TaskID, ScopeClaimID: claimID, ScopeResultTaskID: resultTaskID,
		InvocationID: resultInvocation.ID, ReceiptHead: member.RuntimeReceipt.JournalHead, ResultHash: member.ResultHash,
	}, true, nil
}

func appendGraphMemoryScopeRelease(controllerPath string, release GraphMemoryAdmissionRelease) error {
	if err := Append(controllerPath, "graph.writer.memory-released", release); err == nil {
		return nil
	} else {
		appendErr := err
		latest, inspectErr := Inspect(controllerPath)
		if inspectErr != nil {
			return errors.Join(appendErr, inspectErr)
		}
		if latest.GraphMemoryAdmission != nil {
			active, stillActive := latest.GraphMemoryAdmission.ActiveTaskIDs[release.TaskID]
			if stillActive && active.AdmissionID == release.AdmissionID {
				return appendErr
			}
		}
		events, journalErr := journal.Read(controllerPath)
		if journalErr != nil {
			return errors.Join(appendErr, journalErr)
		}
		for _, event := range events {
			if event.Kind != "graph.writer.memory-released" {
				continue
			}
			var observed GraphMemoryAdmissionRelease
			if canonical.Decode(event.Payload, &observed) == nil && sameCanonical(observed, release) {
				return nil
			}
		}
		return appendErr
	}
}

func validateGraphMemoryScopeReplanRelease(s Snapshot, release GraphMemoryAdmissionRelease, active GraphMemoryAdmissionActive) error {
	if release.ScopeRequestID == "" || release.ScopeMemberTaskID == "" || release.ScopeClaimID == "" || release.ScopeResultTaskID != release.TaskID || release.InvocationID == "" || release.ReceiptHead == "" || release.ResultHash == "" || active.InvocationID != release.InvocationID {
		return errors.New("scope memory release lacks exact settled-result binding")
	}
	var request *ScopeReplanRequest
	for i := range s.ScopeReplanRequests {
		candidate := &s.ScopeReplanRequests[i]
		if candidate.RequestID == release.ScopeRequestID {
			if request != nil {
				return errors.New("scope memory release request is ambiguous")
			}
			request = candidate
		}
	}
	if request == nil {
		return errors.New("scope memory release has no durable scope request")
	}
	for _, member := range request.Cohort.Members {
		if member.TaskID != release.ScopeMemberTaskID {
			continue
		}
		want, found, err := scopeReplanMemoryRelease(*request, member, map[string]GraphMemoryAdmissionActive{release.TaskID: active})
		if err != nil {
			return err
		}
		if !found || !sameCanonical(want, release) {
			return errors.New("scope memory release evidence differs from its durable cohort member")
		}
		return nil
	}
	return errors.New("scope memory release member is absent from its durable request")
}

func validateGraphMemoryCorrectionRelease(s Snapshot, release GraphMemoryAdmissionRelease, active GraphMemoryAdmissionActive) error {
	if release.CorrectionTaskID == "" || release.PriorClaimID == "" || release.PriorScheduleID == "" || release.PriorScheduleDefinitionHash == "" || release.InvocationID == "" || release.ReceiptHead == "" || release.ResultHash == "" {
		return errors.New("semantic correction memory release lacks bound predecessor evidence")
	}
	for _, correction := range s.RoleCorrections {
		if correction.ScheduledTaskID != release.CorrectionTaskID || correction.Invocation.Profile.Role != "writer" || correction.PriorClaim == nil || correction.PriorEvidence == nil {
			continue
		}
		claimID, err := correction.PriorClaim.ID()
		if err != nil {
			return err
		}
		domain, err := runtimeResultDomain(correction.Invocation.Profile.Role)
		if err != nil {
			return err
		}
		resultHash, err := canonical.Hash(domain, correction.Predecessor)
		if err != nil {
			return err
		}
		if correction.PriorTaskID != release.TaskID || claimID != release.PriorClaimID || correction.PriorScheduleID != release.PriorScheduleID || correction.PriorScheduleDefinitionHash != release.PriorScheduleDefinitionHash || correction.PriorClaim.Task.InvocationID != release.InvocationID || correction.ReceiptHead != release.ReceiptHead || resultHash != release.ResultHash || correction.PriorEvidence.Status != taskscheduler.StatusFailed || active.InvocationID != release.InvocationID {
			return errors.New("semantic correction memory release evidence differs")
		}
		return nil
	}
	return errors.New("semantic correction memory release has no durable correction record")
}

func (g *graphMemoryAdmissionGate) signal() {
	g.mu.Lock()
	g.signalLocked()
	g.mu.Unlock()
}

func (g *graphMemoryAdmissionGate) signalLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}

// isolatedMemoryStableMaxWorkers is the run-stable configured maximum used as
// memoryadmission Decision.ConfiguredMaxWorkers. It never changes across
// waves or staged cohorts so durable replay never observes a policy change
// within one run. Version 3 staged cohorts use the configured execution
// ceiling for the whole admitted run; per-wave grants remain bounded by
// isolatedMemoryMaxWorkers. Versions 1 and 2 retain absent-field legacy
// behavior bounding by the frozen selected cohort.
func isolatedMemoryStableMaxWorkers(s Snapshot) int {
	if s.Creation.Execution == nil {
		return 0
	}
	maxWorkers := s.Creation.Execution.EffectiveMaxParallel()
	if maxWorkers > 8 {
		maxWorkers = 8
	}
	if s.GraphIsolationPreparation != nil && s.GraphIsolationPreparation.Version == 3 {
		if maxWorkers < 1 {
			return 0
		}
		return maxWorkers
	}
	if s.GraphIsolationPreparation == nil {
		return 0
	}
	bound := len(s.GraphIsolationPreparation.SelectedTaskIDs)
	if maxWorkers > bound {
		maxWorkers = bound
	}
	if maxWorkers > 8 {
		maxWorkers = 8
	}
	return maxWorkers
}

func isolatedMemoryMaxWorkers(s Snapshot, graphTaskID string) int {
	if s.Creation.Execution == nil || s.GraphIsolationPreparation == nil {
		return 0
	}
	maxWorkers := s.Creation.Execution.EffectiveMaxParallel()
	bound := len(s.GraphIsolationPreparation.SelectedTaskIDs)
	if (s.GraphIsolationPreparation.Version == 2 || s.GraphIsolationPreparation.Version == 3) && len(s.GraphIsolationPreparation.Waves) != 0 {
		bound = 0
		for _, wave := range s.GraphIsolationPreparation.Waves {
			for _, id := range wave {
				if id == graphTaskID {
					bound = len(wave)
					break
				}
			}
			if bound != 0 {
				break
			}
		}
		if bound == 0 {
			return 0
		}
	}
	if maxWorkers > bound {
		maxWorkers = bound
	}
	if maxWorkers > 8 {
		maxWorkers = 8
	}
	return maxWorkers
}
