package providergateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func requestBinding(t *testing.T) Binding {
	t.Helper()
	endpoint := EndpointContract{Version: 1, Provider: "openai", URL: "https://api.example.test/v1/chat/completions", Protocol: "openai-chat-completions-sse-v1"}
	model := ModelContract{Version: 1, Provider: "openai", Model: "qualified-model", Protocol: endpoint.Protocol, MaxCalls: 3, MaxRequestBytes: 1 << 20, MaxResponseBytes: 1 << 20, MaxOutputTokens: 128}
	endpointID, err := endpoint.ID()
	if err != nil {
		t.Fatal(err)
	}
	modelID, err := model.ID()
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{Version: 1, AccessPolicyID: strings.Repeat("a", 64), AccessInvocationID: strings.Repeat("b", 64), RouteID: strings.Repeat("c", 64), ReservedTokens: 1000, EndpointID: endpointID, ModelID: modelID, Endpoint: endpoint, Model: model}
	if _, err := binding.ID(); err != nil {
		t.Fatal(err)
	}
	return binding
}

func requestCatalog() []RequestTool {
	return []RequestTool{{Name: "engorch_source_read", Description: "Read admitted source bytes.", Parameters: json.RawMessage(`{"additionalProperties":false,"properties":{"ratio":{"minimum":0.5,"type":"number"}},"required":["ratio"],"type":"object"}`)}}
}

func firstRequest() string {
	return `{"model":"qualified-model","messages":[{"role":"system","content":"Use the admitted tool only."},{"role":"user","content":"Read https://example.test as ordinary prompt text."}],"max_tokens":73,"stream":true,"stream_options":{"include_usage":true},"tools":[{"type":"function","function":{"name":"engorch_source_read","description":"Read admitted source bytes.","parameters":{"type":"object","required":["ratio"],"properties":{"ratio":{"type":"number","minimum":0.5}},"additionalProperties":false}}}],"tool_choice":"auto"}`
}

func continuationRequest() string {
	return `{"model":"qualified-model","messages":[{"role":"user","content":"Read source."},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"engorch_source_read","arguments":"{\"ratio\":0.5}"}}]},{"role":"tool","content":"{\"success\":true}","tool_call_id":"call_1"}],"max_tokens":73,"stream":true,"stream_options":{"include_usage":true},"tools":[{"type":"function","function":{"name":"engorch_source_read","description":"Read admitted source bytes.","parameters":{"additionalProperties":false,"properties":{"ratio":{"minimum":0.5,"type":"number"}},"required":["ratio"],"type":"object"}}}],"tool_choice":"auto"}`
}

func TestValidateChatCompletionRequestBindsExactRawIdentityAndCatalog(t *testing.T) {
	binding := requestBinding(t)
	for name, body := range map[string]string{"first": firstRequest(), "continuation": continuationRequest()} {
		t.Run(name, func(t *testing.T) {
			observation, err := ValidateChatCompletionRequest([]byte(body), int64(len(body)), binding, 73, requestCatalog())
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte(body))
			if observation.SHA256 != hex.EncodeToString(digest[:]) || observation.SizeBytes != int64(len(body)) || observation.Model != binding.Model.Model || observation.MaxOutputTokens != 73 || observation.ToolCount != 1 || observation.MessageCount < 2 {
				t.Fatal("request observation differs from exact raw request", observation)
			}
			raw, err := json.Marshal(observation)
			if err != nil || strings.Contains(string(raw), "ordinary prompt") || strings.Contains(string(raw), "success") {
				t.Fatal("request observation retained raw message content", string(raw), err)
			}
		})
	}
}

func TestValidateChatCompletionRequestRejectsAmbiguityAndUnsupportedForwarding(t *testing.T) {
	binding := requestBinding(t)
	base := firstRequest()
	cases := map[string]string{
		"duplicate top field": strings.Replace(base, `"model":"qualified-model"`, `"model":"qualified-model","model":"qualified-model"`, 1),
		"escaped duplicate":   strings.Replace(base, `"model":"qualified-model"`, `"model":"qualified-model","\u006dodel":"qualified-model"`, 1),
		"trailing JSON":       base + `{}`,
		"unpaired surrogate":  strings.Replace(base, "Use the admitted tool only.", `\ud800`, 1),
		"wrong model":         strings.Replace(base, "qualified-model", "other-model", 1),
		"wrong output cap":    strings.Replace(base, `"max_tokens":73`, `"max_tokens":72`, 1),
		"null output cap":     strings.Replace(base, `"max_tokens":73`, `"max_tokens":null`, 1),
		"nonstreaming":        strings.Replace(base, `"stream":true`, `"stream":false`, 1),
		"missing usage":       strings.Replace(base, `,"stream_options":{"include_usage":true}`, ``, 1),
		"usage false":         strings.Replace(base, `"include_usage":true`, `"include_usage":false`, 1),
		"extra temperature":   strings.Replace(base, `"stream":true`, `"stream":true,"temperature":0.5`, 1),
		"external response":   strings.Replace(base, `"stream":true`, `"stream":true,"response_format":{"url":"https://outside.test/schema"}`, 1),
		"structured content":  strings.Replace(base, `"content":"Read https://example.test as ordinary prompt text."`, `"content":[{"type":"image_url","image_url":{"url":"https://outside.test/image"}}]`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateChatCompletionRequest([]byte(body), int64(len(body)), binding, 73, requestCatalog()); err == nil {
				t.Fatal("unsafe or ambiguous provider request admitted")
			}
		})
	}
}

func TestValidateChatCompletionRequestRejectsCatalogDriftAndNativeTools(t *testing.T) {
	binding := requestBinding(t)
	base := firstRequest()
	cases := map[string]string{
		"native extra": strings.Replace(base, `}],"tool_choice"`, `},{"type":"function","function":{"name":"bash","description":"shell","parameters":{"type":"object"}}}],"tool_choice"`, 1),
		"wrong name":   strings.Replace(base, "engorch_source_read", "bash", 1),
		"description":  strings.Replace(base, "Read admitted source bytes.", "Changed", 1),
		"schema":       strings.Replace(base, `"minimum":0.5`, `"minimum":0.6`, 1),
		"wrong choice": strings.Replace(base, `"tool_choice":"auto"`, `"tool_choice":"required"`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateChatCompletionRequest([]byte(body), int64(len(body)), binding, 73, requestCatalog()); err == nil {
				t.Fatal("provider catalog drift admitted")
			}
		})
	}
	if _, err := ValidateChatCompletionRequest([]byte(firstRequest()), int64(len(firstRequest())), binding, 73, nil); err == nil {
		t.Fatal("tools admitted for an empty allowed catalog")
	}
}

func TestValidateChatCompletionRequestRejectsMalformedToolHistory(t *testing.T) {
	binding := requestBinding(t)
	base := continuationRequest()
	reused := strings.Replace(base, `}],"max_tokens"`, `},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"engorch_source_read","arguments":"{\"ratio\":0.5}"}}]},{"role":"tool","content":"again","tool_call_id":"call_1"}],"max_tokens"`, 1)
	cases := map[string]string{
		"orphan result":       strings.Replace(base, `"tool_call_id":"call_1"`, `"tool_call_id":"other"`, 1),
		"duplicate call":      strings.Replace(base, `}]},{"role":"tool"`, `},{"id":"call_1","type":"function","function":{"name":"engorch_source_read","arguments":"{\"ratio\":0.5}"}}]},{"role":"tool"`, 1),
		"reused later call":   reused,
		"unknown function":    strings.Replace(base, "engorch_source_read", "bash", 1),
		"ambiguous arguments": strings.Replace(base, `{\"ratio\":0.5}`, `{\"ratio\":0.5,\"ratio\":0.6}`, 1),
		"array arguments":     strings.Replace(base, `{\"ratio\":0.5}`, `[0.5]`, 1),
		"structured result":   strings.Replace(base, `"content":"{\"success\":true}"`, `"content":{"success":true}`, 1),
		"unresolved at end":   strings.Replace(base, `},{"role":"tool","content":"{\"success\":true}","tool_call_id":"call_1"}`, ``, 1),
		"new user in group":   strings.Replace(base, `{"role":"tool","content":"{\"success\":true}","tool_call_id":"call_1"}`, `{"role":"user","content":"continue"}`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateChatCompletionRequest([]byte(body), int64(len(body)), binding, 73, requestCatalog()); err == nil {
				t.Fatal("malformed provider tool history admitted")
			}
		})
	}
}

func TestValidateChatCompletionRequestAllowsDistinctCompletedToolGroups(t *testing.T) {
	binding := requestBinding(t)
	body := strings.Replace(continuationRequest(), `}],"max_tokens"`, `},{"role":"assistant","content":null,"tool_calls":[{"id":"call_2","type":"function","function":{"name":"engorch_source_read","arguments":"{\"ratio\":0.6}"}}]},{"role":"tool","content":"again","tool_call_id":"call_2"}],"max_tokens"`, 1)
	observation, err := ValidateChatCompletionRequest([]byte(body), int64(len(body)), binding, 73, requestCatalog())
	if err != nil || observation.MessageCount != 5 {
		t.Fatal("distinct completed provider tool groups rejected", observation, err)
	}
}

func TestValidateChatCompletionRequestAllowsNoToolTextRequestOnlyWithoutTools(t *testing.T) {
	binding := requestBinding(t)
	body := `{"model":"qualified-model","messages":[{"role":"user","content":"plain text"}],"max_tokens":73,"stream":true,"stream_options":{"include_usage":true}}`
	if _, err := ValidateChatCompletionRequest([]byte(body), int64(len(body)), binding, 73, nil); err != nil {
		t.Fatal(err)
	}
}
