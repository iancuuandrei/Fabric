package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/runtime"
)

func TestExplorerHostFailureIsJournaled(t *testing.T) {
	c := creation(t)
	c.Config.Explorer = &runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "explicit-explorer", Effort: "high", Role: "explorer"}
	c.Config.Codex = &config.Codex{Executable: filepath.Join(t.TempDir(), "missing.exe"), ExecutableHash: strings.Repeat("a", 64), StateRoot: t.TempDir(), AuthSource: filepath.Join(t.TempDir(), "auth.json")}
	path, _ := approvedRepositoryCreation(t, c)
	s, err := StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := expectedExplorerHost(s, "Read fixture source")
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	output, err := canonical.Bytes(Exploration{id, "Unbacked observation", []string{"file.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	model := expected.Invocation.Profile.Model
	unbacked := runtime.Result{Version: 1, InvocationID: expected.Invocation.ID, Requested: expected.Invocation.Profile, ObservedModel: &model, Output: string(output)}
	if _, err := RecordExploration(path, ExplorerRecord{"Read fixture source", expected.Invocation, unbacked}); err == nil {
		t.Fatal("unbacked Codex explorer result recorded")
	}
	if err := Append(path, "explorer.runtime-observed", ExplorerRuntimeReceipt{expected.Invocation.ID, "thread", "turn", strings.Repeat("a", 64), strings.Repeat("b", 64)}); err == nil {
		t.Fatal("runtime receipt without observed host admitted")
	}
	foreign := expected
	foreign.Question = "A different question"
	if err := Append(path, "explorer.host-intent", foreign); err == nil {
		t.Fatal("substituted exploration question admitted")
	}
	foreign = expected
	foreign.Launch.Root = t.TempDir()
	if err := Append(path, "explorer.host-intent", foreign); err == nil {
		t.Fatal("foreign host admitted")
	}
	if _, err := RunExplorer(context.Background(), path, "Read fixture source"); err == nil {
		t.Fatal("missing host executable succeeded")
	}
	s, err = Inspect(path)
	if err != nil || s.ExplorerHost == nil || s.ExplorerHost.Intent != expected || s.ExplorerHost.Ready || s.ExplorerHost.Receipt != nil || len(s.Explorations) != 0 {
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
	if _, err := RunExplorer(context.Background(), path, "Read fixture source"); err == nil {
		t.Fatal("failed preparation silently succeeded on retry")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("failed repeat duplicated intent", err)
	}
	content, err := os.ReadFile(filepath.Join(s.Workspace.Request.Path, "file.txt"))
	if err != nil || string(content) != "base\n" {
		t.Fatal("host preparation mutated explorer source", err)
	}
}
