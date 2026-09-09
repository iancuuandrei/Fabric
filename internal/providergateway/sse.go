package providergateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maximumSSEBytes       = 8 << 20
	maximumSSEEvents      = 16384
	maximumProviderTokens = int64(9007199254740991)
)

var providerToolName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

// ToolCall is a complete function call assembled from ordered provider deltas.
// Arguments retains the exact unambiguous JSON object emitted by the provider.
type ToolCall struct {
	Index     int             `json:"index"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// SSEObservation records bounded wire evidence and provider-reported usage. It
// does not establish actual provider identity, pricing or broad compatibility.
type SSEObservation struct {
	SHA256       string     `json:"sha256"`
	SizeBytes    int        `json:"size_bytes"`
	ResponseID   string     `json:"response_id"`
	Model        string     `json:"model"`
	FinishReason string     `json:"finish_reason"`
	Usage        Usage      `json:"usage"`
	OutputText   string     `json:"output_text"`
	ToolCalls    []ToolCall `json:"tool_calls"`
}

type chatChunk struct {
	ID      string          `json:"id"`
	Object  string          `json:"object"`
	Created *int64          `json:"created"`
	Model   string          `json:"model"`
	Choices []chatChoice    `json:"choices"`
	Usage   json.RawMessage `json:"usage,omitempty"`
}

type chatChoice struct {
	Index        *int64          `json:"index"`
	Delta        json.RawMessage `json:"delta"`
	FinishReason json.RawMessage `json:"finish_reason"`
	Logprobs     json.RawMessage `json:"logprobs,omitempty"`
}

type chatDelta struct {
	Role      *string         `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	ToolCalls json.RawMessage `json:"tool_calls,omitempty"`
}

type toolCallDelta struct {
	Index    *int64             `json:"index"`
	ID       *string            `json:"id,omitempty"`
	Type     *string            `json:"type,omitempty"`
	Function *toolFunctionDelta `json:"function,omitempty"`
}

type toolFunctionDelta struct {
	Name      *string `json:"name,omitempty"`
	Arguments *string `json:"arguments,omitempty"`
}

type toolCallBuilder struct {
	id, name, arguments string
	typeSeen            bool
}

type usageWire struct {
	PromptTokens            *int64                   `json:"prompt_tokens"`
	CompletionTokens        *int64                   `json:"completion_tokens"`
	TotalTokens             *int64                   `json:"total_tokens"`
	PromptTokensDetails     *promptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *completionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type promptTokensDetails struct {
	CachedTokens *int64 `json:"cached_tokens,omitempty"`
}

type completionTokensDetails struct {
	ReasoningTokens *int64 `json:"reasoning_tokens,omitempty"`
}

// ParseChatCompletionSSE strictly admits the complete synthetic-qualified
// OpenAI-compatible chat stream. maxTotalTokens bounds the inclusive prompt
// plus completion total reported by the terminal usage event.
func ParseChatCompletionSSE(raw []byte, maxBytes int, maxTotalTokens int64) (SSEObservation, error) {
	var observation SSEObservation
	if maxBytes < 1 || maxBytes > maximumSSEBytes || len(raw) == 0 || len(raw) > maxBytes || maxTotalTokens < 1 || maxTotalTokens > maximumProviderTokens || !utf8.Valid(raw) || bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		return observation, errors.New("invalid provider SSE bounds")
	}
	events, err := splitSSEEvents(raw)
	if err != nil {
		return observation, err
	}
	if len(events) < 3 || len(events) > maximumSSEEvents || string(events[len(events)-1]) != "[DONE]" {
		return observation, errors.New("provider SSE is incomplete")
	}
	events = events[:len(events)-1]

	var responseID, model string
	var created int64
	identitySet, roleSeen, finishSeen, usageSeen := false, false, false, false
	finish := ""
	builders := map[int]*toolCallBuilder{}
	sawText := false
	var outputText strings.Builder
	var usage Usage
	for _, event := range events {
		if finishSeen && usageSeen {
			return observation, errors.New("provider data follows final usage")
		}
		var chunk chatChunk
		if err := decodeProviderJSON(event, &chunk); err != nil || chunk.Created == nil || chunk.Object != "chat.completion.chunk" || !validProviderIdentity(chunk.ID, 256) || !validProviderIdentity(chunk.Model, 256) || *chunk.Created < 0 || *chunk.Created > maximumProviderTokens || chunk.Choices == nil {
			return observation, errors.New("invalid provider chat chunk")
		}
		if !identitySet {
			responseID, model, created, identitySet = chunk.ID, chunk.Model, *chunk.Created, true
		} else if chunk.ID != responseID || chunk.Model != model || *chunk.Created != created {
			return observation, errors.New("provider response identity substitution")
		}

		if len(chunk.Choices) == 0 {
			if !finishSeen || usageSeen || isMissingOrNull(chunk.Usage) {
				return observation, errors.New("invalid final provider usage event")
			}
			usage, err = decodeProviderUsage(chunk.Usage, maxTotalTokens)
			if err != nil {
				return observation, err
			}
			usageSeen = true
			continue
		}
		if len(chunk.Choices) != 1 || finishSeen || !isMissingOrNull(chunk.Usage) {
			return observation, errors.New("invalid provider choice event")
		}
		choice := chunk.Choices[0]
		if choice.Index == nil || *choice.Index != 0 || len(choice.FinishReason) == 0 || len(choice.Delta) == 0 || !isMissingOrNull(choice.Logprobs) {
			return observation, errors.New("unsupported provider choice")
		}
		var delta chatDelta
		if err := decodeProviderJSON(choice.Delta, &delta); err != nil {
			return observation, errors.New("invalid provider delta")
		}
		finishReason, hasFinish, err := decodeFinishReason(choice.FinishReason)
		if err != nil {
			return observation, err
		}
		if hasFinish {
			if delta.Role != nil || len(delta.Content) != 0 || len(delta.ToolCalls) != 0 {
				return observation, errors.New("finish event carries a delta")
			}
			finish, finishSeen = finishReason, true
			continue
		}
		if delta.Role == nil && len(delta.Content) == 0 && len(delta.ToolCalls) == 0 {
			return observation, errors.New("empty provider delta")
		}
		if !roleSeen && delta.Role == nil {
			return observation, errors.New("provider stream does not start with assistant role")
		}
		if delta.Role != nil {
			if roleSeen || *delta.Role != "assistant" {
				return observation, errors.New("invalid provider delta role")
			}
			roleSeen = true
		}
		if len(delta.Content) != 0 {
			if !bytes.Equal(delta.Content, []byte("null")) {
				var content string
				if decodeProviderJSON(delta.Content, &content) != nil {
					return observation, errors.New("invalid provider content delta")
				}
				if content != "" {
					if len(builders) != 0 {
						return observation, errors.New("mixed provider text and tool deltas")
					}
					sawText = true
					outputText.WriteString(content)
				}
			}
		}
		if len(delta.ToolCalls) != 0 {
			if sawText || bytes.Equal(delta.ToolCalls, []byte("null")) {
				return observation, errors.New("mixed or null provider tool delta")
			}
			if err := accumulateToolCalls(delta.ToolCalls, builders, maxBytes); err != nil {
				return observation, err
			}
		}
	}
	if !identitySet || !roleSeen || !finishSeen || !usageSeen {
		return observation, errors.New("provider SSE lacks role, finish or complete usage")
	}
	toolCalls, err := finishToolCalls(builders)
	if err != nil {
		return observation, err
	}
	if finish == "stop" && len(toolCalls) != 0 || finish == "tool_calls" && len(toolCalls) == 0 {
		return observation, errors.New("provider finish reason differs from deltas")
	}
	hash := sha256.Sum256(raw)
	observation = SSEObservation{
		SHA256: hex.EncodeToString(hash[:]), SizeBytes: len(raw), ResponseID: responseID,
		Model: model, FinishReason: finish, Usage: usage, OutputText: outputText.String(), ToolCalls: toolCalls,
	}
	return observation, nil
}

func splitSSEEvents(raw []byte) ([][]byte, error) {
	if bytes.Contains(raw, []byte{'\r'}) {
		if bytes.Contains(bytes.ReplaceAll(raw, []byte("\r\n"), nil), []byte{'\r'}) {
			return nil, errors.New("invalid provider SSE line ending")
		}
		raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	}
	if !bytes.HasSuffix(raw, []byte("\n\n")) {
		return nil, errors.New("unterminated provider SSE event")
	}
	blocks := bytes.Split(raw[:len(raw)-2], []byte("\n\n"))
	events := make([][]byte, 0, len(blocks))
	for _, block := range blocks {
		if len(block) < 7 || bytes.Contains(block, []byte{'\n'}) || !bytes.HasPrefix(block, []byte("data: ")) {
			return nil, errors.New("unsupported provider SSE field")
		}
		payload := block[6:]
		if len(payload) == 0 {
			return nil, errors.New("empty provider SSE data")
		}
		events = append(events, payload)
	}
	return events, nil
}

func decodeFinishReason(raw []byte) (string, bool, error) {
	if bytes.Equal(raw, []byte("null")) {
		return "", false, nil
	}
	var finish string
	if decodeProviderJSON(raw, &finish) != nil || finish != "stop" && finish != "tool_calls" {
		return "", false, errors.New("unsupported provider finish reason")
	}
	return finish, true, nil
}

func accumulateToolCalls(raw []byte, builders map[int]*toolCallBuilder, maxBytes int) error {
	var deltas []toolCallDelta
	if decodeProviderJSON(raw, &deltas) != nil || len(deltas) == 0 || len(deltas) > 128 {
		return errors.New("invalid provider tool call delta")
	}
	for _, delta := range deltas {
		if delta.Index == nil || *delta.Index < 0 || *delta.Index >= 128 || delta.Function == nil {
			return errors.New("invalid provider tool call index or function")
		}
		index := int(*delta.Index)
		builder := builders[index]
		if builder == nil {
			if index != len(builders) || delta.ID == nil || delta.Type == nil || *delta.Type != "function" {
				return errors.New("incomplete initial provider tool call delta")
			}
			builder = &toolCallBuilder{id: *delta.ID, typeSeen: true}
			builders[index] = builder
		} else {
			if delta.ID != nil && *delta.ID != builder.id || delta.Type != nil && *delta.Type != "function" {
				return errors.New("provider tool call identity substitution")
			}
		}
		if !validProviderIdentity(builder.id, 256) {
			return errors.New("invalid provider tool call ID")
		}
		if delta.Function.Name != nil {
			if builder.name != "" {
				return errors.New("duplicate provider tool function name")
			}
			builder.name = *delta.Function.Name
		}
		if delta.Function.Arguments != nil {
			if len(builder.arguments) > maxBytes-len(*delta.Function.Arguments) {
				return errors.New("provider tool arguments exceed bound")
			}
			builder.arguments += *delta.Function.Arguments
		}
	}
	return nil
}

func finishToolCalls(builders map[int]*toolCallBuilder) ([]ToolCall, error) {
	result := make([]ToolCall, 0, len(builders))
	seenIDs := map[string]bool{}
	for index := 0; index < len(builders); index++ {
		builder := builders[index]
		if builder == nil || !builder.typeSeen || seenIDs[builder.id] || !providerToolName.MatchString(builder.name) || len(builder.name) > 256 || builder.arguments == "" {
			return nil, errors.New("incomplete provider tool call")
		}
		seenIDs[builder.id] = true
		arguments := []byte(builder.arguments)
		if !uniqueProviderJSON(arguments) {
			return nil, errors.New("ambiguous provider tool arguments")
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(arguments, &object) != nil || object == nil {
			return nil, errors.New("provider tool arguments are not an object")
		}
		result = append(result, ToolCall{Index: index, ID: builder.id, Name: builder.name, Arguments: append(json.RawMessage(nil), arguments...)})
	}
	return result, nil
}

func decodeProviderUsage(raw []byte, maxTotalTokens int64) (Usage, error) {
	var wire usageWire
	if decodeProviderJSON(raw, &wire) != nil || wire.PromptTokens == nil || wire.CompletionTokens == nil || wire.TotalTokens == nil {
		return Usage{}, errors.New("provider usage fields missing")
	}
	for _, value := range []*int64{wire.PromptTokens, wire.CompletionTokens, wire.TotalTokens} {
		if *value < 0 || *value > maximumProviderTokens {
			return Usage{}, errors.New("invalid provider usage integer")
		}
	}
	if *wire.PromptTokens > maximumProviderTokens-*wire.CompletionTokens || *wire.TotalTokens != *wire.PromptTokens+*wire.CompletionTokens || *wire.TotalTokens > maxTotalTokens {
		return Usage{}, errors.New("provider usage total differs or exceeds bound")
	}
	usage := Usage{InputTokens: *wire.PromptTokens, OutputTokens: *wire.CompletionTokens}
	if wire.PromptTokensDetails != nil && wire.PromptTokensDetails.CachedTokens != nil {
		value := *wire.PromptTokensDetails.CachedTokens
		if value < 0 || value > usage.InputTokens {
			return Usage{}, errors.New("invalid provider cached-token subset")
		}
		usage.CacheReadTokens = &value
	}
	if wire.CompletionTokensDetails != nil && wire.CompletionTokensDetails.ReasoningTokens != nil {
		value := *wire.CompletionTokensDetails.ReasoningTokens
		if value < 0 || value > usage.OutputTokens {
			return Usage{}, errors.New("invalid provider reasoning-token subset")
		}
		usage.ReasoningTokens = &value
	}
	return usage, nil
}

func validProviderIdentity(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func isMissingOrNull(raw []byte) bool { return len(raw) == 0 || bytes.Equal(raw, []byte("null")) }

func decodeProviderJSON(raw []byte, destination any) error {
	target := reflect.TypeOf(destination)
	if target == nil || target.Kind() != reflect.Pointer || !uniqueProviderJSON(raw) || !exactProviderJSONShape(raw, target.Elem()) {
		return errors.New("ambiguous provider JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination)
}

// exactProviderJSONShape prevents encoding/json's case-insensitive struct-key
// matching from admitting aliases or overwriting an exact field with an alias.
func exactProviderJSONShape(raw []byte, target reflect.Type) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !readExactProviderValue(decoder, target, 0) {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}

func readExactProviderValue(decoder *json.Decoder, target reflect.Type, depth int) bool {
	if depth > 64 {
		return false
	}
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return true
	}
	if target == rawMessageType || target.Kind() == reflect.Interface {
		return readProviderComposite(decoder, delimiter, depth)
	}
	switch delimiter {
	case '{':
		if target.Kind() == reflect.Map {
			for decoder.More() {
				if key, err := decoder.Token(); err != nil {
					return false
				} else if _, ok := key.(string); !ok || !readExactProviderValue(decoder, target.Elem(), depth+1) {
					return false
				}
			}
			closing, err := decoder.Token()
			return err == nil && closing == json.Delim('}')
		}
		if target.Kind() != reflect.Struct {
			return false
		}
		fields := make(map[string]reflect.Type, target.NumField())
		for i := 0; i < target.NumField(); i++ {
			field := target.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				fields[name] = field.Type
			}
		}
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			fieldType, exists := fields[key]
			if err != nil || !ok || !exists || !readExactProviderValue(decoder, fieldType, depth+1) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim('}')
	case '[':
		if target.Kind() != reflect.Slice && target.Kind() != reflect.Array {
			return false
		}
		for decoder.More() {
			if !readExactProviderValue(decoder, target.Elem(), depth+1) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim(']')
	default:
		return false
	}
}

func readProviderComposite(decoder *json.Decoder, opening json.Delim, depth int) bool {
	closing := json.Delim('}')
	if opening == '[' {
		closing = ']'
	} else if opening != '{' {
		return false
	}
	for decoder.More() {
		if opening == '{' {
			if key, err := decoder.Token(); err != nil {
				return false
			} else if _, ok := key.(string); !ok {
				return false
			}
		}
		if !readExactProviderValue(decoder, reflect.TypeOf((*any)(nil)).Elem(), depth+1) {
			return false
		}
	}
	actual, err := decoder.Token()
	return err == nil && actual == closing
}

func uniqueProviderJSON(raw []byte) bool {
	if len(raw) == 0 || !utf8.Valid(raw) || !providerJSONStringsValid(raw) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !readUniqueProviderValue(decoder, 0) {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}

// encoding/json replaces invalid UTF-16 escapes with U+FFFD. Reject them on
// the wire so distinct malformed strings cannot acquire the same identity.
func providerJSONStringsValid(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '"' {
			continue
		}
		for i++; i < len(raw) && raw[i] != '"'; i++ {
			if raw[i] != '\\' {
				continue
			}
			i++
			if i >= len(raw) {
				return false
			}
			if raw[i] != 'u' {
				continue
			}
			if i+4 >= len(raw) {
				return false
			}
			value, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
			if err != nil {
				return false
			}
			i += 4
			if value >= 0xdc00 && value <= 0xdfff {
				return false
			}
			if value >= 0xd800 && value <= 0xdbff {
				if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
					return false
				}
				low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
				if err != nil || low < 0xdc00 || low > 0xdfff {
					return false
				}
				i += 6
			}
		}
	}
	return true
}

func readUniqueProviderValue(decoder *json.Decoder, depth int) bool {
	if depth > 64 {
		return false
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return true
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok || seen[key] {
				return false
			}
			seen[key] = true
			if !readUniqueProviderValue(decoder, depth+1) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim('}')
	case '[':
		for decoder.More() {
			if !readUniqueProviderValue(decoder, depth+1) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim(']')
	default:
		return false
	}
}
