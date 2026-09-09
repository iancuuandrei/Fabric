package codexruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/worktree"
)

func TestCandidateBrokerReadsModifiedStateAndRejectsDrift(t *testing.T) {
	a := sourceAdapter(t)
	r, err := worktree.Prepare(strings.Repeat("a", 64), *a.Source)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := worktree.Acquire(r)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	b, err := worktree.Create(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Path, "source.txt"), []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := worktree.Fingerprint(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	a.Candidate = &CandidateBinding{Workspace: b, Candidate: c}
	events, err := journal.Read(a.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	a.JournalPath = filepath.Join(t.TempDir(), "candidate.jsonl")
	for _, e := range events {
		if err := appendEvent(a.JournalPath, e.Kind, e.Payload); err != nil {
			t.Fatal(err)
		}
		if e.Kind == "runtime.source" {
			if err := appendEvent(a.JournalPath, "runtime.candidate", *a.Candidate); err != nil {
				t.Fatal(err)
			}
		}
	}
	call := func(name, id, args string) ToolResponse {
		t.Helper()
		raw, err := canonical.Bytes(ToolRequest{Arguments: json.RawMessage(args), CallID: id, ThreadID: "thread-1", TurnID: "turn-1", Tool: name})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.HandleTool(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
		s, err := Inspect(a.JournalPath)
		if err != nil {
			t.Fatal(err)
		}
		if s.PendingTool != nil {
			t.Fatal("tool response not durable")
		}
		return s.ToolResponses[len(s.ToolResponses)-1]
	}
	listed := call("candidate_list", "list", `{"after":"","limit":1}`)
	if !listed.Success {
		t.Fatal(listed)
	}
	var page candidatePage
	if err := canonical.Decode([]byte(listed.Content), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Files) != 1 || page.Files[0].Path != "source.txt" || page.NextAfter != nil {
		t.Fatal("candidate list mismatch")
	}
	read := call("candidate_read", "read", `{"path":"source.txt","offset":0,"limit":32}`)
	if !read.Success {
		t.Fatal(read)
	}
	var chunk worktree.SourceChunk
	if err := canonical.Decode([]byte(read.Content), &chunk); err != nil {
		t.Fatal(err)
	}
	if chunk.ContentUTF8 == nil || *chunk.ContentUTF8 != "modified" || chunk.CandidateID != page.CandidateID || chunk.SHA256 != page.Files[0].Hash {
		t.Fatal("candidate read used base or wrong hash")
	}
	if err := os.WriteFile(filepath.Join(r.Path, "extra"), []byte("drift"), 0600); err != nil {
		t.Fatal(err)
	}
	if call("candidate_read", "drift", `{"path":"source.txt","offset":0,"limit":32}`).Success {
		t.Fatal("drift admitted")
	}
}
