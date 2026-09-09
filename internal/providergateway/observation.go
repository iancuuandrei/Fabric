package providergateway

import "errors"

// ObserveChatCompletionSSE validates a complete response against one exact
// admitted call and returns a receipt suitable for durable completion. It has
// no network or journal effects. ResponseSHA256 opaquely binds the response ID
// retained in the validated stream observation.
func ObserveChatCompletionSSE(binding Binding, call CallIntent, raw []byte) (CallReceipt, error) {
	var receipt CallReceipt
	bindingID, err := binding.ID()
	if err != nil {
		return receipt, err
	}
	tokenLimit := providerTokenLimit(binding)
	callID, err := call.ID()
	if err != nil || call.CallID != callID || call.BindingID != bindingID || call.InvocationID != binding.AccessInvocationID || call.RouteID != binding.RouteID || call.EndpointID != binding.EndpointID || call.ModelID != binding.ModelID || call.Sequence > binding.Model.MaxCalls || call.RequestBytes > binding.Model.MaxRequestBytes || call.MaxOutputTokens > binding.Model.MaxOutputTokens || call.MaxOutputTokens > tokenLimit {
		return receipt, errors.New("provider observation call differs from binding")
	}
	observation, err := ParseChatCompletionSSE(raw, int(binding.Model.MaxResponseBytes), tokenLimit)
	if err != nil {
		return receipt, err
	}
	if !AcceptsObservedModel(binding.Model, observation.Model) {
		return receipt, errors.New("provider observation model substitution")
	}
	if observation.Usage.OutputTokens > call.MaxOutputTokens {
		return receipt, errors.New("provider observation exceeds call output limit")
	}
	semanticTools, err := chatSemanticTools(observation.ToolCalls)
	if err != nil {
		return receipt, err
	}
	receipt = CallReceipt{
		Version:        1,
		BindingID:      bindingID,
		InvocationID:   binding.AccessInvocationID,
		CallID:         call.CallID,
		ResponseID:     observation.ResponseID,
		ResponseSHA256: observation.SHA256,
		ResponseBytes:  int64(observation.SizeBytes),
		ObservedModel:  observation.Model,
		Finish:         observation.FinishReason,
		StreamComplete: true,
		UsageComplete:  true,
		Usage:          cloneUsage(observation.Usage),
		Semantic:       responseSemanticProjection(observation.OutputText, semanticTools),
	}
	return receipt, nil
}
