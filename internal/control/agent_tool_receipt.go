package control

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/taskscheduler"
	"harness.local/engorch/internal/toolbridge"
)

// agentToolReceiptVerifier validates one successful projected result against
// immutable journal prefixes. It performs no append, network or runtime work.
type agentToolReceiptVerifier struct {
	controllerPath string
	schedulerPath  string
	binding        agentDispatchBinding
	claimID        string
	controllerHead string
	scheduleID     string
}

func newAgentToolReceiptVerifier(controllerPath, schedulerPath string, binding agentDispatchBinding) (*agentToolReceiptVerifier, error) {
	if binding.AgentTurn != nil {
		turn := *binding.AgentTurn
		binding.AgentTurn = &turn
	}
	verifier := &agentToolReceiptVerifier{controllerPath: controllerPath, schedulerPath: schedulerPath, binding: binding}
	controllerEvents, err := journal.Read(controllerPath)
	if err != nil {
		return nil, err
	}
	controller, err := Replay(controllerEvents)
	if err != nil {
		return nil, err
	}
	dispatch, ok := controller.AgentDispatch[binding.InvocationID]
	admissionID, idErr := dispatch.Admission.ID()
	if binding.AgentTurn == nil || binding.ControllerPath != controllerPath || binding.JournalPath != controllerPath+".agent-tree" || !ok || idErr != nil || admissionID != binding.AdmissionID || dispatch.Admission.AgentTurn == nil || *dispatch.Admission.AgentTurn != *binding.AgentTurn || !sameAgentToolNode(dispatch.Admission.Node, binding.Node) {
		return nil, errors.Join(errors.New("invalid agent tool receipt caller"), idErr)
	}
	treeEvents, err := journal.Read(binding.JournalPath)
	if err != nil {
		return nil, err
	}
	tree, err := replayAgentToolTreePrefix(treeEvents)
	caller, found := agentNodeByID(tree, binding.Node.AgentID)
	if err != nil || tree.TreeID != controller.RunID || !found || !sameAgentToolNode(caller, binding.Node) || caller.Role != "explorer" || caller.Authority != agenttree.AuthorityReadOnly {
		return nil, errors.Join(errors.New("invalid agent tool receipt topology"), err)
	}
	schedulerEvents, err := journal.Read(schedulerPath)
	if err != nil {
		return nil, err
	}
	scheduled, err := taskscheduler.Replay(schedulerEvents)
	if err != nil {
		return nil, err
	}
	dynamic, found := scheduledDynamicTurn(scheduled, binding.AgentTurn.TurnID)
	state, hasState := scheduled.Tasks[binding.AgentTurn.TurnID]
	if !found || !hasState || state.Claim == nil || state.Claim.AgentTurn == nil || *state.Claim.AgentTurn != *binding.AgentTurn || dynamic.ParentAgentID != binding.AgentTurn.ParentAgentID || dynamic.AgentID != binding.AgentTurn.AgentID || dynamic.Task.ControllerPath != controllerPath || dynamic.Task.RunID != controller.RunID || dynamic.Task.InvocationID != binding.InvocationID {
		return nil, errors.New("invalid agent tool receipt claim")
	}
	claimID, err := state.Claim.ID()
	if err != nil {
		return nil, err
	}
	verifier.claimID = claimID
	verifier.controllerHead = state.Claim.ControllerHead
	verifier.scheduleID = scheduled.ScheduleID
	return verifier, nil
}

type agentToolResultEnvelope struct {
	Result    json.RawMessage         `json:"result"`
	Sources   agentToolSourceReceipts `json:"source_receipts"`
	ReceiptID string                  `json:"receipt_id"`
}

// Verify checks the exact call/result pair against the authoritative state as
// it existed at each recorded head. Valid later appends do not alter a prefix.
func (v *agentToolReceiptVerifier) Verify(call toolbridge.Call, result toolbridge.Result) error {
	if v == nil || result.IsError || result.Error != nil {
		return errors.New("successful agent tool result required")
	}
	call, err := normalizeAgentToolCall(call)
	if err != nil {
		return err
	}
	normal, err := canonical.Normalize(result.JSON)
	if err != nil || !bytes.Equal(normal, result.JSON) {
		return errors.Join(errors.New("noncanonical agent tool result"), err)
	}
	var envelope agentToolResultEnvelope
	if canonical.Decode(result.JSON, &envelope) != nil || len(envelope.Result) == 0 {
		return errors.New("invalid agent tool result envelope")
	}
	if err := v.validateSources(call.Tool, envelope.Sources); err != nil {
		return err
	}
	controllerEvents, err := agentToolJournalPrefix(v.controllerPath, envelope.Sources.ControllerHead)
	if err != nil {
		return err
	}
	controller, err := Replay(controllerEvents)
	if err != nil {
		return err
	}
	treeEvents, err := agentToolJournalPrefix(v.binding.JournalPath, envelope.Sources.TreeHead)
	if err != nil {
		return err
	}
	tree, err := replayAgentToolTreePrefix(treeEvents)
	if err != nil {
		return err
	}
	controlEvents, err := agentToolJournalPrefix(v.controllerPath+".agent-control", envelope.Sources.ControlHead)
	if err != nil {
		return err
	}
	controlState, err := agentcontrol.Replay(controlEvents)
	if err != nil {
		return err
	}
	schedulerEvents, err := agentToolJournalPrefix(v.schedulerPath, envelope.Sources.SchedulerHead)
	if err != nil {
		return err
	}
	scheduled, err := taskscheduler.Replay(schedulerEvents)
	if err != nil {
		return err
	}
	if err := v.validateCallerPrefixes(controller, tree, scheduled); err != nil {
		return err
	}
	if controlState.TreeID != tree.TreeID {
		return errors.New("agent tool control prefix tree differs")
	}
	if err := v.verifyOperation(call, envelope.Result, controllerEvents, controller, tree, controlState, scheduled, envelope.Sources, envelope.Sources.ActivityPageHead != ""); err != nil {
		return err
	}
	wantReceipt, err := canonical.Hash("harness.agent-tool-source-receipt.v1", struct {
		Tool         string                  `json:"tool"`
		RequestID    json.RawMessage         `json:"request_id"`
		Arguments    json.RawMessage         `json:"arguments"`
		AgentID      string                  `json:"agent_id"`
		InvocationID string                  `json:"invocation_id"`
		TurnID       string                  `json:"turn_id"`
		Result       json.RawMessage         `json:"result"`
		Sources      agentToolSourceReceipts `json:"sources"`
	}{call.Tool, call.RequestID, call.Arguments, v.binding.Node.AgentID, v.binding.InvocationID, v.binding.AgentTurn.TurnID, envelope.Result, envelope.Sources})
	if err != nil || envelope.ReceiptID != wantReceipt {
		return errors.Join(errors.New("agent tool receipt identity differs"), err)
	}
	return nil
}

func (v *agentToolReceiptVerifier) validateSources(tool string, s agentToolSourceReceipts) error {
	values := []string{s.ControllerHead, s.TreeHead, s.ControlHead, s.SchedulerHead, s.ScheduleID, s.ClaimID, s.ClaimControllerHead, s.AdmissionID}
	for _, value := range values {
		if len(value) != sha256.Size*2 {
			return errors.New("invalid agent tool source identity")
		}
		if _, err := hex.DecodeString(value); err != nil {
			return errors.New("invalid agent tool source identity")
		}
	}
	if s.ScheduleID != v.scheduleID || s.ClaimID != v.claimID || s.ClaimControllerHead != v.controllerHead || s.AdmissionID != v.binding.AdmissionID {
		return errors.New("agent tool source caller binding differs")
	}
	if s.ActivityPageHead != "" {
		if tool != "wait_agent" || s.ActivityPageHead != s.ControlHead || safepath.RequireDigest(s.ActivityPageHead) != nil {
			return errors.New("agent tool activity page prefix differs")
		}
	} else if tool == "wait_agent" {
		// An omitted marker is retained for historical timing-only timeout
		// receipts created before wait selected its control prefix atomically.
	}
	return nil
}

func (v *agentToolReceiptVerifier) validateCallerPrefixes(controller Snapshot, tree agenttree.Snapshot, scheduled taskscheduler.Snapshot) error {
	dispatch, ok := controller.AgentDispatch[v.binding.InvocationID]
	id, idErr := dispatch.Admission.ID()
	caller, callerOK := agentNodeByID(tree, v.binding.Node.AgentID)
	dynamic, dynamicOK := scheduledDynamicTurn(scheduled, v.binding.AgentTurn.TurnID)
	state, stateOK := scheduled.Tasks[v.binding.AgentTurn.TurnID]
	if !ok || idErr != nil || id != v.binding.AdmissionID || dispatch.Admission.AgentTurn == nil || *dispatch.Admission.AgentTurn != *v.binding.AgentTurn || !callerOK || !sameAgentToolNode(caller, v.binding.Node) || tree.TreeID != controller.RunID || !dynamicOK || !stateOK || state.Claim == nil || state.Claim.AgentTurn == nil || *state.Claim.AgentTurn != *v.binding.AgentTurn || dynamic.AgentID != v.binding.Node.AgentID || scheduled.ScheduleID != v.scheduleID {
		return errors.Join(errors.New("agent tool source prefixes changed caller binding"), idErr)
	}
	claimID, err := state.Claim.ID()
	if err != nil || claimID != v.claimID || state.Claim.ControllerHead != v.controllerHead {
		return errors.Join(errors.New("agent tool source claim differs"), err)
	}
	return nil
}

func (v *agentToolReceiptVerifier) verifyOperation(call toolbridge.Call, raw json.RawMessage, controllerEvents []journal.Event, controller Snapshot, tree agenttree.Snapshot, controlState agentcontrol.Snapshot, scheduled taskscheduler.Snapshot, sources agentToolSourceReceipts, exactActivityPage bool) error {
	switch call.Tool {
	case "spawn_agent":
		return v.verifySpawn(call.Arguments, raw, controller, tree, scheduled)
	case "list_agents":
		return v.verifyList(call.Arguments, raw, tree)
	case "wait_agent":
		return v.verifyWait(call, raw, controllerEvents, controller, tree, controlState, scheduled, sources, exactActivityPage)
	case "send_message":
		return v.verifyMessage(call.Arguments, raw, tree, controlState, false)
	case "followup_task":
		return v.verifyFollowUp(call.Arguments, raw, controller, tree, controlState, scheduled)
	case "interrupt_agent":
		return v.verifyInterrupt(call.Arguments, raw, tree, controlState, scheduled)
	default:
		return errors.New("unknown agent tool receipt operation")
	}
}

func (v *agentToolReceiptVerifier) verifySpawn(arguments, raw json.RawMessage, controller Snapshot, tree agenttree.Snapshot, scheduled taskscheduler.Snapshot) error {
	if err := v.verifyNewWorkCaller(controller, scheduled); err != nil {
		return err
	}
	var args spawnAgentArgs
	var output struct {
		AgentID       string `json:"agent_id"`
		ParentAgentID string `json:"parent_agent_id"`
		TurnID        string `json:"turn_id"`
		TaskID        string `json:"task_id"`
		TurnSequence  int    `json:"turn_sequence"`
	}
	if canonical.Decode(arguments, &args) != nil || canonical.Decode(raw, &output) != nil || !boundedAgentToolText(args.Name, 64) || !boundedAgentToolText(args.Question, 4096) || !boundedAgentToolText(args.Nonce, 128) {
		return errors.New("invalid spawn receipt shape")
	}
	turnID, err := agentcontrol.ExplorerSpawnTurnID(controller.RunID, v.binding.Node.AgentID, args.Name, args.Question, args.Nonce)
	node, nodeOK := agentNodeByID(tree, output.AgentID)
	dynamic, dynamicOK := scheduledDynamicTurn(scheduled, output.TurnID)
	base, invocationErr := explorerInvocation(controller, args.Question)
	invocation, scopeErr := scheduledTurnInvocation(base, taskscheduler.OperationExplorer, turnID)
	contextHash, contextErr := access.InputID(invocation.Input)
	wantNodeID, nodeIDErr := agentToolNodeID(controller.RunID, v.binding.Node.AgentID, args.Name, "explorer", agenttree.AuthorityReadOnly, invocation.ID, contextHash)
	if err != nil || invocationErr != nil || scopeErr != nil || contextErr != nil || nodeIDErr != nil || output.AgentID != wantNodeID || output.ParentAgentID != v.binding.Node.AgentID || output.TurnID != turnID || output.TaskID != turnID || !nodeOK || node.ParentAgentID != v.binding.Node.AgentID || node.Name != args.Name || node.Role != "explorer" || node.Authority != agenttree.AuthorityReadOnly || node.InvocationID != invocation.ID || node.ContextSHA256 != contextHash || !dynamicOK || dynamic.AgentID != node.AgentID || dynamic.ParentAgentID != v.binding.Node.AgentID || dynamic.TurnSequence != output.TurnSequence || dynamic.Task.ID != turnID || dynamic.Task.RunID != controller.RunID || dynamic.Task.Operation != taskscheduler.OperationExplorer || dynamic.Task.Input != args.Question || dynamic.Task.InvocationID != invocation.ID || dynamic.Task.ControllerPath != v.controllerPath {
		return errors.Join(errors.New("spawn receipt evidence differs"), err, invocationErr, scopeErr, contextErr, nodeIDErr)
	}
	return nil
}

func (v *agentToolReceiptVerifier) verifyMessage(arguments, raw json.RawMessage, tree agenttree.Snapshot, controlState agentcontrol.Snapshot, wake bool) error {
	var args messageAgentArgs
	if wake {
		var follow followupTaskArgs
		if canonical.Decode(arguments, &follow) != nil {
			return errors.New("invalid follow-up arguments")
		}
		args = messageAgentArgs(follow)
	} else if canonical.Decode(arguments, &args) != nil {
		return errors.New("invalid message arguments")
	}
	if !boundedAgentToolText(args.Nonce, 128) || !boundedAgentToolText(args.Body, agentcontrol.MaximumBodyBytes) {
		return errors.New("invalid message arguments")
	}
	var output struct {
		Message agenttree.Message `json:"message"`
	}
	if canonical.Decode(raw, &output) != nil {
		return errors.New("invalid message receipt shape")
	}
	wantBody := sha256.Sum256([]byte(args.Body))
	wantMessageID, idErr := canonical.Hash("harness.agentcontrol-message.v1", struct {
		TreeID      string `json:"tree_id"`
		FromAgentID string `json:"from_agent_id"`
		ToAgentID   string `json:"to_agent_id"`
		Nonce       string `json:"nonce"`
		BodySHA256  string `json:"body_sha256"`
		Wake        bool   `json:"wake"`
	}{tree.TreeID, v.binding.Node.AgentID, args.AgentID, args.Nonce, hex.EncodeToString(wantBody[:]), wake})
	target, targetOK := agentNodeByID(tree, args.AgentID)
	caller, callerOK := agentNodeByID(tree, v.binding.Node.AgentID)
	allowed := targetOK && callerOK && (target.AgentID == caller.ParentAgentID || target.ParentAgentID == caller.AgentID)
	if wake {
		allowed = targetOK && callerOK && target.ParentAgentID == caller.AgentID && target.Role == "explorer" && target.Authority == agenttree.AuthorityReadOnly
	}
	if idErr != nil || !allowed || output.Message.MessageID != wantMessageID || output.Message.FromAgentID != v.binding.Node.AgentID || output.Message.ToAgentID != args.AgentID || output.Message.Wake != wake || output.Message.BodySHA256 != hex.EncodeToString(wantBody[:]) {
		return errors.Join(errors.New("message receipt envelope differs"), idErr)
	}
	if !agentToolHasTreeMessage(tree, output.Message) {
		return errors.New("message tree evidence unavailable")
	}
	for _, record := range controlState.Messages {
		if record.Message == output.Message && record.Body == args.Body && record.Bytes == len(args.Body) {
			return nil
		}
	}
	return errors.New("message body evidence unavailable")
}

func (v *agentToolReceiptVerifier) verifyFollowUp(arguments, raw json.RawMessage, controller Snapshot, tree agenttree.Snapshot, controlState agentcontrol.Snapshot, scheduled taskscheduler.Snapshot) error {
	if err := v.verifyNewWorkCaller(controller, scheduled); err != nil {
		return err
	}
	var args followupTaskArgs
	var output struct {
		Message      agenttree.Message `json:"message"`
		TurnID       string            `json:"turn_id"`
		TurnSequence int               `json:"turn_sequence"`
		TaskID       string            `json:"task_id"`
	}
	if canonical.Decode(arguments, &args) != nil || canonical.Decode(raw, &output) != nil || !boundedAgentToolText(args.Nonce, 128) || !boundedAgentToolText(args.Body, 4096) {
		return errors.New("invalid follow-up receipt shape")
	}
	messageOnly, err := canonical.Bytes(struct {
		Message agenttree.Message `json:"message"`
	}{output.Message})
	if err != nil || v.verifyMessage(arguments, messageOnly, tree, controlState, true) != nil {
		return errors.New("follow-up message evidence differs")
	}
	request := agentcontrol.MessageRequest{FromAgentID: v.binding.Node.AgentID, ToAgentID: args.AgentID, Nonce: args.Nonce, Body: args.Body}
	turnID, err := agentcontrol.ExplorerFollowUpTurnID(controller.RunID, request)
	dynamic, ok := scheduledDynamicTurn(scheduled, output.TurnID)
	base, invocationErr := explorerInvocation(controller, args.Body)
	invocation, scopeErr := scheduledTurnInvocation(base, taskscheduler.OperationExplorer, turnID)
	if err != nil || invocationErr != nil || scopeErr != nil || output.TurnID != turnID || output.TaskID != turnID || !ok || dynamic.AgentID != args.AgentID || dynamic.ParentAgentID != v.binding.Node.AgentID || dynamic.TurnSequence != output.TurnSequence || dynamic.Task.ID != turnID || dynamic.Task.RunID != controller.RunID || dynamic.Task.ControllerPath != v.controllerPath || dynamic.Task.Operation != taskscheduler.OperationExplorer || dynamic.Task.Input != args.Body || dynamic.Task.InvocationID != invocation.ID {
		return errors.Join(errors.New("follow-up scheduled evidence differs"), err, invocationErr, scopeErr)
	}
	return nil
}

func (v *agentToolReceiptVerifier) verifyNewWorkCaller(controller Snapshot, scheduled taskscheduler.Snapshot) error {
	dispatch, ok := controller.AgentDispatch[v.binding.InvocationID]
	state, stateOK := scheduled.Tasks[v.binding.AgentTurn.TurnID]
	if !ok || dispatch.Observation != nil || !stateOK || state.Claim == nil || state.Status == taskscheduler.StatusUnknown {
		return errors.New("uncertain agent tool caller created new work")
	}
	return nil
}

func (v *agentToolReceiptVerifier) verifyInterrupt(arguments, raw json.RawMessage, tree agenttree.Snapshot, controlState agentcontrol.Snapshot, scheduled taskscheduler.Snapshot) error {
	var args interruptAgentArgs
	var output struct {
		RequestID string                        `json:"request_id"`
		TurnID    string                        `json:"turn_id"`
		Outcome   agentcontrol.InterruptOutcome `json:"outcome"`
	}
	if canonical.Decode(arguments, &args) != nil || canonical.Decode(raw, &output) != nil || output.TurnID != args.TurnID || !boundedAgentToolText(args.Nonce, 128) {
		return errors.New("invalid interrupt receipt shape")
	}
	dynamic, ok := scheduledDynamicTurn(scheduled, args.TurnID)
	child, childOK := agentNodeByID(tree, dynamic.AgentID)
	state, interruptOK := controlState.Interrupts[output.RequestID]
	taskState, stateOK := scheduled.Tasks[dynamic.Task.ID]
	wantClaimID, wantClaimHead := "", ""
	if stateOK && taskState.Claim != nil {
		wantClaimID, _ = taskState.Claim.ID()
		wantClaimHead = taskState.Claim.ControllerHead
	}
	wantRequestID, requestIDErr := state.Request.ID()
	wantTurn := taskscheduler.AgentTurnBinding{ParentAgentID: dynamic.ParentAgentID, AgentID: dynamic.AgentID, TurnID: dynamic.TurnID, TurnSequence: dynamic.TurnSequence}
	if requestIDErr != nil || !ok || !childOK || child.ParentAgentID != v.binding.Node.AgentID || !interruptOK || output.RequestID != wantRequestID || state.Request.TreeID != tree.TreeID || state.Request.Actor != v.binding.Node.AgentID || state.Request.Nonce != args.Nonce || state.Request.ScheduleID != v.scheduleID || state.Request.ClaimID == "" || state.Request.ClaimID != wantClaimID || state.Request.ControllerHead != wantClaimHead || state.Request.AgentTurn != wantTurn || state.Request.InvocationID != dynamic.Task.InvocationID {
		return errors.New("interrupt source evidence differs")
	}
	outcome := agentcontrol.InterruptRequested
	if state.Observation != nil {
		outcome = state.Observation.Outcome
	}
	if output.Outcome != outcome {
		return errors.New("interrupt outcome evidence differs")
	}
	return nil
}

func (v *agentToolReceiptVerifier) verifyList(arguments, raw json.RawMessage, tree agenttree.Snapshot) error {
	var args listAgentsArgs
	var output struct {
		Agents     []agenttree.Node `json:"agents"`
		NextCursor string           `json:"next_cursor,omitempty"`
	}
	if canonical.Decode(arguments, &args) != nil || canonical.Decode(raw, &output) != nil || args.Limit < 1 || args.Limit > agentToolMaximumPage || len(args.After) > 1024 {
		return errors.New("invalid list receipt shape")
	}
	filtered := make([]agenttree.Node, 0, len(tree.Nodes))
	for _, node := range tree.Nodes {
		if agentToolDescendantOrSelf(v.binding.Node, node) {
			filtered = append(filtered, node)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Path < filtered[j].Path })
	if args.After != "" {
		found := false
		for _, node := range filtered {
			found = found || node.Path == args.After
		}
		if !found {
			return errors.New("list receipt cursor differs")
		}
	}
	page := make([]agenttree.Node, 0, args.Limit)
	next := ""
	for _, node := range filtered {
		if node.Path <= args.After {
			continue
		}
		if len(page) == args.Limit {
			next = page[len(page)-1].Path
			break
		}
		page = append(page, node)
	}
	if !sameCanonical(page, output.Agents) || next != output.NextCursor {
		return errors.New("list receipt page differs")
	}
	return nil
}

func (v *agentToolReceiptVerifier) verifyWait(call toolbridge.Call, raw json.RawMessage, controllerEvents []journal.Event, controller Snapshot, tree agenttree.Snapshot, controlState agentcontrol.Snapshot, scheduled taskscheduler.Snapshot, sources agentToolSourceReceipts, exactActivityPage bool) error {
	var args waitAgentArgs
	var output waitAgentOutput
	if canonical.Decode(call.Arguments, &args) != nil || canonical.Decode(raw, &output) != nil || args.AfterSequence < 0 || args.Limit < 1 || args.Limit > agentToolMaximumPage || args.TimeoutMillis < 1 || args.TimeoutMillis > agentToolMaximumWaitMillis {
		return errors.New("invalid wait receipt shape")
	}
	target, ok := agentNodeByID(tree, args.AgentID)
	if !ok || target.AgentID != v.binding.Node.AgentID && target.ParentAgentID != v.binding.Node.AgentID {
		return errors.New("wait receipt target differs")
	}
	want := make([]agentcontrol.Activity, 0, args.Limit)
	for _, activity := range controlState.Activities {
		if activity.AgentID == args.AgentID && activity.Sequence > args.AfterSequence {
			want = append(want, activity)
			if len(want) == args.Limit {
				break
			}
		}
	}
	if output.DeliveryVersion == 0 {
		if len(output.AcceptedResults) != 0 || len(output.ResultOmissions) != 0 || output.Truncated || output.NextAfter != 0 {
			return errors.New("legacy wait receipt contains delivery fields")
		}
		for _, activity := range output.Activities {
			if activity.Kind == "result" {
				return errors.New("legacy wait receipt cannot carry accepted result activity")
			}
		}
		if output.TimedOut {
			if len(output.Activities) != 0 || exactActivityPage && len(want) != 0 {
				return errors.New("wait timeout evidence differs")
			}
			return nil
		}
		if len(want) == 0 || !sameCanonical(want, output.Activities) {
			return errors.New("wait receipt activities differ")
		}
		return nil
	}
	if output.DeliveryVersion != waitAgentDeliveryVersion || !exactActivityPage {
		return errors.New("wait delivery version lacks exact activity prefix")
	}
	evidence := waitAgentPrefixEvidence{sources: sources, controllerEvents: controllerEvents, controller: controller, tree: tree, control: controlState, scheduled: scheduled}
	projection := &agentToolProjectionState{controllerPath: v.controllerPath, schedulerPath: v.schedulerPath, binding: v.binding, claimID: v.claimID, controllerHead: v.controllerHead, scheduleID: v.scheduleID}
	wanted, _, err := projection.projectWaitResult(call, args, want, output.TimedOut, evidence)
	if err != nil || !sameCanonical(wanted, output) {
		return errors.Join(errors.New("wait accepted-result projection differs"), err)
	}
	return nil
}

func agentToolHasTreeMessage(tree agenttree.Snapshot, want agenttree.Message) bool {
	for _, message := range tree.Messages {
		if message == want {
			return true
		}
	}
	return false
}

func agentToolNodeID(treeID, parentID, name, role string, authority agenttree.Authority, invocationID, contextSHA256 string) (string, error) {
	return canonical.Hash("harness.agenttree-node.v1", struct {
		TreeID        string              `json:"tree_id"`
		ParentAgentID string              `json:"parent_agent_id,omitempty"`
		Name          string              `json:"name"`
		Role          string              `json:"role"`
		Authority     agenttree.Authority `json:"authority"`
		InvocationID  string              `json:"invocation_id"`
		ContextSHA256 string              `json:"context_sha256"`
	}{treeID, parentID, name, role, authority, invocationID, contextSHA256})
}

func agentToolJournalPrefix(path, head string) ([]journal.Event, error) {
	events, err := journal.Read(path)
	if err != nil {
		return nil, err
	}
	for index, event := range events {
		if event.Hash == head {
			return append([]journal.Event(nil), events[:index+1]...), nil
		}
	}
	return nil, errors.New("agent tool source prefix unavailable")
}

// replayAgentToolTreePrefix projects the public AgentTree records at a verified
// journal prefix. The full journal is validated by journal.Read before slicing.
func replayAgentToolTreePrefix(events []journal.Event) (agenttree.Snapshot, error) {
	state := agenttree.Snapshot{}
	reservations := map[string]agenttree.Node{}
	nodes := map[string]agenttree.Node{}
	messageIDs := map[string]bool{}
	messageSequences := map[string]int{}
	for _, event := range events {
		switch event.Kind {
		case "agenttree.created":
			var payload struct {
				Version int            `json:"version"`
				TreeID  string         `json:"tree_id"`
				Root    agenttree.Node `json:"root"`
			}
			if canonical.Decode(event.Payload, &payload) != nil || payload.Version != 1 || state.TreeID != "" {
				return agenttree.Snapshot{}, errors.New("invalid agent tree prefix creation")
			}
			state.TreeID, state.RootAgentID = payload.TreeID, payload.Root.AgentID
			nodes[payload.Root.AgentID] = payload.Root
		case "agenttree.child-reserved":
			var payload struct {
				Version       int            `json:"version"`
				ReservationID string         `json:"reservation_id"`
				Node          agenttree.Node `json:"node"`
			}
			if canonical.Decode(event.Payload, &payload) != nil || payload.Version != 1 {
				return agenttree.Snapshot{}, errors.New("invalid agent tree prefix reservation")
			}
			reservations[payload.ReservationID] = payload.Node
		case "agenttree.child-committed":
			var payload struct {
				Version       int    `json:"version"`
				ReservationID string `json:"reservation_id"`
				AgentID       string `json:"agent_id"`
			}
			if canonical.Decode(event.Payload, &payload) != nil || payload.Version != 1 || reservations[payload.ReservationID].AgentID != payload.AgentID {
				return agenttree.Snapshot{}, errors.New("invalid agent tree prefix commit")
			}
			nodes[payload.AgentID] = reservations[payload.ReservationID]
			delete(reservations, payload.ReservationID)
		case "agenttree.status-observed":
			var payload struct {
				Version      int              `json:"version"`
				AgentID      string           `json:"agent_id"`
				From         agenttree.Status `json:"from"`
				To           agenttree.Status `json:"to"`
				ResultSHA256 string           `json:"result_sha256,omitempty"`
			}
			if canonical.Decode(event.Payload, &payload) != nil || payload.Version != 1 {
				return agenttree.Snapshot{}, errors.New("invalid agent tree prefix status")
			}
			node, ok := nodes[payload.AgentID]
			validTransition := payload.From == agenttree.StatusQueued && (payload.To == agenttree.StatusRunning || payload.To == agenttree.StatusCanceled) ||
				payload.From == agenttree.StatusRunning && (payload.To == agenttree.StatusSucceeded || payload.To == agenttree.StatusFailed || payload.To == agenttree.StatusCanceled || payload.To == agenttree.StatusUnknown) ||
				payload.From == agenttree.StatusUnknown && payload.To == agenttree.StatusSucceeded
			validResult := payload.To == agenttree.StatusSucceeded && len(payload.ResultSHA256) == sha256.Size*2 || payload.To != agenttree.StatusSucceeded && payload.ResultSHA256 == ""
			if !ok || node.Status != payload.From || !validTransition || !validResult {
				return agenttree.Snapshot{}, errors.New("agent tree prefix status differs")
			}
			node.Status, node.ResultSHA256 = payload.To, payload.ResultSHA256
			nodes[payload.AgentID] = node
		case "agenttree.message-enqueued":
			var payload struct {
				Version int               `json:"version"`
				Message agenttree.Message `json:"message"`
			}
			if canonical.Decode(event.Payload, &payload) != nil || payload.Version != 1 {
				return agenttree.Snapshot{}, errors.New("invalid agent tree prefix message")
			}
			message := payload.Message
			_, messageIDErr := hex.DecodeString(message.MessageID)
			_, bodyIDErr := hex.DecodeString(message.BodySHA256)
			from, fromOK := nodes[message.FromAgentID]
			to, toOK := nodes[message.ToAgentID]
			if !fromOK || !toOK || message.Wake && (from.Status == agenttree.StatusUnknown || to.Status == agenttree.StatusUnknown) || len(message.MessageID) != sha256.Size*2 || messageIDErr != nil || len(message.BodySHA256) != sha256.Size*2 || bodyIDErr != nil || message.Sequence != messageSequences[message.ToAgentID]+1 || messageIDs[message.MessageID] {
				return agenttree.Snapshot{}, errors.New("invalid agent tree prefix message")
			}
			state.Messages = append(state.Messages, message)
			messageIDs[message.MessageID] = true
			messageSequences[message.ToAgentID] = message.Sequence
		default:
			return agenttree.Snapshot{}, errors.New("unknown agent tree prefix event")
		}
	}
	for _, node := range nodes {
		queued := node
		queued.Status = agenttree.StatusQueued
		queued.ResultSHA256 = ""
		if agenttree.ValidateQueuedNode(state.TreeID, queued) != nil {
			return agenttree.Snapshot{}, errors.New("invalid agent tree prefix node")
		}
		state.Nodes = append(state.Nodes, node)
	}
	sort.Slice(state.Nodes, func(i, j int) bool { return state.Nodes[i].Path < state.Nodes[j].Path })
	sort.Slice(state.Messages, func(i, j int) bool {
		if state.Messages[i].ToAgentID != state.Messages[j].ToAgentID {
			return state.Messages[i].ToAgentID < state.Messages[j].ToAgentID
		}
		return state.Messages[i].Sequence < state.Messages[j].Sequence
	})
	return state, nil
}
