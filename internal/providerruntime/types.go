package providerruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/providercredential"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/providertransport"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/worktree"
)

const maximumPromptBytes = 256 << 10

var (
	// ErrRejected means the direct invocation was rejected before provider egress.
	ErrRejected = errors.New("provider runtime invocation rejected")
	// ErrUnresolved means durable evidence cannot prove a response payload that
	// is safe to return, and another provider call is forbidden.
	ErrUnresolved = errors.New("provider runtime invocation unresolved")
)

// OutputContract selects bounded local parsing after TEXT_PARSE_REQUIRED.
// Native JSON/schema guarantees and schema validation belong to the gateway.
type OutputContract struct {
	Kind string `json:"kind"`
}

func (o OutputContract) validate() error {
	if o.Kind != "json" {
		return errors.New("unsupported direct output contract")
	}
	return nil
}

// Invocation is the complete model-visible initial input and local output
// contract. It is retained in the runtime journal so recovery never rebuilds a
// request from a digest.
type Invocation struct {
	Version int            `json:"version"`
	System  string         `json:"system,omitempty"`
	Prompt  string         `json:"prompt"`
	Output  OutputContract `json:"output"`
}

// InputHash binds the complete direct input, request controls, resource caps,
// and structured-output requirement to model-access admission. Raw controls
// and schema are represented by exact byte digests so decimal lexemes survive.
func (i Invocation) InputHash(expectation providergateway.AdapterRequestExpectation) (string, error) {
	if err := i.validate(); err != nil {
		return "", err
	}
	if _, err := providergateway.EffectiveResponseFraming(expectation.ResponseFraming); err != nil || len(expectation.Tools) != 0 || !directStructuredRequirement(expectation.RequiredCapabilities) || !json.Valid(expectation.Controls) {
		return "", ErrRejected
	}
	controlsDigest := sha256.Sum256(expectation.Controls)
	schemaDigest := sha256.Sum256(expectation.RequiredCapabilities.Schema)
	identity := struct {
		Invocation       Invocation                           `json:"invocation"`
		MaxBytes         int64                                `json:"max_bytes"`
		MaxOutputTokens  int64                                `json:"max_output_tokens"`
		ControlsSHA256   string                               `json:"controls_sha256"`
		Tools            bool                                 `json:"tools"`
		Reasoning        bool                                 `json:"reasoning"`
		StructuredOutput providergateway.StructuredOutputMode `json:"structured_output"`
		SchemaName       string                               `json:"schema_name,omitempty"`
		SchemaSHA256     string                               `json:"schema_sha256"`
		ResponseFraming  providergateway.ResponseFraming      `json:"response_framing,omitempty"`
	}{i, expectation.MaxBytes, expectation.MaxOutputTokens, hex.EncodeToString(controlsDigest[:]), expectation.RequiredCapabilities.Tools, expectation.RequiredCapabilities.Reasoning, expectation.RequiredCapabilities.StructuredOutput, expectation.RequiredCapabilities.SchemaName, hex.EncodeToString(schemaDigest[:]), expectation.ResponseFraming}
	return canonical.Hash("harness.provider-runtime-input.v1", identity)
}

func (i Invocation) validate() error {
	if i.Version != 1 || i.Prompt == "" || !utf8.ValidString(i.Prompt) || !utf8.ValidString(i.System) || len(i.Prompt)+len(i.System) > maximumPromptBytes {
		return errors.New("invalid direct provider input")
	}
	return i.Output.validate()
}

// Config supplies controller-owned production dependencies and exact durable
// identities. WorkspaceLease and WorkspaceRequest must either both be nil or
// identify the exact candidate workspace held throughout egress and recording.
type Config struct {
	JournalPath        string
	AccessJournalPath  string
	GatewayJournalPath string
	Policy             access.Policy
	Intent             access.Intent
	Binding            providergateway.Binding
	Expectation        providergateway.AdapterRequestExpectation
	Credential         *providercredential.Lease
	Transport          *providertransport.Client
	WorkspaceLease     *worktree.Lease
	WorkspaceRequest   *worktree.Request
}

// InvocationRecord is the durable, secret-free identity and exact request for
// one direct call. RequestBody and control bytes are base64 encoded by the
// journal codec so fractional JSON lexemes remain byte exact.
type InvocationRecord struct {
	Version              int                        `json:"version"`
	InvocationID         string                     `json:"invocation_id"`
	RouteID              string                     `json:"route_id"`
	GatewayBindingID     string                     `json:"gateway_binding_id"`
	RequestExpectationID string                     `json:"request_expectation_id"`
	CredentialBindingID  string                     `json:"credential_binding_id"`
	WorkspaceLeaseID     string                     `json:"workspace_lease_id,omitempty"`
	AccessIntent         access.Intent              `json:"access_intent"`
	GatewayBinding       providergateway.Binding    `json:"gateway_binding"`
	CredentialBinding    providercredential.Binding `json:"credential_binding"`
	WorkspaceRequest     *worktree.Request          `json:"workspace_request,omitempty"`
	Invocation           Invocation                 `json:"invocation"`
	RequestBody          []byte                     `json:"request_body"`
	Expectation          requestExpectationRecord   `json:"expectation"`
}

type requestExpectationRecord struct {
	MaxBytes             int64                           `json:"max_bytes"`
	MaxOutputTokens      int64                           `json:"max_output_tokens"`
	Controls             []byte                          `json:"controls"`
	RequiredCapabilities *requiredCapabilitiesRecord     `json:"required_capabilities"`
	ResponseFraming      providergateway.ResponseFraming `json:"response_framing,omitempty"`
}

type requiredCapabilitiesRecord struct {
	Tools            bool                                 `json:"tools"`
	Reasoning        bool                                 `json:"reasoning"`
	StructuredOutput providergateway.StructuredOutputMode `json:"structured_output"`
	SchemaName       string                               `json:"schema_name,omitempty"`
	Schema           []byte                               `json:"schema"`
}

func expectationRecord(source providergateway.AdapterRequestExpectation) requestExpectationRecord {
	result := requestExpectationRecord{MaxBytes: source.MaxBytes, MaxOutputTokens: source.MaxOutputTokens, Controls: append([]byte(nil), source.Controls...), ResponseFraming: source.ResponseFraming}
	if source.RequiredCapabilities != nil {
		result.RequiredCapabilities = &requiredCapabilitiesRecord{
			Tools: source.RequiredCapabilities.Tools, Reasoning: source.RequiredCapabilities.Reasoning,
			StructuredOutput: source.RequiredCapabilities.StructuredOutput, SchemaName: source.RequiredCapabilities.SchemaName,
			Schema: append([]byte{}, source.RequiredCapabilities.Schema...),
		}
	}
	return result
}

func (r requestExpectationRecord) gatewayExpectation() providergateway.AdapterRequestExpectation {
	result := providergateway.AdapterRequestExpectation{MaxBytes: r.MaxBytes, MaxOutputTokens: r.MaxOutputTokens, Tools: []providergateway.RequestTool{}, Controls: append(json.RawMessage(nil), r.Controls...), ResponseFraming: r.ResponseFraming}
	if r.RequiredCapabilities != nil {
		result.RequiredCapabilities = &providergateway.RequiredCapabilities{
			Tools: r.RequiredCapabilities.Tools, Reasoning: r.RequiredCapabilities.Reasoning,
			StructuredOutput: r.RequiredCapabilities.StructuredOutput, SchemaName: r.RequiredCapabilities.SchemaName,
			Schema: append(json.RawMessage(nil), r.RequiredCapabilities.Schema...),
		}
	}
	return result
}

func bindingFor(config Config, invocation Invocation, body []byte) (InvocationRecord, error) {
	inputHash, err := invocation.InputHash(config.Expectation)
	if err != nil || inputHash != config.Intent.InputHash || config.Intent.Route.Runtime != "provider-api" {
		return InvocationRecord{}, ErrRejected
	}
	policyID, err := config.Policy.ID()
	if err != nil || policyID != config.Intent.PolicyID || policyID != config.Binding.AccessPolicyID {
		return InvocationRecord{}, ErrRejected
	}
	invocationID, err := config.Intent.ID()
	if err != nil || invocationID != config.Intent.Reservation.InvocationID || invocationID != config.Binding.AccessInvocationID {
		return InvocationRecord{}, ErrRejected
	}
	routeID, err := config.Intent.Route.ID()
	if err != nil || routeID != config.Binding.RouteID {
		return InvocationRecord{}, ErrRejected
	}
	gatewayID, err := config.Binding.ID()
	if err != nil {
		return InvocationRecord{}, ErrRejected
	}
	expectationID, err := providergateway.AdapterRequestExpectationID(config.Binding, config.Expectation)
	if err != nil || len(config.Expectation.Tools) != 0 || !directStructuredRequirement(config.Expectation.RequiredCapabilities) {
		return InvocationRecord{}, ErrRejected
	}
	if config.Credential == nil {
		return InvocationRecord{}, ErrRejected
	}
	credentialBinding := config.Credential.Binding()
	credentialID, err := credentialBinding.ID()
	if err != nil {
		return InvocationRecord{}, ErrRejected
	}
	workspaceID := ""
	if (config.WorkspaceLease == nil) != (config.WorkspaceRequest == nil) {
		return InvocationRecord{}, ErrRejected
	}
	if config.WorkspaceRequest != nil {
		identity := worktree.LeaseIdentity{Version: 1, Request: *config.WorkspaceRequest}
		workspaceID, err = identity.ID()
		if err != nil {
			return InvocationRecord{}, ErrRejected
		}
	}
	if !json.Valid(body) || int64(len(body)) > config.Expectation.MaxBytes {
		return InvocationRecord{}, ErrRejected
	}
	var workspaceRequest *worktree.Request
	if config.WorkspaceRequest != nil {
		copy := *config.WorkspaceRequest
		workspaceRequest = &copy
	}
	record := InvocationRecord{
		Version: 1, InvocationID: invocationID, RouteID: routeID,
		GatewayBindingID: gatewayID, RequestExpectationID: expectationID,
		CredentialBindingID: credentialID, WorkspaceLeaseID: workspaceID,
		AccessIntent: config.Intent, GatewayBinding: config.Binding,
		CredentialBinding: credentialBinding, WorkspaceRequest: workspaceRequest,
		Invocation: invocation, RequestBody: append([]byte(nil), body...),
		Expectation: expectationRecord(config.Expectation),
	}
	encoded, err := canonical.Bytes(record)
	if err != nil {
		return InvocationRecord{}, ErrRejected
	}
	var frozen InvocationRecord
	if err := canonical.Decode(encoded, &frozen); err != nil || validateBindingRecord(frozen) != nil {
		return InvocationRecord{}, ErrRejected
	}
	return frozen, nil
}

func directStructuredRequirement(required *providergateway.RequiredCapabilities) bool {
	if required == nil || required.Tools {
		return false
	}
	switch required.StructuredOutput {
	case providergateway.StructuredOutputTextParseRequired, providergateway.StructuredOutputJSONOnly, providergateway.StructuredOutputStrictSchema:
		return true
	default:
		return false
	}
}

func validDigest(value string) bool { return safepath.RequireDigest(value) == nil }

// Result is returned from exact journaled payload bytes. JSON preserves the
// provider's exact validated JSON text; JSONSHA256 binds those exact bytes.
type Result struct {
	Version    int                         `json:"version"`
	Text       string                      `json:"text"`
	JSON       string                      `json:"json"`
	JSONSHA256 string                      `json:"json_sha256"`
	Receipt    providergateway.CallReceipt `json:"receipt"`
}
