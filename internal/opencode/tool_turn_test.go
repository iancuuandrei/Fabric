package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/repository"
)

func TestDecodeToolTurnBindsEveryReceiptAndCountsEachGenerationOnce(t *testing.T) {
	binding, state, transcript := toolTurnFixture(t, 1)
	observation, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Text != "complete" || observation.Final.ID != "msg_final" || len(observation.Generations) != 2 || len(observation.Calls) != 1 {
		t.Fatal("unexpected tool turn projection", observation)
	}
	call := observation.Calls[0]
	if call.ProviderCallID != "provider-call-1" || call.BrokerCallID != state.Requests[0].CallID || call.RequestID != state.Requests[0].RequestID || call.Tool != "engorch_source_read" {
		t.Fatal("tool identities were not kept distinct and exact", call)
	}
	if observation.Tokens != (ToolTurnTokens{Input: 27, Output: 7, Reasoning: 3, CacheRead: 5, CacheWrite: 7}) {
		t.Fatal("assistant tokens were not aggregated exactly once", observation.Tokens)
	}
	if len(observation.TranscriptSHA256) != 64 || len(observation.BrokerBindingID) != 64 || len(observation.BrokerStateID) != 64 {
		t.Fatal("observation digests missing", observation)
	}
}

func TestDecodeToolTurnAllowsModelToFinishWithoutCallingAvailableTools(t *testing.T) {
	binding, state, transcript := toolTurnFixture(t, 0)
	observation, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state)
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Generations) != 1 || len(observation.Calls) != 0 || observation.Text != "complete" || observation.Tokens.Input != 18 {
		t.Fatal("valid no-call tool-capable turn rejected", observation)
	}
}

func TestDecodeToolTurnSortsMessagesByCreatedThenID(t *testing.T) {
	binding, state, transcript := toolTurnFixture(t, 1)
	transcript[0], transcript[2] = transcript[2], transcript[0]
	observation, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state)
	if err != nil || observation.Generations[0].Assistant.ID != "msg_tools" || observation.Generations[1].Assistant.ID != "msg_final" {
		t.Fatal("chronological decoding failed", observation, err)
	}
}

func TestDecodeToolTurnMatchesRepeatedCallsByReceiptIdentity(t *testing.T) {
	binding, state, transcript := toolTurnFixture(t, 2)
	observation, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state)
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Calls) != 2 || observation.Calls[0].RequestID == observation.Calls[1].RequestID || observation.Calls[0].BrokerCallID == observation.Calls[1].BrokerCallID {
		t.Fatal("repeated calls were not distinguished by broker receipt", observation.Calls)
	}
}

func TestDecodeToolTurnRejectsTranscriptAndReceiptSubstitutions(t *testing.T) {
	tests := map[string]func([]map[string]any, *contextbroker.State){
		"truncated": func(rows []map[string]any, _ *contextbroker.State) {
			toolState(rows, 0)["metadata"] = map[string]any{"truncated": true}
		},
		"extra metadata": func(rows []map[string]any, _ *contextbroker.State) {
			toolState(rows, 0)["metadata"] = map[string]any{"truncated": false, "unbound": true}
		},
		"input substitution": func(rows []map[string]any, _ *contextbroker.State) {
			toolState(rows, 0)["input"] = map[string]any{"limit": 1, "offset": 1, "path": "source.txt"}
		},
		"tool substitution": func(rows []map[string]any, _ *contextbroker.State) {
			toolPart(rows, 0)["tool"] = "engorch_source_list"
		},
		"provider call duplicate": func(rows []map[string]any, _ *contextbroker.State) {
			part := cloneMap(toolPart(rows, 0))
			part["id"] = "part_tool_duplicate"
			messageParts := parts(rows[1])
			rows[1]["parts"] = append(messageParts[:len(messageParts)-1], part, messageParts[len(messageParts)-1])
		},
		"receipt substitution": func(rows []map[string]any, state *contextbroker.State) {
			foreign := state.Responses[0]
			foreign.CallID = "foreign-broker-call"
			wire, _ := canonical.Bytes(foreign)
			toolState(rows, 0)["output"] = string(wire)
		},
		"broker response failure": func(_ []map[string]any, state *contextbroker.State) {
			state.Responses[0].Success = false
		},
		"broker response accounting": func(_ []map[string]any, state *contextbroker.State) {
			state.ResponseBytes++
		},
		"pending broker": func(_ []map[string]any, state *contextbroker.State) {
			copy := state.Requests[0]
			state.Pending = &copy
		},
		"final tool": func(rows []map[string]any, _ *contextbroker.State) {
			part := cloneMap(toolPart(rows, 0))
			part["id"] = "part_final_tool"
			part["messageID"] = "msg_final"
			finalParts := parts(rows[2])
			rows[2]["parts"] = append(finalParts[:len(finalParts)-1], part, finalParts[len(finalParts)-1])
		},
		"extra user": func(rows []map[string]any, _ *contextbroker.State) {
			rows[2]["info"].(map[string]any)["role"] = "user"
		},
		"unsupported part": func(rows []map[string]any, _ *contextbroker.State) {
			finalParts := parts(rows[2])
			part := basePart("part_retry", "msg_final", "retry")
			rows[2]["parts"] = append(finalParts[:len(finalParts)-1], part, finalParts[len(finalParts)-1])
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			binding, state, transcript := toolTurnFixture(t, 1)
			mutate(transcript, &state)
			if _, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state); err == nil {
				t.Fatal("substituted tool turn admitted")
			}
		})
	}
}

func TestDecodeToolTurnRejectsStepTokenMismatchAndAggregateOverflow(t *testing.T) {
	t.Run("step mismatch", func(t *testing.T) {
		binding, state, transcript := toolTurnFixture(t, 1)
		stepFinish(transcript[1])["tokens"] = tokenMap(9, 5, 1, 2, 3)
		if _, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state); err == nil {
			t.Fatal("step token mismatch admitted")
		}
	})
	t.Run("aggregate overflow", func(t *testing.T) {
		binding, state, transcript := toolTurnFixture(t, 1)
		setMessageTokens(transcript[1], toolTurnMaximumExactInteger, 0, 1, 2, 3)
		setMessageTokens(transcript[2], 1, 3, 2, 3, 4)
		if _, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state); err == nil || !strings.Contains(err.Error(), "overflow") {
			t.Fatal("token aggregate overflow admitted", err)
		}
	})
}

func TestReadToolTurnUsesBoundedSessionTranscriptEndpoint(t *testing.T) {
	binding, state, transcript := toolTurnFixture(t, 1)
	raw := marshalToolTranscript(t, transcript)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/session/"+binding.SessionID+"/message" || request.Method != http.MethodGet {
			http.NotFound(w, request)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	client, err := NewClient(strings.Replace(server.URL, "localhost", "127.0.0.1", 1), "user", "password")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	observation, err := client.ReadToolTurn(context.Background(), binding, "use source_read", state)
	if err != nil || observation.Text != "complete" {
		t.Fatal(observation, err)
	}
}

func toolTurnFixture(t *testing.T, calls int) (Binding, contextbroker.State, []map[string]any) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "source")
	source := repository.Identity{Version: 1, Name: "fixture", Root: root, CommonDir: root, ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	brokerBinding, err := contextbroker.NewBinding(strings.Repeat("c", 64), source, nil, contextbroker.Limits{MaxCalls: 8, MaxRequestBytes: 4096, MaxResponseBytes: 4096, MaxTotalResponseBytes: 32768})
	if err != nil {
		t.Fatal(err)
	}
	bindingID, _ := brokerBinding.ID()
	state := contextbroker.State{Binding: &brokerBinding, Requests: []contextbroker.Request{}, Responses: []contextbroker.Response{}, Calls: calls}
	toolParts := make([]map[string]any, 0, calls)
	for index := 0; index < calls; index++ {
		arguments := json.RawMessage(`{"limit":1,"offset":0,"path":"source.txt"}`)
		request := contextbroker.Request{Version: 1, BindingID: bindingID, InvocationID: brokerBinding.InvocationID, CallID: "broker-call-" + string(rune('1'+index)), Tool: "source_read", Arguments: arguments}
		request.RequestID, err = request.ID()
		if err != nil {
			t.Fatal(err)
		}
		content := json.RawMessage(`{"content":"fixture"}`)
		response := contextbroker.Response{Version: 1, BindingID: bindingID, InvocationID: brokerBinding.InvocationID, RequestID: request.RequestID, CallID: request.CallID, Success: true, Content: content}
		state.Requests = append(state.Requests, request)
		state.Responses = append(state.Responses, response)
		state.ResponseBytes += len(content)
		receipt, _ := canonical.Bytes(response)
		part := basePart("part_tool_"+string(rune('1'+index)), "msg_tools", "tool")
		part["callID"] = "provider-call-" + string(rune('1'+index))
		part["tool"] = "engorch_source_read"
		part["state"] = map[string]any{
			"status": "completed", "input": map[string]any{"path": "source.txt", "offset": 0, "limit": 1},
			"output": string(receipt), "title": "", "metadata": map[string]any{"truncated": false},
			"time": map[string]any{"start": 11 + index, "end": 12 + index},
		}
		toolParts = append(toolParts, part)
	}
	binding := Binding{SessionID: "ses_tool", ParentID: "msg_user", Provider: "fixture", Model: "model", Agent: "build", Directory: root, Root: "/"}
	user := map[string]any{
		"info":  map[string]any{"id": binding.ParentID, "sessionID": binding.SessionID, "role": "user", "agent": binding.Agent, "model": map[string]any{"providerID": binding.Provider, "modelID": binding.Model}, "time": map[string]any{"created": 1}},
		"parts": []map[string]any{mergePart(basePart("part_user", binding.ParentID, "text"), map[string]any{"text": "use source_read"})},
	}
	intermediateParts := []map[string]any{basePart("part_step_start_1", "msg_tools", "step-start")}
	intermediateParts = append(intermediateParts, toolParts...)
	intermediateParts = append(intermediateParts, mergePart(basePart("part_step_finish_1", "msg_tools", "step-finish"), map[string]any{"reason": "tool-calls", "cost": 0, "tokens": tokenMap(9, 4, 1, 2, 3)}))
	intermediate := assistantMessage(binding, "msg_tools", 10, 20, "tool-calls", tokenMap(9, 4, 1, 2, 3), intermediateParts)
	finalParts := []map[string]any{
		basePart("part_step_start_2", "msg_final", "step-start"),
		mergePart(basePart("part_text", "msg_final", "text"), map[string]any{"text": "complete", "time": map[string]any{"start": 31, "end": 32}}),
		mergePart(basePart("part_step_finish_2", "msg_final", "step-finish"), map[string]any{"reason": "stop", "cost": 0, "tokens": tokenMap(18, 3, 2, 3, 4)}),
	}
	final := assistantMessage(binding, "msg_final", 30, 40, "stop", tokenMap(18, 3, 2, 3, 4), finalParts)
	if calls == 0 {
		return binding, state, []map[string]any{user, final}
	}
	return binding, state, []map[string]any{user, intermediate, final}
}

func assistantMessage(binding Binding, id string, created, completed int64, finish string, tokens map[string]any, messageParts []map[string]any) map[string]any {
	return map[string]any{
		"info": map[string]any{
			"id": id, "sessionID": binding.SessionID, "parentID": binding.ParentID, "role": "assistant",
			"providerID": binding.Provider, "modelID": binding.Model, "agent": binding.Agent,
			"path": map[string]any{"cwd": binding.Directory, "root": binding.Root}, "time": map[string]any{"created": created, "completed": completed},
			"finish": finish, "tokens": tokens, "cost": 0,
		},
		"parts": messageParts,
	}
}

func tokenMap(input, output, reasoning, read, write int64) map[string]any {
	return map[string]any{"total": input + output, "input": input, "output": output, "reasoning": reasoning, "cache": map[string]any{"read": read, "write": write}}
}

func setMessageTokens(message map[string]any, input, output, reasoning, read, write int64) {
	tokens := tokenMap(input, output, reasoning, read, write)
	message["info"].(map[string]any)["tokens"] = tokens
	stepFinish(message)["tokens"] = tokens
}

func basePart(id, messageID, kind string) map[string]any {
	return map[string]any{"id": id, "sessionID": "ses_tool", "messageID": messageID, "type": kind}
}

func mergePart(part map[string]any, fields map[string]any) map[string]any {
	for key, value := range fields {
		part[key] = value
	}
	return part
}

func parts(message map[string]any) []map[string]any { return message["parts"].([]map[string]any) }

func toolPart(rows []map[string]any, index int) map[string]any { return parts(rows[1])[1+index] }

func toolState(rows []map[string]any, index int) map[string]any {
	return toolPart(rows, index)["state"].(map[string]any)
}

func stepFinish(message map[string]any) map[string]any {
	messageParts := parts(message)
	return messageParts[len(messageParts)-1]
}

func cloneMap(source map[string]any) map[string]any {
	copy := make(map[string]any, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}

func marshalToolTranscript(t *testing.T, transcript []map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(transcript)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
