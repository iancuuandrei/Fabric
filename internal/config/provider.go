package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/runtime"
)

// Provider is a reusable operator configuration for controller-owned provider
// transport. It contains credential locations, never credential values.
type Provider struct {
	Version     int                     `toml:"version" json:"version"`
	Credentials []ProviderCredential    `toml:"credentials" json:"credentials"`
	Endpoints   []ProviderEndpoint      `toml:"endpoints" json:"endpoints"`
	Models      []ProviderModel         `toml:"models" json:"models"`
	Roles       map[string]ProviderRole `toml:"roles" json:"roles"`
}

// ProviderCredential maps a public credential reference to an environment
// variable name resolved only by the controller.
type ProviderCredential struct {
	Ref         string `toml:"ref" json:"ref"`
	Environment string `toml:"environment" json:"environment"`
}

// ProviderAuth configures credential framing without carrying its value.
type ProviderAuth struct {
	Scheme        string `toml:"scheme" json:"scheme"`
	CredentialRef string `toml:"credential_ref" json:"credential_ref"`
	HeaderName    string `toml:"header_name" json:"header_name,omitempty"`
}

// ProviderEndpoint is the TOML representation of a v2 endpoint contract.
type ProviderEndpoint struct {
	Name          string            `toml:"name" json:"name"`
	Version       int               `toml:"version" json:"version"`
	Provider      string            `toml:"provider" json:"provider"`
	URL           string            `toml:"url" json:"url"`
	AdapterID     string            `toml:"adapter_id" json:"adapter_id"`
	Auth          ProviderAuth      `toml:"auth" json:"auth"`
	PublicHeaders map[string]string `toml:"public_headers" json:"public_headers,omitempty"`
	SessionHeader string            `toml:"session_header" json:"session_header,omitempty"`
}

// Contract returns the validated public endpoint contract.
func (e ProviderEndpoint) Contract() (providergateway.EndpointContract, error) {
	contract := providergateway.EndpointContract{Version: e.Version, Provider: e.Provider, URL: e.URL, AdapterID: e.AdapterID, Auth: &providergateway.AuthContract{Scheme: e.Auth.Scheme, CredentialRef: e.Auth.CredentialRef, HeaderName: e.Auth.HeaderName}, PublicHeaders: cloneStrings(e.PublicHeaders), SessionHeader: e.SessionHeader}
	_, err := contract.ID()
	return contract, err
}

// ProviderCapabilities is the protocol-neutral model capability declaration.
type ProviderCapabilities struct {
	Tools                     bool                                   `toml:"tools" json:"tools"`
	Reasoning                 bool                                   `toml:"reasoning" json:"reasoning"`
	OutputCap                 bool                                   `toml:"output_cap" json:"output_cap"`
	CompleteUsage             bool                                   `toml:"complete_usage" json:"complete_usage"`
	StructuredOutputModes     []providergateway.StructuredOutputMode `toml:"structured_output_modes" json:"structured_output_modes,omitempty"`
	SupportedResponseFramings []providergateway.ResponseFraming      `toml:"supported_response_framings" json:"supported_response_framings,omitempty"`
}

// ProviderPricing declares conservative API price ceilings.
type ProviderPricing struct {
	Currency                    string `toml:"currency" json:"currency"`
	Unit                        string `toml:"unit" json:"unit"`
	MaxInputMicroUSDPerMillion  int64  `toml:"max_input_micro_usd_per_million" json:"max_input_micro_usd_per_million"`
	MaxOutputMicroUSDPerMillion int64  `toml:"max_output_micro_usd_per_million" json:"max_output_micro_usd_per_million"`
}

// ProviderModel is the TOML representation of a v2 model contract.
type ProviderModel struct {
	Name                    string               `toml:"name" json:"name"`
	Version                 int                  `toml:"version" json:"version"`
	Provider                string               `toml:"provider" json:"provider"`
	Model                   string               `toml:"model" json:"model"`
	AdapterID               string               `toml:"adapter_id" json:"adapter_id"`
	Capabilities            ProviderCapabilities `toml:"capabilities" json:"capabilities"`
	AdapterCapabilitiesJSON string               `toml:"adapter_capabilities_json" json:"adapter_capabilities_json"`
	ObservedModelAliases    []string             `toml:"observed_model_aliases" json:"observed_model_aliases,omitempty"`
	ContextWindowTokens     int64                `toml:"context_window_tokens" json:"context_window_tokens"`
	MaxCalls                int                  `toml:"max_calls" json:"max_calls"`
	MaxRequestBytes         int64                `toml:"max_request_bytes" json:"max_request_bytes"`
	MaxResponseBytes        int64                `toml:"max_response_bytes" json:"max_response_bytes"`
	MaxOutputTokens         int64                `toml:"max_output_tokens" json:"max_output_tokens"`
	Pricing                 *ProviderPricing     `toml:"pricing" json:"pricing,omitempty"`
}

// Contract returns the validated public model contract.
func (m ProviderModel) Contract() (providergateway.ModelContract, error) {
	contract := providergateway.ModelContract{Version: m.Version, Provider: m.Provider, Model: m.Model, AdapterID: m.AdapterID, Capabilities: &providergateway.ModelCapabilities{Tools: m.Capabilities.Tools, Reasoning: m.Capabilities.Reasoning, OutputCap: m.Capabilities.OutputCap, CompleteUsage: m.Capabilities.CompleteUsage, StructuredOutputModes: append([]providergateway.StructuredOutputMode(nil), m.Capabilities.StructuredOutputModes...), SupportedResponseFramings: append([]providergateway.ResponseFraming(nil), m.Capabilities.SupportedResponseFramings...)}, AdapterCapabilities: json.RawMessage(m.AdapterCapabilitiesJSON), ObservedModelAliases: append([]string(nil), m.ObservedModelAliases...), ContextWindowTokens: m.ContextWindowTokens, MaxCalls: m.MaxCalls, MaxRequestBytes: m.MaxRequestBytes, MaxResponseBytes: m.MaxResponseBytes, MaxOutputTokens: m.MaxOutputTokens}
	if m.Pricing != nil {
		contract.Pricing = &providergateway.PricingPolicy{Currency: m.Pricing.Currency, Unit: m.Pricing.Unit, MaxInputMicroUSDPerMillion: m.Pricing.MaxInputMicroUSDPerMillion, MaxOutputMicroUSDPerMillion: m.Pricing.MaxOutputMicroUSDPerMillion}
	}
	_, err := contract.ID()
	return contract, err
}

// ProviderVariant is the typed runtime-facing subset of adapter controls.
// AdapterControlsJSON remains exact wire policy and must agree with this value.
type ProviderVariant struct {
	Effort               string `toml:"effort" json:"effort"`
	SystemRole           string `toml:"system_role" json:"system_role,omitempty"`
	ReasoningSummary     string `toml:"reasoning_summary" json:"reasoning_summary,omitempty"`
	ThinkingMode         string `toml:"thinking_mode" json:"thinking_mode,omitempty"`
	ThinkingBudgetTokens *int64 `toml:"thinking_budget_tokens" json:"thinking_budget_tokens,omitempty"`
	TextFormat           string `toml:"text_format" json:"text_format,omitempty"`
	TextVerbosity        string `toml:"text_verbosity" json:"text_verbosity,omitempty"`
}

// ProviderRole selects one endpoint, model and exact adapter control document.
type ProviderRole struct {
	Endpoint             string                          `toml:"endpoint" json:"endpoint"`
	Model                string                          `toml:"model" json:"model"`
	AdapterControlsJSON  string                          `toml:"adapter_controls_json" json:"adapter_controls_json"`
	Variant              ProviderVariant                 `toml:"variant" json:"variant"`
	RequiredCapabilities *ProviderRequiredCapabilities   `toml:"required_capabilities" json:"required_capabilities,omitempty"`
	ResponseFraming      providergateway.ResponseFraming `toml:"response_framing" json:"response_framing,omitempty"`
}

// ProviderRequiredCapabilities is the strict TOML-facing role requirement.
// SchemaJSON is retained exactly and decoded only at the gateway boundary.
type ProviderRequiredCapabilities struct {
	Tools            bool                                 `toml:"tools" json:"tools"`
	Reasoning        bool                                 `toml:"reasoning" json:"reasoning"`
	StructuredOutput providergateway.StructuredOutputMode `toml:"structured_output" json:"structured_output"`
	SchemaName       string                               `toml:"schema_name" json:"schema_name,omitempty"`
	SchemaJSON       string                               `toml:"schema_json" json:"schema_json,omitempty"`
}

func (c ProviderRequiredCapabilities) gateway() *providergateway.RequiredCapabilities {
	return &providergateway.RequiredCapabilities{Tools: c.Tools, Reasoning: c.Reasoning, StructuredOutput: c.StructuredOutput, SchemaName: c.SchemaName, Schema: json.RawMessage(c.SchemaJSON)}
}

// ResolvedProviderRole contains public dispatch configuration. Its credential
// field is an environment name rather than the secret value.
type ResolvedProviderRole struct {
	Endpoint              providergateway.EndpointContract
	Model                 providergateway.ModelContract
	AdapterControls       json.RawMessage
	Variant               ProviderVariant
	RequiredCapabilities  *providergateway.RequiredCapabilities
	CredentialEnvironment string
	ResponseFraming       providergateway.ResponseFraming
}

// ParseProvider parses a bounded standalone provider section. Top-level config
// integration can decode Provider directly with the same strict TOML decoder.
func ParseProvider(raw []byte) (Provider, error) {
	var provider Provider
	if len(raw) == 0 || len(raw) > 64<<10 {
		return provider, errors.New("provider configuration exceeds bounds")
	}
	if err := toml.NewDecoder(bytes.NewReader(raw)).DisallowUnknownFields().Decode(&provider); err != nil {
		return provider, err
	}
	return provider, provider.Validate()
}

// Validate checks standalone identities, contracts, credentials and role mappings.
func (p Provider) Validate() error { return p.validate(nil) }

// validate checks the provider with optional exact native terminal schemas for
// explicitly enabled OpenCode writer/fixer roles. A nil map preserves the
// generic provider contract and never admits a required synthetic tool.
func (p Provider) validate(nativeSchemas map[string]json.RawMessage) error {
	if p.Version != 1 || len(p.Credentials) == 0 || len(p.Credentials) > 32 || len(p.Endpoints) == 0 || len(p.Endpoints) > 32 || len(p.Models) == 0 || len(p.Models) > 64 || len(p.Roles) == 0 || len(p.Roles) > 5 {
		return errors.New("invalid provider configuration cardinality")
	}
	credentials := map[string]string{}
	environments := map[string]bool{}
	for _, credential := range p.Credentials {
		if !providerIdentifier(credential.Ref) || !environmentName(credential.Environment) || credentials[credential.Ref] != "" || environments[credential.Environment] {
			return errors.New("invalid or duplicate provider credential mapping")
		}
		credentials[credential.Ref] = credential.Environment
		environments[credential.Environment] = true
	}
	endpoints := map[string]providergateway.EndpointContract{}
	for _, configured := range p.Endpoints {
		contract, err := configured.Contract()
		if !providerIdentifier(configured.Name) || err != nil || endpoints[configured.Name].Version != 0 || credentials[configured.Auth.CredentialRef] == "" {
			return errors.New("invalid, duplicate or unresolved provider endpoint")
		}
		endpoints[configured.Name] = contract
	}
	models := map[string]providergateway.ModelContract{}
	for _, configured := range p.Models {
		contract, err := configured.Contract()
		if !providerIdentifier(configured.Name) || len(contract.Model) > 128 || err != nil || models[configured.Name].Version != 0 {
			return errors.New("invalid or duplicate provider model")
		}
		models[configured.Name] = contract
	}
	for role, selected := range p.Roles {
		endpoint, endpointOK := endpoints[selected.Endpoint]
		model, modelOK := models[selected.Model]
		if !providerRole(role) || !endpointOK || !modelOK || selected.RequiredCapabilities == nil || endpoint.Provider != model.Provider || endpoint.AdapterID != model.AdapterID {
			return errors.New("invalid provider role mapping")
		}
		terminal, terminalErr := nativeTerminalForRole(role, selected, model, nativeSchemas[role])
		if terminalErr != nil {
			return terminalErr
		}
		if err := validateProviderRoleControls(endpoint, model, selected, terminal); err != nil {
			if errors.Is(err, providergateway.ErrCapabilityUnavailable) {
				return errors.Join(errors.New("invalid provider role mapping"), providergateway.ErrCapabilityUnavailable)
			}
			return errors.New("invalid provider role mapping")
		}
	}
	return nil
}

// ResolveRole returns validated public contracts and an environment variable
// name. The controller resolves the variable value only at dispatch time.
func (p Provider) ResolveRole(role string) (ResolvedProviderRole, error) {
	if err := p.Validate(); err != nil {
		return ResolvedProviderRole{}, err
	}
	return p.resolveRole(role)
}

func (p Provider) resolveRole(role string) (ResolvedProviderRole, error) {
	selected, ok := p.Roles[role]
	if !ok {
		return ResolvedProviderRole{}, errors.New("provider role is not configured")
	}
	var endpoint providergateway.EndpointContract
	for _, value := range p.Endpoints {
		if value.Name == selected.Endpoint {
			endpoint, _ = value.Contract()
		}
	}
	var model providergateway.ModelContract
	for _, value := range p.Models {
		if value.Name == selected.Model {
			model, _ = value.Contract()
		}
	}
	environment := ""
	for _, value := range p.Credentials {
		if value.Ref == endpoint.Auth.CredentialRef {
			environment = value.Environment
		}
	}
	return ResolvedProviderRole{
		Endpoint: endpoint, Model: model,
		AdapterControls:       json.RawMessage(selected.AdapterControlsJSON),
		Variant:               selected.Variant,
		RequiredCapabilities:  selected.RequiredCapabilities.gateway(),
		CredentialEnvironment: environment,
		ResponseFraming:       selected.ResponseFraming,
	}, nil
}

// RoleReservation exposes the exact conservative ceilings used by access
// configuration integration.
func (p Provider) RoleReservation(role string) (int64, *int64, error) {
	resolved, err := p.ResolveRole(role)
	if err != nil {
		return 0, nil, err
	}
	return resolved.Model.ConservativeReservation()
}

// ValidateRoles binds provider roles to runtime profiles and their exact access
// role-to-profile mapping, including per-invocation conservative reservations.
func (p Provider) ValidateRoles(profiles map[string]runtime.Profile, configuredAccess *Access) error {
	return p.validateRoles(profiles, configuredAccess, nil)
}

// ValidateRolesWithNativeWriterOutput is the narrow configuration-v2 path for
// an explicitly enabled OpenCode writer/fixer terminal. The schema map must
// contain only those active roles; all other provider roles use generic rules.
func (p Provider) ValidateRolesWithNativeWriterOutput(profiles map[string]runtime.Profile, configuredAccess *Access, schemas map[string]json.RawMessage) error {
	return p.validateRoles(profiles, configuredAccess, schemas)
}

func (p Provider) validateRoles(profiles map[string]runtime.Profile, configuredAccess *Access, nativeSchemas map[string]json.RawMessage) error {
	if err := p.validate(nativeSchemas); err != nil || configuredAccess == nil {
		if errors.Is(err, providergateway.ErrCapabilityUnavailable) {
			return err
		}
		return errors.New("valid provider and access configuration required")
	}
	accessProfiles := map[string]access.Profile{}
	for _, profile := range configuredAccess.Profiles {
		if _, exists := accessProfiles[profile.Name]; exists {
			return errors.New("duplicate access profile name")
		}
		accessProfiles[profile.Name] = profile
	}
	for role, profile := range profiles {
		selected, exists := p.Roles[role]
		if err := profile.Validate(); err != nil {
			return err
		}
		if profile.Runtime != "opencode-http" && profile.Runtime != "provider-api" {
			if exists {
				return errors.New("provider mapping assigned to another runtime")
			}
			continue
		}
		if !exists || profile.Role != role {
			return errors.New("provider role mapping missing")
		}
		if profile.Runtime == "opencode-http" && !engorchProviderID(profile.Provider) || profile.Runtime == "provider-api" && strings.HasPrefix(profile.Provider, "engorch-") {
			return errors.New("provider identity is incompatible with selected runtime")
		}
		resolved, err := p.resolveRole(role)
		if err == nil {
			required := resolved.RequiredCapabilities
			framing, framingErr := providergateway.EffectiveResponseFraming(resolved.ResponseFraming)
			if framingErr != nil || profile.Runtime == "opencode-http" && framing != providergateway.ResponseFramingSSE {
				return errors.New("provider response framing is incompatible with selected runtime")
			}
			if profile.Runtime == "provider-api" && (required.Tools || required.StructuredOutput == providergateway.StructuredOutputUnsupported) {
				return errors.New("direct provider runtime requires tool-free structured output")
			}
			if profile.Runtime == "opencode-http" && (!required.Tools || required.StructuredOutput != providergateway.StructuredOutputUnsupported) {
				return errors.New("OpenCode runtime requires provider tools and unstructured final output")
			}
		}
		accessName, accessOK := configuredAccess.Roles[role]
		accessProfile, profileOK := accessProfiles[accessName]
		limit, limitOK := configuredAccess.Invocations[role]
		if err != nil || !accessOK || !profileOK || !limitOK || profile.Provider != resolved.Model.Provider || profile.Model != resolved.Model.Model || profile.Effort != resolved.Variant.Effort || accessProfile.Runtime != profile.Runtime || accessProfile.Provider != profile.Provider || accessProfile.CredentialRef != resolved.Endpoint.Auth.CredentialRef {
			return errors.New("provider runtime, access or upstream identity mismatch")
		}
		tokens, cost, err := resolved.Model.ConservativeReservation()
		if err != nil || limit.UnlimitedTokens && limit.Tokens != 0 || !limit.UnlimitedTokens && limit.Tokens < tokens || !coversCost(limit.CostMicroUSD, cost, accessProfile.Kind) {
			return errors.New("provider role reservation is insufficient")
		}
		_ = selected
	}
	for role := range p.Roles {
		if _, ok := profiles[role]; !ok {
			return errors.New("provider mapping names an unconfigured role")
		}
	}
	return nil
}

func validateProviderRoleControls(endpoint providergateway.EndpointContract, model providergateway.ModelContract, role ProviderRole, terminal *providergateway.TerminalStructuredOutputExpectation) error {
	controls := json.RawMessage(role.AdapterControlsJSON)
	reserved, _, err := model.ConservativeReservation()
	if err != nil {
		return err
	}
	endpointID, _ := endpoint.ID()
	modelID, _ := model.ID()
	binding := providergateway.Binding{Version: 1, AccessPolicyID: strings.Repeat("0", 64), AccessInvocationID: strings.Repeat("1", 64), RouteID: strings.Repeat("2", 64), ReservedTokens: reserved, EndpointID: endpointID, ModelID: modelID, Endpoint: endpoint, Model: model}
	if role.RequiredCapabilities == nil {
		return errors.New("explicit provider role capabilities required")
	}
	if _, err := providergateway.AdapterRequestExpectationID(binding, providergateway.AdapterRequestExpectation{MaxBytes: model.MaxRequestBytes, MaxOutputTokens: model.MaxOutputTokens, Controls: controls, RequiredCapabilities: role.RequiredCapabilities.gateway(), ResponseFraming: role.ResponseFraming, TerminalStructuredOutput: terminal}); err != nil {
		return err
	}
	if !providerIdentifier(role.Variant.Effort) {
		return errors.New("explicit provider runtime effort required")
	}
	switch model.AdapterID {
	case providergateway.OpenAIChatCompletionsAdapter:
		if role.Variant != (ProviderVariant{Effort: "none"}) {
			return errors.New("chat adapter has no configured variant controls")
		}
	case providergateway.OpenAIResponsesAdapter:
		var expected providergateway.ResponsesRequestExpectation
		decoder := json.NewDecoder(strings.NewReader(role.AdapterControlsJSON))
		decoder.DisallowUnknownFields()
		decoder.UseNumber()
		if decoder.Decode(&expected) != nil {
			return errors.New("invalid Responses role controls")
		}
		effort := expected.ReasoningEffort
		if effort == "" {
			effort = "none"
		}
		if role.Variant != (ProviderVariant{Effort: effort, SystemRole: expected.SystemRole, ReasoningSummary: expected.ReasoningSummary, TextFormat: expected.TextFormat, TextVerbosity: expected.TextVerbosity}) {
			return errors.New("Responses controls differ from runtime variant")
		}
	case providergateway.AnthropicMessagesAdapter:
		var expected providergateway.AnthropicMessagesRequestExpectation
		decoder := json.NewDecoder(strings.NewReader(role.AdapterControlsJSON))
		decoder.DisallowUnknownFields()
		decoder.UseNumber()
		if decoder.Decode(&expected) != nil {
			return errors.New("invalid Anthropic role controls")
		}
		effort := expected.Effort
		if effort == "" {
			effort = "none"
		}
		systemRole := ""
		if expected.RequireSystem {
			systemRole = "system"
		}
		if role.Variant.Effort != effort || role.Variant.SystemRole != systemRole || role.Variant.ThinkingMode != expected.ThinkingMode || !equalOptionalInt64(role.Variant.ThinkingBudgetTokens, expected.ThinkingBudgetTokens) || role.Variant.ReasoningSummary != "" || role.Variant.TextFormat != "" || role.Variant.TextVerbosity != "" {
			return errors.New("Anthropic controls differ from runtime variant")
		}
	default:
		return errors.New("provider adapter is not executable")
	}
	return nil
}

func nativeTerminalForRole(role string, configured ProviderRole, model providergateway.ModelContract, schema json.RawMessage) (*providergateway.TerminalStructuredOutputExpectation, error) {
	if len(schema) == 0 {
		return nil, nil
	}
	if role != "writer" && role != "fixer" || model.AdapterID != providergateway.OpenAIResponsesAdapter || model.Capabilities == nil || !model.Capabilities.Tools || configured.RequiredCapabilities == nil || !configured.RequiredCapabilities.Tools {
		return nil, errors.New("native structured output requires Responses writer/fixer tools")
	}
	var controls providergateway.ResponsesRequestExpectation
	if json.Unmarshal([]byte(configured.AdapterControlsJSON), &controls) != nil || controls.ToolChoice != "required" && controls.ToolChoice != "auto" {
		return nil, errors.New("native structured output requires Responses tool_choice auto or required")
	}
	digest := sha256.Sum256(schema)
	terminal := &providergateway.TerminalStructuredOutputExpectation{Version: 1, Name: providergateway.StructuredOutputToolName, Schema: append(json.RawMessage(nil), schema...), SchemaSHA256: hex.EncodeToString(digest[:])}
	if err := terminal.Validate(); err != nil {
		return nil, err
	}
	return terminal, nil
}

func equalOptionalInt64(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func coversCost(limit, required *int64, kind string) bool {
	if kind == "subscription" {
		return limit == nil
	}
	return kind == "api" && limit != nil && required != nil && *limit >= *required
}

func providerIdentifier(value string) bool {
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

func engorchProviderID(value string) bool {
	return strings.HasPrefix(value, "engorch-") && len(value) > len("engorch-") && providerIdentifier(value)
}

func providerRole(value string) bool {
	return value == "planner" || value == "writer" || value == "fixer" || value == "explorer" || value == "reviewer"
}

func environmentName(value string) bool {
	if value == "" || len(value) > 128 || !(value[0] == '_' || value[0] >= 'A' && value[0] <= 'Z' || value[0] >= 'a' && value[0] <= 'z') {
		return false
	}
	for _, c := range value[1:] {
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func cloneStrings(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
