package writercontract

import (
	"encoding/json"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"strings"
	"testing"
)

func TestSchemaAndDomainCardinality(t *testing.T) {
	var doc any
	if err := json.Unmarshal(Schema(), &doc); err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("urn:writer", doc); err != nil {
		t.Fatal(err)
	}
	schema, err := c.Compile("urn:writer")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 1, 64, 65} {
		changes := []any{}
		for k := 0; k < n; k++ {
			changes = append(changes, map[string]any{"path": "new.py", "before_hash": nil, "content_base64": "eA==", "executable": false})
		}
		payload := map[string]any{"candidate_id": strings.Repeat("a", 64), "changes": changes}
		valid := n >= 1 && n <= 64
		if (schema.Validate(payload) == nil) != valid {
			t.Fatalf("schema count %d", n)
		}
		if (ValidateCount(n) == nil) != valid {
			t.Fatalf("domain count %d", n)
		}
	}
}

func TestUTF8SchemaAndDomainCardinality(t *testing.T) {
	var doc any
	if err := json.Unmarshal(UTF8Schema(), &doc); err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("urn:writer", doc); err != nil {
		t.Fatal(err)
	}
	schema, err := c.Compile("urn:writer")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 1, 64, 65} {
		changes := []any{}
		for k := 0; k < n; k++ {
			changes = append(changes, map[string]any{"path": "new.py", "before_hash": nil, "content_utf8": "eA==", "executable": false})
		}
		payload := map[string]any{"candidate_id": strings.Repeat("a", 64), "changes": changes}
		valid := n >= 1 && n <= 64
		if (schema.Validate(payload) == nil) != valid {
			t.Fatalf("schema count %d", n)
		}
		if (ValidateCount(n) == nil) != valid {
			t.Fatalf("domain count %d", n)
		}
	}
}
