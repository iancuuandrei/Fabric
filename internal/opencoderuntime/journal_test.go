package opencoderuntime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/writercontract"
)

type runtimeFixture struct {
	path   string
	intent Intent
	paths  Paths
	bound  Bound
	broker *contextbroker.Broker
}

func TestIntentV2ResolvesObservedProjectIntoExactToolSession(t *testing.T) {
	fixture := newRuntimeFixture(t)
	legacyRaw, err := canonical.Bytes(fixture.intent)
	if err != nil || strings.Contains(string(legacyRaw), "session_plan") {
		t.Fatal("v1 intent durable shape changed", err, string(legacyRaw))
	}
	legacy := fixture.intent.Session
	intent := fixture.intent
	intent.Version = 2
	intent.IntentID = ""
	intent.Session = opencode.ToolSessionBinding{}
	intent.SessionPlan = &SessionPlan{
		Agent: legacy.Session.Agent, Provider: legacy.Session.Provider,
		Model: legacy.Session.Model, Variant: legacy.Session.Variant,
		ToolNames: append([]string(nil), legacy.ToolNames...), CatalogSHA256: legacy.CatalogSHA256,
	}
	if _, err := intent.ID(); err != nil {
		t.Fatal("valid v2 session plan rejected", err)
	}
	binding, err := intent.ResolveToolSessionBinding("git_project")
	if err != nil {
		t.Fatal("observed project did not resolve session plan", err)
	}
	if binding.Session.ProjectID != "git_project" || binding.Session.IntentID != intent.Invocation.ID || binding.Session.Directory != intent.Directory || binding.Session.Agent != legacy.Session.Agent || !equalCanonical(binding.ToolNames, legacy.ToolNames) {
		t.Fatal("resolved session binding differs from plan", binding)
	}
	runtimePath := filepath.Join(t.TempDir(), "runtime-v2.jsonl")
	if err := RecordIntent(runtimePath, intent); err != nil {
		t.Fatal("record v2 intent", err)
	}
	sealExpected := fixture.bound.SealExpected
	sealExpected.HostRoot = t.TempDir()
	sealExpected.WorkingDirectory = intent.Directory
	if _, err := RecordBound(runtimePath, intent, fixture.bound.Project, fixture.paths, sealExpected); err != nil {
		t.Fatal("bind observed session to v2 plan", err)
	}
	if state, err := Inspect(runtimePath, intent); err != nil || state.Bound == nil || !equalCanonical(state.Bound.Session.Binding, legacy) || state.Bound.SealExpected.HostRoot == state.Bound.SealExpected.WorkingDirectory {
		t.Fatal("replay v2 planned/observed session split", state, err)
	}

	withPreobservedSession := intent
	withPreobservedSession.Session = legacy
	if _, err := withPreobservedSession.ID(); err == nil {
		t.Fatal("v2 intent admitted a pre-observed project/session binding")
	}
	legacyWithPlan := fixture.intent
	legacyWithPlan.SessionPlan = intent.SessionPlan
	if _, err := legacyWithPlan.ID(); err == nil {
		t.Fatal("v1 intent identity admitted a v2 session plan")
	}
}

func TestRecordBoundStructuredOutputUsesValueIdentityForIndependentExpectations(t *testing.T) {
	fixture := newStructuredBoundFixture(t)
	bound, err := RecordBound(fixture.path, fixture.intent, fixture.project, fixture.paths, fixture.sealExpected)
	if err != nil {
		t.Fatalf("independently allocated identical structured expectation was rejected: %v", err)
	}
	if bound.SealExpected.Dispatch.Dispatch.StructuredOutput == fixture.intent.StructuredOutput {
		t.Fatal("structured expectation pointers were unexpectedly aliased")
	}

	mutatedExpectation, err := opencode.NewStructuredOutputExpectation(json.RawMessage(`{"additionalProperties":false,"properties":{"unauthorized":{"type":"string"}},"required":["unauthorized"],"type":"object"}`))
	if err != nil {
		t.Fatal("build mutated structured expectation", err)
	}
	mutatedSeal := fixture.sealExpected
	mutatedDispatch := mutatedSeal.Dispatch
	mutatedDispatch.Dispatch.StructuredOutput = &mutatedExpectation
	mutatedSeal.Dispatch = mutatedDispatch
	mutatedPath := filepath.Join(t.TempDir(), "runtime-mutated.jsonl")
	if err := RecordIntent(mutatedPath, fixture.intent); err != nil {
		t.Fatal("record mutation fixture intent", err)
	}
	if _, err := RecordBound(mutatedPath, fixture.intent, fixture.project, fixture.paths, mutatedSeal); err == nil {
		t.Fatal("structured schema mutation was admitted at RecordBound")
	}
}

func TestFinalGatewayMatchesEveryGenerationTextAndOrderedTools(t *testing.T) {
	_, bound := writeGatewayTranscript(t, false)
	observation := opencode.ToolTurnObservation{
		Text: "done",
		Generations: []opencode.ToolGenerationObservation{
			{Finish: "tool-calls", TextSHA256: digestText("preface"), Calls: []opencode.ToolCallObservation{
				{ProviderCallID: "call-1", Tool: "source_read", ArgumentsSHA256: strings.Repeat("1", 64)},
				{ProviderCallID: "call-2", Tool: "candidate_read", ArgumentsSHA256: strings.Repeat("2", 64)},
			}},
			{Finish: "stop", TextSHA256: digestText("done"), Calls: []opencode.ToolCallObservation{}},
		},
	}
	state, err := validateFinalGateway(bound, observation)
	if err != nil || !state.Finished || state.Exhausted {
		t.Fatal("exact gateway transcript was not accepted", state, err)
	}

	observation.Generations[0].TextSHA256 = digestText("")
	if _, err := validateFinalGateway(bound, observation); err == nil {
		t.Fatal("intermediate generation text mismatch was admitted")
	}
	observation.Generations[0].TextSHA256 = digestText("preface")
	observation.Generations[0].Calls[0], observation.Generations[0].Calls[1] = observation.Generations[0].Calls[1], observation.Generations[0].Calls[0]
	if _, err := validateFinalGateway(bound, observation); err == nil {
		t.Fatal("reordered tool semantics were admitted")
	}
}

func TestFinalGatewayRejectsExhaustedToolCallAsSuccess(t *testing.T) {
	_, bound := writeGatewayTranscript(t, true)
	observation := opencode.ToolTurnObservation{Generations: []opencode.ToolGenerationObservation{{Finish: "tool-calls", TextSHA256: digestText("preface"), Calls: []opencode.ToolCallObservation{{ProviderCallID: "call-1", Tool: "source_read", ArgumentsSHA256: strings.Repeat("1", 64)}}}}}
	if _, err := validateFinalGateway(bound, observation); err == nil {
		t.Fatal("gateway call-limit exhaustion was accepted as a finished turn")
	}
}

func TestFinalGatewayStructuredOutputBindsTerminalAndProjectsResult(t *testing.T) {
	fixture := writeStructuredGatewayTranscript(t)
	state, err := providergateway.Inspect(fixture.path)
	if err != nil || !state.Finished || state.Exhausted || state.Pending != nil || len(state.Calls) != 2 {
		t.Fatalf("structured gateway transcript did not replay to a terminal state: %#v, %v", state, err)
	}
	if _, err := validateFinalGateway(fixture.bound, fixture.observation); err != nil {
		t.Fatal("source generation and structured terminal were not reconciled", err)
	}
	result, err := opencode.ResultFromSealedToolTurn(fixture.invocation, fixture.observation, fixture.receipt)
	if err != nil || result.Output != string(fixture.value) {
		t.Fatalf("structured terminal did not project through the independent result decoder: %#v, %v", result, err)
	}

	cases := []struct {
		name   string
		mutate func(*structuredGatewayFixture)
	}{
		{name: "wrong call ID", mutate: func(current *structuredGatewayFixture) {
			current.observation.StructuredOutputTool.CallID = "substituted-call"
		}},
		{name: "wrong arguments hash", mutate: func(current *structuredGatewayFixture) {
			current.observation.StructuredOutputTool.ArgumentsSHA256 = strings.Repeat("1", 64)
		}},
		{name: "wrong schema marker", mutate: func(current *structuredGatewayFixture) {
			wrong, err := opencode.NewStructuredOutputExpectation(json.RawMessage(`{"additionalProperties":false,"properties":{"other":{"type":"string"}},"required":["other"],"type":"object"}`))
			if err != nil {
				t.Fatalf("wrong schema fixture: %v", err)
			}
			current.bound.SealExpected.Dispatch.Dispatch.StructuredOutput = &wrong
		}},
		{name: "wrong structured object", mutate: func(current *structuredGatewayFixture) {
			value := json.RawMessage(`{"candidate_id":"different"}`)
			current.observation.StructuredOutput = &value
		}},
		{name: "missing structured value", mutate: func(current *structuredGatewayFixture) {
			current.observation.StructuredOutput = nil
		}},
		{name: "mixed ordinary and terminal capture", mutate: func(current *structuredGatewayFixture) {
			current.observation.Generations[1].Calls = append(current.observation.Generations[1].Calls, current.observation.Generations[0].Calls[0])
		}},
		{name: "nonterminal structured continuation", mutate: func(current *structuredGatewayFixture) {
			terminal := *current.observation.StructuredOutputTool
			current.observation.Generations[0].StructuredOutputTool = &terminal
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			current := cloneStructuredGatewayFixture(fixture)
			test.mutate(&current)
			if _, err := validateFinalGateway(current.bound, current.observation); err == nil {
				t.Fatal("invalid structured gateway reconciliation was admitted")
			}
		})
	}
}

func TestGatewayInitialBindingSurvivesCompletedTranscript(t *testing.T) {
	_, bound := writeGatewayTranscript(t, false)
	bound.SealExpected.Provider = &opencode.ProviderToolTurnSealExpected{}
	intent := Intent{ProviderGatewayBindingID: bound.Gateway.BindingID}

	if err := validateGatewayBoundCurrent(intent, bound); err != nil {
		t.Fatal("completed calls changed the canonical unused gateway prefix", err)
	}
}

func writeGatewayTranscript(t *testing.T, exhausted bool) (string, Bound) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway.jsonl")
	endpoint := providergateway.EndpointContract{Version: 2, Provider: "fixture", URL: "https://api.example.test/v1/chat/completions", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Auth: &providergateway.AuthContract{Scheme: "bearer", CredentialRef: "fixture-key"}}
	endpointID, err := endpoint.ID()
	if err != nil {
		t.Fatal(err)
	}
	maxCalls := 2
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
	appendCall := func(sequence int, finish, text string, tools []providergateway.ResponseToolIdentity) {
		intent := providergateway.CallIntent{Version: 1, Sequence: sequence, BindingID: bindingID, InvocationID: binding.AccessInvocationID, RouteID: binding.RouteID, EndpointID: binding.EndpointID, ModelID: binding.ModelID, RequestSHA256: strings.Repeat(string(rune('d'+sequence)), 64), RequestBytes: 128, MaxOutputTokens: 16, RequestExpectationID: strings.Repeat(string(rune('a'+sequence)), 64)}
		intent.CallID, err = intent.ID()
		if err != nil {
			t.Fatal(err)
		}
		appendUnchecked(t, path, "provider.call-intent", intent)
		receipt := providergateway.CallReceipt{Version: 1, BindingID: bindingID, InvocationID: binding.AccessInvocationID, CallID: intent.CallID, ResponseSHA256: strings.Repeat(string(rune('0'+sequence)), 64), ResponseBytes: 256, ResponseID: "response-" + strconv.Itoa(sequence), ObservedModel: model.Model, Finish: finish, StreamComplete: true, UsageComplete: true, Usage: providergateway.Usage{InputTokens: 5, OutputTokens: 2}, Semantic: &providergateway.ResponseSemanticProjection{Version: 1, Kind: "assistant-turn", ToolCalls: tools, OutputTextSHA256: digestText(text)}, RequestExpectationID: intent.RequestExpectationID}
		appendUnchecked(t, path, "provider.call-receipt", receipt)
	}
	appendCall(1, "tool_calls", "preface", []providergateway.ResponseToolIdentity{
		{ID: "call-1", Name: "source_read", ArgumentsSHA256: strings.Repeat("1", 64)},
		{ID: "call-2", Name: "candidate_read", ArgumentsSHA256: strings.Repeat("2", 64)},
	})
	if !exhausted {
		appendCall(2, "stop", "done", []providergateway.ResponseToolIdentity{})
	}
	state, err := providergateway.Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Finished == exhausted || state.Exhausted != exhausted {
		t.Fatal("gateway fixture terminal state mismatch", state)
	}
	initial := providergateway.State{Binding: &binding}
	initialID, err := canonical.Hash("harness.opencode-runtime-initial-gateway.v1", initial)
	if err != nil {
		t.Fatal(err)
	}
	events, err := journal.Read(path)
	if err != nil || len(events) == 0 {
		t.Fatal("read gateway fixture", err)
	}
	return path, Bound{Paths: Paths{Gateway: path}, Gateway: &GatewayBound{Binding: binding, BindingID: bindingID, InitialHead: events[0].Hash, InitialStateID: initialID}}
}

type structuredGatewayFixture struct {
	path        string
	bound       Bound
	invocation  runtime.Invocation
	expectation opencode.StructuredOutputExpectation
	observation opencode.ToolTurnObservation
	receipt     opencode.ToolTurnTerminalReceipt
	value       json.RawMessage
}

func writeStructuredGatewayTranscript(t *testing.T) structuredGatewayFixture {
	t.Helper()
	value := json.RawMessage(`{"candidate_id":"candidate-1"}`)
	expectation, err := opencode.NewStructuredOutputExpectation(json.RawMessage(`{"additionalProperties":false,"properties":{"candidate_id":{"type":"string"}},"required":["candidate_id"],"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "low", Role: "writer"}, `{"output_schema":`+string(expectation.Schema)+`,"instruction":"write"}`)
	if err != nil {
		t.Fatal(err)
	}
	terminal := providergateway.TerminalStructuredOutputExpectation{Version: 1, Name: providergateway.StructuredOutputToolName, Schema: append(json.RawMessage(nil), expectation.Schema...), SchemaSHA256: digestText(string(expectation.Schema))}
	if err := terminal.Validate(); err != nil {
		t.Fatal(err)
	}
	capabilities, err := canonical.Bytes(providergateway.ResponsesModelCapabilities{Version: 1, FunctionTools: true, Reasoning: false, SystemRoles: []string{"developer"}, Sampling: false, TextFormats: []string{"plain"}, KnownExtensions: []string{}, TrailingCostPingV1: false})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := providergateway.EndpointContract{Version: 2, Provider: "fixture", URL: "https://api.example.test/v1/responses", AdapterID: providergateway.OpenAIResponsesAdapter, Auth: &providergateway.AuthContract{Scheme: "bearer", CredentialRef: "fixture-key"}}
	endpointID, err := endpoint.ID()
	if err != nil {
		t.Fatal(err)
	}
	model := providergateway.ModelContract{Version: 2, Provider: "fixture", Model: "model", AdapterID: providergateway.OpenAIResponsesAdapter, Capabilities: &providergateway.ModelCapabilities{Tools: true, OutputCap: true, CompleteUsage: true}, AdapterCapabilities: capabilities, ContextWindowTokens: 128, MaxCalls: 2, MaxRequestBytes: 4096, MaxResponseBytes: 4096, MaxOutputTokens: 16}
	modelID, err := model.ID()
	if err != nil {
		t.Fatal(err)
	}
	binding := providergateway.Binding{Version: 1, AccessPolicyID: strings.Repeat("a", 64), AccessInvocationID: strings.Repeat("b", 64), RouteID: strings.Repeat("c", 64), ReservedTokens: 100, EndpointID: endpointID, ModelID: modelID, Endpoint: endpoint, Model: model}
	bindingID, err := binding.ID()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "gateway.jsonl")
	appendUnchecked(t, path, "provider.bound", binding)
	appendCall := func(sequence int, requestSHA, expectationID, responseID, finish string, semantic *providergateway.ResponseSemanticProjection) {
		intent := providergateway.CallIntent{Version: 1, Sequence: sequence, BindingID: bindingID, InvocationID: binding.AccessInvocationID, RouteID: binding.RouteID, EndpointID: binding.EndpointID, ModelID: binding.ModelID, RequestSHA256: requestSHA, RequestBytes: 128, MaxOutputTokens: 8, RequestExpectationID: expectationID, TerminalStructuredOutput: &terminal}
		intent.CallID, err = intent.ID()
		if err != nil {
			t.Fatal(err)
		}
		appendUnchecked(t, path, "provider.call-intent", intent)
		receipt := providergateway.CallReceipt{Version: 1, BindingID: bindingID, InvocationID: binding.AccessInvocationID, CallID: intent.CallID, ResponseSHA256: strings.Repeat(string(rune('0'+sequence)), 64), ResponseBytes: 256, ResponseID: responseID, ObservedModel: model.Model, Finish: finish, StreamComplete: true, UsageComplete: true, Usage: providergateway.Usage{InputTokens: 1, OutputTokens: 1}, Semantic: semantic, RequestExpectationID: expectationID}
		appendUnchecked(t, path, "provider.call-receipt", receipt)
	}
	ordinaryArguments := json.RawMessage(`{"path":"source.txt"}`)
	ordinaryIdentity := providergateway.ResponseToolIdentity{ID: "source-call", Name: "source_read", ArgumentsSHA256: digestText(string(ordinaryArguments))}
	appendCall(1, strings.Repeat("d", 64), strings.Repeat("e", 64), "response-source", "tool_calls", &providergateway.ResponseSemanticProjection{Version: 1, Kind: "assistant-turn", ToolCalls: []providergateway.ResponseToolIdentity{ordinaryIdentity}, OutputTextSHA256: digestText("")})
	terminalIdentity := providergateway.ResponseToolIdentity{ID: "structured-call", Name: providergateway.StructuredOutputToolName, ArgumentsSHA256: digestText(string(value))}
	appendCall(2, strings.Repeat("f", 64), strings.Repeat("1", 64), "response-structured", "tool_calls", &providergateway.ResponseSemanticProjection{Version: 1, Kind: "assistant-turn", ToolCalls: []providergateway.ResponseToolIdentity{terminalIdentity}, OutputTextSHA256: digestText(""), TerminalTool: &terminalIdentity, TerminalSchemaSHA256: terminal.SchemaSHA256})
	state, err := providergateway.Inspect(path)
	if err != nil || !state.Finished || state.Exhausted {
		t.Fatalf("structured gateway fixture did not close: %#v, %v", state, err)
	}
	events, err := journal.Read(path)
	if err != nil || len(events) == 0 {
		t.Fatal("read structured gateway fixture", err)
	}
	initial := providergateway.State{Binding: &binding}
	initialID, err := canonical.Hash("harness.opencode-runtime-initial-gateway.v1", initial)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	openCodeBinding := opencode.Binding{SessionID: "session-structured", ParentID: "parent-structured", Provider: invocation.Profile.Provider, Model: invocation.Profile.Model, Agent: "build", Directory: root, Root: root, Variant: invocation.Profile.Effort}
	dispatch, err := opencode.DispatchForInvocationWithStructuredOutput(invocation, openCodeBinding.SessionID, openCodeBinding.ParentID, openCodeBinding.Agent, openCodeBinding.Directory, openCodeBinding.Root, &expectation)
	if err != nil {
		t.Fatal(err)
	}
	sealDispatch := opencode.SynchronousToolDispatchIntent{Invocation: invocation, Dispatch: dispatch}
	bound := Bound{Paths: Paths{Gateway: path}, Gateway: &GatewayBound{Binding: binding, BindingID: bindingID, InitialHead: events[0].Hash, InitialStateID: initialID}, SealExpected: opencode.SynchronousToolTurnSealExpected{Dispatch: sealDispatch}}
	sourceCall := opencode.ToolCallObservation{MessageID: "message-source", PartID: "part-source", ProviderCallID: ordinaryIdentity.ID, Tool: ordinaryIdentity.Name, BindingID: strings.Repeat("2", 64), InvocationID: invocation.ID, RequestID: "request-source", BrokerCallID: "broker-source", ArgumentsSHA256: ordinaryIdentity.ArgumentsSHA256, ContentSHA256: strings.Repeat("3", 64)}
	assistant := opencode.Assistant{ID: "message-final", Binding: openCodeBinding}
	structuredTool := opencode.StructuredOutputToolObservation{PartID: "part-structured", CallID: terminalIdentity.ID, ArgumentsSHA256: terminalIdentity.ArgumentsSHA256, ResultSHA256: strings.Repeat("4", 64)}
	observation := opencode.ToolTurnObservation{Final: assistant, Text: "", StructuredOutput: &value, StructuredOutputTool: &structuredTool, Generations: []opencode.ToolGenerationObservation{{Assistant: assistant, Finish: "tool-calls", Calls: []opencode.ToolCallObservation{sourceCall}, TextSHA256: digestText("")}, {Assistant: assistant, Finish: "tool-calls", Calls: []opencode.ToolCallObservation{}, StructuredOutputTool: &structuredTool, TextSHA256: digestText("")}}, Calls: []opencode.ToolCallObservation{sourceCall}, TranscriptSHA256: strings.Repeat("5", 64), BrokerBindingID: strings.Repeat("2", 64), BrokerStateID: strings.Repeat("6", 64)}
	observationSHA, err := canonical.Hash("harness.opencode-tool-turn-seal-observation.v1", observation)
	if err != nil {
		t.Fatal(err)
	}
	receipt := opencode.ToolTurnTerminalReceipt{Version: 1, IntentID: strings.Repeat("7", 64), ObservationSHA256: observationSHA, TranscriptSHA256: observation.TranscriptSHA256, OpenBrokerStateID: observation.BrokerStateID, ClosedBrokerStateID: strings.Repeat("8", 64), ToolsConfigurationSHA256: strings.Repeat("9", 64), MCPStatusSHA256: strings.Repeat("a", 64), ProcessID: 42, MCPHandlersStopped: true, RootProcessReaped: true, BrokerClosed: true}
	return structuredGatewayFixture{path: path, bound: bound, invocation: invocation, expectation: expectation, observation: observation, receipt: receipt, value: value}
}

func cloneStructuredGatewayFixture(source structuredGatewayFixture) structuredGatewayFixture {
	result := source
	result.observation = source.observation
	result.observation.Generations = append([]opencode.ToolGenerationObservation(nil), source.observation.Generations...)
	for index := range result.observation.Generations {
		result.observation.Generations[index].Calls = append([]opencode.ToolCallObservation(nil), source.observation.Generations[index].Calls...)
		if source.observation.Generations[index].StructuredOutputTool != nil {
			tool := *source.observation.Generations[index].StructuredOutputTool
			result.observation.Generations[index].StructuredOutputTool = &tool
		}
	}
	if source.observation.StructuredOutput != nil {
		value := append(json.RawMessage(nil), (*source.observation.StructuredOutput)...)
		result.observation.StructuredOutput = &value
	}
	if source.observation.StructuredOutputTool != nil {
		tool := *source.observation.StructuredOutputTool
		result.observation.StructuredOutputTool = &tool
	}
	return result
}

func newRuntimeFixture(t *testing.T) runtimeFixture {
	return newRuntimeFixtureWithMetadata(t, false)
}

func newRuntimeFixtureWithMetadata(t *testing.T, metadata bool) runtimeFixture {
	t.Helper()
	sourceRoot := t.TempDir()
	git := func(arguments ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", sourceRoot}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v: %s", err, output)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(sourceRoot, "source.txt"), []byte("committed"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	source, err := repository.Discover(context.Background(), sourceRoot, "opencode-runtime-fixture")
	if err != nil {
		t.Fatal(err)
	}
	prompt := "Use source_list once, then return the fixture result."
	invocation, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "low", Role: "explorer"}, prompt)
	if err != nil {
		t.Fatal(err)
	}
	contextBinding, err := contextbroker.NewBinding(invocation.ID, source, nil, contextbroker.Limits{MaxCalls: 1, MaxRequestBytes: 4096, MaxResponseBytes: 64 << 10, MaxTotalResponseBytes: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	sessionBinding := opencode.ToolSessionBinding{Session: opencode.SessionBinding{IntentID: invocation.ID, ProjectID: "global", Directory: root, Agent: "build", Provider: "fixture", Model: "model", Variant: "low"}, ToolNames: []string{"source_list"}, CatalogSHA256: strings.Repeat("a", 64)}
	projectExpectation := opencode.ProjectExpectation{Directory: root, Mode: opencode.ProjectModeGlobal}
	intent := Intent{Version: 1, Invocation: invocation, Directory: root, Project: projectExpectation, Context: contextBinding, Session: sessionBinding}
	intent.IntentID, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := RecordIntent(runtimePath, intent); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	appendUnchecked(t, sessionPath, "opencode.tool-session-intent", sessionBinding)
	appendUnchecked(t, sessionPath, "opencode.tool-session-observed", struct {
		Binding opencode.ToolSessionBinding `json:"binding"`
		ID      string                      `json:"id"`
	}{sessionBinding, "ses_fixture"})
	brokerPath := filepath.Join(t.TempDir(), "broker.jsonl")
	broker, err := contextbroker.Open(brokerPath, contextBinding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	contextID, _ := contextBinding.ID()
	dispatch, err := opencode.DispatchForInvocation(invocation, "ses_fixture", "msg_user", "build", root, root)
	if err != nil {
		t.Fatal(err)
	}
	syncIntent := opencode.SynchronousToolDispatchIntent{Invocation: invocation, Dispatch: dispatch, BrokerBindingID: contextID, BrokerCatalogID: contextBinding.CatalogID}
	var metadataExpected *opencode.RuntimeMetadataExpectation
	if metadata {
		controllerPath := filepath.Join(root, ".harness", "runs", "controller.jsonl-wal")
		expected, metadataErr := opencode.NewRuntimeMetadataExpectation(opencode.MetadataAuthorityReadOnly, root, []string{controllerPath})
		if metadataErr != nil {
			t.Fatal(metadataErr)
		}
		metadataExpected = &expected
		syncIntent.RuntimeMetadata = metadataExpected
	}
	tools := opencode.ToolsConfigurationReceipt{SHA256: strings.Repeat("b", 64), MCPServer: opencode.ToolsMCPServerName, Endpoint: "http://127.0.0.1:43123/mcp", ToolIDs: []string{"engorch_source_list"}, TimeoutMillis: 5000}
	sealExpected := opencode.SynchronousToolTurnSealExpected{Dispatch: syncIntent, Session: sessionBinding, Tools: tools, ExecutableSHA256: strings.Repeat("c", 64), HostRoot: root, MaxOutputTokens: 128, RuntimeMetadata: metadataExpected}
	paths := Paths{Version: 1, Session: sessionPath, Dispatch: filepath.Join(t.TempDir(), "dispatch.jsonl"), Broker: brokerPath, Seal: filepath.Join(t.TempDir(), "seal.jsonl")}
	project := opencode.ProjectReceipt{SHA256: strings.Repeat("e", 64), ID: "global", Directory: root, Mode: opencode.ProjectModeGlobal, Worktree: "/"}
	bound, err := RecordBound(runtimePath, intent, project, paths, sealExpected)
	if err != nil {
		t.Fatal(err)
	}
	return runtimeFixture{path: runtimePath, intent: intent, paths: paths, bound: bound, broker: broker}
}

type structuredBoundFixture struct {
	path         string
	intent       Intent
	project      opencode.ProjectReceipt
	paths        Paths
	sealExpected opencode.SynchronousToolTurnSealExpected
}

func newStructuredBoundFixture(t *testing.T) structuredBoundFixture {
	t.Helper()
	base := newRuntimeFixture(t)
	schema := writercontract.UTF8Schema()
	expectation, err := opencode.NewStructuredOutputExpectation(schema)
	if err != nil {
		t.Fatal("build writer structured expectation", err)
	}
	invocation, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "low", Role: "writer"}, `{"output_schema":`+string(schema)+`,"instruction":"write"}`)
	if err != nil {
		t.Fatal("build writer invocation", err)
	}
	contextBinding, err := contextbroker.NewBinding(invocation.ID, base.intent.Context.Source, nil, base.intent.Context.Limits)
	if err != nil {
		t.Fatal("build writer context binding", err)
	}
	root := t.TempDir()
	intent := Intent{
		Version: 2, Invocation: invocation, Directory: root,
		Project:          opencode.ProjectExpectation{Directory: root, Mode: opencode.ProjectModeGlobal},
		Context:          contextBinding,
		SessionPlan:      &SessionPlan{Agent: "build", Provider: "fixture", Model: "model", Variant: "low", ToolNames: []string{"source_list"}, CatalogSHA256: strings.Repeat("a", 64)},
		StructuredOutput: &expectation,
	}
	intent.IntentID, err = intent.ID()
	if err != nil {
		t.Fatal("build writer runtime intent", err)
	}
	runtimePath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := RecordIntent(runtimePath, intent); err != nil {
		t.Fatal("record writer runtime intent", err)
	}
	sessionBinding, err := intent.ResolveToolSessionBinding("global")
	if err != nil {
		t.Fatal("resolve writer session binding", err)
	}
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	appendUnchecked(t, sessionPath, "opencode.tool-session-intent", sessionBinding)
	appendUnchecked(t, sessionPath, "opencode.tool-session-observed", struct {
		Binding opencode.ToolSessionBinding `json:"binding"`
		ID      string                      `json:"id"`
	}{sessionBinding, "ses_structured"})
	brokerPath := filepath.Join(t.TempDir(), "broker.jsonl")
	broker, err := contextbroker.Open(brokerPath, contextBinding)
	if err != nil {
		t.Fatal("open writer context broker", err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	contextID, err := contextBinding.ID()
	if err != nil {
		t.Fatal("identify writer context binding", err)
	}
	dispatch, err := opencode.DispatchForInvocationWithStructuredOutput(invocation, "ses_structured", "msg_user", "build", root, root, &expectation)
	if err != nil {
		t.Fatal("build writer dispatch", err)
	}
	syncIntent := opencode.SynchronousToolDispatchIntent{Invocation: invocation, Dispatch: dispatch, BrokerBindingID: contextID, BrokerCatalogID: contextBinding.CatalogID}
	tools := opencode.ToolsConfigurationReceipt{SHA256: strings.Repeat("b", 64), MCPServer: opencode.ToolsMCPServerName, Endpoint: "http://127.0.0.1:43123/mcp", ToolIDs: []string{"engorch_source_list"}, TimeoutMillis: 5000, AllowStructuredOutput: true}
	sealExpected := opencode.SynchronousToolTurnSealExpected{Dispatch: syncIntent, Session: sessionBinding, Tools: tools, ExecutableSHA256: strings.Repeat("c", 64), HostRoot: root, MaxOutputTokens: 128}
	paths := Paths{Version: 1, Session: sessionPath, Dispatch: filepath.Join(t.TempDir(), "dispatch.jsonl"), Broker: brokerPath, Seal: filepath.Join(t.TempDir(), "seal.jsonl")}
	project := opencode.ProjectReceipt{SHA256: strings.Repeat("e", 64), ID: "global", Directory: root, Mode: opencode.ProjectModeGlobal, Worktree: "/"}
	return structuredBoundFixture{path: runtimePath, intent: intent, project: project, paths: paths, sealExpected: sealExpected}
}

func appendUnchecked(t *testing.T, path, kind string, payload any) {
	t.Helper()
	if _, err := journal.Append(path, kind, payload, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestIntentAndBoundRetainExactSourceSessionAndSubjournalIdentity(t *testing.T) {
	fixture := newRuntimeFixture(t)
	state, err := Inspect(fixture.path, fixture.intent)
	if err != nil || state.Intent == nil || state.Bound == nil || state.Result != nil || state.Bound.Project != fixture.bound.Project || state.Bound.Session.SessionID != "ses_fixture" || state.Bound.ContextBindingID == "" || state.Bound.BrokerInitialHead == "" {
		t.Fatal("runtime binding replay mismatch", state, err)
	}
	changed := fixture.intent
	changed.Invocation.Input += " changed"
	if _, err := Inspect(fixture.path, changed); err == nil {
		t.Fatal("changed invocation admitted")
	}
	changed = fixture.intent
	changed.Context.Source.Commit = strings.Repeat("d", len(changed.Context.Source.Commit))
	if _, err := Inspect(fixture.path, changed); err == nil {
		t.Fatal("changed source binding admitted")
	}
	changed = fixture.intent
	changed.Project.Mode = opencode.ProjectModeGit
	changed.Project.Worktree = fixture.intent.Directory
	if _, err := Inspect(fixture.path, changed); err == nil {
		t.Fatal("changed project expectation admitted")
	}
	if err := RecordIntent(fixture.path, fixture.intent); err != nil {
		t.Fatal("exact intent recovery failed", err)
	}
	rebound, err := RecordBound(fixture.path, fixture.intent, fixture.bound.Project, fixture.paths, fixture.bound.SealExpected)
	if err != nil || !equalCanonical(rebound, fixture.bound) {
		t.Fatal("exact bound recovery failed", rebound, err)
	}
}

func TestBoundRejectsForeignPathsSessionAndUsedBroker(t *testing.T) {
	fixture := newRuntimeFixture(t)
	paths := fixture.paths
	paths.Seal = paths.Dispatch
	duplicateRuntime := filepath.Join(t.TempDir(), "duplicate-runtime.jsonl")
	if err := RecordIntent(duplicateRuntime, fixture.intent); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordBound(duplicateRuntime, fixture.intent, fixture.bound.Project, paths, fixture.bound.SealExpected); err == nil {
		t.Fatal("duplicate subordinate journal paths admitted")
	}
	foreignProjectRuntime := filepath.Join(t.TempDir(), "foreign-project-runtime.jsonl")
	if err := RecordIntent(foreignProjectRuntime, fixture.intent); err != nil {
		t.Fatal(err)
	}
	foreignProject := fixture.bound.Project
	foreignProject.Directory = filepath.Dir(foreignProject.Directory)
	if _, err := RecordBound(foreignProjectRuntime, fixture.intent, foreignProject, fixture.paths, fixture.bound.SealExpected); err == nil {
		t.Fatal("foreign OpenCode project receipt admitted")
	}
	// A fresh runtime intent cannot bind after its broker already performed IO.
	otherRuntime := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := RecordIntent(otherRuntime, fixture.intent); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.broker.Call(context.Background(), "broker-list-early", "source_list", json.RawMessage(`{"after":"","limit":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordBound(otherRuntime, fixture.intent, fixture.bound.Project, fixture.paths, fixture.bound.SealExpected); err == nil {
		t.Fatal("used context broker admitted at runtime binding")
	}
}

func TestBoundCurrentRejectsSubstitutedInitialBrokerAnchors(t *testing.T) {
	fixture := newRuntimeFixture(t)
	changed := fixture.bound
	changed.BrokerInitialHead = strings.Repeat("f", 64)
	if err := validateBoundCurrent(fixture.intent, changed); err == nil {
		t.Fatal("substituted initial broker head admitted")
	}
	changed = fixture.bound
	changed.BrokerInitialStateID = strings.Repeat("f", 64)
	if err := validateBoundCurrent(fixture.intent, changed); err == nil {
		t.Fatal("substituted initial broker state admitted")
	}
}

func TestProjectReceiptMustMatchHashBoundGitExpectation(t *testing.T) {
	fixture := newRuntimeFixture(t)
	intent := fixture.intent
	intent.Project = opencode.ProjectExpectation{Directory: intent.Directory, Mode: opencode.ProjectModeGit, Worktree: intent.Directory}
	intent.Session.Session.ProjectID = "git_project"
	receipt := opencode.ProjectReceipt{SHA256: strings.Repeat("e", 64), ID: "git_project", Directory: intent.Directory, Mode: opencode.ProjectModeGit, Worktree: intent.Directory, VCS: "git"}
	if err := validateProjectReceipt(receipt, intent); err != nil {
		t.Fatal("exact Git project receipt rejected", err)
	}
	receipt.Worktree = t.TempDir()
	if err := validateProjectReceipt(receipt, intent); err == nil {
		t.Fatal("foreign Git worktree admitted")
	}
}

func TestCompleteRecoversSealedSubjournalsWithoutResending(t *testing.T) {
	fixture := newRuntimeFixture(t)
	writeTerminalToolTurn(t, &fixture)
	beforeDispatch, _ := os.ReadFile(fixture.paths.Dispatch)
	beforeSeal, _ := os.ReadFile(fixture.paths.Seal)
	record, err := Complete(fixture.path, fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	if record.Result.Output != "tool result accepted" || record.Result.Usage.InputTokens != nil || record.Result.Usage.OutputTokens != nil || record.Result.Usage.CostMinorUnits != nil || record.DispatchHead == "" || record.BrokerHead == "" || record.SealHead == "" {
		t.Fatal("sealed runtime result projection mismatch", record)
	}
	again, err := Complete(fixture.path, fixture.intent)
	if err != nil || !equalCanonical(again, record) {
		t.Fatal("lost-result offline recovery changed result", again, err)
	}
	afterDispatch, _ := os.ReadFile(fixture.paths.Dispatch)
	afterSeal, _ := os.ReadFile(fixture.paths.Seal)
	if string(beforeDispatch) != string(afterDispatch) || string(beforeSeal) != string(afterSeal) {
		t.Fatal("offline runtime recovery mutated execution subjournals")
	}
	state, err := Inspect(fixture.path, fixture.intent)
	if err != nil || state.Result == nil || !equalCanonical(*state.Result, record) {
		t.Fatal("sealed runtime result did not replay", state, err)
	}
}

func TestCompletePersistsReadOnlyViolationBeforeBlockingResult(t *testing.T) {
	fixture := newRuntimeFixtureWithMetadata(t, true)
	workspaceFile := filepath.Join(fixture.intent.Directory, "unauthorized.txt")
	writeTerminalToolTurnWithPatch(t, &fixture, `[`+strconv.Quote(workspaceFile)+`]`)
	if _, err := Complete(fixture.path, fixture.intent); err == nil || !strings.Contains(err.Error(), opencode.MetadataViolationReadOnlyAuthority) {
		t.Fatal("read-only patch did not block result", err)
	}
	events, err := journal.Read(fixture.path)
	if err != nil || len(events) != 3 || events[2].Kind != metadataEvent {
		t.Fatal("classified metadata was not persisted before failed result", events, err)
	}
	state, err := Inspect(fixture.path, fixture.intent)
	if err != nil || state.Metadata == nil || state.Result != nil || len(state.Metadata.Receipts) != 1 || state.Metadata.Receipts[0].Violation != opencode.MetadataViolationReadOnlyAuthority {
		t.Fatal("persisted read-only violation did not replay", state, err)
	}
}

func TestCompleteAllowsEmptyPatchAndPersistsSeparateMetadata(t *testing.T) {
	fixture := newRuntimeFixtureWithMetadata(t, true)
	writeTerminalToolTurnWithPatch(t, &fixture, `[]`)
	record, err := Complete(fixture.path, fixture.intent)
	if err != nil || record.Result.Output != "tool result accepted" {
		t.Fatal("empty patch blocked semantic result", record, err)
	}
	state, err := Inspect(fixture.path, fixture.intent)
	if err != nil || state.Metadata == nil || state.Result == nil || len(state.Metadata.Receipts) != 1 || state.Metadata.Receipts[0].Classification != opencode.PatchClassificationEmpty {
		t.Fatal("empty patch metadata was not preserved apart from result", state, err)
	}
}

func writeTerminalToolTurn(t *testing.T, fixture *runtimeFixture) {
	writeTerminalToolTurnWithPatch(t, fixture, "")
}

func writeTerminalToolTurnWithPatch(t *testing.T, fixture *runtimeFixture, patch string) {
	t.Helper()
	response, err := fixture.broker.Call(context.Background(), "broker-list-1", "source_list", json.RawMessage(`{"after":"","limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	responseBytes, err := canonical.Bytes(response)
	if err != nil {
		t.Fatal(err)
	}
	binding := fixture.bound.SealExpected.Dispatch.Dispatch.Binding
	quotedRoot := strconv.Quote(binding.Root)
	quotedDirectory := strconv.Quote(binding.Directory)
	user := `{"info":{"id":"msg_user","sessionID":"ses_fixture","role":"user","time":{"created":1},"agent":"build","model":{"providerID":"fixture","modelID":"model","variant":"low"}},"parts":[{"id":"prt_user","messageID":"msg_user","sessionID":"ses_fixture","type":"text","text":` + strconv.Quote(fixture.intent.Invocation.Input) + `}]}`
	intermediate := `{"info":{"id":"msg_tool","sessionID":"ses_fixture","parentID":"msg_user","providerID":"fixture","modelID":"model","agent":"build","role":"assistant","finish":"tool-calls","variant":"low","cost":0.0123,"time":{"created":10,"completed":20},"path":{"cwd":` + quotedDirectory + `,"root":` + quotedRoot + `},"tokens":{"total":13,"input":9,"output":4,"reasoning":0,"cache":{"read":0,"write":0}}},"parts":[{"id":"prt_step_1","messageID":"msg_tool","sessionID":"ses_fixture","type":"step-start"},{"id":"prt_tool","messageID":"msg_tool","sessionID":"ses_fixture","type":"tool","callID":"provider-call-1","tool":"engorch_source_list","state":{"status":"completed","input":{"after":"","limit":1},"output":` + strconv.Quote(string(responseBytes)) + `,"title":"","metadata":{"truncated":false},"time":{"start":12,"end":18},"attachments":[]}},{"id":"prt_step_2","messageID":"msg_tool","sessionID":"ses_fixture","type":"step-finish","reason":"tool-calls","tokens":{"total":13,"input":9,"output":4,"reasoning":0,"cache":{"read":0,"write":0}}}]}`
	finalParts := `[{"id":"prt_step_3","messageID":"msg_final","sessionID":"ses_fixture","type":"step-start"},{"id":"prt_text","messageID":"msg_final","sessionID":"ses_fixture","type":"text","text":"tool result accepted","time":{"start":22,"end":29}},{"id":"prt_step_4","messageID":"msg_final","sessionID":"ses_fixture","type":"step-finish","reason":"stop","tokens":{"total":21,"input":18,"output":3,"reasoning":0,"cache":{"read":0,"write":0}}}`
	responseParts := finalParts + `]`
	if patch != "" {
		finalParts += `,{"id":"prt_patch","messageID":"msg_final","sessionID":"ses_fixture","type":"patch","hash":"` + strings.Repeat("a", 40) + `","files":` + patch + `}`
	}
	finalParts += `]`
	final := `{"info":{"id":"msg_final","sessionID":"ses_fixture","parentID":"msg_user","providerID":"fixture","modelID":"model","agent":"build","role":"assistant","finish":"stop","variant":"low","cost":0.0345,"time":{"created":21,"completed":30},"path":{"cwd":` + quotedDirectory + `,"root":` + quotedRoot + `},"tokens":{"total":21,"input":18,"output":3,"reasoning":0,"cache":{"read":0,"write":0}}},"parts":` + finalParts + `}`
	responseFinal := `{"info":{"id":"msg_final","sessionID":"ses_fixture","parentID":"msg_user","providerID":"fixture","modelID":"model","agent":"build","role":"assistant","finish":"stop","variant":"low","cost":0.0345,"time":{"created":21,"completed":30},"path":{"cwd":` + quotedDirectory + `,"root":` + quotedRoot + `},"tokens":{"total":21,"input":18,"output":3,"reasoning":0,"cache":{"read":0,"write":0}}},"parts":` + responseParts + `}`
	transcript := "[" + user + "," + intermediate + "," + final + "]"
	open, err := contextbroker.Inspect(fixture.paths.Broker)
	if err != nil {
		t.Fatal(err)
	}
	openStateID, err := canonical.Hash("harness.opencode-tool-turn-broker-state.v1", open)
	if err != nil {
		t.Fatal(err)
	}
	appendUnchecked(t, fixture.paths.Dispatch, "opencode.sync-tool-intent", fixture.bound.SealExpected.Dispatch)
	appendUnchecked(t, fixture.paths.Dispatch, "opencode.sync-tool-observed", struct {
		Response        string `json:"response,omitempty"`
		Transcript      string `json:"transcript"`
		BrokerBindingID string `json:"broker_binding_id"`
		BrokerCatalogID string `json:"broker_catalog_id"`
		BrokerStateID   string `json:"broker_state_id"`
	}{responseFinal, transcript, fixture.bound.ContextBindingID, fixture.bound.ContextCatalogID, openStateID})
	var offline *opencode.Client
	observation, err := offline.RecoverSynchronousToolTurn(context.Background(), fixture.paths.Dispatch, fixture.paths.Broker, fixture.bound.SealExpected.Dispatch)
	if err != nil {
		t.Fatal(err)
	}
	observationID, _ := canonical.Hash("harness.opencode-tool-turn-seal-observation.v1", observation)
	sealIntent := opencode.SynchronousToolTurnSealIntent{Version: 1, Expected: fixture.bound.SealExpected, ObservationSHA256: observationID, TranscriptSHA256: observation.TranscriptSHA256, OpenBrokerStateID: observation.BrokerStateID, ToolsConfigurationSHA256: fixture.bound.SealExpected.Tools.SHA256, MCPStatusSHA256: strings.Repeat("d", 64), ProcessID: 123, OpenCodeEndpoint: "http://127.0.0.1:43124", MCPEndpoint: fixture.bound.SealExpected.Tools.Endpoint}
	sealIntent.ID, err = canonical.Hash("harness.opencode-tool-turn-seal-intent.v1", struct {
		Version                  int                                      `json:"version"`
		ID                       string                                   `json:"id"`
		Expected                 opencode.SynchronousToolTurnSealExpected `json:"expected"`
		ObservationSHA256        string                                   `json:"observation_sha256"`
		TranscriptSHA256         string                                   `json:"transcript_sha256"`
		OpenBrokerStateID        string                                   `json:"open_broker_state_id"`
		ToolsConfigurationSHA256 string                                   `json:"tools_configuration_sha256"`
		MCPStatusSHA256          string                                   `json:"mcp_status_sha256"`
		ProcessID                int                                      `json:"process_id"`
		OpenCodeEndpoint         string                                   `json:"opencode_endpoint"`
		MCPEndpoint              string                                   `json:"mcp_endpoint"`
	}{sealIntent.Version, "", sealIntent.Expected, sealIntent.ObservationSHA256, sealIntent.TranscriptSHA256, sealIntent.OpenBrokerStateID, sealIntent.ToolsConfigurationSHA256, sealIntent.MCPStatusSHA256, sealIntent.ProcessID, sealIntent.OpenCodeEndpoint, sealIntent.MCPEndpoint})
	if err != nil {
		t.Fatal(err)
	}
	appendUnchecked(t, fixture.paths.Seal, "opencode.tool-turn-seal-intent", sealIntent)
	if err := fixture.broker.Close(); err != nil {
		t.Fatal(err)
	}
	closed, err := contextbroker.Inspect(fixture.paths.Broker)
	if err != nil {
		t.Fatal(err)
	}
	closedID, _ := canonical.Hash("harness.opencode-tool-turn-sealed-broker.v1", closed)
	terminal := opencode.ToolTurnTerminalReceipt{Version: 1, IntentID: sealIntent.ID, ObservationSHA256: sealIntent.ObservationSHA256, TranscriptSHA256: sealIntent.TranscriptSHA256, OpenBrokerStateID: sealIntent.OpenBrokerStateID, ClosedBrokerStateID: closedID, ToolsConfigurationSHA256: sealIntent.ToolsConfigurationSHA256, MCPStatusSHA256: sealIntent.MCPStatusSHA256, ProcessID: sealIntent.ProcessID, MCPHandlersStopped: true, RootProcessReaped: true, BrokerClosed: true}
	appendUnchecked(t, fixture.paths.Seal, "opencode.tool-turn-sealed", terminal)
}
