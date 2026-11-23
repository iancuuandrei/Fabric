package codexruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/anchoredit"
	"github.com/iancuuandrei/fabric/internal/candidatetools"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/testsupport"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

func TestCandidateBrokerReadsModifiedStateAndRejectsDrift(t *testing.T) {
	a := sourceAdapter(t)
	b, c := testsupport.CandidateWorkspace(t, *a.Source, strings.Repeat("a", 64), map[string][]byte{"source.txt": []byte("modified")})
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
	var chunk candidatetools.ReadResult
	if err := canonical.Decode([]byte(read.Content), &chunk); err != nil {
		t.Fatal(err)
	}
	if chunk.ContentUTF8 == nil || *chunk.ContentUTF8 != "modified" || chunk.CandidateID != page.CandidateID || chunk.SHA256 != page.Files[0].Hash {
		t.Fatal("candidate read used base or wrong hash")
	}
	if err := os.WriteFile(filepath.Join(b.Request.Path, "extra"), []byte("drift"), 0600); err != nil {
		t.Fatal(err)
	}
	if call("candidate_read", "drift", `{"path":"source.txt","offset":0,"limit":32}`).Success {
		t.Fatal("drift admitted")
	}
}

func TestAnchoredEditValidationIsDurableAndInvocationBound(t *testing.T) {
	a := sourceAdapter(t)
	i, err := runtime.NewInvocation(runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "explicit-model", Effort: "high", Role: "writer"}, "edit candidate")
	if err != nil {
		t.Fatal(err)
	}
	workspace, candidate := testsupport.CandidateWorkspace(t, *a.Source, strings.Repeat("c", 64), map[string][]byte{"source.txt": []byte("anchor source")})
	binding := CandidateBinding{Workspace: workspace, Candidate: candidate, AnchorValidationVersion: candidatetools.AnchorValidationVersion}
	candidateID, err := candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	_, files, err := worktree.Capture(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	var beforeHash string
	for _, file := range files {
		if file.Path == "source.txt" {
			beforeHash = file.Hash
		}
	}

	events, err := journal.Read(a.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "anchored.jsonl")
	for _, event := range events {
		payload := event.Payload
		if event.Kind == "runtime.intent" {
			payload, err = canonical.Bytes(Intent{Invocation: i, Directory: a.Directory, ToolOutputVersion: 1})
		}
		if event.Kind == "runtime.turn-intent" {
			payload, err = canonical.Bytes(map[string]any{"invocation_id": i.ID})
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := appendEvent(path, event.Kind, payload); err != nil {
			t.Fatal(err)
		}
		if event.Kind == "runtime.source" {
			if err := appendEvent(path, "runtime.candidate", binding); err != nil {
				t.Fatal(err)
			}
		}
	}
	a.JournalPath, a.Candidate = path, &binding
	edits := []anchoredit.Edit{{Before: "anchor", After: "checked"}}
	args, err := canonical.Bytes(candidatetools.AnchoredEditArgs{CandidateID: candidateID, Path: "source.txt", BeforeHash: beforeHash, Edits: edits})
	if err != nil {
		t.Fatal(err)
	}
	requestBytes, err := canonical.Bytes(ToolRequest{Arguments: args, CallID: "anchor-check", ThreadID: "thread-1", TurnID: "turn-1", Tool: candidatetools.ValidateAnchoredEditsName})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := a.HandleTool(context.Background(), requestBytes)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "anchor source") {
		t.Fatal("candidate bytes were returned")
	}
	state, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingTool != nil || len(state.ToolResponses) != 1 || !state.ToolResponses[0].Success {
		t.Fatal("tool response was not replayed", state.PendingTool, state.ToolResponses)
	}
	var validation candidatetools.AnchoredEditValidation
	if err := canonical.Decode([]byte(state.ToolResponses[0].Content), &validation); err != nil {
		t.Fatal(err)
	}
	if validation.CandidateID != candidateID || validation.Path != "source.txt" || validation.BeforeHash != beforeHash || !validation.Valid || validation.ResultHash == "" {
		t.Fatal("anchored edit validation output mismatch", validation)
	}
}

func TestV0RuntimeBindingRejectsAnchoredTool(t *testing.T) {
	a := sourceAdapter(t)
	raw, err := canonical.Bytes(ToolRequest{Arguments: json.RawMessage(`{}`), CallID: "v0", ThreadID: "thread-1", TurnID: "turn-1", Tool: candidatetools.ValidateAnchoredEditsName})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.HandleTool(context.Background(), raw); err == nil {
		t.Fatal("v0 invocation admitted v1 tool")
	}
	state, err := Inspect(a.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ToolResponses) != 0 {
		t.Fatal("rejected v1 tool received response")
	}
}
