package providergateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"path"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/safepath"
)

// ErrCapabilityUnavailable is the stable fail-closed class returned when the
// selected model and adapter cannot satisfy an explicit requirement.
var ErrCapabilityUnavailable = errors.New("CAPABILITY_UNAVAILABLE")

const (
	// OpenAIChatCompletionsAdapter identifies the supported Chat Completions codec.
	OpenAIChatCompletionsAdapter = "openai-chat-completions-sse-v1"
	// OpenAIResponsesAdapter identifies the supported Responses codec.
	OpenAIResponsesAdapter = "openai-responses-sse-v1"
	// AnthropicMessagesAdapter identifies the supported Messages codec.
	AnthropicMessagesAdapter = "anthropic-messages-sse-v1"
)

// AuthContract identifies controller-side credential resolution and framing.
// CredentialRef is a policy key, never the secret value.
type AuthContract struct {
	Scheme        string `json:"scheme"`
	CredentialRef string `json:"credential_ref"`
	HeaderName    string `json:"header_name,omitempty"`
}

// ModelCapabilities are declared provider/model features. They are admission
// policy, not proof that an adversarial upstream honors its declaration.
type ModelCapabilities struct {
	Tools                     bool                   `json:"tools"`
	Reasoning                 bool                   `json:"reasoning"`
	OutputCap                 bool                   `json:"output_cap"`
	CompleteUsage             bool                   `json:"complete_usage"`
	StructuredOutputModes     []StructuredOutputMode `json:"structured_output_modes,omitempty"`
	SupportedResponseFramings []ResponseFraming      `json:"supported_response_framings,omitempty"`
}

// StructuredOutputMode is the selected controller contract for interpreting a
// final assistant text. The finite values deliberately do not infer one wire
// protocol's structured-output feature from another protocol.
type StructuredOutputMode string

const (
	// StructuredOutputUnsupported selects no structured-output contract.
	StructuredOutputUnsupported StructuredOutputMode = "UNSUPPORTED"
	// StructuredOutputTextParseRequired requires the caller to parse text.
	StructuredOutputTextParseRequired StructuredOutputMode = "TEXT_PARSE_REQUIRED"
	// StructuredOutputJSONOnly requires native JSON mode and exact JSON output.
	StructuredOutputJSONOnly StructuredOutputMode = "JSON_ONLY"
	// StructuredOutputStrictSchema requires native strict mode and schema validation.
	StructuredOutputStrictSchema StructuredOutputMode = "STRICT_SCHEMA"
)

// ResponseFraming selects the finite wire framing for one provider call. The
// empty value preserves the original SSE contract and identity.
type ResponseFraming string

// Supported response framing values select finite native wire codecs.
const (
	ResponseFramingSSE  ResponseFraming = "sse"
	ResponseFramingJSON ResponseFraming = "json"
)

// EffectiveResponseFraming validates framing and maps the omitted legacy value
// to SSE without changing existing expectation identities.
func EffectiveResponseFraming(framing ResponseFraming) (ResponseFraming, error) {
	return resolveResponseFraming(framing)
}

func resolveResponseFraming(framing ResponseFraming) (ResponseFraming, error) {
	switch framing {
	case "", ResponseFramingSSE:
		return ResponseFramingSSE, nil
	case ResponseFramingJSON:
		return ResponseFramingJSON, nil
	default:
		return "", errors.New("unsupported provider response framing")
	}
}

// RequiredCapabilities are checked against the selected model and adapter
// before a call intent is journaled or any provider request is sent.
type RequiredCapabilities struct {
	Tools            bool                 `json:"tools"`
	Reasoning        bool                 `json:"reasoning"`
	StructuredOutput StructuredOutputMode `json:"structured_output"`
	SchemaName       string               `json:"schema_name,omitempty"`
	Schema           json.RawMessage      `json:"schema,omitempty"`
}

// PricingPolicy declares conservative API price ceilings. MaxInput covers all
// input categories, including unknown or overlapping cache charges.
type PricingPolicy struct {
	Currency                    string `json:"currency"`
	Unit                        string `json:"unit"`
	MaxInputMicroUSDPerMillion  int64  `json:"max_input_micro_usd_per_million"`
	MaxOutputMicroUSDPerMillion int64  `json:"max_output_micro_usd_per_million"`
}

// AdapterRequestExpectation binds common bounds, tool declarations and exact
// adapter-specific controls for one outbound request.
type AdapterRequestExpectation struct {
	MaxBytes             int64                 `json:"max_bytes"`
	MaxOutputTokens      int64                 `json:"max_output_tokens"`
	Tools                []RequestTool         `json:"tools"`
	Controls             json.RawMessage       `json:"controls"`
	RequiredCapabilities *RequiredCapabilities `json:"required_capabilities,omitempty"`
	ResponseFraming      ResponseFraming       `json:"response_framing,omitempty"`
	// TerminalStructuredOutput binds the reserved native OpenCode result tool.
	// It is intentionally separate from Tools: the latter is the controller's
	// broker catalog, while this tool is added by the native Responses bridge.
	TerminalStructuredOutput *TerminalStructuredOutputExpectation `json:"terminal_structured_output,omitempty"`
}

// ProviderRequestMetadata records non-content facts from a validated request.
type ProviderRequestMetadata struct {
	SHA256          string `json:"sha256"`
	SizeBytes       int64  `json:"size_bytes"`
	Model           string `json:"model"`
	MaxOutputTokens int64  `json:"max_output_tokens"`
	InputItemCount  int    `json:"input_item_count"`
	ToolCount       int    `json:"tool_count"`
}

// AdapterResponseExpectation bounds one buffered provider response.
type AdapterResponseExpectation struct {
	MaxBytes                 int                                  `json:"max_bytes"`
	MaxTotalTokens           int64                                `json:"max_total_tokens"`
	RequiredCapabilities     *RequiredCapabilities                `json:"required_capabilities,omitempty"`
	ResponseFraming          ResponseFraming                      `json:"response_framing,omitempty"`
	TerminalStructuredOutput *TerminalStructuredOutputExpectation `json:"terminal_structured_output,omitempty"`
}

// StructuredOutputToolName is the stock OpenCode native result tool. It is a
// terminal result channel and is never part of the controller broker catalog.
const StructuredOutputToolName = "StructuredOutput"

const structuredOutputToolDescription = `Use this tool to return your final response in the requested structured format.

IMPORTANT:
- You MUST call this tool exactly once at the end of your response
- The input must be valid JSON matching the required schema
- Complete all necessary research and tool calls BEFORE calling this tool
- This tool provides your final answer - no further actions are taken after calling it`

// TerminalStructuredOutputExpectation binds the exact schema and reserved
// native tool identity used to close one Responses turn. SchemaSHA256 is the
// SHA-256 of the exact schema bytes; no cross-language normalization is used.
type TerminalStructuredOutputExpectation struct {
	Version      int             `json:"version"`
	Name         string          `json:"name"`
	Schema       json.RawMessage `json:"schema"`
	SchemaSHA256 string          `json:"schema_sha256"`
}

// Validate checks the bounded schema and exact reserved tool identity.
func (e TerminalStructuredOutputExpectation) Validate() error {
	if e.Version != 1 || e.Name != StructuredOutputToolName || len(e.Schema) < 2 || validateStructuredOutputSchema(e.Schema) != nil || safepath.RequireDigest(e.SchemaSHA256) != nil {
		return errors.New("invalid terminal structured output expectation")
	}
	document, err := decodeStructuredJSON(e.Schema)
	object, objectOK := document.(map[string]any)
	schemaType, typeOK := object["type"].(string)
	if err != nil || !objectOK || !typeOK || schemaType != "object" {
		return errors.New("terminal structured output object schema required")
	}
	digest := sha256.Sum256(e.Schema)
	if e.SchemaSHA256 != hex.EncodeToString(digest[:]) {
		return errors.New("terminal structured output schema identity mismatch")
	}
	return nil
}

// StructuredOutputTool returns the exact provider-facing native result tool
// declaration for an expectation. It is useful to callers that need to
// project the complete provider tool catalog before constructing a request.
func StructuredOutputTool(e TerminalStructuredOutputExpectation) (RequestTool, error) {
	if err := e.Validate(); err != nil {
		return RequestTool{}, err
	}
	parameters, err := structuredOutputToolParameters(e.Schema)
	if err != nil {
		return RequestTool{}, err
	}
	return RequestTool{Name: e.Name, Description: structuredOutputToolDescription, Parameters: parameters}, nil
}

func structuredOutputToolParameters(schema json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if !jsonObject(schema) || json.Unmarshal(schema, &object) != nil || object == nil {
		return nil, errors.New("terminal structured output object schema required")
	}
	// Stock OpenCode removes this annotation before constructing the native
	// tool input schema, while retaining it in the prompt format schema.
	delete(object, "$schema")
	parameters, err := canonical.Bytes(object)
	if err != nil || !jsonObject(parameters) {
		return nil, errors.New("terminal structured output tool schema unavailable")
	}
	return parameters, nil
}

// ProviderResponseMetadata is the common receipt projection. Native decoders
// retain protocol-specific text, tool and reasoning evidence separately.
type ProviderResponseMetadata struct {
	SHA256        string                      `json:"sha256"`
	SizeBytes     int64                       `json:"size_bytes"`
	ResponseID    string                      `json:"response_id"`
	ObservedModel string                      `json:"observed_model"`
	Status        string                      `json:"status"`
	Finish        string                      `json:"finish"`
	UsageComplete bool                        `json:"usage_complete"`
	Usage         Usage                       `json:"usage"`
	Semantic      *ResponseSemanticProjection `json:"semantic,omitempty"`
	Content       *ProviderResponseContent    `json:"content,omitempty"`
}

// ProviderToolCallContent is one validated tool call with broker-canonical
// arguments suitable for a controller-owned direct tool loop.
type ProviderToolCallContent struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ProviderReasoningContent preserves the finite protocol-specific replay
// evidence emitted by supported adapters.
type ProviderReasoningContent struct {
	AdapterID         string                      `json:"adapter_id"`
	Responses         []ResponsesReasoning        `json:"responses,omitempty"`
	AnthropicThinking []AnthropicThinking         `json:"anthropic_thinking,omitempty"`
	AnthropicRedacted []AnthropicRedactedThinking `json:"anthropic_redacted,omitempty"`
}

// ProviderResponseContent is validated bounded content returned to a direct
// runtime after durable completion. Gateway journals retain only its hashes.
type ProviderResponseContent struct {
	OutputText string                    `json:"output_text"`
	ToolCalls  []ProviderToolCallContent `json:"tool_calls"`
	Reasoning  *ProviderReasoningContent `json:"reasoning,omitempty"`
}

// ResponseToolIdentity binds one provider tool-call identity and exact raw
// argument object without retaining the argument content in the journal.
type ResponseToolIdentity struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ArgumentsSHA256 string `json:"arguments_sha256"`
}

// ResponseSemanticProjection binds the ordered tool calls and exact visible
// assistant text derived by a native response decoder.
type ResponseSemanticProjection struct {
	Version          int                    `json:"version"`
	Kind             string                 `json:"kind"`
	ToolCalls        []ResponseToolIdentity `json:"tool_calls"`
	OutputTextSHA256 string                 `json:"output_text_sha256"`
	// TerminalTool is present only for the sole reserved StructuredOutput
	// capture that closes a native structured turn.
	TerminalTool *ResponseToolIdentity `json:"terminal_tool,omitempty"`
	// TerminalSchemaSHA256 binds that capture to the admitted exact schema.
	// It is omitted for ordinary tool responses and legacy projections.
	TerminalSchemaSHA256 string `json:"terminal_schema_sha256,omitempty"`
}

type adapterRequestValidator func([]byte, Binding, AdapterRequestExpectation) (ProviderRequestMetadata, error)
type adapterResponseDecoder func([]byte, Binding, AdapterResponseExpectation) (ProviderResponseMetadata, error)

type protocolAdapter struct {
	id                    string
	requestPath           string
	authSchemes           []string
	capabilitiesValidator func(json.RawMessage) error
	requestValidator      adapterRequestValidator
	responseDecoder       adapterResponseDecoder
	jsonResponseDecoder   adapterResponseDecoder
}

// ValidateRequiredCapabilities rejects a route/request requirement that the
// exact selected model and finite adapter cannot satisfy. It never selects a
// substitute model, adapter, or weaker structured-output mode.
func ValidateRequiredCapabilities(binding Binding, required *RequiredCapabilities) error {
	if required == nil {
		return nil
	}
	if required.StructuredOutput != StructuredOutputUnsupported &&
		required.StructuredOutput != StructuredOutputTextParseRequired &&
		required.StructuredOutput != StructuredOutputJSONOnly &&
		required.StructuredOutput != StructuredOutputStrictSchema {
		return errors.New("invalid required provider capabilities")
	}
	if _, err := binding.ID(); err != nil || binding.Model.Capabilities == nil {
		return errors.New("invalid required provider capability binding")
	}
	if required.StructuredOutput == StructuredOutputStrictSchema {
		if !identifier(required.SchemaName) || len(required.SchemaName) > 64 || len(required.Schema) < 2 || len(required.Schema) > 64<<10 || !jsonObject(required.Schema) {
			return errors.New("invalid required structured output schema")
		}
	} else if required.SchemaName != "" || len(required.Schema) != 0 {
		return errors.New("structured output schema requires STRICT_SCHEMA")
	}
	if required.Tools && !binding.Model.Capabilities.Tools || required.Reasoning && !binding.Model.Capabilities.Reasoning {
		return ErrCapabilityUnavailable
	}
	if required.StructuredOutput == StructuredOutputJSONOnly || required.StructuredOutput == StructuredOutputStrictSchema {
		if !slices.Contains(binding.Model.Capabilities.StructuredOutputModes, required.StructuredOutput) || binding.Model.AdapterID != OpenAIResponsesAdapter && binding.Model.AdapterID != OpenAIChatCompletionsAdapter {
			return ErrCapabilityUnavailable
		}
	}
	if required.StructuredOutput == StructuredOutputStrictSchema {
		// Compilation and instance validation are performed by the bounded schema
		// validator in structured_output.go.
		if err := validateStructuredOutputSchema(required.Schema); err != nil {
			return err
		}
	}
	return nil
}

func (a protocolAdapter) implemented() bool {
	return a.requestValidator != nil && a.responseDecoder != nil && a.jsonResponseDecoder != nil
}

func protocolAdapterFor(id string) (protocolAdapter, bool) {
	adapters := map[string]protocolAdapter{
		OpenAIChatCompletionsAdapter: {
			id: OpenAIChatCompletionsAdapter, requestPath: "/v1/chat/completions", authSchemes: []string{"api-key-header", "bearer"},
			capabilitiesValidator: validateEmptyAdapterCapabilities,
			requestValidator:      validateChatAdapterRequest,
			responseDecoder:       decodeChatAdapterResponse,
			jsonResponseDecoder:   decodeChatAdapterJSONResponse,
		},
		OpenAIResponsesAdapter: {
			id: OpenAIResponsesAdapter, requestPath: "/v1/responses", authSchemes: []string{"api-key-header", "bearer"},
			capabilitiesValidator: validateResponsesModelCapabilities,
			requestValidator:      validateResponsesAdapterRequest,
			responseDecoder:       decodeResponsesAdapterResponse,
			jsonResponseDecoder:   decodeResponsesAdapterJSONResponse,
		},
		AnthropicMessagesAdapter: {
			id: AnthropicMessagesAdapter, requestPath: "/v1/messages", authSchemes: []string{"api-key-header", "bearer"},
			capabilitiesValidator: validateAnthropicMessagesModelCapabilities,
			requestValidator:      validateAnthropicAdapterRequest,
			responseDecoder:       decodeAnthropicAdapterResponse,
			jsonResponseDecoder:   decodeAnthropicAdapterJSONResponse,
		},
	}
	adapter, ok := adapters[id]
	return adapter, ok
}

// AdapterRequestPath returns the controller-local route for an executable
// adapter. It is independent of the arbitrary exact upstream endpoint URL.
func AdapterRequestPath(binding Binding) (string, error) {
	adapter, ok := protocolAdapterFor(binding.Model.AdapterID)
	if _, err := binding.ID(); err != nil || !ok || !adapter.implemented() || adapter.requestPath == "" {
		return "", errors.New("provider request adapter path is unavailable")
	}
	return adapter.requestPath, nil
}

// AdapterRequestExpectationID validates adapter-specific controls and hashes
// exact raw controls and tool schemas by digest. This preserves fractional JSON
// lexemes without asking the integer-only canonical encoder to reinterpret them.
func AdapterRequestExpectationID(binding Binding, expected AdapterRequestExpectation) (string, error) {
	adapter, ok := protocolAdapterFor(binding.Model.AdapterID)
	bindingID, bindingErr := binding.ID()
	tokenLimit := providerTokenLimit(binding)
	if bindingErr != nil || !ok || !adapter.implemented() || expected.MaxBytes < 2 || expected.MaxBytes > binding.Model.MaxRequestBytes || expected.MaxOutputTokens < 1 || expected.MaxOutputTokens > binding.Model.MaxOutputTokens || expected.MaxOutputTokens > tokenLimit {
		return "", errors.New("invalid provider request expectation")
	}
	if err := ValidateRequiredCapabilities(binding, expected.RequiredCapabilities); err != nil {
		return "", err
	}
	if framing, err := resolveResponseFraming(expected.ResponseFraming); err != nil {
		return "", err
	} else if !modelSupportsResponseFraming(binding.Model, framing) {
		return "", ErrCapabilityUnavailable
	}
	if err := validateTerminalStructuredOutputForAdapter(binding.Model.AdapterID, expected.TerminalStructuredOutput); err != nil {
		return "", err
	}
	tools, err := expectationToolIdentities(expected.Tools)
	if err != nil {
		return "", err
	}
	switch binding.Model.AdapterID {
	case OpenAIChatCompletionsAdapter:
		controls, err := decodeChatControls(expected.Controls)
		if err != nil || validateChatStructuredExpectation(binding, controls) != nil || validateRequiredChatControls(expected.RequiredCapabilities, controls) != nil {
			return "", errors.New("invalid chat adapter controls")
		}
	case OpenAIResponsesAdapter:
		controls, err := decodeResponsesControls(expected.Controls)
		allowedTools, toolsErr := providerRequestTools(expected.Tools, expected.TerminalStructuredOutput)
		var capabilities ResponsesModelCapabilities
		if err != nil || toolsErr != nil || validateTerminalStructuredOutputControls(controls, expected.TerminalStructuredOutput) != nil || canonical.Decode(binding.Model.AdapterCapabilities, &capabilities) != nil || validateResponsesExpectation(binding, capabilities, controls, allowedTools) != nil || validateRequiredResponsesControls(expected.RequiredCapabilities, controls) != nil {
			return "", errors.New("invalid Responses request expectation")
		}
	case AnthropicMessagesAdapter:
		controls, err := decodeAnthropicControls(expected.Controls)
		var capabilities AnthropicMessagesModelCapabilities
		if err != nil || canonical.Decode(binding.Model.AdapterCapabilities, &capabilities) != nil || validateAnthropicExpectation(binding, capabilities, controls, expected.Tools) != nil {
			return "", errors.New("invalid Anthropic request expectation")
		}
	default:
		return "", errors.New("provider request expectation adapter unavailable")
	}
	controlsHash := sha256.Sum256(expected.Controls)
	required, err := requiredCapabilitiesIdentityFor(expected.RequiredCapabilities)
	if err != nil {
		return "", err
	}
	projection := struct {
		BindingID                string                               `json:"binding_id"`
		MaxBytes                 int64                                `json:"max_bytes"`
		MaxOutputTokens          int64                                `json:"max_output_tokens"`
		ControlsSHA256           string                               `json:"controls_sha256"`
		Tools                    []expectationToolIdentity            `json:"tools"`
		RequiredCapabilities     *requiredCapabilitiesIdentity        `json:"required_capabilities,omitempty"`
		ResponseFraming          ResponseFraming                      `json:"response_framing,omitempty"`
		TerminalStructuredOutput *TerminalStructuredOutputExpectation `json:"terminal_structured_output,omitempty"`
	}{bindingID, expected.MaxBytes, expected.MaxOutputTokens, hex.EncodeToString(controlsHash[:]), tools, required, expected.ResponseFraming, expected.TerminalStructuredOutput}
	return canonical.Hash("harness.provider-request-expectation.v1", projection)
}

type expectationToolIdentity struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	ParametersSHA256 string `json:"parameters_sha256"`
}

func expectationToolIdentities(tools []RequestTool) ([]expectationToolIdentity, error) {
	if len(tools) > 64 {
		return nil, errors.New("invalid expected provider tool catalog")
	}
	result := make([]expectationToolIdentity, len(tools))
	seen := map[string]bool{}
	for index, tool := range tools {
		if !requestToolIdentifier(tool.Name) || tool.Name == StructuredOutputToolName || tool.Description == "" || len(tool.Description) > 8192 || !utf8.ValidString(tool.Description) || seen[tool.Name] || !jsonObject(tool.Parameters) {
			return nil, errors.New("invalid expected provider tool catalog")
		}
		seen[tool.Name] = true
		digest := sha256.Sum256(tool.Parameters)
		result[index] = expectationToolIdentity{tool.Name, tool.Description, hex.EncodeToString(digest[:])}
	}
	return result, nil
}

func validateTerminalStructuredOutputForAdapter(adapterID string, expectation *TerminalStructuredOutputExpectation) error {
	if expectation == nil {
		return nil
	}
	if adapterID != OpenAIResponsesAdapter {
		return errors.New("terminal structured output requires the Responses adapter")
	}
	return expectation.Validate()
}

func validateTerminalStructuredOutputControls(controls ResponsesRequestExpectation, expectation *TerminalStructuredOutputExpectation) error {
	if expectation != nil && controls.ToolChoice != "auto" && controls.ToolChoice != "required" {
		return errors.New("terminal structured output requires Responses tool_choice auto or required")
	}
	return nil
}

func providerRequestTools(allowed []RequestTool, expectation *TerminalStructuredOutputExpectation) ([]RequestTool, error) {
	if expectation == nil {
		return allowed, nil
	}
	tool, err := StructuredOutputTool(*expectation)
	if err != nil {
		return nil, err
	}
	for _, existing := range allowed {
		if existing.Name == tool.Name {
			return nil, errors.New("terminal structured output tool collides with broker catalog")
		}
	}
	result := make([]RequestTool, 0, len(allowed)+1)
	result = append(result, allowed...)
	result = append(result, tool)
	return result, nil
}

func validateEndpointContractV2(endpoint EndpointContract) error {
	adapter, ok := protocolAdapterFor(endpoint.AdapterID)
	parsed, err := url.Parse(endpoint.URL)
	if err != nil || endpoint.Protocol != "" || !identifier(endpoint.Provider) || !ok || endpoint.Auth == nil || !printable(endpoint.URL, 4096) || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Path == "" || path.Clean(parsed.Path) != parsed.Path || parsed.String() != endpoint.URL {
		return errors.New("invalid provider endpoint v2 contract")
	}
	if parsed.ForceQuery || !canonicalPublicQuery(parsed.RawQuery) || validateAuthContract(*endpoint.Auth, adapter) != nil || validateEndpointHeaders(endpoint) != nil {
		return errors.New("invalid provider endpoint v2 contract")
	}
	return nil
}

func validateEndpointHeaders(endpoint EndpointContract) error {
	if len(endpoint.PublicHeaders) > 16 || endpoint.SessionHeader != "" && !safePublicHeaderName(endpoint.SessionHeader) {
		return errors.New("invalid provider public headers")
	}
	if endpoint.SessionHeader != "" && (endpoint.SessionHeader == endpoint.Auth.HeaderName || endpoint.SessionHeader == "authorization") {
		return errors.New("provider session header conflicts with authentication")
	}
	for name, value := range endpoint.PublicHeaders {
		if !safePublicHeaderName(name) || name == endpoint.SessionHeader || name == endpoint.Auth.HeaderName || name == "authorization" || len(value) == 0 || len(value) > 1024 || strings.TrimSpace(value) != value {
			return errors.New("invalid provider public header")
		}
		for _, character := range value {
			if character < 0x20 || character > 0x7e {
				return errors.New("invalid provider public header value")
			}
		}
	}
	return nil
}

// Public header values are operator-declared metadata. Structural validation
// blocks header injection and authority headers; it cannot detect secrets in
// otherwise valid values, so controller configuration must keep them public.
func safePublicHeaderName(name string) bool {
	if !safeHeaderToken(name) {
		return false
	}
	blocked := map[string]bool{"accept": true, "authorization": true, "connection": true, "content-length": true, "content-type": true, "cookie": true, "host": true, "proxy-authorization": true, "te": true, "trailer": true, "transfer-encoding": true, "upgrade": true, "api-key": true, "x-api-key": true}
	return !blocked[name] && !strings.HasPrefix(name, "proxy-") && !strings.HasPrefix(name, "sec-") && !strings.HasPrefix(name, "forwarded") && !strings.HasPrefix(name, "x-forwarded-")
}

func safeHeaderToken(name string) bool {
	if name == "" || name != strings.ToLower(name) || len(name) > 128 {
		return false
	}
	for _, character := range name {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", character)) {
			return false
		}
	}
	return true
}

// Query values are operator-declared public metadata. Known credential-shaped
// names are rejected, but validation cannot determine whether arbitrary values
// contain secrets; controller configuration must keep them public.
func canonicalPublicQuery(raw string) bool {
	if raw == "" {
		return true
	}
	values, err := url.ParseQuery(raw)
	if err != nil || values.Encode() != raw || len(values) > 16 {
		return false
	}
	for name, entries := range values {
		if len(entries) != 1 || len(name) == 0 || len(name) > 128 || len(entries[0]) == 0 || len(entries[0]) > 1024 || knownCredentialQueryName(name) {
			return false
		}
	}
	return true
}

func knownCredentialQueryName(name string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(name, "-", "_"))
	switch normalized {
	case "key", "api_key", "apikey", "token", "access_token", "secret", "signature", "sig", "auth", "authorization", "credential", "credentials":
		return true
	default:
		return false
	}
}

func validateAuthContract(auth AuthContract, adapter protocolAdapter) error {
	if !identifier(auth.CredentialRef) || !slices.Contains(adapter.authSchemes, auth.Scheme) {
		return errors.New("invalid provider authentication contract")
	}
	switch auth.Scheme {
	case "bearer":
		if auth.HeaderName != "" {
			return errors.New("bearer authentication owns its header")
		}
	case "api-key-header":
		if !safeAPIKeyHeader(auth.HeaderName) {
			return errors.New("unsafe provider API key header")
		}
	default:
		return errors.New("unsupported provider authentication scheme")
	}
	return nil
}

func safeAPIKeyHeader(name string) bool {
	if !safeHeaderToken(name) {
		return false
	}
	blocked := map[string]bool{"authorization": true, "connection": true, "content-length": true, "content-type": true, "cookie": true, "host": true, "proxy-authorization": true, "te": true, "trailer": true, "transfer-encoding": true, "upgrade": true, "anthropic-version": true}
	return !blocked[name] && !strings.HasPrefix(name, "proxy-") && !strings.HasPrefix(name, "sec-") && !strings.HasPrefix(name, "forwarded") && !strings.HasPrefix(name, "x-forwarded-")
}

func validateModelContractV2(model ModelContract) error {
	adapter, ok := protocolAdapterFor(model.AdapterID)
	if model.Protocol != "" || !identifier(model.Provider) || !printable(model.Model, 256) || !ok || model.Capabilities == nil || model.ContextWindowTokens < 1 || model.ContextWindowTokens > maxExact || model.MaxCalls < 1 || model.MaxCalls > 64 || model.MaxRequestBytes < 2 || model.MaxRequestBytes > 1<<20 || model.MaxResponseBytes < 1 || model.MaxResponseBytes > 8<<20 || model.MaxOutputTokens < 1 || model.MaxOutputTokens > model.ContextWindowTokens || !model.Capabilities.OutputCap || !model.Capabilities.CompleteUsage {
		return errors.New("invalid provider model v2 contract")
	}
	normal, err := canonicalAdapterCapabilities(model.AdapterCapabilities)
	if err != nil || !bytes.Equal(normal, model.AdapterCapabilities) || adapter.capabilitiesValidator == nil || adapter.capabilitiesValidator(model.AdapterCapabilities) != nil {
		return errors.New("invalid provider adapter capabilities")
	}
	if model.Pricing != nil {
		if err := model.Pricing.validate(); err != nil {
			return err
		}
	}
	if !slices.IsSorted(model.Capabilities.StructuredOutputModes) {
		return errors.New("invalid structured output modes")
	}
	for index, mode := range model.Capabilities.StructuredOutputModes {
		if mode != StructuredOutputJSONOnly && mode != StructuredOutputStrictSchema || index > 0 && model.Capabilities.StructuredOutputModes[index-1] == mode {
			return errors.New("invalid structured output modes")
		}
	}
	if len(model.Capabilities.SupportedResponseFramings) > 2 || !slices.IsSorted(model.Capabilities.SupportedResponseFramings) {
		return errors.New("invalid supported response framings")
	}
	for index, framing := range model.Capabilities.SupportedResponseFramings {
		if framing != ResponseFramingSSE && framing != ResponseFramingJSON || index > 0 && model.Capabilities.SupportedResponseFramings[index-1] == framing {
			return errors.New("invalid supported response framings")
		}
	}
	if len(model.ObservedModelAliases) > 16 || !slices.IsSorted(model.ObservedModelAliases) {
		return errors.New("invalid observed model aliases")
	}
	for index, alias := range model.ObservedModelAliases {
		if !printable(alias, 256) || alias == model.Model || index > 0 && model.ObservedModelAliases[index-1] == alias {
			return errors.New("invalid observed model aliases")
		}
	}
	return nil
}

func modelSupportsResponseFraming(model ModelContract, framing ResponseFraming) bool {
	if model.Capabilities == nil {
		return false
	}
	if len(model.Capabilities.SupportedResponseFramings) == 0 {
		return framing == ResponseFramingSSE
	}
	if !slices.Contains(model.Capabilities.SupportedResponseFramings, framing) {
		return false
	}
	if framing == ResponseFramingJSON && model.AdapterID == OpenAIResponsesAdapter {
		var capabilities ResponsesModelCapabilities
		if canonical.Decode(model.AdapterCapabilities, &capabilities) != nil || capabilities.TrailingCostPingV1 || capabilities.ContentPartCompletesText || capabilities.FunctionCallDoneNameV1 {
			return false
		}
	}
	return true
}

// AcceptsObservedModel checks the exact provider-reported model identity
// against the requested model and its explicitly configured v2 aliases.
// Aliases never alter the model placed on a request.
func AcceptsObservedModel(model ModelContract, observed string) bool {
	if _, err := model.ID(); err != nil || !printable(observed, 256) {
		return false
	}
	return observed == model.Model || model.Version == 2 && slices.Contains(model.ObservedModelAliases, observed)
}

func canonicalAdapterCapabilities(raw json.RawMessage) ([]byte, error) {
	if len(raw) < 2 || len(raw) > 32<<10 || raw[0] != '{' {
		return nil, errors.New("adapter capabilities must be a bounded object")
	}
	return canonical.Normalize(raw)
}

func validateEmptyAdapterCapabilities(raw json.RawMessage) error {
	if !bytes.Equal(raw, []byte("{}")) {
		return errors.New("adapter capabilities must be empty")
	}
	return nil
}

func (pricing PricingPolicy) validate() error {
	if pricing.Currency != "USD" || pricing.Unit != "micro_usd_per_million_tokens" || pricing.MaxInputMicroUSDPerMillion < 0 || pricing.MaxInputMicroUSDPerMillion > maxExact || pricing.MaxOutputMicroUSDPerMillion < 0 || pricing.MaxOutputMicroUSDPerMillion > maxExact {
		return errors.New("invalid provider pricing policy")
	}
	return nil
}

// ConservativeReservation returns the declared maximum tokens and, when exact
// pricing ceilings exist, micro-USD for all allowed calls. No observed usage is
// refunded and provider compliance remains independently qualified.
func (model ModelContract) ConservativeReservation() (int64, *int64, error) {
	if model.Version != 2 || validateModelContractV2(model) != nil {
		return 0, nil, errors.New("valid provider model v2 contract required")
	}
	tokens := new(big.Int).Mul(big.NewInt(int64(model.MaxCalls)), new(big.Int).Add(big.NewInt(model.ContextWindowTokens), big.NewInt(model.MaxOutputTokens)))
	if !tokens.IsInt64() || tokens.Sign() <= 0 || tokens.Int64() > maxExact {
		return 0, nil, errors.New("provider token reservation overflow")
	}
	if model.Pricing == nil {
		return tokens.Int64(), nil, nil
	}
	input := new(big.Int).Mul(big.NewInt(model.ContextWindowTokens), big.NewInt(model.Pricing.MaxInputMicroUSDPerMillion))
	output := new(big.Int).Mul(big.NewInt(model.MaxOutputTokens), big.NewInt(model.Pricing.MaxOutputMicroUSDPerMillion))
	perCall := new(big.Int).Add(input, output)
	perCall.Add(perCall, big.NewInt(999_999)).Quo(perCall, big.NewInt(1_000_000))
	cost := new(big.Int).Mul(perCall, big.NewInt(int64(model.MaxCalls)))
	if !cost.IsInt64() || cost.Sign() < 0 || cost.Int64() > maxExact {
		return 0, nil, errors.New("provider cost reservation overflow")
	}
	value := cost.Int64()
	return tokens.Int64(), &value, nil
}

func matchingContractProtocol(endpoint EndpointContract, model ModelContract) bool {
	if endpoint.Version != model.Version {
		return false
	}
	if endpoint.Version == 1 {
		return endpoint.Protocol == model.Protocol
	}
	return endpoint.Version == 2 && endpoint.AdapterID == model.AdapterID
}

func profileForGatewayRoute(policy access.Policy, accessID string) (access.Profile, error) {
	for _, profile := range policy.Profiles {
		id, err := profile.ID()
		if err != nil {
			return access.Profile{}, err
		}
		if id == accessID {
			return profile, nil
		}
	}
	return access.Profile{}, errors.New("provider access profile unavailable")
}

// ValidateAdapterRequest dispatches through the adapter fixed by the binding.
// It returns non-content metadata and never forwards or retains the body.
func ValidateAdapterRequest(raw []byte, binding Binding, expected AdapterRequestExpectation) (ProviderRequestMetadata, error) {
	adapter, ok := protocolAdapterFor(binding.Model.AdapterID)
	framing, framingErr := resolveResponseFraming(expected.ResponseFraming)
	if framingErr != nil {
		return ProviderRequestMetadata{}, framingErr
	}
	tokenLimit, tokenLimitErr := ProviderTokenLimit(binding)
	if tokenLimitErr != nil || !ok || !adapter.implemented() || expected.MaxBytes < 2 || expected.MaxBytes > binding.Model.MaxRequestBytes || expected.MaxOutputTokens < 1 || expected.MaxOutputTokens > binding.Model.MaxOutputTokens || expected.MaxOutputTokens > tokenLimit {
		return ProviderRequestMetadata{}, errors.New("provider request adapter is unavailable")
	}
	if err := ValidateRequiredCapabilities(binding, expected.RequiredCapabilities); err != nil {
		return ProviderRequestMetadata{}, err
	}
	if !modelSupportsResponseFraming(binding.Model, framing) {
		return ProviderRequestMetadata{}, ErrCapabilityUnavailable
	}
	if err := validateTerminalStructuredOutputForAdapter(binding.Model.AdapterID, expected.TerminalStructuredOutput); err != nil {
		return ProviderRequestMetadata{}, err
	}
	return adapter.requestValidator(raw, binding, expected)
}

// DecodeAdapterResponse dispatches bounded bytes through the same adapter and
// projects only common receipt metadata.
func DecodeAdapterResponse(raw []byte, binding Binding, expected AdapterResponseExpectation) (ProviderResponseMetadata, error) {
	adapter, ok := protocolAdapterFor(binding.Model.AdapterID)
	framing, framingErr := resolveResponseFraming(expected.ResponseFraming)
	tokenLimit, tokenLimitErr := ProviderTokenLimit(binding)
	if tokenLimitErr != nil || framingErr != nil || !ok || !adapter.implemented() || expected.MaxBytes < 1 || int64(expected.MaxBytes) > binding.Model.MaxResponseBytes || expected.MaxTotalTokens < 1 || expected.MaxTotalTokens > tokenLimit {
		return ProviderResponseMetadata{}, errors.New("provider response adapter is unavailable")
	}
	if !modelSupportsResponseFraming(binding.Model, framing) {
		return ProviderResponseMetadata{}, ErrCapabilityUnavailable
	}
	if err := validateTerminalStructuredOutputForAdapter(binding.Model.AdapterID, expected.TerminalStructuredOutput); err != nil {
		return ProviderResponseMetadata{}, err
	}
	decoder := adapter.responseDecoder
	if framing == ResponseFramingJSON {
		decoder = adapter.jsonResponseDecoder
	}
	metadata, err := decoder(raw, binding, expected)
	if err != nil {
		return metadata, err
	}
	if !metadata.UsageComplete || !AcceptsObservedModel(binding.Model, metadata.ObservedModel) || validateReceiptSemantic(binding.Model.Version, metadata.Finish, metadata.Semantic) != nil {
		return metadata, errors.New("provider response differs from model accounting contract")
	}
	if err := validateProviderResponseContent(metadata.Content, metadata.Semantic, binding.Model.AdapterID, expected); err != nil {
		return metadata, err
	}
	return metadata, nil
}

func validateProviderResponseContent(content *ProviderResponseContent, semantic *ResponseSemanticProjection, adapterID string, expected AdapterResponseExpectation) error {
	maxBytes := expected.MaxBytes
	if content == nil || !utf8.ValidString(content.OutputText) || len(content.OutputText) > maxBytes || content.ToolCalls == nil {
		return errors.New("invalid provider response content")
	}
	used := len(content.OutputText)
	tools := make([]ResponseToolIdentity, len(content.ToolCalls))
	seen := map[string]bool{}
	for index, call := range content.ToolCalls {
		if !printable(call.ID, 256) || !requestToolIdentifier(call.Name) || seen[call.ID] || len(call.Arguments) < 2 || len(call.Arguments) > maxBytes-used {
			return errors.New("invalid provider response tool content")
		}
		seen[call.ID] = true
		normal, err := canonical.Normalize(call.Arguments)
		if err != nil || !bytes.Equal(normal, call.Arguments) {
			return errors.New("noncanonical provider response tool arguments")
		}
		used += len(call.Arguments)
		tools[index], err = responseToolIdentity(call.ID, call.Name, call.Arguments)
		if err != nil {
			return err
		}
	}
	terminal, err := terminalStructuredOutputIdentity(content, expected.TerminalStructuredOutput)
	if err != nil {
		return err
	}
	terminalSchemaSHA256 := terminalStructuredOutputSchemaSHA256(expected.TerminalStructuredOutput, terminal)
	if !reflect.DeepEqual(responseSemanticProjectionWithTerminal(content.OutputText, tools, terminal, terminalSchemaSHA256), semantic) {
		return errors.New("provider response content differs from semantic receipt")
	}
	if content.Reasoning == nil {
		return validateStructuredOutput(content.OutputText, expected.RequiredCapabilities)
	}
	if content.Reasoning.AdapterID != adapterID {
		return errors.New("provider reasoning adapter identity mismatch")
	}
	switch adapterID {
	case OpenAIResponsesAdapter:
		if len(content.Reasoning.AnthropicThinking) != 0 || len(content.Reasoning.AnthropicRedacted) != 0 || len(content.Reasoning.Responses) == 0 {
			return errors.New("invalid Responses reasoning replay")
		}
	case AnthropicMessagesAdapter:
		if len(content.Reasoning.Responses) != 0 || len(content.Reasoning.AnthropicThinking) == 0 && len(content.Reasoning.AnthropicRedacted) == 0 {
			return errors.New("invalid Anthropic reasoning replay")
		}
	default:
		return errors.New("reasoning replay unavailable for adapter")
	}
	raw, err := canonical.Bytes(content.Reasoning)
	if err != nil || len(raw) > maxBytes {
		return errors.New("provider reasoning replay exceeds response bound")
	}
	return validateStructuredOutput(content.OutputText, expected.RequiredCapabilities)
}

type requiredCapabilitiesIdentity struct {
	Tools        bool                 `json:"tools"`
	Reasoning    bool                 `json:"reasoning"`
	Mode         StructuredOutputMode `json:"structured_output"`
	SchemaName   string               `json:"schema_name,omitempty"`
	SchemaSHA256 string               `json:"schema_sha256,omitempty"`
}

func requiredCapabilitiesIdentityFor(source *RequiredCapabilities) (*requiredCapabilitiesIdentity, error) {
	if source == nil {
		return nil, nil
	}
	result := &requiredCapabilitiesIdentity{Tools: source.Tools, Reasoning: source.Reasoning, Mode: source.StructuredOutput, SchemaName: source.SchemaName}
	if len(source.Schema) != 0 {
		digest := sha256.Sum256(source.Schema)
		result.SchemaSHA256 = hex.EncodeToString(digest[:])
	}
	return result, nil
}

func validateRequiredResponsesControls(required *RequiredCapabilities, controls ResponsesRequestExpectation) error {
	mode := StructuredOutputUnsupported
	if required != nil {
		mode = required.StructuredOutput
	}
	switch mode {
	case StructuredOutputUnsupported, StructuredOutputTextParseRequired:
		if controls.TextFormat != "" && controls.TextFormat != "plain" {
			return errors.New("Responses structured controls exceed requirement")
		}
	case StructuredOutputJSONOnly:
		if controls.TextFormat != "json_object" {
			return errors.New("Responses JSON-only control missing")
		}
	case StructuredOutputStrictSchema:
		if controls.TextFormat != "json_schema" || controls.TextSchemaName != required.SchemaName || !bytes.Equal(controls.TextSchema, required.Schema) {
			return errors.New("Responses strict schema control differs from requirement")
		}
	default:
		return errors.New("invalid Responses structured output requirement")
	}
	return nil
}

func cloneRequiredCapabilities(source *RequiredCapabilities) *RequiredCapabilities {
	if source == nil {
		return nil
	}
	result := *source
	result.Schema = append([]byte(nil), source.Schema...)
	return &result
}

func validateChatAdapterRequest(raw []byte, binding Binding, expected AdapterRequestExpectation) (ProviderRequestMetadata, error) {
	controls, err := decodeChatControls(expected.Controls)
	if err != nil || validateChatStructuredExpectation(binding, controls) != nil || validateRequiredChatControls(expected.RequiredCapabilities, controls) != nil {
		return ProviderRequestMetadata{}, errors.New("invalid chat adapter controls")
	}
	framing, framingErr := resolveResponseFraming(expected.ResponseFraming)
	if framingErr != nil {
		return ProviderRequestMetadata{}, framingErr
	}
	o, err := validateChatCompletionRequestForFraming(raw, expected.MaxBytes, binding, expected.MaxOutputTokens, expected.Tools, controls, framing)
	return ProviderRequestMetadata{SHA256: o.SHA256, SizeBytes: o.SizeBytes, Model: o.Model, MaxOutputTokens: o.MaxOutputTokens, InputItemCount: o.MessageCount, ToolCount: o.ToolCount}, err
}

func decodeChatControls(raw json.RawMessage) (ChatRequestExpectation, error) {
	var controls ChatRequestExpectation
	if len(raw) == 0 || !uniqueProviderJSON(raw) || json.Unmarshal(raw, &controls) != nil {
		return controls, errors.New("invalid Chat adapter controls")
	}
	normal, err := json.Marshal(controls)
	if err != nil || !bytes.Equal(normal, raw) {
		return controls, errors.New("noncanonical Chat adapter controls")
	}
	return controls, nil
}

func validateChatStructuredExpectation(binding Binding, controls ChatRequestExpectation) error {
	switch controls.ResponseFormat {
	case "":
		if controls.SchemaName != "" || len(controls.Schema) != 0 {
			return errors.New("Chat schema without response format")
		}
	case "json_object":
		if !slices.Contains(binding.Model.Capabilities.StructuredOutputModes, StructuredOutputJSONOnly) || controls.SchemaName != "" || len(controls.Schema) != 0 {
			return ErrCapabilityUnavailable
		}
	case "json_schema":
		if !slices.Contains(binding.Model.Capabilities.StructuredOutputModes, StructuredOutputStrictSchema) || !identifier(controls.SchemaName) || len(controls.SchemaName) > 64 || validateStructuredOutputSchema(controls.Schema) != nil {
			return ErrCapabilityUnavailable
		}
	default:
		return errors.New("unsupported Chat structured output mode")
	}
	return nil
}

func validateRequiredChatControls(required *RequiredCapabilities, controls ChatRequestExpectation) error {
	mode := StructuredOutputUnsupported
	if required != nil {
		mode = required.StructuredOutput
	}
	switch mode {
	case StructuredOutputUnsupported, StructuredOutputTextParseRequired:
		if controls.ResponseFormat != "" {
			return errors.New("Chat structured controls exceed requirement")
		}
	case StructuredOutputJSONOnly:
		if controls.ResponseFormat != "json_object" {
			return errors.New("Chat JSON-only control missing")
		}
	case StructuredOutputStrictSchema:
		if controls.ResponseFormat != "json_schema" || controls.SchemaName != required.SchemaName || !bytes.Equal(controls.Schema, required.Schema) {
			return errors.New("Chat strict schema control differs from requirement")
		}
	default:
		return errors.New("invalid Chat structured output requirement")
	}
	return nil
}

func decodeChatAdapterResponse(raw []byte, _ Binding, expected AdapterResponseExpectation) (ProviderResponseMetadata, error) {
	o, err := ParseChatCompletionSSE(raw, expected.MaxBytes, expected.MaxTotalTokens)
	if err != nil {
		return ProviderResponseMetadata{SHA256: o.SHA256, SizeBytes: int64(o.SizeBytes)}, err
	}
	tools, err := chatSemanticTools(o.ToolCalls)
	content, contentErr := chatResponseContent(o)
	if err == nil {
		err = contentErr
	}
	return ProviderResponseMetadata{SHA256: o.SHA256, SizeBytes: int64(o.SizeBytes), ResponseID: o.ResponseID, ObservedModel: o.Model, Status: "completed", Finish: o.FinishReason, UsageComplete: true, Usage: o.Usage, Semantic: responseSemanticProjection(o.OutputText, tools), Content: content}, err
}

func decodeChatAdapterJSONResponse(raw []byte, _ Binding, expected AdapterResponseExpectation) (ProviderResponseMetadata, error) {
	o, err := ParseChatCompletionJSON(raw, expected.MaxBytes, expected.MaxTotalTokens)
	if err != nil {
		return ProviderResponseMetadata{SHA256: o.SHA256, SizeBytes: int64(o.SizeBytes)}, err
	}
	tools, err := chatSemanticTools(o.ToolCalls)
	content, contentErr := chatResponseContent(o)
	if err == nil {
		err = contentErr
	}
	return ProviderResponseMetadata{SHA256: o.SHA256, SizeBytes: int64(o.SizeBytes), ResponseID: o.ResponseID, ObservedModel: o.Model, Status: "completed", Finish: o.FinishReason, UsageComplete: true, Usage: o.Usage, Semantic: responseSemanticProjection(o.OutputText, tools), Content: content}, err
}

func validateResponsesAdapterRequest(raw []byte, binding Binding, expected AdapterRequestExpectation) (ProviderRequestMetadata, error) {
	var capabilities ResponsesModelCapabilities
	controls, controlsErr := decodeResponsesControls(expected.Controls)
	if validateResponsesModelCapabilities(binding.Model.AdapterCapabilities) != nil || canonical.Decode(binding.Model.AdapterCapabilities, &capabilities) != nil || controlsErr != nil {
		return ProviderRequestMetadata{}, errors.New("invalid Responses adapter controls")
	}
	allowedTools, toolsErr := providerRequestTools(expected.Tools, expected.TerminalStructuredOutput)
	if toolsErr != nil || validateTerminalStructuredOutputControls(controls, expected.TerminalStructuredOutput) != nil {
		return ProviderRequestMetadata{}, errors.New("invalid Responses terminal structured output controls")
	}
	if err := validateRequiredResponsesControls(expected.RequiredCapabilities, controls); err != nil {
		return ProviderRequestMetadata{}, err
	}
	framing, framingErr := resolveResponseFraming(expected.ResponseFraming)
	if framingErr != nil {
		return ProviderRequestMetadata{}, framingErr
	}
	o, err := validateResponsesRequestForFraming(raw, expected.MaxBytes, binding, capabilities, controls, allowedTools, framing)
	return ProviderRequestMetadata{SHA256: o.SHA256, SizeBytes: o.SizeBytes, Model: o.Model, MaxOutputTokens: o.MaxOutputTokens, InputItemCount: o.InputItemCount, ToolCount: o.ToolCount}, err
}

func decodeResponsesControls(raw json.RawMessage) (ResponsesRequestExpectation, error) {
	var controls ResponsesRequestExpectation
	if len(raw) == 0 || !uniqueProviderJSON(raw) || json.Unmarshal(raw, &controls) != nil {
		return controls, errors.New("invalid Responses adapter controls")
	}
	normal, err := json.Marshal(controls)
	if err != nil || !bytes.Equal(normal, raw) {
		return controls, errors.New("noncanonical Responses adapter controls")
	}
	return controls, nil
}

func decodeResponsesAdapterResponse(raw []byte, binding Binding, expected AdapterResponseExpectation) (ProviderResponseMetadata, error) {
	var capabilities ResponsesModelCapabilities
	if validateResponsesModelCapabilities(binding.Model.AdapterCapabilities) != nil || canonical.Decode(binding.Model.AdapterCapabilities, &capabilities) != nil {
		return ProviderResponseMetadata{}, errors.New("invalid Responses response capabilities")
	}
	o, err := ParseResponsesSSEWithOptions(raw, expected.MaxBytes, expected.MaxTotalTokens, ResponsesSSEOptions{RequireTrailingCostPingV1: capabilities.TrailingCostPingV1, ContentPartCompletesText: capabilities.ContentPartCompletesText, FunctionCallDoneNameV1: capabilities.FunctionCallDoneNameV1})
	if err != nil {
		return ProviderResponseMetadata{SHA256: o.SHA256, SizeBytes: int64(o.SizeBytes)}, err
	}
	tools, err := responsesSemanticTools(o.FunctionCalls)
	content, contentErr := responsesResponseContent(o)
	terminal, terminalErr := terminalStructuredOutputIdentity(content, expected.TerminalStructuredOutput)
	if err == nil {
		err = contentErr
	}
	if err == nil {
		err = terminalErr
	}
	return ProviderResponseMetadata{SHA256: o.SHA256, SizeBytes: int64(o.SizeBytes), ResponseID: o.ResponseID, ObservedModel: o.Model, Status: o.Status, Finish: o.FinishReason, UsageComplete: o.UsageComplete, Usage: o.Usage, Semantic: responseSemanticProjectionWithTerminal(o.OutputText, tools, terminal, terminalStructuredOutputSchemaSHA256(expected.TerminalStructuredOutput, terminal)), Content: content}, err
}

func decodeResponsesAdapterJSONResponse(raw []byte, binding Binding, expected AdapterResponseExpectation) (ProviderResponseMetadata, error) {
	var capabilities ResponsesModelCapabilities
	if validateResponsesModelCapabilities(binding.Model.AdapterCapabilities) != nil || canonical.Decode(binding.Model.AdapterCapabilities, &capabilities) != nil || capabilities.TrailingCostPingV1 || capabilities.ContentPartCompletesText || capabilities.FunctionCallDoneNameV1 {
		return ProviderResponseMetadata{}, errors.New("invalid Responses JSON capabilities")
	}
	o, err := ParseResponsesJSON(raw, expected.MaxBytes, expected.MaxTotalTokens)
	if err != nil {
		return ProviderResponseMetadata{SHA256: o.SHA256, SizeBytes: int64(o.SizeBytes)}, err
	}
	tools, err := responsesSemanticTools(o.FunctionCalls)
	content, contentErr := responsesResponseContent(o)
	terminal, terminalErr := terminalStructuredOutputIdentity(content, expected.TerminalStructuredOutput)
	if err == nil {
		err = contentErr
	}
	if err == nil {
		err = terminalErr
	}
	return ProviderResponseMetadata{SHA256: o.SHA256, SizeBytes: int64(o.SizeBytes), ResponseID: o.ResponseID, ObservedModel: o.Model, Status: o.Status, Finish: o.FinishReason, UsageComplete: o.UsageComplete, Usage: o.Usage, Semantic: responseSemanticProjectionWithTerminal(o.OutputText, tools, terminal, terminalStructuredOutputSchemaSHA256(expected.TerminalStructuredOutput, terminal)), Content: content}, err
}

func validateAnthropicAdapterRequest(raw []byte, binding Binding, expected AdapterRequestExpectation) (ProviderRequestMetadata, error) {
	var capabilities AnthropicMessagesModelCapabilities
	controls, controlsErr := decodeAnthropicControls(expected.Controls)
	if validateAnthropicMessagesModelCapabilities(binding.Model.AdapterCapabilities) != nil || canonical.Decode(binding.Model.AdapterCapabilities, &capabilities) != nil || controlsErr != nil {
		return ProviderRequestMetadata{}, errors.New("invalid Anthropic adapter controls")
	}
	framing, framingErr := resolveResponseFraming(expected.ResponseFraming)
	if framingErr != nil {
		return ProviderRequestMetadata{}, framingErr
	}
	o, err := validateAnthropicMessagesRequestForFraming(raw, expected.MaxBytes, binding, capabilities, controls, expected.Tools, framing)
	return ProviderRequestMetadata{SHA256: o.SHA256, SizeBytes: o.SizeBytes, Model: o.Model, MaxOutputTokens: o.MaxOutputTokens, InputItemCount: o.MessageCount, ToolCount: o.ToolCount}, err
}

func decodeAnthropicControls(raw json.RawMessage) (AnthropicMessagesRequestExpectation, error) {
	var controls AnthropicMessagesRequestExpectation
	if len(raw) == 0 || !uniqueProviderJSON(raw) || json.Unmarshal(raw, &controls) != nil {
		return controls, errors.New("invalid Anthropic adapter controls")
	}
	normal, err := json.Marshal(controls)
	if err != nil || !bytes.Equal(normal, raw) {
		return controls, errors.New("noncanonical Anthropic adapter controls")
	}
	return controls, nil
}

func decodeAnthropicAdapterResponse(raw []byte, _ Binding, expected AdapterResponseExpectation) (ProviderResponseMetadata, error) {
	o, err := ParseAnthropicMessagesSSE(raw, expected.MaxBytes, expected.MaxTotalTokens)
	finish := "stop"
	if o.StopReason == "tool_use" {
		finish = "tool_calls"
	}
	if err != nil {
		return ProviderResponseMetadata{SHA256: o.SHA256, SizeBytes: int64(o.SizeBytes), ResponseID: o.MessageID, ObservedModel: o.Model, Status: o.Status, UsageComplete: o.UsageComplete, Usage: o.Usage}, err
	}
	tools, err := anthropicSemanticTools(o.ToolUses)
	content, contentErr := anthropicResponseContent(o)
	if err == nil {
		err = contentErr
	}
	return ProviderResponseMetadata{SHA256: o.SHA256, SizeBytes: int64(o.SizeBytes), ResponseID: o.MessageID, ObservedModel: o.Model, Status: o.Status, Finish: finish, UsageComplete: o.UsageComplete, Usage: o.Usage, Semantic: responseSemanticProjection(o.OutputText, tools), Content: content}, err
}

func decodeAnthropicAdapterJSONResponse(raw []byte, _ Binding, expected AdapterResponseExpectation) (ProviderResponseMetadata, error) {
	o, err := ParseAnthropicMessagesJSON(raw, expected.MaxBytes, expected.MaxTotalTokens)
	finish := "stop"
	if o.StopReason == "tool_use" {
		finish = "tool_calls"
	}
	if err != nil {
		return ProviderResponseMetadata{SHA256: o.SHA256, SizeBytes: int64(o.SizeBytes), ResponseID: o.MessageID, ObservedModel: o.Model, Status: o.Status, UsageComplete: o.UsageComplete, Usage: o.Usage}, err
	}
	tools, err := anthropicSemanticTools(o.ToolUses)
	content, contentErr := anthropicResponseContent(o)
	if err == nil {
		err = contentErr
	}
	return ProviderResponseMetadata{SHA256: o.SHA256, SizeBytes: int64(o.SizeBytes), ResponseID: o.MessageID, ObservedModel: o.Model, Status: o.Status, Finish: finish, UsageComplete: o.UsageComplete, Usage: o.Usage, Semantic: responseSemanticProjection(o.OutputText, tools), Content: content}, err
}

func chatResponseContent(observation SSEObservation) (*ProviderResponseContent, error) {
	calls := make([]ProviderToolCallContent, len(observation.ToolCalls))
	for index, call := range observation.ToolCalls {
		normal, err := canonical.Normalize(call.Arguments)
		if err != nil {
			return nil, errors.New("provider tool arguments cannot be canonically retained")
		}
		calls[index] = ProviderToolCallContent{call.ID, call.Name, normal}
	}
	return &ProviderResponseContent{OutputText: observation.OutputText, ToolCalls: calls}, nil
}

func responsesResponseContent(observation ResponsesObservation) (*ProviderResponseContent, error) {
	calls := make([]ProviderToolCallContent, len(observation.FunctionCalls))
	for index, call := range observation.FunctionCalls {
		normal, err := canonical.Normalize(call.Arguments)
		if err != nil {
			return nil, errors.New("provider tool arguments cannot be canonically retained")
		}
		calls[index] = ProviderToolCallContent{call.CallID, call.Name, normal}
	}
	var reasoning *ProviderReasoningContent
	if len(observation.Reasoning) != 0 {
		reasoning = &ProviderReasoningContent{AdapterID: OpenAIResponsesAdapter, Responses: append([]ResponsesReasoning(nil), observation.Reasoning...)}
	}
	return &ProviderResponseContent{OutputText: observation.OutputText, ToolCalls: calls, Reasoning: reasoning}, nil
}

func anthropicResponseContent(observation AnthropicMessagesObservation) (*ProviderResponseContent, error) {
	calls := make([]ProviderToolCallContent, len(observation.ToolUses))
	for index, call := range observation.ToolUses {
		normal, err := canonical.Normalize(call.Input)
		if err != nil {
			return nil, errors.New("provider tool arguments cannot be canonically retained")
		}
		calls[index] = ProviderToolCallContent{call.ID, call.Name, normal}
	}
	var reasoning *ProviderReasoningContent
	if len(observation.Thinking) != 0 || len(observation.RedactedThinking) != 0 {
		reasoning = &ProviderReasoningContent{AdapterID: AnthropicMessagesAdapter, AnthropicThinking: append([]AnthropicThinking(nil), observation.Thinking...), AnthropicRedacted: append([]AnthropicRedactedThinking(nil), observation.RedactedThinking...)}
	}
	return &ProviderResponseContent{OutputText: observation.OutputText, ToolCalls: calls, Reasoning: reasoning}, nil
}

func responseSemanticProjection(text string, tools []ResponseToolIdentity) *ResponseSemanticProjection {
	return responseSemanticProjectionWithTerminal(text, tools, nil, "")
}

func responseSemanticProjectionWithTerminal(text string, tools []ResponseToolIdentity, terminal *ResponseToolIdentity, terminalSchemaSHA256 string) *ResponseSemanticProjection {
	digest := sha256.Sum256([]byte(text))
	return &ResponseSemanticProjection{Version: 1, Kind: "assistant-turn", ToolCalls: tools, OutputTextSHA256: hex.EncodeToString(digest[:]), TerminalTool: terminal, TerminalSchemaSHA256: terminalSchemaSHA256}
}

func chatSemanticTools(calls []ToolCall) ([]ResponseToolIdentity, error) {
	result := make([]ResponseToolIdentity, len(calls))
	for index, call := range calls {
		identity, err := responseToolIdentity(call.ID, call.Name, call.Arguments)
		if err != nil {
			return nil, err
		}
		result[index] = identity
	}
	return result, nil
}

func responsesSemanticTools(calls []ResponsesFunctionCall) ([]ResponseToolIdentity, error) {
	result := make([]ResponseToolIdentity, len(calls))
	for index, call := range calls {
		identity, err := responseToolIdentity(call.CallID, call.Name, call.Arguments)
		if err != nil {
			return nil, err
		}
		result[index] = identity
	}
	return result, nil
}

func anthropicSemanticTools(calls []AnthropicToolUse) ([]ResponseToolIdentity, error) {
	result := make([]ResponseToolIdentity, len(calls))
	for index, call := range calls {
		identity, err := responseToolIdentity(call.ID, call.Name, call.Input)
		if err != nil {
			return nil, err
		}
		result[index] = identity
	}
	return result, nil
}

func responseToolIdentity(id, name string, arguments json.RawMessage) (ResponseToolIdentity, error) {
	normal, err := canonical.Normalize(arguments)
	if err != nil || len(normal) < 2 || normal[0] != '{' {
		return ResponseToolIdentity{}, errors.New("provider tool arguments cannot be canonically bound")
	}
	digest := sha256.Sum256(normal)
	return ResponseToolIdentity{ID: id, Name: name, ArgumentsSHA256: hex.EncodeToString(digest[:])}, nil
}

func terminalStructuredOutputIdentity(content *ProviderResponseContent, expectation *TerminalStructuredOutputExpectation) (*ResponseToolIdentity, error) {
	if expectation == nil {
		return nil, nil
	}
	if content == nil {
		return nil, errors.New("terminal structured output response content is missing")
	}
	terminalIndex := -1
	for index, call := range content.ToolCalls {
		if call.Name != expectation.Name {
			continue
		}
		if terminalIndex >= 0 {
			return nil, errors.New("duplicate terminal structured output tool")
		}
		terminalIndex = index
	}
	if terminalIndex < 0 {
		// A structured turn may spend earlier provider calls on ordinary broker
		// tools. The reserved tool is the closure marker only when it appears.
		return nil, nil
	}
	if content.OutputText != "" || len(content.ToolCalls) != 1 {
		return nil, errors.New("terminal structured output must be the sole provider tool result")
	}
	call := content.ToolCalls[terminalIndex]
	if call.Name != expectation.Name || validateStructuredOutputValue(call.Arguments, expectation.Schema) != nil {
		return nil, errors.New("terminal structured output tool arguments differ from schema")
	}
	identity, err := responseToolIdentity(call.ID, call.Name, call.Arguments)
	if err != nil {
		return nil, err
	}
	return &identity, nil
}

func terminalStructuredOutputSchemaSHA256(expectation *TerminalStructuredOutputExpectation, terminal *ResponseToolIdentity) string {
	if expectation == nil || terminal == nil {
		return ""
	}
	return expectation.SchemaSHA256
}

func validateResponseSemanticProjection(projection ResponseSemanticProjection) error {
	if projection.Version != 1 || projection.Kind != "assistant-turn" || safepath.RequireDigest(projection.OutputTextSHA256) != nil || projection.ToolCalls == nil || len(projection.ToolCalls) > 64 {
		return errors.New("invalid provider response semantic projection")
	}
	seen := map[string]bool{}
	for _, call := range projection.ToolCalls {
		if !validProviderIdentity(call.ID, 256) || !requestToolIdentifier(call.Name) || safepath.RequireDigest(call.ArgumentsSHA256) != nil || seen[call.ID] {
			return errors.New("invalid provider response tool identity")
		}
		seen[call.ID] = true
	}
	if projection.TerminalTool != nil {
		terminal := projection.TerminalTool
		emptyDigest := sha256.Sum256(nil)
		if terminal.Name != StructuredOutputToolName || len(projection.ToolCalls) != 1 || projection.OutputTextSHA256 != hex.EncodeToString(emptyDigest[:]) || safepath.RequireDigest(projection.TerminalSchemaSHA256) != nil || !reflect.DeepEqual(*terminal, projection.ToolCalls[0]) {
			return errors.New("invalid terminal provider response tool identity")
		}
	} else if projection.TerminalSchemaSHA256 != "" {
		return errors.New("terminal schema identity without terminal tool")
	}
	return nil
}

func validateReceiptSemantic(modelVersion int, finish string, projection *ResponseSemanticProjection) error {
	if projection == nil {
		if modelVersion == 1 {
			return nil
		}
		return errors.New("provider v2 receipt lacks semantic projection")
	}
	if err := validateResponseSemanticProjection(*projection); err != nil {
		return err
	}
	if projection.TerminalTool != nil && modelVersion != 2 {
		return errors.New("terminal provider response tool requires v2 gateway")
	}
	if finish == "tool_calls" && len(projection.ToolCalls) == 0 || finish == "stop" && len(projection.ToolCalls) != 0 {
		return errors.New("provider semantic tool projection differs from finish")
	}
	if projection.TerminalTool != nil && finish != "tool_calls" {
		return errors.New("terminal provider response tool requires tool_calls finish")
	}
	return nil
}
