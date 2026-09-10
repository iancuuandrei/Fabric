package opencode

import (
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
)

// failedPart rewrites fixture call index into the exact M2ap error envelope:
// status/input/error/time only, no output, title, metadata or attachments.
func failedPart(t *testing.T, transcript []map[string]any, index int) map[string]any {
	t.Helper()
	state := toolState(transcript, index)
	for _, key := range []string{"output", "title", "metadata", "attachments"} {
		delete(state, key)
	}
	state["status"] = "error"
	state["error"] = "MCP error -32800: request cancelled or timed out"
	state["time"] = map[string]any{"start": 11 + index, "end": 12 + index}
	return toolPart(transcript, index)
}

func failBrokerResponse(t *testing.T, state *contextbroker.State, index int) {
	t.Helper()
	content, err := canonical.Bytes(map[string]any{"code": "context_request_failed", "message": "check arguments"})
	if err != nil {
		t.Fatal(err)
	}
	old := len(state.Responses[index].Content)
	state.Responses[index].Success = false
	state.Responses[index].Content = content
	state.ResponseBytes += len(content) - old
}

func TestDecodeToolTurnAdmitsReceiptedFailureAlongsideSuccess(t *testing.T) {
	binding, state, transcript := toolTurnFixture(t, 2)
	failedPart(t, transcript, 0)
	failBrokerResponse(t, &state, 0)
	observation, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state)
	if err != nil {
		t.Fatal("receipted failure with exact broker receipt rejected", err)
	}
	if len(observation.Calls) != 2 {
		t.Fatal("expected two observed calls", len(observation.Calls))
	}
	failed := observation.Calls[0]
	if failed.Tool != "engorch_source_read" || failed.ContentSHA256 != "" || failed.RequestID != state.Requests[0].RequestID || failed.BrokerCallID != state.Requests[0].CallID {
		t.Fatal("failure observation lost exact broker binding", failed)
	}
	if failed.ArgumentsSHA256 == "" {
		t.Fatal("failure observation lost arguments digest")
	}
	ok := observation.Calls[1]
	if ok.ContentSHA256 == "" || ok.RequestID != state.Requests[1].RequestID {
		t.Fatal("success observation disturbed by sibling failure", ok)
	}
	if len(observation.TranscriptSHA256) != 64 || len(observation.BrokerBindingID) != 64 {
		t.Fatal("observation digests missing")
	}
}

func TestDecodeToolTurnRejectsFailureShapeViolations(t *testing.T) {
	tests := map[string]func([]map[string]any, *contextbroker.State){
		"output present": func(rows []map[string]any, _ *contextbroker.State) {
			toolState(rows, 0)["output"] = `{"unexpected":true}`
		},
		"title present": func(rows []map[string]any, _ *contextbroker.State) {
			toolState(rows, 0)["title"] = "Failed"
		},
		"metadata present": func(rows []map[string]any, _ *contextbroker.State) {
			toolState(rows, 0)["metadata"] = map[string]any{"truncated": false}
		},
		"attachments present": func(rows []map[string]any, _ *contextbroker.State) {
			toolState(rows, 0)["attachments"] = []any{}
		},
		"non-error status": func(rows []map[string]any, _ *contextbroker.State) {
			toolState(rows, 0)["status"] = "failed"
		},
		"missing error": func(rows []map[string]any, _ *contextbroker.State) {
			delete(toolState(rows, 0), "error")
		},
		"empty error": func(rows []map[string]any, _ *contextbroker.State) {
			toolState(rows, 0)["error"] = ""
		},
		"oversize error": func(rows []map[string]any, _ *contextbroker.State) {
			toolState(rows, 0)["error"] = strings.Repeat("e", 4097)
		},
		"missing input": func(rows []map[string]any, _ *contextbroker.State) {
			delete(toolState(rows, 0), "input")
		},
		"malformed input": func(rows []map[string]any, _ *contextbroker.State) {
			toolState(rows, 0)["input"] = "not-an-object"
		},
		"time out of bounds": func(rows []map[string]any, _ *contextbroker.State) {
			toolState(rows, 0)["time"] = map[string]any{"start": 11, "end": 99}
		},
		"unreceipted failure": func(rows []map[string]any, _ *contextbroker.State) {
			// Error part with no matching failed broker pair: every broker
			// response stays successful.
		},
		"agent tool failure": func(rows []map[string]any, _ *contextbroker.State) {
			toolPart(rows, 0)["tool"] = "engorch_spawn_agent"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			binding, state, transcript := toolTurnFixture(t, 2)
			failedPart(t, transcript, 0)
			if name != "unreceipted failure" {
				failBrokerResponse(t, &state, 0)
			}
			mutate(transcript, &state)
			if _, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state); err == nil {
				t.Fatal("invalid failure shape admitted")
			}
		})
	}
}

func TestDecodeToolTurnRejectsAmbiguousFailedReceipt(t *testing.T) {
	binding, state, transcript := toolTurnFixture(t, 2)
	failedPart(t, transcript, 0)
	failBrokerResponse(t, &state, 0)
	// A second identical failed pair makes the error part unbindable.
	duplicateRequest := state.Requests[0]
	duplicateRequest.CallID = "broker-call-1-duplicate"
	duplicateRequest.RequestID = ""
	requestID, err := duplicateRequest.ID()
	if err != nil {
		t.Fatal(err)
	}
	duplicateRequest.RequestID = requestID
	duplicateResponse := state.Responses[0]
	duplicateResponse.RequestID = requestID
	duplicateResponse.CallID = duplicateRequest.CallID
	state.Requests = append(state.Requests, duplicateRequest)
	state.Responses = append(state.Responses, duplicateResponse)
	state.Calls++
	state.ResponseBytes += len(duplicateResponse.Content)
	if _, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state); err == nil {
		t.Fatal("ambiguous failed receipt admitted")
	}
}

func TestDecodeToolTurnRejectsSuccessBoundToFailedReceipt(t *testing.T) {
	binding, state, transcript := toolTurnFixture(t, 1)
	failBrokerResponse(t, &state, 0)
	if _, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state); err == nil {
		t.Fatal("completed part bound to failed receipt admitted")
	}
}
