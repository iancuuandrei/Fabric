package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func toolsSpec() ToolsConfigurationSpec {
	return ToolsConfigurationSpec{
		Endpoint:      "http://127.0.0.1:42117/mcp",
		Bearer:        strings.Repeat("a", 32),
		ToolNames:     []string{"source_read", "source_list"},
		TimeoutMillis: 30_000,
	}
}

func TestReadToolsConfigurationBindsExactDirectory(t *testing.T) {
	spec := toolsSpec()
	content, err := BuildToolsConfiguration(spec)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Clean(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/config" || request.URL.Query().Get("directory") != directory || len(request.URL.Query()) != 1 {
			t.Error("tools readback did not bind exact directory")
		}
		writer.Header().Set("content-type", "application/json")
		_, _ = writer.Write([]byte(content))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if receipt, err := client.ReadToolsConfigurationInDirectory(context.Background(), directory, spec); err != nil || len(receipt.SHA256) != 64 {
		t.Fatal("directory-bound tools readback failed", err)
	}
}

func TestBuildAndReadToolsConfiguration(t *testing.T) {
	spec := toolsSpec()
	content, err := BuildToolsConfiguration(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"share":"disabled","plugin":[],"mcp":{"engorch":{"type":"remote","url":"http://127.0.0.1:42117/mcp","enabled":true,"headers":{"Authorization":"Bearer ` + spec.Bearer + `"},"oauth":false,"timeout":30000}},"permission":{"*":"deny","engorch_source_list":"allow","engorch_source_read":"allow"}}`
	if content != want {
		t.Fatalf("configuration mismatch\n got: %s\nwant: %s", content, want)
	}
	receipt, err := decodeToolsConfiguration([]byte(content), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.SHA256) != 64 || receipt.MCPServer != "engorch" || receipt.Endpoint != spec.Endpoint || receipt.TimeoutMillis != spec.TimeoutMillis || strings.Join(receipt.ToolIDs, ",") != "engorch_source_list,engorch_source_read" {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || strings.Contains(string(encoded), spec.Bearer) || strings.Contains(string(encoded), "Authorization") {
		t.Fatal("receipt exposed bearer material", err)
	}
}

func TestNativeStructuredOutputPermissionRequiresExplicitOptIn(t *testing.T) {
	spec := toolsSpec()
	spec.AllowStructuredOutput = true
	content, err := BuildToolsConfiguration(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"share":"disabled","plugin":[],"mcp":{"engorch":{"type":"remote","url":"` + spec.Endpoint + `","enabled":true,"headers":{"Authorization":"Bearer ` + spec.Bearer + `"},"oauth":false,"timeout":30000}},"permission":{"*":"deny","engorch_source_list":"allow","engorch_source_read":"allow","StructuredOutput":"allow"}}`
	if content != want {
		t.Fatalf("native permission configuration mismatch\n got: %s\nwant: %s", content, want)
	}
	receipt, err := decodeToolsConfiguration([]byte(content), spec)
	if err != nil || !receipt.AllowStructuredOutput {
		t.Fatal("native permission was not retained by config readback", receipt, err)
	}
	legacy := toolsSpec()
	if _, err := decodeToolsConfiguration([]byte(content), legacy); err == nil {
		t.Fatal("native StructuredOutput permission bypassed explicit opt in")
	}
	mutated := strings.Replace(content, `"StructuredOutput":"allow"`, `"StructuredOutput":"ask"`, 1)
	if _, err := decodeToolsConfiguration([]byte(mutated), spec); err == nil {
		t.Fatal("non-allow StructuredOutput permission was admitted")
	}
}

func TestToolsConfigurationRejectsExpandedOrChangedProjection(t *testing.T) {
	spec := toolsSpec()
	content, err := BuildToolsConfiguration(spec)
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{
		strings.Replace(content, `"mcp":{"engorch":`, `"mcp":{"other":{},"engorch":`, 1),
		strings.Replace(content, `"timeout":30000`, `"timeout":30000,"extra":true`, 1),
		strings.Replace(content, `"enabled":true`, `"enabled":false`, 1),
		strings.Replace(content, `"oauth":false`, `"oauth":{}`, 1),
		strings.Replace(content, spec.Endpoint, "http://127.0.0.1:42118/mcp", 1),
		strings.Replace(content, spec.Bearer, strings.Repeat("b", 32), 1),
		strings.Replace(content, `"headers":{"Authorization":`, `"headers":{"X-Extra":"x","Authorization":`, 1),
		strings.Replace(content, `"*":"deny","engorch_source_list":"allow"`, `"engorch_source_list":"allow","*":"deny"`, 1),
		strings.Replace(content, `"engorch_source_read":"allow"`, `"engorch_source_read":"allow","bash":"allow"`, 1),
		strings.Replace(content, `"engorch_source_read":"allow"`, `"engorch_source_read":"ask"`, 1),
		strings.Replace(content, `"plugin":[]`, `"plugin":["external"]`, 1),
	}
	for i, raw := range cases {
		if got, err := decodeToolsConfiguration([]byte(raw), spec); err == nil || got.SHA256 != "" {
			t.Fatalf("case %d admitted changed projection: %#v", i, got)
		}
	}
}

func TestToolsConfigurationSpecValidation(t *testing.T) {
	valid := toolsSpec()
	cases := []ToolsConfigurationSpec{
		func() ToolsConfigurationSpec { v := valid; v.Endpoint = "http://localhost:42117/mcp"; return v }(),
		func() ToolsConfigurationSpec { v := valid; v.Endpoint = "http://127.0.0.1:42117/other"; return v }(),
		func() ToolsConfigurationSpec { v := valid; v.Endpoint = "http://127.0.0.1:42117/mcp?"; return v }(),
		func() ToolsConfigurationSpec { v := valid; v.Bearer = "short"; return v }(),
		func() ToolsConfigurationSpec { v := valid; v.Bearer = strings.Repeat("a", 31) + " "; return v }(),
		func() ToolsConfigurationSpec { v := valid; v.TimeoutMillis = 0; return v }(),
		func() ToolsConfigurationSpec { v := valid; v.TimeoutMillis = 30_001; return v }(),
		func() ToolsConfigurationSpec { v := valid; v.ToolNames = nil; return v }(),
		func() ToolsConfigurationSpec { v := valid; v.ToolNames = []string{"source.read"}; return v }(),
		func() ToolsConfigurationSpec { v := valid; v.ToolNames = []string{"same", "same"}; return v }(),
	}
	for i, spec := range cases {
		if content, err := BuildToolsConfiguration(spec); err == nil || content != "" {
			t.Fatalf("case %d admitted invalid spec", i)
		}
	}
}
