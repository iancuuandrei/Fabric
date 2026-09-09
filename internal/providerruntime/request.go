package providerruntime

import (
	"encoding/json"
	"errors"

	"harness.local/engorch/internal/providergateway"
)

// BuildRequest creates one finite protocol request without tools, continuation,
// hosted effects, or provider/model inference from names.
func BuildRequest(binding providergateway.Binding, expected providergateway.AdapterRequestExpectation, invocation Invocation) ([]byte, error) {
	if err := invocation.validate(); err != nil || len(expected.Tools) != 0 {
		return nil, ErrRejected
	}
	var body any
	switch binding.Model.AdapterID {
	case providergateway.OpenAIChatCompletionsAdapter:
		var controls providergateway.ChatRequestExpectation
		if err := json.Unmarshal(expected.Controls, &controls); err != nil {
			return nil, ErrRejected
		}
		messages := []any{}
		if invocation.System != "" {
			messages = append(messages, map[string]any{"role": "system", "content": invocation.System})
		}
		messages = append(messages, map[string]any{"role": "user", "content": invocation.Prompt})
		streaming := expected.ResponseFraming != providergateway.ResponseFramingJSON
		object := map[string]any{"model": binding.Model.Model, "messages": messages, "max_tokens": expected.MaxOutputTokens, "stream": streaming}
		if streaming {
			object["stream_options"] = map[string]any{"include_usage": true}
		}
		switch controls.ResponseFormat {
		case "json_object":
			object["response_format"] = map[string]any{"type": "json_object"}
		case "json_schema":
			object["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": controls.SchemaName, "schema": json.RawMessage(controls.Schema), "strict": true}}
		}
		body = object
	case providergateway.OpenAIResponsesAdapter:
		var controls providergateway.ResponsesRequestExpectation
		if err := json.Unmarshal(expected.Controls, &controls); err != nil {
			return nil, ErrRejected
		}
		input := []any{}
		if invocation.System != "" {
			input = append(input, map[string]any{"role": controls.SystemRole, "content": invocation.System})
		}
		input = append(input, map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": invocation.Prompt}}})
		object := map[string]any{"model": binding.Model.Model, "input": input, "max_output_tokens": expected.MaxOutputTokens, "stream": expected.ResponseFraming != providergateway.ResponseFramingJSON, "store": controls.Store}
		addResponsesControls(object, controls)
		body = object
	case providergateway.AnthropicMessagesAdapter:
		var controls providergateway.AnthropicMessagesRequestExpectation
		if err := json.Unmarshal(expected.Controls, &controls); err != nil {
			return nil, ErrRejected
		}
		object := map[string]any{
			"model":      binding.Model.Model,
			"max_tokens": expected.MaxOutputTokens,
			"stream":     expected.ResponseFraming != providergateway.ResponseFramingJSON,
			"messages": []any{map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": invocation.Prompt}},
			}},
		}
		if controls.RequireSystem {
			if invocation.System == "" {
				return nil, ErrRejected
			}
			object["system"] = []any{map[string]any{"type": "text", "text": invocation.System}}
		} else if invocation.System != "" {
			return nil, ErrRejected
		}
		addAnthropicControls(object, controls)
		body = object
	default:
		return nil, errors.New("direct provider adapter unavailable")
	}
	raw, err := json.Marshal(body)
	if err != nil || int64(len(raw)) > expected.MaxBytes {
		return nil, ErrRejected
	}
	if _, err := providergateway.ValidateAdapterRequest(raw, binding, expected); err != nil {
		return nil, errors.Join(ErrRejected, err)
	}
	return raw, nil
}

func addResponsesControls(object map[string]any, c providergateway.ResponsesRequestExpectation) {
	if c.ReasoningEffort != "" || c.ReasoningSummary != "" {
		object["reasoning"] = map[string]any{"effort": c.ReasoningEffort, "summary": c.ReasoningSummary}
	}
	if len(c.Include) != 0 {
		object["include"] = c.Include
	}
	if c.TextFormat != "" || c.TextVerbosity != "" {
		text := map[string]any{}
		switch c.TextFormat {
		case "plain":
			text["format"] = map[string]any{"type": "text"}
		case "json_object":
			text["format"] = map[string]any{"type": "json_object"}
		case "json_schema":
			text["format"] = map[string]any{"type": "json_schema", "name": c.TextSchemaName, "schema": json.RawMessage(c.TextSchema), "strict": true}
		}
		if c.TextVerbosity != "" {
			text["verbosity"] = c.TextVerbosity
		}
		object["text"] = text
	}
	if c.ToolChoice != "" {
		object["tool_choice"] = c.ToolChoice
	}
	if c.ParallelToolCalls != nil {
		object["parallel_tool_calls"] = *c.ParallelToolCalls
	}
	if c.ServiceTier != "" {
		object["service_tier"] = c.ServiceTier
	}
	if c.PromptCacheKey != "" {
		object["prompt_cache_key"] = c.PromptCacheKey
	}
	if c.Temperature != nil {
		object["temperature"] = c.Temperature
	}
	if c.TopP != nil {
		object["top_p"] = c.TopP
	}
}

func addAnthropicControls(object map[string]any, c providergateway.AnthropicMessagesRequestExpectation) {
	if c.ThinkingMode != "" {
		thinking := map[string]any{"type": c.ThinkingMode}
		if c.ThinkingBudgetTokens != nil {
			thinking["budget_tokens"] = *c.ThinkingBudgetTokens
		}
		object["thinking"] = thinking
	}
	if c.Effort != "" {
		object["output_config"] = map[string]any{"effort": c.Effort}
	}
	if c.Temperature != nil {
		object["temperature"] = c.Temperature
	}
	if c.TopP != nil {
		object["top_p"] = c.TopP
	}
	if c.TopK != nil {
		object["top_k"] = *c.TopK
	}
	if len(c.StopSequences) != 0 {
		object["stop_sequences"] = c.StopSequences
	}
}
