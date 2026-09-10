package opencoderuntime

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/toolbridge"
	"harness.local/engorch/internal/toolreceipts"
)

func TestExecutionMCPQueueDepthValidatesFallback(t *testing.T) {
	f := newRuntimeFixture(t)
	bearer := strings.Repeat("b", 32)
	for _, depth := range []int{-1, 33, 100} {
		cfg := ExecuteConfig{Intent: f.intent, Paths: f.paths, Broker: f.broker, MCPQueueDepth: depth}
		if _, err := newExecutionMCP(cfg, bearer); err == nil {
			t.Fatalf("fallback queue depth %d admitted", depth)
		}
	}
	for _, depth := range []int{0, 1, 32} {
		cfg := ExecuteConfig{Intent: f.intent, Paths: f.paths, Broker: f.broker, MCPQueueDepth: depth}
		if _, err := newExecutionMCP(cfg, bearer); err != nil {
			t.Fatalf("fallback queue depth %d rejected: %v", depth, err)
		}
	}
}

func TestExecutionMCPQueueDepthIgnoredOnComposite(t *testing.T) {
	f := newRuntimeFixture(t)
	agent := toolbridge.Projection{
		Catalog: func() ([]toolbridge.ToolDefinition, error) {
			return []toolbridge.ToolDefinition{{Name: "list_agents", InputSchema: []byte(`{"type":"object","properties":{},"additionalProperties":false}`)}}, nil
		},
		Call: func(context.Context, toolbridge.Call) (toolbridge.Result, error) {
			return toolbridge.Result{}, nil
		},
	}
	binding, err := contextmcp.PrepareRecorderBindingWithQueue(f.broker, f.intent.Invocation.ID, strings.Repeat("d", 64), agent, 32)
	if err != nil {
		t.Fatal(err)
	}
	intent := compositeIntent(t, f.intent)
	intent.ToolReceipts = &binding
	intent.SessionPlan.CatalogSHA256 = binding.CatalogSHA256
	intent.SessionPlan.ToolNames = make([]string, len(binding.Tools))
	for index, tool := range binding.Tools {
		intent.SessionPlan.ToolNames[index] = tool.Tool
	}
	sort.Strings(intent.SessionPlan.ToolNames)
	intent.IntentID, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	paths := f.paths
	paths.Version = 2
	paths.ToolReceipts = filepath.Join(t.TempDir(), "receipts")
	cfg := ExecuteConfig{Intent: intent, Paths: paths, Broker: f.broker, MCPQueueDepth: 33, Composite: &contextmcp.RecorderOwnedConfig{Path: paths.ToolReceipts, InvocationID: intent.Invocation.ID, CallerBindingSHA256: binding.CallerBindingSHA256, CatalogSHA256: binding.CatalogSHA256, AgentProjection: agent, MaxQueuedCalls: 32}, VerifyComposite: func(toolreceipts.Owner, toolbridge.Call, toolbridge.Result) error { return nil }}
	if _, err := newExecutionMCP(cfg, strings.Repeat("b", 32)); err != nil {
		t.Fatal("composite path stopped honoring receipts binding over unrelated knob", err)
	}
}
