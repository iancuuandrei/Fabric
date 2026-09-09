package opencoderuntime

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/toolbridge"
)

func TestInterruptShutdownAcceptsExactCompositeMCPOwnerAndRejectsSubstitution(t *testing.T) {
	fixture := newRuntimeFixture(t)
	agent := toolbridge.Projection{
		Catalog: func() ([]toolbridge.ToolDefinition, error) {
			return []toolbridge.ToolDefinition{{Name: "agent_list", InputSchema: []byte(`{"type":"object","properties":{},"additionalProperties":false}`)}}, nil
		},
		Call: func(context.Context, toolbridge.Call) (toolbridge.Result, error) {
			return toolbridge.Result{JSON: []byte(`{"agents":[]}`)}, nil
		},
	}
	binding, err := contextmcp.PrepareRecorderBinding(fixture.broker, fixture.intent.Invocation.ID, strings.Repeat("d", 64), agent)
	if err != nil {
		t.Fatal(err)
	}
	intent := compositeIntent(t, fixture.intent)
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
	paths := fixture.paths
	paths.Version = 2
	paths.ToolReceipts = filepath.Join(t.TempDir(), "tool-receipts.jsonl")
	composite := &contextmcp.RecorderOwnedConfig{
		Path: paths.ToolReceipts, InvocationID: intent.Invocation.ID,
		CallerBindingSHA256: binding.CallerBindingSHA256, CatalogSHA256: binding.CatalogSHA256,
		AgentProjection: agent,
	}
	cfg := ExecuteConfig{Intent: intent, Paths: paths, Broker: fixture.broker, Composite: composite}
	const bearer = "interrupt-composite-owner-bearer-0001"
	server, err := newExecutionMCP(cfg, bearer)
	if err != nil {
		t.Fatal(err)
	}
	running, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = running.Close(ctx)
		cancel()
		_ = running.Wait()
	}()
	if err := validateInterruptShutdownMCPOwner(cfg, running, bearer); err != nil {
		t.Fatal("exact composite interrupt owner rejected", err)
	}
	if err := running.ValidateOwner(fixture.broker, bearer); err == nil {
		t.Fatal("fixture did not exercise recorder-owned listener")
	}

	changedPath := cfg
	changedPath.Paths.ToolReceipts += ".other"
	if err := validateInterruptShutdownMCPOwner(changedPath, running, bearer); err == nil {
		t.Fatal("substituted composite receipt path accepted")
	}
	changedBinding := cfg
	bindingCopy := binding
	bindingCopy.CallerBindingSHA256 = strings.Repeat("e", 64)
	changedBinding.Intent.ToolReceipts = &bindingCopy
	if err := validateInterruptShutdownMCPOwner(changedBinding, running, bearer); err == nil {
		t.Fatal("substituted composite caller binding accepted")
	}
	if err := validateInterruptShutdownMCPOwner(cfg, running, "interrupt-composite-owner-bearer-0002"); err == nil {
		t.Fatal("substituted composite bearer accepted")
	}
}

func TestInterruptShutdownPreservesLegacyMCPOwnerValidation(t *testing.T) {
	fixture := newRuntimeFixture(t)
	const bearer = "interrupt-legacy-owner-bearer-000001"
	server, err := contextmcp.NewOwned(fixture.broker, bearer, nil)
	if err != nil {
		t.Fatal(err)
	}
	running, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = running.Close(ctx)
		cancel()
		_ = running.Wait()
	}()
	cfg := ExecuteConfig{Intent: fixture.intent, Paths: fixture.paths, Broker: fixture.broker}
	if err := validateInterruptShutdownMCPOwner(cfg, running, bearer); err != nil {
		t.Fatal("legacy interrupt owner rejected", err)
	}
	if err := validateInterruptShutdownMCPOwner(cfg, running, bearer+"-substituted"); err == nil {
		t.Fatal("legacy interrupt owner accepted substituted bearer")
	}
}
