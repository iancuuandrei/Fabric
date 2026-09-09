package codexruntime

import (
	"context"
	"encoding/json"
	"testing"

	"harness.local/engorch/internal/canonical"
)

func TestContextAccountingIncludesFailedAndPendingCalls(t *testing.T) {
	a := sourceAdapter(t)
	for _, entry := range []struct{ id, path string }{{"read", "source.txt"}, {"missing", "missing.txt"}} {
		args, err := canonical.Bytes(readArgs{Path: entry.path, Offset: 0, Limit: 32})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := canonical.Bytes(ToolRequest{Arguments: args, CallID: entry.id, ThreadID: "thread-1", TurnID: "turn-1", Tool: "source_read"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.HandleTool(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := appendEvent(a.JournalPath, "runtime.tool-request", ToolRequest{Arguments: json.RawMessage(`{"after":"","limit":1}`), CallID: "pending", ThreadID: "thread-1", TurnID: "turn-1", Tool: "source_list"}); err != nil {
		t.Fatal(err)
	}
	s, head, err := InspectWithHead(a.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	r, err := MeasureContext(a.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	if r.JournalHead != head || r.Completed || r.PendingCalls != 1 || r.InputBytes != len(s.Intent.Invocation.Input) || r.ProviderUsage.InputTokens != nil || r.OutputBytes != 0 {
		t.Fatal("context accounting scope mismatch", r)
	}
	if len(r.Tools) != 2 || r.Tools[0].Tool != "source_list" || r.Tools[0].Requests != 1 || r.Tools[0].Responses != 0 || r.Tools[1].Requests != 2 || r.Tools[1].Responses != 2 || r.Tools[1].Failures != 1 {
		t.Fatal("tool accounting mismatch", r.Tools)
	}
	want := 0
	for _, response := range s.ToolResponses {
		want += len(response.Content)
	}
	if r.ToolContentBytes != want || r.Tools[1].ContentBytes != want {
		t.Fatal("content byte count mismatch")
	}
}
