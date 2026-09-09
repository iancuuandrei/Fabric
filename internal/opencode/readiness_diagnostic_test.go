package opencode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type readinessDiagnosticHTTP struct {
	Outcome               string `json:"outcome"`
	ConnectCompleted      bool   `json:"connect_completed"`
	RequestWritten        bool   `json:"request_written"`
	FirstResponseByte     bool   `json:"first_response_byte"`
	ResponseBodyCompleted bool   `json:"response_body_completed"`
	StatusCode            int    `json:"status_code,omitempty"`
	ElapsedMillis         int64  `json:"elapsed_ms"`
}

type readinessDiagnosticSample struct {
	ElapsedMillis int64                   `json:"elapsed_ms"`
	ProcessState  string                  `json:"process_state"`
	TCP           string                  `json:"tcp"`
	Health        readinessDiagnosticHTTP `json:"health"`
	Config        readinessDiagnosticHTTP `json:"config"`
}

type readinessDiagnosticAttempt struct {
	Attempt             int                         `json:"attempt"`
	PrivateProxyEntries int                         `json:"private_proxy_entries"`
	ProviderRequests    int64                       `json:"provider_requests"`
	Samples             []readinessDiagnosticSample `json:"samples"`
	FinalProcessState   string                      `json:"final_process_state"`
}

// TestPinnedOpenCodeReadinessPhases is an opt-in diagnostic, not an admission
// test. It observes TCP acceptance, global health, and configuration on each
// same pinned process with fresh no-proxy clients and explicit phase deadlines.
// It never dispatches a session or sends a provider request.
func TestPinnedOpenCodeReadinessPhases(t *testing.T) {
	if os.Getenv("ENGORCH_OPENCODE_READINESS_DIAGNOSTIC") != "1" {
		t.Skip("explicit OpenCode readiness diagnostic opt-in required")
	}
	binary := os.Getenv("ENGORCH_OPENCODE_PROBE_BINARY")
	digest := os.Getenv("ENGORCH_OPENCODE_PROBE_SHA256")
	if !filepath.IsAbs(binary) || len(digest) != 64 {
		t.Fatal("explicit pinned OpenCode binary identity required")
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != digest {
		t.Fatal("binary pin mismatch")
	}
	attempts := 1
	if value := os.Getenv("ENGORCH_OPENCODE_READINESS_ATTEMPTS"); value != "" {
		attempts, err = strconv.Atoi(value)
		if err != nil || attempts < 1 || attempts > 4 {
			t.Fatal("readiness attempts must be between one and four")
		}
	}

	results := make([]readinessDiagnosticAttempt, 0, attempts)
	for attempt := 1; attempt <= attempts; attempt++ {
		results = append(results, runPinnedReadinessDiagnosticAttempt(t, attempt, binary, digest))
	}
	report, err := json.MarshalIndent(struct {
		Schema           string                       `json:"schema"`
		ExecutableSHA256 string                       `json:"executable_sha256"`
		Attempts         []readinessDiagnosticAttempt `json:"attempts"`
	}{"engorch.opencode-readiness-phases.v1", digest, results}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if output := os.Getenv("ENGORCH_OPENCODE_READINESS_REPORT"); output != "" {
		if !filepath.IsAbs(output) {
			t.Fatal("readiness report path must be absolute")
		}
		if err := os.WriteFile(output, report, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("OPENCODE_READINESS_PHASE_RESULT %s", report)
}

func runPinnedReadinessDiagnosticAttempt(t *testing.T, number int, binary, digest string) readinessDiagnosticAttempt {
	t.Helper()
	stateRoot, workspace := t.TempDir(), t.TempDir()
	for _, name := range []string{"home", "config", "data", "cache", "state", "tmp"} {
		if err := os.Mkdir(filepath.Join(stateRoot, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	git := func(arguments ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", workspace}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git readiness fixture failed: %v: %s", err, output)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("committed-only-readiness-diagnostic"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "source")
	if err := os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("dirty-readiness-diagnostic"), 0600); err != nil {
		t.Fatal(err)
	}
	providerCalls := &atomic.Int64{}
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		http.Error(w, "diagnostic forbids provider dispatch", http.StatusForbidden)
	}))
	defer providerServer.Close()
	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)
	}))
	defer mcpServer.Close()
	provider := ProviderConfigurationSpec{
		ProviderID: "engorch-diagnostic", ModelID: "wire-responses-model",
		Protocol: ProviderProtocolOpenAIResponses, GatewayBaseURL: providerServer.URL + "/v1",
		GatewayCapability:   "readiness-diagnostic-gateway-capability-0001",
		ContextWindowTokens: 8192, MaxOutputTokens: 128, Tools: true, Reasoning: true,
		TimeoutMillis: 20_000, RuntimeAgent: "build",
		Variant: ProviderVariantSpec{Name: "high", Responses: &OpenAIResponsesVariantOptions{Effort: "high", Summary: "auto", TextVerbosity: "high"}},
	}
	tools := ToolsConfigurationSpec{
		Endpoint: mcpServer.URL + "/mcp", Bearer: "readiness-diagnostic-mcp-capability-0001",
		ToolNames: []string{"source_read"}, TimeoutMillis: 5000,
	}
	content, err := BuildProviderToolsConfiguration(provider, tools)
	if err != nil {
		t.Fatal(err)
	}
	port := readinessDiagnosticPort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	process, err := startProcess(ctx, binary, digest, stateRoot, workspace, port, "fixture", "fixture-server-secret", io.Discard, true, 128, &tools, &provider, content)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = process.CloseContext(closeCtx)
		select {
		case <-process.Done():
		default:
			t.Error("diagnostic process reap remained unresolved")
		}
	}()

	proxyEntries := 0
	for _, entry := range process.command.Env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.Contains(strings.ToUpper(key), "PROXY") {
			proxyEntries++
		}
	}
	started := time.Now()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	samples := make([]readinessDiagnosticSample, 0, 40)
diagnosticLoop:
	for len(samples) < 40 && time.Since(started) < 32*time.Second {
		state := readinessDiagnosticProcessState(process)
		tcp := readinessDiagnosticTCP(address, 250*time.Millisecond)
		var health, config readinessDiagnosticHTTP
		var probes sync.WaitGroup
		probes.Add(2)
		go func() {
			defer probes.Done()
			health = readinessDiagnosticGET(address, "/global/health", "fixture", "fixture-server-secret", 750*time.Millisecond)
		}()
		go func() {
			defer probes.Done()
			config = readinessDiagnosticGET(address, "/config", "fixture", "fixture-server-secret", 750*time.Millisecond)
		}()
		probes.Wait()
		samples = append(samples, readinessDiagnosticSample{time.Since(started).Milliseconds(), state, tcp, health, config})
		if state == "exited" || config.ResponseBodyCompleted {
			break
		}
		select {
		case <-ctx.Done():
			break diagnosticLoop
		case <-time.After(100 * time.Millisecond):
		}
	}
	return readinessDiagnosticAttempt{number, proxyEntries, providerCalls.Load(), samples, readinessDiagnosticProcessState(process)}
}

func readinessDiagnosticPort(t *testing.T) int {
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

func readinessDiagnosticProcessState(process *Process) string {
	select {
	case <-process.Done():
		return "exited"
	default:
		return "running"
	}
}

func readinessDiagnosticTCP(address string, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	connection, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp4", address)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "timeout"
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return "timeout"
		}
		return "refused_or_other"
	}
	_ = connection.Close()
	return "connected"
}

func readinessDiagnosticGET(address, path, user, password string, timeout time.Duration) readinessDiagnosticHTTP {
	started := time.Now()
	result := readinessDiagnosticHTTP{Outcome: "unknown"}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	trace := &httptrace.ClientTrace{
		ConnectDone:          func(_, _ string, err error) { result.ConnectCompleted = err == nil },
		WroteRequest:         func(info httptrace.WroteRequestInfo) { result.RequestWritten = info.Err == nil },
		GotFirstResponseByte: func() { result.FirstResponseByte = true },
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, "http://"+address+path, nil)
	if err != nil {
		result.Outcome = "request_error"
		return result
	}
	req.SetBasicAuth(user, password)
	req.Header.Set("Accept", "application/json")
	transport := &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: timeout}).DialContext,
		DisableKeepAlives: true, ResponseHeaderTimeout: timeout, MaxResponseHeaderBytes: 64 << 10,
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer transport.CloseIdleConnections()
	response, err := client.Do(req)
	result.ElapsedMillis = time.Since(started).Milliseconds()
	if err != nil {
		switch {
		case !result.ConnectCompleted:
			result.Outcome = "connect_error"
		case !result.RequestWritten:
			result.Outcome = "write_error"
		case !result.FirstResponseByte:
			result.Outcome = "read_header_timeout_or_error"
		default:
			result.Outcome = "response_error"
		}
		return result
	}
	defer response.Body.Close()
	result.StatusCode = response.StatusCode
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	result.ElapsedMillis = time.Since(started).Milliseconds()
	if err != nil {
		result.Outcome = "read_body_timeout_or_error"
		return result
	}
	result.ResponseBodyCompleted = true
	if response.StatusCode == http.StatusOK && path == "/global/health" {
		var health struct {
			Healthy bool   `json:"healthy"`
			Version string `json:"version"`
		}
		fields, decodeErr := wireObject(body)
		if decodeErr != nil || len(fields) != 2 || json.Unmarshal(fields["healthy"], &health.Healthy) != nil || json.Unmarshal(fields["version"], &health.Version) != nil || !health.Healthy || health.Version == "" {
			result.Outcome = "health_body"
			return result
		}
		result.Outcome = "health_valid"
	} else if response.StatusCode == http.StatusOK {
		result.Outcome = "http_200"
	} else {
		result.Outcome = "http_status"
	}
	return result
}
