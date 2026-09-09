package providerproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/providercredential"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/providertransport"
)

const proxyBearer = "fixture-local-proxy-bearer-00000001"

type proxyFixture struct {
	server      *Server
	accessPath  string
	gatewayPath string
	policy      access.Policy
	intent      access.Intent
	gateway     providergateway.Binding
	credential  *providercredential.Lease
	expectation providergateway.AdapterRequestExpectation
	body        []byte
}

func newProxyFixture(t *testing.T) proxyFixture {
	return newProxyFixtureAt(t, "https://api.example.test/custom/model/invoke?api-version=2026-09-01", providertransport.ClientOptions{})
}

func newProxyFixtureAt(t *testing.T, endpointURL string, clientOptions providertransport.ClientOptions) proxyFixture {
	body := []byte(`{"model":"wire-model","messages":[{"role":"user","content":"hello"}],"max_tokens":5,"stream":true,"stream_options":{"include_usage":true}}`)
	return newProxyFixtureForAdapter(t, endpointURL, clientOptions, providergateway.OpenAIChatCompletionsAdapter, []byte("{}"), providergateway.AdapterRequestExpectation{MaxBytes: int64(len(body)), MaxOutputTokens: 5, Controls: []byte("{}")}, body)
}

func newProxyFixtureForAdapter(t *testing.T, endpointURL string, clientOptions providertransport.ClientOptions, adapterID string, adapterCapabilities []byte, expectation providergateway.AdapterRequestExpectation, body []byte) proxyFixture {
	t.Helper()
	root := t.TempDir()
	profile := access.Profile{Version: 1, Name: "fixture-access", Kind: "api", Runtime: "provider-http", Provider: "fixture", CredentialRef: "fixture-key", RepositoryClasses: []access.Class{access.Public}}
	profileID, err := profile.ID()
	if err != nil {
		t.Fatal(err)
	}
	route := access.Route{Version: 1, Role: "explorer", Runtime: "provider-http", Provider: "fixture", Model: "wire-model", Effort: "low", AccessID: profileID, Permission: "read-only"}
	costLimit, reservationCost := int64(10), int64(1)
	policy := access.Policy{Version: 1, RunID: proxyDigest("run:" + root), Class: access.Public, Limits: access.Limits{Tokens: 25, CostMicroUSD: &costLimit, Concurrency: 1}, Routes: []access.Route{route}, Profiles: []access.Profile{profile}}
	policyID, err := policy.ID()
	if err != nil {
		t.Fatal(err)
	}
	intent := access.Intent{Attempt: 1, PolicyID: policyID, InputHash: proxyDigest("input:" + root), Route: route, Reservation: access.Reservation{Tokens: 25, CostMicroUSD: &reservationCost, BillingMode: "api"}}
	intent.Reservation.InvocationID, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	accessPath := filepath.Join(root, "access.jsonl")
	if err := access.ReserveDurable(accessPath, policy, intent); err != nil {
		t.Fatal(err)
	}
	endpoint := providergateway.EndpointContract{Version: 2, Provider: "fixture", URL: endpointURL, AdapterID: adapterID, Auth: &providergateway.AuthContract{Scheme: "bearer", CredentialRef: "fixture-key"}}
	model := providergateway.ModelContract{Version: 2, Provider: "fixture", Model: "wire-model", AdapterID: adapterID, Capabilities: &providergateway.ModelCapabilities{Tools: len(expectation.Tools) > 0, OutputCap: true, CompleteUsage: true}, AdapterCapabilities: adapterCapabilities, ContextWindowTokens: 20, MaxCalls: 1, MaxRequestBytes: 4096, MaxResponseBytes: 4096, MaxOutputTokens: 5, Pricing: &providergateway.PricingPolicy{Currency: "USD", Unit: "micro_usd_per_million_tokens", MaxInputMicroUSDPerMillion: 1, MaxOutputMicroUSDPerMillion: 1}}
	gatewayPath := filepath.Join(root, "gateway.jsonl")
	gateway, err := providergateway.Bind(gatewayPath, accessPath, policy, intent, endpoint, model)
	if err != nil {
		t.Fatal(err)
	}
	source, err := providercredential.NewEnvironmentSource(map[string]string{"fixture-key": "FIXTURE_KEY"}, func(name string) (string, bool) { return "fixture-upstream-secret", name == "FIXTURE_KEY" })
	if err != nil {
		t.Fatal(err)
	}
	credential, err := providercredential.Resolve(context.Background(), accessPath, policy, intent, endpoint, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = credential.Close() })
	transport, err := providertransport.NewClient(clientOptions)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{AccessJournalPath: accessPath, GatewayJournalPath: gatewayPath, Policy: policy, Intent: intent, Gateway: gateway, Credential: credential, Transport: transport, Bearer: proxyBearer, Expectation: expectation, HandlerTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return proxyFixture{server: server, accessPath: accessPath, gatewayPath: gatewayPath, policy: policy, intent: intent, gateway: gateway, credential: credential, expectation: expectation, body: body}
}

func TestNewPreservesFractionalResponsesExpectationIdentity(t *testing.T) {
	minimum, maximum := "0.1", "0.9"
	capabilities, err := canonical.Bytes(providergateway.ResponsesModelCapabilities{Version: 1, FunctionTools: true, SystemRoles: []string{"developer"}, Sampling: true, TemperatureMin: &minimum, TemperatureMax: &maximum, TextFormats: []string{"plain"}, KnownExtensions: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	temperature := json.Number("0.5")
	controls, err := json.Marshal(providergateway.ResponsesRequestExpectation{MaxOutputTokens: 5, StateMode: "full-input-stateless", SystemRole: "developer", Temperature: &temperature})
	if err != nil {
		t.Fatal(err)
	}
	expectation := providergateway.AdapterRequestExpectation{MaxBytes: 4096, MaxOutputTokens: 5, Controls: controls, Tools: []providergateway.RequestTool{{Name: "read", Description: "Read.", Parameters: json.RawMessage(`{"properties":{"ratio":{"minimum":0.5}},"type":"object"}`)}}}
	fixture := newProxyFixtureForAdapter(t, "https://api.example.test/custom/responses?api-version=2026-09-01", providertransport.ClientOptions{}, providergateway.OpenAIResponsesAdapter, capabilities, expectation, []byte(`{"model":"wire-model"}`))
	if fixture.server.binding.RequestPath != "/v1/responses" || fixture.server.binding.RequestExpectationID == "" {
		t.Fatal("Responses proxy did not bind codec path and fractional expectation", fixture.server.binding)
	}
}

func TestProxyComposesRealTransportToExactTLSUpstream(t *testing.T) {
	type capture struct {
		calls         int
		path          string
		authorization string
		body          []byte
	}
	observed := &capture{}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observed.calls++
		observed.path = request.URL.RequestURI()
		observed.authorization = request.Header.Get("Authorization")
		observed.body, _ = io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write(proxyChatSSE())
	}))
	upstream.EnableHTTP2 = false
	upstream.StartTLS()
	defer upstream.Close()
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	fixture := newProxyFixtureAt(t, upstream.URL+"/custom/model/invoke?api-version=2026-09-01", providertransport.ClientOptions{RootCAs: roots})
	running, err := fixture.server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	status, raw := proxyPost(t, running.URL(), proxyBearer, fixture.body, nil)
	if status != http.StatusOK || !bytes.Equal(raw, proxyChatSSE()) {
		t.Fatal("real transport response did not cross the proxy", status, string(raw))
	}
	if observed.calls != 1 || observed.path != "/custom/model/invoke?api-version=2026-09-01" || observed.authorization != "Bearer fixture-upstream-secret" || !bytes.Equal(observed.body, fixture.body) {
		t.Fatal("proxy changed route, credential, body, or attempt count", observed)
	}
	state, err := providergateway.Inspect(fixture.gatewayPath)
	if err != nil || !state.Finished || state.Pending != nil || len(state.Calls) != 1 || state.Calls[0].Receipt == nil {
		t.Fatal("proxy returned before real transport completion", state, err)
	}
	closeProxy(t, running)
}

func TestProxyReturnsOnlyDurablyCompletedResponseAndStopsAtTerminal(t *testing.T) {
	fixture := newProxyFixture(t)
	var calls atomic.Int32
	response := []byte("data: fixture-complete\n\n")
	fixture.server.execute = func(_ context.Context, request providertransport.Request) (providertransport.Result, error) {
		calls.Add(1)
		return completeProxyCall(request, response, "stop")
	}
	running, err := fixture.server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	assertOwner := fixture.server.transport
	if err := running.ValidateOwner(assertOwner, fixture.credential, proxyBearer); err != nil {
		t.Fatal(err)
	}
	if err := running.ValidateOwner(assertOwner, fixture.credential, proxyBearer+"x"); err == nil {
		t.Fatal("foreign local bearer admitted as proxy owner")
	}
	if !strings.HasSuffix(running.URL(), "/v1/chat/completions") || strings.Contains(running.URL(), "/custom/model/invoke") {
		t.Fatal("local adapter path was derived from arbitrary upstream URL", running.URL())
	}
	status, raw := proxyPost(t, running.URL(), proxyBearer, fixture.body, nil)
	if status != http.StatusOK || !bytes.Equal(raw, response) {
		t.Fatal("validated response not returned", status, string(raw))
	}
	state, err := providergateway.Inspect(fixture.gatewayPath)
	if err != nil || !state.Finished || state.Pending != nil || len(state.Calls) != 1 || state.Calls[0].Receipt == nil {
		t.Fatal("response returned before durable completion", state, err)
	}
	status, _ = proxyPost(t, running.URL(), proxyBearer, fixture.body, nil)
	if status != http.StatusConflict || calls.Load() != 1 {
		t.Fatal("terminal proxy admitted another provider call", status, calls.Load())
	}
	closeProxy(t, running)
}

func TestProxyRejectsWrongAuthorityAndForwardingHeadersBeforeTransport(t *testing.T) {
	fixture := newProxyFixture(t)
	var calls atomic.Int32
	fixture.server.execute = func(context.Context, providertransport.Request) (providertransport.Result, error) {
		calls.Add(1)
		return providertransport.Result{}, providertransport.ErrRejected
	}
	running, err := fixture.server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		bearer string
		header http.Header
	}{
		{name: "wrong bearer", bearer: proxyBearer + "x"},
		{name: "forwarded authority", bearer: proxyBearer, header: http.Header{"X-Forwarded-For": []string{"127.0.0.1"}}},
		{name: "unknown accept", bearer: proxyBearer, header: http.Header{"Accept": []string{"application/octet-stream"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, _ := proxyPost(t, running.URL(), test.bearer, fixture.body, test.header)
			if status != http.StatusBadRequest {
				t.Fatal("unsafe request admitted", status)
			}
		})
	}
	request, err := http.NewRequest(http.MethodPost, running.URL(), bytes.NewReader(fixture.body))
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "127.0.0.1:1"
	setProxyHeaders(request, proxyBearer)
	response, err := localClient().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatal("foreign Host reached provider transport", response.StatusCode, calls.Load())
	}
	closeProxy(t, running)
}

func TestPendingFailurePoisonsProxyWithoutResend(t *testing.T) {
	fixture := newProxyFixture(t)
	var calls atomic.Int32
	fixture.server.execute = func(_ context.Context, request providertransport.Request) (providertransport.Result, error) {
		calls.Add(1)
		metadata, err := providergateway.ValidateAdapterRequest(request.Body, request.Binding, request.Expectation)
		if err != nil {
			return providertransport.Result{}, err
		}
		if _, err := providergateway.BeginWithExpectation(request.GatewayJournalPath, request.AccessJournalPath, request.Policy, request.Intent, request.Binding, metadata.SHA256, metadata.SizeBytes, request.Expectation); err != nil {
			return providertransport.Result{}, err
		}
		return providertransport.Result{}, providertransport.ErrPending
	}
	running, err := fixture.server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	status, raw := proxyPost(t, running.URL(), proxyBearer, fixture.body, nil)
	if status != http.StatusBadGateway || strings.Contains(string(raw), "fixture-upstream-secret") {
		t.Fatal("pending failure was exposed incorrectly", status, string(raw))
	}
	status, _ = proxyPost(t, running.URL(), proxyBearer, fixture.body, nil)
	state, inspectErr := providergateway.Inspect(fixture.gatewayPath)
	if status != http.StatusConflict || calls.Load() != 1 || inspectErr != nil || state.Pending == nil {
		t.Fatal("pending provider call was eligible for resend", status, calls.Load(), state, inspectErr)
	}
	closeProxy(t, running)
}

func TestConcurrentRequestRejectedAndCloseWaitsForAdmittedHandler(t *testing.T) {
	fixture := newProxyFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	response := []byte("data: settled\n\n")
	fixture.server.execute = func(_ context.Context, request providertransport.Request) (providertransport.Result, error) {
		close(started)
		<-release
		return completeProxyCall(request, response, "stop")
	}
	running, err := fixture.server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan int, 1)
	go func() {
		status, _ := proxyPost(t, running.URL(), proxyBearer, fixture.body, nil)
		firstDone <- status
	}()
	<-started
	status, _ := proxyPost(t, running.URL(), proxyBearer, fixture.body, nil)
	if status != http.StatusConflict {
		t.Fatal("concurrent provider request was queued or admitted", status)
	}
	closeDone := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { closeDone <- running.Close(ctx) }()
	select {
	case err := <-closeDone:
		t.Fatal("Close returned before admitted handler settled", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if status := <-firstDone; status != http.StatusOK {
		t.Fatal("admitted handler did not complete", status)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := running.Wait(); err != nil {
		t.Fatal(err)
	}
}

func completeProxyCall(request providertransport.Request, response []byte, finish string) (providertransport.Result, error) {
	metadata, err := providergateway.ValidateAdapterRequest(request.Body, request.Binding, request.Expectation)
	if err != nil {
		return providertransport.Result{}, err
	}
	call, err := providergateway.BeginWithExpectation(request.GatewayJournalPath, request.AccessJournalPath, request.Policy, request.Intent, request.Binding, metadata.SHA256, metadata.SizeBytes, request.Expectation)
	if err != nil {
		return providertransport.Result{}, err
	}
	receipt := providergateway.CallReceipt{Version: 1, BindingID: call.BindingID, InvocationID: call.InvocationID, CallID: call.CallID, ResponseSHA256: proxyDigest(string(response)), ResponseBytes: int64(len(response)), ResponseID: "response_fixture", ObservedModel: request.Binding.Model.Model, Finish: finish, StreamComplete: true, UsageComplete: true, Usage: providergateway.Usage{InputTokens: 3, OutputTokens: 2}, Semantic: &providergateway.ResponseSemanticProjection{Version: 1, Kind: "assistant-turn", ToolCalls: []providergateway.ResponseToolIdentity{}, OutputTextSHA256: proxyDigest("")}, RequestExpectationID: call.RequestExpectationID}
	if err := providergateway.Complete(request.GatewayJournalPath, request.AccessJournalPath, request.Policy, request.Intent, request.Binding, call, receipt); err != nil {
		return providertransport.Result{}, err
	}
	return providertransport.Result{Body: append([]byte(nil), response...), Receipt: receipt}, nil
}

func proxyPost(t *testing.T, endpoint, bearer string, body []byte, extra http.Header) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	setProxyHeaders(request, bearer)
	for name, values := range extra {
		request.Header[name] = append([]string(nil), values...)
	}
	response, err := localClient().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, raw
}

func setProxyHeaders(request *http.Request, bearer string) {
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "*/*")
}

func localClient() *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: nil}}
}

func closeProxy(t *testing.T, running *Running) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := running.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := running.Wait(); err != nil {
		t.Fatal(err)
	}
}

func proxyChatSSE() []byte {
	return []byte("data: {\"id\":\"chatcmpl-proxy\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"wire-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-proxy\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"wire-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-proxy\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"wire-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n" +
		"data: [DONE]\n\n")
}

func proxyDigest(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
