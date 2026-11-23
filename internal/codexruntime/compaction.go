package codexruntime

import (
	"encoding/json"
	"errors"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexrpc"
)

const (
	compactionLifecycleVersion = 1
	maxCompactionItems         = 64
	maxCompactionEvents        = maxCompactionItems * 2
	maxCompactionParams        = 16 * 1024
)

// CompactionLifecycle records a metadata-only Codex compaction item phase.
// It is diagnostic evidence bound to one already-dispatched turn.
type CompactionLifecycle struct {
	Version  int    `json:"version"`
	ThreadID string `json:"thread_id"`
	TurnID   string `json:"turn_id"`
	ItemID   string `json:"item_id"`
	Phase    string `json:"phase"`
}

// CompactionUnknown records why lifecycle coverage for the current turn is
// incomplete. It never asserts that a compaction completed.
type CompactionUnknown struct {
	Version  int    `json:"version"`
	ThreadID string `json:"thread_id"`
	TurnID   string `json:"turn_id,omitempty"`
	Reason   string `json:"reason"`
}

// CompactionSummary exposes only proven complete lifecycle pairs. Count is
// positive only when the notification stream completed without ambiguity.
type CompactionSummary struct {
	Coverage string `json:"coverage"`
	Count    int    `json:"count,omitempty"`
}

type compactionItemState struct {
	turnID    string
	started   bool
	completed bool
}

func compactionNotification(m codexrpc.Message) (CompactionLifecycle, bool, error) {
	phase := ""
	switch m.Method {
	case "item/started":
		phase = "started"
	case "item/completed":
		phase = "completed"
	default:
		return CompactionLifecycle{}, false, nil
	}
	if len(m.Params) == 0 || len(m.Params) > codexrpc.MaxMessage {
		return CompactionLifecycle{}, true, errors.New("invalid compaction notification envelope")
	}
	itemType, err := notificationItemType(m.Params)
	if err != nil {
		return CompactionLifecycle{}, true, err
	}
	if itemType != "contextCompaction" {
		if !knownThreadItemType(itemType) {
			return CompactionLifecycle{}, true, errors.New("unknown provider item type")
		}
		// Codex sends the complete ThreadItem on lifecycle notifications. Ordinary
		// user and agent messages can therefore exceed the smaller compaction
		// metadata bound without making compaction coverage ambiguous.
		return CompactionLifecycle{}, false, nil
	}
	if len(m.Params) > maxCompactionParams {
		return CompactionLifecycle{}, true, errors.New("invalid compaction notification envelope")
	}
	var envelope compactionNotificationEnvelope
	if err := decodeCompactionEnvelope(m.Method, m.Params, &envelope); err != nil {
		return CompactionLifecycle{}, true, errors.New("invalid compaction notification envelope")
	}
	itemID, compaction, itemErr := compactionItemID(envelope.Item)
	if itemErr != nil {
		return CompactionLifecycle{}, true, itemErr
	}
	if !compaction {
		return CompactionLifecycle{}, false, nil
	}
	if envelope.ThreadID == "" || len(envelope.ThreadID) > 256 || envelope.TurnID == "" || len(envelope.TurnID) > 256 || itemID == "" || len(itemID) > 256 {
		return CompactionLifecycle{}, true, errors.New("invalid compaction notification identity")
	}
	return CompactionLifecycle{Version: compactionLifecycleVersion, ThreadID: envelope.ThreadID, TurnID: envelope.TurnID, ItemID: itemID, Phase: phase}, true, nil
}

type compactionNotificationEnvelope struct {
	ThreadID string          `json:"threadId"`
	TurnID   string          `json:"turnId"`
	Item     json.RawMessage `json:"item"`
}

func notificationItemType(raw json.RawMessage) (string, error) {
	var envelope struct {
		Item json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Item) == 0 {
		return "", errors.New("invalid provider item")
	}
	var discriminator struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(envelope.Item, &discriminator); err != nil || discriminator.Type == "" || len(discriminator.Type) > 64 {
		return "", errors.New("invalid provider item")
	}
	return discriminator.Type, nil
}

func knownThreadItemType(itemType string) bool {
	switch itemType {
	case "userMessage", "hookPrompt", "agentMessage", "functionCallOutput", "plan", "reasoning",
		"commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "collabAgentToolCall",
		"subAgentActivity", "webSearch", "imageView", "sleep", "imageGeneration", "enteredReviewMode", "exitedReviewMode":
		return true
	default:
		return false
	}
}

func decodeCompactionEnvelope(method string, raw json.RawMessage, envelope *compactionNotificationEnvelope) error {
	switch method {
	case "item/started":
		var wire struct {
			ThreadID    string          `json:"threadId"`
			TurnID      string          `json:"turnId"`
			Item        json.RawMessage `json:"item"`
			StartedAtMS *int64          `json:"startedAtMs,omitempty"`
		}
		if err := canonical.Decode(raw, &wire); err != nil || !validNotificationTimestamp(wire.StartedAtMS) {
			return errors.New("invalid started notification")
		}
		envelope.ThreadID, envelope.TurnID, envelope.Item = wire.ThreadID, wire.TurnID, wire.Item
		return nil
	case "item/completed":
		var wire struct {
			ThreadID      string          `json:"threadId"`
			TurnID        string          `json:"turnId"`
			Item          json.RawMessage `json:"item"`
			CompletedAtMS *int64          `json:"completedAtMs,omitempty"`
		}
		if err := canonical.Decode(raw, &wire); err != nil || !validNotificationTimestamp(wire.CompletedAtMS) {
			return errors.New("invalid completed notification")
		}
		envelope.ThreadID, envelope.TurnID, envelope.Item = wire.ThreadID, wire.TurnID, wire.Item
		return nil
	default:
		return errors.New("unsupported item notification method")
	}
}

func validNotificationTimestamp(timestamp *int64) bool {
	return timestamp == nil || *timestamp >= 0 && *timestamp <= 9007199254740991
}

func compactionItemID(raw json.RawMessage) (string, bool, error) {
	var discriminator struct {
		Type string `json:"type"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &discriminator) != nil {
		return "", false, errors.New("invalid provider item")
	}
	if discriminator.Type != "contextCompaction" {
		return "", false, nil
	}
	var item struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if canonical.Decode(raw, &item) != nil || item.ID == "" || len(item.ID) > 256 {
		return "", true, errors.New("invalid context compaction item")
	}
	return item.ID, true, nil
}

func (a *Adapter) observeCompaction(m codexrpc.Message) error {
	lifecycle, relevant, parseErr := compactionNotification(m)
	if !relevant {
		return nil
	}
	s, err := Inspect(a.JournalPath)
	if err != nil {
		return err
	}
	if s.Thread == nil || !s.TurnPending || s.Result != nil {
		return nil
	}
	if s.compactionUnknown {
		return nil
	}
	if parseErr != nil {
		return a.recordCompactionUnknown(s, "INVALID_NOTIFICATION")
	}
	if lifecycle.ThreadID != s.Thread.ThreadID || s.TurnID != "" && lifecycle.TurnID != s.TurnID {
		return a.recordCompactionUnknown(s, "IDENTITY_MISMATCH")
	}
	if s.compactionEvents >= maxCompactionEvents {
		return a.recordCompactionUnknown(s, "EVENT_LIMIT")
	}
	item := s.compactionItems[lifecycle.ItemID]
	if item == nil && len(s.compactionItems) >= maxCompactionItems {
		return a.recordCompactionUnknown(s, "ITEM_LIMIT")
	}
	if item != nil && (lifecycle.Phase == "started" && item.started || lifecycle.Phase == "completed" && item.completed) {
		return a.recordCompactionUnknown(s, "DUPLICATE_PHASE")
	}
	if lifecycle.Phase == "completed" && (item == nil || !item.started) {
		return a.recordCompactionUnknown(s, "UNMATCHED_COMPLETION")
	}
	if err := appendEvent(a.JournalPath, "runtime.compaction-item", lifecycle); err != nil {
		return a.degradeCompactionAppend(err, lifecycle.ThreadID, lifecycle.TurnID)
	}
	return nil
}

func (a *Adapter) recordCompactionUnknown(s State, reason string) error {
	unknown := CompactionUnknown{Version: compactionLifecycleVersion, ThreadID: s.Thread.ThreadID, TurnID: s.TurnID, Reason: reason}
	if err := appendEvent(a.JournalPath, "runtime.compaction-unknown", unknown); err != nil {
		return a.degradeCompactionAppend(err, unknown.ThreadID, unknown.TurnID)
	}
	return nil
}

// degradeCompactionAppend treats only a validated-journal append failure as
// unavailable advisory telemetry. An unreadable or substituted journal stays
// a hard error and cannot be bypassed by the semantic-result path.
func (a *Adapter) degradeCompactionAppend(appendErr error, threadID, turnID string) error {
	s, inspectErr := Inspect(a.JournalPath)
	if inspectErr != nil {
		return errors.Join(appendErr, inspectErr)
	}
	if s.Thread == nil || s.Thread.ThreadID != threadID || !s.TurnPending || s.Result != nil ||
		s.TurnID != "" && turnID != "" && s.TurnID != turnID ||
		s.TurnID == "" && s.ToolTurnID != "" && turnID != "" && s.ToolTurnID != turnID {
		return appendErr
	}
	a.compactionUnavailable = true
	return nil
}

func (s *State) compactionEvent(kind string, raw json.RawMessage) error {
	switch kind {
	case "runtime.compaction-item":
		var lifecycle CompactionLifecycle
		if canonical.Decode(raw, &lifecycle) != nil || lifecycle.Version != compactionLifecycleVersion || lifecycle.ThreadID == "" || lifecycle.TurnID == "" || lifecycle.ItemID == "" || len(lifecycle.ThreadID) > 256 || len(lifecycle.TurnID) > 256 || len(lifecycle.ItemID) > 256 || lifecycle.Phase != "started" && lifecycle.Phase != "completed" {
			return errors.New("invalid compaction lifecycle record")
		}
		if s.Thread == nil || !s.TurnPending || s.Result != nil || s.compactionUnknown || lifecycle.ThreadID != s.Thread.ThreadID || s.TurnID != "" && lifecycle.TurnID != s.TurnID || s.compactionEvents >= maxCompactionEvents {
			return errors.New("compaction lifecycle binding rejected")
		}
		item := s.compactionItems[lifecycle.ItemID]
		if item == nil {
			if len(s.compactionItems) >= maxCompactionItems {
				return errors.New("compaction item bound exceeded")
			}
			if s.compactionItems == nil {
				s.compactionItems = make(map[string]*compactionItemState)
			}
			item = &compactionItemState{}
			item.turnID = lifecycle.TurnID
			s.compactionItems[lifecycle.ItemID] = item
		} else if item.turnID != lifecycle.TurnID {
			return errors.New("compaction item turn substitution")
		}
		if lifecycle.Phase == "started" {
			if item.started {
				return errors.New("duplicate compaction start")
			}
			item.started = true
		} else {
			if item.completed || !item.started {
				return errors.New("unmatched compaction completion")
			}
			item.completed = true
		}
		s.compactionEvents++
	case "runtime.compaction-unknown":
		var unknown CompactionUnknown
		if canonical.Decode(raw, &unknown) != nil || unknown.Version != compactionLifecycleVersion || unknown.ThreadID == "" || len(unknown.ThreadID) > 256 || unknown.TurnID != "" && len(unknown.TurnID) > 256 || !validCompactionUnknownReason(unknown.Reason) {
			return errors.New("invalid compaction unknown marker")
		}
		if s.Thread == nil || !s.TurnPending || s.Result != nil || s.compactionUnknown || unknown.ThreadID != s.Thread.ThreadID || s.TurnID != "" && unknown.TurnID != "" && unknown.TurnID != s.TurnID {
			return errors.New("compaction unknown binding rejected")
		}
		s.compactionUnknown = true
		s.compactionUnknownTurn = unknown.TurnID
	default:
		return errors.New("unknown compaction event")
	}
	return nil
}

func validCompactionUnknownReason(reason string) bool {
	switch reason {
	case "INVALID_NOTIFICATION", "IDENTITY_MISMATCH", "EVENT_LIMIT", "ITEM_LIMIT", "DUPLICATE_PHASE", "UNMATCHED_COMPLETION", "READBACK_UNPAIRED":
		return true
	default:
		return false
	}
}

func (s *State) finalizeCompactionSummary() {
	if s.compactionEvents == 0 && !s.compactionUnknown {
		return
	}
	summary := &CompactionSummary{Coverage: "UNKNOWN"}
	if s.compactionUnknown || !s.NotificationStreamComplete || s.TurnStatus != "completed" {
		s.Compaction = summary
		return
	}
	for _, item := range s.compactionItems {
		if !item.started || !item.completed {
			s.Compaction = summary
			return
		}
		summary.Count++
	}
	if summary.Count > 0 {
		summary.Coverage = "OBSERVED"
		s.Compaction = summary
	}
}

func (a *Adapter) recordReadbackCompaction(turn codexrpc.Turn, threadID string) error {
	s, err := Inspect(a.JournalPath)
	if err != nil {
		return err
	}
	if s.compactionUnknown {
		return nil
	}
	items := make(map[string]struct{})
	for _, raw := range turn.Items {
		itemID, compaction, itemErr := compactionItemID(raw)
		if !compaction {
			continue
		}
		if itemErr != nil {
			return a.recordCompactionUnknown(s, "READBACK_UNPAIRED")
		}
		if _, duplicate := items[itemID]; duplicate {
			return a.recordCompactionUnknown(s, "READBACK_UNPAIRED")
		}
		items[itemID] = struct{}{}
	}
	if len(items) == 0 && len(s.compactionItems) == 0 && s.compactionEvents == 0 {
		return nil
	}
	if s.Thread == nil || turn.Status != "completed" || turn.ID != s.TurnID || threadID != s.Thread.ThreadID {
		if s.Thread == nil {
			return errors.New("compaction evidence has no bound thread")
		}
		return a.recordCompactionUnknown(s, "READBACK_UNPAIRED")
	}
	// Empty ItemsView is accepted as a complete result by Turn.Result for
	// older servers. Any other non-full view cannot prove correspondence.
	if turn.ItemsView != "" && turn.ItemsView != "full" {
		if len(items) != 0 || len(s.compactionItems) != 0 {
			return a.recordCompactionUnknown(s, "READBACK_UNPAIRED")
		}
		return nil
	}
	if len(items) != len(s.compactionItems) {
		if len(items) != 0 || len(s.compactionItems) != 0 {
			return a.recordCompactionUnknown(s, "READBACK_UNPAIRED")
		}
		return nil
	}
	for itemID := range items {
		state := s.compactionItems[itemID]
		if state == nil || !state.started || !state.completed || state.turnID != turn.ID {
			return a.recordCompactionUnknown(s, "READBACK_UNPAIRED")
		}
	}
	return nil
}
