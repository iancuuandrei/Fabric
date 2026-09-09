package opencode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/sourcetools"
	"harness.local/engorch/internal/toolbridge"
)

const (
	mcpProbeDigest = "88d2fa691b2d9e32fde6d1039382a850ddf96fe49cd41683c6375fe1dc8ec2a5"
	mcpProbeBearer = "mcp-probe-fixture-bearer-00000001"
)

type mcpProbeObservations struct {
	mu     sync.Mutex
	values []toolbridge.Observation
}

func (o *mcpProbeObservations) add(value toolbridge.Observation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.values = append(o.values, value)
}

func (o *mcpProbeObservations) snapshot() []toolbridge.Observation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]toolbridge.Observation(nil), o.values...)
}

func TestPinnedOpenCodeMCPInitializationAndCatalogProbe(t *testing.T) {
	if os.Getenv("ENGORCH_OPENCODE_MCP_PROBE") != "1" {
		t.Skip("explicit pinned OpenCode MCP probe opt-in required")
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(repositoryRoot, ".local", "toolchains", "opencode-1.18.29", "opencode.exe")

	definitions := sourcetools.Catalog()
	tools := make([]toolbridge.ToolDefinition, len(definitions))
	names := make([]string, len(definitions))
	for index, definition := range definitions {
		schema, err := canonical.Bytes(definition.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		tools[index] = toolbridge.ToolDefinition{Name: definition.Name, Description: definition.Description, InputSchema: schema}
		names[index] = definition.Name
	}
	var toolCalls atomic.Int32
	observations := &mcpProbeObservations{}
	bridge, err := toolbridge.New(toolbridge.Config{
		Token: mcpProbeBearer,
		Catalog: func() ([]toolbridge.ToolDefinition, error) {
			return tools, nil
		},
		Call: func(context.Context, toolbridge.Call) (toolbridge.Result, error) {
			toolCalls.Add(1)
			return toolbridge.Result{JSON: json.RawMessage(`{"error":"no-model probe does not admit tool calls"}`), IsError: true}, nil
		},
		Observe:            observations.add,
		MaxConcurrentCalls: 1,
		CallTimeout:        5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	running, err := bridge.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelClose()
		if err := running.Close(closeCtx); err != nil {
			t.Error(err)
		}
		if err := running.Wait(); err != nil {
			t.Error(err)
		}
	}()

	root := t.TempDir()
	for _, name := range []string{"home", "config", "data", "cache", "state", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	spec := ToolsConfigurationSpec{Endpoint: running.URL, Bearer: mcpProbeBearer, ToolNames: names, TimeoutMillis: 5000}
	port := providerProbePort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	output := &probeOutput{}
	process, startupReceipt, attempts, err := StartReadyLimitedToolsProcess(ctx, binary, mcpProbeDigest, root, port, "fixture", "fixture-server-secret", output, 1, 2, 14*time.Second, spec)
	if err != nil {
		t.Fatalf("pinned OpenCode MCP startup failed: %v; attempts: %+v; output: %s", err, attempts, output.String())
	}
	defer func() {
		_ = process.Close()
		select {
		case <-process.Done():
		default:
			t.Error("owned OpenCode MCP process was not reaped")
		}
	}()

	client, err := NewClient("http://127.0.0.1:"+strconv.Itoa(port), "fixture", "fixture-server-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	readback, err := client.ReadToolsConfiguration(ctx, spec)
	if err != nil || !reflect.DeepEqual(readback, startupReceipt) || readback.Endpoint != running.URL || !reflect.DeepEqual(readback.ToolIDs, []string{"engorch_source_list", "engorch_source_read"}) {
		t.Fatal("exact MCP configuration readback failed", readback, startupReceipt, err)
	}

	var status MCPStatus
	statusDeadline := time.NewTimer(15 * time.Second)
	defer statusDeadline.Stop()
	for {
		status, err = client.ReadMCPStatus(ctx)
		if err == nil {
			break
		}
		select {
		case <-statusDeadline.C:
			t.Fatalf("MCP status did not become connected: %v; observations: %+v; output: %s", err, observations.snapshot(), output.String())
		case <-process.Done():
			t.Fatalf("OpenCode exited before MCP connected: %v; output: %s", process.Wait(), output.String())
		case <-ctx.Done():
			t.Fatalf("MCP status deadline exhausted: %v; output: %s", ctx.Err(), output.String())
		case <-time.After(100 * time.Millisecond):
		}
	}
	if len(status.SHA256) != 64 || strings.Trim(status.SHA256, "0123456789abcdef") != "" {
		t.Fatal("invalid MCP status evidence hash", status)
	}

	observationDeadline := time.NewTimer(5 * time.Second)
	defer observationDeadline.Stop()
	for {
		seenInitialize, seenList := false, false
		for _, observation := range observations.snapshot() {
			seenInitialize = seenInitialize || observation.Method == "initialize" && !observation.Notification
			seenList = seenList || observation.Method == "tools/list" && !observation.Notification && observation.CatalogHash == bridge.CatalogHash()
		}
		if seenInitialize && seenList {
			break
		}
		select {
		case <-observationDeadline.C:
			t.Fatalf("OpenCode did not complete initialize/tools-list: %+v", observations.snapshot())
		case <-process.Done():
			t.Fatalf("OpenCode exited before catalog observation: %v", process.Wait())
		case <-time.After(50 * time.Millisecond):
		}
	}
	if toolCalls.Load() != 0 {
		t.Fatal("no-model compatibility probe invoked a tool", toolCalls.Load())
	}
	if len(attempts) == 0 || attempts[len(attempts)-1].Outcome != "ready" || !reflect.DeepEqual(attempts[len(attempts)-1].ToolsConfiguration, startupReceipt) {
		t.Fatal("startup evidence did not bind exact MCP configuration", attempts)
	}
	t.Logf("MCP_PROBE_PASS config_sha256=%s status_sha256=%s catalog_sha256=%s observations=%+v tool_calls=%d", startupReceipt.SHA256, status.SHA256, bridge.CatalogHash(), observations.snapshot(), toolCalls.Load())
}
