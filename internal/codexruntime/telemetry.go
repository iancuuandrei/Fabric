package codexruntime

import (
	"encoding/json"
	"errors"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/codexrpc"
)

const unknownFinishReason = "UNKNOWN"

// TerminalResponseTelemetry is diagnostic provider evidence. RawParams retains
// the exact decoded JSON-RPC params bytes as UTF-8 text; the projected fields
// aid diagnosis but do not establish routing, completion, or result authority.
type TerminalResponseTelemetry struct {
	Method            string          `json:"method"`
	RawParams         string          `json:"raw_params"`
	ParamsBytes       int             `json:"params_bytes"`
	ThreadID          string          `json:"thread_id"`
	TurnID            string          `json:"turn_id"`
	TurnStatus        string          `json:"turn_status"`
	FinishReason      string          `json:"finish_reason"`
	FinishReasonField string          `json:"finish_reason_field,omitempty"`
	IncompleteDetails json.RawMessage `json:"incomplete_details,omitempty"`
	TurnError         json.RawMessage `json:"turn_error,omitempty"`
}

func terminalResponseTelemetry(m codexrpc.Message, threadID, turnID string) (*TerminalResponseTelemetry, error) {
	if m.Method != "turn/completed" {
		return nil, nil
	}
	raw := string(m.Params)
	if len(m.Params) == 0 || len(m.Params) > codexrpc.MaxMessage || !utf8.Valid(m.Params) {
		return nil, errors.New("invalid terminal response telemetry params")
	}
	var wire struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID                string          `json:"id"`
			Status            string          `json:"status"`
			FinishReason      json.RawMessage `json:"finishReason"`
			FinishReasonSnake json.RawMessage `json:"finish_reason"`
			StopReason        json.RawMessage `json:"stopReason"`
			StopReasonSnake   json.RawMessage `json:"stop_reason"`
			IncompleteDetails json.RawMessage `json:"incompleteDetails"`
			IncompleteSnake   json.RawMessage `json:"incomplete_details"`
			Error             json.RawMessage `json:"error"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(m.Params, &wire); err != nil {
		return nil, err
	}
	if wire.ThreadID != threadID || wire.Turn.ID != turnID {
		return nil, errors.New("terminal response telemetry identity mismatch")
	}
	evidence := &TerminalResponseTelemetry{
		Method:       m.Method,
		RawParams:    raw,
		ParamsBytes:  len(m.Params),
		ThreadID:     wire.ThreadID,
		TurnID:       wire.Turn.ID,
		TurnStatus:   wire.Turn.Status,
		FinishReason: unknownFinishReason,
		TurnError:    copyRaw(wire.Turn.Error),
	}
	if len(wire.Turn.IncompleteDetails) != 0 {
		evidence.IncompleteDetails = copyRaw(wire.Turn.IncompleteDetails)
	} else {
		evidence.IncompleteDetails = copyRaw(wire.Turn.IncompleteSnake)
	}
	for _, field := range []struct {
		name string
		raw  json.RawMessage
	}{
		{"finishReason", wire.Turn.FinishReason},
		{"finish_reason", wire.Turn.FinishReasonSnake},
		{"stopReason", wire.Turn.StopReason},
		{"stop_reason", wire.Turn.StopReasonSnake},
	} {
		if len(field.raw) == 0 || string(field.raw) == "null" {
			continue
		}
		var reason string
		if err := json.Unmarshal(field.raw, &reason); err != nil || reason == "" {
			return nil, errors.New("invalid terminal finish reason")
		}
		if evidence.FinishReason != unknownFinishReason && evidence.FinishReason != reason {
			return nil, errors.New("conflicting terminal finish reasons")
		}
		evidence.FinishReason = reason
		if evidence.FinishReasonField == "" {
			evidence.FinishReasonField = field.name
		}
	}
	return evidence, nil
}

func copyRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func validateTerminalResponseTelemetry(got TerminalResponseTelemetry, threadID, turnID string) error {
	want, err := terminalResponseTelemetry(codexrpc.Message{Method: got.Method, Params: json.RawMessage(got.RawParams)}, threadID, turnID)
	if err != nil || want == nil {
		return errors.New("invalid terminal response telemetry")
	}
	wantHash, err := canonical.Hash("runtime.terminal-response", want)
	if err != nil {
		return err
	}
	gotHash, err := canonical.Hash("runtime.terminal-response", got)
	if err != nil || gotHash != wantHash {
		return errors.New("terminal response telemetry projection mismatch")
	}
	return nil
}

func (a *Adapter) recordTerminalResponse(m codexrpc.Message, threadID, turnID string) error {
	evidence, err := terminalResponseTelemetry(m, threadID, turnID)
	if err != nil || evidence == nil {
		return err
	}
	return appendEvent(a.JournalPath, "runtime.terminal-response", *evidence)
}
