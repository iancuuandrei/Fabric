package providergateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
)

func chatV2Model(priced bool) ModelContract {
	model := ModelContract{Version: 2, Provider: "openai", Model: "deployment/opaque-model", AdapterID: OpenAIChatCompletionsAdapter, Capabilities: &ModelCapabilities{Tools: true, OutputCap: true, CompleteUsage: true}, AdapterCapabilities: json.RawMessage("{}"), ContextWindowTokens: 100, MaxCalls: 3, MaxRequestBytes: 4096, MaxResponseBytes: 8192, MaxOutputTokens: 10}
	if priced {
		model.Pricing = &PricingPolicy{Currency: "USD", Unit: "micro_usd_per_million_tokens", MaxInputMicroUSDPerMillion: 1001, MaxOutputMicroUSDPerMillion: 2001}
	}
	return model
}

func v2Endpoint() EndpointContract {
	return EndpointContract{Version: 2, Provider: "openai", URL: "https://gateway.example.test/custom/deployments/name/chat?api-version=2026-09-01", AdapterID: OpenAIChatCompletionsAdapter, Auth: &AuthContract{Scheme: "api-key-header", CredentialRef: "team-key", HeaderName: "api-key"}, PublicHeaders: map[string]string{"anthropic-version": "2023-06-01", "user-agent": "engorch-fixture"}, SessionHeader: "x-engorch-invocation"}
}

func TestV2EndpointBindsArbitraryPathPublicMetadataAndAuthentication(t *testing.T) {
	endpoint := v2Endpoint()
	first, err := endpoint.ID()
	if err != nil {
		t.Fatal(err)
	}
	changed := endpoint
	changed.PublicHeaders = map[string]string{"anthropic-version": "2023-06-01", "user-agent": "other"}
	second, err := changed.ID()
	if err != nil || first == second {
		t.Fatal("public metadata was not identity-bearing", err)
	}

	for name, mutate := range map[string]func(*EndpointContract){
		"credential query":      func(e *EndpointContract) { e.URL = "https://gateway.example.test/custom?api-key=secret" },
		"noncanonical query":    func(e *EndpointContract) { e.URL = "https://gateway.example.test/custom?z=1&a=2" },
		"auth header collision": func(e *EndpointContract) { e.PublicHeaders["api-key"] = "public" },
		"session collision":     func(e *EndpointContract) { e.PublicHeaders[e.SessionHeader] = "caller-value" },
		"newline":               func(e *EndpointContract) { e.PublicHeaders["x-public"] = "ok\r\nbad" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := endpoint
			candidate.PublicHeaders = map[string]string{}
			for k, v := range endpoint.PublicHeaders {
				candidate.PublicHeaders[k] = v
			}
			mutate(&candidate)
			if _, err := candidate.ID(); err == nil {
				t.Fatal("unsafe endpoint metadata admitted")
			}
		})
	}
}

func TestV1ContractHashesRemainByteCompatible(t *testing.T) {
	endpoint := EndpointContract{Version: 1, Provider: "openai", URL: "https://api.example.test/v1/chat/completions", Protocol: OpenAIChatCompletionsAdapter}
	wantEndpoint, err := canonical.Hash("harness.provider-endpoint.v1", struct {
		Version  int    `json:"version"`
		Provider string `json:"provider"`
		URL      string `json:"url"`
		Protocol string `json:"protocol"`
	}{1, endpoint.Provider, endpoint.URL, endpoint.Protocol})
	if err != nil {
		t.Fatal(err)
	}
	gotEndpoint, err := endpoint.ID()
	if err != nil || gotEndpoint != wantEndpoint {
		t.Fatal("v1 endpoint identity changed", gotEndpoint, wantEndpoint, err)
	}

	model := ModelContract{Version: 1, Provider: "openai", Model: "old-model", Protocol: OpenAIChatCompletionsAdapter, MaxCalls: 2, MaxRequestBytes: 4096, MaxResponseBytes: 8192, MaxOutputTokens: 100}
	wantModel, err := canonical.Hash("harness.provider-model.v1", struct {
		Version          int    `json:"version"`
		Provider         string `json:"provider"`
		Model            string `json:"model"`
		Protocol         string `json:"protocol"`
		MaxCalls         int    `json:"max_calls"`
		MaxRequestBytes  int64  `json:"max_request_bytes"`
		MaxResponseBytes int64  `json:"max_response_bytes"`
		MaxOutputTokens  int64  `json:"max_output_tokens"`
	}{1, model.Provider, model.Model, model.Protocol, model.MaxCalls, model.MaxRequestBytes, model.MaxResponseBytes, model.MaxOutputTokens})
	if err != nil {
		t.Fatal(err)
	}
	gotModel, err := model.ID()
	if err != nil || gotModel != wantModel {
		t.Fatal("v1 model identity changed", gotModel, wantModel, err)
	}
}

func TestConservativeReservationRoundsEachAllowedCall(t *testing.T) {
	tokens, cost, err := chatV2Model(true).ConservativeReservation()
	if err != nil || tokens != 330 || cost == nil || *cost != 3 {
		t.Fatal("reservation did not conservatively round each call", tokens, cost, err)
	}
}

func TestObservedModelAliasesAreExactHashBoundV2Policy(t *testing.T) {
	model := chatV2Model(true)
	baseID, err := model.ID()
	if err != nil {
		t.Fatal(err)
	}
	model.ObservedModelAliases = []string{"deployment-2026-08-31", "deployment-2026-09-08"}
	aliasID, err := model.ID()
	if err != nil || aliasID == baseID {
		t.Fatal("aliases were not model-identity bearing", err)
	}
	if !AcceptsObservedModel(model, model.Model) || !AcceptsObservedModel(model, "deployment-2026-09-08") || AcceptsObservedModel(model, "Deployment-2026-09-08") || AcceptsObservedModel(model, "foreign") {
		t.Fatal("observed model acceptance was not exact")
	}
	for _, aliases := range [][]string{{"z", "a"}, {"same", "same"}, {model.Model}} {
		candidate := model
		candidate.ObservedModelAliases = aliases
		if _, err := candidate.ID(); err == nil {
			t.Fatal("ambiguous observed-model aliases admitted", aliases)
		}
	}
}

func newV2BindingFixture(t *testing.T, kind string) gatewayFixture {
	t.Helper()
	root := t.TempDir()
	profile := access.Profile{Version: 1, Name: "provider-key", Kind: kind, Runtime: "controller-gateway", Provider: "openai", CredentialRef: "team-key", RepositoryClasses: []access.Class{access.Private}}
	profileID, err := profile.ID()
	if err != nil {
		t.Fatal(err)
	}
	route := access.Route{Version: 1, Role: "planner", Runtime: profile.Runtime, Provider: profile.Provider, Model: "deployment/opaque-model", Effort: "high", AccessID: profileID, Permission: "read-only"}
	var cost *int64
	if kind == "api" {
		value := int64(3)
		cost = &value
	}
	policy := access.Policy{Version: 1, RunID: strings.Repeat("a", 64), Class: access.Private, Limits: access.Limits{Tokens: 330, CostMicroUSD: cost, Concurrency: 1}, Routes: []access.Route{route}, Profiles: []access.Profile{profile}}
	policyID, err := policy.ID()
	if err != nil {
		t.Fatal(err)
	}
	intent := access.Intent{Attempt: 1, PolicyID: policyID, InputHash: strings.Repeat("b", 64), Route: route, Reservation: access.Reservation{Tokens: 330, CostMicroUSD: cost, BillingMode: kind}}
	intent.Reservation.InvocationID, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	accessPath := filepath.Join(root, "access.jsonl")
	if err := access.ReserveDurable(accessPath, policy, intent); err != nil {
		t.Fatal(err)
	}
	model := chatV2Model(kind == "api")
	path := filepath.Join(root, "provider.jsonl")
	binding, err := Bind(path, accessPath, policy, intent, v2Endpoint(), model)
	if err != nil {
		t.Fatal(err)
	}
	return gatewayFixture{accessPath: accessPath, path: path, policy: policy, intent: intent, endpoint: v2Endpoint(), model: model, binding: binding}
}

func beginV2(t *testing.T, f gatewayFixture, digest string, output int64) CallIntent {
	t.Helper()
	call, err := BeginWithExpectation(f.path, f.accessPath, f.policy, f.intent, f.binding, digest, 256, AdapterRequestExpectation{MaxBytes: f.model.MaxRequestBytes, MaxOutputTokens: output, Controls: json.RawMessage("{}")})
	if err != nil {
		t.Fatal(err)
	}
	return call
}

func TestV2BindingSupportsPricedAPIAndUnpricedSubscriptionKey(t *testing.T) {
	for _, kind := range []string{"api", "subscription"} {
		t.Run(kind, func(t *testing.T) {
			f := newV2BindingFixture(t, kind)
			if _, err := f.binding.ID(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdapterRequestPathIsLocalAndIndependentOfUpstreamURL(t *testing.T) {
	f := newV2BindingFixture(t, "api")
	got, err := AdapterRequestPath(f.binding)
	if err != nil || got != "/v1/chat/completions" {
		t.Fatal(got, err)
	}
	changed := f.binding
	changed.Endpoint.URL = "https://another.example.test/nonstandard/path?api-version=2026-09-01"
	changed.EndpointID, err = changed.Endpoint.ID()
	if err != nil {
		t.Fatal(err)
	}
	got, err = AdapterRequestPath(changed)
	if err != nil || got != "/v1/chat/completions" {
		t.Fatal("local adapter path followed upstream URL", got, err)
	}
}

func TestV2APIBindingRequiresPricingAndExactCredential(t *testing.T) {
	f := newV2BindingFixture(t, "api")
	unpriced := f.model
	unpriced.Pricing = nil
	if _, err := bindingFor(f.policy, f.intent, f.endpoint, unpriced); err == nil {
		t.Fatal("API route admitted without a pricing ceiling")
	}
	foreign := f.endpoint
	foreign.Auth = &AuthContract{Scheme: "api-key-header", CredentialRef: "other-key", HeaderName: "api-key"}
	if _, err := bindingFor(f.policy, f.intent, foreign, f.model); err == nil {
		t.Fatal("foreign credential reference admitted")
	}
}

func TestResponsesAdapterControlsPreserveFractionalSampling(t *testing.T) {
	temperature := json.Number("0.25")
	topP := json.Number("0.875")
	expected := ResponsesRequestExpectation{MaxOutputTokens: 10, StateMode: "full-input-stateless", SystemRole: "developer", Temperature: &temperature, TopP: &topP}
	raw, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeResponsesControls(raw)
	if err != nil || decoded.Temperature == nil || decoded.Temperature.String() != "0.25" || decoded.TopP == nil || decoded.TopP.String() != "0.875" {
		t.Fatal("fractional Responses controls were not preserved", string(raw), decoded, err)
	}
	changed := append([]byte(nil), raw...)
	changed = append(changed[:len(changed)-1], []byte(",\"unknown\":true}")...)
	if _, err := decodeResponsesControls(changed); err == nil {
		t.Fatal("unknown Responses control admitted")
	}
}

func TestResponsesAdapterPropagatesContentPartCompletionCapability(t *testing.T) {
	raw, err := os.ReadFile("testdata/m0g-live-system-02-response.raw")
	if err != nil {
		t.Fatal(err)
	}
	expected := AdapterResponseExpectation{MaxBytes: len(raw), MaxTotalTokens: 402}
	base := responsesCapabilities(true, true)
	base.TrailingCostPingV1 = true
	if _, err := decodeResponsesAdapterResponse(raw, responsesBinding(t, base), expected); err == nil {
		t.Fatal("captured profile admitted without bound capability")
	}
	base.ContentPartCompletesText = true
	metadata, err := decodeResponsesAdapterResponse(raw, responsesBinding(t, base), expected)
	if err != nil || metadata.ResponseID != "resp_6aa01e57f0a4468a1c374c43" || metadata.Usage.InputTokens+metadata.Usage.OutputTokens != 402 {
		t.Fatal("bound content-part completion profile rejected", metadata, err)
	}
}

func TestResponsesAdapterPropagatesFunctionCallDoneNameCapability(t *testing.T) {
	raw := strings.Replace(string(responsesFunctionFixture()), `,"item_id":"fc_fixture","output_index":1,"sequence_number":80`, `,"item_id":"fc_fixture","name":"lookup","output_index":1,"sequence_number":80`, 1)
	expected := AdapterResponseExpectation{MaxBytes: len(raw), MaxTotalTokens: 20}
	base := responsesCapabilities(true, true)
	if _, err := decodeResponsesAdapterResponse([]byte(raw), responsesBinding(t, base), expected); err == nil {
		t.Fatal("unconfigured function-call done name extension admitted")
	}
	base.FunctionCallDoneNameV1 = true
	metadata, err := decodeResponsesAdapterResponse([]byte(raw), responsesBinding(t, base), expected)
	if err != nil || metadata.Finish != "tool_calls" || metadata.Content == nil || len(metadata.Content.ToolCalls) != 1 {
		t.Fatal("bound function-call done name extension rejected", metadata, err)
	}
}

func TestRequestExpectationIdentityPreservesFractionalRawJSON(t *testing.T) {
	f := newV2BindingFixture(t, "api")
	first := AdapterRequestExpectation{MaxBytes: 4096, MaxOutputTokens: 10, Controls: json.RawMessage("{}"), Tools: []RequestTool{{Name: "read", Description: "Read.", Parameters: json.RawMessage(`{"type":"object","properties":{"ratio":{"minimum":0.5}}}`)}}}
	firstID, err := AdapterRequestExpectationID(f.binding, first)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.Tools = append([]RequestTool(nil), first.Tools...)
	second.Tools[0].Parameters = json.RawMessage(`{"type":"object","properties":{"ratio":{"minimum":0.50}}}`)
	secondID, err := AdapterRequestExpectationID(f.binding, second)
	if err != nil || firstID == secondID {
		t.Fatal("exact fractional schema bytes were not identity-bearing", err)
	}
	bad := first
	bad.Tools[0].Parameters = json.RawMessage(`{"type":"object","type":"array"}`)
	if _, err := AdapterRequestExpectationID(f.binding, bad); err == nil {
		t.Fatal("ambiguous tool schema admitted")
	}
}

func TestRequiredCapabilitiesFailClosedWithoutModelOrAdapterSubstitution(t *testing.T) {
	f := newV2BindingFixture(t, "api")
	available := &RequiredCapabilities{Tools: true, StructuredOutput: StructuredOutputTextParseRequired}
	if err := ValidateRequiredCapabilities(f.binding, available); err != nil {
		t.Fatal("available exact capabilities were rejected", err)
	}
	for name, required := range map[string]*RequiredCapabilities{
		"reasoning":     {Reasoning: true, StructuredOutput: StructuredOutputTextParseRequired},
		"json only":     {StructuredOutput: StructuredOutputJSONOnly},
		"strict schema": {StructuredOutput: StructuredOutputStrictSchema, SchemaName: "answer", Schema: json.RawMessage(`{"type":"object"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateRequiredCapabilities(f.binding, required); !errors.Is(err, ErrCapabilityUnavailable) || err.Error() != "CAPABILITY_UNAVAILABLE" {
				t.Fatal("capability mismatch did not fail with the stable class", err)
			}
		})
	}
	if err := ValidateRequiredCapabilities(f.binding, &RequiredCapabilities{StructuredOutput: "future-mode"}); err == nil || errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatal("invalid capability enum was treated as an availability result", err)
	}

	base := AdapterRequestExpectation{MaxBytes: 4096, MaxOutputTokens: 10, Controls: json.RawMessage("{}")}
	baseID, err := AdapterRequestExpectationID(f.binding, base)
	if err != nil {
		t.Fatal(err)
	}
	base.RequiredCapabilities = available
	requiredID, err := AdapterRequestExpectationID(f.binding, base)
	if err != nil || requiredID == baseID {
		t.Fatal("required capabilities were not expectation-identity bearing", requiredID, err)
	}
}

func TestChatAdapterNativeJSONAndStrictSchemaControls(t *testing.T) {
	schema := json.RawMessage(`{"additionalProperties":false,"properties":{"answer":{"type":"string"}},"required":["answer"],"type":"object"}`)
	for _, test := range []struct {
		name       string
		mode       StructuredOutputMode
		controls   ChatRequestExpectation
		wireFormat string
	}{
		{name: "json only", mode: StructuredOutputJSONOnly, controls: ChatRequestExpectation{ResponseFormat: "json_object"}, wireFormat: `{"type":"json_object"}`},
		{name: "strict schema", mode: StructuredOutputStrictSchema, controls: ChatRequestExpectation{ResponseFormat: "json_schema", SchemaName: "answer", Schema: schema}, wireFormat: `{"type":"json_schema","json_schema":{"name":"answer","schema":{"additionalProperties":false,"properties":{"answer":{"type":"string"}},"required":["answer"],"type":"object"},"strict":true}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newV2BindingFixture(t, "api")
			f.binding.Model.Capabilities.StructuredOutputModes = []StructuredOutputMode{test.mode}
			f.binding.ModelID, _ = f.binding.Model.ID()
			controls, _ := json.Marshal(test.controls)
			required := &RequiredCapabilities{StructuredOutput: test.mode}
			if test.mode == StructuredOutputStrictSchema {
				required.SchemaName, required.Schema = "answer", schema
			}
			body := []byte(`{"model":"deployment/opaque-model","messages":[{"role":"user","content":"hello"}],"max_tokens":10,"stream":true,"stream_options":{"include_usage":true},"response_format":` + test.wireFormat + `}`)
			expected := AdapterRequestExpectation{MaxBytes: int64(len(body)), MaxOutputTokens: 10, Controls: controls, RequiredCapabilities: required}
			if _, err := ValidateAdapterRequest(body, f.binding, expected); err != nil {
				t.Fatal("native Chat structured request rejected", err)
			}
			mutated := bytes.Replace(body, []byte(test.wireFormat), []byte(`{"type":"text"}`), 1)
			if _, err := ValidateAdapterRequest(mutated, f.binding, expected); err == nil {
				t.Fatal("substituted Chat response format admitted")
			}
		})
	}
}

func TestV2ReceiptRetainsExactRequestExpectationIdentity(t *testing.T) {
	f := newV2BindingFixture(t, "api")
	call := beginV2(t, f, strings.Repeat("7", 64), 10)
	receipt := f.receipt(call, strings.Repeat("8", 64), "stop", Usage{InputTokens: 3, OutputTokens: 1})
	receipt.Semantic = responseSemanticProjection("answer", []ResponseToolIdentity{})
	receipt.RequestExpectationID = ""
	if err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, call, receipt); err == nil {
		t.Fatal("v2 receipt without the exact request expectation identity was admitted")
	}
	state, err := Inspect(f.path)
	if err != nil || state.Pending == nil || state.Calls[0].Receipt != nil {
		t.Fatal("rejected receipt changed the pending call", state, err)
	}
}

func TestResponseToolIdentityUsesBrokerCanonicalArguments(t *testing.T) {
	first, err := responseToolIdentity("call-1", "read", json.RawMessage(`{"path":"a","limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	reordered, err := responseToolIdentity("call-1", "read", json.RawMessage("{ \"limit\" : 1, \"path\" : \"a\" }"))
	if err != nil || first.ArgumentsSHA256 != reordered.ArgumentsSHA256 {
		t.Fatal("semantically equal tool arguments did not cross-bind", first, reordered, err)
	}
	changed, err := responseToolIdentity("call-1", "read", json.RawMessage(`{"limit":2,"path":"a"}`))
	if err != nil || first.ArgumentsSHA256 == changed.ArgumentsSHA256 {
		t.Fatal("changed tool argument retained semantic identity", err)
	}
	if _, err := responseToolIdentity("call-1", "read", json.RawMessage(`{"ratio":0.5}`)); err == nil {
		t.Fatal("arguments unsupported by broker canonicalization were silently bound")
	}
}

func TestV2ReceiptRejectsInputBeyondDeclaredContextWithoutClearingPending(t *testing.T) {
	f := newV2BindingFixture(t, "api")
	call := beginV2(t, f, strings.Repeat("c", 64), 10)
	receipt := f.receipt(call, strings.Repeat("d", 64), "stop", Usage{InputTokens: 101, OutputTokens: 1})
	if err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, call, receipt); err == nil {
		t.Fatal("provider usage beyond selected context contract admitted")
	}
	state, err := Inspect(f.path)
	if err != nil || state.Pending == nil || state.Calls[0].Receipt != nil {
		t.Fatal("rejected provider violation changed pending UNKNOWN state", state, err)
	}
}

func TestV2JournalAcceptsOnlyConfiguredObservedModelAlias(t *testing.T) {
	f := newV2BindingFixture(t, "api")
	f.model.ObservedModelAliases = []string{"deployment-2026-09-08"}
	f.binding.Model = f.model
	f.binding.ModelID, _ = f.model.ID()
	// Bind a fresh journal because alias policy is part of the immutable model ID.
	f.path = filepath.Join(t.TempDir(), "provider-alias.jsonl")
	var err error
	f.binding, err = Bind(f.path, f.accessPath, f.policy, f.intent, f.endpoint, f.model)
	if err != nil {
		t.Fatal(err)
	}
	call := beginV2(t, f, strings.Repeat("e", 64), 10)
	receipt := f.receipt(call, strings.Repeat("f", 64), "stop", Usage{InputTokens: 3, OutputTokens: 1})
	receipt.ObservedModel = "deployment-2026-09-08"
	receipt.Semantic = responseSemanticProjection("answer", []ResponseToolIdentity{})
	if err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, call, receipt); err != nil {
		t.Fatal(err)
	}
	completed, err := Inspect(f.path)
	if err != nil || completed.Calls[0].Receipt.Semantic == nil || completed.Calls[0].Receipt.Semantic.ToolCalls == nil || len(completed.Calls[0].Receipt.Semantic.ToolCalls) != 0 {
		t.Fatal("empty semantic tool array changed across replay clone", completed, err)
	}

	foreign := newV2BindingFixture(t, "api")
	foreign.model.ObservedModelAliases = []string{"deployment-2026-09-08"}
	foreign.path = filepath.Join(t.TempDir(), "provider-foreign.jsonl")
	foreign.binding, err = Bind(foreign.path, foreign.accessPath, foreign.policy, foreign.intent, foreign.endpoint, foreign.model)
	if err != nil {
		t.Fatal(err)
	}
	foreignCall := beginV2(t, foreign, strings.Repeat("1", 64), 10)
	foreignReceipt := foreign.receipt(foreignCall, strings.Repeat("2", 64), "stop", Usage{InputTokens: 3, OutputTokens: 1})
	foreignReceipt.ObservedModel = "unconfigured-version"
	foreignReceipt.Semantic = responseSemanticProjection("answer", []ResponseToolIdentity{})
	if err := Complete(foreign.path, foreign.accessPath, foreign.policy, foreign.intent, foreign.binding, foreignCall, foreignReceipt); err == nil {
		t.Fatal("unconfigured observed model admitted")
	}
	state, inspectErr := Inspect(foreign.path)
	if inspectErr != nil || state.Pending == nil || state.Calls[0].Receipt != nil {
		t.Fatal("rejected alias changed pending state", state, inspectErr)
	}
}

func TestObservedModelAliasNeverChangesRequestedModel(t *testing.T) {
	f := newV2BindingFixture(t, "api")
	f.model.ObservedModelAliases = []string{"deployment-2026-09-08"}
	f.binding.Model = f.model
	f.binding.ModelID, _ = f.model.ID()
	body := []byte(`{"model":"deployment-2026-09-08","messages":[{"role":"user","content":"hello"}],"max_tokens":10,"stream":true,"stream_options":{"include_usage":true}}`)
	_, err := ValidateAdapterRequest(body, f.binding, AdapterRequestExpectation{MaxBytes: 4096, MaxOutputTokens: 10, Controls: json.RawMessage("{}")})
	if err == nil {
		t.Fatal("observed-model alias admitted as request model")
	}
}
