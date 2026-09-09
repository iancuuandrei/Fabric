package providergateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
)

func responsesCapabilities(tools, reasoning bool) ResponsesModelCapabilities {
	return ResponsesModelCapabilities{
		Version: 1, FunctionTools: tools, Reasoning: reasoning,
		SystemRoles: []string{"developer", "system"}, TextFormats: []string{"plain"},
		KnownExtensions: []string{"prompt_cache_key", "service_tier"},
	}
}

func responsesBinding(t *testing.T, capabilities ResponsesModelCapabilities) Binding {
	t.Helper()
	adapterCapabilities, err := canonical.Bytes(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := EndpointContract{
		Version: 2, Provider: "fixture", URL: "https://api.example.test/v1/responses", AdapterID: OpenAIResponsesAdapter,
		Auth: &AuthContract{Scheme: "bearer", CredentialRef: "FIXTURE_KEY"},
	}
	model := ModelContract{
		Version: 2, Provider: "fixture", Model: "any-qualified-model", AdapterID: OpenAIResponsesAdapter,
		Capabilities:        &ModelCapabilities{Tools: capabilities.FunctionTools, Reasoning: capabilities.Reasoning, OutputCap: true, CompleteUsage: true},
		AdapterCapabilities: adapterCapabilities, ContextWindowTokens: 8192, MaxCalls: 4,
		MaxRequestBytes: 1 << 20, MaxResponseBytes: 1 << 20, MaxOutputTokens: 512,
	}
	if slices.Contains(capabilities.TextFormats, "json_object") {
		model.Capabilities.StructuredOutputModes = append(model.Capabilities.StructuredOutputModes, StructuredOutputJSONOnly)
	}
	if slices.Contains(capabilities.TextFormats, "json_schema") {
		model.Capabilities.StructuredOutputModes = append(model.Capabilities.StructuredOutputModes, StructuredOutputStrictSchema)
	}
	endpointID, err := endpoint.ID()
	if err != nil {
		t.Fatal(err)
	}
	modelID, err := model.ID()
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{
		Version: 1, AccessPolicyID: strings.Repeat("a", 64), AccessInvocationID: strings.Repeat("b", 64),
		RouteID: strings.Repeat("c", 64), ReservedTokens: 4096, EndpointID: endpointID, ModelID: modelID,
		Endpoint: endpoint, Model: model,
	}
	if _, err := binding.ID(); err != nil {
		t.Fatal(err)
	}
	return binding
}

func responsesTools() []RequestTool {
	return []RequestTool{{
		Name: "engorch_source_read", Description: "Read admitted source bytes.",
		Parameters: json.RawMessage(`{"additionalProperties":false,"properties":{"path":{"type":"string"}},"required":["path"],"type":"object"}`),
	}}
}

func responsesExpectation(reasoning bool) ResponsesRequestExpectation {
	parallel := false
	expected := ResponsesRequestExpectation{
		MaxOutputTokens: 128, StateMode: "full-input-stateless", Store: false,
		SystemRole: "developer", ToolChoice: "auto", ParallelToolCalls: &parallel,
		ServiceTier: "default", PromptCacheKey: "run-bound-cache-key", FunctionToolStrict: false,
	}
	if reasoning {
		expected.ReasoningEffort = "high"
		expected.ReasoningSummary = "auto"
		expected.Include = []string{"reasoning.encrypted_content"}
	}
	return expected
}

func validResponsesBody(reasoning bool) []byte {
	body := map[string]any{
		"model": "any-qualified-model", "max_output_tokens": 128, "stream": true, "store": false,
		"input": []any{
			map[string]any{"role": "developer", "content": "Follow the admitted controller instructions."},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Inspect the selected source."}}},
		},
		"tools": []any{map[string]any{
			"type": "function", "name": "engorch_source_read", "description": "Read admitted source bytes.",
			"parameters": map[string]any{"additionalProperties": false, "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []any{"path"}, "type": "object"},
			"strict":     false,
		}},
		"tool_choice": "auto", "parallel_tool_calls": false,
		"service_tier": "default", "prompt_cache_key": "run-bound-cache-key",
	}
	if reasoning {
		body["reasoning"] = map[string]any{"effort": "high", "summary": "auto"}
		body["include"] = []any{"reasoning.encrypted_content"}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestValidateResponsesRequestTextAndFunctions(t *testing.T) {
	capabilities := responsesCapabilities(true, true)
	binding := responsesBinding(t, capabilities)
	expected := responsesExpectation(true)
	raw := validResponsesBody(true)
	observation, err := ValidateResponsesRequest(raw, int64(len(raw)), binding, capabilities, expected, responsesTools())
	if err != nil {
		t.Fatal(err)
	}
	if observation.Model != binding.Model.Model || observation.MaxOutputTokens != 128 || observation.InputItemCount != 2 || observation.ToolCount != 1 || observation.StateMode != "full-input-stateless" || observation.HasReasoning || len(observation.SHA256) != 64 || observation.SizeBytes != int64(len(raw)) {
		t.Fatal("Responses request observation mismatch", observation)
	}
}

func TestValidateResponsesRequestAssistantPhaseRequiresReasoningCapabilityOnly(t *testing.T) {
	capabilities := responsesCapabilities(true, true)
	binding := responsesBinding(t, capabilities)
	expected := responsesExpectation(false)
	for _, test := range []struct {
		name    string
		present bool
		value   any
	}{
		{name: "absent"},
		{name: "null", present: true, value: nil},
		{name: "commentary", present: true, value: "commentary"},
		{name: "final answer", present: true, value: "final_answer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := responsesContinuationWithPhase(test.present, test.value, "assistant")
			observation, err := ValidateResponsesRequest(raw, int64(len(raw)), binding, capabilities, expected, responsesTools())
			if err != nil || !observation.HasReasoning {
				t.Fatal("assistant phase or replay reasoning item was not admitted", observation, err)
			}
		})
	}
}

func TestValidateResponsesRequestRejectsPhaseOutsideAssistantOrFiniteValues(t *testing.T) {
	capabilities := responsesCapabilities(true, true)
	binding := responsesBinding(t, capabilities)
	expected := responsesExpectation(false)
	for _, test := range []struct {
		name  string
		value any
		role  string
	}{
		{name: "unknown assistant phase", value: "internal", role: "assistant"},
		{name: "numeric assistant phase", value: 1, role: "assistant"},
		{name: "object assistant phase", value: map[string]any{"name": "commentary"}, role: "assistant"},
		{name: "system phase", value: "commentary", role: "developer"},
		{name: "user phase", value: "commentary", role: "user"},
		{name: "function phase", value: "commentary", role: "function_call"},
		{name: "reasoning phase", value: "commentary", role: "reasoning"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := responsesContinuationWithPhase(true, test.value, test.role)
			if _, err := ValidateResponsesRequest(raw, int64(len(raw)), binding, capabilities, expected, responsesTools()); err == nil {
				t.Fatal("invalid or non-assistant Responses phase was admitted")
			}
		})
	}
}

func TestValidateResponsesRequestReasoningCapabilityFalseRejectsReplayItem(t *testing.T) {
	capabilities := responsesCapabilities(true, false)
	binding := responsesBinding(t, capabilities)
	expected := responsesExpectation(false)
	raw := responsesContinuationWithPhase(true, "commentary", "assistant")
	if _, err := ValidateResponsesRequest(raw, int64(len(raw)), binding, capabilities, expected, responsesTools()); err == nil || err.Error() != "unexpected Responses reasoning item" {
		t.Fatal("reasoning replay item bypassed a false capability contract", err)
	}
}

func TestValidateResponsesRequestReasoningCapabilityTrueDoesNotPermitControls(t *testing.T) {
	capabilities := responsesCapabilities(true, true)
	binding := responsesBinding(t, capabilities)
	expected := responsesExpectation(false)
	var body map[string]any
	if err := json.Unmarshal(responsesContinuationWithPhase(true, "commentary", "assistant"), &body); err != nil {
		t.Fatal(err)
	}
	body["reasoning"] = map[string]any{"effort": "high", "summary": "auto"}
	body["include"] = []any{"reasoning.encrypted_content"}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateResponsesRequest(raw, int64(len(raw)), binding, capabilities, expected, responsesTools()); err == nil || err.Error() != "unexpected Responses reasoning control" {
		t.Fatal("unrequested Responses reasoning controls were admitted", err)
	}
}

func TestValidateResponsesRequestPrivateM2gArtifactReplay(t *testing.T) {
	artifactPath := os.Getenv("ENGORCH_PRIVATE_PREFLIGHT_ARTIFACT")
	if artifactPath == "" {
		t.Skip("set ENGORCH_PRIVATE_PREFLIGHT_ARTIFACT for the restricted exact replay")
	}
	artifact, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		Cause       string `json:"cause"`
		RequestSHA  string `json:"request_sha256"`
		RequestSize int64  `json:"request_bytes"`
		Body        []byte `json:"body"`
	}
	if err := json.Unmarshal(artifact, &evidence); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(evidence.Body)
	if evidence.RequestSize != 47338 || len(evidence.Body) != 47338 || evidence.RequestSHA != "27f1e6394827f78c79fc6fdefdaa423069bd6d47fe69f10bdbad7e1a46a64d8e" || evidence.RequestSHA != hex.EncodeToString(digest[:]) {
		t.Fatal("restricted artifact identity changed", evidence.RequestSize, len(evidence.Body), evidence.RequestSHA)
	}

	gatewayPath := strings.TrimSuffix(filepath.Dir(artifactPath), ".preflight-evidence")
	events, err := journal.Read(gatewayPath)
	if err != nil || len(events) < 1 || events[0].Kind != "provider.bound" {
		t.Fatal("adjacent gateway binding journal unavailable", err)
	}
	var binding Binding
	if err := json.Unmarshal(events[0].Payload, &binding); err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Input          []json.RawMessage `json:"input"`
		MaxOutput      int64             `json:"max_output_tokens"`
		Store          bool              `json:"store"`
		PromptCacheKey string            `json:"prompt_cache_key"`
		ToolChoice     string            `json:"tool_choice"`
		Tools          []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
			Strict      bool            `json:"strict"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(evidence.Body, &wire); err != nil || len(wire.Input) < 1 || len(wire.Tools) == 0 {
		t.Fatal("restricted request shape could not be decoded", err)
	}
	var firstInput struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(wire.Input[0], &firstInput); err != nil {
		t.Fatal(err)
	}
	tools := make([]RequestTool, len(wire.Tools))
	strict := true
	for index, tool := range wire.Tools {
		tools[index] = RequestTool{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters}
		strict = strict && tool.Strict
	}
	expected := ResponsesRequestExpectation{
		MaxOutputTokens: wire.MaxOutput, StateMode: "full-input-stateless", Store: wire.Store,
		SystemRole: firstInput.Role, ToolChoice: wire.ToolChoice, PromptCacheKey: wire.PromptCacheKey,
		FunctionToolStrict: strict,
	}
	var falseCapabilities ResponsesModelCapabilities
	if err := json.Unmarshal(binding.Model.AdapterCapabilities, &falseCapabilities); err != nil {
		t.Fatal(err)
	}
	if falseCapabilities.Reasoning {
		t.Fatal("restricted binding was expected to preserve the false reasoning capability")
	}
	if _, err := ValidateResponsesRequest(evidence.Body, evidence.RequestSize, binding, falseCapabilities, expected, tools); err == nil || err.Error() != evidence.Cause {
		t.Fatal("pre-fix restricted replay did not reproduce the recorded validation cause", err)
	}

	trueCapabilities := falseCapabilities
	trueCapabilities.Reasoning = true
	trueBinding := binding
	trueModel := binding.Model
	trueModel.Capabilities = cloneModelCapabilities(binding.Model.Capabilities)
	trueModel.Capabilities.Reasoning = true
	trueAdapterCapabilities, err := canonical.Bytes(trueCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	trueModel.AdapterCapabilities = trueAdapterCapabilities
	trueModelID, err := trueModel.ID()
	if err != nil {
		t.Fatal(err)
	}
	trueBinding.Model, trueBinding.ModelID = trueModel, trueModelID
	if _, err := trueBinding.ID(); err != nil {
		t.Fatal(err)
	}
	if observation, err := ValidateResponsesRequest(evidence.Body, evidence.RequestSize, trueBinding, trueCapabilities, expected, tools); err != nil || !observation.HasReasoning {
		t.Fatal("reasoning-enabled replay did not reach assistant phase validation", observation, err)
	}

	var withControl map[string]any
	if err := json.Unmarshal(evidence.Body, &withControl); err != nil {
		t.Fatal(err)
	}
	withControl["reasoning"] = map[string]any{"effort": "high", "summary": "auto"}
	withControl["include"] = []any{"reasoning.encrypted_content"}
	controlledBody, err := json.Marshal(withControl)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateResponsesRequest(controlledBody, int64(len(controlledBody)), trueBinding, trueCapabilities, expected, tools); err == nil || err.Error() != "unexpected Responses reasoning control" {
		t.Fatal("reasoning-enabled replay admitted unrequested reasoning controls", err)
	}
}

func cloneModelCapabilities(source *ModelCapabilities) *ModelCapabilities {
	if source == nil {
		return nil
	}
	clone := *source
	return &clone
}

func responsesContinuationWithPhase(present bool, value any, target string) []byte {
	var body map[string]any
	if err := json.Unmarshal(validResponsesBody(false), &body); err != nil {
		panic(err)
	}
	input := []any{
		map[string]any{"role": "developer", "content": "Follow the admitted controller instructions."},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Inspect the selected source."}}},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Visible commentary."}}},
		map[string]any{"type": "reasoning", "encrypted_content": "opaque-encrypted-reasoning", "summary": []any{}},
		map[string]any{"type": "function_call", "call_id": "call_1", "name": "engorch_source_read", "arguments": `{"path":"source.go"}`},
		map[string]any{"type": "function_call_output", "call_id": "call_1", "output": `{"content":"bounded receipt"}`},
	}
	if target == "assistant" {
		if present {
			input[2].(map[string]any)["phase"] = value
		}
	} else if target == "developer" {
		input[0].(map[string]any)["phase"] = value
	} else if target == "user" {
		input[1].(map[string]any)["phase"] = value
	} else if target == "reasoning" {
		input[3].(map[string]any)["phase"] = value
	} else if target == "function_call" {
		input[4].(map[string]any)["phase"] = value
	}
	body["input"] = input
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestValidateResponsesRequestNativeJSONAndStrictSchemaControls(t *testing.T) {
	schema := json.RawMessage(`{"additionalProperties":false,"properties":{"answer":{"type":"string"}},"required":["answer"],"type":"object"}`)
	for _, test := range []struct {
		name   string
		format string
		mode   StructuredOutputMode
		wire   map[string]any
	}{
		{name: "json only", format: "json_object", mode: StructuredOutputJSONOnly, wire: map[string]any{"type": "json_object"}},
		{name: "strict schema", format: "json_schema", mode: StructuredOutputStrictSchema, wire: map[string]any{"type": "json_schema", "name": "answer", "schema": map[string]any{"additionalProperties": false, "properties": map[string]any{"answer": map[string]any{"type": "string"}}, "required": []any{"answer"}, "type": "object"}, "strict": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			capabilities := responsesCapabilities(true, false)
			capabilities.TextFormats = []string{"json_object", "json_schema", "plain"}
			binding := responsesBinding(t, capabilities)
			expected := responsesExpectation(false)
			expected.TextFormat = test.format
			if test.format == "json_schema" {
				expected.TextSchemaName, expected.TextSchema = "answer", schema
			}
			var body map[string]any
			if err := json.Unmarshal(validResponsesBody(false), &body); err != nil {
				t.Fatal(err)
			}
			body["text"] = map[string]any{"format": test.wire}
			raw, _ := json.Marshal(body)
			required := &RequiredCapabilities{Tools: true, StructuredOutput: test.mode}
			if test.mode == StructuredOutputStrictSchema {
				required.SchemaName, required.Schema = "answer", schema
			}
			controls, _ := json.Marshal(expected)
			adapterExpected := AdapterRequestExpectation{MaxBytes: int64(len(raw)), MaxOutputTokens: 128, Tools: responsesTools(), Controls: controls, RequiredCapabilities: required}
			if _, err := ValidateAdapterRequest(raw, binding, adapterExpected); err != nil {
				t.Fatal("native structured control rejected", err)
			}
			if _, err := AdapterRequestExpectationID(binding, adapterExpected); err != nil {
				t.Fatal("native structured requirement did not acquire an expectation identity", err)
			}
			format := body["text"].(map[string]any)["format"].(map[string]any)
			format["type"] = "text"
			mutated, _ := json.Marshal(body)
			if _, err := ValidateAdapterRequest(mutated, binding, adapterExpected); err == nil {
				t.Fatal("substituted structured control admitted")
			}
		})
	}
}

func TestValidateResponsesRequestStatelessContinuation(t *testing.T) {
	capabilities := responsesCapabilities(true, true)
	binding := responsesBinding(t, capabilities)
	expected := responsesExpectation(true)
	body := map[string]any{}
	if err := json.Unmarshal(validResponsesBody(true), &body); err != nil {
		t.Fatal(err)
	}
	body["input"] = []any{
		map[string]any{"role": "developer", "content": "Follow the admitted controller instructions."},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Inspect the selected source."}}},
		map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "opaque-encrypted-reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "Need source context."}}},
		map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "engorch_source_read", "arguments": `{"path":"source.go"}`},
		map[string]any{"type": "function_call_output", "call_id": "call_1", "output": `{"content":"bounded receipt"}`},
	}
	raw, _ := json.Marshal(body)
	observation, err := ValidateResponsesRequest(raw, int64(len(raw)), binding, capabilities, expected, responsesTools())
	if err != nil {
		t.Fatal(err)
	}
	if observation.InputItemCount != 5 || !observation.HasReasoning {
		t.Fatal("stateless continuation was not classified", observation)
	}
	delete(body["input"].([]any)[2].(map[string]any), "id")
	raw, _ = json.Marshal(body)
	if _, err := ValidateResponsesRequest(raw, int64(len(raw)), binding, capabilities, expected, responsesTools()); err != nil {
		t.Fatal("pinned stateless reasoning projection without provider item ID was rejected", err)
	}
}

func TestValidateResponsesRequestPreservesDecimalControls(t *testing.T) {
	capabilities := responsesCapabilities(false, false)
	capabilities.Sampling = true
	temperatureMin := "0"
	temperatureMax := "2"
	topPMin := "0"
	topPMax := "1"
	capabilities.TemperatureMin = &temperatureMin
	capabilities.TemperatureMax = &temperatureMax
	capabilities.TopPMin = &topPMin
	capabilities.TopPMax = &topPMax
	binding := responsesBinding(t, capabilities)
	temperature := json.Number("0.50")
	topP := json.Number("1.0")
	expected := ResponsesRequestExpectation{
		MaxOutputTokens: 64, StateMode: "full-input-stateless", SystemRole: "system",
		Temperature: &temperature, TopP: &topP,
	}
	raw := []byte(`{"model":"any-qualified-model","input":[{"role":"system","content":"Exact policy."},{"role":"user","content":[{"type":"input_text","text":"Hello"}]}],"max_output_tokens":64,"stream":true,"store":false,"temperature":0.5,"top_p":1}`)
	if _, err := ValidateResponsesRequest(raw, int64(len(raw)), binding, capabilities, expected, nil); err != nil {
		t.Fatal(err)
	}
}

func TestValidateResponsesRequestFractionalModelBoundIntegration(t *testing.T) {
	capabilities := responsesCapabilities(false, false)
	capabilities.Sampling = true
	minimum := "0.1"
	maximum := "0.9"
	capabilities.TemperatureMin = &minimum
	capabilities.TemperatureMax = &maximum

	// responsesBinding obtains the real ModelContract v2 ID and validates the
	// complete Binding, proving fractional ranges survive canonical identity.
	binding := responsesBinding(t, capabilities)
	temperature := json.Number("0.50")
	expected := ResponsesRequestExpectation{
		MaxOutputTokens: 64, StateMode: "full-input-stateless", SystemRole: "system",
		Temperature: &temperature,
	}
	raw := []byte(`{"model":"any-qualified-model","input":[{"role":"system","content":"Exact policy."},{"role":"user","content":[{"type":"input_text","text":"Hello"}]}],"max_output_tokens":64,"stream":true,"store":false,"temperature":0.5}`)
	if _, err := ValidateResponsesRequest(raw, int64(len(raw)), binding, capabilities, expected, nil); err != nil {
		t.Fatal(err)
	}
}

func TestValidateResponsesRequestRejectsSamplingOutsideBoundRange(t *testing.T) {
	capabilities := responsesCapabilities(false, false)
	capabilities.Sampling = true
	minimum := "0"
	maximum := "1"
	capabilities.TopPMin = &minimum
	capabilities.TopPMax = &maximum
	binding := responsesBinding(t, capabilities)
	topP := json.Number("1.01")
	expected := ResponsesRequestExpectation{
		MaxOutputTokens: 64, StateMode: "full-input-stateless", SystemRole: "system", TopP: &topP,
	}
	raw := []byte(`{"model":"any-qualified-model","input":[{"role":"system","content":"Exact policy."},{"role":"user","content":[{"type":"input_text","text":"Hello"}]}],"max_output_tokens":64,"stream":true,"store":false,"top_p":1.01}`)
	if _, err := ValidateResponsesRequest(raw, int64(len(raw)), binding, capabilities, expected, nil); err == nil {
		t.Fatal("out-of-range sampling expectation accepted")
	}
}

func TestValidateResponsesRequestRejectsMalformedDecimalExpectation(t *testing.T) {
	capabilities := responsesCapabilities(false, false)
	capabilities.Sampling = true
	binding := responsesBinding(t, capabilities)
	temperature := json.Number("0.5true")
	expected := ResponsesRequestExpectation{
		MaxOutputTokens: 64, StateMode: "full-input-stateless", SystemRole: "system",
		Temperature: &temperature,
	}
	raw := []byte(`{"model":"any-qualified-model","input":[{"role":"system","content":"Exact policy."},{"role":"user","content":[{"type":"input_text","text":"Hello"}]}],"max_output_tokens":64,"stream":true,"store":false,"temperature":0.5}`)
	if _, err := ValidateResponsesRequest(raw, int64(len(raw)), binding, capabilities, expected, nil); err == nil {
		t.Fatal("malformed decimal expectation accepted")
	}
}

func TestResponsesNumberGuardRejectsExpensiveOrWhitespaceLexemes(t *testing.T) {
	for _, value := range []json.Number{
		json.Number("1e999999999"),
		json.Number("1e-999999999"),
		json.Number(strings.Repeat("9", 97)),
		json.Number(" 0.5"),
		json.Number("0.5\t"),
		json.Number("0.5\\t"),
	} {
		if validJSONNumber(value) {
			t.Fatal("unsafe or invalid numeric lexeme accepted", value)
		}
	}
	for _, value := range []json.Number{json.Number("0"), json.Number("-0.70"), json.Number("1e+3"), json.Number("1e-1024")} {
		if !validJSONNumber(value) {
			t.Fatal("bounded JSON number rejected", value)
		}
	}
}

func TestValidateResponsesRequestRejectsUnboundCapabilities(t *testing.T) {
	bound := responsesCapabilities(true, true)
	binding := responsesBinding(t, bound)
	changed := bound
	changed.SystemRoles = []string{"developer"}
	if _, err := ValidateResponsesRequest(validResponsesBody(true), 1<<20, binding, changed, responsesExpectation(true), responsesTools()); err == nil {
		t.Fatal("unbound Responses capabilities accepted")
	}
}

func TestValidateResponsesRequestRejectsControlDrift(t *testing.T) {
	capabilities := responsesCapabilities(true, true)
	binding := responsesBinding(t, capabilities)
	expected := responsesExpectation(true)
	for name, mutate := range map[string]func(map[string]any){
		"missing store":       func(body map[string]any) { delete(body, "store") },
		"storage enabled":     func(body map[string]any) { body["store"] = true },
		"wrong model":         func(body map[string]any) { body["model"] = "substitute" },
		"wrong cap":           func(body map[string]any) { body["max_output_tokens"] = 129 },
		"not streaming":       func(body map[string]any) { body["stream"] = false },
		"reasoning stripped":  func(body map[string]any) { delete(body, "reasoning") },
		"include stripped":    func(body map[string]any) { delete(body, "include") },
		"parallel stripped":   func(body map[string]any) { delete(body, "parallel_tool_calls") },
		"service stripped":    func(body map[string]any) { delete(body, "service_tier") },
		"unknown extension":   func(body map[string]any) { body["metadata"] = map[string]any{"x": "y"} },
		"server conversation": func(body map[string]any) { body["previous_response_id"] = "resp_unbound" },
	} {
		t.Run(name, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal(validResponsesBody(true), &body); err != nil {
				t.Fatal(err)
			}
			mutate(body)
			raw, _ := json.Marshal(body)
			if _, err := ValidateResponsesRequest(raw, 1<<20, binding, capabilities, expected, responsesTools()); err == nil {
				t.Fatal("drifted Responses control accepted")
			}
		})
	}
}

func TestValidateResponsesRequestRejectsFunctionDrift(t *testing.T) {
	capabilities := responsesCapabilities(true, false)
	binding := responsesBinding(t, capabilities)
	expected := responsesExpectation(false)
	for name, mutate := range map[string]func(map[string]any){
		"strict drift": func(body map[string]any) {
			body["tools"].([]any)[0].(map[string]any)["strict"] = true
		},
		"schema drift": func(body map[string]any) {
			body["tools"].([]any)[0].(map[string]any)["parameters"] = map[string]any{"type": "object"}
		},
		"choice drift": func(body map[string]any) { body["tool_choice"] = "required" },
		"tool order drift": func(body map[string]any) {
			body["tools"] = append(body["tools"].([]any), body["tools"].([]any)[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal(validResponsesBody(false), &body); err != nil {
				t.Fatal(err)
			}
			mutate(body)
			raw, _ := json.Marshal(body)
			if _, err := ValidateResponsesRequest(raw, 1<<20, binding, capabilities, expected, responsesTools()); err == nil {
				t.Fatal("drifted Responses function contract accepted")
			}
		})
	}
}

func TestValidateResponsesRequestRejectsInvalidTranscript(t *testing.T) {
	capabilities := responsesCapabilities(true, false)
	binding := responsesBinding(t, capabilities)
	expected := responsesExpectation(false)
	base := func() map[string]any {
		var body map[string]any
		if err := json.Unmarshal(validResponsesBody(false), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	for name, input := range map[string][]any{
		"wrong system role": {
			map[string]any{"role": "system", "content": "Wrong role."},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Hello"}}},
		},
		"unsupported image": {
			map[string]any{"role": "developer", "content": "Policy."},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "https://example.test/x.png"}}},
		},
		"output before call": {
			map[string]any{"role": "developer", "content": "Policy."},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Hello"}}},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "result"},
		},
		"unresolved call": {
			map[string]any{"role": "developer", "content": "Policy."},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Hello"}}},
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "engorch_source_read", "arguments": `{"path":"x"}`},
		},
		"malformed arguments": {
			map[string]any{"role": "developer", "content": "Policy."},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Hello"}}},
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "engorch_source_read", "arguments": `[]`},
		},
		"duplicate item ID": {
			map[string]any{"role": "developer", "content": "Policy."},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Hello"}}},
			map[string]any{"role": "assistant", "id": "msg_duplicate", "content": []any{map[string]any{"type": "output_text", "text": "First"}}},
			map[string]any{"role": "assistant", "id": "msg_duplicate", "content": []any{map[string]any{"type": "output_text", "text": "Second"}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Continue"}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			body := base()
			body["input"] = input
			raw, _ := json.Marshal(body)
			if _, err := ValidateResponsesRequest(raw, 1<<20, binding, capabilities, expected, responsesTools()); err == nil {
				t.Fatal("invalid Responses transcript accepted")
			}
		})
	}
}

func TestValidateResponsesRequestRejectsDuplicateNestedKey(t *testing.T) {
	capabilities := responsesCapabilities(false, false)
	binding := responsesBinding(t, capabilities)
	expected := ResponsesRequestExpectation{MaxOutputTokens: 64, StateMode: "full-input-stateless", SystemRole: "developer"}
	raw := []byte(`{"model":"any-qualified-model","input":[{"role":"developer","content":"Policy"},{"role":"user","content":[{"type":"input_text","type":"input_text","text":"Hello"}]}],"max_output_tokens":64,"stream":true,"store":false}`)
	if _, err := ValidateResponsesRequest(raw, int64(len(raw)), binding, capabilities, expected, nil); err == nil {
		t.Fatal("duplicate nested Responses key accepted")
	}
}

func TestValidateResponsesModelCapabilitiesExactSchema(t *testing.T) {
	valid, err := canonical.Bytes(responsesCapabilities(true, true))
	if err != nil || validateResponsesModelCapabilities(valid) != nil {
		t.Fatal("valid Responses capabilities rejected", err)
	}
	profile := responsesCapabilities(true, true)
	profile.ContentPartCompletesText = true
	profileRaw, err := canonical.Bytes(profile)
	if err != nil || validateResponsesModelCapabilities(profileRaw) != nil {
		t.Fatal("valid content-part completion capability rejected", err)
	}
	profile.FunctionCallDoneNameV1 = true
	profileRaw, err = canonical.Bytes(profile)
	if err != nil || validateResponsesModelCapabilities(profileRaw) != nil {
		t.Fatal("valid function-call done name capability rejected", err)
	}
	withoutTools := responsesCapabilities(false, false)
	withoutTools.FunctionCallDoneNameV1 = true
	withoutToolsRaw, err := canonical.Bytes(withoutTools)
	if err != nil || validateResponsesModelCapabilities(withoutToolsRaw) == nil {
		t.Fatal("function-call done name capability accepted without function tools")
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"version":1,"function_tools":true,"reasoning":true,"sampling":false,"system_roles":["system","developer"],"text_formats":["plain"],"known_extensions":["prompt_cache_key","service_tier"],"trailing_cost_ping_v1":false}`),
		json.RawMessage(`{"version":1,"function_tools":true,"reasoning":true,"sampling":false,"system_roles":["developer","system"],"text_formats":["plain"],"known_extensions":["unknown"],"trailing_cost_ping_v1":false}`),
		json.RawMessage(`{"version":1,"function_tools":true,"reasoning":true,"sampling":false,"system_roles":["developer","system"],"text_formats":["plain"],"known_extensions":[],"trailing_cost_ping_v1":false,"extra":true}`),
		json.RawMessage(`{"version":1,"function_tools":null,"reasoning":true,"sampling":false,"system_roles":["developer","system"],"text_formats":["plain"],"known_extensions":[],"trailing_cost_ping_v1":false}`),
		json.RawMessage(`{"version":1,"function_tools":true,"reasoning":true,"sampling":false,"system_roles":["developer","system"],"text_formats":["plain"],"known_extensions":null,"trailing_cost_ping_v1":false}`),
		json.RawMessage(`{"version":1,"function_tools":true,"reasoning":true,"sampling":true,"system_roles":["developer","system"],"temperature_min":"0","text_formats":["plain"],"known_extensions":[],"trailing_cost_ping_v1":false}`),
		json.RawMessage(`{"version":1,"function_tools":true,"reasoning":true,"sampling":true,"system_roles":["developer","system"],"temperature_min":"2","temperature_max":"1","text_formats":["plain"],"known_extensions":[],"trailing_cost_ping_v1":false}`),
		json.RawMessage(`{"version":1,"function_tools":true,"reasoning":true,"sampling":false,"system_roles":["developer","system"],"text_formats":["plain"],"known_extensions":[],"trailing_cost_ping_v1":false,"content_part_completes_text":"true"}`),
		json.RawMessage(`{"version":1,"function_tools":true,"reasoning":true,"sampling":false,"system_roles":["developer","system"],"text_formats":["plain"],"known_extensions":[],"trailing_cost_ping_v1":false,"function_call_done_name_v1":"true"}`),
	} {
		if validateResponsesModelCapabilities(raw) == nil {
			t.Fatal("invalid Responses capabilities accepted", string(raw))
		}
	}
}
