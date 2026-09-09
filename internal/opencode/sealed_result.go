package opencode

import (
	"errors"
	"slices"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/safepath"
)

// ResultFromSealedToolTurn projects validated journal evidence into the common
// runtime result shape. The receipt is expected to come from seal replay; it is
// an integrity-bound local record, not a standalone authentication signature.
// OpenCode token fields are deliberately not promoted to provider accounting.
func ResultFromSealedToolTurn(invocation runtime.Invocation, observation ToolTurnObservation, receipt ToolTurnTerminalReceipt) (runtime.Result, error) {
	var zero runtime.Result
	expectedInvocation, err := runtime.NewInvocation(invocation.Profile, invocation.Input)
	if err != nil || invocation.Version != 1 || invocation.ID != expectedInvocation.ID {
		return zero, errors.New("sealed tool result invocation mismatch")
	}
	if err := validateSealedToolTurnEvidence(invocation, observation, receipt); err != nil {
		return zero, err
	}
	if err := ValidateRuntimeMetadataForResult(observation.RuntimeMetadataExpectation, observation.RuntimeMetadata); err != nil {
		return zero, err
	}
	model := observation.Final.Binding.Model
	provider := observation.Final.Binding.Provider
	effort := observation.Final.Binding.Variant
	output := observation.Text
	if observation.StructuredOutput != nil {
		output = string(*observation.StructuredOutput)
	}
	result := runtime.Result{
		Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile,
		ObservedModel: &model, ObservedProvider: &provider, ObservedEffort: &effort,
		Output: output, Usage: runtime.Usage{},
	}
	if err := runtime.ValidateResult(invocation, result, true); err != nil {
		return zero, err
	}
	return result, nil
}

func validateSealedToolTurnEvidence(invocation runtime.Invocation, observation ToolTurnObservation, receipt ToolTurnTerminalReceipt) error {
	for _, digest := range []string{
		receipt.IntentID, receipt.ObservationSHA256, receipt.TranscriptSHA256,
		receipt.OpenBrokerStateID, receipt.ClosedBrokerStateID,
		receipt.ToolsConfigurationSHA256, receipt.MCPStatusSHA256,
		observation.TranscriptSHA256, observation.BrokerBindingID, observation.BrokerStateID,
	} {
		if safepath.RequireDigest(digest) != nil {
			return errors.New("invalid sealed tool result identity")
		}
	}
	if receipt.Version != 1 || receipt.ProcessID <= 0 || !receipt.MCPHandlersStopped || !receipt.RootProcessReaped || !receipt.BrokerClosed {
		return errors.New("tool turn lacks terminal lifecycle receipt")
	}
	observationSHA256, err := canonical.Hash("harness.opencode-tool-turn-seal-observation.v1", observation)
	if err != nil || observationSHA256 != receipt.ObservationSHA256 || observation.TranscriptSHA256 != receipt.TranscriptSHA256 || observation.BrokerStateID != receipt.OpenBrokerStateID {
		return errors.New("sealed tool observation identity mismatch")
	}
	if len(observation.Generations) == 0 {
		return errors.New("sealed tool generations required")
	}
	last := observation.Generations[len(observation.Generations)-1]
	if last.Assistant != observation.Final || len(last.Calls) != 0 {
		return errors.New("sealed tool final generation mismatch")
	}
	if observation.StructuredOutput == nil {
		if last.Finish != "stop" || last.StructuredOutputTool != nil {
			return errors.New("sealed tool final generation mismatch")
		}
	} else {
		if last.Finish != "tool-calls" || last.StructuredOutputTool == nil || observation.StructuredOutputTool == nil || !equalCanonical(last.StructuredOutputTool, observation.StructuredOutputTool) {
			return errors.New("sealed structured output final generation mismatch")
		}
	}
	binding := observation.Final.Binding
	if binding.Provider != invocation.Profile.Provider || binding.Model != invocation.Profile.Model || binding.Variant != invocation.Profile.Effort {
		return errors.New("sealed tool runtime profile mismatch")
	}
	flattened := make([]ToolCallObservation, 0, len(observation.Calls))
	var metadata []PatchSnapshotReceipt
	for _, generation := range observation.Generations {
		for _, call := range generation.Calls {
			if call.BindingID != observation.BrokerBindingID || call.InvocationID != invocation.ID {
				return errors.New("sealed tool call binding mismatch")
			}
			flattened = append(flattened, call)
		}
		metadata = append(metadata, generation.RuntimeMetadata...)
	}
	if !slices.Equal(flattened, observation.Calls) {
		return errors.New("sealed tool call sequence mismatch")
	}
	if !equalCanonical(metadata, observation.RuntimeMetadata) {
		return errors.New("sealed runtime metadata sequence mismatch")
	}
	return nil
}
