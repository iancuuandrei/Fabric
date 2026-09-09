package opencode

import (
	"errors"
	"slices"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/safepath"
)

// ResultFromSealedCompositeToolTurn projects a replay-validated composite seal
// into the common runtime result. OpenCode counters are not provider accounting.
func ResultFromSealedCompositeToolTurn(invocation runtime.Invocation, observation CompositeToolTurnObservation, receipt CompositeToolTurnTerminalReceipt) (runtime.Result, error) {
	var zero runtime.Result
	expected, err := runtime.NewInvocation(invocation.Profile, invocation.Input)
	if err != nil || invocation.Version != 1 || invocation.ID != expected.ID {
		return zero, errors.New("sealed composite result invocation mismatch")
	}
	if err := validateSealedCompositeToolTurnEvidence(invocation, observation, receipt); err != nil {
		return zero, err
	}
	if err := ValidateRuntimeMetadataForResult(observation.RuntimeMetadataExpectation, observation.RuntimeMetadata); err != nil {
		return zero, err
	}
	model := observation.Final.Binding.Model
	provider := observation.Final.Binding.Provider
	effort := observation.Final.Binding.Variant
	result := runtime.Result{
		Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile,
		ObservedModel: &model, ObservedProvider: &provider, ObservedEffort: &effort,
		Output: observation.Text, Usage: runtime.Usage{},
	}
	if err := runtime.ValidateResult(invocation, result, true); err != nil {
		return zero, err
	}
	return result, nil
}

func validateSealedCompositeToolTurnEvidence(invocation runtime.Invocation, observation CompositeToolTurnObservation, receipt CompositeToolTurnTerminalReceipt) error {
	for _, digest := range []string{
		receipt.IntentID, receipt.InvocationID, receipt.ObservationSHA256, receipt.TranscriptSHA256,
		receipt.OpenBrokerStateID, receipt.ClosedBrokerStateID,
		receipt.ReceiptBindingID, receipt.ReceiptJournalHead, receipt.ReceiptStateSHA256,
		receipt.ToolsConfigurationSHA256, receipt.MCPStatusSHA256,
		observation.TranscriptSHA256, observation.ReceiptBindingID,
		observation.ReceiptJournalHead, observation.ReceiptStateSHA256,
	} {
		if safepath.RequireDigest(digest) != nil {
			return errors.New("invalid sealed composite result identity")
		}
	}
	if receipt.Version != 1 || receipt.InvocationID != invocation.ID || receipt.ProcessID <= 0 || !receipt.MCPHandlersStopped || !receipt.ProviderProxyStopped || !receipt.RootProcessReaped || !receipt.BrokerClosed || safepath.RequireDigest(receipt.ProviderProcessSHA256) != nil || safepath.RequireDigest(receipt.ProviderProxySHA256) != nil {
		return errors.New("composite turn lacks provider terminal lifecycle receipt")
	}
	observationID, err := canonical.Hash("harness.opencode-composite-seal-observation.v1", observation)
	if err != nil || observationID != receipt.ObservationSHA256 || observation.TranscriptSHA256 != receipt.TranscriptSHA256 || observation.ReceiptBindingID != receipt.ReceiptBindingID || observation.ReceiptJournalHead != receipt.ReceiptJournalHead || observation.ReceiptStateSHA256 != receipt.ReceiptStateSHA256 {
		return errors.New("sealed composite observation identity mismatch")
	}
	if len(observation.Generations) == 0 {
		return errors.New("sealed composite generations required")
	}
	last := observation.Generations[len(observation.Generations)-1]
	if last.Assistant != observation.Final || last.Finish != "stop" || len(last.Calls) != 0 {
		return errors.New("sealed composite final generation mismatch")
	}
	binding := observation.Final.Binding
	if binding.Provider != invocation.Profile.Provider || binding.Model != invocation.Profile.Model || binding.Variant != invocation.Profile.Effort {
		return errors.New("sealed composite runtime profile mismatch")
	}
	flattened := make([]CompositeToolCallObservation, 0, len(observation.Calls))
	var metadata []PatchSnapshotReceipt
	for _, generation := range observation.Generations {
		for _, call := range generation.Calls {
			if call.BindingID != observation.ReceiptBindingID || call.InvocationID != invocation.ID || safepath.RequireDigest(call.ArgumentsSHA256) != nil || safepath.RequireDigest(call.ProviderArgumentsSHA256) != nil || call.ArgumentsSHA256 == call.ProviderArgumentsSHA256 {
				return errors.New("sealed composite call binding mismatch")
			}
			flattened = append(flattened, call)
		}
		metadata = append(metadata, generation.RuntimeMetadata...)
	}
	if !slices.Equal(flattened, observation.Calls) {
		return errors.New("sealed composite call sequence mismatch")
	}
	if !equalCanonical(metadata, observation.RuntimeMetadata) {
		return errors.New("sealed composite runtime metadata sequence mismatch")
	}
	return nil
}
