package providergateway

import (
	"strings"
	"testing"
)

func observationBindingAndCall(t *testing.T, sequence int, requestByte, maxOutput int64) (Binding, CallIntent) {
	t.Helper()
	endpoint := EndpointContract{Version: 1, Provider: "openai", URL: "https://api.example.test/v1/chat/completions", Protocol: "openai-chat-completions-sse-v1"}
	endpointID, err := endpoint.ID()
	if err != nil {
		t.Fatal(err)
	}
	model := ModelContract{Version: 1, Provider: "openai", Model: "wire-model", Protocol: endpoint.Protocol, MaxCalls: 2, MaxRequestBytes: 4096, MaxResponseBytes: 64 << 10, MaxOutputTokens: 20}
	modelID, err := model.ID()
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{Version: 1, AccessPolicyID: strings.Repeat("a", 64), AccessInvocationID: strings.Repeat("b", 64), RouteID: strings.Repeat("c", 64), ReservedTokens: 40, EndpointID: endpointID, ModelID: modelID, Endpoint: endpoint, Model: model}
	bindingID, err := binding.ID()
	if err != nil {
		t.Fatal(err)
	}
	call := CallIntent{Version: 1, Sequence: sequence, BindingID: bindingID, InvocationID: binding.AccessInvocationID, RouteID: binding.RouteID, EndpointID: binding.EndpointID, ModelID: binding.ModelID, RequestSHA256: strings.Repeat(string(rune(requestByte)), 64), RequestBytes: 256, MaxOutputTokens: maxOutput}
	call.CallID, err = call.ID()
	if err != nil {
		t.Fatal(err)
	}
	return binding, call
}

func TestObserveChatCompletionSSEValidToolAndStopCalls(t *testing.T) {
	firstBinding, firstCall := observationBindingAndCall(t, 1, 'd', 4)
	first, err := ObserveChatCompletionSSE(firstBinding, firstCall, toolSSE())
	if err != nil {
		t.Fatal(err)
	}
	if first.BindingID != firstCall.BindingID || first.InvocationID != firstCall.InvocationID || first.CallID != firstCall.CallID || first.ResponseID != "chatcmpl-fixture" || first.Finish != "tool_calls" || !first.StreamComplete || !first.UsageComplete || first.Usage.InputTokens != 9 || first.Usage.OutputTokens != 4 || first.ResponseBytes != int64(len(toolSSE())) {
		t.Fatal("tool-call receipt differs from admitted observation", first)
	}

	secondBinding, secondCall := observationBindingAndCall(t, 2, 'e', 3)
	secondRaw := stopSSE(`{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}`)
	second, err := ObserveChatCompletionSSE(secondBinding, secondCall, secondRaw)
	if err != nil {
		t.Fatal(err)
	}
	if second.BindingID != secondCall.BindingID || second.InvocationID != secondCall.InvocationID || second.CallID != secondCall.CallID || second.ResponseID != "chatcmpl-fixture" || second.Finish != "stop" || !second.StreamComplete || !second.UsageComplete || second.Usage.InputTokens != 7 || second.Usage.OutputTokens != 3 || second.ResponseBytes != int64(len(secondRaw)) {
		t.Fatal("stop receipt differs from admitted observation", second)
	}
}

func TestObserveChatCompletionSSERejectsSubstitutionAndIncompleteEvidence(t *testing.T) {
	binding, call := observationBindingAndCall(t, 1, 'd', 4)
	valid := toolSSE()
	tests := map[string]func() (Binding, CallIntent, []byte){
		"foreign binding": func() (Binding, CallIntent, []byte) {
			changed := binding
			changed.AccessInvocationID = strings.Repeat("f", 64)
			return changed, call, valid
		},
		"foreign call": func() (Binding, CallIntent, []byte) {
			changed := call
			changed.RequestSHA256 = strings.Repeat("e", 64)
			return binding, changed, valid
		},
		"model substitution": func() (Binding, CallIntent, []byte) {
			return binding, call, []byte(strings.ReplaceAll(string(valid), "wire-model", "foreign-model"))
		},
		"missing usage": func() (Binding, CallIntent, []byte) {
			return binding, call, []byte(strings.Replace(string(valid), sseData(usageChunk(`{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}`)), "", 1))
		},
		"malformed stream": func() (Binding, CallIntent, []byte) {
			return binding, call, valid[:len(valid)-1]
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidateBinding, candidateCall, raw := mutate()
			if _, err := ObserveChatCompletionSSE(candidateBinding, candidateCall, raw); err == nil {
				t.Fatal("invalid provider observation was admitted")
			}
		})
	}
}

func TestObserveChatCompletionSSEEnforcesExactCallAndReservationCaps(t *testing.T) {
	binding, call := observationBindingAndCall(t, 1, 'd', 3)
	overOutput := toolSSE()
	if _, err := ObserveChatCompletionSSE(binding, call, overOutput); err == nil {
		t.Fatal("response above exact call output cap admitted")
	}

	call.MaxOutputTokens = binding.ReservedTokens + 1
	call.CallID, _ = call.ID()
	if _, err := ObserveChatCompletionSSE(binding, call, stopSSE(`{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}`)); err == nil {
		t.Fatal("call output cap above access reservation admitted")
	}

	binding.ReservedTokens = 10
	bindingID, _ := binding.ID()
	call.BindingID = bindingID
	call.MaxOutputTokens = 4
	call.CallID, _ = call.ID()
	if _, err := ObserveChatCompletionSSE(binding, call, toolSSE()); err == nil {
		t.Fatal("inclusive response usage above access reservation admitted")
	}
}
