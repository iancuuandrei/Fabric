package opencoderuntime

import (
	"context"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/contextmcp"
	"github.com/iancuuandrei/fabric/internal/toolbridge"
	"github.com/iancuuandrei/fabric/internal/toolreceipts"
)

func TestExecutionMCPQueueDepthValidatesFallback(t *testing.T) {
	f := newRuntimeFixture(t)
	bearer := strings.Repeat("b", 32)
	for _, depth := range []int{-1, 33, 64, 100} {
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
	queued, wait, err := contextmcp.QueuePolicyForCallBudget(contextmcp.InvocationToolCallBudget, contextmcp.ContextCallTimeout)
	if err != nil || queued != 63 || wait != 63*contextmcp.ContextCallTimeout {
		t.Fatal("full invocation call budget did not map to exact queue policy", queued, wait, err)
	}
	cfg := ExecuteConfig{Intent: f.intent, Paths: f.paths, Broker: f.broker, MCPQueueDepth: queued, MCPQueueWait: wait}
	if _, err := newExecutionMCP(cfg, bearer); err != nil {
		t.Fatal("deadline-bound full-budget queue rejected", err)
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
	intent, binding, paths := queuedCompositeFixture(t, f, agent, 32)
	cfg := ExecuteConfig{Intent: intent, Paths: paths, Broker: f.broker, MCPQueueDepth: 33, Composite: &contextmcp.RecorderOwnedConfig{Path: paths.ToolReceipts, InvocationID: intent.Invocation.ID, CallerBindingSHA256: binding.CallerBindingSHA256, CatalogSHA256: binding.CatalogSHA256, AgentProjection: agent, MaxQueuedCalls: 32}, VerifyComposite: func(toolreceipts.Owner, toolbridge.Call, toolbridge.Result) error { return nil }}
	if _, err := newExecutionMCP(cfg, strings.Repeat("b", 32)); err != nil {
		t.Fatal("composite path stopped honoring receipts binding over unrelated knob", err)
	}
}
