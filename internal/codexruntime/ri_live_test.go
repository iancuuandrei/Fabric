package codexruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/ri"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActualRIBrokerStatus(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("requires Rust RI binary")
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	a := sourceAdapter(t)
	source, err := ri.FromRepository(*a.Source)
	if err != nil {
		t.Fatal(err)
	}
	manifest := ri.Manifest{Format: 1, Source: source, Producers: []ri.Producer{{ID: "fixture", Name: "fixture", Version: "1", ArtifactSHA256: strings.Repeat("a", 64), Inputs: []ri.Input{{Name: "fixture", SHA256: strings.Repeat("b", 64)}}}}}
	record, err := canonical.Bytes(map[string]any{"kind": "manifest", "value": manifest})
	if err != nil {
		t.Fatal(err)
	}
	data := append(record, '\n')
	for _, node := range []map[string]any{
		{"id": "file", "kind": "FILE", "path": "source.go"},
		{"id": "missing", "kind": "SYMBOL", "path": "source.go"},
	} {
		record, err := canonical.Bytes(map[string]any{"kind": "node", "value": node})
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, record...)
		data = append(data, '\n')
	}
	id := ri.SnapshotID(data)
	artifact, err := (ri.Store{Directory: t.TempDir()}).Publish(id, data)
	if err != nil {
		t.Fatal(err)
	}
	a.RI = &RIBinding{Snapshot: ri.SnapshotRef{Path: artifact, ID: id, Source: source}, Executable: executable, ExecutableSHA256: fmt.Sprintf("%x", sha256.Sum256(binary))}
	events, err := journal.Read(a.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	a.JournalPath = filepath.Join(t.TempDir(), "ri-runtime.jsonl")
	for _, event := range events {
		if err := appendEvent(a.JournalPath, event.Kind, event.Payload); err != nil {
			t.Fatal(err)
		}
		if event.Kind == "runtime.source" {
			if err := appendEvent(a.JournalPath, "runtime.ri", *a.RI); err != nil {
				t.Fatal(err)
			}
		}
	}
	call := func(callID string) ToolResponse {
		t.Helper()
		raw, err := canonical.Bytes(ToolRequest{Arguments: json.RawMessage("{}"), CallID: callID, ThreadID: "thread-1", TurnID: "turn-1", Tool: "ri_status"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.HandleTool(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
		state, err := Inspect(a.JournalPath)
		if err != nil {
			t.Fatal(err)
		}
		if state.PendingTool != nil {
			t.Fatal("tool response not durably completed")
		}
		return state.ToolResponses[len(state.ToolResponses)-1]
	}
	response := call("status-1")
	if !response.Success {
		t.Fatal(response.Content)
	}
	var status ri.Status
	if err := canonical.Decode([]byte(response.Content), &status); err != nil {
		t.Fatal(err)
	}
	if status.SnapshotID != id || status.Source != source {
		t.Fatal("broker snapshot identity mismatch")
	}
	for i, test := range []struct {
		tool  string
		args  string
		valid bool
	}{
		{"ri_locate", `{"path":"source.go","offset":0,"producer":"fixture","limit":1,"after":null}`, true},
		{"ri_definition", `{"symbol":"missing","producer":"fixture","limit":1,"after":null}`, true},
		{"ri_references", `{"symbol":"missing","producer":"fixture","limit":1,"after":null}`, true},
		{"ri_definition", `{"symbol":"missing","producer":"fixture","limit":1,"after":null,"definitions":false}`, false},
		{"ri_locate", `{"path":"source.go","offset":0,"producer":"fixture","limit":1,"after":null,"snapshot_id":"other"}`, false},
		{"ri_references", `{"symbol":"missing","producer":"fixture","limit":129,"after":null}`, false},
		{"ri_references", `{"symbol":"missing","producer":"fixture","limit":1,"after":"foreign-cursor"}`, false},
	} {
		raw, err := canonical.Bytes(ToolRequest{Arguments: json.RawMessage(test.args), CallID: fmt.Sprintf("semantic-%d", i), ThreadID: "thread-1", TurnID: "turn-1", Tool: test.tool})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.HandleTool(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
		state, err := Inspect(a.JournalPath)
		if err != nil {
			t.Fatal(err)
		}
		response := state.ToolResponses[len(state.ToolResponses)-1]
		if response.Success != test.valid || state.PendingTool != nil {
			_, detail := semanticRead(context.Background(), *a.RI, test.tool, json.RawMessage(test.args))
			t.Logf("direct query error: %v", detail)
			t.Fatalf("%s: unexpected receipt: %+v", test.tool, response)
		}
		if test.valid {
			var page ri.OccurrencePage
			if err := canonical.Decode([]byte(response.Content), &page); err != nil {
				t.Fatal(err)
			}
			if page.SnapshotID != id || page.AbsenceProven || len(page.Occurrences) != 0 || page.NextAfter != nil {
				t.Fatalf("unexpected empty snapshot page: %+v", page)
			}
		}
	}
	if err := os.WriteFile(artifact, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if call("status-2").Success {
		t.Fatal("broker admitted changed snapshot bytes")
	}
}
