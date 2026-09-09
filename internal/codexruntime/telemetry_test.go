package codexruntime

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/codexrpc"
)

func TestTerminalResponseTelemetryPreservesRawParamsAndUnknownFinishReason(t *testing.T) {
	raw := json.RawMessage("{ \"threadId\" : \"thread-1\", \"turn\" : {\"id\":\"turn-1\",\"status\":\"completed\",\"items\":[]} }")
	got, err := terminalResponseTelemetry(codexrpc.Message{Method: "turn/completed", Params: raw}, "thread-1", "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.RawParams != string(raw) || got.ParamsBytes != len(raw) || got.FinishReason != unknownFinishReason || got.FinishReasonField != "" || got.TurnStatus != "completed" {
		t.Fatal("terminal telemetry lost raw or explicit unknown evidence", got)
	}
}

func TestTerminalResponseTelemetryProjectsKnownTerminationFields(t *testing.T) {
	raw := json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"failed","finishReason":"max_output_tokens","incompleteDetails":{"reason":"limit"},"error":{"message":"cut"}}}`)
	got, err := terminalResponseTelemetry(codexrpc.Message{Method: "turn/completed", Params: raw}, "thread-1", "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.FinishReason != "max_output_tokens" || got.FinishReasonField != "finishReason" || string(got.IncompleteDetails) != `{"reason":"limit"}` || string(got.TurnError) != `{"message":"cut"}` {
		t.Fatal("termination fields not retained", got)
	}
}

func TestTerminalResponseTelemetryDoesNotConferOutcomeAuthority(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runtime.jsonl")
	i := invocation(t)
	settings := codexrpc.ThreadSettings{ThreadID: "thread-1", Model: i.Profile.Model, Provider: i.Profile.Provider, Effort: &i.Profile.Effort, Directory: root, Approval: "never", Sandbox: "readOnly"}
	events := []struct {
		kind    string
		payload any
	}{
		{"runtime.intent", Intent{i, root}},
		{"runtime.thread", settings},
		{"runtime.turn-intent", struct {
			InvocationID string `json:"invocation_id"`
		}{i.ID}},
		{"runtime.turn", struct {
			ID string `json:"id"`
		}{"turn-1"}},
	}
	for _, event := range events {
		if err := appendEvent(path, event.kind, event.payload); err != nil {
			t.Fatal(err)
		}
	}
	raw := json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[]}}`)
	evidence, err := terminalResponseTelemetry(codexrpc.Message{Method: "turn/completed", Params: raw}, "thread-1", "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := appendEvent(path, "runtime.terminal-response", *evidence); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.TerminalResponse == nil || s.TurnStatus != "" || s.ExecutionOutcome != "UNKNOWN" || s.NotificationStreamComplete {
		t.Fatal("diagnostic telemetry changed authoritative state", s)
	}
}

func TestTerminalResponseTelemetryRejectsProjectionTampering(t *testing.T) {
	raw := json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}`)
	got, err := terminalResponseTelemetry(codexrpc.Message{Method: "turn/completed", Params: raw}, "thread-1", "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	got.FinishReason = "length"
	if err := validateTerminalResponseTelemetry(*got, "thread-1", "turn-1"); err == nil {
		t.Fatal("accepted telemetry projection not bound to raw params")
	}
}

func TestTerminalResponseTelemetryRejectsRawParamsThatCannotFitJournalEvent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runtime.jsonl")
	i := invocation(t)
	settings := codexrpc.ThreadSettings{ThreadID: "thread-1", Model: i.Profile.Model, Provider: i.Profile.Provider, Effort: &i.Profile.Effort, Directory: root, Approval: "never", Sandbox: "readOnly"}
	for _, event := range []struct {
		kind    string
		payload any
	}{
		{"runtime.intent", Intent{i, root}},
		{"runtime.thread", settings},
		{"runtime.turn-intent", struct {
			InvocationID string `json:"invocation_id"`
		}{i.ID}},
		{"runtime.turn", struct {
			ID string `json:"id"`
		}{"turn-1"}},
	} {
		if err := appendEvent(path, event.kind, event.payload); err != nil {
			t.Fatal(err)
		}
	}
	// The provider params remain below its 1 MiB envelope bound, while exact
	// retention as an escaped journal string would exceed the event bound.
	raw := json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","diagnostic":"` + strings.Repeat(`\"`, 300000) + `"}}`)
	if len(raw) >= codexrpc.MaxMessage {
		t.Fatal("fixture exceeds provider message bound", len(raw))
	}
	if err := (&Adapter{JournalPath: path}).recordTerminalResponse(codexrpc.Message{Method: "turn/completed", Params: raw}, "thread-1", "turn-1"); err == nil {
		t.Fatal("oversized exact telemetry was silently dropped or admitted")
	}
	s, err := Inspect(path)
	if err != nil || s.TerminalResponse != nil || s.Result != nil {
		t.Fatal("oversized telemetry changed durable outcome", s, err)
	}
}
