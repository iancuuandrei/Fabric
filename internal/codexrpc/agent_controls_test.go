package codexrpc

import (
	"context"
	"encoding/json"
	"harness.local/engorch/internal/runtime"
	"testing"
)

func TestFreshThreadRequestsHardAgentDisable(t *testing.T) {
	dir := t.TempDir()
	p := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "exact", Effort: "medium", Role: "planner"}
	var callErr error
	request := scriptedResponse(t, "", resumeResult(dir, "exact", "medium"), func(ctx context.Context, c *Client) error {
		_, callErr = c.StartThreadWithTools(ctx, p, dir, nil)
		return callErr
	})
	if callErr != nil {
		t.Fatal(callErr)
	}
	var params struct {
		Config map[string]json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"agents.enabled", "features.multi_agent", "features.multi_agent_v2"} {
		if string(params.Config[key]) != "false" {
			t.Fatalf("%s was not explicitly disabled", key)
		}
	}
	if _, exists := params.Config["multi_agent_mode"]; exists {
		t.Fatal("removed control used")
	}
}
