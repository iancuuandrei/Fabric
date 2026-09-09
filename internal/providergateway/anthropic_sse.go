package providergateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// AnthropicToolUse is one fully assembled client-side Anthropic tool call.
type AnthropicToolUse struct {
	Index int             `json:"index"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// AnthropicThinking retains the exact replayable thinking text and signature.
type AnthropicThinking struct {
	Index     int    `json:"index"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

// AnthropicRedactedThinking retains one opaque replayable reasoning block.
type AnthropicRedactedThinking struct {
	Index int    `json:"index"`
	Data  string `json:"data"`
}

// AnthropicMessagesObservation records bounded Messages wire evidence. The
// inclusive InputTokens total adds every reported cache category to Anthropic's
// uncached input_tokens. Nil cache pointers preserve unreported versus zero.
type AnthropicMessagesObservation struct {
	SHA256           string                      `json:"sha256"`
	SizeBytes        int                         `json:"size_bytes"`
	MessageID        string                      `json:"message_id"`
	Model            string                      `json:"model"`
	Status           string                      `json:"status"`
	StopReason       string                      `json:"stop_reason"`
	UsageComplete    bool                        `json:"usage_complete"`
	Usage            Usage                       `json:"usage"`
	TotalTokens      int64                       `json:"total_tokens"`
	OutputText       string                      `json:"output_text"`
	ToolUses         []AnthropicToolUse          `json:"tool_uses"`
	Thinking         []AnthropicThinking         `json:"thinking"`
	RedactedThinking []AnthropicRedactedThinking `json:"redacted_thinking"`
	PingCount        int                         `json:"ping_count"`
}

// AnthropicTerminalError identifies an explicit non-successful provider
// terminal without exposing provider-controlled error text.
type AnthropicTerminalError struct{ Status string }

// Error reports the terminal category without provider-controlled prose.
func (e *AnthropicTerminalError) Error() string {
	if e != nil {
		switch e.Status {
		case "refusal", "max_tokens", "pause_turn", "model_context_window_exceeded",
			"authentication_error", "permission_error", "not_found_error", "invalid_request_error",
			"rate_limit_error", "api_error", "overloaded_error":
			return "provider Anthropic Messages stream ended with " + e.Status
		}
	}
	return "provider Anthropic Messages stream ended unsuccessfully"
}

type anthropicBlock struct {
	kind, id, name, redacted string
	text, input, thinking    strings.Builder
	signature                string
	inputPrepopulated        bool
	stopped                  bool
}

type anthropicUsageState struct {
	input, output *int64
	cacheRead     *int64
	cacheWrite    *int64
	reasoning     *int64
}

// ParseAnthropicMessagesSSE strictly admits a complete Anthropic Messages
// stream for text, client tool-use, thinking and redacted-thinking blocks.
func ParseAnthropicMessagesSSE(raw []byte, maxBytes int, maxTotalTokens int64) (AnthropicMessagesObservation, error) {
	observation := anthropicBaseObservation(raw)
	if maxBytes < 1 || maxBytes > maximumSSEBytes || len(raw) == 0 || len(raw) > maxBytes || maxTotalTokens < 1 || maxTotalTokens > maximumProviderTokens || !utf8.Valid(raw) || bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		return observation, errors.New("invalid Anthropic Messages SSE bounds")
	}
	events, err := splitResponsesSSE(raw)
	if err != nil || len(events) < 1 || len(events) > maximumSSEEvents {
		if err == nil {
			err = errors.New("Anthropic Messages SSE is incomplete")
		}
		return observation, err
	}

	started, messageStopped, stopSeen := false, false, false
	blocks := []*anthropicBlock{}
	usage := anthropicUsageState{}
	for eventIndex, event := range events {
		if messageStopped {
			return observation, errors.New("provider data follows Anthropic message_stop")
		}
		object, err := anthropicObject(event.data)
		if err != nil {
			return observation, err
		}
		typeName, err := anthropicString(object, "type", 256, true)
		if err != nil || typeName != event.name {
			return observation, errors.New("Anthropic SSE event/data type mismatch")
		}

		switch typeName {
		case "message_start":
			if started || anthropicExactKeys(object, "type", "message") != nil {
				return observation, errors.New("invalid Anthropic message_start")
			}
			message, err := anthropicObject(object["message"])
			if err != nil || anthropicExactKeys(message, "id", "type", "role", "content", "model", "stop_reason", "stop_sequence", "usage") != nil || !responsesLiteral(message["type"], "message") || !responsesLiteral(message["role"], "assistant") || !bytes.Equal(message["stop_reason"], []byte("null")) || !bytes.Equal(message["stop_sequence"], []byte("null")) || !responsesEmptyArray(message["content"]) {
				return observation, errors.New("invalid Anthropic initial message")
			}
			observation.MessageID, err = anthropicString(message, "id", 256, true)
			if err == nil {
				observation.Model, err = anthropicString(message, "model", 256, true)
			}
			if err != nil {
				return observation, err
			}
			usage, err = decodeAnthropicUsage(message["usage"], true, usage)
			if err != nil {
				return observation, err
			}
			started = true

		case "ping":
			if !started || anthropicExactKeys(object, "type") != nil {
				return observation, errors.New("invalid Anthropic ping")
			}
			observation.PingCount++

		case "content_block_start":
			if !started || stopSeen || !allAnthropicBlocksStopped(blocks) || anthropicExactKeys(object, "type", "index", "content_block") != nil {
				return observation, errors.New("invalid Anthropic content_block_start")
			}
			index, err := anthropicIndex(object, "index")
			if err != nil || index != len(blocks) {
				return observation, errors.New("non-contiguous Anthropic content block index")
			}
			block, err := decodeAnthropicBlockStart(object["content_block"])
			if err != nil {
				return observation, err
			}
			for _, previous := range blocks {
				if block.id != "" && block.id == previous.id {
					return observation, errors.New("duplicate Anthropic tool-use identity")
				}
			}
			blocks = append(blocks, block)

		case "content_block_delta":
			if !started || stopSeen || anthropicExactKeys(object, "type", "index", "delta") != nil {
				return observation, errors.New("invalid Anthropic content_block_delta")
			}
			block, err := anthropicBoundBlock(object, blocks)
			if err != nil {
				return observation, err
			}
			if err := accumulateAnthropicDelta(block, object["delta"], maxBytes); err != nil {
				return observation, err
			}

		case "content_block_stop":
			if !started || stopSeen || anthropicExactKeys(object, "type", "index") != nil {
				return observation, errors.New("invalid Anthropic content_block_stop")
			}
			block, err := anthropicBoundBlock(object, blocks)
			if err != nil || block.stopped {
				return observation, errors.New("invalid Anthropic content block completion")
			}
			if block.kind == "thinking" && !validProviderIdentity(block.signature, maxBytes) {
				return observation, errors.New("Anthropic thinking block lacks signature")
			}
			if block.kind == "tool_use" {
				arguments := []byte(block.input.String())
				if !uniqueProviderJSON(arguments) {
					return observation, errors.New("ambiguous Anthropic tool input")
				}
				var input map[string]json.RawMessage
				if json.Unmarshal(arguments, &input) != nil || input == nil {
					return observation, errors.New("Anthropic tool input is not an object")
				}
			}
			block.stopped = true

		case "message_delta":
			if !started || !allAnthropicBlocksStopped(blocks) || anthropicExactKeys(object, "type", "delta", "usage") != nil {
				return observation, errors.New("invalid Anthropic message_delta order")
			}
			delta, err := anthropicObject(object["delta"])
			if err != nil || !anthropicAllowedRequiredKeys(delta, []string{"stop_reason", "stop_sequence"}, "stop_details") {
				return observation, errors.New("invalid Anthropic message delta")
			}
			if !bytes.Equal(delta["stop_sequence"], []byte("null")) {
				var sequence string
				if decodeProviderJSON(delta["stop_sequence"], &sequence) != nil || len(sequence) > 1024 || !utf8.ValidString(sequence) {
					return observation, errors.New("invalid Anthropic stop sequence")
				}
			}
			if !bytes.Equal(delta["stop_reason"], []byte("null")) {
				if stopSeen {
					return observation, errors.New("duplicate Anthropic stop reason")
				}
				observation.StopReason, err = anthropicString(delta, "stop_reason", 64, true)
				if err != nil {
					return observation, err
				}
				stopSeen = true
			}
			if details, ok := delta["stop_details"]; ok && !bytes.Equal(details, []byte("null")) {
				if observation.StopReason != "refusal" || validateAnthropicRefusalDetails(details, maxBytes) != nil {
					return observation, errors.New("invalid Anthropic stop details")
				}
			}
			usage, err = decodeAnthropicUsage(object["usage"], false, usage)
			if err != nil {
				return observation, err
			}

		case "message_stop":
			if !started || !stopSeen || !allAnthropicBlocksStopped(blocks) || anthropicExactKeys(object, "type") != nil {
				return observation, errors.New("invalid Anthropic message_stop")
			}
			messageStopped = true

		case "error":
			if eventIndex != len(events)-1 || anthropicExactKeys(object, "type", "error") != nil {
				return observation, errors.New("invalid Anthropic stream error")
			}
			errorObject, err := anthropicObject(object["error"])
			if err != nil || anthropicExactKeys(errorObject, "type", "message") != nil {
				return observation, errors.New("invalid Anthropic stream error payload")
			}
			status, err := anthropicString(errorObject, "type", 128, true)
			if err != nil {
				return observation, err
			}
			if _, err := anthropicString(errorObject, "message", maxBytes, false); err != nil {
				return observation, err
			}
			observation.Status = "error"
			return observation, &AnthropicTerminalError{Status: status}

		default:
			return observation, fmt.Errorf("unsupported Anthropic Messages event %q", typeName)
		}
	}
	if !started || !messageStopped {
		return observation, errors.New("Anthropic Messages SSE lacks message_stop")
	}
	if usage.input == nil || usage.output == nil {
		return observation, errors.New("Anthropic Messages usage is incomplete")
	}
	if err := finishAnthropicObservation(&observation, blocks, usage, maxTotalTokens); err != nil {
		return observation, err
	}
	switch observation.StopReason {
	case "end_turn", "stop_sequence":
		if len(observation.ToolUses) != 0 {
			return observation, errors.New("Anthropic stop reason differs from tool blocks")
		}
	case "tool_use":
		if len(observation.ToolUses) == 0 {
			return observation, errors.New("Anthropic tool_use stop lacks tool block")
		}
	default:
		observation.Status = observation.StopReason
		return observation, &AnthropicTerminalError{Status: observation.StopReason}
	}
	observation.Status = "completed"
	return observation, nil
}

func anthropicBaseObservation(raw []byte) AnthropicMessagesObservation {
	hash := sha256.Sum256(raw)
	return AnthropicMessagesObservation{SHA256: hex.EncodeToString(hash[:]), SizeBytes: len(raw)}
}

func decodeAnthropicBlockStart(raw []byte) (*anthropicBlock, error) {
	object, err := anthropicObject(raw)
	if err != nil {
		return nil, err
	}
	kind, err := anthropicString(object, "type", 64, true)
	if err != nil {
		return nil, err
	}
	block := &anthropicBlock{kind: kind}
	switch kind {
	case "text":
		if anthropicExactKeys(object, "type", "text") != nil {
			return nil, errors.New("invalid Anthropic text block start")
		}
		value, err := anthropicString(object, "text", maximumSSEBytes, false)
		if err != nil {
			return nil, err
		}
		block.text.WriteString(value)
	case "tool_use":
		if anthropicExactKeys(object, "type", "id", "name", "input") != nil {
			return nil, errors.New("invalid Anthropic tool-use block start")
		}
		block.id, err = anthropicString(object, "id", 256, true)
		if err == nil {
			block.name, err = anthropicString(object, "name", 256, true)
		}
		var input map[string]json.RawMessage
		if err != nil || !providerToolName.MatchString(block.name) || decodeProviderJSON(object["input"], &input) != nil || input == nil {
			return nil, errors.New("invalid Anthropic tool-use identity or initial input")
		}
		if len(input) != 0 {
			block.inputPrepopulated = true
			block.input.Write(object["input"])
		}
	case "thinking":
		if anthropicExactKeys(object, "type", "thinking", "signature") != nil {
			return nil, errors.New("invalid Anthropic thinking block start")
		}
		thinking, thinkingErr := anthropicString(object, "thinking", maximumSSEBytes, false)
		signature, signatureErr := anthropicString(object, "signature", maximumSSEBytes, false)
		if thinkingErr != nil || signatureErr != nil {
			return nil, errors.New("invalid Anthropic thinking block start")
		}
		block.thinking.WriteString(thinking)
		block.signature = signature
	case "redacted_thinking":
		if anthropicExactKeys(object, "type", "data") != nil {
			return nil, errors.New("invalid Anthropic redacted-thinking block")
		}
		block.redacted, err = anthropicString(object, "data", maximumSSEBytes, false)
		if err != nil || block.redacted == "" {
			return nil, errors.New("invalid Anthropic redacted-thinking data")
		}
	default:
		return nil, errors.New("unsupported Anthropic content block")
	}
	return block, nil
}

func accumulateAnthropicDelta(block *anthropicBlock, raw []byte, maxBytes int) error {
	if block.stopped || block.kind == "redacted_thinking" {
		return errors.New("Anthropic delta follows completed or opaque block")
	}
	object, err := anthropicObject(raw)
	if err != nil {
		return err
	}
	deltaType, err := anthropicString(object, "type", 64, true)
	if err != nil {
		return err
	}
	var destination *strings.Builder
	var field string
	switch deltaType {
	case "text_delta":
		if block.kind != "text" || anthropicExactKeys(object, "type", "text") != nil {
			return errors.New("Anthropic text delta differs from block type")
		}
		destination, field = &block.text, "text"
	case "input_json_delta":
		if block.kind != "tool_use" || block.inputPrepopulated || anthropicExactKeys(object, "type", "partial_json") != nil {
			return errors.New("Anthropic input delta differs from block type")
		}
		destination, field = &block.input, "partial_json"
	case "thinking_delta":
		if block.kind != "thinking" || block.signature != "" || anthropicExactKeys(object, "type", "thinking") != nil {
			return errors.New("invalid Anthropic thinking delta")
		}
		destination, field = &block.thinking, "thinking"
	case "signature_delta":
		if block.kind != "thinking" || block.signature != "" || anthropicExactKeys(object, "type", "signature") != nil {
			return errors.New("invalid Anthropic signature delta")
		}
		block.signature, err = anthropicString(object, "signature", maxBytes, false)
		if err != nil || block.signature == "" {
			return errors.New("invalid Anthropic thinking signature")
		}
		return nil
	default:
		return errors.New("unsupported Anthropic content delta")
	}
	value, err := anthropicString(object, field, maxBytes, false)
	if err != nil || destination.Len() > maxBytes-len(value) {
		return errors.New("Anthropic content block exceeds bound")
	}
	destination.WriteString(value)
	return nil
}

func decodeAnthropicUsage(raw []byte, initial bool, previous anthropicUsageState) (anthropicUsageState, error) {
	object, err := anthropicObject(raw)
	if err != nil || !anthropicAllowedKeys(object, "input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "cache_creation", "output_tokens_details", "service_tier", "inference_geo") {
		return previous, errors.New("invalid Anthropic usage object")
	}
	for _, key := range []string{"service_tier", "inference_geo"} {
		if _, ok := object[key]; ok {
			if _, err := anthropicString(object, key, 128, true); err != nil {
				return previous, errors.New("invalid Anthropic usage classification")
			}
		}
	}
	if initial {
		input, ok, err := anthropicOptionalInteger(object, "input_tokens")
		if err != nil || !ok {
			return previous, errors.New("Anthropic initial input usage missing")
		}
		previous.input = &input
	}
	if value, ok, err := anthropicOptionalInteger(object, "input_tokens"); err != nil || ok && previous.input != nil && value < *previous.input {
		return previous, errors.New("invalid or regressing Anthropic input usage")
	} else if ok {
		previous.input = &value
	}
	output, ok, err := anthropicOptionalInteger(object, "output_tokens")
	if err != nil || !ok || previous.output != nil && output < *previous.output {
		return previous, errors.New("invalid or regressing Anthropic output usage")
	}
	previous.output = &output
	for key, destination := range map[string]**int64{"cache_read_input_tokens": &previous.cacheRead, "cache_creation_input_tokens": &previous.cacheWrite} {
		value, present, err := anthropicOptionalInteger(object, key)
		if err != nil || present && *destination != nil && value < **destination {
			return previous, errors.New("invalid or regressing Anthropic cache usage")
		}
		if present {
			copy := value
			*destination = &copy
		}
	}
	if breakdown, ok := object["cache_creation"]; ok && !bytes.Equal(breakdown, []byte("null")) {
		breakdownObject, err := anthropicObject(breakdown)
		if err != nil || anthropicExactKeys(breakdownObject, "ephemeral_5m_input_tokens", "ephemeral_1h_input_tokens") != nil {
			return previous, errors.New("invalid Anthropic cache creation breakdown")
		}
		fiveMinutes, fivePresent, fiveErr := anthropicOptionalInteger(breakdownObject, "ephemeral_5m_input_tokens")
		oneHour, hourPresent, hourErr := anthropicOptionalInteger(breakdownObject, "ephemeral_1h_input_tokens")
		if fiveErr != nil || hourErr != nil || !fivePresent || !hourPresent || fiveMinutes > maximumProviderTokens-oneHour || previous.cacheWrite == nil || fiveMinutes+oneHour != *previous.cacheWrite {
			return previous, errors.New("Anthropic cache creation breakdown differs from total")
		}
	}
	if details, ok := object["output_tokens_details"]; ok && !bytes.Equal(details, []byte("null")) {
		detailObject, err := anthropicObject(details)
		if err != nil || !anthropicAllowedKeys(detailObject, "thinking_tokens") {
			return previous, errors.New("invalid Anthropic output usage details")
		}
		value, present, err := anthropicOptionalInteger(detailObject, "thinking_tokens")
		if err != nil || present && previous.reasoning != nil && value < *previous.reasoning {
			return previous, errors.New("invalid or regressing Anthropic thinking usage")
		}
		if present {
			previous.reasoning = &value
		}
	}
	return previous, nil
}

func finishAnthropicObservation(observation *AnthropicMessagesObservation, blocks []*anthropicBlock, state anthropicUsageState, maxTotal int64) error {
	input, output := *state.input, *state.output
	if state.cacheRead != nil {
		if input > maximumProviderTokens-*state.cacheRead {
			return errors.New("Anthropic inclusive input usage overflows")
		}
		input += *state.cacheRead
	}
	if state.cacheWrite != nil {
		if input > maximumProviderTokens-*state.cacheWrite {
			return errors.New("Anthropic inclusive input usage overflows")
		}
		input += *state.cacheWrite
	}
	if input > maximumProviderTokens-output || input+output > maxTotal || state.reasoning != nil && *state.reasoning > output {
		return errors.New("Anthropic total usage differs or exceeds bound")
	}
	observation.Usage = Usage{InputTokens: input, OutputTokens: output, CacheReadTokens: cloneInt64Pointer(state.cacheRead), CacheWriteTokens: cloneInt64Pointer(state.cacheWrite), ReasoningTokens: cloneInt64Pointer(state.reasoning)}
	observation.TotalTokens = input + output
	// Anthropic input_tokens excludes both cache categories. Without both
	// category fields the inclusive input total is only a reported lower bound.
	observation.UsageComplete = state.cacheRead != nil && state.cacheWrite != nil
	observation.ToolUses = []AnthropicToolUse{}
	observation.Thinking = []AnthropicThinking{}
	observation.RedactedThinking = []AnthropicRedactedThinking{}
	var text strings.Builder
	for index, block := range blocks {
		switch block.kind {
		case "text":
			text.WriteString(block.text.String())
		case "tool_use":
			observation.ToolUses = append(observation.ToolUses, AnthropicToolUse{Index: index, ID: block.id, Name: block.name, Input: append(json.RawMessage(nil), []byte(block.input.String())...)})
		case "thinking":
			observation.Thinking = append(observation.Thinking, AnthropicThinking{Index: index, Thinking: block.thinking.String(), Signature: block.signature})
		case "redacted_thinking":
			observation.RedactedThinking = append(observation.RedactedThinking, AnthropicRedactedThinking{Index: index, Data: block.redacted})
		}
	}
	observation.OutputText = text.String()
	return nil
}

func anthropicBoundBlock(object map[string]json.RawMessage, blocks []*anthropicBlock) (*anthropicBlock, error) {
	index, err := anthropicIndex(object, "index")
	if err != nil || index >= len(blocks) || blocks[index].stopped {
		return nil, errors.New("invalid Anthropic content block index")
	}
	return blocks[index], nil
}

func allAnthropicBlocksStopped(blocks []*anthropicBlock) bool {
	for _, block := range blocks {
		if !block.stopped {
			return false
		}
	}
	return true
}

func anthropicObject(raw []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if decodeProviderJSON(raw, &object) != nil || object == nil {
		return nil, errors.New("invalid Anthropic JSON object")
	}
	return object, nil
}

func anthropicExactKeys(object map[string]json.RawMessage, keys ...string) error {
	if len(object) != len(keys) {
		return errors.New("Anthropic object has unsupported fields")
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return errors.New("Anthropic object field missing")
		}
	}
	return nil
}

func anthropicAllowedKeys(object map[string]json.RawMessage, keys ...string) bool {
	allowed := map[string]bool{}
	for _, key := range keys {
		allowed[key] = true
	}
	for key := range object {
		if !allowed[key] {
			return false
		}
	}
	return true
}

func anthropicAllowedRequiredKeys(object map[string]json.RawMessage, required []string, optional ...string) bool {
	allowed := append(append([]string(nil), required...), optional...)
	if !anthropicAllowedKeys(object, allowed...) {
		return false
	}
	for _, key := range required {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}

func validateAnthropicRefusalDetails(raw []byte, maxBytes int) error {
	object, err := anthropicObject(raw)
	if err != nil || anthropicExactKeys(object, "type", "category", "explanation", "recommended_model") != nil || !responsesLiteral(object["type"], "refusal") {
		return errors.New("invalid Anthropic refusal details")
	}
	for _, key := range []string{"category", "recommended_model"} {
		if _, err := anthropicString(object, key, 256, true); err != nil {
			return err
		}
	}
	_, err = anthropicString(object, "explanation", maxBytes, false)
	return err
}

func anthropicString(object map[string]json.RawMessage, key string, limit int, identity bool) (string, error) {
	raw, ok := object[key]
	var value string
	if !ok || decodeProviderJSON(raw, &value) != nil || len(value) > limit || !utf8.ValidString(value) || identity && !validProviderIdentity(value, limit) {
		return "", errors.New("invalid Anthropic string")
	}
	return value, nil
}

func anthropicOptionalInteger(object map[string]json.RawMessage, key string) (int64, bool, error) {
	raw, ok := object[key]
	if !ok || bytes.Equal(raw, []byte("null")) {
		return 0, false, nil
	}
	var value int64
	if decodeProviderJSON(raw, &value) != nil || value < 0 || value > maximumProviderTokens {
		return 0, false, errors.New("invalid Anthropic usage integer")
	}
	return value, true, nil
}

func anthropicIndex(object map[string]json.RawMessage, key string) (int, error) {
	value, ok, err := anthropicOptionalInteger(object, key)
	if err != nil || !ok || value >= 512 {
		return 0, errors.New("invalid Anthropic content block index")
	}
	return int(value), nil
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
