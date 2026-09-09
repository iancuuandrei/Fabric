package cli

import (
	"context"
	"path/filepath"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/codexrpc"
	"harness.local/engorch/internal/codexruntime"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/ri"
	"harness.local/engorch/internal/runtime"
)

// Provider correlation is synthetic; index, publication and Rust queries are real.
func exercisePublishedBroker(t *testing.T, source repository.Identity, binding codexruntime.RIBinding, offset int, digest string) {
	t.Helper()
	root := t.TempDir()
	a := codexruntime.Adapter{Directory: root, JournalPath: filepath.Join(root, "runtime.jsonl"), Source: &source, RI: &binding}
	i, err := runtime.NewInvocation(runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "explicit-fixture-model", Effort: "high", Role: "planner"}, "Navigate the published fixture")
	if err != nil {
		t.Fatal(err)
	}
	effort := i.Profile.Effort
	for _, e := range []struct {
		kind    string
		payload any
	}{
		{"runtime.intent", codexruntime.Intent{Invocation: i, Directory: root}},
		{"runtime.source", source},
		{"runtime.ri", binding},
		{"runtime.thread", codexrpc.ThreadSettings{ThreadID: "thread-1", Model: i.Profile.Model, Provider: i.Profile.Provider, Effort: &effort, Directory: root, Approval: "never", Sandbox: "readOnly"}},
		{"runtime.turn-intent", map[string]any{"invocation_id": i.ID}},
	} {
		if _, err := journal.Append(a.JournalPath, e.kind, e.payload, func([]journal.Event) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	query := func(name string, args any) ri.OccurrencePage {
		t.Helper()
		arguments, err := canonical.Bytes(args)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := canonical.Bytes(codexruntime.ToolRequest{Arguments: arguments, CallID: name, ThreadID: "thread-1", TurnID: "turn-1", Tool: name})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.HandleTool(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
		state, err := codexruntime.Inspect(a.JournalPath)
		if err != nil {
			t.Fatal(err)
		}
		response := state.ToolResponses[len(state.ToolResponses)-1]
		if !response.Success || state.PendingTool != nil {
			t.Fatal("broker call incomplete", response)
		}
		var page ri.OccurrencePage
		if err := canonical.Decode([]byte(response.Content), &page); err != nil {
			t.Fatal(err)
		}
		if page.SnapshotID != binding.Snapshot.ID || page.AbsenceProven {
			t.Fatal("broker page provenance mismatch")
		}
		return page
	}
	located := query("ri_locate", map[string]any{"path": "library.go", "offset": offset, "producer": "scip-go", "limit": 16, "after": nil})
	if len(located.Occurrences) != 1 || located.Occurrences[0].Symbol == nil {
		t.Fatal("broker did not locate real symbol")
	}
	definitions := query("ri_definition", map[string]any{"symbol": *located.Occurrences[0].Symbol, "producer": "scip-go", "limit": 16, "after": nil})
	if len(definitions.Occurrences) != 1 || definitions.Occurrences[0].Spelling != "Greeting" || definitions.Occurrences[0].SourceSHA256 != digest {
		t.Fatal("broker definition provenance mismatch")
	}
	references := query("ri_references", map[string]any{"symbol": *located.Occurrences[0].Symbol, "producer": "scip-go", "limit": 16, "after": nil})
	if len(references.Occurrences) != 0 {
		t.Fatal("definition leaked into reference query")
	}
}
