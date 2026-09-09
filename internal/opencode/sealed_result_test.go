package opencode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/runtime"
)

func TestResultFromSealedToolTurnPreservesExactOutputAndUnknownUsage(t *testing.T) {
	invocation, observation, receipt := sealedResultFixture(t)
	result, err := ResultFromSealedToolTurn(invocation, observation, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != observation.Text || result.InvocationID != invocation.ID || result.Requested != invocation.Profile {
		t.Fatal("sealed result lost exact invocation or output", result)
	}
	if result.ObservedProvider == nil || *result.ObservedProvider != invocation.Profile.Provider || result.ObservedModel == nil || *result.ObservedModel != invocation.Profile.Model || result.ObservedEffort == nil || *result.ObservedEffort != invocation.Profile.Effort {
		t.Fatal("sealed result lost observed runtime profile", result)
	}
	if result.Usage.InputTokens != nil || result.Usage.OutputTokens != nil || result.Usage.CostMinorUnits != nil {
		t.Fatal("host accounting was promoted to provider usage", result.Usage)
	}
	if err := runtime.ValidateResult(invocation, result, true); err != nil {
		t.Fatal("projected sealed result violates runtime contract", err)
	}
}

func TestResultFromSealedToolTurnPreservesOpaqueProviderItemIDBoundary(t *testing.T) {
	invocation, observation, receipt := sealedResultFixture(t)
	observation.Generations[0].Calls[0].ProviderItemID = m1bProviderItemID
	observation.Calls[0].ProviderItemID = m1bProviderItemID
	var err error
	receipt.ObservationSHA256, err = canonical.Hash("harness.opencode-tool-turn-seal-observation.v1", observation)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ResultFromSealedToolTurn(invocation, observation, receipt)
	if err != nil || result.Output != observation.Text {
		t.Fatal("opaque provider item ID did not cross sealed runtime projection", result, err)
	}
	roundTrip := cloneToolTurnObservation(t, observation)
	if roundTrip.Calls[0].ProviderItemID != m1bProviderItemID || roundTrip.Generations[0].Calls[0].ProviderItemID != m1bProviderItemID {
		t.Fatal("sealed observation round trip changed opaque provider item ID", roundTrip.Calls)
	}
}

func TestResultFromSealedToolTurnRejectsSubstitution(t *testing.T) {
	invocation, original, originalReceipt := sealedResultFixture(t)
	tests := []struct {
		name   string
		mutate func(*runtime.Invocation, *ToolTurnObservation, *ToolTurnTerminalReceipt)
	}{
		{"changed invocation", func(i *runtime.Invocation, _ *ToolTurnObservation, _ *ToolTurnTerminalReceipt) { i.Input += " changed" }},
		{"changed output", func(_ *runtime.Invocation, o *ToolTurnObservation, _ *ToolTurnTerminalReceipt) { o.Text += " changed" }},
		{"changed transcript", func(_ *runtime.Invocation, _ *ToolTurnObservation, r *ToolTurnTerminalReceipt) {
			r.TranscriptSHA256 = strings.Repeat("a", 64)
		}},
		{"changed broker state", func(_ *runtime.Invocation, _ *ToolTurnObservation, r *ToolTurnTerminalReceipt) {
			r.OpenBrokerStateID = strings.Repeat("b", 64)
		}},
		{"nonterminal receipt", func(_ *runtime.Invocation, _ *ToolTurnObservation, r *ToolTurnTerminalReceipt) {
			r.RootProcessReaped = false
		}},
		{"provider substitution behind matching hash", func(_ *runtime.Invocation, o *ToolTurnObservation, r *ToolTurnTerminalReceipt) {
			o.Final.Binding.Provider = "foreign"
			o.Generations[len(o.Generations)-1].Assistant = o.Final
			r.ObservationSHA256, _ = canonical.Hash("harness.opencode-tool-turn-seal-observation.v1", *o)
		}},
		{"call binding substitution behind matching hash", func(_ *runtime.Invocation, o *ToolTurnObservation, r *ToolTurnTerminalReceipt) {
			o.Generations[0].Calls[0].BindingID = strings.Repeat("c", 64)
			o.Calls[0] = o.Generations[0].Calls[0]
			r.ObservationSHA256, _ = canonical.Hash("harness.opencode-tool-turn-seal-observation.v1", *o)
		}},
		{"flattened call sequence substitution", func(_ *runtime.Invocation, o *ToolTurnObservation, r *ToolTurnTerminalReceipt) {
			o.Calls = nil
			r.ObservationSHA256, _ = canonical.Hash("harness.opencode-tool-turn-seal-observation.v1", *o)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			i := invocation
			o := cloneToolTurnObservation(t, original)
			r := originalReceipt
			test.mutate(&i, &o, &r)
			if result, err := ResultFromSealedToolTurn(i, o, r); err == nil || result != (runtime.Result{}) {
				t.Fatal("substituted sealed evidence produced a runtime result", result, err)
			}
		})
	}
}

func sealedResultFixture(t *testing.T) (runtime.Invocation, ToolTurnObservation, ToolTurnTerminalReceipt) {
	t.Helper()
	intent, brokerPath, broker := synchronousToolFixture(t)
	t.Cleanup(func() { _ = broker.Close() })
	receipt, err := broker.Call(context.Background(), "broker-list-1", "source_list", json.RawMessage(`{"after":"","limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	_, transcript := synchronousToolWire(t, intent.Dispatch, receipt)
	brokerState, err := contextbroker.Inspect(brokerPath)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := decodeToolTurn([]byte(transcript), intent.Dispatch.Binding, intent.Dispatch.Text, brokerState)
	if err != nil {
		t.Fatal(err)
	}
	observationSHA256, err := canonical.Hash("harness.opencode-tool-turn-seal-observation.v1", observation)
	if err != nil {
		t.Fatal(err)
	}
	terminal := ToolTurnTerminalReceipt{
		Version: 1, IntentID: strings.Repeat("1", 64), ObservationSHA256: observationSHA256,
		TranscriptSHA256: observation.TranscriptSHA256, OpenBrokerStateID: observation.BrokerStateID,
		ClosedBrokerStateID: strings.Repeat("2", 64), ToolsConfigurationSHA256: strings.Repeat("3", 64),
		MCPStatusSHA256: strings.Repeat("4", 64), ProcessID: 123,
		MCPHandlersStopped: true, RootProcessReaped: true, BrokerClosed: true,
	}
	return intent.Invocation, observation, terminal
}

func cloneToolTurnObservation(t *testing.T, observation ToolTurnObservation) ToolTurnObservation {
	t.Helper()
	raw, err := canonical.Bytes(observation)
	if err != nil {
		t.Fatal(err)
	}
	var result ToolTurnObservation
	if err := canonical.Decode(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
