package opencoderuntime

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/toolbridge"
	"harness.local/engorch/internal/toolreceipts"
)

func TestExecutionCompositeCatalogRequiresExactAdmittedBinding(t *testing.T) {
	t.Run("legacy", func(t *testing.T) { testExecutionCompositeBinding(t, 0) })
	t.Run("queued", func(t *testing.T) { testExecutionCompositeBinding(t, 32) })
}

func testExecutionCompositeBinding(t *testing.T, maxQueued int) {
	t.Helper()
	f := newRuntimeFixture(t)
	called := false
	agent := toolbridge.Projection{
		Catalog: func() ([]toolbridge.ToolDefinition, error) {
			return []toolbridge.ToolDefinition{{Name: "list_agents", InputSchema: []byte(`{"type":"object","properties":{},"additionalProperties":false}`)}}, nil
		},
		Call: func(context.Context, toolbridge.Call) (toolbridge.Result, error) {
			called = true
			return toolbridge.Result{}, nil
		},
	}
	binding, err := contextmcp.PrepareRecorderBindingWithQueue(f.broker, f.intent.Invocation.ID, strings.Repeat("d", 64), agent, maxQueued)
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
	cfg := ExecuteConfig{Intent: intent, Paths: paths, Broker: f.broker, Composite: &contextmcp.RecorderOwnedConfig{Path: paths.ToolReceipts, InvocationID: intent.Invocation.ID, CallerBindingSHA256: binding.CallerBindingSHA256, CatalogSHA256: binding.CatalogSHA256, AgentProjection: agent}, VerifyComposite: func(toolreceipts.Owner, toolbridge.Call, toolbridge.Result) error { called = true; return nil }}
	cfg.Composite.MaxQueuedCalls = maxQueued
	tools, err := executionProviderTools(cfg, *intent.SessionPlan)
	if err != nil || len(tools) != len(binding.Tools) {
		t.Fatal("exact composite catalog rejected", err)
	}
	for _, mutate := range []func(*ExecuteConfig){
		func(c *ExecuteConfig) { c.VerifyComposite = nil },
		func(c *ExecuteConfig) { c.Composite.CallerBindingSHA256 = strings.Repeat("e", 64) },
		func(c *ExecuteConfig) { c.Composite.Path += ".other" },
		func(c *ExecuteConfig) { c.Composite.MaxQueuedCalls = (maxQueued + 1) % 33 },
		func(c *ExecuteConfig) { c.Intent.Version = 2 },
	} {
		changed := cfg
		configCopy := *cfg.Composite
		changed.Composite = &configCopy
		mutate(&changed)
		if _, err := executionProviderTools(changed, *intent.SessionPlan); err == nil {
			t.Fatal("substituted runtime catalog admitted")
		}
	}
	events, err := journal.Read(paths.ToolReceipts)
	if err != nil || len(events) != 0 || called {
		t.Fatal("catalog validation performed callback or journal effect", err)
	}
	if _, err := newExecutionMCP(cfg, strings.Repeat("b", 32)); err != nil {
		t.Fatal(err)
	}
	contextProjection, err := contextmcp.Projection(f.broker)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := toolreceipts.Open(toolreceipts.Config{Path: paths.ToolReceipts, InvocationID: binding.InvocationID, CallerBindingSHA256: binding.CallerBindingSHA256, CatalogSHA256: binding.CatalogSHA256, QueuePolicy: binding.QueuePolicy, MaxQueuedCalls: binding.MaxQueuedCalls, Owners: []toolreceipts.OwnedProjection{{Owner: toolreceipts.OwnerContext, Projection: contextProjection}, {Owner: toolreceipts.OwnerAgent, Projection: agent}}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = recorder.Projection().Call(context.Background(), toolbridge.Call{RequestID: []byte(`1`), Tool: "list_agents", Arguments: []byte(`{}`)})
	if !called {
		t.Fatal("fixture call was not admitted")
	}
	if _, err := newExecutionMCP(cfg, strings.Repeat("b", 32)); err == nil {
		t.Fatal("used composite journal admitted before runtime startup")
	}
}
