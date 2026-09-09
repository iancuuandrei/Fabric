package codexrpc

import (
	"context"
	"encoding/json"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/writercontract"
	"strings"
	"testing"
)

func TestWriterSchemaBoundToWireAndReadOnlyRolesUnaffected(t *testing.T) {
	for _, role := range []string{"writer", "fixer", "reviewer", "explorer"} {
		t.Run(role, func(t *testing.T) {
			p := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "exact", Effort: "medium", Role: role}
			raw, _ := json.Marshal(map[string]any{"output_schema": writercontract.Schema()})
			i, err := runtime.NewInvocation(p, string(raw))
			if err != nil {
				t.Fatal(err)
			}
			settings := ThreadSettings{ThreadID: "thread", Model: p.Model, Provider: p.Provider, Effort: &p.Effort, Directory: t.TempDir(), Approval: "never", Sandbox: "readOnly"}
			request := scriptedResponse(t, "", map[string]any{"turn": map[string]any{"id": "turn", "status": "completed", "items": []any{}}}, func(ctx context.Context, c *Client) error { _, _, e := c.StartTurn(ctx, settings, i); return e })
			var params map[string]json.RawMessage
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			_, present := params["outputSchema"]
			if present != (role == "writer" || role == "fixer") {
				t.Fatal("role schema mismatch", role)
			}
			if present {
				var shape struct {
					Properties struct {
						Changes struct {
							Min int `json:"minItems"`
							Max int `json:"maxItems"`
						} `json:"changes"`
					} `json:"properties"`
				}
				json.Unmarshal(params["outputSchema"], &shape)
				if shape.Properties.Changes.Min != 1 || shape.Properties.Changes.Max != 64 {
					t.Fatal("lost cardinality")
				}
			}
		})
	}
}

func TestWriterSchemaSubstitutionFailsBeforeRPC(t *testing.T) {
	p := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "exact", Effort: "medium", Role: "writer"}
	raw := strings.Replace(string(writercontract.Schema()), `"minItems":1`, `"minItems":0`, 1)
	i, err := runtime.NewInvocation(p, `{"output_schema":`+raw+`}`)
	if err != nil {
		t.Fatal(err)
	}
	settings := ThreadSettings{ThreadID: "thread", Model: p.Model, Provider: p.Provider, Effort: &p.Effort, Directory: t.TempDir(), Approval: "never", Sandbox: "readOnly"}
	_, _, err = (&Client{}).StartTurn(context.Background(), settings, i)
	if err == nil || !strings.Contains(err.Error(), "schema substitution") {
		t.Fatal(err)
	}
}

func TestUTF8WriterSchemaBoundToWireAndReadOnlyRolesUnaffected(t *testing.T) {
	for _, role := range []string{"writer", "fixer", "reviewer", "explorer"} {
		t.Run(role, func(t *testing.T) {
			p := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "exact", Effort: "medium", Role: role}
			raw, _ := json.Marshal(map[string]any{"output_schema": writercontract.UTF8Schema()})
			i, err := runtime.NewInvocation(p, string(raw))
			if err != nil {
				t.Fatal(err)
			}
			settings := ThreadSettings{ThreadID: "thread", Model: p.Model, Provider: p.Provider, Effort: &p.Effort, Directory: t.TempDir(), Approval: "never", Sandbox: "readOnly"}
			request := scriptedResponse(t, "", map[string]any{"turn": map[string]any{"id": "turn", "status": "completed", "items": []any{}}}, func(ctx context.Context, c *Client) error { _, _, e := c.StartTurn(ctx, settings, i); return e })
			var params map[string]json.RawMessage
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			_, present := params["outputSchema"]
			if present != (role == "writer" || role == "fixer") {
				t.Fatal("role schema mismatch", role)
			}
			if present {
				var shape struct {
					Properties struct {
						Changes struct {
							Min int `json:"minItems"`
							Max int `json:"maxItems"`
						} `json:"changes"`
					} `json:"properties"`
				}
				json.Unmarshal(params["outputSchema"], &shape)
				if shape.Properties.Changes.Min != 1 || shape.Properties.Changes.Max != 64 {
					t.Fatal("lost cardinality")
				}
			}
		})
	}
}
