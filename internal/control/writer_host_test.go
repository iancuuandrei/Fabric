package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/fileeffects"
	"harness.local/engorch/internal/runtime"
)

func TestWriterHostFailureIsJournaled(t *testing.T) {
	c := creation(t)
	c.Config.Writer = &runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "explicit-writer", Effort: "high", Role: "writer"}
	c.Config.Codex = &config.Codex{Executable: filepath.Join(t.TempDir(), "missing.exe"), ExecutableHash: strings.Repeat("a", 64), StateRoot: t.TempDir(), AuthSource: filepath.Join(t.TempDir(), "auth.json")}
	path, _ := approvedRepositoryCreation(t, c)
	s, err := StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := expectedWriterHost(s)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	output, err := canonical.Bytes(WriterProposal{id, []fileeffects.Change{{Path: "file.txt", BeforeHash: digestText("base\n"), ContentBase64: content64("unbacked\n")}}})
	if err != nil {
		t.Fatal(err)
	}
	model := expected.Invocation.Profile.Model
	unbacked := runtime.Result{Version: 1, InvocationID: expected.Invocation.ID, Requested: expected.Invocation.Profile, ObservedModel: &model, Output: string(output)}
	if _, err := RecordWriterProposal(context.Background(), path, expected.Invocation, unbacked); err == nil {
		t.Fatal("unbacked Codex writer result recorded")
	}
	if err := Append(path, "writer.runtime-observed", WriterRuntimeReceipt{expected.Invocation.ID, "thread", "turn", strings.Repeat("a", 64), strings.Repeat("b", 64)}); err == nil {
		t.Fatal("runtime receipt without observed host admitted")
	}
	foreign := expected
	foreign.Launch.Root = t.TempDir()
	if err := Append(path, "writer.host-intent", foreign); err == nil {
		t.Fatal("foreign host admitted")
	}
	if _, err := RunWriter(context.Background(), path); err == nil {
		t.Fatal("missing host executable succeeded")
	}
	s, err = Inspect(path)
	if err != nil || s.WriterHost == nil || s.WriterHost.Intent != expected || s.WriterHost.Ready || s.WriterHost.Receipt != nil || s.WriterProposal != nil {
		t.Fatal("failed preparation evidence mismatch", err)
	}
	usage, err := MeasureRunUsage(path)
	if err != nil || len(usage.Invocations) != 1 || usage.Invocations[0].JournalPresent || usage.Invocations[0].ReceiptMatched || usage.Invocations[0].Usage != nil {
		t.Fatal("missing runtime presented as measured/admitted", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunWriter(context.Background(), path); err == nil {
		t.Fatal("failed preparation silently succeeded on retry")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("failed repeat duplicated intent", err)
	}
	content, err := os.ReadFile(filepath.Join(s.Workspace.Request.Path, "file.txt"))
	if err != nil || string(content) != "base\n" {
		t.Fatal("host preparation mutated writer source", err)
	}
}
