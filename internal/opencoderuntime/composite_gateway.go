package opencoderuntime

import (
	"errors"
	"strings"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/providergateway"
)

// ValidateCompositeFinalGateway proves that every decoded composite OpenCode
// generation corresponds, in order, to the exact finished provider gateway
// journal bound before dispatch. The returned state carries authoritative
// provider usage; OpenCode transcript token totals are not projected into it.
func ValidateCompositeFinalGateway(bound Bound, observation opencode.CompositeToolTurnObservation) (providergateway.State, error) {
	if bound.Gateway == nil || bound.Paths.Gateway == "" {
		return providergateway.State{}, errors.New("provider gateway runtime evidence missing")
	}
	bindingID, err := bound.Gateway.Binding.ID()
	if err != nil || bindingID != bound.Gateway.BindingID {
		return providergateway.State{}, errors.New("provider gateway binding identity changed")
	}
	events, err := journal.Read(bound.Paths.Gateway)
	if err != nil || len(events) == 0 || events[0].Hash != bound.Gateway.InitialHead {
		return providergateway.State{}, errors.New("provider gateway initial journal changed")
	}
	initial := providergateway.State{Binding: &bound.Gateway.Binding}
	initialID, err := canonical.Hash("harness.opencode-runtime-initial-gateway.v1", initial)
	if err != nil || initialID != bound.Gateway.InitialStateID {
		return providergateway.State{}, errors.New("provider gateway initial state changed")
	}
	state, err := providergateway.Inspect(bound.Paths.Gateway)
	if err != nil || state.Binding == nil || !equalCanonical(*state.Binding, bound.Gateway.Binding) || state.Pending != nil || !state.Finished || state.Exhausted || len(state.Calls) == 0 || len(state.Calls) != len(observation.Generations) {
		return providergateway.State{}, errors.New("provider gateway is not an exact finished composite tool turn")
	}
	for index, call := range state.Calls {
		generation := observation.Generations[index]
		if call.Intent.Sequence != index+1 || call.Receipt == nil || call.Receipt.Semantic == nil || call.Receipt.Semantic.OutputTextSHA256 != generation.TextSHA256 || len(call.Receipt.Semantic.ToolCalls) != len(generation.Calls) {
			return providergateway.State{}, errors.New("provider response semantics differ from composite OpenCode generation")
		}
		if call.Receipt.Finish != strings.ReplaceAll(generation.Finish, "-", "_") {
			return providergateway.State{}, errors.New("provider finish differs from composite OpenCode generation")
		}
		for callIndex, tool := range call.Receipt.Semantic.ToolCalls {
			observed := generation.Calls[callIndex]
			prefix := opencode.ToolsMCPServerName + "_"
			if strings.HasPrefix(observed.Tool, prefix) || tool.Name != prefix+observed.Tool {
				return providergateway.State{}, errors.New("provider tool namespace differs from composite OpenCode generation")
			}
			if tool.ID != observed.ProviderCallID {
				return providergateway.State{}, errors.New("provider tool call identity differs from composite OpenCode generation")
			}
			if tool.ArgumentsSHA256 != observed.ProviderArgumentsSHA256 {
				return providergateway.State{}, errors.New("provider tool argument digest differs from composite OpenCode generation")
			}
		}
	}
	last := state.Calls[len(state.Calls)-1].Receipt
	if last.Finish != "stop" || len(last.Semantic.ToolCalls) != 0 || last.Semantic.OutputTextSHA256 != digestText(observation.Text) {
		return providergateway.State{}, errors.New("provider final text differs from composite OpenCode result")
	}
	return state, nil
}
