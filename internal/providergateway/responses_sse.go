package providergateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ResponsesFunctionCall is one completed Responses function-call output item.
// ItemID and CallID are separate protocol identities; Arguments is retained as
// the exact, unambiguous JSON object needed to continue a tool loop.
type ResponsesFunctionCall struct {
	Index     int             `json:"index"`
	ItemID    string          `json:"item_id"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ResponsesTextOutput binds reconstructed text to its provider output item.
type ResponsesTextOutput struct {
	Index  int             `json:"index"`
	ItemID string          `json:"item_id"`
	Text   string          `json:"text"`
	Phase  json.RawMessage `json:"phase,omitempty"`
}

// ResponsesReasoning retains both encrypted snapshots because current
// Responses streams can legitimately replace encrypted_content between the
// output-item done event and the terminal response snapshot. Adapter policy,
// rather than this wire decoder, selects the continuation representation.
type ResponsesReasoning struct {
	Index                    int      `json:"index"`
	ItemID                   string   `json:"item_id"`
	Summary                  []string `json:"summary"`
	DoneEncryptedContent     *string  `json:"done_encrypted_content"`
	TerminalEncryptedContent *string  `json:"terminal_encrypted_content"`
}

// ResponsesObservation records bounded wire evidence from an OpenAI Responses
// stream. UsageComplete distinguishes unavailable accounting from real zeros.
type ResponsesObservation struct {
	SHA256           string                     `json:"sha256"`
	SizeBytes        int                        `json:"size_bytes"`
	ResponseID       string                     `json:"response_id"`
	Model            string                     `json:"model"`
	Status           string                     `json:"status"`
	FinishReason     string                     `json:"finish_reason"`
	UsageComplete    bool                       `json:"usage_complete"`
	Usage            Usage                      `json:"usage"`
	TotalTokens      int64                      `json:"total_tokens"`
	OutputText       string                     `json:"output_text"`
	TextOutputs      []ResponsesTextOutput      `json:"text_outputs"`
	FunctionCalls    []ResponsesFunctionCall    `json:"function_calls"`
	Reasoning        []ResponsesReasoning       `json:"reasoning"`
	TrailingCostPing *ResponsesTrailingCostPing `json:"trailing_cost_ping,omitempty"`
}

// ResponsesTrailingCostPing is provider-extension metadata. CostDecimal is
// retained exactly and is never added to token usage or treated as billing
// evidence by this decoder.
type ResponsesTrailingCostPing struct {
	CostDecimal string `json:"cost_decimal"`
}

// ResponsesSSEOptions selects explicitly contracted stream extensions.
type ResponsesSSEOptions struct {
	RequireTrailingCostPingV1 bool `json:"require_trailing_cost_ping_v1"`
	ContentPartCompletesText  bool `json:"content_part_completes_text"`
	FunctionCallDoneNameV1    bool `json:"function_call_done_name_v1"`
}

var responsesCostDecimal = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)

// ResponsesTerminalError identifies an explicit provider terminal without
// retaining or returning provider-controlled error prose.
type ResponsesTerminalError struct{ Status string }

// Error reports the terminal category without exposing upstream error text.
func (e *ResponsesTerminalError) Error() string {
	if e != nil {
		switch e.Status {
		case "failed", "incomplete", "error":
			return "provider Responses stream ended with " + e.Status
		}
	}
	return "provider Responses stream ended unsuccessfully"
}

type responsesEvent struct {
	name string
	data []byte
}

type responsesItemBuilder struct {
	kind, id, callID, name string
	text                   strings.Builder
	phase                  responsesPhase
	contentAdded           bool
	contentDone            bool
	textDone               bool
	arguments              strings.Builder
	argumentsDone          bool
	summary                []strings.Builder
	summaryDone            []bool
	done                   bool
	doneEncrypted          *string
	terminalEncrypted      *string
}

// responsesPhase preserves whether the optional provider phase was absent,
// explicitly null, or one of the two typed assistant phases. The raw output
// form keeps legacy observations unchanged while retaining explicit null.
type responsesPhase struct {
	present bool
	null    bool
	value   string
}

func (p responsesPhase) raw() json.RawMessage {
	if !p.present {
		return nil
	}
	if p.null {
		return json.RawMessage("null")
	}
	return json.RawMessage(`"` + p.value + `"`)
}

// ParseResponsesSSE strictly admits one complete Responses SSE stream for the
// text, function-tool and reasoning workflow. A completed response is the only
// successful terminal. The derived finish reason is a local topology label:
// tool_calls when function calls exist, otherwise stop.
func ParseResponsesSSE(raw []byte, maxBytes int, maxTotalTokens int64) (ResponsesObservation, error) {
	return ParseResponsesSSEWithOptions(raw, maxBytes, maxTotalTokens, ResponsesSSEOptions{})
}

// ParseResponsesSSEWithOptions applies the same strict core decoder while
// admitting only explicitly selected, exact stream extensions.
func ParseResponsesSSEWithOptions(raw []byte, maxBytes int, maxTotalTokens int64, options ResponsesSSEOptions) (ResponsesObservation, error) {
	observation := responsesBaseObservation(raw)
	if maxBytes < 1 || maxBytes > maximumSSEBytes || len(raw) == 0 || len(raw) > maxBytes || maxTotalTokens < 1 || maxTotalTokens > maximumProviderTokens || !utf8.Valid(raw) || bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		return observation, errors.New("invalid provider Responses SSE bounds")
	}
	events, err := splitResponsesSSE(raw)
	if err != nil || len(events) < 3 || len(events) > maximumSSEEvents {
		if err == nil {
			err = errors.New("provider Responses SSE is incomplete")
		}
		return observation, err
	}
	var trailingCost string
	trailingCostPresent := false
	if options.RequireTrailingCostPingV1 {
		if len(events) > 0 && events[len(events)-1].name == "ping" {
			trailingCostPresent = true
			cost, err := decodeResponsesTrailingCostPing(events[len(events)-1].data)
			if err != nil {
				return observation, err
			}
			trailingCost = cost
			events = events[:len(events)-1]
		}
	}

	items := []*responsesItemBuilder{}
	created, inProgress, terminal := false, false, false
	lastSequence := int64(-1)
	var responseCreatedAt int64
	for _, event := range events {
		if terminal {
			return observation, errors.New("provider data follows Responses terminal")
		}
		object, err := responsesObject(event.data)
		if err != nil {
			return observation, err
		}
		typeName, err := requiredResponsesString(object, "type", 256)
		if err != nil || typeName != event.name {
			return observation, errors.New("provider Responses event/data type mismatch")
		}
		sequence, err := requiredResponsesInteger(object, "sequence_number")
		if err != nil || sequence <= lastSequence {
			return observation, errors.New("provider Responses sequence is not strictly increasing")
		}
		lastSequence = sequence

		switch typeName {
		case "response.created", "response.in_progress":
			if err := exactResponsesKeys(object, "type", "response", "sequence_number"); err != nil {
				return observation, err
			}
			response, err := decodeResponsesSnapshot(object["response"])
			if err != nil || response.status != "in_progress" || len(response.output) != 0 || response.usagePresent {
				return observation, errors.New("invalid provider Responses lifecycle snapshot")
			}
			if typeName == "response.created" {
				if created || inProgress {
					return observation, errors.New("duplicate provider Responses created event")
				}
				created, responseCreatedAt = true, response.createdAt
				observation.ResponseID, observation.Model = response.id, response.model
			} else {
				if !created || inProgress || response.id != observation.ResponseID || response.model != observation.Model || response.createdAt != responseCreatedAt {
					return observation, errors.New("invalid provider Responses in-progress identity")
				}
				inProgress = true
			}

		case "response.output_item.added":
			if !inProgress || exactResponsesKeys(object, "type", "output_index", "item", "sequence_number") != nil {
				return observation, errors.New("invalid provider Responses output item addition")
			}
			index, err := requiredResponsesIndex(object, "output_index")
			if err != nil || index != len(items) {
				return observation, errors.New("non-contiguous provider Responses output index")
			}
			item, err := decodeResponsesOutputItem(object["item"], false)
			if err != nil {
				return observation, err
			}
			for _, previous := range items {
				if previous.id == item.id || item.callID != "" && previous.callID == item.callID {
					return observation, errors.New("duplicate provider Responses item identity")
				}
			}
			items = append(items, item)

		case "response.content_part.added":
			builder, content, err := responsesContentEvent(object, items, true)
			if err != nil || builder.kind != "message" || builder.contentAdded || builder.text.Len() != 0 || builder.textDone || content != "" {
				return observation, errors.New("invalid provider Responses content-part addition")
			}
			builder.contentAdded = true

		case "response.output_text.delta":
			if !responsesAllowedRequiredKeys(object, []string{"type", "content_index", "delta", "item_id", "output_index", "sequence_number"}, "logprobs", "obfuscation") {
				return observation, errors.New("invalid provider Responses text delta shape")
			}
			builder, err := responsesBoundItem(object, items, "message")
			if err != nil || !builder.contentAdded || builder.contentDone || builder.textDone || !responsesZeroIndex(object, "content_index") || !responsesOptionalEmptyArray(object["logprobs"]) || !responsesOptionalString(object["obfuscation"], maxBytes) {
				return observation, errors.New("invalid provider Responses text delta")
			}
			delta, err := requiredResponsesString(object, "delta", maxBytes)
			if err != nil || builder.text.Len() > maxBytes-len(delta) {
				return observation, errors.New("provider Responses text exceeds bound")
			}
			builder.text.WriteString(delta)

		case "response.output_text.done":
			if err := exactResponsesKeys(object, "type", "content_index", "item_id", "logprobs", "output_index", "sequence_number", "text"); err != nil {
				return observation, err
			}
			builder, err := responsesBoundItem(object, items, "message")
			text, textErr := requiredResponsesString(object, "text", maxBytes)
			if err != nil || textErr != nil || !builder.contentAdded || builder.contentDone || builder.textDone || !responsesZeroIndex(object, "content_index") || !responsesEmptyArray(object["logprobs"]) || text != builder.text.String() {
				return observation, errors.New("provider Responses text done snapshot differs")
			}
			builder.textDone = true

		case "response.content_part.done":
			builder, content, err := responsesContentEvent(object, items, false)
			if err != nil || builder.kind != "message" || !builder.contentAdded || builder.contentDone || (!builder.textDone && !options.ContentPartCompletesText) || content != builder.text.String() {
				return observation, errors.New("provider Responses content-part done snapshot differs")
			}
			builder.textDone = true
			builder.contentDone = true

		case "response.function_call_arguments.delta":
			if !responsesAllowedRequiredKeys(object, []string{"type", "delta", "item_id", "output_index", "sequence_number"}, "obfuscation") {
				return observation, errors.New("invalid provider Responses function arguments delta shape")
			}
			builder, err := responsesBoundItem(object, items, "function_call")
			delta, deltaErr := requiredResponsesString(object, "delta", maxBytes)
			if err != nil || deltaErr != nil || builder.argumentsDone || builder.arguments.Len() > maxBytes-len(delta) || !responsesOptionalString(object["obfuscation"], maxBytes) {
				return observation, errors.New("invalid provider Responses function arguments delta")
			}
			builder.arguments.WriteString(delta)

		case "response.function_call_arguments.done":
			if options.FunctionCallDoneNameV1 {
				if !responsesAllowedRequiredKeys(object, []string{"type", "arguments", "item_id", "output_index", "sequence_number"}, "name") {
					return observation, errors.New("invalid provider Responses function arguments done shape")
				}
			} else if err := exactResponsesKeys(object, "type", "arguments", "item_id", "output_index", "sequence_number"); err != nil {
				return observation, err
			}
			builder, err := responsesBoundItem(object, items, "function_call")
			if err != nil {
				return observation, err
			}
			if options.FunctionCallDoneNameV1 {
				if _, ok := object["name"]; ok {
					name, nameErr := requiredResponsesString(object, "name", 256)
					if nameErr != nil || name != builder.name {
						return observation, errors.New("provider Responses function arguments done name differs")
					}
				}
			}
			arguments, argumentsErr := requiredResponsesString(object, "arguments", maxBytes)
			if err != nil || argumentsErr != nil || builder.argumentsDone || arguments != builder.arguments.String() {
				return observation, errors.New("provider Responses function arguments done snapshot differs")
			}
			builder.argumentsDone = true

		case "response.reasoning_summary_part.added":
			builder, summaryIndex, err := responsesSummaryEvent(object, items, "part", false)
			if err != nil || summaryIndex != len(builder.summary) {
				return observation, errors.New("invalid provider Responses reasoning summary addition")
			}
			builder.summary = append(builder.summary, strings.Builder{})
			builder.summaryDone = append(builder.summaryDone, false)

		case "response.reasoning_summary_text.delta":
			builder, summaryIndex, err := responsesSummaryEvent(object, items, "delta", true)
			if err != nil || summaryIndex >= len(builder.summary) || builder.summaryDone[summaryIndex] {
				return observation, errors.New("invalid provider Responses reasoning summary delta")
			}
			delta, err := requiredResponsesString(object, "delta", maxBytes)
			if err != nil || builder.summary[summaryIndex].Len() > maxBytes-len(delta) {
				return observation, errors.New("provider Responses reasoning summary exceeds bound")
			}
			builder.summary[summaryIndex].WriteString(delta)

		case "response.reasoning_summary_text.done":
			builder, summaryIndex, err := responsesSummaryEvent(object, items, "text", true)
			text, textErr := requiredResponsesString(object, "text", maxBytes)
			if err != nil || textErr != nil || summaryIndex >= len(builder.summary) || builder.summaryDone[summaryIndex] || text != builder.summary[summaryIndex].String() {
				return observation, errors.New("provider Responses reasoning summary text differs")
			}

		case "response.reasoning_summary_part.done":
			builder, summaryIndex, err := responsesSummaryEvent(object, items, "part", false)
			if err != nil || summaryIndex >= len(builder.summary) || builder.summaryDone[summaryIndex] {
				return observation, errors.New("invalid provider Responses reasoning summary completion")
			}
			builder.summaryDone[summaryIndex] = true

		case "response.output_item.done":
			if exactResponsesKeys(object, "type", "output_index", "item", "sequence_number") != nil {
				return observation, errors.New("invalid provider Responses output item completion")
			}
			index, err := requiredResponsesIndex(object, "output_index")
			if err != nil || index >= len(items) || items[index].done {
				return observation, errors.New("invalid provider Responses output item index")
			}
			done, err := decodeResponsesOutputItem(object["item"], true)
			if err != nil || !responsesItemsMatch(items[index], done, true) {
				return observation, errors.New("provider Responses output item done snapshot differs")
			}
			items[index].done, items[index].doneEncrypted = true, done.doneEncrypted

		case "response.completed", "response.incomplete", "response.failed":
			terminal = true
			if exactResponsesKeys(object, "type", "response", "sequence_number") != nil {
				return observation, errors.New("invalid provider Responses terminal")
			}
			response, err := decodeResponsesSnapshot(object["response"])
			if err != nil || response.id != observation.ResponseID || response.model != observation.Model || response.createdAt != responseCreatedAt || response.status != strings.TrimPrefix(typeName, "response.") {
				return observation, errors.New("provider Responses terminal identity differs")
			}
			observation.Status = response.status
			if response.usagePresent {
				usage, total, err := decodeResponsesUsage(response.usage, maxTotalTokens)
				if err != nil {
					return observation, err
				}
				observation.Usage, observation.TotalTokens, observation.UsageComplete = usage, total, true
			}
			if typeName != "response.completed" {
				return observation, &ResponsesTerminalError{Status: response.status}
			}
			if !response.usagePresent || len(response.output) != len(items) {
				return observation, errors.New("completed provider Responses snapshot is incomplete")
			}
			for index, terminalItem := range response.output {
				if !items[index].done || !responsesItemsMatch(items[index], terminalItem, false) {
					return observation, errors.New("provider Responses terminal output differs")
				}
				items[index].terminalEncrypted = terminalItem.doneEncrypted
			}

		case "error":
			terminal = true
			observation.Status = "error"
			if err := validateResponsesError(object); err != nil {
				return observation, err
			}
			return observation, &ResponsesTerminalError{Status: "error"}

		default:
			return observation, fmt.Errorf("unsupported provider Responses event %q", typeName)
		}
	}
	if !created || !inProgress || !terminal || observation.Status != "completed" {
		return observation, errors.New("provider Responses SSE lacks completed terminal")
	}
	if options.RequireTrailingCostPingV1 && !trailingCostPresent {
		return observation, errors.New("provider Responses stream lacks required trailing cost ping")
	}
	if trailingCostPresent {
		observation.TrailingCostPing = &ResponsesTrailingCostPing{CostDecimal: trailingCost}
	}

	observation.FunctionCalls = []ResponsesFunctionCall{}
	observation.Reasoning = []ResponsesReasoning{}
	observation.TextOutputs = []ResponsesTextOutput{}
	var text strings.Builder
	for index, item := range items {
		switch item.kind {
		case "message":
			value := item.text.String()
			text.WriteString(value)
			observation.TextOutputs = append(observation.TextOutputs, ResponsesTextOutput{Index: index, ItemID: item.id, Text: value, Phase: item.phase.raw()})
		case "function_call":
			arguments := []byte(item.arguments.String())
			if !uniqueProviderJSON(arguments) {
				return responsesBaseObservation(raw), errors.New("ambiguous provider Responses function arguments")
			}
			var argumentObject map[string]json.RawMessage
			if json.Unmarshal(arguments, &argumentObject) != nil || argumentObject == nil {
				return responsesBaseObservation(raw), errors.New("provider Responses function arguments are not an object")
			}
			observation.FunctionCalls = append(observation.FunctionCalls, ResponsesFunctionCall{Index: index, ItemID: item.id, CallID: item.callID, Name: item.name, Arguments: append(json.RawMessage(nil), arguments...)})
		case "reasoning":
			summary := make([]string, len(item.summary))
			for i := range item.summary {
				summary[i] = item.summary[i].String()
			}
			observation.Reasoning = append(observation.Reasoning, ResponsesReasoning{Index: index, ItemID: item.id, Summary: summary, DoneEncryptedContent: cloneStringPointer(item.doneEncrypted), TerminalEncryptedContent: cloneStringPointer(item.terminalEncrypted)})
		}
	}
	observation.OutputText = text.String()
	if len(observation.FunctionCalls) != 0 {
		observation.FinishReason = "tool_calls"
	} else {
		for _, item := range items {
			if item.kind == "message" && item.phase.present && !item.phase.null && item.phase.value == "commentary" {
				return responsesBaseObservation(raw), errors.New("provider Responses commentary text cannot be projected as final response")
			}
		}
		observation.FinishReason = "stop"
	}
	return observation, nil
}

func decodeResponsesTrailingCostPing(raw []byte) (string, error) {
	object, err := responsesObject(raw)
	if err != nil || exactResponsesKeys(object, "type", "cost") != nil || !responsesLiteral(object["type"], "ping") {
		return "", errors.New("invalid provider Responses trailing cost ping")
	}
	cost, err := requiredResponsesString(object, "cost", 64)
	if err != nil || !responsesCostDecimal.MatchString(cost) {
		return "", errors.New("invalid provider Responses trailing cost decimal")
	}
	return cost, nil
}

func responsesBaseObservation(raw []byte) ResponsesObservation {
	hash := sha256.Sum256(raw)
	return ResponsesObservation{SHA256: hex.EncodeToString(hash[:]), SizeBytes: len(raw)}
}

func splitResponsesSSE(raw []byte) ([]responsesEvent, error) {
	if bytes.Contains(raw, []byte{'\r'}) {
		if bytes.Contains(bytes.ReplaceAll(raw, []byte("\r\n"), nil), []byte{'\r'}) {
			return nil, errors.New("invalid provider Responses SSE line ending")
		}
		raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	}
	if !bytes.HasSuffix(raw, []byte("\n\n")) {
		return nil, errors.New("unterminated provider Responses SSE event")
	}
	blocks := bytes.Split(raw[:len(raw)-2], []byte("\n\n"))
	events := make([]responsesEvent, 0, len(blocks))
	for _, block := range blocks {
		lines := bytes.Split(block, []byte{'\n'})
		if len(lines) != 2 || !bytes.HasPrefix(lines[0], []byte("event: ")) || !bytes.HasPrefix(lines[1], []byte("data: ")) {
			return nil, errors.New("unsupported provider Responses SSE field")
		}
		name, data := string(lines[0][7:]), lines[1][6:]
		if !validProviderIdentity(name, 256) || len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			return nil, errors.New("invalid provider Responses SSE event")
		}
		events = append(events, responsesEvent{name: name, data: data})
	}
	return events, nil
}

type decodedResponsesSnapshot struct {
	id, model, status string
	createdAt         int64
	output            []*responsesItemBuilder
	usage             json.RawMessage
	usagePresent      bool
}

var responsesSnapshotKeys = map[string]bool{
	"id": true, "object": true, "created_at": true, "status": true, "background": true, "completed_at": true,
	"error": true, "frequency_penalty": true, "incomplete_details": true, "instructions": true, "max_output_tokens": true,
	"max_tool_calls": true, "model": true, "moderation": true, "output": true, "parallel_tool_calls": true,
	"presence_penalty": true, "previous_response_id": true, "prompt": true, "prompt_cache_key": true,
	"prompt_cache_retention": true, "reasoning": true, "safety_identifier": true, "service_tier": true, "store": true,
	"temperature": true, "text": true, "tool_choice": true, "tools": true, "top_logprobs": true, "top_p": true,
	"truncation": true, "usage": true, "user": true, "metadata": true,
}

func decodeResponsesSnapshot(raw []byte) (decodedResponsesSnapshot, error) {
	var result decodedResponsesSnapshot
	object, err := responsesObject(raw)
	if err != nil {
		return result, err
	}
	for key := range object {
		if !responsesSnapshotKeys[key] {
			return result, errors.New("unsupported provider Responses snapshot field")
		}
	}
	result.id, err = requiredResponsesString(object, "id", 256)
	if err != nil {
		return result, err
	}
	result.model, err = requiredResponsesString(object, "model", 256)
	if err != nil {
		return result, err
	}
	result.status, err = requiredResponsesString(object, "status", 32)
	if err != nil {
		return result, err
	}
	result.createdAt, err = requiredResponsesInteger(object, "created_at")
	if err != nil || result.createdAt < 0 {
		return result, errors.New("invalid provider Responses created_at")
	}
	var objectName string
	if decodeProviderJSON(object["object"], &objectName) != nil || objectName != "response" {
		return result, errors.New("invalid provider Responses object")
	}
	var output []json.RawMessage
	if decodeProviderJSON(object["output"], &output) != nil || output == nil {
		return result, errors.New("invalid provider Responses output")
	}
	for _, itemRaw := range output {
		item, itemErr := decodeResponsesOutputItem(itemRaw, true)
		if itemErr != nil {
			return result, itemErr
		}
		result.output = append(result.output, item)
	}
	if usage, ok := object["usage"]; ok && !bytes.Equal(usage, []byte("null")) {
		result.usage, result.usagePresent = usage, true
	}
	return result, nil
}

func decodeResponsesOutputItem(raw []byte, done bool) (*responsesItemBuilder, error) {
	object, err := responsesObject(raw)
	if err != nil {
		return nil, err
	}
	kind, err := requiredResponsesString(object, "type", 64)
	if err != nil {
		return nil, err
	}
	id, err := requiredResponsesString(object, "id", 256)
	if err != nil {
		return nil, err
	}
	builder := &responsesItemBuilder{kind: kind, id: id}
	switch kind {
	case "message":
		if !responsesAllowedRequiredKeys(object, []string{"id", "type", "status", "content", "role"}, "phase") || !responsesLiteral(object["role"], "assistant") || !responsesLiteral(object["status"], map[bool]string{true: "completed", false: "in_progress"}[done]) {
			return nil, errors.New("invalid provider Responses message item")
		}
		phase, err := decodeResponsesPhase(object)
		if err != nil {
			return nil, err
		}
		builder.phase = phase
		var content []json.RawMessage
		if decodeProviderJSON(object["content"], &content) != nil || (!done && len(content) != 0) || done && len(content) != 1 {
			return nil, errors.New("invalid provider Responses message content")
		}
		if done {
			part, err := responsesOutputTextPart(content[0])
			if err != nil {
				return nil, err
			}
			builder.text.WriteString(part)
			builder.textDone = true
			builder.contentAdded = true
			builder.contentDone = true
		}
	case "function_call":
		if exactResponsesKeys(object, "id", "type", "status", "arguments", "call_id", "name") != nil || !responsesLiteral(object["status"], map[bool]string{true: "completed", false: "in_progress"}[done]) {
			return nil, errors.New("invalid provider Responses function-call item")
		}
		builder.callID, err = requiredResponsesString(object, "call_id", 256)
		if err == nil {
			builder.name, err = requiredResponsesString(object, "name", 256)
		}
		arguments, argumentErr := requiredResponsesString(object, "arguments", maximumSSEBytes)
		if err != nil || argumentErr != nil || !providerToolName.MatchString(builder.name) || !done && arguments != "" {
			return nil, errors.New("invalid provider Responses function-call identity")
		}
		builder.arguments.WriteString(arguments)
		builder.argumentsDone = done
	case "reasoning":
		if !responsesAllowedKeys(object, "id", "type", "encrypted_content", "summary", "status") || !responsesOptionalItemStatus(object, done) {
			return nil, errors.New("invalid provider Responses reasoning item")
		}
		if encrypted, ok := object["encrypted_content"]; ok && !bytes.Equal(encrypted, []byte("null")) {
			value := ""
			if decodeProviderJSON(encrypted, &value) != nil || !validProviderIdentity(value, maximumSSEBytes) {
				return nil, errors.New("invalid provider Responses encrypted reasoning")
			}
			builder.doneEncrypted = &value
		}
		var summaries []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if decodeProviderJSON(object["summary"], &summaries) != nil || !done && len(summaries) != 0 {
			return nil, errors.New("invalid provider Responses reasoning summary")
		}
		for _, summary := range summaries {
			if summary.Type != "summary_text" || len(summary.Text) > maximumSSEBytes || !utf8.ValidString(summary.Text) {
				return nil, errors.New("invalid provider Responses reasoning summary part")
			}
			var text strings.Builder
			text.WriteString(summary.Text)
			builder.summary = append(builder.summary, text)
			builder.summaryDone = append(builder.summaryDone, true)
		}
	default:
		return nil, errors.New("unsupported provider Responses output item")
	}
	return builder, nil
}

func responsesItemsMatch(stream, snapshot *responsesItemBuilder, doneEvent bool) bool {
	if stream.kind != snapshot.kind || stream.id != snapshot.id {
		return false
	}
	switch stream.kind {
	case "message":
		return stream.phase == snapshot.phase && stream.contentAdded && stream.contentDone && stream.textDone && stream.text.String() == snapshot.text.String()
	case "function_call":
		return stream.argumentsDone && stream.callID == snapshot.callID && stream.name == snapshot.name && stream.arguments.String() == snapshot.arguments.String()
	case "reasoning":
		if len(stream.summary) != len(snapshot.summary) {
			return false
		}
		for i := range stream.summary {
			if !stream.summaryDone[i] || stream.summary[i].String() != snapshot.summary[i].String() {
				return false
			}
		}
		if doneEvent {
			return true
		}
		return true // encrypted snapshots are intentionally retained independently.
	}
	return false
}

func responsesContentEvent(object map[string]json.RawMessage, items []*responsesItemBuilder, added bool) (*responsesItemBuilder, string, error) {
	if exactResponsesKeys(object, "type", "content_index", "item_id", "output_index", "part", "sequence_number") != nil || !responsesZeroIndex(object, "content_index") {
		return nil, "", errors.New("invalid provider Responses content part")
	}
	builder, err := responsesBoundItem(object, items, "message")
	if err != nil {
		return nil, "", err
	}
	part, err := responsesOutputTextPart(object["part"])
	if err != nil {
		return nil, "", err
	}
	if added && part != "" {
		return nil, "", errors.New("provider Responses initial content is not empty")
	}
	return builder, part, nil
}

func responsesOutputTextPart(raw []byte) (string, error) {
	object, err := responsesObject(raw)
	if err != nil || exactResponsesKeys(object, "type", "annotations", "logprobs", "text") != nil || !responsesLiteral(object["type"], "output_text") || !responsesEmptyArray(object["annotations"]) || !responsesEmptyArray(object["logprobs"]) {
		return "", errors.New("unsupported provider Responses output-text part")
	}
	return requiredResponsesString(object, "text", maximumSSEBytes)
}

func decodeResponsesPhase(object map[string]json.RawMessage) (responsesPhase, error) {
	raw, ok := object["phase"]
	if !ok {
		return responsesPhase{}, nil
	}
	phase := responsesPhase{present: true}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		phase.null = true
		return phase, nil
	}
	var value string
	if decodeProviderJSON(raw, &value) != nil {
		return responsesPhase{}, errors.New("invalid provider Responses message phase")
	}
	if value != "commentary" && value != "final_answer" {
		return responsesPhase{}, errors.New("invalid provider Responses message phase")
	}
	phase.value = value
	return phase, nil
}

func responsesSummaryEvent(object map[string]json.RawMessage, items []*responsesItemBuilder, payload string, hasValue bool) (*responsesItemBuilder, int, error) {
	keys := []string{"type", "item_id", "summary_index", "output_index", "sequence_number"}
	if hasValue {
		keys = append(keys, payload)
	} else if payload == "part" {
		keys = append(keys, "part")
	}
	if exactResponsesKeys(object, keys...) != nil {
		return nil, 0, errors.New("invalid provider Responses reasoning event")
	}
	builder, err := responsesBoundItem(object, items, "reasoning")
	if err != nil {
		return nil, 0, err
	}
	index, err := requiredResponsesIndex(object, "summary_index")
	if err != nil {
		return nil, 0, err
	}
	if !hasValue {
		part, err := responsesObject(object["part"])
		if err != nil || exactResponsesKeys(part, "type", "text") != nil || !responsesLiteral(part["type"], "summary_text") {
			return nil, 0, errors.New("invalid provider Responses reasoning summary part")
		}
		text, err := requiredResponsesString(part, "text", maximumSSEBytes)
		if err != nil || (strings.HasSuffix(requiredType(object), ".added") && text != "") || (strings.HasSuffix(requiredType(object), ".done") && (index >= len(builder.summary) || text != builder.summary[index].String())) {
			return nil, 0, errors.New("provider Responses reasoning part snapshot differs")
		}
	}
	return builder, index, nil
}

func requiredType(object map[string]json.RawMessage) string {
	value, _ := requiredResponsesString(object, "type", 256)
	return value
}

func responsesBoundItem(object map[string]json.RawMessage, items []*responsesItemBuilder, kind string) (*responsesItemBuilder, error) {
	index, err := requiredResponsesIndex(object, "output_index")
	if err != nil || index >= len(items) {
		return nil, errors.New("invalid provider Responses item index")
	}
	id, err := requiredResponsesString(object, "item_id", 256)
	if err != nil || items[index].id != id || items[index].kind != kind || items[index].done {
		return nil, errors.New("provider Responses item identity substitution")
	}
	return items[index], nil
}

func decodeResponsesUsage(raw []byte, maxTotal int64) (Usage, int64, error) {
	object, err := responsesObject(raw)
	if err != nil || exactResponsesKeys(object, "input_tokens", "input_tokens_details", "output_tokens", "output_tokens_details", "total_tokens") != nil {
		return Usage{}, 0, errors.New("invalid provider Responses usage")
	}
	input, inputErr := requiredResponsesInteger(object, "input_tokens")
	output, outputErr := requiredResponsesInteger(object, "output_tokens")
	total, totalErr := requiredResponsesInteger(object, "total_tokens")
	if inputErr != nil || outputErr != nil || totalErr != nil || input < 0 || output < 0 || total < 0 || input > maximumProviderTokens-output || total != input+output || total > maxTotal {
		return Usage{}, 0, errors.New("provider Responses usage total differs or exceeds bound")
	}
	usage := Usage{InputTokens: input, OutputTokens: output}
	if details := object["input_tokens_details"]; !bytes.Equal(details, []byte("null")) {
		detailObject, err := responsesObject(details)
		if err != nil || !responsesAllowedKeys(detailObject, "cached_tokens", "cache_write_tokens") {
			return Usage{}, 0, errors.New("invalid provider Responses input usage details")
		}
		if value, present, err := optionalResponsesInteger(detailObject, "cached_tokens"); err != nil || present && (value < 0 || value > input) {
			return Usage{}, 0, errors.New("invalid provider Responses cached-token subset")
		} else if present {
			usage.CacheReadTokens = &value
		}
		if value, present, err := optionalResponsesInteger(detailObject, "cache_write_tokens"); err != nil || present && (value < 0 || value > input || usage.CacheReadTokens != nil && value > input-*usage.CacheReadTokens) {
			return Usage{}, 0, errors.New("invalid provider Responses cache-write-token subset")
		} else if present {
			usage.CacheWriteTokens = &value
		}
	}
	if details := object["output_tokens_details"]; !bytes.Equal(details, []byte("null")) {
		detailObject, err := responsesObject(details)
		if err != nil || !responsesAllowedKeys(detailObject, "reasoning_tokens") {
			return Usage{}, 0, errors.New("invalid provider Responses output usage details")
		}
		if value, present, err := optionalResponsesInteger(detailObject, "reasoning_tokens"); err != nil || present && (value < 0 || value > output) {
			return Usage{}, 0, errors.New("invalid provider Responses reasoning-token subset")
		} else if present {
			usage.ReasoningTokens = &value
		}
	}
	return usage, total, nil
}

func validateResponsesError(object map[string]json.RawMessage) error {
	if _, nested := object["error"]; nested {
		if exactResponsesKeys(object, "type", "sequence_number", "error") != nil {
			return errors.New("invalid provider Responses error event")
		}
		errorObject, err := responsesObject(object["error"])
		if err != nil || exactResponsesKeys(errorObject, "type", "code", "message", "param") != nil {
			return errors.New("invalid provider Responses error payload")
		}
		return nil
	}
	if exactResponsesKeys(object, "type", "sequence_number", "code", "message", "param") != nil {
		return errors.New("invalid provider Responses error event")
	}
	return nil
}

func responsesObject(raw []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if decodeProviderJSON(raw, &object) != nil || object == nil {
		return nil, errors.New("invalid provider Responses JSON object")
	}
	return object, nil
}

func exactResponsesKeys(object map[string]json.RawMessage, keys ...string) error {
	if len(object) != len(keys) {
		return errors.New("provider Responses object has unsupported fields")
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return errors.New("provider Responses object field missing")
		}
	}
	return nil
}

func responsesAllowedKeys(object map[string]json.RawMessage, keys ...string) bool {
	allowed := make(map[string]bool, len(keys))
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

func responsesAllowedRequiredKeys(object map[string]json.RawMessage, required []string, optional ...string) bool {
	allowed := append(append([]string(nil), required...), optional...)
	if !responsesAllowedKeys(object, allowed...) {
		return false
	}
	for _, key := range required {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}

func requiredResponsesString(object map[string]json.RawMessage, key string, limit int) (string, error) {
	raw, ok := object[key]
	var value string
	if !ok || decodeProviderJSON(raw, &value) != nil || len(value) > limit || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") && (key == "id" || key == "item_id" || key == "call_id" || key == "model" || key == "name" || key == "type") {
		return "", errors.New("invalid provider Responses string")
	}
	if (key == "id" || key == "item_id" || key == "call_id" || key == "model" || key == "name" || key == "type") && !validProviderIdentity(value, limit) {
		return "", errors.New("invalid provider Responses identity")
	}
	return value, nil
}

func requiredResponsesInteger(object map[string]json.RawMessage, key string) (int64, error) {
	raw, ok := object[key]
	var value int64
	if !ok || decodeProviderJSON(raw, &value) != nil || value < 0 || value > maximumProviderTokens {
		return 0, errors.New("invalid provider Responses integer")
	}
	return value, nil
}

func optionalResponsesInteger(object map[string]json.RawMessage, key string) (int64, bool, error) {
	raw, ok := object[key]
	if !ok || bytes.Equal(raw, []byte("null")) {
		return 0, false, nil
	}
	value, err := requiredResponsesInteger(object, key)
	return value, true, err
}

func requiredResponsesIndex(object map[string]json.RawMessage, key string) (int, error) {
	value, err := requiredResponsesInteger(object, key)
	if err != nil || value >= 512 {
		return 0, errors.New("invalid provider Responses index")
	}
	return int(value), nil
}

func responsesLiteral(raw []byte, expected string) bool {
	var value string
	return decodeProviderJSON(raw, &value) == nil && value == expected
}

func responsesOptionalItemStatus(object map[string]json.RawMessage, done bool) bool {
	raw, ok := object["status"]
	if !ok {
		return true
	}
	return responsesLiteral(raw, map[bool]string{true: "completed", false: "in_progress"}[done])
}

func responsesZeroIndex(object map[string]json.RawMessage, key string) bool {
	value, err := requiredResponsesInteger(object, key)
	return err == nil && value == 0
}

func responsesEmptyArray(raw []byte) bool {
	var values []json.RawMessage
	return decodeProviderJSON(raw, &values) == nil && values != nil && len(values) == 0
}

func responsesOptionalEmptyArray(raw []byte) bool {
	return len(raw) == 0 || bytes.Equal(raw, []byte("null")) || responsesEmptyArray(raw)
}

func responsesOptionalString(raw []byte, limit int) bool {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return true
	}
	var value string
	return decodeProviderJSON(raw, &value) == nil && len(value) <= limit && utf8.ValidString(value)
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
