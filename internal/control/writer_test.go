package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/fileeffects"
	"harness.local/engorch/internal/runtime"
)

func TestWriterReplyPreparationAndStaleCandidate(t *testing.T) {
	c := creation(t)
	c.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-writer", Effort: "high", Role: "writer"}
	path, _ := approvedRepositoryCreation(t, c)
	s, err := StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	i, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	if i.Profile != *c.Config.Writer {
		t.Fatal("writer route substituted")
	}
	id, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	output, err := canonical.Bytes(WriterProposal{id, []fileeffects.Change{{Path: "file.txt", BeforeHash: digestText("base\n"), ContentBase64: content64("writer output\n")}}})
	if err != nil {
		t.Fatal(err)
	}
	r := runtime.Result{Version: 1, InvocationID: i.ID, Requested: i.Profile, Output: string(output)}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := PrepareWriterFiles(context.Background(), path, i, r)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("preparation changed journal", err)
	}
	bytes, err := os.ReadFile(filepath.Join(s.Workspace.Request.Path, "file.txt"))
	if err != nil || string(bytes) != "base\n" {
		t.Fatal("preparation mutated source", err)
	}
	bad := r
	bad.Output = strings.Replace(r.Output, id, strings.Repeat("0", 64), 1)
	if _, err := PrepareWriterFiles(context.Background(), path, i, bad); err == nil {
		t.Fatal("foreign candidate admitted")
	}
	bad = r
	bad.Requested.Model = "other"
	if _, err := PrepareWriterFiles(context.Background(), path, i, bad); err == nil {
		t.Fatal("model substitution admitted")
	}
	bad = r
	bad.Output = "```json\n" + r.Output + "\n```"
	if _, err := PrepareWriterFiles(context.Background(), path, i, bad); err == nil {
		t.Fatal("non-contract prose admitted")
	}
	record, err := RecordWriterProposal(context.Background(), path, i, r)
	if err != nil {
		t.Fatal(err)
	}
	p = record.Prepared
	stored, err := Inspect(path)
	if err != nil || stored.WriterProposal == nil || stored.WriterProposal.Result.Output != r.Output {
		t.Fatal("writer reply not durably preserved", err)
	}
	journalBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RecordWriterProposal(context.Background(), path, i, r); err == nil {
		t.Fatal("duplicate proposal recorded")
	}
	changedRecord := record
	// Change the reply to a different valid file transition; the prepared effect
	// must not survive this substitution even though the candidate still matches.
	changedReply, err := canonical.Bytes(WriterProposal{id, []fileeffects.Change{{Path: "file.txt", BeforeHash: digestText("base\n"), ContentBase64: content64("foreign output\n")}}})
	if err != nil {
		t.Fatal(err)
	}
	changedRecord.Result.Output = string(changedReply)
	if err := Append(path, "writer.proposed", changedRecord); err == nil {
		t.Fatal("reply/effect substitution admitted")
	}
	journalAfter, err := os.ReadFile(path)
	if err != nil || string(journalAfter) != string(journalBefore) {
		t.Fatal("rejected proposal changed journal", err)
	}
	if _, err := ApplyFiles(context.Background(), path, p, authorize(t, p)); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareWriterFiles(context.Background(), path, i, r); err == nil {
		t.Fatal("stale writer reply admitted after candidate change")
	}
}
