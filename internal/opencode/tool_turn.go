package opencode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/providergateway"
)

const toolTurnMaximumExactInteger = int64(9007199254740991)

// ToolTurnTokens is the sum of distinct assistant-message usage records. Step
// finish parts are checked against those records but are never added again.
type ToolTurnTokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	Reasoning  int64 `json:"reasoning"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

// ToolCallObservation binds one provider tool part to one durable broker
// request and response. ProviderCallID and BrokerCallID are separate identity
// domains and are deliberately retained as separate fields.
type ToolCallObservation struct {
	MessageID       string         `json:"message_id"`
	PartID          string         `json:"part_id"`
	ProviderCallID  string         `json:"provider_call_id"`
	ProviderItemID  ProviderItemID `json:"provider_item_id,omitempty"`
	Tool            string         `json:"tool"`
	BindingID       string         `json:"binding_id"`
	InvocationID    string         `json:"invocation_id"`
	RequestID       string         `json:"request_id"`
	BrokerCallID    string         `json:"broker_call_id"`
	ArgumentsSHA256 string         `json:"arguments_sha256"`
	ContentSHA256   string         `json:"content_sha256"`
}

// ToolTextObservation retains the provider identity and optional Responses
// message phase for a text part. Phase is metadata only; it does not authorize
// a tool effect or change the text projection.
type ToolTextObservation struct {
	ProviderItemID ProviderItemID  `json:"provider_item_id"`
	Phase          json.RawMessage `json:"phase,omitempty"`
}

// ToolGenerationObservation is one provider generation in chronological
// order. Finish is retained because an intermediate provider may report stop
// while its tool parts require OpenCode to continue the loop.
type ToolGenerationObservation struct {
	Assistant            Assistant                        `json:"assistant"`
	Finish               string                           `json:"finish"`
	Calls                []ToolCallObservation            `json:"calls"`
	StructuredOutputTool *StructuredOutputToolObservation `json:"structured_output_tool,omitempty"`
	TextProviderItemIDs  []ProviderItemID                 `json:"text_provider_item_ids,omitempty"`
	TextProviderItems    []ToolTextObservation            `json:"text_provider_items,omitempty"`
	TextSHA256           string                           `json:"text_sha256"`
	Reasoning            []ToolReasoningObservation       `json:"reasoning,omitempty"`
	RuntimeMetadata      []PatchSnapshotReceipt           `json:"runtime_metadata,omitempty"`
}

// ToolReasoningObservation retains the exact opaque state needed to replay a
// pinned OpenAI Responses reasoning item. ProviderItemID is distinct from tool
// call and broker identities; TextSHA256 binds the visible summary text.
type ToolReasoningObservation struct {
	PartID           string         `json:"part_id"`
	ProviderItemID   ProviderItemID `json:"provider_item_id"`
	EncryptedContent string         `json:"encrypted_content"`
	TextSHA256       string         `json:"text_sha256"`
}

// ToolTurnObservation is a validated transcript and broker projection. It has
// no terminal flag: host quiescence, process reaping and broker closure remain
// controller lifecycle evidence outside this decoder.
type ToolTurnObservation struct {
	Final                      Assistant                        `json:"final"`
	Text                       string                           `json:"text"`
	StructuredOutput           *json.RawMessage                 `json:"structured_output,omitempty"`
	StructuredOutputTool       *StructuredOutputToolObservation `json:"structured_output_tool,omitempty"`
	Generations                []ToolGenerationObservation      `json:"generations"`
	Calls                      []ToolCallObservation            `json:"calls"`
	Tokens                     ToolTurnTokens                   `json:"tokens"`
	TranscriptSHA256           string                           `json:"transcript_sha256"`
	BrokerBindingID            string                           `json:"broker_binding_id"`
	BrokerStateID              string                           `json:"broker_state_id"`
	RuntimeMetadata            []PatchSnapshotReceipt           `json:"runtime_metadata,omitempty"`
	RuntimeMetadataExpectation *RuntimeMetadataExpectation      `json:"runtime_metadata_expectation,omitempty"`
}

// ReadToolTurn reads and validates one dedicated tool-loop session. A returned
// observation proves transcript-to-broker consistency, not terminal host state.
func (c *Client) ReadToolTurn(ctx context.Context, b Binding, prompt string, broker contextbroker.State) (ToolTurnObservation, error) {
	return c.ReadToolTurnWithRuntimeMetadata(ctx, b, prompt, broker, nil)
}

// ReadToolTurnWithRuntimeMetadata validates patch snapshot metadata under an
// explicit non-authorizing classification policy.
func (c *Client) ReadToolTurnWithRuntimeMetadata(ctx context.Context, b Binding, prompt string, broker contextbroker.State, expected *RuntimeMetadataExpectation) (ToolTurnObservation, error) {
	if err := validatePrompt(b, prompt); err != nil {
		return ToolTurnObservation{}, err
	}
	raw, err := c.read(ctx, "/session/"+b.SessionID+"/message")
	if err != nil {
		return ToolTurnObservation{}, err
	}
	return decodeToolTurnWithRuntimeMetadata(raw, b, prompt, broker, expected)
}

// ReadToolTurnWithStructuredOutput validates one opt-in native json_schema
// terminal tool result. The expectation is part of the dispatch identity and
// must be supplied again for readback.
func (c *Client) ReadToolTurnWithStructuredOutput(ctx context.Context, b Binding, prompt string, broker contextbroker.State, expectation StructuredOutputExpectation) (ToolTurnObservation, error) {
	return c.ReadToolTurnWithStructuredOutputAndRuntimeMetadata(ctx, b, prompt, broker, expectation, nil)
}

// ReadToolTurnWithStructuredOutputAndRuntimeMetadata combines the structured
// terminal projection with the existing non-authorizing metadata policy.
func (c *Client) ReadToolTurnWithStructuredOutputAndRuntimeMetadata(ctx context.Context, b Binding, prompt string, broker contextbroker.State, expectation StructuredOutputExpectation, expected *RuntimeMetadataExpectation) (ToolTurnObservation, error) {
	if err := validatePrompt(b, prompt); err != nil {
		return ToolTurnObservation{}, err
	}
	if err := expectation.Validate(); err != nil {
		return ToolTurnObservation{}, err
	}
	raw, err := c.read(ctx, "/session/"+b.SessionID+"/message")
	if err != nil {
		return ToolTurnObservation{}, err
	}
	return decodeToolTurnWithStructuredOutputAndRuntimeMetadata(raw, b, prompt, broker, &expectation, expected)
}

type exactTokens struct {
	Total      *int64
	Input      int64
	Output     int64
	Reasoning  int64
	CacheRead  int64
	CacheWrite int64
}

type turnAssistant struct {
	Assistant        Assistant
	Finish           string
	Tokens           exactTokens
	StructuredOutput json.RawMessage
}

type brokerPair struct {
	Request  contextbroker.Request
	Response contextbroker.Response
}

func decodeToolTurn(raw []byte, b Binding, prompt string, broker contextbroker.State) (ToolTurnObservation, error) {
	return decodeToolTurnWithOptions(raw, b, prompt, broker, nil, nil)
}

func decodeToolTurnWithRuntimeMetadata(raw []byte, b Binding, prompt string, broker contextbroker.State, expected *RuntimeMetadataExpectation) (ToolTurnObservation, error) {
	return decodeToolTurnWithOptions(raw, b, prompt, broker, nil, expected)
}

func decodeToolTurnWithStructuredOutput(raw []byte, b Binding, prompt string, broker contextbroker.State, structured *StructuredOutputExpectation) (ToolTurnObservation, error) {
	return decodeToolTurnWithOptions(raw, b, prompt, broker, structured, nil)
}

func decodeToolTurnWithStructuredOutputAndRuntimeMetadata(raw []byte, b Binding, prompt string, broker contextbroker.State, structured *StructuredOutputExpectation, expected *RuntimeMetadataExpectation) (ToolTurnObservation, error) {
	return decodeToolTurnWithOptions(raw, b, prompt, broker, structured, expected)
}

func decodeToolTurnWithOptions(raw []byte, b Binding, prompt string, broker contextbroker.State, structured *StructuredOutputExpectation, expected *RuntimeMetadataExpectation) (ToolTurnObservation, error) {
	var observation ToolTurnObservation
	if structured != nil {
		if err := structured.Validate(); err != nil {
			return observation, err
		}
	}
	if expected != nil {
		copy := *expected
		copy.KnownControllerPaths = append([]string(nil), expected.KnownControllerPaths...)
		observation.RuntimeMetadataExpectation = &copy
	}
	if err := validatePrompt(b, prompt); err != nil {
		return observation, err
	}
	pairs, bindingID, brokerStateID, err := validateToolBrokerState(broker)
	if err != nil {
		return observation, err
	}
	messages, err := decodeTranscript(raw, b.SessionID)
	if err != nil {
		return observation, err
	}
	if len(messages) < 2 {
		return observation, errors.New("tool turn history incomplete")
	}
	var user *TranscriptMessage
	assistants := make([]struct {
		message TranscriptMessage
		decoded turnAssistant
	}, 0, len(messages)-1)
	for i := range messages {
		message := messages[i]
		if message.Role == "user" {
			if user != nil || message.ID != b.ParentID {
				return observation, errors.New("tool turn contains extra user message")
			}
			copy := message
			user = &copy
			continue
		}
		decoded, err := decodeTurnAssistant(message.Info, b)
		if err != nil {
			return observation, err
		}
		if structured == nil && len(decoded.StructuredOutput) != 0 {
			return observation, errors.New("structured output requires an explicit expectation")
		}
		assistants = append(assistants, struct {
			message TranscriptMessage
			decoded turnAssistant
		}{message: message, decoded: decoded})
	}
	if user == nil || len(assistants) < 1 {
		return observation, errors.New("tool turn requires one user and an assistant")
	}
	userWire, err := canonical.Bytes(map[string]any{"info": user.Info, "parts": user.Parts})
	if err != nil || decodePromptWithStructuredOutput(userWire, b, prompt, structured) != nil {
		return observation, errors.New("tool turn prompt mismatch")
	}
	userCreated, err := toolTurnUserCreated(user.Info)
	if err != nil {
		return observation, err
	}
	sort.Slice(assistants, func(i, j int) bool {
		left, right := assistants[i].decoded.Assistant, assistants[j].decoded.Assistant
		if left.Created != right.Created {
			return left.Created < right.Created
		}
		return left.ID < right.ID
	})

	seenProviderCalls := map[string]bool{}
	seenRequests := map[string]bool{}
	observation.Generations = make([]ToolGenerationObservation, 0, len(assistants))
	observation.Calls = []ToolCallObservation{}
	for index, item := range assistants {
		if item.decoded.Assistant.Created < userCreated || index > 0 && item.decoded.Assistant.Created < assistants[index-1].decoded.Assistant.Completed {
			return ToolTurnObservation{}, errors.New("tool assistant chronology mismatch")
		}
		final := index == len(assistants)-1
		if structured != nil && !final && len(item.decoded.StructuredOutput) != 0 {
			return ToolTurnObservation{}, errors.New("structured output must be terminal")
		}
		generation, err := decodeToolGenerationWithOptions(item.message.Parts, item.decoded, final, pairs, seenProviderCalls, seenRequests, structured, expected)
		if err != nil {
			return ToolTurnObservation{}, err
		}
		if final {
			if structured != nil {
				if item.decoded.Finish != "tool-calls" || len(generation.Calls) != 0 || generation.StructuredOutputTool == nil || len(item.decoded.StructuredOutput) == 0 || generation.TextSHA256 != toolTurnDigest(nil) {
					return ToolTurnObservation{}, errors.New("invalid structured output terminal assistant")
				}
				normalized, normalizeErr := normalizeStructuredOutputValue(item.decoded.StructuredOutput)
				if normalizeErr != nil {
					return ToolTurnObservation{}, normalizeErr
				}
				value := json.RawMessage(append([]byte(nil), normalized...))
				observation.StructuredOutput = &value
				copy := *generation.StructuredOutputTool
				observation.StructuredOutputTool = &copy
				observation.Final = item.decoded.Assistant
			} else {
				if item.decoded.Finish != "stop" || len(generation.Calls) != 0 {
					return ToolTurnObservation{}, errors.New("tool turn final assistant is not a tool-free stop")
				}
				text, metadata, err := DecodeTextPartsWithRuntimeMetadata(item.message.Parts, item.decoded.Assistant, expected)
				if err != nil {
					return ToolTurnObservation{}, err
				}
				if !equalCanonical(metadata, generation.RuntimeMetadata) {
					return ToolTurnObservation{}, errors.New("runtime metadata projection changed")
				}
				observation.Final, observation.Text = item.decoded.Assistant, text
			}
		} else if len(generation.Calls) == 0 || item.decoded.Finish != "tool-calls" && item.decoded.Finish != "stop" {
			return ToolTurnObservation{}, errors.New("invalid intermediate tool generation")
		}
		observation.Tokens, err = addToolTokens(observation.Tokens, item.decoded.Tokens)
		if err != nil {
			return ToolTurnObservation{}, err
		}
		observation.Generations = append(observation.Generations, generation)
		observation.Calls = append(observation.Calls, generation.Calls...)
		observation.RuntimeMetadata = append(observation.RuntimeMetadata, generation.RuntimeMetadata...)
	}
	if len(observation.Calls) != len(pairs) || len(seenRequests) != len(pairs) {
		return ToolTurnObservation{}, errors.New("tool parts and broker receipts are not bijective")
	}
	observation.TranscriptSHA256 = toolTurnDigest(raw)
	observation.BrokerBindingID = bindingID
	observation.BrokerStateID = brokerStateID
	return observation, nil
}

func toolTurnUserCreated(raw []byte) (int64, error) {
	info, err := wireObject(raw)
	if err != nil {
		return 0, err
	}
	var timing struct {
		Created *int64 `json:"created"`
	}
	if field(info, "time", &timing) != nil || timing.Created == nil || *timing.Created < 0 || *timing.Created > toolTurnMaximumExactInteger {
		return 0, errors.New("invalid tool turn user time")
	}
	return *timing.Created, nil
}

func validateToolBrokerState(state contextbroker.State) (map[string]brokerPair, string, string, error) {
	if state.Binding == nil || state.Pending != nil || state.Calls < 0 || state.Calls != len(state.Requests) || len(state.Requests) != len(state.Responses) {
		return nil, "", "", errors.New("broker state is incomplete for tool turn")
	}
	bindingID, err := state.Binding.ID()
	if err != nil {
		return nil, "", "", err
	}
	pairs := make(map[string]brokerPair, len(state.Requests))
	responseBytes := 0
	for index, request := range state.Requests {
		requestID, err := request.ID()
		if err != nil || request.RequestID != requestID || request.BindingID != bindingID || request.InvocationID != state.Binding.InvocationID {
			return nil, "", "", errors.New("broker request differs from binding")
		}
		if _, duplicate := pairs[request.RequestID]; duplicate {
			return nil, "", "", errors.New("duplicate broker request")
		}
		response := state.Responses[index]
		content, err := canonical.Normalize(response.Content)
		if err != nil || len(content) < 2 || content[0] != '{' || !bytes.Equal(content, response.Content) || !response.Success || response.Version != 1 || response.BindingID != request.BindingID || response.InvocationID != request.InvocationID || response.RequestID != request.RequestID || response.CallID != request.CallID {
			return nil, "", "", errors.New("broker response differs from request or is unsuccessful")
		}
		responseBytes += len(content)
		pairs[request.RequestID] = brokerPair{Request: request, Response: response}
	}
	if responseBytes != state.ResponseBytes {
		return nil, "", "", errors.New("broker response accounting mismatch")
	}
	stateID, err := canonical.Hash("harness.opencode-tool-turn-broker-state.v1", state)
	if err != nil {
		return nil, "", "", err
	}
	return pairs, bindingID, stateID, nil
}

func decodeTurnAssistant(raw []byte, expected Binding) (turnAssistant, error) {
	var result turnAssistant
	m, err := wireObject(raw)
	if err != nil {
		return result, err
	}
	result.Assistant.Binding = expected
	for key, want := range map[string]string{"sessionID": expected.SessionID, "parentID": expected.ParentID, "providerID": expected.Provider, "modelID": expected.Model, "agent": expected.Agent, "role": "assistant"} {
		var got string
		if field(m, key, &got) != nil || got != want {
			return turnAssistant{}, errors.New("OpenCode tool assistant binding mismatch")
		}
	}
	if field(m, "id", &result.Assistant.ID) != nil || !locator(result.Assistant.ID) {
		return turnAssistant{}, errors.New("invalid OpenCode tool assistant ID")
	}
	if field(m, "finish", &result.Finish) != nil || result.Finish == "" {
		return turnAssistant{}, errors.New("OpenCode tool assistant finish missing")
	}
	if _, ok := m["error"]; ok {
		return turnAssistant{}, errors.New("OpenCode tool assistant carries error")
	}
	if rawSummary, ok := m["summary"]; ok {
		var summary bool
		if canonical.Decode(rawSummary, &summary) != nil || summary {
			return turnAssistant{}, errors.New("summary assistant rejected")
		}
	}
	var variant string
	if rawVariant, ok := m["variant"]; ok && canonical.Decode(rawVariant, &variant) != nil {
		return turnAssistant{}, errors.New("invalid tool assistant variant")
	}
	if variant != expected.Variant {
		return turnAssistant{}, errors.New("OpenCode tool assistant variant substitution")
	}
	var path struct {
		Cwd  string `json:"cwd"`
		Root string `json:"root"`
	}
	if field(m, "path", &path) != nil || path.Cwd != expected.Directory || path.Root != expected.Root {
		return turnAssistant{}, errors.New("OpenCode tool assistant workspace substitution")
	}
	var timing struct {
		Created   *int64 `json:"created"`
		Completed *int64 `json:"completed"`
	}
	if field(m, "time", &timing) != nil || timing.Created == nil || timing.Completed == nil || *timing.Created < 0 || *timing.Completed < *timing.Created || *timing.Completed > toolTurnMaximumExactInteger {
		return turnAssistant{}, errors.New("incomplete OpenCode tool assistant time")
	}
	if rawStructured, ok := m["structured"]; ok {
		result.StructuredOutput = append(json.RawMessage(nil), rawStructured...)
	}
	result.Assistant.Created, result.Assistant.Completed = *timing.Created, *timing.Completed
	result.Tokens, err = decodeExactTokens(m["tokens"])
	if err != nil {
		return turnAssistant{}, err
	}
	result.Assistant.InputTokens = result.Tokens.Input
	result.Assistant.OutputTokens = result.Tokens.Output
	result.Assistant.ReasoningTokens = result.Tokens.Reasoning
	result.Assistant.CacheRead = result.Tokens.CacheRead
	result.Assistant.CacheWrite = result.Tokens.CacheWrite
	return result, nil
}

func decodeToolGeneration(raw []byte, assistant turnAssistant, final bool, pairs map[string]brokerPair, seenProviderCalls, seenRequests map[string]bool) (ToolGenerationObservation, error) {
	return decodeToolGenerationWithOptions(raw, assistant, final, pairs, seenProviderCalls, seenRequests, nil, nil)
}

func decodeToolGenerationWithRuntimeMetadata(raw []byte, assistant turnAssistant, final bool, pairs map[string]brokerPair, seenProviderCalls, seenRequests map[string]bool, expected *RuntimeMetadataExpectation) (ToolGenerationObservation, error) {
	return decodeToolGenerationWithOptions(raw, assistant, final, pairs, seenProviderCalls, seenRequests, nil, expected)
}

func decodeToolGenerationWithOptions(raw []byte, assistant turnAssistant, final bool, pairs map[string]brokerPair, seenProviderCalls, seenRequests map[string]bool, structured *StructuredOutputExpectation, expected *RuntimeMetadataExpectation) (ToolGenerationObservation, error) {
	generation := ToolGenerationObservation{Assistant: assistant.Assistant, Finish: assistant.Finish, Calls: []ToolCallObservation{}}
	var completedText bytes.Buffer
	var parts []json.RawMessage
	if len(raw) > (1<<20)-10 || json.Unmarshal(raw, &parts) != nil || parts == nil || len(parts) < 2 || len(parts) > 4096 {
		return generation, errors.New("invalid tool assistant parts")
	}
	stepStart, stepFinish := -1, -1
	patchIndexes := []int{}
	for index, rawPart := range parts {
		part, err := wireObject(rawPart)
		if err != nil {
			return generation, err
		}
		var kind string
		if field(part, "type", &kind) != nil {
			return generation, errors.New("tool assistant part type missing")
		}
		switch kind {
		case "step-start":
			if stepStart >= 0 {
				return generation, errors.New("duplicate step start")
			}
			stepStart = index
		case "step-finish":
			if stepFinish >= 0 {
				return generation, errors.New("duplicate step finish")
			}
			stepFinish = index
			var reason string
			if field(part, "reason", &reason) != nil || reason != assistant.Finish {
				return generation, errors.New("step finish reason mismatch")
			}
			tokens, err := decodeExactTokens(part["tokens"])
			if err != nil || !sameExactTokens(tokens, assistant.Tokens) {
				return generation, errors.New("step finish token mismatch")
			}
		case "tool":
			if structured != nil && partToolName(part) == StructuredOutputToolName {
				if !final {
					return generation, errors.New("structured output tool must be terminal")
				}
				if generation.StructuredOutputTool != nil {
					return generation, errors.New("duplicate structured output tool")
				}
				captured, err := decodeStructuredOutputPart(part, assistant, structured, seenProviderCalls)
				if err != nil {
					return generation, err
				}
				generation.StructuredOutputTool = &captured
				continue
			}
			if final {
				return generation, errors.New("final assistant contains tool")
			}
			call, err := decodeToolPart(part, assistant.Assistant, pairs, seenProviderCalls, seenRequests)
			if err != nil {
				return generation, err
			}
			generation.Calls = append(generation.Calls, call)
		case "text":
			itemID, phase, text, err := validateCompletedTextPartWithPhase(part, assistant.Assistant)
			if err != nil {
				return generation, err
			}
			completedText.WriteString(text)
			if itemID != "" {
				generation.TextProviderItemIDs = append(generation.TextProviderItemIDs, itemID)
			}
			if len(phase) != 0 {
				generation.TextProviderItems = append(generation.TextProviderItems, ToolTextObservation{ProviderItemID: itemID, Phase: phase})
			}
		case "reasoning":
			reasoning, err := decodeCompletedReasoningPart(part, assistant.Assistant)
			if err != nil {
				return generation, err
			}
			if reasoning != nil {
				generation.Reasoning = append(generation.Reasoning, *reasoning)
			}
		case "patch":
			if expected == nil {
				return generation, errors.New("unsupported tool assistant part")
			}
			patchIndexes = append(patchIndexes, index)
		default:
			return generation, errors.New("unsupported tool assistant part")
		}
	}
	if stepStart != 0 || stepFinish < 0 || len(patchIndexes) == 0 && stepFinish != len(parts)-1 {
		return generation, errors.New("tool assistant step framing mismatch")
	}
	for _, index := range patchIndexes {
		if index <= stepFinish {
			return generation, errors.New("tool assistant patch precedes step finish")
		}
	}
	var err error
	generation.RuntimeMetadata, err = decodeRuntimeMetadataParts(raw, assistant.Assistant, expected)
	if err != nil {
		return generation, err
	}
	generation.TextSHA256 = toolTurnDigest(completedText.Bytes())
	return generation, nil
}

func partToolName(part map[string]json.RawMessage) string {
	var name string
	if field(part, "tool", &name) != nil {
		return ""
	}
	return name
}

func decodeStructuredOutputPart(part map[string]json.RawMessage, assistant turnAssistant, expectation *StructuredOutputExpectation, seenProviderCalls map[string]bool) (StructuredOutputToolObservation, error) {
	var result StructuredOutputToolObservation
	if expectation == nil {
		return result, errors.New("structured output expectation missing")
	}
	if field(part, "id", &result.PartID) != nil || !locator(result.PartID) || field(part, "callID", &result.CallID) != nil || strings.TrimSpace(result.CallID) == "" || len(result.CallID) > 256 || seenProviderCalls[result.CallID] || partToolName(part) != StructuredOutputToolName {
		return result, errors.New("invalid or duplicate structured output tool identity")
	}
	if _, err := responsesProviderItemID(part); err != nil {
		return result, err
	}
	state, err := wireObject(part["state"])
	if err != nil {
		return result, errors.New("structured output tool state missing")
	}
	var status, title, output string
	if field(state, "status", &status) != nil || status != "completed" || field(state, "title", &title) != nil || title != "Structured Output" || field(state, "output", &output) != nil || output != structuredOutputResult {
		return result, errors.New("structured output tool is not a completed result")
	}
	rawInput, ok := state["input"]
	if !ok {
		return result, errors.New("structured output tool input missing")
	}
	input, err := normalizeStructuredOutputValue(rawInput)
	if err != nil {
		return result, err
	}
	assistantOutput, err := normalizeStructuredOutputValue(assistant.StructuredOutput)
	if err != nil || !bytes.Equal(input, assistantOutput) {
		return result, errors.New("structured output tool input differs from assistant structured output")
	}
	if err := providergateway.ValidateStructuredOutputValue(json.RawMessage(input), expectation.Schema); err != nil {
		return result, errors.New("structured output tool input violates schema")
	}
	metadata, err := wireObject(state["metadata"])
	if err != nil || len(metadata) != 1 {
		return result, errors.New("invalid structured output tool metadata")
	}
	var valid bool
	if field(metadata, "valid", &valid) != nil || !valid {
		return result, errors.New("structured output tool result is not valid")
	}
	for key := range metadata {
		if key != "valid" {
			return result, errors.New("unknown structured output tool metadata")
		}
	}
	var timing struct {
		Start *int64 `json:"start"`
		End   *int64 `json:"end"`
	}
	if field(state, "time", &timing) != nil || timing.Start == nil || timing.End == nil || *timing.Start < assistant.Assistant.Created || *timing.End < *timing.Start || *timing.End > assistant.Assistant.Completed {
		return result, errors.New("invalid structured output tool time")
	}
	for key := range state {
		switch key {
		case "status", "input", "output", "title", "metadata", "time":
		case "attachments":
			var attachments []json.RawMessage
			if canonical.Decode(state[key], &attachments) != nil || len(attachments) != 0 {
				return result, errors.New("structured output tool attachments rejected")
			}
		default:
			return result, errors.New("unknown structured output tool state")
		}
	}
	seenProviderCalls[result.CallID] = true
	result.ArgumentsSHA256 = toolTurnDigest(input)
	result.ResultSHA256 = toolTurnDigest([]byte(output))
	return result, nil
}

// legacyToolTurnObservationV1 reconstructs the observation shape used before
// per-generation text digests were retained. It exists only for explicit
// replay of non-provider v1 seals and must never be used for a new seal.
func legacyToolTurnObservationV1(observation ToolTurnObservation) ToolTurnObservation {
	observation.Generations = append([]ToolGenerationObservation(nil), observation.Generations...)
	for index := range observation.Generations {
		observation.Generations[index].TextSHA256 = ""
	}
	return observation
}

func decodeToolPart(part map[string]json.RawMessage, assistant Assistant, pairs map[string]brokerPair, seenProviderCalls, seenRequests map[string]bool) (ToolCallObservation, error) {
	var result ToolCallObservation
	if field(part, "id", &result.PartID) != nil || !locator(result.PartID) || field(part, "callID", &result.ProviderCallID) != nil || strings.TrimSpace(result.ProviderCallID) == "" || len(result.ProviderCallID) > 256 || seenProviderCalls[result.ProviderCallID] || field(part, "tool", &result.Tool) != nil || strings.TrimSpace(result.Tool) == "" || len(result.Tool) > 256 {
		return result, errors.New("invalid or duplicate provider tool identity")
	}
	seenProviderCalls[result.ProviderCallID] = true
	result.MessageID = assistant.ID
	var err error
	result.ProviderItemID, err = responsesProviderItemID(part)
	if err != nil {
		return result, err
	}
	state, err := wireObject(part["state"])
	if err != nil {
		return result, err
	}
	var status, output, title string
	if field(state, "status", &status) != nil || status != "completed" || field(state, "output", &output) != nil || field(state, "title", &title) != nil || title != "" {
		return result, errors.New("tool call is not a successful completed MCP result")
	}
	input, err := canonical.Normalize(state["input"])
	if err != nil || len(input) < 2 || input[0] != '{' {
		return result, errors.New("invalid tool input")
	}
	metadata, err := wireObject(state["metadata"])
	if err != nil || len(metadata) != 1 {
		return result, errors.New("invalid tool result metadata")
	}
	var truncated bool
	if field(metadata, "truncated", &truncated) != nil || truncated {
		return result, errors.New("truncated tool result rejected")
	}
	var timing struct {
		Start *int64 `json:"start"`
		End   *int64 `json:"end"`
	}
	if field(state, "time", &timing) != nil || timing.Start == nil || timing.End == nil || *timing.Start < assistant.Created || *timing.End < *timing.Start || *timing.End > assistant.Completed {
		return result, errors.New("invalid completed tool time")
	}
	if attachments, ok := state["attachments"]; ok {
		var rows []json.RawMessage
		if canonical.Decode(attachments, &rows) != nil || len(rows) != 0 {
			return result, errors.New("tool attachments rejected")
		}
	}
	canonicalOutput, err := canonical.Normalize([]byte(output))
	if err != nil || !bytes.Equal(canonicalOutput, []byte(output)) {
		return result, errors.New("tool output is not a canonical broker receipt")
	}
	var receipt contextbroker.Response
	if canonical.Decode(canonicalOutput, &receipt) != nil {
		return result, errors.New("invalid broker response receipt")
	}
	pair, ok := pairs[receipt.RequestID]
	if !ok || seenRequests[receipt.RequestID] {
		return result, errors.New("missing or duplicate broker response receipt")
	}
	expected, err := canonical.Bytes(pair.Response)
	if err != nil || !bytes.Equal(expected, canonicalOutput) || result.Tool != "engorch_"+pair.Request.Tool || !bytes.Equal(input, pair.Request.Arguments) {
		return result, errors.New("tool part differs from exact broker request or response")
	}
	seenRequests[receipt.RequestID] = true
	result.BindingID = receipt.BindingID
	result.InvocationID = receipt.InvocationID
	result.RequestID = receipt.RequestID
	result.BrokerCallID = receipt.CallID
	result.ArgumentsSHA256 = toolTurnDigest(pair.Request.Arguments)
	result.ContentSHA256 = toolTurnDigest(receipt.Content)
	return result, nil
}

func decodeExactTokens(raw []byte) (exactTokens, error) {
	var wire struct {
		Total     *int64 `json:"total,omitempty"`
		Input     *int64 `json:"input"`
		Output    *int64 `json:"output"`
		Reasoning *int64 `json:"reasoning"`
		Cache     *struct {
			Read  *int64 `json:"read"`
			Write *int64 `json:"write"`
		} `json:"cache"`
	}
	if canonical.Decode(raw, &wire) != nil || wire.Cache == nil {
		return exactTokens{}, errors.New("invalid tool turn token accounting")
	}
	for _, value := range []*int64{wire.Input, wire.Output, wire.Reasoning, wire.Cache.Read, wire.Cache.Write} {
		if value == nil || *value < 0 || *value > toolTurnMaximumExactInteger {
			return exactTokens{}, errors.New("missing or invalid tool turn token accounting")
		}
	}
	if wire.Total != nil && (*wire.Total < 0 || *wire.Total > toolTurnMaximumExactInteger) {
		return exactTokens{}, errors.New("invalid tool turn total token accounting")
	}
	return exactTokens{Total: wire.Total, Input: *wire.Input, Output: *wire.Output, Reasoning: *wire.Reasoning, CacheRead: *wire.Cache.Read, CacheWrite: *wire.Cache.Write}, nil
}

func sameExactTokens(left, right exactTokens) bool {
	if left.Total == nil != (right.Total == nil) {
		return false
	}
	return (left.Total == nil || *left.Total == *right.Total) && left.Input == right.Input && left.Output == right.Output && left.Reasoning == right.Reasoning && left.CacheRead == right.CacheRead && left.CacheWrite == right.CacheWrite
}

func addToolTokens(total ToolTurnTokens, next exactTokens) (ToolTurnTokens, error) {
	values := []*int64{&total.Input, &total.Output, &total.Reasoning, &total.CacheRead, &total.CacheWrite}
	add := []int64{next.Input, next.Output, next.Reasoning, next.CacheRead, next.CacheWrite}
	for index := range values {
		if *values[index] > toolTurnMaximumExactInteger-add[index] {
			return ToolTurnTokens{}, errors.New("tool turn token aggregate overflow")
		}
		*values[index] += add[index]
	}
	return total, nil
}

func validateCompletedTextPart(part map[string]json.RawMessage, assistant Assistant) (ProviderItemID, string, error) {
	itemID, _, text, err := validateCompletedTextPartWithPhase(part, assistant)
	if err != nil {
		return "", "", err
	}
	return itemID, text, nil
}

func validateCompletedTextPartWithPhase(part map[string]json.RawMessage, assistant Assistant) (ProviderItemID, json.RawMessage, string, error) {
	itemID, phase, err := responsesProviderTextMetadata(part)
	if err != nil {
		return "", nil, "", err
	}
	for _, key := range []string{"synthetic", "ignored"} {
		if raw, ok := part[key]; ok {
			var excluded bool
			if canonical.Decode(raw, &excluded) != nil || excluded {
				return "", nil, "", errors.New("excluded tool-turn text rejected")
			}
		}
	}
	var text string
	if field(part, "text", &text) != nil || !utf8.ValidString(text) || len(text) > 256<<10 {
		return "", nil, "", errors.New("invalid tool-turn text")
	}
	if raw, ok := part["time"]; ok {
		if err := validatePartTime(raw, assistant); err != nil {
			return "", nil, "", err
		}
	}
	return itemID, phase, text, nil
}

// responsesProviderItemID admits only the native OpenAI Responses item
// locator emitted by the pinned adapter. It never treats this provider-domain
// identity as a call ID or broker identity.
func responsesProviderItemID(part map[string]json.RawMessage) (ProviderItemID, error) {
	itemID, _, err := responsesProviderItemMetadata(part, false)
	return itemID, err
}

func responsesProviderTextMetadata(part map[string]json.RawMessage) (ProviderItemID, json.RawMessage, error) {
	return responsesProviderItemMetadata(part, true)
}

func responsesProviderItemMetadata(part map[string]json.RawMessage, allowPhase bool) (ProviderItemID, json.RawMessage, error) {
	raw, ok := part["metadata"]
	if !ok {
		return "", nil, nil
	}
	metadata, err := wireObject(raw)
	if err != nil {
		return "", nil, errors.New("provider-executed or unbound part metadata rejected")
	}
	if len(metadata) == 0 {
		return "", nil, nil
	}
	if len(metadata) != 1 || metadata["openai"] == nil {
		return "", nil, errors.New("provider-executed or unbound part metadata rejected")
	}
	openai, err := wireObject(metadata["openai"])
	if err != nil {
		return "", nil, errors.New("invalid OpenAI Responses item metadata")
	}
	if !allowPhase && len(openai) != 1 || allowPhase && (len(openai) < 1 || len(openai) > 2) {
		return "", nil, errors.New("invalid OpenAI Responses item metadata")
	}
	for key := range openai {
		if key != "itemId" && (!allowPhase || key != "phase") {
			return "", nil, errors.New("invalid OpenAI Responses item metadata")
		}
	}
	if openai["itemId"] == nil {
		return "", nil, errors.New("invalid OpenAI Responses item metadata")
	}
	var itemID ProviderItemID
	if field(openai, "itemId", &itemID) != nil || !validProviderItemID(itemID) {
		return "", nil, errors.New("invalid OpenAI Responses item metadata")
	}
	var phase json.RawMessage
	if allowPhase {
		if rawPhase, ok := openai["phase"]; ok {
			if bytes.Equal(bytes.TrimSpace(rawPhase), []byte("null")) {
				phase = append(json.RawMessage(nil), rawPhase...)
			} else {
				var value string
				if canonical.Decode(rawPhase, &value) != nil || value != "commentary" && value != "final_answer" {
					return "", nil, errors.New("invalid OpenAI Responses item metadata")
				}
				phase = append(json.RawMessage(nil), rawPhase...)
			}
		}
	}
	return itemID, phase, nil
}

func decodeCompletedReasoningPart(part map[string]json.RawMessage, assistant Assistant) (*ToolReasoningObservation, error) {
	var text string
	if field(part, "text", &text) != nil || !utf8.ValidString(text) || len(text) > 256<<10 {
		return nil, errors.New("invalid tool-turn reasoning")
	}
	if err := validatePartTime(part["time"], assistant); err != nil {
		return nil, err
	}
	raw, ok := part["metadata"]
	if !ok {
		return nil, nil
	}
	metadata, err := wireObject(raw)
	if err != nil || len(metadata) != 1 || metadata["openai"] == nil {
		return nil, errors.New("unbound reasoning metadata rejected")
	}
	openai, err := wireObject(metadata["openai"])
	if err != nil || len(openai) != 2 {
		return nil, errors.New("invalid OpenAI Responses reasoning metadata")
	}
	var result ToolReasoningObservation
	if field(part, "id", &result.PartID) != nil || !locator(result.PartID) ||
		field(openai, "itemId", &result.ProviderItemID) != nil || !validProviderItemID(result.ProviderItemID) ||
		field(openai, "reasoningEncryptedContent", &result.EncryptedContent) != nil || !validReasoningEncryptedContent(result.EncryptedContent) {
		return nil, errors.New("invalid OpenAI Responses reasoning metadata")
	}
	result.TextSHA256 = toolTurnDigest([]byte(text))
	return &result, nil
}

func validReasoningEncryptedContent(value string) bool {
	if value == "" || len(value) > 256<<10 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func validatePartTime(raw []byte, assistant Assistant) error {
	var timing struct {
		Start *int64 `json:"start"`
		End   *int64 `json:"end"`
	}
	if canonical.Decode(raw, &timing) != nil || timing.Start == nil || timing.End == nil || *timing.Start < assistant.Created || *timing.End < *timing.Start || *timing.End > assistant.Completed {
		return errors.New("unfinished tool-turn part")
	}
	return nil
}

func toolTurnDigest(value []byte) string {
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}
