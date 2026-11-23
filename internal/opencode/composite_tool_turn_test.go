package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/toolbridge"
	"github.com/iancuuandrei/fabric/internal/toolreceipts"
)

type compositeTurnFixture struct {
	binding      Binding
	receipt      toolreceipts.Binding
	receiptPath  string
	projection   toolbridge.Projection
	state        toolreceipts.State
	transcript   []map[string]any
	observations []toolreceipts.Observation
}

func TestDecodeCompositeToolTurnBindsDistinctProviderMCPAndOwnerEvidence(t *testing.T) {
	fixture := newCompositeTurnFixture(t, false, true)
	verified := map[toolreceipts.Owner]int{}
	observation, err := DecodeCompositeToolTurn(marshalToolTranscript(t, fixture.transcript), fixture.binding, "use both tools", fixture.receiptPath, fixture.receipt, func(owner toolreceipts.Owner, call toolbridge.Call, result toolbridge.Result) error {
		verified[owner]++
		if call.Tool == "" || len(call.RequestID) == 0 || len(result.JSON) == 0 {
			return errors.New("backend input missing")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Text != "complete" || len(observation.Generations) != 2 || len(observation.Calls) != 2 || observation.Tokens != (ToolTurnTokens{Input: 27, Output: 7, Reasoning: 3, CacheRead: 5, CacheWrite: 7}) || verified[toolreceipts.OwnerContext] != 1 || verified[toolreceipts.OwnerAgent] != 1 {
		t.Fatal("composite observation", observation, verified)
	}
	for index, call := range observation.Calls {
		if call.ProviderCallID == call.RequestID || call.ProviderCallID != "provider-composite-"+string(rune('1'+index)) || call.RequestID != fixture.state.Calls[index].Intent.RequestID || call.RequestKey != fixture.state.Calls[index].Intent.RequestKey {
			t.Fatal("provider and MCP identities conflated", call)
		}
		input, err := canonical.Normalize(fixture.observations[index].Call.Arguments)
		if err != nil {
			t.Fatal(err)
		}
		mcpDigest, err := toolreceipts.ArgumentsSHA256(input)
		if err != nil || call.ArgumentsSHA256 != mcpDigest || call.ProviderArgumentsSHA256 != toolTurnDigest(input) || call.ProviderArgumentsSHA256 == call.ArgumentsSHA256 {
			t.Fatal("provider and MCP argument digests conflated", call, err)
		}
	}
	if len(observation.TranscriptSHA256) != 64 || observation.ReceiptBindingID != fixture.receipt.BindingID || len(observation.ReceiptJournalHead) != 64 || len(observation.ReceiptStateSHA256) != 64 {
		t.Fatal("composite identities missing", observation)
	}
}

func TestDecodeCompositeToolTurnAllowsNoCallsWithExactBinding(t *testing.T) {
	fixture := newCompositeTurnFixture(t, false, false)
	observation, err := DecodeCompositeToolTurn(marshalToolTranscript(t, fixture.transcript), fixture.binding, "use both tools", fixture.receiptPath, fixture.receipt, func(toolreceipts.Owner, toolbridge.Call, toolbridge.Result) error {
		t.Fatal("backend verifier called without a transcript call")
		return nil
	})
	if err != nil || len(observation.Calls) != 0 || observation.Text != "complete" {
		t.Fatal(observation, err)
	}
	changed := fixture.receipt
	changed.CatalogSHA256 = strings.Repeat("9", 64)
	if _, err := DecodeCompositeToolTurn(marshalToolTranscript(t, fixture.transcript), fixture.binding, "use both tools", fixture.receiptPath, changed, func(toolreceipts.Owner, toolbridge.Call, toolbridge.Result) error { return nil }); err == nil {
		t.Fatal("no-call transcript admitted changed binding")
	}
}

func TestDecodeCompositeToolTurnRejectsReceiptAndTranscriptSubstitutions(t *testing.T) {
	tests := map[string]func(*compositeTurnFixture){
		"input": func(f *compositeTurnFixture) {
			compositeToolState(f.transcript, 0)["input"] = map[string]any{"changed": true}
		},
		"output": func(f *compositeTurnFixture) {
			compositeToolState(f.transcript, 0)["output"] = `{"changed":true}`
		},
		"tool": func(f *compositeTurnFixture) {
			compositeToolPart(f.transcript, 0)["tool"] = "engorch_send_message"
		},
		"missing part": func(f *compositeTurnFixture) {
			parts := parts(f.transcript[1])
			f.transcript[1]["parts"] = append(parts[:1], parts[2:]...)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newCompositeTurnFixture(t, false, true)
			mutate(&fixture)
			if _, err := DecodeCompositeToolTurn(marshalToolTranscript(t, fixture.transcript), fixture.binding, "use both tools", fixture.receiptPath, fixture.receipt, func(toolreceipts.Owner, toolbridge.Call, toolbridge.Result) error { return nil }); err == nil {
				t.Fatal("composite substitution admitted")
			}
		})
	}
	fixture := newCompositeTurnFixture(t, false, true)
	if _, err := DecodeCompositeToolTurn(marshalToolTranscript(t, fixture.transcript), fixture.binding, "use both tools", fixture.receiptPath, fixture.receipt, func(toolreceipts.Owner, toolbridge.Call, toolbridge.Result) error {
		return errors.New("backend source differs")
	}); err == nil {
		t.Fatal("backend verification failure hidden")
	}
}

func TestDecodeCompositeToolTurnRejectsAmbiguousIdenticalReceipts(t *testing.T) {
	fixture := newCompositeTurnFixture(t, true, true)
	// Add a second call with a distinct MCP request ID but the same tool,
	// arguments and result. No provider field identifies which receipt it used.
	projection := fixture.projection
	args := fixture.observations[0].Call.Arguments
	result, err := projection.Call(context.Background(), toolbridge.Call{RequestID: json.RawMessage(`102`), Tool: "source_read", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	fixture.state, err = toolreceipts.Inspect(fixture.receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.observations = append(fixture.observations, toolreceipts.Observation{Call: toolbridge.Call{RequestID: json.RawMessage(`102`), Tool: "source_read", Arguments: args}, Result: result})
	fixture.transcript = compositeTranscript(fixture.binding, fixture.observations)
	if _, err := DecodeCompositeToolTurn(marshalToolTranscript(t, fixture.transcript), fixture.binding, "use both tools", fixture.receiptPath, fixture.receipt, func(toolreceipts.Owner, toolbridge.Call, toolbridge.Result) error { return nil }); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatal("ambiguous receipt matching admitted", err)
	}
}

func newCompositeTurnFixture(t *testing.T, constantResult, withCalls bool) compositeTurnFixture {
	t.Helper()
	contextProjection := compositeFixtureProjection(t, "source_read", "context", constantResult)
	agentProjection := compositeFixtureProjection(t, "send_message", "agent", constantResult)
	owners := []toolreceipts.OwnedProjection{{Owner: toolreceipts.OwnerContext, Projection: contextProjection}, {Owner: toolreceipts.OwnerAgent, Projection: agentProjection}}
	catalogID, err := toolreceipts.CatalogSHA256(owners)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(t.TempDir(), "receipts")
	recorder, err := toolreceipts.Open(toolreceipts.Config{
		Path: receiptPath, InvocationID: strings.Repeat("c", 64), CallerBindingSHA256: strings.Repeat("d", 64), CatalogSHA256: catalogID, Owners: owners,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{SessionID: "ses_tool", ParentID: "msg_user", Provider: "fixture", Model: "model", Agent: "build", Directory: t.TempDir(), Root: "/"}
	fixture := compositeTurnFixture{binding: binding, receipt: recorder.Binding(), receiptPath: receiptPath, projection: recorder.Projection()}
	if withCalls {
		for index, item := range []struct {
			tool string
			args json.RawMessage
		}{
			{"source_read", json.RawMessage(`{"limit":1,"offset":0,"path":"source.txt"}`)},
			{"send_message", json.RawMessage(`{"agent_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","body":"done","nonce":"one"}`)},
		} {
			call := toolbridge.Call{RequestID: json.RawMessage(string(rune('1'+index)) + "01"), Tool: item.tool, Arguments: item.args}
			result, callErr := recorder.Projection().Call(context.Background(), call)
			if callErr != nil {
				t.Fatal(callErr)
			}
			fixture.observations = append(fixture.observations, toolreceipts.Observation{Call: call, Result: result})
		}
	}
	fixture.state, err = toolreceipts.Inspect(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.transcript = compositeTranscript(binding, fixture.observations)
	return fixture
}

func compositeFixtureProjection(t *testing.T, name, kind string, constant bool) toolbridge.Projection {
	t.Helper()
	schema := json.RawMessage(`{"additionalProperties":true,"type":"object"}`)
	return toolbridge.Projection{
		Catalog: func() ([]toolbridge.ToolDefinition, error) {
			return []toolbridge.ToolDefinition{{Name: name, InputSchema: schema}}, nil
		},
		Call: func(_ context.Context, call toolbridge.Call) (toolbridge.Result, error) {
			value := map[string]any{"kind": kind}
			if !constant {
				value["request_id"] = json.RawMessage(call.RequestID)
			}
			raw, err := canonical.Bytes(value)
			return toolbridge.Result{JSON: raw}, err
		},
	}
}

func compositeTranscript(binding Binding, observations []toolreceipts.Observation) []map[string]any {
	user := map[string]any{
		"info":  map[string]any{"id": binding.ParentID, "sessionID": binding.SessionID, "role": "user", "agent": binding.Agent, "model": map[string]any{"providerID": binding.Provider, "modelID": binding.Model}, "time": map[string]any{"created": 1}},
		"parts": []map[string]any{mergePart(basePart("part_user", binding.ParentID, "text"), map[string]any{"text": "use both tools"})},
	}
	if len(observations) == 0 {
		return []map[string]any{user, compositeFinal(binding)}
	}
	intermediateParts := []map[string]any{basePart("part_composite_start", "msg_composite_tools", "step-start")}
	for index, observation := range observations {
		part := basePart("part_composite_"+string(rune('1'+index)), "msg_composite_tools", "tool")
		part["callID"] = "provider-composite-" + string(rune('1'+index))
		part["tool"] = "engorch_" + observation.Call.Tool
		var input any
		_ = json.Unmarshal(observation.Call.Arguments, &input)
		part["state"] = map[string]any{
			"status": "completed", "input": input, "output": string(observation.Result.JSON), "title": "", "metadata": map[string]any{"truncated": false},
			"time": map[string]any{"start": 11 + index, "end": 12 + index},
		}
		intermediateParts = append(intermediateParts, part)
	}
	intermediateParts = append(intermediateParts, mergePart(basePart("part_composite_finish", "msg_composite_tools", "step-finish"), map[string]any{"reason": "tool-calls", "cost": 0, "tokens": tokenMap(9, 4, 1, 2, 3)}))
	intermediate := assistantMessage(binding, "msg_composite_tools", 10, 20, "tool-calls", tokenMap(9, 4, 1, 2, 3), intermediateParts)
	return []map[string]any{user, intermediate, compositeFinal(binding)}
}

func compositeFinal(binding Binding) map[string]any {
	return assistantMessage(binding, "msg_composite_final", 30, 40, "stop", tokenMap(18, 3, 2, 3, 4), []map[string]any{
		basePart("part_composite_final_start", "msg_composite_final", "step-start"),
		mergePart(basePart("part_composite_text", "msg_composite_final", "text"), map[string]any{"text": "complete", "time": map[string]any{"start": 31, "end": 32}}),
		mergePart(basePart("part_composite_final_finish", "msg_composite_final", "step-finish"), map[string]any{"reason": "stop", "cost": 0, "tokens": tokenMap(18, 3, 2, 3, 4)}),
	})
}

func compositeToolPart(rows []map[string]any, index int) map[string]any {
	return parts(rows[1])[1+index]
}
func compositeToolState(rows []map[string]any, index int) map[string]any {
	return compositeToolPart(rows, index)["state"].(map[string]any)
}

func compositeReasoningCarrier(id, messageID, item, cipher string, start, end int64) map[string]any {
	return reasoningFixturePart(id, messageID, "carrier text", start, end, map[string]any{
		"openai": map[string]any{"itemId": item, "reasoningEncryptedContent": cipher},
	})
}

func compositeReasoningFragment(id, messageID, item string, start, end int64) map[string]any {
	return reasoningFixturePart(id, messageID, "summary text", start, end, map[string]any{
		"openai": map[string]any{"itemId": item},
	})
}

func spliceCompositeFinal(fixture *compositeTurnFixture, middle []map[string]any) {
	finalParts := parts(fixture.transcript[2])
	fixture.transcript[2]["parts"] = append(finalParts[:1], append(middle, finalParts[1:]...)...)
}

func decodeCompositeFixture(t *testing.T, fixture *compositeTurnFixture) (CompositeToolTurnObservation, error) {
	t.Helper()
	return DecodeCompositeToolTurn(marshalToolTranscript(t, fixture.transcript), fixture.binding, "use both tools", fixture.receiptPath, fixture.receipt, func(toolreceipts.Owner, toolbridge.Call, toolbridge.Result) error {
		return nil
	})
}

func TestDecodeCompositeToolTurnPairsMultiSummaryWithSameGenerationCarrier(t *testing.T) {
	fixture := newCompositeTurnFixture(t, false, true)
	item, cipher := "rs_synthetic_summary_item", "synthetic-encrypted-carrier"
	spliceCompositeFinal(&fixture, []map[string]any{
		reasoningFixturePart("part_composite_summary_1", "msg_composite_final", "first summary ", 31, 32, map[string]any{"openai": map[string]any{"itemId": item}}),
		reasoningFixturePart("part_composite_summary_2", "msg_composite_final", "second summary ", 32, 33, map[string]any{"openai": map[string]any{"itemId": item}}),
		compositeReasoningCarrier("part_composite_carrier", "msg_composite_final", item, cipher, 33, 34),
	})
	observation, err := decodeCompositeFixture(t, &fixture)
	if err != nil {
		t.Fatal(err)
	}
	got := observation.Generations[1].Reasoning
	if len(got) != 1 || got[0].ProviderItemID != ProviderItemID(item) || got[0].EncryptedContent != cipher || got[0].PartID != "part_composite_carrier" {
		t.Fatal("composite multi-summary was not paired with its same-generation carrier", got)
	}
	if want := toolTurnDigest([]byte("first summary second summary carrier text")); got[0].TextSHA256 != want {
		t.Fatal("composite aggregated reasoning text digest mismatch", got[0].TextSHA256)
	}
	if observation.Text != "complete" || observation.Generations[1].TextSHA256 != toolTurnDigest([]byte("complete")) {
		t.Fatal("composite final text projection changed while pairing reasoning", observation.Text)
	}
	if len(observation.Calls) != 2 || observation.Calls[0].ProviderCallID == observation.Calls[0].RequestID || observation.Calls[0].RequestID == observation.Calls[1].RequestID {
		t.Fatal("composite tool bindings did not survive reasoning pairing", observation.Calls)
	}
	if len(observation.TranscriptSHA256) != 64 || observation.ReceiptBindingID != fixture.receipt.BindingID || len(observation.ReceiptJournalHead) != 64 || len(observation.ReceiptStateSHA256) != 64 {
		t.Fatal("composite transcript or receipt bindings missing after reasoning pairing", observation)
	}
	encoded, err := canonical.Bytes(observation)
	if err != nil || !strings.Contains(string(encoded), `"encrypted_content":"`+cipher+`"`) || !strings.Contains(string(encoded), `"provider_item_id":"`+item+`"`) {
		t.Fatal("composite encrypted carrier identity absent from canonical observation", string(encoded), err)
	}
	var roundTrip CompositeToolTurnObservation
	if err := canonical.Decode(encoded, &roundTrip); err != nil || !reflect.DeepEqual(roundTrip, observation) {
		t.Fatal("composite paired reasoning observation did not survive canonical round trip", roundTrip, err)
	}
}

func TestDecodeCompositeToolTurnSingleCarrierShapeUnchanged(t *testing.T) {
	fixture := newCompositeTurnFixture(t, false, true)
	spliceCompositeFinal(&fixture, []map[string]any{
		compositeReasoningCarrier("part_composite_reasoning", "msg_composite_final", "rs_provider_item", "synthetic-encrypted-replay", 31, 32),
	})
	observation, err := decodeCompositeFixture(t, &fixture)
	if err != nil {
		t.Fatal(err)
	}
	got := observation.Generations[1].Reasoning
	if len(got) != 1 || got[0].PartID != "part_composite_reasoning" || got[0].ProviderItemID != "rs_provider_item" || got[0].EncryptedContent != "synthetic-encrypted-replay" || got[0].TextSHA256 != toolTurnDigest([]byte("carrier text")) {
		t.Fatal("composite single-carrier reasoning projection changed", got)
	}
}

func TestDecodeCompositeToolTurnRejectsUnpairedOrAmbiguousReasoning(t *testing.T) {
	tests := map[string]func(*compositeTurnFixture){
		"orphan summary": func(f *compositeTurnFixture) {
			spliceCompositeFinal(f, []map[string]any{
				compositeReasoningFragment("part_composite_frag", "msg_composite_final", "rs_item", 31, 32),
			})
		},
		"mismatched provider item": func(f *compositeTurnFixture) {
			spliceCompositeFinal(f, []map[string]any{
				compositeReasoningFragment("part_composite_frag", "msg_composite_final", "rs_item_a", 31, 32),
				compositeReasoningCarrier("part_composite_carrier", "msg_composite_final", "rs_item_b", "synthetic-encrypted-a", 32, 33),
			})
		},
		"duplicate carriers": func(f *compositeTurnFixture) {
			spliceCompositeFinal(f, []map[string]any{
				compositeReasoningCarrier("part_composite_carrier_1", "msg_composite_final", "rs_item", "synthetic-encrypted-a", 31, 32),
				compositeReasoningCarrier("part_composite_carrier_2", "msg_composite_final", "rs_item", "synthetic-encrypted-a", 32, 33),
			})
		},
		"contradictory carriers": func(f *compositeTurnFixture) {
			spliceCompositeFinal(f, []map[string]any{
				compositeReasoningCarrier("part_composite_carrier_1", "msg_composite_final", "rs_item", "synthetic-encrypted-a", 31, 32),
				compositeReasoningCarrier("part_composite_carrier_2", "msg_composite_final", "rs_item", "synthetic-encrypted-b", 32, 33),
			})
		},
		"cross-generation pair": func(f *compositeTurnFixture) {
			intermediateParts := parts(f.transcript[1])
			fragment := compositeReasoningFragment("part_composite_frag", "msg_composite_tools", "rs_shared_item", 13, 14)
			f.transcript[1]["parts"] = append(intermediateParts[:2], append([]map[string]any{fragment}, intermediateParts[2:]...)...)
			spliceCompositeFinal(f, []map[string]any{
				compositeReasoningCarrier("part_composite_carrier", "msg_composite_final", "rs_shared_item", "synthetic-encrypted-a", 31, 32),
			})
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newCompositeTurnFixture(t, false, true)
			mutate(&fixture)
			if _, err := decodeCompositeFixture(t, &fixture); err == nil {
				t.Fatal("composite unpaired or ambiguous reasoning admitted")
			}
		})
	}
}
