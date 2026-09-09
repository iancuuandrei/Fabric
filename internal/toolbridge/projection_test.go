package toolbridge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestComposeFreezesCatalogAndRoutesOnlyExactOwner(t *testing.T) {
	first := []ToolDefinition{{Name: "source_read", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	reads, callsA, callsB := 0, 0, 0
	a := Projection{Catalog: func() ([]ToolDefinition, error) { reads++; return first, nil }, Call: func(_ context.Context, call Call) (Result, error) {
		callsA++
		return Result{JSON: call.Arguments}, nil
	}}
	b := Projection{Catalog: func() ([]ToolDefinition, error) {
		return []ToolDefinition{{Name: "spawn_agent", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
	}, Call: func(_ context.Context, _ Call) (Result, error) {
		callsB++
		return Result{JSON: json.RawMessage(`{"queued":true}`)}, nil
	}}
	projection, err := Compose(a, b)
	if err != nil {
		t.Fatal(err)
	}
	first[0].Name = "substituted"
	first[0].InputSchema[0] = '!'
	catalog, err := projection.Catalog()
	if err != nil || len(catalog) != 2 || catalog[0].Name != "source_read" || catalog[1].Name != "spawn_agent" || !json.Valid(catalog[0].InputSchema) {
		t.Fatal(catalog, err)
	}
	catalog[0].InputSchema[0] = '!'
	again, _ := projection.Catalog()
	if !json.Valid(again[0].InputSchema) || reads != 1 {
		t.Fatal("catalog snapshot mutated or reread")
	}
	if _, err := projection.Call(context.Background(), Call{Tool: "spawn_agent", Arguments: json.RawMessage(`{}`)}); err != nil || callsA != 0 || callsB != 1 {
		t.Fatal("wrong callback", err)
	}
	if _, err := projection.Call(context.Background(), Call{Tool: "substituted"}); err == nil || callsA != 0 || callsB != 1 {
		t.Fatal("unknown tool dispatched")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := projection.Call(ctx, Call{Tool: "source_read"}); !errors.Is(err, context.Canceled) || callsA != 0 {
		t.Fatal("cancelled call dispatched", err)
	}
}

func TestComposeRejectsCollisionsAndInvalidProjections(t *testing.T) {
	projection := Projection{Catalog: func() ([]ToolDefinition, error) {
		return []ToolDefinition{{Name: "same", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
	}, Call: func(context.Context, Call) (Result, error) {
		t.Fatal("construction executed tool")
		return Result{}, nil
	}}
	for _, inputs := range [][]Projection{nil, {{}}, {projection, projection}} {
		if _, err := Compose(inputs...); err == nil {
			t.Fatal("invalid composition accepted")
		}
	}
}
