package agenttree

import (
	"context"
	"errors"
	"time"

	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
)

// MaximumMessages bounds durable mailbox metadata in one tree.
const MaximumMessages = 4096

// Message binds communication to an admitted artifact digest. Message bodies
// remain in the controller-owned context store and do not enter this journal.
type Message struct {
	MessageID   string `json:"message_id"`
	FromAgentID string `json:"from_agent_id"`
	ToAgentID   string `json:"to_agent_id"`
	BodySHA256  string `json:"body_sha256"`
	Sequence    int    `json:"sequence"`
	Wake        bool   `json:"wake"`
}

// MessageRequest names exact sender, recipient, message, and body identities.
type MessageRequest struct {
	MessageID   string
	FromAgentID string
	ToAgentID   string
	BodySHA256  string
}

type messageEvent struct {
	Version int     `json:"version"`
	Message Message `json:"message"`
}

// Send appends a non-waking message. It cannot create runtime work.
func Send(journalPath string, request MessageRequest) (Message, error) {
	return enqueueMessage(journalPath, request, false)
}

// FollowUp appends a waking message. The durable Wake bit is a request for a
// later TaskPool admission, never proof that a turn started.
func FollowUp(journalPath string, request MessageRequest) (Message, error) {
	return enqueueMessage(journalPath, request, true)
}

func enqueueMessage(journalPath string, request MessageRequest, wake bool) (Message, error) {
	if safepath.RequireDigest(request.MessageID) != nil || safepath.RequireDigest(request.BodySHA256) != nil {
		return Message{}, errors.New("invalid agent message identity")
	}
	state, err := Inspect(journalPath)
	if err != nil {
		return Message{}, err
	}
	from, fromOK := findNode(state.Nodes, request.FromAgentID)
	to, toOK := findNode(state.Nodes, request.ToAgentID)
	if !fromOK || !toOK || !messageEndpointsAvailable(from.Status, to.Status, wake) || len(state.Messages) >= MaximumMessages {
		return Message{}, errors.New("agent message endpoint unavailable")
	}
	sequence := 1
	for _, message := range state.Messages {
		if message.ToAgentID == request.ToAgentID && message.Sequence >= sequence {
			sequence = message.Sequence + 1
		}
	}
	message := Message{MessageID: request.MessageID, FromAgentID: request.FromAgentID, ToAgentID: request.ToAgentID, BodySHA256: request.BodySHA256, Sequence: sequence, Wake: wake}
	_, err = journal.Append(journalPath, eventMessage, messageEvent{Version: 1, Message: message}, func(events []journal.Event) error {
		state, replayErr := replay(events)
		if replayErr == nil && len(state.Messages) > MaximumMessages {
			return errors.New("agent message limit exceeded")
		}
		return replayErr
	})
	return message, err
}

func messageEndpointsAvailable(from, to Status, wake bool) bool {
	if !wake {
		return true
	}
	return from != StatusUnknown && to != StatusUnknown
}

// MessagesAfter returns a bounded per-recipient FIFO page.
func MessagesAfter(state Snapshot, recipient string, afterSequence, limit int) ([]Message, error) {
	if _, ok := findNode(state.Nodes, recipient); !ok || afterSequence < 0 || limit < 1 || limit > 256 {
		return nil, errors.New("invalid agent mailbox page")
	}
	result := make([]Message, 0, limit)
	for _, message := range state.Messages {
		if message.ToAgentID == recipient && message.Sequence > afterSequence {
			result = append(result, message)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

// Wait returns once durable mail exists after the supplied sequence. A caller
// deadline is required, so a missing message cannot create an unbounded wait.
func Wait(ctx context.Context, journalPath, recipient string, afterSequence, limit int) ([]Message, error) {
	if ctx == nil {
		return nil, errors.New("agent mailbox wait context required")
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("agent mailbox wait requires deadline")
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := Inspect(journalPath)
		if err != nil {
			return nil, err
		}
		messages, err := MessagesAfter(state, recipient, afterSequence, limit)
		if err != nil || len(messages) > 0 {
			return messages, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
