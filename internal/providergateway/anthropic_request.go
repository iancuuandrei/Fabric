package providergateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
)

// AnthropicMessagesModelCapabilities is the model-bound subset implemented by
// this adapter. It intentionally excludes provider tools, multimodal blocks,
// cache controls, structured output and beta request extensions.
//
// Pinned request evidence: @ai-sdk/anthropic 3.0.111, npm tarball SHA-256
// 73bea91ccdc47af4efa1b6d77b8a4fe4af6303d16abc04aba54b2b1f75f6086e.
type AnthropicMessagesModelCapabilities struct {
	Version                    int      `json:"version"`
	FunctionTools              bool     `json:"function_tools"`
	SystemBlocks               bool     `json:"system_blocks"`
	ThinkingModes              []string `json:"thinking_modes"`
	ThinkingBudgetMin          *int64   `json:"thinking_budget_min,omitempty"`
	ThinkingBudgetMax          *int64   `json:"thinking_budget_max,omitempty"`
	EffortValues               []string `json:"effort_values"`
	ToolChoiceModes            []string `json:"tool_choice_modes"`
	ToolStrict                 bool     `json:"tool_strict"`
	DisableParallelTools       bool     `json:"disable_parallel_tools"`
	StopSequences              bool     `json:"stop_sequences"`
	SamplingParameters         []string `json:"sampling_parameters"`
	TemperatureMin             *string  `json:"temperature_min,omitempty"`
	TemperatureMax             *string  `json:"temperature_max,omitempty"`
	TopPMin                    *string  `json:"top_p_min,omitempty"`
	TopPMax                    *string  `json:"top_p_max,omitempty"`
	TopKMin                    *int64   `json:"top_k_min,omitempty"`
	TopKMax                    *int64   `json:"top_k_max,omitempty"`
	RequireCacheUsageBreakdown bool     `json:"require_cache_usage_breakdown"`
}

// AnthropicMessagesRequestExpectation fixes the optional controls that the
// pinned SDK may emit for one call. Nil pointers and empty values require wire
// omission; MaxOutputTokens is the exact final max_tokens value on the wire.
type AnthropicMessagesRequestExpectation struct {
	MaxOutputTokens      int64        `json:"max_output_tokens"`
	RequireSystem        bool         `json:"require_system"`
	ThinkingMode         string       `json:"thinking_mode"`
	ThinkingBudgetTokens *int64       `json:"thinking_budget_tokens"`
	Effort               string       `json:"effort"`
	ToolChoice           string       `json:"tool_choice"`
	DisableParallelTools *bool        `json:"disable_parallel_tools"`
	FunctionToolStrict   *bool        `json:"function_tool_strict"`
	Temperature          *json.Number `json:"temperature"`
	TopP                 *json.Number `json:"top_p"`
	TopK                 *int64       `json:"top_k"`
	StopSequences        []string     `json:"stop_sequences"`
}

// AnthropicMessagesRequestObservation retains request identity and non-content
// facts only. Validated prompt, tool output and thinking bytes remain caller-owned.
type AnthropicMessagesRequestObservation struct {
	SHA256          string `json:"sha256"`
	SizeBytes       int64  `json:"size_bytes"`
	Model           string `json:"model"`
	MaxOutputTokens int64  `json:"max_output_tokens"`
	MessageCount    int    `json:"message_count"`
	ToolCount       int    `json:"tool_count"`
	HasThinking     bool   `json:"has_thinking"`
}

func validateAnthropicMessagesModelCapabilities(raw json.RawMessage) error {
	if !uniqueProviderJSON(raw) {
		return errors.New("invalid Anthropic capability JSON")
	}
	required := []string{"version", "function_tools", "system_blocks", "thinking_modes", "effort_values", "tool_choice_modes", "tool_strict", "disable_parallel_tools", "stop_sequences", "sampling_parameters", "require_cache_usage_breakdown"}
	optional := []string{"thinking_budget_min", "thinking_budget_max", "temperature_min", "temperature_max", "top_p_min", "top_p_max", "top_k_min", "top_k_max"}
	object, err := exactJSONObject(raw, required, optional)
	if err != nil {
		return errors.New("invalid Anthropic capability schema")
	}
	version, versionOK := jsonInteger(object["version"])
	functionTools, toolsOK := jsonBoolean(object["function_tools"])
	systemBlocks, systemOK := jsonBoolean(object["system_blocks"])
	toolStrict, strictOK := jsonBoolean(object["tool_strict"])
	disableParallel, parallelOK := jsonBoolean(object["disable_parallel_tools"])
	stopSequences, stopsOK := jsonBoolean(object["stop_sequences"])
	cacheUsage, cacheOK := jsonBoolean(object["require_cache_usage_breakdown"])
	thinkingModes, thinkingOK := responsesStringArray(object["thinking_modes"])
	effortValues, effortOK := responsesStringArray(object["effort_values"])
	toolChoiceModes, choiceOK := responsesStringArray(object["tool_choice_modes"])
	sampling, samplingOK := responsesStringArray(object["sampling_parameters"])
	thinkingMin, thinkingMinOK := optionalAnthropicInteger(object, "thinking_budget_min")
	thinkingMax, thinkingMaxOK := optionalAnthropicInteger(object, "thinking_budget_max")
	topKMin, topKMinOK := optionalAnthropicInteger(object, "top_k_min")
	topKMax, topKMaxOK := optionalAnthropicInteger(object, "top_k_max")
	temperatureMin, temperatureMinOK := optionalResponsesDecimal(object, "temperature_min")
	temperatureMax, temperatureMaxOK := optionalResponsesDecimal(object, "temperature_max")
	topPMin, topPMinOK := optionalResponsesDecimal(object, "top_p_min")
	topPMax, topPMaxOK := optionalResponsesDecimal(object, "top_p_max")
	if !versionOK || !toolsOK || !systemOK || !strictOK || !parallelOK || !stopsOK || !cacheOK || !thinkingOK || !effortOK || !choiceOK || !samplingOK || !thinkingMinOK || !thinkingMaxOK || !topKMinOK || !topKMaxOK || !temperatureMinOK || !temperatureMaxOK || !topPMinOK || !topPMaxOK {
		return errors.New("invalid Anthropic capability values")
	}
	value := AnthropicMessagesModelCapabilities{
		Version: int(version), FunctionTools: functionTools, SystemBlocks: systemBlocks,
		ThinkingModes: thinkingModes, ThinkingBudgetMin: thinkingMin, ThinkingBudgetMax: thinkingMax,
		EffortValues: effortValues, ToolChoiceModes: toolChoiceModes, ToolStrict: toolStrict,
		DisableParallelTools: disableParallel, StopSequences: stopSequences,
		SamplingParameters: sampling, TemperatureMin: temperatureMin, TemperatureMax: temperatureMax,
		TopPMin: topPMin, TopPMax: topPMax, TopKMin: topKMin, TopKMax: topKMax,
		RequireCacheUsageBreakdown: cacheUsage,
	}
	return value.validate()
}

func optionalAnthropicInteger(object map[string]json.RawMessage, key string) (*int64, bool) {
	raw, exists := object[key]
	if !exists {
		return nil, true
	}
	value, ok := jsonInteger(raw)
	return &value, ok
}

func (c AnthropicMessagesModelCapabilities) validate() error {
	if c.Version != 1 || !c.RequireCacheUsageBreakdown || c.ThinkingModes == nil || c.EffortValues == nil || c.ToolChoiceModes == nil || c.SamplingParameters == nil ||
		!orderedUniqueAllowed(c.ThinkingModes, []string{"adaptive", "disabled", "enabled"}) ||
		!orderedUniqueTokens(c.EffortValues) ||
		!orderedUniqueAllowed(c.ToolChoiceModes, []string{"any", "auto", "tool"}) ||
		!orderedUniqueAllowed(c.SamplingParameters, []string{"temperature", "top_k", "top_p"}) {
		return errors.New("invalid Anthropic model capabilities")
	}
	if !c.FunctionTools && (len(c.ToolChoiceModes) != 0 || c.ToolStrict || c.DisableParallelTools) || c.FunctionTools && len(c.ToolChoiceModes) == 0 {
		return errors.New("inconsistent Anthropic function capabilities")
	}
	if slices.Contains(c.ThinkingModes, "enabled") != (c.ThinkingBudgetMin != nil && c.ThinkingBudgetMax != nil) || !validPositiveIntegerRange(c.ThinkingBudgetMin, c.ThinkingBudgetMax) {
		return errors.New("invalid Anthropic thinking budget range")
	}
	if slices.Contains(c.SamplingParameters, "temperature") != (c.TemperatureMin != nil && c.TemperatureMax != nil) || !validResponsesRange(c.TemperatureMin, c.TemperatureMax) ||
		slices.Contains(c.SamplingParameters, "top_p") != (c.TopPMin != nil && c.TopPMax != nil) || !validResponsesRange(c.TopPMin, c.TopPMax) ||
		slices.Contains(c.SamplingParameters, "top_k") != (c.TopKMin != nil && c.TopKMax != nil) || !validPositiveIntegerRange(c.TopKMin, c.TopKMax) {
		return errors.New("invalid Anthropic sampling ranges")
	}
	return nil
}

func orderedUniqueTokens(values []string) bool {
	if !slices.IsSorted(values) {
		return false
	}
	for index, value := range values {
		if !requestToken(value) || index > 0 && values[index-1] == value {
			return false
		}
	}
	return true
}

func validPositiveIntegerRange(minimum, maximum *int64) bool {
	if (minimum == nil) != (maximum == nil) {
		return false
	}
	return minimum == nil || *minimum >= 1 && *minimum <= *maximum
}

// ValidateAnthropicMessagesRequest admits the bounded text, function-tool and
// replayable-thinking subset emitted by @ai-sdk/anthropic 3.0.111. It does not
// forward bytes, resolve credentials, or infer capabilities from a model name.
func ValidateAnthropicMessagesRequest(raw []byte, maxBytes int64, binding Binding, capabilities AnthropicMessagesModelCapabilities, expected AnthropicMessagesRequestExpectation, allowed []RequestTool) (AnthropicMessagesRequestObservation, error) {
	return validateAnthropicMessagesRequestForFraming(raw, maxBytes, binding, capabilities, expected, allowed, ResponseFramingSSE)
}

func validateAnthropicMessagesRequestForFraming(raw []byte, maxBytes int64, binding Binding, capabilities AnthropicMessagesModelCapabilities, expected AnthropicMessagesRequestExpectation, allowed []RequestTool, framing ResponseFraming) (AnthropicMessagesRequestObservation, error) {
	encodedCapabilities, encodeErr := canonical.Bytes(capabilities)
	hasReasoning := len(capabilities.ThinkingModes) > 0 || len(capabilities.EffortValues) > 0
	if _, err := binding.ID(); err != nil || encodeErr != nil || binding.Model.Version != 2 || binding.Model.AdapterID != AnthropicMessagesAdapter || binding.Model.Capabilities == nil || binding.Model.Capabilities.Tools != capabilities.FunctionTools || binding.Model.Capabilities.Reasoning != hasReasoning || !binding.Model.Capabilities.CompleteUsage || !bytes.Equal(encodedCapabilities, binding.Model.AdapterCapabilities) || capabilities.validate() != nil || maxBytes < 2 || maxBytes > binding.Model.MaxRequestBytes || int64(len(raw)) > maxBytes || !uniqueProviderJSON(raw) {
		return AnthropicMessagesRequestObservation{}, errors.New("invalid Anthropic request admission bounds, binding, capabilities or JSON")
	}
	if err := validateAnthropicExpectation(binding, capabilities, expected, allowed); err != nil {
		return AnthropicMessagesRequestObservation{}, err
	}
	object, err := exactJSONObject(raw, []string{"model", "max_tokens", "messages", "stream"}, []string{"system", "tools", "tool_choice", "thinking", "output_config", "temperature", "top_p", "top_k", "stop_sequences"})
	if err != nil {
		return AnthropicMessagesRequestObservation{}, errors.New("unsupported Anthropic Messages request schema")
	}
	model, modelOK := jsonString(object["model"])
	maxTokens, maxOK := jsonInteger(object["max_tokens"])
	stream, streamOK := jsonBoolean(object["stream"])
	if !modelOK || model != binding.Model.Model || !maxOK || maxTokens != expected.MaxOutputTokens || !streamOK || stream != (framing == ResponseFramingSSE) {
		return AnthropicMessagesRequestObservation{}, errors.New("Anthropic model, output cap or stream policy mismatch")
	}
	if err := validateAnthropicControls(object, capabilities, expected, maxBytes); err != nil {
		return AnthropicMessagesRequestObservation{}, err
	}
	toolNames, err := validateAnthropicTools(object["tools"], allowed, expected.FunctionToolStrict)
	if err != nil {
		return AnthropicMessagesRequestObservation{}, err
	}
	if err := validateAnthropicToolChoice(object["tool_choice"], expected, toolNames); err != nil {
		return AnthropicMessagesRequestObservation{}, err
	}
	messageCount, observedThinking, err := validateAnthropicMessages(object["messages"], capabilities, toolNames, maxBytes)
	if err != nil {
		return AnthropicMessagesRequestObservation{}, err
	}
	digest := sha256.Sum256(raw)
	return AnthropicMessagesRequestObservation{SHA256: hex.EncodeToString(digest[:]), SizeBytes: int64(len(raw)), Model: model, MaxOutputTokens: maxTokens, MessageCount: messageCount, ToolCount: len(allowed), HasThinking: observedThinking}, nil
}

func validateAnthropicExpectation(binding Binding, capabilities AnthropicMessagesModelCapabilities, expected AnthropicMessagesRequestExpectation, allowed []RequestTool) error {
	if expected.MaxOutputTokens < 1 || expected.MaxOutputTokens > binding.Model.MaxOutputTokens || expected.RequireSystem && !capabilities.SystemBlocks || len(allowed) > 64 || len(allowed) > 0 && !capabilities.FunctionTools {
		return errors.New("Anthropic expectation exceeds model contract")
	}
	if expected.ThinkingMode == "" {
		if expected.ThinkingBudgetTokens != nil {
			return errors.New("Anthropic thinking budget without thinking mode")
		}
	} else if !slices.Contains(capabilities.ThinkingModes, expected.ThinkingMode) {
		return errors.New("unsupported Anthropic thinking mode")
	} else if expected.ThinkingMode == "enabled" {
		if expected.ThinkingBudgetTokens == nil || !integerWithin(expected.ThinkingBudgetTokens, capabilities.ThinkingBudgetMin, capabilities.ThinkingBudgetMax) || *expected.ThinkingBudgetTokens >= expected.MaxOutputTokens {
			return errors.New("invalid Anthropic thinking budget")
		}
	} else if expected.ThinkingBudgetTokens != nil {
		return errors.New("adaptive or disabled Anthropic thinking cannot set a fixed budget")
	}
	if expected.Effort != "" && !slices.Contains(capabilities.EffortValues, expected.Effort) {
		return errors.New("unsupported Anthropic effort")
	}
	if expected.ThinkingMode != "" && (expected.Temperature != nil || expected.TopP != nil || expected.TopK != nil) || expected.Temperature != nil && expected.TopP != nil {
		return errors.New("Anthropic sampling controls conflict")
	}
	if expected.FunctionToolStrict != nil && !capabilities.ToolStrict || expected.DisableParallelTools != nil && !capabilities.DisableParallelTools {
		return errors.New("Anthropic tool control exceeds model contract")
	}
	choiceMode := expected.ToolChoice
	if len(choiceMode) > 5 && choiceMode[:5] == "tool:" {
		choiceMode = "tool"
	}
	if choiceMode != "" && !slices.Contains(capabilities.ToolChoiceModes, choiceMode) {
		return errors.New("unsupported Anthropic tool choice")
	}
	if len(expected.StopSequences) > 0 && !capabilities.StopSequences {
		return errors.New("Anthropic stop sequences exceed model contract")
	}
	if !anthropicSamplingExpected("temperature", expected.Temperature, capabilities.SamplingParameters, capabilities.TemperatureMin, capabilities.TemperatureMax) ||
		!anthropicSamplingExpected("top_p", expected.TopP, capabilities.SamplingParameters, capabilities.TopPMin, capabilities.TopPMax) ||
		!anthropicIntegerSamplingExpected("top_k", expected.TopK, capabilities.SamplingParameters, capabilities.TopKMin, capabilities.TopKMax) {
		return errors.New("Anthropic sampling expectation exceeds model contract")
	}
	for _, sequence := range expected.StopSequences {
		if sequence == "" || !utf8.ValidString(sequence) || len(sequence) > 1024 {
			return errors.New("invalid Anthropic stop sequence")
		}
	}
	return nil
}

func integerWithin(value, minimum, maximum *int64) bool {
	return value == nil || minimum != nil && maximum != nil && *value >= *minimum && *value <= *maximum
}

func anthropicSamplingExpected(name string, value *json.Number, admitted []string, minimum, maximum *string) bool {
	if value == nil {
		return true
	}
	return slices.Contains(admitted, name) && validJSONNumber(*value) && responsesNumberWithin(value, minimum, maximum)
}

func anthropicIntegerSamplingExpected(name string, value *int64, admitted []string, minimum, maximum *int64) bool {
	return value == nil || slices.Contains(admitted, name) && integerWithin(value, minimum, maximum)
}

func validateAnthropicControls(object map[string]json.RawMessage, capabilities AnthropicMessagesModelCapabilities, expected AnthropicMessagesRequestExpectation, maxBytes int64) error {
	if validateAnthropicSystem(object["system"], expected.RequireSystem, capabilities.SystemBlocks, maxBytes) != nil ||
		!optionalNumberEquals(object, "temperature", expected.Temperature) || !optionalNumberEquals(object, "top_p", expected.TopP) ||
		!optionalIntegerEquals(object, "top_k", expected.TopK) || validateAnthropicStops(object["stop_sequences"], expected.StopSequences) != nil ||
		validateAnthropicThinking(object["thinking"], expected) != nil || validateAnthropicEffort(object["output_config"], expected.Effort) != nil {
		return errors.New("Anthropic request controls differ from expectation")
	}
	return nil
}

func validateAnthropicSystem(raw json.RawMessage, required, supported bool, maxBytes int64) error {
	if !required {
		if raw != nil {
			return errors.New("unexpected Anthropic system blocks")
		}
		return nil
	}
	if !supported {
		return errors.New("unsupported Anthropic system blocks")
	}
	items, err := jsonArray(raw, 1, 64)
	if err != nil {
		return err
	}
	for _, item := range items {
		object, objectErr := exactJSONObject(item, []string{"type", "text"}, nil)
		kind, kindOK := jsonString(object["type"])
		if objectErr != nil || !kindOK || kind != "text" || !nonemptyBoundedText(object["text"], maxBytes) {
			return errors.New("invalid Anthropic system block")
		}
	}
	return nil
}

func optionalIntegerEquals(object map[string]json.RawMessage, key string, expected *int64) bool {
	raw, exists := object[key]
	if expected == nil {
		return !exists
	}
	actual, ok := jsonInteger(raw)
	return exists && ok && actual == *expected
}

func validateAnthropicStops(raw json.RawMessage, expected []string) error {
	if len(expected) == 0 {
		if raw != nil {
			return errors.New("unexpected stop sequences")
		}
		return nil
	}
	items, err := jsonArray(raw, len(expected), len(expected))
	if err != nil {
		return err
	}
	for i, item := range items {
		value, ok := jsonString(item)
		if !ok || value != expected[i] {
			return errors.New("stop sequence mismatch")
		}
	}
	return nil
}

func validateAnthropicThinking(raw json.RawMessage, expected AnthropicMessagesRequestExpectation) error {
	if expected.ThinkingMode == "" {
		if raw != nil {
			return errors.New("unexpected thinking control")
		}
		return nil
	}
	required := []string{"type"}
	if expected.ThinkingMode == "enabled" {
		required = append(required, "budget_tokens")
	}
	object, err := exactJSONObject(raw, required, nil)
	mode, ok := jsonString(object["type"])
	if err != nil || !ok || mode != expected.ThinkingMode {
		return errors.New("thinking mismatch")
	}
	if expected.ThinkingBudgetTokens != nil {
		budget, ok := jsonInteger(object["budget_tokens"])
		if !ok || budget != *expected.ThinkingBudgetTokens {
			return errors.New("thinking budget mismatch")
		}
	}
	return nil
}

func validateAnthropicEffort(raw json.RawMessage, expected string) error {
	if expected == "" {
		if raw != nil {
			return errors.New("unexpected output config")
		}
		return nil
	}
	object, err := exactJSONObject(raw, []string{"effort"}, nil)
	actual, ok := jsonString(object["effort"])
	if err != nil || !ok || actual != expected {
		return errors.New("effort mismatch")
	}
	return nil
}

func validateAnthropicTools(raw json.RawMessage, expected []RequestTool, strict *bool) (map[string]bool, error) {
	names := make(map[string]bool, len(expected))
	for _, tool := range expected {
		if !requestToolIdentifier(tool.Name) || tool.Description == "" || len(tool.Description) > 8192 || !utf8.ValidString(tool.Description) || names[tool.Name] || !jsonObject(tool.Parameters) {
			return nil, errors.New("invalid expected Anthropic tool catalog")
		}
		names[tool.Name] = true
	}
	if len(expected) == 0 {
		if raw != nil || strict != nil {
			return nil, errors.New("unexpected Anthropic tool catalog")
		}
		return names, nil
	}
	items, err := jsonArray(raw, len(expected), len(expected))
	if err != nil {
		return nil, errors.New("Anthropic tool catalog mismatch")
	}
	for i, item := range items {
		required := []string{"name", "description", "input_schema"}
		if strict != nil {
			required = append(required, "strict")
		}
		object, objectErr := exactJSONObject(item, required, nil)
		name, nameOK := jsonString(object["name"])
		description, descriptionOK := jsonString(object["description"])
		if objectErr != nil || !nameOK || name != expected[i].Name || !descriptionOK || description != expected[i].Description || !sameJSON(object["input_schema"], expected[i].Parameters) || strict != nil && !rawBooleanEquals(object["strict"], *strict) {
			return nil, errors.New("Anthropic tool catalog mismatch")
		}
	}
	return names, nil
}

func rawBooleanEquals(raw json.RawMessage, expected bool) bool {
	value, ok := jsonBoolean(raw)
	return ok && value == expected
}

func validateAnthropicToolChoice(raw json.RawMessage, expected AnthropicMessagesRequestExpectation, tools map[string]bool) error {
	if expected.ToolChoice == "" {
		if raw != nil || expected.DisableParallelTools != nil {
			return errors.New("unexpected Anthropic tool choice")
		}
		return nil
	}
	if len(tools) == 0 {
		return errors.New("Anthropic tool choice requires tools")
	}
	required := []string{"type"}
	if expected.DisableParallelTools != nil {
		required = append(required, "disable_parallel_tool_use")
	}
	if len(expected.ToolChoice) > 5 && expected.ToolChoice[:5] == "tool:" {
		required = append(required, "name")
	}
	object, err := exactJSONObject(raw, required, nil)
	if err != nil {
		return errors.New("Anthropic tool choice mismatch")
	}
	kind, kindOK := jsonString(object["type"])
	expectedKind := expected.ToolChoice
	if len(expected.ToolChoice) > 5 && expected.ToolChoice[:5] == "tool:" {
		expectedKind = "tool"
		name := expected.ToolChoice[5:]
		actual, ok := jsonString(object["name"])
		if !ok || !tools[name] || actual != name {
			return errors.New("Anthropic named tool choice mismatch")
		}
	}
	if !kindOK || kind != expectedKind || !optionalBooleanEquals(object, "disable_parallel_tool_use", expected.DisableParallelTools) {
		return errors.New("Anthropic tool choice mismatch")
	}
	return nil
}

func validateAnthropicMessages(raw json.RawMessage, capabilities AnthropicMessagesModelCapabilities, tools map[string]bool, maxBytes int64) (int, bool, error) {
	items, err := jsonArray(raw, 1, 256)
	if err != nil {
		return 0, false, errors.New("invalid Anthropic messages")
	}
	pending := map[string]bool{}
	seenCalls := map[string]bool{}
	expectedRole := "user"
	hasThinking := false
	for _, item := range items {
		message, messageErr := exactJSONObject(item, []string{"role", "content"}, nil)
		role, roleOK := jsonString(message["role"])
		if messageErr != nil || !roleOK || role != expectedRole {
			return 0, false, errors.New("Anthropic roles must alternate from user")
		}
		content, contentErr := jsonArray(message["content"], 1, 256)
		if contentErr != nil {
			return 0, false, errors.New("invalid Anthropic message content")
		}
		if role == "user" {
			textSeen := false
			for _, block := range content {
				object, objectErr := exactJSONObject(block, nil, []string{"type", "text", "tool_use_id", "content", "is_error"})
				if objectErr != nil {
					return 0, false, errors.New("invalid Anthropic user block")
				}
				kind, kindOK := jsonString(object["type"])
				if !kindOK {
					return 0, false, errors.New("invalid Anthropic user block type")
				}
				switch kind {
				case "text":
					if len(pending) != 0 || exactTextBlock(object, maxBytes) != nil {
						return 0, false, errors.New("invalid Anthropic user text topology")
					}
					textSeen = true
				case "tool_result":
					if textSeen || validateAnthropicToolResult(object, pending, maxBytes) != nil {
						return 0, false, errors.New("invalid Anthropic tool result")
					}
				default:
					return 0, false, errors.New("unsupported Anthropic user content")
				}
			}
			if len(pending) != 0 {
				return 0, false, errors.New("unresolved Anthropic tool calls")
			}
			expectedRole = "assistant"
		} else {
			normalSeen := false
			toolSeen := false
			for _, block := range content {
				object, objectErr := exactJSONObject(block, nil, []string{"type", "text", "thinking", "signature", "data", "id", "name", "input"})
				if objectErr != nil {
					return 0, false, errors.New("invalid Anthropic assistant block")
				}
				kind, kindOK := jsonString(object["type"])
				if !kindOK {
					return 0, false, errors.New("invalid Anthropic assistant block type")
				}
				switch kind {
				case "thinking":
					if normalSeen || !anthropicReasoningHistorySupported(capabilities) || exactThinkingBlock(object, maxBytes) != nil {
						return 0, false, errors.New("invalid Anthropic thinking block")
					}
					hasThinking = true
				case "redacted_thinking":
					if normalSeen || !anthropicReasoningHistorySupported(capabilities) || exactRedactedThinkingBlock(object, maxBytes) != nil {
						return 0, false, errors.New("invalid Anthropic redacted thinking block")
					}
					hasThinking = true
				case "text":
					if toolSeen || exactTextBlock(object, maxBytes) != nil {
						return 0, false, errors.New("invalid Anthropic assistant text")
					}
					normalSeen = true
				case "tool_use":
					if validateAnthropicToolUse(object, tools, pending, seenCalls) != nil {
						return 0, false, errors.New("invalid Anthropic tool use")
					}
					normalSeen = true
					toolSeen = true
				default:
					return 0, false, errors.New("unsupported Anthropic assistant content")
				}
			}
			expectedRole = "user"
		}
	}
	if expectedRole != "assistant" || len(pending) != 0 {
		return 0, false, errors.New("Anthropic request must end with complete user input")
	}
	return len(items), hasThinking, nil
}

func anthropicReasoningHistorySupported(capabilities AnthropicMessagesModelCapabilities) bool {
	return slices.Contains(capabilities.ThinkingModes, "adaptive") || slices.Contains(capabilities.ThinkingModes, "enabled")
}

func exactTextBlock(object map[string]json.RawMessage, maxBytes int64) error {
	if _, err := exactJSONObjectFromMap(object, []string{"type", "text"}); err != nil || !nonemptyBoundedText(object["text"], maxBytes) {
		return errors.New("invalid text block")
	}
	return nil
}

func exactThinkingBlock(object map[string]json.RawMessage, maxBytes int64) error {
	if _, err := exactJSONObjectFromMap(object, []string{"type", "thinking", "signature"}); err != nil || !nonemptyBoundedText(object["thinking"], maxBytes) || !nonemptyBoundedText(object["signature"], maxBytes) {
		return errors.New("invalid thinking block")
	}
	return nil
}

func exactRedactedThinkingBlock(object map[string]json.RawMessage, maxBytes int64) error {
	if _, err := exactJSONObjectFromMap(object, []string{"type", "data"}); err != nil || !nonemptyBoundedText(object["data"], maxBytes) {
		return errors.New("invalid redacted thinking block")
	}
	return nil
}

func exactJSONObjectFromMap(object map[string]json.RawMessage, required []string) (map[string]json.RawMessage, error) {
	if len(object) != len(required) {
		return nil, errors.New("object key mismatch")
	}
	for _, key := range required {
		if object[key] == nil {
			return nil, errors.New("object key mismatch")
		}
	}
	return object, nil
}

func validateAnthropicToolUse(object map[string]json.RawMessage, tools map[string]bool, pending, seen map[string]bool) error {
	if _, err := exactJSONObjectFromMap(object, []string{"type", "id", "name", "input"}); err != nil {
		return err
	}
	id, idOK := jsonString(object["id"])
	name, nameOK := jsonString(object["name"])
	if !idOK || !printable(id, 256) || seen[id] || !nameOK || !tools[name] || !jsonObject(object["input"]) {
		return errors.New("invalid tool use")
	}
	seen[id] = true
	pending[id] = true
	return nil
}

func validateAnthropicToolResult(object map[string]json.RawMessage, pending map[string]bool, maxBytes int64) error {
	allowed := []string{"type", "tool_use_id", "content"}
	if object["is_error"] != nil {
		allowed = append(allowed, "is_error")
	}
	if _, err := exactJSONObjectFromMap(object, allowed); err != nil {
		return err
	}
	id, idOK := jsonString(object["tool_use_id"])
	if !idOK || !pending[id] {
		return errors.New("unmatched tool result")
	}
	if object["is_error"] != nil {
		value, ok := jsonBoolean(object["is_error"])
		if !ok || !value {
			return errors.New("invalid tool result error flag")
		}
	}
	if !anthropicToolResultContent(object["content"], maxBytes) {
		return errors.New("invalid tool result content")
	}
	delete(pending, id)
	return nil
}

func anthropicToolResultContent(raw json.RawMessage, maxBytes int64) bool {
	if nonemptyBoundedText(raw, maxBytes) {
		return true
	}
	items, err := jsonArray(raw, 1, 256)
	if err != nil {
		return false
	}
	for _, item := range items {
		object, objectErr := exactJSONObject(item, []string{"type", "text"}, nil)
		kind, kindOK := jsonString(object["type"])
		if objectErr != nil || !kindOK || kind != "text" || !nonemptyBoundedText(object["text"], maxBytes) {
			return false
		}
	}
	return true
}
