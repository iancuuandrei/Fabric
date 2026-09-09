package opencode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProjectAdmissionPollsOnlyCanonicalGlobalIdentity(t *testing.T) {
	directory := filepath.Clean(t.TempDir())
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		writer.Header().Set("content-type", "application/json")
		if calls == 1 {
			_, _ = writer.Write([]byte(`{"id":"global","worktree":"/","time":{"created":1,"updated":2},"sandboxes":[]}`))
			return
		}
		body, _ := json.Marshal(map[string]any{"id": "project_123", "worktree": directory, "vcs": "git", "time": map[string]any{"created": 1, "updated": 2}, "sandboxes": []string{}})
		_, _ = writer.Write(body)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	process := &Process{done: make(chan struct{})}
	receipt, polls, elapsed, err := waitForProjectAdmission(context.Background(), process, client, ProjectExpectation{Directory: directory, Mode: ProjectModeGit, Worktree: directory})
	if err != nil || receipt.ID != "project_123" || polls != 2 || elapsed < 0 {
		t.Fatal("project readiness did not retain bounded polling evidence", receipt, polls, elapsed, err)
	}
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		runStartupHelper()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestReadinessDiagnosticsObserveTCPAndAuthenticatedHealth(t *testing.T) {
	called := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if r.URL.Path != "/global/health" || !ok || user != "user" || password != "password" {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"healthy":true,"version":"1.18.29"}`))
		select {
		case called <- struct{}{}:
		default:
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "user", "password")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	diagnostics := startReadinessDiagnostics(ctx, client)
	select {
	case <-called:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	time.Sleep(25 * time.Millisecond)
	observation := diagnostics.stop()
	if !observation.tcpObserved || !observation.healthHTTPObserved || observation.finalHealthErrorClass != "" || observation.firstTCPMillis < 0 || observation.firstHealthHTTPMillis < 0 {
		t.Fatalf("diagnostics = %+v", observation)
	}
}

func TestDiagnosticHealthClassifiesResponseReadTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "user", "password")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	httpObserved, class := client.diagnosticHealth(ctx)
	if httpObserved || class != "read-deadline" {
		t.Fatalf("health diagnostic = observed %t, class %q", httpObserved, class)
	}
}

func TestDiagnosticHealthRejectsUnhealthyOrMalformedBody(t *testing.T) {
	for _, body := range []string{`{"healthy":false,"version":"1.18.29"}`, `{"healthy":true}`, `{"healthy":true,"version":"bad version"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		client, err := NewClient(server.URL, "user", "password")
		if err != nil {
			t.Fatal(err)
		}
		observed, class := client.diagnosticHealth(context.Background())
		client.Close()
		server.Close()
		if !observed || class != "health-body" {
			t.Fatalf("body %s = observed %t, class %q", body, observed, class)
		}
	}
}

func TestReadConfigurationBindsExactWorkingDirectoryQuery(t *testing.T) {
	workingDirectory := filepath.Clean(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config" || r.URL.Query().Get("directory") != workingDirectory || len(r.URL.Query()) != 1 {
			t.Errorf("configuration locator = %q", r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "user", "password")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.readConfiguration(context.Background(), workingDirectory); err != nil {
		t.Fatal(err)
	}
	if _, err := client.readConfiguration(context.Background(), workingDirectory+string(filepath.Separator)+".."); err == nil {
		t.Fatal("unclean working directory accepted")
	}
}

func runStartupHelper() {
	port := ""
	for i, arg := range os.Args {
		if arg == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	marker := filepath.Join(".", ".first-startup-attempt")
	f, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	first := err == nil
	if f != nil {
		_ = f.Close()
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/global/health" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"healthy":true,"version":"test"}`))
			return
		}
		if r.URL.Path == "/project/current" {
			if _, statErr := os.Stat(".helper-project-global"); statErr == nil {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"global","worktree":"/","time":{"created":1,"updated":2},"sandboxes":[]}`))
				return
			}
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/config" {
			http.NotFound(w, r)
			return
		}
		if first {
			<-r.Context().Done()
			return
		}
		config := []byte(`{"share":"disabled","plugin":[],"permission":{"*":"deny"}}`)
		if configured, readErr := os.ReadFile(".helper-config"); readErr == nil {
			config = configured
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(config)
	})
	_ = http.ListenAndServe("127.0.0.1:"+port, handler)
}

func TestStartReadyLimitedProcessRequiresBoundedPolicy(t *testing.T) {
	_, _, evidence, err := StartReadyLimitedProcess(context.Background(), "", strings.Repeat("a", 64), t.TempDir(), 1, "user", "password", io.Discard, 1, 1, time.Second)
	if err == nil || evidence != nil {
		t.Fatal("unbounded readiness policy was admitted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, _, err := StartReadyLimitedProcess(ctx, "", strings.Repeat("a", 64), t.TempDir(), 1, "user", "password", io.Discard, 1, 3, time.Second); err == nil {
		t.Fatal("more than two startup attempts admitted")
	}
}

func TestAdmitProcessDistinguishesConfigurationRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"share":"auto","plugin":[],"permission":{"*":"deny"}}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "user", "password")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	process := &Process{done: make(chan struct{}), launch: processLaunchIdentity{WorkingDirectory: filepath.Clean(t.TempDir())}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	configuration, outcome, err := admitProcess(ctx, process, client)
	if err == nil || outcome != "configuration_rejected" || configuration != (HostConfiguration{}) {
		t.Fatal("unsafe configuration did not fail closed")
	}
}

func TestStartReadyLimitedProcessReapsStalledAttemptBeforeRetry(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	root := t.TempDir()
	for _, name := range []string{"home", "config", "data", "cache", "state", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	_ = listener.Close()
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process, configuration, evidence, err := StartReadyLimitedProcess(ctx, binary, hex.EncodeToString(sum[:]), root, port, "user", "password", io.Discard, 1, 2, 750*time.Millisecond)
	if err != nil {
		t.Fatalf("bounded restart did not recover: %v; evidence: %+v", err, evidence)
	}
	defer func() { _ = process.Close() }()
	if len(evidence) != 2 || evidence[0].Outcome != "timeout" || !evidence[0].Reaped || evidence[1].Outcome != "ready" || evidence[1].Reaped || evidence[1].Configuration != configuration {
		t.Fatalf("unexpected startup lifecycle evidence: %+v", evidence)
	}
}

func TestStartReadyLimitedToolsProcessBindsTypedEnvironmentAndReadback(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	root := t.TempDir()
	for _, name := range []string{"home", "config", "data", "cache", "state", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	spec := ToolsConfigurationSpec{Endpoint: "http://127.0.0.1:43123/mcp", Bearer: strings.Repeat("b", 32), ToolNames: []string{"search", "read"}, TimeoutMillis: 5000}
	content, err := BuildToolsConfiguration(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".helper-config"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	_ = listener.Close()
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process, receipt, evidence, err := StartReadyLimitedToolsProcess(ctx, binary, hex.EncodeToString(sum[:]), root, port, "user", "password", io.Discard, 1, 2, 750*time.Millisecond, spec)
	if err != nil {
		t.Fatalf("typed tools startup failed: %v; evidence: %+v", err, evidence)
	}
	defer func() { _ = process.Close() }()
	if len(evidence) != 2 || !evidence[0].Reaped || evidence[1].Outcome != "ready" || !reflect.DeepEqual(evidence[1].ToolsConfiguration, receipt) || evidence[1].Configuration != (HostConfiguration{}) {
		t.Fatalf("tools startup evidence mismatch: %+v", evidence)
	}
	configEntries := 0
	for _, entry := range process.command.Env {
		if strings.HasPrefix(entry, "OPENCODE_CONFIG_CONTENT=") {
			configEntries++
			if entry != "OPENCODE_CONFIG_CONTENT="+content {
				t.Fatal("typed tools configuration changed in launch environment")
			}
		}
	}
	if configEntries != 1 {
		t.Fatal("typed tools configuration missing or duplicated")
	}
}
