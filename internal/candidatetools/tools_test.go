package candidatetools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/worktree"
)

func TestCatalogPreservesCandidateSchemas(t *testing.T) {
	got, err := canonical.Bytes(Catalog())
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"Description":"List the exact admitted candidate's current regular files with hashes and modes. Use after empty initially, follow next_after until null. Unlike source_list this includes admitted modifications. Drift fails the request.","InputSchema":{"additionalProperties":false,"properties":{"after":{"type":"string"},"limit":{"maximum":128,"minimum":1,"type":"integer"}},"required":["after","limit"],"type":"object"},"Name":"candidate_list"},{"Description":"Read a byte page from the admitted candidate, including modifications. Returns exact full-file hash and binary/UTF-8 views. Follow next_offset until null. Any candidate drift fails the request.","InputSchema":{"additionalProperties":false,"properties":{"limit":{"maximum":32768,"minimum":1,"type":"integer"},"offset":{"minimum":0,"type":"integer"},"path":{"type":"string"}},"required":["path","offset","limit"],"type":"object"},"Name":"candidate_read"}]`
	if string(got) != want {
		t.Fatalf("catalog changed\n got: %s\nwant: %s", got, want)
	}
}

func TestExecuteReadsChangedBytesOmitsDeletionAndRejectsDrift(t *testing.T) {
	binding, lease := candidateFixture(t)
	defer lease.Close()
	root := binding.Workspace.Request.Path
	if err := os.WriteFile(filepath.Join(root, "changed.txt"), []byte("candidate bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	candidate, err := worktree.Fingerprint(context.Background(), binding.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	binding.Candidate = candidate

	content, handled, err := Execute(context.Background(), binding, ListName, json.RawMessage(`{"after":"","limit":1}`))
	if err != nil || !handled {
		t.Fatal(handled, err)
	}
	first := content.(Page)
	if len(first.Files) != 1 || first.Files[0].Path != "changed.txt" || first.NextAfter == nil || *first.NextAfter != "changed.txt" {
		t.Fatal("candidate first page mismatch", first)
	}
	content, handled, err = Execute(context.Background(), binding, ListName, mustArguments(t, map[string]any{"after": *first.NextAfter, "limit": 128}))
	if err != nil || !handled {
		t.Fatal(handled, err)
	}
	second := content.(Page)
	if len(second.Files) != 1 || second.Files[0].Path != "unchanged.txt" || second.NextAfter != nil || second.CandidateID != first.CandidateID {
		t.Fatal("deleted path remained in candidate listing", second)
	}

	content, handled, err = Execute(context.Background(), binding, ReadName, json.RawMessage(`{"path":"changed.txt","offset":0,"limit":32768}`))
	if err != nil || !handled {
		t.Fatal(handled, err)
	}
	chunk := content.(worktree.SourceChunk)
	bytes, err := base64.StdEncoding.DecodeString(chunk.ContentBase64)
	if err != nil || string(bytes) != "candidate bytes" || chunk.CandidateID != first.CandidateID || chunk.SHA256 != first.Files[0].Hash {
		t.Fatal("candidate read did not bind changed bytes", chunk, err)
	}
	if _, handled, err = Execute(context.Background(), binding, ReadName, json.RawMessage(`{"path":"deleted.txt","offset":0,"limit":1}`)); err == nil || !handled {
		t.Fatal("deleted candidate path was readable", handled)
	}

	if err := os.WriteFile(filepath.Join(root, "drift.txt"), []byte("later"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct {
		name string
		args json.RawMessage
	}{
		{ListName, json.RawMessage(`{"after":"","limit":128}`)},
		{ReadName, json.RawMessage(`{"path":"changed.txt","offset":0,"limit":1}`)},
	} {
		if _, handled, err := Execute(context.Background(), binding, request.name, request.args); err == nil || !handled {
			t.Fatal("candidate drift admitted", request.name, handled)
		}
	}
}

func TestExecuteRejectsArgumentsAndLeavesUnknownUnhandled(t *testing.T) {
	for _, request := range []struct {
		name string
		args json.RawMessage
	}{
		{ListName, json.RawMessage(`{"after":"","limit":0}`)},
		{ListName, json.RawMessage(`{"after":"","limit":1,"extra":true}`)},
		{ReadName, json.RawMessage(`{"path":"changed.txt","offset":-1,"limit":1}`)},
		{ReadName, json.RawMessage(`{"path":"changed.txt","offset":0,"limit":32769}`)},
	} {
		if _, handled, err := Execute(context.Background(), Binding{}, request.name, request.args); err == nil || !handled {
			t.Fatal("invalid candidate arguments admitted", request.name, handled)
		}
	}
	if content, handled, err := Execute(context.Background(), Binding{}, "other", json.RawMessage(`{}`)); err != nil || handled || content != nil {
		t.Fatal("unknown candidate tool handled", content, handled, err)
	}
}

func candidateFixture(t *testing.T) (Binding, *worktree.Lease) {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	git("init", "-q")
	for name, body := range map[string]string{"changed.txt": "base bytes", "deleted.txt": "remove me", "unchanged.txt": "retained"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "changed.txt", "deleted.txt", "unchanged.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	source, err := repository.Discover(context.Background(), root, "candidate-tools-fixture")
	if err != nil {
		t.Fatal(err)
	}
	request, err := worktree.Prepare(strings.Repeat("a", 64), source)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := worktree.Acquire(request)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := worktree.Create(context.Background(), request)
	if err != nil {
		lease.Close()
		t.Fatal(err)
	}
	candidate, err := worktree.Fingerprint(context.Background(), workspace)
	if err != nil {
		lease.Close()
		t.Fatal(err)
	}
	return Binding{Workspace: workspace, Candidate: candidate}, lease
}

func mustArguments(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := canonical.Bytes(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
