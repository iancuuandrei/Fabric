package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/toolbridge"
)

func TestAgentToolWaitEnvelope(t *testing.T) {
	if agentToolMaximumWaitMillis != 25000 {
		t.Fatalf("controller wait cap changed: %d", agentToolMaximumWaitMillis)
	}
	if time.Duration(agentToolMaximumWaitMillis)*time.Millisecond >= contextmcp.RecorderOwnedBridgeCallTimeout {
		t.Fatalf("wait cap %dms not below bridge ceiling %v", agentToolMaximumWaitMillis, contextmcp.RecorderOwnedBridgeCallTimeout)
	}
	catalog, err := agentToolCatalog()
	if err != nil {
		t.Fatal(err)
	}
	var waitSchema json.RawMessage
	for _, definition := range catalog {
		if definition.Name == "wait_agent" {
			waitSchema = definition.InputSchema
			break
		}
	}
	if waitSchema == nil {
		t.Fatal("wait_agent missing from catalog")
	}
	var schema struct {
		Properties struct {
			TimeoutMilliseconds struct {
				Maximum int `json:"maximum"`
				Minimum int `json:"minimum"`
			} `json:"timeout_milliseconds"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(waitSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties.TimeoutMilliseconds.Maximum != agentToolMaximumWaitMillis {
		t.Fatalf("schema maximum %d != controller cap %d", schema.Properties.TimeoutMilliseconds.Maximum, agentToolMaximumWaitMillis)
	}
	if schema.Properties.TimeoutMilliseconds.Minimum != 1 {
		t.Fatalf("schema minimum changed: %d", schema.Properties.TimeoutMilliseconds.Minimum)
	}
	catalogFunc := func() ([]toolbridge.ToolDefinition, error) {
		return []toolbridge.ToolDefinition{{Name: "wait_agent", Description: "wait envelope", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
	}
	callFunc := func(context.Context, toolbridge.Call) (toolbridge.Result, error) {
		return toolbridge.Result{JSON: json.RawMessage(`{}`)}, nil
	}
	if _, err := toolbridge.New(toolbridge.Config{Token: strings.Repeat("t", 32), Catalog: catalogFunc, Call: callFunc, CallTimeout: time.Duration(agentToolMaximumWaitMillis) * time.Millisecond}); err != nil {
		t.Fatalf("bridge rejected controller cap: %v", err)
	}
	if _, err := toolbridge.New(toolbridge.Config{Token: strings.Repeat("t", 32), Catalog: catalogFunc, Call: callFunc, CallTimeout: contextmcp.RecorderOwnedBridgeCallTimeout + time.Second}); err == nil {
		t.Fatal("bridge accepted timeout above one-minute ceiling")
	}
	spec := opencode.ToolsConfigurationSpec{Endpoint: "http://127.0.0.1:18080/mcp", Bearer: strings.Repeat("k", 32), ToolNames: []string{"wait_agent"}, TimeoutMillis: agentToolMaximumWaitMillis}
	if _, err := opencode.BuildToolsConfiguration(spec); err != nil {
		t.Fatalf("tools configuration rejected controller cap: %v", err)
	}
	spec.TimeoutMillis = 30001
	if _, err := opencode.BuildToolsConfiguration(spec); err == nil {
		t.Fatal("tools configuration accepted timeout above its ceiling")
	}
}
