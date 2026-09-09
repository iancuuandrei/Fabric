package cli

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"harness.local/engorch/internal/codexruntime"
	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/journal"
)

func assertExplorerReads(t *testing.T, s control.Snapshot) {
	t.Helper()
	path := filepath.Join(s.ExplorerHost.Intent.Launch.Root, "explorer.jsonl")
	state, head, err := codexruntime.InspectWithHead(path)
	if err != nil || head != s.ExplorerHost.RuntimeReceipt.JournalHead || state.Candidate == nil || state.Candidate.Candidate != *s.Candidate || state.PendingTool != nil {
		t.Fatal("explorer runtime evidence mismatch", err)
	}
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]string{}
	for _, event := range events {
		if event.Kind == "runtime.tool-request" {
			var request codexruntime.ToolRequest
			if err := json.Unmarshal(event.Payload, &request); err != nil {
				t.Fatal(err)
			}
			calls[request.CallID] = request.Tool
		}
	}
	success := map[string]bool{}
	for _, response := range state.ToolResponses {
		if response.Success {
			success[calls[response.CallID]] = true
		}
	}
	if !success["candidate_list"] || !success["candidate_read"] {
		t.Fatal("explorer did not execute successful candidate reads")
	}
}
