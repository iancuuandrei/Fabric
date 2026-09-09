package toolbridge

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCatalogIdentityMatchesConstructedTransport(t *testing.T) {
	catalog := []ToolDefinition{
		{Name: "source_read", InputSchema: json.RawMessage(`{"type":"object", "properties":{}}`)},
		{Name: "spawn_agent", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	identity, err := CatalogIdentity(catalog)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{
		Token:   "0123456789abcdef0123456789abcdef",
		Catalog: func() ([]ToolDefinition, error) { return catalog, nil },
		Call: func(context.Context, Call) (Result, error) {
			t.Fatal("identity construction executed a tool")
			return Result{}, nil
		},
	})
	if err != nil || server.CatalogHash() != identity {
		t.Fatal("identity differs from served catalog", err)
	}
	catalog[0], catalog[1] = catalog[1], catalog[0]
	changed, err := CatalogIdentity(catalog)
	if err != nil || changed == identity {
		t.Fatal("catalog order was not bound", err)
	}
	catalog[1].Name = catalog[0].Name
	if _, err := CatalogIdentity(catalog); err == nil {
		t.Fatal("duplicate catalog identity accepted")
	}
}
