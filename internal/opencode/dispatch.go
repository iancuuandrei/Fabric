package opencode

import (
	"context"
	"errors"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
)

// DispatchIntent is persisted before an inference request. The surrounding
// controller remains responsible for host ownership and model/budget admission.
type DispatchIntent struct {
	Binding          Binding                      `json:"binding"`
	Text             string                       `json:"text"`
	StructuredOutput *StructuredOutputExpectation `json:"structured_output,omitempty"`
}

// DispatchState reconstructs one session's durable dispatch and observation.
type DispatchState struct {
	Intent   *DispatchIntent      `json:"intent"`
	Accepted bool                 `json:"accepted"`
	Observed bool                 `json:"observed"`
	TextTurn *TextTurnObservation `json:"text_turn,omitempty"`
}

func replayDispatch(events []journal.Event) (DispatchState, error) {
	s := DispatchState{}
	for _, e := range events {
		switch e.Kind {
		case "opencode.dispatch-intent":
			if s.Intent != nil {
				return DispatchState{}, errors.New("duplicate dispatch intent")
			}
			var intent DispatchIntent
			if err := canonical.Decode(e.Payload, &intent); err != nil {
				return DispatchState{}, err
			}
			if err := validatePrompt(intent.Binding, intent.Text); err != nil {
				return DispatchState{}, err
			}
			if intent.StructuredOutput != nil {
				if err := intent.StructuredOutput.Validate(); err != nil {
					return DispatchState{}, err
				}
			}
			s.Intent = &intent
		case "opencode.dispatch-accepted", "opencode.prompt-observed":
			var binding Binding
			if err := canonical.Decode(e.Payload, &binding); err != nil {
				return DispatchState{}, err
			}
			if s.Intent == nil || binding != s.Intent.Binding || s.Observed || s.TextTurn != nil {
				return DispatchState{}, errors.New("invalid dispatch observation")
			}
			if e.Kind == "opencode.dispatch-accepted" {
				if s.Accepted {
					return DispatchState{}, errors.New("duplicate acceptance")
				}
				s.Accepted = true
			} else {
				s.Observed = true
			}
		case "opencode.text-turn-observed":
			if s.Intent == nil || s.TextTurn != nil {
				return DispatchState{}, errors.New("invalid text turn observation")
			}
			var record struct {
				Transcript string `json:"transcript"`
			}
			if err := canonical.Decode(e.Payload, &record); err != nil {
				return DispatchState{}, err
			}
			a, text, err := decodeTextTurn([]byte(record.Transcript), s.Intent.Binding, s.Intent.Text)
			if err != nil {
				return DispatchState{}, err
			}
			s.TextTurn = &TextTurnObservation{Assistant: a, Text: text}
			s.Observed = true
		default:
			return DispatchState{}, errors.New("unknown dispatch event")
		}
	}
	return s, nil
}

func appendDispatch(path, kind string, payload any) error {
	_, err := journal.Append(path, kind, payload, func(events []journal.Event) error { _, err := replayDispatch(events); return err })
	return err
}

// SubmitPrompt journals intent before the single POST. An existing intent rejects
// another submission, including after a crash before the POST; use recovery.
// The caller owns a private journal path in an already admitted host context.
func (c *Client) SubmitPrompt(ctx context.Context, path string, intent DispatchIntent) error {
	if err := validatePrompt(intent.Binding, intent.Text); err != nil {
		return err
	}
	if intent.StructuredOutput != nil {
		if err := intent.StructuredOutput.Validate(); err != nil {
			return err
		}
	}
	if err := appendDispatch(path, "opencode.dispatch-intent", intent); err != nil {
		return err
	}
	if err := c.promptOnce(ctx, intent.Binding, intent.Text, intent.StructuredOutput); err != nil {
		return err
	}
	return appendDispatch(path, "opencode.dispatch-accepted", intent.Binding)
}

// RecoverPrompt never posts. The expected intent must match the durable journal
// exactly. Observation confirms user-message persistence, not model completion.
func (c *Client) RecoverPrompt(ctx context.Context, path string, expected DispatchIntent) error {
	events, err := journal.Read(path)
	if err != nil {
		return err
	}
	s, err := replayDispatch(events)
	if err != nil {
		return err
	}
	if s.Intent == nil || !equalCanonical(*s.Intent, expected) {
		return errors.New("dispatch intent mismatch")
	}
	if s.Observed {
		return nil
	}
	if expected.StructuredOutput != nil {
		err = c.ReadPromptWithStructuredOutput(ctx, expected.Binding, expected.Text, *expected.StructuredOutput)
	} else {
		err = c.ReadPrompt(ctx, expected.Binding, expected.Text)
	}
	if err != nil {
		return err
	}
	return appendDispatch(path, "opencode.prompt-observed", expected.Binding)
}
