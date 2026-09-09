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
	"sync"
	"testing"
	"time"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/providercredential"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/providertransport"
)

const (
	integrationLocalBearer = "fixture-local-proxy-bearer-transport-integration"
	integrationProviderKey = "fixture-production-transport-secret"
)

type productionProxyFixture struct {
	server      *Server
	accessPath  string
	gatewayPath string
	body        []byte
}

type productionProtocol struct {
	adapterID           string
	adapterCapabilities json.RawMessage
	modelCapabilities   *providergateway.ModelCapabilities
	expectation         providergateway.AdapterRequestExpectation
	body                []byte
	maxCalls            int
	maxResponseBytes    int64
}

func TestProductionProxyChatAllowsExactTwoStepToolLoop(t *testing.T) {
	firstBody, secondBody, tool := integrationChatBodies()
	var mu sync.Mutex
	paths := []string{}
	authorizations := []string{}
	bodies := [][]byte{}
	upstreamCalls := 0
	upstream := newProductionTLSServer(t, func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		mu.Lock()
		upstreamCalls++
		call := upstreamCalls
		paths = append(paths, request.URL.RequestURI())
		authorizations = append(authorizations, request.Header.Get("authorization"))
		bodies = append(bodies, append([]byte(nil), raw...))
		mu.Unlock()
		writer.Header().Set("content-type", "text/event-stream")
		if call == 1 {
			_, _ = writer.Write(integrationChatToolSSE())
			return
		}
		_, _ = writer.Write(integrationChatStopSSE("chatcmpl-second"))
	})
	protocol := productionProtocol{
		adapterID: providergateway.OpenAIChatCompletionsAdapter, adapterCapabilities: json.RawMessage("{}"),
		modelCapabilities: &providergateway.ModelCapabilities{Tools: true, OutputCap: true, CompleteUsage: true},
		expectation:       providergateway.AdapterRequestExpectation{MaxBytes: 4096, MaxOutputTokens: 5, Tools: []providergateway.RequestTool{tool}, Controls: json.RawMessage("{}")},
		body:              firstBody, maxCalls: 2, maxResponseBytes: 16 << 10,
	}
	fixture := newProductionProxyFixture(t, upstream, "/deployments/opaque-model/chat?api-version=2026-09-01", protocol)
	running := listenProductionProxy(t, fixture.server)
	if !strings.HasSuffix(running.URL(), "/v1/chat/completions") || strings.Contains(running.URL(), "deployments") {
		t.Fatal("local codec path exposed the arbitrary upstream route", running.URL())
	}

	status, raw := productionProxyPost(t, running.URL(), firstBody)
	if status != http.StatusOK || !bytes.Equal(raw, integrationChatToolSSE()) {
		t.Fatal("first production tool call did not cross the proxy", status, string(raw))
	}
	state, err := providergateway.Inspect(fixture.gatewayPath)
	expectationID, expectationErr := providergateway.AdapterRequestExpectationID(fixture.server.gateway, fixture.server.expectation)
	if err != nil || expectationErr != nil || state.Pending != nil || state.Finished || state.Exhausted || len(state.Calls) != 1 || state.Calls[0].Intent.RequestExpectationID != expectationID || state.Calls[0].Receipt == nil || state.Calls[0].Receipt.Finish != "tool_calls" {
		t.Fatal("first tool call was not durably available for continuation", state, err)
	}
	status, raw = productionProxyPost(t, running.URL(), secondBody)
	if status != http.StatusOK || !bytes.Equal(raw, integrationChatStopSSE("chatcmpl-second")) {
		t.Fatal("second production stop call did not cross the proxy", status, string(raw))
	}
	state, err = providergateway.Inspect(fixture.gatewayPath)
	if err != nil || state.Pending != nil || !state.Finished || len(state.Calls) != 2 || state.Calls[1].Intent.RequestExpectationID != expectationID || state.Calls[1].Receipt == nil || state.Calls[1].Receipt.Finish != "stop" {
		t.Fatal("second completion was not durable before proxy return", state, err)
	}
	status, _ = productionProxyPost(t, running.URL(), secondBody)
	mu.Lock()
	defer mu.Unlock()
	if status != http.StatusConflict || upstreamCalls != 2 || len(paths) != 2 || paths[0] != "/deployments/opaque-model/chat?api-version=2026-09-01" || paths[1] != paths[0] || authorizations[0] != "Bearer "+integrationProviderKey || authorizations[1] != authorizations[0] || !bytes.Equal(bodies[0], firstBody) || !bytes.Equal(bodies[1], secondBody) {
		t.Fatal("two-call production route, authentication, or body identity changed", status, upstreamCalls, paths)
	}
}

func TestProductionProxyResponsesPreservesFractionalReasoningToolContract(t *testing.T) {
	protocol := integrationResponsesProtocol(t)
	response := integrationResponsesToolSSE()
	var mu sync.Mutex
	upstreamCalls := 0
	var upstreamPath string
	var upstreamBody []byte
	upstream := newProductionTLSServer(t, func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		mu.Lock()
		upstreamCalls++
		upstreamPath = request.URL.RequestURI()
		upstreamBody = append([]byte(nil), raw...)
		mu.Unlock()
		writer.Header().Set("content-type", "text/event-stream")
		_, _ = writer.Write(response)
	})
	fixture := newProductionProxyFixture(t, upstream, "/accounts/fixture/models/opaque:respond?api-version=2026-09-01", protocol)
	running := listenProductionProxy(t, fixture.server)
	if !strings.HasSuffix(running.URL(), "/v1/responses") || strings.Contains(running.URL(), "accounts") {
		t.Fatal("Responses local codec path exposed the arbitrary upstream route", running.URL())
	}
	status, raw := productionProxyPost(t, running.URL(), protocol.body)
	if status != http.StatusOK || !bytes.Equal(raw, response) {
		t.Fatal("production Responses stream did not cross the proxy", status, string(raw))
	}
	state, err := providergateway.Inspect(fixture.gatewayPath)
	expectationID, expectationErr := providergateway.AdapterRequestExpectationID(fixture.server.gateway, fixture.server.expectation)
	if err != nil || expectationErr != nil || state.Pending != nil || !state.Exhausted || len(state.Calls) != 1 || state.Calls[0].Intent.RequestExpectationID != expectationID || state.Calls[0].Receipt == nil || state.Calls[0].Receipt.Finish != "tool_calls" || state.Calls[0].Receipt.Usage.ReasoningTokens == nil || *state.Calls[0].Receipt.Usage.ReasoningTokens != 2 {
		t.Fatal("bounded Responses reasoning/tool receipt was not durable", state, err)
	}
	semantic := state.Calls[0].Receipt.Semantic
	if semantic == nil || semantic.OutputTextSHA256 != integrationDigest("Found.") || len(semantic.ToolCalls) != 1 || semantic.ToolCalls[0].ID != "call_fixture" || semantic.ToolCalls[0].Name != "engorch_lookup" || semantic.ToolCalls[0].ArgumentsSHA256 != integrationDigest(`{"city":"Iași"}`) {
		t.Fatal("text/tool semantic projection did not survive proxy completion", semantic)
	}
	semantic.ToolCalls[0].Name = "caller-mutation"
	replayed, err := providergateway.Inspect(fixture.gatewayPath)
	if err != nil || replayed.Calls[0].Receipt.Semantic.ToolCalls[0].Name != "engorch_lookup" {
		t.Fatal("caller mutation aliased durable semantic replay", replayed, err)
	}
	status, _ = productionProxyPost(t, running.URL(), protocol.body)
	mu.Lock()
	defer mu.Unlock()
	if status != http.StatusConflict || upstreamCalls != 1 || upstreamPath != "/accounts/fixture/models/opaque:respond?api-version=2026-09-01" || !bytes.Equal(upstreamBody, protocol.body) {
		t.Fatal("Responses one-call bound, upstream route, or body changed", status, upstreamCalls, upstreamPath)
	}
}

func TestProductionProxyPendingResponseBlocksRetry(t *testing.T) {
	firstBody, _, tool := integrationChatBodies()
	var mu sync.Mutex
	upstreamCalls := 0
	upstream := newProductionTLSServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		upstreamCalls++
		mu.Unlock()
		writer.Header().Set("content-type", "text/event-stream")
		_, _ = writer.Write([]byte("data: truncated"))
	})
	protocol := productionProtocol{
		adapterID: providergateway.OpenAIChatCompletionsAdapter, adapterCapabilities: json.RawMessage("{}"),
		modelCapabilities: &providergateway.ModelCapabilities{Tools: true, OutputCap: true, CompleteUsage: true},
		expectation:       providergateway.AdapterRequestExpectation{MaxBytes: 4096, MaxOutputTokens: 5, Tools: []providergateway.RequestTool{tool}, Controls: json.RawMessage("{}")},
		body:              firstBody, maxCalls: 2, maxResponseBytes: 4096,
	}
	fixture := newProductionProxyFixture(t, upstream, "/unknown-provider/execute", protocol)
	running := listenProductionProxy(t, fixture.server)
	status, raw := productionProxyPost(t, running.URL(), firstBody)
	if status != http.StatusBadGateway || bytes.Contains(raw, []byte(integrationProviderKey)) || bytes.Contains(raw, firstBody) {
		t.Fatal("unresolved provider stream escaped the proxy boundary", status, string(raw))
	}
	state, err := providergateway.Inspect(fixture.gatewayPath)
	if err != nil || state.Pending == nil || len(state.Calls) != 1 || state.Calls[0].Receipt != nil {
		t.Fatal("malformed production response did not remain pending", state, err)
	}
	status, _ = productionProxyPost(t, running.URL(), firstBody)
	mu.Lock()
	defer mu.Unlock()
	if status != http.StatusConflict || upstreamCalls != 1 {
		t.Fatal("pending production call was retried", status, upstreamCalls)
	}
}

func newProductionProxyFixture(t *testing.T, upstream *httptest.Server, upstreamPath string, protocol productionProtocol) productionProxyFixture {
	t.Helper()
	root := t.TempDir()
	profile := access.Profile{Version: 1, Name: "production-transport", Kind: "api", Runtime: "provider-http", Provider: "fixture", CredentialRef: "fixture-key", RepositoryClasses: []access.Class{access.Public}}
	profileID, err := profile.ID()
	if err != nil {
		t.Fatal(err)
	}
	route := access.Route{Version: 1, Role: "explorer", Runtime: "provider-http", Provider: "fixture", Model: "wire-model", Effort: "low", AccessID: profileID, Permission: "read-only"}
	reservedTokens := int64(protocol.maxCalls * 25)
	cost := int64(protocol.maxCalls)
	policy := access.Policy{Version: 1, RunID: integrationDigest("run:" + root), Class: access.Public, Limits: access.Limits{Tokens: reservedTokens, CostMicroUSD: &cost, Concurrency: 1}, Routes: []access.Route{route}, Profiles: []access.Profile{profile}}
	policyID, err := policy.ID()
	if err != nil {
		t.Fatal(err)
	}
	intent := access.Intent{Attempt: 1, PolicyID: policyID, InputHash: integrationDigest("input:" + root), Route: route, Reservation: access.Reservation{Tokens: reservedTokens, CostMicroUSD: &cost, BillingMode: "api"}}
	intent.Reservation.InvocationID, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	accessPath := filepath.Join(root, "access.jsonl")
	if err := access.ReserveDurable(accessPath, policy, intent); err != nil {
		t.Fatal(err)
	}
	endpoint := providergateway.EndpointContract{
		Version: 2, Provider: "fixture", URL: upstream.URL + upstreamPath, AdapterID: protocol.adapterID,
		Auth:          &providergateway.AuthContract{Scheme: "bearer", CredentialRef: "fixture-key"},
		PublicHeaders: map[string]string{"x-engorch-public-route": "fixture"}, SessionHeader: "x-engorch-session",
	}
	model := providergateway.ModelContract{
		Version: 2, Provider: "fixture", Model: "wire-model", AdapterID: protocol.adapterID,
		Capabilities: protocol.modelCapabilities, AdapterCapabilities: protocol.adapterCapabilities,
		ContextWindowTokens: 20, MaxCalls: protocol.maxCalls, MaxRequestBytes: 4096,
		MaxResponseBytes: protocol.maxResponseBytes, MaxOutputTokens: 5,
		Pricing: &providergateway.PricingPolicy{Currency: "USD", Unit: "micro_usd_per_million_tokens", MaxInputMicroUSDPerMillion: 1, MaxOutputMicroUSDPerMillion: 1},
	}
	gatewayPath := filepath.Join(root, "gateway.jsonl")
	gateway, err := providergateway.Bind(gatewayPath, accessPath, policy, intent, endpoint, model)
	if err != nil {
		t.Fatal(err)
	}
	source, err := providercredential.NewEnvironmentSource(map[string]string{"fixture-key": "FIXTURE_PROVIDER_KEY"}, func(name string) (string, bool) {
		return integrationProviderKey, name == "FIXTURE_PROVIDER_KEY"
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
	roots.AddCert(upstream.Certificate())
	transport, err := providertransport.NewClient(providertransport.ClientOptions{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{
		AccessJournalPath: accessPath, GatewayJournalPath: gatewayPath, Policy: policy, Intent: intent,
		Gateway: gateway, Credential: lease, Transport: transport, Bearer: integrationLocalBearer,
		Expectation: protocol.expectation, HandlerTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return productionProxyFixture{server: server, accessPath: accessPath, gatewayPath: gatewayPath, body: protocol.body}
}

func listenProductionProxy(t *testing.T, server *Server) *Running {
	t.Helper()
	running, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := running.Close(ctx); err != nil {
			t.Errorf("close production proxy: %v", err)
		}
		if err := running.Wait(); err != nil {
			t.Errorf("wait production proxy: %v", err)
		}
	})
	return running
}

func newProductionTLSServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = false
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func productionProxyPost(t *testing.T, endpoint string, body []byte) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("authorization", "Bearer "+integrationLocalBearer)
	request.Header.Set("content-type", "application/json")
	request.Header.Set("accept", "text/event-stream")
	response, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(request)
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

func integrationChatBodies() ([]byte, []byte, providergateway.RequestTool) {
	tool := providergateway.RequestTool{Name: "engorch_lookup", Description: "Read one admitted city record.", Parameters: json.RawMessage(`{"additionalProperties":false,"properties":{"city":{"type":"string"}},"required":["city"],"type":"object"}`)}
	first := []byte(`{"model":"wire-model","messages":[{"role":"user","content":"Look up Iași."}],"max_tokens":5,"stream":true,"stream_options":{"include_usage":true},"tools":[{"type":"function","function":{"name":"engorch_lookup","description":"Read one admitted city record.","parameters":{"additionalProperties":false,"properties":{"city":{"type":"string"}},"required":["city"],"type":"object"}}}],"tool_choice":"auto"}`)
	second := []byte(`{"model":"wire-model","messages":[{"role":"user","content":"Look up Iași."},{"role":"assistant","content":null,"tool_calls":[{"id":"call_fixture","type":"function","function":{"name":"engorch_lookup","arguments":"{\"city\":\"Iași\"}"}}]},{"role":"tool","content":"{\"population\":271692}","tool_call_id":"call_fixture"}],"max_tokens":5,"stream":true,"stream_options":{"include_usage":true},"tools":[{"type":"function","function":{"name":"engorch_lookup","description":"Read one admitted city record.","parameters":{"additionalProperties":false,"properties":{"city":{"type":"string"}},"required":["city"],"type":"object"}}}],"tool_choice":"auto"}`)
	return first, second, tool
}

func integrationChatToolSSE() []byte {
	return integrationChatStream("chatcmpl-first",
		`{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_fixture","type":"function","function":{"name":"engorch_lookup","arguments":"{\"city\":\"Iași\"}"}}]}`,
		"tool_calls")
}

func integrationChatStopSSE(id string) []byte {
	return integrationChatStream(id, `{"role":"assistant","content":"Done."}`, "stop")
}

func integrationChatStream(id, delta, finish string) []byte {
	return []byte("data: {\"id\":\"" + id + "\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"wire-model\",\"choices\":[{\"index\":0,\"delta\":" + delta + ",\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"" + id + "\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"wire-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"" + finish + "\"}]}\n\n" +
		"data: {\"id\":\"" + id + "\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"wire-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n" +
		"data: [DONE]\n\n")
}

func integrationResponsesProtocol(t *testing.T) productionProtocol {
	t.Helper()
	temperatureMinimum, temperatureMaximum := "0.1", "0.9"
	topPMinimum, topPMaximum := "0.2", "1"
	adapterCapabilities, err := canonical.Bytes(providergateway.ResponsesModelCapabilities{
		Version: 1, FunctionTools: true, Reasoning: true, SystemRoles: []string{"developer"}, Sampling: true,
		TemperatureMin: &temperatureMinimum, TemperatureMax: &temperatureMaximum, TopPMin: &topPMinimum, TopPMax: &topPMaximum,
		TextFormats: []string{"plain"}, KnownExtensions: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	temperature, topP, parallel := json.Number("0.50"), json.Number("0.75"), false
	controls, err := json.Marshal(providergateway.ResponsesRequestExpectation{
		MaxOutputTokens: 5, StateMode: "full-input-stateless", SystemRole: "developer",
		ReasoningEffort: "high", ReasoningSummary: "auto", ToolChoice: "auto", ParallelToolCalls: &parallel,
		Temperature: &temperature, TopP: &topP, Include: []string{"reasoning.encrypted_content"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tool := providergateway.RequestTool{Name: "engorch_lookup", Description: "Read one admitted city record.", Parameters: json.RawMessage(`{"additionalProperties":false,"properties":{"city":{"type":"string"}},"required":["city"],"type":"object"}`)}
	body := []byte(`{"model":"wire-model","input":[{"role":"developer","content":"Use only admitted tools."},{"role":"user","content":[{"type":"input_text","text":"Look up Iași."}]}],"max_output_tokens":5,"stream":true,"store":false,"reasoning":{"effort":"high","summary":"auto"},"include":["reasoning.encrypted_content"],"tools":[{"type":"function","name":"engorch_lookup","description":"Read one admitted city record.","parameters":{"additionalProperties":false,"properties":{"city":{"type":"string"}},"required":["city"],"type":"object"},"strict":false}],"tool_choice":"auto","parallel_tool_calls":false,"temperature":0.50,"top_p":0.75}`)
	return productionProtocol{
		adapterID: providergateway.OpenAIResponsesAdapter, adapterCapabilities: adapterCapabilities,
		modelCapabilities: &providergateway.ModelCapabilities{Tools: true, Reasoning: true, OutputCap: true, CompleteUsage: true},
		expectation:       providergateway.AdapterRequestExpectation{MaxBytes: 4096, MaxOutputTokens: 5, Tools: []providergateway.RequestTool{tool}, Controls: controls},
		body:              body, maxCalls: 1, maxResponseBytes: 16 << 10,
	}
}

func integrationResponsesToolSSE() []byte {
	return integrationResponsesEvents(
		`{"type":"response.created","response":`+integrationResponsesSnapshot("in_progress", `[]`, `null`)+`,"sequence_number":10}`,
		`{"type":"response.in_progress","response":`+integrationResponsesSnapshot("in_progress", `[]`, `null`)+`,"sequence_number":20}`,
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
		`{"type":"response.output_item.added","output_index":2,"item":{"id":"msg_fixture","type":"message","status":"in_progress","content":[],"role":"assistant"},"sequence_number":91}`,
		`{"type":"response.content_part.added","content_index":0,"item_id":"msg_fixture","output_index":2,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""},"sequence_number":92}`,
		`{"type":"response.output_text.delta","content_index":0,"delta":"Found.","item_id":"msg_fixture","logprobs":[],"obfuscation":"z","output_index":2,"sequence_number":93}`,
		`{"type":"response.output_text.done","content_index":0,"item_id":"msg_fixture","logprobs":[],"output_index":2,"sequence_number":94,"text":"Found."}`,
		`{"type":"response.content_part.done","content_index":0,"item_id":"msg_fixture","output_index":2,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":"Found."},"sequence_number":95}`,
		`{"type":"response.output_item.done","output_index":2,"item":{"id":"msg_fixture","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"Found."}],"role":"assistant"},"sequence_number":96}`,
		`{"type":"response.completed","response":`+integrationResponsesSnapshot("completed", `[{"id":"rs_fixture","type":"reasoning","encrypted_content":"terminal-cipher","summary":[{"type":"summary_text","text":"private plan"}]},{"id":"fc_fixture","type":"function_call","status":"completed","arguments":"{\"city\":\"Iași\"}","call_id":"call_fixture","name":"engorch_lookup"},{"id":"msg_fixture","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"Found."}],"role":"assistant"}]`, `{"input_tokens":18,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":1},"output_tokens":2,"output_tokens_details":{"reasoning_tokens":2},"total_tokens":20}`)+`,"sequence_number":100}`,
	)
}

func integrationResponsesSnapshot(status, output, usage string) string {
	return `{"id":"resp_fixture","object":"response","created_at":123,"status":"` + status + `","model":"wire-model","output":` + output + `,"usage":` + usage + `}`
}

func integrationResponsesEvents(events ...string) []byte {
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

func integrationDigest(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}
