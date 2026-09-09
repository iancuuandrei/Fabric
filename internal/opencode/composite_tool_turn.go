package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/toolbridge"
	"harness.local/engorch/internal/toolreceipts"
)

// CompositeBackendVerifier proves the matched owner result against its
// authoritative backend. The tool receipt journal alone is transport evidence.
type CompositeBackendVerifier func(toolreceipts.Owner, toolbridge.Call, toolbridge.Result) error

// CompositeToolCallObservation retains provider and MCP identities in their
// separate domains and binds the exact transport receipt selected for a part.
type CompositeToolCallObservation struct {
	MessageID       string             `json:"message_id"`
	PartID          string             `json:"part_id"`
	ProviderCallID  string             `json:"provider_call_id"`
	ProviderItemID  ProviderItemID     `json:"provider_item_id,omitempty"`
	Tool            string             `json:"tool"`
	Owner           toolreceipts.Owner `json:"owner"`
	BindingID       string             `json:"binding_id"`
	InvocationID    string             `json:"invocation_id"`
	RequestID       string             `json:"request_id"`
	RequestKey      string             `json:"request_key"`
	ArgumentsSHA256 string             `json:"arguments_sha256"`
	// ProviderArgumentsSHA256 is the provider semantic digest: plain SHA-256
	// over canonical arguments. ArgumentsSHA256 remains the domain-separated
	// MCP receipt identity and the two must never be substituted.
	ProviderArgumentsSHA256 string `json:"provider_arguments_sha256"`
	ResultSHA256            string `json:"result_sha256"`
}

// CompositeToolGenerationObservation is one chronologically ordered provider
// generation with its exact composite tool calls.
type CompositeToolGenerationObservation struct {
	Assistant           Assistant                      `json:"assistant"`
	Finish              string                         `json:"finish"`
	Calls               []CompositeToolCallObservation `json:"calls"`
	TextProviderItemIDs []ProviderItemID               `json:"text_provider_item_ids,omitempty"`
	TextProviderItems   []ToolTextObservation          `json:"text_provider_items,omitempty"`
	TextSHA256          string                         `json:"text_sha256"`
	Reasoning           []ToolReasoningObservation     `json:"reasoning,omitempty"`
	RuntimeMetadata     []PatchSnapshotReceipt         `json:"runtime_metadata,omitempty"`
}

// CompositeToolTurnObservation is a transcript projection validated against
// both the transport receipt journal and every selected owner backend.
type CompositeToolTurnObservation struct {
	Final                      Assistant                            `json:"final"`
	Text                       string                               `json:"text"`
	Generations                []CompositeToolGenerationObservation `json:"generations"`
	Calls                      []CompositeToolCallObservation       `json:"calls"`
	Tokens                     ToolTurnTokens                       `json:"tokens"`
	TranscriptSHA256           string                               `json:"transcript_sha256"`
	ReceiptBindingID           string                               `json:"receipt_binding_id"`
	ReceiptJournalHead         string                               `json:"receipt_journal_head"`
	ReceiptStateSHA256         string                               `json:"receipt_state_sha256"`
	RuntimeMetadata            []PatchSnapshotReceipt               `json:"runtime_metadata,omitempty"`
	RuntimeMetadataExpectation *RuntimeMetadataExpectation          `json:"runtime_metadata_expectation,omitempty"`
}

// ReadCompositeToolTurn reads one session transcript and validates its calls
// through the exact composite transport binding and owner backend verifier.
func (c *Client) ReadCompositeToolTurn(ctx context.Context, b Binding, prompt string, receiptPath string, binding toolreceipts.Binding, verify CompositeBackendVerifier) (CompositeToolTurnObservation, error) {
	return c.ReadCompositeToolTurnWithRuntimeMetadata(ctx, b, prompt, receiptPath, binding, verify, nil)
}

// ReadCompositeToolTurnWithRuntimeMetadata preserves classified snapshot
// receipts without mixing them into composite semantic text or tool evidence.
func (c *Client) ReadCompositeToolTurnWithRuntimeMetadata(ctx context.Context, b Binding, prompt string, receiptPath string, binding toolreceipts.Binding, verify CompositeBackendVerifier, expected *RuntimeMetadataExpectation) (CompositeToolTurnObservation, error) {
	if err := validatePrompt(b, prompt); err != nil {
		return CompositeToolTurnObservation{}, err
	}
	raw, err := c.read(ctx, "/session/"+b.SessionID+"/message")
	if err != nil {
		return CompositeToolTurnObservation{}, err
	}
	state, head, err := toolreceipts.InspectWithHeadContext(ctx, receiptPath)
	if err != nil {
		return CompositeToolTurnObservation{}, err
	}
	return decodeCompositeToolTurnStateWithRuntimeMetadata(raw, b, prompt, binding, state, head, verify, expected)
}

// DecodeCompositeToolTurn validates an already captured OpenCode transcript.
// It never infers that the provider call ID equals the MCP request ID.
func DecodeCompositeToolTurn(raw []byte, b Binding, prompt string, receiptPath string, binding toolreceipts.Binding, verify CompositeBackendVerifier) (CompositeToolTurnObservation, error) {
	return DecodeCompositeToolTurnWithRuntimeMetadata(raw, b, prompt, receiptPath, binding, verify, nil)
}

// DecodeCompositeToolTurnWithRuntimeMetadata validates captured transcript
// metadata under the same policy durably bound before dispatch.
func DecodeCompositeToolTurnWithRuntimeMetadata(raw []byte, b Binding, prompt string, receiptPath string, binding toolreceipts.Binding, verify CompositeBackendVerifier, expected *RuntimeMetadataExpectation) (CompositeToolTurnObservation, error) {
	state, head, err := toolreceipts.InspectWithHead(receiptPath)
	if err != nil {
		return CompositeToolTurnObservation{}, err
	}
	return decodeCompositeToolTurnStateWithRuntimeMetadata(raw, b, prompt, binding, state, head, verify, expected)
}

func decodeCompositeToolTurnState(raw []byte, b Binding, prompt string, binding toolreceipts.Binding, state toolreceipts.State, receiptHead string, verify CompositeBackendVerifier) (CompositeToolTurnObservation, error) {
	return decodeCompositeToolTurnStateWithRuntimeMetadata(raw, b, prompt, binding, state, receiptHead, verify, nil)
}

func decodeCompositeToolTurnStateWithRuntimeMetadata(raw []byte, b Binding, prompt string, binding toolreceipts.Binding, state toolreceipts.State, receiptHead string, verify CompositeBackendVerifier, expected *RuntimeMetadataExpectation) (CompositeToolTurnObservation, error) {
	var observation CompositeToolTurnObservation
	if expected != nil {
		copy := *expected
		copy.KnownControllerPaths = append([]string(nil), expected.KnownControllerPaths...)
		observation.RuntimeMetadataExpectation = &copy
	}
	if err := validatePrompt(b, prompt); err != nil || verify == nil {
		return observation, errors.Join(errors.New("composite backend verifier required"), err)
	}
	bindingID, err := binding.ID()
	if err != nil || binding.BindingID != bindingID || receiptHead == "" {
		return observation, errors.Join(errors.New("invalid composite tool receipt binding"), err)
	}
	if state.Binding == nil {
		return observation, errors.New("composite tool receipt journal unavailable")
	}
	messages, err := decodeTranscript(raw, b.SessionID)
	if err != nil {
		return observation, err
	}
	if len(messages) < 2 {
		return observation, errors.New("composite tool turn history incomplete")
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
				return observation, errors.New("composite tool turn contains extra user message")
			}
			copy := message
			user = &copy
			continue
		}
		decoded, decodeErr := decodeTurnAssistant(message.Info, b)
		if decodeErr != nil {
			return observation, decodeErr
		}
		assistants = append(assistants, struct {
			message TranscriptMessage
			decoded turnAssistant
		}{message, decoded})
	}
	if user == nil || len(assistants) == 0 {
		return observation, errors.New("composite tool turn requires one user and an assistant")
	}
	userWire, err := canonical.Bytes(map[string]any{"info": user.Info, "parts": user.Parts})
	if err != nil || decodePrompt(userWire, b, prompt) != nil {
		return observation, errors.New("composite tool turn prompt mismatch")
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
	usedReceipts := map[string]bool{}
	transport := make([]toolreceipts.Observation, 0, len(state.Calls))
	backend := make([]struct {
		owner  toolreceipts.Owner
		call   toolbridge.Call
		result toolbridge.Result
	}, 0, len(state.Calls))
	observation.Generations = make([]CompositeToolGenerationObservation, 0, len(assistants))
	observation.Calls = []CompositeToolCallObservation{}
	for index, item := range assistants {
		if item.decoded.Assistant.Created < userCreated || index > 0 && item.decoded.Assistant.Created < assistants[index-1].decoded.Assistant.Completed {
			return CompositeToolTurnObservation{}, errors.New("composite tool assistant chronology mismatch")
		}
		final := index == len(assistants)-1
		generation, generationTransport, generationBackend, decodeErr := decodeCompositeToolGenerationWithRuntimeMetadata(item.message.Parts, item.decoded, final, binding, state, seenProviderCalls, usedReceipts, expected)
		if decodeErr != nil {
			return CompositeToolTurnObservation{}, decodeErr
		}
		if final {
			if item.decoded.Finish != "stop" || len(generation.Calls) != 0 {
				return CompositeToolTurnObservation{}, errors.New("composite tool final assistant is not a tool-free stop")
			}
			text, metadata, textErr := DecodeTextPartsWithRuntimeMetadata(item.message.Parts, item.decoded.Assistant, expected)
			if textErr != nil {
				return CompositeToolTurnObservation{}, textErr
			}
			if !equalCanonical(metadata, generation.RuntimeMetadata) {
				return CompositeToolTurnObservation{}, errors.New("composite runtime metadata projection changed")
			}
			observation.Final, observation.Text = item.decoded.Assistant, text
		} else if len(generation.Calls) == 0 || item.decoded.Finish != "tool-calls" && item.decoded.Finish != "stop" {
			return CompositeToolTurnObservation{}, errors.New("invalid composite intermediate tool generation")
		}
		observation.Tokens, err = addToolTokens(observation.Tokens, item.decoded.Tokens)
		if err != nil {
			return CompositeToolTurnObservation{}, err
		}
		observation.Generations = append(observation.Generations, generation)
		observation.Calls = append(observation.Calls, generation.Calls...)
		observation.RuntimeMetadata = append(observation.RuntimeMetadata, generation.RuntimeMetadata...)
		transport = append(transport, generationTransport...)
		backend = append(backend, generationBackend...)
	}
	if len(usedReceipts) != len(state.Calls) || len(observation.Calls) != len(state.Calls) {
		return CompositeToolTurnObservation{}, errors.New("composite tool parts and receipts are not bijective")
	}
	if err := state.ValidateTranscript(binding, transport); err != nil {
		return CompositeToolTurnObservation{}, err
	}
	for _, selected := range backend {
		if err := verify(selected.owner, selected.call, selected.result); err != nil {
			return CompositeToolTurnObservation{}, errors.Join(errors.New("composite owner backend verification failed"), err)
		}
	}
	stateID, err := canonical.Hash("harness.opencode-composite-tool-receipt-state.v1", state)
	if err != nil {
		return CompositeToolTurnObservation{}, err
	}
	observation.TranscriptSHA256 = toolTurnDigest(raw)
	observation.ReceiptBindingID = bindingID
	observation.ReceiptJournalHead = receiptHead
	observation.ReceiptStateSHA256 = stateID
	return observation, nil
}

func decodeCompositeToolGeneration(raw []byte, assistant turnAssistant, final bool, binding toolreceipts.Binding, state toolreceipts.State, seenProviderCalls, usedReceipts map[string]bool) (CompositeToolGenerationObservation, []toolreceipts.Observation, []struct {
	owner  toolreceipts.Owner
	call   toolbridge.Call
	result toolbridge.Result
}, error) {
	return decodeCompositeToolGenerationWithRuntimeMetadata(raw, assistant, final, binding, state, seenProviderCalls, usedReceipts, nil)
}

func decodeCompositeToolGenerationWithRuntimeMetadata(raw []byte, assistant turnAssistant, final bool, binding toolreceipts.Binding, state toolreceipts.State, seenProviderCalls, usedReceipts map[string]bool, expected *RuntimeMetadataExpectation) (CompositeToolGenerationObservation, []toolreceipts.Observation, []struct {
	owner  toolreceipts.Owner
	call   toolbridge.Call
	result toolbridge.Result
}, error) {
	generation := CompositeToolGenerationObservation{Assistant: assistant.Assistant, Finish: assistant.Finish, Calls: []CompositeToolCallObservation{}}
	var completedText bytes.Buffer
	var parts []json.RawMessage
	if len(raw) > (1<<20)-10 || json.Unmarshal(raw, &parts) != nil || parts == nil || len(parts) < 2 || len(parts) > 4096 {
		return generation, nil, nil, errors.New("invalid composite tool assistant parts")
	}
	var transport []toolreceipts.Observation
	var backend []struct {
		owner  toolreceipts.Owner
		call   toolbridge.Call
		result toolbridge.Result
	}
	stepStart, stepFinish := -1, -1
	patchIndexes := []int{}
	for index, rawPart := range parts {
		part, err := wireObject(rawPart)
		if err != nil {
			return generation, nil, nil, err
		}
		var kind string
		if field(part, "type", &kind) != nil {
			return generation, nil, nil, errors.New("composite tool assistant part type missing")
		}
		switch kind {
		case "step-start":
			if stepStart >= 0 {
				return generation, nil, nil, errors.New("duplicate composite step start")
			}
			stepStart = index
		case "step-finish":
			if stepFinish >= 0 {
				return generation, nil, nil, errors.New("duplicate composite step finish")
			}
			stepFinish = index
			var reason string
			if field(part, "reason", &reason) != nil || reason != assistant.Finish {
				return generation, nil, nil, errors.New("composite step finish reason mismatch")
			}
			tokens, tokenErr := decodeExactTokens(part["tokens"])
			if tokenErr != nil || !sameExactTokens(tokens, assistant.Tokens) {
				return generation, nil, nil, errors.New("composite step finish token mismatch")
			}
		case "tool":
			if final {
				return generation, nil, nil, errors.New("composite final assistant contains tool")
			}
			call, observed, owner, decodeErr := decodeCompositeToolPart(part, assistant.Assistant, binding, state, seenProviderCalls, usedReceipts)
			if decodeErr != nil {
				return generation, nil, nil, decodeErr
			}
			generation.Calls = append(generation.Calls, call)
			transport = append(transport, observed)
			backend = append(backend, struct {
				owner  toolreceipts.Owner
				call   toolbridge.Call
				result toolbridge.Result
			}{owner, observed.Call, observed.Result})
		case "text":
			itemID, phase, text, textErr := validateCompletedTextPartWithPhase(part, assistant.Assistant)
			if textErr != nil {
				return generation, nil, nil, textErr
			}
			completedText.WriteString(text)
			if itemID != "" {
				generation.TextProviderItemIDs = append(generation.TextProviderItemIDs, itemID)
			}
			if len(phase) != 0 {
				generation.TextProviderItems = append(generation.TextProviderItems, ToolTextObservation{ProviderItemID: itemID, Phase: phase})
			}
		case "reasoning":
			reasoning, reasoningErr := decodeCompletedReasoningPart(part, assistant.Assistant)
			if reasoningErr != nil {
				return generation, nil, nil, reasoningErr
			}
			if reasoning != nil {
				generation.Reasoning = append(generation.Reasoning, *reasoning)
			}
		case "patch":
			if expected == nil {
				return generation, nil, nil, errors.New("unsupported composite tool assistant part")
			}
			patchIndexes = append(patchIndexes, index)
		default:
			return generation, nil, nil, errors.New("unsupported composite tool assistant part")
		}
	}
	if stepStart != 0 || stepFinish < 0 || len(patchIndexes) == 0 && stepFinish != len(parts)-1 {
		return generation, nil, nil, errors.New("composite tool assistant step framing mismatch")
	}
	for _, index := range patchIndexes {
		if index <= stepFinish {
			return generation, nil, nil, errors.New("composite tool assistant patch precedes step finish")
		}
	}
	var metadataErr error
	generation.RuntimeMetadata, metadataErr = decodeRuntimeMetadataParts(raw, assistant.Assistant, expected)
	if metadataErr != nil {
		return generation, nil, nil, metadataErr
	}
	generation.TextSHA256 = toolTurnDigest(completedText.Bytes())
	return generation, transport, backend, nil
}

func decodeCompositeToolPart(part map[string]json.RawMessage, assistant Assistant, binding toolreceipts.Binding, state toolreceipts.State, seenProviderCalls, usedReceipts map[string]bool) (CompositeToolCallObservation, toolreceipts.Observation, toolreceipts.Owner, error) {
	var result CompositeToolCallObservation
	var providerTool string
	if field(part, "id", &result.PartID) != nil || !locator(result.PartID) || field(part, "callID", &result.ProviderCallID) != nil || strings.TrimSpace(result.ProviderCallID) == "" || len(result.ProviderCallID) > 256 || seenProviderCalls[result.ProviderCallID] || field(part, "tool", &providerTool) != nil || !strings.HasPrefix(providerTool, "engorch_") {
		return result, toolreceipts.Observation{}, "", errors.New("invalid or duplicate composite provider tool identity")
	}
	result.Tool = strings.TrimPrefix(providerTool, "engorch_")
	if result.Tool == "" || len(result.Tool) > 128 {
		return result, toolreceipts.Observation{}, "", errors.New("invalid composite tool name")
	}
	seenProviderCalls[result.ProviderCallID] = true
	result.MessageID = assistant.ID
	var err error
	result.ProviderItemID, err = responsesProviderItemID(part)
	if err != nil {
		return result, toolreceipts.Observation{}, "", err
	}
	toolState, err := wireObject(part["state"])
	if err != nil {
		return result, toolreceipts.Observation{}, "", err
	}
	var status, output, title string
	if field(toolState, "status", &status) != nil || status != "completed" || field(toolState, "output", &output) != nil || field(toolState, "title", &title) != nil || title != "" {
		return result, toolreceipts.Observation{}, "", errors.New("composite tool call is not a successful completed result")
	}
	input, err := canonical.Normalize(toolState["input"])
	if err != nil || len(input) < 2 || input[0] != '{' {
		return result, toolreceipts.Observation{}, "", errors.New("invalid composite tool input")
	}
	metadata, err := wireObject(toolState["metadata"])
	if err != nil || len(metadata) != 1 {
		return result, toolreceipts.Observation{}, "", errors.New("invalid composite tool metadata")
	}
	var truncated bool
	if field(metadata, "truncated", &truncated) != nil || truncated {
		return result, toolreceipts.Observation{}, "", errors.New("truncated composite tool result rejected")
	}
	var timing struct {
		Start *int64 `json:"start"`
		End   *int64 `json:"end"`
	}
	if field(toolState, "time", &timing) != nil || timing.Start == nil || timing.End == nil || *timing.Start < assistant.Created || *timing.End < *timing.Start || *timing.End > assistant.Completed {
		return result, toolreceipts.Observation{}, "", errors.New("invalid completed composite tool time")
	}
	if attachments, ok := toolState["attachments"]; ok {
		var rows []json.RawMessage
		if canonical.Decode(attachments, &rows) != nil || len(rows) != 0 {
			return result, toolreceipts.Observation{}, "", errors.New("composite tool attachments rejected")
		}
	}
	canonicalOutput, err := canonical.Normalize([]byte(output))
	if err != nil || len(canonicalOutput) < 2 || canonicalOutput[0] != '{' || !bytes.Equal(canonicalOutput, []byte(output)) {
		return result, toolreceipts.Observation{}, "", errors.New("composite tool output is not canonical")
	}
	callbackResult := toolbridge.Result{JSON: json.RawMessage(canonicalOutput)}
	argumentsHash, argumentsErr := toolreceipts.ArgumentsSHA256(input)
	resultHash, resultErr := toolreceipts.ResultSHA256(callbackResult)
	if argumentsErr != nil || resultErr != nil {
		return result, toolreceipts.Observation{}, "", errors.Join(argumentsErr, resultErr)
	}
	matched := -1
	for index, recorded := range state.Calls {
		if recorded.Receipt == nil || usedReceipts[recorded.Intent.RequestKey] || recorded.Intent.Tool != result.Tool || recorded.Intent.ArgumentsSHA256 != argumentsHash || recorded.Receipt.ResultSHA256 != resultHash {
			continue
		}
		if matched >= 0 {
			return result, toolreceipts.Observation{}, "", errors.New("ambiguous composite tool receipt match")
		}
		matched = index
	}
	if matched < 0 {
		return result, toolreceipts.Observation{}, "", errors.New("composite tool receipt unavailable")
	}
	recorded := state.Calls[matched]
	requestID := json.RawMessage(recorded.Intent.RequestID)
	call := toolbridge.Call{RequestID: requestID, Tool: result.Tool, Arguments: json.RawMessage(input)}
	usedReceipts[recorded.Intent.RequestKey] = true
	result.Owner = recorded.Receipt.Owner
	result.BindingID = binding.BindingID
	result.InvocationID = binding.InvocationID
	result.RequestID = recorded.Intent.RequestID
	result.RequestKey = recorded.Intent.RequestKey
	result.ArgumentsSHA256 = argumentsHash
	result.ProviderArgumentsSHA256 = toolTurnDigest(input)
	result.ResultSHA256 = resultHash
	return result, toolreceipts.Observation{Call: call, Result: callbackResult}, recorded.Receipt.Owner, nil
}
