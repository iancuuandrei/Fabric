package codexrpc

import (
	"context"
	"encoding/json"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/writercontract"
	"strings"
	"testing"
)

func TestWriterSchemaBoundToWireAndReadOnlyRolesUnaffected(t *testing.T) {
	for _, role := range []string{"writer", "fixer", "reviewer"} {
		t.Run(role, func(t *testing.T) {
			p := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "exact", Effort: "medium", Role: role}
			payload := map[string]any{"output_schema": writercontract.Schema()}
			if role == "reviewer" {
				payload = map[string]any{}
			}
			raw, _ := json.Marshal(payload)
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
	for _, role := range []string{"writer", "fixer", "reviewer"} {
		t.Run(role, func(t *testing.T) {
			p := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "exact", Effort: "medium", Role: role}
			payload := map[string]any{"output_schema": writercontract.UTF8Schema()}
			if role == "reviewer" {
				payload = map[string]any{}
			}
			raw, _ := json.Marshal(payload)
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

func TestCandidateBoundWriterSchemaIsForwardedOnlyForEnvelopeCandidate(t *testing.T) {
	p := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "exact", Effort: "medium", Role: "writer"}
	candidate := strings.Repeat("a", 64)
	schema, err := writercontract.UTF8SchemaForCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"candidate_id": candidate, "output_schema": schema, "instruction": "write"})
	i, err := runtime.NewInvocation(p, string(input))
	if err != nil {
		t.Fatal(err)
	}
	settings := ThreadSettings{ThreadID: "thread", Model: p.Model, Provider: p.Provider, Effort: &p.Effort, Directory: t.TempDir(), Approval: "never", Sandbox: "readOnly"}
	request := scriptedResponse(t, "", map[string]any{"turn": map[string]any{"id": "turn", "status": "completed", "items": []any{}}}, func(ctx context.Context, c *Client) error {
		_, _, err := c.StartTurn(ctx, settings, i)
		return err
	})
	var params map[string]json.RawMessage
	if err := json.Unmarshal(request.Params, &params); err != nil || string(params["outputSchema"]) == "" {
		t.Fatal("candidate-bound writer schema did not reach turn/start", err)
	}
	wrongSchema, err := writercontract.UTF8SchemaForCandidate(strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := runtime.NewInvocation(p, `{"candidate_id":"`+candidate+`","output_schema":`+string(wrongSchema)+`,"instruction":"write"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&Client{}).StartTurn(context.Background(), settings, wrong); err == nil || !strings.Contains(err.Error(), "schema substitution") {
		t.Fatalf("writer schema for another candidate was not rejected before RPC: %v", err)
	}
}

func TestCandidateBoundAnchoredWriterSchemaIsForwardedForExactEnvelopeCandidate(t *testing.T) {
	p := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "exact", Effort: "medium", Role: "writer"}
	candidate := strings.Repeat("a", 64)
	schema, err := writercontract.AnchoredEditsSchemaForCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"candidate_id": candidate, "output_schema": schema, "instruction": "edit anchors"})
	i, err := runtime.NewInvocation(p, string(input))
	if err != nil {
		t.Fatal(err)
	}
	settings := ThreadSettings{ThreadID: "thread", Model: p.Model, Provider: p.Provider, Effort: &p.Effort, Directory: t.TempDir(), Approval: "never", Sandbox: "readOnly"}
	request := scriptedResponse(t, "", map[string]any{"turn": map[string]any{"id": "turn", "status": "completed", "items": []any{}}}, func(ctx context.Context, c *Client) error {
		_, _, err := c.StartTurn(ctx, settings, i)
		return err
	})
	var params map[string]json.RawMessage
	if err := json.Unmarshal(request.Params, &params); err != nil || string(params["outputSchema"]) == "" {
		t.Fatal("candidate-bound anchored schema did not reach turn/start", err)
	}
	wrongSchema, _ := writercontract.AnchoredEditsSchemaForCandidate(strings.Repeat("b", 64))
	wrongInput, _ := json.Marshal(map[string]any{"candidate_id": candidate, "output_schema": wrongSchema, "instruction": "edit anchors"})
	wrong, err := runtime.NewInvocation(p, string(wrongInput))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&Client{}).StartTurn(context.Background(), settings, wrong); err == nil || !strings.Contains(err.Error(), "schema substitution") {
		t.Fatalf("anchored schema for another candidate was not rejected before RPC: %v", err)
	}
}

func TestCandidateBoundStrictAnchoredWriterSchemaIsForwardedForExactEnvelopeCandidate(t *testing.T) {
	p := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "exact", Effort: "medium", Role: "writer"}
	candidate := strings.Repeat("a", 64)
	schema, err := writercontract.StrictAnchoredEditsSchemaForCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"candidate_id": candidate, "output_schema": schema, "instruction": "edit anchors"})
	i, err := runtime.NewInvocation(p, string(input))
	if err != nil {
		t.Fatal(err)
	}
	settings := ThreadSettings{ThreadID: "thread", Model: p.Model, Provider: p.Provider, Effort: &p.Effort, Directory: t.TempDir(), Approval: "never", Sandbox: "readOnly"}
	request := scriptedResponse(t, "", map[string]any{"turn": map[string]any{"id": "turn", "status": "completed", "items": []any{}}}, func(ctx context.Context, c *Client) error {
		_, _, err := c.StartTurn(ctx, settings, i)
		return err
	})
	var params map[string]json.RawMessage
	if err := json.Unmarshal(request.Params, &params); err != nil || string(params["outputSchema"]) == "" {
		t.Fatal("candidate-bound strict anchored schema did not reach turn/start", err)
	}
	wrongSchema, _ := writercontract.StrictAnchoredEditsSchemaForCandidate(strings.Repeat("b", 64))
	wrongInput, _ := json.Marshal(map[string]any{"candidate_id": candidate, "output_schema": wrongSchema, "instruction": "edit anchors"})
	wrong, err := runtime.NewInvocation(p, string(wrongInput))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&Client{}).StartTurn(context.Background(), settings, wrong); err == nil || !strings.Contains(err.Error(), "schema substitution") {
		t.Fatalf("strict anchored schema for another candidate was not rejected before RPC: %v", err)
	}
}
