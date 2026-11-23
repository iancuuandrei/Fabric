package control

import (
	"context"
	"errors"
	"sort"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// scopeReplannedWriterInputs retains the original model invocation as provenance.
// It grants no mutation authority and never projects an old child effect as a
// parent effect. The request and design were validated by controller replay.
func scopeReplannedWriterInputs(s Snapshot) (ScopeReplanRequest, []GraphWriterMember, []fileeffects.Change, error) {
	var empty ScopeReplanRequest
	if !cohortScopeReplanningEnabled(s) || s.Creation.Execution.Validate() != nil || s.State != "IMPLEMENTING" || s.Graph == nil || s.Candidate == nil || s.Workspace == nil || s.WorkspaceOutcome != "CONFIRMED" || s.FileIntent != nil || len(s.ScopeReplans) == 0 || len(s.ScopeReplans) != len(s.ScopeReplanRequests) {
		return empty, nil, nil, errors.New("scope aggregate requires a completed candidate-bound cohort replan")
	}
	if err := autonomousDispatchBlocked(s); err != nil {
		return empty, nil, nil, err
	}
	request := s.ScopeReplanRequests[len(s.ScopeReplanRequests)-1]
	revision := s.ScopeReplans[len(s.ScopeReplans)-1]
	requestID, err := scopeReplanRequestID(request)
	if err != nil || requestID != request.RequestID || revision.Version != 2 || revision.CohortRequestID != request.RequestID || revision.Sequence != request.Sequence || revision.Revision != request.NextRevision || revision.GraphHash != request.GraphHash || !sameCanonical(revision.Graph, request.Graph) || s.Graph.Digest != request.GraphHash || s.Graph.Revision != request.NextRevision || !sameCanonical(s.Graph.Graph, request.Graph) {
		return empty, nil, nil, errors.Join(errors.New("scope aggregate revision or request substituted"), err)
	}
	candidateID, err := s.Candidate.ID()
	sourceID, sourceErr := s.Creation.Repository.ID()
	if err != nil || sourceErr != nil || candidateID != request.CandidateID || revision.CandidateID != candidateID || sourceID != request.SourceID || request.RunID != s.RunID || request.PlanID != s.PlanID {
		return empty, nil, nil, errors.New("scope aggregate candidate or source substituted")
	}
	updates := make(map[string]string, len(request.Updates))
	for _, update := range request.Updates {
		if _, exists := updates[update.TaskID]; exists || update.NextTaskID == "" {
			return empty, nil, nil, errors.New("scope aggregate task mapping duplicated")
		}
		updates[update.TaskID] = update.NextTaskID
	}
	ready, err := graphReadyTasks(s)
	if err != nil {
		return empty, nil, nil, err
	}
	readyTasks := make(map[string]engineeringplan.Task)
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			readyTasks[task.ID] = task
		}
	}
	if len(request.Cohort.Members) == 0 || len(request.Cohort.Members) > 8 || len(readyTasks) != len(request.Cohort.Members) {
		return empty, nil, nil, errors.New("scope aggregate differs from the ready implementation cohort")
	}
	var members []GraphWriterMember
	var changes []fileeffects.Change
	seenPaths := make(map[string]bool)
	for _, original := range request.Cohort.Members {
		resultInvocation := original.Invocation
		if original.ResultInvocation != nil {
			resultInvocation = *original.ResultInvocation
		}
		taskID := original.TaskID
		if next, ok := updates[taskID]; ok {
			taskID = next
		}
		task, ok := readyTasks[taskID]
		if !ok || resultInvocation.ID != original.RuntimeReceipt.InvocationID || original.ResultHash != original.RuntimeReceipt.ResultHash || !graphWriterChangesWithinTask(task, original.Changes) {
			return empty, nil, nil, errors.New("scope aggregate member lacks exact admitted ownership or provenance")
		}
		delete(readyTasks, taskID)
		members = append(members, GraphWriterMember{TaskID: taskID, InvocationID: resultInvocation.ID})
		for _, change := range original.Changes {
			if seenPaths[change.Path] {
				return empty, nil, nil, errors.New("scope aggregate member changes overlap")
			}
			seenPaths[change.Path] = true
			changes = append(changes, change)
		}
	}
	if len(readyTasks) != 0 {
		return empty, nil, nil, errors.New("scope aggregate omits a ready implementation")
	}
	sort.Slice(members, func(i, j int) bool { return members[i].TaskID < members[j].TaskID })
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return request, members, changes, nil
}

func buildScopeReplannedGraphWriterBatch(s Snapshot, beforeFiles []worktree.FileState) (GraphWriterBatchRecord, error) {
	request, members, changes, err := scopeReplannedWriterInputs(s)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	identity := struct {
		RequestID string              `json:"request_id"`
		GraphHash string              `json:"graph_hash"`
		Candidate string              `json:"candidate_id"`
		Members   []GraphWriterMember `json:"members"`
	}{request.RequestID, s.Graph.Digest, request.CandidateID, members}
	nonce, err := canonical.Hash("harness.scope-replanned-writer-aggregate.v1", identity)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	proposal := fileeffects.Proposal{Version: 1, Nonce: "scope-writers-" + nonce, Before: *s.Candidate, BeforeFiles: beforeFiles, Changes: changes}
	proposal.After, err = composeGraphWriterCandidate(proposal.Before, beforeFiles, changes)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	prepared, err := preparedFiles(s, proposal)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	return GraphWriterBatchRecord{Version: 3, GraphDigest: s.Graph.Digest, Revision: s.Graph.Revision, CandidateID: request.CandidateID, Members: members, Prepared: prepared, ScopeReplanRequestID: request.RequestID}, nil
}

// PrepareScopeReplannedGraphWriterBatch captures and preflights a fresh parent
// proposal from already completed model work after its explicit ownership replan.
// It records the proposal only; normal file authorization/application still owns
// the parent effect. No provider or historical child effect is dispatched here.
func PrepareScopeReplannedGraphWriterBatch(ctx context.Context, path string) (GraphWriterBatchRecord, error) {
	s, err := Inspect(path)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	_, _, changes, err := scopeReplannedWriterInputs(s)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	if s.GraphWriterBatch != nil {
		expected, err := buildScopeReplannedGraphWriterBatch(s, s.GraphWriterBatch.Prepared.Proposal.BeforeFiles)
		if err != nil || !sameCanonical(expected, *s.GraphWriterBatch) {
			return GraphWriterBatchRecord{}, errors.Join(errors.New("existing scope aggregate differs"), err)
		}
		return expected, nil
	}
	fresh, err := PrepareFiles(ctx, path, changes)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	latest, err := Inspect(path)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	if latest.Candidate == nil || fresh.Proposal.Before != *latest.Candidate || latest.Graph == nil || s.Graph.Digest != latest.Graph.Digest || s.ControllerHead != latest.ControllerHead {
		return GraphWriterBatchRecord{}, errors.New("scope aggregate changed during parent preparation")
	}
	batch, err := buildScopeReplannedGraphWriterBatch(latest, fresh.Proposal.BeforeFiles)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	if err := recordGraphWriterBatch(path, batch); err != nil {
		return GraphWriterBatchRecord{}, err
	}
	return batch, nil
}

func replayScopeReplannedGraphWriterBatch(s *Snapshot, batch GraphWriterBatchRecord) error {
	if s.GraphWriterBatch != nil || batch.Version != 3 {
		return errors.New("scope aggregate transition duplicated or invalid")
	}
	expected, err := buildScopeReplannedGraphWriterBatch(*s, batch.Prepared.Proposal.BeforeFiles)
	if err != nil || !sameCanonical(expected, batch) {
		return errors.Join(errors.New("scope aggregate differs from completed replan evidence"), err)
	}
	s.GraphWriterBatch = &batch
	return nil
}

// scopeReplannedWriterChanges resolves only a recorded v3 aggregate member.
// Historical invocation identity remains provenance for the newly owned task.
func scopeReplannedWriterChanges(s Snapshot, taskID, invocationID string) ([]fileeffects.Change, bool) {
	if s.GraphWriterBatch == nil || s.GraphWriterBatch.Version != 3 {
		return nil, false
	}
	var request *ScopeReplanRequest
	for index := range s.ScopeReplanRequests {
		if s.ScopeReplanRequests[index].RequestID == s.GraphWriterBatch.ScopeReplanRequestID {
			request = &s.ScopeReplanRequests[index]
		}
	}
	if request == nil {
		return nil, false
	}
	for _, original := range request.Cohort.Members {
		resultInvocation := original.Invocation
		if original.ResultInvocation != nil {
			resultInvocation = *original.ResultInvocation
		}
		mapped := original.TaskID
		for _, update := range request.Updates {
			if update.TaskID == original.TaskID {
				mapped = update.NextTaskID
			}
		}
		if mapped != taskID || invocationID != "" && invocationID != resultInvocation.ID {
			continue
		}
		for _, member := range s.GraphWriterBatch.Members {
			if member.TaskID == taskID && member.InvocationID == resultInvocation.ID {
				return original.Changes, true
			}
		}
	}
	return nil, false
}
