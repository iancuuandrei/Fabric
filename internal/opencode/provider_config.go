package opencode

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
)

// ProviderProtocol selects the pinned OpenCode SDK transport. These values
// match the provider gateway adapter identities; they do not identify an
// upstream vendor.
type ProviderProtocol string

const (
	// ProviderProtocolOpenAIChat selects OpenAI-compatible chat completions.
	ProviderProtocolOpenAIChat ProviderProtocol = "openai-chat-completions-sse-v1"
	// ProviderProtocolOpenAIResponses selects the OpenAI Responses transport.
	ProviderProtocolOpenAIResponses ProviderProtocol = "openai-responses-sse-v1"
	// ProviderProtocolAnthropic selects the Anthropic Messages transport.
	ProviderProtocolAnthropic    ProviderProtocol = "anthropic-messages-sse-v1"
	providerMaximumExactInteger                   = int64(1<<53 - 1)
	providerMaximumTimeoutMillis                  = 300_000
)

const encryptedReasoningInclude = "reasoning.encrypted_content"

// OpenAIResponsesVariantOptions are the exact controls expected on a selected
// Responses reasoning variant. Summary and Effort are model-bound tokens.
type OpenAIResponsesVariantOptions struct {
	Effort        string
	Summary       string
	TextVerbosity string
	Temperature   *json.Number
	TopP          *json.Number
}

// AnthropicVariantOptions are the exact @ai-sdk/anthropic provider controls
// for one selected variant. A budget is required only for enabled thinking.
type AnthropicVariantOptions struct {
	ThinkingMode         string
	ThinkingBudgetTokens *int64
	Effort               string
}

// ProviderVariantSpec binds the runtime Profile effort name separately from
// protocol controls. Name "none" with no Responses options is the explicit
// nonreasoning variant.
type ProviderVariantSpec struct {
	Name      string
	Responses *OpenAIResponsesVariantOptions
	Anthropic *AnthropicVariantOptions
}

// ProviderConfigurationSpec grants one OpenCode runtime identity access to one
// loopback gateway and one model. GatewayCapability is scoped proxy authority,
// never an upstream credential, and is omitted from receipts.
type ProviderConfigurationSpec struct {
	ProviderID          string
	ModelID             string
	Protocol            ProviderProtocol
	GatewayBaseURL      string
	GatewayCapability   string
	ContextWindowTokens int64
	MaxOutputTokens     int64
	Tools               bool
	Reasoning           bool
	TimeoutMillis       int
	RuntimeAgent        string
	Variant             ProviderVariantSpec
}

// ProviderConfigurationReceipt is the secret-free projection of one exact
// provider-and-tools /config readback. SHA256 binds the full wire snapshot.
type ProviderConfigurationReceipt struct {
	SHA256               string                    `json:"sha256"`
	ProviderID           string                    `json:"provider_id"`
	ModelID              string                    `json:"model_id"`
	Protocol             ProviderProtocol          `json:"protocol"`
	SDKPackage           string                    `json:"sdk_package"`
	SDKVersion           string                    `json:"sdk_version,omitempty"`
	GatewayBaseURL       string                    `json:"gateway_base_url"`
	ContextWindowTokens  int64                     `json:"context_window_tokens"`
	MaxOutputTokens      int64                     `json:"max_output_tokens"`
	RuntimeOutputTokens  int64                     `json:"runtime_output_tokens"`
	Tools                bool                      `json:"tools"`
	Reasoning            bool                      `json:"reasoning"`
	TimeoutMillis        int                       `json:"timeout_millis"`
	Variant              string                    `json:"variant"`
	ReasoningEffort      string                    `json:"reasoning_effort,omitempty"`
	ReasoningSummary     string                    `json:"reasoning_summary,omitempty"`
	ThinkingMode         string                    `json:"thinking_mode,omitempty"`
	ThinkingBudgetTokens *int64                    `json:"thinking_budget_tokens,omitempty"`
	AnthropicEffort      string                    `json:"anthropic_effort,omitempty"`
	RuntimeAgent         string                    `json:"runtime_agent,omitempty"`
	TextVerbosity        string                    `json:"text_verbosity,omitempty"`
	Temperature          string                    `json:"temperature,omitempty"`
	TopP                 string                    `json:"top_p,omitempty"`
	ToolsConfiguration   ToolsConfigurationReceipt `json:"tools_configuration"`
}

type normalizedProviderConfiguration struct {
	spec                ProviderConfigurationSpec
	sdkPackage          string
	sdkVersion          string
	runtimeOutputTokens int64
	variant             map[string]any
	temperatureEnabled  bool
	temperature         *json.Number
	topP                *json.Number
}

// BuildProviderToolsConfiguration composes the existing exact MCP/permission
// builder with one provider and model. It does not accept raw configuration.
func BuildProviderToolsConfiguration(provider ProviderConfigurationSpec, tools ToolsConfigurationSpec) (string, error) {
	provider, tools = snapshotProviderConfiguration(provider), snapshotToolsConfiguration(tools)
	normalized, err := normalizeProviderConfiguration(provider)
	if err != nil {
		return "", err
	}
	toolsConfig, err := BuildToolsConfiguration(tools)
	if err != nil {
		return "", err
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(toolsConfig), &config); err != nil {
		return "", errors.New("cannot compose OpenCode provider configuration")
	}

	providerValue := map[string]any{
		"npm":       normalized.sdkPackage,
		"name":      "EngOrch loopback gateway",
		"whitelist": []string{provider.ModelID},
		"options": map[string]any{
			"baseURL": provider.GatewayBaseURL,
			"apiKey":  provider.GatewayCapability,
			"timeout": provider.TimeoutMillis,
		},
		"models": map[string]any{provider.ModelID: map[string]any{
			"name":        provider.ModelID,
			"provider":    map[string]string{"npm": normalized.sdkPackage},
			"tool_call":   provider.Tools,
			"reasoning":   provider.Reasoning,
			"temperature": normalized.temperatureEnabled,
			"limit": map[string]int64{
				"context": provider.ContextWindowTokens,
				"output":  normalized.runtimeOutputTokens,
			},
			"modalities": map[string][]string{"input": {"text"}, "output": {"text"}},
			"variants":   map[string]any{provider.Variant.Name: normalized.variant},
		}},
	}
	if normalized.temperature != nil || normalized.topP != nil {
		agent := map[string]any{}
		if normalized.temperature != nil {
			agent["temperature"] = *normalized.temperature
		}
		if normalized.topP != nil {
			agent["top_p"] = *normalized.topP
		}
		encoded, err := json.Marshal(map[string]any{provider.RuntimeAgent: agent})
		if err != nil {
			return "", errors.New("cannot encode OpenCode provider sampling configuration")
		}
		config["agent"] = encoded
	}
	for key, value := range map[string]any{
		"enabled_providers": []string{provider.ProviderID},
		"model":             provider.ProviderID + "/" + provider.ModelID,
		"small_model":       provider.ProviderID + "/" + provider.ModelID,
		"provider":          map[string]any{provider.ProviderID: providerValue},
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", errors.New("cannot encode OpenCode provider configuration")
		}
		config[key] = encoded
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return "", errors.New("cannot encode OpenCode provider configuration")
	}
	return string(raw), nil
}

// ReadProviderToolsConfiguration performs one /config read and admits both the
// exact provider projection and the existing tools projection from those same
// bytes.
func (c *Client) ReadProviderToolsConfiguration(ctx context.Context, provider ProviderConfigurationSpec, tools ToolsConfigurationSpec) (ProviderConfigurationReceipt, error) {
	provider, tools = snapshotProviderConfiguration(provider), snapshotToolsConfiguration(tools)
	if _, err := normalizeProviderConfiguration(provider); err != nil {
		return ProviderConfigurationReceipt{}, err
	}
	if _, err := normalizeToolsConfiguration(tools); err != nil {
		return ProviderConfigurationReceipt{}, err
	}
	raw, err := c.read(ctx, "/config")
	if err != nil {
		return ProviderConfigurationReceipt{}, err
	}
	return decodeProviderToolsConfiguration(raw, provider, tools)
}

func snapshotProviderConfiguration(spec ProviderConfigurationSpec) ProviderConfigurationSpec {
	if spec.Variant.Responses != nil {
		options := *spec.Variant.Responses
		if options.Temperature != nil {
			value := json.Number(options.Temperature.String())
			options.Temperature = &value
		}
		if options.TopP != nil {
			value := json.Number(options.TopP.String())
			options.TopP = &value
		}
		spec.Variant.Responses = &options
	}
	if spec.Variant.Anthropic != nil {
		options := *spec.Variant.Anthropic
		if options.ThinkingBudgetTokens != nil {
			budget := *options.ThinkingBudgetTokens
			options.ThinkingBudgetTokens = &budget
		}
		spec.Variant.Anthropic = &options
	}
	return spec
}

func snapshotToolsConfiguration(spec ToolsConfigurationSpec) ToolsConfigurationSpec {
	spec.ToolNames = append([]string(nil), spec.ToolNames...)
	return spec
}

func decodeProviderToolsConfiguration(raw []byte, provider ProviderConfigurationSpec, tools ToolsConfigurationSpec) (ProviderConfigurationReceipt, error) {
	normalized, err := normalizeProviderConfiguration(provider)
	if err != nil {
		return ProviderConfigurationReceipt{}, err
	}
	toolsReceipt, err := decodeToolsConfiguration(raw, tools)
	if err != nil {
		return ProviderConfigurationReceipt{}, err
	}
	config, err := wireObject(raw)
	if err != nil {
		return ProviderConfigurationReceipt{}, err
	}
	var enabled []string
	var model, smallModel string
	selector := provider.ProviderID + "/" + provider.ModelID
	if field(config, "enabled_providers", &enabled) != nil || len(enabled) != 1 || enabled[0] != provider.ProviderID ||
		field(config, "model", &model) != nil || model != selector ||
		field(config, "small_model", &smallModel) != nil || smallModel != selector {
		return ProviderConfigurationReceipt{}, errors.New("OpenCode provider allowlist mismatch")
	}
	var providers map[string]json.RawMessage
	if field(config, "provider", &providers) != nil || len(providers) != 1 || providers[provider.ProviderID] == nil {
		return ProviderConfigurationReceipt{}, errors.New("OpenCode provider set mismatch")
	}
	configured, err := wireObject(providers[provider.ProviderID])
	if err != nil || !toolsExactKeys(configured, "npm", "name", "whitelist", "options", "models") {
		return ProviderConfigurationReceipt{}, errors.New("OpenCode provider shape mismatch")
	}
	var npm, name string
	var whitelist []string
	if field(configured, "npm", &npm) != nil || npm != normalized.sdkPackage ||
		field(configured, "name", &name) != nil || name != "EngOrch loopback gateway" ||
		field(configured, "whitelist", &whitelist) != nil || len(whitelist) != 1 || whitelist[0] != provider.ModelID {
		return ProviderConfigurationReceipt{}, errors.New("OpenCode provider identity mismatch")
	}
	if err := validateProviderOptions(configured["options"], provider); err != nil {
		return ProviderConfigurationReceipt{}, err
	}
	var models map[string]json.RawMessage
	if field(configured, "models", &models) != nil || len(models) != 1 || models[provider.ModelID] == nil {
		return ProviderConfigurationReceipt{}, errors.New("OpenCode provider model set mismatch")
	}
	if err := validateProviderModel(models[provider.ModelID], normalized); err != nil {
		return ProviderConfigurationReceipt{}, err
	}
	if err := validateProviderAgent(config["agent"], normalized); err != nil {
		return ProviderConfigurationReceipt{}, err
	}

	receipt := ProviderConfigurationReceipt{
		SHA256: toolsReceipt.SHA256, ProviderID: provider.ProviderID, ModelID: provider.ModelID,
		Protocol: provider.Protocol, SDKPackage: normalized.sdkPackage, SDKVersion: normalized.sdkVersion, GatewayBaseURL: provider.GatewayBaseURL,
		ContextWindowTokens: provider.ContextWindowTokens, MaxOutputTokens: provider.MaxOutputTokens,
		RuntimeOutputTokens: normalized.runtimeOutputTokens,
		Tools:               provider.Tools, Reasoning: provider.Reasoning, TimeoutMillis: provider.TimeoutMillis,
		Variant: provider.Variant.Name, ToolsConfiguration: toolsReceipt,
	}
	if provider.Variant.Responses != nil {
		receipt.ReasoningEffort = provider.Variant.Responses.Effort
		receipt.ReasoningSummary = provider.Variant.Responses.Summary
		receipt.TextVerbosity = provider.Variant.Responses.TextVerbosity
		if provider.Variant.Responses.Temperature != nil {
			receipt.RuntimeAgent = provider.RuntimeAgent
			receipt.Temperature = provider.Variant.Responses.Temperature.String()
		}
		if provider.Variant.Responses.TopP != nil {
			receipt.RuntimeAgent = provider.RuntimeAgent
			receipt.TopP = provider.Variant.Responses.TopP.String()
		}
	}
	if provider.Variant.Anthropic != nil {
		receipt.ThinkingMode = provider.Variant.Anthropic.ThinkingMode
		receipt.AnthropicEffort = provider.Variant.Anthropic.Effort
		if provider.Variant.Anthropic.ThinkingBudgetTokens != nil {
			budget := *provider.Variant.Anthropic.ThinkingBudgetTokens
			receipt.ThinkingBudgetTokens = &budget
		}
	}
	return receipt, nil
}

func normalizeProviderConfiguration(spec ProviderConfigurationSpec) (normalizedProviderConfiguration, error) {
	sdk, sdkVersion, ok := providerSDK(spec.Protocol)
	parsed, err := url.Parse(spec.GatewayBaseURL)
	if !safeProviderID(spec.ProviderID) || !printableProviderValue(spec.ModelID, 256) || !ok || err != nil ||
		parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Path != "/v1" || parsed.RawPath != "" {
		return normalizedProviderConfiguration{}, errors.New("invalid OpenCode provider identity or gateway")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 || parsed.Host != "127.0.0.1:"+strconv.Itoa(port) || parsed.String() != spec.GatewayBaseURL {
		return normalizedProviderConfiguration{}, errors.New("exact IPv4 loopback provider gateway required")
	}
	if len(spec.GatewayCapability) < 32 || len(spec.GatewayCapability) > 512 || !safeCapability(spec.GatewayCapability) {
		return normalizedProviderConfiguration{}, errors.New("safe scoped provider gateway capability required")
	}
	if spec.ContextWindowTokens < 1 || spec.ContextWindowTokens > providerMaximumExactInteger ||
		spec.MaxOutputTokens < 1 || spec.MaxOutputTokens > spec.ContextWindowTokens ||
		spec.TimeoutMillis < 1 || spec.TimeoutMillis > providerMaximumTimeoutMillis {
		return normalizedProviderConfiguration{}, errors.New("invalid OpenCode provider limits")
	}
	if !requestOptionToken(spec.Variant.Name) {
		return normalizedProviderConfiguration{}, errors.New("invalid OpenCode provider variant")
	}
	variant := map[string]any{}
	runtimeOutputTokens := spec.MaxOutputTokens
	if spec.Variant.Responses != nil && spec.Variant.Anthropic != nil {
		return normalizedProviderConfiguration{}, errors.New("multiple provider variant option sets")
	}
	if spec.Variant.Responses == nil && spec.Variant.Anthropic == nil {
		if spec.Variant.Name != "none" || spec.Reasoning {
			return normalizedProviderConfiguration{}, errors.New("explicit nonreasoning variant required")
		}
	} else if spec.Variant.Responses != nil {
		options := spec.Variant.Responses
		if spec.Protocol != ProviderProtocolOpenAIResponses {
			return normalizedProviderConfiguration{}, errors.New("Responses options require OpenAI Responses protocol")
		}
		if spec.Reasoning {
			if spec.Variant.Name != options.Effort || !requestOptionToken(options.Effort) || !requestOptionToken(options.Summary) || options.Effort == "none" {
				return normalizedProviderConfiguration{}, errors.New("invalid OpenAI Responses reasoning variant")
			}
			variant["reasoningEffort"] = options.Effort
			variant["reasoningSummary"] = options.Summary
			variant["include"] = []string{encryptedReasoningInclude}
		} else if spec.Variant.Name != "none" || options.Effort != "" || options.Summary != "" {
			return normalizedProviderConfiguration{}, errors.New("invalid nonreasoning OpenAI Responses variant")
		}
		if options.TextVerbosity != "" {
			if !requestOptionToken(options.TextVerbosity) {
				return normalizedProviderConfiguration{}, errors.New("invalid OpenAI Responses text verbosity")
			}
			variant["textVerbosity"] = options.TextVerbosity
		}
		if options.Temperature != nil || options.TopP != nil {
			if spec.Reasoning {
				return normalizedProviderConfiguration{}, errors.New("OpenCode Responses reasoning does not emit sampling controls")
			}
			if !requestOptionToken(spec.RuntimeAgent) {
				return normalizedProviderConfiguration{}, errors.New("explicit OpenCode runtime agent required for sampling")
			}
			if !providerNumberWithin(options.Temperature, "0", "2") || !providerNumberWithin(options.TopP, "0", "1") {
				return normalizedProviderConfiguration{}, errors.New("invalid OpenAI Responses sampling control")
			}
		}
		if !spec.Reasoning && options.TextVerbosity == "" && options.Temperature == nil && options.TopP == nil {
			return normalizedProviderConfiguration{}, errors.New("empty OpenAI Responses options")
		}
		return normalizedProviderConfiguration{
			spec: spec, sdkPackage: sdk, sdkVersion: sdkVersion, runtimeOutputTokens: runtimeOutputTokens, variant: variant,
			temperatureEnabled: options.Temperature != nil, temperature: options.Temperature, topP: options.TopP,
		}, nil
	} else {
		options := spec.Variant.Anthropic
		if spec.Protocol != ProviderProtocolAnthropic {
			return normalizedProviderConfiguration{}, errors.New("Anthropic options require Anthropic protocol")
		}
		effortName := options.Effort
		if effortName == "" {
			effortName = "none"
		} else if !requestOptionToken(effortName) || effortName == "none" {
			return normalizedProviderConfiguration{}, errors.New("invalid Anthropic effort")
		}
		if spec.Variant.Name != effortName {
			return normalizedProviderConfiguration{}, errors.New("Anthropic effort differs from variant")
		}
		thinking := map[string]any{"type": options.ThinkingMode}
		switch options.ThinkingMode {
		case "enabled":
			if !spec.Reasoning || options.ThinkingBudgetTokens == nil || *options.ThinkingBudgetTokens < 1 || *options.ThinkingBudgetTokens >= spec.MaxOutputTokens {
				return normalizedProviderConfiguration{}, errors.New("invalid enabled Anthropic thinking budget")
			}
			thinking["budgetTokens"] = *options.ThinkingBudgetTokens
			runtimeOutputTokens -= *options.ThinkingBudgetTokens
		case "adaptive":
			if !spec.Reasoning || options.ThinkingBudgetTokens != nil {
				return normalizedProviderConfiguration{}, errors.New("adaptive Anthropic thinking cannot set a budget")
			}
		case "disabled":
			if spec.Reasoning || options.ThinkingBudgetTokens != nil {
				return normalizedProviderConfiguration{}, errors.New("disabled Anthropic thinking cannot set a budget")
			}
		default:
			return normalizedProviderConfiguration{}, errors.New("invalid Anthropic thinking mode")
		}
		variant["thinking"] = thinking
		if options.Effort != "" {
			variant["effort"] = options.Effort
		}
	}
	return normalizedProviderConfiguration{spec: spec, sdkPackage: sdk, sdkVersion: sdkVersion, runtimeOutputTokens: runtimeOutputTokens, variant: variant}, nil
}

func providerNumberWithin(value *json.Number, minimum, maximum string) bool {
	if value == nil {
		return true
	}
	text := value.String()
	if text == "" || len(text) > 128 {
		return false
	}
	if !json.Valid([]byte(text)) {
		return false
	}
	number, lower, upper := new(big.Rat), new(big.Rat), new(big.Rat)
	if _, ok := number.SetString(text); !ok {
		return false
	}
	_, lowerOK := lower.SetString(minimum)
	_, upperOK := upper.SetString(maximum)
	return lowerOK && upperOK && number.Cmp(lower) >= 0 && number.Cmp(upper) <= 0
}

func providerSDK(protocol ProviderProtocol) (string, string, bool) {
	switch protocol {
	case ProviderProtocolOpenAIChat:
		return "@ai-sdk/openai-compatible", "", true
	case ProviderProtocolOpenAIResponses:
		return "@ai-sdk/openai", "", true
	case ProviderProtocolAnthropic:
		return "@ai-sdk/anthropic", "3.0.111", true
	default:
		return "", "", false
	}
}

func safeProviderID(value string) bool {
	if !strings.HasPrefix(value, "engorch-") || len(value) > 128 || len(value) == len("engorch-") {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
			return false
		}
	}
	return true
}

func printableProviderValue(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func requestOptionToken(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.') {
			return false
		}
	}
	return true
}

func validateProviderOptions(raw json.RawMessage, expected ProviderConfigurationSpec) error {
	options, err := wireObject(raw)
	if err != nil || !toolsExactKeys(options, "baseURL", "apiKey", "timeout") {
		return errors.New("OpenCode provider options mismatch")
	}
	var baseURL, capability string
	var timeout int
	if field(options, "baseURL", &baseURL) != nil || baseURL != expected.GatewayBaseURL ||
		field(options, "timeout", &timeout) != nil || timeout != expected.TimeoutMillis ||
		field(options, "apiKey", &capability) != nil || len(capability) != len(expected.GatewayCapability) ||
		subtle.ConstantTimeCompare([]byte(capability), []byte(expected.GatewayCapability)) != 1 {
		return errors.New("OpenCode provider gateway options mismatch")
	}
	return nil
}

func validateProviderModel(raw json.RawMessage, expected normalizedProviderConfiguration) error {
	model, err := wireObject(raw)
	if err != nil || !toolsExactKeys(model, "name", "provider", "tool_call", "reasoning", "temperature", "limit", "modalities", "variants") {
		return errors.New("OpenCode provider model shape mismatch")
	}
	var name string
	var tools, reasoning, temperature bool
	if field(model, "name", &name) != nil || name != expected.spec.ModelID ||
		field(model, "tool_call", &tools) != nil || tools != expected.spec.Tools ||
		field(model, "reasoning", &reasoning) != nil || reasoning != expected.spec.Reasoning ||
		field(model, "temperature", &temperature) != nil || temperature != expected.temperatureEnabled {
		return errors.New("OpenCode provider model capability mismatch")
	}
	provider, err := wireObject(model["provider"])
	if err != nil || !toolsExactKeys(provider, "npm") {
		return errors.New("OpenCode provider model SDK mismatch")
	}
	var npm string
	if field(provider, "npm", &npm) != nil || npm != expected.sdkPackage {
		return errors.New("OpenCode provider model SDK mismatch")
	}
	limit, err := wireObject(model["limit"])
	if err != nil || !toolsExactKeys(limit, "context", "output") {
		return errors.New("OpenCode provider model limit mismatch")
	}
	var contextTokens, outputTokens int64
	if field(limit, "context", &contextTokens) != nil || contextTokens != expected.spec.ContextWindowTokens ||
		field(limit, "output", &outputTokens) != nil || outputTokens != expected.runtimeOutputTokens {
		return errors.New("OpenCode provider model limit mismatch")
	}
	modalities, err := wireObject(model["modalities"])
	if err != nil || !toolsExactKeys(modalities, "input", "output") || !exactTextModality(modalities["input"]) || !exactTextModality(modalities["output"]) {
		return errors.New("OpenCode provider model modality mismatch")
	}
	variants, err := wireObject(model["variants"])
	if err != nil || len(variants) != 1 || variants[expected.spec.Variant.Name] == nil {
		return errors.New("OpenCode provider variant set mismatch")
	}
	actual, err := wireObject(variants[expected.spec.Variant.Name])
	if err != nil || !equalProviderVariant(actual, expected.variant) {
		return errors.New("OpenCode provider variant options mismatch")
	}
	return nil
}

func validateProviderAgent(raw json.RawMessage, expected normalizedProviderConfiguration) error {
	if expected.temperature == nil && expected.topP == nil {
		return nil
	}
	agents, err := wireObject(raw)
	if err != nil || len(agents) != 1 || agents[expected.spec.RuntimeAgent] == nil {
		return errors.New("OpenCode provider sampling agent mismatch")
	}
	agent, err := wireObject(agents[expected.spec.RuntimeAgent])
	if err != nil {
		return errors.New("OpenCode provider sampling agent mismatch")
	}
	wantKeys := []string{}
	if expected.temperature != nil {
		wantKeys = append(wantKeys, "temperature")
	}
	if expected.topP != nil {
		wantKeys = append(wantKeys, "top_p")
	}
	if agent["options"] != nil || agent["permission"] != nil {
		wantKeys = append(wantKeys, "options", "permission")
		options, optionsErr := wireObject(agent["options"])
		permission, permissionErr := wireObject(agent["permission"])
		if optionsErr != nil || permissionErr != nil || len(options) != 0 || len(permission) != 0 {
			return errors.New("OpenCode provider sampling agent defaults mismatch")
		}
	}
	if !toolsExactKeys(agent, wantKeys...) || !providerOptionalNumberEquals(agent["temperature"], expected.temperature) || !providerOptionalNumberEquals(agent["top_p"], expected.topP) {
		return errors.New("OpenCode provider sampling controls mismatch")
	}
	return nil
}

func providerOptionalNumberEquals(raw json.RawMessage, expected *json.Number) bool {
	if expected == nil {
		return raw == nil
	}
	if raw == nil {
		return false
	}
	left, right := new(big.Rat), new(big.Rat)
	_, leftOK := left.SetString(string(raw))
	_, rightOK := right.SetString(expected.String())
	return leftOK && rightOK && left.Cmp(right) == 0
}

func exactTextModality(raw json.RawMessage) bool {
	var values []string
	return json.Unmarshal(raw, &values) == nil && len(values) == 1 && values[0] == "text"
}

func equalProviderVariant(actual map[string]json.RawMessage, expected map[string]any) bool {
	got, err := json.Marshal(actual)
	if err != nil {
		return false
	}
	want, err := json.Marshal(expected)
	if err != nil {
		return false
	}
	canonicalGot, gotErr := canonical.Normalize(got)
	canonicalWant, wantErr := canonical.Normalize(want)
	return gotErr == nil && wantErr == nil && bytes.Equal(canonicalGot, canonicalWant)
}
