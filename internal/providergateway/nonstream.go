package providergateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

type chatCompletionJSON struct {
	ID                string           `json:"id"`
	Object            string           `json:"object"`
	Created           *int64           `json:"created"`
	Model             string           `json:"model"`
	Choices           []chatJSONChoice `json:"choices"`
	Usage             json.RawMessage  `json:"usage"`
	SystemFingerprint json.RawMessage  `json:"system_fingerprint,omitempty"`
	ServiceTier       json.RawMessage  `json:"service_tier,omitempty"`
}

type chatJSONChoice struct {
	Index        *int64          `json:"index"`
	Message      json.RawMessage `json:"message"`
	FinishReason json.RawMessage `json:"finish_reason"`
	Logprobs     json.RawMessage `json:"logprobs,omitempty"`
}

type chatJSONMessage struct {
	Role        string          `json:"role"`
	Content     json.RawMessage `json:"content"`
	ToolCalls   json.RawMessage `json:"tool_calls,omitempty"`
	Refusal     json.RawMessage `json:"refusal,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

type chatJSONToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function chatJSONToolFunction `json:"function"`
}

type chatJSONToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ParseChatCompletionJSON admits one complete non-streaming Chat Completions
// response and projects the same final observation as the SSE decoder.
func ParseChatCompletionJSON(raw []byte, maxBytes int, maxTotalTokens int64) (SSEObservation, error) {
	var observation SSEObservation
	if err := validateProviderJSONBounds(raw, maxBytes, maxTotalTokens); err != nil {
		return observation, err
	}
	var wire chatCompletionJSON
	if decodeProviderJSON(raw, &wire) != nil || wire.Created == nil || wire.Object != "chat.completion" || !validProviderIdentity(wire.ID, 256) || !validProviderIdentity(wire.Model, 256) || *wire.Created < 0 || *wire.Created > maximumProviderTokens || len(wire.Choices) != 1 || len(wire.Usage) == 0 {
		return observation, errors.New("invalid provider chat completion")
	}
	for _, optional := range []json.RawMessage{wire.SystemFingerprint, wire.ServiceTier} {
		if !isMissingOrNull(optional) {
			var value string
			if decodeProviderJSON(optional, &value) != nil || !validProviderIdentity(value, 256) {
				return observation, errors.New("invalid provider chat metadata")
			}
		}
	}
	choice := wire.Choices[0]
	if choice.Index == nil || *choice.Index != 0 || len(choice.Message) == 0 || !isMissingOrNull(choice.Logprobs) {
		return observation, errors.New("unsupported provider chat choice")
	}
	finish, present, err := decodeFinishReason(choice.FinishReason)
	if err != nil || !present {
		return observation, errors.New("provider chat completion lacks supported finish")
	}
	var message chatJSONMessage
	if decodeProviderJSON(choice.Message, &message) != nil || message.Role != "assistant" || len(message.Content) == 0 || !isMissingOrNull(message.Refusal) || len(message.Annotations) != 0 && !isEmptyJSONArray(message.Annotations) {
		return observation, errors.New("invalid provider chat message")
	}
	text := ""
	if !bytes.Equal(message.Content, []byte("null")) {
		if decodeProviderJSON(message.Content, &text) != nil || len(text) > maxBytes || !utf8.ValidString(text) {
			return observation, errors.New("invalid provider chat content")
		}
	}
	tools, err := decodeChatJSONToolCalls(message.ToolCalls, maxBytes)
	if err != nil || text != "" && len(tools) != 0 || finish == "stop" && len(tools) != 0 || finish == "tool_calls" && len(tools) == 0 {
		return observation, errors.New("provider chat finish differs from content")
	}
	usage, err := decodeProviderUsage(wire.Usage, maxTotalTokens)
	if err != nil {
		return observation, err
	}
	hash := sha256.Sum256(raw)
	return SSEObservation{SHA256: hex.EncodeToString(hash[:]), SizeBytes: len(raw), ResponseID: wire.ID, Model: wire.Model, FinishReason: finish, Usage: usage, OutputText: text, ToolCalls: tools}, nil
}

func decodeChatJSONToolCalls(raw []byte, maxBytes int) ([]ToolCall, error) {
	if isMissingOrNull(raw) {
		return []ToolCall{}, nil
	}
	var calls []chatJSONToolCall
	if decodeProviderJSON(raw, &calls) != nil || len(calls) == 0 || len(calls) > 128 {
		return nil, errors.New("invalid provider chat tool calls")
	}
	result := make([]ToolCall, len(calls))
	seen := map[string]bool{}
	for index, call := range calls {
		arguments := []byte(call.Function.Arguments)
		var object map[string]json.RawMessage
		if !validProviderIdentity(call.ID, 256) || seen[call.ID] || call.Type != "function" || !providerToolName.MatchString(call.Function.Name) || len(call.Function.Name) > 256 || len(arguments) < 2 || len(arguments) > maxBytes || !uniqueProviderJSON(arguments) || json.Unmarshal(arguments, &object) != nil || object == nil {
			return nil, errors.New("invalid provider chat tool call")
		}
		seen[call.ID] = true
		result[index] = ToolCall{Index: index, ID: call.ID, Name: call.Function.Name, Arguments: append(json.RawMessage(nil), arguments...)}
	}
	return result, nil
}

// ParseResponsesJSON admits one complete non-streaming Responses object.
func ParseResponsesJSON(raw []byte, maxBytes int, maxTotalTokens int64) (ResponsesObservation, error) {
	observation := responsesBaseObservation(raw)
	if err := validateProviderJSONBounds(raw, maxBytes, maxTotalTokens); err != nil {
		return observation, err
	}
	object, err := responsesObject(raw)
	if err != nil || !missingOrNullResponsesField(object, "error") || !missingOrNullResponsesField(object, "incomplete_details") || !missingOrFalseResponsesField(object, "background") {
		return observation, errors.New("invalid completed provider Responses state")
	}
	snapshot, err := decodeResponsesSnapshot(raw)
	if err != nil || snapshot.status != "completed" || !snapshot.usagePresent {
		return observation, errors.New("invalid completed provider Responses object")
	}
	for _, item := range snapshot.output {
		if item.kind == "message" && item.phase.present {
			return observation, errors.New("unsupported provider Responses JSON message phase")
		}
	}
	usage, total, err := decodeResponsesUsage(snapshot.usage, maxTotalTokens)
	if err != nil {
		return observation, err
	}
	observation.ResponseID, observation.Model, observation.Status = snapshot.id, snapshot.model, snapshot.status
	observation.Usage, observation.TotalTokens, observation.UsageComplete = usage, total, true
	observation.FunctionCalls = []ResponsesFunctionCall{}
	observation.Reasoning = []ResponsesReasoning{}
	observation.TextOutputs = []ResponsesTextOutput{}
	seenItems, seenCalls := map[string]bool{}, map[string]bool{}
	var text strings.Builder
	for index, item := range snapshot.output {
		if seenItems[item.id] || item.callID != "" && seenCalls[item.callID] {
			return responsesBaseObservation(raw), errors.New("duplicate provider Responses item identity")
		}
		seenItems[item.id] = true
		if item.callID != "" {
			seenCalls[item.callID] = true
		}
		switch item.kind {
		case "message":
			value := item.text.String()
			text.WriteString(value)
			observation.TextOutputs = append(observation.TextOutputs, ResponsesTextOutput{Index: index, ItemID: item.id, Text: value})
		case "function_call":
			arguments := []byte(item.arguments.String())
			var object map[string]json.RawMessage
			if !uniqueProviderJSON(arguments) || json.Unmarshal(arguments, &object) != nil || object == nil {
				return responsesBaseObservation(raw), errors.New("invalid provider Responses function arguments")
			}
			observation.FunctionCalls = append(observation.FunctionCalls, ResponsesFunctionCall{Index: index, ItemID: item.id, CallID: item.callID, Name: item.name, Arguments: append(json.RawMessage(nil), arguments...)})
		case "reasoning":
			summary := make([]string, len(item.summary))
			for i := range item.summary {
				summary[i] = item.summary[i].String()
			}
			observation.Reasoning = append(observation.Reasoning, ResponsesReasoning{Index: index, ItemID: item.id, Summary: summary, TerminalEncryptedContent: cloneStringPointer(item.doneEncrypted)})
		default:
			return responsesBaseObservation(raw), errors.New("unsupported provider Responses output item")
		}
	}
	observation.OutputText = text.String()
	observation.FinishReason = "stop"
	if len(observation.FunctionCalls) != 0 {
		observation.FinishReason = "tool_calls"
	}
	return observation, nil
}

func missingOrNullResponsesField(object map[string]json.RawMessage, key string) bool {
	raw, ok := object[key]
	return !ok || bytes.Equal(raw, []byte("null"))
}

func missingOrFalseResponsesField(object map[string]json.RawMessage, key string) bool {
	raw, ok := object[key]
	return !ok || bytes.Equal(raw, []byte("false"))
}

// ParseAnthropicMessagesJSON admits one complete non-streaming Messages object.
func ParseAnthropicMessagesJSON(raw []byte, maxBytes int, maxTotalTokens int64) (AnthropicMessagesObservation, error) {
	observation := anthropicBaseObservation(raw)
	if err := validateProviderJSONBounds(raw, maxBytes, maxTotalTokens); err != nil {
		return observation, err
	}
	object, err := anthropicObject(raw)
	if err != nil || anthropicExactKeys(object, "id", "type", "role", "content", "model", "stop_reason", "stop_sequence", "usage") != nil || !responsesLiteral(object["type"], "message") || !responsesLiteral(object["role"], "assistant") {
		return observation, errors.New("invalid Anthropic Messages object")
	}
	observation.MessageID, err = anthropicString(object, "id", 256, true)
	if err == nil {
		observation.Model, err = anthropicString(object, "model", 256, true)
	}
	if err == nil {
		observation.StopReason, err = anthropicString(object, "stop_reason", 64, true)
	}
	if err != nil || !bytes.Equal(object["stop_sequence"], []byte("null")) && !validAnthropicStopSequence(object["stop_sequence"]) {
		return observation, errors.New("invalid Anthropic Messages identity or stop")
	}
	var content []json.RawMessage
	if decodeProviderJSON(object["content"], &content) != nil || content == nil || len(content) > 128 {
		return observation, errors.New("invalid Anthropic Messages content")
	}
	blocks := make([]*anthropicBlock, 0, len(content))
	seen := map[string]bool{}
	for _, rawBlock := range content {
		block, err := decodeAnthropicBlockStart(rawBlock)
		if err != nil || block.id != "" && seen[block.id] {
			return observation, errors.New("invalid Anthropic Messages content block")
		}
		if block.id != "" {
			seen[block.id] = true
		}
		if block.kind == "tool_use" {
			blockObject, _ := anthropicObject(rawBlock)
			block.input.Reset()
			block.input.Write(blockObject["input"])
			block.inputPrepopulated = true
		}
		block.stopped = true
		blocks = append(blocks, block)
	}
	usage, err := decodeAnthropicUsage(object["usage"], true, anthropicUsageState{})
	if err != nil || usage.input == nil || usage.output == nil {
		return observation, errors.New("invalid Anthropic Messages usage")
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

func validateProviderJSONBounds(raw []byte, maxBytes int, maxTotalTokens int64) error {
	if maxBytes < 1 || maxBytes > maximumSSEBytes || len(raw) == 0 || len(raw) > maxBytes || maxTotalTokens < 1 || maxTotalTokens > maximumProviderTokens || !utf8.Valid(raw) || bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) || !uniqueProviderJSON(raw) {
		return errors.New("invalid provider JSON bounds")
	}
	return nil
}

func isEmptyJSONArray(raw []byte) bool {
	var values []json.RawMessage
	return decodeProviderJSON(raw, &values) == nil && len(values) == 0
}

func validAnthropicStopSequence(raw []byte) bool {
	var value string
	return decodeProviderJSON(raw, &value) == nil && len(value) <= 1024 && utf8.ValidString(value)
}
