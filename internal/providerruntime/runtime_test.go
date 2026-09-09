package providerruntime

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/providercredential"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/providertransport"
)

func TestBuildRequestCoversFiniteDirectCodecsWithoutTools(t *testing.T) {
	invocation := Invocation{Version: 1, System: "Return one JSON object.", Prompt: "Classify this input.", Output: OutputContract{Kind: "json"}}
	for _, adapter := range []string{providergateway.OpenAIChatCompletionsAdapter, providergateway.OpenAIResponsesAdapter, providergateway.AnthropicMessagesAdapter} {
		t.Run(adapter, func(t *testing.T) {
			binding, expectation := requestFixture(t, adapter)
			raw, err := BuildRequest(binding, expectation, invocation)
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := providergateway.ValidateAdapterRequest(raw, binding, expectation)
			if err != nil || metadata.Model != binding.Model.Model || metadata.ToolCount != 0 || metadata.MaxOutputTokens != expectation.MaxOutputTokens {
				t.Fatal("direct request differs from its adapter contract", metadata, err)
			}
			if strings.Contains(string(raw), `"tools"`) || strings.Contains(string(raw), `"tool_choice"`) {
				t.Fatal("direct structured inference exposed tool-loop fields", string(raw))
			}
		})
	}
}

func TestBuildRequestSelectsNonStreamingJSONWithoutChangingDirectBoundary(t *testing.T) {
	invocation := Invocation{Version: 1, System: "Return one JSON object.", Prompt: "Classify this input.", Output: OutputContract{Kind: "json"}}
	for _, adapter := range []string{providergateway.OpenAIChatCompletionsAdapter, providergateway.OpenAIResponsesAdapter, providergateway.AnthropicMessagesAdapter} {
		t.Run(adapter, func(t *testing.T) {
			binding, expectation := requestFixture(t, adapter)
			expectation.ResponseFraming = providergateway.ResponseFramingJSON
			binding.Model.Capabilities.SupportedResponseFramings = []providergateway.ResponseFraming{providergateway.ResponseFramingJSON}
			var contractErr error
			binding.ModelID, contractErr = binding.Model.ID()
			if contractErr != nil {
				t.Fatal(contractErr)
			}
			raw, err := BuildRequest(binding, expectation, invocation)
			if err != nil || !strings.Contains(string(raw), `"stream":false`) || strings.Contains(string(raw), `"stream_options"`) {
				t.Fatal("direct finite request framing differs", string(raw), err)
			}
			if _, err := providergateway.ValidateAdapterRequest(raw, binding, expectation); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBuildResponsesNativeStructuredControls(t *testing.T) {
	schema := json.RawMessage(`{"additionalProperties":false,"properties":{"answer":{"type":"number"}},"required":["answer"],"type":"object"}`)
	for _, test := range []struct {
		mode   providergateway.StructuredOutputMode
		format string
	}{
		{providergateway.StructuredOutputJSONOnly, "json_object"},
		{providergateway.StructuredOutputStrictSchema, "json_schema"},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			adapterCapabilities, err := canonical.Bytes(providergateway.ResponsesModelCapabilities{Version: 1, SystemRoles: []string{"developer"}, TextFormats: []string{"json_object", "json_schema", "plain"}, KnownExtensions: []string{}})
			if err != nil {
				t.Fatal(err)
			}
			endpoint := providergateway.EndpointContract{Version: 2, Provider: "fixture", URL: "https://api.example.test/v1/responses", AdapterID: providergateway.OpenAIResponsesAdapter, Auth: &providergateway.AuthContract{Scheme: "bearer", CredentialRef: "fixture-key"}}
			model := providergateway.ModelContract{Version: 2, Provider: "fixture", Model: "qualified-model", AdapterID: providergateway.OpenAIResponsesAdapter, Capabilities: &providergateway.ModelCapabilities{OutputCap: true, CompleteUsage: true, StructuredOutputModes: []providergateway.StructuredOutputMode{providergateway.StructuredOutputJSONOnly, providergateway.StructuredOutputStrictSchema}}, AdapterCapabilities: adapterCapabilities, ContextWindowTokens: 128, MaxCalls: 1, MaxRequestBytes: 4096, MaxResponseBytes: 4096, MaxOutputTokens: 16}
			endpointID, err := endpoint.ID()
			if err != nil {
				t.Fatal(err)
			}
			modelID, err := model.ID()
			if err != nil {
				t.Fatal(err)
			}
			binding := providergateway.Binding{Version: 1, AccessPolicyID: strings.Repeat("a", 64), AccessInvocationID: strings.Repeat("b", 64), RouteID: strings.Repeat("c", 64), ReservedTokens: 144, EndpointID: endpointID, ModelID: modelID, Endpoint: endpoint, Model: model}
			controls := providergateway.ResponsesRequestExpectation{MaxOutputTokens: 16, StateMode: "full-input-stateless", SystemRole: "developer", TextFormat: test.format}
			required := &providergateway.RequiredCapabilities{StructuredOutput: test.mode}
			if test.mode == providergateway.StructuredOutputStrictSchema {
				controls.TextSchemaName, controls.TextSchema = "answer", schema
				required.SchemaName, required.Schema = "answer", schema
			}
			controlBytes, err := json.Marshal(controls)
			if err != nil {
				t.Fatal(err)
			}
			expectation := providergateway.AdapterRequestExpectation{MaxBytes: 4096, MaxOutputTokens: 16, Controls: controlBytes, RequiredCapabilities: required}
			raw, err := BuildRequest(binding, expectation, Invocation{Version: 1, System: "Return JSON.", Prompt: "Answer.", Output: OutputContract{Kind: "json"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := providergateway.ValidateAdapterRequest(raw, binding, expectation); err != nil {
				t.Fatal(err)
			}
			if test.mode == providergateway.StructuredOutputStrictSchema && !strings.Contains(string(raw), `"strict":true`) {
				t.Fatal("strict Responses schema control missing", string(raw))
			}
		})
	}
}

func TestRunUsesProductionTransportOnceAndRecoversExactResult(t *testing.T) {
	var calls atomic.Int32
	server := newRuntimeTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("authorization") != "Bearer fixture-secret" {
			t.Error("transport request identity mismatch")
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write(chatJSONStream(`{"answer":"ok"}`))
	})
	config, invocation := runtimeFixture(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := Run(ctx, config, invocation)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(ctx, config, invocation)
	if err != nil || calls.Load() != 1 || first.Text != `{"answer":"ok"}` || !json.Valid([]byte(first.JSON)) || !reflect.DeepEqual(first, second) {
		t.Fatal("completed direct result did not recover exactly", calls.Load(), first, second, err)
	}
	state, err := Inspect(config.JournalPath)
	if err != nil || state.Binding == nil || state.Result == nil {
		t.Fatal("direct invocation/result were not durable", state, err)
	}
}

func TestRunFiniteJSONPersistsFramingAndRecoversOfflineWithoutResend(t *testing.T) {
	var calls atomic.Int32
	server := newRuntimeTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("accept") != "application/json" || !strings.Contains(string(body), `"stream":false`) || strings.Contains(string(body), `"stream_options"`) {
			t.Error("finite direct request framing mismatch")
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write(chatJSONCompletion(`{"answer":"ok"}`))
	})
	config, invocation := runtimeFixtureFraming(t, server, providergateway.StructuredOutputTextParseRequired, providergateway.ResponseFramingJSON)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := Run(ctx, config, invocation)
	if err != nil || calls.Load() != 1 || first.Text != `{"answer":"ok"}` {
		t.Fatal("finite direct execution failed", calls.Load(), first, err)
	}
	state, err := Inspect(config.JournalPath)
	if err != nil || state.Binding == nil || state.Binding.Expectation.ResponseFraming != providergateway.ResponseFramingJSON || state.Binding.Expectation.gatewayExpectation().ResponseFraming != providergateway.ResponseFramingJSON {
		t.Fatal("durable runtime binding lost finite framing", state, err)
	}
	server.Close()
	second, err := Run(ctx, config, invocation)
	if err != nil || calls.Load() != 1 || !reflect.DeepEqual(first, second) {
		t.Fatal("offline recovery resent or changed finite result", calls.Load(), first, second, err)
	}
}

func TestRunNeverResendsGatewayCompletionMissingRuntimePayload(t *testing.T) {
	var calls atomic.Int32
	server := newRuntimeTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write(chatJSONStream(`{"answer":"ok"}`))
	})
	config, invocation := runtimeFixture(t, server)
	body, err := BuildRequest(config.Binding, config.Expectation, invocation)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := bindingFor(config, invocation, body)
	if err != nil {
		t.Fatal("could not create crash-window binding", err)
	}
	if err := validateBindingRecord(binding); err != nil {
		t.Fatal("could not validate crash-window binding", err)
	}
	rawBinding, _ := canonical.Bytes(binding)
	var roundTrip InvocationRecord
	if err := canonical.Decode(rawBinding, &roundTrip); err != nil {
		t.Fatalf("round-trip decode failed: %v\n%s", err, rawBinding)
	}
	if err := validateBindingRecord(roundTrip); err != nil {
		t.Fatalf("round-trip binding invalid: %v\n%s\n%#v", err, rawBinding, roundTrip)
	}
	if err := appendEvent(config.JournalPath, boundEvent, binding); err != nil {
		t.Fatal("could not journal crash-window binding", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := config.Transport.Execute(ctx, providertransport.Request{AccessJournalPath: config.AccessJournalPath, GatewayJournalPath: config.GatewayJournalPath, Policy: config.Policy, Intent: config.Intent, Binding: config.Binding, Lease: config.Credential, Body: body, Expectation: config.Expectation}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, config, invocation); err != ErrUnresolved || calls.Load() != 1 {
		t.Fatal("missing crash-window content was replayed or misclassified", calls.Load(), err)
	}
}

func TestRunNativeChatStructuredModesThroughProductionTransport(t *testing.T) {
	for _, mode := range []providergateway.StructuredOutputMode{providergateway.StructuredOutputJSONOnly, providergateway.StructuredOutputStrictSchema} {
		t.Run(string(mode), func(t *testing.T) {
			var calls atomic.Int32
			server := newRuntimeTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), `"response_format"`) {
					t.Error("native Chat response_format missing")
				}
				w.Header().Set("content-type", "text/event-stream")
				_, _ = w.Write(chatJSONStream(`{"answer":"ok"}`))
			})
			config, invocation := runtimeFixtureMode(t, server, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := Run(ctx, config, invocation)
			if err != nil || result.JSON != `{"answer":"ok"}` || calls.Load() != 1 {
				t.Fatal(result, calls.Load(), err)
			}
		})
	}
}

func TestRunInvalidStrictOutputStaysUnresolvedAndIsNeverResent(t *testing.T) {
	var calls atomic.Int32
	server := newRuntimeTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write(chatJSONStream(`{"answer":7}`))
	})
	config, invocation := runtimeFixtureMode(t, server, providergateway.StructuredOutputStrictSchema)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Run(ctx, config, invocation); err != ErrUnresolved {
		t.Fatal("invalid strict output was not unresolved", err)
	}
	if _, err := Run(ctx, config, invocation); err != ErrUnresolved || calls.Load() != 1 {
		t.Fatal("invalid strict output was resent", calls.Load(), err)
	}
	gateway, err := providergateway.Inspect(config.GatewayJournalPath)
	if err != nil || gateway.Pending == nil || gateway.Finished {
		t.Fatal("invalid strict output did not retain unknown provider call", gateway, err)
	}
}

func TestResultRejectsContentSubstitutionAndJSONAmbiguity(t *testing.T) {
	server := newRuntimeTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write(chatJSONStream(`{"answer":"ok"}`))
	})
	config, invocation := runtimeFixture(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := Run(ctx, config, invocation)
	if err != nil {
		t.Fatal(err)
	}
	state, err := Inspect(config.JournalPath)
	if err != nil || state.Binding == nil {
		t.Fatal(err)
	}
	altered := result
	altered.Text = `{"answer":1.25}`
	altered.JSON, altered.JSONSHA256, err = validateOutput(invocation.Output, altered.Text)
	if err != nil {
		t.Fatal("fractional JSON was rejected", err)
	}
	if err := validateResult(*state.Binding, altered); err != ErrUnresolved {
		t.Fatal("altered exact JSON was not rejected against the original semantic receipt", err)
	}
	alteredPath := filepath.Join(t.TempDir(), "altered.jsonl")
	if err := appendEvent(alteredPath, boundEvent, *state.Binding); err != nil {
		t.Fatal(err)
	}
	if err := appendEvent(alteredPath, resultEvent, altered); err == nil {
		t.Fatal("a newly hashed substituted result event was admitted")
	}
	if _, _, err := validateOutput(invocation.Output, `{"answer":1,"answer":2}`); err == nil {
		t.Fatal("duplicate JSON keys were admitted")
	}
	if _, _, err := validateOutput(invocation.Output, `{"answer":1} {"extra":2}`); err == nil {
		t.Fatal("trailing JSON was admitted")
	}
}

func TestExpectationJournalPreservesFractionalControlBytes(t *testing.T) {
	expectation := providergateway.AdapterRequestExpectation{
		MaxBytes: 4096, MaxOutputTokens: 16, Controls: json.RawMessage(`{"temperature":0.50}`),
		RequiredCapabilities: &providergateway.RequiredCapabilities{StructuredOutput: providergateway.StructuredOutputTextParseRequired}, ResponseFraming: providergateway.ResponseFramingJSON,
	}
	record := expectationRecord(expectation)
	raw, err := canonical.Bytes(record)
	if err != nil {
		t.Fatal(err)
	}
	var recovered requestExpectationRecord
	if err := canonical.Decode(raw, &recovered); err != nil {
		t.Fatal(err)
	}
	if string(recovered.Controls) != string(expectation.Controls) || string(recovered.gatewayExpectation().Controls) != string(expectation.Controls) || recovered.ResponseFraming != providergateway.ResponseFramingJSON || recovered.gatewayExpectation().ResponseFraming != providergateway.ResponseFramingJSON {
		t.Fatal("fractional provider control lexemes changed across journal encoding")
	}
}

func TestInvocationRecordFreezesMutableConfiguration(t *testing.T) {
	server := newRuntimeTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write(chatJSONStream(`{"answer":"ok"}`))
	})
	config, invocation := runtimeFixtureMode(t, server, providergateway.StructuredOutputStrictSchema)
	body, err := BuildRequest(config.Binding, config.Expectation, invocation)
	if err != nil {
		t.Fatal(err)
	}
	record, err := bindingFor(config, invocation, body)
	if err != nil {
		t.Fatal(err)
	}
	config.Binding.Model.Capabilities.StructuredOutputModes[0] = providergateway.StructuredOutputJSONOnly
	config.Expectation.Controls[0] = '['
	config.Expectation.RequiredCapabilities.Schema[0] = '['
	if err := validateBindingRecord(record); err != nil {
		t.Fatal("frozen invocation record followed caller mutation", err)
	}
	if record.GatewayBinding.Model.Capabilities.StructuredOutputModes[0] != providergateway.StructuredOutputStrictSchema || record.Expectation.Controls[0] == '[' || record.Expectation.RequiredCapabilities.Schema[0] == '[' {
		t.Fatal("invocation record aliases mutable caller configuration")
	}
}

func requestFixture(t *testing.T, adapter string) (providergateway.Binding, providergateway.AdapterRequestExpectation) {
	t.Helper()
	capabilities := json.RawMessage("{}")
	controls := json.RawMessage("{}")
	modelCapabilities := &providergateway.ModelCapabilities{OutputCap: true, CompleteUsage: true}
	switch adapter {
	case providergateway.OpenAIResponsesAdapter:
		capabilities, _ = canonical.Bytes(providergateway.ResponsesModelCapabilities{Version: 1, SystemRoles: []string{"developer"}, TextFormats: []string{"plain"}, KnownExtensions: []string{}})
		controls, _ = json.Marshal(providergateway.ResponsesRequestExpectation{MaxOutputTokens: 16, StateMode: "full-input-stateless", SystemRole: "developer"})
	case providergateway.AnthropicMessagesAdapter:
		capabilities, _ = canonical.Bytes(providergateway.AnthropicMessagesModelCapabilities{Version: 1, SystemBlocks: true, ThinkingModes: []string{}, EffortValues: []string{}, ToolChoiceModes: []string{}, SamplingParameters: []string{}, RequireCacheUsageBreakdown: true})
		controls, _ = json.Marshal(providergateway.AnthropicMessagesRequestExpectation{MaxOutputTokens: 16, RequireSystem: true})
	}
	endpoint := providergateway.EndpointContract{Version: 2, Provider: "fixture", URL: "https://api.example.test" + adapterPath(adapter), AdapterID: adapter, Auth: &providergateway.AuthContract{Scheme: "bearer", CredentialRef: "fixture-key"}}
	model := providergateway.ModelContract{Version: 2, Provider: "fixture", Model: "qualified-model", AdapterID: adapter, Capabilities: modelCapabilities, AdapterCapabilities: capabilities, ContextWindowTokens: 128, MaxCalls: 1, MaxRequestBytes: 4096, MaxResponseBytes: 4096, MaxOutputTokens: 16}
	endpointID, err := endpoint.ID()
	if err != nil {
		t.Fatal(err)
	}
	modelID, err := model.ID()
	if err != nil {
		t.Fatal(err)
	}
	binding := providergateway.Binding{Version: 1, AccessPolicyID: strings.Repeat("a", 64), AccessInvocationID: strings.Repeat("b", 64), RouteID: strings.Repeat("c", 64), ReservedTokens: 144, EndpointID: endpointID, ModelID: modelID, Endpoint: endpoint, Model: model}
	expectation := providergateway.AdapterRequestExpectation{MaxBytes: 4096, MaxOutputTokens: 16, Controls: controls, RequiredCapabilities: &providergateway.RequiredCapabilities{StructuredOutput: providergateway.StructuredOutputTextParseRequired}}
	return binding, expectation
}

func runtimeFixture(t *testing.T, server *httptest.Server) (Config, Invocation) {
	return runtimeFixtureMode(t, server, providergateway.StructuredOutputTextParseRequired)
}

func runtimeFixtureMode(t *testing.T, server *httptest.Server, mode providergateway.StructuredOutputMode) (Config, Invocation) {
	return runtimeFixtureFraming(t, server, mode, "")
}

func runtimeFixtureFraming(t *testing.T, server *httptest.Server, mode providergateway.StructuredOutputMode, framing providergateway.ResponseFraming) (Config, Invocation) {
	t.Helper()
	root := t.TempDir()
	invocation := Invocation{Version: 1, System: "Return exactly one JSON object with answer.", Prompt: "Produce the fixture answer.", Output: OutputContract{Kind: "json"}}
	chatControls := providergateway.ChatRequestExpectation{}
	required := &providergateway.RequiredCapabilities{StructuredOutput: mode}
	structuredModes := []providergateway.StructuredOutputMode(nil)
	if mode == providergateway.StructuredOutputJSONOnly {
		chatControls.ResponseFormat = "json_object"
		structuredModes = []providergateway.StructuredOutputMode{mode}
	}
	if mode == providergateway.StructuredOutputStrictSchema {
		schema := json.RawMessage(`{"additionalProperties":false,"properties":{"answer":{"type":"string"}},"required":["answer"],"type":"object"}`)
		chatControls.ResponseFormat, chatControls.SchemaName, chatControls.Schema = "json_schema", "answer", schema
		required.SchemaName, required.Schema = "answer", schema
		structuredModes = []providergateway.StructuredOutputMode{mode}
	}
	controls, err := json.Marshal(chatControls)
	if err != nil {
		t.Fatal(err)
	}
	expectation := providergateway.AdapterRequestExpectation{MaxBytes: 4096, MaxOutputTokens: 16, Controls: controls, RequiredCapabilities: required, ResponseFraming: framing}
	inputHash, err := invocation.InputHash(expectation)
	if err != nil {
		t.Fatal(err)
	}
	profile := access.Profile{Version: 1, Name: "fixture-access", Kind: "subscription", Runtime: "provider-api", Provider: "fixture", CredentialRef: "fixture-key", RepositoryClasses: []access.Class{access.Public}}
	profileID, err := profile.ID()
	if err != nil {
		t.Fatal(err)
	}
	route := access.Route{Version: 1, Role: "explorer", Runtime: "provider-api", Provider: "fixture", Model: "qualified-model", Effort: "low", AccessID: profileID, Permission: "read-only"}
	policy := access.Policy{Version: 1, RunID: strings.Repeat("1", 64), Class: access.Public, Limits: access.Limits{Tokens: 144, Concurrency: 1}, Routes: []access.Route{route}, Profiles: []access.Profile{profile}}
	policyID, err := policy.ID()
	if err != nil {
		t.Fatal(err)
	}
	intent := access.Intent{Attempt: 1, PolicyID: policyID, InputHash: inputHash, Route: route, Reservation: access.Reservation{Tokens: 144, BillingMode: "subscription"}}
	intent.Reservation.InvocationID, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	accessPath := filepath.Join(root, "access.jsonl")
	if err := access.ReserveDurable(accessPath, policy, intent); err != nil {
		t.Fatal(err)
	}
	endpoint := providergateway.EndpointContract{Version: 2, Provider: "fixture", URL: server.URL + "/v1/chat/completions", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Auth: &providergateway.AuthContract{Scheme: "bearer", CredentialRef: "fixture-key"}}
	responseFramings := []providergateway.ResponseFraming(nil)
	if framing == providergateway.ResponseFramingJSON {
		responseFramings = []providergateway.ResponseFraming{providergateway.ResponseFramingJSON}
	}
	model := providergateway.ModelContract{Version: 2, Provider: "fixture", Model: "qualified-model", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Capabilities: &providergateway.ModelCapabilities{OutputCap: true, CompleteUsage: true, StructuredOutputModes: structuredModes, SupportedResponseFramings: responseFramings}, AdapterCapabilities: json.RawMessage("{}"), ContextWindowTokens: 128, MaxCalls: 1, MaxRequestBytes: 4096, MaxResponseBytes: 4096, MaxOutputTokens: 16}
	gatewayPath := filepath.Join(root, "gateway.jsonl")
	binding, err := providergateway.Bind(gatewayPath, accessPath, policy, intent, endpoint, model)
	if err != nil {
		t.Fatal(err)
	}
	source, err := providercredential.NewEnvironmentSource(map[string]string{"fixture-key": "FIXTURE_KEY"}, func(name string) (string, bool) { return "fixture-secret", name == "FIXTURE_KEY" })
	if err != nil {
		t.Fatal(err)
	}
	credential, err := providercredential.Resolve(context.Background(), accessPath, policy, intent, endpoint, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = credential.Close() })
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, err := providertransport.NewClient(providertransport.ClientOptions{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	return Config{JournalPath: filepath.Join(root, "runtime.jsonl"), AccessJournalPath: accessPath, GatewayJournalPath: gatewayPath, Policy: policy, Intent: intent, Binding: binding, Expectation: expectation, Credential: credential, Transport: client}, invocation
}

func adapterPath(adapter string) string {
	switch adapter {
	case providergateway.OpenAIResponsesAdapter:
		return "/v1/responses"
	case providergateway.AnthropicMessagesAdapter:
		return "/v1/messages"
	default:
		return "/v1/chat/completions"
	}
}

func newRuntimeTLSServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = false
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func chatJSONStream(output string) []byte {
	encoded, _ := json.Marshal(output)
	return []byte("data: {\"id\":\"chatcmpl-direct\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"qualified-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":" + string(encoded) + "},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-direct\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"qualified-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-direct\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"qualified-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n" +
		"data: [DONE]\n\n")
}

func chatJSONCompletion(output string) []byte {
	encoded, _ := json.Marshal(output)
	return []byte(`{"id":"chatcmpl-direct","object":"chat.completion","created":1,"model":"qualified-model","choices":[{"index":0,"message":{"role":"assistant","content":` + string(encoded) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
}
