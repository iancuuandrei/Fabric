package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/taskscheduler"
	"harness.local/engorch/internal/toolbridge"
)

const (
	agentToolMaximumPage       = 64
	agentToolMaximumWaitMillis = 5000
)

type agentToolProjectionState struct {
	controllerPath string
	schedulerPath  string
	binding        agentDispatchBinding
	service        *agentcontrol.ScheduledService
	claimID        string
	controllerHead string
	scheduleID     string
}

type agentToolSourceReceipts struct {
	ControllerHead      string `json:"controller_head"`
	TreeHead            string `json:"tree_head"`
	ControlHead         string `json:"agent_control_head"`
	ActivityPageHead    string `json:"activity_page_head,omitempty"`
	SchedulerHead       string `json:"scheduler_head"`
	ScheduleID          string `json:"schedule_id"`
	ClaimID             string `json:"claim_id"`
	ClaimControllerHead string `json:"claim_controller_head"`
	AdmissionID         string `json:"admission_id"`
}

// newAgentToolProjection constructs the six finite AgentControl tools for one
// already admitted dynamic agent turn. It creates no listener or model loop.
func newAgentToolProjection(controllerPath, schedulerPath string, binding agentDispatchBinding) (toolbridge.Projection, error) {
	if binding.AgentTurn != nil {
		turn := *binding.AgentTurn
		binding.AgentTurn = &turn
	}
	state := &agentToolProjectionState{controllerPath: controllerPath, schedulerPath: schedulerPath, binding: binding}
	service, claimID, controllerHead, scheduleID, err := state.validate()
	if err != nil {
		return toolbridge.Projection{}, err
	}
	state.service = service
	state.claimID = claimID
	state.controllerHead = controllerHead
	state.scheduleID = scheduleID
	catalog, err := agentToolCatalog()
	if err != nil {
		return toolbridge.Projection{}, err
	}
	return toolbridge.Projection{
		Catalog: func() ([]toolbridge.ToolDefinition, error) {
			result := make([]toolbridge.ToolDefinition, len(catalog))
			for i, definition := range catalog {
				result[i] = definition
				result[i].InputSchema = append([]byte(nil), definition.InputSchema...)
			}
			return result, nil
		},
		Call: state.call,
	}, nil
}

func (p *agentToolProjectionState) validate() (*agentcontrol.ScheduledService, string, string, string, error) {
	b := p.binding
	if p.controllerPath == "" || p.schedulerPath == "" || b.ControllerPath != p.controllerPath || b.JournalPath != p.controllerPath+".agent-tree" || b.AgentTurn == nil || safepath.RequireDigest(b.AdmissionID) != nil || safepath.RequireDigest(b.InvocationID) != nil {
		return nil, "", "", "", errors.New("invalid agent tool caller binding")
	}
	controller, err := Inspect(p.controllerPath)
	if err != nil {
		return nil, "", "", "", err
	}
	dispatch, ok := controller.AgentDispatch[b.InvocationID]
	admissionID, idErr := dispatch.Admission.ID()
	closed := dispatch.Observation != nil && dispatch.Observation.Status != agenttree.StatusUnknown
	if !ok || idErr != nil || closed || admissionID != b.AdmissionID || dispatch.Admission.RunID != controller.RunID || dispatch.Admission.TreeID != controller.RunID || dispatch.Admission.Invocation.ID != b.InvocationID || dispatch.Admission.AgentTurn == nil || *dispatch.Admission.AgentTurn != *b.AgentTurn || !sameAgentToolNode(dispatch.Admission.Node, b.Node) {
		return nil, "", "", "", errors.Join(errors.New("agent tool controller admission differs"), idErr)
	}
	tree, err := agenttree.Inspect(b.JournalPath)
	caller, found := agentNodeByID(tree, b.Node.AgentID)
	if err != nil || tree.TreeID != controller.RunID || !found || !sameAgentToolNode(caller, b.Node) || caller.Role != "explorer" || caller.Authority != agenttree.AuthorityReadOnly {
		return nil, "", "", "", errors.Join(errors.New("agent tool caller role or authority unsupported"), err)
	}
	scheduled, err := taskscheduler.Inspect(p.schedulerPath)
	if err != nil || scheduled.Definition == nil {
		return nil, "", "", "", errors.Join(errors.New("agent tool schedule unavailable"), err)
	}
	dynamic, found := scheduledDynamicTurn(scheduled, b.AgentTurn.TurnID)
	if !found || dynamic.Task.ControllerPath != p.controllerPath || dynamic.Task.RunID != controller.RunID || dynamic.Task.InvocationID != b.InvocationID || dynamic.ParentAgentID != b.AgentTurn.ParentAgentID || dynamic.AgentID != b.AgentTurn.AgentID || dynamic.TurnID != b.AgentTurn.TurnID || dynamic.TurnSequence != b.AgentTurn.TurnSequence {
		return nil, "", "", "", errors.New("agent tool scheduled turn differs")
	}
	taskState, found := scheduled.Tasks[dynamic.Task.ID]
	if !found || taskState.Claim == nil || taskState.Claim.AgentTurn == nil || *taskState.Claim.AgentTurn != *b.AgentTurn || taskState.Claim.ScheduleID != scheduled.ScheduleID || !sameCanonical(taskState.Claim.Task, dynamic.Task) {
		return nil, "", "", "", errors.New("agent tool scheduled claim unavailable")
	}
	claimID, claimErr := taskState.Claim.ID()
	if claimErr != nil || safepath.RequireDigest(claimID) != nil {
		return nil, "", "", "", errors.Join(errors.New("agent tool scheduled claim invalid"), claimErr)
	}
	if p.claimID != "" && (claimID != p.claimID || taskState.Claim.ControllerHead != p.controllerHead || scheduled.ScheduleID != p.scheduleID) {
		return nil, "", "", "", errors.New("agent tool scheduled claim generation changed")
	}
	switch taskState.Status {
	case taskscheduler.StatusClaimed, taskscheduler.StatusRunning, taskscheduler.StatusUnknown:
	default:
		return nil, "", "", "", errors.New("agent tool caller turn is not open")
	}
	service, err := agentcontrol.BindScheduled(b.JournalPath, p.controllerPath+".agent-control", p.schedulerPath)
	if err != nil {
		return nil, "", "", "", err
	}
	return service, claimID, taskState.Claim.ControllerHead, scheduled.ScheduleID, nil
}

func sameAgentToolNode(a, b agenttree.Node) bool {
	return a.AgentID == b.AgentID && a.ParentAgentID == b.ParentAgentID && a.Path == b.Path && a.Name == b.Name && a.Role == b.Role && a.Authority == b.Authority && a.InvocationID == b.InvocationID && a.ContextSHA256 == b.ContextSHA256
}

func (p *agentToolProjectionState) call(ctx context.Context, call toolbridge.Call) (toolbridge.Result, error) {
	if ctx == nil || ctx.Err() != nil {
		if ctx == nil {
			return toolbridge.Result{}, errors.New("agent tool call context required")
		}
		return toolbridge.Result{}, errors.Join(errors.New("agent tool call context required"), ctx.Err())
	}
	var err error
	call, err = normalizeAgentToolCall(call)
	if err != nil {
		return toolbridge.Result{}, err
	}
	_, _, _, _, err = p.validate()
	if err != nil {
		return toolbridge.Result{}, err
	}
	status, err := p.claimStatus()
	if err != nil {
		return toolbridge.Result{}, err
	}
	uncertain, err := p.callerUncertain()
	if err != nil {
		return toolbridge.Result{}, err
	}
	var result any
	var selectedControlHead string
	switch call.Tool {
	case "spawn_agent":
		if status == taskscheduler.StatusUnknown || uncertain {
			return toolbridge.Result{}, errors.New("uncertain caller cannot spawn agent")
		}
		result, err = p.spawn(ctx, call.Arguments)
	case "list_agents":
		result, err = p.list(ctx, call.Arguments)
	case "wait_agent":
		return p.waitReceipted(ctx, call)
	case "send_message":
		result, err = p.message(ctx, call.Arguments)
	case "followup_task":
		if status == taskscheduler.StatusUnknown || uncertain {
			return toolbridge.Result{}, errors.New("uncertain caller cannot schedule follow-up")
		}
		result, err = p.followup(ctx, call.Arguments)
	case "interrupt_agent":
		result, err = p.interrupt(ctx, call.Arguments)
	default:
		err = errors.New("agent tool unavailable")
	}
	if err != nil {
		return toolbridge.Result{}, err
	}
	return p.receiptedResult(call, result, selectedControlHead)
}

func (p *agentToolProjectionState) callerUncertain() (bool, error) {
	controller, err := Inspect(p.controllerPath)
	if err != nil {
		return false, err
	}
	dispatch, ok := controller.AgentDispatch[p.binding.InvocationID]
	if !ok {
		return false, errors.New("agent tool controller admission unavailable")
	}
	return dispatch.Observation != nil && dispatch.Observation.Status == agenttree.StatusUnknown, nil
}

func (p *agentToolProjectionState) claimStatus() (taskscheduler.Status, error) {
	scheduled, err := taskscheduler.Inspect(p.schedulerPath)
	if err != nil {
		return "", err
	}
	for _, state := range scheduled.Tasks {
		if state.Claim == nil {
			continue
		}
		claimID, idErr := state.Claim.ID()
		if idErr != nil {
			return "", idErr
		}
		if claimID == p.claimID {
			return state.Status, nil
		}
	}
	return "", errors.New("agent tool scheduled claim unavailable")
}

type spawnAgentArgs struct {
	Name     string `json:"name"`
	Question string `json:"question"`
	Nonce    string `json:"nonce"`
}

func (p *agentToolProjectionState) spawn(ctx context.Context, raw json.RawMessage) (any, error) {
	var args spawnAgentArgs
	if canonical.Decode(raw, &args) != nil || !boundedAgentToolText(args.Name, 64) || !boundedAgentToolText(args.Question, 4096) || !boundedAgentToolText(args.Nonce, 128) {
		return nil, errors.New("invalid spawn_agent arguments")
	}
	if err := p.authorizeTarget(agentToolSpawn, ""); err != nil {
		return nil, err
	}
	node, dynamic, err := SpawnExplorerAgent(ctx, p.controllerPath, p.schedulerPath, p.binding.Node.AgentID, args.Name, args.Question, args.Nonce)
	if err != nil {
		return nil, err
	}
	return struct {
		AgentID       string `json:"agent_id"`
		ParentAgentID string `json:"parent_agent_id"`
		TurnID        string `json:"turn_id"`
		TurnSequence  int    `json:"turn_sequence"`
		TaskID        string `json:"task_id"`
	}{node.AgentID, node.ParentAgentID, dynamic.TurnID, dynamic.TurnSequence, dynamic.Task.ID}, nil
}

type listAgentsArgs struct {
	After string `json:"after,omitempty"`
	Limit int    `json:"limit"`
}

func (p *agentToolProjectionState) list(ctx context.Context, raw json.RawMessage) (any, error) {
	var args listAgentsArgs
	if canonical.Decode(raw, &args) != nil || args.Limit < 1 || args.Limit > agentToolMaximumPage || len(args.After) > 1024 || !utf8.ValidString(args.After) {
		return nil, errors.New("invalid list_agents arguments")
	}
	if err := p.authorizeTarget(agentToolList, ""); err != nil {
		return nil, err
	}
	nodes, err := p.service.List(ctx, "", agenttree.MaximumNodes)
	if err != nil {
		return nil, err
	}
	filtered := make([]agenttree.Node, 0, len(nodes))
	for _, node := range nodes {
		if agentToolDescendantOrSelf(p.binding.Node, node) {
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
			return nil, errors.New("list_agents cursor is outside caller subtree")
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
	return struct {
		Agents     []agenttree.Node `json:"agents"`
		NextCursor string           `json:"next_cursor,omitempty"`
	}{page, next}, nil
}

type waitAgentArgs struct {
	AgentID       string `json:"agent_id"`
	AfterSequence int    `json:"after_sequence"`
	Limit         int    `json:"limit"`
	TimeoutMillis int    `json:"timeout_milliseconds"`
}

func (p *agentToolProjectionState) wait(ctx context.Context, raw json.RawMessage) (any, string, error) {
	_, page, timedOut, err := p.waitPage(ctx, raw)
	if err != nil {
		return nil, "", err
	}
	return struct {
		Activities []agentcontrol.Activity `json:"activities"`
		TimedOut   bool                    `json:"timed_out"`
	}{page.Activities, timedOut}, page.JournalHead, nil
}

func (p *agentToolProjectionState) waitPage(ctx context.Context, raw json.RawMessage) (waitAgentArgs, agentcontrol.ActivityPage, bool, error) {
	var args waitAgentArgs
	if canonical.Decode(raw, &args) != nil || safepath.RequireDigest(args.AgentID) != nil || args.AfterSequence < 0 || args.Limit < 1 || args.Limit > agentToolMaximumPage || args.TimeoutMillis < 1 || args.TimeoutMillis > agentToolMaximumWaitMillis {
		return waitAgentArgs{}, agentcontrol.ActivityPage{}, false, errors.New("invalid wait_agent arguments")
	}
	if err := p.authorizeTarget(agentToolWait, args.AgentID); err != nil {
		return waitAgentArgs{}, agentcontrol.ActivityPage{}, false, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(args.TimeoutMillis)*time.Millisecond)
	defer cancel()
	page, err := p.service.WaitWithHead(waitCtx, args.AgentID, args.AfterSequence, args.Limit)
	timedOut := errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil
	if err != nil && !timedOut {
		return waitAgentArgs{}, agentcontrol.ActivityPage{}, false, err
	}
	if safepath.RequireDigest(page.JournalHead) != nil {
		return waitAgentArgs{}, agentcontrol.ActivityPage{}, false, errors.New("wait_agent exact control prefix unavailable")
	}
	if page.Activities == nil {
		page.Activities = []agentcontrol.Activity{}
	}
	return args, page, timedOut, nil
}

type messageAgentArgs struct {
	AgentID string `json:"agent_id"`
	Nonce   string `json:"nonce"`
	Body    string `json:"body"`
}

func (p *agentToolProjectionState) message(ctx context.Context, raw json.RawMessage) (any, error) {
	var args messageAgentArgs
	if canonical.Decode(raw, &args) != nil || safepath.RequireDigest(args.AgentID) != nil || !boundedAgentToolText(args.Nonce, 128) || !boundedAgentToolText(args.Body, agentcontrol.MaximumBodyBytes) {
		return nil, errors.New("invalid send_message arguments")
	}
	if err := p.authorizeTarget(agentToolMessage, args.AgentID); err != nil {
		return nil, err
	}
	record, err := p.service.Send(ctx, agentcontrol.MessageRequest{FromAgentID: p.binding.Node.AgentID, ToAgentID: args.AgentID, Nonce: args.Nonce, Body: args.Body})
	if err != nil {
		return nil, err
	}
	return struct {
		Message agenttree.Message `json:"message"`
	}{record.Message}, nil
}

type followupTaskArgs struct {
	AgentID string `json:"agent_id"`
	Nonce   string `json:"nonce"`
	Body    string `json:"body"`
}

func (p *agentToolProjectionState) followup(ctx context.Context, raw json.RawMessage) (any, error) {
	var args followupTaskArgs
	if canonical.Decode(raw, &args) != nil || safepath.RequireDigest(args.AgentID) != nil || !boundedAgentToolText(args.Nonce, 128) || !boundedAgentToolText(args.Body, 4096) {
		return nil, errors.New("invalid followup_task arguments")
	}
	if err := p.authorizeTarget(agentToolFollowUp, args.AgentID); err != nil {
		return nil, err
	}
	record, dynamic, err := FollowUpExplorerAgent(ctx, p.controllerPath, p.schedulerPath, agentcontrol.MessageRequest{FromAgentID: p.binding.Node.AgentID, ToAgentID: args.AgentID, Nonce: args.Nonce, Body: args.Body}, args.Body)
	if err != nil {
		return nil, err
	}
	return struct {
		Message      agenttree.Message `json:"message"`
		TurnID       string            `json:"turn_id"`
		TurnSequence int               `json:"turn_sequence"`
		TaskID       string            `json:"task_id"`
	}{record.Message, dynamic.TurnID, dynamic.TurnSequence, dynamic.Task.ID}, nil
}

type interruptAgentArgs struct {
	TurnID string `json:"turn_id"`
	Nonce  string `json:"nonce"`
}

func (p *agentToolProjectionState) interrupt(ctx context.Context, raw json.RawMessage) (any, error) {
	var args interruptAgentArgs
	if canonical.Decode(raw, &args) != nil || safepath.RequireDigest(args.TurnID) != nil || !boundedAgentToolText(args.Nonce, 128) {
		return nil, errors.New("invalid interrupt_agent arguments")
	}
	scheduled, err := taskscheduler.Inspect(p.schedulerPath)
	if err != nil {
		return nil, err
	}
	dynamic, ok := scheduledDynamicTurn(scheduled, args.TurnID)
	if !ok {
		return nil, errors.New("interrupt_agent turn unavailable")
	}
	if err := p.authorizeTarget(agentToolInterrupt, dynamic.AgentID); err != nil {
		return nil, err
	}
	state, err := RequestAgentInterrupt(ctx, p.controllerPath, p.schedulerPath, args.TurnID, p.binding.Node.AgentID, args.Nonce)
	if err != nil {
		return nil, err
	}
	outcome := agentcontrol.InterruptRequested
	if state.Observation != nil {
		outcome = state.Observation.Outcome
	}
	return struct {
		RequestID string                        `json:"request_id"`
		TurnID    string                        `json:"turn_id"`
		Outcome   agentcontrol.InterruptOutcome `json:"outcome"`
	}{state.RequestID, state.Request.AgentTurn.TurnID, outcome}, nil
}

type agentToolOperation uint8

const (
	agentToolSpawn agentToolOperation = iota + 1
	agentToolList
	agentToolWait
	agentToolMessage
	agentToolFollowUp
	agentToolInterrupt
)

// authorizeTarget is the single topology and role policy for every projected
// agent operation. The caller identity is never accepted from tool arguments.
func (p *agentToolProjectionState) authorizeTarget(operation agentToolOperation, targetID string) error {
	tree, err := agenttree.Inspect(p.binding.JournalPath)
	if err != nil {
		return err
	}
	caller, ok := agentNodeByID(tree, p.binding.Node.AgentID)
	if !ok || !sameAgentToolNode(caller, p.binding.Node) || caller.Role != "explorer" || caller.Authority != agenttree.AuthorityReadOnly {
		return errors.New("agent tool caller authority changed")
	}
	if operation == agentToolSpawn || operation == agentToolList {
		if targetID != "" {
			return errors.New("agent tool target not accepted")
		}
		return nil
	}
	target, ok := agentNodeByID(tree, targetID)
	if !ok {
		return errors.New("agent tool target unavailable")
	}
	self := target.AgentID == caller.AgentID
	parent := target.AgentID == caller.ParentAgentID
	directChild := target.ParentAgentID == caller.AgentID
	switch operation {
	case agentToolWait:
		if self || directChild {
			return nil
		}
	case agentToolMessage:
		if parent || directChild {
			return nil
		}
	case agentToolFollowUp, agentToolInterrupt:
		if directChild && target.Role == "explorer" && target.Authority == agenttree.AuthorityReadOnly {
			return nil
		}
	}
	return errors.New("agent tool target outside permitted topology")
}

func agentToolDescendantOrSelf(caller, node agenttree.Node) bool {
	return node.Path == caller.Path || strings.HasPrefix(node.Path, caller.Path+"/")
}

func boundedAgentToolText(value string, maximum int) bool {
	return len(value) >= 1 && len(value) <= maximum && strings.TrimSpace(value) == value && utf8.ValidString(value) && strings.IndexByte(value, 0) < 0
}

func normalizeAgentToolCall(call toolbridge.Call) (toolbridge.Call, error) {
	if len(call.RequestID) == 0 || !json.Valid(call.RequestID) || len(call.Arguments) == 0 {
		return toolbridge.Call{}, errors.New("agent tool request identity required")
	}
	normalID, idErr := canonical.Normalize(call.RequestID)
	normalArgs, argsErr := canonical.Normalize(call.Arguments)
	if idErr != nil || argsErr != nil || !bytes.Equal(normalID, call.RequestID) {
		return toolbridge.Call{}, errors.Join(errors.New("invalid agent tool call encoding"), idErr, argsErr)
	}
	call.Arguments = normalArgs
	return call, nil
}

func (p *agentToolProjectionState) receiptedResult(call toolbridge.Call, result any, selectedControlHead string) (toolbridge.Result, error) {
	sources, err := p.sourceReceipts(selectedControlHead)
	if err != nil {
		return toolbridge.Result{}, err
	}
	return p.receiptedResultWithSources(call, result, sources)
}

func (p *agentToolProjectionState) receiptedResultWithSources(call toolbridge.Call, result any, sources agentToolSourceReceipts) (toolbridge.Result, error) {
	resultJSON, err := agentToolCanonicalBytes(result)
	if err != nil {
		return toolbridge.Result{}, err
	}
	receiptPayload := struct {
		Tool         string                  `json:"tool"`
		RequestID    json.RawMessage         `json:"request_id"`
		Arguments    json.RawMessage         `json:"arguments"`
		AgentID      string                  `json:"agent_id"`
		InvocationID string                  `json:"invocation_id"`
		TurnID       string                  `json:"turn_id"`
		Result       json.RawMessage         `json:"result"`
		Sources      agentToolSourceReceipts `json:"sources"`
	}{call.Tool, call.RequestID, call.Arguments, p.binding.Node.AgentID, p.binding.InvocationID, p.binding.AgentTurn.TurnID, resultJSON, sources}
	if _, err := agentToolCanonicalBytes(receiptPayload); err != nil {
		return toolbridge.Result{}, err
	}
	receiptID, err := canonical.Hash("harness.agent-tool-source-receipt.v1", receiptPayload)
	if err != nil {
		return toolbridge.Result{}, err
	}
	encoded, err := agentToolCanonicalBytes(struct {
		Result    json.RawMessage         `json:"result"`
		Sources   agentToolSourceReceipts `json:"source_receipts"`
		ReceiptID string                  `json:"receipt_id"`
	}{resultJSON, sources, receiptID})
	if err != nil {
		return toolbridge.Result{}, err
	}
	return toolbridge.Result{JSON: encoded}, nil
}

func (p *agentToolProjectionState) sourceReceipts(selectedControlHead string) (agentToolSourceReceipts, error) {
	controller, err := agentToolJournalHead(p.controllerPath)
	if err != nil {
		return agentToolSourceReceipts{}, err
	}
	tree, err := agentToolJournalHead(p.binding.JournalPath)
	if err != nil {
		return agentToolSourceReceipts{}, err
	}
	control := selectedControlHead
	if control == "" {
		control, err = agentToolJournalHead(p.controllerPath + ".agent-control")
		if err != nil {
			return agentToolSourceReceipts{}, err
		}
	} else if _, err = agentToolJournalPrefix(p.controllerPath+".agent-control", control); err != nil {
		return agentToolSourceReceipts{}, errors.Join(errors.New("selected agent control prefix unavailable"), err)
	}
	scheduler, err := agentToolJournalHead(p.schedulerPath)
	if err != nil {
		return agentToolSourceReceipts{}, err
	}
	return agentToolSourceReceipts{
		ControllerHead: controller, TreeHead: tree, ControlHead: control, ActivityPageHead: selectedControlHead, SchedulerHead: scheduler,
		ScheduleID: p.scheduleID, ClaimID: p.claimID, ClaimControllerHead: p.controllerHead, AdmissionID: p.binding.AdmissionID,
	}, nil
}

func agentToolJournalHead(path string) (string, error) {
	events, err := journal.Read(path)
	if err != nil || len(events) == 0 {
		return "", errors.Join(errors.New("agent tool source journal unavailable"), err)
	}
	return events[len(events)-1].Hash, nil
}

func agentToolCatalog() ([]toolbridge.ToolDefinition, error) {
	definitions := []struct {
		name, description, schema string
	}{
		{"spawn_agent", "Create one read-only explorer child with one durable scheduled turn.", `{"additionalProperties":false,"properties":{"name":{"maxLength":64,"minLength":1,"type":"string"},"nonce":{"maxLength":128,"minLength":1,"type":"string"},"question":{"maxLength":4096,"minLength":1,"type":"string"}},"required":["name","question","nonce"],"type":"object"}`},
		{"list_agents", "List a bounded page in the caller's durable agent subtree.", `{"additionalProperties":false,"properties":{"after":{"maxLength":1024,"type":"string"},"limit":{"maximum":64,"minimum":1,"type":"integer"}},"required":["limit"],"type":"object"}`},
		{"wait_agent", "Wait for bounded durable activity on the caller or one direct child.", `{"additionalProperties":false,"properties":{"after_sequence":{"minimum":0,"type":"integer"},"agent_id":{"maxLength":64,"minLength":64,"type":"string"},"limit":{"maximum":64,"minimum":1,"type":"integer"},"timeout_milliseconds":{"maximum":5000,"minimum":1,"type":"integer"}},"required":["agent_id","after_sequence","limit","timeout_milliseconds"],"type":"object"}`},
		{"send_message", "Store one non-waking message to the caller's parent or direct child.", `{"additionalProperties":false,"properties":{"agent_id":{"maxLength":64,"minLength":64,"type":"string"},"body":{"maxLength":65536,"minLength":1,"type":"string"},"nonce":{"maxLength":128,"minLength":1,"type":"string"}},"required":["agent_id","nonce","body"],"type":"object"}`},
		{"followup_task", "Store one waking message and schedule the next turn for a direct explorer child.", `{"additionalProperties":false,"properties":{"agent_id":{"maxLength":64,"minLength":64,"type":"string"},"body":{"maxLength":4096,"minLength":1,"type":"string"},"nonce":{"maxLength":128,"minLength":1,"type":"string"}},"required":["agent_id","nonce","body"],"type":"object"}`},
		{"interrupt_agent", "Request interruption of one exact open turn belonging to a direct explorer child.", `{"additionalProperties":false,"properties":{"nonce":{"maxLength":128,"minLength":1,"type":"string"},"turn_id":{"maxLength":64,"minLength":64,"type":"string"}},"required":["turn_id","nonce"],"type":"object"}`},
	}
	result := make([]toolbridge.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		schema, err := canonical.Normalize([]byte(definition.schema))
		if err != nil {
			return nil, err
		}
		result = append(result, toolbridge.ToolDefinition{Name: definition.name, Description: definition.description, InputSchema: schema})
	}
	return result, nil
}
