package opencode

import (
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/toolbridge"
	"harness.local/engorch/internal/toolreceipts"
)

func TestResultFromSealedCompositeToolTurnRequiresExactReceiptAndCallSequence(t *testing.T) {
	invocation, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "low", Role: "writer"}, "use both tools")
	if err != nil {
		t.Fatal(err)
	}
	fixture := newCompositeTurnFixture(t, false, true)
	observation, err := DecodeCompositeToolTurn(marshalToolTranscript(t, fixture.transcript), fixture.binding, "use both tools", fixture.receiptPath, fixture.receipt, func(_ toolreceipts.Owner, _ toolbridge.Call, _ toolbridge.Result) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for generationIndex := range observation.Generations {
		for callIndex := range observation.Generations[generationIndex].Calls {
			observation.Generations[generationIndex].Calls[callIndex].InvocationID = invocation.ID
		}
	}
	for index := range observation.Calls {
		observation.Calls[index].InvocationID = invocation.ID
	}
	observation.ReceiptBindingID = strings.Repeat("a", 64)
	for generationIndex := range observation.Generations {
		for callIndex := range observation.Generations[generationIndex].Calls {
			observation.Generations[generationIndex].Calls[callIndex].BindingID = observation.ReceiptBindingID
		}
	}
	for index := range observation.Calls {
		observation.Calls[index].BindingID = observation.ReceiptBindingID
	}
	observation.Final.Binding.Variant = "low"
	last := len(observation.Generations) - 1
	observation.Generations[last].Assistant = observation.Final
	hash, err := canonical.Hash("harness.opencode-composite-seal-observation.v1", observation)
	if err != nil {
		t.Fatal(err)
	}
	receipt := CompositeToolTurnTerminalReceipt{
		Version: 1, IntentID: strings.Repeat("b", 64), InvocationID: invocation.ID, ObservationSHA256: hash,
		TranscriptSHA256: observation.TranscriptSHA256, OpenBrokerStateID: strings.Repeat("c", 64),
		ClosedBrokerStateID: strings.Repeat("d", 64), ReceiptBindingID: observation.ReceiptBindingID,
		ReceiptJournalHead: observation.ReceiptJournalHead, ReceiptStateSHA256: observation.ReceiptStateSHA256,
		ToolsConfigurationSHA256: strings.Repeat("e", 64), MCPStatusSHA256: strings.Repeat("f", 64),
		ProcessID: 1, MCPHandlersStopped: true, ProviderProxyStopped: true, RootProcessReaped: true, BrokerClosed: true,
		ProviderProcessSHA256: strings.Repeat("1", 64), ProviderProxySHA256: strings.Repeat("2", 64),
	}
	result, err := ResultFromSealedCompositeToolTurn(invocation, observation, receipt)
	if err != nil || result.Output != observation.Text || result.InvocationID != invocation.ID || result.Usage.InputTokens != nil {
		t.Fatal("sealed composite result projection failed", result, err)
	}
	changed := observation
	changed.Calls = append([]CompositeToolCallObservation(nil), changed.Calls...)
	changed.Calls[0], changed.Calls[1] = changed.Calls[1], changed.Calls[0]
	if result, err := ResultFromSealedCompositeToolTurn(invocation, changed, receipt); err == nil || result != (runtime.Result{}) {
		t.Fatal("substituted composite call order produced a result", result, err)
	}
	changed = observation
	changed.Calls = append([]CompositeToolCallObservation(nil), changed.Calls...)
	changed.Generations = append([]CompositeToolGenerationObservation(nil), changed.Generations...)
	changed.Generations[0].Calls = append([]CompositeToolCallObservation(nil), changed.Generations[0].Calls...)
	changed.Calls[0].ProviderArgumentsSHA256 = strings.Repeat("0", 64)
	changed.Generations[0].Calls[0].ProviderArgumentsSHA256 = strings.Repeat("0", 64)
	if result, err := ResultFromSealedCompositeToolTurn(invocation, changed, receipt); err == nil || result != (runtime.Result{}) {
		t.Fatal("tampered provider argument digest produced a result", result, err)
	}
}
