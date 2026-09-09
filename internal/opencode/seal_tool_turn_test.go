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
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/journal"
)

func init() {
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		return
	}
	if _, err := os.Stat(filepath.Join(".", "seal-helper.enabled")); err != nil {
		return
	}
	port := ""
	for index, value := range os.Args {
		if value == "--port" && index+1 < len(os.Args) {
			port = os.Args[index+1]
		}
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		name := ""
		switch {
		case request.URL.Path == "/global/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"healthy":true,"version":"test"}`))
			return
		case request.URL.Path == "/config":
			name = "seal-config.json"
		case request.URL.Path == "/mcp":
			name = "seal-mcp.json"
		case strings.HasPrefix(request.URL.Path, "/session/") && strings.HasSuffix(request.URL.Path, "/message"):
			name = "seal-transcript.json"
		default:
			http.NotFound(w, request)
			return
		}
		raw, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			http.Error(w, "fixture missing", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	})
	server := &http.Server{Addr: "127.0.0.1:" + port, Handler: handler}
	if server.ListenAndServe() != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

type sealFixture struct {
	sealPath, dispatchPath, brokerPath string
	expected                           SynchronousToolTurnSealExpected
	spec                               ToolsConfigurationSpec
	client                             *Client
	process                            *Process
	running                            *contextmcp.OwnedRunning
	broker                             *contextbroker.Broker
}

func TestSealSynchronousToolTurnSuccessAndOfflineRecovery(t *testing.T) {
	fixture := newSealFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	receipt, err := SealSynchronousToolTurn(ctx, fixture.sealPath, fixture.dispatchPath, fixture.brokerPath, fixture.expected, fixture.spec, fixture.client, fixture.process, fixture.running, fixture.broker)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.MCPHandlersStopped || !receipt.RootProcessReaped || !receipt.BrokerClosed {
		t.Fatal("terminal lifecycle flags missing")
	}
	select {
	case <-fixture.process.Done():
	default:
		t.Fatal("owned OpenCode root was not reaped")
	}
	state, err := contextbroker.Inspect(fixture.brokerPath)
	if err != nil || !state.Closed {
		t.Fatal("broker was not durably closed", err)
	}
	recovered, err := RecoverSynchronousToolTurnSeal(fixture.sealPath, fixture.dispatchPath, fixture.brokerPath, fixture.expected)
	if err != nil || !equalCanonical(recovered, receipt) {
		t.Fatal("offline seal replay failed", err)
	}
	events, err := journal.Read(fixture.sealPath)
	if err != nil || len(events) != 2 {
		t.Fatal("seal journal shape mismatch", err)
	}
	raw, _ := os.ReadFile(fixture.sealPath)
	if strings.Contains(string(raw), fixture.spec.Bearer) || strings.Contains(string(raw), fixture.client.password) {
		t.Fatal("seal journal contains a credential")
	}
}

func TestSealIntentSeparatesStateRootFromWorkingDirectory(t *testing.T) {
	fixture := newSealFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := SealSynchronousToolTurn(ctx, fixture.sealPath, fixture.dispatchPath, fixture.brokerPath, fixture.expected, fixture.spec, fixture.client, fixture.process, fixture.running, fixture.broker); err != nil {
		t.Fatal(err)
	}
	events, err := journal.Read(fixture.sealPath)
	if err != nil || len(events) != 2 {
		t.Fatal("read sealed fixture", err)
	}
	var intent SynchronousToolTurnSealIntent
	if err := canonical.Decode(events[0].Payload, &intent); err != nil {
		t.Fatal(err)
	}
	intent.Expected.HostRoot = t.TempDir()
	intent.Expected.WorkingDirectory = fixture.expected.Session.Session.Directory
	intent.ID, err = synchronousToolTurnSealIntentID(intent)
	if err != nil || validateSynchronousToolTurnSealIntent(intent) != nil {
		t.Fatal("distinct state root and workspace were rejected", err)
	}
}

func TestSealSynchronousToolTurnPreIntentFailuresDoNotClose(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *sealFixture)
	}{
		{"tampered transcript", func(t *testing.T, f *sealFixture) {
			writeSealFile(t, f.expected.Session.Session.Directory, "seal-transcript.json", `[]`)
		}},
		{"foreign broker pointer", func(t *testing.T, f *sealFixture) {
			owned := f.broker
			t.Cleanup(func() { _ = owned.Close() })
			_, _, other := synchronousToolFixture(t)
			f.broker = other
		}},
		{"unowned process handle", func(t *testing.T, f *sealFixture) {
			owned := f.process
			t.Cleanup(func() { _ = owned.Close() })
			f.process = &Process{command: owned.command, done: owned.done, cancel: owned.cancel}
		}},
		{"foreign admitted host root", func(t *testing.T, f *sealFixture) {
			f.expected.HostRoot = t.TempDir()
		}},
		{"foreign admitted working directory", func(t *testing.T, f *sealFixture) {
			f.expected.WorkingDirectory = t.TempDir()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSealFixture(t)
			test.mutate(t, fixture)
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			if _, err := SealSynchronousToolTurn(ctx, fixture.sealPath, fixture.dispatchPath, fixture.brokerPath, fixture.expected, fixture.spec, fixture.client, fixture.process, fixture.running, fixture.broker); err == nil {
				t.Fatal("invalid topology was sealed")
			}
			if events, err := journal.Read(fixture.sealPath); err != nil || len(events) != 0 {
				t.Fatal("pre-intent failure wrote seal history", err)
			}
			select {
			case <-fixture.process.Done():
				t.Fatal("pre-intent failure reaped process")
			default:
			}
			state, _ := contextbroker.Inspect(fixture.brokerPath)
			if state.Closed {
				t.Fatal("pre-intent failure closed broker")
			}
		})
	}
}

func TestSealLiveRejectsSessionDispatchStructuredOutputMismatchBeforeEffects(t *testing.T) {
	fixture := newSealFixture(t)
	base := fixture.expected
	sessionExpectation := mustSealStructuredOutputExpectation(t, `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)
	dispatchExpectation := mustSealStructuredOutputExpectation(t, `{"type":"object","properties":{"candidate":{"type":"string"}},"required":["candidate"],"additionalProperties":false}`)
	for _, test := range []struct {
		name     string
		session  *StructuredOutputExpectation
		dispatch *StructuredOutputExpectation
	}{
		{name: "session only", session: &sessionExpectation},
		{name: "dispatch only", dispatch: &dispatchExpectation},
		{name: "different valid schemas", session: &sessionExpectation, dispatch: &dispatchExpectation},
	} {
		t.Run(test.name, func(t *testing.T) {
			expected := base
			expected.Session.StructuredOutput = test.session
			expected.Dispatch.Dispatch.StructuredOutput = test.dispatch
			expected.Tools.AllowStructuredOutput = test.dispatch != nil
			if _, _, _, err := validateSynchronousToolTurnSealLive(fixture.dispatchPath, fixture.brokerPath, expected, fixture.spec, fixture.client, fixture.process, fixture.running, fixture.broker); err == nil {
				t.Fatal("mismatched session/dispatch structured output was admitted")
			}
			if events, err := journal.Read(fixture.sealPath); err != nil || len(events) != 0 {
				t.Fatal("live pre-seal mismatch wrote seal history", err)
			}
			select {
			case <-fixture.process.Done():
				t.Fatal("live pre-seal mismatch reaped process")
			default:
			}
			state, err := contextbroker.Inspect(fixture.brokerPath)
			if err != nil || state.Closed {
				t.Fatal("live pre-seal mismatch closed broker", err)
			}
		})
	}
}

func TestSealIntentRejectsSessionDispatchStructuredOutputMismatch(t *testing.T) {
	fixture := newSealFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := SealSynchronousToolTurn(ctx, fixture.sealPath, fixture.dispatchPath, fixture.brokerPath, fixture.expected, fixture.spec, fixture.client, fixture.process, fixture.running, fixture.broker); err != nil {
		t.Fatal("failed to create valid seal fixture", err)
	}
	events, err := journal.Read(fixture.sealPath)
	if err != nil || len(events) != 2 {
		t.Fatal("read valid seal fixture", err)
	}
	var base SynchronousToolTurnSealIntent
	if err := canonical.Decode(events[0].Payload, &base); err != nil {
		t.Fatal(err)
	}
	sessionExpectation := mustSealStructuredOutputExpectation(t, `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)
	dispatchExpectation := mustSealStructuredOutputExpectation(t, `{"type":"object","properties":{"candidate":{"type":"string"}},"required":["candidate"],"additionalProperties":false}`)
	for _, test := range []struct {
		name     string
		session  *StructuredOutputExpectation
		dispatch *StructuredOutputExpectation
	}{
		{name: "session only", session: &sessionExpectation},
		{name: "dispatch only", dispatch: &dispatchExpectation},
		{name: "different valid schemas", session: &sessionExpectation, dispatch: &dispatchExpectation},
	} {
		t.Run(test.name, func(t *testing.T) {
			intent := base
			intent.Expected.Session.StructuredOutput = test.session
			intent.Expected.Dispatch.Dispatch.StructuredOutput = test.dispatch
			intent.Expected.Tools.AllowStructuredOutput = test.dispatch != nil
			intent.ID, err = synchronousToolTurnSealIntentID(intent)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateSynchronousToolTurnSealIntent(intent); err == nil {
				t.Fatal("mismatched session/dispatch structured output was durably admitted")
			}
		})
	}
}

func mustSealStructuredOutputExpectation(t *testing.T, schema string) StructuredOutputExpectation {
	t.Helper()
	expectation, err := NewStructuredOutputExpectation(json.RawMessage(schema))
	if err != nil {
		t.Fatal(err)
	}
	return expectation
}

func TestSealSynchronousToolTurnReapDeadlineCannotSeal(t *testing.T) {
	fixture := newSealFixture(t)
	fixture.process.cancel = func() {}
	defer func() {
		_ = fixture.process.command.Process.Kill()
		<-fixture.process.Done()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	if _, err := SealSynchronousToolTurn(ctx, fixture.sealPath, fixture.dispatchPath, fixture.brokerPath, fixture.expected, fixture.spec, fixture.client, fixture.process, fixture.running, fixture.broker); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("reap deadline did not remain unsealed", err)
	}
	events, err := journal.Read(fixture.sealPath)
	if err != nil || len(events) != 1 || events[0].Kind != "opencode.tool-turn-seal-intent" {
		t.Fatal("reap deadline did not leave exact intent-only state", err)
	}
	select {
	case <-fixture.process.Done():
		t.Fatal("fixture unexpectedly reaped during deadline test")
	default:
	}
}

func TestSealSynchronousToolTurnPostIntentFailureRemainsUnsealed(t *testing.T) {
	fixture := newSealFixture(t)
	originalCancel := fixture.process.cancel
	fixture.process.cancel = func() {
		_ = fixture.broker.Close()
		originalCancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := SealSynchronousToolTurn(ctx, fixture.sealPath, fixture.dispatchPath, fixture.brokerPath, fixture.expected, fixture.spec, fixture.client, fixture.process, fixture.running, fixture.broker); err == nil {
		t.Fatal("post-intent broker change was sealed")
	}
	events, err := journal.Read(fixture.sealPath)
	if err != nil || len(events) != 1 || events[0].Kind != "opencode.tool-turn-seal-intent" {
		t.Fatal("post-intent failure did not remain intent-only", err)
	}
	if _, err := RecoverSynchronousToolTurnSeal(fixture.sealPath, fixture.dispatchPath, fixture.brokerPath, fixture.expected); err == nil {
		t.Fatal("intent-only seal recovered as terminal")
	}
}

func newSealFixture(t *testing.T) *sealFixture {
	t.Helper()
	intent, brokerPath, broker := synchronousToolFixture(t)
	root := t.TempDir()
	intent.Dispatch, _ = DispatchForInvocation(intent.Invocation, "ses_fixture", "msg_user", "build", root, root)
	receipt, err := broker.Call(context.Background(), "broker-list-1", "source_list", json.RawMessage(`{"after":"","limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	response, transcript := synchronousToolWire(t, intent.Dispatch, receipt)
	wireRoot := strconv.Quote(root)
	wireRoot = wireRoot[1 : len(wireRoot)-1]
	response = strings.ReplaceAll(response, "/candidate", wireRoot)
	transcript = strings.ReplaceAll(transcript, "/candidate", wireRoot)
	state, _ := contextbroker.Inspect(brokerPath)
	observation, err := decodeToolTurn([]byte(transcript), intent.Dispatch.Binding, intent.Dispatch.Text, state)
	if err != nil {
		t.Fatal(err)
	}
	dispatchPath := filepath.Join(t.TempDir(), "dispatch.jsonl")
	if err := appendSynchronousToolDispatch(dispatchPath, "opencode.sync-tool-intent", intent, state); err != nil {
		t.Fatal(err)
	}
	record := synchronousToolRecord{Response: response, Transcript: transcript, BrokerBindingID: observation.BrokerBindingID, BrokerCatalogID: intent.BrokerCatalogID, BrokerStateID: observation.BrokerStateID}
	if err := appendSynchronousToolDispatch(dispatchPath, "opencode.sync-tool-observed", record, state); err != nil {
		t.Fatal(err)
	}
	bearer := strings.Repeat("b", 40)
	bridge, err := contextmcp.NewOwned(broker, bearer, nil)
	if err != nil {
		t.Fatal(err)
	}
	running, err := bridge.Listen()
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(broker.Catalog()))
	for _, definition := range broker.Catalog() {
		names = append(names, definition.Name)
	}
	sort.Strings(names)
	spec := ToolsConfigurationSpec{Endpoint: running.URL(), Bearer: bearer, ToolNames: names, TimeoutMillis: 15000}
	config, err := BuildToolsConfiguration(spec)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := decodeToolsConfiguration([]byte(config), spec)
	if err != nil {
		t.Fatal(err)
	}
	writeSealFile(t, root, "seal-config.json", config)
	writeSealFile(t, root, "seal-mcp.json", `{"engorch":{"status":"connected"}}`)
	writeSealFile(t, root, "seal-transcript.json", transcript)
	writeSealFile(t, root, "seal-helper.enabled", "enabled")
	client, process, executableSHA256 := startSealHelper(t, root, spec)
	session := ToolSessionBinding{Session: SessionBinding{IntentID: intent.Invocation.ID, ProjectID: "project_fixture", Directory: root, Agent: "build", Provider: "fixture", Model: "model", Variant: "low"}, ToolNames: names, CatalogSHA256: running.CatalogHash()}
	expected := SynchronousToolTurnSealExpected{Dispatch: intent, Session: session, Tools: tools, ExecutableSHA256: executableSHA256, HostRoot: root, MaxOutputTokens: 128}
	fixture := &sealFixture{filepath.Join(t.TempDir(), "seal.jsonl"), dispatchPath, brokerPath, expected, spec, client, process, running, broker}
	t.Cleanup(func() { cleanupSealFixture(fixture) })
	return fixture
}

func startSealHelper(t *testing.T, root string, spec ToolsConfigurationSpec) (*Client, *Process, string) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	for _, directory := range []string{"home", "config", "data", "cache", "state", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0700); err != nil {
			t.Fatal(err)
		}
	}
	binary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	process, admitted, attempts, err := StartReadyLimitedToolsProcess(ctx, binary, digest, root, port, "fixture-user", "fixture-password", io.Discard, 128, 1, 3*time.Second, spec)
	if err != nil || !equalCanonical(admitted, mustToolsReceipt(t, spec)) {
		t.Fatal("seal helper startup failed", err, attempts)
	}
	client, err := NewClient("http://127.0.0.1:"+strconv.Itoa(port), "fixture-user", "fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	return client, process, digest
}

func mustToolsReceipt(t *testing.T, spec ToolsConfigurationSpec) ToolsConfigurationReceipt {
	t.Helper()
	raw, err := BuildToolsConfiguration(spec)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := decodeToolsConfiguration([]byte(raw), spec)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func writeSealFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func cleanupSealFixture(f *sealFixture) {
	if f == nil {
		return
	}
	if f.running != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = f.running.Close(ctx)
		cancel()
	}
	if f.process != nil {
		_ = f.process.Close()
	}
	if f.client != nil {
		f.client.Close()
	}
	if f.broker != nil {
		_ = f.broker.Close()
	}
}
