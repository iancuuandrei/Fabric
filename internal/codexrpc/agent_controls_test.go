package codexrpc

import (
	"context"
	"encoding/json"
	"github.com/iancuuandrei/fabric/internal/runtime"
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
	if _, exists := params.Config["model_auto_compact_token_limit"]; exists {
		t.Fatal("legacy thread unexpectedly set automatic compaction")
	}
}

func TestThreadStartBindsExplicitAutoCompactionThreshold(t *testing.T) {
	dir := t.TempDir()
	p := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "exact", Effort: "medium", Role: "planner"}
	i, err := runtime.NewInvocationWithCodexAutoCompact(p, "objective", &runtime.CodexAutoCompactOptions{Version: runtime.CodexAutoCompactVersion, TokenLimit: 64000})
	if err != nil {
		t.Fatal(err)
	}
	var settings ThreadSettings
	request := scriptedResponse(t, "", resumeResult(dir, "exact", "medium"), func(ctx context.Context, c *Client) error {
		settings, err = c.StartThreadForInvocation(ctx, i, dir, nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var params struct {
		Config map[string]json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	var got int64
	if err := json.Unmarshal(params.Config["model_auto_compact_token_limit"], &got); err != nil || got != 64000 {
		t.Fatalf("thread/start omitted exact requested threshold: %s, %v", params.Config["model_auto_compact_token_limit"], err)
	}
	if err := settings.ValidateInvocation(i, dir); err != nil {
		t.Fatal(err)
	}
	settings.RequestedAutoCompactTokenLimit = nil
	if settings.ValidateInvocation(i, dir) == nil {
		t.Fatal("thread request without threshold matched enabled invocation")
	}
}
