package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"harness.local/engorch/internal/canonical"
)

const (
	providerProbeDigest = "88d2fa691b2d9e32fde6d1039382a850ddf96fe49cd41683c6375fe1dc8ec2a5"
	providerProbeModel  = "wire-model"
	providerProbeLimit  = int64(73)
)

type providerWireProbe struct {
	mu       sync.Mutex
	requests []providerWireRequest
}

type providerWireRequest struct {
	Method        string
	Path          string
	Authorization string
	Body          map[string]json.RawMessage
}

func (p *providerWireProbe) snapshot() []providerWireRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]providerWireRequest(nil), p.requests...)
}

func (p *providerWireProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var body map[string]json.RawMessage
	decodeErr := json.NewDecoder(r.Body).Decode(&body)
	p.mu.Lock()
	p.requests = append(p.requests, providerWireRequest{
		Method: r.Method, Path: r.URL.EscapedPath(), Authorization: r.Header.Get("Authorization"), Body: body,
	})
	p.mu.Unlock()
	if decodeErr != nil || r.Method != http.MethodPost || r.URL.EscapedPath() != "/v1/chat/completions" || r.URL.RawQuery != "" {
		http.Error(w, "unexpected fixture request", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	chunks := []string{
		`{"id":"chatcmpl-engorch","object":"chat.completion.chunk","created":1,"model":"wire-model","choices":[{"index":0,"delta":{"role":"assistant","content":"synthetic provider response"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-engorch","object":"chat.completion.chunk","created":1,"model":"wire-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-engorch","object":"chat.completion.chunk","created":1,"model":"wire-model","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
	}
	for _, chunk := range chunks {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		if flush, ok := w.(http.Flusher); ok {
			flush.Flush()
		}
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestPinnedProviderWireProbe(t *testing.T) {
	if os.Getenv("ENGORCH_OPENCODE_PROVIDER_PROBE") != "1" {
		t.Skip("explicit synthetic provider probe opt-in required")
	}
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(repository, ".local", "toolchains", "opencode-1.18.29", "opencode.exe")

	provider := &providerWireProbe{}
	providerServer := httptest.NewServer(provider)
	defer providerServer.Close()
	if !strings.HasPrefix(providerServer.URL, "http://127.0.0.1:") {
		t.Fatal("fixture provider is not bound to explicit IPv4 loopback")
	}

	root := t.TempDir()
	for _, name := range []string{"home", "config", "data", "cache", "state", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	configDirectory := filepath.Join(root, "config", "opencode")
	if err := os.Mkdir(configDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"share":      "disabled",
		"plugin":     []string{},
		"permission": map[string]string{"*": "deny"},
		"provider": map[string]any{
			"engorch-fixture": map[string]any{
				"npm": "@ai-sdk/openai-compatible", "name": "EngOrch loopback fixture",
				"options": map[string]string{"baseURL": providerServer.URL + "/v1", "apiKey": "fixture-provider-key"},
				"models": map[string]any{providerProbeModel: map[string]any{
					"name": "Wire fixture model", "limit": map[string]int64{"context": 8192, "output": 4096},
				}},
			},
		},
	}
	configBytes, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDirectory, "opencode.json"), configBytes, 0600); err != nil {
		t.Fatal(err)
	}

	port := providerProbePort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	output := &probeOutput{}
	process, _, attempts, err := StartReadyLimitedProcess(ctx, binary, providerProbeDigest, root, port, "fixture", "fixture-server-secret", output, providerProbeLimit, 2, 14*time.Second)
	if err != nil {
		t.Fatalf("pinned OpenCode startup failed: %v; attempts: %+v; output: %s", err, attempts, output.String())
	}
	defer func() {
		_ = process.Close()
		select {
		case <-process.Done():
		default:
			t.Error("owned OpenCode process was not reaped")
		}
	}()

	client, err := NewClient("http://127.0.0.1:"+strconv.Itoa(port), "fixture", "fixture-server-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	projectRaw, err := client.read(ctx, "/project/current")
	if err != nil {
		t.Fatal("read isolated project:", err)
	}
	project, err := wireObject(projectRaw)
	if err != nil {
		t.Fatal(err)
	}
	var projectID string
	if field(project, "id", &projectID) != nil || projectID == "" {
		t.Fatal("project identity missing")
	}

	sessionBinding := SessionBinding{
		IntentID: strings.Repeat("a", 64), ProjectID: projectID, Directory: root,
		Agent: "build", Provider: "engorch-fixture", Model: providerProbeModel,
	}
	sessionID, err := client.CreateSession(ctx, filepath.Join(root, "session.jsonl"), sessionBinding)
	if err != nil {
		t.Fatal("create admitted fixture session:", err)
	}
	prompt := "Return the synthetic fixture response without using tools."
	intent := DispatchIntent{Binding: Binding{
		SessionID: sessionID, ParentID: "msg_engorchprovider", Provider: sessionBinding.Provider,
		Model: sessionBinding.Model, Agent: sessionBinding.Agent, Directory: root, Root: "/",
	}, Text: prompt}
	dispatchJournal := filepath.Join(root, "dispatch.jsonl")
	if err := submitSynchronousProviderProbe(ctx, client, dispatchJournal, intent); err != nil {
		t.Fatalf("submit one admitted synchronous prompt: %v; provider requests: %d; private logs: %s", err, len(provider.snapshot()), providerProbeLogs(root))
	}
	assertProviderProbeWire(t, provider.snapshot())
	if err := client.ReadPrompt(ctx, intent.Binding, intent.Text); err != nil {
		raw, _ := client.read(ctx, "/session/"+sessionID+"/message/"+intent.Binding.ParentID)
		if len(raw) > 4096 {
			raw = raw[:4096]
		}
		t.Fatalf("read exact persisted prompt: %v; message: %s", err, raw)
	}

	var observation TextTurnObservation
	var lastTranscript []byte
	for {
		if raw, readErr := client.read(ctx, "/session/"+sessionID+"/message"); readErr == nil {
			lastTranscript = raw
		}
		observation, err = client.ObserveTextTurn(ctx, dispatchJournal, intent)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			diagnosticContext, stopDiagnostic := context.WithTimeout(context.Background(), 2*time.Second)
			raw, readErr := client.read(diagnosticContext, "/session/"+sessionID+"/message")
			stopDiagnostic()
			if len(raw) > 4096 {
				raw = raw[:4096]
			}
			if len(lastTranscript) > 4096 {
				lastTranscript = lastTranscript[:4096]
			}
			t.Fatalf("synthetic turn was not persisted: %v; provider requests: %d; last transcript: %s; final transcript: %s (%v); output: %s", err, len(provider.snapshot()), lastTranscript, raw, readErr, output.String())
		case <-process.Done():
			t.Fatalf("OpenCode exited during provider probe: %v; output: %s", process.Wait(), output.String())
		case <-time.After(100 * time.Millisecond):
		}
	}
	if observation.Text != "synthetic provider response" || observation.Assistant.Binding != intent.Binding {
		t.Fatal("validated observation differs from synthetic provider response")
	}
	transcript, err := client.ReadTranscript(ctx, sessionID)
	if err != nil || len(transcript) != 2 || transcript[0].Role != "user" || transcript[1].Role != "assistant" {
		t.Fatal("validated transcript differs from one text turn", err)
	}

	assertProviderProbeWire(t, provider.snapshot())
}

func assertProviderProbeWire(t *testing.T, requests []providerWireRequest) {
	t.Helper()
	if len(requests) != 1 {
		t.Fatalf("provider received %d requests; expected exactly one: %+v", len(requests), requests)
	}
	request := requests[0]
	if request.Method != http.MethodPost || request.Path != "/v1/chat/completions" || request.Authorization != "Bearer fixture-provider-key" {
		t.Fatal("provider request endpoint or fixture authorization changed")
	}
	var model string
	var maxTokens int64
	var stream bool
	if json.Unmarshal(request.Body["model"], &model) != nil || model != providerProbeModel ||
		json.Unmarshal(request.Body["max_tokens"], &maxTokens) != nil || maxTokens != providerProbeLimit ||
		json.Unmarshal(request.Body["stream"], &stream) != nil || !stream {
		t.Fatalf("provider wire did not bind model/output limit/stream: model=%q max_tokens=%d stream=%v", model, maxTokens, stream)
	}
}

func providerProbeLogs(root string) string {
	entries, err := os.ReadDir(filepath.Join(root, "data", "opencode", "log"))
	if err != nil {
		return "unavailable"
	}
	var result strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") || result.Len() >= 16<<10 {
			continue
		}
		file, openErr := os.Open(filepath.Join(root, "data", "opencode", "log", entry.Name()))
		if openErr != nil {
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(file, int64((16<<10)-result.Len())))
		_ = file.Close()
		result.Write(raw)
	}
	return strings.ReplaceAll(strings.ReplaceAll(result.String(), "fixture-provider-key", "[redacted]"), "fixture-server-secret", "[redacted]")
}

func submitSynchronousProviderProbe(ctx context.Context, client *Client, journalPath string, intent DispatchIntent) error {
	if err := validatePrompt(intent.Binding, intent.Text); err != nil {
		return err
	}
	if err := appendDispatch(journalPath, "opencode.dispatch-intent", intent); err != nil {
		return err
	}
	body, err := canonical.Bytes(map[string]any{
		"messageID": intent.Binding.ParentID, "agent": intent.Binding.Agent, "variant": intent.Binding.Variant,
		"model": map[string]string{"providerID": intent.Binding.Provider, "modelID": intent.Binding.Model},
		"parts": []map[string]string{{"type": "text", "text": intent.Text}},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.base+"/session/"+intent.Binding.SessionID+"/message", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(client.user, client.password)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := client.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if readErr != nil || len(responseBody) > 1<<20 || response.StatusCode != http.StatusOK {
		diagnostic := strings.ReplaceAll(string(responseBody), "fixture-provider-key", "[redacted]")
		if len(diagnostic) > 4096 {
			diagnostic = diagnostic[:4096]
		}
		return fmt.Errorf("synchronous OpenCode prompt failed with status %d: %s", response.StatusCode, diagnostic)
	}
	if len(responseBody) > 0 && !json.Valid(responseBody) {
		return fmt.Errorf("synchronous OpenCode prompt returned invalid JSON")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return appendDispatch(journalPath, "opencode.dispatch-accepted", intent.Binding)
}

func providerProbePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}
