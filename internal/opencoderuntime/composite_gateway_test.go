package opencoderuntime

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/toolreceipts"
)

func TestCompositeFinalGatewayMatchesExactGenerationsAndUsesGatewayUsage(t *testing.T) {
	_, bound := writeCompositeGatewayTranscript(t, false, compositeProviderToolName)
	observation := compositeGatewayObservation(t)
	observation.Tokens = opencode.ToolTurnTokens{Input: 999, Output: 888, Reasoning: 777, CacheRead: 666, CacheWrite: 555}
	state, err := ValidateCompositeFinalGateway(bound, observation)
	if err != nil || !state.Finished || state.Exhausted || state.Aggregate.InputTokens != 15 || state.Aggregate.OutputTokens != 6 {
		t.Fatal("exact composite gateway transcript was not accepted with authoritative usage", state, err)
	}
}

func TestCompositeFinalGatewayRejectsIdentityAndSemanticSubstitution(t *testing.T) {
	_, exactBound := writeCompositeGatewayTranscript(t, false, compositeProviderToolName)
	cases := []struct {
		name        string
		mutateBound func(*Bound)
		mutate      func(*opencode.CompositeToolTurnObservation)
	}{
		{"binding id", func(bound *Bound) { bound.Gateway.BindingID = strings.Repeat("f", 64) }, nil},
		{"model identity", func(bound *Bound) { bound.Gateway.Binding.Model.Model = "substituted-model" }, nil},
		{"initial head", func(bound *Bound) { bound.Gateway.InitialHead = strings.Repeat("f", 64) }, nil},
		{"initial state", func(bound *Bound) { bound.Gateway.InitialStateID = strings.Repeat("f", 64) }, nil},
		{"generation count", nil, func(observation *opencode.CompositeToolTurnObservation) {
			observation.Generations = observation.Generations[:1]
		}},
		{"generation text", nil, func(observation *opencode.CompositeToolTurnObservation) {
			observation.Generations[0].TextSHA256 = digestText("substituted")
		}},
		{"generation finish", nil, func(observation *opencode.CompositeToolTurnObservation) { observation.Generations[0].Finish = "stop" }},
		{"tool order", nil, func(observation *opencode.CompositeToolTurnObservation) {
			observation.Generations[0].Calls[0], observation.Generations[1].Calls[0] = observation.Generations[1].Calls[0], observation.Generations[0].Calls[0]
		}},
		{"provider call id", nil, func(observation *opencode.CompositeToolTurnObservation) {
			observation.Generations[0].Calls[0].ProviderCallID = "substituted-call"
		}},
		{"tool name", nil, func(observation *opencode.CompositeToolTurnObservation) {
			observation.Generations[0].Calls[0].Tool = "candidate_read"
		}},
		{"double-prefixed decoded tool", nil, func(observation *opencode.CompositeToolTurnObservation) {
			observation.Generations[0].Calls[0].Tool = "engorch_source_read"
		}},
		{"arguments", nil, func(observation *opencode.CompositeToolTurnObservation) {
			observation.Generations[0].Calls[0].ProviderArgumentsSHA256 = strings.Repeat("f", 64)
		}},
		{"MCP digest substituted for provider digest", nil, func(observation *opencode.CompositeToolTurnObservation) {
			observation.Generations[0].Calls[0].ProviderArgumentsSHA256 = observation.Generations[0].Calls[0].ArgumentsSHA256
		}},
		{"final finish", nil, func(observation *opencode.CompositeToolTurnObservation) {
			observation.Generations[2].Finish = "tool-calls"
		}},
		{"final text", nil, func(observation *opencode.CompositeToolTurnObservation) { observation.Text = "substituted" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			bound := exactBound
			gateway := *exactBound.Gateway
			bound.Gateway = &gateway
			observation := compositeGatewayObservation(t)
			if test.mutateBound != nil {
				test.mutateBound(&bound)
			}
			if test.mutate != nil {
				test.mutate(&observation)
			}
			if _, err := ValidateCompositeFinalGateway(bound, observation); err == nil {
				t.Fatal("gateway substitution accepted")
			}
		})
	}
}

func TestCompositeFinalGatewayRejectsExhaustedTurn(t *testing.T) {
	_, bound := writeCompositeGatewayTranscript(t, true, compositeProviderToolName)
	observation := compositeGatewayObservation(t)
	observation.Generations = observation.Generations[:1]
	if _, err := ValidateCompositeFinalGateway(bound, observation); err == nil {
		t.Fatal("exhausted gateway accepted as a finished composite turn")
	}
}

func TestCompositeFinalGatewayRequiresExactPinnedProviderNamespace(t *testing.T) {
	for _, test := range []struct {
		name     string
		toolName func(string) string
		mutate   func(*opencode.CompositeToolTurnObservation)
	}{
		{"missing", func(name string) string { return name }, nil},
		{"foreign", func(name string) string { return "foreign_" + name }, nil},
		{"double", func(name string) string { return "engorch_engorch_" + name }, func(observation *opencode.CompositeToolTurnObservation) {
			observation.Generations[0].Calls[0].Tool = "engorch_source_read"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, bound := writeCompositeGatewayTranscript(t, false, test.toolName)
			observation := compositeGatewayObservation(t)
			if test.mutate != nil {
				test.mutate(&observation)
			}
			if _, err := ValidateCompositeFinalGateway(bound, observation); err == nil {
				t.Fatal("invalid provider namespace accepted")
			}
		})
	}
}

func compositeGatewayObservation(t *testing.T) opencode.CompositeToolTurnObservation {
	t.Helper()
	call := func(id, name, arguments string) opencode.CompositeToolCallObservation {
		mcpHash, err := toolreceipts.ArgumentsSHA256(json.RawMessage(arguments))
		if err != nil {
			t.Fatal(err)
		}
		providerHash := digestText(arguments)
		if providerHash == mcpHash {
			t.Fatal("provider and MCP argument identity domains unexpectedly coincide")
		}
		return opencode.CompositeToolCallObservation{ProviderCallID: id, Tool: name, ArgumentsSHA256: mcpHash, ProviderArgumentsSHA256: providerHash}
	}
	return opencode.CompositeToolTurnObservation{
		Text: "done",
		Generations: []opencode.CompositeToolGenerationObservation{
			{Finish: "tool-calls", TextSHA256: digestText("source preface"), Calls: []opencode.CompositeToolCallObservation{call("call-1", "source_read", `{"path":"file.txt"}`)}},
			{Finish: "tool-calls", TextSHA256: digestText("agent preface"), Calls: []opencode.CompositeToolCallObservation{call("call-2", "list_agents", `{"limit":8}`)}},
			{Finish: "stop", TextSHA256: digestText("done"), Calls: []opencode.CompositeToolCallObservation{}},
		},
	}
}

func compositeProviderToolName(name string) string { return opencode.ToolsMCPServerName + "_" + name }

func writeCompositeGatewayTranscript(t *testing.T, exhausted bool, providerToolName func(string) string) (string, Bound) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "composite-gateway.jsonl")
	endpoint := providergateway.EndpointContract{Version: 2, Provider: "fixture", URL: "https://api.example.test/v1/chat/completions", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Auth: &providergateway.AuthContract{Scheme: "bearer", CredentialRef: "fixture-key"}}
	endpointID, err := endpoint.ID()
	if err != nil {
		t.Fatal(err)
	}
	maxCalls := 3
	if exhausted {
		maxCalls = 1
	}
	model := providergateway.ModelContract{Version: 2, Provider: "fixture", Model: "model", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Capabilities: &providergateway.ModelCapabilities{Tools: true, OutputCap: true, CompleteUsage: true}, AdapterCapabilities: json.RawMessage(`{}`), ContextWindowTokens: 128, MaxCalls: maxCalls, MaxRequestBytes: 4096, MaxResponseBytes: 4096, MaxOutputTokens: 32}
	modelID, err := model.ID()
	if err != nil {
		t.Fatal(err)
	}
	binding := providergateway.Binding{Version: 1, AccessPolicyID: strings.Repeat("a", 64), AccessInvocationID: strings.Repeat("b", 64), RouteID: strings.Repeat("c", 64), ReservedTokens: 100, EndpointID: endpointID, ModelID: modelID, Endpoint: endpoint, Model: model}
	bindingID, err := binding.ID()
	if err != nil {
		t.Fatal(err)
	}
	appendUnchecked(t, path, "provider.bound", binding)
	appendCall := func(sequence int, finish, text, name, argumentsHash string) {
		intent := providergateway.CallIntent{Version: 1, Sequence: sequence, BindingID: bindingID, InvocationID: binding.AccessInvocationID, RouteID: binding.RouteID, EndpointID: binding.EndpointID, ModelID: binding.ModelID, RequestSHA256: strings.Repeat(strconv.Itoa(sequence), 64), RequestBytes: 128, MaxOutputTokens: 16, RequestExpectationID: strings.Repeat(string(rune('a'+sequence)), 64)}
		intent.CallID, err = intent.ID()
		if err != nil {
			t.Fatal(err)
		}
		appendUnchecked(t, path, "provider.call-intent", intent)
		tools := []providergateway.ResponseToolIdentity{}
		if name != "" {
			tools = append(tools, providergateway.ResponseToolIdentity{ID: "call-" + strconv.Itoa(sequence), Name: providerToolName(name), ArgumentsSHA256: argumentsHash})
		}
		receipt := providergateway.CallReceipt{Version: 1, BindingID: bindingID, InvocationID: binding.AccessInvocationID, CallID: intent.CallID, ResponseSHA256: strings.Repeat(string(rune('0'+sequence)), 64), ResponseBytes: 256, ResponseID: "response-" + strconv.Itoa(sequence), ObservedModel: model.Model, Finish: finish, StreamComplete: true, UsageComplete: true, Usage: providergateway.Usage{InputTokens: 5, OutputTokens: 2}, Semantic: &providergateway.ResponseSemanticProjection{Version: 1, Kind: "assistant-turn", ToolCalls: tools, OutputTextSHA256: digestText(text)}, RequestExpectationID: intent.RequestExpectationID}
		appendUnchecked(t, path, "provider.call-receipt", receipt)
	}
	appendCall(1, "tool_calls", "source preface", "source_read", digestText(`{"path":"file.txt"}`))
	if !exhausted {
		appendCall(2, "tool_calls", "agent preface", "list_agents", digestText(`{"limit":8}`))
		appendCall(3, "stop", "done", "", "")
	}
	state, err := providergateway.Inspect(path)
	if err != nil || state.Finished == exhausted || state.Exhausted != exhausted {
		t.Fatal("composite gateway fixture terminal state mismatch", state, err)
	}
	initial := providergateway.State{Binding: &binding}
	initialID, err := canonical.Hash("harness.opencode-runtime-initial-gateway.v1", initial)
	if err != nil {
		t.Fatal(err)
	}
	events, err := journal.Read(path)
	if err != nil || len(events) == 0 {
		t.Fatal("read composite gateway fixture", err)
	}
	return path, Bound{Paths: Paths{Gateway: path}, Gateway: &GatewayBound{Binding: binding, BindingID: bindingID, InitialHead: events[0].Hash, InitialStateID: initialID}}
}
