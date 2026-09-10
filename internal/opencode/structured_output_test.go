package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
)

func TestStructuredOutputExpectationBindsCanonicalSchemaAndFormat(t *testing.T) {
	expectation, err := NewStructuredOutputExpectation(json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := expectation.Validate(); err != nil {
		t.Fatal(err)
	}
	format, err := expectation.PromptFormat()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(format, &decoded); err != nil {
		t.Fatal(err)
	}
	var kind string
	var retry int
	if err := canonical.Decode(decoded["type"], &kind); err != nil || kind != "json_schema" || canonical.Decode(decoded["retryCount"], &retry) != nil || retry != 0 {
		t.Fatalf("unexpected native format: %s", format)
	}
	var schema map[string]json.RawMessage
	if err := canonical.Decode(decoded["schema"], &schema); err != nil || schema == nil {
		t.Fatal("native format did not retain schema")
	}
	changed := expectation
	changed.SchemaSHA256 = strings.Repeat("0", 64)
	if err := changed.Validate(); err == nil {
		t.Fatal("schema identity substitution was admitted")
	}
	if _, err := NewStructuredOutputExpectation(json.RawMessage(`[]`)); err == nil {
		t.Fatal("array schema was admitted")
	}
	if _, err := NewStructuredOutputExpectation(json.RawMessage(strings.Repeat("x", maxStructuredOutputSchemaBytes+1))); err == nil {
		t.Fatal("oversize schema was admitted")
	}
}

func TestDecodeStructuredOutputToolTurnBindsTerminalToolAndPreservesLegacy(t *testing.T) {
	binding, state, transcript, expectation := structuredToolTurnFixture(t)
	raw := marshalToolTranscript(t, transcript)
	observation, err := decodeToolTurnWithStructuredOutput(raw, binding, "use source_read", state, &expectation)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Text != "" || observation.StructuredOutput == nil || len(observation.Calls) != 1 || len(observation.Generations) != 2 {
		t.Fatalf("unexpected structured observation: %+v", observation)
	}
	wantValue := json.RawMessage(`{"candidate_id":"candidate-1","changes":[]}`)
	if !bytes.Equal(*observation.StructuredOutput, wantValue) || observation.Generations[1].Finish != "tool-calls" || observation.StructuredOutputTool == nil {
		t.Fatalf("structured terminal projection changed: %+v", observation)
	}
	tool := observation.StructuredOutputTool
	if tool.PartID != "part_structured" || tool.CallID != "structured-call-1" || len(tool.ArgumentsSHA256) != 64 || len(tool.ResultSHA256) != 64 {
		t.Fatalf("structured tool identity incomplete: %+v", tool)
	}
	if _, err := decodeToolTurn(raw, binding, "use source_read", state); err == nil {
		t.Fatal("structured transcript silently entered legacy text projection")
	}
}

func TestDecodeStructuredOutputRejectsAmbiguousOrNonterminalProjection(t *testing.T) {
	cases := map[string]func([]map[string]any){
		"missing assistant value": func(rows []map[string]any) {
			delete(rows[2]["info"].(map[string]any), "structured")
		},
		"mismatched input": func(rows []map[string]any) {
			parts(rows[2])[1]["state"].(map[string]any)["input"] = map[string]any{"candidate_id": "other", "changes": []any{}}
		},
		"schema wrong type": func(rows []map[string]any) {
			value := map[string]any{"candidate_id": 7, "changes": []any{}}
			rows[2]["info"].(map[string]any)["structured"] = value
			parts(rows[2])[1]["state"].(map[string]any)["input"] = value
		},
		"schema missing required": func(rows []map[string]any) {
			value := map[string]any{"candidate_id": "candidate-1"}
			rows[2]["info"].(map[string]any)["structured"] = value
			parts(rows[2])[1]["state"].(map[string]any)["input"] = value
		},
		"duplicate terminal tool": func(rows []map[string]any) {
			finalParts := parts(rows[2])
			duplicate := cloneMap(finalParts[1])
			duplicate["id"] = "part_structured_duplicate"
			rows[2]["parts"] = append(finalParts[:len(finalParts)-1], duplicate, finalParts[len(finalParts)-1])
		},
		"nonterminal structured tool": func(rows []map[string]any) {
			structured := cloneMap(parts(rows[2])[1])
			rows[1]["parts"] = append(parts(rows[1])[:len(parts(rows[1]))-1], structured, parts(rows[1])[len(parts(rows[1]))-1])
		},
		"unknown state field": func(rows []map[string]any) {
			parts(rows[2])[1]["state"].(map[string]any)["unexpected"] = true
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			binding, state, transcript, expectation := structuredToolTurnFixture(t)
			mutate(transcript)
			if _, err := decodeToolTurnWithStructuredOutput(marshalToolTranscript(t, transcript), binding, "use source_read", state, &expectation); err == nil {
				t.Fatal("ambiguous structured output transcript was admitted")
			}
		})
	}
}

func TestDecodeStructuredOutputAcceptsAdvisoryCommentaryWithSingleCapture(t *testing.T) {
	binding, state, transcript, expectation := structuredToolTurnFixture(t)
	finalParts := parts(transcript[2])
	text := mergePart(basePart("part_commentary", "msg_final", "text"), map[string]any{"text": "commentary"})
	transcript[2]["parts"] = append(finalParts[:len(finalParts)-1], text, finalParts[len(finalParts)-1])
	observation, err := decodeToolTurnWithStructuredOutput(marshalToolTranscript(t, transcript), binding, "use source_read", state, &expectation)
	if err != nil {
		t.Fatal("terminal capture with advisory commentary was rejected", err)
	}
	wantValue := json.RawMessage(`{"candidate_id":"candidate-1","changes":[]}`)
	if observation.StructuredOutput == nil || !bytes.Equal(*observation.StructuredOutput, wantValue) || observation.StructuredOutputTool == nil {
		t.Fatalf("advisory commentary changed the bound capture: %+v", observation)
	}
	if observation.Generations[1].TextSHA256 != toolTurnDigest([]byte("commentary")) {
		t.Fatal("advisory commentary was not retained as generation evidence")
	}
	if len(observation.Calls) != 1 {
		t.Fatalf("advisory commentary admitted broker work: %+v", observation)
	}
}

func TestStructuredOutputResponseReadbackMatchesTerminalTool(t *testing.T) {
	binding, _, transcript, expectation := structuredToolTurnFixture(t)
	final := transcript[len(transcript)-1]
	envelope := map[string]any{"info": final["info"], "parts": final["parts"]}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	returned, err := decodeSynchronousResponseWithStructuredOutput(raw, binding, expectation)
	if err != nil {
		t.Fatal(err)
	}
	if returned.StructuredOutput == nil || returned.StructuredOutputTool == nil || returned.Text != "" {
		t.Fatalf("unexpected structured response: %+v", returned)
	}
	changed := expectation
	changed.RetryCount = 1
	if _, err := decodeSynchronousResponseWithStructuredOutput(raw, binding, changed); err == nil {
		t.Fatal("changed structured expectation was admitted")
	}
}

func TestStructuredOutputSyncResponseAcceptsAdvisoryCommentary(t *testing.T) {
	binding, _, transcript, expectation := structuredToolTurnFixture(t)
	final := transcript[len(transcript)-1]
	finalParts := parts(final)
	text := mergePart(basePart("part_commentary", "msg_final", "text"), map[string]any{"text": "commentary"})
	final["parts"] = append(finalParts[:len(finalParts)-1], text, finalParts[len(finalParts)-1])
	envelope := map[string]any{"info": final["info"], "parts": final["parts"]}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	returned, err := decodeSynchronousResponseWithStructuredOutput(raw, binding, expectation)
	if err != nil {
		t.Fatal("synchronous terminal with advisory commentary was rejected", err)
	}
	if returned.StructuredOutput == nil || returned.StructuredOutputTool == nil {
		t.Fatalf("advisory commentary changed the bound synchronous capture: %+v", returned)
	}
}

func TestSubmitSynchronousToolTurnStructuredOutputBindsFormatResponseAndReadback(t *testing.T) {
	intent, brokerPath, broker := synchronousToolFixture(t)
	_, _, rows, expectation := structuredToolTurnFixture(t)
	// Keep the real synchronous fixture's immutable invocation, broker binding
	// and prompt while replacing only its wire projection with the structured
	// terminal result.
	intent.Dispatch.StructuredOutput = &expectation
	rows[0]["info"].(map[string]any)["id"] = intent.Dispatch.Binding.ParentID
	rows[0]["info"].(map[string]any)["sessionID"] = intent.Dispatch.Binding.SessionID
	rows[0]["info"].(map[string]any)["agent"] = intent.Dispatch.Binding.Agent
	rows[0]["info"].(map[string]any)["model"] = map[string]any{"providerID": intent.Dispatch.Binding.Provider, "modelID": intent.Dispatch.Binding.Model, "variant": intent.Dispatch.Binding.Variant}
	rows[0]["parts"].([]map[string]any)[0]["text"] = intent.Dispatch.Text
	final := rows[len(rows)-1]
	finalInfo := final["info"].(map[string]any)
	finalInfo["id"] = "msg_final"
	finalInfo["sessionID"] = intent.Dispatch.Binding.SessionID
	finalInfo["parentID"] = intent.Dispatch.Binding.ParentID
	finalInfo["providerID"] = intent.Dispatch.Binding.Provider
	finalInfo["modelID"] = intent.Dispatch.Binding.Model
	finalInfo["agent"] = intent.Dispatch.Binding.Agent
	finalInfo["variant"] = intent.Dispatch.Binding.Variant
	finalInfo["path"] = map[string]any{"cwd": intent.Dispatch.Binding.Directory, "root": intent.Dispatch.Binding.Root}
	value := map[string]any{"candidate_id": "candidate-1", "changes": []any{}}
	finalInfo["finish"] = "tool-calls"
	finalInfo["structured"] = value
	finalParts := final["parts"].([]map[string]any)
	finalParts[1] = mergePart(basePart("part_structured", "msg_final", "tool"), map[string]any{
		"callID": "structured-call-1", "tool": StructuredOutputToolName,
		"state": map[string]any{
			"status": "completed", "input": value, "output": structuredOutputResult, "title": "Structured Output",
			"metadata": map[string]any{"valid": true}, "time": map[string]any{"start": 31, "end": 32},
		},
	})
	finalParts[2]["reason"] = "tool-calls"
	path := filepath.Join(t.TempDir(), "sync-tool-structured.jsonl")
	var mu sync.Mutex
	var transcript, response []byte
	var posts, gets int
	var err error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if _, ok := body["format"]; !ok {
				t.Error("structured synchronous body omitted native format")
			}
			receipt, err := broker.Call(r.Context(), "broker-list-1", "source_list", json.RawMessage(`{"after":"","limit":1}`))
			if err != nil {
				t.Error(err)
				http.Error(w, "broker call failed", http.StatusInternalServerError)
				return
			}
			_, legacyTranscript := synchronousToolWire(t, intent.Dispatch, receipt)
			var legacyRows []map[string]any
			if err := json.Unmarshal([]byte(legacyTranscript), &legacyRows); err != nil {
				t.Error(err)
				return
			}
			legacyRows[0]["info"].(map[string]any)["format"], err = expectation.PromptFormat()
			if err != nil {
				t.Error(err)
				return
			}
			legacyFinal := legacyRows[len(legacyRows)-1]
			legacyInfo := legacyFinal["info"].(map[string]any)
			legacyInfo["finish"] = "tool-calls"
			legacyInfo["structured"] = value
			legacyParts := legacyFinal["parts"].([]any)
			legacyParts[1] = map[string]any{
				"id": "part_structured", "messageID": "msg_final", "sessionID": "ses_fixture", "type": "tool",
				"callID": "structured-call-1", "tool": StructuredOutputToolName,
				"state": map[string]any{"status": "completed", "input": value, "output": structuredOutputResult, "title": "Structured Output", "metadata": map[string]any{"valid": true}, "time": map[string]any{"start": 22, "end": 29}, "attachments": []any{}},
			}
			legacyParts[2].(map[string]any)["reason"] = "tool-calls"
			transcript, _ = json.Marshal(legacyRows)
			response, _ = json.Marshal(map[string]any{"info": legacyFinal["info"], "parts": legacyFinal["parts"]})
			mu.Lock()
			posts++
			mu.Unlock()
			writeSynchronousJSON(w, string(response))
		case http.MethodGet:
			mu.Lock()
			gets++
			body := string(transcript)
			mu.Unlock()
			writeSynchronousJSON(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	observation, err := client.SubmitSynchronousToolTurn(ctx, path, brokerPath, intent)
	if err != nil {
		t.Fatal(err)
	}
	if observation.StructuredOutput == nil || observation.StructuredOutputTool == nil || posts != 1 || gets != 1 || len(observation.Calls) != 1 {
		t.Fatalf("structured synchronous turn was not fully bound: %+v posts=%d gets=%d", observation, posts, gets)
	}
}

func structuredToolTurnFixture(t *testing.T) (Binding, contextbroker.State, []map[string]any, StructuredOutputExpectation) {
	t.Helper()
	binding, state, transcript := toolTurnFixture(t, 1)
	expectation, err := NewStructuredOutputExpectation(json.RawMessage(`{"type":"object","properties":{"candidate_id":{"type":"string"},"changes":{"type":"array"}},"required":["candidate_id","changes"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	format, err := expectation.PromptFormat()
	if err != nil {
		t.Fatal(err)
	}
	transcript[0]["info"].(map[string]any)["format"] = format
	value := map[string]any{"candidate_id": "candidate-1", "changes": []any{}}
	transcript[2]["info"].(map[string]any)["finish"] = "tool-calls"
	transcript[2]["info"].(map[string]any)["structured"] = value
	finalParts := parts(transcript[2])
	structured := mergePart(basePart("part_structured", "msg_final", "tool"), map[string]any{
		"callID": "structured-call-1", "tool": StructuredOutputToolName,
		"state": map[string]any{
			"status": "completed", "input": value, "output": structuredOutputResult, "title": "Structured Output",
			"metadata": map[string]any{"valid": true}, "time": map[string]any{"start": 31, "end": 32},
		},
	})
	transcript[2]["parts"] = []map[string]any{
		finalParts[0], structured,
		mergePart(cloneMap(finalParts[len(finalParts)-1]), map[string]any{"reason": "tool-calls"}),
	}
	return binding, state, transcript, expectation
}
