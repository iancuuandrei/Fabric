package control

import (
	"context"
	"errors"
	"reflect"

	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/taskscheduler"
)

func acceptedExplorerResultReference(controllerPath, controllerHead string, snapshot Snapshot, invocation runtime.Invocation, turn *taskscheduler.AgentTurnBinding, exact *ExplorerRecord) (agentcontrol.AcceptedResultReference, bool, error) {
	var zero agentcontrol.AcceptedResultReference
	if turn == nil {
		return zero, false, nil
	}
	events, err := agentToolJournalPrefix(controllerPath, controllerHead)
	if err != nil {
		return zero, false, err
	}
	prefix, err := Replay(events)
	if err != nil || !reflect.DeepEqual(prefix, snapshot) {
		return zero, false, errors.Join(errors.New("scheduled exploration controller prefix differs"), err)
	}
	if err := validateControllerJournalPath(controllerPath, prefix.Creation, prefix.RunID); err != nil {
		return zero, false, err
	}
	var record *ExplorerRecord
	for index := range snapshot.Explorations {
		candidate := &snapshot.Explorations[index]
		if candidate.Invocation.ID != invocation.ID {
			continue
		}
		if record != nil {
			return zero, false, errors.New("scheduled exploration result is ambiguous")
		}
		record = candidate
	}
	if record == nil {
		return zero, false, nil
	}
	if exact != nil && !sameCanonical(*record, *exact) {
		return zero, false, errors.New("scheduled exploration result changed before indexing")
	}
	dispatch, ok := snapshot.AgentDispatch[invocation.ID]
	if !ok || dispatch.Admission.AgentTurn == nil || *dispatch.Admission.AgentTurn != *turn || dispatch.Admission.Invocation != invocation || dispatch.Observation == nil || dispatch.Observation.Status != agenttree.StatusSucceeded {
		return zero, false, errors.New("scheduled exploration lacks exact successful agent dispatch")
	}
	admissionID, err := dispatch.Admission.ID()
	if err != nil || dispatch.Observation.AdmissionID != admissionID || dispatch.Observation.InvocationID != invocation.ID {
		return zero, false, errors.Join(errors.New("scheduled exploration admission identity differs"), err)
	}
	resultHash, err := canonical.Hash("harness.explorer-result.v1", record.Result)
	if err != nil || dispatch.Observation.ResultSHA256 != resultHash {
		return zero, false, errors.Join(errors.New("scheduled exploration result identity differs"), err)
	}
	eventHash := ""
	for _, event := range events {
		if event.Kind != "explorer.recorded" {
			continue
		}
		var observed ExplorerRecord
		if err := canonical.Decode(event.Payload, &observed); err != nil {
			return zero, false, err
		}
		if observed.Invocation.ID != invocation.ID {
			continue
		}
		if !sameCanonical(observed, *record) || eventHash != "" {
			return zero, false, errors.New("scheduled exploration controller event differs")
		}
		eventHash = event.Hash
	}
	if eventHash == "" {
		return zero, false, errors.New("scheduled exploration controller event unavailable")
	}
	return agentcontrol.AcceptedResultReference{
		Version: 1, TreeID: snapshot.RunID,
		ParentAgentID: turn.ParentAgentID, AgentID: turn.AgentID,
		TurnID: turn.TurnID, TurnSequence: turn.TurnSequence,
		InvocationID: invocation.ID, AdmissionID: admissionID,
		ControllerEventHash: eventHash, Role: agentcontrol.AcceptedResultRoleExplorer,
		ResultKind: agentcontrol.AcceptedResultKindExploration, ResultSHA256: resultHash,
	}, true, nil
}

func acceptedExplorerResultIndexed(controllerPath, controllerHead string, snapshot Snapshot, invocation runtime.Invocation, turn *taskscheduler.AgentTurnBinding) (bool, error) {
	reference, found, err := acceptedExplorerResultReference(controllerPath, controllerHead, snapshot, invocation, turn, nil)
	if err != nil || !found {
		return false, err
	}
	wantID, err := reference.ID()
	state, inspectErr := agentcontrol.Inspect(controllerPath + ".agent-control")
	if err != nil || inspectErr != nil {
		return false, errors.Join(err, inspectErr)
	}
	for _, result := range state.Results {
		if result.Reference.TurnID != turn.TurnID {
			continue
		}
		if result.ResultID != wantID || result.Reference != reference {
			return false, errors.New("accepted scheduled exploration index differs")
		}
		return true, nil
	}
	return false, nil
}

func ensureAcceptedExplorerResult(ctx context.Context, controllerPath string, claim taskscheduler.Claim, invocation runtime.Invocation, exact *ExplorerRecord) (bool, error) {
	if claim.Task.Operation != taskscheduler.OperationExplorer || claim.AgentTurn == nil {
		return false, nil
	}
	events, err := journal.Read(controllerPath)
	if err != nil {
		return false, err
	}
	if len(events) == 0 {
		return false, errors.New("scheduled exploration controller is empty")
	}
	snapshot, err := Replay(events)
	if err != nil {
		return false, err
	}
	if err := validateControllerJournalPath(controllerPath, snapshot.Creation, snapshot.RunID); err != nil {
		return false, err
	}
	controllerHead := events[len(events)-1].Hash
	reference, found, err := acceptedExplorerResultReference(controllerPath, controllerHead, snapshot, invocation, claim.AgentTurn, exact)
	if err != nil || !found {
		return false, err
	}
	service, err := agentcontrol.Bind(controllerPath+".agent-tree", controllerPath+".agent-control")
	if err != nil {
		return false, err
	}
	indexed, err := service.IndexAcceptedResult(ctx, reference)
	wantID, idErr := reference.ID()
	if err != nil || idErr != nil || indexed.ResultID != wantID || indexed.Reference != reference {
		return false, errors.Join(errors.New("scheduled exploration result index unavailable"), err, idErr)
	}
	return true, nil
}

func scheduledEvidenceWithAcceptedResult(controllerPath string, snapshot Snapshot, head string, invocation runtime.Invocation, task taskscheduler.TaskSpec, turn *taskscheduler.AgentTurnBinding) (taskscheduler.Evidence, error) {
	evidence := scheduledEvidence(snapshot, head, invocation, task)
	if task.Operation != taskscheduler.OperationExplorer || turn == nil || evidence.Status != taskscheduler.StatusSucceeded {
		return evidence, nil
	}
	indexed, err := acceptedExplorerResultIndexed(controllerPath, head, snapshot, invocation, turn)
	if err != nil {
		return taskscheduler.Evidence{}, err
	}
	if !indexed {
		// The semantic controller result exists but its body-free child index is
		// absent. UNKNOWN keeps scheduler recovery on the receipt-only reconcile
		// seam; Running would bypass that repair path.
		evidence.Status = taskscheduler.StatusUnknown
	}
	return evidence, nil
}
