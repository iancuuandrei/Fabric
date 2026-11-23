package codexrpc

import (
	"context"
	"encoding/json"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"strings"
	"testing"
)

func TestReviewSchemaBoundToWire(t *testing.T) {
	exerciseReviewSchemaBoundToWire(t, runtime.ReviewOutputSchema())
}

func TestReviewRecheckSchemaBoundToWire(t *testing.T) {
	exerciseReviewSchemaBoundToWire(t, runtime.RepairReviewOutputSchema())
}

func exerciseReviewSchemaBoundToWire(t *testing.T, schema json.RawMessage) {
	p := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "exact", Effort: "high", Role: "reviewer"}
	raw, err := json.Marshal(map[string]any{"output_schema": schema})
	if err != nil {
		t.Fatal(err)
	}
	i, err := runtime.NewInvocation(p, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	settings := ThreadSettings{ThreadID: "thread", Model: p.Model, Provider: p.Provider, Effort: &p.Effort, Directory: t.TempDir(), Approval: "never", Sandbox: "readOnly"}
	request := scriptedResponse(t, "", map[string]any{"turn": map[string]any{"id": "turn", "status": "completed", "items": []any{}}}, func(ctx context.Context, c *Client) error { _, _, err := c.StartTurn(ctx, settings, i); return err })
	var params map[string]json.RawMessage
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	got, err := canonical.Hash("schema", params["outputSchema"])
	want, _ := canonical.Hash("schema", schema)
	if err != nil || got != want {
		t.Fatal("review schema changed on wire", err)
	}
}

func TestReviewSchemaSubstitutionFailsBeforeRPC(t *testing.T) {
	p := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "exact", Effort: "high", Role: "reviewer"}
	i, err := runtime.NewInvocation(p, `{"output_schema":{"type":"string"}}`)
	if err != nil {
		t.Fatal(err)
	}
	settings := ThreadSettings{ThreadID: "thread", Model: p.Model, Provider: p.Provider, Effort: &p.Effort, Directory: t.TempDir(), Approval: "never", Sandbox: "readOnly"}
	_, _, err = (&Client{}).StartTurn(context.Background(), settings, i)
	if err == nil || !strings.Contains(err.Error(), "review output schema substitution") {
		t.Fatal("invalid schema reached provider", err)
	}
}
