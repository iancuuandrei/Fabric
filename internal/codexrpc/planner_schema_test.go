package codexrpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestPlannerGraphSchemaBoundToWire(t *testing.T) {
	p := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "exact", Effort: "high", Role: "planner"}
	for _, schema := range []json.RawMessage{nil, engineeringplan.PlannerJSONSchema()} {
		payload := map[string]any{}
		if schema != nil {
			payload["output_schema"] = schema
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		i, err := runtime.NewInvocation(p, string(raw))
		if err != nil {
			t.Fatal(err)
		}
		settings := ThreadSettings{ThreadID: "thread", Model: p.Model, Provider: p.Provider, Effort: &p.Effort, Directory: t.TempDir(), Approval: "never", Sandbox: "readOnly"}
		request := scriptedResponse(t, "", map[string]any{"turn": map[string]any{"id": "turn", "status": "completed", "items": []any{}}}, func(ctx context.Context, c *Client) error {
			_, _, err := c.StartTurn(ctx, settings, i)
			return err
		})
		var params map[string]json.RawMessage
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatal(err)
		}
		wireSchema, present := params["outputSchema"]
		if present != (schema != nil) {
			t.Fatal("planner wire schema presence differs from invocation")
		}
		if present {
			got, _ := canonical.Hash("schema", wireSchema)
			want, _ := canonical.Hash("schema", schema)
			if got != want {
				t.Fatal("planner schema changed on wire")
			}
		}
	}
}

func TestPlannerSchemaSubstitutionFailsBeforeRPC(t *testing.T) {
	p := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "exact", Effort: "high", Role: "planner"}
	i, err := runtime.NewInvocation(p, `{"output_schema":{"type":"string"}}`)
	if err != nil {
		t.Fatal(err)
	}
	settings := ThreadSettings{ThreadID: "thread", Model: p.Model, Provider: p.Provider, Effort: &p.Effort, Directory: t.TempDir(), Approval: "never", Sandbox: "readOnly"}
	_, _, err = (&Client{}).StartTurn(context.Background(), settings, i)
	if err == nil || !strings.Contains(err.Error(), "planner output schema substitution") {
		t.Fatal("invalid schema reached provider transport", err)
	}
}

func TestPlannerAgentContextSchemaBoundToWire(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(engineeringplan.PlannerJSONSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	items := schema["properties"].(map[string]any)["tasks"].(map[string]any)["items"].(map[string]any)
	items["properties"].(map[string]any)["skills"] = map[string]any{"type": "array", "maxItems": 4, "items": map[string]any{"type": "string", "pattern": "^[a-z0-9]+(-[a-z0-9]+)*$", "maxLength": 64}}
	items["required"] = append(items["required"].([]any), "skills")
	raw, err := json.Marshal(map[string]any{"output_schema": schema})
	if err != nil {
		t.Fatal(err)
	}
	p := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "exact", Effort: "high", Role: "planner"}
	i, err := runtime.NewInvocation(p, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	settings := ThreadSettings{ThreadID: "thread", Model: p.Model, Provider: p.Provider, Effort: &p.Effort, Directory: t.TempDir(), Approval: "never", Sandbox: "readOnly"}
	request := scriptedResponse(t, "", map[string]any{"turn": map[string]any{"id": "turn", "status": "completed", "items": []any{}}}, func(ctx context.Context, c *Client) error {
		_, _, err := c.StartTurn(ctx, settings, i)
		return err
	})
	var params map[string]json.RawMessage
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	expectedSchema, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	sharedSchema, err := engineeringplan.AgentContextPlannerJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	if string(sharedSchema) != string(expectedSchema) {
		t.Fatal("shared schema changed the previous augmentation bytes")
	}
	want, err := canonical.Hash("schema", json.RawMessage(expectedSchema))
	if err != nil {
		t.Fatal(err)
	}
	got, err := canonical.Hash("schema", params["outputSchema"])
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatal("agent-context schema changed on wire")
	}
}

func TestPlannerAgentContextSchemaMutationFailsBeforeRPC(t *testing.T) {
	schema, err := engineeringplan.AgentContextPlannerJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	for _, replacement := range []string{`"maxItems":5`, `"maxItems":40`} {
		mutated := strings.Replace(string(schema), `"maxItems":4`, replacement, 1)
		if mutated == string(schema) {
			t.Fatal("negative control did not mutate the skill bound")
		}
		raw, err := json.Marshal(map[string]any{"output_schema": json.RawMessage(mutated)})
		if err != nil {
			t.Fatal(err)
		}
		p := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "exact", Effort: "high", Role: "planner"}
		i, err := runtime.NewInvocation(p, string(raw))
		if err != nil {
			t.Fatal(err)
		}
		settings := ThreadSettings{ThreadID: "thread", Model: p.Model, Provider: p.Provider, Effort: &p.Effort, Directory: t.TempDir(), Approval: "never", Sandbox: "readOnly"}
		_, _, err = (&Client{}).StartTurn(context.Background(), settings, i)
		if err == nil || !strings.Contains(err.Error(), "planner output schema substitution") {
			t.Fatal("mutated schema reached provider transport", err)
		}
	}
}
