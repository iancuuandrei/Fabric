package contextbroker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/candidatetools"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/worktree"
)

func TestSourceBrokerPersistsExactRequestAndResponse(t *testing.T) {
	source := contextSourceFixture(t)
	limits := fixtureLimits()
	binding, err := NewBinding(strings.Repeat("a", 64), source, nil, limits)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "context.jsonl")
	broker, err := Open(path, binding)
	if err != nil {
		t.Fatal(err)
	}
	response, err := broker.Call(context.Background(), "source-1", "source_read", json.RawMessage(` { "limit": 32, "offset": 0, "path": "source.txt" } `))
	if err != nil || !response.Success {
		t.Fatal(response, err)
	}
	var chunk repository.SourceChunk
	if err := canonical.Decode(response.Content, &chunk); err != nil {
		t.Fatal(err)
	}
	content, err := base64.StdEncoding.DecodeString(chunk.ContentBase64)
	if err != nil || string(content) != "committed" || chunk.Commit != source.Commit {
		t.Fatal(string(content), chunk, err)
	}
	events, err := journal.Read(path)
	if err != nil || len(events) != 3 || events[0].Kind != boundEvent || events[1].Kind != requestEvent || events[2].Kind != responseEvent {
		t.Fatal("wrong durable ordering", err)
	}
	state, err := Inspect(path)
	if err != nil || state.Pending != nil || state.Calls != 1 || len(state.Requests) != 1 || len(state.Responses) != 1 || string(state.Responses[0].Content) != string(response.Content) {
		t.Fatal(state, err)
	}
	request := state.Requests[0]
	if request.CallID != "source-1" || request.RequestID != response.RequestID || request.InvocationID != binding.InvocationID || request.BindingID != response.BindingID || string(request.Arguments) != `{"limit":32,"offset":0,"path":"source.txt"}` {
		t.Fatal("inspect did not retain exact canonical request", request)
	}
	state.Requests[0].CallID = "mutated"
	state.Requests[0].Arguments[2] = 'X'
	replayed, err := Inspect(path)
	if err != nil || len(replayed.Requests) != 1 || replayed.Requests[0].CallID != "source-1" || string(replayed.Requests[0].Arguments) != `{"limit":32,"offset":0,"path":"source.txt"}` {
		t.Fatal("returned request mutation affected journal replay", replayed, err)
	}
	if err := broker.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = Inspect(path)
	if err != nil || !state.Closed {
		t.Fatal(state, err)
	}
	if _, err := broker.Call(context.Background(), "after-close", "source_list", json.RawMessage(`{"after":"","limit":1}`)); err == nil {
		t.Fatal("call admitted after close")
	}
}

func TestCandidateBrokerSnapshotsBindingAndCatalog(t *testing.T) {
	source := contextSourceFixture(t)
	request, err := worktree.Prepare(strings.Repeat("b", 64), source)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := worktree.Acquire(request)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	workspace, err := worktree.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(request.Path, "source.txt"), []byte("candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	candidate, err := worktree.Fingerprint(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	original := candidatetools.Binding{Workspace: workspace, Candidate: candidate}
	binding, err := NewBinding(strings.Repeat("c", 64), source, &original, fixtureLimits())
	if err != nil {
		t.Fatal(err)
	}
	original.Candidate.FilesHash = strings.Repeat("f", 64)
	if binding.Candidate == nil || binding.Candidate.Candidate != candidate {
		t.Fatal("candidate binding aliases caller mutation")
	}

	broker, err := Open(filepath.Join(t.TempDir(), "candidate.jsonl"), binding)
	if err != nil {
		t.Fatal(err)
	}
	catalog := broker.Catalog()
	if len(catalog) != 4 || catalog[2].Name != "candidate_list" || catalog[3].Name != "candidate_read" {
		t.Fatal("candidate catalog missing", catalog)
	}
	catalog[0].InputSchema["type"] = "changed"
	if broker.Catalog()[0].InputSchema["type"] != "object" {
		t.Fatal("catalog aliases caller mutation")
	}
	response, err := broker.Call(context.Background(), "candidate-1", "candidate_read", json.RawMessage(`{"path":"source.txt","offset":0,"limit":32}`))
	if err != nil || !response.Success {
		t.Fatal(response, err)
	}
	var chunk worktree.SourceChunk
	if err := canonical.Decode(response.Content, &chunk); err != nil || chunk.ContentUTF8 == nil || *chunk.ContentUTF8 != "candidate" {
		t.Fatal(chunk, err)
	}
}

func TestBrokerLimitsReturnsValueSnapshot(t *testing.T) {
	source := contextSourceFixture(t)
	want := fixtureLimits()
	binding, err := NewBinding(strings.Repeat("3", 64), source, nil, want)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := Open(filepath.Join(t.TempDir(), "limits-snapshot.jsonl"), binding)
	if err != nil {
		t.Fatal(err)
	}
	got := broker.Limits()
	if got != want {
		t.Fatal("broker limits differ from binding", got, want)
	}
	got.MaxResponseBytes = 2
	if broker.Limits() != want {
		t.Fatal("returned limits mutated broker state")
	}
}

func TestLegacyJSONLRequestJournalFailurePerformsNoDomainIO(t *testing.T) {
	source := contextSourceFixture(t)
	binding, _ := NewBinding(strings.Repeat("d", 64), source, nil, fixtureLimits())
	path := filepath.Join(t.TempDir(), "locked.jsonl")
	// An existing empty file explicitly selects the retained legacy JSONL
	// backend, whose conservative sidecar lock remains part of its recovery
	// contract. SQLite journals use database transactions instead.
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	broker, err := Open(path, binding)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	broker.execute = func(context.Context, Binding, string, json.RawMessage) (any, bool, error) {
		calls++
		return map[string]any{}, true, nil
	}
	if err := os.WriteFile(path+".lock", []byte("held"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Call(context.Background(), "blocked", "source_list", json.RawMessage(`{"after":"","limit":1}`)); err == nil {
		t.Fatal("call crossed failed request journal")
	}
	if calls != 0 {
		t.Fatal("domain IO ran before durable request")
	}
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatal(err)
	}
	state, err := Inspect(path)
	if err != nil || state.Calls != 0 || state.Pending != nil {
		t.Fatal(state, err)
	}
}

func TestPendingRecoveryNeverResendsAndCloseShutsDown(t *testing.T) {
	source := contextSourceFixture(t)
	limits := fixtureLimits()
	limits.MaxResponseBytes = 64
	binding, _ := NewBinding(strings.Repeat("e", 64), source, nil, limits)
	path := filepath.Join(t.TempDir(), "pending.jsonl")
	broker, err := Open(path, binding)
	if err != nil {
		t.Fatal(err)
	}
	executed := 0
	broker.execute = func(context.Context, Binding, string, json.RawMessage) (any, bool, error) {
		executed++
		return map[string]string{"content": strings.Repeat("x", 128)}, true, nil
	}
	if _, err := broker.Call(context.Background(), "unknown", "source_list", json.RawMessage(`{"after":"","limit":1}`)); err == nil {
		t.Fatal("over-budget response returned")
	}
	state, err := Inspect(path)
	if err != nil || state.Pending == nil || state.Calls != 1 || len(state.Requests) != 1 || len(state.Responses) != 0 || executed != 1 {
		t.Fatal(state, executed, err)
	}

	recovered, err := Open(path, binding)
	if err != nil {
		t.Fatal(err)
	}
	replayed := 0
	recovered.execute = func(context.Context, Binding, string, json.RawMessage) (any, bool, error) {
		replayed++
		return map[string]any{}, true, nil
	}
	if _, err := recovered.Call(context.Background(), "next", "source_list", json.RawMessage(`{"after":"","limit":1}`)); err == nil || replayed != 0 {
		t.Fatal("pending request was resent or bypassed", replayed)
	}
	if err := recovered.Close(); err == nil {
		t.Fatal("pending broker durably closed")
	}
	if _, err := recovered.Call(context.Background(), "after-local-close", "source_list", json.RawMessage(`{"after":"","limit":1}`)); err == nil || replayed != 0 {
		t.Fatal("local shutdown did not stop calls", replayed)
	}
}

func TestReplayRejectsSubstitutionDuplicatesAndLimits(t *testing.T) {
	source := contextSourceFixture(t)
	limits := fixtureLimits()
	limits.MaxCalls = 1
	binding, _ := NewBinding(strings.Repeat("f", 64), source, nil, limits)
	path := filepath.Join(t.TempDir(), "limits.jsonl")
	broker, err := Open(path, binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Call(context.Background(), "one", "source_list", json.RawMessage(`{"after":"","limit":1}`)); err != nil {
		t.Fatal(err)
	}
	executed := 0
	broker.execute = func(context.Context, Binding, string, json.RawMessage) (any, bool, error) {
		executed++
		return map[string]any{}, true, nil
	}
	for _, callID := range []string{"two", "one"} {
		if _, err := broker.Call(context.Background(), callID, "source_list", json.RawMessage(`{"after":"","limit":1}`)); err == nil {
			t.Fatal("call limit or duplicate identity admitted", callID)
		}
	}
	if executed != 0 {
		t.Fatal("rejected request executed")
	}
	state, err := Inspect(path)
	if err != nil || state.Calls != 1 || len(state.Responses) != 1 {
		t.Fatal(state, err)
	}
}

func TestReplayRejectsResponseAndBindingSubstitution(t *testing.T) {
	source := contextSourceFixture(t)
	binding, _ := NewBinding(strings.Repeat("1", 64), source, nil, fixtureLimits())
	path := filepath.Join(t.TempDir(), "identity.jsonl")
	broker, err := Open(path, binding)
	if err != nil {
		t.Fatal(err)
	}
	bindingID, _ := binding.ID()
	request := Request{Version: 1, BindingID: bindingID, InvocationID: binding.InvocationID, CallID: "exact-call", Tool: "source_list", Arguments: json.RawMessage(`{"after":"","limit":1}`)}
	request.RequestID, _ = request.ID()
	if err := broker.append(requestEvent, request); err != nil {
		t.Fatal(err)
	}
	bad := Response{Version: 1, BindingID: bindingID, InvocationID: binding.InvocationID, RequestID: request.RequestID, CallID: "substituted", Success: true, Content: json.RawMessage(`{"files":[]}`)}
	if err := broker.append(responseEvent, bad); err == nil {
		t.Fatal("substituted response admitted")
	}
	state, err := Inspect(path)
	if err != nil || state.Pending == nil || state.Pending.CallID != request.CallID || len(state.Responses) != 0 {
		t.Fatal(state, err)
	}

	changed := binding
	changed.InvocationID = strings.Repeat("2", 64)
	if _, err := Open(path, changed); err == nil {
		t.Fatal("foreign invocation binding opened existing journal")
	}
}

func contextSourceFixture(t *testing.T) repository.Identity {
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
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("committed"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	source, err := repository.Discover(context.Background(), root, "context-broker-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	return source
}

func fixtureLimits() Limits {
	return Limits{MaxCalls: 8, MaxRequestBytes: 1024, MaxResponseBytes: 64 << 10, MaxTotalResponseBytes: 256 << 10}
}
