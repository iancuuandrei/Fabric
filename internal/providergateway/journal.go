package providergateway

import (
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
)

const (
	boundEvent                = "provider.bound"
	intentEvent               = "provider.call-intent"
	receiptEvent              = "provider.call-receipt"
	maxExact                  = int64(9007199254740991)
	providerReservationReason = "model_contract_conservative_maximum"
)

// EndpointContract is an operator-selected upstream identity. Recording it
// neither makes the URL reachable nor authorizes network access.
type EndpointContract struct {
	Version       int               `json:"version"`
	Provider      string            `json:"provider"`
	URL           string            `json:"url"`
	Protocol      string            `json:"protocol,omitempty"`
	AdapterID     string            `json:"adapter_id,omitempty"`
	Auth          *AuthContract     `json:"auth,omitempty"`
	PublicHeaders map[string]string `json:"public_headers,omitempty"`
	SessionHeader string            `json:"session_header,omitempty"`
}

// ID validates and hashes the exact upstream endpoint contract.
func (e EndpointContract) ID() (string, error) {
	if e.Version == 2 {
		if err := validateEndpointContractV2(e); err != nil {
			return "", err
		}
		return canonical.Hash("harness.provider-endpoint.v2", e)
	}
	parsed, err := url.Parse(e.URL)
	if err != nil || e.Version != 1 || e.AdapterID != "" || e.Auth != nil || e.PublicHeaders != nil || e.SessionHeader != "" || !identifier(e.Provider) || e.Protocol != "openai-chat-completions-sse-v1" || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" || parsed.Path != "/v1/chat/completions" || parsed.RawPath != "" || parsed.String() != e.URL {
		return "", errors.New("invalid provider endpoint contract")
	}
	return canonical.Hash("harness.provider-endpoint.v1", e)
}

// ModelContract retains every limit needed to replay the subcall state machine.
// These are controller policy declarations, not evidence a provider enforces them.
type ModelContract struct {
	Version              int                `json:"version"`
	Provider             string             `json:"provider"`
	Model                string             `json:"model"`
	Protocol             string             `json:"protocol,omitempty"`
	AdapterID            string             `json:"adapter_id,omitempty"`
	Capabilities         *ModelCapabilities `json:"capabilities,omitempty"`
	AdapterCapabilities  json.RawMessage    `json:"adapter_capabilities,omitempty"`
	ObservedModelAliases []string           `json:"observed_model_aliases,omitempty"`
	ContextWindowTokens  int64              `json:"context_window_tokens,omitempty"`
	MaxCalls             int                `json:"max_calls"`
	MaxRequestBytes      int64              `json:"max_request_bytes"`
	MaxResponseBytes     int64              `json:"max_response_bytes"`
	MaxOutputTokens      int64              `json:"max_output_tokens"`
	Pricing              *PricingPolicy     `json:"pricing,omitempty"`
}

// ID validates and hashes the exact model limits, capabilities and pricing.
func (m ModelContract) ID() (string, error) {
	if m.Version == 2 {
		if err := validateModelContractV2(m); err != nil {
			return "", err
		}
		return canonical.Hash("harness.provider-model.v2", m)
	}
	if m.Version != 1 || m.AdapterID != "" || m.Capabilities != nil || len(m.AdapterCapabilities) != 0 || m.ObservedModelAliases != nil || m.ContextWindowTokens != 0 || m.Pricing != nil || !identifier(m.Provider) || !printable(m.Model, 256) || m.Protocol != "openai-chat-completions-sse-v1" || m.MaxCalls < 1 || m.MaxCalls > 64 || m.MaxRequestBytes < 2 || m.MaxRequestBytes > 1<<20 || m.MaxResponseBytes < 1 || m.MaxResponseBytes > 8<<20 || m.MaxOutputTokens < 1 || m.MaxOutputTokens > maxExact {
		return "", errors.New("invalid provider model contract")
	}
	return canonical.Hash("harness.provider-model.v1", m)
}

// EngOrchBudgetBinding makes an unlimited EngOrch accounting admission explicit.
// A nil value retains the historical finite-admission representation.
type EngOrchBudgetBinding struct {
	Mode string `json:"mode"`
}

// ProviderReservation is a technical hard bound derived from the exact model
// contract. It is neither an EngOrch accounting ceiling nor an invented default.
type ProviderReservation struct {
	Tokens    int64  `json:"tokens"`
	Reason    string `json:"reason"`
	HardLimit bool   `json:"hard_limit"`
}

// Binding fixes one active model-access admission to exact endpoint and model
// contracts. The full contracts are retained so journal replay is self-contained.
// ReservedTokens keeps its historical meaning and encoding for finite admission;
// unlimited admission uses the two explicit optional projections instead.
type Binding struct {
	Version             int                   `json:"version"`
	AccessPolicyID      string                `json:"access_policy_id"`
	AccessInvocationID  string                `json:"access_invocation_id"`
	RouteID             string                `json:"route_id"`
	ReservedTokens      int64                 `json:"reserved_tokens"`
	EngOrchBudget       *EngOrchBudgetBinding `json:"engorch_budget,omitempty"`
	ProviderReservation *ProviderReservation  `json:"provider_reservation,omitempty"`
	EndpointID          string                `json:"endpoint_id"`
	ModelID             string                `json:"model_id"`
	Endpoint            EndpointContract      `json:"endpoint"`
	Model               ModelContract         `json:"model"`
}

// ID validates and hashes the admitted access, endpoint and model binding.
func (b Binding) ID() (string, error) {
	endpointID, endpointErr := b.Endpoint.ID()
	modelID, modelErr := b.Model.ID()
	if b.Version != 1 || safepath.RequireDigest(b.AccessPolicyID) != nil || safepath.RequireDigest(b.AccessInvocationID) != nil || safepath.RequireDigest(b.RouteID) != nil || !b.validTokenBinding() || endpointErr != nil || modelErr != nil || b.EndpointID != endpointID || b.ModelID != modelID || b.Endpoint.Provider != b.Model.Provider || !matchingContractProtocol(b.Endpoint, b.Model) {
		return "", errors.New("invalid provider gateway binding")
	}
	return canonical.Hash("harness.provider-gateway-binding.v1", b)
}

func (b Binding) validTokenBinding() bool {
	if b.EngOrchBudget == nil && b.ProviderReservation == nil {
		return b.ReservedTokens >= 1 && b.ReservedTokens <= maxExact
	}
	if b.ReservedTokens != 0 || b.EngOrchBudget == nil || b.EngOrchBudget.Mode != "unlimited" || b.ProviderReservation == nil || !b.ProviderReservation.HardLimit || b.ProviderReservation.Reason != providerReservationReason {
		return false
	}
	tokens, _, err := b.Model.ConservativeReservation()
	return err == nil && b.ProviderReservation.Tokens == tokens
}

// ProviderTokenLimit returns the validated numeric limit required by provider
// request and response protocols without reclassifying it as an EngOrch budget.
func ProviderTokenLimit(binding Binding) (int64, error) {
	if _, err := binding.ID(); err != nil {
		return 0, err
	}
	return providerTokenLimit(binding), nil
}

func providerTokenLimit(binding Binding) int64 {
	if binding.ProviderReservation != nil {
		return binding.ProviderReservation.Tokens
	}
	return binding.ReservedTokens
}

// Usage retains inclusive provider input/output totals. Optional detail fields
// are subsets of those totals and are never added to them for budget accounting.
type Usage struct {
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	ReasoningTokens  *int64 `json:"reasoning_tokens"`
	CacheReadTokens  *int64 `json:"cache_read_tokens"`
	CacheWriteTokens *int64 `json:"cache_write_tokens"`
}

// CallIntent is persisted before a caller may perform one provider request.
// It contains the exact request digest and size, never the request body.
type CallIntent struct {
	Version              int    `json:"version"`
	Sequence             int    `json:"sequence"`
	BindingID            string `json:"binding_id"`
	InvocationID         string `json:"invocation_id"`
	RouteID              string `json:"route_id"`
	EndpointID           string `json:"endpoint_id"`
	ModelID              string `json:"model_id"`
	RequestSHA256        string `json:"request_sha256"`
	RequestBytes         int64  `json:"request_bytes"`
	MaxOutputTokens      int64  `json:"max_output_tokens"`
	RequestExpectationID string `json:"request_expectation_id,omitempty"`
	// TerminalStructuredOutput is copied from the admitted request expectation
	// so replay can bind a terminal semantic marker to durable authority. It is
	// omitted for legacy calls and ordinary v2 calls without native output.
	TerminalStructuredOutput *TerminalStructuredOutputExpectation `json:"terminal_structured_output,omitempty"`
	CallID                   string                               `json:"call_id"`
}

// ID validates and hashes a call intent without its derived CallID.
func (c CallIntent) ID() (string, error) {
	if c.Version != 1 || c.Sequence < 1 || c.Sequence > 64 || safepath.RequireDigest(c.BindingID) != nil || safepath.RequireDigest(c.InvocationID) != nil || safepath.RequireDigest(c.RouteID) != nil || safepath.RequireDigest(c.EndpointID) != nil || safepath.RequireDigest(c.ModelID) != nil || safepath.RequireDigest(c.RequestSHA256) != nil || c.RequestExpectationID != "" && safepath.RequireDigest(c.RequestExpectationID) != nil || c.RequestBytes < 2 || c.RequestBytes > 1<<20 || c.MaxOutputTokens < 1 || c.MaxOutputTokens > maxExact || c.TerminalStructuredOutput != nil && c.TerminalStructuredOutput.Validate() != nil {
		return "", errors.New("invalid provider call intent")
	}
	c.CallID = ""
	return canonical.Hash("harness.provider-call.v1", c)
}

// CallReceipt records a fully buffered and validated streaming observation.
// UsageComplete distinguishes real zero usage from missing provider accounting.
type CallReceipt struct {
	Version              int                         `json:"version"`
	BindingID            string                      `json:"binding_id"`
	InvocationID         string                      `json:"invocation_id"`
	CallID               string                      `json:"call_id"`
	ResponseSHA256       string                      `json:"response_sha256"`
	ResponseBytes        int64                       `json:"response_bytes"`
	ResponseID           string                      `json:"response_id"`
	ObservedModel        string                      `json:"observed_model"`
	Finish               string                      `json:"finish"`
	StreamComplete       bool                        `json:"stream_complete"`
	UsageComplete        bool                        `json:"usage_complete"`
	Usage                Usage                       `json:"usage"`
	Semantic             *ResponseSemanticProjection `json:"semantic,omitempty"`
	RequestExpectationID string                      `json:"request_expectation_id,omitempty"`
}

// Call pairs an intent with its optional terminal observation.
type Call struct {
	Intent  CallIntent          `json:"intent"`
	Receipt *CallReceipt        `json:"receipt,omitempty"`
	Failure *FailureObservation `json:"failure,omitempty"`
}

// State is reconstructed only from the fully validated journal.
type State struct {
	Binding   *Binding    `json:"binding,omitempty"`
	Calls     []Call      `json:"calls"`
	Pending   *CallIntent `json:"pending,omitempty"`
	Aggregate Usage       `json:"aggregate"`
	Finished  bool        `json:"finished"`
	Exhausted bool        `json:"exhausted"`
}

// Bind creates the immutable gateway binding after validating the exact access
// policy and intent. It performs no provider or other network operation.
func Bind(path, accessPath string, policy access.Policy, intent access.Intent, endpoint EndpointContract, model ModelContract) (Binding, error) {
	binding, err := BindingForAccess(policy, intent, endpoint, model)
	if err != nil {
		return Binding{}, err
	}
	if err := access.RequireActive(accessPath, policy, intent); err != nil {
		return Binding{}, err
	}
	if _, err := journal.Append(path, boundEvent, binding, func(events []journal.Event) error {
		_, err := replay(events)
		return err
	}); err != nil {
		return Binding{}, err
	}
	return binding, nil
}

// Begin persists one subcall intent after a fresh active-access check. A pending
// call is delivery/effect UNKNOWN and blocks every later call. The returned
// record is evidence only; this package exposes no API capable of network egress.
func Begin(path, accessPath string, policy access.Policy, intent access.Intent, binding Binding, requestSHA256 string, requestBytes, maxOutputTokens int64) (CallIntent, error) {
	if binding.Model.Version != 1 {
		return CallIntent{}, errors.New("provider v2 call requires a bound request expectation")
	}
	return begin(path, accessPath, policy, intent, binding, requestSHA256, requestBytes, maxOutputTokens, "", nil)
}

// BeginWithExpectation validates and durably binds the complete request
// expectation before a v2 provider call may leave the controller.
func BeginWithExpectation(path, accessPath string, policy access.Policy, intent access.Intent, binding Binding, requestSHA256 string, requestBytes int64, expected AdapterRequestExpectation) (CallIntent, error) {
	if binding.Model.Version != 2 {
		return CallIntent{}, errors.New("provider expectation API requires a v2 model contract")
	}
	expectationID, err := AdapterRequestExpectationID(binding, expected)
	if err != nil {
		return CallIntent{}, err
	}
	return begin(path, accessPath, policy, intent, binding, requestSHA256, requestBytes, expected.MaxOutputTokens, expectationID, expected.TerminalStructuredOutput)
}

func begin(path, accessPath string, policy access.Policy, intent access.Intent, binding Binding, requestSHA256 string, requestBytes, maxOutputTokens int64, expectationID string, terminal *TerminalStructuredOutputExpectation) (CallIntent, error) {
	expected, err := BindingForAccess(policy, intent, binding.Endpoint, binding.Model)
	if err != nil || !reflect.DeepEqual(expected, binding) {
		return CallIntent{}, errors.New("provider gateway binding differs from admission")
	}
	if err := access.RequireActive(accessPath, policy, intent); err != nil {
		return CallIntent{}, err
	}
	state, err := Inspect(path)
	if err != nil {
		return CallIntent{}, err
	}
	if state.Binding == nil || !reflect.DeepEqual(*state.Binding, binding) {
		return CallIntent{}, errors.New("provider gateway journal binding changed")
	}
	bindingID, _ := binding.ID()
	call := CallIntent{Version: 1, Sequence: len(state.Calls) + 1, BindingID: bindingID, InvocationID: binding.AccessInvocationID, RouteID: binding.RouteID, EndpointID: binding.EndpointID, ModelID: binding.ModelID, RequestSHA256: requestSHA256, RequestBytes: requestBytes, MaxOutputTokens: maxOutputTokens, RequestExpectationID: expectationID, TerminalStructuredOutput: cloneTerminalStructuredOutputExpectation(terminal)}
	call.CallID, err = call.ID()
	if err != nil {
		return CallIntent{}, err
	}
	if _, err := journal.Append(path, intentEvent, call, func(events []journal.Event) error {
		next, err := replay(events)
		if err != nil {
			return err
		}
		if next.Binding == nil || !reflect.DeepEqual(*next.Binding, binding) {
			return errors.New("provider gateway journal binding changed")
		}
		return nil
	}); err != nil {
		return CallIntent{}, err
	}
	return call, nil
}

func cloneTerminalStructuredOutputExpectation(source *TerminalStructuredOutputExpectation) *TerminalStructuredOutputExpectation {
	if source == nil {
		return nil
	}
	result := *source
	result.Schema = append([]byte(nil), source.Schema...)
	return &result
}

// validateReceiptTerminalBinding ties the semantic closure marker to the
// exact native output contract persisted in the pending call intent. Ordinary
// tool calls remain valid while a structured turn is still in progress.
func validateReceiptTerminalBinding(call CallIntent, receipt CallReceipt) error {
	marker := receipt.Semantic != nil && receipt.Semantic.TerminalTool != nil
	if call.TerminalStructuredOutput == nil {
		if marker {
			return errors.New("terminal receipt lacks an admitted structured output expectation")
		}
		return nil
	}
	if marker {
		if receipt.Semantic.TerminalSchemaSHA256 != call.TerminalStructuredOutput.SchemaSHA256 || receipt.Semantic.TerminalTool.Name != call.TerminalStructuredOutput.Name {
			return errors.New("terminal receipt differs from admitted structured output expectation")
		}
		return nil
	}
	if receipt.Finish == "stop" {
		return errors.New("structured output expectation lacks terminal capture")
	}
	return nil
}

// Complete durably records one already-observed response. Active access is
// required again so an unexpected owner terminalization cannot admit a late
// response. Failures leave the call pending and do not release access budget.
func Complete(path, accessPath string, policy access.Policy, intent access.Intent, binding Binding, call CallIntent, receipt CallReceipt) error {
	expected, err := BindingForAccess(policy, intent, binding.Endpoint, binding.Model)
	if err != nil || !reflect.DeepEqual(expected, binding) {
		return errors.New("provider gateway binding differs from admission")
	}
	if err := access.RequireActive(accessPath, policy, intent); err != nil {
		return err
	}
	_, err = journal.Append(path, receiptEvent, receipt, func(events []journal.Event) error {
		state, err := replay(events)
		if err != nil {
			return err
		}
		if state.Binding == nil || !reflect.DeepEqual(*state.Binding, binding) || len(state.Calls) == 0 || state.Calls[len(state.Calls)-1].Receipt == nil || !reflect.DeepEqual(state.Calls[len(state.Calls)-1].Intent, call) {
			return errors.New("provider receipt differs from pending call")
		}
		return nil
	})
	return err
}

// Inspect validates the complete journal without granting access or egress.
func Inspect(path string) (State, error) {
	events, err := journal.Read(path)
	if err != nil {
		return State{}, err
	}
	return replay(events)
}

func replay(events []journal.Event) (State, error) {
	state := State{Calls: []Call{}}
	digests := map[string]bool{}
	for _, event := range events {
		switch event.Kind {
		case boundEvent:
			if state.Binding != nil || len(state.Calls) != 0 {
				return State{}, errors.New("provider gateway already bound")
			}
			var binding Binding
			if canonical.Decode(event.Payload, &binding) != nil {
				return State{}, errors.New("invalid provider gateway binding event")
			}
			if _, err := binding.ID(); err != nil {
				return State{}, err
			}
			state.Binding = &binding
		case intentEvent:
			if state.Binding == nil || state.Pending != nil || state.Finished || len(state.Calls) >= state.Binding.Model.MaxCalls {
				return State{}, errors.New("provider call intent transition rejected")
			}
			var call CallIntent
			if canonical.Decode(event.Payload, &call) != nil {
				return State{}, errors.New("invalid provider call intent event")
			}
			bindingID, _ := state.Binding.ID()
			callID, err := call.ID()
			tokenLimit := providerTokenLimit(*state.Binding)
			remaining := tokenLimit - state.Aggregate.InputTokens - state.Aggregate.OutputTokens
			expectationBound := state.Binding.Model.Version == 1 && call.RequestExpectationID == "" || state.Binding.Model.Version == 2 && call.RequestExpectationID != ""
			terminalValid := call.TerminalStructuredOutput == nil
			if call.TerminalStructuredOutput != nil {
				terminalValid = state.Binding.Model.Version == 2 && state.Binding.Model.AdapterID == OpenAIResponsesAdapter && call.TerminalStructuredOutput.Validate() == nil
			}
			if err != nil || !expectationBound || !terminalValid || call.CallID != callID || call.Sequence != len(state.Calls)+1 || call.BindingID != bindingID || call.InvocationID != state.Binding.AccessInvocationID || call.RouteID != state.Binding.RouteID || call.EndpointID != state.Binding.EndpointID || call.ModelID != state.Binding.ModelID || call.RequestBytes > state.Binding.Model.MaxRequestBytes || call.MaxOutputTokens > state.Binding.Model.MaxOutputTokens || remaining <= 0 || call.MaxOutputTokens > remaining || digests[call.RequestSHA256] {
				return State{}, errors.New("provider call intent differs from binding")
			}
			digests[call.RequestSHA256] = true
			state.Calls = append(state.Calls, Call{Intent: call})
			pending := call
			state.Pending = &pending
		case failureEvent:
			if err := replayFailure(&state, event.Payload); err != nil {
				return State{}, err
			}
		case receiptEvent:
			if state.Binding == nil || state.Pending == nil || len(state.Calls) == 0 {
				return State{}, errors.New("provider receipt without pending call")
			}
			var receipt CallReceipt
			if canonical.Decode(event.Payload, &receipt) != nil {
				return State{}, errors.New("invalid provider call receipt event")
			}
			bindingID, _ := state.Binding.ID()
			expectationMatched := state.Binding.Model.Version == 1 && receipt.RequestExpectationID == "" || state.Binding.Model.Version == 2 && receipt.RequestExpectationID == state.Pending.RequestExpectationID
			if receipt.Version != 1 || receipt.BindingID != bindingID || receipt.InvocationID != state.Binding.AccessInvocationID || receipt.CallID != state.Pending.CallID || !expectationMatched || safepath.RequireDigest(receipt.ResponseSHA256) != nil || receipt.ResponseBytes < 1 || receipt.ResponseBytes > state.Binding.Model.MaxResponseBytes || !printable(receipt.ResponseID, 256) || !AcceptsObservedModel(state.Binding.Model, receipt.ObservedModel) || !receipt.StreamComplete || !receipt.UsageComplete || (receipt.Finish != "tool_calls" && receipt.Finish != "stop") || validateReceiptSemantic(state.Binding.Model.Version, receipt.Finish, receipt.Semantic) != nil || validateReceiptTerminalBinding(*state.Pending, receipt) != nil {
				return State{}, errors.New("provider call receipt differs from binding")
			}
			if err := validateUsage(receipt.Usage); err != nil || receipt.Usage.OutputTokens > state.Pending.MaxOutputTokens {
				if err == nil {
					err = errors.New("provider output usage exceeds call limit")
				}
				return State{}, err
			}
			if state.Binding.Model.Version == 2 && receipt.Usage.InputTokens > state.Binding.Model.ContextWindowTokens {
				return State{}, errors.New("provider input usage exceeds model context contract")
			}
			aggregate, err := addUsage(state.Aggregate, receipt.Usage, len(state.Calls) == 1)
			if err != nil {
				return State{}, err
			}
			if aggregate.InputTokens > providerTokenLimit(*state.Binding)-aggregate.OutputTokens {
				if state.Binding.EngOrchBudget != nil {
					return State{}, errors.New("provider usage exceeds technical model-contract reservation")
				}
				return State{}, errors.New("provider usage exceeds access reservation")
			}
			copy := receipt
			copy.Usage = cloneUsage(copy.Usage)
			state.Calls[len(state.Calls)-1].Receipt = &copy
			state.Pending = nil
			state.Aggregate = aggregate
			terminal := receipt.Semantic != nil && receipt.Semantic.TerminalTool != nil
			// A native StructuredOutput tool call is a terminal semantic capture;
			// preserve the provider's actual tool_calls finish while closing the
			// gateway. It must not be reported as ordinary call exhaustion.
			state.Finished = receipt.Finish == "stop" || terminal
			state.Exhausted = receipt.Finish == "tool_calls" && !terminal && len(state.Calls) >= state.Binding.Model.MaxCalls
		default:
			return State{}, errors.New("unknown provider gateway event")
		}
	}
	return cloneState(state), nil
}

// BindingForAccess derives the exact provider binding from validated access and
// model contracts. Callers must not reconstruct technical reservations.
func BindingForAccess(policy access.Policy, intent access.Intent, endpoint EndpointContract, model ModelContract) (Binding, error) {
	return bindingFor(policy, intent, endpoint, model)
}

func bindingFor(policy access.Policy, intent access.Intent, endpoint EndpointContract, model ModelContract) (Binding, error) {
	policyID, err := policy.ID()
	if err != nil || intent.PolicyID != policyID {
		return Binding{}, errors.New("provider access policy mismatch")
	}
	invocationID, err := intent.ID()
	if err != nil || invocationID != intent.Reservation.InvocationID {
		return Binding{}, errors.New("provider API admission mismatch")
	}
	routeID, err := intent.Route.ID()
	if err != nil {
		return Binding{}, err
	}
	endpointID, err := endpoint.ID()
	if err != nil {
		return Binding{}, err
	}
	modelID, err := model.ID()
	if err != nil {
		return Binding{}, err
	}
	if endpoint.Provider != intent.Route.Provider || model.Provider != intent.Route.Provider || model.Model != intent.Route.Model || !matchingContractProtocol(endpoint, model) {
		return Binding{}, errors.New("provider contracts differ from route")
	}
	if endpoint.Version == 2 {
		profile, profileErr := profileForGatewayRoute(policy, intent.Route.AccessID)
		if profileErr != nil || endpoint.Auth == nil || profile.Provider != intent.Route.Provider || profile.Runtime != intent.Route.Runtime || profile.Kind != intent.Reservation.BillingMode || profile.CredentialRef == "" || endpoint.Auth.CredentialRef != profile.CredentialRef {
			return Binding{}, errors.New("provider credential admission mismatch")
		}
		adapter, ok := protocolAdapterFor(endpoint.AdapterID)
		if !ok || !adapter.implemented() {
			return Binding{}, errors.New("provider protocol adapter is not executable")
		}
		reservedTokens, reservedCost, reserveErr := model.ConservativeReservation()
		if reserveErr != nil || !intent.Reservation.UnlimitedTokens && reservedTokens > intent.Reservation.Tokens {
			return Binding{}, errors.New("provider token reservation does not cover model contract")
		}
		if profile.Kind == "api" {
			if intent.Reservation.CostMicroUSD == nil || reservedCost == nil || *reservedCost > *intent.Reservation.CostMicroUSD {
				return Binding{}, errors.New("provider cost reservation does not cover model pricing")
			}
		} else if profile.Kind != "subscription" {
			return Binding{}, errors.New("unsupported provider billing profile")
		}
	} else if intent.Reservation.BillingMode != "api" {
		return Binding{}, errors.New("legacy provider gateway requires API billing")
	} else if intent.Reservation.UnlimitedTokens {
		return Binding{}, errors.New("unlimited provider admission requires explicit model contract bounds")
	}
	binding := Binding{Version: 1, AccessPolicyID: policyID, AccessInvocationID: invocationID, RouteID: routeID, ReservedTokens: intent.Reservation.Tokens, EndpointID: endpointID, ModelID: modelID, Endpoint: endpoint, Model: model}
	if intent.Reservation.UnlimitedTokens {
		technicalTokens, _, err := model.ConservativeReservation()
		if err != nil {
			return Binding{}, errors.New("provider technical reservation unavailable")
		}
		binding.EngOrchBudget = &EngOrchBudgetBinding{Mode: "unlimited"}
		binding.ProviderReservation = &ProviderReservation{Tokens: technicalTokens, Reason: providerReservationReason, HardLimit: true}
	}
	if _, err := binding.ID(); err != nil {
		return Binding{}, err
	}
	return binding, nil
}

func validateUsage(u Usage) error {
	if u.InputTokens < 0 || u.InputTokens > maxExact || u.OutputTokens < 0 || u.OutputTokens > maxExact {
		return errors.New("invalid complete provider usage")
	}
	for _, detail := range []struct {
		value  *int64
		parent int64
	}{{u.ReasoningTokens, u.OutputTokens}, {u.CacheReadTokens, u.InputTokens}, {u.CacheWriteTokens, u.InputTokens}} {
		if detail.value != nil && (*detail.value < 0 || *detail.value > detail.parent) {
			return errors.New("invalid provider usage detail")
		}
	}
	return nil
}

func addUsage(total, next Usage, first bool) (Usage, error) {
	if total.InputTokens > maxExact-next.InputTokens || total.OutputTokens > maxExact-next.OutputTokens {
		return Usage{}, errors.New("provider usage aggregate overflow")
	}
	result := Usage{InputTokens: total.InputTokens + next.InputTokens, OutputTokens: total.OutputTokens + next.OutputTokens}
	result.ReasoningTokens = addOptional(total.ReasoningTokens, next.ReasoningTokens, first)
	result.CacheReadTokens = addOptional(total.CacheReadTokens, next.CacheReadTokens, first)
	result.CacheWriteTokens = addOptional(total.CacheWriteTokens, next.CacheWriteTokens, first)
	for _, value := range []*int64{result.ReasoningTokens, result.CacheReadTokens, result.CacheWriteTokens} {
		if value != nil && *value < 0 {
			return Usage{}, errors.New("provider usage detail aggregate overflow")
		}
	}
	return result, nil
}

func addOptional(total, next *int64, first bool) *int64 {
	if next == nil || (!first && total == nil) {
		return nil
	}
	base := int64(0)
	if total != nil {
		base = *total
	}
	if *next > maxExact-base {
		value := int64(-1)
		return &value
	}
	value := base + *next
	return &value
}

func cloneUsage(u Usage) Usage {
	u.ReasoningTokens = cloneOptional(u.ReasoningTokens)
	u.CacheReadTokens = cloneOptional(u.CacheReadTokens)
	u.CacheWriteTokens = cloneOptional(u.CacheWriteTokens)
	return u
}

func cloneOptional(source *int64) *int64 {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func cloneState(s State) State {
	if s.Binding != nil {
		binding := *s.Binding
		if binding.EngOrchBudget != nil {
			budget := *binding.EngOrchBudget
			binding.EngOrchBudget = &budget
		}
		if binding.ProviderReservation != nil {
			reservation := *binding.ProviderReservation
			binding.ProviderReservation = &reservation
		}
		s.Binding = &binding
	}
	if s.Pending != nil {
		pending := *s.Pending
		s.Pending = &pending
	}
	s.Calls = append([]Call(nil), s.Calls...)
	for index := range s.Calls {
		if s.Calls[index].Failure != nil {
			failure := *s.Calls[index].Failure
			s.Calls[index].Failure = &failure
		}
		if s.Calls[index].Receipt != nil {
			receipt := *s.Calls[index].Receipt
			receipt.Usage = cloneUsage(receipt.Usage)
			if receipt.Semantic != nil {
				semantic := *receipt.Semantic
				if semantic.ToolCalls != nil {
					semantic.ToolCalls = make([]ResponseToolIdentity, len(receipt.Semantic.ToolCalls))
					copy(semantic.ToolCalls, receipt.Semantic.ToolCalls)
				}
				receipt.Semantic = &semantic
			}
			s.Calls[index].Receipt = &receipt
		}
	}
	s.Aggregate = cloneUsage(s.Aggregate)
	return s
}

func identifier(value string) bool {
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

func printable(value string, maximum int) bool {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, c := range value {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}
