package providergateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"unicode/utf8"
)

// RequestTool is one exact provider-facing function declaration. Name already
// includes any namespace prefix used on the provider wire.
type RequestTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ChatRequestExpectation fixes the finite native structured-output control for
// Chat Completions. Empty fields require response_format omission.
type ChatRequestExpectation struct {
	ResponseFormat string          `json:"response_format,omitempty"`
	SchemaName     string          `json:"schema_name,omitempty"`
	Schema         json.RawMessage `json:"schema,omitempty"`
}

// RequestObservation retains only bounded wire identity and non-content facts.
// The caller continues to own the validated raw bytes; this type is not a body
// that can be reserialized and forwarded.
type RequestObservation struct {
	SHA256          string `json:"sha256"`
	SizeBytes       int64  `json:"size_bytes"`
	Model           string `json:"model"`
	MaxOutputTokens int64  `json:"max_output_tokens"`
	MessageCount    int    `json:"message_count"`
	ToolCount       int    `json:"tool_count"`
}

// ValidateChatCompletionRequest admits the small request subset exercised by
// the pinned OpenCode OpenAI-compatible adapter. It validates exact raw bytes
// but returns only their digest and size. It performs no logging, forwarding,
// credential lookup, network access, retry, or provider qualification.
func ValidateChatCompletionRequest(raw []byte, maxBytes int64, binding Binding, expectedMaxOutputTokens int64, allowed []RequestTool) (RequestObservation, error) {
	return validateChatCompletionRequest(raw, maxBytes, binding, expectedMaxOutputTokens, allowed, ChatRequestExpectation{})
}

func validateChatCompletionRequest(raw []byte, maxBytes int64, binding Binding, expectedMaxOutputTokens int64, allowed []RequestTool, expected ChatRequestExpectation) (RequestObservation, error) {
	if _, err := binding.ID(); err != nil || maxBytes < 2 || maxBytes > binding.Model.MaxRequestBytes || int64(len(raw)) > maxBytes || !uniqueProviderJSON(raw) {
		return RequestObservation{}, errors.New("invalid provider request admission bounds or JSON")
	}
	return validateChatCompletionRequestForFraming(raw, maxBytes, binding, expectedMaxOutputTokens, allowed, expected, ResponseFramingSSE)
}

func validateChatCompletionRequestForFraming(raw []byte, maxBytes int64, binding Binding, expectedMaxOutputTokens int64, allowed []RequestTool, expected ChatRequestExpectation, framing ResponseFraming) (RequestObservation, error) {
	if _, err := binding.ID(); err != nil || maxBytes < 2 || maxBytes > binding.Model.MaxRequestBytes || int64(len(raw)) > maxBytes || !uniqueProviderJSON(raw) {
		return RequestObservation{}, errors.New("invalid provider request admission bounds or JSON")
	}
	required := []string{"model", "messages", "max_tokens", "stream"}
	if framing == ResponseFramingSSE {
		required = append(required, "stream_options")
	}
	object, err := exactJSONObject(raw, required, []string{"tools", "tool_choice", "response_format"})
	if err != nil {
		return RequestObservation{}, errors.New("unsupported provider request schema")
	}
	model, ok := jsonString(object["model"])
	maxTokens, tokensOK := jsonInteger(object["max_tokens"])
	stream, streamOK := jsonBoolean(object["stream"])
	wantStream := framing == ResponseFramingSSE
	if !ok || model != binding.Model.Model || !tokensOK || maxTokens != expectedMaxOutputTokens || expectedMaxOutputTokens < 1 || expectedMaxOutputTokens > binding.Model.MaxOutputTokens || !streamOK || stream != wantStream {
		return RequestObservation{}, errors.New("provider request model, output cap or stream mismatch")
	}
	if framing == ResponseFramingSSE {
		streamOptions, err := exactJSONObject(object["stream_options"], []string{"include_usage"}, nil)
		includeUsage, usageOK := jsonBoolean(streamOptions["include_usage"])
		if err != nil || !usageOK || !includeUsage {
			return RequestObservation{}, errors.New("complete provider usage was not requested")
		}
	}
	if err := validateChatResponseFormat(object["response_format"], expected); err != nil {
		return RequestObservation{}, err
	}
	if choiceRaw, exists := object["tool_choice"]; exists {
		choice, valid := jsonString(choiceRaw)
		if !valid || choice != "auto" {
			return RequestObservation{}, errors.New("unsupported provider tool choice")
		}
	}
	toolNames, err := validateRequestTools(object["tools"], allowed)
	if err != nil {
		return RequestObservation{}, err
	}
	messages, err := validateRequestMessages(object["messages"], toolNames)
	if err != nil {
		return RequestObservation{}, err
	}
	digest := sha256.Sum256(raw)
	return RequestObservation{SHA256: hex.EncodeToString(digest[:]), SizeBytes: int64(len(raw)), Model: model, MaxOutputTokens: maxTokens, MessageCount: messages, ToolCount: len(allowed)}, nil
}

func validateChatResponseFormat(raw json.RawMessage, expected ChatRequestExpectation) error {
	if expected.ResponseFormat == "" {
		if raw != nil || expected.SchemaName != "" || len(expected.Schema) != 0 {
			return errors.New("unexpected Chat response format")
		}
		return nil
	}
	switch expected.ResponseFormat {
	case "json_object":
		value, err := exactJSONObject(raw, []string{"type"}, nil)
		kind, ok := jsonString(value["type"])
		if err != nil || !ok || kind != "json_object" || expected.SchemaName != "" || len(expected.Schema) != 0 {
			return errors.New("Chat JSON response format differs from expectation")
		}
	case "json_schema":
		value, err := exactJSONObject(raw, []string{"type", "json_schema"}, nil)
		kind, kindOK := jsonString(value["type"])
		definition, definitionErr := exactJSONObject(value["json_schema"], []string{"name", "schema", "strict"}, nil)
		name, nameOK := jsonString(definition["name"])
		strict, strictOK := jsonBoolean(definition["strict"])
		if err != nil || !kindOK || kind != "json_schema" || definitionErr != nil || !nameOK || name != expected.SchemaName || !strictOK || !strict || !bytes.Equal(definition["schema"], expected.Schema) {
			return errors.New("Chat strict schema format differs from expectation")
		}
	default:
		return errors.New("unsupported Chat response format")
	}
	return nil
}

func validateRequestTools(raw json.RawMessage, allowed []RequestTool) (map[string]bool, error) {
	names := make(map[string]bool, len(allowed))
	for _, tool := range allowed {
		if !requestToolIdentifier(tool.Name) || tool.Description == "" || len(tool.Description) > 8192 || !utf8.ValidString(tool.Description) || names[tool.Name] || !jsonObject(tool.Parameters) {
			return nil, errors.New("invalid expected provider tool catalog")
		}
		names[tool.Name] = true
	}
	if len(allowed) == 0 {
		if raw != nil {
			return nil, errors.New("unexpected provider tool catalog")
		}
		return names, nil
	}
	items, err := jsonArray(raw, 1, 64)
	if err != nil || len(items) != len(allowed) {
		return nil, errors.New("provider tool catalog differs from expected catalog")
	}
	for index, expected := range allowed {
		tool, err := exactJSONObject(items[index], []string{"type", "function"}, nil)
		if err != nil {
			return nil, errors.New("provider tool catalog differs from expected catalog")
		}
		kind, kindOK := jsonString(tool["type"])
		function, functionErr := exactJSONObject(tool["function"], []string{"name", "description", "parameters"}, nil)
		name, nameOK := jsonString(function["name"])
		description, descriptionOK := jsonString(function["description"])
		if !kindOK || kind != "function" || functionErr != nil || !nameOK || !descriptionOK || name != expected.Name || description != expected.Description || !sameJSON(function["parameters"], expected.Parameters) {
			return nil, errors.New("provider tool catalog differs from expected catalog")
		}
	}
	return names, nil
}

func validateRequestMessages(raw json.RawMessage, allowed map[string]bool) (int, error) {
	messages, err := jsonArray(raw, 1, 256)
	if err != nil {
		return 0, errors.New("invalid provider message list")
	}
	seenUser := false
	pending := map[string]bool{}
	seenCallIDs := map[string]bool{}
	lastRole := ""
	for _, item := range messages {
		message, err := exactJSONObject(item, []string{"role", "content"}, []string{"tool_calls", "tool_call_id"})
		if err != nil {
			return 0, errors.New("invalid provider message")
		}
		role, ok := jsonString(message["role"])
		if !ok {
			return 0, errors.New("invalid provider message role")
		}
		_, hasCalls := message["tool_calls"]
		toolCallIDRaw, hasToolCallID := message["tool_call_id"]
		switch role {
		case "system":
			if seenUser || len(pending) != 0 || hasCalls || hasToolCallID || !nonemptyText(message["content"]) {
				return 0, errors.New("invalid provider system message")
			}
		case "user":
			if seenUser || len(pending) != 0 || hasCalls || hasToolCallID || !nonemptyText(message["content"]) {
				return 0, errors.New("invalid provider user message")
			}
			seenUser = true
		case "assistant":
			if !seenUser || len(pending) != 0 || !hasCalls || hasToolCallID || !assistantTextOrNull(message["content"]) {
				return 0, errors.New("invalid provider assistant tool message")
			}
			calls, err := decodeRequestToolCalls(message["tool_calls"], allowed)
			if err != nil {
				return 0, err
			}
			for _, id := range calls {
				if seenCallIDs[id] {
					return 0, errors.New("reused provider tool call identity")
				}
				seenCallIDs[id] = true
				pending[id] = true
			}
		case "tool":
			toolCallID, idOK := jsonString(toolCallIDRaw)
			if !seenUser || len(pending) == 0 || hasCalls || !hasToolCallID || !idOK || !pending[toolCallID] || !nonemptyText(message["content"]) {
				return 0, errors.New("invalid provider tool result message")
			}
			delete(pending, toolCallID)
		default:
			return 0, errors.New("unsupported provider message role")
		}
		lastRole = role
	}
	if !seenUser || len(pending) != 0 || (lastRole != "user" && lastRole != "tool") {
		return 0, errors.New("provider request ends with unresolved message state")
	}
	return len(messages), nil
}

func decodeRequestToolCalls(raw json.RawMessage, allowed map[string]bool) ([]string, error) {
	items, err := jsonArray(raw, 1, 64)
	if err != nil {
		return nil, errors.New("invalid assistant provider tool calls")
	}
	ids := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		call, err := exactJSONObject(item, []string{"id", "type", "function"}, nil)
		if err != nil {
			return nil, errors.New("invalid provider tool call")
		}
		id, idOK := jsonString(call["id"])
		kind, kindOK := jsonString(call["type"])
		function, functionErr := exactJSONObject(call["function"], []string{"name", "arguments"}, nil)
		name, nameOK := jsonString(function["name"])
		arguments, argumentsOK := jsonString(function["arguments"])
		if !idOK || !printable(id, 256) || seen[id] || !kindOK || kind != "function" || functionErr != nil || !nameOK || !allowed[name] || !argumentsOK || len(arguments) < 2 || len(arguments) > 64<<10 || !jsonObject([]byte(arguments)) {
			return nil, errors.New("invalid provider tool call identity or arguments")
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

func exactJSONObject(raw []byte, required, optional []string) (map[string]json.RawMessage, error) {
	if !jsonObject(raw) {
		return nil, errors.New("JSON object required")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil, errors.New("JSON object required")
	}
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, key := range required {
		allowed[key] = true
		if _, exists := object[key]; !exists {
			return nil, errors.New("required JSON field missing")
		}
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key := range object {
		if !allowed[key] {
			return nil, errors.New("unsupported JSON field")
		}
	}
	return object, nil
}

func jsonArray(raw []byte, minimum, maximum int) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if !uniqueProviderJSON(raw) || len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, errors.New("JSON array required")
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || len(items) < minimum || len(items) > maximum {
		return nil, errors.New("invalid JSON array length")
	}
	return items, nil
}

func jsonObject(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return uniqueProviderJSON(raw) && len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

func sameJSON(left, right []byte) bool {
	if !uniqueProviderJSON(left) || !uniqueProviderJSON(right) {
		return false
	}
	decode := func(raw []byte) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return nil, errors.New("trailing JSON")
		}
		return value, nil
	}
	a, err := decode(left)
	if err != nil {
		return false
	}
	b, err := decode(right)
	return err == nil && reflect.DeepEqual(a, b)
}

func jsonString(raw []byte) (string, bool) {
	var value string
	err := json.Unmarshal(raw, &value)
	return value, err == nil && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func jsonInteger(raw []byte) (int64, bool) {
	var value int64
	err := json.Unmarshal(raw, &value)
	return value, err == nil && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func jsonBoolean(raw []byte) (bool, bool) {
	var value bool
	err := json.Unmarshal(raw, &value)
	return value, err == nil && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func nonemptyText(raw []byte) bool {
	value, ok := jsonString(raw)
	return ok && value != "" && utf8.ValidString(value)
}

func assistantTextOrNull(raw []byte) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true
	}
	value, ok := jsonString(raw)
	return ok && utf8.ValidString(value)
}

func requestToolIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
