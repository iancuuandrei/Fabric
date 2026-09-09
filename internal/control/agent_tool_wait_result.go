package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/taskscheduler"
	"harness.local/engorch/internal/toolbridge"
)

const waitAgentDeliveryVersion = 1

var errAgentToolResultTooLarge = errors.New("agent tool result exceeds canonical or wire bound")

type waitAgentAcceptedResult struct {
	ActivitySequence int                         `json:"activity_sequence"`
	Result           agentcontrol.AcceptedResult `json:"result"`
	Exploration      Exploration                 `json:"exploration"`
}

type waitAgentResultOmission struct {
	ActivitySequence int    `json:"activity_sequence"`
	ResultID         string `json:"result_id"`
	Reason           string `json:"reason"`
}

type waitAgentOutput struct {
	DeliveryVersion int                       `json:"delivery_version,omitempty"`
	Activities      []agentcontrol.Activity   `json:"activities"`
	AcceptedResults []waitAgentAcceptedResult `json:"accepted_results,omitempty"`
	ResultOmissions []waitAgentResultOmission `json:"result_omissions,omitempty"`
	TimedOut        bool                      `json:"timed_out"`
	Truncated       bool                      `json:"truncated,omitempty"`
	NextAfter       int                       `json:"next_after_sequence,omitempty"`
}

type waitAgentPrefixEvidence struct {
	sources          agentToolSourceReceipts
	controllerEvents []journal.Event
	controller       Snapshot
	tree             agenttree.Snapshot
	control          agentcontrol.Snapshot
	scheduled        taskscheduler.Snapshot
}

type waitAgentResolvedActivity struct {
	accepted *waitAgentAcceptedResult
	omission *waitAgentResultOmission
}

func (p *agentToolProjectionState) waitReceipted(ctx context.Context, call toolbridge.Call) (toolbridge.Result, error) {
	args, page, timedOut, err := p.waitPage(ctx, call.Arguments)
	if err != nil {
		return toolbridge.Result{}, err
	}
	evidence, err := p.captureWaitEvidence(page.JournalHead)
	if err != nil {
		return toolbridge.Result{}, err
	}
	_, result, err := p.projectWaitResult(call, args, page.Activities, timedOut, evidence)
	if err != nil {
		return toolbridge.Result{}, err
	}
	return result, nil
}

func (p *agentToolProjectionState) captureWaitEvidence(controlHead string) (waitAgentPrefixEvidence, error) {
	var result waitAgentPrefixEvidence
	if controlHead == "" {
		return result, errors.New("wait agent control prefix required")
	}
	controllerEvents, err := journal.Read(p.controllerPath)
	if err != nil || len(controllerEvents) == 0 {
		return result, errors.Join(errors.New("wait controller prefix unavailable"), err)
	}
	controller, err := Replay(controllerEvents)
	if err != nil {
		return result, err
	}
	treeEvents, err := journal.Read(p.binding.JournalPath)
	if err != nil || len(treeEvents) == 0 {
		return result, errors.Join(errors.New("wait tree prefix unavailable"), err)
	}
	tree, err := replayAgentToolTreePrefix(treeEvents)
	if err != nil {
		return result, err
	}
	controlEvents, err := agentToolJournalPrefix(p.controllerPath+".agent-control", controlHead)
	if err != nil {
		return result, err
	}
	control, err := agentcontrol.Replay(controlEvents)
	if err != nil {
		return result, err
	}
	schedulerEvents, err := journal.Read(p.schedulerPath)
	if err != nil || len(schedulerEvents) == 0 {
		return result, errors.Join(errors.New("wait scheduler prefix unavailable"), err)
	}
	scheduled, err := taskscheduler.Replay(schedulerEvents)
	if err != nil {
		return result, err
	}
	verifier := agentToolReceiptVerifier{controllerPath: p.controllerPath, schedulerPath: p.schedulerPath, binding: p.binding, claimID: p.claimID, controllerHead: p.controllerHead, scheduleID: p.scheduleID}
	if err := verifier.validateCallerPrefixes(controller, tree, scheduled); err != nil {
		return result, err
	}
	if control.TreeID != tree.TreeID {
		return result, errors.New("wait control prefix tree differs")
	}
	sources := agentToolSourceReceipts{
		ControllerHead:   controllerEvents[len(controllerEvents)-1].Hash,
		TreeHead:         treeEvents[len(treeEvents)-1].Hash,
		ControlHead:      controlHead,
		ActivityPageHead: controlHead,
		SchedulerHead:    schedulerEvents[len(schedulerEvents)-1].Hash,
		ScheduleID:       p.scheduleID, ClaimID: p.claimID,
		ClaimControllerHead: p.controllerHead, AdmissionID: p.binding.AdmissionID,
	}
	result = waitAgentPrefixEvidence{sources: sources, controllerEvents: controllerEvents, controller: controller, tree: tree, control: control, scheduled: scheduled}
	return result, nil
}

func (p *agentToolProjectionState) projectWaitResult(call toolbridge.Call, args waitAgentArgs, activities []agentcontrol.Activity, timedOut bool, evidence waitAgentPrefixEvidence) (waitAgentOutput, toolbridge.Result, error) {
	target, ok := agentNodeByID(evidence.tree, args.AgentID)
	if !ok || target.AgentID != p.binding.Node.AgentID && target.ParentAgentID != p.binding.Node.AgentID {
		return waitAgentOutput{}, toolbridge.Result{}, errors.New("wait target differs at selected prefix")
	}
	if activities == nil {
		activities = []agentcontrol.Activity{}
	}
	resolved, err := p.resolveWaitActivities(activities, evidence)
	if err != nil {
		return waitAgentOutput{}, toolbridge.Result{}, err
	}
	if timedOut {
		if len(activities) != 0 {
			return waitAgentOutput{}, toolbridge.Result{}, errors.New("wait timeout contains activity")
		}
		output := waitAgentOutput{DeliveryVersion: waitAgentDeliveryVersion, Activities: []agentcontrol.Activity{}, TimedOut: true}
		result, err := p.receiptedResultWithSources(call, output, evidence.sources)
		if err != nil {
			return waitAgentOutput{}, toolbridge.Result{}, err
		}
		if err := boundedAgentToolWire(call, result); err != nil {
			return waitAgentOutput{}, toolbridge.Result{}, err
		}
		return output, result, nil
	}
	if len(activities) == 0 {
		return waitAgentOutput{}, toolbridge.Result{}, errors.New("wait returned without activity")
	}
	var selected waitAgentOutput
	var selectedResult toolbridge.Result
	for count := 1; count <= len(activities); count++ {
		candidate := waitAgentOutput{DeliveryVersion: waitAgentDeliveryVersion, Activities: append([]agentcontrol.Activity(nil), activities[:count]...)}
		for index := 0; index < count; index++ {
			if resolved[index].accepted != nil {
				candidate.AcceptedResults = append(candidate.AcceptedResults, *resolved[index].accepted)
			}
			if resolved[index].omission != nil {
				candidate.ResultOmissions = append(candidate.ResultOmissions, *resolved[index].omission)
			}
		}
		candidate.Truncated = count < len(activities)
		if candidate.Truncated {
			candidate.NextAfter = candidate.Activities[len(candidate.Activities)-1].Sequence
		}
		encoded, encodeErr := p.receiptedResultWithSources(call, candidate, evidence.sources)
		if encodeErr == nil {
			encodeErr = boundedAgentToolWire(call, encoded)
		}
		if errors.Is(encodeErr, errAgentToolResultTooLarge) {
			break
		}
		if encodeErr != nil {
			return waitAgentOutput{}, toolbridge.Result{}, encodeErr
		}
		selected, selectedResult = candidate, encoded
	}
	if len(selected.Activities) == 0 {
		return waitAgentOutput{}, toolbridge.Result{}, errors.New("one accepted wait activity exceeds response bound")
	}
	if len(selected.Activities) < len(activities) && !selected.Truncated {
		return waitAgentOutput{}, toolbridge.Result{}, errors.New("wait truncation marker unavailable")
	}
	return selected, selectedResult, nil
}

func (p *agentToolProjectionState) resolveWaitActivities(activities []agentcontrol.Activity, evidence waitAgentPrefixEvidence) ([]waitAgentResolvedActivity, error) {
	resolved := make([]waitAgentResolvedActivity, len(activities))
	for index, activity := range activities {
		if activity.Kind != "result" {
			continue
		}
		accepted, err := exactAcceptedResult(evidence.control, activity.ResultID)
		if err != nil {
			return nil, err
		}
		if err := p.validateWaitAcceptedResult(activity, accepted, evidence); err != nil {
			return nil, err
		}
		if accepted.Reference.AgentID != p.binding.Node.AgentID && accepted.Reference.ParentAgentID != p.binding.Node.AgentID {
			resolved[index].omission = &waitAgentResultOmission{ActivitySequence: activity.Sequence, ResultID: accepted.ResultID, Reason: "outside-caller-result-scope"}
			continue
		}
		record, err := explorerRecordAtEvent(evidence.controllerEvents, accepted.Reference.ControllerEventHash)
		if err != nil {
			return nil, err
		}
		var exploration Exploration
		if err := canonical.Decode([]byte(record.Result.Output), &exploration); err != nil {
			return nil, errors.Join(errors.New("accepted exploration body invalid"), err)
		}
		resolved[index].accepted = &waitAgentAcceptedResult{ActivitySequence: activity.Sequence, Result: accepted, Exploration: exploration}
	}
	return resolved, nil
}

func exactAcceptedResult(state agentcontrol.Snapshot, resultID string) (agentcontrol.AcceptedResult, error) {
	var found *agentcontrol.AcceptedResult
	for index := range state.Results {
		candidate := &state.Results[index]
		if candidate.ResultID != resultID {
			continue
		}
		if found != nil {
			return agentcontrol.AcceptedResult{}, errors.New("accepted result identity is ambiguous")
		}
		found = candidate
	}
	if found == nil {
		return agentcontrol.AcceptedResult{}, errors.New("accepted result reference unavailable")
	}
	wantID, err := found.Reference.ID()
	if err != nil || wantID != found.ResultID {
		return agentcontrol.AcceptedResult{}, errors.Join(errors.New("accepted result identity differs"), err)
	}
	return *found, nil
}

func (p *agentToolProjectionState) validateWaitAcceptedResult(activity agentcontrol.Activity, accepted agentcontrol.AcceptedResult, evidence waitAgentPrefixEvidence) error {
	ref := accepted.Reference
	if activity.AgentID != ref.AgentID && activity.AgentID != ref.ParentAgentID || activity.TurnID != ref.TurnID || activity.TurnSequence != ref.TurnSequence || activity.Status != agenttree.StatusSucceeded || activity.ResultSHA256 != ref.ResultSHA256 || activity.ResultID != accepted.ResultID || ref.TreeID != evidence.tree.TreeID {
		return errors.New("accepted result activity differs")
	}
	child, childOK := agentNodeByID(evidence.tree, ref.AgentID)
	parent, parentOK := agentNodeByID(evidence.tree, ref.ParentAgentID)
	if !childOK || !parentOK || child.ParentAgentID != parent.AgentID || child.Role != agentcontrol.AcceptedResultRoleExplorer {
		return errors.New("accepted result topology differs")
	}
	dynamic, dynamicOK := scheduledDynamicTurn(evidence.scheduled, ref.TurnID)
	if !dynamicOK || dynamic.ParentAgentID != ref.ParentAgentID || dynamic.AgentID != ref.AgentID || dynamic.TurnSequence != ref.TurnSequence || dynamic.Task.InvocationID != ref.InvocationID || dynamic.Task.Operation != taskscheduler.OperationExplorer || dynamic.Task.ControllerPath != p.controllerPath || dynamic.Task.RunID != evidence.controller.RunID {
		return errors.New("accepted result scheduled turn differs")
	}
	dispatch, dispatchOK := evidence.controller.AgentDispatch[ref.InvocationID]
	admissionID, admissionErr := dispatch.Admission.ID()
	if !dispatchOK || admissionErr != nil || admissionID != ref.AdmissionID || dispatch.Admission.AgentTurn == nil || dispatch.Admission.AgentTurn.ParentAgentID != ref.ParentAgentID || dispatch.Admission.AgentTurn.AgentID != ref.AgentID || dispatch.Admission.AgentTurn.TurnID != ref.TurnID || dispatch.Admission.AgentTurn.TurnSequence != ref.TurnSequence || dispatch.Observation == nil || dispatch.Observation.Status != agenttree.StatusSucceeded || dispatch.Observation.ResultSHA256 != ref.ResultSHA256 {
		return errors.Join(errors.New("accepted result controller admission differs"), admissionErr)
	}
	record, err := explorerRecordAtEvent(evidence.controllerEvents, ref.ControllerEventHash)
	if err != nil {
		return err
	}
	resultHash, err := canonical.Hash("harness.explorer-result.v1", record.Result)
	if err != nil || record.Invocation.ID != ref.InvocationID || record.Question != dynamic.Task.Input || resultHash != ref.ResultSHA256 || ref.Role != agentcontrol.AcceptedResultRoleExplorer || ref.ResultKind != agentcontrol.AcceptedResultKindExploration {
		return errors.Join(errors.New("accepted result controller event differs"), err)
	}
	return nil
}

func explorerRecordAtEvent(events []journal.Event, eventHash string) (ExplorerRecord, error) {
	for _, event := range events {
		if event.Hash != eventHash {
			continue
		}
		if event.Kind != "explorer.recorded" {
			return ExplorerRecord{}, errors.New("accepted result event kind differs")
		}
		var record ExplorerRecord
		if err := canonical.Decode(event.Payload, &record); err != nil {
			return ExplorerRecord{}, err
		}
		return record, nil
	}
	return ExplorerRecord{}, errors.New("accepted result controller event unavailable at prefix")
}

func boundedAgentToolWire(call toolbridge.Call, result toolbridge.Result) error {
	wire, err := toolbridge.EncodeToolResult(call.RequestID, result)
	if err != nil {
		return err
	}
	if len(wire) > contextmcp.MaxWireResponseBytes {
		return errAgentToolResultTooLarge
	}
	return nil
}

func agentToolCanonicalBytes(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(raw) > canonical.MaxBytes {
		return nil, errAgentToolResultTooLarge
	}
	normal, err := canonical.Normalize(raw)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(raw, normal) {
		return normal, nil
	}
	return raw, nil
}
