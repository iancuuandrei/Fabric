package opencoderuntime

import (
	"bytes"
	"testing"

	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/toolbridge"
)

func TestProviderRequestToolsForCatalogPreservesContextProjection(t *testing.T) {
	f := newRuntimeFixture(t)
	p, err := contextmcp.Projection(f.broker)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := p.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ProviderRequestToolsForCatalog(catalog, f.intent.Session.ToolNames)
	want, oldErr := ProviderRequestToolsForSession(f.broker.Catalog(), f.intent.Session.ToolNames)
	if err != nil || oldErr != nil || !equalCanonical(got, want) {
		t.Fatalf("legacy projection differs: %v %v", err, oldErr)
	}
	catalog = append(catalog, toolbridge.ToolDefinition{Name: "send_message", Description: "Send a bounded message", InputSchema: []byte(`{"type":"object","properties":{"body":{"type":"string"}},"required":["body"],"additionalProperties":false}`)})
	mixed, err := ProviderRequestToolsForCatalog(catalog, []string{"send_message", f.intent.Session.ToolNames[0]})
	if err != nil || len(mixed) != 2 {
		t.Fatalf("mixed projection: %v", err)
	}
	found := false
	for _, tool := range mixed {
		if tool.Name == "engorch_send_message" {
			found = true
		}
	}
	if !found {
		t.Fatal("agent tool missing namespace")
	}
	before := append([]byte(nil), catalog[len(catalog)-1].InputSchema...)
	for i := range mixed {
		mixed[i].Parameters[0] ^= 1
	}
	if !bytes.Equal(before, catalog[len(catalog)-1].InputSchema) {
		t.Fatal("projection aliases source schema")
	}
	for _, names := range [][]string{{"missing"}, {"send_message", "send_message"}} {
		if _, err := ProviderRequestToolsForCatalog(catalog, names); err == nil {
			t.Fatal("invalid selection accepted", names)
		}
	}
	duplicate := append(append([]toolbridge.ToolDefinition(nil), catalog...), catalog[0])
	if _, err := ProviderRequestToolsForCatalog(duplicate, f.intent.Session.ToolNames); err == nil {
		t.Fatal("duplicate catalog accepted")
	}
}
