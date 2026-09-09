package providertransport

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/providercredential"
	"harness.local/engorch/internal/providergateway"
)

const fixtureSecret = "fixture-provider-secret"

type transportFixture struct {
	accessPath  string
	gatewayPath string
	policy      access.Policy
	intent      access.Intent
	binding     providergateway.Binding
	lease       *providercredential.Lease
	client      *Client
	request     Request
}

type fixtureProtocol struct {
	unlimited           bool
	adapterID           string
	path                string
	capabilities        *providergateway.ModelCapabilities
	adapterCapabilities json.RawMessage
	body                []byte
	expectation         providergateway.AdapterRequestExpectation
	observedAliases     []string
	privacy             *access.PrivacyPolicy
}

type requestCapture struct {
	mu               sync.Mutex
	calls            int
	method           string
	path             string
	body             []byte
	authorization    string
	apiKey           string
	userAgent        string
	session          string
	pendingAtRequest bool
}

func TestExecuteSinglePostCompletesBeforeReturningForAPIAndSubscription(t *testing.T) {
	for _, test := range []struct {
		name, billing, scheme, keyHeader string
	}{
		{name: "api bearer", billing: "api", scheme: "bearer"},
		{name: "subscription key header", billing: "subscription", scheme: "api-key-header", keyHeader: "x-provider-key"},
	} {
		t.Run(test.name, func(t *testing.T) {
			capture := &requestCapture{}
			var gatewayPath string
			server := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				state, err := providergateway.Inspect(gatewayPath)
				capture.mu.Lock()
				capture.calls++
				capture.method = r.Method
				capture.path = r.URL.RequestURI()
				capture.body = append([]byte(nil), body...)
				capture.authorization = r.Header.Get("authorization")
				capture.apiKey = r.Header.Get("x-provider-key")
				capture.userAgent = r.Header.Get("user-agent")
				capture.session = r.Header.Get("x-provider-session")
				capture.pendingAtRequest = err == nil && state.Pending != nil && len(state.Calls) == 1
				capture.mu.Unlock()
				w.Header().Set("content-type", "text/event-stream; charset=utf-8")
				_, _ = w.Write(validChatSSE())
			})
			fixture := newTransportFixture(t, server, test.billing, test.scheme, test.keyHeader, 4096)
			gatewayPath = fixture.gatewayPath
			t.Setenv("HTTPS_PROXY", "https://127.0.0.1:1")

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := fixture.client.Execute(ctx, fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			if string(result.Body) != string(validChatSSE()) || result.Receipt.Finish != "stop" || !result.Receipt.StreamComplete || !result.Receipt.UsageComplete {
				t.Fatal("validated response was not returned after completion", result)
			}
			state, err := providergateway.Inspect(fixture.gatewayPath)
			if err != nil || state.Pending != nil || !state.Finished || len(state.Calls) != 1 || state.Calls[0].Receipt == nil {
				t.Fatal("provider completion was not durable before return", state, err)
			}
			capture.mu.Lock()
			defer capture.mu.Unlock()
			if capture.calls != 1 || capture.method != http.MethodPost || capture.path != "/v1/chat/completions" || string(capture.body) != string(fixture.request.Body) || !capture.pendingAtRequest || capture.userAgent != "engorch-transport-test/1" {
				t.Fatal("outbound request differed from admitted single POST", capture)
			}
			expectedSession, _ := canonical.Hash("harness.provider-session.v1", struct {
				InvocationID string `json:"invocation_id"`
			}{InvocationID: fixture.intent.Reservation.InvocationID})
			if capture.session != expectedSession {
				t.Fatal("session header was not derived from invocation identity")
			}
			if test.scheme == "bearer" && (capture.authorization != "Bearer "+fixtureSecret || capture.apiKey != "") {
				t.Fatal("bearer authentication differed")
			}
			if test.scheme == "api-key-header" && (capture.apiKey != fixtureSecret || capture.authorization != "") {
				t.Fatal("API key header authentication differed")
			}
			assertSecretAbsent(t, fixture.accessPath, fixture.gatewayPath)
		})
	}
}

func TestExecuteFiniteJSONUsesBoundFramingAndDurableReceipt(t *testing.T) {
	var accept, body string
	server := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		accept = r.Header.Get("accept")
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("content-type", "application/json; charset=utf-8")
		_, _ = w.Write(validChatJSON())
	})
	requestBody := []byte(`{"model":"wire-model","messages":[{"role":"user","content":"hello"}],"max_tokens":5,"stream":false}`)
	fixture := newTransportFixtureForProtocol(t, server, "api", "bearer", "", 4096, fixtureProtocol{
		adapterID: providergateway.OpenAIChatCompletionsAdapter, path: "/v1/chat/completions",
		capabilities:        &providergateway.ModelCapabilities{Tools: true, OutputCap: true, CompleteUsage: true, SupportedResponseFramings: []providergateway.ResponseFraming{providergateway.ResponseFramingJSON}},
		adapterCapabilities: json.RawMessage("{}"), body: requestBody,
		expectation: providergateway.AdapterRequestExpectation{MaxBytes: int64(len(requestBody)), MaxOutputTokens: 5, Controls: json.RawMessage("{}"), ResponseFraming: providergateway.ResponseFramingJSON},
	})
	expectationID, err := providergateway.AdapterRequestExpectationID(fixture.binding, fixture.request.Expectation)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := fixture.client.Execute(ctx, fixture.request)
	if err != nil || accept != "application/json" || !strings.Contains(body, `"stream":false`) || result.Content == nil || result.Content.OutputText != "ok" || result.Receipt.ObservedModel != "wire-model" || result.Receipt.Finish != "stop" || !result.Receipt.StreamComplete || result.Receipt.RequestExpectationID != expectationID {
		t.Fatal("finite JSON transport differed", accept, body, result, err)
	}
	state, err := providergateway.Inspect(fixture.gatewayPath)
	if err != nil || state.Pending != nil || len(state.Calls) != 1 || state.Calls[0].Receipt == nil || state.Calls[0].Receipt.RequestExpectationID != expectationID {
		t.Fatal("finite JSON completion was not durably bound", state, err)
	}
}

func TestExecuteFiniteJSONAcrossAllProductionAdapters(t *testing.T) {
	for _, adapter := range []string{providergateway.OpenAIChatCompletionsAdapter, providergateway.OpenAIResponsesAdapter, providergateway.AnthropicMessagesAdapter} {
		for _, framing := range []providergateway.ResponseFraming{providergateway.ResponseFramingJSON, providergateway.ResponseFramingSSE} {
			for _, scheme := range []string{"bearer", "api-key-header"} {
				t.Run(adapter+"/"+string(framing)+"/"+scheme, func(t *testing.T) {
					mediaType := "application/json"
					responseBody := finiteJSONResponse(adapter)
					protocol := finiteProtocolFixture(t, adapter)
					protocol.privacy = &access.PrivacyPolicy{Version: 1, Training: "excluded", Retention: "zero"}
					if framing == providergateway.ResponseFramingSSE {
						mediaType = "text/event-stream"
						responseBody = finiteSSETextResponse(adapter)
						protocol.capabilities.SupportedResponseFramings = []providergateway.ResponseFraming{framing}
						protocol.expectation.ResponseFraming = framing
						protocol.body = []byte(strings.Replace(string(protocol.body), `"stream":false`, `"stream":true`, 1))
						if adapter == providergateway.OpenAIChatCompletionsAdapter {
							protocol.body = []byte(strings.Replace(string(protocol.body), `"stream":true`, `"stream":true,"stream_options":{"include_usage":true}`, 1))
						}
						protocol.expectation.MaxBytes = int64(len(protocol.body))
					}
					var accept, path string
					var calls int
					server := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
						calls++
						if _, readErr := io.Copy(io.Discard, r.Body); readErr != nil {
							t.Error("request body read failed", readErr)
						}
						if scheme == "bearer" {
							if r.Header.Get("authorization") != "Bearer "+fixtureSecret || r.Header.Get("x-provider-key") != "" {
								t.Error("bearer header differs")
							}
						} else if r.Header.Get("x-provider-key") != fixtureSecret || r.Header.Get("authorization") != "" {
							t.Error("named header differs")
						}
						accept, path = r.Header.Get("accept"), r.URL.Path
						w.Header().Set("content-type", mediaType)
						_, _ = w.Write(responseBody)
					})
					header := ""
					if scheme == "api-key-header" {
						header = "x-provider-key"
					}
					fixture := newTransportFixtureForProtocol(t, server, "subscription", scheme, header, 16<<10, protocol)
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					result, err := fixture.client.Execute(ctx, fixture.request)
					if err != nil || accept != mediaType || path != adapterPathForTest(adapter) || result.Content == nil || result.Content.OutputText != "ok" || result.Receipt.ObservedModel != "wire-model" || result.Receipt.Usage.InputTokens != 3 || result.Receipt.Usage.OutputTokens != 2 {
						t.Fatal("finite production adapter differed", accept, path, result, err)
					}
					state, inspectErr := providergateway.Inspect(fixture.gatewayPath)
					if inspectErr != nil || state.Pending != nil || len(state.Calls) != 1 || state.Calls[0].Receipt == nil || !reflect.DeepEqual(*state.Calls[0].Receipt, result.Receipt) {
						t.Fatal("content returned without durable completion", inspectErr)
					}
					assertSecretAbsent(t, fixture.gatewayPath, fixture.accessPath)
					if _, replayErr := fixture.client.Execute(ctx, fixture.request); replayErr == nil || calls != 1 {
						t.Fatal("completed invocation resent", replayErr, calls)
					}
				})
			}
		}
	}
}

func TestExecuteFiniteJSONWrongMediaTypeStaysPendingAndNeverResends(t *testing.T) {
	var calls int
	server := newTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write(finiteJSONResponse(providergateway.OpenAIChatCompletionsAdapter))
	})
	fixture := newFiniteTransportFixture(t, server, providergateway.OpenAIChatCompletionsAdapter)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if result, err := fixture.client.Execute(ctx, fixture.request); !errors.Is(err, ErrPending) || len(result.Body) != 0 || calls != 1 {
		t.Fatal("wrong JSON media type did not remain unresolved", result, err, calls)
	}
	if result, err := fixture.client.Execute(ctx, fixture.request); !errors.Is(err, ErrPending) || len(result.Body) != 0 || calls != 1 {
		t.Fatal("wrong JSON media type was resent", result, err, calls)
	}
}

func TestExecuteFiniteJSONCancellationStaysPendingWithoutResend(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	server := newTLSServer(t, func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	fixture := newFiniteTransportFixture(t, server, providergateway.OpenAIChatCompletionsAdapter)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		<-started
		cancel()
	}()
	result, err := fixture.client.Execute(ctx, fixture.request)
	if !errors.Is(err, ErrPending) || !errors.Is(err, context.Canceled) || len(result.Body) != 0 {
		t.Fatal("cancelled finite call did not remain unresolved", result, err)
	}
	var attempt *AttemptError
	if !errors.As(err, &attempt) || !attempt.Recorded || attempt.Observation.Class != providergateway.FailureLocalCancellation {
		t.Fatal("finite local cancellation diagnostic unavailable", err)
	}
	assertPending(t, fixture)
}

func TestFiniteResponsesTrailingCostCapabilityRejectsBeforeAdmissionOrEgress(t *testing.T) {
	var calls int
	server := newTLSServer(t, func(http.ResponseWriter, *http.Request) { calls++ })
	protocol := finiteProtocolFixture(t, providergateway.OpenAIResponsesAdapter)
	var capabilities providergateway.ResponsesModelCapabilities
	if err := json.Unmarshal(protocol.adapterCapabilities, &capabilities); err != nil {
		t.Fatal(err)
	}
	capabilities.TrailingCostPingV1 = true
	protocol.adapterCapabilities, _ = canonical.Bytes(capabilities)
	fixture := newTransportFixtureForProtocol(t, server, "subscription", "api-key-header", "x-provider-key", 4096, protocol)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := fixture.client.Execute(ctx, fixture.request); !errors.Is(err, providergateway.ErrCapabilityUnavailable) || calls != 0 {
		t.Fatal("stream-only cost capability reached admission or egress", err, calls)
	}
	state, err := providergateway.Inspect(fixture.gatewayPath)
	if err != nil || state.Pending != nil || len(state.Calls) != 0 {
		t.Fatal("rejected JSON cost capability changed gateway", state, err)
	}
}

func TestExecuteRedirectLeavesOnePendingCallAndNeverFollowsOrResends(t *testing.T) {
	var mu sync.Mutex
	paths := []string{}
	server := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	})
	fixture := newTransportFixture(t, server, "api", "bearer", "", 4096)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if result, err := fixture.client.Execute(ctx, fixture.request); !errors.Is(err, ErrPending) || len(result.Body) != 0 || strings.Contains(err.Error(), fixtureSecret) {
		t.Fatal("redirect did not become a sanitized pending outcome", result, err)
	}
	if result, err := fixture.client.Execute(ctx, fixture.request); !errors.Is(err, ErrPending) || len(result.Body) != 0 {
		t.Fatal("existing pending call did not continue to require recovery", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || paths[0] != "/v1/chat/completions" {
		t.Fatal("redirect was followed or call was resent", paths)
	}
	assertPending(t, fixture)
	assertSecretAbsent(t, fixture.accessPath, fixture.gatewayPath)
}

func TestExecuteCancellationLeavesPendingWithoutResponse(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	server := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	fixture := newTransportFixture(t, server, "api", "bearer", "", 4096)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		select {
		case <-started:
			cancel()
		case <-time.After(5 * time.Second):
		}
	}()
	result, err := fixture.client.Execute(ctx, fixture.request)
	if !errors.Is(err, ErrPending) || !errors.Is(err, context.Canceled) || len(result.Body) != 0 {
		t.Fatal("cancelled call did not retain an unresolved outcome", result, err)
	}
	var attempt *AttemptError
	if !errors.As(err, &attempt) || !attempt.Recorded || attempt.Observation.Class != providergateway.FailureLocalCancellation {
		t.Fatal("streaming local cancellation diagnostic unavailable", err)
	}
	select {
	case <-started:
	default:
		t.Fatal("cancellation occurred before the admitted request was sent")
	}
	assertPending(t, fixture)
}

func TestExecuteRejectsMalformedAndOversizedResponsesAsPending(t *testing.T) {
	for _, test := range []struct {
		name     string
		response []byte
		maxBytes int64
	}{
		{name: "malformed", response: []byte("not an SSE stream"), maxBytes: 4096},
		{name: "oversized", response: []byte(strings.Repeat("x", 129)), maxBytes: 128},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := newTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("content-type", "text/event-stream")
				_, _ = w.Write(test.response)
			})
			fixture := newTransportFixture(t, server, "api", "bearer", "", test.maxBytes)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := fixture.client.Execute(ctx, fixture.request)
			if !errors.Is(err, ErrPending) || len(result.Body) != 0 || calls != 1 {
				t.Fatal("invalid response was returned or retried", result, err, calls)
			}
			assertPending(t, fixture)
		})
	}
}

func TestExecuteRejectsBeforeBeginWithoutFiniteDeadlineOrWithForeignLease(t *testing.T) {
	serverCalls := 0
	server := newTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		serverCalls++
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write(validChatSSE())
	})
	fixture := newTransportFixture(t, server, "api", "bearer", "", 4096)
	if _, err := fixture.client.Execute(context.Background(), fixture.request); !errors.Is(err, ErrRejected) {
		t.Fatal("unbounded execution context was admitted", err)
	}
	foreign := newTransportFixture(t, server, "api", "bearer", "", 4096)
	request := fixture.request
	request.Lease = foreign.lease
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := fixture.client.Execute(ctx, request); !errors.Is(err, ErrRejected) {
		t.Fatal("foreign credential lease was admitted", err)
	}
	state, err := providergateway.Inspect(fixture.gatewayPath)
	if err != nil || state.Pending != nil || len(state.Calls) != 0 || serverCalls != 0 {
		t.Fatal("rejected request produced a durable or network effect", state, err, serverCalls)
	}
}

func TestExecuteClassifiesExistingBeginStateAsPendingWithoutSending(t *testing.T) {
	serverCalls := 0
	server := newTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		serverCalls++
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write(validChatSSE())
	})
	fixture := newTransportFixture(t, server, "api", "bearer", "", 4096)
	metadata, err := providergateway.ValidateAdapterRequest(fixture.request.Body, fixture.binding, fixture.request.Expectation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := providergateway.BeginWithExpectation(fixture.gatewayPath, fixture.accessPath, fixture.policy, fixture.intent, fixture.binding, metadata.SHA256, metadata.SizeBytes, fixture.request.Expectation); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := fixture.client.Execute(ctx, fixture.request)
	if !errors.Is(err, ErrPending) || len(result.Body) != 0 || serverCalls != 0 {
		t.Fatal("existing pending intent was misclassified or sent", result, err, serverCalls)
	}
	assertPending(t, fixture)
}

func TestExecuteResponsesAdapterFreezesFractionalReasoningToolContract(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	var capturedBody []byte
	server := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		close(started)
		<-release
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write(validResponsesToolSSE("provider-wire-model"))
	})
	fixture := newResponsesTransportFixture(t, server)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	request := fixture.request
	expectedBody := append([]byte(nil), request.Body...)
	expectedExpectationID, err := providergateway.AdapterRequestExpectationID(request.Binding, request.Expectation)
	if err != nil {
		t.Fatal(err)
	}
	alternateExpectation := request.Expectation
	var alternateControls providergateway.ResponsesRequestExpectation
	if err := json.Unmarshal(alternateExpectation.Controls, &alternateControls); err != nil {
		t.Fatal(err)
	}
	alternateTemperature := json.Number("0.60")
	alternateControls.Temperature = &alternateTemperature
	alternateExpectation.Controls, err = json.Marshal(alternateControls)
	if err != nil {
		t.Fatal(err)
	}
	alternateExpectationID, err := providergateway.AdapterRequestExpectationID(request.Binding, alternateExpectation)
	if err != nil || alternateExpectationID == expectedExpectationID {
		t.Fatal("distinct valid request controls did not acquire distinct identity", alternateExpectationID, err)
	}
	type execution struct {
		result Result
		err    error
	}
	finished := make(chan execution, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		result, err := fixture.client.Execute(ctx, request)
		finished <- execution{result: result, err: err}
	}()
	<-started

	request.Body[0] = 'x'
	request.Expectation = alternateExpectation
	request.Expectation.Tools[0].Parameters[0] = 'x'
	request.Binding.Model.AdapterCapabilities[0] = 'x'
	request.Binding.Endpoint.PublicHeaders["user-agent"] = "mutated-after-send"
	releaseOnce.Do(func() { close(release) })

	outcome := <-finished
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	if string(capturedBody) != string(expectedBody) || outcome.result.Receipt.ObservedModel != "provider-wire-model" || outcome.result.Receipt.Finish != "tool_calls" || outcome.result.Receipt.Usage.InputTokens != 18 || outcome.result.Receipt.Usage.OutputTokens != 2 {
		t.Fatal("Responses request or bounded reasoning/tool observation changed", outcome.result)
	}
	if outcome.result.Receipt.Usage.ReasoningTokens == nil || *outcome.result.Receipt.Usage.ReasoningTokens != 2 || outcome.result.Receipt.Usage.CacheReadTokens == nil || *outcome.result.Receipt.Usage.CacheReadTokens != 2 || outcome.result.Receipt.Usage.CacheWriteTokens == nil || *outcome.result.Receipt.Usage.CacheWriteTokens != 1 {
		t.Fatal("Responses usage subsets were not preserved", outcome.result.Receipt.Usage)
	}
	content := outcome.result.Content
	if content == nil || content.OutputText != "" || content.ToolCalls == nil || len(content.ToolCalls) != 1 || content.ToolCalls[0].ID != "call_fixture" || content.ToolCalls[0].Name != "engorch_lookup" || string(content.ToolCalls[0].Arguments) != `{"city":"Iași"}` || content.Reasoning == nil || content.Reasoning.AdapterID != providergateway.OpenAIResponsesAdapter || len(content.Reasoning.Responses) != 1 || content.Reasoning.Responses[0].DoneEncryptedContent == nil || *content.Reasoning.Responses[0].DoneEncryptedContent != "done-cipher" || content.Reasoning.Responses[0].TerminalEncryptedContent == nil || *content.Reasoning.Responses[0].TerminalEncryptedContent != "terminal-cipher" {
		t.Fatal("validated neutral response content was not returned", content)
	}
	if outcome.result.Receipt.RequestExpectationID != expectedExpectationID {
		t.Fatal("receipt lost the exact request expectation identity", outcome.result.Receipt)
	}
	semantic := outcome.result.Receipt.Semantic
	if semantic == nil || semantic.Version != 1 || semantic.Kind != "assistant-turn" || semantic.OutputTextSHA256 != digestText("") || len(semantic.ToolCalls) != 1 || semantic.ToolCalls[0].ID != "call_fixture" || semantic.ToolCalls[0].Name != "engorch_lookup" || semantic.ToolCalls[0].ArgumentsSHA256 != digestText(`{"city":"Iași"}`) {
		t.Fatal("Responses semantic projection was not retained", semantic)
	}
	semantic.ToolCalls[0].Name = "caller-mutation"
	content.ToolCalls[0].Arguments[0] = 'x'
	content.Reasoning.Responses[0].Summary[0] = "caller-mutation"
	state, err := providergateway.Inspect(fixture.gatewayPath)
	if err != nil || state.Pending != nil || len(state.Calls) != 1 || state.Calls[0].Intent.RequestExpectationID != expectedExpectationID || state.Calls[0].Intent.RequestExpectationID == alternateExpectationID || state.Calls[0].Receipt == nil || state.Calls[0].Receipt.Semantic == nil || state.Calls[0].Receipt.Semantic.ToolCalls[0].Name != "engorch_lookup" {
		t.Fatal("Responses completion was not durably recorded", state, err)
	}
}

func newTransportFixture(t *testing.T, server *httptest.Server, billing, scheme, header string, maxResponseBytes int64) transportFixture {
	body := []byte(`{"model":"wire-model","messages":[{"role":"user","content":"hello"}],"max_tokens":5,"stream":true,"stream_options":{"include_usage":true}}`)
	return newTransportFixtureForProtocol(t, server, billing, scheme, header, maxResponseBytes, fixtureProtocol{
		adapterID:           providergateway.OpenAIChatCompletionsAdapter,
		path:                "/v1/chat/completions",
		capabilities:        &providergateway.ModelCapabilities{Tools: true, OutputCap: true, CompleteUsage: true},
		adapterCapabilities: json.RawMessage("{}"),
		body:                body,
		expectation:         providergateway.AdapterRequestExpectation{MaxBytes: int64(len(body)), MaxOutputTokens: 5, Controls: json.RawMessage("{}")},
	})
}

func newResponsesTransportFixture(t *testing.T, server *httptest.Server) transportFixture {
	t.Helper()
	temperatureMinimum, temperatureMaximum := "0.1", "0.9"
	topPMinimum, topPMaximum := "0.2", "1"
	adapterCapabilities, err := canonical.Bytes(providergateway.ResponsesModelCapabilities{
		Version: 1, FunctionTools: true, Reasoning: true,
		SystemRoles: []string{"developer"}, Sampling: true,
		TemperatureMin: &temperatureMinimum, TemperatureMax: &temperatureMaximum,
		TopPMin: &topPMinimum, TopPMax: &topPMaximum,
		TextFormats: []string{"plain"}, KnownExtensions: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	temperature := json.Number("0.50")
	topP := json.Number("0.75")
	parallel := false
	controls := providergateway.ResponsesRequestExpectation{
		MaxOutputTokens: 5, StateMode: "full-input-stateless", Store: false,
		SystemRole: "developer", ReasoningEffort: "high", ReasoningSummary: "auto",
		ToolChoice: "auto", ParallelToolCalls: &parallel, Temperature: &temperature, TopP: &topP,
		Include: []string{"reasoning.encrypted_content"}, FunctionToolStrict: false,
	}
	required := &providergateway.RequiredCapabilities{Tools: true, Reasoning: true, StructuredOutput: providergateway.StructuredOutputTextParseRequired}
	controlBytes, err := json.Marshal(controls)
	if err != nil {
		t.Fatal(err)
	}
	tool := providergateway.RequestTool{
		Name: "engorch_lookup", Description: "Read one admitted city record.",
		Parameters: json.RawMessage(`{"additionalProperties":false,"properties":{"city":{"type":"string"}},"required":["city"],"type":"object"}`),
	}
	body := []byte(`{"model":"wire-model","input":[{"role":"developer","content":"Use only admitted tools."},{"role":"user","content":[{"type":"input_text","text":"Look up Iași."}]}],"max_output_tokens":5,"stream":true,"store":false,"reasoning":{"effort":"high","summary":"auto"},"include":["reasoning.encrypted_content"],"tools":[{"type":"function","name":"engorch_lookup","description":"Read one admitted city record.","parameters":{"additionalProperties":false,"properties":{"city":{"type":"string"}},"required":["city"],"type":"object"},"strict":false}],"tool_choice":"auto","parallel_tool_calls":false,"temperature":0.50,"top_p":0.75}`)
	return newTransportFixtureForProtocol(t, server, "subscription", "api-key-header", "x-provider-key", 16<<10, fixtureProtocol{
		adapterID: providergateway.OpenAIResponsesAdapter, path: "/v1/responses",
		capabilities:        &providergateway.ModelCapabilities{Tools: true, Reasoning: true, OutputCap: true, CompleteUsage: true},
		adapterCapabilities: adapterCapabilities, observedAliases: []string{"provider-wire-model"}, body: body,
		expectation: providergateway.AdapterRequestExpectation{MaxBytes: int64(len(body)), MaxOutputTokens: 5, Tools: []providergateway.RequestTool{tool}, Controls: controlBytes, RequiredCapabilities: required},
	})
}

func newFiniteTransportFixture(t *testing.T, server *httptest.Server, adapter string) transportFixture {
	return newTransportFixtureForProtocol(t, server, "subscription", "api-key-header", "x-provider-key", 16<<10, finiteProtocolFixture(t, adapter))
}

func finiteProtocolFixture(t *testing.T, adapter string) fixtureProtocol {
	t.Helper()
	modelCapabilities := &providergateway.ModelCapabilities{OutputCap: true, CompleteUsage: true, SupportedResponseFramings: []providergateway.ResponseFraming{providergateway.ResponseFramingJSON}}
	required := &providergateway.RequiredCapabilities{StructuredOutput: providergateway.StructuredOutputTextParseRequired}
	switch adapter {
	case providergateway.OpenAIChatCompletionsAdapter:
		body := []byte(`{"model":"wire-model","messages":[{"role":"user","content":"hello"}],"max_tokens":5,"stream":false}`)
		return fixtureProtocol{adapterID: adapter, path: "/v1/chat/completions", capabilities: modelCapabilities, adapterCapabilities: json.RawMessage("{}"), body: body, expectation: providergateway.AdapterRequestExpectation{MaxBytes: int64(len(body)), MaxOutputTokens: 5, Controls: json.RawMessage("{}"), RequiredCapabilities: required, ResponseFraming: providergateway.ResponseFramingJSON}}
	case providergateway.OpenAIResponsesAdapter:
		capabilities := providergateway.ResponsesModelCapabilities{Version: 1, SystemRoles: []string{"developer"}, TextFormats: []string{"plain"}, KnownExtensions: []string{}}
		adapterCapabilities, err := canonical.Bytes(capabilities)
		if err != nil {
			t.Fatal(err)
		}
		controls, err := json.Marshal(providergateway.ResponsesRequestExpectation{MaxOutputTokens: 5, StateMode: "full-input-stateless", SystemRole: "developer"})
		if err != nil {
			t.Fatal(err)
		}
		body := []byte(`{"model":"wire-model","input":[{"role":"developer","content":"policy"},{"role":"user","content":[{"type":"input_text","text":"hello"}]}],"max_output_tokens":5,"stream":false,"store":false}`)
		return fixtureProtocol{adapterID: adapter, path: "/v1/responses", capabilities: modelCapabilities, adapterCapabilities: adapterCapabilities, body: body, expectation: providergateway.AdapterRequestExpectation{MaxBytes: int64(len(body)), MaxOutputTokens: 5, Controls: controls, RequiredCapabilities: required, ResponseFraming: providergateway.ResponseFramingJSON}}
	case providergateway.AnthropicMessagesAdapter:
		capabilities := providergateway.AnthropicMessagesModelCapabilities{Version: 1, ThinkingModes: []string{}, EffortValues: []string{}, ToolChoiceModes: []string{}, SamplingParameters: []string{}, RequireCacheUsageBreakdown: true}
		adapterCapabilities, err := canonical.Bytes(capabilities)
		if err != nil {
			t.Fatal(err)
		}
		controls, err := json.Marshal(providergateway.AnthropicMessagesRequestExpectation{MaxOutputTokens: 5})
		if err != nil {
			t.Fatal(err)
		}
		body := []byte(`{"model":"wire-model","max_tokens":5,"stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
		return fixtureProtocol{adapterID: adapter, path: "/v1/messages", capabilities: modelCapabilities, adapterCapabilities: adapterCapabilities, body: body, expectation: providergateway.AdapterRequestExpectation{MaxBytes: int64(len(body)), MaxOutputTokens: 5, Controls: controls, RequiredCapabilities: required, ResponseFraming: providergateway.ResponseFramingJSON}}
	default:
		t.Fatal("unsupported finite fixture adapter", adapter)
		return fixtureProtocol{}
	}
}

func finiteJSONResponse(adapter string) []byte {
	switch adapter {
	case providergateway.OpenAIResponsesAdapter:
		return []byte(`{"id":"resp_fixture","object":"response","created_at":1,"status":"completed","model":"wire-model","output":[{"id":"msg_fixture","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"ok"}],"role":"assistant"}],"usage":{"input_tokens":3,"input_tokens_details":null,"output_tokens":2,"output_tokens_details":null,"total_tokens":5}}`)
	case providergateway.AnthropicMessagesAdapter:
		return []byte(`{"id":"msg_fixture","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"wire-model","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2}}`)
	default:
		return validChatJSON()
	}
}

func adapterPathForTest(adapter string) string {
	switch adapter {
	case providergateway.OpenAIResponsesAdapter:
		return "/v1/responses"
	case providergateway.AnthropicMessagesAdapter:
		return "/v1/messages"
	default:
		return "/v1/chat/completions"
	}
}

func TestExecuteCapabilityUnavailableHasNoDurableOrNetworkEffect(t *testing.T) {
	calls := 0
	server := newTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write(validChatSSE())
	})
	fixture := newTransportFixture(t, server, "api", "bearer", "", 4096)
	fixture.request.Expectation.RequiredCapabilities = &providergateway.RequiredCapabilities{Reasoning: true, StructuredOutput: providergateway.StructuredOutputTextParseRequired}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := fixture.client.Execute(ctx, fixture.request); !errors.Is(err, providergateway.ErrCapabilityUnavailable) || err.Error() != "CAPABILITY_UNAVAILABLE" {
		t.Fatal("capability mismatch was not returned exactly", err)
	}
	state, err := providergateway.Inspect(fixture.gatewayPath)
	if err != nil || state.Pending != nil || len(state.Calls) != 0 || calls != 0 {
		t.Fatal("capability mismatch created a durable or network effect", state, err, calls)
	}
}

func newTransportFixtureForProtocol(t *testing.T, server *httptest.Server, billing, scheme, header string, maxResponseBytes int64, protocol fixtureProtocol) transportFixture {
	t.Helper()
	root := t.TempDir()
	profile := access.Profile{Version: 1, Name: "fixture-access", Kind: billing, Runtime: "provider-http", Provider: "fixture", CredentialRef: "fixture-key", RepositoryClasses: []access.Class{access.Public}}
	if protocol.privacy != nil {
		profile.Version, profile.Privacy = 2, protocol.privacy
	}
	profileID, err := profile.ID()
	if err != nil {
		t.Fatal(err)
	}
	route := access.Route{Version: 1, Role: "explorer", Runtime: "provider-http", Provider: "fixture", Model: "wire-model", Effort: "low", AccessID: profileID, Permission: "read-only"}
	reservationTokens := int64(25)
	if protocol.unlimited {
		reservationTokens = 0
	}
	var reservationCost *int64
	var limitCost *int64
	if billing == "api" {
		value := int64(1)
		reservationCost = &value
		limit := int64(10)
		limitCost = &limit
	}
	policy := access.Policy{Version: 1, RunID: digestText("run:" + root), Class: access.Public, Limits: access.Limits{Tokens: reservationTokens, UnlimitedTokens: protocol.unlimited, CostMicroUSD: limitCost, Concurrency: 1}, Routes: []access.Route{route}, Profiles: []access.Profile{profile}}
	policyID, err := policy.ID()
	if err != nil {
		t.Fatal(err)
	}
	intent := access.Intent{Attempt: 1, PolicyID: policyID, InputHash: digestText("input:" + root), Route: route, Reservation: access.Reservation{Tokens: reservationTokens, UnlimitedTokens: protocol.unlimited, CostMicroUSD: reservationCost, BillingMode: billing}}
	intent.Reservation.InvocationID, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	accessPath := filepath.Join(root, "access.jsonl")
	if err := access.ReserveDurable(accessPath, policy, intent); err != nil {
		t.Fatal(err)
	}
	endpoint := providergateway.EndpointContract{
		Version: 2, Provider: "fixture", URL: server.URL + protocol.path, AdapterID: protocol.adapterID,
		Auth:          &providergateway.AuthContract{Scheme: scheme, CredentialRef: "fixture-key", HeaderName: header},
		PublicHeaders: map[string]string{"user-agent": "engorch-transport-test/1"}, SessionHeader: "x-provider-session",
	}
	model := providergateway.ModelContract{
		Version: 2, Provider: "fixture", Model: "wire-model", AdapterID: protocol.adapterID,
		Capabilities: protocol.capabilities, AdapterCapabilities: protocol.adapterCapabilities, ObservedModelAliases: protocol.observedAliases, ContextWindowTokens: 20, MaxCalls: 1,
		MaxRequestBytes: 4096, MaxResponseBytes: maxResponseBytes, MaxOutputTokens: 5,
	}
	if billing == "api" {
		model.Pricing = &providergateway.PricingPolicy{Currency: "USD", Unit: "micro_usd_per_million_tokens", MaxInputMicroUSDPerMillion: 1, MaxOutputMicroUSDPerMillion: 1}
	}
	gatewayPath := filepath.Join(root, "provider.jsonl")
	binding, err := providergateway.Bind(gatewayPath, accessPath, policy, intent, endpoint, model)
	if err != nil {
		t.Fatal(err)
	}
	source, err := providercredential.NewEnvironmentSource(map[string]string{"fixture-key": "FIXTURE_PROVIDER_KEY"}, func(name string) (string, bool) {
		return fixtureSecret, name == "FIXTURE_PROVIDER_KEY"
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := providercredential.Resolve(context.Background(), accessPath, policy, intent, endpoint, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, err := NewClient(ClientOptions{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{AccessJournalPath: accessPath, GatewayJournalPath: gatewayPath, Policy: policy, Intent: intent, Binding: binding, Lease: lease, Body: protocol.body, Expectation: protocol.expectation}
	return transportFixture{accessPath: accessPath, gatewayPath: gatewayPath, policy: policy, intent: intent, binding: binding, lease: lease, client: client, request: request}
}

func digestText(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func newTLSServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = false
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func validChatSSE() []byte {
	chunks := []string{
		`{"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1,"model":"wire-model","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1,"model":"wire-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1,"model":"wire-model","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
	}
	var builder strings.Builder
	for _, chunk := range chunks {
		builder.WriteString("data: ")
		builder.WriteString(chunk)
		builder.WriteString("\n\n")
	}
	builder.WriteString("data: [DONE]\n\n")
	return []byte(builder.String())
}

func validChatJSON() []byte {
	return []byte(`{"id":"chatcmpl-fixture","object":"chat.completion","created":1,"model":"wire-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
}

func validResponsesToolSSE(model string) []byte {
	return responsesEvents(
		`{"type":"response.created","response":`+responsesSnapshot(model, "in_progress", `[]`, `null`)+`,"sequence_number":10}`,
		`{"type":"response.in_progress","response":`+responsesSnapshot(model, "in_progress", `[]`, `null`)+`,"sequence_number":20}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_fixture","type":"reasoning","encrypted_content":"added-cipher","summary":[]},"sequence_number":30}`,
		`{"type":"response.reasoning_summary_part.added","item_id":"rs_fixture","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""},"sequence_number":31}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_fixture","output_index":0,"summary_index":0,"delta":"private plan","sequence_number":32}`,
		`{"type":"response.reasoning_summary_text.done","item_id":"rs_fixture","output_index":0,"summary_index":0,"text":"private plan","sequence_number":33}`,
		`{"type":"response.reasoning_summary_part.done","item_id":"rs_fixture","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":"private plan"},"sequence_number":34}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_fixture","type":"reasoning","encrypted_content":"done-cipher","summary":[{"type":"summary_text","text":"private plan"}]},"sequence_number":40}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_fixture","type":"function_call","status":"in_progress","arguments":"","call_id":"call_fixture","name":"engorch_lookup"},"sequence_number":50}`,
		`{"type":"response.function_call_arguments.delta","delta":"{\"city\":","item_id":"fc_fixture","obfuscation":"x","output_index":1,"sequence_number":60}`,
		`{"type":"response.function_call_arguments.delta","delta":"\"Iași\"}","item_id":"fc_fixture","obfuscation":"y","output_index":1,"sequence_number":70}`,
		`{"type":"response.function_call_arguments.done","arguments":"{\"city\":\"Iași\"}","item_id":"fc_fixture","output_index":1,"sequence_number":80}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"fc_fixture","type":"function_call","status":"completed","arguments":"{\"city\":\"Iași\"}","call_id":"call_fixture","name":"engorch_lookup"},"sequence_number":90}`,
		`{"type":"response.completed","response":`+responsesSnapshot(model, "completed", `[{"id":"rs_fixture","type":"reasoning","encrypted_content":"terminal-cipher","summary":[{"type":"summary_text","text":"private plan"}]},{"id":"fc_fixture","type":"function_call","status":"completed","arguments":"{\"city\":\"Iași\"}","call_id":"call_fixture","name":"engorch_lookup"}]`, `{"input_tokens":18,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":1},"output_tokens":2,"output_tokens_details":{"reasoning_tokens":2},"total_tokens":20}`)+`,"sequence_number":100}`,
	)
}

func responsesSnapshot(model, status, output, usage string) string {
	return `{"id":"resp_fixture","object":"response","created_at":123,"status":"` + status + `","model":"` + model + `","output":` + output + `,"usage":` + usage + `}`
}

func responsesEvents(events ...string) []byte {
	var result strings.Builder
	for _, event := range events {
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(event), &envelope); err != nil {
			panic(err)
		}
		result.WriteString("event: ")
		result.WriteString(envelope.Type)
		result.WriteString("\ndata: ")
		result.WriteString(event)
		result.WriteString("\n\n")
	}
	return []byte(result.String())
}

func assertPending(t *testing.T, fixture transportFixture) {
	t.Helper()
	state, err := providergateway.Inspect(fixture.gatewayPath)
	if err != nil || state.Pending == nil || len(state.Calls) != 1 || state.Calls[0].Receipt != nil {
		t.Fatal("uncertain call was not retained as pending", state, err)
	}
}

func assertSecretAbsent(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		raw, err := journal.ExportJSONL(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), fixtureSecret) {
			t.Fatal("provider secret reached a durable journal")
		}
	}
}
