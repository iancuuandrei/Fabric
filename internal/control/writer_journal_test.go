package control

import (
	"context"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/runtime"
)

type utf8WriterChange struct {
	Path       string  `json:"path"`
	BeforeHash *string `json:"before_hash"`
	Content    string  `json:"content_utf8"`
	Executable bool    `json:"executable"`
}

type utf8WriterReply struct {
	CandidateID string             `json:"candidate_id"`
	Changes     []utf8WriterChange `json:"changes"`
}

func TestWriterReplayAcceptsUnsortedReplyAndRejectsSubstitution(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*utf8WriterReply)
	}{
		{name: "contents", mutate: func(reply *utf8WriterReply) { reply.Changes[0].Content = "foreign contents\n" }},
		{name: "path", mutate: func(reply *utf8WriterReply) { reply.Changes[0].Path = "scripts/foreign.py" }},
		{name: "metadata", mutate: func(reply *utf8WriterReply) { reply.Changes[0].Executable = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, invocation, reply, prepared, validResult := unsortedUTF8WriterFixture(t)
			badReply := reply
			badReply.Changes = append([]utf8WriterChange(nil), reply.Changes...)
			tc.mutate(&badReply)
			badOutput, err := canonical.Bytes(badReply)
			if err != nil {
				t.Fatal(err)
			}
			badRecord := WriterRecord{Invocation: invocation, Result: validResult, Prepared: prepared}
			badRecord.Result.Output = string(badOutput)
			if err := Append(path, "writer.proposed", badRecord); err == nil {
				t.Fatal("writer substitution admitted", tc.name)
			}
			goodRecord := WriterRecord{Invocation: invocation, Result: validResult, Prepared: prepared}
			if err := Append(path, "writer.proposed", goodRecord); err != nil {
				t.Fatal("valid unsorted writer reply rejected after substitution", err)
			}
			s, err := Inspect(path)
			if err != nil || s.WriterProposal == nil || s.WriterProposal.Result.Output != validResult.Output {
				t.Fatal("valid writer reply was not durably preserved", err)
			}
		})
	}
}

func TestWriterReplayRejectsDuplicateChanges(t *testing.T) {
	path, invocation, reply, _, result := unsortedUTF8WriterFixture(t)
	reply.Changes = append(reply.Changes, reply.Changes[0])
	output, err := canonical.Bytes(reply)
	if err != nil {
		t.Fatal(err)
	}
	result.Output = string(output)
	if _, err := RecordWriterProposal(context.Background(), path, invocation, result); err == nil {
		t.Fatal("duplicate writer changes admitted")
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.WriterProposal != nil {
		t.Fatal("duplicate writer changes advanced workflow")
	}
}

func TestWriterReplayPreservesUnsortedObservedOutput(t *testing.T) {
	path, invocation, _, _, result := unsortedUTF8WriterFixture(t)
	record, err := RecordWriterProposal(context.Background(), path, invocation, result)
	if err != nil {
		t.Fatal("unsorted writer reply rejected", err)
	}
	if record.Result.Output != result.Output {
		t.Fatal("observed writer output was rewritten")
	}
	paths := make([]string, 0, len(record.Prepared.Proposal.Changes))
	for _, change := range record.Prepared.Proposal.Changes {
		paths = append(paths, change.Path)
	}
	want := []string{"docs/evaluation/agent-benchmark-summary.md", "scripts/summarize_agent_benchmark.py", "scripts/test_summarize_agent_benchmark.py"}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("prepared changes are not sorted: got %v want %v", paths, want)
		}
	}
}

func unsortedUTF8WriterFixture(t *testing.T) (string, runtime.Invocation, utf8WriterReply, PreparedFiles, runtime.Result) {
	t.Helper()
	path, invocation := utf8WriterInvocationFixture(t)
	s, err := Inspect(path)
	if err != nil || s.Candidate == nil {
		t.Fatal("candidate unavailable", err)
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	reply := utf8WriterReply{CandidateID: candidateID, Changes: []utf8WriterChange{
		{Path: "scripts/summarize_agent_benchmark.py", Content: "summarize\n", Executable: false},
		{Path: "scripts/test_summarize_agent_benchmark.py", Content: "test\n", Executable: false},
		{Path: "docs/evaluation/agent-benchmark-summary.md", Content: "docs\n", Executable: false},
	}}
	output, err := canonical.Bytes(reply)
	if err != nil {
		t.Fatal(err)
	}
	result := runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, Output: string(output)}
	prepared, err := PrepareWriterFiles(context.Background(), path, invocation, result)
	if err != nil {
		t.Fatal(err)
	}
	return path, invocation, reply, prepared, result
}
