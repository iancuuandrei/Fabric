package opencoderuntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/writercontract"
)

func TestResolvedWriterBindsNativePermissionThroughProductionSessionAndConfig(t *testing.T) {
	schema := writercontract.UTF8Schema()
	expectation, err := opencode.NewStructuredOutputExpectation(schema)
	if err != nil {
		t.Fatal("build structured expectation", err)
	}
	invocation, err := runtime.NewInvocation(runtime.Profile{
		Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "low", Role: "writer",
	}, `{"output_schema":`+string(schema)+`,"instruction":"write"}`)
	if err != nil {
		t.Fatal("build writer invocation", err)
	}
	root := t.TempDir()
	intent := Intent{
		Version: 2, Invocation: invocation, Directory: root,
		SessionPlan: &SessionPlan{
			Agent: "build", Provider: invocation.Profile.Provider, Model: invocation.Profile.Model,
			Variant: invocation.Profile.Effort, ToolNames: []string{"source_list"}, CatalogSHA256: strings.Repeat("a", 64),
		},
		StructuredOutput: &expectation,
	}
	binding, err := intent.ResolveToolSessionBinding("global")
	if err != nil {
		t.Fatal("resolve structured writer session", err)
	}
	if binding.StructuredOutput == nil || binding.StructuredOutput == intent.StructuredOutput {
		t.Fatal("resolved session did not carry an independent structured expectation")
	}
	if string(binding.StructuredOutput.Schema) != string(expectation.Schema) {
		t.Fatal("resolved session structured schema changed")
	}

	tools := opencode.ToolsConfigurationSpec{
		Endpoint: "http://127.0.0.1:43123/mcp", Bearer: strings.Repeat("a", 32),
		ToolNames: []string{"source_list"}, TimeoutMillis: 5000, AllowStructuredOutput: intent.StructuredOutput != nil,
	}
	config, err := opencode.BuildToolsConfiguration(tools)
	if err != nil {
		t.Fatal("build native bootstrap config", err)
	}
	if !strings.Contains(config, `"StructuredOutput":"allow"`) {
		t.Fatal("native bootstrap config omitted exact StructuredOutput allow")
	}
	legacyConfig, err := opencode.BuildToolsConfiguration(opencode.ToolsConfigurationSpec{
		Endpoint: tools.Endpoint, Bearer: tools.Bearer, ToolNames: tools.ToolNames, TimeoutMillis: tools.TimeoutMillis,
	})
	if err != nil {
		t.Fatal("build legacy bootstrap config", err)
	}
	if strings.Contains(legacyConfig, opencode.StructuredOutputToolName) {
		t.Fatal("legacy bootstrap config gained native StructuredOutput permission")
	}

	permissions := []map[string]string{
		{"permission": "*", "pattern": "*", "action": "deny"},
		{"permission": "engorch_source_list", "pattern": "*", "action": "allow"},
		{"permission": opencode.StructuredOutputToolName, "pattern": "*", "action": "allow"},
	}
	permissionJSON, err := json.Marshal(permissions)
	if err != nil {
		t.Fatal("encode expected permissions", err)
	}
	response, err := json.Marshal(map[string]any{
		"id": "ses_structured", "projectID": binding.Session.ProjectID, "directory": binding.Session.Directory,
		"agent": binding.Session.Agent, "title": "engorch:" + binding.Session.IntentID,
		"metadata":   map[string]string{"engorch_intent_id": binding.Session.IntentID, "engorch_catalog_sha256": binding.CatalogSHA256},
		"model":      map[string]string{"id": binding.Session.Model, "providerID": binding.Session.Provider, "variant": binding.Session.Variant},
		"permission": json.RawMessage(permissionJSON),
	})
	if err != nil {
		t.Fatal("encode session response", err)
	}
	posts, reads := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/config":
			_, _ = w.Write([]byte(config))
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			posts++
			var request struct {
				Permission []map[string]string `json:"permission"`
			}
			if decodeErr := json.NewDecoder(r.Body).Decode(&request); decodeErr != nil {
				t.Errorf("decode production session request: %v", decodeErr)
			} else {
				got, _ := json.Marshal(request.Permission)
				if string(got) != string(permissionJSON) {
					t.Errorf("production session permission differs\n got: %s\nwant: %s", got, permissionJSON)
				}
			}
			_, _ = w.Write(response)
		case r.Method == http.MethodGet && r.URL.Path == "/session/ses_structured":
			reads++
			_, _ = w.Write(response)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := opencode.NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal("build fixture OpenCode client", err)
	}
	defer client.Close()
	configReceipt, err := client.ReadToolsConfiguration(context.Background(), tools)
	if err != nil || !configReceipt.AllowStructuredOutput || len(configReceipt.ToolIDs) != 1 || configReceipt.ToolIDs[0] != "engorch_source_list" {
		t.Fatal("production bootstrap config readback lost native marker", configReceipt, err)
	}
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	id, err := client.CreateToolSession(context.Background(), sessionPath, binding)
	if err != nil || id != "ses_structured" || posts != 1 || reads != 1 {
		t.Fatal("production structured session creation failed", id, err, posts, reads)
	}
	record, err := opencode.ReadToolSession(sessionPath, binding)
	if err != nil || record.Binding.StructuredOutput == nil || record.Binding.StructuredOutput.SchemaSHA256 != expectation.SchemaSHA256 {
		t.Fatal("structured permission marker was not durable", record, err)
	}
	raw, err := journal.ExportJSONL(sessionPath)
	if err != nil || !strings.Contains(string(raw), `"structured_output"`) {
		t.Fatal("session journal omitted structured identity", err)
	}
}

func TestResolvedStructuredPermissionRejectsPlannerAndSchemaSubstitution(t *testing.T) {
	schema := writercontract.UTF8Schema()
	expectation, err := opencode.NewStructuredOutputExpectation(schema)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(runtime.Profile{
		Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "low", Role: "writer",
	}, `{"output_schema":`+string(schema)+`,"instruction":"write"}`)
	if err != nil {
		t.Fatal(err)
	}
	base := Intent{Version: 2, Invocation: invocation, Directory: t.TempDir(), SessionPlan: &SessionPlan{
		Agent: "build", Provider: invocation.Profile.Provider, Model: invocation.Profile.Model,
		Variant: invocation.Profile.Effort, ToolNames: []string{"source_list"}, CatalogSHA256: strings.Repeat("a", 64),
	}, StructuredOutput: &expectation}
	planner := base
	planner.Invocation.Profile.Role = "planner"
	if _, err := planner.ResolveToolSessionBinding("global"); err == nil {
		t.Fatal("planner received native StructuredOutput session permission")
	}
	wrongSchema := json.RawMessage(`{"type":"object","properties":{"wrong":{"type":"string"}},"required":["wrong"],"additionalProperties":false}`)
	wrong, err := opencode.NewStructuredOutputExpectation(wrongSchema)
	if err != nil {
		t.Fatal(err)
	}
	substituted := base
	substituted.StructuredOutput = &wrong
	if _, err := substituted.ResolveToolSessionBinding("global"); err == nil {
		t.Fatal("substituted schema received native StructuredOutput session permission")
	}
}
