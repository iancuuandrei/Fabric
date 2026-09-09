package opencode

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/sourcetools"
	"harness.local/engorch/internal/toolbridge"
)

func TestProjectOpenCodeOpenAIToolsBindsOriginalAndProjectedCatalogs(t *testing.T) {
	catalog := []toolbridge.ToolDefinition{{
		Name: "source_read", Description: "Read source.",
		InputSchema: json.RawMessage(`{
			"type":"object","title":"dropped","additionalProperties":false,
			"properties":{
				"path":{"type":"string","description":"relative","pattern":"^[a-z]+$"},
				"offset":{"type":"integer","minimum":0},
				"limit":{"type":"integer","minimum":1,"maximum":32768,"default":4}
			},
			"required":["path","offset","limit"]
		}`),
	}}
	receipt, err := ProjectOpenCodeOpenAITools(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Version != 1 || receipt.Projection != OpenCodeOpenAISchemaProjection || receipt.UpstreamCommit != OpenCodeOpenAIUpstreamCommit || receipt.TransformSHA256 != OpenCodeOpenAITransformSHA256 || len(receipt.Tools) != 1 {
		t.Fatal("projection identity mismatch", receipt)
	}
	want := `{"additionalProperties":false,"properties":{"limit":{"type":"integer"},"offset":{"type":"integer"},"path":{"description":"relative","type":"string"}},"required":["path","offset","limit"],"type":"object"}`
	if string(receipt.Tools[0].Parameters) != want {
		t.Fatalf("projected schema mismatch\n got %s\nwant %s", receipt.Tools[0].Parameters, want)
	}
	server, err := toolbridge.New(toolbridge.Config{
		Token:   strings.Repeat("t", 32),
		Catalog: func() ([]toolbridge.ToolDefinition, error) { return catalog, nil },
		Call: func(context.Context, toolbridge.Call) (toolbridge.Result, error) {
			return toolbridge.Result{JSON: json.RawMessage(`{}`)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.OriginalCatalogSHA256 != server.CatalogHash() || len(receipt.ProjectedCatalogSHA256) != 64 || receipt.ProjectedCatalogSHA256 == receipt.OriginalCatalogSHA256 {
		t.Fatal("catalog digest binding mismatch", receipt)
	}
	catalog[0].Name = "changed"
	catalog[0].InputSchema[0] = '['
	if receipt.Tools[0].Name != "source_read" || string(receipt.Tools[0].Parameters) != want {
		t.Fatal("receipt aliases caller catalog")
	}
}

func TestProjectOpenCodeOpenAISchemaMatchesPinnedInference(t *testing.T) {
	input := []toolbridge.ToolDefinition{{Name: "complex", InputSchema: json.RawMessage(`{
		"type":"object","properties":{
			"booleanSchema":true,
			"constant":{"const":"x","type":"string"},
			"reference":{"$ref":"#/$defs/item","minimum":2},
			"composition":{"oneOf":[{"type":"string","minLength":2},{"type":"null"}]},
			"object":{"required":["x",4],"additionalProperties":{"type":"integer","maximum":9}},
			"array":{"prefixItems":[{"type":"number"}]},
			"formatted":{"format":"date","maxLength":10},
			"bounded":{"minimum":0},
			"unknown":{"title":"gone"},
			"typed":{"type":["integer","bogus","null"]},
			"enumOnly":{"enum":["a","b"]}
		},
		"required":["booleanSchema"],
		"additionalProperties":false
	}`)}}
	receipt, err := ProjectOpenCodeOpenAITools(input)
	if err != nil {
		t.Fatal(err)
	}
	want := json.RawMessage(`{"additionalProperties":false,"properties":{"array":{"items":{"type":"string"},"type":"array"},"booleanSchema":{"type":"string"},"bounded":{"type":"number"},"composition":{"oneOf":[{"type":"string"},{"type":"null"}]},"constant":{"enum":["x"],"type":"string"},"enumOnly":{"enum":["a","b"],"type":"string"},"formatted":{"type":"string"},"object":{"additionalProperties":{"type":"integer"},"properties":{},"required":["x"],"type":"object"},"reference":{"$ref":"#/$defs/item"},"typed":{"type":["integer","null"]},"unknown":{}},"required":["booleanSchema"],"type":"object"}`)
	if !reflect.DeepEqual(receipt.Tools[0].Parameters, want) {
		t.Fatalf("pinned inference mismatch\n got %s\nwant %s", receipt.Tools[0].Parameters, want)
	}
}

func TestProjectOpenCodeOpenAIToolsRejectsUnboundCatalogs(t *testing.T) {
	valid := toolbridge.ToolDefinition{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}
	cases := map[string][]toolbridge.ToolDefinition{
		"empty":         {},
		"duplicate":     {valid, valid},
		"bad name":      {{Name: "read.dot", InputSchema: valid.InputSchema}},
		"bad schema":    {{Name: "read", InputSchema: json.RawMessage(`{"type":"object","x":1,"x":2}`)}},
		"non-object":    {{Name: "read", InputSchema: json.RawMessage(`{"type":"array"}`)}},
		"unsafe number": {{Name: "read", InputSchema: json.RawMessage(`{"type":"object","minimum":9007199254740992}`)}},
	}
	for name, catalog := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ProjectOpenCodeOpenAITools(catalog); err == nil {
				t.Fatal("unsafe catalog accepted")
			}
		})
	}
}

func TestProjectedWireSchemaDoesNotWeakenBrokerBounds(t *testing.T) {
	root := t.TempDir()
	git := func(arguments ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("committed"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "source")
	source, err := repository.Discover(context.Background(), root, "provider-schema-bound")
	if err != nil {
		t.Fatal(err)
	}
	catalog := sourcetools.Catalog()
	tools := make([]toolbridge.ToolDefinition, 0, len(catalog))
	for _, definition := range catalog {
		schema, encodeErr := canonical.Bytes(definition.InputSchema)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		tools = append(tools, toolbridge.ToolDefinition{Name: definition.Name, Description: definition.Description, InputSchema: schema})
	}
	projection, err := ProjectOpenCodeOpenAITools(tools)
	if err != nil {
		t.Fatal(err)
	}
	var read ProviderWireTool
	for _, tool := range projection.Tools {
		if tool.Name == sourcetools.ReadName {
			read = tool
		}
	}
	if read.Name == "" || strings.Contains(string(read.Parameters), "minimum") || strings.Contains(string(read.Parameters), "maximum") {
		t.Fatal("pinned wire projection retained numeric bounds", string(read.Parameters))
	}
	binding, err := contextbroker.NewBinding(strings.Repeat("a", 64), source, nil, contextbroker.Limits{MaxCalls: 1, MaxRequestBytes: 4096, MaxResponseBytes: 4096, MaxTotalResponseBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := contextbroker.Open(filepath.Join(t.TempDir(), "context.jsonl"), binding)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	response, err := broker.Call(context.Background(), "out-of-range", sourcetools.ReadName, json.RawMessage(`{"path":"source.txt","offset":0,"limit":32769}`))
	if err != nil || response.Success || !strings.Contains(string(response.Content), `"code":"context_request_failed"`) {
		t.Fatal("broker did not deny arguments outside the original catalog bound", response, err)
	}
}
