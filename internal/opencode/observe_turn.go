package opencode

import (
	"context"
	"errors"

	"harness.local/engorch/internal/journal"
)

// TextTurnObservation is a validated persisted snapshot. It deliberately has no
// terminal flag: admission still requires the caller's host lifecycle evidence.
type TextTurnObservation struct {
	Assistant Assistant `json:"assistant"`
	Text      string    `json:"text"`
}

// ObserveTextTurn records a complete text-turn snapshot or replays the existing
// record offline. The raw transcript is stored as a string to preserve decimal
// wire fields without weakening the journal's canonical numeric contract.
func (c *Client) ObserveTextTurn(ctx context.Context, path string, expected DispatchIntent) (TextTurnObservation, error) {
	events, err := journal.Read(path)
	if err != nil {
		return TextTurnObservation{}, err
	}
	state, err := replayDispatch(events)
	if err != nil {
		return TextTurnObservation{}, err
	}
	if state.Intent == nil || !equalCanonical(*state.Intent, expected) {
		return TextTurnObservation{}, errors.New("turn intent mismatch")
	}
	if state.TextTurn != nil {
		return *state.TextTurn, nil
	}
	raw, err := c.read(ctx, "/session/"+expected.Binding.SessionID+"/message")
	if err != nil {
		return TextTurnObservation{}, err
	}
	a, text, err := decodeTextTurn(raw, expected.Binding, expected.Text)
	if err != nil {
		return TextTurnObservation{}, err
	}
	record := struct {
		Transcript string `json:"transcript"`
	}{string(raw)}
	if err := appendDispatch(path, "opencode.text-turn-observed", record); err != nil {
		return TextTurnObservation{}, err
	}
	return TextTurnObservation{Assistant: a, Text: text}, nil
}
