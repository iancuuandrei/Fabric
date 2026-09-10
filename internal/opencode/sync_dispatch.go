package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
)

// SynchronousTextTurnObservation is a validated persisted snapshot. It is not
// evidence that the owned host is quiescent or that provider-side effects ended.
type SynchronousTextTurnObservation struct {
	Assistant Assistant `json:"assistant"`
	Text      string    `json:"text"`
}

type synchronousTurnRecord struct {
	Response   string `json:"response,omitempty"`
	Transcript string `json:"transcript"`
}

type synchronousDispatchState struct {
	Intent      *DispatchIntent
	Observation *SynchronousTextTurnObservation
}

const maxSynchronousEvidenceBytes = (1 << 20) - (4 << 10)

func replaySynchronousDispatch(events []journal.Event) (synchronousDispatchState, error) {
	state := synchronousDispatchState{}
	for _, event := range events {
		switch event.Kind {
		case "opencode.sync-dispatch-intent":
			if state.Intent != nil {
				return synchronousDispatchState{}, errors.New("duplicate synchronous dispatch intent")
			}
			var intent DispatchIntent
			if err := canonical.Decode(event.Payload, &intent); err != nil {
				return synchronousDispatchState{}, err
			}
			if err := validatePrompt(intent.Binding, intent.Text); err != nil {
				return synchronousDispatchState{}, err
			}
			if intent.StructuredOutput != nil {
				if err := intent.StructuredOutput.Validate(); err != nil {
					return synchronousDispatchState{}, err
				}
			}
			state.Intent = &intent
		case "opencode.sync-turn-observed":
			if state.Intent == nil || state.Observation != nil {
				return synchronousDispatchState{}, errors.New("invalid synchronous turn observation")
			}
			var record synchronousTurnRecord
			if err := canonical.Decode(event.Payload, &record); err != nil {
				return synchronousDispatchState{}, err
			}
			observation, err := validateSynchronousRecord(record, *state.Intent)
			if err != nil {
				return synchronousDispatchState{}, err
			}
			state.Observation = &observation
		default:
			return synchronousDispatchState{}, errors.New("unknown synchronous dispatch event")
		}
	}
	return state, nil
}

func appendSynchronousDispatch(path, kind string, payload any) error {
	_, err := journal.Append(path, kind, payload, func(events []journal.Event) error {
		_, err := replaySynchronousDispatch(events)
		return err
	})
	return err
}

// SubmitSynchronousTextTurn records the exact intent before one synchronous
// POST. It validates both the returned assistant envelope and an independent
// transcript readback before recording raw evidence. Any error is unresolved;
// callers must use recovery and must never automatically submit again.
func (c *Client) SubmitSynchronousTextTurn(ctx context.Context, path string, intent DispatchIntent) (SynchronousTextTurnObservation, error) {
	if err := requireSynchronousRequest(ctx, c, intent); err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	if err := appendSynchronousDispatch(path, "opencode.sync-dispatch-intent", intent); err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	body, err := synchronousPromptBody(intent)
	if err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	response, err := c.request(ctx, http.MethodPost, "/session/"+intent.Binding.SessionID+"/message", body)
	if err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	returned, returnedText, err := decodeSynchronousResponse(response, intent.Binding)
	if err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	transcript, err := c.read(ctx, "/session/"+intent.Binding.SessionID+"/message")
	if err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	observed, text, err := decodeTextTurn(transcript, intent.Binding, intent.Text)
	if err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	if returned != observed || returnedText != text {
		return SynchronousTextTurnObservation{}, errors.New("synchronous response and transcript differ")
	}
	record := synchronousTurnRecord{Response: string(response), Transcript: string(transcript)}
	if err := validateSynchronousEvidenceBound(record); err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	if err := appendSynchronousDispatch(path, "opencode.sync-turn-observed", record); err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	return SynchronousTextTurnObservation{Assistant: observed, Text: text}, nil
}

// RecoverSynchronousTextTurn never sends a prompt. It replays an existing raw
// observation offline, or performs one GET-only transcript observation for the
// exact unresolved intent. The result remains a snapshot, not host terminality.
func (c *Client) RecoverSynchronousTextTurn(ctx context.Context, path string, expected DispatchIntent) (SynchronousTextTurnObservation, error) {
	if err := validatePrompt(expected.Binding, expected.Text); err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	events, err := journal.Read(path)
	if err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	state, err := replaySynchronousDispatch(events)
	if err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	if state.Intent == nil || !equalCanonical(*state.Intent, expected) {
		return SynchronousTextTurnObservation{}, errors.New("synchronous dispatch intent mismatch")
	}
	if state.Observation != nil {
		return *state.Observation, nil
	}
	if err := requireSynchronousContext(ctx, c); err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	transcript, err := c.read(ctx, "/session/"+expected.Binding.SessionID+"/message")
	if err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	assistant, text, err := decodeTextTurn(transcript, expected.Binding, expected.Text)
	if err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	record := synchronousTurnRecord{Transcript: string(transcript)}
	if err := validateSynchronousEvidenceBound(record); err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	if err := appendSynchronousDispatch(path, "opencode.sync-turn-observed", record); err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	return SynchronousTextTurnObservation{Assistant: assistant, Text: text}, nil
}

func requireSynchronousRequest(ctx context.Context, c *Client, intent DispatchIntent) error {
	if err := validatePrompt(intent.Binding, intent.Text); err != nil {
		return err
	}
	if intent.StructuredOutput != nil {
		return errors.New("structured output requires a synchronous tool turn")
	}
	return requireSynchronousContext(ctx, c)
}

func requireSynchronousContext(ctx context.Context, c *Client) error {
	if ctx == nil {
		return errors.New("synchronous OpenCode request requires context")
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("synchronous OpenCode request requires deadline")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.http == nil {
		return errors.New("uninitialized OpenCode client")
	}
	return nil
}

func synchronousPromptBody(intent DispatchIntent) ([]byte, error) {
	return promptBody(intent.Binding, intent.Text, intent.StructuredOutput)
}

func decodeSynchronousResponse(raw []byte, binding Binding) (Assistant, string, error) {
	decoded, err := decodeSynchronousResponseWithOptions(raw, binding, nil)
	if err != nil {
		return Assistant{}, "", err
	}
	return decoded.Assistant, decoded.Text, nil
}

type synchronousResponseObservation struct {
	Assistant            Assistant
	Text                 string
	StructuredOutput     *json.RawMessage
	StructuredOutputTool *StructuredOutputToolObservation
}

func decodeSynchronousResponseWithStructuredOutput(raw []byte, binding Binding, expectation StructuredOutputExpectation) (synchronousResponseObservation, error) {
	return decodeSynchronousResponseWithOptions(raw, binding, &expectation)
}

func decodeSynchronousResponseWithOptions(raw []byte, binding Binding, expectation *StructuredOutputExpectation) (synchronousResponseObservation, error) {
	var result synchronousResponseObservation
	if expectation != nil {
		if err := expectation.Validate(); err != nil {
			return result, err
		}
	}
	envelope, err := wireObject(raw)
	if err != nil {
		return result, err
	}
	if expectation == nil {
		info, infoErr := wireObject(envelope["info"])
		if infoErr != nil {
			return result, infoErr
		}
		if _, ok := info["structured"]; ok {
			return result, errors.New("structured output requires an explicit expectation")
		}
		assistant, err := DecodeAssistant(envelope["info"], binding)
		if err != nil {
			return result, err
		}
		text, err := DecodeTextParts(envelope["parts"], assistant)
		if err != nil {
			return result, err
		}
		result.Assistant, result.Text = assistant, text
		return result, nil
	}
	assistant, err := decodeTurnAssistant(envelope["info"], binding)
	if err != nil {
		return result, err
	}
	if assistant.Finish != "tool-calls" || len(assistant.StructuredOutput) == 0 {
		return result, errors.New("structured output response is not a tool-calls terminal")
	}
	var pairs map[string]brokerPair
	generation, err := decodeToolGenerationWithOptions(envelope["parts"], assistant, true, pairs, map[string]bool{}, map[string]bool{}, expectation, nil)
	if err != nil {
		return result, err
	}
	if len(generation.Calls) != 0 || generation.StructuredOutputTool == nil {
		return result, errors.New("structured output response terminal projection mismatch")
	}
	// Advisory message text may accompany the single terminal capture (R34/R35:
	// live Muse turns routinely pair short text with StructuredOutput under
	// tool_choice=auto). The text stays hashed in generation evidence and
	// grants no effect authority; only the validated capture below is returned.
	normalized, err := normalizeStructuredOutputValue(assistant.StructuredOutput)
	if err != nil {
		return result, err
	}
	value := json.RawMessage(append([]byte(nil), normalized...))
	tool := *generation.StructuredOutputTool
	result.Assistant, result.StructuredOutput, result.StructuredOutputTool = assistant.Assistant, &value, &tool
	return result, nil
}

func validateSynchronousRecord(record synchronousTurnRecord, intent DispatchIntent) (SynchronousTextTurnObservation, error) {
	if err := validateSynchronousEvidenceBound(record); err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	if intent.StructuredOutput != nil {
		return SynchronousTextTurnObservation{}, errors.New("structured output requires a synchronous tool turn")
	}
	assistant, text, err := decodeTextTurn([]byte(record.Transcript), intent.Binding, intent.Text)
	if err != nil {
		return SynchronousTextTurnObservation{}, err
	}
	if record.Response != "" {
		returned, returnedText, err := decodeSynchronousResponse([]byte(record.Response), intent.Binding)
		if err != nil {
			return SynchronousTextTurnObservation{}, err
		}
		if returned != assistant || returnedText != text {
			return SynchronousTextTurnObservation{}, errors.New("recorded synchronous response and transcript differ")
		}
	}
	return SynchronousTextTurnObservation{Assistant: assistant, Text: text}, nil
}

func validateSynchronousEvidenceBound(record synchronousTurnRecord) error {
	if record.Transcript == "" {
		return errors.New("synchronous transcript evidence required")
	}
	raw, err := canonical.Bytes(record)
	if err != nil || len(raw) > maxSynchronousEvidenceBytes {
		return errors.New("combined synchronous evidence exceeds journal bound")
	}
	return nil
}
