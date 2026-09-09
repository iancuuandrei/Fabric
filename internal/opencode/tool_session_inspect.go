package opencode

import (
	"errors"

	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
)

// ToolSessionRecord is one completed, journaled tool-session creation. Binding
// and SessionID are the exact replayed observation; JournalHead binds them to
// the complete validated journal read which produced the record.
type ToolSessionRecord struct {
	Binding     ToolSessionBinding `json:"binding"`
	SessionID   string             `json:"session_id"`
	JournalHead string             `json:"journal_head"`
}

// ReadToolSession reads one completed tool-session journal without contacting
// OpenCode. It never creates or reconciles a session. Intent-only history is an
// unresolved outcome which must be recovered through an explicitly owned live
// client before this reader can return a record.
func ReadToolSession(path string, expected ToolSessionBinding) (ToolSessionRecord, error) {
	var result ToolSessionRecord
	expected = snapshotToolSessionBinding(expected)
	if err := expected.Validate(); err != nil {
		return result, err
	}
	events, err := journal.Read(path)
	if err != nil {
		return result, err
	}
	state, err := replayToolSessionCreation(events)
	if err != nil {
		return result, err
	}
	if state.Binding == nil {
		return result, errors.New("tool session creation intent missing")
	}
	if !equalToolSessionBinding(*state.Binding, expected) {
		return result, errors.New("tool session creation intent mismatch")
	}
	if state.ID == "" {
		return result, errors.New("tool session creation unresolved")
	}
	if len(events) == 0 || safepath.RequireDigest(events[len(events)-1].Hash) != nil {
		return result, errors.New("tool session journal head missing")
	}
	recorded := snapshotToolSessionBinding(*state.Binding)
	if err := recorded.Validate(); err != nil {
		return result, err
	}
	return ToolSessionRecord{Binding: recorded, SessionID: state.ID, JournalHead: events[len(events)-1].Hash}, nil
}
