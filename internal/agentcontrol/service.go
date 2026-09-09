package agentcontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/taskscheduler"
)

const (
	// MaximumBodyBytes bounds one stored message body.
	MaximumBodyBytes = 64 << 10
	// MaximumStoredBytes bounds all message bodies retained by one service.
	MaximumStoredBytes = 16 << 20
	// MaximumActivities bounds message and node observations.
	MaximumActivities = 8192
)

// Service binds one AgentTree to its controller-owned message and activity journal.
type Service struct {
	treePath    string
	journalPath string
	treeID      string
}

// MessageRequest contains exact endpoints, caller nonce, and bounded body text.
type MessageRequest struct {
	FromAgentID string `json:"from_agent_id"`
	ToAgentID   string `json:"to_agent_id"`
	Nonce       string `json:"nonce"`
	Body        string `json:"body"`
}

// MessageRecord stores retrievable body bytes beside their AgentTree envelope.
type MessageRecord struct {
	Message agenttree.Message `json:"message"`
	Body    string            `json:"body"`
	Bytes   int               `json:"bytes"`
}

// Activity is one monotonic per-agent message or lifecycle observation.
type Activity struct {
	AgentID      string           `json:"agent_id"`
	Sequence     int              `json:"sequence"`
	Kind         string           `json:"kind"`
	MessageID    string           `json:"message_id,omitempty"`
	TurnID       string           `json:"turn_id,omitempty"`
	TurnSequence int              `json:"turn_sequence,omitempty"`
	Status       agenttree.Status `json:"status,omitempty"`
	ResultSHA256 string           `json:"result_sha256,omitempty"`
	ResultID     string           `json:"result_id,omitempty"`
	InterruptID  string           `json:"interrupt_id,omitempty"`
	Interrupt    InterruptOutcome `json:"interrupt,omitempty"`
}

// ObserveTurn appends one controller-proven dynamic turn state. The caller
// must first durably record the matching scheduler/controller transition.
func (s *Service) ObserveTurn(ctx context.Context, turn taskscheduler.AgentTurnBinding, status agenttree.Status, resultSHA256 string) error {
	if err := validateService(s); err != nil || ctx == nil || ctx.Err() != nil || safepath.RequireDigest(turn.ParentAgentID) != nil || safepath.RequireDigest(turn.AgentID) != nil || safepath.RequireDigest(turn.TurnID) != nil || turn.TurnSequence < 1 {
		return errors.Join(errors.New("invalid agent turn observation"), err, contextError(ctx))
	}
	if status != agenttree.StatusRunning && status != agenttree.StatusSucceeded && status != agenttree.StatusUnknown || status == agenttree.StatusSucceeded && safepath.RequireDigest(resultSHA256) != nil || status != agenttree.StatusSucceeded && resultSHA256 != "" {
		return errors.New("invalid agent turn status observation")
	}
	tree, err := agenttree.Inspect(s.treePath)
	node, ok := nodeByID(tree, turn.AgentID)
	if err != nil || tree.TreeID != s.treeID || !ok || node.ParentAgentID != turn.ParentAgentID {
		return errors.Join(errors.New("agent turn node unavailable"), err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		state, inspectErr := Inspect(s.journalPath)
		if inspectErr != nil {
			return inspectErr
		}
		for index := len(state.Activities) - 1; index >= 0; index-- {
			prior := state.Activities[index]
			if prior.Kind == "turn" && prior.TurnID == turn.TurnID {
				if prior.AgentID == turn.AgentID && prior.TurnSequence == turn.TurnSequence && prior.Status == status && prior.ResultSHA256 == resultSHA256 {
					return nil
				}
				if prior.Status == agenttree.StatusUnknown && status == agenttree.StatusSucceeded {
					break
				}
				if prior.Status == agenttree.StatusRunning && (status == agenttree.StatusSucceeded || status == agenttree.StatusUnknown) {
					break
				}
				return errors.New("agent turn observation differs")
			}
		}
		activity := Activity{AgentID: turn.AgentID, Sequence: nextActivitySequence(state, turn.AgentID), Kind: "turn", TurnID: turn.TurnID, TurnSequence: turn.TurnSequence, Status: status, ResultSHA256: resultSHA256}
		_, appendErr := journal.Append(s.journalPath, "agentcontrol.activity", activityEvent{Version: 1, Activity: activity}, func(events []journal.Event) error {
			_, replayErr := Replay(events)
			return replayErr
		})
		if appendErr == nil {
			return nil
		}
	}
	return errors.New("agent turn activity contention")
}

// Snapshot is the validated message and activity projection.
type Snapshot struct {
	TreeID     string                    `json:"tree_id"`
	Messages   []MessageRecord           `json:"messages"`
	Activities []Activity                `json:"activities"`
	Latest     map[string]Activity       `json:"latest"`
	Observed   map[string]Activity       `json:"observed_status"`
	Results    []AcceptedResult          `json:"accepted_results,omitempty"`
	Interrupts map[string]InterruptState `json:"interrupts,omitempty"`
}

type boundEvent struct {
	Version int    `json:"version"`
	TreeID  string `json:"tree_id"`
}

type messageEvent struct {
	Version  int           `json:"version"`
	Record   MessageRecord `json:"record"`
	Activity Activity      `json:"activity"`
}

type activityEvent struct {
	Version  int      `json:"version"`
	Activity Activity `json:"activity"`
}

// Bind opens or creates the service binding for an existing AgentTree.
func Bind(treePath, journalPath string) (*Service, error) {
	if !exactAbsolutePath(treePath) || !exactAbsolutePath(journalPath) || treePath == journalPath {
		return nil, errors.New("invalid agent control paths")
	}
	tree, err := agenttree.Inspect(treePath)
	if err != nil || tree.TreeID == "" {
		return nil, errors.Join(errors.New("agent tree unavailable"), err)
	}
	service := &Service{treePath: treePath, journalPath: journalPath, treeID: tree.TreeID}
	state, err := Inspect(journalPath)
	if err != nil {
		return nil, err
	}
	if state.TreeID == "" {
		_, err = journal.Append(journalPath, "agentcontrol.bound", boundEvent{Version: 1, TreeID: tree.TreeID}, func(events []journal.Event) error {
			_, replayErr := Replay(events)
			return replayErr
		})
		if err != nil {
			return nil, err
		}
	} else if state.TreeID != tree.TreeID {
		return nil, errors.New("agent control tree identity differs")
	}
	return service, nil
}

// Inspect replays a message and activity journal without reading message bodies elsewhere.
func Inspect(path string) (Snapshot, error) {
	events, err := journal.Read(path)
	if err != nil {
		return Snapshot{}, err
	}
	return Replay(events)
}

// Replay validates the complete message and activity history.
func Replay(events []journal.Event) (Snapshot, error) {
	state := Snapshot{Latest: map[string]Activity{}, Observed: map[string]Activity{}, Interrupts: map[string]InterruptState{}}
	messageIDs := map[string]bool{}
	storedBytes := 0
	for index, event := range events {
		switch event.Kind {
		case "agentcontrol.bound":
			var payload boundEvent
			if index != 0 || canonical.Decode(event.Payload, &payload) != nil || payload.Version != 1 || safepath.RequireDigest(payload.TreeID) != nil {
				return Snapshot{}, errors.New("invalid agent control binding")
			}
			state.TreeID = payload.TreeID
		case "agentcontrol.message":
			var payload messageEvent
			if canonical.Decode(event.Payload, &payload) != nil || payload.Version != 1 || validateMessageRecord(payload.Record) != nil || messageIDs[payload.Record.Message.MessageID] || len(state.Messages) >= agenttree.MaximumMessages || storedBytes > MaximumStoredBytes-payload.Record.Bytes {
				return Snapshot{}, errors.New("invalid agent control message")
			}
			if validateActivity(state, payload.Activity) != nil || payload.Activity.Kind != "message" || payload.Activity.AgentID != payload.Record.Message.ToAgentID || payload.Activity.MessageID != payload.Record.Message.MessageID {
				return Snapshot{}, errors.New("invalid message activity")
			}
			messageIDs[payload.Record.Message.MessageID] = true
			storedBytes += payload.Record.Bytes
			state.Messages = append(state.Messages, payload.Record)
			state.Activities = append(state.Activities, payload.Activity)
			state.Latest[payload.Activity.AgentID] = payload.Activity
		case "agentcontrol.activity":
			var payload activityEvent
			if canonical.Decode(event.Payload, &payload) != nil || payload.Version != 1 || validateActivity(state, payload.Activity) != nil || payload.Activity.Kind != "status" && payload.Activity.Kind != "turn" || payload.Activity.MessageID != "" {
				return Snapshot{}, errors.New("invalid agent status activity")
			}
			state.Activities = append(state.Activities, payload.Activity)
			state.Latest[payload.Activity.AgentID] = payload.Activity
			if payload.Activity.Kind == "status" {
				state.Observed[payload.Activity.AgentID] = payload.Activity
			}
		case "agentcontrol.interrupt-requested", "agentcontrol.interrupt-observed":
			if err := replayInterrupt(&state, event); err != nil {
				return Snapshot{}, err
			}
		case "agentcontrol.result-accepted":
			if err := replayAcceptedResult(&state, event); err != nil {
				return Snapshot{}, err
			}
		default:
			return Snapshot{}, errors.New("unknown agent control event")
		}
	}
	if len(events) > 0 && state.TreeID == "" {
		return Snapshot{}, errors.New("agent control binding missing")
	}
	return state, nil
}

// Send stores a non-waking message and makes its exact body retrievable.
func (s *Service) Send(ctx context.Context, request MessageRequest) (MessageRecord, error) {
	return s.storeMessage(ctx, request, false)
}

// FollowUp stores a waking message. Scheduling the resulting turn remains a controller action.
func (s *Service) FollowUp(ctx context.Context, request MessageRequest) (MessageRecord, error) {
	return s.storeMessage(ctx, request, true)
}

func (s *Service) storeMessage(ctx context.Context, request MessageRequest, wake bool) (MessageRecord, error) {
	if err := validateService(s); err != nil || ctx == nil || ctx.Err() != nil {
		return MessageRecord{}, errors.Join(err, contextError(ctx))
	}
	messageID, bodySHA256, err := s.messageIdentity(request, wake)
	if err != nil {
		return MessageRecord{}, err
	}
	state, err := Inspect(s.journalPath)
	if err != nil {
		return MessageRecord{}, err
	}
	if prior, ok := exactStoredMessage(state, messageID); ok {
		if prior.Body == request.Body && prior.Message.FromAgentID == request.FromAgentID && prior.Message.ToAgentID == request.ToAgentID && prior.Message.Wake == wake && prior.Message.BodySHA256 == bodySHA256 {
			return prior, nil
		}
		return MessageRecord{}, errors.New("agent message body differs")
	}
	storedBytes := 0
	for _, stored := range state.Messages {
		storedBytes += stored.Bytes
	}
	if len(state.Messages) >= agenttree.MaximumMessages || len(state.Activities) >= MaximumActivities || storedBytes > MaximumStoredBytes-len(request.Body) {
		return MessageRecord{}, errors.New("agent control storage limit exceeded")
	}
	envelopeRequest := agenttree.MessageRequest{MessageID: messageID, FromAgentID: request.FromAgentID, ToAgentID: request.ToAgentID, BodySHA256: bodySHA256}
	var envelope agenttree.Message
	if wake {
		envelope, err = agenttree.FollowUp(s.treePath, envelopeRequest)
	} else {
		envelope, err = agenttree.Send(s.treePath, envelopeRequest)
	}
	if err != nil {
		tree, inspectErr := agenttree.Inspect(s.treePath)
		envelope, _ = exactTreeMessage(tree, messageID)
		if inspectErr != nil || envelope.MessageID == "" || envelope.FromAgentID != request.FromAgentID || envelope.ToAgentID != request.ToAgentID || envelope.BodySHA256 != bodySHA256 || envelope.Wake != wake {
			return MessageRecord{}, errors.Join(err, inspectErr)
		}
	}
	record := MessageRecord{Message: envelope, Body: request.Body, Bytes: len(request.Body)}
	for attempt := 0; attempt < 3; attempt++ {
		state, inspectErr := Inspect(s.journalPath)
		if inspectErr != nil {
			return MessageRecord{}, inspectErr
		}
		if prior, ok := exactStoredMessage(state, messageID); ok {
			if prior == record {
				return prior, nil
			}
			return MessageRecord{}, errors.New("agent message body differs")
		}
		activity := Activity{AgentID: request.ToAgentID, Sequence: nextActivitySequence(state, request.ToAgentID), Kind: "message", MessageID: messageID}
		_, appendErr := journal.Append(s.journalPath, "agentcontrol.message", messageEvent{Version: 1, Record: record, Activity: activity}, func(events []journal.Event) error {
			_, replayErr := Replay(events)
			return replayErr
		})
		if appendErr == nil {
			return record, nil
		}
	}
	return MessageRecord{}, errors.New("agent message activity contention")
}

func (s *Service) messageIdentity(request MessageRequest, wake bool) (string, string, error) {
	if strings.TrimSpace(request.Nonce) == "" || len(request.Nonce) > 128 || len(request.Body) < 1 || len(request.Body) > MaximumBodyBytes || !utf8.ValidString(request.Body) || strings.IndexByte(request.Body, 0) >= 0 || safepath.RequireDigest(request.FromAgentID) != nil || safepath.RequireDigest(request.ToAgentID) != nil {
		return "", "", errors.New("invalid agent message request")
	}
	bodyHash := sha256.Sum256([]byte(request.Body))
	bodySHA256 := hex.EncodeToString(bodyHash[:])
	messageID, err := canonical.Hash("harness.agentcontrol-message.v1", struct {
		TreeID      string `json:"tree_id"`
		FromAgentID string `json:"from_agent_id"`
		ToAgentID   string `json:"to_agent_id"`
		Nonce       string `json:"nonce"`
		BodySHA256  string `json:"body_sha256"`
		Wake        bool   `json:"wake"`
	}{s.treeID, request.FromAgentID, request.ToAgentID, request.Nonce, bodySHA256, wake})
	if err != nil {
		return "", "", err
	}
	return messageID, bodySHA256, nil
}

// MessagesAfter returns a bounded FIFO page including verified body text.
func (s *Service) MessagesAfter(recipient string, afterSequence, limit int) ([]MessageRecord, error) {
	if err := validateService(s); err != nil || safepath.RequireDigest(recipient) != nil || afterSequence < 0 || limit < 1 || limit > 256 {
		return nil, errors.Join(err, errors.New("invalid agent message page"))
	}
	state, err := Inspect(s.journalPath)
	if err != nil {
		return nil, err
	}
	tree, err := agenttree.Inspect(s.treePath)
	if err != nil || tree.TreeID != s.treeID {
		return nil, errors.Join(errors.New("agent tree identity changed"), err)
	}
	result := make([]MessageRecord, 0, limit)
	for _, record := range state.Messages {
		if record.Message.ToAgentID == recipient && record.Message.Sequence > afterSequence {
			envelope, ok := exactTreeMessage(tree, record.Message.MessageID)
			if !ok || envelope != record.Message {
				return nil, errors.New("agent message envelope differs")
			}
			result = append(result, record)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

// List returns a bounded canonical-path page after reconciling node observations.
func (s *Service) List(ctx context.Context, afterPath string, limit int) ([]agenttree.Node, error) {
	if err := validateService(s); err != nil || ctx == nil || ctx.Err() != nil || limit < 1 || limit > 256 {
		return nil, errors.Join(err, contextError(ctx), errors.New("invalid agent list request"))
	}
	if err := s.reconcileTree(ctx); err != nil {
		return nil, err
	}
	tree, err := agenttree.Inspect(s.treePath)
	if err != nil {
		return nil, err
	}
	if afterPath != "" {
		found := false
		for _, node := range tree.Nodes {
			found = found || node.Path == afterPath
		}
		if !found {
			return nil, errors.New("agent list cursor unavailable")
		}
	}
	result := make([]agenttree.Node, 0, limit)
	for _, node := range tree.Nodes {
		if node.Path > afterPath {
			result = append(result, node)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

// ActivitiesAfter returns a bounded per-agent activity page.
func (s *Service) ActivitiesAfter(agentID string, afterSequence, limit int) ([]Activity, error) {
	page, err := s.ActivitiesAfterWithHead(agentID, afterSequence, limit)
	if err != nil {
		return nil, err
	}
	return page.Activities, nil
}

// Wait returns when a later durable message or lifecycle observation exists.
func (s *Service) Wait(ctx context.Context, agentID string, afterSequence, limit int) ([]Activity, error) {
	page, err := s.WaitWithHead(ctx, agentID, afterSequence, limit)
	if err != nil {
		return nil, err
	}
	return page.Activities, nil
}

func (s *Service) reconcileTree(ctx context.Context) error {
	tree, err := agenttree.Inspect(s.treePath)
	if err != nil || tree.TreeID != s.treeID {
		return errors.Join(errors.New("agent tree identity changed"), err)
	}
	for _, node := range tree.Nodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		for attempt := 0; attempt < 3; attempt++ {
			state, inspectErr := Inspect(s.journalPath)
			if inspectErr != nil {
				return inspectErr
			}
			latest, ok := state.Observed[node.AgentID]
			if ok && latest.Kind == "status" && latest.Status == node.Status && latest.ResultSHA256 == node.ResultSHA256 {
				break
			}
			activity := Activity{AgentID: node.AgentID, Sequence: nextActivitySequence(state, node.AgentID), Kind: "status", Status: node.Status, ResultSHA256: node.ResultSHA256}
			_, appendErr := journal.Append(s.journalPath, "agentcontrol.activity", activityEvent{Version: 1, Activity: activity}, func(events []journal.Event) error {
				_, replayErr := Replay(events)
				return replayErr
			})
			if appendErr == nil {
				break
			}
			if attempt == 2 {
				return errors.New("agent activity contention")
			}
		}
	}
	return nil
}

func validateMessageRecord(record MessageRecord) error {
	digest := sha256.Sum256([]byte(record.Body))
	if record.Bytes != len(record.Body) || record.Bytes < 1 || record.Bytes > MaximumBodyBytes || !utf8.ValidString(record.Body) || strings.IndexByte(record.Body, 0) >= 0 || record.Message.BodySHA256 != hex.EncodeToString(digest[:]) || safepath.RequireDigest(record.Message.MessageID) != nil || safepath.RequireDigest(record.Message.FromAgentID) != nil || safepath.RequireDigest(record.Message.ToAgentID) != nil || record.Message.Sequence < 1 {
		return errors.New("invalid stored agent message")
	}
	return nil
}

func validateActivity(state Snapshot, activity Activity) error {
	if len(state.Activities) >= MaximumActivities || safepath.RequireDigest(activity.AgentID) != nil || activity.Sequence != nextActivitySequence(state, activity.AgentID) {
		return errors.New("invalid agent activity sequence")
	}
	switch activity.Kind {
	case "message":
		if safepath.RequireDigest(activity.MessageID) != nil || activity.TurnID != "" || activity.TurnSequence != 0 || activity.Status != "" || activity.ResultSHA256 != "" || activity.ResultID != "" || activity.InterruptID != "" || activity.Interrupt != "" {
			return errors.New("invalid message activity")
		}
	case "status":
		if activity.MessageID != "" || activity.TurnID != "" || activity.TurnSequence != 0 || activity.ResultID != "" || activity.InterruptID != "" || activity.Interrupt != "" || !validStatus(activity.Status) || activity.Status == agenttree.StatusSucceeded && safepath.RequireDigest(activity.ResultSHA256) != nil || activity.Status != agenttree.StatusSucceeded && activity.ResultSHA256 != "" {
			return errors.New("invalid status activity")
		}
	case "turn":
		if activity.MessageID != "" || activity.ResultID != "" || activity.InterruptID != "" || activity.Interrupt != "" || safepath.RequireDigest(activity.TurnID) != nil || activity.TurnSequence < 1 || activity.Status != agenttree.StatusRunning && activity.Status != agenttree.StatusSucceeded && activity.Status != agenttree.StatusUnknown || activity.Status == agenttree.StatusSucceeded && safepath.RequireDigest(activity.ResultSHA256) != nil || activity.Status != agenttree.StatusSucceeded && activity.ResultSHA256 != "" {
			return errors.New("invalid turn activity")
		}
		priorStatus := agenttree.Status("")
		for index := len(state.Activities) - 1; index >= 0; index-- {
			prior := state.Activities[index]
			if prior.Kind == "turn" && prior.TurnID == activity.TurnID {
				if prior.AgentID != activity.AgentID || prior.TurnSequence != activity.TurnSequence {
					return errors.New("agent turn identity changed")
				}
				priorStatus = prior.Status
				break
			}
		}
		if priorStatus == "" && activity.Status != agenttree.StatusRunning || priorStatus == agenttree.StatusRunning && activity.Status != agenttree.StatusSucceeded && activity.Status != agenttree.StatusUnknown || priorStatus == agenttree.StatusUnknown && activity.Status != agenttree.StatusSucceeded || priorStatus == agenttree.StatusSucceeded {
			return errors.New("invalid agent turn activity transition")
		}
	case "interrupt":
		if activity.MessageID != "" || activity.ResultID != "" || safepath.RequireDigest(activity.TurnID) != nil || activity.TurnSequence < 1 || activity.Status != "" || activity.ResultSHA256 != "" || safepath.RequireDigest(activity.InterruptID) != nil || !validInterruptOutcome(activity.Interrupt) {
			return errors.New("invalid interrupt activity")
		}
	case "result":
		if activity.MessageID != "" || safepath.RequireDigest(activity.ResultID) != nil || safepath.RequireDigest(activity.TurnID) != nil || activity.TurnSequence < 1 || activity.Status != agenttree.StatusSucceeded || safepath.RequireDigest(activity.ResultSHA256) != nil || activity.InterruptID != "" || activity.Interrupt != "" {
			return errors.New("invalid accepted result activity")
		}
	default:
		return errors.New("invalid agent activity kind")
	}
	return nil
}

func validStatus(status agenttree.Status) bool {
	return status == agenttree.StatusQueued || status == agenttree.StatusRunning || status == agenttree.StatusSucceeded || status == agenttree.StatusFailed || status == agenttree.StatusCanceled || status == agenttree.StatusUnknown
}

func nextActivitySequence(state Snapshot, agentID string) int {
	if latest, ok := state.Latest[agentID]; ok {
		return latest.Sequence + 1
	}
	return 1
}

func exactStoredMessage(state Snapshot, messageID string) (MessageRecord, bool) {
	for _, record := range state.Messages {
		if record.Message.MessageID == messageID {
			return record, true
		}
	}
	return MessageRecord{}, false
}

func exactTreeMessage(state agenttree.Snapshot, messageID string) (agenttree.Message, bool) {
	for _, message := range state.Messages {
		if message.MessageID == messageID {
			return message, true
		}
	}
	return agenttree.Message{}, false
}

func validateService(service *Service) error {
	if service == nil || service.treeID == "" || !exactAbsolutePath(service.treePath) || !exactAbsolutePath(service.journalPath) {
		return errors.New("invalid agent control service")
	}
	return nil
}

func exactAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context required")
	}
	return ctx.Err()
}
