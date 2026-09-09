package providergateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"slices"
	"strings"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
)

// ResponsesModelCapabilities is the protocol-specific portion of a model
// contract. It describes admitted wire features, not provider availability or
// a claim that a model implements them. The containing model contract must bind
// its canonical JSON before this value is used for request admission.
//
// Schema references:
//   - https://developers.openai.com/api/reference/cli/resources/responses/methods/create
//   - https://github.com/vercel/ai/blob/ai%406.0.84/packages/openai/src/responses/openai-responses-language-model.ts
type ResponsesModelCapabilities struct {
	Version                  int      `json:"version"`
	FunctionTools            bool     `json:"function_tools"`
	Reasoning                bool     `json:"reasoning"`
	SystemRoles              []string `json:"system_roles"`
	Sampling                 bool     `json:"sampling"`
	TemperatureMin           *string  `json:"temperature_min,omitempty"`
	TemperatureMax           *string  `json:"temperature_max,omitempty"`
	TopPMin                  *string  `json:"top_p_min,omitempty"`
	TopPMax                  *string  `json:"top_p_max,omitempty"`
	TextFormats              []string `json:"text_formats"`
	KnownExtensions          []string `json:"known_extensions"`
	TrailingCostPingV1       bool     `json:"trailing_cost_ping_v1"`
	ContentPartCompletesText bool     `json:"content_part_completes_text,omitempty"`
	FunctionCallDoneNameV1   bool     `json:"function_call_done_name_v1,omitempty"`
}

// ResponsesRequestExpectation fixes every optional control that may appear on
// one admitted request. Empty strings and nil pointers require field omission.
// ToolChoice is "", "auto", "none", "required", or "function:<name>".
// json.Number preserves decimal values without converting them to float64.
type ResponsesRequestExpectation struct {
	MaxOutputTokens    int64           `json:"max_output_tokens"`
	StateMode          string          `json:"state_mode"`
	Store              bool            `json:"store"`
	PreviousResponseID string          `json:"previous_response_id"`
	SystemRole         string          `json:"system_role"`
	ReasoningEffort    string          `json:"reasoning_effort"`
	ReasoningSummary   string          `json:"reasoning_summary"`
	ToolChoice         string          `json:"tool_choice"`
	ParallelToolCalls  *bool           `json:"parallel_tool_calls"`
	Instructions       *string         `json:"instructions"`
	TextFormat         string          `json:"text_format"`
	TextSchemaName     string          `json:"text_schema_name"`
	TextSchema         json.RawMessage `json:"text_schema,omitempty"`
	TextVerbosity      string          `json:"text_verbosity"`
	Temperature        *json.Number    `json:"temperature"`
	TopP               *json.Number    `json:"top_p"`
	Include            []string        `json:"include"`
	ServiceTier        string          `json:"service_tier"`
	PromptCacheKey     string          `json:"prompt_cache_key"`
	FunctionToolStrict bool            `json:"function_tool_strict"`
}

// ResponsesRequestObservation retains wire identity and non-content facts. The
// validated request bytes remain caller-owned and are never copied here.
type ResponsesRequestObservation struct {
	SHA256          string `json:"sha256"`
	SizeBytes       int64  `json:"size_bytes"`
	Model           string `json:"model"`
	MaxOutputTokens int64  `json:"max_output_tokens"`
	InputItemCount  int    `json:"input_item_count"`
	ToolCount       int    `json:"tool_count"`
	StateMode       string `json:"state_mode"`
	HasReasoning    bool   `json:"has_reasoning"`
}

// validateResponsesModelCapabilities admits the exact canonical object stored
// by a v2 model contract. Arrays are ordered sets so two differently ordered
// configurations cannot acquire the same operational interpretation.
func validateResponsesModelCapabilities(raw json.RawMessage) error {
	if !uniqueProviderJSON(raw) {
		return errors.New("invalid Responses capability JSON")
	}
	object, err := exactJSONObject(raw,
		[]string{"version", "function_tools", "reasoning", "system_roles", "sampling", "text_formats", "known_extensions", "trailing_cost_ping_v1"},
		[]string{"temperature_min", "temperature_max", "top_p_min", "top_p_max", "content_part_completes_text", "function_call_done_name_v1"})
	if err != nil {
		return errors.New("invalid Responses capability schema")
	}
	version, versionOK := jsonInteger(object["version"])
	functionTools, functionToolsOK := jsonBoolean(object["function_tools"])
	reasoning, reasoningOK := jsonBoolean(object["reasoning"])
	sampling, samplingOK := jsonBoolean(object["sampling"])
	trailingCostPing, trailingCostPingOK := jsonBoolean(object["trailing_cost_ping_v1"])
	contentPartCompletesText := false
	contentPartCompletesTextOK := true
	if raw, ok := object["content_part_completes_text"]; ok {
		contentPartCompletesText, contentPartCompletesTextOK = jsonBoolean(raw)
	}
	functionCallDoneNameV1 := false
	functionCallDoneNameV1OK := true
	if raw, ok := object["function_call_done_name_v1"]; ok {
		functionCallDoneNameV1, functionCallDoneNameV1OK = jsonBoolean(raw)
	}
	systemRoles, systemRolesOK := responsesStringArray(object["system_roles"])
	textFormats, textFormatsOK := responsesStringArray(object["text_formats"])
	knownExtensions, knownExtensionsOK := responsesStringArray(object["known_extensions"])
	if !versionOK || !functionToolsOK || !reasoningOK || !samplingOK || !trailingCostPingOK || !contentPartCompletesTextOK || !functionCallDoneNameV1OK || !systemRolesOK || !textFormatsOK || !knownExtensionsOK {
		return errors.New("invalid Responses capability values")
	}
	temperatureMin, temperatureMinOK := optionalResponsesDecimal(object, "temperature_min")
	temperatureMax, temperatureMaxOK := optionalResponsesDecimal(object, "temperature_max")
	topPMin, topPMinOK := optionalResponsesDecimal(object, "top_p_min")
	topPMax, topPMaxOK := optionalResponsesDecimal(object, "top_p_max")
	if !temperatureMinOK || !temperatureMaxOK || !topPMinOK || !topPMaxOK {
		return errors.New("invalid Responses sampling capability bounds")
	}
	value := ResponsesModelCapabilities{
		Version: int(version), FunctionTools: functionTools, Reasoning: reasoning,
		SystemRoles: systemRoles, Sampling: sampling, TemperatureMin: temperatureMin,
		TemperatureMax: temperatureMax, TopPMin: topPMin, TopPMax: topPMax,
		TextFormats: textFormats, KnownExtensions: knownExtensions,
		TrailingCostPingV1: trailingCostPing, ContentPartCompletesText: contentPartCompletesText, FunctionCallDoneNameV1: functionCallDoneNameV1,
	}
	return value.validate()
}

func optionalResponsesDecimal(object map[string]json.RawMessage, key string) (*string, bool) {
	raw, exists := object[key]
	if !exists {
		return nil, true
	}
	value, ok := jsonString(raw)
	if !ok || !validJSONNumber(json.Number(value)) {
		return nil, false
	}
	return &value, true
}

func responsesStringArray(raw json.RawMessage) ([]string, bool) {
	items, err := jsonArray(raw, 0, 32)
	if err != nil {
		return nil, false
	}
	values := make([]string, len(items))
	for index, item := range items {
		value, ok := jsonString(item)
		if !ok {
			return nil, false
		}
		values[index] = value
	}
	return values, true
}

func (c ResponsesModelCapabilities) validate() error {
	if c.Version != 1 || !orderedUniqueAllowed(c.SystemRoles, []string{"developer", "system"}) || !orderedUniqueAllowed(c.TextFormats, []string{"json_object", "json_schema", "plain"}) || !orderedUniqueAllowed(c.KnownExtensions, []string{"prompt_cache_key", "service_tier"}) {
		return errors.New("invalid Responses model capabilities")
	}
	if len(c.SystemRoles) == 0 || len(c.TextFormats) == 0 {
		return errors.New("Responses model capabilities require text and system-role declarations")
	}
	if !c.Sampling && (c.TemperatureMin != nil || c.TemperatureMax != nil || c.TopPMin != nil || c.TopPMax != nil) {
		return errors.New("Responses sampling ranges require sampling support")
	}
	if c.FunctionCallDoneNameV1 && !c.FunctionTools {
		return errors.New("Responses function-call done-name capability requires function tools")
	}
	if !validResponsesRange(c.TemperatureMin, c.TemperatureMax) || !validResponsesRange(c.TopPMin, c.TopPMax) {
		return errors.New("invalid Responses sampling range")
	}
	return nil
}

func validResponsesRange(minimum, maximum *string) bool {
	if (minimum == nil) != (maximum == nil) {
		return false
	}
	if minimum == nil {
		return true
	}
	comparison, ok := compareResponsesNumbers(json.Number(*minimum), json.Number(*maximum))
	return ok && comparison <= 0
}

func orderedUniqueAllowed(values, allowed []string) bool {
	if !slices.IsSorted(values) {
		return false
	}
	for index, value := range values {
		if !slices.Contains(allowed, value) || index > 0 && values[index-1] == value {
			return false
		}
	}
	return true
}

// ValidateResponsesRequest validates the text/function-tool request emitted by
// the pinned @ai-sdk/openai Responses adapter. It does not forward the request,
// resolve credentials, parse a response, or infer model capabilities from an ID.
func ValidateResponsesRequest(raw []byte, maxBytes int64, binding Binding, capabilities ResponsesModelCapabilities, expected ResponsesRequestExpectation, allowed []RequestTool) (ResponsesRequestObservation, error) {
	return validateResponsesRequestForFraming(raw, maxBytes, binding, capabilities, expected, allowed, ResponseFramingSSE)
}

func validateResponsesRequestForFraming(raw []byte, maxBytes int64, binding Binding, capabilities ResponsesModelCapabilities, expected ResponsesRequestExpectation, allowed []RequestTool, framing ResponseFraming) (ResponsesRequestObservation, error) {
	encodedCapabilities, encodeErr := canonical.Bytes(capabilities)
	if _, err := binding.ID(); err != nil || encodeErr != nil || binding.Model.Version != 2 || binding.Model.AdapterID != OpenAIResponsesAdapter || binding.Model.Capabilities == nil || binding.Model.Capabilities.Tools != capabilities.FunctionTools || binding.Model.Capabilities.Reasoning != capabilities.Reasoning || !bytes.Equal(encodedCapabilities, binding.Model.AdapterCapabilities) || capabilities.validate() != nil || maxBytes < 2 || maxBytes > binding.Model.MaxRequestBytes || int64(len(raw)) > maxBytes || !uniqueProviderJSON(raw) {
		return ResponsesRequestObservation{}, errors.New("invalid Responses request admission bounds, binding, capabilities or JSON")
	}
	if err := validateResponsesExpectation(binding, capabilities, expected, allowed); err != nil {
		return ResponsesRequestObservation{}, err
	}
	object, err := exactJSONObject(raw,
		[]string{"model", "input", "max_output_tokens", "stream", "store"},
		[]string{"tools", "tool_choice", "parallel_tool_calls", "reasoning", "include", "instructions", "text", "temperature", "top_p", "previous_response_id", "service_tier", "prompt_cache_key"})
	if err != nil {
		return ResponsesRequestObservation{}, errors.New("unsupported Responses request schema")
	}
	model, modelOK := jsonString(object["model"])
	maxTokens, tokensOK := jsonInteger(object["max_output_tokens"])
	stream, streamOK := jsonBoolean(object["stream"])
	store, storeOK := jsonBoolean(object["store"])
	if !modelOK || model != binding.Model.Model || !tokensOK || maxTokens != expected.MaxOutputTokens || !streamOK || stream != (framing == ResponseFramingSSE) || !storeOK || store != expected.Store {
		return ResponsesRequestObservation{}, errors.New("Responses model, output cap, stream or storage policy mismatch")
	}
	if err := validateExpectedResponsesControls(object, capabilities, expected); err != nil {
		return ResponsesRequestObservation{}, err
	}
	toolNames, err := validateResponsesTools(object["tools"], allowed, expected.FunctionToolStrict)
	if err != nil {
		return ResponsesRequestObservation{}, err
	}
	if err := validateResponsesToolChoice(object["tool_choice"], expected.ToolChoice, toolNames); err != nil {
		return ResponsesRequestObservation{}, err
	}
	items, hasReasoning, err := validateResponsesInput(object["input"], capabilities, expected, toolNames, maxBytes)
	if err != nil {
		return ResponsesRequestObservation{}, err
	}
	digest := sha256.Sum256(raw)
	return ResponsesRequestObservation{
		SHA256: hex.EncodeToString(digest[:]), SizeBytes: int64(len(raw)), Model: model,
		MaxOutputTokens: maxTokens, InputItemCount: items, ToolCount: len(allowed),
		StateMode: expected.StateMode, HasReasoning: hasReasoning,
	}, nil
}

func validateResponsesExpectation(binding Binding, capabilities ResponsesModelCapabilities, expected ResponsesRequestExpectation, allowed []RequestTool) error {
	if expected.StateMode != "full-input-stateless" || expected.Store || expected.PreviousResponseID != "" {
		return errors.New("unsupported or internally inconsistent Responses state mode")
	}
	if expected.MaxOutputTokens < 1 || expected.MaxOutputTokens > binding.Model.MaxOutputTokens || !slices.Contains(capabilities.SystemRoles, expected.SystemRole) {
		return errors.New("Responses output or system-role expectation exceeds model contract")
	}
	if len(allowed) > 64 || len(allowed) > 0 && !capabilities.FunctionTools {
		return errors.New("Responses function tools differ from model capabilities")
	}
	if expected.ReasoningEffort != "" || expected.ReasoningSummary != "" {
		if !capabilities.Reasoning || !requestToken(expected.ReasoningEffort) || !requestToken(expected.ReasoningSummary) {
			return errors.New("Responses reasoning expectation exceeds model capabilities")
		}
	}
	reasoningSelected := expected.ReasoningEffort != "" || expected.ReasoningSummary != ""
	if reasoningSelected {
		if len(expected.Include) != 1 || expected.Include[0] != "reasoning.encrypted_content" {
			return errors.New("stateless reasoning requires encrypted reasoning content")
		}
	} else if len(expected.Include) != 0 {
		return errors.New("unexpected Responses include fields")
	}
	if expected.TextFormat != "" && !slices.Contains(capabilities.TextFormats, expected.TextFormat) {
		return errors.New("unsupported Responses text format")
	}
	if err := validateResponsesStructuredExpectation(expected, binding.Model.Capabilities); err != nil {
		return err
	}
	if expected.TextVerbosity != "" && !requestToken(expected.TextVerbosity) {
		return errors.New("invalid Responses text verbosity")
	}
	if (expected.Temperature != nil || expected.TopP != nil) && !capabilities.Sampling {
		return errors.New("Responses sampling expectation exceeds model capabilities")
	}
	for _, value := range []*json.Number{expected.Temperature, expected.TopP} {
		if value != nil && !validJSONNumber(*value) {
			return errors.New("invalid Responses decimal expectation")
		}
	}
	if !responsesNumberWithin(expected.Temperature, capabilities.TemperatureMin, capabilities.TemperatureMax) ||
		!responsesNumberWithin(expected.TopP, capabilities.TopPMin, capabilities.TopPMax) {
		return errors.New("Responses sampling expectation exceeds model range")
	}
	if expected.Instructions != nil && (!utf8.ValidString(*expected.Instructions) || *expected.Instructions == "" || int64(len(*expected.Instructions)) > binding.Model.MaxRequestBytes) {
		return errors.New("invalid Responses instructions expectation")
	}
	if expected.ServiceTier != "" && (!slices.Contains(capabilities.KnownExtensions, "service_tier") || !requestToken(expected.ServiceTier)) {
		return errors.New("unsupported Responses service tier")
	}
	if expected.PromptCacheKey != "" && (!slices.Contains(capabilities.KnownExtensions, "prompt_cache_key") || !printable(expected.PromptCacheKey, 256)) {
		return errors.New("unsupported Responses prompt cache key")
	}
	if expected.ParallelToolCalls != nil && len(allowed) == 0 {
		return errors.New("parallel tool control without tools")
	}
	return nil
}

func validateExpectedResponsesControls(object map[string]json.RawMessage, capabilities ResponsesModelCapabilities, expected ResponsesRequestExpectation) error {
	if !optionalStringEquals(object, "previous_response_id", expected.PreviousResponseID) ||
		!optionalStringPointerEquals(object, "instructions", expected.Instructions) ||
		!optionalStringEquals(object, "service_tier", expected.ServiceTier) ||
		!optionalStringEquals(object, "prompt_cache_key", expected.PromptCacheKey) ||
		!optionalBooleanEquals(object, "parallel_tool_calls", expected.ParallelToolCalls) ||
		!optionalNumberEquals(object, "temperature", expected.Temperature) ||
		!optionalNumberEquals(object, "top_p", expected.TopP) {
		return errors.New("Responses optional control differs from expectation")
	}
	if err := validateResponsesReasoning(object["reasoning"], capabilities, expected); err != nil {
		return err
	}
	if err := validateResponsesInclude(object["include"], expected.Include); err != nil {
		return err
	}
	return validateResponsesText(object["text"], expected)
}

func optionalStringEquals(object map[string]json.RawMessage, key, expected string) bool {
	raw, exists := object[key]
	if expected == "" {
		return !exists
	}
	actual, ok := jsonString(raw)
	return exists && ok && actual == expected
}

func optionalStringPointerEquals(object map[string]json.RawMessage, key string, expected *string) bool {
	raw, exists := object[key]
	if expected == nil {
		return !exists
	}
	actual, ok := jsonString(raw)
	return exists && ok && actual == *expected
}

func optionalBooleanEquals(object map[string]json.RawMessage, key string, expected *bool) bool {
	raw, exists := object[key]
	if expected == nil {
		return !exists
	}
	actual, ok := jsonBoolean(raw)
	return exists && ok && actual == *expected
}

func optionalNumberEquals(object map[string]json.RawMessage, key string, expected *json.Number) bool {
	raw, exists := object[key]
	if expected == nil {
		return !exists
	}
	if !exists || !validJSONNumber(*expected) {
		return false
	}
	var actual json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&actual) != nil || !validJSONNumber(actual) {
		return false
	}
	comparison, ok := compareResponsesNumbers(actual, *expected)
	return ok && comparison == 0
}

func validJSONNumber(value json.Number) bool {
	text := value.String()
	if !boundedJSONNumberLexeme(text) {
		return false
	}
	var decoded json.Number
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if decoder.Decode(&decoded) != nil {
		return false
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return false
	}
	_, ok := new(big.Rat).SetString(decoded.String())
	return ok
}

// boundedJSONNumberLexeme limits rational construction independently of the
// request byte cap. The limits are parser resource bounds, not provider model
// capabilities. Sampling support and any known provider range are bound by the
// model contract separately.
func boundedJSONNumberLexeme(text string) bool {
	const maxLexemeBytes = 128
	const maxSignificandDigits = 96
	const maxAbsoluteExponent = 1024
	if text == "" || len(text) > maxLexemeBytes {
		return false
	}
	index := 0
	if text[index] == '-' {
		index++
		if index == len(text) {
			return false
		}
	}
	digits := 0
	if text[index] == '0' {
		index++
		if index < len(text) && text[index] >= '0' && text[index] <= '9' {
			return false
		}
	} else if text[index] >= '1' && text[index] <= '9' {
		for index < len(text) && text[index] >= '0' && text[index] <= '9' {
			index++
			digits++
		}
	} else {
		return false
	}
	if digits == 0 {
		digits = 1
	}
	if index < len(text) && text[index] == '.' {
		index++
		fractionStart := index
		for index < len(text) && text[index] >= '0' && text[index] <= '9' {
			index++
			digits++
		}
		if index == fractionStart {
			return false
		}
	}
	if digits > maxSignificandDigits {
		return false
	}
	if index < len(text) && (text[index] == 'e' || text[index] == 'E') {
		index++
		if index == len(text) {
			return false
		}
		if text[index] == '+' || text[index] == '-' {
			index++
			if index == len(text) {
				return false
			}
		}
		exponent := 0
		exponentStart := index
		for index < len(text) && text[index] >= '0' && text[index] <= '9' {
			if exponent > maxAbsoluteExponent/10 {
				return false
			}
			exponent = exponent*10 + int(text[index]-'0')
			if exponent > maxAbsoluteExponent {
				return false
			}
			index++
		}
		if index == exponentStart {
			return false
		}
	}
	return index == len(text)
}

func compareResponsesNumbers(leftValue, rightValue json.Number) (int, bool) {
	if !validJSONNumber(leftValue) || !validJSONNumber(rightValue) {
		return 0, false
	}
	left, right := new(big.Rat), new(big.Rat)
	_, leftOK := left.SetString(leftValue.String())
	_, rightOK := right.SetString(rightValue.String())
	if !leftOK || !rightOK {
		return 0, false
	}
	return left.Cmp(right), true
}

func responsesNumberWithin(value *json.Number, minimum, maximum *string) bool {
	if value == nil || minimum == nil && maximum == nil {
		return true
	}
	if minimum == nil || maximum == nil {
		return false
	}
	lower, lowerOK := compareResponsesNumbers(*value, json.Number(*minimum))
	upper, upperOK := compareResponsesNumbers(*value, json.Number(*maximum))
	return lowerOK && upperOK && lower >= 0 && upper <= 0
}

func validateResponsesReasoning(raw json.RawMessage, capabilities ResponsesModelCapabilities, expected ResponsesRequestExpectation) error {
	if expected.ReasoningEffort == "" && expected.ReasoningSummary == "" {
		if raw != nil {
			return errors.New("unexpected Responses reasoning control")
		}
		return nil
	}
	if !capabilities.Reasoning {
		return errors.New("Responses reasoning not supported by model contract")
	}
	object, err := exactJSONObject(raw, []string{"effort", "summary"}, nil)
	effort, effortOK := jsonString(object["effort"])
	summary, summaryOK := jsonString(object["summary"])
	if err != nil || !effortOK || !summaryOK || effort != expected.ReasoningEffort || summary != expected.ReasoningSummary {
		return errors.New("Responses reasoning control differs from expectation")
	}
	return nil
}

func validateResponsesInclude(raw json.RawMessage, expected []string) error {
	if len(expected) == 0 {
		if raw != nil {
			return errors.New("unexpected Responses include control")
		}
		return nil
	}
	items, err := jsonArray(raw, len(expected), len(expected))
	if err != nil {
		return errors.New("invalid Responses include control")
	}
	for index, item := range items {
		value, ok := jsonString(item)
		if !ok || value != expected[index] {
			return errors.New("Responses include control differs from expectation")
		}
	}
	return nil
}

func validateResponsesText(raw json.RawMessage, expected ResponsesRequestExpectation) error {
	format, verbosity := expected.TextFormat, expected.TextVerbosity
	if format == "" && verbosity == "" {
		if raw != nil {
			return errors.New("unexpected Responses text control")
		}
		return nil
	}
	required := []string{}
	optional := []string{}
	if format != "" {
		required = append(required, "format")
	}
	if verbosity != "" {
		required = append(required, "verbosity")
	}
	object, err := exactJSONObject(raw, required, optional)
	if err != nil {
		return errors.New("invalid Responses text control")
	}
	if verbosity != "" {
		actual, ok := jsonString(object["verbosity"])
		if !ok || actual != verbosity {
			return errors.New("Responses text verbosity differs from expectation")
		}
	}
	if format == "" {
		return nil
	}
	switch format {
	case "plain":
		value, err := exactJSONObject(object["format"], []string{"type"}, nil)
		kind, ok := jsonString(value["type"])
		if err != nil || !ok || kind != "text" {
			return errors.New("Responses plain text format differs from expectation")
		}
	case "json_object":
		value, err := exactJSONObject(object["format"], []string{"type"}, nil)
		kind, ok := jsonString(value["type"])
		if err != nil || !ok || kind != "json_object" {
			return errors.New("Responses JSON format differs from expectation")
		}
	case "json_schema":
		value, err := exactJSONObject(object["format"], []string{"type", "name", "schema", "strict"}, nil)
		kind, kindOK := jsonString(value["type"])
		name, nameOK := jsonString(value["name"])
		strict, strictOK := jsonBoolean(value["strict"])
		if err != nil || !kindOK || kind != "json_schema" || !nameOK || name != expected.TextSchemaName || !strictOK || !strict || !bytes.Equal(value["schema"], expected.TextSchema) {
			return errors.New("Responses strict schema format differs from expectation")
		}
	default:
		return errors.New("unsupported Responses text format")
	}
	return nil
}

func validateResponsesStructuredExpectation(expected ResponsesRequestExpectation, capabilities *ModelCapabilities) error {
	if capabilities == nil {
		return errors.New("missing Responses model capabilities")
	}
	switch expected.TextFormat {
	case "", "plain":
		if expected.TextSchemaName != "" || len(expected.TextSchema) != 0 {
			return errors.New("plain Responses output cannot carry a schema")
		}
	case "json_object":
		if !slices.Contains(capabilities.StructuredOutputModes, StructuredOutputJSONOnly) || expected.TextSchemaName != "" || len(expected.TextSchema) != 0 {
			return ErrCapabilityUnavailable
		}
	case "json_schema":
		if !slices.Contains(capabilities.StructuredOutputModes, StructuredOutputStrictSchema) || !identifier(expected.TextSchemaName) || len(expected.TextSchemaName) > 64 || validateStructuredOutputSchema(expected.TextSchema) != nil {
			return ErrCapabilityUnavailable
		}
	default:
		return errors.New("unsupported Responses structured output mode")
	}
	return nil
}

func validateResponsesTools(raw json.RawMessage, expected []RequestTool, strict bool) (map[string]bool, error) {
	names := make(map[string]bool, len(expected))
	for _, tool := range expected {
		if !requestToolIdentifier(tool.Name) || tool.Description == "" || len(tool.Description) > 8192 || !utf8.ValidString(tool.Description) || names[tool.Name] || !jsonObject(tool.Parameters) {
			return nil, errors.New("invalid expected Responses function catalog")
		}
		names[tool.Name] = true
	}
	if len(expected) == 0 {
		if raw != nil {
			return nil, errors.New("unexpected Responses function catalog")
		}
		return names, nil
	}
	items, err := jsonArray(raw, len(expected), len(expected))
	if err != nil {
		return nil, errors.New("Responses function catalog differs from expectation")
	}
	for index, item := range items {
		object, objectErr := exactJSONObject(item, []string{"type", "name", "description", "parameters", "strict"}, nil)
		kind, kindOK := jsonString(object["type"])
		name, nameOK := jsonString(object["name"])
		description, descriptionOK := jsonString(object["description"])
		actualStrict, strictOK := jsonBoolean(object["strict"])
		if objectErr != nil || !kindOK || kind != "function" || !nameOK || name != expected[index].Name || !descriptionOK || description != expected[index].Description || !strictOK || actualStrict != strict || !sameJSON(object["parameters"], expected[index].Parameters) {
			return nil, errors.New("Responses function catalog differs from expectation")
		}
	}
	return names, nil
}

func validateResponsesToolChoice(raw json.RawMessage, expected string, tools map[string]bool) error {
	if expected == "" {
		if raw != nil {
			return errors.New("unexpected Responses tool choice")
		}
		return nil
	}
	if len(tools) == 0 {
		return errors.New("Responses tool choice requires a function catalog")
	}
	if name, selected := strings.CutPrefix(expected, "function:"); selected {
		object, err := exactJSONObject(raw, []string{"type", "name"}, nil)
		kind, kindOK := jsonString(object["type"])
		actual, nameOK := jsonString(object["name"])
		if err != nil || !kindOK || kind != "function" || !nameOK || actual != name || !tools[name] {
			return errors.New("Responses named tool choice differs from expectation")
		}
		return nil
	}
	choice, ok := jsonString(raw)
	if !ok || choice != expected || choice != "auto" && choice != "none" && choice != "required" {
		return errors.New("Responses tool choice differs from expectation")
	}
	return nil
}

func validateResponsesInput(raw json.RawMessage, capabilities ResponsesModelCapabilities, expected ResponsesRequestExpectation, tools map[string]bool, maxBytes int64) (int, bool, error) {
	items, err := jsonArray(raw, 1, 256)
	if err != nil {
		return 0, false, errors.New("invalid Responses input list")
	}
	seenUser := false
	lastKind := ""
	pending := map[string]string{}
	seenCalls := map[string]bool{}
	seenItemIDs := map[string]bool{}
	hasReasoning := false
	for _, item := range items {
		object, objectErr := exactJSONObject(item, nil, []string{"role", "content", "type", "id", "call_id", "name", "arguments", "output", "encrypted_content", "summary", "phase"})
		if objectErr != nil {
			return 0, false, errors.New("invalid Responses input item")
		}
		if role, roleOK := jsonString(object["role"]); roleOK {
			if object["type"] != nil || object["call_id"] != nil || object["name"] != nil || object["arguments"] != nil || object["output"] != nil || object["encrypted_content"] != nil || object["summary"] != nil {
				return 0, false, errors.New("mixed Responses message and item fields")
			}
			switch role {
			case "system", "developer":
				if seenUser || role != expected.SystemRole || !nonemptyBoundedText(object["content"], maxBytes) || object["id"] != nil || object["phase"] != nil {
					return 0, false, errors.New("invalid Responses system message")
				}
			case "user":
				if len(pending) != 0 || object["id"] != nil || object["phase"] != nil || validateResponsesContent(object["content"], "input_text", maxBytes) != nil {
					return 0, false, errors.New("invalid Responses user message")
				}
				seenUser = true
			case "assistant":
				if !seenUser || len(pending) != 0 || validateResponsesContent(object["content"], "output_text", maxBytes) != nil || !optionalPrintableID(object["id"]) || !validResponsesAssistantPhase(object["phase"]) {
					return 0, false, errors.New("invalid Responses assistant message")
				}
				if !reserveResponsesItemID(object["id"], seenItemIDs) {
					return 0, false, errors.New("duplicate Responses item ID")
				}
			default:
				return 0, false, errors.New("unsupported Responses message role")
			}
			lastKind = role
			continue
		}
		kind, kindOK := jsonString(object["type"])
		if !kindOK || !seenUser {
			return 0, false, errors.New("invalid Responses typed input item")
		}
		switch kind {
		case "function_call":
			if !capabilities.FunctionTools || object["content"] != nil || object["output"] != nil || object["encrypted_content"] != nil || object["summary"] != nil || object["phase"] != nil || !optionalPrintableID(object["id"]) {
				return 0, false, errors.New("invalid Responses function call")
			}
			callID, callOK := jsonString(object["call_id"])
			name, nameOK := jsonString(object["name"])
			arguments, argumentsOK := jsonString(object["arguments"])
			if !callOK || !printable(callID, 256) || seenCalls[callID] || !nameOK || !tools[name] || !argumentsOK || len(arguments) < 2 || int64(len(arguments)) > maxBytes || !jsonObject([]byte(arguments)) {
				return 0, false, errors.New("invalid Responses function identity or arguments")
			}
			if !reserveResponsesItemID(object["id"], seenItemIDs) {
				return 0, false, errors.New("duplicate Responses item ID")
			}
			seenCalls[callID] = true
			pending[callID] = name
		case "function_call_output":
			if object["content"] != nil || object["name"] != nil || object["arguments"] != nil || object["id"] != nil || object["encrypted_content"] != nil || object["summary"] != nil || object["phase"] != nil {
				return 0, false, errors.New("invalid Responses function output")
			}
			callID, callOK := jsonString(object["call_id"])
			if !callOK || pending[callID] == "" || !nonemptyBoundedText(object["output"], maxBytes) {
				return 0, false, errors.New("unmatched or invalid Responses function output")
			}
			delete(pending, callID)
		case "reasoning":
			if !capabilities.Reasoning || object["content"] != nil || object["call_id"] != nil || object["name"] != nil || object["arguments"] != nil || object["output"] != nil || object["phase"] != nil {
				return 0, false, errors.New("unexpected Responses reasoning item")
			}
			encrypted, encryptedOK := jsonString(object["encrypted_content"])
			if !optionalPrintableID(object["id"]) || !encryptedOK || encrypted == "" || int64(len(encrypted)) > maxBytes || validateReasoningSummary(object["summary"], maxBytes) != nil {
				return 0, false, errors.New("invalid stateless Responses reasoning item")
			}
			if !reserveResponsesItemID(object["id"], seenItemIDs) {
				return 0, false, errors.New("duplicate Responses item ID")
			}
			hasReasoning = true
		default:
			return 0, false, errors.New("unsupported Responses input item type")
		}
		lastKind = kind
	}
	if !seenUser || len(pending) != 0 || lastKind != "user" && lastKind != "function_call_output" {
		return 0, false, errors.New("Responses request ends with unresolved input state")
	}
	return len(items), hasReasoning, nil
}

func reserveResponsesItemID(raw json.RawMessage, seen map[string]bool) bool {
	if raw == nil {
		return true
	}
	id, ok := jsonString(raw)
	if !ok || seen[id] {
		return false
	}
	seen[id] = true
	return true
}

func validateResponsesContent(raw json.RawMessage, expectedType string, maxBytes int64) error {
	items, err := jsonArray(raw, 1, 256)
	if err != nil {
		return err
	}
	for _, item := range items {
		object, objectErr := exactJSONObject(item, []string{"type", "text"}, nil)
		kind, kindOK := jsonString(object["type"])
		if objectErr != nil || !kindOK || kind != expectedType || !nonemptyBoundedText(object["text"], maxBytes) {
			return errors.New("invalid Responses message content")
		}
	}
	return nil
}

func validateReasoningSummary(raw json.RawMessage, maxBytes int64) error {
	items, err := jsonArray(raw, 0, 256)
	if err != nil {
		return err
	}
	for _, item := range items {
		object, objectErr := exactJSONObject(item, []string{"type", "text"}, nil)
		kind, kindOK := jsonString(object["type"])
		text, textOK := jsonString(object["text"])
		if objectErr != nil || !kindOK || kind != "summary_text" || !textOK || !utf8.ValidString(text) || int64(len(text)) > maxBytes {
			return errors.New("invalid Responses reasoning summary")
		}
	}
	return nil
}

func optionalPrintableID(raw json.RawMessage) bool {
	if raw == nil {
		return true
	}
	value, ok := jsonString(raw)
	return ok && printable(value, 256)
}

func validResponsesAssistantPhase(raw json.RawMessage) bool {
	if raw == nil || bytes.Equal(raw, []byte("null")) {
		return true
	}
	phase, ok := jsonString(raw)
	return ok && (phase == "commentary" || phase == "final_answer")
}

func nonemptyBoundedText(raw json.RawMessage, maxBytes int64) bool {
	value, ok := jsonString(raw)
	return ok && value != "" && utf8.ValidString(value) && int64(len(value)) <= maxBytes
}

func requestToken(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
