package providergateway

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"harness.local/engorch/internal/access"
)

type gatewayFixture struct {
	accessPath string
	path       string
	policy     access.Policy
	intent     access.Intent
	endpoint   EndpointContract
	model      ModelContract
	binding    Binding
}

func newGatewayFixture(t *testing.T, reserved int64) gatewayFixture {
	return newGatewayFixtureWithCalls(t, reserved, 2)
}

func newGatewayFixtureWithCalls(t *testing.T, reserved int64, maxCalls int) gatewayFixture {
	t.Helper()
	root := t.TempDir()
	cost := int64(1000)
	profile := access.Profile{Version: 1, Name: "provider-api", Kind: "api", Runtime: "opencode-http", Provider: "openai", CredentialRef: "provider-key", RepositoryClasses: []access.Class{access.Private}}
	profileID, err := profile.ID()
	if err != nil {
		t.Fatal(err)
	}
	route := access.Route{Version: 1, Role: "planner", Runtime: "opencode-http", Provider: "openai", Model: "qualified-model", Effort: "high", AccessID: profileID, Permission: "read-only"}
	policy := access.Policy{Version: 1, RunID: strings.Repeat("a", 64), Class: access.Private, Limits: access.Limits{Tokens: reserved, CostMicroUSD: &cost, Concurrency: 1}, Routes: []access.Route{route}, Profiles: []access.Profile{profile}}
	policyID, err := policy.ID()
	if err != nil {
		t.Fatal(err)
	}
	intentCost := int64(1000)
	intent := access.Intent{Attempt: 1, PolicyID: policyID, InputHash: strings.Repeat("b", 64), Route: route, Reservation: access.Reservation{Tokens: reserved, CostMicroUSD: &intentCost, BillingMode: "api"}}
	intent.Reservation.InvocationID, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	accessPath := filepath.Join(root, "access.jsonl")
	if err := access.ReserveDurable(accessPath, policy, intent); err != nil {
		t.Fatal(err)
	}
	endpoint := EndpointContract{Version: 1, Provider: "openai", URL: "https://api.example.test/v1/chat/completions", Protocol: "openai-chat-completions-sse-v1"}
	model := ModelContract{Version: 1, Provider: "openai", Model: "qualified-model", Protocol: endpoint.Protocol, MaxCalls: maxCalls, MaxRequestBytes: 4096, MaxResponseBytes: 8192, MaxOutputTokens: 100}
	path := filepath.Join(root, "provider.jsonl")
	binding, err := Bind(path, accessPath, policy, intent, endpoint, model)
	if err != nil {
		t.Fatal(err)
	}
	return gatewayFixture{accessPath: accessPath, path: path, policy: policy, intent: intent, endpoint: endpoint, model: model, binding: binding}
}

func (f gatewayFixture) begin(t *testing.T, digest string, output int64) CallIntent {
	t.Helper()
	call, err := Begin(f.path, f.accessPath, f.policy, f.intent, f.binding, digest, 256, output)
	if err != nil {
		t.Fatal(err)
	}
	return call
}

func (f gatewayFixture) receipt(call CallIntent, digest, finish string, usage Usage) CallReceipt {
	bindingID, _ := f.binding.ID()
	return CallReceipt{Version: 1, BindingID: bindingID, InvocationID: f.intent.Reservation.InvocationID, CallID: call.CallID, ResponseSHA256: digest, ResponseBytes: 512, ResponseID: "chatcmpl-fixture-" + string(rune('0'+call.Sequence)), ObservedModel: f.model.Model, Finish: finish, StreamComplete: true, UsageComplete: true, Usage: usage, RequestExpectationID: call.RequestExpectationID}
}

func int64Pointer(value int64) *int64 { return &value }

func TestTwoSubcallToolLoopAggregatesCompleteProviderUsage(t *testing.T) {
	f := newGatewayFixture(t, 100)
	first := f.begin(t, strings.Repeat("c", 64), 40)
	firstUsage := Usage{InputTokens: 20, OutputTokens: 5, ReasoningTokens: int64Pointer(2), CacheReadTokens: int64Pointer(3), CacheWriteTokens: int64Pointer(1)}
	if err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, first, f.receipt(first, strings.Repeat("d", 64), "tool_calls", firstUsage)); err != nil {
		t.Fatal(err)
	}
	second := f.begin(t, strings.Repeat("e", 64), 30)
	secondUsage := Usage{InputTokens: 30, OutputTokens: 10, ReasoningTokens: int64Pointer(4), CacheReadTokens: int64Pointer(5)}
	if err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, second, f.receipt(second, strings.Repeat("f", 64), "stop", secondUsage)); err != nil {
		t.Fatal(err)
	}
	state, err := Inspect(f.path)
	if err != nil || state.Pending != nil || !state.Finished || len(state.Calls) != 2 {
		t.Fatal("two-call provider state mismatch", state, err)
	}
	if state.Aggregate.InputTokens != 50 || state.Aggregate.OutputTokens != 15 || state.Aggregate.ReasoningTokens == nil || *state.Aggregate.ReasoningTokens != 6 || state.Aggregate.CacheReadTokens == nil || *state.Aggregate.CacheReadTokens != 8 || state.Aggregate.CacheWriteTokens != nil {
		t.Fatal("provider usage aggregate mismatch", state.Aggregate)
	}
	if _, err := Begin(f.path, f.accessPath, f.policy, f.intent, f.binding, strings.Repeat("1", 64), 10, 1); err == nil {
		t.Fatal("call after terminal stop admitted")
	}
	raw, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"provider-key", "secret", "raw prompt", "Bearer "} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("provider journal retained credential or raw content")
		}
	}
}

func TestPendingCrashBlocksNextAndMalformedReceiptDoesNotClearIt(t *testing.T) {
	f := newGatewayFixture(t, 100)
	call := f.begin(t, strings.Repeat("c", 64), 20)
	if _, err := Begin(f.path, f.accessPath, f.policy, f.intent, f.binding, strings.Repeat("d", 64), 10, 10); err == nil {
		t.Fatal("second call admitted while first outcome unknown")
	}
	state, err := Inspect(f.path)
	if err != nil || state.Pending == nil || state.Pending.CallID != call.CallID {
		t.Fatal("pending call did not survive journal recovery", state, err)
	}
	incomplete := f.receipt(call, strings.Repeat("e", 64), "tool_calls", Usage{})
	incomplete.UsageComplete = false
	if err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, call, incomplete); err == nil {
		t.Fatal("incomplete provider usage admitted")
	}
	state, err = Inspect(f.path)
	if err != nil || state.Pending == nil || state.Calls[0].Receipt != nil {
		t.Fatal("rejected receipt mutated pending state", state, err)
	}
}

func TestRepeatedRequestDigestAndCallLimitFailClosed(t *testing.T) {
	f := newGatewayFixture(t, 100)
	first := f.begin(t, strings.Repeat("c", 64), 20)
	if err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, first, f.receipt(first, strings.Repeat("d", 64), "tool_calls", Usage{InputTokens: 10, OutputTokens: 2})); err != nil {
		t.Fatal(err)
	}
	if _, err := Begin(f.path, f.accessPath, f.policy, f.intent, f.binding, first.RequestSHA256, 256, 20); err == nil {
		t.Fatal("repeated request digest admitted as hidden retry")
	}
	second := f.begin(t, strings.Repeat("e", 64), 20)
	if err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, second, f.receipt(second, strings.Repeat("f", 64), "tool_calls", Usage{InputTokens: 10, OutputTokens: 2})); err != nil {
		t.Fatal(err)
	}
	state, err := Inspect(f.path)
	if err != nil || !state.Exhausted || state.Finished {
		t.Fatal("tool continuation at call ceiling was not exposed as exhausted", state, err)
	}
	if _, err := Begin(f.path, f.accessPath, f.policy, f.intent, f.binding, strings.Repeat("1", 64), 256, 20); err == nil {
		t.Fatal("contract call ceiling exceeded")
	}
}

func TestBeginRejectsExhaustedReservationAndRemainingOutputOverflow(t *testing.T) {
	f := newGatewayFixtureWithCalls(t, 20, 3)
	first := f.begin(t, strings.Repeat("c", 64), 10)
	if err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, first, f.receipt(first, strings.Repeat("d", 64), "tool_calls", Usage{InputTokens: 8, OutputTokens: 2})); err != nil {
		t.Fatal(err)
	}
	if _, err := Begin(f.path, f.accessPath, f.policy, f.intent, f.binding, strings.Repeat("e", 64), 256, 11); err == nil {
		t.Fatal("output cap above known remaining reservation admitted")
	}
	second := f.begin(t, strings.Repeat("e", 64), 10)
	if err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, second, f.receipt(second, strings.Repeat("f", 64), "tool_calls", Usage{InputTokens: 5, OutputTokens: 5})); err != nil {
		t.Fatal(err)
	}
	if _, err := Begin(f.path, f.accessPath, f.policy, f.intent, f.binding, strings.Repeat("1", 64), 256, 1); err == nil {
		t.Fatal("call admitted after inclusive reservation was exactly exhausted")
	}
}

func TestExactPolicyRouteAndBindingAreRequired(t *testing.T) {
	f := newGatewayFixture(t, 100)
	driftedPolicy := f.policy
	driftedPolicy.Class = access.Public
	if _, err := Begin(f.path, f.accessPath, driftedPolicy, f.intent, f.binding, strings.Repeat("c", 64), 256, 10); err == nil {
		t.Fatal("policy drift admitted")
	}
	foreignIntent := f.intent
	foreignIntent.Attempt = 2
	foreignIntent.Reservation.InvocationID, _ = foreignIntent.ID()
	if _, err := Begin(f.path, f.accessPath, f.policy, foreignIntent, f.binding, strings.Repeat("c", 64), 256, 10); err == nil {
		t.Fatal("foreign access intent admitted")
	}
	foreignRoute := f.intent
	foreignRoute.Route.Model = "other-model"
	foreignRoute.Reservation.InvocationID, _ = foreignRoute.ID()
	if _, err := Begin(f.path, f.accessPath, f.policy, foreignRoute, f.binding, strings.Repeat("c", 64), 256, 10); err == nil {
		t.Fatal("foreign provider route admitted")
	}
	changed := f.binding
	changed.Model.MaxCalls++
	changed.ModelID, _ = changed.Model.ID()
	if _, err := Begin(f.path, f.accessPath, f.policy, f.intent, changed, strings.Repeat("c", 64), 256, 10); err == nil {
		t.Fatal("changed provider contract admitted")
	}
	state, err := Inspect(f.path)
	if err != nil || state.Binding == nil || !reflect.DeepEqual(*state.Binding, f.binding) || len(state.Calls) != 0 {
		t.Fatal("rejected mismatch changed durable state", state, err)
	}
}

func TestReceiptMustNameExactPendingState(t *testing.T) {
	f := newGatewayFixture(t, 100)
	call := f.begin(t, strings.Repeat("c", 64), 20)
	foreign := call
	foreign.Sequence++
	foreign.CallID, _ = foreign.ID()
	receipt := f.receipt(call, strings.Repeat("d", 64), "stop", Usage{InputTokens: 1, OutputTokens: 1})
	if err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, foreign, receipt); err == nil {
		t.Fatal("receipt admitted against foreign pending state")
	}
	state, err := Inspect(f.path)
	if err != nil || state.Pending == nil || state.Pending.CallID != call.CallID || state.Calls[0].Receipt != nil {
		t.Fatal("foreign state receipt changed pending call", state, err)
	}
}

func TestOverBudgetOrInvalidObservationLeavesAccessAndCallPending(t *testing.T) {
	f := newGatewayFixture(t, 30)
	call := f.begin(t, strings.Repeat("c", 64), 10)
	overOutput := f.receipt(call, strings.Repeat("d", 64), "stop", Usage{InputTokens: 1, OutputTokens: 11})
	if err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, call, overOutput); err == nil {
		t.Fatal("per-call output overflow admitted")
	}
	overReservation := f.receipt(call, strings.Repeat("e", 64), "stop", Usage{InputTokens: 25, OutputTokens: 10})
	if err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, call, overReservation); err == nil {
		t.Fatal("access reservation overflow admitted")
	}
	badDetail := f.receipt(call, strings.Repeat("f", 64), "stop", Usage{InputTokens: 5, OutputTokens: 5, ReasoningTokens: int64Pointer(6)})
	if err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, call, badDetail); err == nil {
		t.Fatal("invalid raw usage detail admitted")
	}
	state, err := Inspect(f.path)
	if err != nil || state.Pending == nil || state.Calls[0].Receipt != nil {
		t.Fatal("invalid receipt changed pending state", state, err)
	}
	if err := access.RequireActive(f.accessPath, f.policy, f.intent); err != nil {
		t.Fatal("invalid provider receipt refunded or terminalized access", err)
	}
}

func TestCompleteRejectsUnexpectedAccessTerminal(t *testing.T) {
	f := newGatewayFixture(t, 100)
	call := f.begin(t, strings.Repeat("c", 64), 20)
	routeID, _ := f.intent.Route.ID()
	if err := access.RecordTerminal(f.accessPath, f.policy, access.Receipt{InvocationID: f.intent.Reservation.InvocationID, RouteID: routeID, Status: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	receipt := f.receipt(call, strings.Repeat("d", 64), "stop", Usage{InputTokens: 1, OutputTokens: 1})
	if err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, call, receipt); err == nil {
		t.Fatal("late provider receipt admitted after access terminal")
	}
	state, err := Inspect(f.path)
	if err != nil || state.Pending == nil {
		t.Fatal("late receipt cleared pending provider call", state, err)
	}
}

func TestInspectReturnsIndependentReplayState(t *testing.T) {
	f := newGatewayFixture(t, 100)
	call := f.begin(t, strings.Repeat("c", 64), 20)
	detail := int64(1)
	if err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, call, f.receipt(call, strings.Repeat("d", 64), "tool_calls", Usage{InputTokens: 2, OutputTokens: 2, ReasoningTokens: &detail})); err != nil {
		t.Fatal(err)
	}
	first, err := Inspect(f.path)
	if err != nil {
		t.Fatal(err)
	}
	first.Binding.Model.Model = "mutated"
	first.Calls[0].Intent.RequestSHA256 = strings.Repeat("0", 64)
	*first.Calls[0].Receipt.Usage.ReasoningTokens = 99
	*first.Aggregate.ReasoningTokens = 99
	second, err := Inspect(f.path)
	if err != nil || second.Binding.Model.Model != f.model.Model || second.Calls[0].Intent.RequestSHA256 != strings.Repeat("c", 64) || *second.Calls[0].Receipt.Usage.ReasoningTokens != 1 || *second.Aggregate.ReasoningTokens != 1 {
		t.Fatal("caller mutation changed replay state", second, err)
	}
}

func TestValidatedSSEObservationCompletesExactDurableCall(t *testing.T) {
	f := newGatewayFixture(t, 100)
	f.binding.Model.Model = "wire-model"
	f.binding.Model.MaxOutputTokens = 20
	f.binding.ModelID, _ = f.binding.Model.ID()
	// Use a fresh journal because the complete binding identity includes model.
	f.path = filepath.Join(t.TempDir(), "provider-observation.jsonl")
	bound, err := Bind(f.path, f.accessPath, f.policy, f.intent, f.binding.Endpoint, f.binding.Model)
	if err == nil {
		// The access route is intentionally exact and must reject model substitution.
		t.Fatal("binding unexpectedly admitted model differing from access route", bound)
	}

	// Rebind a fixture whose access route and validated SSE model agree.
	f = newGatewayFixture(t, 100)
	call := f.begin(t, strings.Repeat("c", 64), 20)
	raw := []byte(strings.ReplaceAll(string(toolSSE()), "wire-model", f.model.Model))
	receipt, err := ObserveChatCompletionSSE(f.binding, call, raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, call, receipt); err != nil {
		t.Fatal("validated SSE receipt did not compose with durable completion", err)
	}
	state, err := Inspect(f.path)
	if err != nil || state.Pending != nil || len(state.Calls) != 1 || state.Calls[0].Receipt == nil || state.Calls[0].Receipt.ResponseID == "" || state.Calls[0].Receipt.ResponseSHA256 != receipt.ResponseSHA256 {
		t.Fatal("durable SSE observation mismatch", state, err)
	}
}
